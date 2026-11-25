package chart

import (
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/maxmoneycash/hlp/internal/theme"
)

// shot renders a Grid the way a GPU terminal (Ghostty, Kitty, WezTerm)
// does: block, box, braille and triangle glyphs as exact cell sprites, text
// in Go Mono (bold where the cell is bold). It is the eye for the setup-rush
// port: README images and side-by-side checks against the reference renders.
type shot struct {
	cw, ch      int
	reg, bold   font.Face
	defFG, defB color.RGBA
}

var (
	shotFaces    sync.Once
	shotReg      *opentype.Font
	shotBoldFont *opentype.Font
)

func newShot(cw, ch int, def theme.Theme) *shot {
	shotFaces.Do(func() {
		shotReg, _ = opentype.Parse(gomono.TTF)
		shotBoldFont, _ = opentype.Parse(gomonobold.TTF)
		// HLP_SHOT_FONT / HLP_SHOT_FONT_BOLD: a TTF to draw text with
		// (README images use Fira Code when it is installed).
		if b, err := os.ReadFile(os.Getenv("HLP_SHOT_FONT")); err == nil {
			if f, err := opentype.Parse(b); err == nil {
				shotReg, shotBoldFont = f, f
			}
		}
		if b, err := os.ReadFile(os.Getenv("HLP_SHOT_FONT_BOLD")); err == nil {
			if f, err := opentype.Parse(b); err == nil {
				shotBoldFont = f
			}
		}
	})
	size := float64(cw) / 0.6 * 0.92
	reg, _ := opentype.NewFace(shotReg, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	bold, _ := opentype.NewFace(shotBoldFont, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	rgba := func(c theme.RGB, fb color.RGBA) color.RGBA {
		if !c.Valid {
			return fb
		}
		return color.RGBA{c.R, c.G, c.B, 0xff}
	}
	return &shot{cw: cw, ch: ch, reg: reg, bold: bold,
		defFG: rgba(def.Text.True, color.RGBA{0xdd, 0xdd, 0xdd, 0xff}),
		defB:  rgba(def.BG.True, color.RGBA{0, 0, 0, 0xff})}
}

// arms: up, right, down, left. 1 light, 2 heavy, 3 double.
type boxArms struct {
	u, r, d, l int
	dash       int
}

var shotBoxes = map[rune]boxArms{
	'│': {1, 0, 1, 0, 0}, '┃': {2, 0, 2, 0, 0}, '─': {0, 1, 0, 1, 0}, '━': {0, 2, 0, 2, 0},
	'║': {3, 0, 3, 0, 0}, '═': {0, 3, 0, 3, 0},
	'┌': {0, 1, 1, 0, 0}, '┐': {0, 0, 1, 1, 0}, '└': {1, 1, 0, 0, 0}, '┘': {1, 0, 0, 1, 0},
	'╭': {0, 1, 1, 0, 0}, '╮': {0, 0, 1, 1, 0}, '╰': {1, 1, 0, 0, 0}, '╯': {1, 0, 0, 1, 0},
	'├': {1, 1, 1, 0, 0}, '┤': {1, 0, 1, 1, 0}, '┬': {0, 1, 1, 1, 0}, '┴': {1, 1, 0, 1, 0}, '┼': {1, 1, 1, 1, 0},
	'┝': {1, 2, 1, 0, 0}, '┥': {1, 0, 1, 2, 0}, '┿': {1, 2, 1, 2, 0}, '┷': {1, 2, 0, 2, 0}, '┯': {0, 2, 1, 2, 0},
	'╫': {3, 1, 3, 1, 0}, '╨': {3, 1, 0, 1, 0}, '╥': {0, 1, 3, 1, 0},
	'╵': {1, 0, 0, 0, 0}, '╷': {0, 0, 1, 0, 0}, '╹': {2, 0, 0, 0, 0}, '╻': {0, 0, 2, 0, 0},
	'╽': {1, 0, 2, 0, 0}, '╿': {2, 0, 1, 0, 0},
	'┈': {0, 1, 0, 1, 4}, '┄': {0, 1, 0, 1, 3}, '╌': {0, 1, 0, 1, 2},
	'┊': {1, 0, 1, 0, 4}, '┆': {1, 0, 1, 0, 3}, '╎': {1, 0, 1, 0, 2},
}

func (s *shot) render(w io.Writer, g *Grid) error {
	img := image.NewRGBA(image.Rect(0, 0, g.W*s.cw, g.H*s.ch))
	fill := func(x0, y0, x1, y1 int, c color.RGBA) {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				img.SetRGBA(x, y, c)
			}
		}
	}
	for y := 0; y < g.H; y++ {
		for x := 0; x < g.W; x++ {
			c := g.Cells[y*g.W+x]
			fg, bg := s.defFG, s.defB
			if c.FG.True.Valid {
				fg = color.RGBA{c.FG.True.R, c.FG.True.G, c.FG.True.B, 0xff}
			}
			if c.BG.True.Valid {
				bg = color.RGBA{c.BG.True.R, c.BG.True.G, c.BG.True.B, 0xff}
			}
			if c.Reverse {
				fg, bg = bg, fg
			}
			px, py := x*s.cw, y*s.ch
			fill(px, py, px+s.cw, py+s.ch, bg)
			s.glyph(img, fill, px, py, c.Ch, fg, c.Bold)
		}
	}
	return png.Encode(w, img)
}

