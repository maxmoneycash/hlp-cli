package chart

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// NiceStep returns the smallest step in {1, 2, 2.5, 5, 10} x 10^k that is >= raw
// (Heckbert's nice numbers, with 2.5 added because traders expect 2.5/25/250
// steps on price ladders). Invalid input returns 1.
func NiceStep(raw float64) float64 {
	if raw <= 0 || math.IsNaN(raw) || math.IsInf(raw, 0) {
		return 1
	}
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if m*mag >= raw*(1-1e-9) {
			return m * mag
		}
	}
	return 10 * mag
}

// finerStep returns the next smaller value in the 1-2-2.5-5 sequence.
func finerStep(step float64) float64 {
	if step <= 0 || math.IsNaN(step) || math.IsInf(step, 0) {
		return 0
	}
	mag := math.Pow(10, math.Floor(math.Log10(step)))
	m := step / mag
	switch {
	case m > 4.99:
		return 2.5 * mag
	case m > 2.49:
		return 2 * mag
	case m > 1.99:
		return mag
	}
	return 5 * mag / 10
}

// StepDecimals is the number of decimals needed to print multiples of step exactly.
func StepDecimals(step float64) int {
	for d := 0; d <= 10; d++ {
		v := step * math.Pow(10, float64(d))
		if math.Abs(v-math.Round(v)) < 1e-6*math.Max(1, v) {
			return d
		}
	}
	return 10
}

// PriceTicks returns tick values within [lo,hi] with roughly one label per
// rowsPerTick rows of a plot rows tall (at least two ticks' worth of range).
func PriceTicks(lo, hi float64, rows, rowsPerTick int) (ticks []float64, step float64) {
	n := float64(rows) / float64(max(rowsPerTick, 1))
	if n < 2 {
		n = 2
	}
	step = NiceStep((hi - lo) / n)
	// NiceStep rounds up, which can leave a short plot with a single label
	// (13k of range over 9 rows -> 10k steps). Take the next finer nice
	// step while it still gives at most n+1.5 labels.
	if finer := finerStep(step); finer > 0 && (hi-lo)/finer <= n+1.5 {
		step = finer
	}
	start := math.Ceil(lo/step) * step
	for i := 0; ; i++ {
		v := start + float64(i)*step // multiply, never accumulate: no drift
		if v > hi+step*1e-9 || i > 1000 {
			break
		}
		ticks = append(ticks, v)
	}
	return ticks, step
}

// LogTicks returns 1-2-5 ticks per decade inside [lo,hi] (both > 0) for
// log-scaled axes, thinned so that at most maxTicks remain.
func LogTicks(lo, hi float64, maxTicks int) []float64 {
	if lo <= 0 || hi <= lo {
		return nil
	}
	var out []float64
	for _, mults := range [][]float64{{1, 2, 5}, {1, 2.5}, {1}} {
		out = out[:0]
		for e := math.Floor(math.Log10(lo)); e <= math.Ceil(math.Log10(hi)); e++ {
			for _, m := range mults {
				v := m * math.Pow(10, e)
				if v >= lo && v <= hi {
					out = append(out, v)
				}
			}
		}
		if len(out) <= maxTicks {
			break
		}
	}
	return out
}

// PriceDecimals picks display decimals the way Hyperliquid quotes prices: 5
// significant figures, at most 6 decimals, and never fewer than the tick
// step needs.
func PriceDecimals(p float64, stepDec int) int {
	p = math.Abs(p)
	if p == 0 || math.IsNaN(p) || math.IsInf(p, 0) {
		return min(stepDec, 6)
	}
	d := 5 - 1 - int(math.Floor(math.Log10(p)))
	d = max(0, min(d, 6))
	return max(d, min(stepDec, 8))
}

// FormatPrice prints v with thousands separators and dec fixed decimals.
func FormatPrice(v float64, dec int) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "-"
	}
	s := strconv.FormatFloat(math.Abs(v), 'f', max(dec, 0), 64)
	intPart, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, frac = s[:i], s[i:]
	}
	var b strings.Builder
	b.Grow(len(s) + len(s)/3 + 1)
	if v < 0 && strings.Trim(s, "0.") != "" {
		b.WriteByte('-')
	}
	for i, r := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String() + frac
}

// CompactNum formats a magnitude as 950, 9.50K, 12.3M, 4.56B, 1.20T with
// three significant figures (volume labels, depth totals, bar values).
func CompactNum(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "-"
	}
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	units := []struct {
		f float64
		s string
	}{{1e12, "T"}, {1e9, "B"}, {1e6, "M"}, {1e3, "K"}}
	for _, u := range units {
		if v >= u.f*0.9995 {
			return sign + sig3(v/u.f) + u.s
		}
	}
	if v >= 100 || v == 0 {
		return sign + strconv.FormatFloat(v, 'f', 0, 64)
	}
	return sign + sig3(v)
}

// sig3 prints v (>= 0, < 1000) with three significant figures.
func sig3(v float64) string {
	switch {
	case v >= 99.95:
		return strconv.FormatFloat(v, 'f', 0, 64)
	case v >= 9.995:
		return strconv.FormatFloat(v, 'f', 1, 64)
	case v >= 0.9995 || v == 0:
		return strconv.FormatFloat(v, 'f', 2, 64)
	}
	d := 2 - int(math.Floor(math.Log10(v)))
	return strconv.FormatFloat(v, 'f', min(d, 8), 64)
}

