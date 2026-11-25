package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/config"
)

const testKey = "sk-test-SUPERSECRET-0123456789abcdef"

var fastRetry = retryPolicy{attempts: 3, base: time.Millisecond, max: 5 * time.Millisecond}

// sse writes events in the Anthropic "event: x\ndata: {...}\n\n" shape.
func sse(w io.Writer, events ...string) {
	for _, e := range events {
		var typ struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(e), &typ)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ.Type, e)
	}
}

// oaSSE writes OpenAI "data: {...}\n\n" chunks and a final [DONE].
func oaSSE(w io.Writer, chunks ...string) {
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func collect() (*[]Chunk, func(Chunk)) {
	var got []Chunk
	return &got, func(c Chunk) { got = append(got, c) }
}

func textOf(chunks []Chunk) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(c.Text)
	}
	return b.String()
}

// ---------------------------------------------------------------- anthropic

func newAnt(t *testing.T, h http.HandlerFunc) *Anthropic {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	a := NewAnthropic(AnthropicOptions{BaseURL: srv.URL, Model: "claude-test", Key: config.NewSecret(testKey)})
	a.retry = fastRetry
	return a
}

func TestAnthropicTextStreaming(t *testing.T) {
	var gotReq map[string]any
	a := newAnt(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != testKey || r.Header.Get("anthropic-version") == "" {
			t.Errorf("missing auth headers")
		}
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w,
			`{"type":"message_start","message":{"usage":{"input_tokens":12,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"ping"}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"BTC is "}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"up 2%."}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
			`{"type":"message_stop"}`,
		)
	})
	got, on := collect()
	msg, err := a.Chat(context.Background(), Request{System: "be brief", Messages: []Message{{Role: User, Content: "btc?"}}, JSON: false}, on)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "BTC is up 2%." || msg.Role != Assistant || len(msg.ToolCalls) != 0 {
		t.Fatalf("msg = %+v", msg)
	}
	if textOf(*got) != msg.Content {
		t.Fatalf("streamed %q", textOf(*got))
	}
	last := (*got)[len(*got)-1]
	if !last.Done || last.Usage == nil || last.Usage.InputTokens != 12 || last.Usage.OutputTokens != 7 {
		t.Fatalf("done chunk %+v", last)
	}
	if gotReq["model"] != "claude-test" || gotReq["system"] != "be brief" || gotReq["stream"] != true {
		t.Fatalf("request %v", gotReq)
	}
	if a.Name() != "anthropic:claude-test" || !a.Local() {
		t.Fatalf("name/local %s %v", a.Name(), a.Local())
	}
}

func TestAnthropicToolRoundTrip(t *testing.T) {
	var calls int32
	var second map[string]any
	a := newAnt(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			tools := req["tools"].([]any)
			if tools[0].(map[string]any)["input_schema"] == nil {
				t.Errorf("tool without input_schema: %v", tools[0])
			}
			sse(w,
				`{"type":"message_start","message":{"usage":{"input_tokens":5}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"need data"}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"SIG"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Checking."}}`,
				`{"type":"content_block_stop","index":1}`,
				`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"tu_1","name":"get_candles","input":{}}}`,
				`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"coin\":"}}`,
				`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"BTC\"}"}}`,
				`{"type":"content_block_stop","index":2}`,
				`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"tu_2","name":"get_funding","input":{}}}`,
				`{"type":"content_block_stop","index":3}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":30}}`,
				`{"type":"message_stop"}`,
			)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&second)
		sse(w,
			`{"type":"message_start","message":{"usage":{"input_tokens":50}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Done"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_stop"}`,
		)
	})
	tools := []ToolSpec{{Name: "get_candles", Description: "chart", Schema: json.RawMessage(`{"type":"object","properties":{"coin":{"type":"string"}}}`)}, {Name: "get_funding"}}
	got, on := collect()
	msg, err := a.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "btc"}}, Tools: tools}, on)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 2 {
		t.Fatalf("parallel tool calls: %+v", msg.ToolCalls)
	}
	if msg.ToolCalls[0].ID != "tu_1" || msg.ToolCalls[0].Name != "get_candles" || string(msg.ToolCalls[0].Args) != `{"coin":"BTC"}` {
		t.Fatalf("call 0 %+v", msg.ToolCalls[0])
	}
	if string(msg.ToolCalls[1].Args) != `{}` {
		t.Fatalf("empty input should be {}: %s", msg.ToolCalls[1].Args)
	}
	if len(msg.Reasoning) != 1 || !strings.Contains(string(msg.Reasoning[0]), `"signature":"SIG"`) {
		t.Fatalf("thinking not captured: %s", msg.Reasoning)
	}
	nTool := 0
	for _, c := range *got {
		if c.ToolCall != nil {
			nTool++
		}
	}
	if nTool != 2 {
		t.Fatalf("tool chunks %d", nTool)
	}

	hist := []Message{{Role: User, Content: "btc"}, msg,
		{Role: ToolRole, ToolCallID: "tu_1", Content: `{"ok":true}`},
		{Role: ToolRole, ToolCallID: "tu_2", Content: `{"ok":false,"error":{"code":"X"}}`}}
	final, err := a.Chat(context.Background(), Request{Messages: hist, Tools: tools}, nil)
	if err != nil || final.Content != "Done" {
		t.Fatalf("final %+v %v", final, err)
	}
	msgs := second["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("want user/assistant/user, got %d: %v", len(msgs), msgs)
	}
	asst := msgs[1].(map[string]any)["content"].([]any)
	if asst[0].(map[string]any)["type"] != "thinking" || asst[0].(map[string]any)["signature"] != "SIG" {
		t.Fatalf("thinking must be replayed first: %v", asst)
	}
	results := msgs[2].(map[string]any)["content"].([]any)
	if len(results) != 2 {
		t.Fatalf("parallel tool results must share one user message: %v", results)
	}
	r0 := results[0].(map[string]any)
	r1 := results[1].(map[string]any)
	if r0["type"] != "tool_result" || r0["tool_use_id"] != "tu_1" || r0["is_error"] != nil || r1["is_error"] != true {
		t.Fatalf("results %v", results)
	}
}

