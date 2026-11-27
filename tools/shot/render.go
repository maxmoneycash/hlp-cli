package main

// render.go paints a grid onto an RGBA image. Text goes through the font
// set; block elements, box drawing, braille, the ◢◣◤◥ tag corners and the
// powerline arrows are drawn geometrically to the exact cell bounds, the way
// GPU terminals (Ghostty, Kitty, WezTerm) do, so adjoining cells meet with no
// seams.

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"unicode/utf8"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

func fix(v fixed.Int26_6) float64 { return float64(v) / 64 }

type painter struct {
	img    *image.RGBA
	fs     *fontSet
	fg, bg color.RGBA // terminal defaults
	glass  float64    // opacity of the default ground (0 or 1 = opaque)
	pal    [16]color.RGBA
	lw     int // light stroke width in px
}

// ansi16 is a balanced, modern 16-colour palette (close to Ghostty's and
// iTerm2's defaults) for apps that use indexed colours.
var ansi16 = [16]string{
	"#1d1f21", "#e5534b", "#57ab5a", "#c69026", "#539bf5", "#b083f0", "#39c5cf", "#c5c8c6",
	"#636e7b", "#ff6a69", "#6bc46d", "#daaa3f", "#6cb6ff", "#dcbdfb", "#56d4dd", "#ffffff",
}

func hexColor(s string) (color.RGBA, bool) {
	if len(s) > 0 && s[0] == '#' {
		s = s[1:]
	}
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 && len(s) != 8 {
		return color.RGBA{}, false
	}
	var v [4]uint8
	v[3] = 255
	for i := 0; i < len(s)/2; i++ {
		var b uint8
		for _, ch := range s[i*2 : i*2+2] {
			b <<= 4
			switch {
			case ch >= '0' && ch <= '9':
				b |= uint8(ch - '0')
			case ch >= 'a' && ch <= 'f':
				b |= uint8(ch - 'a' + 10)
			case ch >= 'A' && ch <= 'F':
				b |= uint8(ch - 'A' + 10)
			default:
				return color.RGBA{}, false
			}
		}
		v[i] = b
	}
	return color.RGBA{v[0], v[1], v[2], v[3]}, true
}

func mustHex(s string) color.RGBA { c, _ := hexColor(s); return c }

func (p *painter) resolve(c tcolor, def color.RGBA) color.RGBA {
	switch c.kind {
	case colRGB:
		return c.rgb
	case colIndex:
		i := int(c.idx)
		if i < 16 {
			return p.pal[i]
		}
		if i < 232 {
			i -= 16
			lv := func(n int) uint8 {
				if n == 0 {
					return 0
				}
				return uint8(55 + n*40)
			}
			return color.RGBA{lv(i / 36), lv(i / 6 % 6), lv(i % 6), 255}
		}
		g := uint8(8 + (i-232)*10)
		return color.RGBA{g, g, g, 255}
	}
	return def
}

func mix(a, b color.RGBA, t float64) color.RGBA {
	l := func(x, y uint8) uint8 { return uint8(math.Round(float64(x)*(1-t) + float64(y)*t)) }
	return color.RGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), 255}
}

// ground is the default background as the window paints it: translucent
// (premultiplied) when the terminal is see-through, like Warp or Ghostty
// over a wallpaper.
func (p *painter) ground() color.RGBA {
	if p.glass <= 0 || p.glass >= 1 {
		return p.bg
	}
	m := func(v uint8) uint8 { return uint8(math.Round(float64(v) * p.glass)) }
	return color.RGBA{m(p.bg.R), m(p.bg.G), m(p.bg.B), uint8(math.Round(255 * p.glass))}
}

func (p *painter) fill(x0, y0, x1, y1 int, c color.RGBA) {
	r := image.Rect(x0, y0, x1, y1).Intersect(p.img.Bounds())
	if r.Empty() {
		return
	}
	draw.Draw(p.img, r, image.NewUniform(c), image.Point{}, draw.Src)
}

func (p *painter) blend(x0, y0, x1, y1 int, c color.RGBA, a float64) {
	r := image.Rect(x0, y0, x1, y1).Intersect(p.img.Bounds())
	mask := image.NewUniform(color.Alpha{uint8(math.Round(a * 255))})
	draw.DrawMask(p.img, r, image.NewUniform(c), image.Point{}, mask, image.Point{}, draw.Over)
}

