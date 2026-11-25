package chart

import (
	"math"
	"strconv"
	"strings"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/theme"
)

// DepthOpts renders an order book.
type DepthOpts struct {
	Width, Height int
	Levels        int  // per side, default fits Height
	Ladder        bool // true: price ladder with size bars; false: cumulative depth curve
	SzUSD         bool // bars by USD notional instead of base size
	Decimals      int  // price decimals (-1 = infer from szDecimals rule)
	// SzDecimals is the asset's size precision (Hyperliquid szDecimals). It
	// caps the size column's decimals; a level that would still print as
	// zero shows its exact size instead. 0 = unknown.
	SzDecimals int
	// Compact is the rush transcript ladder (docs/DESIGN.md 5.2 Book):
	// PRICE and SIZE columns only (the running total stays as the wash),
	// UPPERCASE labels, a spread row of `SPREAD 0.01%` without the mid,
	// and bars of 40 cells at most.
	Compact bool
}

// Depth renders a book: ladder (asks over bids, spread row, horizontal
// eighth-block size bars) or a two-sided cumulative depth chart.
func Depth(b domain.Book, o DepthOpts, th theme.Theme) *Grid {
	w, h := max(o.Width, 1), max(o.Height, 1)
	if len(b.Bids) == 0 && len(b.Asks) == 0 {
		g := NewGrid(w, h)
		g.Text(max((w-8)/2, 0), h/2, "no book", th.Dim, LLabel)
		return g
	}
	dec := o.Decimals
	if dec < 0 {
		dec = bookDecimals(b)
	}
	if o.Ladder {
		return depthLadder(b, o, w, h, dec, th)
	}
	return depthCurve(b, o, w, h, dec, th)
}

// bookDecimals infers price decimals from the mid (5 significant figures)
// and the spacing between levels.
func bookDecimals(b domain.Book) int {
	ref := b.Mid()
	if ref == 0 {
		if len(b.Bids) > 0 {
			ref = b.Bids[0].Px
		} else {
			ref = b.Asks[0].Px
		}
	}
	step := math.Inf(1)
	for _, side := range [][]domain.Level{b.Bids, b.Asks} {
		for i := 1; i < len(side); i++ {
			if d := math.Abs(side[i].Px - side[i-1].Px); d > 0 {
				step = math.Min(step, d)
			}
		}
	}
	sd := 0
	if !math.IsInf(step, 0) {
		sd = StepDecimals(step)
	}
	return PriceDecimals(ref, min(sd, 8))
}

// sizeDecimals picks fixed size decimals so a column of sizes aligns. The
// largest level sets it, so a thin level beside a wall may round to zero;
// levelSize catches that.
func sizeDecimals(maxSz float64) int {
	switch {
	case maxSz >= 1000:
		return 0
	case maxSz >= 10:
		return 2
	case maxSz >= 0.01:
		return 4
	}
	return 6
}

// levelSize formats a base size at the column's decimals (capped at the
// asset's szDecimals when known). A non-zero size that rounds to zero, a
// 0.0002 BTC level under a 26 BTC wall, prints at its own precision: the
// exact szDecimals when known, else three significant figures.
func levelSize(sz float64, sdec, szDecimals int) string {
	dec := sdec
	if szDecimals > 0 {
		dec = min(dec, szDecimals)
	}
	s := FormatPrice(sz, dec)
	if sz <= 0 || strings.Trim(s, "0.,") != "" {
		return s
	}
	if szDecimals > 0 {
		return FormatPrice(sz, szDecimals)
	}
	return sig3(sz)
}

func levelQty(l domain.Level, usd bool) float64 {
	if usd {
		return l.Px * l.Sz
	}
	return l.Sz
}

