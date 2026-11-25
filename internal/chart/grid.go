package chart

import (
	"image/color"
	"strconv"
	"strings"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/maxmoneycash/hlp/internal/theme"
)

// Layer orders paint priority: a higher layer may overwrite a lower one.
type Layer uint8

const (
	LEmpty   Layer = iota
	LGrid          // gridlines ┈ ┊
	LOverlay       // braille indicator lines, last-price line, crosshair lines
	LVolume
	LCandle
	LAxis
	LLabel
)

// Cell is one terminal cell: 24 bytes (theme.Color is 8), so a 200x60
// frame is 288 KB of cells instead of 672 KB.
type Cell struct {
	Ch      rune
	FG, BG  theme.Color // zero Color = terminal default
	Bold    bool
	Reverse bool // SGR 7, used for upper-partial-block emulation and tags
	Layer   Layer
}

// Grid is a W x H cell buffer.
type Grid struct {
	W, H  int
	Cells []Cell

	memo *overlayMemo // overlay state kept while the grid is reused (CandleOpts.Into)
}

// NewGrid returns a grid filled with spaces.
func NewGrid(w, h int) *Grid {
	w, h = max(w, 0), max(h, 0)
	g := &Grid{W: w, H: h, Cells: make([]Cell, w*h)}
	for i := range g.Cells {
		g.Cells[i].Ch = ' '
	}
	return g
}

// NewGridFrom reuses g as a blank w x h grid: the cell buffer is resliced
// when it is large enough (reallocated otherwise) and every cell is reset
// to a space. A nil g allocates a new grid. A frame renderer that keeps
// one grid per pane allocates no cells after the first frame.
func NewGridFrom(g *Grid, w, h int) *Grid {
	if g == nil {
		return NewGrid(w, h)
	}
	w, h = max(w, 0), max(h, 0)
	if n := w * h; cap(g.Cells) >= n {
		g.Cells = g.Cells[:n]
	} else {
		g.Cells = make([]Cell, n)
	}
	g.W, g.H = w, h
	for i := range g.Cells {
		g.Cells[i] = Cell{Ch: ' '}
	}
	return g
}

// overlayMemo returns the grid's overlay state, creating it on first use.
func (g *Grid) overlayMemo() *overlayMemo {
	if g.memo == nil {
		g.memo = &overlayMemo{}
	}
	return g.memo
}

// At returns the cell at (x,y) or nil when out of bounds.
func (g *Grid) At(x, y int) *Cell {
	if x < 0 || y < 0 || x >= g.W || y >= g.H {
		return nil
	}
	return &g.Cells[y*g.W+x]
}

// Set writes a glyph if layer >= the cell's current layer. The background
// is preserved (crosshair tints survive); Bold and Reverse are reset, so the
// caller sets them on the returned cell when needed. Returns nil when the
// cell is out of bounds or holds a higher layer.
func (g *Grid) Set(x, y int, ch rune, fg theme.Color, layer Layer) *Cell {
	c := g.At(x, y)
	if c == nil || layer < c.Layer {
		return nil
	}
	c.Ch, c.FG, c.Layer, c.Bold, c.Reverse = ch, fg, layer, false, false
	return c
}

// Text writes a string starting at (x,y); returns columns written. Every
// rune occupies one cell (callers only pass narrow glyphs).
func (g *Grid) Text(x, y int, s string, fg theme.Color, layer Layer) int {
	n := 0
	for _, r := range s {
		g.Set(x+n, y, r, fg, layer)
		n++
	}
	return n
}

// TintBG sets a background color on a cell without touching its glyph.
func (g *Grid) TintBG(x, y int, bg theme.Color) {
	if c := g.At(x, y); c != nil {
		c.BG = bg
	}
}

// Blit copies src into g with its top-left corner at (x,y), clipping at the
// edges. Used to compose panes (for example a chart above a depth ladder).
func (g *Grid) Blit(src *Grid, x, y int) {
	for sy := 0; sy < src.H; sy++ {
		for sx := 0; sx < src.W; sx++ {
			if c := g.At(x+sx, y+sy); c != nil {
				*c = src.Cells[sy*src.W+sx]
			}
		}
	}
}