func (s *shot) glyph(img *image.RGBA, fill func(int, int, int, int, color.RGBA), px, py int, r rune, fg color.RGBA, bold bool) {
	cw, ch := s.cw, s.ch
	switch {
	case r == ' ' || r == 0:
	case r >= 0x2581 && r <= 0x2588:
		n := int(r - 0x2580)
		fill(px, py+ch-(ch*n+4)/8, px+cw, py+ch, fg)
	case r >= 0x2589 && r <= 0x258F:
		n := 8 - int(r-0x2588)
		fill(px, py, px+(cw*n+4)/8, py+ch, fg)
	case r == '▐':
		fill(px+cw/2, py, px+cw, py+ch, fg)
	case r == '▕':
		fill(px+cw-max(cw/8, 1), py, px+cw, py+ch, fg)
	case r == '▀':
		fill(px, py, px+cw, py+ch/2, fg)
	case r == '▔':
		fill(px, py, px+cw, py+max(ch/8, 1), fg)
	case r == '▪':
		d := cw / 3
		fill(px+cw/2-d/2, py+ch/2-d/2, px+cw/2-d/2+d, py+ch/2-d/2+d, fg)
	case r == '◥' || r == '◣' || r == '◢' || r == '◤':
		for yy := 0; yy < ch; yy++ {
			f := float64(yy) + 0.5
			for xx := 0; xx < cw; xx++ {
				fx := (float64(xx) + 0.5) / float64(cw) * float64(ch)
				var in bool
				switch r {
				case '◥': // upper right
					in = fx >= f
				case '◣': // lower left
					in = fx <= f
				case '◢': // lower right
					in = fx >= float64(ch)-f
				case '◤':
					in = fx <= float64(ch)-f
				}
				if in {
					img.SetRGBA(px+xx, py+yy, fg)
				}
			}
		}
	case r >= 0x2800 && r <= 0x28FF:
		bits := int(r - 0x2800)
		pos := [8][2]int{{0, 0}, {0, 1}, {0, 2}, {1, 0}, {1, 1}, {1, 2}, {0, 3}, {1, 3}}
		dw, dh := max(cw/3, 2), max(ch/7, 2)
		for i, p := range pos {
			if bits&(1<<i) != 0 {
				dx := px + cw/4 - dw/2 + p[0]*cw/2
				dy := py + ch/8 - dh/2 + p[1]*ch/4
				fill(dx, dy, dx+dw, dy+dh, fg)
			}
		}
	default:
		if b, ok := shotBoxes[r]; ok {
			s.box(fill, px, py, b, fg)
			return
		}
		face := s.reg
		if bold {
			face = s.bold
		}
		m := face.Metrics()
		base := py + (ch+(m.Ascent-m.Descent).Ceil())/2
		d := &font.Drawer{Dst: img, Src: image.NewUniform(fg), Face: face, Dot: fixed.P(px, base)}
		d.DrawString(string(r))
	}
}

func (s *shot) box(fill func(int, int, int, int, color.RGBA), px, py int, b boxArms, c color.RGBA) {
	cw, ch := s.cw, s.ch
	cx, cy := px+cw/2, py+ch/2
	thick := func(w int) int {
		if w == 2 {
			return 3
		}
		return 1
	}
	gapd := max(cw/5, 2)
	vseg := func(y0, y1, w int) {
		if w == 3 {
			fill(cx-gapd, y0, cx-gapd+1, y1, c)
			fill(cx+gapd, y0, cx+gapd+1, y1, c)
			return
		}
		t := thick(w)
		if b.dash > 0 {
			seg := ch / b.dash
			for y := y0; y < y1; y++ {
				if (y-py)%seg < seg*3/5 {
					fill(cx-t/2, y, cx-t/2+t, y+1, c)
				}
			}
			return
		}
		fill(cx-t/2, y0, cx-t/2+t, y1, c)
	}
	hseg := func(x0, x1, w int) {
		if w == 3 {
			fill(x0, cy-gapd, x1, cy-gapd+1, c)
			fill(x0, cy+gapd, x1, cy+gapd+1, c)
			return
		}
		t := thick(w)
		if b.dash > 0 {
			seg := max(cw/b.dash*2, 2)
			if b.dash == 4 {
				seg = max(cw/2, 2)
			}
			for x := x0; x < x1; x++ {
				if (x-px)%seg < (seg+1)/2 {
					fill(x, cy-t/2, x+1, cy-t/2+t, c)
				}
			}
			return
		}
		fill(x0, cy-t/2, x1, cy-t/2+t, c)
	}
	// Double verticals with single horizontals: the horizontal runs the
	// whole cell (╫ ╨ ╥).
	if b.u > 0 {
		end := cy + 1
		if b.u == 3 && b.d == 0 {
			end = cy + 1
		}
		vseg(py, end, b.u)
	}
	if b.d > 0 {
		vseg(cy, py+ch, b.d)
	}
	if b.l > 0 {
		hseg(px, cx+1, b.l)
	}
	if b.r > 0 {
		hseg(cx, px+cw, b.r)
	}
}

