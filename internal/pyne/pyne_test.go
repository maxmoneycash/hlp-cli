package pyne

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/strategy"
)

// The unit tests run against a fake pyne: this test binary re-executes
// itself with HLP_FAKE_PYNE=1 (see TestMain) and replays outputs recorded
// from hoox-pyne 0.7.0 in testdata/ (HLP_PYNE=1 HLP_PYNE_RECORD=1 go test
// -run TestLivePyne re-records them). CI needs no Python.

const fakeEnv = "HLP_FAKE_PYNE"

func TestMain(m *testing.M) {
	if os.Getenv(fakeEnv) == "1" {
		os.Exit(fakePyne(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakePyne implements just enough of the pyne CLI. HLP_FAKE_MODE picks a
// behaviour; HLP_FAKE_RECORD (a path) receives the argv and the inputs it
// was given as JSON.
func fakePyne(args []string) int {
	mode := os.Getenv("HLP_FAKE_MODE")
	fixture := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(os.Getenv("HLP_FAKE_TESTDATA"), name))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		return b
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: pyne COMMAND")
		return 2
	}
	switch args[0] {
	case "--version":
		if v := os.Getenv("HLP_FAKE_VERSION"); v != "" {
			fmt.Println(v)
			return 0
		}
		fmt.Println("0.7.0")
		return 0
	case "diagnostics":
		src, _ := os.ReadFile(args[1])
		if strings.Contains(string(src), ")close") {
			os.Stdout.Write(fixture("diagnostics_error.json"))
			return 1
		}
		fmt.Println(`{"ok": true, "file": "x.pine", "diagnostics": []}`)
		return 0
	case "execute":
	default:
		fmt.Fprintln(os.Stderr, "unknown command", args[0])
		return 2
	}
	flags := map[string]string{}
	for i := 2; i < len(args); i++ {
		if k, v, ok := strings.Cut(args[i], "="); ok && strings.HasPrefix(k, "--") {
			flags[k] = v
		} else if strings.HasPrefix(args[i], "--") {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				flags[args[i]] = args[i+1]
				i++
			} else {
				flags[args[i]] = "true"
			}
		}
	}
	src, err := os.ReadFile(args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	var bars []wireBar
	if b, err := os.ReadFile(flags["--ohlcv"]); err != nil || json.Unmarshal(b, &bars) != nil || len(bars) == 0 {
		fmt.Println(`{"ok": false, "error": "bad ohlcv", "kind": "runtime"}`)
		return 1
	}
	if rec := os.Getenv("HLP_FAKE_RECORD"); rec != "" {
		inputs, _ := os.ReadFile(flags["--inputs"])
		b, _ := json.Marshal(map[string]any{"args": args, "inputs": json.RawMessage(inputs), "src": string(src), "bars": len(bars)})
		_ = os.WriteFile(rec, b, 0o600)
	}
	switch mode {
	case "sleep":
		time.Sleep(30 * time.Second)
	case "garbage":
		fmt.Fprintln(os.Stderr, "Traceback (most recent call last):\n  boom")
		return 1
	case "huge":
		os.Stdout.Write([]byte(strings.Repeat("x", 4<<20)))
		return 0
	case "tailfail":
		if strings.Contains(string(src), ".trade|") {
			fmt.Println(`{"ok": false, "error": "Unknown builtin strategy.closedtrades.entry_bar_index", "kind": "runtime"}`)
			return 1
		}
	case "parse":
		os.Stdout.Write(fixture("execute_parse_error.json"))
		return 1
	}
	os.Stdout.Write(replay(fixture("execute_ok.json"), string(src), mode))
	return 0
}

var fakeNonceRE = regexp.MustCompile(`"hlp\.([0-9a-f]+)\.bar\|"`)

// replay rewrites the recorded envelope for this run: the export lines get
// the nonce the instrumented script carries (the fixture was recorded with
// plain "hlp.bar|" prefixes), and some modes edit the logs.
func replay(raw []byte, src, mode string) []byte {
	var env map[string]any
	if json.Unmarshal(raw, &env) != nil {
		return raw
	}
	meta := env["extras"].(map[string]any)["meta"].(map[string]any)
	logs, _ := meta["logs"].([]any)
	nonce := ""
	if m := fakeNonceRE.FindStringSubmatch(src); m != nil {
		nonce = m[1]
	}
	lastBar := -1
	for i, l := range logs {
		lm := l.(map[string]any)
		msg, _ := lm["message"].(string)
		for _, k := range []string{"bar|", "trade|"} {
			if nonce != "" && strings.HasPrefix(msg, "hlp."+k) {
				msg = "hlp." + nonce + "." + strings.TrimPrefix(msg, "hlp.")
			}
		}
		if strings.Contains(msg, ".bar|") {
			lastBar = i
		}
		lm["message"] = msg
	}
	switch mode {
	case "forge": // the script's own log.info("hlp.bar|...") lines
		logs = append(logs,
			map[string]any{"level": "info", "message": "hlp.bar|301|1794735900000|1000000000|0|nan|999999|0|99|99"},
			map[string]any{"level": "info", "message": "hlp.trade|1|2|1|2|1|2|1|999999|0|fake|fake"})
	case "phantomflip": // a close-fill strategy closes on the phantom bar
		if lastBar >= 0 {
			lm := logs[lastBar].(map[string]any)
			f := strings.Split(lm["message"].(string), "|")
			f[4] = "0.0"
			lm["message"] = strings.Join(f, "|")
		}
	}
	meta["logs"] = logs
	out, _ := json.Marshal(env)
	return out
}

func fakeEngine(t *testing.T, mode string) (*Engine, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	td, _ := filepath.Abs("testdata")
	rec := filepath.Join(t.TempDir(), "rec.json")
	t.Setenv(fakeEnv, "1")
	t.Setenv("HLP_FAKE_MODE", mode)
	t.Setenv("HLP_FAKE_TESTDATA", td)
	t.Setenv("HLP_FAKE_RECORD", rec)
	t.Setenv(EnvBin, self)
	e, err := Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	e.Timeout = 10 * time.Second
	return e, rec
}

func readRec(t *testing.T, path string) (args []string, inputs map[string]any, src string, bars int) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Args   []string       `json:"args"`
		Inputs map[string]any `json:"inputs"`
		Src    string         `json:"src"`
		Bars   int            `json:"bars"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r.Args, r.Inputs, r.Src, r.Bars
}

func macdSrc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(), "testdata", "pine-samples", "02-macd-cross.pine"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDetect(t *testing.T) {
	e, _ := fakeEngine(t, "")
	if e.Version != "0.7.0" {
		t.Fatalf("version %q", e.Version)
	}
	t.Setenv("HLP_FAKE_VERSION", "0.5.9")
	if _, err := Detect(context.Background()); err == nil || !strings.Contains(err.Error(), "older") {
		t.Fatalf("old version: %v", err)
	}
	t.Setenv("HLP_FAKE_VERSION", "pyne dev build")
	if _, err := Detect(context.Background()); err == nil {
		t.Fatal("expected an error for an unparsable version")
	}
	t.Setenv(EnvBin, filepath.Join(t.TempDir(), "nope"))
	if _, err := Detect(context.Background()); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("missing binary: %v", err)
	}
	t.Setenv(EnvBin, "")
	t.Setenv("PATH", t.TempDir())
	if _, err := Detect(context.Background()); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("not on PATH: %v", err)
	}
	if Available(context.Background()) {
		t.Fatal("Available with no pyne")
	}
}

func TestExecuteDecodes(t *testing.T) {
	e, rec := fakeEngine(t, "")
	cs := btc15m(t)
	r, err := e.Execute(context.Background(), macdSrc(t), cs, Options{
		Ticker: "BTC", Interval: domain.I15m,
		Inputs: map[string]any{"fastLength": 10, "signal length": 7, "nope": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	args, inputs, src, bars := readRec(t, rec)
	if bars != len(cs) {
		t.Errorf("fake saw %d bars, want %d", bars, len(cs))
	}
	want := map[string]any{"Fast length": float64(10), "Signal length": float64(7), "nope": float64(1)}
	for k, v := range want {
		if inputs[k] != v {
			t.Errorf("inputs[%q] = %v, want %v (all %v)", k, inputs[k], v, inputs)
		}
	}
	joined := strings.Join(args, " ")
	for _, w := range []string{"--mode interpret", "--ticker=BTC", "--timeframe=15", "--sample 8"} {
		if !strings.Contains(joined, w) {
			t.Errorf("argv %q lacks %q", joined, w)
		}
	}
	if !fakeNonceRE.MatchString(src) || strings.Contains(src, `"hlp.trade|"`) || !strings.HasPrefix(src, "//@version=6") {
		t.Error("script was not instrumented as expected")
	}
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], `"nope"`) {
		t.Errorf("warnings %v", r.Warnings)
	}
	// Values recorded from hoox-pyne 0.7.0 on btc-15m.json.
	if r.Script.Name != "MACD signal flip" || r.Script.Type != "strategy" || r.Bars != 301 {
		t.Errorf("script %+v bars %d", r.Script, r.Bars)
	}
	if len(r.Equity) != 301 || len(r.Position) != 301 {
		t.Fatalf("equity %d position %d", len(r.Equity), len(r.Position))
	}
	if r.Stats.ClosedTrades != 16 || len(r.Trades) != 16 || r.Stats.WinTrades != 7 {
		t.Errorf("stats %+v trades %d", r.Stats, len(r.Trades))
	}
	if math.Abs(r.Stats.NetProfit-(-333.8361566571424)) > 1e-6 || math.Abs(r.Stats.InitialCapital-10000) > 1e-6 {
		t.Errorf("net %.6f capital %.6f", r.Stats.NetProfit, r.Stats.InitialCapital)
	}
	t0 := r.Trades[0]
	if !t0.Long || t0.EntryID != "Bull" || t0.EntryBar != 40 || t0.ExitBar != 73 || t0.EntryPx != 71907 || t0.ExitPx != 70934 {
		t.Errorf("trade 0 = %+v", t0)
	}
	if r.Trades[1].Long {
		t.Errorf("trade 1 should be short: %+v", r.Trades[1])
	}
	if r.Open == nil || r.Open.Size <= 0 {
		t.Errorf("open position %+v", r.Open)
	}
	if p, ok := r.Plots["Equity"]; !ok || len(p.Tail) != 8 || p.Len != 301 {
		t.Errorf("plots %+v", r.Plots)
	}
	for _, l := range r.Logs {
		if strings.Contains(l, "hlp.") {
			t.Fatalf("instrumentation leaked into Logs: %q", l)
		}
	}
	fs := Fills(r, "BTC")
	if len(fs) != 33 || fs[0].Side != domain.Buy || fs[1].Dir != "Close Long" {
		t.Errorf("fills %d first %+v %+v", len(fs), fs[0], fs[1])
	}
	// The open position (16 closed trades + 1 open) gets one opening fill
	// at its average price, stamped where the position began.
	if last := fs[len(fs)-1]; r.Open == nil || last.Px != r.Open.AvgPx || last.Sz != math.Abs(r.Open.Size) || !strings.HasPrefix(last.Dir, "Open") || last.Time != r.Trades[len(r.Trades)-1].ExitTime {
		t.Errorf("open fill %+v, open %+v", last, r.Open)
	}
}

func TestExecuteParseError(t *testing.T) {
	e, _ := fakeEngine(t, "parse")
	_, err := e.Execute(context.Background(), "//@version=6\nstrategy(\"x\")\nx = )close\n", btc15m(t)[:10], Options{})
	var se *ScriptError
	if !errors.As(err, &se) || se.Kind != "parse" {
		t.Fatalf("err = %v", err)
	}
	// Diagnostics come from `pyne diagnostics` on the untouched source.
	if len(se.Diagnostics) == 0 || se.Diagnostics[0].Line != 3 || se.Diagnostics[0].Col != 5 {
		t.Fatalf("diagnostics %+v", se.Diagnostics)
	}
	if !strings.Contains(err.Error(), "3:5:") {
		t.Errorf("error text %q", err)
	}
}

func TestExecuteTailRejected(t *testing.T) {
	e, _ := fakeEngine(t, "tailfail")
	r, err := e.Execute(context.Background(), macdSrc(t), btc15m(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Instrumented || len(r.Trades) != 0 || len(r.Warnings) == 0 {
		t.Fatalf("instrumented=%v trades=%d warnings=%v", r.Instrumented, len(r.Trades), r.Warnings)
	}
}

func TestExecuteFailures(t *testing.T) {
	cs := btc15m(t)
	t.Run("timeout", func(t *testing.T) {
		e, _ := fakeEngine(t, "sleep")
		e.Timeout = 500 * time.Millisecond
		start := time.Now()
		_, err := e.Execute(context.Background(), macdSrc(t), cs, Options{})
		var te *TimeoutError
		if !errors.As(err, &te) {
			t.Fatalf("err = %v", err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("took %s to kill pyne", d)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		e, _ := fakeEngine(t, "sleep")
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(200 * time.Millisecond); cancel() }()
		if _, err := e.Execute(ctx, macdSrc(t), cs, Options{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("garbage", func(t *testing.T) {
		e, _ := fakeEngine(t, "garbage")
		_, err := e.Execute(context.Background(), macdSrc(t), cs, Options{})
		var ee *ExitError
		if !errors.As(err, &ee) || !strings.Contains(ee.Stderr, "Traceback") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("huge", func(t *testing.T) {
		e, _ := fakeEngine(t, "huge")
		e.MaxOutput = 1 << 20
		if _, err := e.Execute(context.Background(), macdSrc(t), cs, Options{}); !errors.Is(err, ErrOutputTooLarge) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("limits", func(t *testing.T) {
		e, _ := fakeEngine(t, "")
		if _, err := e.Execute(context.Background(), macdSrc(t), nil, Options{}); err == nil {
			t.Error("no candles accepted")
		}
		if _, err := e.Execute(context.Background(), macdSrc(t), make([]domain.Candle, MaxBars+1), Options{}); err == nil {
			t.Error("too many candles accepted")
		}
		if _, err := e.Execute(context.Background(), strings.Repeat("x", MaxSourceBytes+1), cs, Options{}); err == nil {
			t.Error("oversized script accepted")
		}
		bad := append([]domain.Candle(nil), cs[:3]...)
		bad[1].C = math.NaN()
		if _, err := e.Execute(context.Background(), macdSrc(t), bad, Options{}); err == nil {
			t.Error("NaN candle accepted")
		}
		bad = append([]domain.Candle(nil), cs[:3]...)
		bad[2].T = bad[0].T
		if _, err := e.Execute(context.Background(), macdSrc(t), bad, Options{}); err == nil {
			t.Error("unordered candles accepted")
		}
	})
}

func TestTempFilesRemoved(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	e, _ := fakeEngine(t, "")
	if _, err := e.Execute(context.Background(), macdSrc(t), btc15m(t), Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Check(context.Background(), macdSrc(t)); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(filepath.Join(tmp, "hlp-pyne-*"))
	if len(left) != 0 {
		t.Fatalf("temp dirs left behind: %v", left)
	}
}

func TestCheck(t *testing.T) {
	e, _ := fakeEngine(t, "")
	ds, err := e.Check(context.Background(), "//@version=6\nindicator(\"x\")\nx = )close\nplot(x)\n")
	if err != nil {
		t.Fatal(err)
	}
	if errs := Errors(ds); len(errs) != 1 || errs[0].Line != 3 || errs[0].Col != 5 || errs[0].Severity != "error" {
		t.Fatalf("diagnostics %+v (PYNE's duplicate parse/E001 pair should collapse)", ds)
	}
	ds, err = e.Check(context.Background(), macdSrc(t))
	if err != nil || len(ds) != 0 {
		t.Fatalf("clean script: %v %v", ds, err)
	}
}

func TestResolveInputs(t *testing.T) {
	src := `//@version=6
strategy("x")
fast = input.int(12, "Fast length")
int slow = input.int(title = "Slow length", defval = 26)
kind = input.string("EMA", "MA type", options = ["EMA", "SMA"])
useHtf = input.bool(true, title = "Use HTF")
htf = input.timeframe("D", "Filter timeframe")
start = input.time(timestamp("2024-01-01T00:00:00"), "Start") // comment, "x"
src2 = input.source(close, "Source")
`
	got, unknown := resolveInputs(src, map[string]any{"FAST": 1, "slow": 2, "ma type": "SMA", "useHtf": false, "Other": 3, "htf": "240", "START": 5, "src2": "open"})
	want := map[string]any{"Fast length": 1, "Slow length": 2, "MA type": "SMA", "Use HTF": false, "Other": 3, "Filter timeframe": "240", "Start": 5, "Source": "open"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%q = %v, want %v (%v)", k, got[k], v, got)
		}
	}
	if len(unknown) != 1 || unknown[0] != "Other" {
		t.Errorf("unknown %v", unknown)
	}
}

func TestChildEnvDropsSecrets(t *testing.T) {
	got := childEnv([]string{"PATH=/bin", "HOME=/h", "HL_PRIVATE_KEY=0xabc", "ANTHROPIC_API_KEY=k", "GITHUB_TOKEN=t", "LANG=C"})
	if strings.Join(got, ",") != "PATH=/bin,HOME=/h,LANG=C" {
		t.Errorf("childEnv = %v", got)
	}
}

func TestTVTimeframe(t *testing.T) {
	for iv, want := range map[domain.Interval]string{domain.I1m: "1", domain.I15m: "15", domain.I1h: "60", domain.I4h: "240", domain.I1d: "D", domain.I1w: "W", domain.I1M: "M", "": "1"} {
		if got := TVTimeframe(iv); got != want {
			t.Errorf("%s -> %s, want %s", iv, got, want)
		}
	}
}

func TestInstrumentOnlyStrategies(t *testing.T) {
	if isStrategy("//@version=6\nindicator(\"x\")\nplot(close)\n") {
		t.Error("indicator detected as strategy")
	}
	if !isStrategy(macdSrc(t)) {
		t.Error("strategy not detected")
	}
	out := instrument("strategy(\"x\")\nif close > open\n    strategy.entry(\"L\", strategy.long)", "ab12")
	if !strings.Contains(out, `"hlp.ab12.bar|"`) || !strings.Contains(out, `"hlp.ab12.trade|"`) {
		t.Errorf("tail prefixes lack the nonce:\n%s", out)
	}
	if !strings.Contains(out, "strategy.long)\n\n// ---- appended by hlp") {
		t.Errorf("tail not on its own top-level line:\n%s", out)
	}
}

func TestBacktestShape(t *testing.T) {
	e, _ := fakeEngine(t, "")
	cs := btc15m(t)
	res, r, err := e.Backtest(context.Background(), macdSrc(t), "BTC", domain.I15m, cs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Equity) != len(cs) || res.Equity[0].T != cs[0].T+15*60_000 {
		t.Errorf("equity %d points, first T %d", len(res.Equity), res.Equity[0].T)
	}
	if res.Trades != 2*len(r.Trades)+1 || res.Spec.Name != "MACD signal flip" || res.Tearsheet.Meta.Source != "backtest (pyne)" {
		t.Errorf("trades %d spec %+v source %q", res.Trades, res.Spec, res.Tearsheet.Meta.Source)
	}
	eq := res.Plots["Equity"]
	if len(eq) != len(cs) || !math.IsNaN(eq[0]) || math.IsNaN(eq[len(eq)-1]) {
		t.Errorf("plot alignment wrong")
	}
}

// fakeCtx is a minimal strategy.Context that fills orders immediately.
type fakeCtx struct {
	bars   []domain.Candle
	pos    float64
	orders []strategy.Order
	plots  map[string]float64
}

func (c *fakeCtx) Now() int64                          { return c.bars[len(c.bars)-1].T }
func (c *fakeCtx) Bars(string) []domain.Candle         { return c.bars }
func (c *fakeCtx) Price(string) float64                { return c.bars[len(c.bars)-1].C }
func (c *fakeCtx) Position(string) float64             { return c.pos }
func (c *fakeCtx) Equity() float64                     { return 10000 }
func (c *fakeCtx) Cash() float64                       { return 10000 }
func (c *fakeCtx) SetTargets(map[string]float64) error { return nil }
func (c *fakeCtx) CloseAll() error                     { c.pos = 0; return nil }
func (c *fakeCtx) Mark(bool, string)                   {}
func (c *fakeCtx) Logf(string, ...any)                 {}
func (c *fakeCtx) Plot(name string, v float64) {
	if c.plots == nil {
		c.plots = map[string]float64{}
	}
	c.plots[name] = v
}
func (c *fakeCtx) Order(o strategy.Order) error {
	c.orders = append(c.orders, o)
	sz := o.Size
	if o.ReduceOnly && sz == 0 {
		sz = math.Abs(c.pos)
	}
	if o.Side == domain.Buy {
		c.pos += sz
	} else {
		c.pos -= sz
	}
	return nil
}

var _ strategy.Strategy = (*Follower)(nil)

func TestFollowerOrdersDelta(t *testing.T) {
	e, rec := fakeEngine(t, "")
	cs := btc15m(t)
	fl, err := e.Compile(context.Background(), macdSrc(t), "BTC", domain.I15m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fl.Spec().Name != "MACD signal flip" || fl.Spec().Kind != "pine" {
		t.Errorf("spec %+v", fl.Spec())
	}
	// The fixture's last bar holds a long position; starting flat the
	// follower must buy exactly that size.
	ctx := &fakeCtx{bars: cs[:300]}
	if err := fl.OnBar(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, _, bars := readRec(t, rec)
	if bars != 301 { // 300 real + 1 phantom
		t.Errorf("pyne saw %d bars, want 301", bars)
	}
	want := fl.LastResult().Position.Last()
	if len(ctx.orders) != 1 || ctx.orders[0].Kind != strategy.Market || math.Abs(ctx.pos-want) > 1e-12 {
		t.Fatalf("orders %+v pos %v want %v", ctx.orders, ctx.pos, want)
	}
	if _, ok := ctx.plots["Equity"]; !ok {
		t.Error("plot not forwarded")
	}
	// Same bar again: no new run, no new order.
	if err := fl.OnBar(ctx); err != nil || len(ctx.orders) != 1 {
		t.Fatalf("repeat bar: %v orders %d", err, len(ctx.orders))
	}
	// Next bar, already at target: nothing to do.
	ctx.bars = cs[:301]
	if err := fl.OnBar(ctx); err != nil || len(ctx.orders) != 1 {
		t.Fatalf("at target: %v orders %d", err, len(ctx.orders))
	}
	// Flip the held position: follower closes and re-opens to target.
	ctx.pos = -0.5
	ctx.bars = append(append([]domain.Candle(nil), cs...), domain.Candle{T: cs[300].T + 900_000, O: 1, H: 1, L: 1, C: 1})
	if err := fl.OnBar(ctx); err != nil || math.Abs(ctx.pos-want) > 1e-12 {
		t.Fatalf("flip: %v pos %v", err, ctx.pos)
	}
}

func TestCompileRejects(t *testing.T) {
	e, _ := fakeEngine(t, "")
	if _, err := e.Compile(context.Background(), "//@version=6\nindicator(\"x\")\nplot(close)\n", "BTC", domain.I1h, nil); err == nil {
		t.Error("indicator accepted as strategy")
	}
	_, err := e.Compile(context.Background(), "//@version=6\nstrategy(\"x\")\nx = )close\n", "BTC", domain.I1h, nil)
	var se *ScriptError
	if !errors.As(err, &se) || se.Diagnostics[0].Line != 3 {
		t.Fatalf("broken script: %v", err)
	}
}

func TestScriptCannotForgeExport(t *testing.T) {
	e, _ := fakeEngine(t, "forge")
	r, err := e.Execute(context.Background(), macdSrc(t), btc15m(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Equity) != 301 || len(r.Trades) != 16 {
		t.Fatalf("forged lines parsed: equity %d trades %d", len(r.Equity), len(r.Trades))
	}
	if r.Stats.FinalEquity > 1e6 || r.Stats.ClosedTrades != 16 {
		t.Fatalf("forged stats %+v", r.Stats)
	}
	forged := 0
	for _, l := range r.Logs {
		if strings.Contains(l, "hlp.bar|") || strings.Contains(l, "hlp.trade|") {
			forged++
		}
	}
	if forged != 2 {
		t.Errorf("script's own hlp.* lines should stay in Logs, got %d: %v", forged, r.Logs)
	}
}

func TestFillsOnCloseDetect(t *testing.T) {
	for src, want := range map[string]bool{
		"strategy(\"x\", process_orders_on_close = true)":  true,
		"strategy(\"x\", calc_on_order_fills=true)":        true,
		"strategy.close_all(immediately = true)":           true,
		"strategy(\"x\", process_orders_on_close=false)":   false,
		"// process_orders_on_close=true\nstrategy(\"x\")": false,
		"strategy(\"x\")\nstrategy.close(\"L\")":           false,
	} {
		if got := fillsOnClose(src); got != want {
			t.Errorf("fillsOnClose(%q) = %v, want %v", src, got, want)
		}
	}
}

// A close-fill script's target must not move because of the phantom bar:
// the fake closes the position on the phantom bar only.
func TestFollowerIgnoresPhantomCloseFills(t *testing.T) {
	e, _ := fakeEngine(t, "phantomflip")
	cs := btc15m(t)
	pooc := strings.Replace(macdSrc(t), "strategy(\"MACD signal flip\"", "strategy(\"MACD signal flip\", process_orders_on_close = true", 1)
	if pooc == macdSrc(t) {
		t.Fatal("could not set process_orders_on_close in the sample")
	}
	for _, tc := range []struct {
		src     string
		flatten bool
	}{{pooc, false}, {macdSrc(t), true}} {
		fl, err := e.Compile(context.Background(), tc.src, "BTC", domain.I15m, nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx := &fakeCtx{bars: cs[:300], pos: 0.5}
		if err := fl.OnBar(ctx); err != nil {
			t.Fatal(err)
		}
		pos := fl.LastResult().Position
		real := pos[len(pos)-2].V
		if real == 0 {
			t.Fatal("fixture should hold a position on the real last bar")
		}
		if tc.flatten && ctx.pos != 0 {
			t.Errorf("next-open script: target should follow the phantom row (0), pos %v", ctx.pos)
		}
		if !tc.flatten && math.Abs(ctx.pos-real) > 1e-12 {
			t.Errorf("close-fill script: target %v, want the real last bar's %v", ctx.pos, real)
		}
	}
}
