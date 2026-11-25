package main

// frame.go composes the final picture: a macOS-style window (traffic
// lights, centred title, rounded corners, hairline edge) holding the
// terminal, a soft two-layer drop shadow, and a subtle backdrop.

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

type frameOpts struct {
	frame    bool
	shadow   bool
	title    string
	pad      float64 // pt
	margin   float64 // pt
	radius   float64 // pt
	backdrop string  // dark | light | none | #hex[,#hex]
	scale    float64
}

// roundRect returns the outline of a rounded rectangle.
func roundRect(x, y, w, h, r float64) [][2]float64 {
	r = math.Min(r, math.Min(w, h)/2)
	var pts [][2]float64
	corner := func(cx, cy, a0 float64) {
		for i := 0; i <= 12; i++ {
			a := a0 + float64(i)/12*math.Pi/2
			pts = append(pts, [2]float64{cx + r*math.Cos(a), cy + r*math.Sin(a)})
		}
	}
	corner(x+w-r, y+r, -math.Pi/2)
	corner(x+w-r, y+h-r, 0)
	corner(x+r, y+h-r, math.Pi/2)
	corner(x+r, y+r, math.Pi)
	return pts
}

func alphaMask(w, h int, polys ...[][2]float64) *image.Alpha {
	z := vector.NewRasterizer(w, h)
	for _, pts := range polys {
		z.MoveTo(float32(pts[0][0]), float32(pts[0][1]))
		for _, q := range pts[1:] {
			z.LineTo(float32(q[0]), float32(q[1]))
		}
		z.ClosePath()
	}
	m := image.NewAlpha(image.Rect(0, 0, w, h))
	z.Draw(m, m.Bounds(), image.Opaque, image.Point{})
	return m
}

func reverse(p [][2]float64) [][2]float64 {
	out := make([][2]float64, len(p))
	for i := range p {
		out[i] = p[len(p)-1-i]
	}
	return out
}

// blur approximates a gaussian with three box blurs (separable, running sums).
func blur(m *image.Alpha, sigma float64) {
	if sigma < 0.5 {
		return
	}
	w, h := m.Rect.Dx(), m.Rect.Dy()
	r := int(math.Round(math.Sqrt(12*sigma*sigma/3+1)-1) / 2)
	r = max(r, 1)
	tmp := make([]float64, max(w, h))
	src := make([]float64, max(w, h))
	pass := func(get func(i int) float64, set func(i int, v float64), n int) {
		for i := 0; i < n; i++ {
			src[i] = get(i)
		}
		for k := 0; k < 3; k++ {
			acc := 0.0
			for i := -r; i <= r; i++ {
				acc += src[min(max(i, 0), n-1)]
			}
			for i := 0; i < n; i++ {
				tmp[i] = acc / float64(2*r+1)
				acc += src[min(i+r+1, n-1)] - src[max(i-r, 0)]
			}
			copy(src[:n], tmp[:n])
		}
		for i := 0; i < n; i++ {
			set(i, src[i])
		}
	}
	for y := 0; y < h; y++ {
		row := y * m.Stride
		pass(func(i int) float64 { return float64(m.Pix[row+i]) },
			func(i int, v float64) { m.Pix[row+i] = uint8(math.Min(255, math.Round(v))) }, w)
	}
	for x := 0; x < w; x++ {
		pass(func(i int) float64 { return float64(m.Pix[i*m.Stride+x]) },
			func(i int, v float64) { m.Pix[i*m.Stride+x] = uint8(math.Min(255, math.Round(v))) }, h)
	}
}

// titleFace finds a UI font for the window title.
func titleFace(px float64, fallback font.Face) font.Face {
	for _, f := range []string{
		"SF-Pro-Text-Medium.otf", "SF-Pro-Text-Regular.otf", "/System/Library/Fonts/SFNS.ttf",
		"/System/Library/Fonts/HelveticaNeue.ttc#0",
	} {
		if r := resolve(f); r != "" {
			if ft, err := loadFont(r); err == nil {
				if fc, err := opentype.NewFace(ft, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingNone}); err == nil {
					return fc
				}
			}
		}
	}
	return fallback
}

func backdrop(dst *image.RGBA, spec string) {
	b := dst.Bounds()
	var c0, c1 color.RGBA
	switch spec {
	case "none", "transparent":
		return
	case "", "dark":
		c0, c1 = mustHex("#30333d"), mustHex("#15161b")
	case "light":
		c0, c1 = mustHex("#f1f2f5"), mustHex("#d6dae1")
	case "warm":
		c0, c1 = mustHex("#3a302a"), mustHex("#16130f")
	default:
		a, bb, ok := cutHex(spec)
		if !ok {
			c0, c1 = mustHex("#30333d"), mustHex("#15161b")
		} else {
			c0, c1 = a, bb
		}
	}
	w, h := float64(b.Dx()), float64(b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			// Diagonal gradient, top-left light to bottom-right dark, plus a
			// tiny ordered dither so 8-bit banding does not show.
			t := (float64(x)/w*0.35 + float64(y)/h*0.65)
			d := (float64((x*7+y*13)%16)/16 - 0.5) / 255 * 1.5
			l := func(a, b uint8) uint8 {
				v := float64(a)*(1-t) + float64(b)*t + d*255
				return uint8(math.Max(0, math.Min(255, math.Round(v))))
			}
			dst.SetRGBA(x, y, color.RGBA{l(c0.R, c1.R), l(c0.G, c1.G), l(c0.B, c1.B), 255})
		}
	}
}

