package sim

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/strategy"
)

// Resting is an order waiting to be filled (market orders wait for the next
// bar open in a backtest; limits and stops wait for price).
type Resting struct {
	strategy.Order
	ID     uint64 `json:"id"`
	Placed int64  `json:"placed"` // Now() when placed
}

// ErrHalted is returned by Order once the account has been wiped out.
var ErrHalted = errors.New("account wiped out: trading halted")

// Engine is the simulated broker plus the strategy.Context handed to
// strategies. The owning engine (backtest or paper) feeds it closed bars
// with AddBar, advances the clock with SetNow and asks it to execute
// orders; the strategy talks to it through the strategy.Context methods.
//
// Engine is not safe for concurrent use; paper sessions serialize access.
type Engine struct {
	L        *Ledger
	Costs    strategy.Costs
	Universe []string

	bars    map[string][]domain.Candle
	known   map[string]bool
	now     int64
	orders  []*Resting
	nextID  uint64
	funding map[string]float64
	std     context.Context
	halted  bool

	// Rules are per-coin exchange constraints (lot size, $10 minimum,
	// margin and liquidation; see rules.go). Coins without rules fill any
	// size with no margin model.
	Rules map[string]Rules
	lev   map[string][2]float64
	stats Stats

	journal []TaggedFill // recent fills, for strategies that track trades
	fillSeq uint64

	// MaxBars caps the history kept per coin (0 = unlimited). Paper
	// sessions cap it so memory stays flat on long runs.
	MaxBars int

	// Hooks (all optional).
	OnPlot func(name string, v float64)
	OnMark func(coin string, buy bool, label string)
	OnLog  func(msg string)
	OnFill func(f domain.Fill, tag string)
}

var (
	_ strategy.Context      = (*Engine)(nil)
	_ strategy.PositionInfo = (*Engine)(nil)
	_ strategy.FundingInfo  = (*Engine)(nil)
	_ strategy.StdContexter = (*Engine)(nil)
)

// NewEngine returns an engine with a fresh ledger.
func NewEngine(capital float64, costs strategy.Costs, universe []string) *Engine {
	return NewEngineWithLedger(NewLedger(capital), costs, universe)
}

// NewEngineWithLedger wraps an existing (for example restored) ledger.
func NewEngineWithLedger(l *Ledger, costs strategy.Costs, universe []string) *Engine {
	e := &Engine{L: l, Costs: costs, Universe: append([]string(nil), universe...),
		bars: map[string][]domain.Candle{}, known: map[string]bool{}, funding: map[string]float64{}, nextID: 1}
	for _, c := range universe {
		e.known[c] = true
	}
	return e
}

// SetNow sets the current bar time (ms).
func (e *Engine) SetNow(t int64) { e.now = t }

// SetStdContext attaches a cancellation context returned by StdContext.
func (e *Engine) SetStdContext(ctx context.Context) { e.std = ctx }

// SetFunding records the latest hourly funding rate for coin.
func (e *Engine) SetFunding(coin string, rate float64) { e.funding[coin] = rate }

// Halted reports whether the account was wiped out.
func (e *Engine) Halted() bool { return e.halted }

// AddBar appends a closed bar for coin. A bar with the same open time as the
// last one replaces it; older bars are ignored.
func (e *Engine) AddBar(coin string, c domain.Candle) {
	e.known[coin] = true
	bs := e.bars[coin]
	if n := len(bs); n > 0 {
		switch {
		case c.T == bs[n-1].T:
			bs[n-1] = c
			return
		case c.T < bs[n-1].T:
			return
		}
	}
	bs = append(bs, c)
	if e.MaxBars > 0 && len(bs) > e.MaxBars*2 {
		bs = append([]domain.Candle(nil), bs[len(bs)-e.MaxBars:]...)
	}
	e.bars[coin] = bs
}

// LastBar returns the last closed bar for coin.
func (e *Engine) LastBar(coin string) (domain.Candle, bool) {
	bs := e.bars[coin]
	if len(bs) == 0 {
		return domain.Candle{}, false
	}
	return bs[len(bs)-1], true
}

// Pending returns copies of the resting orders in placement order.
func (e *Engine) Pending() []Resting {
	out := make([]Resting, len(e.orders))
	for i, o := range e.orders {
		out[i] = *o
	}
	return out
}

