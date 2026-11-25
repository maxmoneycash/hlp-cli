package chart

import (
	"math"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/theme"
)

// lowerBlocks[k] fills the bottom k/8 of a cell (index 0 = blank).
var lowerBlocks = [9]rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// leftBlocks[k] fills the left k/8 of a cell (index 0 = blank).
var leftBlocks = [9]rune{' ', '▏', '▎', '▍', '▌', '▋', '▊', '▉', '█'}

// charset holds chrome glyphs so the ASCII style never emits non-7-bit bytes.
type charset struct {
	gridH, gridV, last, axisV, axisTick, axisH, tickDown, corner, hline rune
	up, down, live                                                      rune
}

var (
	unicodeChars = charset{'┈', '┊', '┄', '│', '├', '─', '┬', '┘', '╌', '▲', '▼', '▶'}
	asciiChars   = charset{' ', ' ', '-', '|', '+', '-', '+', '+', '-', '^', 'v', '>'}
)

// candlePainter draws one candle into the plot. x is the body's left column.
type candlePainter struct {
	g       *Grid
	style   Style
	bodyW   int
	plotH   int
	units   func(p float64, perCell int) int
	rowY    func(r int) int
	wickMix float64
	th      theme.Theme
}

func (cp *candlePainter) draw(x int, c domain.Candle, col theme.Color) {
	switch cp.style {
	case Lines:
		cp.drawLines(x+cp.bodyW/2, c, col, '┃')
	case Mono:
		body := '┃'
		if c.Up() {
			body = '║'
		}
		cp.drawLines(x+cp.bodyW/2, c, cp.th.Text, body)
	case Half:
		cp.drawHalf(x, c, col, tint(cp.th, col, 0.55))
	case ASCII:
		cp.drawASCII(x+cp.bodyW/2, c)
	default:
		cp.drawBlocks(x, c, col)
	}
}

// bodySpan returns the body and wick spans in sub-cell units, giving a doji
// one unit so it is always visible.
func (cp *candlePainter) bodySpan(c domain.Candle, per int) (bl, bh, wl, wh int) {
	bl, bh = cp.units(math.Min(c.O, c.C), per), cp.units(math.Max(c.O, c.C), per)
	wl, wh = cp.units(c.L, per), cp.units(c.H, per)
	if bh == bl {
		if bh < cp.plotH*per {
			bh++
		} else {
			bl--
		}
	}
	return
}

// drawBlocks renders one candle with eighth-block precision.
//
// The plot has 8*plotH units. For row r (0 = bottom) the cell covers
// [8r, 8r+8). Body [bl,bh) and wick [wl,wh) are intersected with the cell to
// get local (a,b) in 0..8 and the glyph comes from bodyGlyph / wickGlyph.
// Wick flags are per candle, so every column of a wide body rounds the same
// way (no notches); the wick and its doji crossings live in the center column.
func (cp *candlePainter) drawBlocks(x int, c domain.Candle, col theme.Color) {
	cp.drawBlocksW(x, c, col, col)
}

