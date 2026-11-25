package theme

import (
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// rushWant is the DESIGN.md section 2 token table: truecolor, 256 and 16.
// A 16 of -2 means omitted at 16 colors (reverse video or not drawn).
var rushWant = map[string]struct {
	hex       string
	c256, c16 int
}{
	"BG":         {"#131315", 233, 0},
	"Surface":    {"#1a1a1d", 234, -2},
	"Raised":     {"#202023", 235, -2},
	"Border":     {"#303034", 236, 8},
	"Grid":       {"#1e1e21", 234, -2},
	"Floor":      {"#2c2c30", 236, -2},
	"Text":       {"#f4f3ef", 255, 15},
	"Muted":      {"#97979c", 246, 7},
	"Dim":        {"#6b6b70", 242, 8},
	"Accent":     {"#ff5a1f", 202, 3},
	"AccentEdge": {"#a8360b", 130, -2},
	"Up":         {"#f4f3ef", 255, 15},
	"Down":       {"#ff4b3e", 203, 9},
	"Warn":       {"#e9b949", 179, 11},
	"Error":      {"#ff4b3e", 203, 9},
	"Success":    {"#f4f3ef", 255, 15},
	"MA1":        {"#8ab4f8", 111, 12},
	"MA2":        {"#b4a2ff", 147, 13},
	"MA3":        {"#e9b949", 179, 11},
	"Accent2":    {"#8ab4f8", 111, 12},
	"AxisLine":   {"#303034", 236, 8},
	"AxisText":   {"#97979c", 246, 7},
	"Cursor":     {"#202023", 235, -2},
}

func rush(t *testing.T) Theme {
	t.Helper()
	th, ok := Get(NameRush)
	if !ok {
		t.Fatal("rush is not registered")
	}
	return th
}

// TestRushTokensExact: every rush token resolves at TrueColor, ANSI256 and
// ANSI to exactly the DESIGN.md section 2 values.
func TestRushTokensExact(t *testing.T) {
	th := rush(t)
	toks := th.Tokens()
	if len(toks) != len(rushWant) {
		t.Errorf("rush has %d tokens, the table has %d", len(toks), len(rushWant))
	}
	for _, nc := range toks {
		w, ok := rushWant[nc.Name]
		if !ok {
			t.Errorf("token %s is not in the DESIGN table", nc.Name)
			continue
		}
		c := nc.Color
		if got := c.Resolve(colorprofile.TrueColor); !reflect.DeepEqual(got, lipgloss.Color(w.hex)) {
			t.Errorf("%s truecolor = %v, want %s", nc.Name, got, w.hex)
		}
		if got := c.Resolve(colorprofile.ANSI256); !reflect.DeepEqual(got, lipgloss.Color(itoa(w.c256))) {
			t.Errorf("%s 256 = %v, want %d", nc.Name, got, w.c256)
		}
		got16 := c.Resolve(colorprofile.ANSI)
		if w.c16 == -2 {
			if got16 != nil {
				t.Errorf("%s 16 = %v, want omitted", nc.Name, got16)
			}
		} else if !reflect.DeepEqual(got16, lipgloss.Color(itoa(w.c16))) {
			t.Errorf("%s 16 = %v, want %d", nc.Name, got16, w.c16)
		}
		if c.Resolve(colorprofile.Ascii) != nil {
			t.Errorf("%s paints with no color", nc.Name)
		}
	}
	if th.VolMix != 0.30 || th.ChartSkin != "rush" || !th.Dark {
		t.Errorf("VolMix %v ChartSkin %q Dark %v", th.VolMix, th.ChartSkin, th.Dark)
	}
	for _, n := range Names() {
		if o, _ := Get(n); n != NameRush && n != NamePaper && o.ChartSkin != "" {
			t.Errorf("%s pairs with chart skin %q; only rush and paper have one", n, o.ChartSkin)
		}
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestRushContrast(t *testing.T) {
	th := rush(t)
	for _, c := range []struct {
		name string
		c    Color
		min  float64
	}{{"Text", th.Text, 15}, {"Muted", th.Muted, 4.5}, {"Accent", th.Accent, 4.5}, {"Down", th.Down, 4.5}} {
		if r := Contrast(c.c.True, th.BG.True); r < c.min {
			t.Errorf("%s on BG %.2f:1, want >= %.1f", c.name, r, c.min)
		}
	}
}

// hue returns the HSV hue of c in degrees and its saturation.
func hue(c RGB) (h, s float64) {
	r, g, b := float64(c.R)/255, float64(c.G)/255, float64(c.B)/255
	hi, lo := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	d := hi - lo
	if hi > 0 {
		s = d / hi
	}
	switch {
	case d == 0:
		return 0, s
	case hi == r:
		h = 60 * math.Mod((g-b)/d, 6)
	case hi == g:
		h = 60 * ((b-r)/d + 2)
	default:
		h = 60 * ((r-g)/d + 4)
	}
	if h < 0 {
		h += 360
	}
	return h, s
}

// TestRushNoTealOrGreen: no rush token except the MA lines has a hue in
// 140..200 degrees, at any saturation.
func TestRushNoTealOrGreen(t *testing.T) {
	for _, nc := range rush(t).Tokens() {
		if strings.HasPrefix(nc.Name, "MA") {
			continue
		}
		if h, s := hue(nc.Color.True); s > 0 && h >= 140 && h <= 200 {
			t.Errorf("%s %s has hue %.0f (teal or green)", nc.Name, nc.Color.True, h)
		}
	}
	if h, _ := hue(Hex("#26A69A")); h < 140 || h > 200 {
		t.Fatalf("hue check is broken: hlp teal reads %.0f", h)
	}
}

// TestRushAccentNotDown: automatic downsampling would turn the orange into
// red 196; the hand picks keep Accent and Down apart at every profile.
func TestRushAccentNotDown(t *testing.T) {
	th := rush(t)
	if th.Accent.C256 != 202 || th.Down.C256 != 203 {
		t.Errorf("256: Accent %d, Down %d; want 202 and 203", th.Accent.C256, th.Down.C256)
	}
	if th.Accent.C16 != 3 || th.Down.C16 != 9 {
		t.Errorf("16: Accent %d, Down %d; want 3 and 9", th.Accent.C16, th.Down.C16)
	}
	if th.Warn.C16 == th.Accent.C16 {
		t.Error("Warn and Accent collide at 16 colors")
	}
}

func TestRushStyles(t *testing.T) {
	th := rush(t)
	s := th.Styles(colorprofile.TrueColor)
	noColor := func(c any) bool { _, ok := c.(lipgloss.NoColor); return ok }

	// No boxes anywhere.
	for name, st := range map[string]lipgloss.Style{"Card": s.Card, "Input": s.Input} {
		if b, top, right, bottom, left := st.GetBorder(); top || right || bottom || left || b != (lipgloss.Border{}) {
			t.Errorf("%s still draws a border", name)
		}
	}
	if !reflect.DeepEqual(s.Label.GetForeground(), lipgloss.Color("#97979c")) || s.Label.GetBold() {
		t.Errorf("Label: fg %v bold %v; want Muted, not bold", s.Label.GetForeground(), s.Label.GetBold())
	}
	if !reflect.DeepEqual(s.Reticle.GetForeground(), lipgloss.Color("#ff5a1f")) {
		t.Errorf("Reticle fg %v", s.Reticle.GetForeground())
	}
	if !reflect.DeepEqual(s.Keycap.GetBackground(), lipgloss.Color("#ff5a1f")) ||
		!reflect.DeepEqual(s.Keycap.GetForeground(), lipgloss.Color("#131315")) || !s.Keycap.GetBold() {
		t.Errorf("Keycap bg %v fg %v bold %v", s.Keycap.GetBackground(), s.Keycap.GetForeground(), s.Keycap.GetBold())
	}
	if !reflect.DeepEqual(s.KeycapEdge.GetForeground(), lipgloss.Color("#a8360b")) || !s.KeycapLip() {
		t.Errorf("KeycapEdge fg %v", s.KeycapEdge.GetForeground())
	}
	if !reflect.DeepEqual(s.FlapFace.GetBackground(), lipgloss.Color("#1a1a1d")) {
		t.Errorf("FlapFace bg %v", s.FlapFace.GetBackground())
	}
	if !reflect.DeepEqual(s.FloorDot.GetForeground(), lipgloss.Color("#2c2c30")) || s.FloorFunc() == nil {
		t.Errorf("FloorDot fg %v", s.FloorDot.GetForeground())
	}
	if got := ansi.Strip(s.FloorFunc()("·")); got != "·" {
		t.Errorf("FloorFunc changed the glyph: %q", got)
	}
	// Gains are ink.
	if !reflect.DeepEqual(s.Sign(0).GetForeground(), s.Text.GetForeground()) {
		t.Error("rush Sign(>=0) should be ink")
	}
	if !reflect.DeepEqual(s.Sign(-1).GetForeground(), lipgloss.Color("#ff4b3e")) {
		t.Error("rush Sign(<0) should be red")
	}

	// 256 colors: hand picks.
	s256 := th.Styles(colorprofile.ANSI256)
	if !reflect.DeepEqual(s256.Keycap.GetBackground(), lipgloss.Color("202")) || !s256.KeycapLip() {
		t.Errorf("256 keycap bg %v", s256.Keycap.GetBackground())
	}

	// 16 colors: the keycap is reverse video of the accent, no lip, no flap
	// face, no floor.
	s16 := th.Styles(colorprofile.ANSI)
	if !s16.Keycap.GetReverse() || !reflect.DeepEqual(s16.Keycap.GetForeground(), lipgloss.Color("3")) {
		t.Errorf("16 keycap: reverse %v fg %v", s16.Keycap.GetReverse(), s16.Keycap.GetForeground())
	}
	if s16.KeycapLip() || !noColor(s16.FlapFace.GetBackground()) || s16.FloorFunc() != nil {
		t.Error("16 colors must omit the lip, the flap face and the floor")
	}

	// No color: reverse video keycap, nothing else painted.
	for _, p := range []colorprofile.Profile{colorprofile.Ascii, colorprofile.NoTTY} {
		sa := th.Styles(p)
		if !sa.Keycap.GetReverse() || sa.FloorFunc() != nil || sa.KeycapLip() {
			t.Errorf("%v: keycap reverse %v, floor %v, lip %v", p, sa.Keycap.GetReverse(), sa.FloorFunc() != nil, sa.KeycapLip())
		}
		for name, st := range map[string]lipgloss.Style{"Label": sa.Label, "Reticle": sa.Reticle} {
			if !noColor(st.GetForeground()) {
				t.Errorf("%v: %s has a color", p, name)
			}
		}
	}
}

// TestDerivedTokens: themes that predate rush get Raised, Floor and
// AccentEdge filled in, so every style resolves.
func TestDerivedTokens(t *testing.T) {
	for _, n := range Names() {
		th, _ := Get(n)
		for _, c := range []NamedColor{{"Raised", th.Raised}, {"Floor", th.Floor}, {"AccentEdge", th.AccentEdge}} {
			if !c.Color.True.Valid || c.Color.C256 < 0 {
				t.Errorf("%s.%s not derived: %+v", n, c.Name, c.Color)
			}
		}
	}
	h, _ := Get(NameHLP)
	if h.Raised != h.Surface || h.Floor.True != h.Border.True || h.Floor.C16 != -2 {
		t.Errorf("hlp derived Raised %v Floor %v", h.Raised, h.Floor)
	}
}

// TestPaperNoGreen: the paper app theme follows rush's rules on a light
// ground: ink gains, no green or teal token, a paired chart skin.
func TestPaperNoGreen(t *testing.T) {
	th, ok := Get(NamePaper)
	if !ok || th.Dark || th.ChartSkin != "paper" || th.Up != th.Text {
		t.Fatalf("paper: ok %v dark %v skin %q up %v", ok, th.Dark, th.ChartSkin, th.Up)
	}
	if c := Contrast(th.Text.True, th.BG.True); c < 7 {
		t.Errorf("paper text contrast %.1f", c)
	}
	if c := Contrast(th.Muted.True, th.BG.True); c < 4.5 {
		t.Errorf("paper muted contrast %.1f", c)
	}
}
