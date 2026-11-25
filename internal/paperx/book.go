package paperx

import (
	"math"

	"github.com/shopspring/decimal"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// Level is one book level in exact decimals.
type Level struct {
	Px decimal.Decimal `json:"px"`
	Sz decimal.Decimal `json:"sz"`
}

// Book is an L2 snapshot as the engine sees it. At is the local receive
// time (ms), which is what latency is measured against.
type Book struct {
	Coin string  `json:"coin"`
	At   int64   `json:"at"`
	Bids []Level `json:"bids"` // descending
	Asks []Level `json:"asks"` // ascending
}

// BookFrom converts a domain book. Hyperliquid prices and sizes are short
// decimal strings, so the shortest float formatting recovers them exactly.
func BookFrom(b domain.Book, at int64) Book {
	conv := func(ls []domain.Level) []Level {
		out := make([]Level, 0, len(ls))
		for _, l := range ls {
			if l.Sz <= 0 || l.Px <= 0 {
				continue
			}
			out = append(out, Level{Px: decimal.NewFromFloat(l.Px), Sz: decimal.NewFromFloat(l.Sz)})
		}
		return out
	}
	return Book{Coin: b.Coin, At: at, Bids: conv(b.Bids), Asks: conv(b.Asks)}
}

// Mid is the midpoint, or zero with a side empty.
func (b *Book) Mid() decimal.Decimal {
	if b == nil || len(b.Bids) == 0 || len(b.Asks) == 0 {
		return decimal.Zero
	}
	return b.Bids[0].Px.Add(b.Asks[0].Px).Div(decimal.NewFromInt(2))
}

func (b *Book) side(ask bool) []Level {
	if ask {
		return b.Asks
	}
	return b.Bids
}

// size is the real size at px on one side (zero when absent).
func (b *Book) size(ask bool, px decimal.Decimal) decimal.Decimal {
	for _, l := range b.side(ask) {
		if l.Px.Equal(px) {
			return l.Sz
		}
	}
	return decimal.Zero
}

// covers reports whether px lies inside the visible range of a side, so
// a missing level there really means an empty level (not one beyond the
// snapshot's depth).
func (b *Book) covers(ask bool, px decimal.Decimal) bool {
	ls := b.side(ask)
	if len(ls) == 0 {
		return false
	}
	best, worst := ls[0].Px, ls[len(ls)-1].Px
	if ask {
		return !px.LessThan(best) && !px.GreaterThan(worst)
	}
	return !px.GreaterThan(best) && !px.LessThan(worst)
}

// ---- self-impact overlay ----

// overlayLeft is how much of a past take is still missing from a level
// whose real size is now real: the take decays with the half-life, any
// growth of the real level past what it showed after the take is refill,
// and a level that vanished takes the overlay with it.
func (e *Engine) overlayLeft(c Consumed, real decimal.Decimal, at int64) decimal.Decimal {
	if real.Sign() <= 0 {
		return decimal.Zero
	}
	left := c.Sz
	if dt := at - c.At; dt > 0 {
		f := math.Pow(0.5, float64(dt)/float64(e.cfg.ImpactHalfLife.Milliseconds()))
		left = left.Mul(decimal.NewFromFloat(f))
	}
	if refill := real.Sub(c.Seen); refill.Sign() > 0 {
		left = left.Sub(refill)
	}
	if left.Sign() < 0 {
		return decimal.Zero
	}
	return left
}

func (e *Engine) overlayIndex(coin string, ask bool, px decimal.Decimal) int {
	for i, c := range e.st.Overlay {
		if c.Coin == coin && c.Ask == ask && c.Px.Equal(px) {
			return i
		}
	}
	return -1
}

// effective is the size this account can still take at a level.
func (e *Engine) effective(coin string, ask bool, px, real decimal.Decimal, at int64) decimal.Decimal {
	i := e.overlayIndex(coin, ask, px)
	if i < 0 {
		return real
	}
	eff := real.Sub(e.overlayLeft(e.st.Overlay[i], real, at))
	if eff.Sign() < 0 {
		return decimal.Zero
	}
	return eff
}

// consume records a take of sz from a level whose real size is real.
func (e *Engine) consume(coin string, ask bool, px, sz, real decimal.Decimal, at int64) {
	if i := e.overlayIndex(coin, ask, px); i >= 0 {
		c := &e.st.Overlay[i]
		c.Sz = e.overlayLeft(*c, real, at).Add(sz)
		c.At, c.Seen = at, real
		return
	}
	e.st.Overlay = append(e.st.Overlay, Consumed{Coin: coin, Ask: ask, Px: px, Sz: sz, At: at, Seen: real})
}

// pruneOverlay drops takes that the book has fully absorbed.
func (e *Engine) pruneOverlay(b *Book) {
	lot := decimal.New(1, -12)
	if s, ok := e.st.Specs[b.Coin]; ok {
		lot = s.lot()
	}
	keep := e.st.Overlay[:0]
	for _, c := range e.st.Overlay {
		if c.Coin == b.Coin && b.covers(c.Ask, c.Px) {
			left := e.overlayLeft(c, b.size(c.Ask, c.Px), b.At)
			if left.LessThan(lot) {
				continue
			}
		}
		keep = append(keep, c)
	}
	e.st.Overlay = keep
}

// ImpactLeft reports the liquidity still missing per level for coin (for
// the report and tests).
func (e *Engine) ImpactLeft(coin string) []Level {
	b := e.books[coin]
	var out []Level
	for _, c := range e.st.Overlay {
		if c.Coin != coin {
			continue
		}
		real := c.Seen
		at := c.At
		if b != nil {
			real, at = b.size(c.Ask, c.Px), b.At
		}
		if l := e.overlayLeft(c, real, at); l.Sign() > 0 {
			out = append(out, Level{Px: c.Px, Sz: l})
		}
	}
	return out
}
