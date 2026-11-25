package strategy_test

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/llm"
	"github.com/maxmoneycash/hlp/internal/strategy"
	"github.com/maxmoneycash/hlp/internal/strategy/backtest"
)

func TestParseModelRefOpenRouter(t *testing.T) {
	for in, want := range map[string]strategy.ModelRef{
		"openrouter/anthropic/claude-sonnet-5":     {Provider: "openrouter", Model: "anthropic/claude-sonnet-5"},
		"OpenRouter/meta-llama/llama-4-scout:free": {Provider: "openrouter", Model: "meta-llama/llama-4-scout:free"},
		"openrouter:openai/gpt-5":                  {Provider: "openrouter", Model: "openai/gpt-5"},
		"ollama:qwen3:8b":                          {Provider: "ollama", Model: "qwen3:8b"},
		"qwen3:8b":                                 {Provider: "ollama", Model: "qwen3:8b"},
	} {
		if got := strategy.ParseModelRef(in); got != want {
			t.Errorf("%s: %+v", in, got)
		}
	}
}

func TestParseDecisions(t *testing.T) {
	menu := []string{"BTC", "ETH", "xyz:SP500"}
	resolve := func(s string) (string, error) {
		if strings.EqualFold(s, "SPY") {
			return "xyz:SP500", nil
		}
		return "", context.Canceled
	}
	reply := "```json\n" + `{"analysis":"BTC trend is up\u001b[31m; ETH lags.",
	 "trades":[
	  {"action":"buy","coin":"btc","usd":1500,"leverage":2,"confidence":1.7,"why":"trend","evidence":"+4% over 24 bars"},
	  {"action":"SHORT","coin":"SPY","usd":"$800","leverage":"1x","confidence":0.4,"why":"risk-off"},
	  {"action":"CLOSE","coin":"ETH","confidence":0.6},
	  {"action":"BUY","coin":"DOGE","usd":100},
	  {"action":"YOLO","coin":"BTC","usd":1},
	  {"action":"SELL","coin":"BTC","usd":5},
	  {"action":"BUY","coin":"SP500","usd":0},
	  {"action":"BUY","coin":"ETH","usd":"lots"}],
	 "stance":"long BTC, short SPX"}` + "\n```"
	out, err := strategy.ParseDecisions(reply, menu, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Decisions) != 3 {
		t.Fatalf("decisions %+v rejected %v", out.Decisions, out.Rejected)
	}
	d := out.Decisions
	if d[0].Action != "BUY" || d[0].Coin != "BTC" || d[0].USD != 1500 || d[0].Leverage != 2 || d[0].Confidence != 1 || d[0].Evidence == "" || d[0].Why != "trend" {
		t.Errorf("buy %+v", d[0])
	}
	if d[1].Action != "SELL" || d[1].Coin != "xyz:SP500" || d[1].USD != 800 || d[1].Leverage != 1 {
		t.Errorf("alias %+v", d[1])
	}
	if d[2].Action != "CLOSE" || d[2].Coin != "ETH" {
		t.Errorf("close %+v", d[2])
	}
	if len(out.Rejected) != 5 || strings.ContainsRune(out.Analysis, 0x1b) || out.Stance != "long BTC, short SPX" {
		t.Errorf("rejected %v analysis %q", out.Rejected, out.Analysis)
	}
	if _, err := strategy.ParseDecisions(`{"weights":{"BTC":1}}`, menu, nil); err == nil {
		t.Error("missing trades list accepted")
	}
	if out, err := strategy.ParseDecisions(`{"analysis":"nothing to do","trades":[]}`, menu, nil); err != nil || len(out.Decisions) != 0 {
		t.Errorf("empty trades: %v", err)
	}

	picks, rej, err := strategy.ParseScout(`{"shortlist":["eth","SP500","DOGE","ETH","BTC"]}`, menu, 2, nil)
	if err != nil || strings.Join(picks, ",") != "ETH,xyz:SP500" || len(rej) != 1 {
		t.Fatalf("scout %v %v %v", picks, rej, err)
	}
	if _, _, err := strategy.ParseScout(`{"pick":"BTC"}`, menu, 2, nil); err == nil {
		t.Error("reply without a shortlist accepted")
	}
}

// scriptedLLM answers scout and decide calls from separate scripts.
type scriptedLLM struct {
	mu     sync.Mutex
	scout  []string
	decide []string
	reqs   []llm.Request
	sN, dN int
}

func (f *scriptedLLM) Name() string { return "fake:model" }
func (f *scriptedLLM) Local() bool  { return false }
func (f *scriptedLLM) Chat(_ context.Context, req llm.Request, onChunk func(llm.Chunk)) (llm.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	var r string
	if strings.HasPrefix(req.System, "SHORTLIST step") {
		r = f.scout[f.sN%len(f.scout)]
		f.sN++
	} else {
		r = f.decide[f.dN%len(f.decide)]
		f.dN++
	}
	if onChunk != nil {
		onChunk(llm.Chunk{Done: true, Usage: &llm.Usage{InputTokens: 900, OutputTokens: 200}})
	}
	return llm.Message{Role: llm.Assistant, Content: r}, nil
}

