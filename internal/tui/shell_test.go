package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/maxmoneycash/hlp-cli/internal/core"
)

// The rush shell chrome (DESIGN.md sections 4 to 7).

func TestWelcomeLeavesWithTheFirstCommand(t *testing.T) {
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	d := start(t, m, 140, 42)
	if !m.welcomeOnly() {
		t.Fatal("a fresh shell shows the welcome")
	}
	s := d.screen()
	for _, want := range []string{"/chart BTC   /vaults   /vault hlp", "ask or /", "┌─"} {
		if !strings.Contains(s, want) {
			t.Errorf("welcome is missing %q:\n%s", want, s)
		}
	}
	d.typeText("/chart BTC 1h")
	d.send(key("enter"))
	for _, e := range m.entries {
		if e.kind == entryWelcome {
			t.Fatal("the welcome entry stayed in the transcript")
		}
	}
	if s := d.screen(); strings.Contains(s, "ask or /") || strings.Contains(s, "/vault hlp") {
		t.Errorf("first-run copy after a command:\n%s", s)
	}
	if !m.seen.Seen(tipWelcome) {
		t.Error("the first-run line is a tip shown once ever")
	}
	// A new shell on the same data dir never shows the first-run line.
	m2 := New(Options{Env: m.env, Registry: m.reg})
	m2.reduced, m2.reducedSet = true, true
	t.Cleanup(m2.Close)
	d2 := start(t, m2, 140, 42)
	if strings.Contains(d2.screen(), "/vault hlp") {
		t.Error("the first-run line came back")
	}
}

func TestNoHintRowInTranscript(t *testing.T) {
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	d := start(t, m, 140, 42)
	d.typeText("/vaults")
	d.send(key("enter"))
	rows := strings.Split(d.screen(), "\n")
	if len(rows) != 42 {
		t.Fatalf("%d rows", len(rows))
	}
	if strings.Trim(rows[40], "─") != "" {
		t.Errorf("row H-2 should be the hairline: %q", rows[40])
	}
	last := rows[41]
	if !strings.HasPrefix(strings.TrimSpace(last), "❯") || !strings.HasSuffix(strings.TrimSpace(last), "? keys") {
		t.Errorf("last row should be the prompt with ? keys: %q", last)
	}
	for _, gone := range []string{"enter send", "ctrl+k palette", "mainnet", "no model", "live"} {
		if strings.Contains(strings.Join(rows, "\n"), gone) {
			t.Errorf("chrome still says %q", gone)
		}
	}
	if strings.TrimSpace(rows[39]) != "" && !strings.HasPrefix(strings.TrimSpace(rows[39]), "└─") {
		t.Errorf("one blank row (or the reticle) above the hairline: %q", rows[39])
	}
}

func TestOneReticleOnTheNewestCard(t *testing.T) {
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	d := start(t, m, 140, 80)
	for i := 0; i < 3; i++ {
		d.typeText("/vaults")
		d.send(key("enter"))
	}
	s := d.screen()
	if n := strings.Count(s, "┌─"); n != 1 {
		t.Fatalf("%d top reticles, want 1:\n%s", n, s)
	}
	if n := strings.Count(s, "└─"); n != 1 {
		t.Fatalf("%d bottom reticles, want 1", n)
	}
	rows := strings.Split(s, "\n")
	top := -1
	for i, r := range rows {
		if strings.HasPrefix(r, "┌─") {
			top = i
		}
	}
	if top < 0 || !strings.Contains(rows[top+1], "❯ /vaults") {
		t.Fatalf("the reticle should open above the newest echo line")
	}
	// Typing moves focus to the prompt: no reticle.
	d.typeText("/c")
	if strings.Contains(d.screen(), "┌─") {
		t.Error("no reticle while the input has text")
	}
}

