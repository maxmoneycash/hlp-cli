package chart

import (
	"math"

	"github.com/maxmoneycash/hlp/internal/theme"
)

// BarsOpts configures BarsWith.
type BarsOpts struct {
	Width  int
	Format func(v float64) string // value text; nil = CompactNum with a sign
}

// Bars renders a horizontal bar list (exposure by coin, leaderboard PnL).
// Values may be negative (drawn left of a center axis).
func Bars(labels []string, values []float64, width int, th theme.Theme) *Grid {
	return BarsWith(labels, values, BarsOpts{Width: width}, th)
}

// BarsWith is Bars with a custom value format (percent, USD, ...).
//
// Layout per row: label, value, then the bar region. With only non-negative
// values bars grow right from the region's left edge; with negatives a
// center axis splits the region in proportion to the largest value on each
// side, so the zero line sits where the data puts it. Bar ends have
// eighth-cell precision on both sides (left-growing ends use SGR 7 on a
// left partial block, the same trick the candles use for upper partials).
func BarsWith(labels []string, values []float64, o BarsOpts, th theme.Theme) *Grid {
	width := max(o.Width, 1)
	n := min(len(labels), len(values))
	g := NewGrid(width, max(n, 1))
	if n == 0 {
		return g
	}
	format := o.Format
	if format == nil {
		format = func(v float64) string {
			s := CompactNum(v)
			if v > 0 {
				s = "+" + s
			}
			return s
		}
	}
	labelW, valW := 0, 0
	maxPos, maxNeg := 0.0, 0.0
	for i := 0; i < n; i++ {
		labelW = max(labelW, len([]rune(labels[i])))
		valW = max(valW, len([]rune(format(values[i]))))
		v := values[i]
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		maxPos, maxNeg = math.Max(maxPos, v), math.Max(maxNeg, -v)
	}
	labelW = min(labelW, max(width/3, 4))
	xV := labelW + 2
	xB := xV + valW + 2
	barW := width - xB - 1
	for i := 0; i < n; i++ {
		lbl := []rune(labels[i])
		if len(lbl) > labelW {
			lbl = append(lbl[:labelW-1], '…')
		}
		g.Text(0, i, string(lbl), th.Text, LLabel)
		vs := format(values[i])
		g.Text(xV+valW-len([]rune(vs)), i, vs, th.AxisText, LLabel)
	}
	if barW < 3 || maxPos+maxNeg == 0 {
		return g
	}
	axis := xB - 1 // no negatives: bars start at the region's left edge
	posW := barW
	if maxNeg > 0 {
		negW := int(math.Round(float64(barW-1) * maxNeg / (maxPos + maxNeg)))
		axis = xB + negW
		posW = barW - negW - 1
		for y := 0; y < n; y++ {
			g.Set(axis, y, '│', th.AxisLine, LAxis)
		}
	}
	scale := math.Max(maxPos, maxNeg)
	if maxNeg > 0 {
		// Both sides share one scale: units per cell are equal left and right.
		scale = (maxPos + maxNeg) / float64(barW-1)
	} else {
		scale = maxPos / float64(posW)
	}
	for i := 0; i < n; i++ {
		v := values[i]
		if math.IsNaN(v) || math.IsInf(v, 0) || v == 0 {
			continue
		}
		u := int(math.Round(math.Abs(v) / scale * 8))
		if u == 0 {
			u = 1
		}
		if v > 0 {
			for x := axis + 1; u > 0 && x < width; x++ {
				f := min(u, 8)
				g.Set(x, i, leftBlocks[f], th.Up, LCandle)
				u -= f
			}
			continue
		}
		for x := axis - 1; u > 0 && x >= xB; x-- {
			f := min(u, 8)
			ch, rev := rightBlock(f)
			if c := g.Set(x, i, ch, th.Down, LCandle); c != nil {
				c.Reverse = rev
			}
			u -= f
		}
	}
	return g
}

// rightBlock returns a glyph filling the right k/8 of a cell. Unicode has
// only ▐ (4/8) and ▕ (1/8); the rest are an SGR 7 left block of 8-k.
func rightBlock(k int) (rune, bool) {
	switch k {
	case 8:
		return '█', false
	case 4:
		return '▐', false
	case 1:
		return '▕', false
	}
	return leftBlocks[8-k], true
}