// drawBlocksW is drawBlocks with a separate wick ink (setup-rush candy and
// neon wicks differ from their bodies). Cells that show only the wick line
// take wick; every cell holding body takes col.
func (cp *candlePainter) drawBlocksW(x int, c domain.Candle, col, wick theme.Color) {
	bl, bh, wl, wh := cp.bodySpan(c, 8)
	cx := x + cp.bodyW/2
	r0, r1 := max(min(wl, bl)/8, 0), min((max(wh, bh)+7)/8, cp.plotH)
	type bodyRow struct {
		y, share int
		ch       rune
		rev      bool
		up, down bool // the candle continues above / below this cell
	}
	var buf [16]bodyRow // most candles span a few rows: keep them off the heap
	rows := buf[:0]
	visible := false // some row shows the body as more than a wick line
	for r := r0; r < r1; r++ {
		c0, c1 := 8*r, 8*r+8
		a, b := max(bl, c0)-c0, min(bh, c1)-c0
		wa, wb := max(wl, c0)-c0, min(wh, c1)-c0
		hasBody, hasWick := b > a, wb > wa
		if !hasBody && !hasWick {
			continue
		}
		y := cp.rowY(r)
		if !hasBody {
			if ch := wickGlyph(wa, wb); ch != 0 {
				cp.g.Set(cx, y, ch, wick, LCandle)
			}
			continue
		}
		wickAbove := wh > bh && b < 8 && bh < c1
		wickBelow := wl < bl && a > 0 && bl > c0
		ch, rev := bodyGlyph(a, b, wickAbove, wickBelow)
		visible = visible || ch != '│'
		rows = append(rows, bodyRow{y: y, share: b - a, ch: ch, rev: rev,
			up: max(wh, bh) > c1, down: min(wl, bl) < c0})
	}
	// A short body split across a cell boundary, with wicks past both
	// ends, can round to a bare wick in every row. Never lose a body: draw
	// it as a thin bar in the row holding most of it.
	if !visible && len(rows) > 0 {
		best := 0
		for i, br := range rows {
			if br.share > rows[best].share {
				best = i
			}
		}
		br := &rows[best]
		switch {
		case br.up && br.down:
			br.ch = '┿'
		case br.up:
			br.ch = '┷'
		case br.down:
			br.ch = '┯'
		default:
			br.ch = '━'
		}
		br.rev = false
	}
	for _, br := range rows {
		for bx := 0; bx < cp.bodyW; bx++ {
			xx, gl := x+bx, br.ch
			if xx != cx {
				switch gl {
				case '│':
					continue // the wick belongs to the center column only
				case '┿', '┷', '┯':
					gl = '━'
				}
			}
			ink := col
			if gl == '│' {
				ink = wick
			}
			if cell := cp.g.Set(xx, br.y, gl, ink, LCandle); cell != nil {
				cell.Reverse = br.rev
			}
		}
	}
}

// bodyGlyph returns the glyph for a body covering [a,b) eighths of a cell.
// rev=true means "draw with SGR 7": the glyph is a lower block painted in the
// background color while the cell shows the body color above it. That is how
// upper 1/8..7/8 blocks are drawn: Unicode only has ▀ (4/8) and ▔ (1/8), and
// the Legacy Computing upper blocks are missing from most terminal fonts.
// Where a wick continues past an edge the edge rounds to the whole cell,
// because no glyph holds both a partial block and a thin line.
func bodyGlyph(a, b int, wickAbove, wickBelow bool) (rune, bool) {
	n := b - a
	switch {
	case a == 0 && b == 8:
		return '█', false
	case a > 0 && b < 8: // body starts and ends inside this cell
		if n >= 6 {
			return '█', false
		}
		if n <= 2 { // thin body / doji: heavy bar that keeps wick continuity
			switch {
			case wickAbove && wickBelow:
				return '┿', false
			case wickAbove:
				return '┷', false
			case wickBelow:
				return '┯', false
			}
			return '━', false
		}
		if a+b >= 8 {
			return '▀', false
		}
		return '▄', false
	case a == 0: // top edge of the body: body fills the bottom b/8
		if wickAbove {
			if b >= 4 {
				return '█', false
			}
			return '│', false
		}
		return lowerBlocks[b], false
	default: // b == 8: bottom edge, body fills the top 8-a
		if wickBelow {
			if a <= 4 {
				return '█', false
			}
			return '│', false
		}
		switch a {
		case 4:
			return '▀', false
		case 7:
			return '▔', false
		}
		return lowerBlocks[a], true
	}
}

// wickGlyph covers [wa,wb) eighths with light box drawing at half-cell
// precision (thresholds at 2/8 and 6/8). 0 means "draw nothing".
func wickGlyph(wa, wb int) rune {
	switch {
	case wa == 0 && wb == 8:
		return '│'
	case wa == 0: // the wick ends inside this cell going up
		if wb >= 6 {
			return '│'
		}
		if wb >= 2 {
			return '╷'
		}
		return 0
	case wb == 8: // the wick starts inside this cell
		if wa <= 2 {
			return '│'
		}
		if wa <= 6 {
			return '╵'
		}
		return 0
	}
	if wb-wa >= 5 {
		return '│'
	}
	if wa+wb >= 8 {
		return '╵'
	}
	return '╷'
}

