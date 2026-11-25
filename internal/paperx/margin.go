package paperx

import (
	"sort"

	"github.com/shopspring/decimal"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// markOr is the mark for coin, or fallback when none is known.
func (e *Engine) markOr(coin string, fallback decimal.Decimal) decimal.Decimal {
	if m, ok := e.st.Marks[coin]; ok && m.Sign() > 0 {
		return m
	}
	if b, ok := e.books[coin]; ok {
		if mid := b.Mid(); mid.Sign() > 0 {
			return mid
		}
	}
	return fallback
}

func (e *Engine) upnl(p *Position) decimal.Decimal {
	return e.markOr(p.Coin, p.Entry).Sub(p.Entry).Mul(p.Szi)
}

func (e *Engine) notional(p *Position) decimal.Decimal {
	return p.Szi.Abs().Mul(e.markOr(p.Coin, p.Entry))
}

func levD(l int) decimal.Decimal { return decimal.NewFromInt(int64(max(l, 1))) }

// crossEquity is the cross account value: balance plus cross unrealized
// PnL (isolated margin has been moved out of the balance).
func (e *Engine) crossEquity() decimal.Decimal {
	eq := e.st.Balance
	for _, p := range e.st.Positions {
		if !p.Isolated {
			eq = eq.Add(e.upnl(p))
		}
	}
	return eq
}

// Equity is the whole account value at the current marks.
func (e *Engine) Equity() decimal.Decimal {
	eq := e.crossEquity()
	for _, p := range e.st.Positions {
		if p.Isolated {
			eq = eq.Add(p.Margin).Add(e.upnl(p))
		}
	}
	return eq
}

func (e *Engine) crossMarginUsed() decimal.Decimal {
	m := decimal.Zero
	for _, p := range e.st.Positions {
		if !p.Isolated {
			m = m.Add(e.notional(p).Div(levD(p.Leverage)))
		}
	}
	return m
}

// orderMargin is the margin open orders reserve (except skip).
func (e *Engine) orderMargin(skip uint64) decimal.Decimal {
	m := decimal.Zero
	for _, o := range e.st.Orders {
		if o.OID == skip || o.ReduceOnly || o.Trigger != nil || !(o.Status == StatusOpen || o.Status == StatusPending) {
			continue
		}
		m = m.Add(o.Sz.Mul(o.Px).Div(levD(o.Leverage)))
	}
	return m
}

// available is what a new order may use as initial margin.
func (e *Engine) available(skip uint64) decimal.Decimal {
	return e.crossEquity().Sub(e.crossMarginUsed()).Sub(e.orderMargin(skip))
}

// marginNeeded is the initial margin for the part of o that grows the
// position.
func (e *Engine) marginNeeded(o *Order) decimal.Decimal {
	grow := o.Sz
	if p, ok := e.st.Positions[o.Coin]; ok && p.Szi.Sign() != 0 && p.Szi.Sign() != sign(o.Side).Sign() {
		grow = o.Sz.Sub(p.Szi.Abs())
		if grow.Sign() <= 0 {
			return decimal.Zero
		}
	}
	return grow.Mul(o.Px).Div(levD(o.Leverage))
}

// checkLiquidation liquidates isolated positions under their maintenance
// margin, then cross positions (largest notional first) while the cross
// account value is under the cross maintenance margin.
func (e *Engine) checkLiquidation(at int64) {
	for _, c := range sortedCoins(e.st.Positions) {
		p := e.st.Positions[c]
		if p == nil || !p.Isolated {
			continue
		}
		if p.Margin.Add(e.upnl(p)).LessThan(maintenance(e.st.Specs[c], e.notional(p))) {
			e.liquidate(p, at)
		}
	}
	for guard := 0; guard < 64; guard++ {
		var cross []*Position
		mm := decimal.Zero
		for _, c := range sortedCoins(e.st.Positions) {
			if p := e.st.Positions[c]; !p.Isolated {
				cross = append(cross, p)
				mm = mm.Add(maintenance(e.st.Specs[c], e.notional(p)))
			}
		}
		if len(cross) == 0 || !e.crossEquity().LessThan(mm) {
			return
		}
		sort.SliceStable(cross, func(i, j int) bool { return e.notional(cross[i]).GreaterThan(e.notional(cross[j])) })
		e.liquidate(cross[0], at)
	}
}

func sortedCoins(m map[string]*Position) []string {
	out := make([]string, 0, len(m))
	for c := range m {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// liquidate closes p at market through a fresh book, and closes what the
// book cannot absorb at mark through the backstop. Every live order on the
// coin is cancelled first.
func (e *Engine) liquidate(p *Position, at int64) {
	coin := p.Coin
	for _, o := range e.st.Orders {
		if o.Coin == coin && o.Live() {
			e.finish(o, StatusCanceled, "liquidation", at)
		}
	}
	e.st.Stats.Liquidations++
	side := domain.Sell
	if p.Szi.Sign() < 0 {
		side = domain.Buy
	}
	o := &Order{OID: e.st.NextOID, Coin: coin, Side: side, Sz: p.Szi.Abs(), OrigSz: p.Szi.Abs(), TIF: Ioc, Market: true,
		ReduceOnly: true, Placed: at, ActiveAt: at, Updated: at, Status: StatusPending, Error: "liquidation"}
	e.st.NextOID++
	if b, ok := e.books[coin]; ok && b.At >= at-e.cfg.BookStale.Milliseconds() {
		o.ArrivalMid = b.Mid()
		e.liquidating = true
		e.walk(o, b, at, true)
		e.liquidating = false
	}
	if rest := e.reduceCap(o, o.Sz); rest.Sign() > 0 {
		mark := e.markOr(coin, p.Entry)
		e.book(o.OID, coin, side, mark, rest, false, at, "backstop", o, true, true)
		e.st.Stats.Backstops++
	}
	e.finish(o, StatusFilled, "liquidation", at)
	e.st.Closed = capOrders(append(e.st.Closed, o))
	if e.st.Balance.Sign() < 0 && len(e.st.Positions) == 0 {
		e.st.Balance = decimal.Zero // the backstop absorbs a negative account
	}
}

// LiqPx solves the mark at which p is liquidated, other marks held: for
// cross, where the cross account value meets the cross maintenance margin;
// for isolated, where its margin plus PnL meets its own. Zero = none.
func (e *Engine) LiqPx(p *Position) decimal.Decimal {
	s := e.st.Specs[p.Coin]
	rate := mmRate(s, e.notional(p))
	abs := p.Szi.Abs()
	den := p.Szi.Sub(abs.Mul(rate))
	if den.IsZero() {
		return decimal.Zero
	}
	var num decimal.Decimal
	if p.Isolated {
		num = p.Szi.Mul(p.Entry).Sub(p.Margin)
	} else {
		other := e.st.Balance
		mmOther := decimal.Zero
		for c, q := range e.st.Positions {
			if q.Isolated || c == p.Coin {
				continue
			}
			other = other.Add(e.upnl(q))
			mmOther = mmOther.Add(maintenance(e.st.Specs[c], e.notional(q)))
		}
		num = mmOther.Sub(other).Add(p.Szi.Mul(p.Entry))
	}
	px := num.Div(den)
	if px.Sign() <= 0 {
		return decimal.Zero
	}
	return px
}
