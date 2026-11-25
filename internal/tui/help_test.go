package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/maxmoneycash/hlp/internal/config"
	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/theme"
)

// The ? overlay at 80x24: command lists wrap inside their column (never
// back to column 0), and the overlay fits the height it is given.
func TestHelpOverlayFits(t *testing.T) {
	th, _ := theme.Get("hlp")
	env := &core.Env{Config: config.Defaults(), Theme: th, Profile: colorprofile.TrueColor, Now: time.Now}
	env.Paths.DataDir = t.TempDir()
	m := New(Options{Env: env, Registry: core.Default()})
	for _, h := range []int{18, 60} {
		lines := m.helpLines(80, h)
		if len(lines) > h {
			t.Fatalf("H=%d: %d lines", h, len(lines))
		}
		all := ansi.Strip(strings.Join(lines, "\n"))
		if !strings.Contains(all, "/vaults") || !strings.Contains(all, "/trades") {
			t.Fatalf("commands missing:\n%s", all)
		}
		for _, l := range lines {
			p := ansi.Strip(l)
			if strings.HasPrefix(p, "/") || lipglossWidth(p) > 80 {
				t.Fatalf("H=%d: line escapes the layout: %q", h, p)
			}
		}
	}
}

func lipglossWidth(s string) int { return ansi.StringWidth(s) }
