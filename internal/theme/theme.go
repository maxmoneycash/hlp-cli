package theme

import (
	"fmt"
	"image/color"
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// RGB is a 24-bit color. Valid=false means "terminal default".
type RGB struct {
	R, G, B uint8
	Valid   bool
}

// Hex parses "#rrggbb" or "rrggbb". Invalid input yields RGB{} (terminal default).
func Hex(s string) RGB {
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return RGB{}
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return RGB{}
	}
	return RGB{uint8(v >> 16), uint8(v >> 8), uint8(v), true}
}

// String returns "#rrggbb", or "" for the terminal default.
func (c RGB) String() string {
	if !c.Valid {
		return ""
	}
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

// Mix returns a*(1-t) + b*t in sRGB space (good enough for UI tints).
func Mix(a, b RGB, t float64) RGB {
	l := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t + 0.5) }
	return RGB{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), true}
}

// Color is one palette token with explicit per-profile picks.
// C256/C16 of -1 mean "derive automatically" (only acceptable for tints).
//
// The indexes are int16 so a Color is 8 bytes: every chart cell carries two
// of them, and a 200x60 frame is 12,000 cells (PERFORMANCE #8).
type Color struct {
	True RGB
	C256 int16 // 0-255, or -1
	C16  int16 // 0-15, or -1 (use -2 for "omit entirely at 16 colors", e.g. gridlines)
}

// C builds a Color from a hex string and hand-picked fallbacks.
func C(hex string, c256, c16 int) Color {
	return Color{True: Hex(hex), C256: int16(c256), C16: int16(c16)}
}

// Resolve returns the color to paint for profile p, or nil for "no color"
// (ASCII/NoTTY profiles, or C16 == -2 at the ANSI profile).
func (c Color) Resolve(p colorprofile.Profile) color.Color {
	switch p {
	case colorprofile.TrueColor:
		if !c.True.Valid {
			return nil
		}
		return lipgloss.Color(c.True.String())
	case colorprofile.ANSI256:
		if c.C256 >= 0 {
			return lipgloss.Color(strconv.Itoa(int(c.C256)))
		}
		if c.True.Valid {
			return p.Convert(lipgloss.Color(c.True.String()))
		}
	case colorprofile.ANSI:
		if c.C16 >= 0 {
			return lipgloss.Color(strconv.Itoa(int(c.C16)))
		}
		if c.C16 == -1 && c.True.Valid {
			return p.Convert(lipgloss.Color(c.True.String()))
		}
	}
	return nil
}

// Theme is the complete palette. Chart tokens carry hand-picked per-profile values;
// chrome tokens drive the Crush-style TUI.
type Theme struct {
	Name string
	Dark bool

	// Chart tokens.
	BG       Color // assumed background, used for Mix() and PNG export only; never painted as page bg
	Up       Color
	Down     Color
	Grid     Color
	AxisLine Color
	AxisText Color
	Text     Color
	Dim      Color
	Cursor   Color // crosshair column tint
	MA       [3]Color
	VolMix   float64 // volume bar = Mix(BG, Up/Down, VolMix)

	// Chrome tokens.
	Accent  Color // brand accent: prompt, focused borders, selection
	Accent2 Color // secondary accent: gradients, pills
	Muted   Color
	Border  Color
	Surface Color // one step lighter (dark) or darker (light) than BG: status bar, pills, selected rows
	Warn    Color
	Error   Color
	Success Color

	// Rush tokens (DESIGN.md section 2). Themes that predate rush derive them
	// in Register: Raised from Surface, Floor from Border, AccentEdge from
	// Accent mixed toward BG.
	Raised     Color // one step above Surface: hover, selected on a surface, keycap press
	Floor      Color // floor-grid dots in empty states
	AccentEdge Color // keycap lip under the Accent face

	// ChartSkin is the chart.Palette name this theme pairs with in the TUI
	// ("rush" for rush). "" leaves the choice to config.
	ChartSkin string
}

var registry = map[string]Theme{}

// Register adds or replaces a theme. Called from init() in builtin.go.
// Names are stored lower-case.
func Register(t Theme) {
	t.Name = strings.ToLower(t.Name)
	registry[t.Name] = t.withDerived()
}

// withDerived fills the rush-era tokens a theme leaves zero, so every
// registered theme has a full token set (Validate checks all of them).
func (t Theme) withDerived() Theme {
	if !t.Raised.True.Valid {
		t.Raised = t.Surface
	}
	if !t.Floor.True.Valid {
		t.Floor = t.Border
		t.Floor.C16 = -2
	}
	if !t.AccentEdge.True.Valid && t.Accent.True.Valid {
		edge := Mix(t.Accent.True, t.BG.True, 0.35)
		t.AccentEdge = Color{True: edge, C256: near256(edge), C16: -2}
	}
	return t
}

// Get returns a theme by name (case-insensitive).
// Spellings with dashes, underscores or spaces match too ("tokyo-night"
// is tokyonight).
func Get(name string) (Theme, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if t, ok := registry[n]; ok {
		return t, true
	}
	t, ok := registry[strings.NewReplacer("-", "", "_", "", " ", "").Replace(n)]
	return t, ok
}

// Names returns registered theme names, sorted.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Default is the theme used when config names none: "rush" on dark and
// light backgrounds alike. On a light terminal the TUI repaints the ground
// (OSC 11) instead of switching palettes (DESIGN.md section 2).
func Default(darkBackground bool) Theme {
	_ = darkBackground
	t, _ := Get(NameRush)
	return t
}

// near256 is a hand-pick stand-in for derived tokens: Hue256 for a colored
// ink, otherwise the nearest step of the xterm gray ramp (232-255).
func near256(c RGB) int16 {
	if i := Hue256(c); i >= 0 {
		return i
	}
	y := (int(c.R) + int(c.G) + int(c.B)) / 3
	g := (y - 8 + 5) / 10
	return int16(232 + min(max(g, 0), 23))
}

// cubeLevels are the channel values of the xterm 6x6x6 color cube.
var cubeLevels = [6]int{0, 95, 135, 175, 215, 255}

// Hue256 returns the nearest xterm-256 color cube entry (16-231) that keeps
// c's hue: when c is saturated (HSV saturation above 0.2), grays and the
// cube's own neutral diagonal are skipped, so a dark tint of red stays red
// instead of collapsing into the gray ramp. It returns -1 for invalid or
// near-neutral colors (let the profile converter pick those). The result
// is int16 so it drops straight into Color.C256.
func Hue256(c RGB) int16 {
	if !c.Valid {
		return -1
	}
	hi := max(c.R, c.G, c.B)
	lo := min(c.R, c.G, c.B)
	if hi == 0 || float64(hi-lo)/float64(hi) <= 0.2 {
		return -1
	}
	best, bestD := -1, 1<<30
	for r := 0; r < 6; r++ {
		for g := 0; g < 6; g++ {
			for b := 0; b < 6; b++ {
				if r == g && g == b {
					continue
				}
				dr, dg, db := cubeLevels[r]-int(c.R), cubeLevels[g]-int(c.G), cubeLevels[b]-int(c.B)
				// weighted toward perceived luminance (green counts most)
				d := 3*dr*dr + 4*dg*dg + 2*db*db
				if d < bestD {
					best, bestD = 16+36*r+6*g+b, d
				}
			}
		}
	}
	return int16(best)
}
