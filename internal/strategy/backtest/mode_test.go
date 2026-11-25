package backtest_test

import (
	"context"
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/strategy"
	"github.com/maxmoneycash/hlp/internal/strategy/backtest"
	"github.com/maxmoneycash/hlp/internal/strategy/internal/testkit"
	"github.com/maxmoneycash/hlp/internal/strategy/sim"
)

func holdOnce(size float64) *funcStrategy {
	done := false
	return &funcStrategy{spec: strategy.Spec{Name: "hold", Universe: []string{"BTC"}, Interval: domain.I15m},
		onBar: func(ctx strategy.Context) error {
			if done {
				return nil
			}
			done = true
			return ctx.Order(strategy.Order{Coin: "BTC", Side: domain.Buy, Size: size})
		}}
}

// Markets apply Hyperliquid's lot size; TradingView mode ignores them and
// slippage, and keeps the script commission.
func TestMarketsAndTradingViewMode(t *testing.T) {
	cs := sample(t)
	src := &testkit.Source{Candle: map[string][]domain.Candle{"BTC": cs}}
	rules := map[string]sim.Rules{"BTC": {SzDecimals: 2, MaxLeverage: 40}}
	res, err := backtest.Run(context.Background(), holdOnce(0.123456), src, backtest.Options{Capital: 10000, Markets: rules, Now: after(cs)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fills) != 1 || res.Fills[0].Sz != 0.12 {
		t.Fatalf("lot size: %+v", res.Fills)
	}
	costs := strategy.Costs{TakerBps: 10, MakerBps: 10, SlippageBps: 50, Funding: true}
	res, err = backtest.Run(context.Background(), holdOnce(0.123456), src, backtest.Options{Capital: 10000, Markets: rules, Costs: costs,
		Mode: backtest.ModeTradingView, Now: after(cs)})
	if err != nil {
		t.Fatal(err)
	}
	f := res.Fills[0]
	if f.Sz != 0.123456 || f.Px != cs[1].O || f.Fee <= 0 {
		t.Fatalf("tradingview mode fill %+v", f)
	}
	if !strings.Contains(strings.Join(res.Warnings, ";"), "TradingView broker mode") {
		t.Fatalf("warnings %v", res.Warnings)
	}
	if _, err := backtest.Run(context.Background(), holdOnce(1), src, backtest.Options{Mode: "nope", Now: after(cs)}); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

// History returns the closed candles an external engine runs over.
func TestHistoryWindow(t *testing.T) {
	cs := sample(t)
	src := &testkit.Source{Candle: map[string][]domain.Candle{"BTC": cs}}
	got, warns, err := backtest.History(context.Background(), src, "BTC", domain.I15m, backtest.Options{Now: after(cs)})
	if err != nil || len(got) != len(cs) || len(warns) != 0 {
		t.Fatalf("history %d/%d %v %v", len(got), len(cs), warns, err)
	}
	got, _, _ = backtest.History(context.Background(), src, "BTC", domain.I15m, backtest.Options{Start: cs[10].T, Now: after(cs)})
	if got[0].T != cs[10].T {
		t.Fatalf("start %d want %d", got[0].T, cs[10].T)
	}
}
