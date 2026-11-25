package chart

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/theme"
)

// The setup-rush look: a skin
// paints its own ground, hollow and solid bodies at sub-cell precision,
// quiet grid and volume, chamfered price tags, level lines with shaded
// target and stop zones, and pattern brackets. Everything here runs only
// when candleRender.sk is set; the pre-skin renderer is untouched.

const (
	skinPad  = 0.04 // price padding (core.js fits the camera with 2-4 %)
	cardMaxH = 16   // up to this height a chart is an inline card (port spec 3.2)
)

// skin is a Palette resolved for one frame: every translucent canvas color
// pre-blended over the ground (port spec 4.2).
type skin struct {
	p                    Palette
	volUp, volDown       theme.Color
	zoneT, zoneS         theme.Color
	entry, order         theme.Color
	liveGain, liveLoss   theme.Color
	liveBull             theme.Color // the live line under a white up tag
	nameBull, nameBear   theme.Color
	brBull, brBear       theme.Color
	oldBull, oldBear     theme.Color
	tagMuted             theme.Color
	inkDark, inkLight    theme.Color
	cursor, guide        theme.Color
	tailFrom, lineFillTo float64
}

// resolveSkin returns the frame's skin, or nil for the pre-skin renderer.
func resolveSkin(o CandleOpts) *skin {
	if strings.EqualFold(strings.TrimSpace(o.Skin), SkinNone) || o.Style.legacy() {
		return nil
	}
	p, ok := PaletteByName(o.Skin)
	if !ok {
		p, _ = PaletteByName(DefaultPalette)
	}
	return newSkin(p)
}

func (s *skin) hollowUp() bool { return s.p.HollowUp() }

func newSkin(p Palette) *skin {
	bg := p.BG.True
	mixFG := func(c theme.Color, a float64) theme.Color {
		col := fgColor(theme.Mix(bg, c.True, a))
		col.C16 = c.C16
		return col
	}
	mixFaint := func(c theme.Color, a float64) theme.Color { return faintColor(theme.Mix(bg, c.True, a)) }
	mixWash := func(c theme.Color, a float64) theme.Color { return washColor(theme.Mix(bg, c.True, a)) }
	s := &skin{p: p}
	volA, wash, nameA := 0.10, 1.0, 0.70
	if p.Light {
		volA = 0.12
	}
	if p.Ext != nil {
		wash, nameA = p.Ext.Wash, p.Ext.NameAlpha
	}
	s.volUp, s.volDown = mixFaint(p.Bull, volA), mixFaint(p.Bear, volA)
	if p.Ext != nil {
		s.volUp, s.volDown = mixFaint(p.Ext.VolUp, 0.55), mixFaint(p.Ext.VolDown, 0.55)
	}
	// At 16 colors volume stays as bright-black blocks (grid and washes go).
	s.volUp.C16, s.volDown.C16 = 8, 8
	s.zoneT, s.zoneS = mixWash(p.Bull, 0.06*wash), mixWash(p.Bear, 0.07*wash)
	entryInk := fgColor(theme.Hex("#eef2f8"))
	if p.Light {
		entryInk = fgColor(theme.Hex("#191a17"))
	}
	s.entry = mixFG(entryInk, 0.45)
	s.entry.C16 = 7
	s.order = mixFG(p.Text, 0.45)
	s.order.C16 = 8
	s.liveGain, s.liveLoss = mixFG(p.Gain, 0.75), mixFG(p.Loss, 0.75)
	s.liveBull = mixFG(p.Bull, 0.75)
	s.nameBull, s.nameBear = mixFG(p.Bull, nameA), mixFG(p.Bear, nameA)
	s.brBull, s.brBear = mixFG(p.Bull, 0.55), mixFG(p.Bear, 0.55)
	s.oldBull, s.oldBear = mixFG(p.Bull, 0.45), mixFG(p.Bear, 0.45)
	s.tagMuted = mixFG(p.Muted, 0.9)
	s.inkDark = theme.Color{True: theme.Hex("#07090f"), C256: 16, C16: 0}
	s.inkLight = theme.Color{True: theme.Hex("#f4f3ef"), C256: 231, C16: 15}
	s.cursor = mixWash(p.Text, 0.07)
	s.guide = mixFaint(p.Muted, 0.55)
	s.lineFillTo = 0.14
	if p.Light {
		s.lineFillTo = 0.10
	}
	return s
}

// inkOn is the tag text color on fill: setup-rush's near-black on every
// dark-ground fill, by luminance on light grounds and graphite (inkOn).
func (s *skin) inkOn(fill theme.Color) theme.Color {
	if (s.p.Light || (s.p.Ext != nil && s.p.Ext.AutoInk)) && theme.Luminance(fill.True) < 0.22 {
		return s.inkLight
	}
	return s.inkDark
}

func sameRGB(a, b theme.Color) bool { return a.True == b.True }

// ---- numbers ----

// rushStep is setup-rush's niceStep (core.js:2706): 1-2-5 by thresholds.
func rushStep(raw float64) float64 {
	if raw <= 0 || math.IsNaN(raw) || math.IsInf(raw, 0) {
		return 1
	}
	p := math.Pow(10, math.Floor(math.Log10(raw)))
	n := raw / p
	switch {
	case n < 1.5:
		return p
	case n < 3:
		return 2 * p
	case n < 7:
		return 5 * p
	}
	return 10 * p
}

// rushTicks returns the grid prices for a plot rows tall: about one line per
// three rows, between 2 and 6 (at most 4 on a card), at least 3 rows apart,
// and never fewer than 3 when a finer step keeps them 2 rows apart: a
// compact chart needs three labelled prices to read its scale.
func rushTicks(lo, hi float64, rows int, card bool) ([]float64, float64) {
	n := clampi(rows/3, 2, 6)
	limit := 8
	if card {
		n, limit = min(n, 3), 4
	}
	span := hi - lo
	gen := func(step float64) []float64 {
		var ticks []float64
		start := math.Ceil(lo/step) * step
		for i := 0; i < 1000; i++ {
			v := start + float64(i)*step
			if v > hi+step*1e-9 {
				break
			}
			ticks = append(ticks, v)
		}
		return ticks
	}
	step := rushStep(span / float64(n))
	if !(span > 0) {
		return gen(step), step
	}
	gap := func(step float64) float64 { return step / span * float64(rows) }
	ticks := gen(step)
	for pass := 0; pass < 6; pass++ {
		bigger := rushStep(step * 1.6)
		if len(ticks) > limit || (gap(step) < 3 && len(gen(bigger)) >= 2) {
			step, ticks = bigger, gen(bigger)
			continue
		}
		break
	}
	for pass := 0; pass < 3 && len(ticks) < 3; pass++ {
		finer := rushStep(step / 2.2)
		if gap(finer) < 2 || len(gen(finer)) > max(limit, 4) {
			break
		}
		step, ticks = finer, gen(finer)
	}
	return ticks, step
}