// FormatPercent prints a fraction as a signed percent: 0.1234 -> "+12.34%".
func FormatPercent(f float64, dec int) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "-"
	}
	s := strconv.FormatFloat(f*100, 'f', dec, 64)
	if strings.Trim(s, "-0.") == "" {
		return strconv.FormatFloat(0, 'f', dec, 64) + "%"
	}
	if f > 0 {
		s = "+" + s
	}
	return s + "%"
}

// timeSteps are the label spacings the time axis may use, smallest first.
var timeSteps = []time.Duration{
	time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 3 * time.Hour, 4 * time.Hour, 6 * time.Hour, 12 * time.Hour,
	day, 2 * day, 7 * day, 14 * day, 30 * day, 90 * day, 365 * day,
}

const day = 24 * time.Hour

// TimeStep picks the smallest wall-clock step that keeps labels at least
// minGapCols apart when each candle (interval long) takes colsPerCandle columns.
func TimeStep(interval time.Duration, colsPerCandle, minGapCols int) time.Duration {
	if interval <= 0 {
		interval = time.Minute
	}
	minCandles := int(math.Ceil(float64(minGapCols) / float64(max(colsPerCandle, 1))))
	need := time.Duration(minCandles) * interval
	for _, s := range timeSteps {
		if s >= need && s >= interval {
			return s
		}
	}
	return timeSteps[len(timeSteps)-1]
}

// IsTimeBoundary reports whether t (in its own location) is aligned to step.
// Steps of a day or more align on midnight (weeks: Mondays; 30 days: the 1st
// of a month; 90 days: quarters; a year: January 1st).
func IsTimeBoundary(t time.Time, step time.Duration) bool {
	midnight := t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0
	switch {
	case step >= 365*day:
		return midnight && t.Day() == 1 && t.Month() == time.January
	case step >= 30*day:
		return midnight && t.Day() == 1 && (step < 90*day || (int(t.Month())-1)%3 == 0)
	case step >= 7*day:
		return midnight && t.Weekday() == time.Monday
	case step >= day:
		return midnight && (t.YearDay()-1)%int(step/day) == 0
	}
	_, off := t.Zone()
	return (t.Unix()+int64(off))%int64(step.Seconds()) == 0
}

// TimeLabel formats a tick: the year in January and month names for monthly
// steps, the date at midnight, the clock otherwise.
func TimeLabel(t time.Time, step time.Duration) string {
	return strings.ToUpper(timeLabel(t, step)) // label style: NOV 16, AUG
}

func timeLabel(t time.Time, step time.Duration) string {
	if step >= 30*day {
		if t.Month() == time.January {
			return t.Format("2006")
		}
		return t.Format("Jan")
	}
	if t.Hour() == 0 && t.Minute() == 0 {
		if t.YearDay() == 1 {
			return t.Format("2006")
		}
		return t.Format("Jan 2")
	}
	return t.Format("15:04")
}

// FormatCountdown prints the time left in a bar: 59s, 14:21, 3:04:05, 2d 03h.
func FormatCountdown(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int64(d / time.Second)
	switch {
	case s >= 86400:
		return strconv.FormatInt(s/86400, 10) + "d " + pad2(s%86400/3600) + "h"
	case s >= 3600:
		return strconv.FormatInt(s/3600, 10) + ":" + pad2(s%3600/60) + ":" + pad2(s%60)
	}
	return pad2(s/60) + ":" + pad2(s%60)
}

func pad2(v int64) string {
	if v < 10 {
		return "0" + strconv.FormatInt(v, 10)
	}
	return strconv.FormatInt(v, 10)
}

// timeMark is one label on the time axis: visible bar k, its time and text.
type timeMark struct {
	k     int
	t     time.Time
	label string
	major bool // a date on a clock axis (tick charts)
}

// timeMarks lists the candidate labels of the visible bars. Clock charts
// label bars that open on a wall-clock step boundary. Tick charts (bars by
// trade count, volume or range) label every few bars, anchored on the bar's
// series index so labels do not crawl as bars arrive, with the first
// trade's time; the painters still drop labels that would collide.
func (r *candleRender) timeMarks(step time.Duration, minGap int) []timeMark {
	var out []timeMark
	if !r.o.TickBars {
		for k := 0; k < r.n; k++ {
			t := time.UnixMilli(r.draw[r.start+k].T).In(r.loc)
			if IsTimeBoundary(t, step) {
				out = append(out, timeMark{k: k, t: t, label: TimeLabel(t, step)})
			}
		}
		return out
	}
	if r.n == 0 {
		return nil
	}
	span := r.draw[r.start+r.n-1].T - r.draw[r.start].T
	layout, w := "15:04", 5
	switch {
	case span < 3600_000:
		layout, w = "15:04:05", 8
	case span >= 3*24*3600_000:
		layout, w = "Jan 2", 6
	}
	every := max((w+minGap+r.stride-1)/r.stride, 1)
	prevDay := -1
	for k := 0; k < r.n; k++ {
		if (r.start+k)%every != 0 {
			continue
		}
		t := time.UnixMilli(r.draw[r.start+k].T).In(r.loc)
		day := t.YearDay() + 400*t.Year()
		m := timeMark{k: k, t: t, label: strings.ToUpper(t.Format(layout))}
		switch {
		case layout == "Jan 2" && day == prevDay:
			continue // one date label per day
		case layout != "Jan 2" && prevDay >= 0 && day != prevDay:
			m.label, m.major = strings.ToUpper(t.Format("Jan 2")), true
		}
		prevDay = day
		out = append(out, m)
	}
	return out
}
