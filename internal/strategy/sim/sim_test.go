package sim

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/strategy"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9*math.Max(1, math.Abs(b)) }

func TestLedgerFillLifecycle(t *testing.T) {
	l := NewLedger(1000)
	f := l.Fill("BTC", domain.Buy, 1, 100, 0.001, 1)
	if f.Dir != "Open Long" || !near(f.Fee, 0.1) || l.Position("BTC") != 1 {
		t.Fatalf("open: %+v pos=%v", f, l.Position("BTC"))
	}
	l.Fill("BTC", domain.Buy, 1, 110, 0, 2)
	if !near(l.Entry("BTC"), 105) {
		t.Fatalf("avg entry = %v, want 105", l.Entry("BTC"))
	}
	f = l.Fill("BTC", domain.Sell, 1, 120, 0, 3)
	if f.Dir != "Close Long" || !near(f.ClosedPnL, 15) || !near(l.Entry("BTC"), 105) {
		t.Fatalf("reduce: %+v entry=%v", f, l.Entry("BTC"))
	}
	f = l.Fill("BTC", domain.Sell, 3, 100, 0, 4)
	if f.Dir != "Long > Short" || !near(f.ClosedPnL, -5) || l.Position("BTC") != -2 || l.Entry("BTC") != 100 {
		t.Fatalf("flip: %+v pos=%v entry=%v", f, l.Position("BTC"), l.Entry("BTC"))
	}
	f = l.Fill("BTC", domain.Buy, 2, 90, 0, 5)
	if f.Dir != "Close Short" || !near(f.ClosedPnL, 20) || l.Position("BTC") != 0 {
		t.Fatalf("close short: %+v", f)
	}
	// 1000 - 0.1 fee + 15 - 5 + 20
	if !near(l.Cash(), 1029.9) || !near(l.Realized(), 30) || !near(l.FeesPaid(), 0.1) {
		t.Fatalf("cash=%v realized=%v fees=%v", l.Cash(), l.Realized(), l.FeesPaid())
	}
	if len(l.Coins()) != 0 {
		t.Fatalf("flat ledger lists coins %v", l.Coins())
	}
}

func TestLedgerEquityAccountFunding(t *testing.T) {
	l := NewLedger(1000)
	l.Fill("ETH", domain.Sell, 2, 50, 0, 1)
	px := func(string) float64 { return 40 }
	if !near(l.Equity(px), 1020) || !near(l.Gross(px), 80) {
		t.Fatalf("equity=%v gross=%v", l.Equity(px), l.Gross(px))
	}
	pay := l.ApplyFunding("ETH", 0.001, 40) // short receives when funding > 0
	if !near(pay, -0.08) || !near(l.Cash(), 1000.08) || !near(l.FundingPaid(), -0.08) {
		t.Fatalf("funding pay=%v cash=%v", pay, l.Cash())
	}
	if l.ApplyFunding("BTC", 0.001, 1) != 0 {
		t.Fatal("funding on a flat coin")
	}
	a := l.Account("paper:x", px, 9)
	if a.Equity != l.Equity(px) || len(a.Positions) != 1 || a.Positions[0].Size != -2 || !near(a.Positions[0].UPnL, 20) || !near(a.NetNtl, -80) {
		t.Fatalf("account %+v", a)
	}
	st := l.State()
	b, _ := json.Marshal(st)
	var back LedgerState
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	l2 := LedgerFromState(back)
	if l2.Equity(px) != l.Equity(px) || l2.Position("ETH") != -2 || l2.Capital() != 1000 {
		t.Fatalf("state round trip mismatch")
	}
}

func bar(t int64, o, h, l, c float64) domain.Candle {
	return domain.Candle{T: t, O: o, H: h, L: l, C: c, V: 1}
}

