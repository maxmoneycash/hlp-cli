package core_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/core/coretest"
	"github.com/maxmoneycash/hlp-cli/internal/llm"
	"github.com/maxmoneycash/hlp-cli/internal/macro"
)

// The cost guard refuses multi-call LLM runs without
// --llm, before any model is called, and names the estimate.
func TestLLMGuardRefusesWithoutConfirmation(t *testing.T) {
	env := coretest.Env(t, nil)
	pine := filepath.Join(coretest.RepoRoot(), "examples", "pine", "ema-cross.pine")
	for _, c := range []struct{ tool, args string }{
		{"run_backtest", `{"file":"llm:ollama:qwen3:8b","days":3}`},
		{"arena", `{"entrants":["llm:ollama:qwen3:8b","` + pine + `@BTC"]}`},
		{"paper", `{"action":"start","file":"llm:ollama:qwen3:8b"}`},
	} {
		_, err := invoke(t, env, c.tool, c.args)
		if code(err) != core.CodeConfirmRequired {
			t.Fatalf("%s: want CONFIRM_REQUIRED, got %v", c.tool, err)
		}
		msg := err.Error()
		for _, want := range []string{"LLM calls", "ollama:qwen3:8b", "--max-llm-calls 200", "no cost"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s: message lacks %q: %s", c.tool, want, msg)
			}
		}
		if e := core.AsError(err); !strings.Contains(e.Hint, "--llm") {
			t.Errorf("%s: hint %q", c.tool, e.Hint)
		}
	}
	// Pine-only arenas and backtests need no confirmation.
	if _, err := invoke(t, env, "run_backtest", `{"file":"`+pine+`","coin":"BTC","interval":"15m"}`); code(err) == core.CodeConfirmRequired {
		t.Fatal("pine backtest guarded")
	}
}