// SetPending restores resting orders (paper resume).
func (e *Engine) SetPending(rs []Resting) {
	e.orders = e.orders[:0]
	for i := range rs {
		r := rs[i]
		e.orders = append(e.orders, &r)
		if r.ID >= e.nextID {
			e.nextID = r.ID + 1
		}
	}
}

// ---- strategy.Context ----

// Now returns the current bar open time (ms).
func (e *Engine) Now() int64 { return e.now }

// Bars returns the closed bars for coin, oldest first. The slice is shared:
// strategies must not modify it.
func (e *Engine) Bars(coin string) []domain.Candle {
	bs := e.bars[coin]
	if e.MaxBars > 0 && len(bs) > e.MaxBars {
		bs = bs[len(bs)-e.MaxBars:]
	}
	return bs[:len(bs):len(bs)]
}

// Price returns the last close for coin, or 0 when it has no bars yet.
func (e *Engine) Price(coin string) float64 {
	if c, ok := e.LastBar(coin); ok {
		return c.C
	}
	return 0
}

// Position returns the signed base size held in coin.
func (e *Engine) Position(coin string) float64 { return e.L.Position(coin) }

// Equity is cash plus unrealized PnL at the last closes.
func (e *Engine) Equity() float64 { return e.L.Equity(e.Price) }

// Cash is capital plus realized PnL minus fees and funding.
func (e *Engine) Cash() float64 { return e.L.Cash() }

// EntryPrice implements strategy.PositionInfo.
func (e *Engine) EntryPrice(coin string) float64 { return e.L.Entry(coin) }

// RealizedPnL implements strategy.PositionInfo (net of fees and funding).
func (e *Engine) RealizedPnL() float64 { return e.L.Cash() - e.L.Capital() }

// InitialCapital implements strategy.PositionInfo.
func (e *Engine) InitialCapital() float64 { return e.L.Capital() }

// FundingRate implements strategy.FundingInfo.
func (e *Engine) FundingRate(coin string) float64 { return e.funding[coin] }

// StdContext implements strategy.StdContexter.
func (e *Engine) StdContext() context.Context {
	if e.std == nil {
		return context.Background()
	}
	return e.std
}

// Order validates and queues an order (see strategy.Order for semantics).
func (e *Engine) Order(o strategy.Order) error {
	if o.Kind == strategy.Cancel {
		e.cancel(func(r *Resting) bool {
			if o.Tag != "" {
				return r.Tag == o.Tag && (o.Coin == "" || r.Coin == o.Coin)
			}
			return o.Coin == "" || r.Coin == o.Coin
		})
		return nil
	}
	if e.halted {
		return ErrHalted
	}
	if o.Kind == "" {
		o.Kind = strategy.Market
	}
	if !e.known[o.Coin] {
		return fmt.Errorf("order: unknown coin %q (not in the strategy universe)", o.Coin)
	}
	if o.Side != domain.Buy && o.Side != domain.Sell {
		return fmt.Errorf("order %s: side must be B or A, got %q", o.Coin, o.Side)
	}
	if bad(o.Size) || bad(o.NotionalUSD) || o.Size < 0 || o.NotionalUSD < 0 {
		return fmt.Errorf("order %s: size and notional must be finite and >= 0", o.Coin)
	}
	if o.Size > 0 && o.NotionalUSD > 0 {
		return fmt.Errorf("order %s: set Size or NotionalUSD, not both", o.Coin)
	}
	if o.Size == 0 && o.NotionalUSD == 0 && !o.ReduceOnly {
		return fmt.Errorf("order %s: zero size", o.Coin)
	}
	switch o.Kind {
	case strategy.Market:
	case strategy.Limit, strategy.Stop:
		if bad(o.Px) || o.Px <= 0 {
			return fmt.Errorf("order %s: %s order needs a positive price", o.Coin, o.Kind)
		}
		if o.Tag != "" {
			e.cancel(func(r *Resting) bool { return r.Tag == o.Tag && r.Coin == o.Coin && r.Kind == o.Kind })
		}
	default:
		return fmt.Errorf("order %s: unknown kind %q", o.Coin, o.Kind)
	}
	e.orders = append(e.orders, &Resting{Order: o, ID: e.nextID, Placed: e.now})
	e.nextID++
	return nil
}

