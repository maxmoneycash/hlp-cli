package hyperliquid

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestAccountSumsAllDexesSpotEarn(t *testing.T) {
	f := newFakeHL(t)
	c := f.client(t)
	ctx := context.Background()
	a, err := c.Account(ctx, addrTrader)
	if err != nil {
		t.Fatal(err)
	}
	// native 1670088.165282 + xyz 0 + spot USDC 2522.459339.
	if !near(a.PerpEquity, 1670088.1652820001, 1e-6) || !near(a.SpotUSD, 2522.459339, 1e-9) || a.EarnUSD != 0 {
		t.Fatalf("parts %+v", a)
	}
	if !near(a.Equity, 1670088.1652820001+2522.459339, 1e-6) {
		t.Fatalf("equity %v", a.Equity)
	}
	if len(a.Positions) != 2 || a.Positions[0].Coin != "BTC" || a.Positions[0].Size != 11.45136 || !a.Positions[0].Cross {
		t.Fatalf("positions %+v", a.Positions)
	}
	if !near(a.GrossNtl, 748312.02192+89239.52263, 1e-6) || !near(a.NetNtl, a.GrossNtl, 1e-9) {
		t.Fatalf("ntl %v %v", a.GrossNtl, a.NetNtl)
	}
	if !near(a.Positions[0].Mark, 748312.02192/11.45136, 1e-6) {
		t.Fatal("mark")
	}
	if len(a.OpenOrders) != 2 || a.OpenOrders[0].TIF != "Alo" || a.OpenOrders[0].Side != domain.Sell {
		t.Fatalf("orders %+v", a.OpenOrders)
	}
	if a.Address != addrTrader || a.Time == 0 {
		t.Fatal("address/time")
	}
	if f.count("clearinghouseState") < 2 {
		t.Fatal("must query every dex")
	}

	// HLP Strategy X: $1,000 perp + ~$100.86M Earn.
	x, err := c.Account(ctx, addrStrategyX)
	if err != nil {
		t.Fatal(err)
	}
	if !near(x.EarnUSD, 100859337.9991511256, 1e-3) || !near(x.Equity, 100859337.9991511256+1000, 1e-3) {
		t.Fatalf("strategy X %+v", x)
	}
	if _, err := c.Account(ctx, "not-an-address"); !errors.Is(err, ErrInvalidAddress) {
		t.Fatal("bad address accepted")
	}
}

func TestEarnUSDNetsBorrow(t *testing.T) {
	s := BorrowLendUserState{TokenToState: []TokenBorrowLend{
		{Token: 0, State: BorrowLendTokenState{Supply: BorrowLendSide{Value: "100"}, Borrow: BorrowLendSide{Value: "30"}}},
		{Token: 150, State: BorrowLendTokenState{Supply: BorrowLendSide{Value: "2"}}},
	}}
	got := EarnUSD(s, func(tok int) float64 {
		if tok == 150 {
			return 50
		}
		return 1
	})
	if got != 170 {
		t.Fatal(got)
	}
}

func TestFillsMapping(t *testing.T) {
	f := newFakeHL(t)
	c := f.client(t)
	fills, err := c.Fills(context.Background(), addrTrader, 0, 0)
	if err != nil || len(fills) != 3 {
		t.Fatalf("%v %d", err, len(fills))
	}
	if fills[0].Time > fills[2].Time {
		t.Fatal("not oldest first")
	}
	var inits domain.Fill
	for _, fl := range fills {
		if fl.Coin == "INIT" {
			inits = fl
		}
	}
	if inits.ClosedPnL != 14.06002 || inits.Fee != -0.002893 || inits.Side != domain.Sell || inits.TID != 279186103928867 {
		t.Fatalf("%+v", inits)
	}
	if _, err := c.Fills(context.Background(), addrTrader, 1, 2); err != nil || f.count("userFillsByTime") != 1 {
		t.Fatalf("by time: %v", err)
	}
	if ToFill(WireFill{Liquidation: []byte(`{"liquidatedUser":"0x1"}`)}).Liq != true {
		t.Fatal("liquidation flag")
	}
}