func TestAnthropicMalformedToolArgs(t *testing.T) {
	a := newAnt(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"type":"message_start","message":{}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu","name":"get_candles","input":{}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"coin\": BTC"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_stop"}`,
		)
	})
	msg, err := a.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var s string
	if err := json.Unmarshal(msg.ToolCalls[0].Args, &s); err != nil || s != `{"coin": BTC` {
		t.Fatalf("malformed args should be a JSON string literal, got %s", msg.ToolCalls[0].Args)
	}
	// Replaying it sends {} as input (the API requires an object).
	req := a.buildRequest(Request{Messages: []Message{msg}})
	if !strings.Contains(string(req.Messages[0].Content[0]), `"input":{}`) {
		t.Fatalf("replay %s", req.Messages[0].Content[0])
	}
}

func TestAnthropicErrorsAreScrubbed(t *testing.T) {
	var hits int32
	a := newAnt(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(401)
		fmt.Fprintf(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key %s"}}`, testKey)
	})
	_, err := a.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "hi"}}}, nil)
	var le *Error
	if !errors.As(err, &le) || le.Status != 401 || le.Type != "authentication_error" {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), testKey) || strings.Contains(fmt.Sprintf("%+v %#v", err, err), "SUPERSECRET") {
		t.Fatalf("key leaked: %v", err)
	}
	if hits != 1 {
		t.Fatalf("401 must not be retried, hits=%d", hits)
	}
}

func TestAnthropicRetriesThenStreamError(t *testing.T) {
	var hits int32
	a := newAnt(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(529)
			fmt.Fprint(w, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
			return
		}
		sse(w, `{"type":"message_start","message":{}}`, `{"type":"error","error":{"type":"overloaded_error","message":"mid-stream"}}`)
	})
	_, err := a.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "hi"}}}, nil)
	var le *Error
	if !errors.As(err, &le) || le.Message != "mid-stream" || hits != 3 {
		t.Fatalf("err %v hits %d", err, hits)
	}
}

