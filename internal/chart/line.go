package chart

import (
	"math"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/theme"
)

// Named is one series on a line chart.
type Named struct {
	Name   string
	Series domain.Series
	Color  int // 0 = theme.Accent, 1..3 = theme.MA[0..2]
	// Col, when set, overrides Color with an explicit token, so a series
	// keeps one identity color on every chart (BTC is always the same hue).
	Col theme.Color
}

// LineOpts renders equity/NAV/PnL/drawdown curves (vault tearsheets,
// backtests, arena leaderboards).
type LineOpts struct {
	Width, Height int
	Series        []Named
	Area          bool // tinted fill under the first series + eighth-block edge
	// AreaToBaseline fills between the first series and Baseline instead
	// of down to the plot floor: an underwater drawdown, or NAV around 0%.
	AreaToBaseline bool
	AreaMix        float64 // area wash strength (0 = 0.16)
	Percent        bool    // y-axis as % change from the first point
	// PercentAxis labels the axis and the end tag as percents of the
	// values themselves (0.1 -> 10%), without rebasing: rates like APR.
	PercentAxis bool
	Baseline    float64 // horizontal reference line (e.g. 1.0); NaN = none
	// TagCol overrides the end-value tag's color (zero = the subject's):
	// a drawdown at 0% is not an alarm, so its tag is ink.
	TagCol theme.Color
	// LegendOthers names only the context series in the legend: the card
	// header already names the subject (a backtest's strategy).
	LegendOthers bool
	Title        string
	Location     *time.Location
}