func TestVaultListFromStats(t *testing.T) {
	f := newFakeHL(t)
	f.srv.Config.Handler = statsHandler(t, f.srv.Config.Handler, nil)
	c := f.client(t)
	now := time.UnixMilli(1794735355130)
	c.opts.Now = func() time.Time { return now }
	vs, meta, err := c.VaultList(context.Background())
	if err != nil || len(vs) != 4 || meta.Source != "stats-data" || meta.Stale {
		t.Fatalf("%v %d %+v", err, len(vs), meta)
	}
	by := map[string]domain.VaultSummary{}
	for _, v := range vs {
		by[v.Address] = v
	}
	hlp := by[addrHLP]
	if hlp.Rel != "parent" || hlp.Kind != domain.VaultHyperCore || !near(hlp.TVL, 151305702.4667499959, 1e-3) || !near(hlp.APR, 0.2212745973312073, 1e-12) {
		t.Fatalf("HLP %+v", hlp)
	}
	if by[addrStrategyX].Rel != "child" {
		t.Fatal("child rel")
	}
	closed := by["0x00043d4a2c258892172dc6ce5f62e2e3936ae0dc"]
	if !closed.Closed || closed.AllowDeposits {
		t.Fatalf("closed vault %+v", closed)
	}
	g := by["0xac26cf5f3c46b5e102048c65b977d2551b72a9c7"]
	if g.Name != "Long HYPE & BTC | Short Garbage" || g.AgeDays < 200 || len(g.Spark) != 12 || g.Spark[0].T != 1744227338023 {
		t.Fatalf("garbage %+v", g)
	}
	// month pnl 0 -> 253402.991261 over TVL 2728089.188674.
	want := 253402.991261 / (2728089.1886740001 - 253402.991261)
	if r, ok := g.Returns[domain.W1M]; !ok || !near(r, want, 1e-12) {
		t.Fatalf("1m return %v want %v", g.Returns, want)
	}
	if _, ok := g.Returns[domain.WAll]; ok {
		t.Fatal("allTime return must not be approximated from stats pnls")
	}
	if g.Sources[0] != "stats-data" {
		t.Fatal("sources")
	}
}

// statsHandler wraps h with a /stats/vaults route that serves the sample
// with an ETag; hits counts requests; inm records If-None-Match values.
func statsHandler(t *testing.T, h http.Handler, hits *atomic.Int32) http.Handler {
	body := readFx(t, "stats/Mainnet_vaults_sample.json")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stats/vaults" {
			h.ServeHTTP(w, r)
			return
		}
		if hits != nil {
			hits.Add(1)
		}
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Write(body)
	})
}