// poly fills antialiased polygons (each a closed list of points) clipped to
// the rect (x0,y0)-(x1,y1).
func (p *painter) poly(x0, y0, x1, y1 int, c color.RGBA, polys ...[][2]float64) {
	w, h := x1-x0, y1-y0
	if w <= 0 || h <= 0 {
		return
	}
	z := vector.NewRasterizer(w, h)
	for _, pts := range polys {
		if len(pts) < 3 {
			continue
		}
		z.MoveTo(float32(pts[0][0]-float64(x0)), float32(pts[0][1]-float64(y0)))
		for _, q := range pts[1:] {
			z.LineTo(float32(q[0]-float64(x0)), float32(q[1]-float64(y0)))
		}
		z.ClosePath()
	}
	z.Draw(p.img, image.Rect(x0, y0, x1, y1), image.NewUniform(c), image.Point{})
}

func circle(cx, cy, r float64) [][2]float64 {
	n := 24
	pts := make([][2]float64, n)
	for i := range pts {
		a := 2 * math.Pi * float64(i) / float64(n)
		pts[i] = [2]float64{cx + r*math.Cos(a), cy + r*math.Sin(a)}
	}
	return pts
}

// strokePath returns the outline polygon of a smooth open polyline stroked
// with width t (butt caps).
func strokePath(pts [][2]float64, t float64) [][2]float64 {
	n := len(pts)
	left := make([][2]float64, n)
	right := make([][2]float64, n)
	for i := range pts {
		var dx, dy float64
		switch {
		case i == 0:
			dx, dy = pts[1][0]-pts[0][0], pts[1][1]-pts[0][1]
		case i == n-1:
			dx, dy = pts[i][0]-pts[i-1][0], pts[i][1]-pts[i-1][1]
		default:
			dx, dy = pts[i+1][0]-pts[i-1][0], pts[i+1][1]-pts[i-1][1]
		}
		l := math.Hypot(dx, dy)
		if l == 0 {
			l = 1
		}
		nx, ny := -dy/l*t/2, dx/l*t/2
		left[i] = [2]float64{pts[i][0] + nx, pts[i][1] + ny}
		right[i] = [2]float64{pts[i][0] - nx, pts[i][1] - ny}
	}
	out := append([][2]float64{}, left...)
	for i := n - 1; i >= 0; i-- {
		out = append(out, right[i])
	}
	return out
}

// draw paints the whole grid with its top-left cell at (ox, oy).
func (p *painter) draw(g *grid, ox, oy int) {
	cw, ch := p.fs.cellW, p.fs.cellH
	type item struct {
		x, y, span int
		c          *cell
		fg, bg     color.RGBA
	}
	var items []item
	// Pass 1: backgrounds, so glyphs that overhang a neighbour are not cut.
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			c := g.at(x, y)
			if c.cont {
				continue
			}
			fg := p.resolve(c.attrs.fg, p.fg)
			bg := p.resolve(c.attrs.bg, p.bg)
			if c.attrs.rev {
				fg, bg = bg, fg
			}
			if c.attrs.dim {
				fg = mix(fg, bg, 0.45)
			}
			span := 1
			if c.wide && x+1 < g.w {
				span = 2
			}
			px, py := ox+x*cw, oy+y*ch
			if p.glass > 0 && p.glass < 1 && c.attrs.bg.kind == colDefault && !c.attrs.rev {
				p.fill(px, py, px+span*cw, py+ch, p.ground())
			} else {
				p.fill(px, py, px+span*cw, py+ch, bg)
			}
			items = append(items, item{x, y, span, c, fg, bg})
		}
	}
	// Pass 2: glyphs and decorations.
	for _, it := range items {
		c := it.c
		px, py := ox+it.x*cw, oy+it.y*ch
		w := it.span * cw
		if c.text != "" && c.text != " " && !c.attrs.hide {
			r, _ := utf8.DecodeRuneInString(c.text)
			if !p.special(r, px, py, w, ch, it.fg, it.bg) {
				p.text(c, px, py, w, it.fg)
			}
		}
		if c.attrs.under {
			ul := it.fg
			if c.attrs.ulColor.kind != colDefault {
				ul = p.resolve(c.attrs.ulColor, it.fg)
			}
			y := py + int(p.fs.ascent) + max(p.lw, int(math.Round(p.fs.px*0.12)))
			p.fill(px, y, px+w, y+p.lw, ul)
		}
		if c.attrs.strike {
			y := py + int(p.fs.ascent-p.fs.px*0.3)
			p.fill(px, y, px+w, y+p.lw, it.fg)
		}
	}
}