// TestShotANSI renders a captured terminal screen ($HLP_SHOT_ANSI, e.g.
// `tmux capture-pane -e -p`) to $HLP_SHOT_ANSI.png. Truecolor and 256-color
// SGR, bold and reverse are understood.
func TestShotANSI(t *testing.T) {
	in := os.Getenv("HLP_SHOT_ANSI")
	if in == "" {
		t.Skip("set HLP_SHOT_ANSI=capture.txt")
	}
	raw, err := os.ReadFile(in)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	w := 0
	for _, l := range lines {
		w = max(w, ansi.StringWidth(l))
	}
	g := NewGrid(w, len(lines))
	for y, l := range lines {
		var fg, bg theme.Color
		bold, rev := false, false
		x := 0
		rs := []rune(l)
		for i := 0; i < len(rs); i++ {
			if rs[i] == 0x1b && i+1 < len(rs) && rs[i+1] == '[' {
				j := i + 2
				for j < len(rs) && rs[j] != 'm' {
					j++
				}
				ps := strings.Split(string(rs[i+2:j]), ";")
				for k := 0; k < len(ps); k++ {
					n, _ := strconv.Atoi(ps[k])
					switch {
					case n == 0:
						fg, bg, bold, rev = theme.Color{}, theme.Color{}, false, false
					case n == 1:
						bold = true
					case n == 7:
						rev = true
					case n == 22:
						bold = false
					case n == 27:
						rev = false
					case n == 39:
						fg = theme.Color{}
					case n == 49:
						bg = theme.Color{}
					case (n == 38 || n == 48) && k+1 < len(ps):
						var c theme.RGB
						if ps[k+1] == "2" && k+4 < len(ps) {
							r, _ := strconv.Atoi(ps[k+2])
							gg, _ := strconv.Atoi(ps[k+3])
							b, _ := strconv.Atoi(ps[k+4])
							c = theme.RGB{R: uint8(r), G: uint8(gg), B: uint8(b), Valid: true}
							k += 4
						} else if ps[k+1] == "5" && k+2 < len(ps) {
							idx, _ := strconv.Atoi(ps[k+2])
							c = xterm256(idx)
							k += 2
						}
						if n == 38 {
							fg = theme.Color{True: c}
						} else {
							bg = theme.Color{True: c}
						}
					case n >= 30 && n <= 37, n >= 90 && n <= 97:
						fg = theme.Color{True: xterm256(ansi16(n))}
					case n >= 40 && n <= 47, n >= 100 && n <= 107:
						bg = theme.Color{True: xterm256(ansi16(n - 10))}
					}
				}
				i = j
				continue
			}
			if c := g.At(x, y); c != nil {
				*c = Cell{Ch: rs[i], FG: fg, BG: bg, Bold: bold, Reverse: rev}
			}
			x++
		}
	}
	f, err := os.Create(in + ".png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := newShot(10, 20, theme.Theme{}).render(f, g); err != nil {
		t.Fatal(err)
	}
}

func ansi16(n int) int {
	if n >= 90 {
		return n - 90 + 8
	}
	return n - 30
}

// xterm256 is the standard xterm palette entry i.
func xterm256(i int) theme.RGB {
	base := []string{"000000", "cd0000", "00cd00", "cdcd00", "0000ee", "cd00cd", "00cdcd", "e5e5e5",
		"7f7f7f", "ff0000", "00ff00", "ffff00", "5c5cff", "ff00ff", "00ffff", "ffffff"}
	switch {
	case i < 16:
		return theme.Hex(base[max(i, 0)])
	case i < 232:
		lv := [6]uint8{0, 95, 135, 175, 215, 255}
		i -= 16
		return theme.RGB{R: lv[i/36], G: lv[i/6%6], B: lv[i%6], Valid: true}
	}
	v := uint8(8 + 10*(i-232))
	return theme.RGB{R: v, G: v, B: v, Valid: true}
}
