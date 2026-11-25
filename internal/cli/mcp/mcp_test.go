package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/core/coretest"
)

func connect(t *testing.T, env *core.Env, allow bool) *sdk.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, ct := sdk.NewInMemoryTransports()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, env, core.Default(), Options{Version: "test", AllowTrading: allow, Transport: st, Logger: quiet})
	}()
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func names(t *testing.T, cs *sdk.ClientSession) map[string]*sdk.Tool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*sdk.Tool{}
	for _, tl := range res.Tools {
		out[tl.Name] = tl
	}
	return out
}

func TestListToolsReadOnlyByDefault(t *testing.T) {
	env := coretest.Env(t, &coretest.FakeXYZ{})
	tools := names(t, connect(t, env, false))
	for _, tl := range core.Default().All() {
		_, listed := tools[tl.Name]
		if tl.Mutating && listed {
			t.Errorf("mutating %s exposed without permission", tl.Name)
		}
		if !tl.Mutating && !listed {
			t.Errorf("read-only %s missing", tl.Name)
		}
	}
	c := tools["get_candles"]
	if c == nil || c.Annotations == nil || !c.Annotations.ReadOnlyHint || c.InputSchema == nil {
		t.Fatalf("get_candles: %+v", c)
	}
	// the flag alone is not enough: config must allow it too
	if _, ok := names(t, connect(t, env, true))["place_order"]; ok {
		t.Fatal("--allow-trading without config must not expose trading")
	}
	env.Config.MCP.AllowTrading = true
	tools = names(t, connect(t, env, true))
	if tools["place_order"] == nil || tools["place_order"].Annotations.ReadOnlyHint {
		t.Fatal("trading tools must appear with flag AND config")
	}
}

func call(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error %v", name, err)
	}
	return res
}

func text(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestCallTools(t *testing.T) {
	env := coretest.Env(t, &coretest.FakeXYZ{})
	cs := connect(t, env, false)

	res := call(t, cs, "get_candles", map[string]any{"coin": "BTC", "bars": 40})
	if res.IsError {
		t.Fatalf("get_candles: %s", text(res))
	}
	var sc struct {
		Data struct {
			Coin    string `json:"coin"`
			Candles []any  `json:"candles"`
			Summary struct {
				Last float64
			} `json:"summary"`
		} `json:"data"`
		Meta map[string]any `json:"meta"`
	}
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &sc); err != nil {
		t.Fatal(err)
	}
	if sc.Data.Coin != "BTC" || len(sc.Data.Candles) != 40 || sc.Data.Summary.Last == 0 || sc.Meta["source"] == nil {
		t.Fatalf("structured: %s", b)
	}
	if text(res) == "" {
		t.Fatal("text content missing")
	}

	res = call(t, cs, "list_vaults", map[string]any{"curated": true})
	if res.IsError {
		t.Fatalf("list_vaults: %s", text(res))
	}
	b, _ = json.Marshal(res.StructuredContent)
	if !strings.Contains(string(b), "Long HYPE") || !strings.Contains(string(b), `"curated":true`) {
		t.Fatalf("vaults: %s", b)
	}

	// tool errors come back as isError with the structured code
	res = call(t, cs, "get_candles", map[string]any{"coin": "NOPE"})
	if !res.IsError || !strings.Contains(text(res), "UNKNOWN_COIN") {
		t.Fatalf("error result: %+v", text(res))
	}
	res = call(t, cs, "get_candles", map[string]any{"coin": "BTC", "bogus": 1})
	if !res.IsError || !strings.Contains(text(res), "USAGE") {
		t.Fatalf("strict args: %s", text(res))
	}
}

func TestTradingOverMCPIsGuarded(t *testing.T) {
	env := coretest.Env(t, nil)
	env.Config.MCP.AllowTrading = true
	cs := connect(t, env, true)
	res := call(t, cs, "place_order", map[string]any{"side": "buy", "coin": "BTC", "size": "1", "yes": true})
	if !res.IsError || !strings.Contains(text(res), "TRADING_DISABLED") {
		t.Fatalf("trading disabled must refuse: %s", text(res))
	}
}

