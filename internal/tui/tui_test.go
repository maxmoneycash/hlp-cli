package tui

import (
	"context"
	"encoding/json"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/maxmoneycash/hlp-cli/internal/agent"
	"github.com/maxmoneycash/hlp-cli/internal/config"
	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/hyperliquid/ws"
	"github.com/maxmoneycash/hlp-cli/internal/llm"
	"github.com/maxmoneycash/hlp-cli/internal/theme"
)

// ---- fixtures ---------------------------------------------------------------

func loadCandles(t testing.TB) []domain.Candle {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "testdata", "candles", "btc-15m.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	f := func(v any) float64 {
		s, _ := v.(string)
		x, _ := strconv.ParseFloat(s, 64)
		return x
	}
	out := make([]domain.Candle, len(raw))
	for i, r := range raw {
		out[i] = domain.Candle{T: int64(r["t"].(float64)), O: f(r["o"]), H: f(r["h"]), L: f(r["l"]), C: f(r["c"]), V: f(r["v"])}
	}
	return out
}

func sampleVaults() []domain.VaultSummary {
	mk := func(name, addr string, tvl, all, dd, sharpe float64, curated bool) domain.VaultSummary {
		return domain.VaultSummary{Name: name, Address: addr, TVL: tvl, MaxDD: dd, Sharpe: sharpe, Curated: curated, AgeDays: 300,
			Returns: map[domain.Window]float64{domain.WAll: all}, AllowDeposits: true}
	}
	return []domain.VaultSummary{
		mk("Hyperliquidity Provider (HLP)", "0xdfc24b077bc1425ad1dea75bcb6f8158e10df303", 151e6, 0.9, -0.08, 1.9, true),
		mk("Long HYPE & BTC | Short Garbage", "0xac26cf5f3c46b5e102048c65b977d2551b72a9c7", 2.7e6, 4.39, -0.52, 1.1, true),
		mk("Grid Bot Alpha", "0x1111111111111111111111111111111111111111", 9e5, 0.31, -0.12, 2.4, false),
		mk("Gridiron", "0x2222222222222222222222222222222222222222", 4e5, -0.1, -0.33, -0.2, false),
	}
}

// ---- fakes ------------------------------------------------------------------

// fakeStream counts live subscriptions so tests can assert nothing leaks.
type fakeStream struct{ subs atomic.Int64 }

func (s *fakeStream) Subscribe(ctx context.Context, sub ws.Subscription) (<-chan ws.Event, func(), error) {
	s.subs.Add(1)
	var once sync.Once
	return make(chan ws.Event), func() { once.Do(func() { s.subs.Add(-1) }) }, nil
}
func (s *fakeStream) Close() error { return nil }

type fakes struct {
	stream   *fakeStream
	canceled atomic.Bool
	calls    atomic.Int64
}