// LineChart renders braille polylines with a right value axis and a bottom
// time axis. The first series is the subject: it is drawn last (on top),
// gets the optional area fill and the end-value tag; the others are context.
// With Percent every series is rebased to its own first point, so a vault
// and its benchmarks compare from the same start.
func LineChart(o LineOpts, th theme.Theme) *Grid {
	w, h := max(o.Width, 1), max(o.Height, 1)
	g := NewGrid(w, h)
	series := make([]Named, 0, len(o.Series))
	for _, s := range o.Series {
		if len(s.Series) > 0 {
			series = append(series, s)
		}
	}
	if len(series) == 0 || w < 16 || h < 4 {
		msg := "no data"
		if o.Title != "" {
			msg = o.Title + "  " + msg
		}
		g.Text(max((w-len(msg))/2, 0), h/2, msg, th.Dim, LLabel)
		return g
	}

	// Values (rebased in Percent mode) and the common time and value range.
	vals := make([][]float64, len(series))
	t0, t1 := int64(math.MaxInt64), int64(math.MinInt64)
	lo, hi := math.Inf(1), math.Inf(-1)
	for k, s := range series {
		vals[k] = make([]float64, len(s.Series))
		base := math.NaN()
		for i, p := range s.Series {
			v := p.V
			if o.Percent {
				if math.IsNaN(base) && !math.IsNaN(v) && v != 0 {
					base = v
				}
				v = v/base - 1
			}
			vals[k][i] = v
			if !math.IsNaN(v) && !math.IsInf(v, 0) {
				lo, hi = math.Min(lo, v), math.Max(hi, v)
			}
			t0, t1 = min(t0, p.T), max(t1, p.T)
		}
	}
	if math.IsInf(lo, 0) {
		g.Text(max((w-7)/2, 0), h/2, "no data", th.Dim, LLabel)
		return g
	}
	baseline := o.Baseline
	if o.Percent && baseline == 1 {
		baseline = 0 // a 1.0 NAV baseline is 0% once rebased
	}
	hasBase := !math.IsNaN(baseline)
	pad := (hi - lo) * 0.08
	if pad == 0 {
		pad = math.Max(math.Abs(hi)*0.01, 1e-9)
	}
	lo, hi = lo-pad, hi+pad
	if o.Area && !o.Percent && lo > 0 && lo < (hi-lo)*0.6 {
		lo = 0 // an area chart of a positive quantity reads honestly from zero
	}

	header := 0
	if o.Title != "" || len(series) > 1 {
		header = 1
	}
	plotTop, plotH := header, h-header-2
	axisRow := plotTop + plotH
	if plotH < 2 {
		plotH, axisRow = h-header-1, h-1
	}

	ticks, step := PriceTicks(lo, hi, plotH, 3)
	format := func(v float64) string {
		return FormatPrice(v, PriceDecimals(math.Max(math.Abs(hi), math.Abs(lo)), StepDecimals(step)))
	}
	if o.Percent || o.PercentAxis {
		format = func(v float64) string { return FormatPercent(v, max(StepDecimals(step*100), 0)) }
	}
	last := vals[0][len(vals[0])-1]
	// The end tag is the number a card header shows: percents keep one
	// decimal (+367.9%, −0.9%), never the axis step's rounding (+368%).
	tagText := format
	if o.Percent || o.PercentAxis {
		tagText = func(v float64) string { return FormatPercent(v, 1) }
	}
	axisW := len(tagText(last))
	for _, t := range ticks {
		axisW = max(axisW, len(format(t)))
	}
	axisW += 3
	plotW := w - axisW
	if plotW < 8 {
		plotW, axisW = w, 0
	}

	yOf := func(v float64) float64 { return (hi - v) / (hi - lo) * float64(4*plotH-1) }
	xOf := func(t int64) int {
		if t1 == t0 {
			return 2*plotW - 1
		}
		return int(math.Round(float64(t-t0) / float64(t1-t0) * float64(2*plotW-1)))
	}
	rowOf := func(v float64) int { return plotTop + clampi(int(yOf(v))/4, 0, plotH-1) }

	// Gridlines and baseline.
	for _, t := range ticks {
		y := rowOf(t)
		for x := 0; x < plotW; x++ {
			g.Set(x, y, '┈', th.Grid, LGrid)
		}
	}
	if hasBase && baseline >= lo && baseline <= hi {
		y := rowOf(baseline)
		for x := 0; x < plotW; x++ {
			g.Set(x, y, '┄', th.Dim, LGrid)
		}
	}

	// Context series first, the subject last so it sits on top.
	b := newBraille(plotW, plotH)
	colorOf := func(n Named) theme.Color {
		if n.Col != (theme.Color{}) {
			return n.Col
		}
		if n.Color >= 1 && n.Color <= 3 {
			return th.MA[n.Color-1]
		}
		return th.Accent
	}
	for k := len(series) - 1; k >= 0; k-- {
		b.reset()
		xs := make([]int, len(vals[k]))
		ys := make([]float64, len(vals[k]))
		for i, p := range series[k].Series {
			xs[i] = xOf(p.T)
			v := vals[k][i]
			if math.IsNaN(v) || math.IsInf(v, 0) {
				ys[i] = math.NaN()
				continue
			}
			ys[i] = yOf(v)
		}
		b.polyline(xs, ys)
		col := colorOf(series[k])
		if k > 0 {
			col = tint(th, col, 0.6) // context lines recede: the subject leads
		}
		if k == 0 && o.Area {
			mix := o.AreaMix
			if mix <= 0 {
				mix = 0.16
			}
			baseRow := -1
			if o.AreaToBaseline && hasBase && baseline >= lo && baseline <= hi {
				baseRow = int(yOf(baseline)) / 4
			}
			paintArea(g, b, plotTop, plotH, col, th, mix, baseRow)
		}
		b.flush(g, 0, plotTop, col)
	}

	// Axes.
	if axisW > 0 {
		for y := plotTop; y < axisRow; y++ {
			g.Set(plotW, y, '│', th.AxisLine, LAxis)
		}
		g.Set(plotW, axisRow, '┘', th.AxisLine, LAxis)
	}
	for x := 0; x < plotW; x++ {
		g.Set(x, axisRow, '─', th.AxisLine, LAxis)
	}
	lastY := rowOf(last)
	if axisW > 0 {
		for _, t := range ticks {
			y := rowOf(t)
			if y == lastY {
				continue
			}
			g.Set(plotW, y, '├', th.AxisLine, LAxis)
			g.Text(plotW+2, y, format(t), th.AxisText, LLabel)
		}
		col := colorOf(series[0])
		if o.TagCol.True.Valid {
			col = o.TagCol
		}
		s := " " + tagText(last)
		for len(s) < axisW {
			s += " "
		}
		for i, ch := range s {
			if c := g.Set(plotW+i, lastY, ch, col, LLabel); c != nil {
				c.Reverse, c.Bold = true, true
			}
		}
	}
	if axisRow+1 < h {
		paintLineTimeAxis(g, t0, t1, plotW, axisRow, o.Location, th)
	}

	// Header: title, then a legend when there is more than one series.
	if header == 1 {
		x := 1
		if o.Title != "" {
			x += g.Text(x, 0, o.Title, th.Text, LLabel)
			for xx := 1; xx < x; xx++ {
				g.At(xx, 0).Bold = true
			}
			x += 2
		}
		if len(series) > 1 {
			for k, s := range series {
				if k == 0 && o.LegendOthers {
					continue
				}
				lbl := s.Name
				need := len([]rune(lbl)) + 2
				if x+need > w-1 {
					break
				}
				col := colorOf(s)
				if k > 0 {
					col = tint(th, col, 0.6)
				}
				g.Set(x, 0, '━', col, LLabel)
				// label style: UPPERCASE muted, the swatch carries the hue
				x += 2 + g.Text(x+2, 0, strings.ToUpper(lbl), th.AxisText, LLabel)
				x += 2
			}
		}
	}
	return g
}

