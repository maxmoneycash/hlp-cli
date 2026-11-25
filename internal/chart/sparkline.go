package chart

import (
	"math"
	"strings"
)

// Sparkline renders values as width eighth-block glyphs (▁▂▃▄▅▆▇█), no color.
// Resamples to width (last value always kept). Empty input -> spaces.
//
// Longer input is bucketed (each glyph shows its bucket's last value, so the
// final glyph is always the latest value); shorter input is stretched by
// linear interpolation. The minimum maps to ▁ and the maximum to █; a flat
// series is a row of ▄. NaN values become spaces.
func Sparkline(vals []float64, width int) string {
	if width <= 0 {
		return ""
	}
	if len(vals) == 0 {
		return strings.Repeat(" ", width)
	}
	rs := resample(vals, width)
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range rs {
		if !math.IsNaN(v) {
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
	}
	var b strings.Builder
	b.Grow(width * 3)
	for _, v := range rs {
		switch {
		case math.IsNaN(v) || math.IsInf(lo, 0):
			b.WriteByte(' ')
		case hi == lo:
			b.WriteRune('▄')
		default:
			b.WriteRune(lowerBlocks[1+int(math.Round((v-lo)/(hi-lo)*7))])
		}
	}
	return b.String()
}

// resample maps vals onto exactly n points (see Sparkline).
func resample(vals []float64, n int) []float64 {
	out := make([]float64, n)
	m := len(vals)
	switch {
	case m == n:
		copy(out, vals)
	case m > n:
		for i := 0; i < n; i++ {
			// The bucket ending at index ((i+1)*m)/n - 1; the last bucket ends at m-1.
			out[i] = vals[(i+1)*m/n-1]
		}
	case m == 1:
		for i := range out {
			out[i] = vals[0]
		}
	default:
		for i := 0; i < n; i++ {
			f := float64(i) * float64(m-1) / float64(n-1)
			j := int(f)
			if j >= m-1 {
				out[i] = vals[m-1]
				continue
			}
			a, b := vals[j], vals[j+1]
			out[i] = a + (b-a)*(f-float64(j))
		}
	}
	return out
}