// depthLadder draws the classic book: asks (farthest first) over a spread
// row over bids. Each row has price, size and cumulative total, then a bar
// region where the level's size is a solid eighth-block bar and the running
// total is a soft wash behind it, so walls and thin levels read at a glance.
func depthLadder(b domain.Book, o DepthOpts, w, h, dec int, th theme.Theme) *Grid {
	g := NewGrid(w, h)
	per := o.Levels
	if per <= 0 {
		per = (h - 2) / 2
	}
	per = max(min(per, (h-2)/2), 1)
	asks := b.Asks[:min(per, len(b.Asks))]
	bids := b.Bids[:min(per, len(b.Bids))]

	maxQ, maxCum, maxSz := 0.0, 0.0, 0.0
	cum := func(side []domain.Level) []float64 {
		out := make([]float64, len(side))
		s := 0.0
		for i, l := range side {
			q := levelQty(l, o.SzUSD)
			s += q
			out[i] = s
			maxQ = math.Max(maxQ, q)
			maxSz = math.Max(maxSz, l.Sz)
		}
		maxCum = math.Max(maxCum, s)
		return out
	}
	askCum, bidCum := cum(asks), cum(bids)
	sdec := sizeDecimals(maxSz)
	fmtSz := func(l domain.Level) string {
		if o.SzUSD {
			return "$" + CompactNum(l.Px*l.Sz)
		}
		return levelSize(l.Sz, sdec, o.SzDecimals)
	}
	fmtCum := func(v float64) string {
		if o.SzUSD {
			return "$" + CompactNum(v)
		}
		return CompactNum(v)
	}

	// Column widths.
	lPrice, lSize, lTotal := "Price", "Size", "Total"
	labelInk := th.Dim
	if o.Compact {
		lPrice, lSize, lTotal, labelInk = "PRICE", "SIZE", "TOTAL", th.Muted
	}
	pw, sw, tw, nw := len(lPrice), len(lSize), len(lTotal), len("#")
	hasN := false
	for i, l := range asks {
		pw, sw, tw = max(pw, len(FormatPrice(l.Px, dec))), max(sw, len(fmtSz(l))), max(tw, len(fmtCum(askCum[i])))
		nw, hasN = max(nw, len(strconv.Itoa(l.N))), hasN || l.N > 0
	}
	for i, l := range bids {
		pw, sw, tw = max(pw, len(FormatPrice(l.Px, dec))), max(sw, len(fmtSz(l))), max(tw, len(fmtCum(bidCum[i])))
		nw, hasN = max(nw, len(strconv.Itoa(l.N))), hasN || l.N > 0
	}
	midStr := ""
	if mid := b.Mid(); mid > 0 {
		midStr = FormatPrice(mid, dec+1)
		pw = max(pw, len(midStr))
	}
	x0 := 1
	xS := x0 + pw + 2
	xN := xS + sw + 2 // order count, only when the bars keep room
	xT := xN
	xB := xT + tw + 2
	showTotal, showN := true, false
	switch {
	case o.Compact || w-xB < 8: // compact or narrow: drop the total column first
		showTotal = false
		xB = xT
	case w-xB >= 14 && hasN:
		showN = true
		xT += nw + 2
		xB += nw + 2
	}
	barW := w - xB - 1
	if o.Compact {
		barW = min(barW, 40)
	}
	if barW < 4 {
		barW = 0
	}

	rjust := func(s string, n int) string {
		if len(s) >= n {
			return s
		}
		return strings.Repeat(" ", n-len(s)) + s
	}
	g.Text(x0, 0, rjust(lPrice, pw), labelInk, LLabel)
	g.Text(xS, 0, rjust(lSize, sw), labelInk, LLabel)
	if showN {
		g.Text(xN, 0, rjust("#", nw), labelInk, LLabel)
	}
	if showTotal {
		g.Text(xT, 0, rjust(lTotal, tw), labelInk, LLabel)
	}

	row := func(y int, l domain.Level, total float64, col theme.Color) {
		g.Text(x0, y, rjust(FormatPrice(l.Px, dec), pw), col, LLabel)
		g.Text(xS, y, rjust(fmtSz(l), sw), th.Text, LLabel)
		if showN {
			g.Text(xN, y, rjust(strconv.Itoa(l.N), nw), th.Dim, LLabel)
		}
		if showTotal {
			g.Text(xT, y, rjust(fmtCum(total), tw), th.AxisText, LLabel)
		}
		if barW == 0 || maxQ <= 0 {
			return
		}
		if !o.Compact {
			// the cumulative depth as a faint wash behind the size bar;
			// a card has no TOTAL column to explain it, so it reads as a
			// second, darker bar there and is left out
			wash := bgTint(th, col, 0.14)
			cw := int(math.Round(total / maxCum * float64(barW)))
			for x := 0; x < cw; x++ {
				g.TintBG(xB+x, y, wash)
			}
		}
		u := int(math.Round(levelQty(l, o.SzUSD) / maxQ * float64(barW*8)))
		if u == 0 && l.Sz > 0 {
			u = 1
		}
		bar := tint(th, col, 0.7)
		if o.Compact { // asks and bids at one weight (25%), under the prices
			bar = tint(th, col, 0.25)
		}
		for x := 0; x < barW && u > 0; x++ {
			f := min(u, 8)
			g.Set(xB+x, y, leftBlocks[f], bar, LCandle)
			u -= f
		}
	}

	// Asks: farthest at the top, best ask just above the spread row.
	y := 1
	for i := per - 1; i >= 0; i-- {
		if i < len(asks) {
			row(y, asks[i], askCum[i], th.Down)
		}
		y++
	}
	// Spread row: mid price, then the spread in absolute and relative terms.
	if midStr != "" && o.Compact && len(b.Asks) > 0 && len(b.Bids) > 0 {
		// Compact: the mid is the card's focal (bold ink in the price
		// column), the spread a muted label after it.
		mid := b.Mid()
		x := x0 + g.Text(x0, y, rjust(midStr, pw), th.Text, LLabel)
		for xx := x0; xx < x; xx++ {
			g.At(xx, y).Bold = true
		}
		pct := strings.TrimPrefix(FormatPercent((b.Asks[0].Px-b.Bids[0].Px)/mid, 2), "+")
		x = xS + g.Text(xS, y, "SPREAD", th.Muted, LLabel)
		g.Text(x+2, y, pct, th.Text, LLabel)
	} else if midStr != "" && !o.Compact {
		mid := b.Mid()
		x := x0 + g.Text(x0, y, rjust(midStr, pw), th.Text, LLabel)
		for xx := x0; xx < x; xx++ {
			g.At(xx, y).Bold = true
		}
		spread := b.Asks[0].Px - b.Bids[0].Px
		pct := strings.TrimPrefix(FormatPercent(spread/mid, 3), "+")
		g.Text(xS, y, "spread "+FormatPrice(spread, dec)+"  "+pct, th.Dim, LLabel)
	} else if mid := b.Mid(); mid > 0 && len(b.Asks) > 0 && len(b.Bids) > 0 {
		// Compact: the header already shows the mid; say the spread once.
		pct := strings.TrimPrefix(FormatPercent((b.Asks[0].Px-b.Bids[0].Px)/mid, 2), "+")
		x := x0 + g.Text(x0, y, "SPREAD", th.Muted, LLabel)
		g.Text(x+2, y, pct, th.Text, LLabel)
	}
	y++
	for i := 0; i < per; i++ {
		if i < len(bids) {
			row(y, bids[i], bidCum[i], th.Up)
		}
		y++
	}
	return g
}

