package tui

import (
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"

	"github.com/maxmoneycash/hlp-cli/internal/config"
	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/theme"
)

// While hlp paints the terminal ground, cells painted with that same ground
// become the default background, so a translucent terminal shows one even
// glass instead of opaque chart blocks.
func TestGroundCellsUseTheDefaultBackground(t *testing.T) {
	th, _ := theme.Get("rush")
	env := &core.Env{Config: config.Defaults(), Theme: th, Profile: colorprofile.TrueColor, Now: time.Now}
	env.Config.Theme = "rush"
	env.Paths.DataDir = t.TempDir()
	m := New(Options{Env: env, Registry: fakeRegistry(t, &fakes{})})
	t.Cleanup(m.Close)
	if !m.paintsBackground() {
		t.Skip("rush does not paint its ground in this configuration")
	}
	m.setFrame([]string{"\x1b[38;2;1;2;3;48;2;19;19;21mA\x1b[48;2;19;19;21mB\x1b[48;2;19;19;210mC\x1b[48;2;19;19;21;1mD"})
	want := "\x1b[38;2;1;2;3;49mA\x1b[49mB\x1b[48;2;19;19;210mC\x1b[49;1mD"
	if got := m.frame[0]; got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}