// SetTargets converts signed NAV weights into market orders. Coins with an
// open position that are missing from weights are closed. Coins without a
// price yet are skipped with a log line. Trades smaller than $1 or 0.01% of
// equity are skipped to avoid churn.
func (e *Engine) SetTargets(weights map[string]float64) error {
	if e.halted {
		return ErrHalted
	}
	for c, w := range weights {
		if bad(w) {
			return fmt.Errorf("targets: weight for %s is not finite", c)
		}
		if !e.known[c] {
			return fmt.Errorf("targets: unknown coin %q (not in the strategy universe)", c)
		}
	}
	e.cancel(func(r *Resting) bool { return r.Kind == strategy.Market && r.Tag == "rebalance" })
	eq := e.Equity()
	coins := map[string]bool{}
	for c := range weights {
		coins[c] = true
	}
	for _, c := range e.L.Coins() {
		coins[c] = true
	}
	keys := make([]string, 0, len(coins))
	for c := range coins {
		keys = append(keys, c)
	}
	sort.Strings(keys)
	minTrade := math.Max(1, 1e-4*math.Abs(eq))
	type leg struct {
		coin  string
		delta float64
		px    float64
	}
	var legs []leg
	for _, c := range keys {
		px := e.Price(c)
		if px <= 0 {
			if weights[c] != 0 {
				e.logf("rebalance: no price for %s yet, skipped", c)
			}
			continue
		}
		target := weights[c] * eq / px
		delta := target - e.L.Position(c)
		if math.Abs(delta)*px < minTrade {
			continue
		}
		legs = append(legs, leg{c, delta, px})
	}
	// Reduce exposure before adding it (matters for margin-aware venues).
	sort.SliceStable(legs, func(i, j int) bool {
		ri := math.Abs(e.L.Position(legs[i].coin)+legs[i].delta) < math.Abs(e.L.Position(legs[i].coin))
		rj := math.Abs(e.L.Position(legs[j].coin)+legs[j].delta) < math.Abs(e.L.Position(legs[j].coin))
		return ri && !rj
	})
	for _, l := range legs {
		side := domain.Buy
		if l.delta < 0 {
			side = domain.Sell
		}
		if err := e.Order(strategy.Order{Coin: l.coin, Side: side, Size: math.Abs(l.delta), Kind: strategy.Market, Tag: "rebalance"}); err != nil {
			return err
		}
	}
	return nil
}

// CloseAll cancels resting orders and queues reduce-only market orders that
// flatten every open position.
func (e *Engine) CloseAll() error {
	e.cancel(func(*Resting) bool { return true })
	for _, c := range e.L.Coins() {
		side := domain.Sell
		if e.L.Position(c) < 0 {
			side = domain.Buy
		}
		if err := e.Order(strategy.Order{Coin: c, Side: side, Kind: strategy.Market, ReduceOnly: true, Tag: "close_all"}); err != nil && !errors.Is(err, ErrHalted) {
			return err
		}
	}
	return nil
}

// Plot forwards to OnPlot.
func (e *Engine) Plot(name string, v float64) {
	if e.OnPlot != nil {
		e.OnPlot(name, v)
	}
}

// Mark forwards to OnMark for the first universe coin.
func (e *Engine) Mark(buy bool, label string) {
	if e.OnMark != nil {
		coin := ""
		if len(e.Universe) > 0 {
			coin = e.Universe[0]
		}
		e.OnMark(coin, buy, label)
	}
}

// Logf forwards a formatted decision note to OnLog.
func (e *Engine) Logf(format string, args ...any) { e.logf(format, args...) }

func (e *Engine) logf(format string, args ...any) {
	if e.OnLog != nil {
		e.OnLog(fmt.Sprintf(format, args...))
	}
}

// ---- execution ----

// FillMarketAt fills coin's queued market orders at px (+/- slippage,
// taker fee) at time t. Backtests pass the next bar's open; paper passes a
// mark when no book is available.
func (e *Engine) FillMarketAt(coin string, px float64, t int64) []domain.Fill {
	slip := e.Costs.SlippageBps / 1e4
	return e.fillMarket(coin, t, func(side domain.Side, sz float64) float64 {
		if side == domain.Buy {
			return px * (1 + slip)
		}
		return px * (1 - slip)
	})
}