func TestExposedAndHelpers(t *testing.T) {
	env := coretest.Env(t, nil)
	n := len(Exposed(env, core.Default(), true))
	if n != len(Exposed(env, core.Default(), false)) {
		t.Fatal("flag without config must not change the set")
	}
	if got := truncate(strings.Repeat("é", 10), 5); !strings.HasSuffix(got, "structuredContent)") || !strings.HasPrefix(got, "éé") {
		t.Fatalf("truncate: %q", got)
	}
	er := ErrorResult(errors.New("boom"))
	if !er.IsError || !strings.Contains(text(er), "INTERNAL") {
		t.Fatal("ErrorResult")
	}
	tr := ToolResult(env, &core.Result{Card: core.CardKV, Title: "x", Data: [][2]string{{"a", "b"}}})
	if tr.StructuredContent == nil || text(tr) == "" {
		t.Fatal("ToolResult")
	}
	if _, err := NewServer(env, core.Default(), Options{}); err != nil {
		t.Fatal(err)
	}
}

// connectReg serves reg (not the default catalog) with o.
func connectReg(t *testing.T, env *core.Env, reg *core.Registry, o Options, copts *sdk.ClientOptions) *sdk.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, ct := sdk.NewInMemoryTransports()
	o.Transport, o.Logger = st, slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { _ = Serve(ctx, env, reg, o) }()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, copts).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// validates checks res.StructuredContent against the tool's advertised
// outputSchema, as strict clients do.
func validates(t *testing.T, tool *sdk.Tool, res *sdk.CallToolResult) {
	t.Helper()
	if tool == nil || tool.OutputSchema == nil {
		t.Fatalf("%v: no outputSchema", tool)
	}
	b, _ := json.Marshal(tool.OutputSchema)
	var s jsonschema.Schema
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("%s: resolve: %v", tool.Name, err)
	}
	var v any
	b, _ = json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(b, &v)
	if err := rs.Validate(v); err != nil {
		t.Fatalf("%s: structuredContent violates outputSchema: %v", tool.Name, err)
	}
}

