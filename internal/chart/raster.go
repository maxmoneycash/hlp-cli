package chart

import (
	"image"
	"image/color"
	"image/png"
	"io"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/maxmoneycash/hlp/internal/theme"
)

// boxSpec describes a box-drawing glyph: stroke weight per arm (up, right,
// down, left; 0 none, 1 light, 2 heavy), dash count and double-line flag.
type boxSpec struct {
	u, r, d, l int
	dash       int
	dbl        bool
}

var boxes = map[rune]boxSpec{
	'│': {1, 0, 1, 0, 0, false}, '┃': {2, 0, 2, 0, 0, false}, '─': {0, 1, 0, 1, 0, false},
	'━': {0, 2, 0, 2, 0, false}, '╵': {1, 0, 0, 0, 0, false}, '╷': {0, 0, 1, 0, 0, false},
	'╹': {2, 0, 0, 0, 0, false}, '╻': {0, 0, 2, 0, 0, false}, '╽': {1, 0, 2, 0, 0, false},
	'╿': {2, 0, 1, 0, 0, false}, '┿': {1, 2, 1, 2, 0, false}, '┷': {1, 2, 0, 2, 0, false},
	'┯': {0, 2, 1, 2, 0, false}, '┬': {0, 1, 1, 1, 0, false}, '┴': {1, 1, 0, 1, 0, false},
	'┘': {1, 0, 0, 1, 0, false}, '├': {1, 1, 1, 0, 0, false}, '┤': {1, 0, 1, 1, 0, false},
	'┼': {1, 1, 1, 1, 0, false}, '┈': {0, 1, 0, 1, 4, false}, '┄': {0, 1, 0, 1, 3, false},
	'╌': {0, 1, 0, 1, 2, false}, '┊': {1, 0, 1, 0, 4, false}, '║': {1, 0, 1, 0, 0, true},
	'╭': {0, 1, 1, 0, 0, false}, '╮': {0, 0, 1, 1, 0, false}, '╰': {1, 1, 0, 0, 0, false},
	'╯': {1, 0, 0, 1, 0, false},
}

// PNG rasterizes the grid procedurally (block, box and braille glyphs drawn
// as shapes; text via x/image basicfont) for README images and share cards.
// cellW x cellH pixels per cell (default 9 x 18 when 0).
//
// It draws the way GPU terminals (Ghostty, Kitty, WezTerm) do: block
// elements and box drawing fill the cell exactly, so the image matches what
// a good terminal shows. Colors are the truecolor values; the terminal
// default background and foreground are the theme's BG and Text.
func (g *Grid) PNG(w io.Writer, th theme.Theme, cellW, cellH int) error {
	if cellW <= 0 {
		cellW = 9
	}
	if cellH <= 0 {
		cellH = 18
	}
	img := image.NewRGBA(image.Rect(0, 0, g.W*cellW, g.H*cellH))
	fill := func(x0, y0, x1, y1 int, c color.RGBA) {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				img.SetRGBA(x, y, c)
			}
		}
	}
	rgba := func(c theme.Color, def theme.RGB) color.RGBA {
		v := c.True
		if !v.Valid {
			v = def
		}
		return color.RGBA{v.R, v.G, v.B, 0xff}
	}
	for y := 0; y < g.H; y++ {
		for x := 0; x < g.W; x++ {
			c := g.Cells[y*g.W+x]
			fg, bg := rgba(c.FG, th.Text.True), rgba(c.BG, th.BG.True)
			if c.Reverse {
				fg, bg = bg, fg
			}
			px, py := x*cellW, y*cellH
			fill(px, py, px+cellW, py+cellH, bg)
			r := c.Ch
			switch {
			case r == ' ' || r == 0:
			case r >= 0x2581 && r <= 0x2588: // lower n/8
				n := int(r - 0x2580)
				fill(px, py+cellH-cellH*n/8, px+cellW, py+cellH, fg)
			case r >= 0x2589 && r <= 0x258F: // left 7/8 .. 1/8
				n := 8 - int(r-0x2588)
				fill(px, py, px+cellW*n/8, py+cellH, fg)
			case r == '▐':
				fill(px+cellW/2, py, px+cellW, py+cellH, fg)
			case r == '▕':
				fill(px+cellW-cellW/8-1, py, px+cellW, py+cellH, fg)
			case r == '▀':
				fill(px, py, px+cellW, py+cellH/2, fg)
			case r == '▔':
				fill(px, py, px+cellW, py+cellH/8, fg)
			case r >= 0x2800 && r <= 0x28FF:
				bits := int(r - 0x2800)
				pos := [8][2]int{{0, 0}, {0, 1}, {0, 2}, {1, 0}, {1, 1}, {1, 2}, {0, 3}, {1, 3}}
				dw, dh := max(cellW/5, 1), max(cellH/9, 1)
				for i, p := range pos {
					if bits&(1<<i) != 0 {
						dx := px + cellW/4 - dw/2 + p[0]*cellW/2
						dy := py + cellH/8 - dh/2 + p[1]*cellH/4
						fill(dx, dy, dx+dw, dy+dh, fg)
					}
				}
			default:
				if b, ok := boxes[r]; ok {
					drawBox(fill, px, py, cellW, cellH, b, fg)
				} else if !drawShape(fill, px, py, cellW, cellH, r, fg) && r < 0x80 {
					d := &font.Drawer{Dst: img, Src: image.NewUniform(fg), Face: basicfont.Face7x13,
						Dot: fixed.P(px+(cellW-7)/2, py+(cellH+9)/2)}
					d.DrawString(string(r))
				}
			}
		}
	}
	return png.Encode(w, img)
}