func TestStatsETagCacheAndStale(t *testing.T) {
	var hits atomic.Int32
	var inm []string
	body := readFx(t, "stats/Mainnet_vaults_sample.json")
	down := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		inm = append(inm, r.Header.Get("If-None-Match"))
		if down {
			w.WriteHeader(503)
			return
		}
		if r.Header.Get("If-None-Match") == `"abc"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"abc"`)
		w.Write(body)
	}))
	defer srv.Close()
	dir := t.TempDir()
	now := time.Unix(1794735000, 0)
	ep := Endpoints{Info: srv.URL + "/info", Stats: srv.URL}
	mk := func() *Client {
		return New(Options{Endpoints: &ep, CacheDir: dir, Now: func() time.Time { return now }})
	}
	c := mk()
	ctx := context.Background()
	v1, m1, err := c.StatsVaults(ctx)
	if err != nil || len(v1) != 4 || hits.Load() != 1 || inm[0] != "" {
		t.Fatalf("first: %v %d", err, hits.Load())
	}
	// Within 5 minutes: memory, no request.
	if _, _, err := c.StatsVaults(ctx); err != nil || hits.Load() != 1 {
		t.Fatal("refreshed too early")
	}
	// After 5 minutes: conditional GET -> 304 -> cached copy.
	now = now.Add(6 * time.Minute)
	v2, m2, err := c.StatsVaults(ctx)
	if err != nil || hits.Load() != 2 || inm[1] != `"abc"` || len(v2) != 4 || !m2.AsOf.After(m1.AsOf) {
		t.Fatalf("304 path: %v hits=%d inm=%q", err, hits.Load(), inm)
	}
	if _, err := os.Stat(c.statsPath()); err != nil {
		t.Fatalf("disk cache: %v", err)
	}
	// A new process reuses the disk cache and ETag.
	now = now.Add(6 * time.Minute)
	c2 := mk()
	if _, _, err := c2.StatsVaults(ctx); err != nil || inm[2] != `"abc"` {
		t.Fatalf("disk etag: %v %q", err, inm)
	}
	// Network down: stale cache served with Meta.Stale.
	now = now.Add(6 * time.Minute)
	down = true
	v3, m3, err := c2.StatsVaults(ctx)
	if err != nil || !m3.Stale || len(m3.Warnings) == 0 || len(v3) != 4 {
		t.Fatalf("stale: %v %+v", err, m3)
	}
	// No cache + down -> error.
	c3 := New(Options{Endpoints: &ep, Now: func() time.Time { return now }})
	if _, _, err := c3.StatsVaults(ctx); err == nil {
		t.Fatal("expected error")
	}
}

func TestVaultDetail(t *testing.T) {
	f := newFakeHL(t)
	c := f.client(t)
	ctx := context.Background()
	d, err := c.Vault(ctx, addrHLP)
	if err != nil {
		t.Fatal(err)
	}
	s := d.Summary
	if s.Name != "Hyperliquidity Provider (HLP)" || s.Rel != "parent" || len(d.Children) != 7 || d.LockupDays != HLPLockupDays {
		t.Fatalf("summary %+v children %d", s, len(d.Children))
	}
	if !near(s.TVL, 151318595.7991909981, 1e-3) || d.CapacityUSD != -1 || s.SPModel != domain.SPHL {
		t.Fatalf("tvl/capacity %v %v", s.TVL, d.CapacityUSD)
	}
	if len(d.NAV) != 3 || d.NAV[0].V != 1 || len(d.Equity) != 3 || len(d.PnL) != 3 {
		t.Fatalf("curves %+v", d.NAV)
	}
	// Flow-neutral modified Dietz: r1 = ΔP / (A0 + F/2), F = ΔA - ΔP.
	dp := 140712776.9123339951 - 138942028.3325499892
	a0, a1 := d.Equity[0].V, d.Equity[1].V
	if !near(a0, 174263419.7225500047, 1e-3) {
		t.Fatalf("a0 %v", a0)
	}
	r1 := dp / (a0 + ((a1-a0)-dp)/2)
	if !near(d.NAV[1].V, 1+r1, 1e-12) {
		t.Fatalf("nav1 %v want %v", d.NAV[1].V, 1+r1)
	}
	if _, ok := s.Returns[domain.W1D]; !ok {
		t.Fatal("1d return")
	}
	if d.Account == nil || len(d.Composition) != 1 || len(d.Followers) != 2 || d.Followers[0].DaysFollowing != 693 {
		t.Fatalf("account/followers %+v", d)
	}
	if d.Meta.Source != "hyperliquid" || d.Meta.Partial {
		t.Fatalf("meta %+v", d.Meta)
	}
	x, err := c.Vault(ctx, addrStrategyX)
	if err != nil || x.Summary.Rel != "child" || x.Summary.LeaderFraction != 1 || x.LockupDays != UserLockupDays {
		t.Fatalf("child %v %+v", err, x.Summary)
	}
	if _, err := c.Vault(ctx, "0x0000000000000000000000000000000000000002"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-vault: %v", err)
	}
}