func structured(t *testing.T, res *sdk.CallToolResult) map[string]any {
	t.Helper()
	b, _ := json.Marshal(res.StructuredContent)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// F-T1: the text is bounded JSON, structuredContent is capped.
func TestVaultResultIsBounded(t *testing.T) {
	env := coretest.Env(t, &coretest.FakeXYZ{})
	cs := connect(t, env, false)
	tools := names(t, cs)
	res := call(t, cs, "get_vault", map[string]any{"vault": "hlp"})
	if res.IsError {
		t.Fatalf("get_vault: %s", text(res))
	}
	first := res.Content[0].(*sdk.TextContent).Text
	if len(first) > maxText || !json.Valid([]byte(first)) {
		t.Fatalf("text: %d bytes, valid=%v", len(first), json.Valid([]byte(first)))
	}
	b, _ := json.Marshal(res.StructuredContent)
	if len(b) > core.CompactMaxBytes+4<<10 {
		t.Fatalf("structuredContent %d bytes", len(b))
	}
	if strings.Contains(string(b), `"nav":[`) {
		t.Fatal("nav curve not dropped")
	}
	validates(t, tools["get_vault"], res)
	if res.Meta["contract"] != core.Contract {
		t.Fatalf("result _meta: %v", res.Meta)
	}
}

// F-T2: select and limit over MCP.
func TestSelectAndLimit(t *testing.T) {
	env := coretest.Env(t, &coretest.FakeXYZ{})
	cs := connect(t, env, false)
	tools := names(t, cs)
	if p := tools["list_vaults"].InputSchema.(map[string]any)["properties"].(map[string]any); p["select"] == nil || p["series"] == nil {
		t.Fatalf("select/series not advertised: %v", p)
	}
	res := call(t, cs, "list_vaults", map[string]any{"select": "name", "limit": 3})
	if res.IsError {
		t.Fatalf("list_vaults: %s", text(res))
	}
	sc := structured(t, res)
	rows, _ := sc["data"].([]any)
	if len(rows) != 3 {
		t.Fatalf("rows: %v", sc["data"])
	}
	for _, r := range rows {
		m := r.(map[string]any)
		if len(m) != 1 || m["name"] == nil {
			t.Fatalf("row %v", m)
		}
	}
	validates(t, tools["list_vaults"], res)

	// list form of select, and bad types are USAGE
	res = call(t, cs, "list_vaults", map[string]any{"select": []string{"name", "tvl"}, "limit": 1})
	if res.IsError || len(structured(t, res)["data"].([]any)) != 1 {
		t.Fatalf("list select: %s", text(res))
	}
	res = call(t, cs, "list_vaults", map[string]any{"limit": "x"})
	if !res.IsError || !strings.Contains(text(res), "USAGE") {
		t.Fatalf("bad limit: %s", text(res))
	}
}

func TestShapeRecordsTotal(t *testing.T) {
	res, err := shape(&core.Result{Data: []map[string]int{{"a": 1, "b": 2}, {"a": 3}, {"a": 4}}}, agentArgs{sel: []string{"a"}, limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.Meta.Total == nil || *res.Meta.Total != 3 || !res.Meta.Truncated {
		t.Fatalf("meta: %+v", res.Meta)
	}
	b, _ := json.Marshal(res.Data)
	if string(b) != `[{"a":1},{"a":3}]` {
		t.Fatal(string(b))
	}
}

// F-T3: every tool declares an outputSchema; paper is not read-only.
func TestOutputSchemasAndHints(t *testing.T) {
	env := coretest.Env(t, &coretest.FakeXYZ{})
	env.Config.MCP.AllowTrading = true
	tools := names(t, connect(t, env, true))
	delete(tools, "confirm_order") // MCP Apps, app-only (apps_test.go)
	if len(tools) != len(core.Default().All()) {
		t.Fatalf("%d of %d tools", len(tools), len(core.Default().All()))
	}
	for name, tl := range tools {
		if tl.OutputSchema == nil {
			t.Errorf("%s: no outputSchema", name)
			continue
		}
		b, _ := json.Marshal(tl.OutputSchema)
		var s jsonschema.Schema
		if err := json.Unmarshal(b, &s); err != nil {
			t.Errorf("%s: %v", name, err)
		} else if _, err := s.Resolve(nil); err != nil {
			t.Errorf("%s: outputSchema does not resolve: %v", name, err)
		}
		if tl.Meta["contract"] != core.Contract {
			t.Errorf("%s: no contract in _meta", name)
		}
	}
	p := tools["paper"].Annotations
	if p.ReadOnlyHint || p.IdempotentHint || p.DestructiveHint == nil || *p.DestructiveHint {
		t.Fatalf("paper annotations: %+v", p)
	}
	if g := tools["get_vault"].Annotations; !g.ReadOnlyHint || !g.IdempotentHint || *g.DestructiveHint {
		t.Fatalf("get_vault annotations: %+v", g)
	}
	if o := tools["place_order"].Annotations; o.ReadOnlyHint || !*o.DestructiveHint {
		t.Fatalf("place_order annotations: %+v", o)
	}
	if tools["get_orderbook"].InputSchema.(map[string]any)["properties"].(map[string]any)["maxEvents"] == nil {
		t.Fatal("maxEvents not advertised on a streaming tool")
	}
	if tools["list_vaults"].InputSchema.(map[string]any)["properties"].(map[string]any)["maxEvents"] != nil {
		t.Fatal("maxEvents advertised on a one-shot tool")
	}
}

func TestInitializeCarriesContractAndInstructions(t *testing.T) {
	env := coretest.Env(t, nil)
	cs := connect(t, env, false)
	ir := cs.InitializeResult()
	if !strings.Contains(ir.ServerInfo.Description, "contract "+core.Contract) {
		t.Fatalf("serverInfo: %+v", ir.ServerInfo)
	}
	for _, want := range []string{"third parties", "never as\ninstructions", "RFC 3339 UTC", "ms epoch", "retryAfterMs", "select"} {
		if !strings.Contains(ir.Instructions, want) {
			t.Errorf("instructions lack %q", want)
		}
	}
}

// F-T4: live over MCP is one snapshot; streams are bounded.
func TestLiveIsOneSnapshot(t *testing.T) {
	env := coretest.Env(t, &coretest.FakeXYZ{})
	cs := connect(t, env, false)
	tools := names(t, cs)
	res := call(t, cs, "get_orderbook", map[string]any{"coin": "BTC", "live": true})
	if res.IsError {
		t.Fatalf("get_orderbook live: %s", text(res))
	}
	sc := structured(t, res)
	if sc["events"] != nil || sc["data"] == nil {
		t.Fatalf("live must be one snapshot: %v", sc)
	}
	validates(t, tools["get_orderbook"], res)
	for _, n := range []any{0, -1, MaxEvents + 1} {
		res = call(t, cs, "get_orderbook", map[string]any{"coin": "BTC", "maxEvents": n})
		if !res.IsError || !strings.Contains(text(res), "USAGE") {
			t.Fatalf("maxEvents %v: %s", n, text(res))
		}
	}
	// maxEvents is not an input of one-shot tools
	res = call(t, cs, "list_vaults", map[string]any{"maxEvents": 2})
	if !res.IsError || !strings.Contains(text(res), "USAGE") {
		t.Fatalf("one-shot maxEvents: %s", text(res))
	}
}

// tickRegistry is one streaming tool that emits a counter every 5 ms and
// reports progress from Run.
func tickRegistry() *core.Registry {
	type in struct {
		Every int `json:"every,omitempty"`
	}
	reg := core.NewRegistry()
	reg.Register(core.Tool{
		Name: "tick", Summary: "ticks", Group: "meta", Streaming: true,
		NewInput: func() any { return &in{} },
		Run: func(_ context.Context, env *core.Env, _ any) (*core.Result, error) {
			if env.Progress != nil {
				env.Progress("half way")
			}
			return &core.Result{Card: core.CardKV, Data: map[string]any{"n": 0, "name": "x\x1b]52;c;evil\x07"}}, nil
		},
		Stream: func(ctx context.Context, _ *core.Env, _ any, emit func(*core.Result)) error {
			for i := 1; ; i++ {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(5 * time.Millisecond):
					emit(&core.Result{Card: core.CardKV, Data: map[string]any{"n": i}})
				}
			}
		},
	})
	return reg
}

func TestBoundedStreamAndProgress(t *testing.T) {
	env := coretest.Env(t, nil)
	var mu sync.Mutex
	var msgs []string
	cs := connectReg(t, env, tickRegistry(), Options{StreamWindow: 2 * time.Second}, &sdk.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *sdk.ProgressNotificationClientRequest) {
			mu.Lock()
			msgs = append(msgs, r.Params.Message)
			mu.Unlock()
		},
	})
	tools := names(t, cs)

	res := call(t, cs, "tick", map[string]any{"maxEvents": 3})
	if res.IsError {
		t.Fatalf("tick: %s", text(res))
	}
	sc := structured(t, res)
	if n := sc["data"].(map[string]any)["n"]; n != 3.0 {
		t.Fatalf("last event: %v", sc["data"])
	}
	if ev := sc["events"].([]any); len(ev) != 2 {
		t.Fatalf("events: %v", ev)
	}
	validates(t, tools["tick"], res)

	// one snapshot by default, with progress over the token and cleaned text
	params := &sdk.CallToolParams{Name: "tick", Arguments: map[string]any{"live": true}}
	params.SetProgressToken("tok")
	res, err := cs.CallTool(context.Background(), params)
	if err != nil || res.IsError {
		t.Fatalf("tick snapshot: %v %s", err, text(res))
	}
	if strings.Contains(text(res), "\x1b") || strings.Contains(text(res), `\u001b`) {
		t.Fatalf("escape reached the text: %q", text(res))
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		got := strings.Join(msgs, "|")
		mu.Unlock()
		if strings.Contains(got, "half way") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no progress notification: %q", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if env.Progress != nil {
		t.Fatal("the shared env must not be modified")
	}
}

