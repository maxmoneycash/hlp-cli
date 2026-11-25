package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/config"
	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/llm"
)

// fakeProvider replays scripted replies and records every request.
type fakeProvider struct {
	mu      sync.Mutex
	replies []llm.Message
	errs    []error
	reqs    []llm.Request
	block   bool // wait for ctx cancel instead of replying
}

func (f *fakeProvider) Name() string { return "fake:model" }
func (f *fakeProvider) Local() bool  { return true }
func (f *fakeProvider) Chat(ctx context.Context, req llm.Request, on func(llm.Chunk)) (llm.Message, error) {
	f.mu.Lock()
	cp := req
	cp.Messages = append([]llm.Message(nil), req.Messages...)
	f.reqs = append(f.reqs, cp)
	n := len(f.reqs) - 1
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
		return llm.Message{}, ctx.Err()
	}
	if n < len(f.errs) && f.errs[n] != nil {
		return llm.Message{}, f.errs[n]
	}
	if n >= len(f.replies) {
		return llm.Message{Role: llm.Assistant, Content: "fallback"}, nil
	}
	m := f.replies[n]
	if on != nil && m.Content != "" {
		for _, w := range strings.SplitAfter(m.Content, " ") {
			on(llm.Chunk{Text: w})
		}
		on(llm.Chunk{Done: true})
	}
	return m, nil
}

type testInput struct {
	Coin     string `json:"coin" jsonschema:"market"`
	Interval string `json:"interval,omitempty"`
}

type testOrder struct {
	Coin   string  `json:"coin"`
	Side   string  `json:"side"`
	USD    float64 `json:"usd,omitempty"`
	DryRun bool    `json:"dry-run,omitempty"`
	Yes    bool    `json:"yes,omitempty"`
}

// testRegistry has a read-only chart tool, a list tool, a failing tool and
// a mutating order tool that records what it received.
func testRegistry(t *testing.T, gotOrder *testOrder) *core.Registry {
	t.Helper()
	r := core.NewRegistry()
	r.Register(core.Tool{Name: "get_candles", Command: "chart", Slash: "chart", Summary: "Candlestick chart", Group: "market",
		NewInput: func() any { return new(testInput) },
		Run: func(ctx context.Context, env *core.Env, in any) (*core.Result, error) {
			i := in.(*testInput)
			candles := make([]domain.Candle, 500)
			for k := range candles {
				candles[k] = domain.Candle{T: int64(k) * 900_000, O: 1, H: 2, L: 0.5, C: 1.5, V: 10}
			}
			return &core.Result{Card: core.CardChart, Title: i.Coin, Data: &core.ChartData{Coin: i.Coin, Interval: domain.I15m, Candles: candles,
				Summary: core.ChartSummary{Last: 101, Change: 0.02}, Plain: "▁▂▃ chart"}, Meta: domain.Meta{Source: "hyperliquid"}}, nil
		}})
	r.Register(core.Tool{Name: "list_vaults", Summary: "Vaults", NewInput: func() any { return new(struct{}) },
		Run: func(ctx context.Context, env *core.Env, in any) (*core.Result, error) {
			var rows []domain.VaultSummary
			for k := 0; k < 400; k++ {
				rows = append(rows, domain.VaultSummary{Name: fmt.Sprintf("vault-%03d", k)})
			}
			return &core.Result{Card: core.CardVaults, Data: rows}, nil
		}})
	r.Register(core.Tool{Name: "get_funding", Summary: "Funding", NewInput: func() any { return new(struct{}) },
		Run: func(ctx context.Context, env *core.Env, in any) (*core.Result, error) {
			return nil, &core.Error{Code: core.CodeAPI, Message: "upstream 502", Hint: "retry"}
		}})
	r.Register(core.Tool{Name: "slow", Summary: "slow", NewInput: func() any { return new(struct{}) },
		Run: func(ctx context.Context, env *core.Env, in any) (*core.Result, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}})
	r.Register(core.Tool{Name: "place_order", Summary: "Order", Mutating: true, NewInput: func() any { return new(testOrder) },
		Run: func(ctx context.Context, env *core.Env, in any) (*core.Result, error) {
			o := in.(*testOrder)
			if gotOrder != nil {
				*gotOrder = *o
			}
			if env.Confirm != nil {
				ok, err := env.Confirm(ctx, core.OrderPreview{Action: "order", Coin: o.Coin, DryRun: true})
				if err != nil || !ok {
					return nil, &core.Error{Code: core.CodeConfirmRequired, Message: "declined"}
				}
			} else if !o.Yes {
				return nil, &core.Error{Code: core.CodeConfirmRequired, Message: "confirmation required"}
			}
			return &core.Result{Card: core.CardOrder, Data: &core.OrderPreview{Action: "order", Coin: o.Coin, DryRun: true}}, nil
		}})
	return r
}