func (p *painter) text(c *cell, px, py, w int, fg color.RGBA) {
	st := stRegular
	switch {
	case c.attrs.bold && c.attrs.italic:
		st = stBoldItalic
	case c.attrs.bold:
		st = stBold
	case c.attrs.italic:
		st = stItalic
	}
	r, _ := utf8.DecodeRuneInString(c.text)
	face, fakeBold := p.fs.pick(r, st)
	adv := fix(font.MeasureString(face, c.text))
	// A fallback glyph wider than its cells is drawn smaller rather than
	// spilling into the neighbour.
	if adv > float64(w)+0.5 {
		face = p.fs.scaled(face, float64(w)/adv)
		adv = fix(font.MeasureString(face, c.text))
	}
	x := float64(px) + (float64(w)-adv)/2
	y := float64(py) + p.fs.ascent
	d := &font.Drawer{Dst: p.img, Src: image.NewUniform(fg), Face: face,
		Dot: fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(y * 64)}}
	d.DrawString(c.text)
	if fakeBold {
		off := math.Max(1, p.fs.px/24)
		d.Dot = fixed.Point26_6{X: fixed.Int26_6((x + off) * 64), Y: fixed.Int26_6(y * 64)}
		d.DrawString(c.text)
	}
}

// boxArms gives up/right/down/left arm styles for U+2500..U+257F
// (0 none, 1 light, 2 heavy, 3 double), as a 4-digit "URDL" string.
var boxArms = map[rune]string{
	0x2500: "0101", 0x2501: "0202", 0x2502: "1010", 0x2503: "2020",
	0x250C: "0110", 0x250D: "0210", 0x250E: "0120", 0x250F: "0220",
	0x2510: "0011", 0x2511: "0012", 0x2512: "0021", 0x2513: "0022",
	0x2514: "1100", 0x2515: "1200", 0x2516: "2100", 0x2517: "2200",
	0x2518: "1001", 0x2519: "1002", 0x251A: "2001", 0x251B: "2002",
	0x251C: "1110", 0x251D: "1210", 0x251E: "2110", 0x251F: "1120",
	0x2520: "2120", 0x2521: "2210", 0x2522: "1220", 0x2523: "2220",
	0x2524: "1011", 0x2525: "1012", 0x2526: "2011", 0x2527: "1021",
	0x2528: "2021", 0x2529: "2012", 0x252A: "1022", 0x252B: "2022",
	0x252C: "0111", 0x252D: "0112", 0x252E: "0211", 0x252F: "0212",
	0x2530: "0121", 0x2531: "0122", 0x2532: "0221", 0x2533: "0222",
	0x2534: "1101", 0x2535: "1102", 0x2536: "1201", 0x2537: "1202",
	0x2538: "2101", 0x2539: "2102", 0x253A: "2201", 0x253B: "2202",
	0x253C: "1111", 0x253D: "1112", 0x253E: "1211", 0x253F: "1212",
	0x2540: "2111", 0x2541: "1121", 0x2542: "2121", 0x2543: "2112",
	0x2544: "2211", 0x2545: "1122", 0x2546: "1221", 0x2547: "2212",
	0x2548: "1222", 0x2549: "2122", 0x254A: "2221", 0x254B: "2222",
	0x2550: "0303", 0x2551: "3030", 0x2552: "0310", 0x2553: "0130", 0x2554: "0330",
	0x2555: "0013", 0x2556: "0031", 0x2557: "0033", 0x2558: "1300", 0x2559: "3100",
	0x255A: "3300", 0x255B: "1003", 0x255C: "3001", 0x255D: "3003", 0x255E: "1310",
	0x255F: "3130", 0x2560: "3330", 0x2561: "1013", 0x2562: "3031", 0x2563: "3033",
	0x2564: "0313", 0x2565: "0131", 0x2566: "0333", 0x2567: "1303", 0x2568: "3101",
	0x2569: "3303", 0x256A: "1313", 0x256B: "3131", 0x256C: "3333",
	0x2574: "0001", 0x2575: "1000", 0x2576: "0100", 0x2577: "0010",
	0x2578: "0002", 0x2579: "2000", 0x257A: "0200", 0x257B: "0020",
	0x257C: "0201", 0x257D: "1020", 0x257E: "0102", 0x257F: "2010",
}

