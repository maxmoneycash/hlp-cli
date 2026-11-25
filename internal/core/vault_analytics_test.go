package core_test

import (
	"context"
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/core/coretest"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/hlpxyz"
)

type analyticsXYZ struct {
	*coretest.FakeXYZ
	options chan hlpxyz.DetailOpts
}

func (s analyticsXYZ) VaultDetail(_ context.Context, _ string, opts hlpxyz.DetailOpts) (hlpxyz.VaultDetail, error) {
	s.options <- opts
	d := hlpxyz.VaultDetail{Light: opts.Light}
	d.Meta.LastUpdated = coretest.Now.UnixMilli()
	d.Meta.FundingCount = 1
	d.Leverage = []hlpxyz.LeverageRow{{TS: coretest.Now.UnixMilli(), Leverage: 2, NetLeverage: -1, AccountValue: 100}}
	if !opts.Light {
		d.Funding = []hlpxyz.FundingRow{{TS: coretest.Now.UnixMilli(), Coin: "BTC", Payment: -4, Rate: 0.0001}}
	}
	return d, nil
}

func TestVaultAnalyticsUsesFullActivityOnlyWhenRequested(t *testing.T) {
	x := analyticsXYZ{&coretest.FakeXYZ{}, make(chan hlpxyz.DetailOpts, 2)}
	env := coretest.Env(t, x)
	light := mustInvoke(t, env, "get_vault", `{"vault":"long hype btc"}`).Data.(domain.VaultDetail)
	if opts := <-x.options; !opts.Light || !opts.Lev {
		t.Fatalf("default fetched full history: %+v", opts)
	}
	if light.Analytics == nil || !light.Analytics.History.Light || len(light.Analytics.Funding) != 0 || len(light.Analytics.Leverage) != 1 {
		t.Fatalf("light analytics: %+v", light.Analytics)
	}
	full := mustInvoke(t, env, "get_vault", `{"vault":"long hype btc","fills":true}`).Data.(domain.VaultDetail)
	if opts := <-x.options; opts.Light || opts.Lev {
		t.Fatalf("activity omitted full history: %+v", opts)
	}
	if full.Analytics.History.Light || len(full.Analytics.Funding) != 1 || full.Analytics.Funding[0].Payment != -4 {
		t.Fatalf("full analytics: %+v", full.Analytics)
	}
	if full.Freshness["nav"].AsOf != full.Analytics.NAV.BuiltAt || full.Freshness["history"].AsOf != coretest.Now.UnixMilli() {
		t.Fatalf("source ages lost at core boundary: %+v", full.Freshness)
	}
}
