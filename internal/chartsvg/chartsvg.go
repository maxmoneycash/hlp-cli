// Package chartsvg renders candles and equity curves as standalone SVG in
// the setup-rush look, for the MCP Apps chart view. It reads the skins from internal/chart (Palettes) and never changes
// them, so a chart in Claude Desktop matches the terminal chart.
//
// Output is deterministic (fixed float formatting, UTC times) so it can be
// golden-tested. Every text node is XML-escaped; the SVG carries no script
// and no external references.
package chartsvg

import (
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp/internal/chart"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/theme"
)

// Geometry from setup-rush core.js (AXIS_W, AXIS_H) and candle-paint.js.
const (
	axisW     = 62.0
	axisH     = 26.0
	padTop    = 10.0
	bodyRatio = 0.74
	volShare  = 0.18 // volume band, fraction of the plot height
	margin    = 0.04 // price range margin
	fontStack = `ui-monospace,SFMono-Regular,Menlo,Consolas,monospace`
	tagInk    = "#07090f"
)

// Options configures Candles.
type Options struct {
	Width, Height int    // viewBox size; 0 = 720 x 360
	Skin          string // internal/chart palette name; "" = terminal
	Style         string // classic (default), solid, hollow
	Coin          string // watermark, e.g. BTC
	Interval      string // watermark, e.g. 1h
	Lines         []chart.HLine
	NoVolume      bool
}

// Palette resolves a skin name, falling back to the default skin.
func Palette(name string) chart.Palette {
	if p, ok := chart.PaletteByName(name); ok {
		return p
	}
	p, _ := chart.PaletteByName(chart.DefaultPalette)
	return p
}

func hex(c theme.Color) string {
	if s := c.True.String(); s != "" {
		return s
	}
	return "none"
}

// f formats a coordinate with one decimal (deterministic, compact).
func f(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "0"
	}
	s := strconv.FormatFloat(math.Round(v*10)/10, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	if s == "-0" {
		return "0"
	}
	return s
}

func esc(s string) string { return html.EscapeString(domain.CleanText(s)) }

// step is setup-rush niceStep (core.js:2706): 1, 2, 5, 10 x 10^k.
func step(raw float64) float64 {
	if raw <= 0 || math.IsNaN(raw) || math.IsInf(raw, 0) {
		return 1
	}
	p := math.Pow(10, math.Floor(math.Log10(raw)))
	switch n := raw / p; {
	case n < 1.5:
		return p
	case n < 3:
		return 2 * p
	case n < 7:
		return 5 * p
	}
	return 10 * p
}

// price formats p with the decimals the step needs (at least the price's
// own precision: 0 above 1000, 1 above 100, 2 above 1).
func price(p, st float64) string {
	a := math.Abs(p)
	d := 2
	switch {
	case a >= 1000:
		d = 0
	case a >= 100:
		d = 1
	case a > 0 && a < 1:
		d = min(8, -int(math.Floor(math.Log10(a)))+3)
	}
	if st > 0 && st < 1 {
		d = max(d, min(8, int(math.Ceil(-math.Log10(st)-1e-9))))
	}
	return chart.FormatPrice(p, d)
}

// chamfer is the setup-rush tag path: top-right and bottom-left corners cut
// by c (core.js rr, :4148).
func chamfer(x, y, w, h, c float64) string {
	return fmt.Sprintf("M%s %sH%sL%s %sV%sH%sL%s %sZ",
		f(x), f(y), f(x+w-c), f(x+w), f(y+c), f(y+h), f(x+c), f(x), f(y+h-c))
}

// inkOn picks dark or light text for a fill by luminance.
func inkOn(c theme.RGB) string {
	y := 0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)
	if y > 110 {
		return tagInk
	}
	return "#f4f3ef"
}

type look struct {
	body, wick, outline string
	hollow              bool
}

// candleLook ports setup-rush's candle-paint.js candleLook.
func candleLook(p chart.Palette, style string, up bool) look {
	pick := func(u, d theme.Color) theme.Color {
		if up {
			return u
		}
		return d
	}
	switch style {
	case "solid":
		c := hex(pick(p.Bull, p.Bear))
		return look{body: c, wick: c}
	case "hollow":
		c := hex(pick(p.Bull, p.Bear))
		return look{body: hex(p.BG), wick: c, outline: c, hollow: true}
	}
	body := pick(p.Up, p.Down)
	l := look{body: hex(body), wick: hex(pick(p.WickUp, p.WickDown))}
	hollow := body.True == p.BG.True
	if p.Outline.True.Valid && (up || hollow) {
		l.outline = hex(p.Outline)
	}
	l.hollow = hollow && l.outline != ""
	if l.outline == l.body { // an outline in the body's own ink is a solid body
		l.outline = ""
	}
	return l
}