// FillMarketBook fills coin's queued market orders by walking book levels
// (VWAP of the levels consumed). Size beyond the visible depth fills at the
// worst level +/- slippage. Falls back to FillMarketAt(mid) semantics when a
// side is empty.
func (e *Engine) FillMarketBook(coin string, b domain.Book, fallback float64, t int64) []domain.Fill {
	slip := e.Costs.SlippageBps / 1e4
	return e.fillMarket(coin, t, func(side domain.Side, sz float64) float64 {
		levels := b.Asks
		if side == domain.Sell {
			levels = b.Bids
		}
		if len(levels) == 0 {
			if fallback <= 0 {
				fallback = b.Mid()
			}
			if side == domain.Buy {
				return fallback * (1 + slip)
			}
			return fallback * (1 - slip)
		}
		left, cost := sz, 0.0
		for _, lv := range levels {
			take := math.Min(left, lv.Sz)
			cost += take * lv.Px
			left -= take
			if left <= 1e-12 {
				break
			}
		}
		if left > 1e-12 {
			worst := levels[len(levels)-1].Px
			if side == domain.Buy {
				worst *= 1 + slip
			} else {
				worst *= 1 - slip
			}
			cost += left * worst
		}
		return cost / sz
	})
}

func (e *Engine) fillMarket(coin string, t int64, price func(domain.Side, float64) float64) []domain.Fill {
	var fills []domain.Fill
	keep := e.orders[:0]
	var due []*Resting
	for _, r := range e.orders {
		if r.Coin == coin && r.Kind == strategy.Market {
			due = append(due, r)
			continue
		}
		keep = append(keep, r)
	}
	e.orders = keep
	for _, r := range due {
		// Price a 1-unit probe first to size notional orders.
		probe := price(r.Side, 1)
		sz, ok := e.resolveSize(r, probe)
		if !ok {
			continue
		}
		if sz, ok = e.applyRules(r, sz, probe); !ok {
			continue
		}
		px := price(r.Side, sz)
		if px <= 0 || bad(px) {
			continue
		}
		f := e.L.Fill(coin, r.Side, sz, px, e.Costs.TakerBps/1e4, t)
		fills = append(fills, f)
		e.emit(f, r.Tag)
	}
	e.cleanup(coin)
	return fills
}

// MatchResting fills coin's resting limit and stop orders against bar,
// walking the intrabar path the way TradingView's broker emulator does:
// open -> nearer extreme -> farther extreme -> close. Only orders placed
// before the bar opened are eligible. Limit fills pay the maker fee at the
// limit (or the open, if it gapped through); stops pay taker plus slippage.
func (e *Engine) MatchResting(coin string, bar domain.Candle) []domain.Fill {
	type cand struct {
		r   *Resting
		pos float64
		px  float64
	}
	path := []float64{bar.O, bar.L, bar.H, bar.C}
	if bar.H-bar.O < bar.O-bar.L {
		path = []float64{bar.O, bar.H, bar.L, bar.C}
	}
	slip := e.Costs.SlippageBps / 1e4
	var fills []domain.Fill
	for {
		var best *cand
		for _, r := range e.orders {
			if r.Coin != coin || r.Kind == strategy.Market || r.Placed >= bar.T {
				continue
			}
			pos, px, ok := touch(path, r)
			if !ok {
				continue
			}
			if best == nil || pos < best.pos || (pos == best.pos && r.ID < best.r.ID) {
				best = &cand{r, pos, px}
			}
		}
		if best == nil {
			break
		}
		r := best.r
		e.remove(r)
		fee := e.Costs.MakerBps / 1e4
		px := best.px
		if r.Kind == strategy.Stop {
			fee = e.Costs.TakerBps / 1e4
			if r.Side == domain.Buy {
				px *= 1 + slip
			} else {
				px *= 1 - slip
			}
		}
		sz, ok := e.resolveSize(r, px)
		if ok {
			sz, ok = e.applyRules(r, sz, px)
		}
		if ok {
			f := e.L.Fill(coin, r.Side, sz, px, fee, bar.T)
			fills = append(fills, f)
			e.emit(f, r.Tag)
			if r.Tag != "" { // one-cancels-other
				e.cancel(func(o *Resting) bool { return o.Tag == r.Tag && o.Coin == coin && o.Kind != strategy.Market })
			}
		}
		e.cleanup(coin)
	}
	return fills
}

