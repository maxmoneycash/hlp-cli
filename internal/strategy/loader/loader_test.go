package loader_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/llm"
	"github.com/maxmoneycash/hlp/internal/strategy"
	"github.com/maxmoneycash/hlp/internal/strategy/internal/testkit"
	"github.com/maxmoneycash/hlp/internal/strategy/loader"
)

type fakeLLM struct{ name string }

func (f *fakeLLM) Name() string { return f.name }
func (f *fakeLLM) Local() bool  { return strings.HasPrefix(f.name, "ollama") }
func (f *fakeLLM) Chat(context.Context, llm.Request, func(llm.Chunk)) (llm.Message, error) {
	return llm.Message{Content: "{}"}, nil
}

func ex(parts ...string) string {
	return filepath.Join(append([]string{testkit.RepoRoot(), "examples"}, parts...)...)
}

// opts never touches the network: every model reference goes to a fake.
func opts(seen *[]strategy.ModelRef) loader.Options {
	return loader.Options{
		Coin: "HYPE", Interval: domain.I1h,
		Provider: func(_ context.Context, ref strategy.ModelRef) (llm.Provider, error) {
			*seen = append(*seen, ref)
			return &fakeLLM{name: ref.String()}, nil
		},
		Default: &fakeLLM{name: "ollama:default"},
	}
}

func TestLoadKinds(t *testing.T) {
	var seen []strategy.ModelRef
	o := opts(&seen)
	ctx := context.Background()
	cases := []struct {
		ref, kind, name string
	}{
		{ex("pine", "supertrend.pine"), loader.KindPine, "Supertrend"},
		{ex("thesis", "long-spy-short-memes.yaml"), loader.KindThesis, "Long SPY / Short memes"},
		{ex("llm", "macro-trader.yaml"), loader.KindLLM, "Macro trader"},
		{"llm:" + ex("llm", "contrarian.yaml"), loader.KindLLM, "Contrarian"},
		{"llm:ollama:qwen3:8b", loader.KindLLM, "qwen3:8b"},
		{"thesis:long BTC, short ETH", loader.KindThesis, "Long BTC / Short ETH"},
		{"long majors, short memes, 2x", loader.KindThesis, "Long majors / Short memes"},
	}
	for _, c := range cases {
		l, err := loader.Load(ctx, c.ref, o)
		if err != nil {
			t.Fatalf("%s: %v", c.ref, err)
		}
		if l.Kind != c.kind || l.Name != c.name || l.Strategy == nil || l.Strategy.Spec().Name != c.name {
			t.Fatalf("%s: %+v", c.ref, l)
		}
	}
	if l, _ := loader.Load(ctx, ex("pine", "supertrend.pine"), o); l.Strategy.Spec().Universe[0] != "HYPE" || l.Pine == nil {
		t.Fatal("pine coin binding")
	}
	if len(seen) != 3 || seen[0].Model != "qwen3:8b" || seen[1].Model != "llama3.1:8b" {
		t.Fatalf("model refs %+v", seen)
	}
	for _, bad := range []string{"", ex("arena", "llm-vs-llm.yaml"), "nope.pine", "thesis:dance", ex("thesis")} {
		if _, err := loader.Load(ctx, bad, o); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestLoadResolveAndWarnings(t *testing.T) {
	var seen []strategy.ModelRef
	o := opts(&seen)
	o.Resolve = func(s string) (domain.Market, error) {
		switch s {
		case "SPY":
			return domain.Market{Coin: "xyz:SP500", Kind: domain.HIP3}, nil
		case "BTC", "HYPE":
			return domain.Market{Coin: s, Kind: domain.Perp}, nil
		}
		return domain.Market{}, errors.New("unknown")
	}
	l, err := loader.Load(context.Background(), "long SPY, short BTC", o)
	if err != nil {
		t.Fatal(err)
	}
	if w := l.Thesis.Weights(); w["xyz:SP500"] != 1 || len(l.Warnings) == 0 || !strings.Contains(l.Warnings[0], "HIP-3") {
		t.Fatalf("resolved %v warnings %v", w, l.Warnings)
	}
	o.Coin = "NOPE"
	if _, err := loader.Load(context.Background(), ex("pine", "ema-cross.pine"), o); err == nil {
		t.Fatal("unresolvable pine coin accepted")
	}
	// A remote model gets a cost warning.
	o.Provider = func(context.Context, strategy.ModelRef) (llm.Provider, error) {
		return &fakeLLM{name: "anthropic:x"}, nil
	}
	o.Resolve = nil
	l, err = loader.Load(context.Background(), "llm:anthropic:claude-x", o)
	if err != nil || len(l.Warnings) != 1 || !strings.Contains(l.Warnings[0], "paid") {
		t.Fatalf("remote warning %+v %v", l, err)
	}
	// Provider failures surface instead of silently skipping the bot.
	if l, err := loader.Load(context.Background(), ex("llm", "macro-trader.yaml"), loader.Options{Provider: func(context.Context, strategy.ModelRef) (llm.Provider, error) {
		return nil, errors.New("down")
	}}); err == nil {
		t.Fatalf("provider error swallowed: %+v", l)
	}
}

func TestLoadArenas(t *testing.T) {
	var seen []strategy.ModelRef
	o := opts(&seen)
	spec, es, err := loader.LoadArena(context.Background(), ex("arena", "llm-vs-llm.yaml"), o)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "LLM vs LLM" || len(es) != 2 || es[0].Name != "Qwen3 8B" || es[1].Name != "Llama 3.1 8B" {
		t.Fatalf("arena %+v %+v", spec, es)
	}
	if es[0].Model.Model != "qwen3:8b" || es[1].Model.Model != "llama3.1:8b" || es[0].Strategy.Spec().Name != "Qwen3 8B" {
		t.Fatalf("models %+v %+v", es[0].Model, es[1].Model)
	}
	_, es, err = loader.LoadArena(context.Background(), ex("arena", "ai-vs-thesis.yaml"), o)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, e := range es {
		kinds = append(kinds, e.Kind+":"+e.Strategy.Spec().Name)
	}
	if got := strings.Join(kinds, ","); got != "llm:Macro LLM,llm:Contrarian LLM,pine:Supertrend HYPE,thesis:Long SPY / Short memes" {
		t.Fatalf("entrants %s", got)
	}
	if es[2].Strategy.Spec().Universe[0] != "HYPE" {
		t.Fatal("pine entrant coin")
	}
	inline := "name: x\nentrants:\n  - llm: ollama:qwen3:8b\n    name: Q\n  - thesis: long BTC\n"
	a, err := loader.ParseArena([]byte(inline))
	if err != nil || a.Capital != 10000 || a.Interval != domain.I1h {
		t.Fatalf("parse %+v %v", a, err)
	}
	for _, bad := range []string{
		"entrants:\n  - thesis: long BTC\n",
		"entrants:\n  - thesis: long BTC\n    llm: x\n  - thesis: long ETH\n",
		"capital: -1\nentrants:\n  - thesis: long BTC\n  - thesis: long ETH\n",
		"interval: 2m\nentrants:\n  - thesis: long BTC\n  - thesis: long ETH\n",
		"bogus: 1\n",
	} {
		if _, err := loader.ParseArena([]byte(bad)); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
	if d := loader.DefaultLLMConfig(); len(d.Universe) != len(loader.DefaultScoutPool) || d.Prompt == "" || d.Scout == nil || d.Scout.Max != 4 {
		t.Fatal("default bot")
	}
}
