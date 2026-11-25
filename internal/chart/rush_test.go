package chart

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/theme"
)

// TestRushGolden pins every setup-rush style on three skins at card and pane
// size (port spec 5).
func TestRushGolden(t *testing.T) {
	cs := loadBTC(t)
	th := hlpTheme(t)
	for _, skin := range []string{"terminal", "rush", "paper", "neon"} {
		for _, st := range []Style{Classic, Solid, Hollow, OHLC, LineStyle, Heikin} {
			for _, size := range [][3]int{{60, 14, 2}, {110, 40, 4}} {
				name := fmt.Sprintf("rush-%s-%s-%dx%d", skin, st, size[0], size[1])
				t.Run(name, func(t *testing.T) {
					o := rideOpts(cs, size[0], size[1])
					o.Skin, o.Style, o.Zoom = skin, st, size[2]
					g, vp := Candles(cs, o, th)
					if vp.Count == 0 {
						t.Fatal("no candles visible")
					}
					plain := g.Plain()
					assertWidths(t, name, plain, o.Width, o.Height)
					for _, p := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256, colorprofile.ANSI, colorprofile.ASCII, colorprofile.NoTTY} {
						assertWidths(t, name+"/"+p.String(), g.ANSI(p), o.Width, o.Height)
					}
					golden(t, name, plain)
				})
			}
		}
	}
}

// TestRushEveryPalette renders every skin at every common size without
// panics, at the exact frame size, with the ground painted.
func TestRushEveryPalette(t *testing.T) {
	cs := loadBTC(t)
	for _, p := range Palettes {
		for _, sz := range [][2]int{{24, 8}, {30, 12}, {40, 14}, {80, 16}, {140, 44}} {
			o := rideOpts(cs, sz[0], sz[1])
			o.Skin, o.Header, o.Cursor = p.Name, sz[1] > 16, 3
			g, _ := Candles(cs, o, hlpTheme(t))
			assertWidths(t, p.Name, g.Plain(), sz[0], sz[1])
			if g.At(0, sz[1]-1).BG != p.BG {
				t.Fatalf("%s %v: ground not painted", p.Name, sz)
			}
		}
	}
}

// TestPaletteBlends checks the pre-blended tokens against the port spec's
// table 4.2 (computed by chartport/blend.py, sRGB source-over).
func TestPaletteBlends(t *testing.T) {
	want := map[string][2]string{ // grid, entry
		"terminal": {"#161617", "#717276"},
		"paper":    {"#b5b3aa", "#787871"},
		"pro":      {"#1d212c", "#767a82"},
		"neon":     {"#200626", "#726e82"},
		"crt":      {"#08150b", "#6d7071"},
	}
	for name, w := range want {
		p, ok := PaletteByName(name)
		if !ok {
			t.Fatalf("no palette %s", name)
		}
		s := newSkin(p)
		if got := p.Grid.True.String(); !near(got, w[0]) {
			t.Errorf("%s grid %s, want %s", name, got, w[0])
		}
		if got := s.entry.True.String(); !near(got, w[1]) {
			t.Errorf("%s entry %s, want %s", name, got, w[1])
		}
	}
	p, _ := PaletteByName("terminal")
	s := newSkin(p)
	for _, c := range []struct {
		got  theme.Color
		want string
	}{{s.volUp, "#212122"}, {s.volDown, "#221010"}, {s.zoneT, "#181819"}, {s.zoneS, "#1b0f0f"},
		{s.nameBull, "#aeadab"}, {s.liveGain, "#349854"}, {s.liveLoss, "#c23b31"}} {
		if !near(c.got.True.String(), c.want) {
			t.Errorf("terminal token %s, want %s", c.got.True.String(), c.want)
		}
	}
}

// near allows one unit of rounding difference per channel.
func near(a, b string) bool {
	x, y := theme.Hex(a), theme.Hex(b)
	d := func(p, q uint8) bool { return absi(int(p)-int(q)) <= 1 }
	return x.Valid && y.Valid && d(x.R, y.R) && d(x.G, y.G) && d(x.B, y.B)
}