// stepDec is core.js stepDec: the decimals a grid step carries
// (0.25 -> 2, 2.5 -> 1, 25 -> 0, 5e-9 -> 9).
func stepDec(step float64) int {
	if !(step > 0) || math.IsInf(step, 0) {
		return 0
	}
	v, _ := strconv.ParseFloat(strconv.FormatFloat(step, 'g', 3, 64), 64)
	e := strconv.FormatFloat(v, 'e', -1, 64)
	i := strings.IndexByte(e, 'e')
	mant, exp := e[:i], e[i+1:]
	x, _ := strconv.Atoi(exp)
	frac := 0
	if j := strings.IndexByte(mant, '.'); j >= 0 {
		frac = len(mant) - j - 1
	}
	return max(0, frac-x)
}

var subDigits = []rune("₀₁₂₃₄₅₆₇₈₉")

// rushPrice is core.js fmtPx: 62,410.5 · 2.14 · 0.0412 · 0.0₄1234 (zeros
// counted, DEX-screener style), with enough decimals for the grid step.
func rushPrice(p, step float64) string {
	if math.IsNaN(p) || math.IsInf(p, 0) {
		return "-"
	}
	a := math.Abs(p)
	need := stepDec(step)
	if a >= 1 {
		d := 2
		switch {
		case a >= 1000:
			d = 0
		case a >= 100:
			d = 1
		}
		if step > 0 {
			d = max(d, min(6, need))
		}
		m := math.Pow(10, float64(d))
		return FormatPrice(math.Round(p*m)/m, d) // toLocaleString rounds half away from zero
	}
	if a == 0 {
		return "0"
	}
	z := -int(math.Floor(math.Log10(a))) - 1
	if z < 3 {
		return strconv.FormatFloat(p, 'f', min(6, max(z+4, need)), 64)
	}
	sig := min(5, max(4, need-z))
	n := math.Round(a * math.Pow(10, float64(z+sig)))
	if n >= math.Pow(10, float64(sig)) {
		z--
		n = math.Round(a * math.Pow(10, float64(z+sig)))
	}
	if z < 3 {
		return strconv.FormatFloat(p, 'f', min(6, z+sig), 64)
	}
	var b strings.Builder
	if p < 0 {
		b.WriteRune('−')
	}
	b.WriteString("0.0")
	for _, c := range strconv.Itoa(z) {
		b.WriteRune(subDigits[c-'0'])
	}
	b.WriteString(strconv.FormatFloat(n, 'f', 0, 64))
	return b.String()
}

func runeLen(s string) int { return len([]rune(s)) }

// ---- layout ----

// layoutRush is layout() for a skin: one time row and no axis rule, volume
// as a band inside the plot, the axis as wide as its widest tag.
func (r *candleRender) layoutRush() {
	o := r.o
	if o.Header {
		r.headerRows = 1
	}
	if o.Height >= 10 {
		r.timeRows = 1 // cards too: a window with no times can't be read
	}
	r.plotH = max(o.Height-r.headerRows-r.timeRows, 2)
	r.plotTop = r.headerRows
	r.volTop = r.plotTop + r.plotH
	r.axisRow = r.volTop
	r.volRows = 0
	switch {
	case o.VolRows == 0:
	case o.VolRows > 0:
		r.band = min(o.VolRows, max(r.plotH/4, 1))
	case o.Height <= cardMaxH:
		if r.plotH >= 12 {
			r.band = 1
		}
	default:
		r.band = clampi(int(math.Round(0.11*float64(r.plotH))), 1, 6)
	}
	r.noAxis = o.Width < 36
	setZoom := func(z int) {
		r.o.Zoom = z
		r.bodyW, r.gap = zoomGeometry(z)
		r.stride = r.bodyW + r.gap
	}
	// Zoom shrinks (6, 4, 2, 1) until at least 12 candles fit (8 on a
	// narrow chart, where stride 1 leaves no gap between bodies).
	if o.Style != LineStyle {
		least := 12
		if o.Width < 48 {
			least = 8
		}
		for r.stride > 1 && (o.Width-10-rightPad)/r.stride < least {
			setZoom(ZoomOut(normZoom(r.o.Zoom)))
		}
		// A few candles grow wider (setup-rush fits its camera to them)
		// instead of hugging the right edge of an empty plot.
		if avail := o.Width - 10 - rightPad; len(r.draw)*r.stride < avail/3 {
			for z := ZoomIn(normZoom(r.o.Zoom)); z != r.o.Zoom; z = ZoomIn(z) {
				b, g := zoomGeometry(z)
				if len(r.draw)*(b+g) > avail {
					break
				}
				setZoom(z)
			}
		}
	}

	offset := clampi(o.Offset, 0, len(r.draw)-1)
	r.axisW = 10
	if r.noAxis {
		// No price column: the live tag still gets its own columns at the
		// right, never over the newest candles.
		r.axisW = runeLen(rushPrice(r.real[len(r.real)-1-offset].C, 0)) + 4
	}
	for pass := 0; pass < 3; pass++ {
		r.plotW = o.Width - r.axisW
		r.avail = r.plotW - rightPad
		n := max(r.avail/r.stride, 1)
		offset = panOffset(len(r.draw), o.Offset, n)
		r.end = len(r.draw) - offset
		r.start = max(r.end-n, 0)
		r.n = r.end - r.start
		r.solveRange()
		if r.noAxis {
			w := runeLen(r.tagText(r.real[r.end-1].C)) + 4 // caps and padding
			if w == r.axisW {
				break
			}
			r.axisW = w
			continue
		}
		w := runeLen(r.tagText(r.real[r.end-1].C))
		for _, t := range r.ticks {
			w = max(w, runeLen(rushPrice(t, r.step)))
		}
		for _, hl := range o.Lines {
			if hl.Price > 0 {
				w = max(w, runeLen(r.tagText(hl.Price)))
			}
		}
		w = max(w+4, 7) // left cap (in the gap), pad, label, pad, right cap
		if w == r.axisW {
			break
		}
		r.axisW = w
	}
	r.plotW = o.Width - r.axisW
	r.avail = r.plotW - rightPad
	r.o.Offset = offset // the applied pan (Viewport.Offset)
	r.maxOffset = panOffset(len(r.draw), len(r.draw), max(r.avail/r.stride, 1))
}

// tagText formats a tag's price with the same decimals as the header
// readout (the market's price precision), so the live tag and the header
// never disagree (66.626, not 66.63).
func (r *candleRender) tagText(p float64) string {
	return FormatPrice(p, r.dec)
}

// u8 maps a price to eighths of a row from the plot bottom.
func (r *candleRender) u8(p float64) int { return r.units(p, 8) }

// rowOfU8 is the screen row holding eighth u (clamped to the plot).
func (r *candleRender) rowOfU8(u int) int { return r.rowY(clampi(u/8, 0, r.plotH-1)) }

// level3 is the three-level horizontal line for an eighth offset inside a
// cell: bottom, middle or top of the cell.
func level3(e int) rune {
	switch {
	case e <= 1:
		return '▁'
	case e >= 6:
		return '▔'
	}
	return '─'
}

// lineRow returns the row and glyph of a 1-px horizontal line at price p.
func (r *candleRender) lineRow(p float64) (int, rune) {
	u := r.u8(p)
	if u >= r.plotH*8 {
		return r.rowY(r.plotH - 1), '▔'
	}
	return r.rowY(u / 8), level3(u % 8)
}