// dashes: rune -> (count, vertical, heavy)
var dashes = map[rune][3]int{
	0x2504: {3, 0, 0}, 0x2505: {3, 0, 1}, 0x2506: {3, 1, 0}, 0x2507: {3, 1, 1},
	0x2508: {4, 0, 0}, 0x2509: {4, 0, 1}, 0x250A: {4, 1, 0}, 0x250B: {4, 1, 1},
	0x254C: {2, 0, 0}, 0x254D: {2, 0, 1}, 0x254E: {2, 1, 0}, 0x254F: {2, 1, 1},
}

func (p *painter) special(r rune, x0, y0, w, h int, fg, bg color.RGBA) bool {
	x1, y1 := x0+w, y0+h
	fw, fh := float64(w), float64(h)
	fx, fy := float64(x0), float64(y0)
	ry := func(n, d int) int { return y0 + int(math.Round(fh*float64(n)/float64(d))) }
	rx := func(n, d int) int { return x0 + int(math.Round(fw*float64(n)/float64(d))) }
	switch {
	// ---- block elements
	case r == 0x2580:
		p.fill(x0, y0, x1, ry(1, 2), fg)
	case r >= 0x2581 && r <= 0x2588:
		p.fill(x0, ry(8-int(r-0x2580), 8), x1, y1, fg)
	case r >= 0x2589 && r <= 0x258F:
		p.fill(x0, y0, rx(8-int(r-0x2588), 8), y1, fg)
	case r == 0x2590:
		p.fill(rx(1, 2), y0, x1, y1, fg)
	case r >= 0x2591 && r <= 0x2593:
		p.blend(x0, y0, x1, y1, fg, float64(r-0x2590)*0.25)
	case r == 0x2594:
		p.fill(x0, y0, x1, ry(1, 8), fg)
	case r == 0x2595:
		p.fill(rx(7, 8), y0, x1, y1, fg)
	case r >= 0x2596 && r <= 0x259F:
		// quadrant bits: UL=1 UR=2 LL=4 LR=8
		q := map[rune]int{0x2596: 4, 0x2597: 8, 0x2598: 1, 0x2599: 1 | 4 | 8, 0x259A: 1 | 8,
			0x259B: 1 | 2 | 4, 0x259C: 1 | 2 | 8, 0x259D: 2, 0x259E: 2 | 4, 0x259F: 2 | 4 | 8}[r]
		mx, my := rx(1, 2), ry(1, 2)
		if q&1 != 0 {
			p.fill(x0, y0, mx, my, fg)
		}
		if q&2 != 0 {
			p.fill(mx, y0, x1, my, fg)
		}
		if q&4 != 0 {
			p.fill(x0, my, mx, y1, fg)
		}
		if q&8 != 0 {
			p.fill(mx, my, x1, y1, fg)
		}
	// ---- triangles (price-tag corners) and powerline
	case r == 0x25E2: // ◢
		p.poly(x0, y0, x1, y1, fg, [][2]float64{{fx + fw, fy}, {fx + fw, fy + fh}, {fx, fy + fh}})
	case r == 0x25E3: // ◣
		p.poly(x0, y0, x1, y1, fg, [][2]float64{{fx, fy}, {fx + fw, fy + fh}, {fx, fy + fh}})
	case r == 0x25E4: // ◤
		p.poly(x0, y0, x1, y1, fg, [][2]float64{{fx, fy}, {fx + fw, fy}, {fx, fy + fh}})
	case r == 0x25E5: // ◥
		p.poly(x0, y0, x1, y1, fg, [][2]float64{{fx, fy}, {fx + fw, fy}, {fx + fw, fy + fh}})
	case r == 0xE0B0:
		p.poly(x0, y0, x1, y1, fg, [][2]float64{{fx, fy}, {fx + fw, fy + fh/2}, {fx, fy + fh}})
	case r == 0xE0B2:
		p.poly(x0, y0, x1, y1, fg, [][2]float64{{fx + fw, fy}, {fx, fy + fh/2}, {fx + fw, fy + fh}})
	case r == 0xE0B1:
		t := float64(p.lw)
		p.poly(x0, y0, x1, y1, fg, strokePath([][2]float64{{fx, fy}, {fx + fw - t, fy + fh/2}, {fx, fy + fh}}, t))
	case r == 0xE0B3:
		t := float64(p.lw)
		p.poly(x0, y0, x1, y1, fg, strokePath([][2]float64{{fx + fw, fy}, {fx + t, fy + fh/2}, {fx + fw, fy + fh}}, t))
	// ---- braille: 2x4 round dots
	case r >= 0x2800 && r <= 0x28FF:
		bits := int(r - 0x2800)
		pos := [8][2]int{{0, 0}, {0, 1}, {0, 2}, {1, 0}, {1, 1}, {1, 2}, {0, 3}, {1, 3}}
		rad := math.Min(fw/4, fh/8) * 0.62
		var dots [][][2]float64
		for i, q := range pos {
			if bits&(1<<i) != 0 {
				cx := fx + fw*(0.25+0.5*float64(q[0]))
				cy := fy + fh*(0.125+0.25*float64(q[1]))
				dots = append(dots, circle(cx, cy, rad))
			}
		}
		p.poly(x0, y0, x1, y1, fg, dots...)
	// ---- box drawing
	case r >= 0x256D && r <= 0x2570:
		p.arc(r, x0, y0, w, h, fg)
	case r >= 0x2571 && r <= 0x2573:
		t := float64(p.lw) * 1.1
		if r != 0x2572 {
			p.poly(x0, y0, x1, y1, fg, strokePath([][2]float64{{fx, fy + fh}, {fx + fw, fy}}, t))
		}
		if r != 0x2571 {
			p.poly(x0, y0, x1, y1, fg, strokePath([][2]float64{{fx, fy}, {fx + fw, fy + fh}}, t))
		}
	default:
		if d, ok := dashes[r]; ok {
			p.dash(x0, y0, w, h, d[0], d[1] == 1, d[2] == 1, fg)
			return true
		}
		arms, ok := boxArms[r]
		if !ok {
			return false
		}
		p.box(arms, x0, y0, w, h, fg)
	}
	return true
}