func TestEngineMarketAndCosts(t *testing.T) {
	e := NewEngine(1000, strategy.Costs{TakerBps: 10, SlippageBps: 100}, []string{"BTC"})
	e.AddBar("BTC", bar(0, 100, 100, 100, 100))
	if err := e.Order(strategy.Order{Coin: "BTC", Side: domain.Buy, NotionalUSD: 500}); err != nil {
		t.Fatal(err)
	}
	fs := e.FillMarketAt("BTC", 200, 1)
	if len(fs) != 1 || !near(fs[0].Px, 202) || !near(fs[0].Sz, 500/202.0) || !near(fs[0].Fee, 0.5) {
		t.Fatalf("fill %+v", fs)
	}
	if len(e.Pending()) != 0 {
		t.Fatal("market order left pending")
	}
}

func TestEngineOrderValidation(t *testing.T) {
	e := NewEngine(1000, strategy.Costs{}, []string{"BTC"})
	bad := []strategy.Order{
		{Coin: "DOGE", Side: domain.Buy, Size: 1},
		{Coin: "BTC", Side: "X", Size: 1},
		{Coin: "BTC", Side: domain.Buy},
		{Coin: "BTC", Side: domain.Buy, Size: 1, NotionalUSD: 1},
		{Coin: "BTC", Side: domain.Buy, Size: math.NaN()},
		{Coin: "BTC", Side: domain.Buy, Size: 1, Kind: strategy.Limit},
		{Coin: "BTC", Side: domain.Buy, Size: 1, Kind: "iceberg"},
	}
	for i, o := range bad {
		if err := e.Order(o); err == nil {
			t.Errorf("case %d: expected error for %+v", i, o)
		}
	}
	if err := e.SetTargets(map[string]float64{"DOGE": 1}); err == nil {
		t.Error("SetTargets accepted an unknown coin")
	}
	if err := e.SetTargets(map[string]float64{"BTC": math.Inf(1)}); err == nil {
		t.Error("SetTargets accepted inf")
	}
}

func TestEngineRestingBracketOCO(t *testing.T) {
	e := NewEngine(10000, strategy.Costs{MakerBps: 1, TakerBps: 5}, []string{"BTC"})
	e.AddBar("BTC", bar(0, 100, 100, 100, 100))
	e.L.Fill("BTC", domain.Buy, 1, 100, 0, 0)
	e.SetNow(0)
	_ = e.Order(strategy.Order{Coin: "BTC", Side: domain.Sell, Kind: strategy.Limit, Px: 110, ReduceOnly: true, Tag: "tp"})
	_ = e.Order(strategy.Order{Coin: "BTC", Side: domain.Sell, Kind: strategy.Stop, Px: 95, ReduceOnly: true, Tag: "tp"})
	// Bar placed at the same time is not eligible.
	if fs := e.MatchResting("BTC", bar(0, 100, 120, 90, 100)); len(fs) != 0 {
		t.Fatalf("order filled on its own bar: %+v", fs)
	}
	// Open 100, high 111 is nearer than low 88: path O->H->L->C, so the
	// take-profit fills first and the stop is cancelled (OCO).
	fs := e.MatchResting("BTC", bar(1, 100, 111, 88, 96))
	if len(fs) != 1 || fs[0].Px != 110 || !near(fs[0].ClosedPnL, 10) || !near(fs[0].Fee, 110*1e-4) {
		t.Fatalf("bracket fills %+v", fs)
	}
	if len(e.Pending()) != 0 || e.Position("BTC") != 0 {
		t.Fatalf("pending=%v pos=%v", e.Pending(), e.Position("BTC"))
	}
}

func TestEngineStopGapAndPathOrder(t *testing.T) {
	e := NewEngine(10000, strategy.Costs{TakerBps: 0, SlippageBps: 0}, []string{"BTC"})
	e.L.Fill("BTC", domain.Buy, 1, 100, 0, 0)
	_ = e.Order(strategy.Order{Coin: "BTC", Side: domain.Sell, Kind: strategy.Limit, Px: 110, ReduceOnly: true, Tag: "x"})
	_ = e.Order(strategy.Order{Coin: "BTC", Side: domain.Sell, Kind: strategy.Stop, Px: 95, ReduceOnly: true, Tag: "x"})
	// Low is nearer to the open: O->L->H, the stop triggers first.
	fs := e.MatchResting("BTC", bar(1, 100, 105, 97.9, 104))
	if len(fs) != 0 {
		t.Fatalf("nothing should trigger yet: %+v", fs)
	}
	fs = e.MatchResting("BTC", bar(2, 99, 112, 94, 111))
	if len(fs) != 1 || fs[0].Px != 95 {
		t.Fatalf("stop first: %+v", fs)
	}
	// Gap through a stop fills at the open.
	e.L.Fill("BTC", domain.Buy, 1, 100, 0, 3)
	_ = e.Order(strategy.Order{Coin: "BTC", Side: domain.Sell, Kind: strategy.Stop, Px: 95, ReduceOnly: true})
	fs = e.MatchResting("BTC", bar(4, 90, 91, 89, 90))
	if len(fs) != 1 || fs[0].Px != 90 {
		t.Fatalf("gap fill: %+v", fs)
	}
}