// fromCol maps a candle open time to its body-left column (0 when the time
// is before the window or zero, -1 when after it).
func (r *candleRender) fromCol(t int64) int {
	if t <= 0 || r.n == 0 || t <= r.draw[r.start].T {
		return 0
	}
	for k := 0; k < r.n; k++ {
		if r.draw[r.start+k].T >= t {
			return max(r.colOf(k), 0)
		}
	}
	return -1
}

// ---- paint ----

func (r *candleRender) paintRush() {
	r.reserved = map[int]bool{}
	r.labelRows = map[int][][2]int{}
	r.liveRow = -1
	r.headX = -1
	for i := range r.g.Cells {
		r.g.Cells[i].BG = r.sk.p.BG
	}
	r.paintZones()
	r.paintRushGrid()
	r.paintRushTime()
	r.paintRushVolume()
	r.paintRushCandles()
	r.paintOverlays()
	r.paintRushLast()
	r.paintRushLevels()
	r.paintRushMarkers()
	r.paintRushTicks()
	r.paintRushCrosshair()
	if r.o.Header {
		r.paintHeader()
	}
}

// paintZones shades the target and stop zones between the entry line and
// each target/stop level (core.js drawRiskBoxes).
func (r *candleRender) paintZones() {
	var entry *HLine
	for i := range r.o.Lines {
		if r.o.Lines[i].Kind == LineEntry && r.o.Lines[i].Price > 0 {
			entry = &r.o.Lines[i]
			break
		}
	}
	if entry == nil {
		return
	}
	ey, _ := r.priceRow(entry.Price)
	x0 := r.fromCol(entry.From)
	if x0 < 0 {
		return
	}
	// The entry row belongs to the zone that covers more of it, so the two
	// washes meet at the entry with no unshaded row between them.
	upper := r.u8(entry.Price)%8 < 4 // the entry sits low in its row
	var meet theme.Color
	for _, hl := range r.o.Lines {
		var col theme.Color
		switch hl.Kind {
		case LineTP:
			col = r.sk.zoneT
		case LineStop:
			col = r.sk.zoneS
		default:
			continue
		}
		if hl.Price <= 0 || math.IsNaN(hl.Price) {
			continue
		}
		ly, _ := r.priceRow(hl.Price)
		a, b := ly, ey-1
		if ly > ey {
			a, b = ey+1, ly
		}
		if ly != ey && (ly < ey) == upper {
			meet = col
		}
		for y := max(a, r.plotTop); y <= min(b, r.plotTop+r.plotH-1); y++ {
			for x := x0; x < r.plotW; x++ {
				r.g.TintBG(x, y, col)
			}
		}
	}
	if meet.True.Valid && ey >= r.plotTop && ey < r.plotTop+r.plotH {
		for x := x0; x < r.plotW; x++ {
			r.g.TintBG(x, ey, meet)
		}
	}
}

// paintRushGrid draws the horizontals in the blended grid ink: solid like
// setup-rush's, only fainter.
func (r *candleRender) paintRushGrid() {
	for _, t := range r.ticks {
		y, ok := r.priceRow(t)
		if !ok {
			continue
		}
		for x := 0; x < r.plotW; x++ {
			r.g.Set(x, y, '─', r.sk.p.gridOver(r.g.At(x, y).BG), LGrid)
		}
	}
}

// paintRushTime writes the time row: clock labels under round times (bold
// in text on the larger unit), dotted verticals through the plot on panes,
// and the SYM · TF watermark at the right.
func (r *candleRender) paintRushTime() {
	if r.timeRows == 0 {
		return
	}
	y := r.axisRow
	wmX := r.plotW
	if r.o.Coin != "" && r.o.Width >= 48 && !r.o.Header && !r.o.NoTitle { // a header or the card already names it
		wm := strings.ToUpper(r.o.Coin)
		if r.o.Interval != "" {
			wm += " · " + strings.ToUpper(string(r.o.Interval))
		}
		wmX = r.plotW - 1 - runeLen(wm)
		if wmX > r.plotW/2 {
			r.g.Text(wmX, y, wm, r.sk.p.Muted, LLabel)
			for x := wmX; x < wmX+runeLen(wm); x++ {
				r.g.At(x, y).Bold = true
			}
		} else {
			wmX = r.plotW
		}
	}
	gap := 9
	if r.o.Width < 48 {
		gap = 12
	}
	step := TimeStep(r.ivDur, r.stride, gap)
	lastEnd := -2
	for _, m := range r.timeMarks(step, gap) {
		k, t, lbl := m.k, m.t, m.label
		cx := r.centerOf(k)
		x0 := cx - runeLen(lbl)/2
		x1 := x0 + runeLen(lbl)
		if x0 <= lastEnd+1 || x0 < 1 || x1 > r.plotW-1 || x1 > wmX-2 {
			continue
		}
		// core.js drawAxes: every round hour (and every date) in bold text
		major := t.Minute() == 0
		if step >= day {
			major = t.Day() == 1
		}
		if r.o.TickBars {
			major = m.major
		}
		col, bold := r.sk.p.Muted, false
		if major {
			col, bold = r.sk.p.Text, true
		}
		r.g.Text(x0, y, lbl, col, LLabel)
		for x := x0; x < x1; x++ {
			r.g.At(x, y).Bold = bold
		}
		lastEnd = x1 - 1
		if r.o.Height > cardMaxH {
			for yy := r.plotTop; yy < r.plotTop+r.plotH; yy++ {
				// the dotted vertical passes under the horizontals
				if cell := r.g.At(cx, yy); cell != nil && cell.Ch == ' ' {
					r.g.Set(cx, yy, '┊', r.sk.p.gridOver(cell.BG), LGrid)
				}
			}
		}
	}
}

// paintRushVolume draws the closed bars' volume as a band at the plot floor,
// behind the candles and very muted (core.js drawVolume).
func (r *candleRender) paintRushVolume() {
	if r.band <= 0 {
		return
	}
	end := r.end
	if end == len(r.real) {
		end-- // the forming bar is skipped
	}
	vmax := 0.0
	for _, c := range r.real[r.start:max(end, r.start)] {
		vmax = math.Max(vmax, c.V)
	}
	if vmax <= 0 {
		return
	}
	floor := r.plotTop + r.plotH - 1
	for k := 0; k < end-r.start; k++ {
		c := r.real[r.start+k]
		col := r.sk.volUp
		if !c.Up() {
			col = r.sk.volDown
		}
		u := int(math.Round(c.V / vmax * float64(r.band*8)))
		if c.V > 0 && u == 0 {
			u = 1
		}
		for row := 0; row < r.band; row++ {
			f := clampi(u-8*row, 0, 8)
			if f == 0 {
				break
			}
			for bx := 0; bx < r.bodyW; bx++ {
				r.g.Set(r.colOf(k)+bx, floor-row, lowerBlocks[f], col, LVolume)
			}
		}
	}
}

// bodyKind is how a classic body is drawn.
type bodyKind int

const (
	kindSolid  bodyKind = iota
	kindHollow          // body == ground: an outline only
	kindFilled          // filled body with an outline (paper, candy, mint, graphite up)
)