func TestCapacityAndCurves(t *testing.T) {
	if Capacity(1e6, 0.10, false) != 1e6 || Capacity(1e6, 0.04, false) != 0 || Capacity(1e6, 0.5, true) != -1 {
		t.Fatal("capacity")
	}
	if Capacity(1e6, 0.0495, false) != 0 {
		t.Fatal("capacity clamps at 0")
	}
	// Interval 1 earns 10 while 100 is deposited: the flow is 100% of the
	// starting equity, so the interval is unmeasurable and counts as 0
	// (simple Dietz credited +10% on the old base). Interval 2 has no flow:
	// -10.5 on 210 is -5%.
	av := domain.Series{{T: 1, V: 100}, {T: 2, V: 210}, {T: 3, V: 199.5}}
	pnl := domain.Series{{T: 1, V: 0}, {T: 2, V: 10}, {T: 3, V: -0.5}}
	c := TWRCurve(av, pnl)
	if !near(c[1].V, 1, 1e-12) || !near(c[2].V, 1-10.5/210, 1e-12) {
		t.Fatalf("%+v", c)
	}
	if !near(maxDrawdown(c), -10.5/210, 1e-12) {
		t.Fatal(maxDrawdown(c))
	}
	// A 20% deposit is measurable: 10 earned on 100 + 20/2 = 110.
	c = TWRCurve(domain.Series{{T: 1, V: 100}, {T: 2, V: 130}}, domain.Series{{T: 1, V: 0}, {T: 2, V: 10}})
	if !near(c[1].V, 1+10.0/110, 1e-12) {
		t.Fatalf("modified Dietz %+v", c)
	}
	if TWRCurve(nil, nil) != nil {
		t.Fatal("empty")
	}
}

func TestUserRoleCached(t *testing.T) {
	f := newFakeHL(t)
	c := f.client(t)
	for i := 0; i < 3; i++ {
		r, err := c.UserRole(context.Background(), addrHLP)
		if err != nil || r != "vault" {
			t.Fatal(err, r)
		}
	}
	if f.count("userRole") != 1 {
		t.Fatal("not cached")
	}
	eq, err := c.UserVaultEquities(context.Background(), addrHLP)
	if err != nil || len(eq) != 1 {
		t.Fatal(err)
	}
	p, err := c.Portfolio(context.Background(), addrTrader)
	if err != nil || len(p) == 0 {
		t.Fatal(err)
	}
}

// --- Live smoke tests (HLP_LIVE=1). Read-only, a handful of requests. ---

func liveClient(t *testing.T) *Client {
	if os.Getenv("HLP_LIVE") != "1" {
		t.Skip("set HLP_LIVE=1 to run live tests")
	}
	return New(Options{UserAgent: "hlp-cli/test"})
}

func TestLiveRegistry(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Registry().Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	for in, id := range map[string]int{"HYPE": 159, "xyz:SP500": 110052, "BTC": 0, "HYPE/USDC": 10107} {
		m, err := c.Registry().Resolve(in)
		if err != nil || m.AssetID != id {
			t.Errorf("%s: %+v %v", in, m, err)
		}
	}
	if m, err := c.Registry().Resolve("SPY"); err != nil || m.Coin != "xyz:SP500" {
		t.Errorf("SPY: %+v %v", m, err)
	}
}

func TestLiveCandlesAndVault(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cs, err := c.Candles(ctx, "BTC", domain.I1h, 24, 0)
	if err != nil || len(cs) != 24 {
		t.Fatalf("%v %d", err, len(cs))
	}
	d, err := c.Vault(ctx, addrHLP)
	if err != nil || d.Summary.TVL < 1e6 {
		t.Fatalf("%v %+v", err, d.Summary)
	}
}