func call(id, name, args string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: name, Args: json.RawMessage(args)}
}

func newEnv() *core.Env {
	return &core.Env{Guard: core.DenyAll{}, Now: func() time.Time { return time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC) }}
}

func TestRunToolRoundTrip(t *testing.T) {
	fp := &fakeProvider{replies: []llm.Message{
		{Role: llm.Assistant, Content: "Let me look. ", ToolCalls: []llm.ToolCall{call("t1", "get_candles", `{"coin":"BTC"}`), call("t2", "get_funding", `{}`)}},
		{Role: llm.Assistant, Content: "BTC is up 2% on the day."},
	}}
	a := &Agent{Provider: fp, Registry: testRegistry(t, nil), Env: newEnv()}
	var evs []Event
	hist, err := a.Run(context.Background(), nil, "how is btc", func(e Event) { evs = append(evs, e) })
	if err != nil {
		t.Fatal(err)
	}
	// user, assistant(tool calls), tool, tool, assistant
	if len(hist) != 5 || hist[0].Role != llm.User || hist[2].Role != llm.ToolRole || hist[4].Content != "BTC is up 2% on the day." {
		t.Fatalf("history %+v", hist)
	}
	if hist[2].ToolCallID != "t1" || hist[3].ToolCallID != "t2" {
		t.Fatal("tool results must answer calls in order")
	}
	var fed map[string]any
	if err := json.Unmarshal([]byte(hist[2].Content), &fed); err != nil {
		t.Fatalf("tool result is not JSON: %s", hist[2].Content)
	}
	data := fed["data"].(map[string]any)
	if fed["ok"] != true || data["plain"] != "▁▂▃ chart" || data["candles"] != nil || data["bars"].(float64) != 500 {
		t.Fatalf("chart digest %s", hist[2].Content)
	}
	if !strings.Contains(hist[3].Content, `"ok":false`) || !strings.Contains(hist[3].Content, "API_ERROR") {
		t.Fatalf("tool error fed back: %s", hist[3].Content)
	}
	// The second request carries the tool results and the tool specs.
	if len(fp.reqs) != 2 || len(fp.reqs[1].Messages) != 4 || len(fp.reqs[0].Tools) == 0 || fp.reqs[0].System == "" {
		t.Fatalf("requests %+v", fp.reqs)
	}
	kinds := ""
	var text strings.Builder
	for _, e := range evs {
		kinds += string(e.Kind) + ","
		if e.Kind == EvText {
			text.WriteString(e.Text)
		}
	}
	want := "tool_start,tool_result,tool_start,tool_result,"
	if !strings.Contains(kinds, want) || !strings.HasSuffix(kinds, "done,") {
		t.Fatalf("events %s", kinds)
	}
	if text.String() != "Let me look. BTC is up 2% on the day." {
		t.Fatalf("streamed text %q", text.String())
	}
	for _, e := range evs {
		if e.Kind == EvToolResult && e.Tool == "get_candles" && (e.Result == nil || e.Result.Card != core.CardChart) {
			t.Fatal("tool_result event must carry the Result for the card")
		}
		if e.Kind == EvToolResult && e.Tool == "get_funding" && e.Err == nil {
			t.Fatal("failed tool must carry Err")
		}
	}
}