// look is candle-paint.js candleLook for one direction.
func (r *candleRender) look(up bool) (body, wick, line theme.Color, kind bodyKind) {
	p := r.sk.p
	dir := p.Bear
	if up {
		dir = p.Bull
	}
	switch r.o.Style {
	case Solid, OHLC:
		return dir, dir, theme.Color{}, kindSolid
	case Hollow:
		if !r.sk.hollowUp() {
			return p.BG, dir, dir, kindHollow
		}
		// a hollow-up skin (terminal, rush) is already hollow up: keep its
		// down bodies solid, as the landing does, not a dim outline
	case Heikin:
		if up {
			return p.BG, p.Bull, p.Bull, kindHollow
		}
		return p.Bear, p.Bear, theme.Color{}, kindSolid
	}
	body, wick = p.Down, p.WickDown
	if up {
		body, wick = p.Up, p.WickUp
	}
	hasOutline := p.Outline != (theme.Color{})
	switch {
	case sameRGB(body, p.BG):
		line = wick
		if hasOutline {
			line = p.Outline
		}
		return body, wick, line, kindHollow
	case hasOutline && up && !sameRGB(p.Outline, body):
		// an outline in the body's own ink (terminal) is just a solid body
		return body, wick, p.Outline, kindFilled
	}
	return body, wick, theme.Color{}, kindSolid
}

func (r *candleRender) paintRushCandles() {
	if r.o.Style == LineStyle {
		r.paintLineStyle()
		return
	}
	cp := &candlePainter{g: r.g, style: Blocks, bodyW: r.bodyW, plotH: r.plotH,
		units: r.units, rowY: r.rowY, th: r.th}
	for k := 0; k < r.n; k++ {
		c := r.draw[r.start+k]
		x := r.colOf(k)
		up := c.Up()
		body, wick, line, kind := r.look(up)
		if r.o.Style == OHLC {
			r.drawOHLC(x, c, body)
			continue
		}
		switch kind {
		case kindHollow:
			r.drawOutlined(x, c, line, wick, theme.Color{})
		case kindFilled:
			r.drawOutlined(x, c, line, wick, body)
		default:
			cp.drawBlocksW(x, c, body, wick)
		}
		// The forming bar's print edge: its close as a 1-px line across the
		// body, so a hollow candle (row-precise edges) never looks late.
		if k == r.n-1 && r.end == len(r.real) && r.bodyW >= 3 && kind != kindSolid {
			y, gl := r.lineRow(r.real[r.end-1].C)
			col := r.sk.p.Bear
			if r.real[r.end-1].Up() {
				col = r.sk.p.Bull
			}
			for bx := 1; bx < r.bodyW-1; bx++ {
				if cell := r.g.At(x+bx, y); cell != nil && cell.Ch == ' ' {
					r.g.Set(x+bx, y, gl, col, LCandle)
				}
			}
		}
	}
}

// drawOutlined draws a body with a 1-px outline in line (port spec 2.3):
// hollow when fill is the zero Color, else filled (paper's white bodies
// with a dark edge). A hollow body 3 or more columns wide is a joined
// box-drawing outline (drawBox). A filled body's outline sits on its own
// edges, so it is exactly as wide as a solid one: the sides are the
// edge-column eighths ▏ and ▕ on the fill, the lids ▁ and ▔ on the rows
// just outside the body. Edges round to the nearest row boundary. A body under about
// half a row is a flat tick in the outline ink across the body width (the
// source fills a flat bar and skips the outline below 3 px). A 1-column
// hollow body is a heavy ┃ in the outline ink (║ read as two rails, a
// tint as a muddy body); a 1-column filled body keeps ║ on its fill.
func (r *candleRender) drawOutlined(x int, c domain.Candle, line, wick, fill theme.Color) {
	top := r.plotH * 8
	top8 := clampi(r.u8(math.Max(c.O, c.C)), 0, top)
	bot8 := clampi(r.u8(math.Min(c.O, c.C)), 0, top)
	wh8, wl8 := clampi(r.u8(c.H), 0, top), clampi(r.u8(c.L), 0, top)
	bt, bb := (top8+4)/8, (bot8+4)/8 // row boundaries: the body is rows [bb, bt)
	cx := x + r.bodyW/2
	filled := fill.True.Valid
	wickUp := func(from int) { // wick from row boundary `from` up to the high
		for row := from; row < r.plotH && wh8 > 8*row; row++ {
			if ch := wickGlyph(0, min(wh8, 8*row+8)-8*row); ch != 0 {
				r.g.Set(cx, r.rowY(row), ch, wick, LCandle)
			}
		}
	}
	wickDown := func(from int) { // wick from row boundary `from` down to the low
		for row := from - 1; row >= 0 && wl8 < 8*row+8; row-- {
			if ch := wickGlyph(max(wl8, 8*row)-8*row, 8); ch != 0 {
				r.g.Set(cx, r.rowY(row), ch, wick, LCandle)
			}
		}
	}
	// An open body of 3 or more columns is a joined box-drawing outline
	// (drawBox): its top and bottom rules sit on the rows holding the
	// body's edges, so a body inside one row is a flat tick.
	boxRow := func(u8 int) int { return clampi(u8/8, 0, r.plotH-1) }
	box := !filled && r.bodyW > 1
	if bt <= bb || top8-bot8 <= 2 || box && boxRow(top8) == boxRow(bot8) {
		// flat: a tick at the body's middle, the wicks joining it
		row := clampi((top8+bot8)/16, 0, r.plotH-1)
		above, below := wh8 > 8*row+4, wl8 < 8*row+4
		wickUp(row + 1)
		wickDown(row)
		y := r.rowY(row)
		for bx := 0; bx < r.bodyW; bx++ {
			ch := '━'
			if x+bx == cx {
				switch {
				case above && below:
					ch = '┿'
				case above:
					ch = '┷'
				case below:
					ch = '┯'
				}
			}
			r.g.Set(x+bx, y, ch, line, LCandle)
		}
		return
	}
	bt, bb = min(bt, r.plotH), max(bb, 0)
	if r.bodyW == 1 && !filled {
		// One column has no room for an outline (a ║ "box" reads as two
		// rails, a dim tint reads as a muddy gray body): the body is a
		// heavy ┃ in the outline ink, the wicks the light │, so an open
		// candle stays crisp and lighter than a solid one.
		wickUp(bt)
		wickDown(bb)
		for row := bb; row < bt; row++ {
			r.g.Set(x, r.rowY(row), '┃', line, LCandle)
		}
		return
	}
	if box {
		r.drawBox(x, cx, boxRow(top8), boxRow(bot8), wh8, wl8, line, wickUp, wickDown)
		return
	}
	if r.bodyW == 1 {
		wickUp(bt)
		wickDown(bb)
		for row := bb; row < bt; row++ {
			if cell := r.g.Set(x, r.rowY(row), '║', line, LCandle); cell != nil && filled {
				cell.BG = fill
			}
		}
		return
	}
	// lids on the rows just outside the body; at the plot's edge, inside it
	lidTop, lidTopCh := bt, '▁'
	if bt >= r.plotH {
		lidTop, lidTopCh = bt-1, '▔'
	}
	lidBot, lidBotCh := bb-1, '▔'
	if bb <= 0 {
		lidBot, lidBotCh = bb, '▁'
	}
	wickUp(bt)
	wickDown(bb)
	for row := bb; row < bt; row++ {
		y := r.rowY(row)
		for bx := 0; bx < r.bodyW; bx++ {
			ch := ' '
			switch {
			case bx == 0:
				ch = '▏'
			case bx == r.bodyW-1:
				ch = '▕'
			case row == lidTop:
				ch = lidTopCh
			case row == lidBot:
				ch = lidBotCh
			}
			if cell := r.g.Set(x+bx, y, ch, line, LCandle); cell != nil && filled {
				cell.BG = fill
			}
		}
	}
	for _, lid := range [2]struct {
		row int
		ch  rune
	}{{lidTop, lidTopCh}, {lidBot, lidBotCh}} {
		if lid.row >= bb && lid.row < bt {
			continue // drawn inside the body above
		}
		y := r.rowY(lid.row)
		for bx := 0; bx < r.bodyW; bx++ {
			if x+bx == cx {
				// the wick enters the body here: it wins the centre cell
				if cell := r.g.At(x+bx, y); cell != nil && cell.Layer == LCandle {
					continue
				}
			}
			r.g.Set(x+bx, y, lid.ch, line, LCandle)
		}
	}
}

