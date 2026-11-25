package paperx

import (
	"fmt"
	"sort"

	"github.com/shopspring/decimal"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// Engine is one paper account plus the market state that drives it. It is
// not safe for concurrent use: feed it events from one goroutine (the
// Runner does) and persist State between processes (Store does).
type Engine struct {
	st     *State
	cfg    Config
	books  map[string]*Book
	prints map[printKey]decimal.Decimal // prints per level since the last book
	// liquidating marks fills booked by a liquidation walk
	liquidating bool
}

type printKey struct {
	coin string
	ask  bool // the resting side the print hit
	px   string
}

var (
	bps10k = decimal.NewFromInt(10_000)
	one    = decimal.NewFromInt(1)
)

// New wraps a state (a fresh one when st is nil).
func New(st *State, cfg Config) *Engine {
	if st == nil {
		st = NewState("default", DefaultBalance, 0)
	}
	st.init()
	return &Engine{st: st, cfg: cfg.withDefaults(), books: map[string]*Book{}, prints: map[printKey]decimal.Decimal{}}
}

// State is the account (live pointer; persist it with Store.Save).
func (e *Engine) State() *State { return e.st }

// Config is the effective configuration.
func (e *Engine) Config() Config { return e.cfg }

// SetSpec installs or refreshes a market's rules.
func (e *Engine) SetSpec(s Spec) {
	if s.MaxDecimals == 0 {
		s.MaxDecimals = 6
	}
	e.st.Specs[s.Coin] = s
}

// Book returns the last book seen for coin.
func (e *Engine) Book(coin string) (Book, bool) {
	b, ok := e.books[coin]
	if !ok {
		return Book{}, false
	}
	return *b, true
}

// Mark returns the last mark for coin (zero when unknown).
func (e *Engine) Mark(coin string) decimal.Decimal { return e.st.Marks[coin] }

// Order finds an order by oid among live and recent orders.
func (e *Engine) Order(oid uint64) (*Order, bool) {
	for _, o := range e.st.Orders {
		if o.OID == oid {
			return o, true
		}
	}
	for i := len(e.st.Closed) - 1; i >= 0; i-- {
		if e.st.Closed[i].OID == oid {
			return e.st.Closed[i], true
		}
	}
	return nil, false
}

// FillsFor returns the fills of one order, oldest first.
func (e *Engine) FillsFor(oid uint64) []Fill {
	var out []Fill
	for _, f := range e.st.Fills {
		if f.OID == oid {
			out = append(out, f)
		}
	}
	return out
}

// Coins lists the coins with live orders or positions (what a feed must
// follow), sorted.
func (e *Engine) Coins() []string {
	set := map[string]bool{}
	for c := range e.st.Positions {
		set[c] = true
	}
	for _, o := range e.st.Orders {
		set[o.Coin] = true
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func (e *Engine) touch(at int64) {
	if at > e.st.Updated {
		e.st.Updated = at
	}
}

func (e *Engine) levFor(coin string) LevSetting {
	if p, ok := e.st.Positions[coin]; ok {
		return LevSetting{Leverage: p.Leverage, Isolated: p.Isolated}
	}
	if l, ok := e.st.Leverage[coin]; ok && l.Leverage > 0 {
		return l
	}
	s := e.st.Specs[coin]
	lev := 20
	if s.MaxLeverage > 0 && s.MaxLeverage < lev {
		lev = s.MaxLeverage
	}
	return LevSetting{Leverage: lev, Isolated: s.OnlyIsolated}
}

// SetLeverage is updateLeverage: refused while a position in the other
// margin mode is open.
func (e *Engine) SetLeverage(coin string, lev int, isolated bool) error {
	s, ok := e.st.Specs[coin]
	if !ok {
		return &Reject{Code: "UNKNOWN_MARKET", Msg: "Unknown asset " + coin + "."}
	}
	if lev < 1 || (s.MaxLeverage > 0 && lev > s.MaxLeverage) {
		return reject("LEVERAGE", s, msgLeverage)
	}
	if s.OnlyIsolated && !isolated {
		return reject("ISOLATED", s, msgIsolated)
	}
	if p, ok := e.st.Positions[coin]; ok {
		if p.Isolated != isolated {
			return reject("LEVERAGE", s, "Cannot switch margin mode with an open position.")
		}
		p.Leverage = lev
	}
	e.st.Leverage[coin] = LevSetting{Leverage: lev, Isolated: isolated}
	return nil
}

// Submit validates and queues an order. Rules that need no book are
// checked now (a rejected order is returned with Status rejected and a
// *Reject error); the rest run when the first book at or after
// now + Latency arrives (OnBook).
func (e *Engine) Submit(r OrderReq, now int64) (*Order, error) {
	e.touch(now)
	s, ok := e.st.Specs[r.Coin]
	if !ok {
		return nil, &Reject{Code: "UNKNOWN_MARKET", Msg: "Unknown asset " + r.Coin + "."}
	}
	if r.TIF == "" {
		r.TIF = Gtc
	}
	if r.Market {
		r.TIF = Ioc
	}
	cur := e.levFor(r.Coin)
	o := &Order{OID: e.st.NextOID, CLOID: r.CLOID, Coin: r.Coin, Side: r.Side, Px: r.Px, Sz: r.Sz, OrigSz: r.Sz,
		TIF: r.TIF, Market: r.Market, ReduceOnly: r.ReduceOnly, Leverage: cur.Leverage, Isolated: cur.Isolated,
		Placed: now, ActiveAt: now + e.cfg.Latency.Milliseconds(), Updated: now, Status: StatusPending, Grouping: GroupNA}
	e.st.NextOID++
	e.st.Stats.Orders++
	if rej := validStatic(s, r, cur.Leverage); rej != nil {
		o.Status, o.Error = StatusRejected, rej.Msg
		e.st.Stats.Rejections++
		e.st.Closed = capOrders(append(e.st.Closed, o))
		return o, rej
	}
	if r.Leverage > 0 {
		if p, ok := e.st.Positions[r.Coin]; ok && p.Isolated != r.Isolated {
			o.Status, o.Error = StatusRejected, reject("LEVERAGE", s, "Cannot switch margin mode with an open position.").Msg
			e.st.Stats.Rejections++
			e.st.Closed = capOrders(append(e.st.Closed, o))
			return o, &Reject{Code: "LEVERAGE", Msg: o.Error}
		}
		o.Leverage, o.Isolated = r.Leverage, r.Isolated
	}
	e.st.Orders = append(e.st.Orders, o)
	if e.st.Synced[r.Coin] == 0 {
		e.st.Synced[r.Coin] = now // nothing before the first order needs replaying
	}
	// TP/SL legs: reduce-only trigger-market orders on the other side.
	group := fmt.Sprintf("g%d", o.OID)
	for _, leg := range []struct {
		px   decimal.Decimal
		kind string
	}{{r.TP, "tp"}, {r.SL, "sl"}} {
		if leg.px.IsZero() {
			continue
		}
		c := &Order{OID: e.st.NextOID, Coin: r.Coin, Side: opposite(r.Side), Px: leg.px, Sz: decimal.Zero, OrigSz: decimal.Zero,
			TIF: Ioc, Market: true, ReduceOnly: true, Placed: now, Updated: now, Status: StatusWaiting,
			Trigger: &Trigger{Px: leg.px, Kind: leg.kind}, Parent: o.OID, Group: group, Grouping: GroupNormalTpsl}
		if r.PositionTpsl {
			c.Status, c.Grouping, c.Group = StatusTrigger, GroupPositionTps, "pos:"+r.Coin
		}
		o.Grouping = c.Grouping
		e.st.NextOID++
		e.st.Orders = append(e.st.Orders, c)
	}
	return o, nil
}

// Cancel cancels one live order (oid > 0) or every live order on coin
// (oid 0; coin "" = all coins). It returns the cancelled oids.
func (e *Engine) Cancel(coin string, oid uint64, at int64) ([]uint64, error) {
	e.touch(at)
	var out []uint64
	for _, o := range e.st.Orders {
		if !o.Live() || (oid != 0 && o.OID != oid) || (oid == 0 && coin != "" && o.Coin != coin) {
			continue
		}
		e.finish(o, StatusCanceled, "", at)
		out = append(out, o.OID)
		// untriggered children of a parent that never filled go with it
		if o.Filled.IsZero() {
			for _, c := range e.st.Orders {
				if c.Parent == o.OID && c.Status == StatusWaiting {
					e.finish(c, StatusCanceled, "parent canceled", at)
					out = append(out, c.OID)
				}
			}
		}
	}
	e.sweep()
	if oid != 0 && len(out) == 0 {
		return nil, &Reject{Code: "NOT_FOUND", Msg: "Order was never placed, already canceled, or filled."}
	}
	return out, nil
}

func (e *Engine) finish(o *Order, status, msg string, at int64) {
	o.Status, o.Updated = status, at
	if msg != "" {
		o.Error = msg
	}
}

// sweep moves finished orders to Closed.
func (e *Engine) sweep() {
	live := e.st.Orders[:0]
	for _, o := range e.st.Orders {
		if o.Live() {
			live = append(live, o)
		} else {
			e.st.Closed = append(e.st.Closed, o)
		}
	}
	for i := len(live); i < len(e.st.Orders); i++ {
		e.st.Orders[i] = nil
	}
	e.st.Orders = live
	e.st.Closed = capOrders(e.st.Closed)
}

func capOrders(os []*Order) []*Order {
	if len(os) > MaxClosed {
		return append([]*Order(nil), os[len(os)-MaxClosed:]...)
	}
	return os
}

func opposite(s domain.Side) domain.Side {
	if s == domain.Buy {
		return domain.Sell
	}
	return domain.Buy
}

func sign(s domain.Side) decimal.Decimal {
	if s == domain.Buy {
		return one
	}
	return one.Neg()
}

// ---- events ----

// OnBook installs a new L2 snapshot received at `at` and runs everything a
// book decides: queue shrinkage from cancels, resting orders the book has
// crossed, and pending orders whose latency has elapsed.
func (e *Engine) OnBook(db domain.Book, at int64) {
	b := BookFrom(db, at)
	e.OnBookExact(b)
}

// OnBookExact is OnBook for a book already in decimals.
func (e *Engine) OnBookExact(b Book) {
	at := b.At
	e.touch(at)
	bp := &b
	e.books[b.Coin] = bp
	e.MarkSynced(b.Coin, at)
	// 1. queue: level shrinkage that prints do not explain is cancels,
	//    which shrink the queue ahead pro rata.
	for _, o := range e.st.Orders {
		if o.Coin != b.Coin || o.Status != StatusOpen {
			continue
		}
		own := o.Side == domain.Sell // resting sells sit on the asks
		if !bp.covers(own, o.Px) && len(bp.side(own)) > 0 {
			continue // beyond the visible depth: no information
		}
		real := bp.size(own, o.Px)
		printed := e.prints[printKey{b.Coin, own, o.Px.String()}]
		expected := o.LevelSeen.Sub(printed)
		if expected.Sign() > 0 && real.LessThan(expected) && o.Queue.Sign() > 0 {
			o.Queue = o.Queue.Mul(real).Div(expected)
		}
		if o.Queue.GreaterThan(real) {
			o.Queue = real
		}
		o.LevelSeen = real
	}
	for k := range e.prints {
		if k.coin == b.Coin {
			delete(e.prints, k)
		}
	}
	// 2. resting orders the book has moved through
	for _, o := range e.restingByPriority(b.Coin) {
		ask := o.Side == domain.Buy // a resting buy is crossed by asks at or below it
		ls := bp.side(ask)
		if len(ls) == 0 {
			continue
		}
		crossed := o.Side == domain.Buy && !ls[0].Px.GreaterThan(o.Px) || o.Side == domain.Sell && !ls[0].Px.LessThan(o.Px)
		if !crossed {
			continue
		}
		for _, l := range ls {
			if o.Side == domain.Buy && l.Px.GreaterThan(o.Px) || o.Side == domain.Sell && l.Px.LessThan(o.Px) || o.Sz.Sign() <= 0 {
				break
			}
			take := e.lotFloor(o.Coin, decMin(o.Sz, e.effective(o.Coin, ask, l.Px, l.Sz, at)))
			take = e.reduceCap(o, take)
			if take.Sign() <= 0 {
				continue
			}
			f := e.applyFill(o, o.Px, take, true, at, "book")
			f.QueueWaitMs = at - o.Rested
			e.consume(o.Coin, ask, l.Px, take, l.Sz, at)
		}
		e.afterFill(o, at)
	}
	// 3. pending orders whose latency has elapsed, in placement order
	for _, o := range append([]*Order(nil), e.st.Orders...) {
		if o.Coin == b.Coin && o.Status == StatusPending && o.ActiveAt <= at {
			e.activate(o, bp, at)
		}
	}
	e.pruneOverlay(bp)
	e.sweep()
}

// OnTrades applies public prints received at `at`. A print's side is the
// aggressor's: a buy lifts asks and fills resting sells, a sell hits bids.
func (e *Engine) OnTrades(ts []domain.Trade, at int64) {
	e.touch(at)
	for _, t := range ts {
		px, sz := decimal.NewFromFloat(t.Px), decimal.NewFromFloat(t.Sz)
		if px.Sign() <= 0 || sz.Sign() <= 0 {
			continue
		}
		hitsAsks := t.Side == domain.Buy
		k := printKey{t.Coin, hitsAsks, px.String()}
		e.prints[k] = e.prints[k].Add(sz)
		avail := sz
		for _, o := range e.restingByPriority(t.Coin) {
			if avail.Sign() <= 0 {
				break
			}
			if (o.Side == domain.Sell) != hitsAsks || o.Rested > at {
				continue
			}
			var fill decimal.Decimal
			switch {
			case o.Side == domain.Sell && o.Px.LessThan(px), o.Side == domain.Buy && o.Px.GreaterThan(px):
				// printed through our price: the level, and the queue in
				// it, was cleared first
				o.Queue = decimal.Zero
				fill = decMin(o.Sz, avail)
			case o.Px.Equal(px):
				q := decMin(o.Queue, avail)
				o.Queue = o.Queue.Sub(q)
				avail = avail.Sub(q)
				fill = decMin(o.Sz, avail)
			default:
				continue
			}
			fill = e.reduceCap(o, e.lotFloor(o.Coin, fill))
			if fill.Sign() <= 0 {
				continue
			}
			avail = avail.Sub(fill)
			f := e.applyFill(o, o.Px, fill, true, at, "book")
			f.QueueWaitMs = at - o.Rested
			e.afterFill(o, at)
		}
	}
	e.sweep()
}

// OnMark updates a mark price: TP/SL triggers fire on it, and maintenance
// margin is checked at it.
func (e *Engine) OnMark(coin string, mark decimal.Decimal, at int64) {
	if mark.Sign() <= 0 {
		return
	}
	e.touch(at)
	e.st.Marks[coin], e.st.MarkAt[coin] = mark, at
	for _, o := range append([]*Order(nil), e.st.Orders...) {
		if o.Coin != coin || o.Status != StatusTrigger || o.Trigger == nil {
			continue
		}
		up := mark.GreaterThanOrEqual(o.Trigger.Px)
		down := mark.LessThanOrEqual(o.Trigger.Px)
		// a sell closes a long: TP above, SL below; a buy closes a short
		fire := o.Side == domain.Sell && (o.Trigger.Kind == "tp" && up || o.Trigger.Kind == "sl" && down) ||
			o.Side == domain.Buy && (o.Trigger.Kind == "tp" && down || o.Trigger.Kind == "sl" && up)
		if fire {
			e.fire(o, mark, at)
		}
	}
	e.checkLiquidation(at)
	// a fresh book may already be past the trigger latency
	if b, ok := e.books[coin]; ok && b.At >= at {
		for _, o := range append([]*Order(nil), e.st.Orders...) {
			if o.Coin == coin && o.Status == StatusPending && o.ActiveAt <= b.At {
				e.activate(o, b, b.At)
			}
		}
	}
	e.sweep()
}

// fire turns a trigger into a market Ioc bounded by TriggerSlippage from
// the mark, and cancels its one-cancels-other siblings.
func (e *Engine) fire(o *Order, mark decimal.Decimal, at int64) {
	s := e.st.Specs[o.Coin]
	p, ok := e.st.Positions[o.Coin]
	if !ok {
		e.finish(o, StatusCanceled, "position closed", at)
		return
	}
	if o.Grouping == GroupPositionTps || o.Sz.IsZero() {
		o.Sz = p.Szi.Abs()
	}
	if o.OrigSz.IsZero() {
		o.OrigSz = o.Sz
	}
	f := one.Add(e.cfg.TriggerSlippage)
	dir := +1
	if o.Side == domain.Sell {
		f, dir = one.Sub(e.cfg.TriggerSlippage), -1
	}
	o.Px = roundPx(s, mark.Mul(f), dir)
	o.Triggered, o.Status, o.ActiveAt, o.Updated = true, StatusPending, at+e.cfg.Latency.Milliseconds(), at
	for _, sib := range e.st.Orders {
		if sib != o && sib.Group == o.Group && sib.Live() && sib.Trigger != nil && !sib.Triggered {
			e.finish(sib, StatusCanceled, "OCO: sibling triggered", at)
		}
	}
}

// OnFunding settles one hourly funding print (rate per hour, at the
// oracle price) for coin's position, once per hour boundary, only when the
// position was open before it.
func (e *Engine) OnFunding(coin string, hour int64, rate, oracle decimal.Decimal) {
	p, ok := e.st.Positions[coin]
	if !ok || p.Opened >= hour || p.FundedTo >= hour || oracle.Sign() <= 0 {
		return
	}
	pay := p.Szi.Mul(oracle).Mul(rate) // longs pay a positive rate
	if p.Isolated {
		p.Margin = p.Margin.Sub(pay)
	} else {
		e.st.Balance = e.st.Balance.Sub(pay)
	}
	p.Funding = p.Funding.Add(pay)
	p.FundedTo = hour
	e.st.Funding = append(e.st.Funding, FundingEntry{Coin: coin, Time: hour, Rate: rate, Oracle: oracle, Szi: p.Szi, Payment: pay})
	if len(e.st.Funding) > MaxFunding {
		e.st.Funding = append([]FundingEntry(nil), e.st.Funding[len(e.st.Funding)-MaxFunding:]...)
	}
	e.touch(hour)
}

// ---- activation and matching ----

func (e *Engine) restingByPriority(coin string) []*Order {
	var out []*Order
	for _, o := range e.st.Orders {
		if o.Coin == coin && o.Status == StatusOpen {
			out = append(out, o)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.Px.Equal(b.Px) {
			if a.Side == domain.Buy {
				return a.Px.GreaterThan(b.Px)
			}
			return a.Px.LessThan(b.Px)
		}
		return a.Rested < b.Rested
	})
	return out
}

func (e *Engine) activate(o *Order, b *Book, at int64) {
	s := e.st.Specs[o.Coin]
	o.ArrivalMid = b.Mid()
	o.Updated = at
	fail := func(r *Reject) {
		e.finish(o, StatusRejected, r.Msg, at)
		e.st.Stats.Rejections++
		for _, c := range e.st.Orders {
			if c.Parent == o.OID && c.Status == StatusWaiting {
				e.finish(c, StatusCanceled, "parent rejected", at)
			}
		}
	}
	if o.ReduceOnly {
		if cap := e.reduceCap(o, o.Sz); cap.Sign() <= 0 {
			if o.Triggered {
				e.finish(o, StatusCanceled, "position closed", at)
				return
			}
			fail(reject("REDUCE_ONLY", s, msgReduce))
			return
		} else {
			o.Sz = cap
		}
	}
	ask := o.Side == domain.Buy // the side a buy takes from
	opp := b.side(ask)
	crosses := len(opp) > 0 && (o.Side == domain.Buy && !opp[0].Px.GreaterThan(o.Px) || o.Side == domain.Sell && !opp[0].Px.LessThan(o.Px))
	if o.TIF == Alo && crosses {
		bid, ask := "0", "0"
		if len(b.Bids) > 0 {
			bid = b.Bids[0].Px.String()
		}
		if len(b.Asks) > 0 {
			ask = b.Asks[0].Px.String()
		}
		fail(reject("ALO_CROSS", s, msgAlo, bid, ask))
		return
	}
	if !o.ReduceOnly && !o.Triggered {
		if need := e.marginNeeded(o); need.GreaterThan(e.available(o.OID)) {
			fail(reject("MARGIN", s, msgMargin))
			return
		}
	}
	if crosses {
		e.walk(o, b, at, false)
	}
	switch {
	case o.Sz.Sign() <= 0:
		e.finish(o, StatusFilled, "", at)
	case o.TIF == Ioc:
		if o.Filled.IsZero() {
			e.finish(o, StatusCanceled, reject("IOC_NO_MATCH", s, msgIocNone).Msg, at)
		} else {
			e.finish(o, StatusCanceled, "", at) // the remainder of an Ioc is cancelled
		}
	default:
		own := o.Side == domain.Sell
		o.Status, o.Rested = StatusOpen, at
		o.Queue = b.size(own, o.Px)
		o.LevelSeen = o.Queue
	}
	e.afterFill(o, at)
}

// walk takes liquidity for o from the effective book up to its limit
// (or the whole visible book when unbounded, for liquidations). It never
// fills past the book's depth.
func (e *Engine) walk(o *Order, b *Book, at int64, unbounded bool) []Fill {
	ask := o.Side == domain.Buy
	start := len(e.st.Fills)
	for _, l := range b.side(ask) {
		if o.Sz.Sign() <= 0 {
			break
		}
		if !unbounded && (o.Side == domain.Buy && l.Px.GreaterThan(o.Px) || o.Side == domain.Sell && l.Px.LessThan(o.Px)) {
			break
		}
		take := e.reduceCap(o, e.lotFloor(o.Coin, decMin(o.Sz, e.effective(o.Coin, ask, l.Px, l.Sz, at))))
		if take.Sign() <= 0 {
			continue
		}
		e.applyFill(o, l.Px, take, false, at, "book")
		e.consume(o.Coin, ask, l.Px, take, l.Sz, at)
	}
	if len(e.st.Fills) < start {
		start = 0 // the history cap trimmed the front
	}
	fills := e.st.Fills[start:]
	if len(fills) == 0 {
		return nil
	}
	// slippage vs the arrival mid, and impact: the extra cost against the
	// same size walked through the untouched real book
	var qty, cost decimal.Decimal
	for _, f := range fills {
		qty = qty.Add(f.Sz)
		cost = cost.Add(f.Sz.Mul(f.Px))
	}
	mid := o.ArrivalMid
	if mid.IsZero() {
		mid = b.Mid()
	}
	if mid.Sign() <= 0 {
		return fills
	}
	vwap := cost.Div(qty)
	sg := sign(o.Side)
	slip, _ := vwap.Sub(mid).Mul(sg).Div(mid).Mul(bps10k).Float64()
	left, rawCost := qty, decimal.Zero
	for _, l := range b.side(ask) {
		if left.Sign() <= 0 {
			break
		}
		r := decMin(left, l.Sz)
		rawCost = rawCost.Add(r.Mul(l.Px))
		left = left.Sub(r)
	}
	impact := 0.0
	if left.Sign() == 0 {
		impact, _ = vwap.Sub(rawCost.Div(qty)).Mul(sg).Div(mid).Mul(bps10k).Float64()
		impact = max(impact, 0)
	}
	for i := range fills {
		fills[i].SlippageBps, fills[i].ImpactBps = round2(slip), round2(impact)
	}
	return fills
}

func round2(f float64) float64 {
	d := decimal.NewFromFloat(f).Round(2)
	v, _ := d.Float64()
	return v
}

// afterFill arms normalTpsl children with the parent's filled size.
func (e *Engine) afterFill(o *Order, at int64) {
	if o.Status == StatusOpen && o.Sz.Sign() <= 0 {
		e.finish(o, StatusFilled, "", at)
	}
	if o.Filled.IsZero() || o.Trigger != nil {
		return
	}
	for _, c := range e.st.Orders {
		if c.Parent == o.OID && c.Grouping == GroupNormalTpsl && (c.Status == StatusWaiting || c.Status == StatusTrigger) && !c.Triggered {
			c.Sz, c.OrigSz, c.Status, c.Updated = o.Filled, o.Filled, StatusTrigger, at
		}
	}
}

func (e *Engine) lotFloor(coin string, sz decimal.Decimal) decimal.Decimal {
	s, ok := e.st.Specs[coin]
	if !ok {
		return sz
	}
	return sz.RoundFloor(int32(s.SzDecimals))
}

// reduceCap limits a reduce-only take to what still reduces the position.
func (e *Engine) reduceCap(o *Order, sz decimal.Decimal) decimal.Decimal {
	if !o.ReduceOnly {
		return sz
	}
	p, ok := e.st.Positions[o.Coin]
	if !ok || p.Szi.Sign() == sign(o.Side).Sign() {
		return decimal.Zero
	}
	return decMin(sz, p.Szi.Abs())
}

func decMin(a, b decimal.Decimal) decimal.Decimal {
	if a.LessThan(b) {
		return a
	}
	return b
}

// applyFill books one execution against the account.
func (e *Engine) applyFill(o *Order, px, sz decimal.Decimal, maker bool, at int64, model string) *Fill {
	return e.book(o.OID, o.Coin, o.Side, px, sz, maker, at, model, o, false, false)
}

func (e *Engine) book(oid uint64, coin string, side domain.Side, px, sz decimal.Decimal, maker bool, at int64, model string, o *Order, liq, backstop bool) *Fill {
	liq = liq || e.liquidating
	rate := e.cfg.TakerBps
	if maker {
		rate = e.cfg.MakerBps
	}
	fee := px.Mul(sz).Mul(rate).Div(bps10k)
	if backstop {
		fee = decimal.Zero
	}
	p, ok := e.st.Positions[coin]
	if !ok {
		lev := e.levFor(coin)
		if o != nil && o.Leverage > 0 {
			lev = LevSetting{Leverage: o.Leverage, Isolated: o.Isolated}
		}
		p = &Position{Coin: coin, Leverage: lev.Leverage, Isolated: lev.Isolated, Opened: at, FundedTo: at}
		e.st.Positions[coin] = p
	}
	start := p.Szi
	signed := sz.Mul(sign(side))
	closed := decimal.Zero
	pnl := decimal.Zero
	if start.Sign() != 0 && start.Sign() != signed.Sign() {
		closed = decMin(start.Abs(), sz)
		pnl = px.Sub(p.Entry).Mul(closed).Mul(decimal.NewFromInt(int64(start.Sign())))
		if p.Isolated {
			rel := p.Margin.Mul(closed).Div(start.Abs())
			p.Margin = p.Margin.Sub(rel)
			back := rel.Add(pnl)
			if back.Sign() < 0 && liq {
				back = decimal.Zero // the backstop absorbs a loss past the isolated margin
			}
			e.st.Balance = e.st.Balance.Add(back)
		} else {
			e.st.Balance = e.st.Balance.Add(pnl)
		}
	}
	if opened := sz.Sub(closed); opened.Sign() > 0 {
		absStart := start.Abs().Sub(closed) // 0 after a flip
		if absStart.Sign() > 0 && start.Sign() == signed.Sign() {
			p.Entry = p.Entry.Mul(absStart).Add(px.Mul(opened)).Div(absStart.Add(opened))
		} else {
			p.Entry = px
			if start.Sign() != 0 {
				p.Opened, p.FundedTo = at, at // a flip starts a new position
			}
		}
		if p.Isolated {
			m := px.Mul(opened).Div(decimal.NewFromInt(int64(max(p.Leverage, 1))))
			p.Margin = p.Margin.Add(m)
			e.st.Balance = e.st.Balance.Sub(m)
		}
	}
	p.Szi = start.Add(signed)
	e.st.Balance = e.st.Balance.Sub(fee)
	if o != nil {
		o.Sz = o.Sz.Sub(sz)
		o.Filled = o.Filled.Add(sz)
		o.Notional = o.Notional.Add(px.Mul(sz))
		o.Fees = o.Fees.Add(fee)
		o.Updated = at
	}
	f := Fill{TID: e.st.NextTID, OID: oid, Coin: coin, Side: side, Px: px, Sz: sz, Fee: fee, Time: at,
		Dir: direction(start, p.Szi), StartPos: start, ClosedPnL: pnl, Maker: maker, Liquidation: liq, Backstop: backstop, Model: model}
	if o != nil {
		f.ArrivalMid = o.ArrivalMid
	}
	e.st.NextTID++
	e.st.Stats.Fills++
	e.st.Fills = append(e.st.Fills, f)
	if len(e.st.Fills) > MaxFills {
		e.st.Fills = append([]Fill(nil), e.st.Fills[len(e.st.Fills)-MaxFills:]...)
	}
	if p.Szi.IsZero() {
		if p.Isolated && p.Margin.Sign() != 0 {
			if p.Margin.Sign() > 0 {
				e.st.Balance = e.st.Balance.Add(p.Margin)
			}
			p.Margin = decimal.Zero
		}
		delete(e.st.Positions, coin)
		for _, t := range e.st.Orders {
			if t.Coin == coin && t.Trigger != nil && t.Live() && !t.Triggered {
				e.finish(t, StatusCanceled, "position closed", at)
			}
		}
	}
	return &e.st.Fills[len(e.st.Fills)-1]
}

func direction(start, end decimal.Decimal) string {
	switch {
	case start.Sign() >= 0 && end.Sign() > 0 && end.GreaterThan(start):
		return "Open Long"
	case start.Sign() > 0 && end.Sign() >= 0:
		return "Close Long"
	case start.Sign() <= 0 && end.Sign() < 0 && end.LessThan(start):
		return "Open Short"
	case start.Sign() < 0 && end.Sign() <= 0:
		return "Close Short"
	case start.Sign() > 0 && end.Sign() < 0:
		return "Long > Short"
	case start.Sign() < 0 && end.Sign() > 0:
		return "Short > Long"
	}
	return "Trade"
}
