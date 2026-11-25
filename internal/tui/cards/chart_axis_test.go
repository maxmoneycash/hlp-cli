package cards

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"

	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/theme"
)

var axisLabel = regexp.MustCompile(`\s[0-9][0-9,]*(\.[0-9]+)?\s*$`)

// The inline TUI chart must show a readable price scale: at least three
// labelled gridlines at both inline heights, beside the live price tag.
func TestCompactChartHasThreeAxisLabels(t *testing.T) {
	for _, compact := range []bool{true, false} {
		o := plainOpts(120)
		o.Compact = compact
		out := Render(&core.Result{Card: core.CardChart, Data: fixtureChart(t)}, o)
		n := 0
		for _, l := range strings.Split(out, "\n") {
			if !strings.ContainsAny(l, "◥◣") && axisLabel.MatchString(l) {
				n++
			}
		}
		if n < 3 {
			t.Errorf("compact=%v: %d axis labels:\n%s", compact, n, out)
		}
	}
}

// A light UI theme must not paint the dark setup-rush ground (a black slab
// of 48;5;232 behind every chart row).
func TestLightThemeChartHasNoBlackGround(t *testing.T) {
	th, ok := theme.Get("light")
	if !ok {
		th = theme.Default(false)
	}
	if th.Dark {
		t.Skip("no light theme")
	}
	for _, p := range []colorprofile.Profile{colorprofile.ANSI256, colorprofile.TrueColor} {
		out := Render(&core.Result{Card: core.CardChart, Data: fixtureChart(t)}, Opts{Width: 100, Theme: th, Profile: p})
		if strings.Contains(out, "48;5;232") || strings.Contains(out, "48;5;16m") || strings.Contains(out, "48;2;10;10;11") {
			t.Fatalf("%v: light theme chart paints a dark ground", p)
		}
	}
}
