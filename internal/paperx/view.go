package paperx

import (
	"sort"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// AddressPrefix names a paper account where an address is expected:
// `hlp account paper:default`.
const AddressPrefix = "paper:"

// ParseAddress returns the account id of "paper:<id>" (id "default" for
// a bare "paper:" or "paper"), and false for anything else.
func ParseAddress(s string) (string, bool) {
	t := strings.TrimSpace(s)
	if strings.EqualFold(t, "paper") {
		return "default", true
	}
	if len(t) < len(AddressPrefix) || !strings.EqualFold(t[:len(AddressPrefix)], AddressPrefix) {
		return "", false
	}
	id := t[len(AddressPrefix):]
	if id == "" {
		id = "default"
	}
	return id, true
}

func f64(d decimal.Decimal) float64 {
	v, _ := d.Float64()
	return v
}

// usd rounds money for output: never 18-decimal noise.
func usd(d decimal.Decimal) float64 { return f64(d.Round(6)) }

// Account renders the paper account as a domain.Account, so every account
// card and agent reader works on it unchanged. Role is "paper".
func (e *Engine) Account() domain.Account {
	st := e.st
	a := domain.Account{Address: AddressPrefix + st.ID, Role: "paper", Time: st.Updated, Positions: []domain.Position{}}
	eq := e.Equity()
	a.Equity, a.PerpEquity = usd(eq), usd(eq)
	used := decimal.Zero
	gross, net := decimal.Zero, decimal.Zero
	for _, c := range sortedCoins(st.Positions) {
		p := st.Positions[c]
		mark := e.markOr(c, p.Entry)
		ntl := p.Szi.Abs().Mul(mark)
		m := ntl.Div(levD(p.Leverage))
		if p.Isolated {
			m = p.Margin
		}
		used = used.Add(m)
		gross = gross.Add(ntl)
		net = net.Add(p.Szi.Mul(mark))
		up := e.upnl(p)
		roe := 0.0
		if m.Sign() > 0 {
			roe = f64(up.Div(m).Round(6))
		}
		a.Positions = append(a.Positions, domain.Position{
			Coin: c, Size: f64(p.Szi), Entry: f64(p.Entry.Round(8)), Mark: f64(mark), Value: usd(ntl), UPnL: usd(up),
			ROE: roe, Leverage: float64(p.Leverage), Cross: !p.Isolated, LiqPx: f64(e.LiqPx(p).Round(8)),
			MarginUsed: usd(m), FundingAll: usd(p.Funding),
		})
	}
	sort.SliceStable(a.Positions, func(i, j int) bool { return a.Positions[i].Value > a.Positions[j].Value })
	a.MarginUsed, a.GrossNtl, a.NetNtl = usd(used), usd(gross), usd(net)
	if w := e.available(0); w.Sign() > 0 {
		a.Withdrawable = usd(w)
	}
	for _, o := range st.Orders {
		a.OpenOrders = append(a.OpenOrders, o.View())
	}
	return a
}

// View renders an order as a domain.Order.
func (o *Order) View() domain.Order {
	typ := "Limit"
	tp := 0.0
	if o.Trigger != nil {
		tp = f64(o.Trigger.Px)
		typ = "Stop Market"
		if o.Trigger.Kind == "tp" {
			typ = "Take Profit Market"
		}
	} else if o.Market {
		typ = "Market"
	}
	return domain.Order{Coin: o.Coin, Side: o.Side, Px: f64(o.Px), Sz: f64(o.Sz), OrigSz: f64(o.OrigSz), OID: o.OID,
		CLOID: o.CLOID, Type: typ, TIF: o.TIF, ReduceOnly: o.ReduceOnly, TriggerPx: tp, Time: o.Placed}
}

// DomainFills renders fills newest first as domain.Fill.
func DomainFills(fs []Fill) []domain.Fill {
	out := make([]domain.Fill, 0, len(fs))
	for i := len(fs) - 1; i >= 0; i-- {
		f := fs[i]
		out = append(out, domain.Fill{Coin: f.Coin, Side: f.Side, Px: f64(f.Px), Sz: f64(f.Sz), Time: f.Time, Dir: f.Dir,
			StartPos: f64(f.StartPos), ClosedPnL: usd(f.ClosedPnL), Fee: usd(f.Fee), OID: f.OID, TID: f.TID, Liq: f.Liquidation})
	}
	return out
}

// OrderView is an order result for agents: decimals as exchange strings.
type OrderView struct {
	Mode       string     `json:"mode"` // always "paper"
	Account    string     `json:"account"`
	OID        uint64     `json:"oid"`
	CLOID      string     `json:"cloid,omitempty"`
	Coin       string     `json:"coin"`
	Side       string     `json:"side"`
	Type       string     `json:"type"` // limit | market | tp | sl
	TIF        string     `json:"tif"`
	Px         string     `json:"px"`
	Sz         string     `json:"sz"`
	Filled     string     `json:"filled"`
	Remaining  string     `json:"remaining"`
	AvgPx      string     `json:"avgPx,omitempty"`
	Fees       float64    `json:"fees"`
	Status     string     `json:"status"`
	Error      string     `json:"error,omitempty"`
	ReduceOnly bool       `json:"reduceOnly,omitempty"`
	Leverage   int        `json:"leverage,omitempty"`
	Isolated   bool       `json:"isolated,omitempty"`
	QueueAhead string     `json:"queueAhead,omitempty"` // resting: size ahead at the price
	TriggerPx  string     `json:"triggerPx,omitempty"`
	Parent     uint64     `json:"parent,omitempty"`
	Grouping   string     `json:"grouping,omitempty"`
	Placed     int64      `json:"placed"`
	Fills      []FillView `json:"fills,omitempty"`
}

// FillView is a fill for agents.
type FillView struct {
	TID         uint64  `json:"tid"`
	OID         uint64  `json:"oid"`
	Coin        string  `json:"coin"`
	Side        string  `json:"side"`
	Px          string  `json:"px"`
	Sz          string  `json:"sz"`
	Fee         float64 `json:"fee"`
	Time        int64   `json:"time"`
	Dir         string  `json:"dir"`
	ClosedPnL   float64 `json:"closedPnl"`
	Maker       bool    `json:"maker"`
	Liquidation bool    `json:"liquidation,omitempty"`
	Backstop    bool    `json:"backstop,omitempty"`
	Model       string  `json:"model"`
	ArrivalMid  float64 `json:"arrivalMid,omitempty"`
	SlippageBps float64 `json:"slippageBps"`
	ImpactBps   float64 `json:"impactBps"`
	QueueWaitMs int64   `json:"queueWaitMs,omitempty"`
}

func wire(d decimal.Decimal) string {
	if d.IsZero() {
		return "0"
	}
	s := d.String()
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

func sideWord(s domain.Side) string {
	if s == domain.Buy {
		return "buy"
	}
	return "sell"
}

// ViewFill renders one fill.
func ViewFill(f Fill) FillView {
	return FillView{TID: f.TID, OID: f.OID, Coin: f.Coin, Side: sideWord(f.Side), Px: wire(f.Px), Sz: wire(f.Sz), Fee: usd(f.Fee),
		Time: f.Time, Dir: f.Dir, ClosedPnL: usd(f.ClosedPnL), Maker: f.Maker, Liquidation: f.Liquidation, Backstop: f.Backstop,
		Model: f.Model, ArrivalMid: f64(f.ArrivalMid), SlippageBps: f.SlippageBps, ImpactBps: f.ImpactBps, QueueWaitMs: f.QueueWaitMs}
}

// ViewOrder renders an order with its fills.
func (e *Engine) ViewOrder(o *Order) OrderView {
	typ := "limit"
	switch {
	case o.Trigger != nil:
		typ = o.Trigger.Kind
	case o.Market:
		typ = "market"
	}
	v := OrderView{Mode: Mode, Account: e.st.ID, OID: o.OID, CLOID: o.CLOID, Coin: o.Coin, Side: sideWord(o.Side), Type: typ, TIF: o.TIF,
		Px: wire(o.Px), Sz: wire(o.OrigSz), Filled: wire(o.Filled), Remaining: wire(o.Sz), Fees: usd(o.Fees), Status: o.Status,
		Error: o.Error, ReduceOnly: o.ReduceOnly, Leverage: o.Leverage, Isolated: o.Isolated, Parent: o.Parent, Grouping: o.Grouping, Placed: o.Placed}
	if o.Filled.Sign() > 0 {
		v.AvgPx = wire(o.Notional.Div(o.Filled).Round(8))
	}
	if o.Status == StatusOpen {
		v.QueueAhead = wire(o.Queue.Round(8))
	}
	if o.Trigger != nil {
		v.TriggerPx = wire(o.Trigger.Px)
	}
	for _, f := range e.FillsFor(o.OID) {
		v.Fills = append(v.Fills, ViewFill(f))
	}
	return v
}

// Summary is `paper summary`: the account with its P&L split.
type Summary struct {
	Mode       string         `json:"mode"`
	ID         string         `json:"id"`
	Network    domain.Network `json:"network,omitempty"`
	Initial    float64        `json:"initial"`
	Balance    float64        `json:"balance"`
	Equity     float64        `json:"equity"`
	PnL        float64        `json:"pnl"`
	Realized   float64        `json:"realized"`
	Unrealized float64        `json:"unrealized"`
	Fees       float64        `json:"fees"`
	Funding    float64        `json:"funding"` // + paid
	Stats      Stats          `json:"stats"`
	Orders     []OrderView    `json:"orders"`
	Account    domain.Account `json:"account"`
	Created    int64          `json:"created"`
	Updated    int64          `json:"updated"`
	FeedNote   string         `json:"feedNote,omitempty"`
}

// Summarize builds the summary at the current marks.
func (e *Engine) Summarize() Summary {
	st := e.st
	s := Summary{Mode: Mode, ID: st.ID, Network: st.Network, Initial: usd(st.Initial), Balance: usd(st.Balance), Stats: st.Stats,
		Created: st.Created, Updated: st.Updated, Orders: []OrderView{}}
	eq := e.Equity()
	s.Equity, s.PnL = usd(eq), usd(eq.Sub(st.Initial))
	var real, fees, fund, up decimal.Decimal
	for _, f := range st.Fills {
		real, fees = real.Add(f.ClosedPnL), fees.Add(f.Fee)
	}
	for _, p := range st.Positions {
		up = up.Add(e.upnl(p))
	}
	for _, fe := range st.Funding {
		fund = fund.Add(fe.Payment)
	}
	s.Realized, s.Fees, s.Funding, s.Unrealized = usd(real), usd(fees), usd(fund), usd(up)
	for _, o := range st.Orders {
		s.Orders = append(s.Orders, e.ViewOrder(o))
	}
	s.Account = e.Account()
	return s
}
