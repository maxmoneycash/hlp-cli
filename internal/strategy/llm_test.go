package strategy_test

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/llm"
	"github.com/maxmoneycash/hlp-cli/internal/strategy"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/backtest"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/internal/testkit"
)

// fakeLLM replays canned replies and records requests. No network.
type fakeLLM struct {
	mu      sync.Mutex
	replies []string
	err     error
	local   bool
	reqs    []llm.Request
}

func (f *fakeLLM) Name() string { return "fake:model" }
func (f *fakeLLM) Local() bool  { return f.local }
func (f *fakeLLM) Chat(_ context.Context, req llm.Request, _ func(llm.Chunk)) (llm.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return llm.Message{}, f.err
	}
	r := f.replies[(len(f.reqs)-1)%len(f.replies)]
	return llm.Message{Role: llm.Assistant, Content: r}, nil
}

func cfg() strategy.LLMConfig {
	return strategy.LLMConfig{Name: "bot", Universe: []string{"BTC", "ETH"}, Interval: domain.I15m,
		Prompt: "Long BTC when it trends, short ETH when it lags.", Lookback: 24}
}

func data(t *testing.T) (*testkit.Source, []domain.Candle) {
	t.Helper()
	cs, err := testkit.BTC15m()
	if err != nil {
		t.Fatal(err)
	}
	eth := make([]domain.Candle, len(cs))
	for i, c := range cs {
		eth[i] = domain.Candle{T: c.T, O: c.O / 25, H: c.H / 25, L: c.L / 25, C: c.C / 25, V: c.V}
	}
	return &testkit.Source{Candle: map[string][]domain.Candle{"BTC": cs, "ETH": eth}}, cs
}

func TestLLMStrategyBacktestWithFakeModel(t *testing.T) {
	src, cs := data(t)
	f := &fakeLLM{local: true, replies: []string{
		"```json\n{\"weights\": {\"btc\": 0.8, \"ETH\": -0.4}, \"note\": \"BTC leads, ETH lags\"}\n```",
		`{"weights": {"BTC": 0.8}, "note": "close the ETH short"}`,
		`I think {"weights": {"DOGE": 1}}`, // not in the universe: keep the book
		`not json at all`,
	}}
	st, err := strategy.NewLLM(f, cfg())
	if err != nil {
		t.Fatal(err)
	}
	if sp := st.Spec(); sp.Kind != "llm" || sp.Warmup != 24 || sp.Interval != domain.I15m {
		t.Fatalf("spec %+v", sp)
	}
	res, err := backtest.Run(context.Background(), st, src, backtest.Options{
		Now: func() time.Time { return time.UnixMilli(cs[len(cs)-1].T + 86_400_000) }})
	if err != nil {
		t.Fatal(err)
	}
	if want := len(cs) - 23; len(f.reqs) != want {
		t.Fatalf("%d model calls, want one per decision bar (%d)", len(f.reqs), want)
	}
	logs := strings.Join(res.Logs, "\n")
	for _, want := range []string{"BTC leads, ETH lags | BTC +0.80 ETH -0.40", "close the ETH short | BTC +0.80",
		"invalid reply (DOGE is not in the universe", "invalid reply (no JSON object"} {
		if !strings.Contains(logs, want) {
			t.Errorf("missing log %q", want)
		}
	}
	if len(res.Fills) == 0 {
		t.Fatal("no trades")
	}
	if n, ok := st.(strategy.Noter); !ok || n.LastNote() == "" {
		t.Fatal("LastNote")
	}
	r := f.reqs[0]
	if !r.JSON || r.Temperature == nil || *r.Temperature != 0.2 || r.MaxTokens != 400 {
		t.Fatalf("request %+v", r)
	}
	if !strings.Contains(r.System, "BTC, ETH") || !strings.Contains(r.System, "Long BTC when it trends") || !strings.Contains(r.System, "<= 2") {
		t.Fatalf("system prompt:\n%s", r.System)
	}
	snap := r.Messages[0].Content
	if !strings.Contains(snap, "equity $10000.00") || !strings.Contains(snap, "\nBTC | ") || !strings.Contains(snap, "recent closes") {
		t.Fatalf("snapshot:\n%s", snap)
	}
}

