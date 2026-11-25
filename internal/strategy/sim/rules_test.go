package sim

import (
	"math"
	"testing"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/strategy"
)

func dogeEngine(capital float64) *Engine {
	e := NewEngine(capital, strategy.Costs{}, []string{"DOGE"})
	e.Rules = map[string]Rules{"DOGE": {SzDecimals: 0, MaxLeverage: 10}}
	return e
}

// A3: sizes floor to szDecimals; under $10 notional and below-lot orders
// are dropped with a log line; reduce-only closes are exempt.
func TestRulesLotAndMinNotional(t *testing.T) {
	e := dogeEngine(10000)
	var logs []string
	e.OnLog = func(m string) { logs = append(logs, m) }
	e.AddBar("DOGE", bar(0, 5, 5, 5, 5))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(e.Order(strategy.Order{Coin: "DOGE", Side: domain.Buy, Size: 3.7}))
	fs := e.FillMarketAt("DOGE", 5, 1)
	if len(fs) != 1 || fs[0].Sz != 3 {
		t.Fatalf("3.7 DOGE at $5: %+v", fs)
	}
	must(e.Order(strategy.Order{Coin: "DOGE", Side: domain.Buy, Size: 0.4}))
	must(e.Order(strategy.Order{Coin: "DOGE", Side: domain.Buy, Size: 1})) // $5 < $10
	if fs := e.FillMarketAt("DOGE", 5, 2); len(fs) != 0 {
		t.Fatalf("dust filled: %+v", fs)
	}
	if st := e.Stats(); st.BelowLot != 1 || st.BelowMinNotional != 1 {
		t.Fatalf("stats %+v logs %v", st, logs)
	}
	// 3.7 DOGE at $0.07 is $0.26: dropped (the audit repro).
	must(e.Order(strategy.Order{Coin: "DOGE", Side: domain.Buy, Size: 3.7}))
	if fs := e.FillMarketAt("DOGE", 0.07, 3); len(fs) != 0 {
		t.Fatalf("3.7 DOGE at $0.07 filled: %+v", fs)
	}
	// A reduce-only close of a $15 position is never dropped.
	must(e.Order(strategy.Order{Coin: "DOGE", Side: domain.Sell, ReduceOnly: true}))
	if fs := e.FillMarketAt("DOGE", 1, 4); len(fs) != 1 || fs[0].Sz != 3 || e.Position("DOGE") != 0 {
		t.Fatalf("reduce-only close: %+v", fs)
	}
	if len(logs) < 3 {
		t.Fatalf("drops not logged: %v", logs)
	}
}

// A2: a 40x DOGE order is clipped to the 10x max leverage, and an adverse
// move liquidates at maintenance margin with equity left >= 0.
func TestRulesMarginClipAndLiquidation(t *testing.T) {
	e := dogeEngine(10000)
	e.Costs = strategy.Costs{TakerBps: 4.5}
	e.AddBar("DOGE", bar(0, 0.1, 0.1, 0.1, 0.1))
	if err := e.Order(strategy.Order{Coin: "DOGE", Side: domain.Buy, Size: 4_000_000}); err != nil {
		t.Fatal(err)
	}
	fs := e.FillMarketAt("DOGE", 0.1, 1)
	if len(fs) != 1 || fs[0].Sz != 1_000_000 || e.Stats().Clipped != 1 {
		t.Fatalf("40x not clipped to 10x: %+v %+v", fs, e.Stats())
	}
	// Script leverage below max: 2x.
	e2 := dogeEngine(10000)
	e2.SetLeverage("DOGE", 2, 1)
	e2.AddBar("DOGE", bar(0, 0.1, 0.1, 0.1, 0.1))
	_ = e2.Order(strategy.Order{Coin: "DOGE", Side: domain.Buy, Size: 4_000_000})
	if fs := e2.FillMarketAt("DOGE", 0.1, 1); len(fs) != 1 || fs[0].Sz != 200_000 {
		t.Fatalf("script 2x: %+v", fs)
	}
	_ = e2.Order(strategy.Order{Coin: "DOGE", Side: domain.Sell, Size: 400_000}) // flip into a 1x short
	if fs := e2.FillMarketAt("DOGE", 0.1, 2); len(fs) != 1 || e2.Position("DOGE") != -100_000 {
		t.Fatalf("flip clipped to 1x short: %+v pos %v", fs, e2.Position("DOGE"))
	}

	var fills []domain.Fill
	e.OnFill = func(f domain.Fill, tag string) {
		if tag == "liquidation" {
			fills = append(fills, f)
		}
	}
	// No liquidation on a mild bar.
	if e.CheckLiquidation("DOGE", bar(2, 0.1, 0.1, 0.098, 0.099)) {
		t.Fatal("liquidated on a 2% dip")
	}
	// -12%: equity 10000 - 120000 < 0 would be negative without the model.
	b := bar(3, 0.099, 0.1, 0.088, 0.09)
	if !e.CheckLiquidation("DOGE", b) || len(fills) != 1 || !fills[0].Liq || e.Position("DOGE") != 0 {
		t.Fatalf("no liquidation: %+v pos %v", fills, e.Position("DOGE"))
	}
	e.AddBar("DOGE", b)
	if eq := e.Equity(); eq < 0 || eq > 10000 {
		t.Fatalf("equity after liquidation %v", eq)
	}
	// Liquidation price: equity reached maintenance (5% of notional at 10x).
	px := fills[0].Px
	maint := 1_000_000 * px / 20
	eqAt := 10000 - 1_000_000*0.1*4.5e-4 + 1_000_000*(px-0.1)
	if math.Abs(eqAt-maint) > 1e-6 || px <= b.L || px >= b.O {
		t.Fatalf("liq px %v: equity %v maint %v", px, eqAt, maint)
	}
	if e.Stats().Liquidations != 1 {
		t.Fatalf("stats %+v", e.Stats())
	}
}

// Without rules nothing changes (unit tests and non-Hyperliquid callers).
func TestNoRulesNoAccountModel(t *testing.T) {
	e := NewEngine(100, strategy.Costs{}, []string{"DOGE"})
	e.AddBar("DOGE", bar(0, 0.1, 0.1, 0.1, 0.1))
	_ = e.Order(strategy.Order{Coin: "DOGE", Side: domain.Buy, Size: 3.7})
	if fs := e.FillMarketAt("DOGE", 0.1, 1); len(fs) != 1 || fs[0].Sz != 3.7 {
		t.Fatalf("%+v", fs)
	}
	if e.CheckLiquidation("DOGE", bar(1, 0.1, 0.1, 0.0001, 0.0001)) {
		t.Fatal("liquidation without rules")
	}
}
