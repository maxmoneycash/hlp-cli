package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/maxmoneycash/hlp-cli/internal/core"
)

// TestDumpScreens renders real frames against core's offline fixture env
// so a human can look at the shell without a terminal. Opt-in:
//
//	HLP_TUI_DUMP=/tmp/screens HLP_FIXTURES=<dir> go test ./internal/tui -run DumpScreens
//
// Each step writes <n>-<name>.txt (plain) and .ansi (styled).
func TestDumpScreens(t *testing.T) {
	out := os.Getenv("HLP_TUI_DUMP")
	if out == "" || os.Getenv("HLP_FIXTURES") == "" {
		t.Skip("set HLP_TUI_DUMP and HLP_FIXTURES to dump screens")
	}
	home := t.TempDir()
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(k, home)
	}
	env, err := core.NewEnv(context.Background(), core.EnvOptions{Offline: true, Profile: colorprofile.TrueColor, Theme: "hlp"})
	if err != nil {
		t.Fatal(err)
	}
	m := New(Options{Env: env, Registry: core.Default()})
	t.Cleanup(m.Close)
	w, h := 140, 45
	d := start(t, m, w, h)
	n := 0
	shot := func(name string) {
		n++
		d.drain()
		time.Sleep(30 * time.Millisecond)
		d.drain()
		frame := strings.Join(m.render(), "\n")
		base := filepath.Join(out, fmt.Sprintf("%02d-%s", n, name))
		_ = os.MkdirAll(out, 0o755)
		_ = os.WriteFile(base+".ansi", []byte(frame+"\n"), 0o644)
		_ = os.WriteFile(base+".txt", []byte(ansi.Strip(frame)+"\n"), 0o644)
	}
	run := func(line string) {
		d.typeText(line)
		d.send(key("enter"))
		deadline := time.Now().Add(3 * time.Second)
		for len(m.jobs) > 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
			d.drain()
		}
	}
	shot("welcome")
	d.typeText("/va")
	shot("autocomplete")
	d.send(key("esc"))
	for range 3 {
		d.send(key("backspace"))
	}
	run("/chart BTC")
	shot("chart-inline")
	run("/vaults")
	shot("vaults-inline")
	d.send(key("enter"))
	shot("vaults-pane")
	d.send(key("s"), key("s"), key("s"), key("s"), key("s"))
	shot("vaults-sorted")
	d.send(key("down"), key("enter"))
	deadline := time.Now().Add(3 * time.Second)
	for len(m.jobs) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		d.drain()
	}
	shot("tearsheet")
	d.send(key("esc"), key("esc"))
	run("/book BTC")
	shot("book")
	d.send(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	shot("book-pane")
	d.send(key("esc"))
	run("/chart BTC")
	d.send(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}, key("c"), key("left"), key("left"))
	shot("chart-pane-crosshair")
	d.send(key("esc"))
	d.send(key("ctrl+k"))
	d.typeText("fund")
	shot("palette")
	d.send(key("esc"), key("?"))
	shot("help")
	d.send(key("x"), key("ctrl+b"))
	shot("side-panel")
	d.send(key("ctrl+b"))
	run("/nope")
	run("what should i buy")
	shot("suggestions")
	d.send(tea.WindowSizeMsg{Width: 80, Height: 30})
	shot("narrow")
}
