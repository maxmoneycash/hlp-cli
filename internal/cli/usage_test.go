package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/maxmoneycash/hlp-cli/internal/core"
)

func TestSafeUsage(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"interval", "1m 3m 5m; default 15m", "Interval: 1m 3m 5m; default 15m"},
		{"address", "0x address of a user", "Address (0x…) of a user"},
		{"ma", "SMA periods to overlay", "Simple moving average periods to overlay"},
		{"capital", "", "Starting capital in USD (default 10000)"},
		{"coin", "market for Pine", "Market for Pine"},
		{"thing", "JSON blob", "Thing: JSON blob"},
	} {
		if got := safeUsage(c.name, c.in); got != c.want {
			t.Errorf("safeUsage(%q, %q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// Every flag usage in the command tree must start with a word the help
// renderer's title-casing leaves alone, and none may be empty.
func TestHelpUsagesSurviveTitleCase(t *testing.T) {
	isolate(t)
	a := &app{io: IO{Getenv: func(string) string { return "" }}, g: &globals{}, reg: core.Default()}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		check := func(f *pflag.Flag) {
			first := strings.Fields(f.Usage + " x")[0]
			if strings.TrimSpace(f.Usage) == "" || !survivesTitle(first) {
				t.Errorf("%s --%s: usage %q", c.CommandPath(), f.Name, f.Usage)
			}
		}
		c.LocalFlags().VisitAll(check)
		if strings.HasPrefix(c.Short, "Commands for") {
			t.Errorf("%s has the cobra default short", c.CommandPath())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(a.rootCmd())
}