// depthCurve draws the two-sided cumulative depth chart: bids stepping down
// to the mid from the left, asks stepping up from the mid to the right,
// filled with eighth-block columns.
func depthCurve(b domain.Book, o DepthOpts, w, h, dec int, th theme.Theme) *Grid {
	g := NewGrid(w, h)
	plotH := h - 2
	if plotH < 2 || w < 12 {
		g.Text(0, 0, "book too small", th.Dim, LLabel)
		return g
	}
	mid := b.Mid()
	if mid == 0 {
		if len(b.Bids) > 0 {
			mid = b.Bids[0].Px
		} else {
			mid = b.Asks[0].Px
		}
	}
	reach := 0.0
	if n := len(b.Bids); n > 0 {
		reach = math.Max(reach, mid-b.Bids[n-1].Px)
	}
	if n := len(b.Asks); n > 0 {
		reach = math.Max(reach, b.Asks[n-1].Px-mid)
	}
	if reach <= 0 {
		reach = math.Max(mid*0.001, 1e-9)
	}
	lo, hi := mid-reach, mid+reach
	depthAt := func(p float64) (float64, bool) {
		s := 0.0
		if p < mid {
			for _, l := range b.Bids {
				if l.Px >= p {
					s += levelQty(l, o.SzUSD)
				}
			}
			return s, true
		}
		for _, l := range b.Asks {
			if l.Px <= p {
				s += levelQty(l, o.SzUSD)
			}
		}
		return s, false
	}
	vals := make([]float64, w)
	bid := make([]bool, w)
	maxV := 0.0
	for x := 0; x < w; x++ {
		p := lo + (float64(x)+0.5)/float64(w)*(hi-lo)
		vals[x], bid[x] = depthAt(p)
		maxV = math.Max(maxV, vals[x])
	}
	if maxV <= 0 {
		maxV = 1
	}
	for x := 0; x < w; x++ {
		col := th.Down
		if bid[x] {
			col = th.Up
		}
		fill := tint(th, col, 0.5)
		u := int(math.Round(vals[x] / maxV * float64(plotH*8)))
		for r := 0; r < plotH && u > 0; r++ {
			f := min(u, 8)
			g.Set(x, plotH-r, lowerBlocks[f], fill, LCandle)
			u -= f
		}
	}
	// Mid marker and labels.
	mx := w / 2
	for y := 1; y <= plotH; y++ {
		g.Set(mx, y, '┊', th.Dim, LGrid)
	}
	maxL := CompactNum(maxV)
	if o.SzUSD {
		maxL = "$" + maxL
	}
	g.Text(1, 0, maxL, th.Dim, LLabel)
	ml := FormatPrice(mid, dec)
	g.Text(max(mx-len(ml)/2, 0), 0, ml, th.Text, LLabel)
	ll, rl := FormatPrice(lo, dec), FormatPrice(hi, dec)
	g.Text(0, h-1, ll, th.AxisText, LLabel)
	g.Text(max(w-len(rl), 0), h-1, rl, th.AxisText, LLabel)
	return g
}