func fakeRegistry(t testing.TB, f *fakes) *core.Registry {
	cs := loadCandles(t)
	r := core.NewRegistry()
	chartRun := func(_ context.Context, _ *core.Env, in any) (*core.Result, error) {
		f.calls.Add(1)
		ci := in.(*core.ChartInput)
		iv := ci.Interval
		if iv == "" {
			iv = domain.I15m
		}
		return &core.Result{Card: core.CardChart, Data: &core.ChartData{Coin: strings.ToUpper(ci.Coin), Interval: iv, Candles: cs,
			Summary: core.ChartSummary{Last: cs[len(cs)-1].C}}, Meta: domain.Meta{Source: "fixture"}}, nil
	}
	r.Register(core.Tool{Name: "get_candles", Slash: "chart", Aliases: []string{"c"}, Positional: []string{"coin", "interval"},
		Group: "market", Streaming: true, Summary: "Candlestick chart", NewInput: func() any { return new(core.ChartInput) }, Run: chartRun})
	r.Register(core.Tool{Name: "list_vaults", Slash: "vaults", Aliases: []string{"v"}, Positional: []string{"query"}, Group: "vault",
		Summary: "Vault screener", NewInput: func() any { return new(core.VaultsInput) },
		Run: func(context.Context, *core.Env, any) (*core.Result, error) {
			return &core.Result{Card: core.CardVaults, Data: sampleVaults(), Meta: domain.Meta{Source: "fixture"}}, nil
		}})
	r.Register(core.Tool{Name: "get_vault", Slash: "vault", Positional: []string{"vault"}, Group: "vault",
		Summary: "Vault tearsheet", NewInput: func() any { return new(core.VaultInput) },
		Run: func(_ context.Context, _ *core.Env, in any) (*core.Result, error) {
			v := in.(*core.VaultInput)
			for _, s := range sampleVaults() {
				if s.Address == v.Vault || strings.Contains(strings.ToLower(s.Name), strings.ToLower(v.Vault)) {
					return &core.Result{Card: core.CardVault, Title: s.Name, Data: domain.VaultDetail{Summary: s}}, nil
				}
			}
			return nil, core.Errorf(core.CodeUnknownVault, "no vault %q", v.Vault)
		}})
	r.Register(core.Tool{Name: "slow", Slash: "slow", Group: "meta", Summary: "Blocks until canceled",
		NewInput: func() any { return new(struct{}) },
		Run: func(ctx context.Context, _ *core.Env, _ any) (*core.Result, error) {
			<-ctx.Done()
			f.canceled.Store(true)
			return nil, ctx.Err()
		}})
	r.Register(core.Tool{Name: "live", Slash: "live", Group: "meta", Streaming: true, Summary: "Streams forever",
		NewInput: func() any { return new(struct{}) },
		Run: func(context.Context, *core.Env, any) (*core.Result, error) {
			return &core.Result{Card: core.CardKV, Title: "live", Data: [][2]string{{"tick", "0"}}}, nil
		},
		Stream: func(ctx context.Context, env *core.Env, _ any, emit func(*core.Result)) error {
			_, cancel, err := f.stream.Subscribe(ctx, ws.Subscription{Type: "trades", Coin: "BTC"})
			if err != nil {
				return err
			}
			defer cancel()
			emit(&core.Result{Card: core.CardKV, Title: "live", Data: [][2]string{{"tick", "1"}}})
			<-ctx.Done()
			return nil
		}})
	r.Register(core.Tool{Name: "place_order", Slash: "buy", Positional: []string{"coin", "usd"}, Group: "trade", Mutating: true,
		Summary: "Place an order", NewInput: func() any { return new(core.OrderInput) },
		Run: func(ctx context.Context, env *core.Env, in any) (*core.Result, error) {
			o := in.(*core.OrderInput)
			p := core.OrderPreview{Action: "order", Coin: o.Coin, Side: domain.Buy, NotionalUSD: o.USD, DryRun: true, Network: domain.Testnet}
			ok, err := env.Confirm(ctx, p)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, core.Errorf(core.CodeSafety, "declined")
			}
			return &core.Result{Card: core.CardOrder, Data: &p}, nil
		}})
	return r
}

// testParse maps positional args and --flag value pairs onto the input's
// JSON fields (a small stand-in for core.ParseArgs in these tests).
func testParse(t core.Tool, argv []string) (json.RawMessage, error) {
	m := map[string]any{}
	pos := 0
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if strings.HasPrefix(a, "--") && i+1 < len(argv) {
			m[strings.TrimPrefix(a, "--")] = argv[i+1]
			i++
			continue
		}
		if pos >= len(t.Positional) {
			return nil, core.Errorf(core.CodeUsage, "unexpected argument %q", a)
		}
		name := t.Positional[pos]
		if f, err := strconv.ParseFloat(a, 64); err == nil && name == "usd" {
			m[name] = f
		} else {
			m[name] = a
		}
		pos++
	}
	return json.Marshal(m)
}

// testInvoke decodes args into the tool input and runs it.
func testInvoke(reg *core.Registry) func(context.Context, *core.Env, core.Call) (*core.Result, error) {
	return func(ctx context.Context, env *core.Env, c core.Call) (*core.Result, error) {
		t, ok := reg.Get(c.Tool)
		if !ok {
			return nil, core.Errorf(core.CodeUsage, "no tool %s", c.Tool)
		}
		in := t.NewInput()
		if len(c.Args) > 0 {
			if err := json.Unmarshal(c.Args, in); err != nil {
				return nil, core.Errorf(core.CodeUsage, "%v", err)
			}
		}
		return t.Run(ctx, env, in)
	}
}

func newTestModel(t *testing.T, f *fakes, withStream bool) *Model {
	t.Helper()
	reg := fakeRegistry(t, f)
	th, _ := theme.Get("hlp")
	env := &core.Env{Config: config.Defaults(), Theme: th, Profile: colorprofile.TrueColor, Now: time.Now}
	env.Config.Theme = "hlp"
	env.Paths.DataDir = t.TempDir()
	if withStream {
		env.Stream = f.stream
	}
	m := New(Options{Env: env, Registry: reg})
	m.parse = testParse
	m.invoke = testInvoke(reg)
	// Motion off by default: a blink tick (605 ms) would outlast the
	// driver's command timeout. Motion tests turn it back on.
	m.reduced, m.reducedSet = true, true
	m.intent = func(text string, _ *core.Registry) (core.Call, bool) {
		if strings.HasPrefix(text, "chart ") {
			coin := strings.Fields(text)[1]
			return core.Call{Tool: "get_candles", Args: json.RawMessage(`{"coin":"` + coin + `"}`)}, true
		}
		return core.Call{}, false
	}
	t.Cleanup(m.Close)
	return m
}

// ---- driver -----------------------------------------------------------------