func TestEngineTagReplaceCancelAndReduceOnly(t *testing.T) {
	e := NewEngine(1000, strategy.Costs{}, []string{"BTC", "ETH"})
	_ = e.Order(strategy.Order{Coin: "BTC", Side: domain.Buy, Size: 1, Kind: strategy.Limit, Px: 90, Tag: "a"})
	_ = e.Order(strategy.Order{Coin: "BTC", Side: domain.Buy, Size: 1, Kind: strategy.Limit, Px: 91, Tag: "a"})
	_ = e.Order(strategy.Order{Coin: "ETH", Side: domain.Buy, Size: 1, Kind: strategy.Limit, Px: 9, Tag: "b"})
	if p := e.Pending(); len(p) != 2 || p[0].Px != 91 || p[0].Coin != "BTC" {
		t.Fatalf("tag replace: %+v", p)
	}
	_ = e.Order(strategy.Order{Kind: strategy.Cancel, Tag: "a"})
	if p := e.Pending(); len(p) != 1 || p[0].Coin != "ETH" {
		t.Fatalf("cancel by tag: %+v", p)
	}
	_ = e.Order(strategy.Order{Kind: strategy.Cancel})
	if len(e.Pending()) != 0 {
		t.Fatal("cancel all")
	}
	// Reduce-only never opens or flips.
	_ = e.Order(strategy.Order{Coin: "BTC", Side: domain.Sell, ReduceOnly: true})
	if fs := e.FillMarketAt("BTC", 100, 1); len(fs) != 0 {
		t.Fatalf("reduce-only opened a position: %+v", fs)
	}
	e.L.Fill("BTC", domain.Buy, 2, 100, 0, 1)
	_ = e.Order(strategy.Order{Coin: "BTC", Side: domain.Sell, Size: 5, ReduceOnly: true})
	if fs := e.FillMarketAt("BTC", 100, 2); len(fs) != 1 || fs[0].Sz != 2 || e.Position("BTC") != 0 {
		t.Fatalf("reduce-only clip: %+v", fs)
	}
}

func TestEngineSetTargetsAndCloseAll(t *testing.T) {
	e := NewEngine(10000, strategy.Costs{}, []string{"BTC", "ETH", "SOL"})
	e.AddBar("BTC", bar(0, 100, 100, 100, 100))
	e.AddBar("ETH", bar(0, 10, 10, 10, 10))
	var logs []string
	e.OnLog = func(s string) { logs = append(logs, s) }
	if err := e.SetTargets(map[string]float64{"BTC": 0.5, "ETH": -0.25, "SOL": 0.25}); err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 { // SOL has no price yet
		t.Fatalf("logs %v", logs)
	}
	e.FillMarketAt("BTC", 100, 1)
	e.FillMarketAt("ETH", 10, 1)
	if !near(e.Position("BTC"), 50) || !near(e.Position("ETH"), -250) {
		t.Fatalf("positions BTC=%v ETH=%v", e.Position("BTC"), e.Position("ETH"))
	}
	// Coins missing from the new targets are closed.
	if err := e.SetTargets(map[string]float64{"BTC": 0.5}); err != nil {
		t.Fatal(err)
	}
	e.FillMarketAt("ETH", 10, 2)
	if e.Position("ETH") != 0 {
		t.Fatalf("ETH not closed: %v", e.Position("ETH"))
	}
	if len(e.Pending()) != 0 {
		t.Fatalf("no-op rebalance queued orders: %+v", e.Pending())
	}
	if err := e.CloseAll(); err != nil {
		t.Fatal(err)
	}
	e.FillMarketAt("BTC", 100, 3)
	if e.Position("BTC") != 0 {
		t.Fatal("CloseAll left a position")
	}
}