func cutHex(s string) (color.RGBA, color.RGBA, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			a, ok1 := hexColor(s[:i])
			b, ok2 := hexColor(s[i+1:])
			return a, b, ok1 && ok2
		}
	}
	a, ok := hexColor(s)
	return a, a, ok
}

// compose renders the grid and wraps it in the frame.
func compose(g *grid, p *painter, o frameOpts) *image.RGBA {
	s := o.scale
	pt := func(v float64) int { return int(math.Round(v * s)) }
	gw, gh := g.w*p.fs.cellW, g.h*p.fs.cellH
	pad := pt(o.pad)
	if !o.frame {
		img := image.NewRGBA(image.Rect(0, 0, gw+2*pad, gh+2*pad))
		draw.Draw(img, img.Bounds(), image.NewUniform(p.bg), image.Point{}, draw.Src)
		p.img = img
		p.draw(g, pad, pad)
		return img
	}
	titleH := pt(34)
	winW, winH := gw+2*pad, titleH+pt(o.pad*0.35)+gh+pad
	win := image.NewRGBA(image.Rect(0, 0, winW, winH))
	draw.Draw(win, win.Bounds(), image.NewUniform(p.bg), image.Point{}, draw.Src)
	p.img = win
	p.draw(g, pad, titleH+pt(o.pad*0.35))

	// Traffic lights.
	lights := []string{"#ff5f57", "#febc2e", "#28c840"}
	for i, hc := range lights {
		cx, cy := float64(pt(20+float64(i)*20)), float64(titleH)/2
		rad := 6 * s
		c := mustHex(hc)
		p.poly(0, 0, winW, titleH, c, circle(cx, cy, rad))
		// faint rim, like the real ones
		rim := mix(c, color.RGBA{0, 0, 0, 255}, 0.18)
		p.poly(0, 0, winW, titleH, rim, circle(cx, cy, rad), reverse(circle(cx, cy, rad-0.5*s)))
	}
	if o.title != "" {
		tf := titleFace(13*s, p.fs.face(p.fs.fam.fonts[stRegular]))
		tc := mix(p.bg, p.fg, 0.55)
		w := fix(font.MeasureString(tf, o.title))
		m := tf.Metrics()
		base := float64(titleH)/2 + (fix(m.CapHeight))/2
		if m.CapHeight == 0 {
			base = float64(titleH)/2 + 4.5*s
		}
		d := &font.Drawer{Dst: win, Src: image.NewUniform(tc), Face: tf,
			Dot: fixed.Point26_6{X: fixed.Int26_6((float64(winW) - w) / 2 * 64), Y: fixed.Int26_6(base * 64)}}
		d.DrawString(o.title)
	}

	margin := pt(o.margin)
	W, H := winW+2*margin, winH+2*margin
	out := image.NewRGBA(image.Rect(0, 0, W, H))
	backdrop(out, o.backdrop)
	rad := o.radius * s
	wx, wy := float64(margin), float64(margin)
	if o.shadow {
		// Wide soft key shadow below, plus a tight contact shadow.
		for _, sh := range []struct{ dy, sigma, a float64 }{{22 * s, 26 * s, 0.55}, {2 * s, 3 * s, 0.35}} {
			m := alphaMask(W, H, roundRect(wx, wy+sh.dy, float64(winW), float64(winH), rad))
			blur(m, sh.sigma)
			for i := range m.Pix {
				m.Pix[i] = uint8(float64(m.Pix[i]) * sh.a)
			}
			draw.DrawMask(out, out.Bounds(), image.NewUniform(color.RGBA{0, 0, 0, 255}), image.Point{}, m, image.Point{}, draw.Over)
		}
	}
	rr := roundRect(wx, wy, float64(winW), float64(winH), rad)
	mask := alphaMask(W, H, rr)
	draw.DrawMask(out, image.Rect(margin, margin, margin+winW, margin+winH), win, image.Point{}, mask, image.Pt(margin, margin), draw.Over)
	// Hairline edges: a dark outer line and a faint light inner line, the
	// way macOS draws dark windows.
	hl := math.Max(1, s*0.5)
	outer := alphaMask(W, H, roundRect(wx-hl, wy-hl, float64(winW)+2*hl, float64(winH)+2*hl, rad+hl), reverse(rr))
	draw.DrawMask(out, out.Bounds(), image.NewUniform(color.RGBA{0, 0, 0, 255}), image.Point{}, withAlpha(outer, 0.55), image.Point{}, draw.Over)
	inner := alphaMask(W, H, rr, reverse(roundRect(wx+hl, wy+hl, float64(winW)-2*hl, float64(winH)-2*hl, rad-hl)))
	draw.DrawMask(out, out.Bounds(), image.NewUniform(color.RGBA{255, 255, 255, 255}), image.Point{}, withAlpha(inner, 0.14), image.Point{}, draw.Over)
	return out
}

func withAlpha(m *image.Alpha, a float64) *image.Alpha {
	for i := range m.Pix {
		m.Pix[i] = uint8(float64(m.Pix[i]) * a)
	}
	return m
}