func TestScoutDecideBacktestEnforcesLimits(t *testing.T) {
	src, cs := data(t)
	f := &scriptedLLM{
		scout: []string{`{"shortlist":["BTC","DOGE"]}`},
		decide: []string{
			// Way over every limit: clipped to max_leverage, then max_gross.
			`{"analysis":"BTC trends.","trades":[{"action":"BUY","coin":"BTC","usd":1000000,"leverage":10,"confidence":0.8,"why":"trend","evidence":"+3% in 24 bars"}],"stance":"max long BTC"}`,
			`{"analysis":"Nothing new.","trades":[],"stance":"hold"}`,
			`{"analysis":"Take profit.","trades":[{"action":"CLOSE","coin":"BTC","confidence":0.7,"why":"target hit"}],"stance":"flat"}`,
		},
	}
	c := cfg()
	c.Scout = &strategy.ScoutConfig{Max: 1}
	c.MaxGross, c.MaxLeverage, c.MaxCalls = 1.5, 3, 6
	st, err := strategy.NewLLM(f, c)
	if err != nil {
		t.Fatal(err)
	}
	res, err := backtest.Run(context.Background(), st, src, backtest.Options{
		Now: func() time.Time { return time.UnixMilli(cs[len(cs)-1].T + 86_400_000) }})
	if err != nil {
		t.Fatal(err)
	}
	if f.sN != 3 || f.dN != 3 {
		t.Fatalf("scout %d decide %d calls (cap 6)", f.sN, f.dN)
	}
	// The decide prompt states the data, cost and limit rules and the reply
	// shape; no macro block in a backtest.
	var sys, user string
	for _, r := range f.reqs {
		if !strings.HasPrefix(r.System, "SHORTLIST step") {
			sys, user = r.System, r.Messages[0].Content
			break
		}
	}
	for _, want := range []string{"must appear in the input", "You get no news", "empty trades list is the normal", "applied by the engine", `"evidence"`, `"confidence"`, "1.5x equity"} {
		if !strings.Contains(sys, want) {
			t.Errorf("decide prompt lacks %q", want)
		}
	}
	if !strings.Contains(user, "MARKET_DATA") || strings.Contains(user, "ETH |") || strings.Contains(user, "PREDICTION_MARKETS") {
		t.Errorf("decide payload must list only the scouted coin:\n%s", user)
	}
	if len(res.Fills) != 2 {
		t.Fatalf("fills %+v", res.Fills)
	}
	open := res.Fills[0].Px * res.Fills[0].Sz
	if open < 1.45*10000 || open > 1.5*10000 {
		t.Fatalf("opening notional %.0f: want ~1.5x equity (gross cap)", open)
	}
	if res.Fills[1].Side != domain.Sell || math.Abs(res.Fills[1].Sz-res.Fills[0].Sz) > 1e-9 {
		t.Fatalf("close fill %+v", res.Fills[1])
	}
	logs := strings.Join(res.Logs, "\n")
	for _, want := range []string{"[fake:model] scout: BTC", "[fake:model] BUY BTC $1000000 3x conf 0.80: trend | key: +3% in 24 bars (clipped to 3x equity; clipped by max gross 1.5x)",
		"[fake:model] no trade: hold", "[fake:model] CLOSE BTC", "call budget of 6 reached"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %q:\n%s", want, logs)
		}
	}
	d := st.(strategy.Decider).LastDecision()
	if d == nil || d.Model != "fake:model" || len(d.Actions) != 1 || d.Actions[0].Model != "fake:model" || d.Actions[0].OrderUSD >= 0 || d.Calls != 6 {
		t.Fatalf("last decision %+v", d)
	}
	if strategy.Noter(st.(strategy.Noter)).LastNote() != "flat" || st.(strategy.ModelIDer).ModelID() != "fake:model" {
		t.Fatal("note / model id")
	}
	est := st.(strategy.CallEstimator).EstimateCalls(100)
	if est.Calls != 6 || est.PerBar != 2 || est.InputTokens <= 0 || est.Model != "fake:model" {
		t.Fatalf("estimate %+v", est)
	}
}

// liveCtx is a minimal paper-like Context at wall-clock time.
type liveCtx struct {
	now    int64
	bars   map[string][]domain.Candle
	pos    map[string]float64
	orders []strategy.Order
	logs   []string
}

func (c *liveCtx) Now() int64                          { return c.now }
func (c *liveCtx) Bars(coin string) []domain.Candle    { return c.bars[coin] }
func (c *liveCtx) Price(coin string) float64           { b := c.bars[coin]; return b[len(b)-1].C }
func (c *liveCtx) Position(coin string) float64        { return c.pos[coin] }
func (c *liveCtx) Equity() float64                     { return 10000 }
func (c *liveCtx) Cash() float64                       { return 10000 }
func (c *liveCtx) Order(o strategy.Order) error        { c.orders = append(c.orders, o); return nil }
func (c *liveCtx) SetTargets(map[string]float64) error { return nil }
func (c *liveCtx) CloseAll() error                     { return nil }
func (c *liveCtx) Plot(string, float64)                {}
func (c *liveCtx) Mark(bool, string)                   {}
func (c *liveCtx) Logf(f string, a ...any)             { c.logs = append(c.logs, f) }