// drawLines draws half-cell box-drawing candles. Each cell has a lower and
// an upper half, each none / wick / body; the 3x3 table maps to a glyph.
// body '║' draws hollow (up) candles for the no-color mono style.
func (cp *candlePainter) drawLines(x int, c domain.Candle, col theme.Color, body rune) {
	bl, bh, wl, wh := cp.bodySpan(c, 2)
	kind := func(h int) int {
		switch {
		case h >= bl && h < bh:
			return 2
		case h >= wl && h < wh:
			return 1
		}
		return 0
	}
	r0, r1 := max(min(wl, bl)/2, 0), min((max(wh, bh)+1)/2, cp.plotH)
	for r := r0; r < r1; r++ {
		ch := linesGlyph(kind(2*r), kind(2*r+1), body)
		if ch != 0 {
			cp.g.Set(x, cp.rowY(r), ch, col, LCandle)
		}
	}
}

// linesGlyph maps (lower half, upper half) kinds (0 none, 1 wick, 2 body)
// to a box-drawing glyph. Double-line half glyphs do not exist, so a hollow
// body uses ║ for any half that contains body.
func linesGlyph(lo, up int, body rune) rune {
	if body == '║' && (lo == 2 || up == 2) {
		return '║'
	}
	switch [2]int{lo, up} {
	case [2]int{2, 2}:
		return body
	case [2]int{1, 1}:
		return '│'
	case [2]int{2, 1}:
		return '╽'
	case [2]int{1, 2}:
		return '╿'
	case [2]int{2, 0}:
		return '╻'
	case [2]int{0, 2}:
		return '╹'
	case [2]int{1, 0}:
		return '╷'
	case [2]int{0, 1}:
		return '╵'
	}
	return 0
}

// drawHalf draws half-block "pixel" candles: two vertical pixels per cell
// (fg = upper, bg = lower), the wick in a dimmer tint of the body color.
func (cp *candlePainter) drawHalf(x int, c domain.Candle, col, wick theme.Color) {
	bl, bh, wl, wh := cp.bodySpan(c, 2)
	px := func(h int) (theme.Color, bool) {
		switch {
		case h >= bl && h < bh:
			return col, true
		case h >= wl && h < wh:
			return wick, true
		}
		return theme.Color{}, false
	}
	r0, r1 := max(min(wl, bl)/2, 0), min((max(wh, bh)+1)/2, cp.plotH)
	for r := r0; r < r1; r++ {
		lc, lok := px(2 * r)
		uc, uok := px(2*r + 1)
		for bx := 0; bx < cp.bodyW; bx++ {
			xx, y := x+bx, cp.rowY(r)
			switch {
			case lok && uok && lc == uc:
				cp.g.Set(xx, y, '█', lc, LCandle)
			case lok && uok:
				if cell := cp.g.Set(xx, y, '▀', uc, LCandle); cell != nil {
					cell.BG = lc
				}
			case uok:
				cp.g.Set(xx, y, '▀', uc, LCandle)
			case lok:
				cp.g.Set(xx, y, '▄', lc, LCandle)
			}
		}
	}
}

// drawASCII draws 7-bit candles: 'O' up bodies, '#' down bodies, '|' wicks.
func (cp *candlePainter) drawASCII(x int, c domain.Candle) {
	bl, bh := cp.units(math.Min(c.O, c.C), 1), cp.units(math.Max(c.O, c.C), 1)
	wl, wh := cp.units(c.L, 1), cp.units(c.H, 1)
	body := '#'
	if c.Up() {
		body = 'O'
	}
	for r := max(min(wl, bl), 0); r <= min(max(wh, bh), cp.plotH-1); r++ {
		switch {
		case r >= bl && (r < bh || r == bl):
			cp.g.Set(x, cp.rowY(r), body, theme.Color{}, LCandle)
		case r >= wl && r <= wh && wh > wl:
			cp.g.Set(x, cp.rowY(r), '|', theme.Color{}, LCandle)
		}
	}
}

// braille is a 2x4-dots-per-cell canvas for overlay lines. Dots are written
// only into cells whose layer is at most LOverlay, so lines pass behind
// candles, and they OR into an existing braille glyph so two lines sharing a
// cell keep both sets of dots (the later line's color wins).
type braille struct {
	w, h int // in cells
	bits []uint8
}