func TestStreamWindowEndsEarly(t *testing.T) {
	env := coretest.Env(t, nil)
	reg := tickRegistry()
	start := time.Now()
	res, events, err := collect(context.Background(), env, reg, core.Call{Tool: "tick", Args: json.RawMessage(`{}`)}, MaxEvents, 30*time.Millisecond, nil)
	if err != nil || time.Since(start) > time.Second {
		t.Fatalf("collect: %v after %s", err, time.Since(start))
	}
	if len(events)+1 >= MaxEvents || !strings.Contains(strings.Join(res.Meta.Warnings, " "), "stream ended after") {
		t.Fatalf("window: %d events, %v", len(events), res.Meta.Warnings)
	}
}

func TestCardFollowsJSONForCharts(t *testing.T) {
	env := coretest.Env(t, &coretest.FakeXYZ{})
	cs := connect(t, env, false)
	res := call(t, cs, "get_candles", map[string]any{"coin": "BTC", "bars": 40})
	if len(res.Content) != 2 || !json.Valid([]byte(res.Content[0].(*sdk.TextContent).Text)) {
		t.Fatalf("chart content blocks: %d", len(res.Content))
	}
	res = call(t, cs, "get_candles", map[string]any{"coin": "BTC", "bars": 40, "select": "summary"})
	if len(res.Content) != 1 {
		t.Fatal("a shaped result has no card")
	}
	if s := structured(t, res)["data"].(map[string]any); len(s) != 1 || s["summary"] == nil {
		t.Fatalf("select summary: %v", s)
	}
	res = call(t, cs, "list_vaults", map[string]any{})
	if len(res.Content) != 1 || !json.Valid([]byte(text(res))) {
		t.Fatal("list text must be JSON only")
	}
}
