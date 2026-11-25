package pine_test

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/backtest"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/internal/testkit"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/pine"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/sim"
)

func runOn(t *testing.T, src, coin string, cs []domain.Candle, o backtest.Options, po pine.Options) *backtest.Result {
	t.Helper()
	s, err := pine.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	st, err := pine.CompileWith(s, coin, domain.I1h, nil, po)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	o.Now = func() time.Time { return time.UnixMilli(cs[len(cs)-1].T + 24*3600_000) }
	res, err := backtest.Run(context.Background(), st, &testkit.Source{Candle: map[string][]domain.Candle{coin: cs}}, o)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// B-T5 (spec A2/A3): a 40x DOGE entry is clipped to DOGE's 10x max
// leverage and liquidated, never driving equity below 0; 3.7 DOGE is
// dropped with a warning.
func TestDogeLeverageAndLotSize(t *testing.T) {
	var closes []float64
	for i := 0; i < 10; i++ {
		closes = append(closes, 0.1)
	}
	for p := 0.1; p > 0.05; p -= 0.004 {
		closes = append(closes, p)
	}
	cs := path(closes, 0.0005)
	rules := map[string]sim.Rules{"DOGE": {SzDecimals: 0, MaxLeverage: 10}}
	src := `//@version=6
strategy("40x", initial_capital = 10000, margin_long = 2.5)
if bar_index == 5
    strategy.entry("L", strategy.long, qty = 4000000)
`
	res := runOn(t, src, "DOGE", cs, backtest.Options{Capital: 10000, Markets: rules}, pine.Options{})
	if len(res.Fills) < 2 || res.Fills[0].Sz > 1_000_000 || res.Fills[0].Sz < 990_000 {
		t.Fatalf("entry not clipped to 10x: %+v", res.Fills)
	}
	if !res.Fills[1].Liq || res.Account.Liquidations != 1 || res.Account.Clipped != 1 {
		t.Fatalf("no liquidation: %+v %+v", res.Fills, res.Account)
	}
	for _, p := range res.Equity {
		if p.V < 0 {
			t.Fatalf("equity %v < 0 at %d", p.V, p.T)
		}
	}
	// The same order without exchange rules: negative equity (the bug).
	bare := runOn(t, src, "DOGE", cs, backtest.Options{Capital: 10000}, pine.Options{})
	if bare.Equity.Last() >= 0 && !containsS(bare.Warnings, "wiped out") {
		t.Fatalf("control run did not go negative: %v", bare.Equity.Last())
	}
	// Default margin (none) is 1x: 10000 / 0.1 minus fees.
	src1 := `//@version=6
strategy("1x", initial_capital = 10000)
if bar_index == 5
    strategy.entry("L", strategy.long, qty = 4000000)
`
	res = runOn(t, src1, "DOGE", cs, backtest.Options{Capital: 10000, Markets: rules}, pine.Options{})
	if len(res.Fills) == 0 || res.Fills[0].Sz > 100_000 || res.Fills[0].Sz < 99_000 {
		t.Fatalf("1x default: %+v", res.Fills)
	}
	// 3.7 DOGE ($0.37) is dropped and the run says so.
	src2 := `//@version=6
strategy("dust", default_qty_type = strategy.fixed, default_qty_value = 3.7)
if bar_index == 5
    strategy.entry("L", strategy.long)
`
	res = runOn(t, src2, "DOGE", cs, backtest.Options{Capital: 10000, Markets: rules}, pine.Options{})
	if len(res.Fills) != 0 || res.Account.BelowMinNotional != 1 || !containsS(res.Warnings, "minimum notional") {
		t.Fatalf("dust: fills %+v account %+v warnings %v", res.Fills, res.Account, res.Warnings)
	}
}

// B-T6 (spec A4): syminfo.mintick follows the price across powers of ten
// and respects the 6 - szDecimals decimal cap.
func TestMintickPowerOfTenAndSzDecimals(t *testing.T) {
	cs := path([]float64{0.95, 0.99, 1.2, 9.5, 12, 0.0005}, 0)
	src := "//@version=5\nindicator(\"tick\")\nplot(syminfo.mintick, \"tick\")\n"
	s, _ := pine.Parse(src)
	for _, tc := range []struct {
		sz   int
		want []float64
	}{
		{0, []float64{1e-5, 1e-5, 1e-4, 1e-4, 1e-3, 1e-6}},
		{2, []float64{1e-4, 1e-4, 1e-4, 1e-4, 1e-3, 1e-4}},
	} {
		st, err := pine.CompileWith(s, "BTC", domain.I1h, nil, pine.Options{SzDecimals: tc.sz})
		if err != nil {
			t.Fatal(err)
		}
		res := run(t, st, cs, backtest.Options{})
		for i, w := range tc.want {
			if g := res.Plots["tick"][i]; math.Abs(g-w) > w*1e-9 {
				t.Fatalf("szDecimals %d bar %d (px %v): mintick %v want %v", tc.sz, i, cs[i].C, g, w)
			}
		}
	}
}

// B-T4 (spec A1, test plan 2): on the recorded BTC data sample 06 never
// keeps a position open while price trades through its exit channel for
// more than one bar (the stop for "T1 L" is placed and fires).
func TestSample06StopsFire(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(testkit.RepoRoot(), "testdata", "pine-samples", "06-donchian-turtle.pine"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ds := range datasets(t) {
		s, err := pine.Parse(string(b))
		if err != nil {
			t.Fatal(err)
		}
		st, err := pine.Compile(s, "BTC", ds.iv, nil)
		if err != nil {
			t.Fatal(err)
		}
		res := run(t, st, ds.cs, backtest.Options{})
		pos := make([]float64, len(ds.cs))
		fi := 0
		cur := 0.0
		for i, c := range ds.cs {
			for fi < len(res.Fills) && res.Fills[fi].Time <= c.T {
				f := res.Fills[fi]
				if f.Side == domain.Buy {
					cur += f.Sz
				} else {
					cur -= f.Sz
				}
				fi++
			}
			if math.Abs(cur) < 1e-12 {
				cur = 0
			}
			pos[i] = cur
		}
		lo, hi := res.Plots["Exit low"], res.Plots["Exit high"]
		streak := 0
		for i := 1; i < len(ds.cs); i++ {
			through := (pos[i] > 0 && pos[i-1] > 0 && ds.cs[i].L < lo[i-1]) || (pos[i] < 0 && pos[i-1] < 0 && ds.cs[i].H > hi[i-1])
			if through {
				streak++
			} else {
				streak = 0
			}
			if streak > 1 {
				t.Fatalf("%s: position %v open through its exit channel at bar %d", ds.name, pos[i], i)
			}
		}
		if res.Equity.Last() < 0.5*res.Equity[0].V {
			t.Fatalf("%s: sample 06 lost more than half: %v", ds.name, res.Equity.Last())
		}
	}
}

func containsS(xs []string, sub string) bool {
	for _, x := range xs {
		if strings.Contains(x, sub) {
			return true
		}
	}
	return false
}