// Candles renders an OHLCV chart: grid, price axis, volume, candles, level
// lines, and the live price line with its chamfered tag. Fewer than one
// candle renders the empty frame.
//
// The root carries data-* attributes a view uses for a hover crosshair
// without re-deriving the layout: data-x0 (first slot centre), data-slot
// (slot width), data-n, data-top/data-bottom (plot y) and data-hi/data-lo
// (prices at those y).
func Candles(cs []domain.Candle, o Options) string {
	W, H := float64(o.Width), float64(o.Height)
	if W <= 0 {
		W = 720
	}
	if H <= 0 {
		H = 360
	}
	p := Palette(o.Skin)
	plotL, plotR := 0.0, W-axisW
	plotT, plotB := padTop, H-axisH
	priceB := plotB
	if !o.NoVolume {
		priceB = plotB - (plotB-plotT)*volShare*0.5 // candles leave room above the volume floor
	}

	lo, hi := math.Inf(1), math.Inf(-1)
	maxV := 0.0
	for _, c := range cs {
		lo, hi = math.Min(lo, c.L), math.Max(hi, c.H)
		maxV = math.Max(maxV, c.V)
	}
	for _, l := range o.Lines {
		if l.Price > 0 && len(cs) > 0 {
			lo, hi = math.Min(lo, l.Price), math.Max(hi, l.Price)
		}
	}
	if len(cs) == 0 || math.IsInf(lo, 0) {
		lo, hi = 0, 1
	}
	if hi-lo <= 0 {
		d := math.Max(math.Abs(hi)*0.01, 1e-9)
		lo, hi = lo-d, hi+d
	}
	pad := (hi - lo) * margin
	lo, hi = lo-pad, hi+pad
	y := func(v float64) float64 { return priceB - (v-lo)/(hi-lo)*(priceB-plotT) }

	n := len(cs)
	slot := (plotR - plotL) / math.Max(float64(n), 1)
	bodyW := math.Max(1, math.Round(slot*bodyRatio))
	wickW := 1.0
	if bodyW >= 14 {
		wickW = 2
	}
	x := func(i int) float64 { return plotL + slot*(float64(i)+0.5) }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %s %s" width="%s" height="%s" role="img"`,
		f(W), f(H), f(W), f(H))
	fmt.Fprintf(&b, ` data-x0="%s" data-slot="%s" data-n="%d" data-top="%s" data-bottom="%s" data-hi="%s" data-lo="%s"`,
		f(x(0)), strconv.FormatFloat(slot, 'f', 3, 64), n, f(plotT), f(priceB),
		strconv.FormatFloat(hi, 'g', 10, 64), strconv.FormatFloat(lo, 'g', 10, 64))
	label := strings.TrimSpace(strings.ToUpper(o.Coin + " " + o.Interval))
	fmt.Fprintf(&b, ` font-family="%s"><title>%s</title>`, fontStack, esc(label+" chart"))
	fmt.Fprintf(&b, `<rect width="100%%" height="100%%" fill="%s"/>`, hex(p.BG))

	// Grid and price labels.
	st := step((hi - lo) / 6)
	b.WriteString(`<g class="grid">`)
	var labels strings.Builder
	for v := math.Ceil(lo/st) * st; v <= hi; v += st {
		yy := y(v)
		if yy < plotT+4 || yy > priceB-2 {
			continue
		}
		fmt.Fprintf(&b, `<line x1="%s" y1="%s" x2="%s" y2="%s" stroke="%s" stroke-width="1"/>`, f(plotL), f(yy), f(plotR), f(yy), hex(p.Grid))
		fmt.Fprintf(&labels, `<text x="%s" y="%s" data-y="%s">%s</text>`, f(plotR+8), f(yy+3.5), f(yy), esc(price(v, st)))
	}
	b.WriteString(`</g>`)

	// Time row: one label per >= 72 px, UTC.
	if n > 0 {
		every := int(math.Ceil(72 / slot))
		dayFmt := false
		if n > 1 && cs[n-1].T-cs[0].T > 0 {
			per := time.Duration(cs[1].T-cs[0].T) * time.Millisecond
			dayFmt = per*time.Duration(every) >= 24*time.Hour
		}
		fmt.Fprintf(&b, `<g class="time" fill="%s" font-size="10">`, hex(p.Muted))
		for i := every / 2; i < n; i += every {
			if x(i) > plotR-70 { // keep the watermark clear
				break
			}
			t := time.UnixMilli(cs[i].T).UTC()
			s := t.Format("15:04")
			if dayFmt || (t.Hour() == 0 && t.Minute() == 0) {
				s = t.Format("Jan 2")
			}
			fmt.Fprintf(&b, `<text x="%s" y="%s" text-anchor="middle">%s</text>`, f(x(i)), f(plotB+17), esc(s))
		}
		b.WriteString(`</g>`)
	}
	if label != "" {
		fmt.Fprintf(&b, `<text x="%s" y="%s" text-anchor="end" font-size="10" font-weight="700" fill="%s">%s</text>`,
			f(plotR-4), f(plotB+17), hex(p.Muted), esc(strings.ReplaceAll(label, " ", " · ")))
	}

	// Volume floor.
	if !o.NoVolume && maxV > 0 {
		volH := (plotB - plotT) * volShare
		b.WriteString(`<g class="vol" fill-opacity="0.22">`)
		for i, c := range cs {
			h := c.V / maxV * volH
			if h < 0.5 {
				continue
			}
			col := hex(p.Bull)
			if !c.Up() {
				col = hex(p.Bear)
			}
			fmt.Fprintf(&b, `<rect x="%s" y="%s" width="%s" height="%s" fill="%s"/>`, f(x(i)-bodyW/2), f(plotB-h), f(bodyW), f(h), col)
		}
		b.WriteString(`</g>`)
	}

	// Candles: wicks, then bodies (candle-paint.js paintBars).
	b.WriteString(`<g class="candles">`)
	for i, c := range cs {
		l := candleLook(p, o.Style, c.Up())
		cx := x(i)
		top, bot := y(math.Max(c.O, c.C)), y(math.Min(c.O, c.C))
		fmt.Fprintf(&b, `<rect x="%s" y="%s" width="%s" height="%s" fill="%s"/>`,
			f(cx-wickW/2), f(y(c.H)), f(wickW), f(math.Max(y(c.L)-y(c.H), 1)), l.wick)
		h := math.Max(bot-top, 1)
		if l.hollow || l.outline != "" {
			fmt.Fprintf(&b, `<rect x="%s" y="%s" width="%s" height="%s" fill="%s" stroke="%s" stroke-width="1"/>`,
				f(cx-bodyW/2+0.5), f(top+0.5), f(math.Max(bodyW-1, 1)), f(math.Max(h-1, 0.5)), l.body, l.outline)
		} else {
			fmt.Fprintf(&b, `<rect x="%s" y="%s" width="%s" height="%s" fill="%s"/>`, f(cx-bodyW/2), f(top), f(bodyW), f(h), l.body)
		}
	}
	b.WriteString(`</g>`)

	// Level lines with 16 px axis tags.
	for _, l := range o.Lines {
		if l.Price <= 0 || n == 0 {
			continue
		}
		col := levelInk(p, l.Kind)
		yy := y(l.Price)
		fmt.Fprintf(&b, `<line x1="%s" y1="%s" x2="%s" y2="%s" stroke="%s" stroke-dasharray="4 3" stroke-opacity="0.8"/>`,
			f(plotL), f(yy), f(plotR), f(yy), col.hex)
		fmt.Fprintf(&b, `<path d="%s" fill="%s"/>`, chamfer(plotR+2, yy-8, axisW-4, 16, 4), col.hex)
		text := l.Label
		if text == "" {
			text = string(l.Kind)
		}
		fmt.Fprintf(&b, `<text x="%s" y="%s" font-size="9" font-weight="800" fill="%s">%s</text>`,
			f(plotR+7), f(yy+3), inkOn(col.rgb), esc(text))
	}

	// Price labels sit above the levels but under the live tag.
	fmt.Fprintf(&b, `<g class="axis" fill="%s" font-size="10.5">%s</g>`, hex(p.Muted), labels.String())

	// Live price: dotted line from the forming body to the axis, then the tag.
	if n > 0 {
		last := cs[n-1]
		ink := p.Gain
		if !last.Up() {
			ink = p.Loss
		}
		yy := y(last.C)
		fmt.Fprintf(&b, `<line class="last" x1="%s" y1="%s" x2="%s" y2="%s" stroke="%s" stroke-dasharray="2 3" stroke-opacity="0.75"/>`,
			f(x(n-1)+bodyW/2+4), f(yy), f(plotR), f(yy), hex(ink))
		fmt.Fprintf(&b, `<path class="tag" d="%s" fill="%s"/>`, chamfer(plotR+2, yy-11, axisW-4, 22, 6), hex(ink))
		tst := st
		if math.Abs(last.C) < 1000 {
			tst = st / 10
		}
		fmt.Fprintf(&b, `<text x="%s" y="%s" font-size="10.5" font-weight="800" fill="%s">%s</text>`,
			f(plotR+7), f(yy+4), inkOn(ink.True), esc(price(last.C, tst)))
	}
	b.WriteString(`</svg>`)
	return b.String()
}