func (p *painter) thick(style byte) int {
	if style == '2' {
		return p.lw * 2
	}
	return p.lw
}

// box draws straight box-drawing arms. Each arm runs from its cell edge to
// the centre and overlaps the perpendicular strokes so joints are solid.
func (p *painter) box(arms string, x0, y0, w, h int, c color.RGBA) {
	u, rt, d, l := arms[0], arms[1], arms[2], arms[3]
	x1, y1 := x0+w, y0+h
	gap := p.lw // half the distance between double lines' inner edges
	// span of a stroke of this style across the centre line: [a, b)
	span := func(style byte, origin, size int) (int, int) {
		if style == '3' {
			cpos := origin + size/2
			return cpos - gap - p.lw, cpos + gap + p.lw
		}
		t := p.thick(style)
		a := origin + (size-t)/2
		return a, a + t
	}
	hmax := func() (int, int) { // vertical extent of the horizontal strokes
		a, b := y0+h/2, y0+h/2
		for _, s := range []byte{l, rt} {
			if s != '0' {
				sa, sb := span(s, y0, h)
				a, b = min(a, sa), max(b, sb)
			}
		}
		return a, b
	}
	vmax := func() (int, int) {
		a, b := x0+w/2, x0+w/2
		for _, s := range []byte{u, d} {
			if s != '0' {
				sa, sb := span(s, x0, w)
				a, b = min(a, sa), max(b, sb)
			}
		}
		return a, b
	}
	ha, hb := hmax()
	va, vb := vmax()
	vline := func(style byte, ya, yb int) {
		if style == '3' {
			cpos := x0 + w/2
			p.fill(cpos-gap-p.lw, ya, cpos-gap, yb, c)
			p.fill(cpos+gap, ya, cpos+gap+p.lw, yb, c)
			return
		}
		a, b := span(style, x0, w)
		p.fill(a, ya, b, yb, c)
	}
	hline := func(style byte, xa, xb int) {
		if style == '3' {
			cpos := y0 + h/2
			p.fill(xa, cpos-gap-p.lw, xb, cpos-gap, c)
			p.fill(xa, cpos+gap, xb, cpos+gap+p.lw, c)
			return
		}
		a, b := span(style, y0, h)
		p.fill(xa, a, xb, b, c)
	}
	if u != '0' {
		end := hb
		if l == '0' && rt == '0' {
			_, end = span(u, y0, h)
		}
		vline(u, y0, end)
	}
	if d != '0' {
		start := ha
		if l == '0' && rt == '0' {
			start, _ = span(d, y0, h)
		}
		vline(d, start, y1)
	}
	if l != '0' {
		end := vb
		if u == '0' && d == '0' {
			_, end = span(l, x0, w)
		}
		hline(l, x0, end)
	}
	if rt != '0' {
		start := va
		if u == '0' && d == '0' {
			start, _ = span(rt, x0, w)
		}
		hline(rt, start, x1)
	}
}

