package backtest_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/strategy"
	"github.com/maxmoneycash/hlp/internal/strategy/backtest"
	"github.com/maxmoneycash/hlp/internal/strategy/internal/testkit"
)

// funcStrategy adapts closures to strategy.Strategy.
type funcStrategy struct {
	spec  strategy.Spec
	onBar func(ctx strategy.Context) error
}

func (f *funcStrategy) Spec() strategy.Spec              { return f.spec }
func (f *funcStrategy) Init(strategy.Context) error      { return nil }
func (f *funcStrategy) OnBar(ctx strategy.Context) error { return f.onBar(ctx) }

func sample(t *testing.T) []domain.Candle {
	t.Helper()
	cs, err := testkit.BTC15m()
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func after(cs []domain.Candle) func() time.Time {
	return func() time.Time { return time.UnixMilli(cs[len(cs)-1].T + 24*3600_000) }
}

func TestBuyAndHoldEqualsPriceReturnMinusFees(t *testing.T) {
	cs := sample(t)
	costs := strategy.Costs{TakerBps: 4.5, SlippageBps: 2}
	bought := false
	s := &funcStrategy{spec: strategy.Spec{Name: "hold", Universe: []string{"BTC"}, Interval: domain.I15m},
		onBar: func(ctx strategy.Context) error {
			if bought {
				return nil
			}
			bought = true
			return ctx.Order(strategy.Order{Coin: "BTC", Side: domain.Buy, Size: 0.1})
		}}
	res, err := backtest.Run(context.Background(), s, &testkit.Source{Candle: map[string][]domain.Candle{"BTC": cs}},
		backtest.Options{Capital: 10000, Costs: costs, Now: after(cs)})
	if err != nil {
		t.Fatal(err)
	}
	fillPx := cs[1].O * (1 + 2e-4)
	fee := 0.1 * fillPx * 4.5e-4
	want := 10000 + 0.1*(cs[len(cs)-1].C-fillPx) - fee
	if got := res.Equity.Last(); math.Abs(got-want) > 1e-6 {
		t.Fatalf("final equity %.6f, want %.6f", got, want)
	}
	if len(res.Fills) != 1 || res.Fills[0].Time != cs[1].T || res.Trades != 1 || math.Abs(res.Fees-fee) > 1e-9 {
		t.Fatalf("fills %+v fees %v", res.Fills, res.Fees)
	}
	if res.Equity[0].V != 10000 || len(res.Equity) != len(cs) {
		t.Fatalf("equity curve starts %v with %d points (want %d)", res.Equity[0].V, len(res.Equity), len(cs))
	}
	if res.Tearsheet.Title != "hold" || res.Tearsheet.Meta.Source != "backtest" {
		t.Fatalf("tearsheet meta %+v", res.Tearsheet.Meta)
	}
}

// TestNoLookAhead: decisions only see closed bars and fill on the next bar,
// so a strategy that peeks at the next close (through a side channel) must
// do strictly better than the honest one with the same rule.
func TestNoLookAhead(t *testing.T) {
	cs := sample(t)
	idx := map[int64]int{}
	for i, c := range cs {
		idx[c.T] = i
	}
	rule := func(peek bool) *funcStrategy {
		return &funcStrategy{spec: strategy.Spec{Name: "dir", Universe: []string{"BTC"}, Interval: domain.I15m},
			onBar: func(ctx strategy.Context) error {
				bars := ctx.Bars("BTC")
				last := bars[len(bars)-1]
				if last.T != ctx.Now() {
					return errors.New("context exposes a bar that is not the current one")
				}
				i := idx[ctx.Now()]
				if i+1 >= len(cs) {
					return nil
				}
				up := last.C > last.O // honest: momentum of the closed bar
				if peek {
					up = cs[i+1].C > cs[i+1].O // cheat: the bar we will trade in
				}
				target := 0.5
				if !up {
					target = -0.5
				}
				return ctx.SetTargets(map[string]float64{"BTC": target})
			}}
	}
	opts := backtest.Options{NoCosts: true, Now: after(cs)}
	src := &testkit.Source{Candle: map[string][]domain.Candle{"BTC": cs}}
	honest, err := backtest.Run(context.Background(), rule(false), src, opts)
	if err != nil {
		t.Fatal(err)
	}
	cheat, err := backtest.Run(context.Background(), rule(true), src, opts)
	if err != nil {
		t.Fatal(err)
	}
	if cheat.Equity.Last() <= honest.Equity.Last() {
		t.Fatalf("peeking (%.2f) should beat the honest rule (%.2f)", cheat.Equity.Last(), honest.Equity.Last())
	}
	for _, f := range honest.Fills {
		if i := idx[f.Time]; f.Px != cs[i].O {
			t.Fatalf("fill at %d px %v is not that bar's open %v", f.Time, f.Px, cs[i].O)
		}
	}
}

func TestFundingApplied(t *testing.T) {
	cs := sample(t)
	var fund []domain.FundingPoint
	for h := cs[0].T - 3600_000; h < cs[len(cs)-1].T+3600_000; h += 3600_000 {
		fund = append(fund, domain.FundingPoint{Coin: "BTC", Time: h + 37, Rate: 0.0001})
	}
	short := false
	s := &funcStrategy{spec: strategy.Spec{Name: "short", Universe: []string{"BTC"}, Interval: domain.I15m},
		onBar: func(ctx strategy.Context) error {
			if short {
				return nil
			}
			short = true
			return ctx.Order(strategy.Order{Coin: "BTC", Side: domain.Sell, Size: 1})
		}}
	res, err := backtest.Run(context.Background(), s, &testkit.Source{Candle: map[string][]domain.Candle{"BTC": cs}, Fund: map[string][]domain.FundingPoint{"BTC": fund}},
		backtest.Options{Costs: strategy.Costs{Funding: true}, Now: after(cs)})
	if err != nil {
		t.Fatal(err)
	}
	// Short 1 BTC from bar 1 on: every print inside [bar1.T, last bar close)
	// pays -rate * close of the bar it settled in.
	want := 0.0
	for _, f := range fund {
		for _, c := range cs[1:] {
			if f.Time >= c.T && f.Time < c.T+15*60_000 {
				want += -1 * c.C * f.Rate
			}
		}
	}
	if want >= 0 || math.Abs(res.Funding-want) > 1e-9 {
		t.Fatalf("funding paid %.6f, want %.6f", res.Funding, want)
	}
	noFund, _ := backtest.Run(context.Background(), &funcStrategy{spec: s.spec, onBar: func(ctx strategy.Context) error {
		if ctx.Position("BTC") == 0 && len(ctx.Bars("BTC")) == 1 {
			return ctx.Order(strategy.Order{Coin: "BTC", Side: domain.Sell, Size: 1})
		}
		return nil
	}}, &testkit.Source{Candle: map[string][]domain.Candle{"BTC": cs}}, backtest.Options{NoCosts: true, Now: after(cs)})
	if diff := res.Equity.Last() - noFund.Equity.Last(); math.Abs(diff-(-want)) > 1e-6 {
		t.Fatalf("equity difference %.6f does not equal funding received %.6f", diff, -want)
	}
}

func TestDeterministicAndPlots(t *testing.T) {
	cs := sample(t)
	mk := func() *funcStrategy {
		return &funcStrategy{spec: strategy.Spec{Name: "osc", Universe: []string{"BTC"}, Interval: domain.I15m, Warmup: 10},
			onBar: func(ctx strategy.Context) error {
				bars := ctx.Bars("BTC")
				ctx.Plot("close", bars[len(bars)-1].C)
				if len(bars)%20 == 0 {
					ctx.Mark(true, "tick")
					ctx.Logf("bar %d", len(bars))
					side := domain.Buy
					if ctx.Position("BTC") > 0 {
						side = domain.Sell
					}
					return ctx.Order(strategy.Order{Coin: "BTC", Side: side, NotionalUSD: 5000, Tag: "flip"})
				}
				return nil
			}}
	}
	src := &testkit.Source{Candle: map[string][]domain.Candle{"BTC": cs}}
	a, err := backtest.Run(context.Background(), mk(), src, backtest.Options{Now: after(cs)})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := backtest.Run(context.Background(), mk(), src, backtest.Options{Now: after(cs)})
	if !reflect.DeepEqual(a.Equity, b.Equity) || !reflect.DeepEqual(a.Fills, b.Fills) {
		t.Fatal("two runs differ")
	}
	p := a.Plots["close"]
	if len(p) != len(cs) || !math.IsNaN(p[0]) || p[20] != cs[20].C {
		t.Fatalf("plot alignment: len=%d p0=%v p20=%v", len(p), p[0], p[20])
	}
	if len(a.Logs) == 0 || !strings.Contains(a.Logs[0], "bar 20") || len(a.Markers) == 0 {
		t.Fatalf("logs %v markers %v", a.Logs, a.Markers)
	}
	// Warmup 10: the curve starts at bar 9's close.
	if a.Equity[0].T != cs[9].T+15*60_000 {
		t.Fatalf("equity starts at %d", a.Equity[0].T)
	}
}

func TestMultiCoinListingAndWindow(t *testing.T) {
	t0 := int64(1_700_000_000_000) / 86_400_000 * 86_400_000
	btc := testkit.Synthetic(t0, domain.I1d, 60, func(i int) float64 { return 100 + float64(i) })
	late := testkit.Synthetic(t0+20*86_400_000, domain.I1d, 40, func(i int) float64 { return 10 })
	src := &testkit.Source{Candle: map[string][]domain.Candle{"BTC": btc, "NEW": late}}
	var seenNew []int
	s := &funcStrategy{spec: strategy.Spec{Name: "pair", Universe: []string{"BTC", "NEW"}, Interval: domain.I1d},
		onBar: func(ctx strategy.Context) error {
			seenNew = append(seenNew, len(ctx.Bars("NEW")))
			w := map[string]float64{"BTC": 1}
			if len(ctx.Bars("NEW")) > 0 {
				w["NEW"] = -0.5
			}
			return ctx.SetTargets(w)
		}}
	now := func() time.Time { return time.UnixMilli(t0 + 100*86_400_000) }
	res, err := backtest.Run(context.Background(), s, src, backtest.Options{NoCosts: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if seenNew[0] != 0 || seenNew[len(seenNew)-1] == 0 {
		t.Fatalf("listing not respected: %v", seenNew[:3])
	}
	found := false
	for _, w := range res.Warnings {
		found = found || strings.Contains(w, "NEW: history starts")
	}
	if !found {
		t.Fatalf("missing listing warning: %v", res.Warnings)
	}
	// Window: start at day 30, end at day 50.
	res, err = backtest.Run(context.Background(), s, src, backtest.Options{NoCosts: true, Now: now,
		Start: t0 + 30*86_400_000, End: t0 + 50*86_400_000})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(res.Candles["BTC"]); n != 20 {
		t.Fatalf("windowed candles = %d, want 20", n)
	}
	// Start before the data: warning about the effective start.
	res, err = backtest.Run(context.Background(), s, src, backtest.Options{NoCosts: true, Now: now, Start: t0 - 90*86_400_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, ";"), "after the requested start") {
		t.Fatalf("warnings %v", res.Warnings)
	}
}

func TestRunErrors(t *testing.T) {
	cs := sample(t)
	src := &testkit.Source{Candle: map[string][]domain.Candle{"BTC": cs}}
	ok := func(strategy.Context) error { return nil }
	cases := []struct {
		name string
		s    strategy.Strategy
		ds   strategy.DataSource
	}{
		{"nil", nil, src},
		{"empty universe", &funcStrategy{spec: strategy.Spec{Name: "x"}, onBar: ok}, src},
		{"bad interval", &funcStrategy{spec: strategy.Spec{Name: "x", Universe: []string{"BTC"}, Interval: "7m"}, onBar: ok}, src},
		{"unknown coin", &funcStrategy{spec: strategy.Spec{Name: "x", Universe: []string{"NOPE"}, Interval: domain.I15m}, onBar: ok}, src},
		{"warmup too long", &funcStrategy{spec: strategy.Spec{Name: "x", Universe: []string{"BTC"}, Interval: domain.I15m, Warmup: 1000}, onBar: ok}, src},
		{"strategy error", &funcStrategy{spec: strategy.Spec{Name: "x", Universe: []string{"BTC"}, Interval: domain.I15m},
			onBar: func(strategy.Context) error { return errors.New("boom") }}, src},
	}
	for _, c := range cases {
		if _, err := backtest.Run(context.Background(), c.s, c.ds, backtest.Options{Now: after(cs)}); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := backtest.Run(ctx, &funcStrategy{spec: strategy.Spec{Name: "x", Universe: []string{"BTC"}, Interval: domain.I15m}, onBar: ok}, src, backtest.Options{}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled context: %v", err)
	}
}

func TestDropsFormingBarAndDefaultsCosts(t *testing.T) {
	cs := sample(t)
	n := 0
	s := &funcStrategy{spec: strategy.Spec{Name: "count", Universe: []string{"BTC"}, Interval: domain.I15m},
		onBar: func(ctx strategy.Context) error { n++; return nil }}
	// "now" is inside the last bar: it is still forming and must be dropped.
	now := func() time.Time { return time.UnixMilli(cs[len(cs)-1].T + 60_000) }
	res, err := backtest.Run(context.Background(), s, &testkit.Source{Candle: map[string][]domain.Candle{"BTC": cs}}, backtest.Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if n != len(cs)-1 || len(res.Candles["BTC"]) != len(cs)-1 {
		t.Fatalf("decisions %d candles %d, want %d", n, len(res.Candles["BTC"]), len(cs)-1)
	}
}

func TestProgressReported(t *testing.T) {
	cs := sample(t)
	fund := []domain.FundingPoint{{Coin: "BTC", Time: cs[1].T + 37, Rate: 0.0001}}
	s := &funcStrategy{spec: strategy.Spec{Name: "idle", Universe: []string{"BTC"}, Interval: domain.I15m},
		onBar: func(strategy.Context) error { return nil }}
	var stages []string
	_, err := backtest.Run(context.Background(), s, &testkit.Source{Candle: map[string][]domain.Candle{"BTC": cs}, Fund: map[string][]domain.FundingPoint{"BTC": fund}},
		backtest.Options{Costs: strategy.Costs{Funding: true}, Now: after(cs), Progress: func(stage string, done, total int) {
			stages = append(stages, fmt.Sprintf("%s %d/%d", stage, done, total))
		}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"candles 0/1", "candles 1/1", "funding 0/1", "funding 1/1"}
	if strings.Join(stages, ",") != strings.Join(want, ",") {
		t.Fatalf("progress %v, want %v", stages, want)
	}
}