func TestPaletteNames(t *testing.T) {
	if Palettes[0].Name != DefaultPalette || len(Palettes) != 32 {
		t.Fatalf("palettes: first %q, %d total", Palettes[0].Name, len(Palettes))
	}
	for _, alias := range []string{"night", "Terminal", "", "highcontrast"} {
		if _, ok := PaletteByName(alias); !ok {
			t.Errorf("alias %q not found", alias)
		}
	}
	if _, ok := PaletteByName("nope"); ok {
		t.Error("unknown palette found")
	}
	// rush is terminal on the rush theme's ground (DESIGN.md section 2 rule 5)
	r, ok := PaletteByName("rush")
	term, _ := PaletteByName("terminal")
	rt, _ := theme.Get(theme.NameRush)
	if !ok || r.BG.True != rt.BG.True || !r.HollowUp() || r.Down != term.Down || r.Text != term.Text {
		t.Errorf("rush skin: ground %s (theme %s), up %v down %v", r.BG.True, rt.BG.True, r.Up, r.Down)
	}
	if rt.ChartSkin != r.Name {
		t.Errorf("theme rush pairs with skin %q, want %q", rt.ChartSkin, r.Name)
	}
	// the landing's ChartWall: hollow up bodies, solid red down (owner
	// direction, CRITIQUE.md follow-up)
	for _, n := range []string{"terminal", "rush"} {
		if p, _ := PaletteByName(n); !p.HollowUp() || p.Down.True == p.BG.True {
			t.Errorf("%s: up candles must be hollow (up == ground), down solid", n)
		}
	}
}

