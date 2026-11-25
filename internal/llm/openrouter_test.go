package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/config"
)

func TestOpenRouterPreset(t *testing.T) {
	t.Setenv(OpenRouterKeyEnv, testKey)
	var got http.Header
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		got = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"ok\":true}"}}],"usage":{"prompt_tokens":11,"completion_tokens":3}}`)
	}))
	defer srv.Close()

	p, err := New(config.LLMConfig{Provider: "openrouter", BaseURL: srv.URL, Model: "anthropic/claude-sonnet-5"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "openrouter:anthropic/claude-sonnet-5" {
		t.Fatalf("name %q", p.Name())
	}
	msg, err := p.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "hi"}}, JSON: true}, nil)
	if err != nil || msg.Content != `{"ok":true}` {
		t.Fatalf("chat %q %v", msg.Content, err)
	}
	if got.Get("Authorization") != "Bearer "+testKey || got.Get("X-Title") != "hlp" || !strings.Contains(got.Get("HTTP-Referer"), "github.com/maxmoneycash/hlp") {
		t.Fatalf("headers %v", got)
	}
	if !strings.Contains(body, `"model":"anthropic/claude-sonnet-5"`) || !strings.Contains(body, `"json_object"`) {
		t.Fatalf("body %s", body)
	}

	// Default model and endpoint; a remote endpoint needs the key.
	q, err := New(config.LLMConfig{Provider: "openrouter"}, nil)
	if err != nil || q.Name() != "openrouter:"+DefaultOpenRouterModel || q.Local() {
		t.Fatalf("default %v %v", q, err)
	}
	t.Setenv(OpenRouterKeyEnv, "")
	if _, err := New(config.LLMConfig{Provider: "openrouter"}, nil); err == nil || !strings.Contains(err.Error(), OpenRouterKeyEnv) || strings.Contains(err.Error(), testKey) {
		t.Fatalf("missing key: %v", err)
	}
	// The key env var still goes through the allowlist.
	if _, err := New(config.LLMConfig{Provider: "openrouter", APIKeyEnv: "HLP_AGENT_KEY"}, nil); err == nil {
		t.Fatal("trading key env accepted")
	}
	if _, err := New(config.LLMConfig{Provider: "openrouter", BaseURL: "http://openrouter.example/api/v1"}, nil); err == nil {
		t.Fatal("plain-http remote base accepted")
	}
}

const catalogJSON = `{"data":[
 {"id":"openai/gpt-5","name":"OpenAI: GPT-5","context_length":400000,"created":1767225600,"pricing":{"prompt":"0.00000125","completion":"0.00001"}},
 {"id":"anthropic/claude-sonnet-5","name":"Anthropic: Claude Sonnet 5\u001b[31m","context_length":200000,"created":1770000000,"pricing":{"prompt":"0.000003","completion":"0.000015"}},
 {"id":"openrouter/auto","name":"Auto Router","pricing":{"prompt":"-1","completion":"-1"}},
 {"id":"meta-llama/llama-4-scout:free","name":"Llama 4 Scout (free)","pricing":{"prompt":"0","completion":"0"}},
 {"id":"bad id","name":"x"}
]}`

func catalogServer(t *testing.T, hits *atomic.Int32, fail *atomic.Bool) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "" {
			t.Errorf("catalog request %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if fail != nil && fail.Load() {
			http.Error(w, `{"error":{"message":"down"}}`, http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, catalogJSON)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCatalogDecodeCacheAndPrices(t *testing.T) {
	var hits atomic.Int32
	var fail atomic.Bool
	srv := catalogServer(t, &hits, &fail)
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	file := filepath.Join(t.TempDir(), "cache", "openrouter-models.json")
	c := NewCatalog(CatalogOptions{BaseURL: srv.URL, CacheFile: file, Now: clock})

	ms, fetched, err := c.Models(context.Background())
	if err != nil || len(ms) != 4 || !fetched.Equal(now) {
		t.Fatalf("models %d %v %v", len(ms), fetched, err)
	}
	if ms[0].ID != "anthropic/claude-sonnet-5" || strings.ContainsRune(ms[0].Name, 0x1b) {
		t.Fatalf("sorted/cleaned: %+v", ms[0])
	}
	if ms[0].Ref() != "llm:openrouter/anthropic/claude-sonnet-5" || ms[0].Price.InPerM != 3 || ms[0].Price.OutPerM != 15 || !ms[0].Price.Known {
		t.Fatalf("price %+v", ms[0])
	}
	if m, ok := c.Lookup(context.Background(), "openrouter/auto"); !ok || m.Price.Known {
		t.Fatalf("variable price must be unknown: %+v", m)
	}
	ctx := context.Background()
	for name, want := range map[string]float64{
		"openrouter:anthropic/claude-sonnet-5": 3, "anthropic:claude-sonnet-5": 3, "openai:gpt-5": 1.25, "ollama:qwen3:8b": 0,
	} {
		p, ok := c.PriceFor(ctx, name)
		if !ok || p.InPerM != want {
			t.Errorf("%s: %+v %v", name, p, ok)
		}
	}
	if _, ok := c.PriceFor(ctx, "openrouter:openrouter/auto"); ok {
		t.Error("unpriced model priced")
	}
	if got := (Price{InPerM: 3, OutPerM: 15, Known: true}).Cost(1_000_000, 100_000); got != 4.5 {
		t.Errorf("cost %v", got)
	}
	if hits.Load() != 1 {
		t.Fatalf("memory cache: %d fetches", hits.Load())
	}

	// A new process reads the disk cache; a stale cache is served (with
	// the error) when the refresh fails.
	c2 := NewCatalog(CatalogOptions{BaseURL: srv.URL, CacheFile: file, Now: clock})
	if ms, _, err := c2.Models(ctx); err != nil || len(ms) != 4 || hits.Load() != 1 {
		t.Fatalf("disk cache: %d %v hits=%d", len(ms), err, hits.Load())
	}
	now = now.Add(2 * CatalogTTL)
	fail.Store(true)
	c3 := NewCatalog(CatalogOptions{BaseURL: srv.URL, CacheFile: file, Now: clock})
	ms, _, err = c3.Models(ctx)
	if err == nil || len(ms) != 4 {
		t.Fatalf("stale fallback: %d %v", len(ms), err)
	}
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("cache file mode: %v %v", st, err)
	}
}

func TestBudgetCapsAndTallies(t *testing.T) {
	var calls atomic.Int32
	fake := &stubProvider{name: "openrouter:anthropic/claude-sonnet-5", calls: &calls}
	price := func(name string) (Price, bool) {
		if name == fake.name {
			return Price{InPerM: 3, OutPerM: 15, Known: true}, true
		}
		return Price{}, false
	}
	b := NewBudget(5, price)
	p := b.Wrap(fake)
	if b.Wrap(p) != p || p.Name() != fake.name || fmt.Sprint(p) != fake.name {
		t.Fatal("wrap identity / name")
	}
	other := b.Wrap(&stubProvider{name: "openai:gpt-x", calls: &calls})
	var wg sync.WaitGroup
	var refused atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q := p
			if i%2 == 1 {
				q = other
			}
			if _, err := q.Chat(context.Background(), Request{}, nil); errors.Is(err, ErrBudget) {
				refused.Add(1)
			}
		}(i)
	}
	wg.Wait()
	st := b.Stats()
	if calls.Load() != 5 || refused.Load() != 3 || st.Calls != 5 || st.Refused != 3 || st.Max != 5 {
		t.Fatalf("cap: upstream %d refused %d stats %+v", calls.Load(), refused.Load(), st)
	}
	if st.InputTokens != 5*1000 || st.OutputTokens != 5*100 {
		t.Fatalf("tokens %+v", st)
	}
	if len(st.Unpriced) != 1 || st.Unpriced[0] != "openai:gpt-x" || len(st.Models) != 2 {
		t.Fatalf("models %+v", st)
	}
	var priced ModelUsage
	for _, m := range st.Models {
		if m.Model == fake.name {
			priced = m
		}
	}
	if want := float64(priced.Calls) * (1000*3 + 100*15) / 1e6; !priced.Priced || abs(priced.CostUSD-want) > 1e-12 || abs(st.CostUSD-want) > 1e-12 {
		t.Fatalf("cost %+v want %v", priced, want)
	}
	if (*Budget)(nil).Wrap(fake) != Provider(fake) {
		t.Fatal("nil budget must not wrap")
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

type stubProvider struct {
	name  string
	calls *atomic.Int32
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) Local() bool  { return false }
func (s *stubProvider) Chat(_ context.Context, _ Request, onChunk func(Chunk)) (Message, error) {
	s.calls.Add(1)
	if onChunk != nil {
		onChunk(Chunk{Done: true, Usage: &Usage{InputTokens: 1000, OutputTokens: 100}})
	}
	return Message{Role: Assistant, Content: "{}"}, nil
}

// TestLiveOpenRouterModels hits OpenRouter's free public catalog once
// (no key, no paid call). Run with HLP_LIVE=1.
func TestLiveOpenRouterModels(t *testing.T) {
	if os.Getenv("HLP_LIVE") != "1" {
		t.Skip("set HLP_LIVE=1 to call the public OpenRouter /models endpoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := NewCatalog(CatalogOptions{})
	ms, _, err := c.Models(ctx)
	if err != nil || len(ms) < 10 {
		t.Fatalf("live catalog: %d models, %v", len(ms), err)
	}
	priced := 0
	for _, m := range ms {
		if m.Price.Known {
			priced++
		}
	}
	if priced == 0 {
		t.Fatal("no priced models")
	}
	t.Logf("%d models, %d priced; first %s", len(ms), priced, ms[0].ID)
}

func TestListPrice(t *testing.T) {
	for _, c := range []struct {
		in, out string
		known   bool
	}{
		{`"0.000003"`, `"0.000015"`, true},
		{`0.000003`, `0.000015`, true},
		{`"0"`, `"0"`, true},
		{`"-1"`, `"-1"`, false},
		{`"0.000003"`, `null`, false},
		{`"0.000003"`, ``, false},
		{`"abc"`, `"0.1"`, false},
		{`"5"`, `"5"`, false},
		{`"NaN"`, `"0"`, false},
	} {
		if p := listPrice(json.RawMessage(c.in), json.RawMessage(c.out)); p.Known != c.known {
			t.Errorf("listPrice(%s, %s) = %+v, want known %v", c.in, c.out, p, c.known)
		}
	}
}
