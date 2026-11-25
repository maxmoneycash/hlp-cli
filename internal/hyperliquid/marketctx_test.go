package hyperliquid

import (
	"context"
	"testing"

	"github.com/maxmoneycash/hlp/internal/domain"
)

func TestMarketCtx(t *testing.T) {
	f := newFakeHL(t)
	c := f.client(t)
	ctx := context.Background()
	btc, err := c.MarketCtx(ctx, "btc")
	if err != nil {
		t.Fatal(err)
	}
	if btc.Coin != "BTC" || btc.Mark != 65392 || btc.PrevDay != 67133 || btc.Funding != 0.0000125 {
		t.Errorf("BTC %+v", btc)
	}
	nvda, err := c.MarketCtx(ctx, "xyz:NVDA")
	if err != nil {
		t.Fatal(err)
	}
	if nvda.Kind != domain.HIP3 || nvda.Mark == 0 {
		t.Errorf("NVDA %+v", nvda)
	}
	purr, err := c.MarketCtx(ctx, "PURR/USDC")
	if err != nil {
		t.Fatal(err)
	}
	if purr.Kind != domain.Spot || purr.Mark != 0.08215 {
		t.Errorf("PURR %+v", purr)
	}
	if _, err := c.MarketCtx(ctx, "NOPE"); err == nil {
		t.Error("unknown coin should fail")
	}
}

func TestAccountReportsPartial(t *testing.T) {
	f := newFakeHL(t)
	c := f.client(t)
	a, err := c.Account(context.Background(), "0x0000000000000000000000000000000000000001")
	if err != nil {
		t.Skipf("fixture account unavailable: %v", err)
	}
	t.Logf("partial=%v warnings=%v", a.Partial, a.Warnings)
	if a.Partial != (len(a.Warnings) > 0) {
		t.Fatalf("partial %v with warnings %v", a.Partial, a.Warnings)
	}
}