func TestParseStyle(t *testing.T) {
	for in, want := range map[string]Style{"bars": OHLC, "OHLC": OHLC, "": Classic, "ha": Heikin,
		"line": LineStyle, "lines": Lines, "hollow": Hollow, "blocks": Blocks, "solid": Solid} {
		if got, ok := ParseStyle(in); !ok || got != want {
			t.Errorf("ParseStyle(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := ParseStyle("pixel"); ok {
		t.Error("pixel accepted")
	}
}

func TestRushStep(t *testing.T) {
	for _, c := range [][2]float64{{0.14, 0.1}, {0.16, 0.2}, {2.9, 2}, {3, 5}, {6.9, 5}, {7, 10}, {140, 100}, {450, 500}, {0.0007, 0.001}, {0.00069, 0.0005}} {
		if got := rushStep(c[0]); math.Abs(got-c[1]) > c[1]*1e-9 {
			t.Errorf("rushStep(%v) = %v, want %v", c[0], got, c[1])
		}
	}
}

func TestRushPrice(t *testing.T) {
	for _, c := range []struct {
		p, step float64
		want    string
	}{
		{64210.5, 0, "64,211"}, {64210.5, 100, "64,211"}, {767.4, 0.1, "767.4"}, {767.43, 0.01, "767.43"},
		{151.32, 0, "151.3"}, {2.14, 0, "2.14"}, {0.0412, 0, "0.04120"}, {0.1523, 0, "0.1523"},
		{0.00001234, 0, "0.0₄1234"}, {-0.00001234, 0, "−0.0₄1234"}, {0, 0, "0"}, {232.25, 0.25, "232.25"},
	} {
		if got := rushPrice(c.p, c.step); got != c.want {
			t.Errorf("rushPrice(%v, %v) = %q, want %q", c.p, c.step, got, c.want)
		}
	}
	if stepDec(0.25) != 2 || stepDec(2.5) != 1 || stepDec(25) != 0 || stepDec(5e-9) != 9 {
		t.Error("stepDec")
	}
}

// TestMonotone: between two points the curve never leaves their range.
func TestMonotone(t *testing.T) {
	xs := []float64{0, 4, 8, 12, 16, 20}
	ys := []float64{10, 2, 2, 30, 29, 40}
	x0, out := monotone(xs, ys)
	if x0 != 0 || len(out) != 21 {
		t.Fatalf("x0 %d len %d", x0, len(out))
	}
	for i := 0; i < len(xs)-1; i++ {
		lo, hi := math.Min(ys[i], ys[i+1]), math.Max(ys[i], ys[i+1])
		for x := int(xs[i]); x <= int(xs[i+1]); x++ {
			if v := out[x]; v < lo-1e-9 || v > hi+1e-9 {
				t.Fatalf("x=%d y=%v outside [%v,%v]", x, v, lo, hi)
			}
		}
	}
}

// TestHollowGlyphs checks the open body on a tall hollow candle with wicks
// both sides: at body width 3 it is one joined box-drawing outline (┌┐ over
// │ │ over └┘, no ▏▕ eighths whose corners gap into [ ] brackets) with
// the wicks entering its rules as ┴ and ┬; at body width 1 the body is a
// heavy ┃.
func TestHollowGlyphs(t *testing.T) {
	cs := []domain.Candle{}
	for i := 0; i < 30; i++ {
		cs = append(cs, domain.Candle{T: int64(i) * 60000, O: 100, H: 101, L: 99, C: 100.5, V: 1})
	}
	cs = append(cs, domain.Candle{T: 30 * 60000, O: 100, H: 110, L: 90, C: 108, V: 1})
	cs = append(cs, domain.Candle{T: 31 * 60000, O: 100, H: 101, L: 99, C: 99.5, V: 1})
	column := func(g *Grid, x int) string {
		var col []rune
		for y := 0; y < g.H; y++ {
			col = append(col, g.At(x, y).Ch)
		}
		return string(col)
	}
	for _, zoom := range []int{4, 2} {
		g, vp := Candles(cs, CandleOpts{Width: 60, Height: 30, Zoom: zoom, Cursor: -1, VolRows: 0, Style: Hollow}, hlpTheme(t))
		x := vp.CenterX(30)
		if zoom == 4 {
			left, right := column(g, x-1), column(g, x+1)
			if !strings.Contains(left, "┌││") || !strings.Contains(left, "│└") || !strings.Contains(right, "┐││") || !strings.Contains(right, "│┘") ||
				strings.ContainsAny(left+right, "▏▕▁▔") {
				t.Errorf("zoom 4 edges %q / %q: want joined ┌│└ / ┐│┘ sides", left, right)
			}
			if c := column(g, x); !strings.Contains(c, "│┴") || !strings.Contains(c, "┬│") {
				t.Errorf("zoom 4 centre column %q: want the wicks joining the rules as ┴ and ┬", c)
			}
		}
		if zoom == 2 {
			// One column: a heavy ┃ body in the outline ink between │
			// wicks, never ║ (two rails) or a muddy tinted block.
			c := column(g, x)
			if strings.ContainsAny(c, "║█") || !strings.Contains(c, "┃") || !strings.Contains(c, "│") {
				t.Errorf("zoom 2 centre column %q: want a ┃ body with │ wicks, no ║ or █", c)
			}
		}
	}
}

// TestFlatHollow: a flat market (O=H=L=C) and a doji draw as ticks, never
// as a one-row hollow box (║, or side posts around a tick).
func TestFlatHollow(t *testing.T) {
	var cs []domain.Candle
	for i := 0; i < 30; i++ {
		cs = append(cs, domain.Candle{T: int64(i) * 60000, O: 100, H: 100, L: 100, C: 100, V: 1})
	}
	cs = append(cs, domain.Candle{T: 30 * 60000, O: 100, H: 100.4, L: 99.6, C: 100.001, V: 1})
	for _, zoom := range []int{2, 4} {
		g, _ := Candles(cs, CandleOpts{Width: 60, Height: 14, Zoom: zoom, Cursor: -1, Style: Hollow}, hlpTheme(t))
		plain := g.Plain()
		if strings.ContainsAny(plain, "║▏▕") {
			t.Errorf("zoom %d: flat candles drawn as boxes:\n%s", zoom, plain)
		}
		if !strings.Contains(plain, "━") {
			t.Errorf("zoom %d: no flat ticks:\n%s", zoom, plain)
		}
	}
}

// TestNarrowTag: without a price axis the live tag gets its own columns at
// the right and never covers a candle.
func TestNarrowTag(t *testing.T) {
	cs := loadBTC(t)
	for _, sz := range [][2]int{{30, 10}, {34, 14}} {
		o := rideOpts(cs, sz[0], sz[1])
		o.Lines, o.Markers, o.Zoom = nil, nil, 2
		g, vp := Candles(cs, o, hlpTheme(t))
		golden(t, fmt.Sprintf("rush-narrow-%dx%d", sz[0], sz[1]), g.Plain())
		edge := vp.CenterX(vp.First+vp.Count-1) + 1
		for y := 0; y < g.H; y++ {
			for x := 0; x <= edge && x < g.W; x++ {
				if c := g.At(x, y); c.Layer == LLabel && (c.Ch == '◥' || c.Ch == '◣') {
					t.Errorf("%v: tag cap at x=%d, over the candles (newest centre %d)", sz, x, edge-1)
				}
			}
		}
	}
}

// TestSkin256Neutral: at 256 colors a neutral skin token lands on a gray,
// not on a tinted cube entry (paper's warm-gray ground, the entry line).
func TestSkin256Neutral(t *testing.T) {
	isGray := func(i int16) bool {
		switch i {
		case 16, 59, 102, 145, 188, 231:
			return true
		}
		return i >= 232 && i <= 255
	}
	p, _ := PaletteByName("paper")
	s := newSkin(p)
	for name, c := range map[string]theme.Color{"paper bg": p.BG, "paper grid": p.Grid, "paper entry": s.entry} {
		if !isGray(c.C256) {
			t.Errorf("%s %s -> 256 index %d, want a gray", name, c.True.String(), c.C256)
		}
	}
	if p, _ := PaletteByName("terminal"); p.Bear.C256 < 16 || isGray(p.Bear.C256) {
		t.Errorf("terminal red -> %d, want a cube red", p.Bear.C256)
	}
}

// TestPanClampsToHistory: panning past the loaded history (the shoot's
// "-72 bars", left 60% blank) stops at the oldest candle with the window
// still full, on the skinned and the legacy renderer.
func TestPanClampsToHistory(t *testing.T) {
	cs := loadBTC(t)
	th := hlpTheme(t)
	for _, skin := range []string{"terminal", SkinNone} {
		base := CandleOpts{Width: 120, Height: 30, Zoom: 2, Cursor: -1, VolRows: -1, Skin: skin}
		_, full := Candles(cs, base, th)
		if full.MaxOffset != len(cs)-full.Count || full.Offset != 0 {
			t.Fatalf("%s: MaxOffset %d (want %d), Offset %d", skin, full.MaxOffset, len(cs)-full.Count, full.Offset)
		}
		o := base
		o.Offset = len(cs) + 500
		_, vp := Candles(cs, o, th)
		if vp.First != 0 || vp.Count != full.Count || vp.Offset != full.MaxOffset {
			t.Errorf("%s: over-pan shows First %d Count %d Offset %d; want 0, %d, %d", skin, vp.First, vp.Count, vp.Offset, full.Count, full.MaxOffset)
		}
		o.Offset = 5
		if _, vp := Candles(cs, o, th); vp.Offset != 5 || vp.First+vp.Count != len(cs)-5 {
			t.Errorf("%s: in-range pan changed: %+v", skin, vp)
		}
		// Fewer candles than fit: no pan at all.
		few := cs[len(cs)-10:]
		o.Offset = 8
		if _, vp := Candles(few, o, th); vp.Offset != 0 || vp.Count != len(few) || vp.MaxOffset != 0 {
			t.Errorf("%s: short history panned: %+v", skin, vp)
		}
	}
}