type fakeMacro struct{ calls int }

func (m *fakeMacro) MacroContext(_ context.Context, coins []string) string {
	m.calls++
	return "PREDICTION_MARKETS (background odds, not trade signals):\n- [kalshi] Inflation: \"CPI above 3%?\" YES 41%\n"
}

func TestMacroOnlyOnLiveBarsAndOrdersTagged(t *testing.T) {
	now := time.Now().Truncate(time.Hour)
	mk := func(px float64) []domain.Candle {
		var out []domain.Candle
		for i := 30; i > 0; i-- {
			out = append(out, domain.Candle{T: now.Add(-time.Duration(i) * time.Hour).UnixMilli(), O: px, H: px, L: px, C: px * (1 + float64(30-i)/1000)})
		}
		return out
	}
	ctx := &liveCtx{now: now.UnixMilli(), bars: map[string][]domain.Candle{"BTC": mk(70000), "ETH": mk(2500)}, pos: map[string]float64{"ETH": 1}}
	f := &scriptedLLM{scout: []string{`{"shortlist":["BTC"]}`},
		decide: []string{`{"analysis":"r","trades":[{"action":"SELL","coin":"BTC","usd":2000,"leverage":1,"confidence":0.5,"why":"fade","evidence":"funding"},{"action":"CLOSE","coin":"ETH","confidence":0.9,"why":"exit"}],"stance":"short BTC"}`}}
	m := &fakeMacro{}
	c := cfg()
	c.Interval = domain.I1h
	c.Scout = &strategy.ScoutConfig{Max: 2}
	c.MacroSource = m
	st, err := strategy.NewLLM(f, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.OnBar(ctx); err != nil {
		t.Fatal(err)
	}
	if m.calls != 2 { // scout + decide
		t.Fatalf("macro calls %d", m.calls)
	}
	for _, r := range f.reqs {
		if !strings.Contains(r.Messages[0].Content, "PREDICTION_MARKETS") || !strings.Contains(r.System, "never as a reason on their own") {
			t.Fatalf("macro block or rule missing:\nSYSTEM %s\nUSER %s", r.System, r.Messages[0].Content)
		}
	}
	// Decide sees the scouted coin plus the open ETH position.
	if u := f.reqs[1].Messages[0].Content; !strings.Contains(u, "\nBTC |") || !strings.Contains(u, "\nETH |") || !strings.Contains(u, "POSITIONS (coin") {
		t.Fatalf("menu:\n%s", u)
	}
	// Orders: cancel stale, close ETH first (reduce-only), then the BTC short; all tagged.
	if len(ctx.orders) != 3 || ctx.orders[0].Kind != strategy.Cancel || !ctx.orders[1].ReduceOnly || ctx.orders[1].Coin != "ETH" ||
		ctx.orders[2].Coin != "BTC" || ctx.orders[2].Side != domain.Sell || ctx.orders[2].NotionalUSD != 2000 {
		t.Fatalf("orders %+v", ctx.orders)
	}
	for _, o := range ctx.orders {
		if o.Tag != "llm:fake:model" {
			t.Fatalf("untagged order %+v", o)
		}
	}
	// macro: false opts the bot out.
	off := false
	c.Macro = &off
	m2 := &fakeMacro{}
	c.MacroSource = m2
	st2, _ := strategy.NewLLM(&scriptedLLM{scout: f.scout, decide: f.decide}, c)
	_ = st2.Init(ctx)
	_ = st2.OnBar(ctx)
	if m2.calls != 0 {
		t.Fatal("macro: false ignored")
	}
}

func TestRunBudgetStopsCalls(t *testing.T) {
	src, cs := data(t)
	f := &scriptedLLM{scout: []string{`{"shortlist":["BTC"]}`}, decide: []string{`{"analysis":"r","trades":[]}`}}
	b := llm.NewBudget(3, nil)
	c := cfg()
	c.Scout = &strategy.ScoutConfig{Max: 1}
	st, err := strategy.NewLLM(b.Wrap(f), c)
	if err != nil {
		t.Fatal(err)
	}
	res, err := backtest.Run(context.Background(), st, src, backtest.Options{
		Now: func() time.Time { return time.UnixMilli(cs[len(cs)-1].T + 86_400_000) }})
	if err != nil {
		t.Fatal(err)
	}
	if st := b.Stats(); st.Calls != 3 || st.Refused != 1 || f.sN+f.dN != 3 {
		t.Fatalf("budget %+v upstream %d", st, f.sN+f.dN)
	}
	if n := strings.Count(strings.Join(res.Logs, "\n"), "LLM call budget is spent"); n != 1 {
		t.Fatalf("budget log %d times", n)
	}
	if fl, ok := st.(strategy.Failer); ok && fl.LastFailure() != "" {
		t.Fatalf("budget stop is not a failure: %q", fl.LastFailure())
	}
}