func TestAnthropicTruncatedStream(t *testing.T) {
	a := newAnt(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"type":"message_start","message":{}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"par"}}`)
	})
	if _, err := a.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "hi"}}}, nil); err == nil {
		t.Fatal("stream without message_stop must fail")
	}
}

func TestAnthropicCancelStopsStreaming(t *testing.T) {
	release := make(chan struct{})
	a := newAnt(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"type":"message_start","message":{}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"first"}}`)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	_, err := a.Chat(ctx, Request{Messages: []Message{{Role: User, Content: "hi"}}}, func(c Chunk) {
		if c.Text == "first" {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancel did not stop the stream promptly")
	}
}

func TestAnthropicJSONModeAndSystemMessages(t *testing.T) {
	a := NewAnthropic(AnthropicOptions{})
	req := a.buildRequest(Request{System: "base", JSON: true, Messages: []Message{{Role: System, Content: "extra"}, {Role: User, Content: "a"}, {Role: User, Content: "b"}}})
	if !strings.Contains(req.System, "base") || !strings.Contains(req.System, "extra") || !strings.Contains(req.System, "JSON object") {
		t.Fatalf("system %q", req.System)
	}
	if len(req.Messages) != 1 || len(req.Messages[0].Content) != 2 {
		t.Fatalf("consecutive user turns must merge: %+v", req.Messages)
	}
	if req.MaxTokens != 16000 || req.Model != DefaultAnthropicModel {
		t.Fatalf("defaults %d %s", req.MaxTokens, req.Model)
	}
	if a.Local() {
		t.Fatal("api.anthropic.com is not local")
	}
}

// ---------------------------------------------------------------- openai

func newOA(t *testing.T, h http.HandlerFunc, model string) *OpenAI {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p := NewOpenAI(OpenAIOptions{Label: "ollama", BaseURL: srv.URL + "/v1", Model: model})
	p.retry = fastRetry
	return p
}

func TestOpenAITextStreamingAndModelResolution(t *testing.T) {
	var gotReq map[string]any
	p := newOA(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"object":"list","data":[{"id":"qwen3:8b"},{"id":"llama3.2"}]}`)
		case "/v1/chat/completions":
			if r.Header.Get("Authorization") != "" {
				t.Error("local server must not get an Authorization header without a key")
			}
			_ = json.NewDecoder(r.Body).Decode(&gotReq)
			w.Header().Set("Content-Type", "text/event-stream")
			oaSSE(w,
				`{"choices":[{"delta":{"role":"assistant","content":""}}]}`,
				`{"choices":[{"delta":{"content":"Hello "}}]}`,
				`{"choices":[{"delta":{"content":"trader"},"finish_reason":"stop"}]}`,
			)
		default:
			http.NotFound(w, r)
		}
	}, "")
	got, on := collect()
	msg, err := p.Chat(context.Background(), Request{System: "sys", Messages: []Message{{Role: User, Content: "hi"}}, JSON: true}, on)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "Hello trader" || textOf(*got) != "Hello trader" || !(*got)[len(*got)-1].Done {
		t.Fatalf("msg %+v chunks %+v", msg, *got)
	}
	if gotReq["model"] != "qwen3:8b" {
		t.Fatalf("first model should be picked, got %v", gotReq["model"])
	}
	if _, ok := gotReq["stream_options"]; ok {
		t.Fatal("stream_options must not be sent to local servers")
	}
	if rf, _ := gotReq["response_format"].(map[string]any); rf["type"] != "json_object" {
		t.Fatalf("json mode %v", gotReq["response_format"])
	}
	msgs := gotReq["messages"].([]any)
	if msgs[0].(map[string]any)["role"] != "system" {
		t.Fatalf("system first %v", msgs)
	}
	if p.Name() != "ollama:qwen3:8b" || !p.Local() || p.Model() != "qwen3:8b" {
		t.Fatalf("name %s", p.Name())
	}
}

func TestOpenAIToolCallsIndexed(t *testing.T) {
	var second map[string]any
	var n int32
	p := newOA(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 2 {
			_ = json.NewDecoder(r.Body).Decode(&second)
			oaSSE(w, `{"choices":[{"delta":{"content":"ok"}}]}`)
			return
		}
		oaSSE(w,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"get_candles","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"coin\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","type":"function","function":{"name":"get_orderbook","arguments":"{\"coin\":\"ETH\"}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"BTC\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":4}}`,
		)
	}, "m")
	got, on := collect()
	msg, err := p.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}, Tools: []ToolSpec{{Name: "get_candles"}}}, on)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 2 || string(msg.ToolCalls[0].Args) != `{"coin":"BTC"}` || msg.ToolCalls[1].Name != "get_orderbook" || msg.ToolCalls[1].ID != "c2" {
		t.Fatalf("calls %+v", msg.ToolCalls)
	}
	last := (*got)[len(*got)-1]
	if last.Usage == nil || last.Usage.InputTokens != 9 {
		t.Fatalf("usage %+v", last)
	}
	hist := []Message{{Role: User, Content: "x"}, msg, {Role: ToolRole, ToolCallID: "c1", Content: "r1"}, {Role: ToolRole, ToolCallID: "c2", Content: "r2"}}
	if _, err := p.Chat(context.Background(), Request{Messages: hist}, nil); err != nil {
		t.Fatal(err)
	}
	msgs := second["messages"].([]any)
	asst := msgs[1].(map[string]any)
	tcs := asst["tool_calls"].([]any)
	fn := tcs[0].(map[string]any)["function"].(map[string]any)
	if fn["arguments"] != `{"coin":"BTC"}` || asst["content"] != nil {
		t.Fatalf("assistant replay %v", asst)
	}
	if msgs[2].(map[string]any)["role"] != "tool" || msgs[2].(map[string]any)["tool_call_id"] != "c1" {
		t.Fatalf("tool replay %v", msgs[2])
	}
}