type ink struct {
	hex string
	rgb theme.RGB
}

func levelInk(p chart.Palette, k chart.HLineKind) ink {
	c := p.Muted
	switch k {
	case chart.LineLiq, chart.LineStop:
		c = p.Loss
	case chart.LineTP:
		c = p.Gain
	case chart.LineOrder:
		c = p.Text
	}
	return ink{hex(c), c.True}
}

// SparkOptions configures Spark.
type SparkOptions struct {
	Width, Height int    // 0 = 320 x 72
	Skin          string // palette; "" = terminal
	// Baseline draws a faint line at this value (1.0 for a share price
	// curve, 0 for PnL); NaN = none.
	Baseline float64
	// Area fills under the line with a fade.
	Area bool
}

// Spark renders a small line chart (equity, share price, drawdown) in the
// skin's gain or loss ink by the sign of the move. Fewer than two points
// render an empty frame.
func Spark(s domain.Series, o SparkOptions) string {
	W, H := float64(o.Width), float64(o.Height)
	if W <= 0 {
		W = 320
	}
	if H <= 0 {
		H = 72
	}
	p := Palette(o.Skin)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %s %s" width="%s" height="%s" role="img" preserveAspectRatio="none">`,
		f(W), f(H), f(W), f(H))
	var pts []domain.Point
	for _, pt := range s {
		if !math.IsNaN(pt.V) && !math.IsInf(pt.V, 0) {
			pts = append(pts, pt)
		}
	}
	if len(pts) < 2 {
		b.WriteString(`</svg>`)
		return b.String()
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, pt := range pts {
		lo, hi = math.Min(lo, pt.V), math.Max(hi, pt.V)
	}
	if !math.IsNaN(o.Baseline) && o.Baseline >= lo-(hi-lo) && o.Baseline <= hi+(hi-lo) {
		lo, hi = math.Min(lo, o.Baseline), math.Max(hi, o.Baseline)
	}
	if hi-lo <= 0 {
		hi, lo = hi+1e-9, lo-1e-9
	}
	t0, t1 := float64(pts[0].T), float64(pts[len(pts)-1].T)
	if t1 <= t0 {
		t1 = t0 + 1
	}
	const pad = 3.0
	X := func(t int64) float64 { return pad + (float64(t)-t0)/(t1-t0)*(W-2*pad) }
	Y := func(v float64) float64 { return pad + (hi-v)/(hi-lo)*(H-2*pad) }
	col := p.Gain
	if pts[len(pts)-1].V < pts[0].V {
		col = p.Loss
	}
	var d strings.Builder
	for i, pt := range pts {
		if i == 0 {
			d.WriteString("M")
		} else {
			d.WriteString("L")
		}
		d.WriteString(f(X(pt.T)) + " " + f(Y(pt.V)))
	}
	if !math.IsNaN(o.Baseline) && o.Baseline >= lo && o.Baseline <= hi {
		fmt.Fprintf(&b, `<line x1="0" x2="%s" y1="%s" y2="%s" stroke="%s" stroke-dasharray="2 3"/>`, f(W), f(Y(o.Baseline)), f(Y(o.Baseline)), hex(p.Muted))
	}
	if o.Area {
		fmt.Fprintf(&b, `<defs><linearGradient id="sg" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="%s" stop-opacity="0.28"/><stop offset="1" stop-color="%s" stop-opacity="0"/></linearGradient></defs>`,
			hex(col), hex(col))
		fmt.Fprintf(&b, `<path d="%sL%s %sL%s %sZ" fill="url(#sg)"/>`, d.String(), f(X(pts[len(pts)-1].T)), f(H), f(X(pts[0].T)), f(H))
	}
	fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="%s" stroke-width="1.5" stroke-linejoin="round"/>`, d.String(), hex(col))
	b.WriteString(`</svg>`)
	return b.String()
}