func TestRunMaxSteps(t *testing.T) {
	loop := llm.Message{Role: llm.Assistant, ToolCalls: []llm.ToolCall{call("x", "get_funding", `{}`)}}
	fp := &fakeProvider{replies: []llm.Message{loop, loop, loop, loop}}
	a := &Agent{Provider: fp, Registry: testRegistry(t, nil), Env: newEnv(), MaxSteps: 2}
	hist, err := a.Run(context.Background(), nil, "loop", nil)
	if !errors.Is(err, ErrMaxSteps) {
		t.Fatalf("err %v", err)
	}
	if len(fp.reqs) != 3 {
		t.Fatalf("2 tool rounds = 3 model calls, got %d", len(fp.reqs))
	}
	last := hist[len(hist)-1]
	if last.Role != llm.ToolRole || !strings.Contains(last.Content, "step limit") {
		t.Fatalf("dangling tool call must be answered: %+v", last)
	}
	assertConsistent(t, hist)
}

func TestMutatingHiddenUnlessAllowed(t *testing.T) {
	var got testOrder
	reg := testRegistry(t, &got)
	specs, _ := ToolSpecs(reg, false)
	for _, s := range specs {
		if s.Name == "place_order" {
			t.Fatal("mutating tool advertised without AllowMutating")
		}
	}
	// A hallucinated mutating call is refused without running.
	fp := &fakeProvider{replies: []llm.Message{
		{Role: llm.Assistant, ToolCalls: []llm.ToolCall{call("o", "place_order", `{"coin":"HYPE","side":"buy","usd":100,"yes":true}`)}},
		{Role: llm.Assistant, Content: "I can't place orders."},
	}}
	a := &Agent{Provider: fp, Registry: reg, Env: newEnv()}
	var errs []Event
	hist, err := a.Run(context.Background(), nil, "buy", func(e Event) {
		if e.Kind == EvError {
			errs = append(errs, e)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Coin != "" {
		t.Fatal("refused tool must not run")
	}
	if len(errs) != 1 || errs[0].Tool != "place_order" || !strings.Contains(hist[2].Content, core.CodeSafety) {
		t.Fatalf("refusal %+v %s", errs, hist[2].Content)
	}
}

func TestYesStrippedAndConfirmApplies(t *testing.T) {
	var got testOrder
	reg := testRegistry(t, &got)
	specs, err := ToolSpecs(reg, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range specs {
		if s.Name == "place_order" {
			found = true
			if strings.Contains(string(s.Schema), `"yes"`) {
				t.Fatalf("yes must not be offered to the model: %s", s.Schema)
			}
			if !strings.Contains(s.Description, "DRY-RUN") && !strings.Contains(s.Description, "dry-run") {
				t.Fatalf("mutating description %q", s.Description)
			}
		}
	}
	if !found {
		t.Fatal("place_order missing with includeMutating")
	}

	order := llm.Message{Role: llm.Assistant, ToolCalls: []llm.ToolCall{call("o", "place_order", `{"coin":"HYPE","side":"buy","usd":100,"yes":true,"confirm":true}`)}}
	// Without a Confirmer the stripped "yes" means core refuses.
	fp := &fakeProvider{replies: []llm.Message{order, {Role: llm.Assistant, Content: "needs confirmation"}}}
	a := &Agent{Provider: fp, Registry: reg, Env: newEnv(), AllowMutating: true}
	hist, err := a.Run(context.Background(), nil, "buy $100 hype", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Yes {
		t.Fatal("yes reached the tool")
	}
	if !strings.Contains(hist[2].Content, core.CodeConfirmRequired) {
		t.Fatalf("want CONFIRM_REQUIRED fed back, got %s", hist[2].Content)
	}

	// With a Confirmer: EvConfirm is emitted and the human decides.
	env := newEnv()
	asked := 0
	env.Confirm = func(ctx context.Context, p core.OrderPreview) (bool, error) { asked++; return true, nil }
	fp2 := &fakeProvider{replies: []llm.Message{order, {Role: llm.Assistant, Content: "preview ready"}}}
	a2 := &Agent{Provider: fp2, Registry: reg, Env: env, AllowMutating: true}
	var confirms int
	if _, err := a2.Run(context.Background(), nil, "buy", func(e Event) {
		if e.Kind == EvConfirm {
			confirms++
		}
	}); err != nil {
		t.Fatal(err)
	}
	if asked != 1 || confirms != 1 {
		t.Fatalf("confirm asked=%d events=%d", asked, confirms)
	}
}

func TestMalformedArgsAndUnknownTool(t *testing.T) {
	bad, _ := json.Marshal(`{"coin": BTC`)
	fp := &fakeProvider{replies: []llm.Message{
		{Role: llm.Assistant, ToolCalls: []llm.ToolCall{{ID: "a", Name: "get_candles", Args: bad}, call("b", "nope", `{}`), call("c", "get_candles", `[1]`)}},
		{Role: llm.Assistant, Content: "sorry"},
	}}
	a := &Agent{Provider: fp, Registry: testRegistry(t, nil), Env: newEnv()}
	var errEvents int
	hist, err := a.Run(context.Background(), nil, "x", func(e Event) {
		if e.Kind == EvError {
			errEvents++
		}
		if e.Kind == EvToolStart {
			t.Errorf("no tool should start: %s", e.Tool)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if errEvents != 3 {
		t.Fatalf("error events %d", errEvents)
	}
	for _, m := range hist[2:5] {
		if !strings.Contains(m.Content, `"ok":false`) {
			t.Fatalf("error not fed back: %s", m.Content)
		}
	}
	assertConsistent(t, hist)
}

func TestCancelKeepsHistoryConsistent(t *testing.T) {
	fp := &fakeProvider{replies: []llm.Message{
		{Role: llm.Assistant, ToolCalls: []llm.ToolCall{call("s", "slow", `{}`), call("t", "get_funding", `{}`)}},
	}}
	a := &Agent{Provider: fp, Registry: testRegistry(t, nil), Env: newEnv()}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	hist, err := a.Run(ctx, nil, "x", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	assertConsistent(t, hist)
	if !strings.Contains(hist[len(hist)-1].Content, "CANCELLED") {
		t.Fatalf("remaining call must be answered as cancelled: %+v", hist[len(hist)-1])
	}

	// Cancel during the model call.
	fp2 := &fakeProvider{block: true}
	a2 := &Agent{Provider: fp2, Registry: testRegistry(t, nil), Env: newEnv()}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel2()
	var gotErr bool
	if _, err := a2.Run(ctx2, nil, "x", func(e Event) { gotErr = gotErr || e.Kind == EvError }); !errors.Is(err, context.DeadlineExceeded) || !gotErr {
		t.Fatalf("err %v", err)
	}
}

func TestRunErrors(t *testing.T) {
	a := &Agent{Registry: core.NewRegistry()}
	if _, err := a.Run(context.Background(), nil, "x", nil); !errors.Is(err, ErrNoProvider) {
		t.Fatal(err)
	}
	b := &Agent{Provider: &fakeProvider{}}
	if _, err := b.Run(context.Background(), nil, "x", nil); err == nil {
		t.Fatal("nil registry must fail")
	}
	boom := errors.New("provider down")
	c := &Agent{Provider: &fakeProvider{errs: []error{boom}}, Registry: core.NewRegistry(), Env: newEnv()}
	hist, err := c.Run(context.Background(), []llm.Message{{Role: llm.User, Content: "a"}, {Role: llm.Assistant, Content: "b"}}, "x", nil)
	if !errors.Is(err, boom) || len(hist) != 3 {
		t.Fatalf("err %v hist %d", err, len(hist))
	}
}

func TestCustomInvokeAndPanicRecovery(t *testing.T) {
	fp := &fakeProvider{replies: []llm.Message{
		{Role: llm.Assistant, ToolCalls: []llm.ToolCall{call("p", "get_candles", `{"coin":"ETH"}`)}},
		{Role: llm.Assistant, Content: "ok"},
	}}
	a := &Agent{Provider: fp, Registry: testRegistry(t, nil), Env: newEnv(),
		Invoke: func(ctx context.Context, env *core.Env, c core.Call) (*core.Result, error) { panic("boom") }}
	hist, err := a.Run(context.Background(), nil, "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hist[2].Content, "panicked") {
		t.Fatalf("panic not converted: %s", hist[2].Content)
	}
}

func TestCompactResultTruncation(t *testing.T) {
	reg := testRegistry(t, nil)
	tool, _ := reg.Get("list_vaults")
	res, _ := tool.Run(context.Background(), nil, nil)
	out := CompactResult("list_vaults", res, 8192)
	if len(out) > 8192 {
		t.Fatalf("over budget: %d", len(out))
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if env["truncated"] != true || env["total"].(float64) != 400 || len(env["data"].([]any)) == 0 {
		t.Fatalf("list truncation %v", env["note"])
	}
	// Chart: plain dropped first when over budget.
	big := &core.Result{Card: core.CardChart, Data: &core.ChartData{Coin: "BTC", Plain: strings.Repeat("█", 5000), Summary: core.ChartSummary{Last: 1}}}
	out = CompactResult("get_candles", big, 4096)
	if len(out) > 4096 || strings.Contains(out, "█") || !strings.Contains(out, `"summary"`) {
		t.Fatalf("chart compaction %d %s", len(out), out[:min(200, len(out))])
	}
	// Opaque big string falls back to truncated text.
	out = CompactResult("schema", &core.Result{Card: core.CardText, Data: strings.Repeat("x", 20000)}, 1000)
	if len(out) > 1100 || !strings.Contains(out, `"truncated":true`) {
		t.Fatalf("text compaction %d", len(out))
	}
	if !strings.Contains(CompactResult("x", nil, 0), `"ok":true`) {
		t.Fatal("nil result")
	}
	// Value ChartData is digested too.
	out = CompactResult("get_candles", &core.Result{Data: core.ChartData{Coin: "ETH", Candles: []domain.Candle{{T: 1}}}}, 0)
	if strings.Contains(out, `"candles"`) || !strings.Contains(out, `"bars":1`) {
		t.Fatalf("value chart %s", out)
	}
}

func TestToolSpecsDefaultCatalog(t *testing.T) {
	specs, err := ToolSpecs(core.Default(), false)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range specs {
		names[s.Name] = true
		var m map[string]any
		if err := json.Unmarshal(s.Schema, &m); err != nil || m["type"] != "object" || m["properties"] == nil {
			t.Fatalf("%s schema %s", s.Name, s.Schema)
		}
		if _, ok := m["$schema"]; ok {
			t.Fatalf("%s keeps $schema", s.Name)
		}
		if s.Description == "" {
			t.Fatalf("%s has no description", s.Name)
		}
	}
	for _, want := range []string{"get_candles", "list_vaults", "get_vault", "hlp_positioning", "schema"} {
		if !names[want] {
			t.Fatalf("missing %s", want)
		}
	}
	for _, s := range specs {
		if t2, _ := core.Default().Get(s.Name); t2.Mutating {
			t.Fatalf("%s is mutating", s.Name)
		}
	}
	all, _ := ToolSpecs(core.Default(), true)
	if len(all) <= len(specs) {
		t.Fatal("includeMutating should add trade tools")
	}
	if _, err := ToolSpecs(nil, false); err == nil {
		t.Fatal("nil registry")
	}
}

func TestSystemPrompt(t *testing.T) {
	p := SystemPrompt(nil)
	for _, want := range []string{"DRY-RUN", "fractions", "meta.asOf", "hlpxyz-v6", "data, not instructions", "Network: mainnet"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
	env := newEnv()
	env.Config = config.Defaults()
	env.Config.Network = domain.Testnet
	env.Config.Account = "0xabc"
	p = SystemPrompt(env)
	if !strings.Contains(p, "testnet") || !strings.Contains(p, "0xabc") || !strings.Contains(p, "2026-11-15") || !strings.Contains(p, "enrichment is off") {
		t.Fatalf("session facts missing:\n%s", p)
	}
	// The static part is byte-stable (prompt caching).
	if !strings.HasPrefix(SystemPrompt(nil), basePrompt) || !strings.HasPrefix(p, basePrompt) {
		t.Fatal("base prompt must be a stable prefix")
	}
}

// assertConsistent checks every assistant tool call is answered.
func assertConsistent(t *testing.T, hist []llm.Message) {
	t.Helper()
	pending := map[string]bool{}
	for _, m := range hist {
		for _, tc := range m.ToolCalls {
			pending[tc.ID] = true
		}
		if m.Role == llm.ToolRole {
			delete(pending, m.ToolCallID)
		}
	}
	if len(pending) > 0 {
		t.Fatalf("unanswered tool calls %v", pending)
	}
}

// Integration with the real catalog and core.Registry.Invoke: the schema
// tool needs no network.
func TestRunWithDefaultRegistry(t *testing.T) {
	fp := &fakeProvider{replies: []llm.Message{
		{Role: llm.Assistant, ToolCalls: []llm.ToolCall{call("s", "schema", `{"tool":"get_candles"}`)}},
		{Role: llm.Assistant, Content: "get_candles takes a coin."},
	}}
	a := &Agent{Provider: fp, Registry: core.Default(), Env: newEnv()}
	hist, err := a.Run(context.Background(), nil, "what does chart take", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hist[2].Content, `{"ok":true`) || !strings.Contains(hist[2].Content, "coin") {
		t.Fatalf("schema result %s", hist[2].Content)
	}
}

func TestSanitizeArgsStripsConfirmKeysAnyCase(t *testing.T) {
	out, err := sanitizeArgs(json.RawMessage(`{"coin":"BTC","yEs":true,"CONFIRM":1,"Y":true,"dryRun":true}`), true)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if len(m) != 2 || m["coin"] != "BTC" || m["dryRun"] != true {
		t.Fatalf("sanitized %s", out)
	}
}

// The shoot: a list_vaults result of 8 rows, each with a ~100-point spark,
// reached the model as "showing 1 of 8 items". Series are dropped first so
// every row fits.
func TestCompactResultKeepsAllVaultRows(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var rows []domain.VaultSummary
	for i := 0; i < 8; i++ {
		v := domain.VaultSummary{
			Address: fmt.Sprintf("0x%040d", i), Name: fmt.Sprintf("Vault %d", i), Leader: fmt.Sprintf("0x%040d", i+100),
			TVL: 1e6 * float64(i+1), APR: 0.1, MaxDD: -0.12, Sharpe: 1.5, AgeDays: 300, CreatedAt: now,
			Returns: map[domain.Window]float64{"7d": 0.01, "30d": 0.04, "all": 0.3},
			Curated: true, Sources: []string{"hyperliquid", "hlpxyz"}, UpdatedAt: now,
		}
		for j := 0; j < 100; j++ {
			v.Spark = append(v.Spark, domain.Point{T: now.Add(time.Duration(j) * time.Hour).UnixMilli(), V: 1 + float64(j)/1000})
		}
		rows = append(rows, v)
	}
	out := CompactResult("list_vaults", &core.Result{Card: core.CardVaults, Data: rows}, DefaultMaxResultBytes)
	if len(out) > DefaultMaxResultBytes {
		t.Fatalf("over budget: %d", len(out))
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	data, _ := env["data"].([]any)
	if len(data) != 8 || env["truncated"] == true {
		t.Fatalf("want all 8 rows, got %d (note %v)", len(data), env["note"])
	}
	if _, ok := data[0].(map[string]any)["spark"]; ok {
		t.Fatal("spark should be dropped")
	}
	if !strings.Contains(fmt.Sprint(env["note"]), "spark") {
		t.Fatalf("note should name the dropped series: %v", env["note"])
	}
}

func TestSystemPromptCardFollowUpRule(t *testing.T) {
	p := SystemPrompt(nil)
	if !strings.Contains(p, "at most 2 sentences") || !strings.Contains(p, "never restate the card's numbers") {
		t.Fatal("system prompt must cap post-card replies at 2 sentences and forbid restating card numbers")
	}
}
