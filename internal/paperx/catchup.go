package paperx

import (
	"sort"

	"github.com/shopspring/decimal"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// Catch-up: a one-shot CLI process has no feed attached between commands.
// When an account is loaded again, the time it was detached is replayed
// from 1m candles, conservatively:
//
//   - a resting order fills only when the price traded strictly THROUGH it
//     (a print through a price clears that level, queue included, under
//     price-time priority), in full, as maker, at its own price. A touch
//     alone fills nothing: the queue position is unknown.
//   - marks follow the candle path (open, nearer extreme, farther extreme,
//     close); TP/SL fire on it and fill at the trigger price, or at the
//     open when the candle gapped past it;
//   - liquidation is checked on the same path and, without a book, closes
//     through the backstop at the path price.
//
// Fills from catch-up are labelled Model "catchup".

// CatchUp replays coin's candles (oldest first, step ms apart) up to upTo
// and returns the number of fills it made. Only candles that open at or
// after the coin's last synced time are used.
func (e *Engine) CatchUp(coin string, cs []domain.Candle, step, upTo int64) int {
	from := e.st.Synced[coin]
	before := e.st.Stats.Fills
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].T < cs[j].T })
	for _, c := range cs {
		if c.T < from || c.T >= upTo || c.H <= 0 {
			continue
		}
		pts := pathOf(c, step)
		for i, pt := range pts {
			px := decimal.NewFromFloat(pt.px)
			e.catchupResting(coin, px, c.T, pt.t)
			e.OnMark(coin, px, pt.t)
			e.catchupTriggered(coin, px, i == 0, pt.t)
		}
		e.st.Synced[coin] = max(e.st.Synced[coin], min(c.T+step, upTo))
	}
	e.sweep()
	return e.st.Stats.Fills - before
}

type pathPt struct {
	px float64
	t  int64
}

// pathOf is the bar path O -> nearer extreme -> farther extreme -> C.
func pathOf(c domain.Candle, step int64) []pathPt {
	hiFirst := c.H-c.O <= c.O-c.L
	a, b := c.L, c.H
	if hiFirst {
		a, b = c.H, c.L
	}
	q := step / 4
	return []pathPt{{c.O, c.T}, {a, c.T + q}, {b, c.T + 2*q}, {c.C, c.T + step - 1}}
}

func (e *Engine) catchupResting(coin string, px decimal.Decimal, candleT, at int64) {
	for _, o := range e.restingByPriority(coin) {
		if o.Rested > candleT {
			continue // rested inside this candle: its path may predate the order
		}
		through := o.Side == domain.Buy && px.LessThan(o.Px) || o.Side == domain.Sell && px.GreaterThan(o.Px)
		if !through {
			continue
		}
		sz := e.reduceCap(o, o.Sz)
		if sz.IsZero() {
			e.finish(o, StatusCanceled, "position closed", at)
			continue
		}
		f := e.applyFill(o, o.Px, sz, true, at, "catchup")
		f.QueueWaitMs = at - o.Rested
		o.Queue = decimal.Zero
		e.afterFill(o, at)
	}
}

// catchupTriggered fills triggers that fired on the path (no book to walk).
func (e *Engine) catchupTriggered(coin string, px decimal.Decimal, gap bool, at int64) {
	for _, o := range e.st.Orders {
		if o.Coin != coin || o.Status != StatusPending || !o.Triggered || o.Trigger == nil {
			continue
		}
		fillPx := o.Trigger.Px
		if gap { // the candle opened past the trigger
			fillPx = px
		}
		sz := e.reduceCap(o, o.Sz)
		if sz.Sign() > 0 {
			o.ArrivalMid = px
			e.applyFill(o, fillPx, sz, false, at, "catchup")
		}
		if o.Sz.Sign() <= 0 {
			e.finish(o, StatusFilled, "", at)
		} else {
			e.finish(o, StatusCanceled, "", at)
		}
	}
}

// CatchUpFunding settles funding prints (oldest first) for coin. priceAt
// supplies the oracle price at a time (zero = use the current mark).
func (e *Engine) CatchUpFunding(coin string, pts []domain.FundingPoint, priceAt func(t int64) decimal.Decimal) int {
	n := 0
	for _, p := range pts {
		px := decimal.Zero
		if priceAt != nil {
			px = priceAt(p.Time)
		}
		if px.Sign() <= 0 {
			px = e.st.Marks[coin]
		}
		before := len(e.st.Funding)
		e.OnFunding(coin, p.Time, decimal.NewFromFloat(p.Rate), px)
		if len(e.st.Funding) != before {
			n++
		}
	}
	return n
}

// FundingFrom is the earliest funding hour still to settle for coin's
// position (0 when there is no position).
func (e *Engine) FundingFrom(coin string) int64 {
	p, ok := e.st.Positions[coin]
	if !ok {
		return 0
	}
	return max(p.FundedTo, p.Opened) + 1
}

// SyncedAt is the time coin's events were last applied up to.
func (e *Engine) SyncedAt(coin string) int64 { return e.st.Synced[coin] }

// MarkSynced records that coin is current up to at (a live book or a
// catch-up that found nothing to replay).
func (e *Engine) MarkSynced(coin string, at int64) {
	if at > e.st.Synced[coin] {
		e.st.Synced[coin] = at
	}
}
