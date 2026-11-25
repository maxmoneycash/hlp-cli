package theme

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Auto is the theme name that means "pick by terminal background".
const Auto = "auto"

// UnknownThemeError is returned by Select for a name that is not registered.
type UnknownThemeError struct {
	Name  string
	Known []string
}

func (e *UnknownThemeError) Error() string {
	return fmt.Sprintf("unknown theme %q (available: %s)", e.Name, strings.Join(e.Known, ", "))
}

// Select resolves a configured theme name. "" and "auto" return
// Default(darkBackground); any other name must be registered.
func Select(name string, darkBackground bool) (Theme, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" || n == Auto {
		return Default(darkBackground), nil
	}
	if t, ok := Get(n); ok {
		return t, nil
	}
	return Default(darkBackground), &UnknownThemeError{Name: name, Known: Names()}
}

// Next returns the theme after name in Names() order, wrapping around. It
// drives the TUI's ctrl+t cycle. An unknown name yields the first theme.
func Next(name string) Theme {
	names := Names()
	if len(names) == 0 {
		return Theme{}
	}
	n := strings.ToLower(name)
	for i, x := range names {
		if x == n {
			t, _ := Get(names[(i+1)%len(names)])
			return t
		}
	}
	t, _ := Get(names[0])
	return t
}

// Luminance returns the WCAG 2.x relative luminance of c (0 black .. 1 white).
func Luminance(c RGB) float64 {
	lin := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= 0.04045 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// Contrast returns the WCAG contrast ratio between a and b (1 .. 21).
func Contrast(a, b RGB) float64 {
	la, lb := Luminance(a), Luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// Tokens returns every color token with its field name, in declaration order.
// MA lines are named MA1..MA3.
func (t Theme) Tokens() []NamedColor {
	return []NamedColor{
		{"BG", t.BG}, {"Up", t.Up}, {"Down", t.Down}, {"Grid", t.Grid},
		{"AxisLine", t.AxisLine}, {"AxisText", t.AxisText}, {"Text", t.Text},
		{"Dim", t.Dim}, {"Cursor", t.Cursor},
		{"MA1", t.MA[0]}, {"MA2", t.MA[1]}, {"MA3", t.MA[2]},
		{"Accent", t.Accent}, {"Accent2", t.Accent2}, {"Muted", t.Muted},
		{"Border", t.Border}, {"Surface", t.Surface}, {"Warn", t.Warn},
		{"Error", t.Error}, {"Success", t.Success},
		{"Raised", t.Raised}, {"Floor", t.Floor}, {"AccentEdge", t.AccentEdge},
	}
}

// NamedColor is a token name and its value, as listed by Theme.Tokens.
type NamedColor struct {
	Name  string
	Color Color
}

// tintTokens may skip the 16-color profile (C16 = -2): painting them there
// would fill whole areas with black or gray. Surfaces become reverse video
// there, and the floor grid and keycap lip are omitted (DESIGN.md section 2).
var tintTokens = map[string]bool{"Grid": true, "Cursor": true, "Surface": true,
	"Raised": true, "Floor": true, "AccentEdge": true}

// Validate checks that every token has a valid truecolor value and explicit
// ANSI256/ANSI16 picks (C16 = -2 is allowed only for the tints: Grid,
// Cursor, Surface, Raised, Floor and AccentEdge), that VolMix is in (0, 1), and that Up and Down are readable on BG
// (contrast >= 3:1) and distinct at every profile.
func (t Theme) Validate() error {
	var errs []error
	if t.Name == "" {
		errs = append(errs, errors.New("theme has no name"))
	}
	for _, nc := range t.Tokens() {
		c := nc.Color
		if !c.True.Valid {
			errs = append(errs, fmt.Errorf("%s.%s: no truecolor value", t.Name, nc.Name))
		}
		if c.C256 < 0 || c.C256 > 255 {
			errs = append(errs, fmt.Errorf("%s.%s: C256 %d is not a hand-picked 0-255 index", t.Name, nc.Name, c.C256))
		}
		switch {
		case c.C16 >= 0 && c.C16 <= 15:
		case c.C16 == -2 && tintTokens[nc.Name]:
		default:
			errs = append(errs, fmt.Errorf("%s.%s: C16 %d is not a hand-picked 0-15 index", t.Name, nc.Name, c.C16))
		}
	}
	if t.VolMix <= 0 || t.VolMix >= 1 {
		errs = append(errs, fmt.Errorf("%s: VolMix %.2f outside (0, 1)", t.Name, t.VolMix))
	}
	for _, nc := range []NamedColor{{"Up", t.Up}, {"Down", t.Down}} {
		if r := Contrast(nc.Color.True, t.BG.True); r < 3 {
			errs = append(errs, fmt.Errorf("%s.%s: contrast %.2f:1 against BG, want >= 3:1", t.Name, nc.Name, r))
		}
	}
	if t.Up.True == t.Down.True || t.Up.C256 == t.Down.C256 || t.Up.C16 == t.Down.C16 {
		errs = append(errs, fmt.Errorf("%s: Up and Down collide at some color profile", t.Name))
	}
	return errors.Join(errs...)
}