// touch returns where along path (segment index + fraction) the order
// triggers and its fill price.
func touch(path []float64, r *Resting) (float64, float64, bool) {
	p := r.Px
	o := path[0]
	// Already through at the open: fill at the open.
	switch {
	case r.Kind == strategy.Limit && r.Side == domain.Buy && o <= p,
		r.Kind == strategy.Limit && r.Side == domain.Sell && o >= p,
		r.Kind == strategy.Stop && r.Side == domain.Buy && o >= p,
		r.Kind == strategy.Stop && r.Side == domain.Sell && o <= p:
		return 0, o, true
	}
	for i := 0; i+1 < len(path); i++ {
		a, b := path[i], path[i+1]
		lo, hi := math.Min(a, b), math.Max(a, b)
		if p >= lo && p <= hi && a != b {
			return float64(i) + math.Abs(p-a)/math.Abs(b-a), p, true
		}
	}
	return 0, 0, false
}

// resolveSize turns an order into a fill size at px, applying reduce-only.
func (e *Engine) resolveSize(r *Resting, px float64) (float64, bool) {
	sz := r.Size
	if sz == 0 && r.NotionalUSD > 0 && px > 0 {
		sz = r.NotionalUSD / px
	}
	if r.ReduceOnly {
		pos := e.L.Position(r.Coin)
		if pos == 0 || (pos > 0) == (r.Side == domain.Buy) {
			return 0, false
		}
		if sz == 0 || sz > math.Abs(pos) {
			sz = math.Abs(pos)
		}
	}
	if sz <= 0 || bad(sz) {
		return 0, false
	}
	return sz, true
}

// cleanup drops resting reduce-only orders for coin once it is flat.
func (e *Engine) cleanup(coin string) {
	if e.L.Position(coin) != 0 {
		return
	}
	e.cancel(func(r *Resting) bool { return r.Coin == coin && r.ReduceOnly && r.Kind != strategy.Market })
}

func (e *Engine) cancel(match func(*Resting) bool) {
	keep := e.orders[:0]
	for _, r := range e.orders {
		if !match(r) {
			keep = append(keep, r)
		}
	}
	for i := len(keep); i < len(e.orders); i++ {
		e.orders[i] = nil
	}
	e.orders = keep
}

func (e *Engine) remove(x *Resting) { e.cancel(func(r *Resting) bool { return r == x }) }

// ApplyFunding charges funding on coin's position at px.
func (e *Engine) ApplyFunding(coin string, rate, px float64) float64 {
	return e.L.ApplyFunding(coin, rate, px)
}

// CheckWipeout halts trading and flattens every position at the last
// closes when equity has fallen to zero or below. It reports whether the
// account was (already) wiped out.
func (e *Engine) CheckWipeout(t int64) bool {
	if e.halted {
		return true
	}
	if e.Equity() > 0 {
		return false
	}
	e.orders = nil
	for _, c := range e.L.Coins() {
		side := domain.Sell
		pos := e.L.Position(c)
		if pos < 0 {
			side = domain.Buy
		}
		f := e.L.Fill(c, side, math.Abs(pos), e.Price(c), e.Costs.TakerBps/1e4, t)
		f.Liq = true
		e.emit(f, "liquidation")
	}
	e.halted = true
	e.logf("account wiped out (equity <= 0): positions closed, trading halted")
	return true
}

func bad(x float64) bool { return math.IsNaN(x) || math.IsInf(x, 0) }

// TaggedFill is a fill with the tag of the order that caused it (a Pine
// entry or exit id, "liquidation", ...) and a sequence number.
type TaggedFill struct {
	domain.Fill
	Tag string
	Seq uint64
}

// maxJournal bounds the fill journal (strategies read it every bar).
const maxJournal = 1024

// emit records f in the journal and calls OnFill.
func (e *Engine) emit(f domain.Fill, tag string) {
	e.fillSeq++
	e.journal = append(e.journal, TaggedFill{Fill: f, Tag: tag, Seq: e.fillSeq})
	if len(e.journal) > 2*maxJournal {
		e.journal = append([]TaggedFill(nil), e.journal[len(e.journal)-maxJournal:]...)
	}
	if e.OnFill != nil {
		e.OnFill(f, tag)
	}
}

// FillsAfter returns the journaled fills with Seq > seq, oldest first (at
// most the last 1024). Pine reads it to keep its trade list from fills.
func (e *Engine) FillsAfter(seq uint64) []TaggedFill {
	i := len(e.journal)
	for i > 0 && e.journal[i-1].Seq > seq {
		i--
	}
	return e.journal[i:len(e.journal):len(e.journal)]
}