func TestEngineBookFillAndWipeout(t *testing.T) {
	e := NewEngine(1000, strategy.Costs{SlippageBps: 100}, []string{"BTC"})
	_ = e.Order(strategy.Order{Coin: "BTC", Side: domain.Buy, Size: 30})
	b := domain.Book{Asks: []domain.Level{{Px: 100, Sz: 1}, {Px: 101, Sz: 1}}, Bids: []domain.Level{{Px: 99, Sz: 5}}}
	fs := e.FillMarketBook("BTC", b, 100, 1)
	// 1@100 + 1@101 + 28@(101*1.01): beyond the visible depth
	want := (100 + 101 + 28*101*1.01) / 30
	if len(fs) != 1 || !near(fs[0].Px, want) {
		t.Fatalf("book fill %+v want px %v", fs, want)
	}
	// Price collapses: equity <= 0 -> flatten and halt.
	e.AddBar("BTC", bar(2, 60, 60, 60, 60))
	if !e.CheckWipeout(2) || !e.Halted() || e.Position("BTC") != 0 {
		t.Fatalf("wipeout not handled: halted=%v pos=%v", e.Halted(), e.Position("BTC"))
	}
	if err := e.Order(strategy.Order{Coin: "BTC", Side: domain.Buy, Size: 1}); err == nil {
		t.Fatal("halted engine accepted an order")
	}
}

func TestEngineContextExtras(t *testing.T) {
	e := NewEngine(500, strategy.Costs{}, []string{"BTC"})
	e.MaxBars = 2
	for i := int64(0); i < 10; i++ {
		e.AddBar("BTC", bar(i, 1, 1, 1, float64(i+1)))
	}
	e.AddBar("BTC", bar(3, 1, 1, 1, 99)) // older than the last bar: ignored
	if bs := e.Bars("BTC"); len(bs) != 2 || bs[1].C != 10 || e.Price("BTC") != 10 {
		t.Fatalf("bars %+v", bs)
	}
	e.SetFunding("BTC", 0.0001)
	if e.FundingRate("BTC") != 0.0001 || e.InitialCapital() != 500 || e.RealizedPnL() != 0 || e.Cash() != 500 {
		t.Fatal("context extras")
	}
	if e.StdContext() == nil {
		t.Fatal("nil std context")
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.SetStdContext(ctx)
	cancel()
	if strategy.StdContext(e).Err() == nil {
		t.Fatal("std context not propagated")
	}
	var plotted, marked bool
	e.OnPlot = func(string, float64) { plotted = true }
	e.OnMark = func(coin string, buy bool, _ string) { marked = coin == "BTC" && buy }
	e.Plot("x", 1)
	e.Mark(true, "go")
	if !plotted || !marked {
		t.Fatal("hooks not called")
	}
	e.SetPending([]Resting{{Order: strategy.Order{Coin: "BTC", Side: domain.Buy, Size: 1, Kind: strategy.Limit, Px: 1}, ID: 41}})
	_ = e.Order(strategy.Order{Coin: "BTC", Side: domain.Buy, Size: 1, Kind: strategy.Limit, Px: 2})
	if p := e.Pending(); len(p) != 2 || p[1].ID != 42 {
		t.Fatalf("SetPending ids %+v", p)
	}
	if c, ok := e.LastBar("ETH"); ok || c.T != 0 {
		t.Fatal("LastBar for unknown coin")
	}
	if e.EntryPrice("BTC") != 0 || e.Position("BTC") != 0 || e.Equity() != 500 {
		t.Fatal("flat engine")
	}
	e.L.Fill("BTC", domain.Buy, 1, 10, 0, 1)
	if e.ApplyFunding("BTC", 0.01, 10) != 0.1 {
		t.Fatal("ApplyFunding")
	}
}