func TestLLMBudgetErrorsAndValidation(t *testing.T) {
	src, cs := data(t)
	now := func() time.Time { return time.UnixMilli(cs[len(cs)-1].T + 86_400_000) }
	// Remote providers get a default call cap; failures hold the book.
	f := &fakeLLM{err: errors.New("503")}
	c := cfg()
	c.MaxCalls = 5
	st, _ := strategy.NewLLM(f, c)
	res, err := backtest.Run(context.Background(), st, src, backtest.Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.reqs) != 5 || len(res.Fills) != 0 || !strings.Contains(strings.Join(res.Logs, "\n"), "call budget of 5 reached") {
		t.Fatalf("calls %d fills %d", len(f.reqs), len(res.Fills))
	}
	remote := &fakeLLM{replies: []string{"{}"}}
	st, _ = strategy.NewLLM(remote, cfg())
	_, _ = backtest.Run(context.Background(), st, src, backtest.Options{Now: now})
	if len(remote.reqs) != len(cs)-23 { // under the default 500 cap
		t.Fatalf("remote calls %d", len(remote.reqs))
	}
	if _, err := strategy.NewLLM(nil, cfg()); err == nil {
		t.Error("nil provider accepted")
	}
	for name, mut := range map[string]func(*strategy.LLMConfig){
		"universe": func(c *strategy.LLMConfig) { c.Universe = nil },
		"dup":      func(c *strategy.LLMConfig) { c.Universe = []string{"BTC", "BTC"} },
		"interval": func(c *strategy.LLMConfig) { c.Interval = "2m" },
		"gross":    func(c *strategy.LLMConfig) { c.MaxGross = -1 },
		"lookback": func(c *strategy.LLMConfig) { c.Lookback = 1 },
		"prompt":   func(c *strategy.LLMConfig) { c.Prompt = " " },
	} {
		c := cfg()
		mut(&c)
		if _, err := strategy.NewLLM(&fakeLLM{}, c); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestParseWeights(t *testing.T) {
	u := []string{"BTC", "ETH", "xyz:SP500"}
	w, note, err := strategy.ParseWeights(`{"weights":{"btc":3,"XYZ:SP500":"-1"},"note":" go "}`, u, 2)
	if err != nil || note != "go" {
		t.Fatal(err, note)
	}
	// BTC clamped to 2, then the book (|2|+|1| = 3) scaled to gross 2.
	if math.Abs(w["BTC"]-4.0/3) > 1e-12 || math.Abs(w["xyz:SP500"]+2.0/3) > 1e-12 {
		t.Fatalf("weights %v", w)
	}
	if w, _, err := strategy.ParseWeights(`{"BTC": 0.5, "note": "bare map"}`, u, 2); err != nil || w["BTC"] != 0.5 || len(w) != 1 {
		t.Fatalf("bare map %v %v", w, err)
	}
	if w, _, err := strategy.ParseWeights(`{"weights": null, "note": "flat"}`, u, 2); err != nil || len(w) != 0 {
		t.Fatalf("null weights %v %v", w, err)
	}
	if w, _, err := strategy.ParseWeights(`{"weights": {"BTC": 0.1}, "note": "brace } in note"}`, u, 2); err != nil || w["BTC"] != 0.1 {
		t.Fatalf("brace in string %v %v", w, err)
	}
	for _, bad := range []string{"", "{", `{"weights": [1]}`, `{"weights": {"SOL": 1}}`, `{"weights": {"BTC": "lots"}}`, `{"weights": {"BTC": true}}`} {
		if _, _, err := strategy.ParseWeights(bad, u, 2); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestModelRefAndConfigFiles(t *testing.T) {
	for in, want := range map[string]strategy.ModelRef{
		"ollama:qwen3:8b":              {Provider: "ollama", Model: "qwen3:8b"},
		"anthropic:claude-sonnet":      {Provider: "anthropic", Model: "claude-sonnet"},
		"llama3.1:8b":                  {Provider: "ollama", Model: "llama3.1:8b"},
		"lmstudio:qwen2.5-7b-instruct": {Provider: "lmstudio", Model: "qwen2.5-7b-instruct"},
		"":                             {},
	} {
		if got := strategy.ParseModelRef(in); got != want {
			t.Errorf("%q -> %+v, want %+v", in, got, want)
		}
	}
	m := strategy.ModelRef{Provider: "ollama", Model: "qwen3:8b", BaseURL: "http://localhost:11434/v1"}
	if m.String() != "ollama:qwen3:8b" || m.LLMConfig().BaseURL != "" || m.LLMConfig().Model != "qwen3:8b" || m.IsZero() {
		t.Fatal("ModelRef helpers")
	}
	files, _ := filepath.Glob(filepath.Join(testkit.RepoRoot(), "examples", "llm", "*.yaml"))
	if len(files) < 2 {
		t.Fatal("expected LLM bot examples")
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		c, err := strategy.ParseLLMConfig(b)
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(f), err)
		}
		if c.Model.IsZero() || c.Timeout != 90*time.Second || c.Prompt == "" {
			t.Fatalf("%s: %+v", filepath.Base(f), c)
		}
	}
	mapping := "name: x\nmodel: {provider: openai-compatible, model: m}\nuniverse: [BTC]\nprompt: p\ntimeout: 30s\n"
	c, err := strategy.ParseLLMConfig([]byte(mapping))
	if err != nil || c.Model.Model != "m" || c.Timeout != 30*time.Second || c.Interval != domain.I1h || c.MaxGross != 2 {
		t.Fatalf("mapping form %+v %v", c, err)
	}
	if _, err := strategy.ParseLLMConfig([]byte("universe: [BTC]\nprompt: p\nbogus: 1\n")); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestStdContextDefault(t *testing.T) {
	if strategy.StdContext(nil) == nil {
		t.Fatal("nil")
	}
	if strategy.DefaultCosts.TakerBps != 4.5 || !strategy.DefaultCosts.Funding {
		t.Fatal("default costs")
	}
}
