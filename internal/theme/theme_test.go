package theme

import (
	"errors"
	"flag"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/")

var profiles = []colorprofile.Profile{
	colorprofile.TrueColor, colorprofile.ANSI256, colorprofile.ANSI, colorprofile.Ascii, colorprofile.NoTTY,
}

func TestNamesSnapshot(t *testing.T) {
	want := []string{"catppuccin", "colorblind", "gruvbox", "hlp", "hyperliquid", "light", "mono", "paper", "rush", "tokyonight"}
	if _, ok := Get("tokyo-night"); !ok {
		t.Fatal("tokyo-night alias")
	}
	if got := Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

func TestEveryThemeValidates(t *testing.T) {
	for _, n := range Names() {
		th, _ := Get(n)
		if err := th.Validate(); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
}

func TestValidateCatchesBadTokens(t *testing.T) {
	th, _ := Get(NameHLP)
	th.Accent = C("#50E3C2", -1, 6) // automatic downsampling is not allowed
	th.Text = Color{C256: 252, C16: 15}
	th.Border.C16 = -2 // only tints may skip 16 colors
	th.Down = C("#101418", 203, 1)
	th.VolMix = 1
	err := th.Validate()
	if err == nil {
		t.Fatal("Validate accepted a broken theme")
	}
	for _, want := range []string{"Accent: C256 -1", "Text: no truecolor", "Border: C16 -2", "Down: contrast", "VolMix"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if err := (Theme{}).Validate(); err == nil || !strings.Contains(err.Error(), "no name") {
		t.Errorf("empty theme: %v", err)
	}
}

// TestContrast: Up and Down stay readable against the assumed background in
// truecolor (>= 3:1, the WCAG floor for graphical objects).
func TestContrast(t *testing.T) {
	for _, n := range Names() {
		th, _ := Get(n)
		for _, c := range []NamedColor{{"Up", th.Up}, {"Down", th.Down}, {"Text", th.Text}} {
			if r := Contrast(c.Color.True, th.BG.True); r < 3 {
				t.Errorf("%s.%s contrast %.2f < 3", n, c.Name, r)
			}
		}
		if r := Contrast(th.Text.True, th.BG.True); r < 7 {
			t.Errorf("%s.Text contrast %.2f < 7 (body text should be AAA)", n, r)
		}
	}
	if r := Contrast(Hex("#000000"), Hex("#FFFFFF")); math.Abs(r-21) > 1e-9 {
		t.Errorf("black/white contrast = %v, want 21", r)
	}
	if r := Contrast(Hex("#777777"), Hex("#777777")); r != 1 {
		t.Errorf("equal colors contrast = %v, want 1", r)
	}
	if l := Luminance(Hex("#FFFFFF")); math.Abs(l-1) > 1e-9 {
		t.Errorf("Luminance(white) = %v", l)
	}
}

// TestDarkFlag: Dark must match the background, or Default/Select pick wrong.
func TestDarkFlag(t *testing.T) {
	for _, n := range Names() {
		th, _ := Get(n)
		if dark := Luminance(th.BG.True) < 0.2; dark != th.Dark {
			t.Errorf("%s: Dark=%v but BG luminance says %v", n, th.Dark, dark)
		}
	}
}

// TestDistinctAtEveryProfile: the pairs a trader must tell apart never collapse
// to one color after the per-profile picks. Mono is exempt from the MA check at
// 16 colors (only four grays exist; documented in builtin.go).
func TestDistinctAtEveryProfile(t *testing.T) {
	for _, n := range Names() {
		th, _ := Get(n)
		type pair struct {
			a, b   string
			x, y   Color
			skip16 bool
		}
		pairs := []pair{
			{a: "Up", b: "Down", x: th.Up, y: th.Down},
			{a: "Text", b: "BG", x: th.Text, y: th.BG},
			{a: "AxisText", b: "BG", x: th.AxisText, y: th.BG},
			{a: "Accent", b: "BG", x: th.Accent, y: th.BG},
			{a: "Warn", b: "Error", x: th.Warn, y: th.Error, skip16: n == NameMono},
		}
		for i, ma := range th.MA {
			name := "MA" + string(rune('1'+i))
			pairs = append(pairs,
				pair{a: name, b: "Up", x: ma, y: th.Up, skip16: n == NameMono},
				pair{a: name, b: "Down", x: ma, y: th.Down, skip16: n == NameMono})
		}
		for _, p := range pairs {
			if p.x.True == p.y.True {
				t.Errorf("%s: %s == %s in truecolor", n, p.a, p.b)
			}
			if p.x.C256 == p.y.C256 {
				t.Errorf("%s: %s == %s at 256 colors (%d)", n, p.a, p.b, p.x.C256)
			}
			if !p.skip16 && p.x.C16 == p.y.C16 {
				t.Errorf("%s: %s == %s at 16 colors (%d)", n, p.a, p.b, p.x.C16)
			}
		}
	}
}

// TestPicksNearTruecolor catches typos in hand-picked 256-color indexes: a pick
// may deviate on purpose but never by a whole hue.
func TestPicksNearTruecolor(t *testing.T) {
	for _, n := range Names() {
		th, _ := Get(n)
		for _, nc := range th.Tokens() {
			got := xterm256(int(nc.Color.C256))
			if d := dist(got, nc.Color.True); d > 64 {
				t.Errorf("%s.%s: C256 %d (%s) is %.0f away from %s", n, nc.Name, nc.Color.C256, got, d, nc.Color.True)
			}
		}
	}
}

func xterm256(i int) RGB {
	switch {
	case i < 16:
		// Representative xterm defaults; only used for the distance check.
		std := []string{"000000", "800000", "008000", "808000", "000080", "800080", "008080", "c0c0c0",
			"808080", "ff0000", "00ff00", "ffff00", "0000ff", "ff00ff", "00ffff", "ffffff"}
		return Hex(std[i])
	case i < 232:
		i -= 16
		lv := func(x int) uint8 {
			if x == 0 {
				return 0
			}
			return uint8(55 + x*40)
		}
		return RGB{lv(i / 36), lv(i / 6 % 6), lv(i % 6), true}
	default:
		g := uint8(8 + (i-232)*10)
		return RGB{g, g, g, true}
	}
}

func dist(a, b RGB) float64 {
	dr, dg, db := float64(a.R)-float64(b.R), float64(a.G)-float64(b.G), float64(a.B)-float64(b.B)
	return math.Sqrt(dr*dr + dg*dg + db*db)
}

func TestResolve(t *testing.T) {
	c := C("#2B3139", 238, 8)
	cases := []struct {
		p    colorprofile.Profile
		want any
	}{
		{colorprofile.TrueColor, lipgloss.Color("#2b3139")},
		{colorprofile.ANSI256, lipgloss.Color("238")},
		{colorprofile.ANSI, lipgloss.Color("8")},
		{colorprofile.Ascii, nil},
		{colorprofile.NoTTY, nil},
	}
	for _, tc := range cases {
		got := c.Resolve(tc.p)
		if tc.want == nil {
			if got != nil {
				t.Errorf("%v: got %v, want nil", tc.p, got)
			}
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%v: got %v, want %v", tc.p, got, tc.want)
		}
	}
	// -2 omits the color at 16 colors; -1 derives it.
	if got := C("#1A1F26", 235, -2).Resolve(colorprofile.ANSI); got != nil {
		t.Errorf("C16=-2 resolved to %v", got)
	}
	if got := C("#FF0000", -1, -1).Resolve(colorprofile.ANSI); got == nil {
		t.Error("C16=-1 should derive a color")
	}
	if got := C("#FF0000", -1, -1).Resolve(colorprofile.ANSI256); got == nil {
		t.Error("C256=-1 should derive a color")
	}
	if got := (Color{C256: -1, C16: -1}).Resolve(colorprofile.TrueColor); got != nil {
		t.Errorf("terminal default resolved to %v", got)
	}
	// Every token of every theme resolves at every color profile, except the
	// documented 16-color skips.
	for _, n := range Names() {
		th, _ := Get(n)
		for _, nc := range th.Tokens() {
			for _, p := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256, colorprofile.ANSI} {
				if nc.Color.Resolve(p) == nil && !(p == colorprofile.ANSI && nc.Color.C16 == -2) {
					t.Errorf("%s.%s did not resolve at %v", n, nc.Name, p)
				}
			}
		}
	}
}

// TestAxisGrayStaysGray is a downsampling regression: the
// slate axis line must not downsample to teal 23 / cyan 6.
func TestAxisGrayStaysGray(t *testing.T) {
	th, _ := Get(NameHLP)
	if got := th.AxisLine.Resolve(colorprofile.ANSI256); !reflect.DeepEqual(got, lipgloss.Color("238")) {
		t.Errorf("256: %v", got)
	}
	if got := th.AxisLine.Resolve(colorprofile.ANSI); !reflect.DeepEqual(got, lipgloss.Color("8")) {
		t.Errorf("16: %v", got)
	}
}

func TestHexAndMix(t *testing.T) {
	if got := Hex("#26a69a"); got != (RGB{0x26, 0xa6, 0x9a, true}) {
		t.Errorf("Hex = %v", got)
	}
	if got := Hex("26A69A").String(); got != "#26a69a" {
		t.Errorf("String = %q", got)
	}
	for _, bad := range []string{"", "#123", "#GGGGGG", "#1234567"} {
		if Hex(bad).Valid {
			t.Errorf("Hex(%q) valid", bad)
		}
	}
	if (RGB{}).String() != "" {
		t.Error("invalid RGB should print empty")
	}
	if got := Mix(Hex("#000000"), Hex("#FFFFFF"), 0.5); got != (RGB{128, 128, 128, true}) {
		t.Errorf("Mix = %v", got)
	}
	if got := Mix(Hex("#102030"), Hex("#FFFFFF"), 0); got != Hex("#102030") {
		t.Errorf("Mix t=0 = %v", got)
	}
}

func TestGetSelectDefaultNext(t *testing.T) {
	if th, ok := Get("  TokyoNight "); !ok || th.Name != NameTokyoNight {
		t.Errorf("Get is not case-insensitive: %v %v", th.Name, ok)
	}
	if _, ok := Get("nope"); ok {
		t.Error("Get(nope) ok")
	}
	if Default(true).Name != NameRush || Default(false).Name != NameRush {
		t.Error("Default should be rush on dark and light backgrounds")
	}
	for _, tc := range []struct {
		name string
		dark bool
		want string
	}{{"", true, NameRush}, {"auto", false, NameRush}, {"AUTO", true, NameRush}, {"gruvbox", false, NameGruvbox}, {"hlp", true, NameHLP}, {"light", true, NameLight}} {
		th, err := Select(tc.name, tc.dark)
		if err != nil || th.Name != tc.want {
			t.Errorf("Select(%q,%v) = %s, %v", tc.name, tc.dark, th.Name, err)
		}
	}
	th, err := Select("solarized", true)
	var ute *UnknownThemeError
	if !errors.As(err, &ute) || th.Name != NameRush || !strings.Contains(err.Error(), "tokyonight") {
		t.Errorf("Select(unknown) = %s, %v", th.Name, err)
	}
	// Next cycles through every theme and wraps.
	seen := map[string]bool{}
	cur := NameHLP
	for range Names() {
		cur = Next(cur).Name
		seen[cur] = true
	}
	if len(seen) != len(Names()) || cur != NameHLP {
		t.Errorf("Next cycle visited %v, ended on %s", seen, cur)
	}
	if Next("unknown").Name != Names()[0] {
		t.Error("Next(unknown) should return the first theme")
	}
}

func TestRegisterLowercases(t *testing.T) {
	th, _ := Get(NameHLP)
	th.Name = "ZZTest"
	Register(th)
	defer delete(registry, "zztest")
	if _, ok := Get("zztest"); !ok {
		t.Fatal("registered theme not found")
	}
}

func TestStyles(t *testing.T) {
	for _, n := range Names() {
		th, _ := Get(n)
		for _, p := range profiles {
			s := th.Styles(p)
			// Badges always stay visible: a background color, or reverse video.
			for name, b := range map[string]lipgloss.Style{"Badge": s.Badge, "BadgeWarn": s.BadgeWarn, "BadgeError": s.BadgeError} {
				_, hasBG := b.GetBackground().(lipgloss.NoColor)
				if hasBG && !b.GetReverse() {
					t.Errorf("%s/%v: %s has neither background nor reverse", n, p, name)
				}
			}
			// Selection is visible at every profile.
			if _, noBG := s.Selected.GetBackground().(lipgloss.NoColor); noBG && !s.Selected.GetReverse() {
				t.Errorf("%s/%v: Selected is invisible", n, p)
			}
			// The accent is reserved: card heads and table headers do not use it.
			if p == colorprofile.TrueColor && reflect.DeepEqual(s.CardHead.GetForeground(), s.Accent.GetForeground()) {
				t.Errorf("%s: CardHead uses the accent", n)
			}
		}
	}
	th, _ := Get(NameHLP)
	s := th.Styles(colorprofile.TrueColor)
	if s.Sign(0.5).GetForeground() != s.Up.GetForeground() || s.Sign(0).GetForeground() != s.Up.GetForeground() {
		t.Error("Sign(>=0) should be Up")
	}
	if s.Sign(-0.1).GetForeground() != s.Down.GetForeground() {
		t.Error("Sign(<0) should be Down")
	}
	// No color: nothing paints a foreground.
	a := th.Styles(colorprofile.Ascii)
	if _, ok := a.Up.GetForeground().(lipgloss.NoColor); !ok {
		t.Errorf("Ascii Up has a color: %v", a.Up.GetForeground())
	}
}

// TestPreviewGolden freezes the plain layout of every theme's swatch and checks
// that every line has the requested width at every profile.
func TestPreviewGolden(t *testing.T) {
	var all strings.Builder
	for _, n := range Names() {
		th, _ := Get(n)
		for _, p := range profiles {
			out := Preview(th, p, 46)
			for i, line := range strings.Split(out, "\n") {
				if w := ansi.StringWidth(line); w != 46 {
					t.Errorf("%s/%v line %d width %d, want 46: %q", n, p, i, w, line)
				}
			}
		}
		all.WriteString(ansi.Strip(Preview(th, colorprofile.TrueColor, 46)))
		all.WriteString("\n\n")
	}
	// Narrow widths are clamped, never clipped.
	th, _ := Get(NameHLP)
	for _, line := range strings.Split(Preview(th, colorprofile.TrueColor, 3), "\n") {
		if w := ansi.StringWidth(line); w != PreviewMinWidth {
			t.Errorf("narrow line width %d: %q", w, line)
		}
	}
	// No color profiles emit no color SGR (bold/reverse may remain).
	for _, p := range []colorprofile.Profile{colorprofile.Ascii, colorprofile.NoTTY} {
		out := Preview(th, p, 40)
		if strings.Contains(out, "38;") || strings.Contains(out, "48;") {
			t.Errorf("%v preview carries color: %q", p, out)
		}
	}
	golden(t, "preview.golden", all.String())
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

// TestPreviewDump prints every theme's swatch in color for a human to look at:
// HLP_PREVIEW=1 go test ./internal/theme -run PreviewDump -v (inside tmux or a
// truecolor terminal). Set HLP_PREVIEW=256 or 16 to see the hand-picked palettes.
func TestPreviewDump(t *testing.T) {
	mode := os.Getenv("HLP_PREVIEW")
	if mode == "" {
		t.Skip("set HLP_PREVIEW=1 (truecolor), 256 or 16")
	}
	p := colorprofile.TrueColor
	switch mode {
	case "256":
		p = colorprofile.ANSI256
	case "16":
		p = colorprofile.ANSI
	}
	for _, n := range Names() {
		th, _ := Get(n)
		s := th.Styles(p)
		os.Stdout.WriteString(Preview(th, p, 46) + "\n")
		os.Stdout.WriteString(s.StatusBar.Render(" mainnet ") + " " + s.BadgeWarn.Render("TESTNET") + " " +
			s.Badge.Render("DRY RUN") + " " + s.BadgeError.Render("MAINNET") + " " + s.Selected.Render(" HLP Strategy A ") +
			" " + s.Key.Render("ctrl+k") + " " + s.KeyDesc.Render("palette") + " " + s.Dim.Render("hl · 2s ago") + "\n\n")
	}
}

func TestHue256KeepsHue(t *testing.T) {
	if Hue256(Hex("#808080")) != -1 || Hue256(RGB{}) != -1 {
		t.Fatal("neutral and invalid colors are left to the converter")
	}
	// a dark tint of red must stay reddish, never a gray
	idx := Hue256(Hex("#4a1c26"))
	if idx < 16 || idx > 231 {
		t.Fatalf("index %d", idx)
	}
	i := idx - 16
	r, g, b := i/36, (i/6)%6, i%6
	if !(r > g && r > b) {
		t.Fatalf("dark red mapped to %d (r%d g%d b%d)", idx, r, g, b)
	}
}