// fakeChat is a local OpenAI-compatible server: scout calls get one
// answer, decide calls another. It counts calls.
func fakeChat(t *testing.T, calls *atomic.Int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		reply := `{"analysis":"BTC trend","trades":[{"action":"BUY","coin":"BTC","usd":4000,"leverage":1,"confidence":0.7,"why":"trend","evidence":"higher highs"}],"stance":"long BTC"}`
		if strings.Contains(string(b), "SHORTLIST step") {
			reply = `{"shortlist":["BTC"]}`
		}
		out, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": reply}}},
			"usage": map[string]int{"prompt_tokens": 800, "completion_tokens": 150}})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLLMBacktestBudgetModelAndVsBench(t *testing.T) {
	var calls atomic.Int32
	srv := fakeChat(t, &calls)
	env := coretest.Env(t, nil)
	env.Config.LLM.Provider, env.Config.LLM.BaseURL, env.Config.LLM.Model = "ollama", srv.URL, "fake-model"
	dir := t.TempDir()
	bot := filepath.Join(dir, "bot.yaml")
	if err := os.WriteFile(bot, []byte("name: Scout\nuniverse: [BTC]\ninterval: 15m\nlookback: 24\nscout: {max: 1}\nprompt: trade the trend\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := mustInvoke(t, env, "run_backtest", `{"file":"llm:`+bot+`","llm":true,"maxLlmCalls":5}`)
	d := res.Data.(*core.BacktestData)
	if calls.Load() != 5 || d.LLM == nil || d.LLM.Calls != 5 || d.LLM.Refused != 1 || d.LLM.Max != 5 || d.LLM.InputTokens != 5*800 {
		t.Fatalf("budget: upstream %d stats %+v", calls.Load(), d.LLM)
	}
	if d.Model != "ollama:fake-model" {
		t.Fatalf("model %q", d.Model)
	}
	if d.Bench == nil || d.Bench.Name != "BTC" || d.VsBench == nil || math.Abs(*d.VsBench-((1+d.Tearsheet.Metrics.TotalReturn)/(1+d.Bench.Return)-1)) > 1e-12 {
		t.Fatalf("bench %+v vs %v", d.Bench, d.VsBench)
	}
	if len(d.Fills) == 0 {
		t.Fatal("no fills from the decide step")
	}
	logs := strings.Join(d.Logs, "\n")
	if !strings.Contains(logs, "[ollama:fake-model] BUY BTC $4000") || !strings.Contains(logs, "budget is spent") {
		t.Fatalf("logs:\n%s", logs)
	}
	found := false
	for _, w := range res.Meta.Warnings {
		found = found || strings.HasPrefix(w, "llm: ~") && strings.Contains(w, "stops at --max-llm-calls 5")
	}
	if !found {
		t.Fatalf("estimate missing from warnings %v", res.Meta.Warnings)
	}
	b, _ := json.Marshal(d)
	for _, k := range []string{`"vsBench":`, `"bench":{"name":"BTC"`, `"model":"ollama:fake-model"`, `"llm":{"maxCalls":5`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("json lacks %s", k)
		}
	}

	// --bench names another benchmark.
	res = mustInvoke(t, env, "run_backtest", `{"file":"llm:`+bot+`","llm":true,"maxLlmCalls":2,"bench":["BTC"]}`)
	if d := res.Data.(*core.BacktestData); d.Bench == nil || d.Bench.Name != "BTC" {
		t.Fatalf("explicit bench %+v", d.Bench)
	}
}

func TestModelsAndMacroTools(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, `{"data":[
		 {"id":"openai/gpt-5","name":"GPT-5","created":2,"pricing":{"prompt":"0.00000125","completion":"0.00001"}},
		 {"id":"anthropic/claude-sonnet-5","name":"Claude Sonnet 5","created":3,"pricing":{"prompt":"0.000003","completion":"0.000015"}},
		 {"id":"meta-llama/llama-4-scout:free","name":"Llama 4 Scout","created":1,"pricing":{"prompt":"0","completion":"0"}}]}`)
	}))
	defer srv.Close()
	env := coretest.Env(t, nil)
	env.LLMCatalog = llm.NewCatalog(llm.CatalogOptions{BaseURL: srv.URL})

	d := mustInvoke(t, env, "list_llm_models", `{"sort":"price"}`).Data.(*core.ModelsData)
	if d.Total != 3 || d.Models[0].ID != "meta-llama/llama-4-scout:free" || d.Models[2].Entrant != "llm:openrouter/anthropic/claude-sonnet-5" || d.Models[2].InPerM != 3 {
		t.Fatalf("models %+v", d.Models)
	}
	if d := mustInvoke(t, env, "list_llm_models", `{"query":"claude"}`).Data.(*core.ModelsData); d.Total != 1 || len(d.Pairs()) != 1 || !strings.Contains(d.Pairs()[0][1], "$3.00 in") {
		t.Fatalf("query %+v", d)
	}
	if d := mustInvoke(t, env, "list_llm_models", `{"free":true}`).Data.(*core.ModelsData); d.Total != 1 || d.Pairs()[0][1] != "free" {
		t.Fatalf("free %+v", d)
	}
	if hits.Load() != 1 {
		t.Fatalf("catalog fetched %d times", hits.Load())
	}

	// Macro off (nil) and on (fake HIP-4 source only: no third-party calls).
	m := mustInvoke(t, env, "get_macro", `{}`)
	if md := m.Data.(*core.MacroData); md.Enabled || len(md.Pairs()) != 1 {
		t.Fatalf("macro off %+v", md)
	}
	env.Macro = macro.New(macro.Options{Sources: []string{macro.SourceHyperliquid}, Outcomes: fakeHIP4{}})
	m = mustInvoke(t, env, "get_macro", `{"coins":["btc"]}`)
	md := m.Data.(*core.MacroData)
	if !md.Enabled || len(md.Signals) != 2 || md.Signals[0].Coin != "BTC" || !strings.Contains(md.Prompt, "not trade signals") {
		t.Fatalf("macro %+v", md)
	}
	if !strings.Contains(strings.Join(m.Meta.Warnings, ";"), "not a trading signal") {
		t.Fatalf("warning %v", m.Meta.Warnings)
	}
}

type fakeHIP4 struct{}

func (fakeHIP4) Outcomes(context.Context) ([]macro.Outcome, error) {
	return []macro.Outcome{
		{ID: 1, Name: "template:binaryPrice", Description: "perp:xyz:SP500|threshold:7000|time:20261201-2000", Mids: map[int]float64{0: 0.3}},
		{ID: 2, Name: "template:binaryPrice", Description: "perp:BTC|threshold:100000|time:20261231-0000", Mids: map[int]float64{0: 0.45}},
	}, nil
}