func TestNoTicksWhenIdleUnfocusedOrReduced(t *testing.T) {
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	start(t, m, 140, 42)
	if cmd := m.motionCmd(); cmd != nil {
		t.Fatal("reduced motion must schedule no tick")
	}
	m.reduced = false
	m.blinkArmed, m.flapArmed = false, false
	if m.motionCmd() == nil {
		t.Fatal("a focused idle prompt blinks")
	}
	if m.motionCmd() != nil {
		t.Fatal("a pending tick must not be armed twice")
	}
	m.blinkArmed = false
	m.Update(tea.BlurMsg{})
	m.blinkArmed = false
	if cmd := m.motionCmd(); cmd != nil {
		t.Fatal("an unfocused terminal must schedule no tick")
	}
	if !m.cursorOn() {
		t.Fatal("the cursor is solid while unfocused")
	}
	// Flaps: a tick only while one is in flight.
	m.Update(tea.FocusMsg{})
	m.blinkArmed, m.flapArmed = true, false // isolate the flap
	m.mids["BTC"], m.prevDay["BTC"] = 64951, 65000
	m.setFlaps()
	if m.motionCmd() != nil {
		t.Fatal("a settled flap needs no tick")
	}
	now := time.Now()
	m.now = func() time.Time { return now }
	m.flaps["BTC"].Set("64,953 \u22120.1%", now.Add(-time.Hour)) // outside the throttle window
	m.flaps["BTC"].Set("64,957 \u22120.1%", now)
	if m.motionCmd() == nil {
		t.Fatal("a flap in flight ticks")
	}
}

func TestSeenTipsWrittenOnce(t *testing.T) {
	dir := t.TempDir()
	s := loadSeen(dir)
	p := filepath.Join(dir, "seen.json")
	s.Mark(tipNoModel)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]bool
	if json.Unmarshal(b, &got) != nil || !got[tipNoModel] {
		t.Fatalf("seen.json = %s", b)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	s.Mark(tipNoModel) // already seen: no write
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("a tip already seen was written again")
	}
	s.Mark(tipWelcome)
	if s2 := loadSeen(dir); !s2.Seen(tipWelcome) || !s2.Seen(tipNoModel) {
		t.Fatal("seen.json lost a tip")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".seen-*"))
	if len(matches) != 0 {
		t.Fatalf("temp files left: %v", matches)
	}
}

func TestEchoLineIsCut(t *testing.T) {
	for in, want := range map[string]string{
		"/positions 0xb83de012dba672c76a7dbbbf3e459cb59d7d6e36":               "/positions 0xb83d…6e36",
		"/backtest examples/pine/ema-cross.pine --coin BTC":                   "/backtest ema-cross.pine --coin BTC",
		"/arena --entrants a/s.pine@HYPE,b/e.pine@BTC,examples/thesis/x.yaml": "/arena --entrants s.pine@HYPE +2",
		"/chart PURR/USDC 1h":    "/chart PURR/USDC 1h",
		"/vaults --tag memes,ai": "/vaults --tag memes,ai",
	} {
		if got := echoText(in); got != want {
			t.Errorf("echoText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTopBarIsOneQuietLine(t *testing.T) {
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, true)
	d := start(t, m, 140, 42)
	m.mids["BTC"], m.prevDay["BTC"] = 64951, 65537
	m.tickerKey = ""
	d.send(wsStateMsg("live"))
	top := strings.Split(d.screen(), "\n")[0]
	if !strings.HasPrefix(top, "  hlp") || !strings.Contains(top, "BTC  64,951 \u22120.9%") || !strings.HasSuffix(strings.TrimRight(top, " "), "■") {
		t.Fatalf("top bar %q", top)
	}
	// In a pane: the crumb, no ticker, the mark stays.
	m.pushPane(newPane(&core.Result{Card: core.CardVaults, Data: sampleVaults()}, 0, core.Tool{}, core.Call{}))
	m.setFrame(m.render())
	top = ansi.Strip(m.render()[0])
	if strings.Contains(top, "BTC") || !strings.Contains(top, "› VAULTS") || !strings.HasSuffix(strings.TrimRight(top, " "), "■") {
		t.Fatalf("pane top bar %q", top)
	}
}