// drawBox draws an open body as one continuous box-drawing outline,
// ┌─┴─┐ over │ │ over └─┬─┘, with the wicks joining the rules at the
// centre column, so the corners and the wick entries never gap the way
// eighth-block sides and lids do (they read as [ ] brackets). The rules
// sit mid-row on rows rt (top) and rb (bottom), rt > rb.
func (r *candleRender) drawBox(x, cx, rt, rb, wh8, wl8 int, line theme.Color, wickUp, wickDown func(int)) {
	right := x + r.bodyW - 1
	for row := rb; row <= rt; row++ {
		y := r.rowY(row)
		for xx := x; xx <= right; xx++ {
			ch := ' '
			switch {
			case row == rt && xx == x:
				ch = '┌'
			case row == rt && xx == right:
				ch = '┐'
			case row == rb && xx == x:
				ch = '└'
			case row == rb && xx == right:
				ch = '┘'
			case xx == x || xx == right:
				ch = '│'
			case row == rt && xx == cx && wh8 > 8*rt+5:
				ch = '┴'
			case row == rb && xx == cx && wl8 < 8*rb+3:
				ch = '┬'
			case row == rt || row == rb:
				ch = '─'
			}
			r.g.Set(xx, y, ch, line, LCandle)
		}
	}
	wickUp(rt + 1)
	wickDown(rb)
}

// drawOHLC draws setup-rush "bars": the high-low line with the open tick on
// the left and the close tick on the right, each one stroke thick and
// joined to the stem at mid-row (row precision, like the source's ticks
// sitting flush on the stem).
func (r *candleRender) drawOHLC(x int, c domain.Candle, col theme.Color) {
	cx := x + r.bodyW/2
	top := r.plotH * 8
	wl8, wh8 := clampi(r.u8(c.L), 0, top), clampi(r.u8(c.H), 0, top)
	o8, c8 := min(r.u8(c.O), top-1), min(r.u8(c.C), top-1)
	ro, rc := clampi(o8/8, 0, r.plotH-1), clampi(c8/8, 0, r.plotH-1)
	// the stem reaches the middle of both tick rows at least
	wl8, wh8 = min(wl8, 8*min(ro, rc)+4), max(wh8, 8*max(ro, rc)+4)
	for row := max(wl8/8, 0); row < min((wh8+7)/8, r.plotH); row++ {
		c0 := 8 * row
		if ch := wickGlyph(max(wl8, c0)-c0, min(wh8, c0+8)-c0); ch != 0 {
			r.g.Set(cx, r.rowY(row), ch, col, LCandle)
		}
	}
	yo, yc := r.rowY(ro), r.rowY(rc)
	for xx := x; xx < cx; xx++ {
		r.g.Set(xx, yo, '─', col, LCandle)
	}
	for xx := cx + 1; xx < x+r.bodyW; xx++ {
		r.g.Set(xx, yc, '─', col, LCandle)
	}
	above := func(row int) bool { return wh8 > 8*row+4 }
	below := func(row int) bool { return wl8 < 8*row+4 }
	join := func(row int, l, rt bool) rune {
		u, d := above(row), below(row)
		switch {
		case l && rt && u && d:
			return '┼'
		case l && rt && u:
			return '┴'
		case l && rt && d:
			return '┬'
		case l && rt:
			return '─'
		case l && u && d:
			return '┤'
		case l && u:
			return '┘'
		case l && d:
			return '┐'
		case l:
			return '─'
		case u && d:
			return '├'
		case u:
			return '└'
		case d:
			return '┌'
		}
		return '─'
	}
	if ro == rc {
		r.g.Set(cx, yo, join(ro, true, true), col, LCandle)
		return
	}
	r.g.Set(cx, yo, join(ro, true, false), col, LCandle)
	r.g.Set(cx, yc, join(rc, false, true), col, LCandle)
}

// ---- the line style ----

// monotone returns y at every integer x from xs[0] to xs[len-1] along the
// Fritsch-Carlson monotone cubic through (xs, ys) (line-path.js
// monotoneSegments): between two points it never leaves their range.
func monotone(xs, ys []float64) (x0 int, out []float64) {
	n := len(xs)
	if n == 0 {
		return 0, nil
	}
	x0 = int(math.Round(xs[0]))
	if n == 1 {
		return x0, []float64{ys[0]}
	}
	h, d, m := make([]float64, n-1), make([]float64, n-1), make([]float64, n)
	for i := 0; i < n-1; i++ {
		h[i] = xs[i+1] - xs[i]
		if h[i] != 0 {
			d[i] = (ys[i+1] - ys[i]) / h[i]
		}
	}
	m[0], m[n-1] = d[0], d[n-2]
	for i := 1; i < n-1; i++ {
		if d[i-1]*d[i] <= 0 {
			m[i] = 0
		} else {
			m[i] = (d[i-1] + d[i]) / 2
		}
	}
	for i := 0; i < n-1; i++ {
		if d[i] == 0 {
			m[i], m[i+1] = 0, 0
			continue
		}
		a, b := m[i]/d[i], m[i+1]/d[i]
		if s := a*a + b*b; s > 9 {
			t := 3 / math.Sqrt(s)
			m[i], m[i+1] = t*a*d[i], t*b*d[i]
		}
	}
	x1 := int(math.Round(xs[n-1]))
	out = make([]float64, 0, max(x1-x0+1, 1))
	seg := 0
	for x := x0; x <= x1; x++ {
		fx := float64(x)
		for seg < n-2 && fx > xs[seg+1] {
			seg++
		}
		if h[seg] == 0 {
			out = append(out, ys[seg+1])
			continue
		}
		t := clampf((fx-xs[seg])/h[seg], 0, 1)
		t2, t3 := t*t, t*t*t
		y := (2*t3-3*t2+1)*ys[seg] + (t3-2*t2+t)*h[seg]*m[seg] + (-2*t3+3*t2)*ys[seg+1] + (t3-t2)*h[seg]*m[seg+1]
		out = append(out, y)
	}
	return x0, out
}