// driver feeds messages to the model and runs the returned commands
// synchronously (commands that block, like stream readers, time out and
// are abandoned; Close cancels them).
type driver struct {
	t    *testing.T
	m    *Model
	quit bool
}

// late collects messages from commands that outlived the driver's timeout
// (a blocked confirm reader, a slow tool); drain feeds them in later, the
// way the Bubble Tea runtime would.
var late = make(chan tea.Msg, 256)

func (d *driver) send(msgs ...tea.Msg) {
	d.drain()
	for _, msg := range msgs {
		d.process(msg, 0)
	}
	d.drain()
}

func (d *driver) drain() {
	for {
		select {
		case msg := <-late:
			d.process(msg, 0)
		default:
			return
		}
	}
}

func (d *driver) process(msg tea.Msg, depth int) {
	if msg == nil || depth > 40 {
		return
	}
	if _, ok := msg.(tea.QuitMsg); ok {
		d.quit = true
		return
	}
	switch msg.(type) {
	case spinMsg, blinkMsg, flapMsg:
		return // ticks would loop forever in a test
	}
	_, cmd := d.m.Update(msg)
	for _, next := range collect(cmd, 300*time.Millisecond) {
		d.process(next, depth+1)
	}
}

// collect runs cmd (expanding batches concurrently) and returns the
// messages produced within the timeout.
func collect(cmd tea.Cmd, timeout time.Duration) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		if b, ok := msg.(tea.BatchMsg); ok {
			var out []tea.Msg
			var mu sync.Mutex
			var wg sync.WaitGroup
			for _, c := range b {
				wg.Add(1)
				go func(c tea.Cmd) {
					defer wg.Done()
					ms := collect(c, timeout)
					mu.Lock()
					out = append(out, ms...)
					mu.Unlock()
				}(c)
			}
			wg.Wait()
			return out
		}
		if msg == nil {
			return nil
		}
		return []tea.Msg{msg}
	case <-time.After(timeout):
		go func() {
			if msg := <-ch; msg != nil {
				if _, isBatch := msg.(tea.BatchMsg); !isBatch {
					late <- msg
				}
			}
		}()
		return nil
	}
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+t":
		return tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}
	case "ctrl+b":
		return tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl}
	case "ctrl+k":
		return tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}
	case "ctrl+o":
		return tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func (d *driver) typeText(s string) {
	for _, r := range s {
		if r == ' ' {
			d.send(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
			continue
		}
		d.send(key(string(r)))
	}
}

// screen is the current frame with styling stripped.
func (d *driver) screen() string {
	return ansi.Strip(strings.Join(d.m.render(), "\n"))
}

func start(t *testing.T, m *Model, w, h int) *driver {
	d := &driver{t: t, m: m}
	d.send(tea.WindowSizeMsg{Width: w, Height: h})
	for _, msg := range collect(m.Init(), 200*time.Millisecond) {
		d.process(msg, 0)
	}
	return d
}

func lastEntry(m *Model) *entry { return m.entries[len(m.entries)-1] }

// ---- tests ------------------------------------------------------------------

func TestSlashChartAddsCard(t *testing.T) {
	f := &fakes{stream: &fakeStream{}}
	m := newTestModel(t, f, false)
	d := start(t, m, 120, 40)
	d.typeText("/chart BTC 1h")
	d.send(key("enter"))
	e := lastEntry(m)
	if e.kind != entryCard || e.result == nil || e.result.Card != core.CardChart {
		t.Fatalf("want chart card, got kind %v err %v", e.kind, e.err)
	}
	if got := e.result.Data.(*core.ChartData).Interval; got != domain.I1h {
		t.Errorf("interval %q", got)
	}
	s := d.screen()
	if !strings.Contains(s, "❯ /chart BTC 1h") || !strings.Contains(s, "BTC") {
		t.Errorf("screen lacks the echoed command or card:\n%s", s)
	}
	if m.in.Value() != "" {
		t.Errorf("input not cleared: %q", m.in.Value())
	}
}

func TestFrameFitsTerminal(t *testing.T) {
	f := &fakes{stream: &fakeStream{}}
	m := newTestModel(t, f, true)
	d := start(t, m, 90, 30)
	d.typeText("/chart BTC")
	d.send(key("enter"), key("ctrl+b"))
	for _, size := range [][2]int{{90, 30}, {140, 45}, {60, 20}} {
		d.send(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		lines := m.render()
		if len(lines) != size[1] {
			t.Fatalf("%dx%d: %d lines", size[0], size[1], len(lines))
		}
		for i, l := range lines {
			if w := lipgloss.Width(l); w > size[0] {
				t.Fatalf("%dx%d: line %d is %d wide", size[0], size[1], i, w)
			}
		}
	}
}

func TestUnknownSlashSuggests(t *testing.T) {
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	d := start(t, m, 100, 30)
	d.typeText("/opne") // fuzzy-matches /open, but Enter never runs a fuzzy guess
	d.send(key("enter"))
	e := lastEntry(m)
	if e.kind != entryInfo || !strings.Contains(ansi.Strip(e.text), "/opne?  /open") || strings.Contains(e.text, "No command") {
		t.Fatalf("want suggestions, got %v %q", e.kind, e.text)
	}
}

func TestUsageErrorShowsHint(t *testing.T) {
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	d := start(t, m, 100, 30)
	d.typeText("/vault a b c")
	d.send(key("enter"))
	e := lastEntry(m)
	if e.kind != entryError || !strings.Contains(e.err.(*core.Error).Hint, "/vault <vault>") {
		t.Fatalf("usage hint missing: %v", e.err)
	}
}

func TestEscCancelsRunningTool(t *testing.T) {
	f := &fakes{stream: &fakeStream{}}
	m := newTestModel(t, f, false)
	d := start(t, m, 100, 30)
	d.typeText("/slow")
	d.send(key("enter"))
	if lastEntry(m).kind != entryPending || len(m.jobs) != 1 {
		t.Fatalf("want a pending job, got kind %v jobs %d", lastEntry(m).kind, len(m.jobs))
	}
	if !strings.Contains(d.screen(), "esc stop") {
		t.Error("prompt row should offer esc while running")
	}
	d.send(key("esc"))
	if len(m.jobs) != 0 || lastEntry(m).kind != entryInfo || lastEntry(m).state != "STOPPED" || !strings.Contains(d.screen(), "□ STOPPED") {
		t.Fatalf("esc did not interrupt: jobs %d kind %v", len(m.jobs), lastEntry(m).kind)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !f.canceled.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !f.canceled.Load() {
		t.Error("tool context was not canceled")
	}
}

func TestCtrlCTwiceQuits(t *testing.T) {
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	d := start(t, m, 100, 30)
	d.typeText("draft")
	d.send(key("ctrl+c"))
	if d.quit || m.in.Value() != "" || !strings.Contains(d.screen(), "ctrl+c again to quit") {
		t.Fatalf("first ctrl+c should clear and arm: quit=%v input=%q", d.quit, m.in.Value())
	}
	d.send(key("ctrl+c"))
	if !d.quit {
		t.Fatal("second ctrl+c should quit")
	}
	// Any other key disarms.
	m2 := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	d2 := start(t, m2, 100, 30)
	d2.send(key("ctrl+c"), key("x"), key("ctrl+c"))
	if d2.quit {
		t.Fatal("ctrl+c, x, ctrl+c must not quit")
	}
}

func TestAutocompleteVaFirst(t *testing.T) {
	got := matchCommands(commandsFrom(core.Default()), "/va")
	if len(got) < 2 || got[0].slash != "vault" || got[1].slash != "vaults" {
		names := []string{}
		for _, c := range got {
			names = append(names, c.slash)
		}
		t.Fatalf("/va suggests %v", names)
	}
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	d := start(t, m, 100, 30)
	d.typeText("/va")
	s := d.screen()
	if !strings.Contains(s, "/vault <vault>") || !strings.Contains(s, "/vaults") {
		t.Fatalf("popup missing:\n%s", s)
	}
	d.send(key("down"), key("tab"))
	if m.in.Value() != "/vaults " {
		t.Fatalf("tab accepted %q", m.in.Value())
	}
	if !strings.Contains(d.screen(), "[query]") { // optional positionals are bracketed
		t.Error("ghost arg hint missing")
	}
}

func TestEnterAcceptsCompletionThenRuns(t *testing.T) {
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	d := start(t, m, 100, 30)
	d.typeText("/vaults")
	d.send(key("enter"))
	if e := lastEntry(m); e.kind != entryCard || e.result.Card != core.CardVaults {
		t.Fatalf("exact match should run: %v", e.kind)
	}
}

func TestFreeTextIntentAndNoModelHint(t *testing.T) {
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	d := start(t, m, 100, 30)
	d.typeText("chart eth")
	d.send(key("enter"))
	if e := lastEntry(m); e.kind != entryCard || e.result.Card != core.CardChart {
		t.Fatalf("intent did not run chart: %v", e.kind)
	}
	d.typeText("what should i buy")
	d.send(key("enter"))
	if e := lastEntry(m); e.kind != entryInfo || !strings.Contains(ansi.Strip(e.text), "! NO MODEL") || !m.seen.Seen(tipNoModel) {
		t.Fatalf("want no-model hint, got %v %q", e.kind, e.text)
	}
}

func TestHistoryUpDownAndPersistence(t *testing.T) {
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	d := start(t, m, 100, 30)
	d.typeText("/vaults")
	d.send(key("enter"))
	d.typeText("/chart BTC")
	d.send(key("enter"))
	d.typeText("dra")
	d.send(key("up"))
	if m.in.Value() != "/chart BTC" {
		t.Fatalf("up: %q", m.in.Value())
	}
	d.send(key("up"))
	if m.in.Value() != "/vaults" {
		t.Fatalf("up up: %q", m.in.Value())
	}
	d.send(key("down"), key("down"))
	if m.in.Value() != "dra" {
		t.Fatalf("down restores scratch: %q", m.in.Value())
	}
	h := newHistory(m.env.Paths.DataDir)
	if len(h.items) != 2 || h.items[1] != "/chart BTC" {
		t.Fatalf("persisted history: %q", h.items)
	}
}

func TestOpenChartPaneAndKeys(t *testing.T) {
	f := &fakes{stream: &fakeStream{}}
	m := newTestModel(t, f, false)
	d := start(t, m, 120, 40)
	d.typeText("/chart BTC")
	d.send(key("enter"), key("enter")) // empty enter opens the latest card
	p := m.topPane()
	if p == nil || p.kind != paneChart {
		t.Fatal("chart pane not open")
	}
	if p.vp.Count == 0 {
		t.Log("viewport empty (chart renderer may still be a stub)")
	}
	// the default skin draws open up bodies: auto zoom is stride 4 (3-column
	// boxes, as on the inline card) even where stride 4 shows < 60 bars
	if p.zoom != 0 || p.shownZoom != 4 {
		t.Errorf("default zoom = %d shown %d", p.zoom, p.shownZoom)
	}
	if defaultZoom(120) != 2 {
		t.Errorf("solid-body auto zoom at 120 columns = %d, want 2", defaultZoom(120))
	}
	d.send(key("+"))
	if p.zoom != 6 {
		t.Errorf("zoom = %d", p.zoom)
	}
	d.send(key("0"))
	if p.zoom != 0 {
		t.Errorf("reset zoom = %d", p.zoom)
	}
	if defaultZoom(80) != 2 || defaultZoom(300) != 4 {
		t.Error("defaultZoom")
	}
	d.send(key("c"))
	if p.cursor != 0 {
		t.Errorf("crosshair = %d", p.cursor)
	}
	d.send(key("L"), key("s"), key("i"))
	if !p.log || p.style != 1 || !p.noMAs {
		t.Errorf("toggles: log %v style %d noMAs %v", p.log, p.style, p.noMAs)
	}
	before := f.calls.Load()
	d.send(key("5")) // 1h
	if f.calls.Load() != before+1 {
		t.Fatal("interval key did not refetch")
	}
	if got := p.res.Data.(*core.ChartData).Interval; got != domain.I1h {
		t.Errorf("pane interval %q", got)
	}
	if !strings.Contains(d.screen(), "› BTC 1H") {
		t.Error("status bar should name the pane")
	}
	d.send(key("esc"))
	if m.topPane() != nil {
		t.Fatal("esc should close the pane")
	}
}

func TestVaultsPaneSortFilterOpen(t *testing.T) {
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	m.openOnReady = "vaults"
	d := start(t, m, 140, 40)
	d.typeText("/vaults")
	d.send(key("enter"))
	p := m.topPane()
	if p == nil || p.kind != paneVaults || len(p.view) != 4 {
		t.Fatalf("vaults pane: %+v", p)
	}
	if p.view[0].Name != "Hyperliquidity Provider (HLP)" {
		t.Errorf("default sort tvl desc: %s", p.view[0].Name)
	}
	d.typeText("/grid")
	d.send(key("enter"))
	if len(p.view) != 2 {
		t.Fatalf("filter grid -> %d rows", len(p.view))
	}
	for i := 0; i < 5; i++ { // tvl -> apr -> ret7d -> ret30d -> retall -> sharpe
		d.send(key("s"))
	}
	if cardsKey := p.sortIdx; cardsKey != 5 || p.view[0].Name != "Grid Bot Alpha" {
		t.Errorf("sort by sharpe: idx %d first %s", p.sortIdx, p.view[0].Name)
	}
	if !strings.Contains(strings.ToLower(d.screen()), "sharpe ▾") {
		t.Error("sort marker missing on screen")
	}
	d.send(key("enter"))
	top := m.topPane()
	if top == p || top.kind != paneCard || top.res.Card != core.CardVault {
		t.Fatalf("enter should push the tearsheet")
	}
	d.send(key("esc"))
	if m.topPane() != p {
		t.Fatal("esc returns to the screener")
	}
}

func TestConfirmCardForMutatingTool(t *testing.T) {
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	d := start(t, m, 100, 30)
	d.typeText("/buy HYPE 100")
	// The tool blocks inside Env.Confirm; the driver delivers the confirm
	// request to the model, which opens the card.
	d.send(key("enter"))
	deadline := time.Now().Add(2 * time.Second)
	for m.confirm == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		d.drain()
	}
	if m.confirm == nil {
		t.Fatal("confirm card did not open")
	}
	s := d.screen()
	if !strings.Contains(s, "TESTNET") || !strings.Contains(s, "DRY RUN") || !strings.Contains(s, "confirm") || !strings.Contains(s, "CANCEL") {
		t.Fatalf("confirm view:\n%s", s)
	}
	// ←→ moves the keycap: the selected answer is the one keycap.
	m.confirm.openedAt = time.Now().Add(-time.Second)
	d.send(key("left"))
	if !m.confirm.answer || !strings.Contains(d.screen(), " CONFIRM     cancel") {
		t.Fatalf("left should select CONFIRM:\n%s", d.screen())
	}
	reply := m.confirm.req.reply
	d.send(key("esc"))
	if m.confirm != nil {
		t.Fatal("esc should close the confirm")
	}
	select {
	case ok := <-reply:
		t.Fatalf("reply already consumed? got %v", ok)
	default:
	}
}

func TestConfirmerAnswers(t *testing.T) {
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	d := start(t, m, 100, 30)
	res := make(chan bool, 1)
	go func() {
		ok, _ := m.confirmer(context.Background(), core.OrderPreview{Action: "close_all", DryRun: true})
		res <- ok
	}()
	deadline := time.Now().Add(2 * time.Second)
	for m.confirm == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		d.drain()
	}
	if m.confirm == nil {
		t.Fatal("no confirm")
	}
	// Answering takes ←/→ then enter once the 400 ms input guard is over
	// (audit S5); a typed "y" never answers.
	yes := func() {
		m.confirm.openedAt = time.Now().Add(-time.Second)
		d.send(tea.KeyPressMsg{Code: tea.KeyLeft}, key("enter"))
	}
	yes()
	if m.confirm == nil || m.confirm.stage != 1 {
		t.Fatal("close_all must ask twice")
	}
	yes()
	select {
	case ok := <-res:
		if !ok {
			t.Fatal("double yes should confirm")
		}
	case <-time.After(time.Second):
		t.Fatal("confirmer did not return")
	}
}

func TestLiveStreamsOnlyWhileVisibleAndFocused(t *testing.T) {
	f := &fakes{stream: &fakeStream{}}
	m := newTestModel(t, f, false)
	d := start(t, m, 100, 30)
	d.typeText("/live")
	d.send(key("enter"))
	waitSubs(t, f, 1)
	if e := lastEntry(m); e.kind != entryCard {
		t.Fatalf("live card: %v", e.kind)
	}
	d.send(tea.BlurMsg{})
	waitSubs(t, f, 0)
	d.send(tea.FocusMsg{})
	waitSubs(t, f, 1)
	// Push the card out of view with enough output.
	for i := 0; i < 6; i++ {
		d.typeText("/vaults")
		d.send(key("enter"))
	}
	waitSubs(t, f, 0)
	d.send(key("ctrl+c"), key("ctrl+c"))
	m.Close()
	waitSubs(t, f, 0)
}

func TestNoGoroutineLeakAfterQuit(t *testing.T) {
	f := &fakes{stream: &fakeStream{}}
	m := newTestModel(t, f, false)
	d := start(t, m, 100, 30)
	d.typeText("/live")
	d.send(key("enter"))
	waitSubs(t, f, 1)
	d.send(key("ctrl+c"), key("ctrl+c"))
	if !d.quit {
		t.Fatal("did not quit")
	}
	m.Close()
	waitSubs(t, f, 0)
	if len(m.live) != 0 {
		t.Fatalf("live map not empty: %d", len(m.live))
	}
}

func waitSubs(t *testing.T, f *fakes, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for f.stream.subs.Load() != want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := f.stream.subs.Load(); got != want {
		t.Fatalf("subscriptions = %d, want %d", got, want)
	}
}

func TestThemeCycleAndHelpAndPalette(t *testing.T) {
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	d := start(t, m, 100, 30)
	first := m.th.Name
	d.send(key("ctrl+t"))
	if m.th.Name == first {
		t.Error("ctrl+t should change the theme")
	}
	d.send(key("?"))
	if !m.showHelp || !strings.Contains(d.screen(), "KEYS") || !strings.Contains(d.screen(), "COMMANDS") {
		t.Fatal("? should show help")
	}
	d.send(key("x"))
	if m.showHelp {
		t.Fatal("any key closes help")
	}
	d.send(key("ctrl+k"))
	d.typeText("vaults")
	d.send(key("enter"))
	if m.palette != nil || lastEntry(m).kind != entryCard {
		t.Fatal("palette enter should run /vaults")
	}
	d.typeText("/theme light")
	d.send(key("enter"))
	if m.th.Name != "light" {
		t.Errorf("theme = %s", m.th.Name)
	}
	d.typeText("/clear")
	d.send(key("enter"))
	if !m.welcomeOnly() {
		t.Error("/clear should empty the transcript back to the welcome")
	}
}

func TestMergeCandle(t *testing.T) {
	cs := loadCandles(t)[:3]
	r := &core.Result{Card: core.CardChart, Data: &core.ChartData{Coin: "BTC", Candles: cs}}
	upd := cs[2]
	upd.C = 1
	nr := mergeCandle(r, upd)
	d := nr.Data.(*core.ChartData)
	if len(d.Candles) != 3 || d.Candles[2].C != 1 || d.Summary.Last != 1 {
		t.Fatalf("replace open bar: %+v", d.Candles[2])
	}
	if r.Data.(*core.ChartData).Candles[2].C == 1 {
		t.Fatal("merge must not mutate the old result")
	}
	nxt := cs[2]
	nxt.T += 900_000
	if d := mergeCandle(nr, nxt).Data.(*core.ChartData); len(d.Candles) != 4 {
		t.Fatal("newer candle should append")
	}
	if mergeCandle(nr, cs[0]) != nil {
		t.Fatal("stale candle should be ignored")
	}
}

func TestEditor(t *testing.T) {
	var e editor
	e.Insert("hello world")
	e.HandleKey(tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
	if e.Value() != "hello " {
		t.Fatalf("ctrl+w: %q", e.Value())
	}
	e.HandleKey(tea.KeyPressMsg{Code: tea.KeyHome})
	e.Insert(">")
	if e.Value() != ">hello " {
		t.Fatalf("home insert: %q", e.Value())
	}
	e.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	e.HandleKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if e.Value() != ">hello" {
		t.Fatalf("backspace: %q", e.Value())
	}
	e.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	if !e.Multiline() {
		t.Fatal("shift+enter should insert a newline")
	}
	st := lipgloss.NewStyle()
	if rows := e.View(20, "❯", "", st, st, st, st, true); len(rows) != 2 {
		t.Fatalf("multiline view rows %d", len(rows))
	}
	var long editor
	long.Insert(strings.Repeat("x", 50))
	for _, r := range long.View(20, "❯", "", st, st, st, st, true) {
		if lipgloss.Width(r) > 20 {
			t.Fatalf("wrapped row too wide: %q", r)
		}
	}
}

func TestSplitArgsAndHints(t *testing.T) {
	got := splitArgs(`vault "Long HYPE & BTC" --window 1y`)
	if strings.Join(got, "|") != "vault|Long HYPE & BTC|--window|1y" {
		t.Fatalf("split: %q", got)
	}
	c := command{slash: "chart", args: "<coin> <interval>"}
	if argHint(c, 0, true) != "<coin> <interval>" || argHint(c, 1, true) != "<interval>" || argHint(c, 1, false) != "" || argHint(c, 2, true) != "" {
		t.Fatal("argHint")
	}
	if compactArgs([]byte(`{"coin":"BTC","interval":"4h"}`)) != "BTC 4h" {
		t.Fatalf("compactArgs: %q", compactArgs([]byte(`{"coin":"BTC","interval":"4h"}`)))
	}
	if _, ok := findCommand(commandsFrom(core.Default()), "c"); !ok {
		t.Fatal("alias lookup")
	}
	if lo, hi := visibleRange(5, 10, 0); lo != 0 || hi != 5 {
		t.Fatal("short transcript sits at the top")
	}
	if lo, hi := visibleRange(50, 10, 5); lo != 35 || hi != 45 {
		t.Fatalf("visibleRange: %d %d", lo, hi)
	}
}

func TestWSStateFeed(t *testing.T) {
	f := NewWSStateFeed()
	for i := 0; i < 20; i++ {
		f.OnState("reconnecting")
	}
	f.OnState("live")
	var last string
	for len(f.C()) > 0 {
		last = <-f.C()
	}
	if last != "live" {
		t.Fatalf("latest state lost: %q", last)
	}
	var nilFeed *WSStateFeed
	nilFeed.OnState("x") // must not panic
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, true)
	d := start(t, m, 100, 30)
	d.send(wsStateMsg("live"))
	if top := strings.SplitN(d.screen(), "\n", 2)[0]; !strings.HasSuffix(strings.TrimRight(top, " "), "■") {
		t.Errorf("top bar should end with the live mark: %q", top)
	}
	d.send(tea.BlurMsg{})
	if top := strings.SplitN(d.screen(), "\n", 2)[0]; !strings.HasSuffix(strings.TrimRight(top, " "), "□") {
		t.Errorf("blur should show the paused mark: %q", top)
	}
}

func TestSuggestCommands(t *testing.T) {
	cmds := commandsFrom(core.Default())
	for typo, want := range map[string]string{"opne": "open", "vualts": "vaults", "chrat": "chart", "fundign": "funding"} {
		got := suggestCommands(cmds, typo)
		if len(got) == 0 || got[0].slash != want {
			t.Errorf("%s -> %v, want %s first", typo, got, want)
		}
	}
	if osaDistance("ab", "ba") != 1 || osaDistance("", "abc") != 3 {
		t.Fatal("osaDistance")
	}
}

func TestAgentEventsBecomeTranscript(t *testing.T) {
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	d := start(t, m, 100, 30)
	m.agentRun = 1
	m.agentCh = make(chan tea.Msg) // never delivers; events are injected below
	ev := func(e agent.Event) tea.Msg { return agentEventMsg{run: 1, ev: e} }
	res := &core.Result{Card: core.CardKV, Title: "asset", Data: [][2]string{{"coin", "BTC"}}}
	for _, msg := range []tea.Msg{
		ev(agent.Event{Kind: agent.EvText, Text: "Looking "}),
		ev(agent.Event{Kind: agent.EvText, Text: "it up."}),
		ev(agent.Event{Kind: agent.EvToolStart, Tool: "get_candles", Args: json.RawMessage(`{"coin":"BTC"}`)}),
		ev(agent.Event{Kind: agent.EvToolResult, Tool: "get_candles", Result: res}),
		ev(agent.Event{Kind: agent.EvText, Text: "Done."}),
		agentEventMsg{run: 99, ev: agent.Event{Kind: agent.EvText, Text: "stale run"}},
		agentDoneMsg{run: 1, hist: []llm.Message{{Role: llm.User, Content: "q"}}},
	} {
		_ = m.update(msg) // skip re-arming waits on the fake channel
	}
	kinds := []entryKind{}
	for _, e := range m.entries { // the welcome left with the first entry
		kinds = append(kinds, e.kind)
	}
	want := []entryKind{entryAssistant, entryCard, entryAssistant}
	if len(kinds) != len(want) {
		t.Fatalf("entries %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("entries %v, want %v", kinds, want)
		}
	}
	if m.entries[0].text != "Looking it up." || len(m.agentHist) != 1 {
		t.Fatalf("text %q hist %d", m.entries[1].text, len(m.agentHist))
	}
	if strings.Contains(d.screen(), "stale run") {
		t.Fatal("events from an old run must be ignored")
	}
}

func TestHistoryEscaping(t *testing.T) {
	s := "a\\b\nc"
	if unescapeHistory(escapeHistory(s)) != s {
		t.Fatal("history escaping round trip")
	}
}

func TestLightThemeOnDarkTerminalPaintsBackground(t *testing.T) {
	f := &fakes{stream: &fakeStream{}}
	m := newTestModel(t, f, false)
	start(t, m, 80, 24)
	m.update(tea.BackgroundColorMsg{Color: color.Black})
	if v := m.View(); v.BackgroundColor != nil {
		t.Fatal("dark theme on a dark terminal must not repaint")
	}
	light, ok := theme.Get("light")
	if !ok {
		for _, n := range theme.Names() {
			if th, _ := theme.Get(n); !th.Dark {
				light, ok = th, true
				break
			}
		}
	}
	if !ok {
		t.Skip("no light theme")
	}
	m.setTheme(light)
	v := m.View()
	if v.BackgroundColor == nil || v.ForegroundColor == nil {
		t.Fatal("light theme on a dark terminal must paint its own background")
	}
}

func TestTickerShowsLivePrices(t *testing.T) {
	m := newTestModel(t, &fakes{stream: &fakeStream{}}, false)
	start(t, m, 100, 30)
	if s := m.ticker(90); s != "" {
		t.Fatalf("no prices yet, got %q", s)
	}
	m.mids["BTC"], m.prevDay["BTC"] = 101000, 100000
	m.mids["ETH"], m.prevDay["ETH"] = 3000, 3100
	s := ansi.Strip(m.ticker(90))
	if !strings.Contains(s, "BTC  101,000 +1.0%") || !strings.Contains(s, "ETH  3,000.0 −3.2%") {
		t.Fatalf("ticker %q", s)
	}
	if w := ansi.Strip(m.ticker(22)); strings.Contains(w, "ETH") || !strings.Contains(w, "BTC") {
		t.Fatalf("ticker must drop whole items to fit: %q", w)
	}
}

// TestPVaultSlash: /pvault (the strategy-vault alias) is a slash command
// in the shell: it completes and dispatches to the strategy-vault tool.
func TestPVaultSlash(t *testing.T) {
	reg := core.Default()
	c, ok := findCommand(commandsFrom(reg), "pvault")
	if !ok || c.tool != "get_strategy_vault" {
		t.Fatalf("/pvault: %+v %v", c, ok)
	}
	if tool, ok := reg.BySlash("pvault"); !ok || tool.Name != "get_strategy_vault" {
		t.Fatalf("BySlash(pvault) = %v %v", tool.Name, ok)
	}
}