// Lines returns Plain() split into rows.
func (g *Grid) Lines() []string { return strings.Split(g.Plain(), "\n") }

// ANSI serializes with run-length SGR for profile p (TrueColor, ANSI256,
// ANSI, ASCII, NoTTY). Lines are exactly W cells, joined by "\n", each
// ending in a single SGR reset when it carried attributes. NoTTY/ASCII emit
// no color but keep bold and SGR 7 (tags and upper partial blocks survive).
//
// Colors resolve through each token's hand-picked palette: TrueColor uses
// True, ANSI256 uses C256, ANSI uses C16 (never automatic downsampling). A
// foreground token that is deliberately omitted at the profile (C16 == -2,
// e.g. gridlines at 16 colors) renders its cell as a blank, so faint chrome
// disappears instead of turning into default-colored noise.
func (g *Grid) ANSI(p colorprofile.Profile) string {
	var b strings.Builder
	b.Grow(ansiSizeHint(g.W, g.H, p))
	cache := &sgrCache{p: p, keys: make([]theme.Color, 0, 32), vals: make([]sgrColor, 0, 32)}
	res := cache.get
	colored := p == colorprofile.TrueColor || p == colorprofile.ANSI256 || p == colorprofile.ANSI
	for y := 0; y < g.H; y++ {
		var curFG, curBG sgrColor
		var curBold, curRev, open bool
		for x := 0; x < g.W; x++ {
			c := &g.Cells[y*g.W+x]
			var fg, bg sgrColor
			ch := c.Ch
			if colored {
				if c.BG != (theme.Color{}) {
					bg = res(c.BG)
				}
				if c.Ch != ' ' || c.Reverse {
					fg = res(c.FG)
				}
				if fg.omit && !c.Reverse {
					ch = ' '
					fg = sgrColor{}
				}
			}
			bold, rev := c.Bold, c.Reverse
			if ch == ' ' && !rev {
				// A blank shows only its background: drop fg/bold so runs merge.
				fg, bold = sgrColor{}, false
			}
			if !open || fg != curFG || bg != curBG || bold != curBold || rev != curRev {
				plainNow := fg.code == "" && bg.code == "" && !bold && !rev
				if plainNow {
					if open {
						b.WriteString("\x1b[0m")
					}
					open = false
				} else {
					b.WriteString("\x1b[0")
					if bold {
						b.WriteString(";1")
					}
					if rev {
						b.WriteString(";7")
					}
					if fg.code != "" {
						b.WriteByte(';')
						b.WriteString(fg.code)
					}
					if bg.code != "" {
						b.WriteByte(';')
						b.WriteString(bg.bgCode)
					}
					b.WriteByte('m')
					open = true
				}
				curFG, curBG, curBold, curRev = fg, bg, bold, rev
			}
			b.WriteRune(ch)
		}
		if open {
			b.WriteString("\x1b[0m")
		}
		if y < g.H-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// ansiSizeHint estimates the serialized size of a w x h grid so the
// builder allocates once instead of doubling three times. The per-cell
// figures are a full skinned chart's (every cell carries a background) at
// each profile, rounded up; a 200x60 TrueColor frame is about 144 KB.
func ansiSizeHint(w, h int, p colorprofile.Profile) int {
	per := 3 // glyph only: braille and blocks are 3 UTF-8 bytes
	switch p {
	case colorprofile.TrueColor:
		per = 13
	case colorprofile.ANSI256:
		per = 10
	case colorprofile.ANSI:
		per = 4
	}
	return w*h*per + h
}

// sgrCache memoizes resolved colors for one serialization. A frame uses a
// few dozen distinct colors and runs repeat, so a last-hit check plus a
// short linear scan beats hashing 8-byte keys per cell.
type sgrCache struct {
	p       colorprofile.Profile
	keys    []theme.Color
	vals    []sgrColor
	lastKey theme.Color
	lastVal sgrColor
	hasLast bool
}

func (c *sgrCache) get(k theme.Color) sgrColor {
	if c.hasLast && k == c.lastKey {
		return c.lastVal
	}
	v, found := sgrColor{}, false
	for i := range c.keys {
		if c.keys[i] == k {
			v, found = c.vals[i], true
			break
		}
	}
	if !found {
		v = resolveSGR(k, c.p)
		c.keys = append(c.keys, k)
		c.vals = append(c.vals, v)
	}
	c.lastKey, c.lastVal, c.hasLast = k, v, true
	return v
}

// sgrColor is a resolved color: the SGR parameters for fg and bg use.
type sgrColor struct {
	code, bgCode string
	omit         bool // deliberately absent at this profile (C16 == -2)
}

// resolveSGR mirrors theme.Color.Resolve but yields SGR parameters directly
// so the serializer never parses color strings per cell. The fg and bg
// parameters share one allocation; the 16 basic colors allocate nothing.
func resolveSGR(c theme.Color, p colorprofile.Profile) sgrColor {
	if c == (theme.Color{}) {
		return sgrColor{}
	}
	switch p {
	case colorprofile.TrueColor:
		if !c.True.Valid {
			return sgrColor{}
		}
		return sgrPair(true, int(c.True.R), int(c.True.G), int(c.True.B))
	case colorprofile.ANSI256:
		n := int(c.C256)
		if n < 0 {
			if !c.True.Valid {
				return sgrColor{}
			}
			n = convertIndex(c.True, p)
		}
		return sgrPair(false, n, 0, 0)
	case colorprofile.ANSI:
		n := int(c.C16)
		switch {
		case n == -2:
			return sgrColor{omit: true}
		case n < 0:
			if !c.True.Valid {
				return sgrColor{}
			}
			n = convertIndex(c.True, p)
		}
		n = clampi(n, 0, 15)
		return sgrColor{code: sgr16FG[n], bgCode: sgr16BG[n]}
	}
	return sgrColor{}
}

// sgr16FG and sgr16BG are the basic-color parameters (30-37, 90-97 and
// 40-47, 100-107).
var sgr16FG, sgr16BG = func() (fg, bg [16]string) {
	for n := range 16 {
		if n < 8 {
			fg[n], bg[n] = strconv.Itoa(30+n), strconv.Itoa(40+n)
		} else {
			fg[n], bg[n] = strconv.Itoa(90+n-8), strconv.Itoa(100+n-8)
		}
	}
	return
}()

// sgrPair builds "38;2;r;g;b" + "48;2;r;g;b" (rgb) or "38;5;n" + "48;5;n"
// in one string and slices it into the fg and bg parameters.
func sgrPair(rgb bool, a, b, c int) sgrColor {
	var buf [48]byte
	param := func(out []byte, lead byte) []byte {
		out = append(out, lead, '8', ';')
		if rgb {
			out = append(out, '2', ';')
			out = strconv.AppendInt(out, int64(a), 10)
			out = append(out, ';')
			out = strconv.AppendInt(out, int64(b), 10)
			out = append(out, ';')
			return strconv.AppendInt(out, int64(c), 10)
		}
		out = append(out, '5', ';')
		return strconv.AppendInt(out, int64(a), 10)
	}
	out := param(buf[:0], '3')
	k := len(out)
	s := string(param(out, '4'))
	return sgrColor{code: s[:k], bgCode: s[k:]}
}

// convertIndex downsamples an RGB value to a palette index for p.
func convertIndex(rgb theme.RGB, p colorprofile.Profile) int {
	switch v := p.Convert(color.RGBA{rgb.R, rgb.G, rgb.B, 0xff}).(type) {
	case ansi.IndexedColor:
		return int(v)
	case ansi.BasicColor:
		return int(v)
	}
	return 7
}

// Plain returns glyphs only (golden tests, LLM-readable `--plain` output).
// Cells drawn with SGR 7 show their underlying lower block, so an upper 3/8
// body edge appears as '▅' here while the terminal shows the inverse.
func (g *Grid) Plain() string {
	var b strings.Builder
	b.Grow(g.W*g.H*3 + g.H)
	for y := 0; y < g.H; y++ {
		for x := 0; x < g.W; x++ {
			b.WriteRune(g.Cells[y*g.W+x].Ch)
		}
		if y < g.H-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
