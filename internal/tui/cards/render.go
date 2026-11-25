package cards

import (
	"math"
	"strings"

	"github.com/maxmoneycash/hlp/internal/chart"
)

// Chart entry points, indirected so card-layout golden tests stay stable
// while the chart renderer evolves on its own golden tests. Production code
// never reassigns these.
var (
	candlesFn = chart.Candles
	lineFn    = chart.LineChart
	sparkFn   = chart.Sparkline
	depthFn   = chart.Depth
	heatmapFn = chart.Heatmap
	barsFn    = chart.Bars
)

// spark returns a sparkline exactly width cells wide (padding or trimming
// whatever the renderer returns).
func spark(vals []float64, width int) string {
	if width <= 0 {
		return ""
	}
	clean := make([]float64, 0, len(vals))
	for _, v := range vals {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			clean = append(clean, v)
		}
	}
	if len(clean) == 0 {
		return strings.Repeat(" ", width)
	}
	s := []rune(sparkFn(clean, width))
	if len(s) > width {
		s = s[len(s)-width:]
	}
	if len(s) < width {
		return strings.Repeat(" ", width-len(s)) + string(s)
	}
	return string(s)
}

// meter draws a left-anchored eighth-block bar for frac in [0,1].
func meter(frac float64, width int) string {
	if width <= 0 || math.IsNaN(frac) {
		return ""
	}
	frac = math.Max(0, math.Min(1, frac))
	eighths := int(math.Round(frac * float64(width*8)))
	full, rem := eighths/8, eighths%8
	s := strings.Repeat("█", full)
	if rem > 0 {
		s += string([]rune("▏▎▍▌▋▊▉")[rem-1])
	}
	return s
}

// lowSpark compresses an eighth-block sparkline into ▁..▅, so stacked table
// rows keep a visible gap instead of forming a solid wall.
func lowSpark(s string) string {
	const levels = "▁▂▃▄▅▆▇█"
	low := []rune("▁▂▃▄▅")
	lv := []rune(levels)
	out := []rune(s)
	for i, r := range out {
		for k, l := range lv {
			if r == l {
				out[i] = low[int(math.Round(float64(k)*4/7))]
				break
			}
		}
	}
	return string(out)
}

// lineSpark draws vals as a thin 1-row braille line exactly width cells
// wide: a trend that reads as a line, not a wall of filled blocks, so the
// row's numbers stay the heaviest thing in it.
func lineSpark(vals []float64, width int) string {
	return lineRows(vals, width, 1)[0]
}

// lineRows draws vals as a thin braille line width cells wide and rows
// tall (2 samples per cell, 4 dot rows per row). Each dot column is the
// mean of its bucket, and vertical jumps are joined so the line never
// breaks.
func lineRows(vals []float64, width, rows int) []string {
	rows = max(rows, 1)
	out := make([]string, rows)
	if width <= 0 {
		return out
	}
	clean := make([]float64, 0, len(vals))
	for _, v := range vals {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			clean = append(clean, v)
		}
	}
	if len(clean) < 2 {
		for i := range out {
			out[i] = strings.Repeat(" ", width)
		}
		return out
	}
	lo, hi := clean[0], clean[0]
	for _, v := range clean {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	n, dots := 2*width, 4*rows
	ys := make([]int, n)
	for x := 0; x < n; x++ {
		a := x * len(clean) / n
		b := min(max((x+1)*len(clean)/n, a+1), len(clean))
		sum := 0.0
		for _, v := range clean[a:b] {
			sum += v
		}
		v := sum / float64(b-a)
		y := float64(dots-1) / 2
		if hi > lo {
			y = (hi - v) / (hi - lo) * float64(dots-1) // 0 = top dot row
		}
		ys[x] = int(math.Round(y))
	}
	bits := [2][4]rune{{0x01, 0x02, 0x04, 0x40}, {0x08, 0x10, 0x20, 0x80}}
	grid := make([][]rune, rows)
	for r := range grid {
		grid[r] = make([]rune, width)
	}
	for x := 0; x < n; x++ {
		y0, y1 := ys[x], ys[x]
		if x > 0 { // join to the previous sample
			if prev := ys[x-1]; prev < y0 {
				y0 = prev + 1
			} else if prev > y1 {
				y1 = prev - 1
			}
			y0, y1 = min(y0, ys[x]), max(y1, ys[x])
		}
		for y := y0; y <= y1; y++ {
			grid[y/4][x/2] |= bits[x%2][y%4]
		}
	}
	for r := range grid {
		for c := range grid[r] {
			grid[r][c] += 0x2800
		}
		out[r] = string(grid[r])
	}
	return out
}