func TestOpenAIToolCallsWithoutIndex(t *testing.T) {
	// Some local servers send whole calls with no index field.
	p := newOA(t, func(w http.ResponseWriter, r *http.Request) {
		oaSSE(w,
			`{"choices":[{"delta":{"tool_calls":[{"id":"a","function":{"name":"get_funding","arguments":"{}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"id":"b","function":{"name":"get_markets","arguments":"{\"sort\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"\"oi\"}"}}]}}]}`,
		)
	}, "m")
	msg, err := p.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 2 || msg.ToolCalls[0].ID != "a" || string(msg.ToolCalls[1].Args) != `{"sort":"oi"}` {
		t.Fatalf("calls %+v", msg.ToolCalls)
	}
}

func TestOpenAIMalformedArgsAndErrors(t *testing.T) {
	p := newOA(t, func(w http.ResponseWriter, r *http.Request) {
		oaSSE(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"z","function":{"name":"get_candles","arguments":"{coin: BTC"}}]}}]}`)
	}, "m")
	msg, err := p.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if argsString(msg.ToolCalls[0].Args) != "{coin: BTC" || json.Valid([]byte("{coin: BTC")) {
		t.Fatalf("args %s", msg.ToolCalls[0].Args)
	}

	q := newOA(t, func(w http.ResponseWriter, r *http.Request) {
		oaSSE(w, `{"error":{"message":"model crashed","type":"server_error"}}`)
	}, "m")
	if _, err := q.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}}, nil); err == nil || !strings.Contains(err.Error(), "model crashed") {
		t.Fatalf("stream error %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprintf(w, `{"error":{"message":"Incorrect API key provided: %s","type":"invalid_request_error"}}`, testKey)
	}))
	defer srv.Close()
	k := NewOpenAI(OpenAIOptions{BaseURL: srv.URL, Model: "gpt", Key: config.NewSecret(testKey)})
	_, err = k.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}}, nil)
	if err == nil || strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatalf("key leaked or no error: %v", err)
	}
	if k.Local() {
		// 127.0.0.1 httptest is loopback; it is local.
	}
}

func TestOpenAINonStreamingFallback(t *testing.T) {
	p := newOA(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"hi","tool_calls":[{"id":"t","type":"function","function":{"name":"schema","arguments":"{}"}}]}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}, "m")
	got, on := collect()
	msg, err := p.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}}, on)
	if err != nil || msg.Content != "hi" || len(msg.ToolCalls) != 1 || !(*got)[len(*got)-1].Done {
		t.Fatalf("%+v %v", msg, err)
	}
}

func TestOpenAINoModels(t *testing.T) {
	p := newOA(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[]}`) }, "")
	if _, err := p.Chat(context.Background(), Request{}, nil); err == nil || !strings.Contains(err.Error(), "no models") {
		t.Fatalf("err %v", err)
	}
}

