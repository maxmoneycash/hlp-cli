package core_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/core/coretest"
	"github.com/maxmoneycash/hlp/internal/domain"
)

func TestClosePosition(t *testing.T) {
	env := coretest.Env(t, nil)
	env.Config.Account = coretest.Account

	// trading disabled: refused with exit 3 unless --dry-run previews
	if _, err := invoke(t, env, "close_position", `{"coin":"BTC","yes":true}`); core.ExitCode(err) != 3 {
		t.Fatalf("disabled trading: %v", err)
	}
	p := mustInvoke(t, env, "close_position", `{"coin":"btc","pct":50,"dry-run":true}`).Data.(*core.OrderPreview)
	// long 0.2 BTC -> sell 0.1 reduce-only below the 73,500 mid
	if p.Coin != "BTC" || p.Side != domain.Sell || p.Size != "0.1" || !p.DryRun || len(p.Checks) == 0 {
		t.Fatalf("half close: %+v", p)
	}
	all := mustInvoke(t, env, "close_position", `{"all":true,"dry-run":true}`)
	if ap := all.Data.(*core.OrderPreview); ap.Action != "close_all" || !strings.Contains(ap.Size, "2 positions") {
		t.Fatalf("close all: %+v", ap)
	}
	if !strings.Contains(all.Title, "all 2 positions") {
		t.Fatalf("title %q", all.Title)
	}
	for args, want := range map[string]string{
		`{"dry-run":true}`:                         core.CodeUsage,
		`{"coin":"BTC","all":true,"dry-run":true}`: core.CodeUsage,
		`{"coin":"BTC","pct":150,"dry-run":true}`:  core.CodeUsage,
		`{"coin":"DOGE","dry-run":true}`:           core.CodeNotFound,
		`{"coin":"NOPE","dry-run":true}`:           core.CodeUnknownCoin,
	} {
		if _, err := invoke(t, env, "close_position", args); code(err) != want {
			t.Errorf("%s: %v, want %s", args, err, want)
		}
	}
	env.Config.Account = ""
	if _, err := invoke(t, env, "close_position", `{"coin":"BTC","dry-run":true}`); code(err) != core.CodeUsage {
		t.Fatalf("no account: %v", err)
	}
}

func TestChartSummaryJSON(t *testing.T) {
	b, err := json.Marshal(core.ChartSummary{Last: 2, Open: 1, Change: 1, MA: map[string]float64{"sma20": 1.5}})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"last":2`, `"open":1`, `"change":1`, `"ma":{"sma20":1.5}`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("%s missing %s", b, k)
		}
	}
}

func TestPlotsJSON(t *testing.T) {
	p := core.Plots{"ema": {math.NaN(), 1.5, math.Inf(1)}}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"ema":[null,1.5,null]}` {
		t.Fatalf("got %s", b)
	}
	var back core.Plots
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !math.IsNaN(back["ema"][0]) || back["ema"][1] != 1.5 {
		t.Fatalf("round trip %v", back)
	}
	if b, _ := json.Marshal(core.BacktestData{Plots: core.Plots{"x": {math.NaN()}}}); !strings.Contains(string(b), `"plots":{"x":[null]}`) {
		t.Fatalf("backtest data %s", b)
	}
}

func TestAggregatePositionsROE(t *testing.T) {
	a := core.AggregatePositions("0xhlp", []domain.Account{
		{Positions: []domain.Position{{Coin: "X", Size: 10, Entry: 1, Mark: 0.5, Value: 5, UPnL: -5, MarginUsed: 0.25}}},
		{Positions: []domain.Position{{Coin: "X", Size: -5, Entry: 1, Mark: 0.5, Value: 2.5, UPnL: 2.5, MarginUsed: 0.125}}},
	})
	if len(a.Positions) != 1 {
		t.Fatalf("positions %+v", a.Positions)
	}
	x := a.Positions[0]
	if x.Size != 5 || math.Abs(x.ROE-(-0.5)) > 1e-12 || x.UPnL != -2.5 {
		t.Fatalf("aggregate %+v", x)
	}
}

func TestAggregatePreservesGrossRiskAndFullyHedgedLegs(t *testing.T) {
	a := core.AggregatePositions("parent", []domain.Account{
		{Address: "0xA", Equity: 100, MarginUsed: 20, Positions: []domain.Position{{Coin: "BTC", Size: 2, Value: 200, Entry: 90, Mark: 100, UPnL: 20, ROE: 1, Leverage: 10, MarginUsed: 20, FundingAll: 3}}},
		{Address: "0xB", Equity: 100, MarginUsed: 40, Positions: []domain.Position{{Coin: "BTC", Size: -2, Value: 200, Entry: 110, Mark: 100, UPnL: 20, ROE: 0.5, Leverage: 5, MarginUsed: 40, FundingAll: -1}}},
	})
	if !a.Aggregated || a.GrossNtl != 400 || a.NetNtl != 0 || a.LongNtl != 200 || a.ShortNtl != 200 || a.MarginUsed != 60 || len(a.Positions) != 1 {
		t.Fatalf("netting erased gross risk: %+v", a)
	}
	p := a.Positions[0]
	if !p.Aggregated || p.Size != 0 || p.Value != 0 || p.GrossValue != 400 || p.LongValue != 200 || p.ShortValue != 200 || p.UPnL != 40 || p.FundingAll != 2 || p.MarginUsed != 60 || len(p.Legs) != 2 {
		t.Fatalf("fully hedged coin lost real exposure/PnL: %+v", p)
	}
	if p.Entry != 0 || p.Leverage != 0 || p.Legs[0].Leverage != 10 || p.Legs[1].Leverage != 5 || p.Legs[1].ROE != 0.5 || p.Legs[0].Vault != "0xa" {
		t.Fatalf("per-account risk conflated: %+v", p)
	}
}