var dotBit = [2][4]uint8{{0x01, 0x02, 0x04, 0x40}, {0x08, 0x10, 0x20, 0x80}}

func newBraille(w, h int) *braille { return &braille{w: w, h: h, bits: make([]uint8, w*h)} }

func (b *braille) reset() { clear(b.bits) }

// dot sets one dot at dot coordinates (x right, y down).
func (b *braille) dot(x, y int) {
	if x < 0 || y < 0 || x >= 2*b.w || y >= 4*b.h {
		return
	}
	b.bits[(y/4)*b.w+x/2] |= dotBit[x%2][y%4]
}

// line draws a Bresenham segment between two dot positions.
func (b *braille) line(x0, y0, x1, y1 int) {
	dx, dy := absi(x1-x0), -absi(y1-y0)
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	// Clip absurd spans (a value far outside the range) to keep this O(cells).
	if dx > 8*b.w || -dy > 16*b.h {
		return
	}
	err := dx + dy
	for {
		b.dot(x0, y0)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

// polyline connects points; NaN y values break the line. Points that land
// on the same dot column are averaged first, so a long series squeezed into
// a narrow plot draws one thin line (one dot per column), not a 2-dot-thick
// ladder of every point's jitter.
func (b *braille) polyline(xs []int, ys []float64) {
	px, py := 0, 0
	have := false
	emit := func(x int, y float64) {
		yy := int(math.Round(y))
		if have {
			b.line(px, py, x, yy)
		} else {
			b.dot(x, yy)
		}
		px, py, have = x, yy, true
	}
	cx, sum, n := 0, 0.0, 0
	flushCol := func() {
		if n > 0 {
			emit(cx, sum/float64(n))
		}
		n, sum = 0, 0
	}
	for i := range xs {
		if math.IsNaN(ys[i]) {
			flushCol()
			have = false
			continue
		}
		if n > 0 && xs[i] != cx {
			flushCol()
		}
		cx = xs[i]
		sum += ys[i]
		n++
	}
	flushCol()
}

// flush writes the canvas into g with its top-left cell at (ox,oy).
func (b *braille) flush(g *Grid, ox, oy int, col theme.Color) {
	for cy := 0; cy < b.h; cy++ {
		for cx := 0; cx < b.w; cx++ {
			bits := b.bits[cy*b.w+cx]
			if bits == 0 {
				continue
			}
			c := g.At(ox+cx, oy+cy)
			if c == nil || c.Layer > LOverlay {
				continue
			}
			if c.Layer == LOverlay && c.Ch >= 0x2800 && c.Ch <= 0x28FF {
				bits |= uint8(c.Ch - 0x2800)
			}
			g.Set(ox+cx, oy+cy, rune(0x2800+int(bits)), col, LOverlay)
		}
	}
}

// topDot returns the smallest dot row set in cell column cx, or -1.
func (b *braille) topDot(cx int) int {
	for cy := 0; cy < b.h; cy++ {
		bits := b.bits[cy*b.w+cx]
		if bits == 0 {
			continue
		}
		for row := 0; row < 4; row++ {
			if bits&(dotBit[0][row]|dotBit[1][row]) != 0 {
				return cy*4 + row
			}
		}
	}
	return -1
}

// tint returns c mixed toward the theme background by (1-t): t=1 is c,
// t=0 is the background. The 16-color pick stays c's own (tints of a hue
// read as that hue at 16 colors); the 256-color pick keeps the hue
// (theme.Hue256).
func tint(th theme.Theme, c theme.Color, t float64) theme.Color {
	if !c.True.Valid || !th.BG.True.Valid {
		return c
	}
	mixed := theme.Mix(th.BG.True, c.True, t)
	// At 256 colors pick a cube entry that keeps the hue: a converter
	// would drop dark tints (down-volume bars) into the gray ramp.
	return theme.Color{True: mixed, C256: theme.Hue256(mixed), C16: c.C16}
}

// bgTint is like tint but omitted at 16 colors (for background washes,
// which would otherwise become full-strength color blocks).
func bgTint(th theme.Theme, c theme.Color, t float64) theme.Color {
	col := tint(th, c, t)
	col.C16 = -2
	return col
}

func absi(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func clampi(v, lo, hi int) int { return max(lo, min(hi, v)) }