func (p *painter) dash(x0, y0, w, h, n int, vertical, heavy bool, c color.RGBA) {
	t := p.lw
	if heavy {
		t *= 2
	}
	if vertical {
		a := x0 + (w-t)/2
		seg := float64(h) / float64(n)
		for i := 0; i < n; i++ {
			s := float64(y0) + seg*float64(i) + seg*0.2
			p.fill(a, int(math.Round(s)), a+t, int(math.Round(s+seg*0.6)), c)
		}
		return
	}
	a := y0 + (h-t)/2
	seg := float64(w) / float64(n)
	for i := 0; i < n; i++ {
		s := float64(x0) + seg*float64(i) + seg*0.2
		p.fill(int(math.Round(s)), a, int(math.Round(s+seg*0.6)), a+t, c)
	}
}

// arc draws ╭╮╯╰ as a straight-arc-straight stroke centred on the same
// lines as the light box strokes, so it joins ─ and │ seamlessly.
func (p *painter) arc(r rune, x0, y0, w, h int, c color.RGBA) {
	t := float64(p.lw)
	cx := float64(x0+(w-p.lw)/2) + t/2
	cy := float64(y0+(h-p.lw)/2) + t/2
	rad := math.Min(float64(w), float64(h)) / 2
	fx0, fy0, fx1, fy1 := float64(x0), float64(y0), float64(x0+w), float64(y0+h)
	var hx, vy float64 // the edge the horizontal and vertical arms run to
	var sx, sy float64 // direction signs toward those edges
	switch r {
	case 0x256D: // ╭ right + down
		hx, vy, sx, sy = fx1, fy1, 1, 1
	case 0x256E: // ╮ left + down
		hx, vy, sx, sy = fx0, fy1, -1, 1
	case 0x256F: // ╯ left + up
		hx, vy, sx, sy = fx0, fy0, -1, -1
	case 0x2570: // ╰ right + up
		hx, vy, sx, sy = fx1, fy0, 1, -1
	}
	// Arc centre sits diagonally inside the corner.
	acx, acy := cx+sx*rad, cy+sy*rad
	pts := [][2]float64{{hx, cy}}
	for i := 0; i <= 16; i++ {
		a := float64(i) / 16 * math.Pi / 2
		// from the point (cx+sx*rad, cy) to (cx, cy+sy*rad)
		x := acx - sx*rad*math.Sin(a)
		y := acy - sy*rad*math.Cos(a)
		pts = append(pts, [2]float64{x, y})
	}
	pts = append(pts, [2]float64{cx, vy})
	p.poly(x0, y0, x0+w, y0+h, c, strokePath(pts, t))
}