// paintArea tints the cells under the subject line: every cell below the
// topmost dot of a column gets a soft wash of the line color. With baseRow
// >= 0 (a plot row) it fills only between the line and that row instead,
// above or below it.
func paintArea(g *Grid, b *braille, plotTop, plotH int, col theme.Color, th theme.Theme, mix float64, baseRow int) {
	wash := bgTint(th, col, mix)
	for cx := 0; cx < b.w; cx++ {
		top := b.topDot(cx)
		if top < 0 {
			continue
		}
		if baseRow >= 0 {
			lineRow := top / 4
			if lineRow == baseRow {
				continue // on the baseline (a 0% drawdown): no wash, no band
			}
			lo, hi := min(lineRow, baseRow), max(lineRow, baseRow)
			for cy := lo; cy <= hi && cy < plotH; cy++ {
				g.TintBG(cx, plotTop+cy, wash)
			}
			continue
		}
		for cy := top / 4; cy < plotH; cy++ {
			if top%4 >= 2 && cy == top/4 {
				continue // the line sits low in this cell: wash starts below it
			}
			g.TintBG(cx, plotTop+cy, wash)
		}
	}
}

// paintLineTimeAxis labels a continuous time axis with wall-clock aligned
// dates (or times for spans under three days).
func paintLineTimeAxis(g *Grid, t0, t1 int64, plotW, axisRow int, loc *time.Location, th theme.Theme) {
	if loc == nil {
		loc = time.UTC
	}
	span := time.Duration(t1-t0) * time.Millisecond
	if span <= 0 || plotW < 10 {
		return
	}
	perCol := span / time.Duration(plotW)
	step := TimeStep(max(perCol, time.Minute), 1, 11)
	if span >= 3*day && step < day {
		step = day
	}
	if span >= 60*day && step < 30*day {
		// one label a month (AUG SEP OCT), never a row of weekly dates
		step = 30 * day
	}
	format := func(t time.Time) string {
		switch {
		case step >= 365*day:
			return t.Format("2006")
		case step >= 30*day:
			if t.Month() == time.January {
				return t.Format("2006")
			}
			return t.Format("Jan")
		case step >= day:
			return t.Format("Jan 2")
		}
		if t.Hour() == 0 && t.Minute() == 0 {
			return t.Format("Jan 2")
		}
		return t.Format("15:04")
	}
	// Walk aligned boundaries from the start.
	start := time.UnixMilli(t0).In(loc)
	var cur time.Time
	switch {
	case step >= 30*day:
		cur = time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, loc)
	case step >= day:
		cur = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
	default:
		// Align in local time: Truncate works on absolute time, so with a
		// non-zero UTC offset a 12h step would land on 04:00/16:00 and no
		// tick would ever be a local boundary (an empty time axis).
		_, off := start.Zone()
		sec := int64(step / time.Second)
		cur = time.Unix(((start.Unix()+int64(off))/sec)*sec-int64(off), 0).In(loc)
	}
	lastEnd := -2
	for i := 0; i < 4000 && cur.UnixMilli() <= t1; i++ {
		next := cur.Add(step)
		if step >= 30*day {
			next = cur.AddDate(0, 1, 0)
		} else if step >= day {
			next = cur.AddDate(0, 0, 1)
		}
		if cur.UnixMilli() >= t0 && IsTimeBoundary(cur, step) {
			x := int(math.Round(float64(cur.UnixMilli()-t0) / float64(t1-t0) * float64(plotW-1)))
			lbl := strings.ToUpper(format(cur)) // label style: AUG, NOV 16
			x0 := x - len(lbl)/2
			if x0 > lastEnd+1 && x0 >= 0 && x0+len(lbl) <= plotW {
				g.Set(x, axisRow, '┬', th.AxisLine, LAxis)
				lastEnd = x0 + g.Text(x0, axisRow+1, lbl, th.AxisText, LLabel) - 1
			}
		}
		cur = next
	}
}