func drawBox(fill func(int, int, int, int, color.RGBA), px, py, cw, chh int, b boxSpec, c color.RGBA) {
	cx, cy := px+cw/2, py+chh/2
	wv := func(w int) int {
		if w == 2 {
			return 3
		}
		return 1
	}
	vseg := func(y0, y1, w int) {
		t := wv(w)
		if b.dbl {
			fill(cx-3, y0, cx-2, y1, c)
			fill(cx+2, y0, cx+3, y1, c)
			return
		}
		if b.dash > 0 {
			seg := max(chh/b.dash, 2)
			for y := y0; y < y1; y++ {
				if (y-py)%seg < seg/2+1 {
					fill(cx-t/2, y, cx-t/2+t, y+1, c)
				}
			}
			return
		}
		fill(cx-t/2, y0, cx-t/2+t, y1, c)
	}
	hseg := func(x0, x1, w int) {
		t := wv(w)
		if b.dash > 0 {
			seg := max(cw/2, 2)
			for x := x0; x < x1; x++ {
				if (x-px)%seg < seg/2 {
					fill(x, cy-t/2, x+1, cy-t/2+t, c)
				}
			}
			return
		}
		fill(x0, cy-t/2, x1, cy-t/2+t, c)
	}
	if b.u > 0 {
		vseg(py, cy+1, b.u)
	}
	if b.d > 0 {
		vseg(cy, py+chh, b.d)
	}
	if b.l > 0 {
		hseg(px, cx+1, b.l)
	}
	if b.r > 0 {
		hseg(cx, px+cw, b.r)
	}
}

// drawShape draws the few non-ASCII symbols the charts use that basicfont
// lacks (markers, the live hint, the middle dot and the ellipsis).
func drawShape(fill func(int, int, int, int, color.RGBA), px, py, cw, chh int, r rune, c color.RGBA) bool {
	switch r {
	case '▲', '▼':
		hh := cw - 2
		top := py + (chh-hh/2*2)/2
		for i := 0; i < hh/2+1; i++ {
			y := top + i
			if r == '▼' {
				y = top + hh/2 - i
			}
			fill(px+cw/2-i, y, px+cw/2+i+1, y+1, c)
		}
	case '▶':
		h := cw - 2
		top := py + (chh-h)/2
		for i := 0; i <= h/2; i++ {
			fill(px+1, top+i, px+1+i+1, top+i+1, c)
			fill(px+1, top+h-i, px+1+i+1, top+h-i+1, c)
		}
	case '·':
		fill(px+cw/2-1, py+chh/2-1, px+cw/2+1, py+chh/2+1, c)
	case '…':
		for i := 0; i < 3; i++ {
			x := px + 1 + i*(cw-2)/3
			fill(x, py+chh*3/4, x+1, py+chh*3/4+1, c)
		}
	default:
		return false
	}
	return true
}
