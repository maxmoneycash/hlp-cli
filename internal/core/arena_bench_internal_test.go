package core

import (
	"context"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/strategy"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/paper"
)

type decidingStrat struct{ name string }

func (d decidingStrat) Spec() strategy.Spec {
	return strategy.Spec{Name: d.name, Kind: "llm", Universe: []string{"BTC", "ETH"}, Interval: domain.I1h}
}
func (decidingStrat) Init(strategy.Context) error  { return nil }
func (decidingStrat) OnBar(strategy.Context) error { return nil }
func (decidingStrat) ModelID() string              { return "openrouter:anthropic/claude-sonnet-5" }
func (decidingStrat) LastDecision() *strategy.Decision {
	return &strategy.Decision{Model: "openrouter:anthropic/claude-sonnet-5", Scouted: []string{"BTC"},
		Actions: []strategy.Action{{Action: "BUY", Coin: "BTC", USD: 1000, Confidence: 0.6, OrderUSD: 1000, Model: "openrouter:anthropic/claude-sonnet-5"}}}
}

// The arena payload carries the equal-weight benchmark, each entrant's
// vsBench, its model and latest decision.
func TestArenaDataBenchAndDecisions(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	env := &Env{Now: func() time.Time { return now }}
	s := decidingStrat{"Claude"}
	b := &arenaBench{env: env, name: "EW(BTC,ETH)", coins: benchCoins([]strategy.Strategy{s}),
		start: map[string]float64{"BTC": 100, "ETH": 10}, last: map[string]float64{"BTC": 110, "ETH": 10.5},
		from: now.Add(-time.Hour).UnixMilli(), fetched: now}
	r := &arenaRun{kinds: map[string]string{"Claude": "llm"}, strats: map[string]strategy.Strategy{"Claude": s}, run: &llmRun{budget: nil}, bench: b}
	snaps := []paper.Snapshot{{Strategy: "Claude", Capital: 10000, Started: now.Add(-time.Hour).UnixMilli(),
		Equity: domain.Series{{T: now.Add(-time.Hour).UnixMilli(), V: 10000}, {T: now.UnixMilli(), V: 10900}}}}
	d := r.data(context.Background(), snaps)
	if d.Bench == nil || d.Bench.Name != "EW(BTC,ETH)" || len(d.Bench.Coins) != 2 || abs(d.Bench.Return-0.075) > 1e-12 {
		t.Fatalf("bench %+v", d.Bench)
	}
	e := d.Entrants[0]
	if e.VsBench == nil || abs(*e.VsBench-(1.09/1.075-1)) > 1e-9 || e.Model != "openrouter:anthropic/claude-sonnet-5" || e.Decision == nil || e.Decision.Actions[0].OrderUSD != 1000 {
		t.Fatalf("entrant %+v vs %v", e, e.VsBench)
	}
	if isEW("BTC") || !isEW("") || !isEW("EW") {
		t.Fatal("isEW")
	}
	if vsBench(0.1, nil) != nil || vsBench(0.1, &BenchInfo{Return: -1}) != nil {
		t.Fatal("vsBench without a usable yardstick")
	}
	// A growth ratio: +10% against a halved yardstick is a 120% edge.
	if v := vsBench(0.1, &BenchInfo{Return: -0.5}); v == nil || abs(*v-1.2) > 1e-12 {
		t.Fatalf("vsBench ratio %v", v)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