func TestOpenAIHostedSendsKeyAndUsageOption(t *testing.T) {
	var auth string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
		oaSSE(w, `{"choices":[{"delta":{"content":"x"}}]}`)
	}))
	defer srv.Close()
	p := NewOpenAI(OpenAIOptions{BaseURL: srv.URL, Model: "gpt", Key: config.NewSecret(testKey)})
	// Pretend the server is remote to exercise the hosted branch.
	req := p.buildRequest("gpt", Request{})
	if req.StreamOptions != nil {
		t.Fatal("loopback is local")
	}
	remote := NewOpenAI(OpenAIOptions{BaseURL: "https://api.example.com/v1"})
	if r := remote.buildRequest("gpt", Request{}); r.StreamOptions == nil || !r.StreamOptions.IncludeUsage {
		t.Fatal("hosted endpoints should ask for usage")
	}
	if _, err := p.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}}, nil); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer "+testKey {
		t.Fatal("missing bearer")
	}
}

// ---------------------------------------------------------------- New / Detect

type fakeSecrets map[string]string

func (f fakeSecrets) Get(n string) (config.Secret, error) {
	if v, ok := f[n]; ok {
		return config.NewSecret(v), nil
	}
	return config.Secret{}, config.ErrSecretNotFound
}
func (f fakeSecrets) Set(string, config.Secret) error { return nil }
func (f fakeSecrets) Delete(string) error             { return nil }

func TestNew(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	if p, err := New(config.LLMConfig{}, nil); p != nil || err != nil {
		t.Fatalf("empty provider: %v %v", p, err)
	}
	p, err := New(config.LLMConfig{Provider: "ollama", Model: "qwen3:8b"}, nil)
	if err != nil || p.Name() != "ollama:qwen3:8b" || !p.Local() {
		t.Fatalf("ollama %v %v", p, err)
	}
	p, err = New(config.LLMConfig{Provider: "lmstudio"}, nil)
	if err != nil || !strings.HasPrefix(p.Name(), "lmstudio") || !p.Local() {
		t.Fatalf("lmstudio %v %v", p, err)
	}
	if _, err := New(config.LLMConfig{Provider: "anthropic"}, fakeSecrets{}); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("anthropic without key: %v", err)
	}
	t.Setenv("MY_API_KEY", testKey)
	p, err = New(config.LLMConfig{Provider: "anthropic", APIKeyEnv: "MY_API_KEY"}, nil)
	if err != nil || p.Name() != "anthropic:"+DefaultAnthropicModel || p.Local() {
		t.Fatalf("anthropic %v %v", p, err)
	}
	p, err = New(config.LLMConfig{Provider: "anthropic", Model: "claude-x"}, fakeSecrets{config.SecretAnthropicKey: testKey})
	if err != nil || p.Name() != "anthropic:claude-x" {
		t.Fatalf("keychain key %v %v", p, err)
	}
	if _, err := New(config.LLMConfig{Provider: "openai-compatible", BaseURL: "https://api.example.com/v1", Model: "m"}, nil); err == nil {
		t.Fatal("hosted openai without key must fail")
	}
	p, err = New(config.LLMConfig{Provider: "openai-compatible", BaseURL: "http://127.0.0.1:8080/v1"}, nil)
	if err != nil || !p.Local() {
		t.Fatalf("local openai %v %v", p, err)
	}
	if _, err := New(config.LLMConfig{Provider: "gemini"}, nil); err == nil {
		t.Fatal("unknown provider must fail")
	}
	// Keys never appear in any error or name.
	for _, cfg := range []config.LLMConfig{{Provider: "anthropic", APIKeyEnv: "MY_API_KEY"}, {Provider: "openai-compatible", APIKeyEnv: "MY_API_KEY", BaseURL: "https://x/v1"}} {
		p, err := New(cfg, nil)
		if err != nil && strings.Contains(err.Error(), "SUPERSECRET") {
			t.Fatal("key in error")
		}
		if p != nil && strings.Contains(fmt.Sprintf("%v %+v %s", p, p, p.Name()), "SUPERSECRET") {
			t.Fatal("key printed by provider formatting")
		}
	}
}

