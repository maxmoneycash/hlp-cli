// Package sim is the simulated broker shared by the backtest and paper
// engines: a perp ledger (cash, net positions, average entries, fees,
// funding, closed PnL), resting-order matching with Pine-style intrabar
// paths, and the strategy.Context implementation both engines hand to
// strategies. Keeping one implementation means a strategy behaves the same
// in a backtest and in paper mode.
//
// Nothing here talks to the network or to
// hyperliquid/exchange; every fill is simulated.
package sim

import (
	"math"
	"sort"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// Pos is one net perp position.
type Pos struct {
	Size  float64 `json:"size"`  // signed base units
	Entry float64 `json:"entry"` // average entry price
	// Funding is the cumulative funding paid (+) or received (-) while open.
	Funding float64 `json:"funding,omitempty"`
}

// LedgerState is the persistable form of a Ledger.
type LedgerState struct {
	Capital   float64        `json:"capital"`
	Cash      float64        `json:"cash"`
	Realized  float64        `json:"realized"`
	Fees      float64        `json:"fees"`
	Funding   float64        `json:"funding"`
	Positions map[string]Pos `json:"positions"`
	NextOID   uint64         `json:"nextOid"`
}

// Ledger is a cross-margin perp account. Cash is the realized balance:
// capital + closed PnL - fees - funding. Equity adds unrealized PnL.
type Ledger struct {
	capital  float64
	cash     float64
	realized float64
	fees     float64
	funding  float64
	pos      map[string]*Pos
	nextOID  uint64
}

// NewLedger returns a flat ledger holding capital USD.
func NewLedger(capital float64) *Ledger {
	return &Ledger{capital: capital, cash: capital, pos: map[string]*Pos{}, nextOID: 1}
}

// LedgerFromState restores a persisted ledger.
func LedgerFromState(s LedgerState) *Ledger {
	l := &Ledger{capital: s.Capital, cash: s.Cash, realized: s.Realized, fees: s.Fees, funding: s.Funding,
		pos: map[string]*Pos{}, nextOID: s.NextOID}
	if l.nextOID == 0 {
		l.nextOID = 1
	}
	for c, p := range s.Positions {
		p := p
		if p.Size != 0 {
			l.pos[c] = &p
		}
	}
	return l
}

// State returns a persistable copy.
func (l *Ledger) State() LedgerState {
	ps := make(map[string]Pos, len(l.pos))
	for c, p := range l.pos {
		ps[c] = *p
	}
	return LedgerState{Capital: l.capital, Cash: l.cash, Realized: l.realized, Fees: l.fees,
		Funding: l.funding, Positions: ps, NextOID: l.nextOID}
}

// Capital is the starting balance.
func (l *Ledger) Capital() float64 { return l.capital }

// Cash is capital + realized PnL - fees - funding.
func (l *Ledger) Cash() float64 { return l.cash }

// Realized is the gross closed PnL (before fees and funding).
func (l *Ledger) Realized() float64 { return l.realized }

// FeesPaid is the cumulative trading fees.
func (l *Ledger) FeesPaid() float64 { return l.fees }

// FundingPaid is the cumulative net funding paid (negative = received).
func (l *Ledger) FundingPaid() float64 { return l.funding }

// Position returns the signed size for coin.
func (l *Ledger) Position(coin string) float64 {
	if p := l.pos[coin]; p != nil {
		return p.Size
	}
	return 0
}

// Entry returns the average entry price for coin (0 when flat).
func (l *Ledger) Entry(coin string) float64 {
	if p := l.pos[coin]; p != nil {
		return p.Entry
	}
	return 0
}

// Coins returns coins with open positions, sorted.
func (l *Ledger) Coins() []string {
	out := make([]string, 0, len(l.pos))
	for c := range l.pos {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Equity marks every open position at price(coin). Coins with no price
// (price <= 0) are marked at entry.
func (l *Ledger) Equity(price func(string) float64) float64 {
	eq := l.cash
	for c, p := range l.pos {
		px := price(c)
		if px <= 0 {
			px = p.Entry
		}
		eq += p.Size * (px - p.Entry)
	}
	return eq
}

// Gross returns the sum of |notional| at price.
func (l *Ledger) Gross(price func(string) float64) float64 {
	g := 0.0
	for c, p := range l.pos {
		px := price(c)
		if px <= 0 {
			px = p.Entry
		}
		g += math.Abs(p.Size * px)
	}
	return g
}

// Fill books an execution of sz (> 0) base units at px with the given fee
// rate (fraction of notional). It returns the resulting domain.Fill with
// Hyperliquid-style Dir labels and closed PnL.
func (l *Ledger) Fill(coin string, side domain.Side, sz, px, feeRate float64, t int64) domain.Fill {
	signed := sz
	if side == domain.Sell {
		signed = -sz
	}
	p := l.pos[coin]
	if p == nil {
		p = &Pos{}
	}
	start := p.Size
	closed := 0.0
	switch {
	case start == 0 || sameSign(start, signed):
		// open or add
		newSize := start + signed
		p.Entry = (p.Entry*math.Abs(start) + px*sz) / math.Abs(newSize)
		p.Size = newSize
	default:
		// reduce, close or flip
		closeQty := math.Min(math.Abs(start), sz)
		if start > 0 {
			closed = closeQty * (px - p.Entry)
		} else {
			closed = closeQty * (p.Entry - px)
		}
		p.Size = start + signed
		if math.Abs(p.Size) < 1e-12 {
			p.Size = 0
		}
		if p.Size != 0 && !sameSign(p.Size, start) {
			p.Entry = px // flipped: the remainder opened at px
			p.Funding = 0
		}
	}
	fee := sz * px * feeRate
	l.realized += closed
	l.fees += fee
	l.cash += closed - fee
	if p.Size == 0 {
		delete(l.pos, coin)
	} else {
		l.pos[coin] = p
	}
	oid := l.nextOID
	l.nextOID++
	return domain.Fill{Coin: coin, Side: side, Px: px, Sz: sz, Time: t, Dir: dirLabel(start, p.Size),
		StartPos: start, ClosedPnL: closed, Fee: fee, OID: oid, TID: oid}
}

// ApplyFunding charges one funding payment: size * px * rate is paid by
// longs when rate > 0 (received when negative). Returns the payment.
func (l *Ledger) ApplyFunding(coin string, rate, px float64) float64 {
	p := l.pos[coin]
	if p == nil || px <= 0 || math.IsNaN(rate) {
		return 0
	}
	pay := p.Size * px * rate
	p.Funding += pay
	l.funding += pay
	l.cash -= pay
	return pay
}

// Account renders the ledger as a domain.Account marked at price.
func (l *Ledger) Account(addr string, price func(string) float64, t int64) domain.Account {
	eq := l.Equity(price)
	a := domain.Account{Address: addr, Role: "paper", Equity: eq, PerpEquity: eq, Time: t}
	for _, c := range l.Coins() {
		p := l.pos[c]
		px := price(c)
		if px <= 0 {
			px = p.Entry
		}
		val := math.Abs(p.Size * px)
		upnl := p.Size * (px - p.Entry)
		roe := 0.0
		if p.Entry > 0 {
			roe = upnl / math.Abs(p.Size*p.Entry)
		}
		lev := 0.0
		if eq > 0 {
			lev = val / eq
		}
		a.Positions = append(a.Positions, domain.Position{Coin: c, Size: p.Size, Entry: p.Entry, Mark: px,
			Value: val, UPnL: upnl, ROE: roe, Leverage: lev, Cross: true, FundingAll: -p.Funding})
		a.GrossNtl += val
		a.NetNtl += p.Size * px
	}
	a.MarginUsed = 0
	a.Withdrawable = math.Max(eq, 0)
	return a
}

func sameSign(a, b float64) bool { return (a > 0 && b > 0) || (a < 0 && b < 0) }

func dirLabel(start, end float64) string {
	switch {
	case start == 0 && end > 0:
		return "Open Long"
	case start == 0 && end < 0:
		return "Open Short"
	case start > 0 && end > start:
		return "Open Long"
	case start < 0 && end < start:
		return "Open Short"
	case start > 0 && end < 0:
		return "Long > Short"
	case start < 0 && end > 0:
		return "Short > Long"
	case start > 0:
		return "Close Long"
	case start < 0:
		return "Close Short"
	}
	return ""
}
