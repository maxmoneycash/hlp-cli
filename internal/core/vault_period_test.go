package core_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/core/coretest"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hlpxyz"
	"github.com/maxmoneycash/hlp/internal/hyperliquid"
)

type periodXYZ struct {
	*coretest.FakeXYZ
	compositionRange chan string
}

type periodHL struct{ hyperliquid.Reader }

func (r periodHL) Vault(ctx context.Context, addr string) (domain.VaultDetail, error) {
	d, err := r.Reader.Vault(ctx, addr)
	// The real Hyperliquid Vault read returns account/portfolio, not fills.
	d.Fills = nil
	return d, err
}

func (s periodXYZ) Composition(_ context.Context, _ string, rng string) (hlpxyz.Composition, error) {
	s.compositionRange <- rng
	return hlpxyz.Composition{Range: rng, UpdatedAt: coretest.Now.UnixMilli()}, nil
}

func (s periodXYZ) VaultDetail(_ context.Context, _ string, opts hlpxyz.DetailOpts) (hlpxyz.VaultDetail, error) {
	day := int64(24 * time.Hour / time.Millisecond)
	now := coretest.Now.UnixMilli()
	d := hlpxyz.VaultDetail{}
	d.Meta.LastUpdated = now
	d.Meta.DataStart = now - 90*day
	d.Meta.DataEnd = now
	if opts.Light {
		d.Light = true
		return d, nil
	}
	d.Trades = &hlpxyz.TradesT1{W: "t1", N: 4, Coins: []string{"BTC"}, Dirs: []string{"Close Long"},
		T0: now - 60*day, TS: []int64{0, 50 * day, day, 10 * day}, C: []int{0, 0, 0, 0}, D: []int{0, 0, 0, 0},
		TID: []*int64{nil, nil, nil, nil}, OID: []*int64{nil, nil, nil, nil}, Px: []float64{100, 100, 100, 100}, Sz: []float64{1, 1, 1, 1}, SP: []float64{1, 1, 1, 1}, Fee: []float64{0, 0, 0, 0}, PnL: []float64{1000, 10, -20, 2000}}
	return d, nil
}

func TestClipSeriesUsesExactWindowBoundary(t *testing.T) {
	s := domain.Series{{T: 10, V: 1}, {T: 20, V: 2}, {T: 30, V: 3}}
	got := core.ClipSeries(s, 20)
	if len(got) != 2 || got[0].T != 20 {
		t.Fatalf("exact boundary expanded the window: %+v", got)
	}
	got = core.ClipSeries(s, 25)
	if len(got) != 2 || got[0].T != 20 {
		t.Fatalf("missing boundary lost its real base: %+v", got)
	}
}

func (s periodXYZ) NAV(context.Context, string) (hlpxyz.NAVCurve, error) {
	n := hlpxyz.NAVCurve{NavNow: 1, BuiltAt: coretest.Now.UnixMilli()}
	for i := 90; i >= 0; i-- {
		n.Curve = append(n.Curve, struct {
			TS  int64   `json:"ts"`
			NAV float64 `json:"nav"`
		}{TS: coretest.Now.Add(-time.Duration(i) * 24 * time.Hour).UnixMilli(), NAV: 1})
	}
	return n, nil
}

func TestVaultPeriodFiltersTradeMetricsAndRequestsCompositionWindow(t *testing.T) {
	x := periodXYZ{&coretest.FakeXYZ{}, make(chan string, 1)}
	env := coretest.Env(t, x)
	env.HL = periodHL{env.HL}
	d := mustInvoke(t, env, "get_vault", `{"vault":"long hype btc","window":"1m","fills":true}`).Data.(domain.VaultDetail)
	if got := <-x.compositionRange; got != "1m" {
		t.Fatalf("composition range=%q", got)
	}
	if d.Tearsheet == nil {
		t.Fatal("missing tearsheet")
	}
	if math.Abs(d.Tearsheet.Metrics.WinRate-0.5) > 1e-10 || math.Abs(d.Tearsheet.Metrics.ProfitFactor-0.5) > 1e-10 {
		t.Fatalf("out-of-range fills contaminated metrics: %+v", d.Tearsheet.Metrics)
	}
	d = mustInvoke(t, env, "get_vault", `{"vault":"long hype btc","window":"1y"}`).Data.(domain.VaultDetail)
	if got := <-x.compositionRange; got != "1y" {
		t.Fatalf("selected year still fetched %q composition", got)
	}
}