func clampf(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// paintLineStyle draws setup-rush's Line style in braille: the monotone
// curve of closes (every braille column, gaps filled), a fading wash
// under it, the oldest columns dissolving into the left edge and a green
// or red tail at the head.
func (r *candleRender) paintLineStyle() {
	if r.n == 0 {
		return
	}
	H := float64(4*r.plotH - 1)
	yOf := func(p float64) float64 { return (r.fhi - r.scale(p)) / (r.fhi - r.flo) * H }
	xs, ys := make([]float64, 0, r.n), make([]float64, 0, r.n)
	for k := 0; k < r.n; k++ {
		c := r.real[r.start+k]
		x := float64(r.centerOf(k)*2 + 1)
		if k == r.n-1 && r.end == len(r.real) && !r.o.Now.IsZero() && r.ivDur > 0 {
			frac := clampf(float64(r.o.Now.Sub(time.UnixMilli(c.T)))/float64(r.ivDur), 0, 1)
			x -= (1 - frac) * float64(r.stride*2)
			if k > 0 && x <= xs[k-1] {
				x = xs[k-1] + 1
			}
		}
		xs, ys = append(xs, x), append(ys, yOf(c.C))
	}
	x0, curve := monotone(xs, ys)
	b := newBraille(r.plotW, r.plotH)
	prev := -1
	for i, y := range curve {
		x := x0 + i
		yi := int(math.Round(clampf(y, 0, H)))
		lo, hi := yi, yi
		if prev >= 0 {
			lo, hi = min(prev, yi), max(prev, yi)
		}
		for yy := lo; yy <= hi; yy++ {
			b.dot(x, yy)
		}
		prev = yi
	}
	headCol := int(xs[len(xs)-1]) / 2
	r.headX = headCol
	last := r.real[r.end-1]
	ink := r.sk.p.Loss
	if last.Up() {
		ink = r.sk.p.Gain
	}
	p := r.sk.p
	text := p.Text
	bg := p.BG.True
	// The wash under the curve: text ink from lineFillTo at the curve's
	// highest point to 0 at the floor, neutral all the way (paintLine). It
	// starts under the curve's dots: a cell whose dots sit in its lower
	// half stays clear, so the wash never shows above the line.
	topDot := 4 * r.plotH
	for cx := 0; cx < r.plotW; cx++ {
		if d := b.topDot(cx); d >= 0 {
			topDot = min(topDot, d)
		}
	}
	top := float64(topDot) / 4
	floor := float64(r.plotH)
	firstCol := int(xs[0]) / 2
	for cx := firstCol; cx <= headCol && cx < r.plotW; cx++ {
		d := b.topDot(cx)
		if d < 0 {
			continue
		}
		row0 := d / 4
		if d%4 >= 2 {
			row0++
		}
		for row := row0; row < r.plotH; row++ {
			a := r.sk.lineFillTo * clampf(1-(float64(row)+0.5-top)/math.Max(floor-top, 1), 0, 1)
			if a < 0.004 {
				break
			}
			cell := r.g.At(cx, r.plotTop+row)
			ground := washColor(theme.Mix(cell.BG.True, text.True, a))
			cell.BG = ground
			// what lies under the wash keeps its own step over the new ground
			switch {
			case cell.Layer == LGrid:
				cell.FG = p.gridOver(ground)
			case cell.Layer == LVolume && sameRGB(cell.FG, r.sk.volUp):
				cell.FG.True = theme.Mix(ground.True, p.Bull.True, 0.10)
			case cell.Layer == LVolume && sameRGB(cell.FG, r.sk.volDown):
				cell.FG.True = theme.Mix(ground.True, p.Bear.True, 0.10)
			}
		}
	}
	fade := firstCol <= 3
	tailLen := 1.2 * float64(r.stride)
	for cy := 0; cy < r.plotH; cy++ {
		for cx := 0; cx < r.plotW; cx++ {
			bits := b.bits[cy*b.w+cx]
			if bits == 0 {
				continue
			}
			col := text
			if fade && cx-firstCol < 3 {
				col = fgColor(theme.Mix(bg, text.True, [3]float64{1.0 / 6, 0.5, 5.0 / 6}[cx-firstCol]))
				col.C16 = text.C16
			}
			if dist := float64(headCol - cx); dist <= tailLen {
				t := clampf((tailLen-dist)/(tailLen-0.2*float64(r.stride)), 0, 1)
				col = fgColor(theme.Mix(text.True, ink.True, t))
				if t > 0.5 {
					col.C16 = ink.C16
				}
			}
			r.g.Set(cx, r.plotTop+cy, rune(0x2800+int(bits)), col, LCandle)
		}
	}
}

// ---- tags ----

// rushTag paints a chamfered price tag on the axis at row y: the setup-rush
// ◥ cap in the axis gap, one cell of padding, the price (aligned with the
// axis labels), padding to the right edge and the ◣ cap, so the dotted
// price line runs straight into the tag.
func (r *candleRender) rushTag(y int, text string, fill theme.Color) {
	w := r.o.Width
	x0 := r.plotW
	ink := r.sk.inkOn(fill)
	left, right := '◥', '◣'
	switch r.o.TagShape {
	case TagFlat:
		left, right = '▐', '▌'
	case TagPowerline:
		left, right = '', ''
	}
	r.g.Set(x0, y, left, fill, LLabel)
	if c := r.g.Set(x0+1, y, ' ', fill, LLabel); c != nil {
		c.BG, c.Reverse = ink, true
	}
	x := x0 + 2
	for _, ch := range text {
		if x >= w-1 {
			break
		}
		if c := r.g.Set(x, y, ch, fill, LLabel); c != nil {
			c.BG, c.Reverse, c.Bold = ink, true, true
		}
		x++
	}
	for ; x < w-1; x++ {
		if c := r.g.Set(x, y, ' ', fill, LLabel); c != nil {
			c.BG, c.Reverse = ink, true
		}
	}
	r.g.Set(w-1, y, right, fill, LLabel)
	r.reserved[y] = true
}

// paintRushLast draws the dotted last-price line from the forming bar to the
// axis and the live tag in gain or loss (core.js drawAxes), plus the bar
// countdown under it.
func (r *candleRender) paintRushLast() {
	last := r.real[r.end-1]
	up := last.C >= last.O
	fill, line := r.sk.p.Loss, r.sk.liveLoss
	if up {
		fill, line = r.sk.p.Gain, r.sk.liveGain
		if r.sk.hollowUp() {
			// terminal and rush: the landing's tag is white up, red down
			fill, line = r.sk.p.Bull, r.sk.liveBull
		}
	}
	y, _ := r.priceRow(last.C)
	r.liveRow, r.lastRow = y, y
	glyph := '┈'
	if r.sk.p.Ext != nil {
		glyph, line = '─', r.sk.p.Ext.LastLine
	}
	x0 := r.colOf(r.n-1) + r.bodyW + 1
	if r.headX >= 0 {
		x0 = r.headX + 1
	}
	for x := x0; x < r.plotW; x++ {
		r.under(x, y, glyph, line)
	}
	if r.o.QuietTag {
		x0 := r.plotW + 1
		if r.noAxis {
			x0 = r.plotW
		}
		r.g.Text(x0+1, y, r.tagText(last.C), r.sk.p.Muted, LLabel)
		r.reserved[y] = true
	} else {
		r.rushTag(y, r.tagText(last.C), fill)
	}
	if r.o.Now.IsZero() || r.o.Offset > 0 || y+1 >= r.plotTop+r.plotH || r.noAxis {
		return
	}
	closeAt := time.UnixMilli(last.T).Add(r.ivDur)
	if left := closeAt.Sub(r.o.Now); left > 0 && left <= r.ivDur {
		r.g.Text(r.plotW+2, y+1, FormatCountdown(left), r.sk.p.Muted, LLabel)
		r.reserved[y+1] = true
	}
}

// levelStyle is one HLine kind's line glyph and inks.
func (r *candleRender) levelStyle(k HLineKind) (glyph rune, line, tag theme.Color) {
	s, p := r.sk, r.sk.p
	mix := func(c theme.Color, a float64) theme.Color {
		col := fgColor(theme.Mix(p.BG.True, c.True, a))
		col.C16 = c.C16
		return col
	}
	switch k {
	case LineTP:
		return '─', mix(p.Bull, 0.6), p.Bull
	case LineStop:
		return '─', mix(p.Bear, 0.65), p.Bear
	case LineLiq:
		return '╌', mix(p.Bear, 0.35), mix(p.Bear, 0.8)
	case LineOrder:
		return '┈', s.order, s.tagMuted
	}
	return '╌', s.entry, s.tagMuted
}

// paintRushLevels draws entry, target, stop, liq and order levels: the line
// from its From bar to the axis, an axis tag in the level's ink and the
// uppercase label (core.js drawRiskBoxes).
func (r *candleRender) paintRushLevels() {
	var entry float64
	for _, hl := range r.o.Lines {
		if hl.Kind == LineEntry && hl.Price > 0 {
			entry = hl.Price
		}
	}
	card := r.o.Height <= cardMaxH
	for _, hl := range r.o.Lines {
		if hl.Price <= 0 || math.IsNaN(hl.Price) || math.IsInf(hl.Price, 0) {
			continue
		}
		glyph, line, tagCol := r.levelStyle(hl.Kind)
		y, inside := r.priceRow(hl.Price)
		x0 := r.fromCol(hl.From)
		if x0 < 0 {
			continue
		}
		label := strings.ToUpper(strings.TrimSpace(hl.Label))
		if i := strings.IndexByte(label, '|'); i >= 0 {
			label = strings.TrimSpace(label[:i])
		}
		if label == "" && hl.Kind != LineEntry {
			label = strings.ToUpper(string(hl.Kind))
		}
		if !inside {
			arrow := r.cs.up
			if r.scale(hl.Price) < r.flo {
				arrow = r.cs.down
			}
			label = string(arrow) + " " + label
			tagCol = fgColor(theme.Mix(r.sk.p.BG.True, tagCol.True, 0.55))
		} else {
			for x := x0; x < r.plotW; x++ {
				r.under(x, y, glyph, line)
			}
		}
		if !r.noAxis {
			ty := -1
			for _, d := range []int{0, -1, 1, -2, 2} {
				t := y + d
				if t >= r.plotTop && t < r.plotTop+r.plotH && !r.reserved[t] && absi(t-r.liveRow) >= 2 {
					ty = t
					break
				}
			}
			if ty >= 0 && !(hl.Kind == LineEntry && hl.Label == "") {
				r.rushTag(ty, r.tagText(hl.Price), tagCol)
			}
		}
		if card {
			continue
		}
		// The label sits right-aligned inside the plot, on the side away
		// from the entry, and never on the live price row.
		labCol := tagCol
		if hl.Kind == LineEntry || hl.Kind == LineOrder {
			labCol = r.sk.p.Muted
		}
		dirs := []int{-1, 1}
		if entry > 0 && hl.Price < entry {
			dirs = []int{1, -1}
		}
		if !inside {
			dirs = []int{0}
		}
		x1 := r.plotW - 1
		lx := x1 - runeLen(label)
		if lx < 1 || label == "" {
			continue
		}
		for _, d := range dirs {
			ly := y + d
			if ly < r.plotTop || ly >= r.plotTop+r.plotH || ly == r.liveRow || !r.free(lx, x1, ly) {
				continue
			}
			r.g.Text(lx, ly, label, labCol, LLabel)
			for x := lx; x < x1; x++ {
				r.g.At(x, ly).Bold = true
			}
			r.claim(lx, x1, ly)
			break
		}
	}
}

// under draws a line glyph over grid, volume and washes but never over a
// candle or a label.
func (r *candleRender) under(x, y int, ch rune, fg theme.Color) {
	if c := r.g.At(x, y); c != nil && c.Layer < LCandle {
		c.Ch, c.FG, c.Bold, c.Reverse, c.Layer = ch, fg, false, false, LOverlay
	}
}

// free reports whether cells [x0,x1) of row y hold no candle and no label.
func (r *candleRender) free(x0, x1, y int) bool {
	for x := x0; x < x1; x++ {
		c := r.g.At(x, y)
		if c == nil || c.Layer >= LCandle {
			return false
		}
	}
	for _, s := range r.labelRows[y] {
		if x0 < s[1]+1 && s[0] < x1+1 {
			return false
		}
	}
	return true
}

func (r *candleRender) claim(x0, x1, y int) { r.labelRows[y] = append(r.labelRows[y], [2]int{x0, x1}) }

// paintRushMarkers draws pattern marks (a bracket under the lows or over the
// highs, and the pattern's name: core.js drawSetupMarks) for markers with a
// label, and ▲/▼ for plain buy/sell markers.
func (r *candleRender) paintRushMarkers() {
	if len(r.o.Markers) == 0 {
		return
	}
	idx := make(map[int64]int, r.n)
	for k := 0; k < r.n; k++ {
		idx[r.draw[r.start+k].T] = k
	}
	named := 0
	for i := len(r.o.Markers) - 1; i >= 0; i-- {
		if r.o.Markers[i].Label != "" {
			if _, ok := idx[r.o.Markers[i].T]; ok {
				named++
			}
		}
	}
	bottom := r.plotTop + r.plotH - 1
	for _, m := range r.o.Markers {
		k1, ok := idx[m.T]
		if !ok {
			continue
		}
		ink := r.sk.p.Bear
		if m.Buy {
			ink = r.sk.p.Bull
		}
		if m.Label == "" {
			c := r.draw[r.start+k1]
			x := r.centerOf(k1)
			if m.Buy {
				y, _ := r.priceRow(c.L)
				for yy := y + 1; yy <= bottom; yy++ {
					if cell := r.g.At(x, yy); cell != nil && cell.Layer < LCandle {
						r.g.Set(x, yy, r.cs.up, ink, LCandle)
						break
					}
				}
				continue
			}
			y, _ := r.priceRow(c.H)
			for yy := y - 1; yy >= r.plotTop; yy-- {
				if cell := r.g.At(x, yy); cell != nil && cell.Layer < LCandle {
					r.g.Set(x, yy, r.cs.down, ink, LCandle)
					break
				}
			}
			continue
		}
		named--
		k0 := k1
		if m.From > 0 {
			if k, ok := idx[m.From]; ok && k <= k1 {
				k0 = k
			}
		}
		lo, hi := math.Inf(1), math.Inf(-1)
		for k := k0; k <= k1; k++ {
			c := r.draw[r.start+k]
			lo, hi = math.Min(lo, c.L), math.Max(hi, c.H)
		}
		x0, x1 := r.colOf(k0), r.colOf(k1)+r.bodyW // [x0, x1)
		if x1-x0 < 2 {
			x1 = x0 + 2
		}
		var by, ny int
		if m.Buy {
			y, _ := r.priceRow(lo)
			by, ny = y+1, y+2
		} else {
			y, _ := r.priceRow(hi)
			by, ny = y-1, y-2
		}
		if by < r.plotTop || by > bottom {
			continue
		}
		br, nm, old := r.sk.brBear, r.sk.nameBear, r.sk.oldBear
		if m.Buy {
			br, nm, old = r.sk.brBull, r.sk.nameBull, r.sk.oldBull
		}
		_ = ink
		if named >= 2 {
			// older setups keep a tick only
			cx := (x0 + x1 - 1) / 2
			if c := r.g.At(cx, by); c != nil && c.Layer < LCandle {
				r.g.Set(cx, by, '▪', old, LLabel)
			}
			continue
		}
		for x := x0; x < x1; x++ {
			ch := '─'
			switch {
			case x == x0 && m.Buy:
				ch = '└'
			case x == x1-1 && m.Buy:
				ch = '┘'
			case x == x0:
				ch = '┌'
			case x == x1-1:
				ch = '┐'
			}
			if c := r.g.At(x, by); c != nil && c.Layer < LCandle {
				r.g.Set(x, by, ch, br, LLabel)
			}
		}
		if ny < r.plotTop || ny > bottom {
			continue
		}
		name := patternName(m.Label)
		// the whole name, else its first word; never initials nobody reads
		cands := []string{name}
		if f := strings.Fields(name); len(f) > 1 {
			cands = append(cands, f[0])
		}
		for _, s := range cands {
			w := runeLen(s)
			if w == 0 {
				continue
			}
			lx := clampi((x0+x1)/2-w/2, 0, max(r.plotW-1-w, 0))
			if !r.free(lx, lx+w, ny) {
				continue
			}
			r.g.Text(lx, ny, s, nm, LLabel)
			for x := lx; x < lx+w; x++ {
				r.g.At(x, ny).Bold = true
			}
			r.claim(lx, lx+w, ny)
			break
		}
	}
}

// patternName strips "bullish"/"bearish", turns dashes into spaces and
// uppercases (core.js drawSetupMarks).
func patternName(s string) string {
	f := strings.Fields(strings.NewReplacer("-", " ", "_", " ").Replace(s))
	out := f[:0]
	for _, w := range f {
		lw := strings.ToLower(w)
		if lw == "bullish" || lw == "bearish" {
			continue
		}
		out = append(out, strings.ToUpper(w))
	}
	return strings.Join(out, " ")
}

// paintRushTicks writes the grid prices on axis rows no tag has claimed.
func (r *candleRender) paintRushTicks() {
	if r.noAxis {
		return
	}
	// Labels keep a row of air from a tag; when that would leave fewer
	// than three, a label may sit right beside a tag (only the tag's own
	// row is never shared), so a compact chart still shows its scale.
	pick := func(strict bool) []int {
		var ys []int
		for _, t := range r.ticks {
			y, ok := r.priceRow(t)
			if !ok || r.reserved[y] || y >= r.plotTop+r.plotH || strict && (r.reserved[y-1] || r.reserved[y+1]) {
				ys = append(ys, -1)
				continue
			}
			ys = append(ys, y)
		}
		return ys
	}
	count := func(ys []int) int {
		n := 0
		for _, y := range ys {
			if y >= 0 {
				n++
			}
		}
		return n
	}
	ys := pick(true)
	if count(ys) < 3 {
		ys = pick(false)
	}
	for i, t := range r.ticks {
		if ys[i] >= 0 {
			r.g.Text(r.plotW+2, ys[i], rushPrice(t, r.step), r.sk.p.Muted, LLabel)
		}
	}
}

// paintRushCrosshair tints the cursor candle's columns, draws quiet guides
// through its centre and close, and tags the axis and the time row.
func (r *candleRender) paintRushCrosshair() {
	if r.o.Cursor < 0 || r.o.Cursor >= r.n {
		return
	}
	k := r.n - 1 - r.o.Cursor
	r.cursorIndex = r.start + k
	c := r.real[r.cursorIndex]
	cx := r.centerOf(k)
	for y := r.plotTop; y < r.plotTop+r.plotH; y++ {
		for bx := 0; bx < r.bodyW; bx++ {
			if cell := r.g.At(r.colOf(k)+bx, y); cell != nil && sameRGB(cell.BG, r.sk.p.BG) {
				cell.BG = r.sk.cursor
			}
		}
		if cell := r.g.At(cx, y); cell != nil && cell.Layer <= LGrid {
			r.g.Set(cx, y, '┊', r.sk.guide, LGrid)
		}
	}
	y, _ := r.priceRow(c.C)
	for x := 0; x < r.plotW; x++ {
		if cell := r.g.At(x, y); cell != nil && cell.Layer <= LGrid {
			r.g.Set(x, y, '┈', r.sk.guide, LOverlay)
		}
	}
	if y != r.liveRow && !r.noAxis {
		r.rushTag(y, r.tagText(c.C), r.sk.tagMuted)
	}
	if r.timeRows == 0 {
		return
	}
	format := "Mon 02 Jan 15:04"
	if r.ivDur >= day {
		format = "Mon 02 Jan 2006"
	}
	tl := " " + strings.ToUpper(time.UnixMilli(c.T).In(r.loc).Format(format)) + " "
	ty := r.axisRow
	x0 := clampi(cx-len(tl)/2, 0, max(r.plotW-len(tl), 0))
	for x := 0; x < r.plotW; x++ {
		if cell := r.g.At(x, ty); cell != nil && x >= x0-2 && x < x0+len(tl)+2 {
			cell.Ch, cell.Bold, cell.Reverse = ' ', false, false
		}
	}
	// whole labels only: never leave "18:" beside the tag
	blank := func(x int) bool { cell := r.g.At(x, ty); return cell == nil || cell.Ch == ' ' }
	for x := x0 - 3; x >= 0 && !blank(x); x-- {
		r.g.At(x, ty).Ch = ' '
	}
	for x := x0 + len(tl) + 2; x < r.plotW && !blank(x); x++ {
		r.g.At(x, ty).Ch = ' '
	}
	ink := r.sk.inkOn(r.sk.tagMuted)
	i := 0
	for _, ch := range tl {
		if cell := r.g.Set(x0+i, ty, ch, r.sk.tagMuted, LLabel); cell != nil {
			cell.BG, cell.Reverse, cell.Bold = ink, true, true
		}
		i++
	}
}