func TestDetect(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer dead.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"local-model"}]}`)
	}))
	defer live.Close()

	start := time.Now()
	p := DetectEndpoints(context.Background(), nil, []LocalEndpoint{
		{Label: "ollama", BaseURL: dead.URL + "/v1"},
		{Label: "slow", BaseURL: slow.URL + "/v1"},
		{Label: "lmstudio", BaseURL: live.URL + "/v1"},
	})
	if p == nil || p.Name() != "lmstudio:local-model" || !p.Local() {
		t.Fatalf("detect %v", p)
	}
	if time.Since(start) > time.Second {
		t.Fatal("probes must be bounded by DetectTimeout")
	}
	if DetectEndpoints(context.Background(), nil, []LocalEndpoint{{Label: "x", BaseURL: dead.URL + "/v1"}}) != nil {
		t.Fatal("no live endpoint should give nil")
	}
}

// ---------------------------------------------------------------- helpers

func TestReadSSEMultiline(t *testing.T) {
	var evs []sseEvent
	err := readSSE(strings.NewReader(": comment\nevent: a\ndata: 1\ndata: 2\n\ndata: x\n"), func(e sseEvent) bool {
		evs = append(evs, e)
		return true
	})
	if err != nil || len(evs) != 2 || evs[0].Event != "a" || string(evs[0].Data) != "1\n2" || string(evs[1].Data) != "x" {
		t.Fatalf("%+v %v", evs, err)
	}
}

func TestErrorFormatting(t *testing.T) {
	e := &Error{Provider: "anthropic", Status: 429, Type: "rate_limit_error", Message: "slow down"}
	if e.Error() != "llm anthropic 429 rate_limit_error: slow down" || !e.Retryable() {
		t.Fatal(e.Error())
	}
	if (&Error{Status: 400}).Retryable() {
		t.Fatal("400 is not retryable")
	}
	if scrub("key sk-test-SUPERSECRET-0123456789abcdef and tail 89abcdef", config.NewSecret(testKey)) != "key <redacted> and tail <redacted>" {
		t.Fatal("scrub")
	}
	if parseRetryAfter("2") != 2*time.Second || parseRetryAfter("soon") != 0 {
		t.Fatal("retry-after")
	}
	if !isLocalURL("http://[::1]:1/v1") || isLocalURL("https://api.openai.com") || isLocalURL("::bad") {
		t.Fatal("isLocalURL")
	}
}

func TestAnthropicRefusal(t *testing.T) {
	a := newAnt(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"type":"message_start","message":{}}`,
			`{"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`)
	})
	msg, err := a.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}}, nil)
	if err != nil || msg.Content != RefusalText {
		t.Fatalf("%+v %v", msg, err)
	}
}
