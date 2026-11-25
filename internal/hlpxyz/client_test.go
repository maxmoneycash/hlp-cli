package hlpxyz

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// TestFixturesDecode decodes every hlp.xyz fixture into its wire type.
func TestFixturesDecode(t *testing.T) {
	type v6s struct {
		Statuses map[string]V6Status `json:"statuses"`
	}
	type sparks struct {
		Sparks map[string][][2]float64 `json:"sparks"`
	}
	type bulk struct {
		Vaults []LiveVault `json:"vaults"`
	}
	type idxList struct {
		Indexes []Index `json:"indexes"`
	}
	type curves struct {
		Curves     map[string][][2]float64 `json:"curves"`
		Incomplete bool                    `json:"incomplete"`
	}
	type hist struct {
		Samples []PVaultSample `json:"samples"`
	}
	type bench struct {
		Prices []struct {
			Date  string  `json:"date"`
			Price float64 `json:"price"`
		} `json:"prices"`
	}
	cases := map[string]func() any{
		"vaults_summary.json":         func() any { return new(SummaryResponse) },
		"vaults_summary_trader.json":  func() any { return new(SummaryResponse) },
		"vaults_v6status.json":        func() any { return new(v6s) },
		"vaults_v5status.json":        func() any { return new(v6s) },
		"vaults_v6spark.json":         func() any { return new(sparks) },
		"vault_live.json":             func() any { return new(VaultLive) },
		"vault_live_hlp.json":         func() any { return new(VaultLive) },
		"vaults_live_bulk.json":       func() any { return new(bulk) },
		"vaults_live_trader.json":     func() any { return new(bulk) },
		"vault_detail_enc_t1.json":    func() any { return new(VaultDetail) },
		"vault_detail_light_lev.json": func() any { return new(VaultDetail) },
		"trader_detail_light.json":    func() any { return new(VaultDetail) },
		"vault_detail_untracked.json": func() any { return new(VaultDetail) },
		"vault_composition_1m.json":   func() any { return new(Composition) },
		"user_v6vault.json":           func() any { return new(NAVCurve) },
		"user_v5vault.json":           func() any { return new(NAVCurve) },
		"user_v4vault.json":           func() any { return new(NAVCurve) },
		"user_v3vault.json":           func() any { return new(NAVCurve) },
		"user_v2vault.json":           func() any { return new(NAVCurve) },
		"idx_list.json":               func() any { return new(idxList) },
		"idx_get_mavericks.json":      func() any { return new(Index) },
		"idx_curves.json":             func() any { return new(curves) },
		"idx_cap.json":                func() any { return new(map[string]any) },
		"idx_lev.json":                func() any { return new(map[string]any) },
		"idx_trades.json":             func() any { return new(map[string]any) },
		"idx_tracked.json":            func() any { return new(map[string]any) },
		"idx_depclose.json":           func() any { return new(map[string]any) },
		"pvault_status.json":          func() any { return new(PVaultStatus) },
		"pvault_history.json":         func() any { return new(hist) },
		"quant_live_spxmeme.json":     func() any { return new(QuantLive) },
		"quant_live_trend.json":       func() any { return new(QuantLive) },
		"quant_spxmeme_static.json":   func() any { return new(QuantStatic) },
		"quant_trend_static.json":     func() any { return new(QuantStatic) },
		"benchmark_spy.json":          func() any { return new(bench) },
		"errors.json":                 func() any { return new(map[string]struct{ Error string }) },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			dec := json.NewDecoder(bytes.NewReader(readFixture(t, name)))
			v := mk()
			if err := dec.Decode(v); err != nil {
				t.Fatalf("decode: %v", err)
			}
		})
	}

	// Spot checks on decoded values.
	var st v6s
	json.Unmarshal(readFixture(t, "vaults_v6status.json"), &st)
	if s := st.Statuses[addrGarbage]; s.NavNow < 8.7 || s.Ret["all"] < 600 || s.WindowAccuracy["6m"] == nil || s.WindowAccuracy["1d"] != nil {
		t.Errorf("v6status decoded wrong: %+v", s)
	}
	var light VaultDetail
	json.Unmarshal(readFixture(t, "vault_detail_light_lev.json"), &light)
	if !light.Light || len(light.Leverage) != 3 || light.Trades == nil || light.Trades.N != 0 {
		t.Errorf("light detail: light=%v lev=%d trades=%+v", light.Light, len(light.Leverage), light.Trades)
	}
	var ps PVaultStatus
	json.Unmarshal(readFixture(t, "pvault_status.json"), &ps)
	if ps.LastKeeperRun.State.Phase != "OPEN" || len(ps.LastBotRun.Book) != 4 || !ps.LastBotRun.Agent.Usable {
		t.Errorf("pvault status: %+v", ps)
	}
	var errs map[string]json.RawMessage
	json.Unmarshal(readFixture(t, "errors.json"), &errs)
	for k, b := range errs {
		var ae *APIError
		if err := bodyError(b, "/x"); !errors.As(err, &ae) || ae.Message == "" {
			t.Errorf("errors.json %s not recognized as an API error: %v", k, err)
		}
	}
}

// TestWebsiteParity: perfSeries[-1].sharePrice / baseSP<W> - 1 reproduces
// the website's percent returns (as fractions).
func TestWebsiteParity(t *testing.T) {
	var r SummaryResponse
	if err := json.Unmarshal(readFixture(t, "vaults_summary.json"), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Vaults) == 0 {
		t.Fatal("no vaults")
	}
	for _, v := range r.Vaults {
		ret := SummaryReturns(v)
		if got, want := ret[domain.WYTD], v.ReturnYtd/100; math.Abs(got-want) > 1e-6 {
			t.Errorf("%s ytd %v, want %v", v.Name, got, want)
		}
		if got, want := ret[domain.WAll], v.ReturnAll/100; math.Abs(got-want) > 1e-6 {
			t.Errorf("%s all %v, want %v", v.Name, got, want)
		}
	}
}

func TestNormalizeAddress(t *testing.T) {
	if a, err := NormalizeAddress("  0xAC26CF5F3C46B5E102048C65B977D2551B72A9C7 "); err != nil || a != addrGarbage {
		t.Fatalf("got %q %v", a, err)
	}
	for _, bad := range []string{"", "0x123", "ac26cf5f3c46b5e102048c65b977d2551b72a9c7", "0xzz26cf5f3c46b5e102048c65b977d2551b72a9c7", "HLP"} {
		if _, err := NormalizeAddress(bad); !errors.Is(err, ErrBadAddress) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestVaultList(t *testing.T) {
	f := newFakeServer(t)
	// Add a fourth, long-dead row with no v6 metrics to exercise staleness.
	var sum map[string]json.RawMessage
	json.Unmarshal(readFixture(t, "vaults_summary.json"), &sum)
	var rows []map[string]any
	json.Unmarshal(sum["vaults"], &rows)
	old := map[string]any{}
	for k, v := range rows[0] {
		old[k] = v
	}
	old["address"] = "0x1111111111111111111111111111111111111111"
	old["name"] = "Old Vault"
	old["lastUpdated"] = fixtureNow.Add(-72 * time.Hour).UnixMilli()
	old["equity"] = 5.0
	rows = append(rows, old)
	body, _ := json.Marshal(map[string]any{"count": len(rows), "vaults": rows})
	f.set("/api/vaults?summary", func(w http.ResponseWriter, r *http.Request) { w.Write(body) })

	c := f.client()
	got, meta, err := c.VaultList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if meta.Source != "hlp.xyz" || meta.Partial || meta.Stale || meta.AsOf.IsZero() {
		t.Errorf("meta = %+v", meta)
	}
	if len(got) != 4 {
		t.Fatalf("%d rows", len(got))
	}
	by := map[string]domain.VaultSummary{}
	for i, r := range got {
		by[r.Address] = r
		if i > 0 && got[i-1].TVL < r.TVL {
			t.Error("rows not sorted by TVL desc")
		}
		if !r.Curated || r.Kind != domain.VaultHyperCore || len(r.Sources) != 1 || r.Sources[0] != "hlp.xyz" {
			t.Errorf("%s: curated/kind/sources = %v %v %v", r.Name, r.Curated, r.Kind, r.Sources)
		}
	}

	hlp := by[HLPAddress]
	if hlp.TVL != 151318485.70641 {
		t.Errorf("HLP TVL = %v, want 151.3M from /live (not 358M summary)", hlp.TVL)
	}
	if math.Abs(hlp.Returns[domain.WAll]-1.5828660689832944) > 1e-12 || math.Abs(hlp.MaxDD-(-0.05768302282132077)) > 1e-12 {
		t.Errorf("HLP returns/maxDD not fractions from v6status: %v %v", hlp.Returns[domain.WAll], hlp.MaxDD)
	}
	if hlp.Rel != "parent" || hlp.SPModel != domain.SPv6 || hlp.AccuracyPct != 0.058 || hlp.Stale {
		t.Errorf("HLP row = %+v", hlp)
	}
	if hlp.Sharpe != 7.171523811378771 || len(hlp.Spark) != 6 {
		t.Errorf("HLP sharpe/spark = %v/%d", hlp.Sharpe, len(hlp.Spark))
	}

	g := by[addrGarbage]
	if g.TVL != 2783296.090273 || math.Abs(g.Returns[domain.WYTD]-4.3022925138456986) > 1e-12 || g.Returns[domain.W1D] > 0.01 {
		t.Errorf("garbage row = %+v", g)
	}
	if g.AgeDays < 580 || g.CreatedAt.IsZero() || g.MaxGrossLev == 0 {
		t.Errorf("garbage age/created/lev = %d %v %v", g.AgeDays, g.CreatedAt, g.MaxGrossLev)
	}

	o := by["0x1111111111111111111111111111111111111111"]
	if !o.Stale || o.SPModel != domain.SPSummary || o.TVL != 5 {
		t.Errorf("old row = %+v, want stale summary-only row", o)
	}
	if math.Abs(o.Returns[domain.WYTD]-rows[0]["returnYtd"].(float64)/100) > 1e-6 {
		t.Errorf("summary fallback returns = %v", o.Returns)
	}

	// Cached: a second call within 60 s makes no requests.
	before := f.count("/api/vaults?summary")
	if _, _, err := c.VaultList(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.count("/api/vaults?summary") != before {
		t.Error("second VaultList hit the network")
	}
}

func TestVaultListPartial(t *testing.T) {
	f := newFakeServer(t)
	f.set("/api/vaults?v6spark", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	})
	rows, meta, err := f.client().VaultList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !meta.Partial || len(meta.Warnings) != 1 || !strings.Contains(meta.Warnings[0], "v6spark") {
		t.Fatalf("meta = %+v", meta)
	}
	for _, r := range rows {
		if len(r.Spark) == 0 {
			t.Errorf("%s: no fallback spark from perfSeries", r.Name)
		}
	}
	if f.count("/api/vaults?v6spark") != 1+maxRetries {
		t.Errorf("v6spark tried %d times, want %d", f.count("/api/vaults?v6spark"), 1+maxRetries)
	}

	// Bulk /live slower than the budget: summary TVL with a warning, but
	// HLP still gets its live TVL from the single /live call.
	f.set("/api/vaults?v6spark", nil)
	f.set("/api/vaults?live", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	start := time.Now()
	rows, meta, err = f.client(func(o *Options) { o.LiveBudget = 50 * time.Millisecond }).VaultList(context.Background())
	if err != nil || !meta.Partial || !strings.Contains(strings.Join(meta.Warnings, ";"), "live TVL") {
		t.Fatalf("slow live: %v %+v", err, meta)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("VaultList waited %s for slow /live", time.Since(start))
	}
	for _, r := range rows {
		switch r.Address {
		case HLPAddress:
			if r.TVL != 151318485.70641 {
				t.Errorf("HLP TVL = %v, want live", r.TVL)
			}
		case addrGarbage:
			if r.TVL != 2689834.743324 {
				t.Errorf("garbage TVL = %v, want summary equity", r.TVL)
			}
		}
	}

	f.set("/api/vaults?summary", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"nope"}`, http.StatusBadRequest)
	})
	if _, _, err := f.client().VaultList(WithoutCache(context.Background())); err == nil {
		t.Fatal("summary failure must fail the list")
	}
}

func TestVaultDetailLoading(t *testing.T) {
	f := newFakeServer(t)
	var n atomic.Int32
	detail := readFixture(t, "vault_detail_enc_t1.json")
	f.set("/api/vault/"+addrGarbage, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("enc") != "t1" {
			t.Errorf("query = %s, want enc=t1", r.URL.RawQuery)
		}
		if n.Add(1) <= 2 {
			w.Write([]byte(`{"status":"loading"}`))
			return
		}
		w.Write(detail)
	})
	d, err := f.client().VaultDetail(context.Background(), addrGarbage, DetailOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if n.Load() != 3 || d.Trades == nil || d.Trades.N != 6 || d.VaultInfo.Name == "" {
		t.Fatalf("calls=%d detail=%+v", n.Load(), d.VaultInfo)
	}

	// Gives up after LoadingTries with ErrLoading; never caches "loading".
	f.set("/api/vault/"+addrBredo, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"status":"loading"}`)) })
	c := f.client(func(o *Options) { o.LoadingTries = 3 })
	if _, err := c.VaultDetail(context.Background(), addrBredo, DetailOpts{}); !errors.Is(err, ErrLoading) {
		t.Fatalf("err = %v, want ErrLoading", err)
	}
	if got := f.count("/api/vault/" + addrBredo); got != 3 {
		t.Fatalf("tries = %d, want 3", got)
	}

	// Context cancellation stops the polling.
	ctx, cancel := context.WithCancel(context.Background())
	c = f.client(func(o *Options) { o.LoadingRetry = time.Hour })
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := c.VaultDetail(ctx, addrBredo, DetailOpts{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestVaultDetailVariants(t *testing.T) {
	f := newFakeServer(t)
	c := f.client()
	d, err := c.VaultDetail(context.Background(), addrGarbage, DetailOpts{Light: true, Lev: true, Trader: true})
	if err != nil {
		t.Fatal(err)
	}
	q := f.last().URL.Query()
	if q.Get("light") != "1" || q.Get("lev") != "1" || q.Get("kind") != "trader" || q.Get("enc") != "" {
		t.Errorf("query = %v", q)
	}
	if !d.Light || len(d.Deps) != 3 {
		t.Errorf("light detail = %+v", d.Meta)
	}

	f.set("/api/vault/"+addrBredo, func(w http.ResponseWriter, r *http.Request) { w.Write(readFixture(t, "vault_detail_untracked.json")) })
	if _, err := c.VaultDetail(context.Background(), addrBredo, DetailOpts{}); !errors.Is(err, ErrNoCache) {
		t.Fatalf("err = %v, want ErrNoCache", err)
	}
	if _, err := c.VaultDetail(context.Background(), "HLP", DetailOpts{}); !errors.Is(err, ErrBadAddress) {
		t.Fatalf("err = %v, want ErrBadAddress", err)
	}
}

func TestVaultLive(t *testing.T) {
	f := newFakeServer(t)
	c := f.client()
	if _, err := c.VaultLive(context.Background(), "x"+HLPAddress[1:]); !errors.Is(err, ErrBadAddress) {
		t.Fatalf("malformed address: %v", err)
	}
	v, err := c.VaultLive(context.Background(), "0x"+strings.ToUpper(HLPAddress[2:]))
	if err != nil || v.TotalEquity != 151318485.70641 || v.LockupDays != 4 {
		t.Fatalf("HLP live = %+v %v", v, err)
	}
	// {"error":"Vault not found"} with 200 or 404 -> ErrNotAVault.
	notFound := []byte(`{"error":"Vault not found"}`)
	f.set("/api/vault/"+addrBredo+"/live", func(w http.ResponseWriter, r *http.Request) { w.Write(notFound) })
	if _, err := c.VaultLive(context.Background(), addrBredo); !errors.Is(err, ErrNotAVault) {
		t.Fatalf("err = %v, want ErrNotAVault", err)
	}
	f.set("/api/vault/"+addrGarbage+"/live", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write(notFound)
	})
	if _, err := c.VaultLive(context.Background(), addrGarbage); !errors.Is(err, ErrNotAVault) {
		t.Fatalf("err = %v, want ErrNotAVault", err)
	}
}

func TestLiveChunks(t *testing.T) {
	f := newFakeServer(t)
	var mu sync.Mutex
	var sizes []int
	f.set("/api/vaults?live", func(w http.ResponseWriter, r *http.Request) {
		addrs := strings.Split(r.URL.Query().Get("addresses"), ",")
		mu.Lock()
		sizes = append(sizes, len(addrs))
		mu.Unlock()
		var out []LiveVault
		for _, a := range addrs {
			out = append(out, LiveVault{Address: strings.ToUpper(a[:2]) + a[2:], TotalEquity: 1})
		}
		json.NewEncoder(w).Encode(map[string]any{"vaults": out})
	})
	var addrs []string
	for i := 0; i < 120; i++ {
		addrs = append(addrs, "0x"+strings.Repeat("0", 37)+string(rune('a'+i/100))+string(rune('0'+(i/10)%10))+string(rune('0'+i%10)))
	}
	addrs = append(addrs, addrs[0]) // duplicate
	got, err := f.client().Live(context.Background(), addrs)
	if err != nil {
		t.Fatal(err)
	}
	sort.Ints(sizes)
	if len(got) != 120 || len(sizes) != 3 || sizes[0] != 20 || sizes[1] != 50 || sizes[2] != 50 {
		t.Fatalf("got %d rows in chunks %v", len(got), sizes)
	}
	if got[0].Address != strings.ToLower(got[0].Address) {
		t.Error("addresses not lowercased")
	}
	if _, err := f.client().Live(context.Background(), []string{"bad"}); !errors.Is(err, ErrBadAddress) {
		t.Fatalf("err = %v", err)
	}
}

func TestBenchmark(t *testing.T) {
	f := newFakeServer(t)
	c := f.client()
	start := time.Date(2026, 10, 15, 13, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	s, err := c.Benchmark(context.Background(), "spy", start, end)
	if err != nil {
		t.Fatal(err)
	}
	q := f.last().URL.Query()
	if q.Get("symbol") != "SPY" || q.Get("start") != "2026-10-15" || q.Get("end") != "2026-10-20" || q.Get("interval") != "day" {
		t.Fatalf("query = %v", q)
	}
	if len(s) != 3 || s[0].T != time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC).UnixMilli() || s[1].V != 763.25 {
		t.Fatalf("series = %+v", s)
	}
	if _, err := c.Benchmark(context.Background(), "DOGE", start, end); err == nil {
		t.Fatal("unknown symbol accepted")
	}
	if _, err := c.Benchmark(context.Background(), "BTC", end, start); err == nil {
		t.Fatal("reversed range accepted")
	}
}

func TestCompositionNAV(t *testing.T) {
	f := newFakeServer(t)
	c := f.client()
	comp, err := c.Composition(context.Background(), addrGarbage, "")
	if err != nil || len(comp.Points) != 3 || comp.Points[0].V["HYPE"] < 2e6 {
		t.Fatalf("composition = %+v %v", comp, err)
	}
	if got := f.last().URL.Query().Get("range"); got != "1m" {
		t.Errorf("range = %q", got)
	}
	if _, err := c.Composition(context.Background(), addrGarbage, "5y"); err == nil {
		t.Error("bad range accepted")
	}
	nav, err := c.NAV(context.Background(), addrGarbage)
	if err != nil || nav.NavNow < 8.8 || len(nav.Curve) != 6 || nav.WindowReturns["allTime"] < 7 {
		t.Fatalf("nav = %+v %v", nav, err)
	}
	if q := f.last().URL.Query(); q.Get("action") != "v6vault" || q.Get("address") != addrGarbage {
		t.Errorf("query = %v", q)
	}
}

func TestIndexesFundsQuant(t *testing.T) {
	f := newFakeServer(t)
	c := f.client()
	ctx := context.Background()
	idx, err := c.Indexes(ctx, true)
	if err != nil || len(idx) != 1 || idx[0].Def.Slug != "mavericks" || f.last().URL.Query().Get("all") != "1" {
		t.Fatalf("indexes = %+v %v", idx, err)
	}
	m, err := c.Index(ctx, "Mavericks")
	if err != nil || m.Def.Name != "Mavericks" || len(m.Allocs) == 0 || f.last().URL.Query().Get("slug") != "mavericks" {
		t.Fatalf("index = %+v %v", m.Def, err)
	}
	if _, err := c.Index(ctx, "../etc"); err == nil {
		t.Error("bad slug accepted")
	}
	st, err := c.PVaultStatus(ctx)
	if err != nil || st.ChainID != 998 || st.LastKeeperRun.State.SharePrice < 1 {
		t.Fatalf("pvault status = %+v %v", st, err)
	}
	h, err := c.PVaultHistory(ctx, 998, addrPVault)
	if err != nil || len(h) != 6 || h[0].TSec != 1794476056 || h[5].SharePrice != 1.028644 || h[5].SupplyShares <= 0 {
		t.Fatalf("pvault history = %+v %v", h, err)
	}
	if q := f.last().URL.Query(); q.Get("chain") != "998" || q.Get("vault") != addrPVault {
		t.Errorf("history query = %v (vault must keep its checksum case)", q)
	}
	if _, err := c.PVaultHistory(ctx, 0, addrPVault); err == nil {
		t.Error("chain 0 accepted")
	}
	q, err := c.Quant(ctx, "spxmeme")
	if err != nil || q.Gross != 1.95 || len(q.Positions) != 4 || q.Positions[0].Weight != -0.075 {
		t.Fatalf("quant = %+v %v", q, err)
	}
	qs, err := c.QuantBacktest(ctx, "spxmeme")
	if err != nil || qs.Name != "Long SPY, Short Memecoins" || len(qs.Periods) != 3 {
		t.Fatalf("quant static = %+v %v", qs, err)
	}
	if _, err := c.Quant(ctx, "a/b"); err == nil {
		t.Error("bad id accepted")
	}
}

func TestSummaryTraders(t *testing.T) {
	f := newFakeServer(t)
	r, err := f.client().Summary(context.Background(), true)
	if err != nil || len(r.Vaults) == 0 || f.last().URL.Query().Get("kind") != "trader" {
		t.Fatalf("summary traders = %d %v", len(r.Vaults), err)
	}
	sp, err := f.client().V6Spark(context.Background())
	if err != nil || len(sp[HLPAddress]) != 6 || sp[HLPAddress][0].T != 1683758700034 {
		t.Fatalf("spark = %+v %v", sp[HLPAddress], err)
	}
	st, err := f.client().V6Status(context.Background())
	if err != nil || st[addrBredo].AnchorCount != 7 {
		t.Fatalf("status = %v", err)
	}
}

func TestTransportHeadersGzipAndCache(t *testing.T) {
	f := newFakeServer(t)
	f.set("/api/vaults?v6status", func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "hlp-cli/") {
			t.Errorf("User-Agent = %q", ua)
		}
		if r.Header.Get("Accept-Encoding") != "gzip" {
			t.Errorf("Accept-Encoding = %q", r.Header.Get("Accept-Encoding"))
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("Authorization header sent")
		}
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		gz.Write(readFixture(t, "vaults_v6status.json"))
		gz.Close()
	})
	now := fixtureNow
	dir := t.TempDir()
	mk := func() *Client {
		return f.client(func(o *Options) { o.CacheDir = dir; o.Now = func() time.Time { return now } })
	}
	c := mk()
	ctx := context.Background()
	if st, err := c.V6Status(ctx); err != nil || st[HLPAddress].NavNow == 0 {
		t.Fatalf("gzip decode: %v", err)
	}
	if _, err := c.V6Status(ctx); err != nil || f.count("/api/vaults?v6status") != 1 {
		t.Fatalf("memory cache miss: hits=%d err=%v", f.count("/api/vaults?v6status"), err)
	}
	// A fresh client reads the disk cache.
	if _, err := mk().V6Status(ctx); err != nil || f.count("/api/vaults?v6status") != 1 {
		t.Fatalf("disk cache miss: hits=%d err=%v", f.count("/api/vaults?v6status"), err)
	}
	// WithoutCache forces a refresh.
	if _, err := c.V6Status(WithoutCache(ctx)); err != nil || f.count("/api/vaults?v6status") != 2 {
		t.Fatalf("WithoutCache: hits=%d", f.count("/api/vaults?v6status"))
	}
	// After the 60 s TTL the next call refetches.
	now = now.Add(61 * time.Second)
	if _, err := c.V6Status(ctx); err != nil || f.count("/api/vaults?v6status") != 3 {
		t.Fatalf("TTL: hits=%d", f.count("/api/vaults?v6status"))
	}
	// Network failure after expiry serves the stale copy (VaultList marks it).
	now = now.Add(10 * time.Minute)
	f.set("/api/vaults?v6status", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", 503) })
	body, info, err := c.fetch(ctx, vaultsReq("v6status", ttlList))
	if err != nil || !info.Stale || len(body) == 0 {
		t.Fatalf("stale fallback: %v %+v", err, info)
	}
}

func TestRateLimit429(t *testing.T) {
	f := newFakeServer(t)
	var n atomic.Int32
	f.set("/api/quant?trend", func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("X-Ratelimit-Reset", "0.05")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write(readFixture(t, "quant_live_trend.json"))
	})
	start := time.Now()
	q, err := f.client().Quant(context.Background(), "trend")
	if err != nil || q.ID != "trend" || n.Load() != 2 {
		t.Fatalf("quant after 429 = %+v %v (calls %d)", q.ID, err, n.Load())
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Error("did not wait for x-ratelimit-reset")
	}

	// A reset beyond the deadline fails fast with ErrRateLimited.
	f.set("/api/quant?spxmeme", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Ratelimit-Reset", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := f.client().Quant(ctx, "spxmeme"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

func TestResetDelay(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	h := http.Header{}
	h.Set("X-Ratelimit-Reset", "12")
	if d := resetDelay(h, now); d != 12*time.Second {
		t.Errorf("relative = %v", d)
	}
	h.Set("X-Ratelimit-Reset", "1800000005")
	if d := resetDelay(h, now); d != 5*time.Second {
		t.Errorf("absolute = %v", d)
	}
	h = http.Header{}
	h.Set("Retry-After", "3")
	if d := resetDelay(h, now); d != 3*time.Second {
		t.Errorf("retry-after = %v", d)
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter(600) // 10/s, burst 100
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		if err := l.wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	if err := l.wait(ctx); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 50*time.Millisecond {
		t.Fatalf("101st token after %v, want ~100ms", d)
	}
	// Default budget: burst + one minute of refill stays under 120.
	d := newLimiter(90)
	if d.capacity+d.rate*60 >= 120 {
		t.Fatalf("burst %v + 60s refill exceeds hlp.xyz's 120/window", d.capacity)
	}
	short, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	l.block(time.Now().Add(time.Hour))
	if err := l.wait(short); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("blocked wait = %v", err)
	}
}

func TestForbiddenEndpoints(t *testing.T) {
	f := newFakeServer(t)
	c := f.client()
	for _, r := range []request{
		{path: "/api/auth"},
		{path: "/api/user", query: map[string][]string{"action": {"record"}}},
		{path: "/api/user", query: map[string][]string{"action": {"portfolio"}}},
		{path: "/api/user", query: map[string][]string{"action": {"manageSave"}}},
		{path: "/api/vaults", query: map[string][]string{"action": {"idxInvStart"}}},
		{path: "/api/vaults", query: map[string][]string{"action": {"idxMine"}}},
		{path: "/api/pvault", query: map[string][]string{"action": {"notify"}}},
		{path: "/api/quant", query: map[string][]string{"action": {"real-run"}}},
		{path: "/api/casa", query: map[string][]string{"action": {"summary"}}},
	} {
		if _, _, err := c.fetch(context.Background(), r); !errors.Is(err, ErrForbiddenEndpoint) {
			t.Errorf("%s %v: err = %v", r.path, r.query, err)
		}
	}
	if len(f.requests) != 0 {
		t.Fatalf("forbidden requests reached the server: %d", len(f.requests))
	}
}

func TestAPIErrorStatus(t *testing.T) {
	f := newFakeServer(t)
	f.set("/api/vaults?idxGet", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Unknown index action"}`))
	})
	_, err := f.client().Index(context.Background(), "nope")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 400 || ae.Message != "Unknown index action" {
		t.Fatalf("err = %#v", err)
	}
	if f.count("/api/vaults?idxGet") != 1 {
		t.Error("4xx must not be retried")
	}
	if !strings.Contains(err.Error(), "Unknown index action") {
		t.Error(err.Error())
	}
}

func TestCachePrune(t *testing.T) {
	dir := t.TempDir()
	c := &cache{mem: map[string]cacheEntry{}, dir: dir}
	now := time.Now()
	old := dir + "/old.json"
	tmp := dir + "/x.123.tmp"
	for _, p := range []string{old, tmp} {
		if err := os.WriteFile(p, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	os.Chtimes(old, now.Add(-48*time.Hour), now.Add(-48*time.Hour))
	c.put("fresh", cacheEntry{FetchedAt: now.UnixMilli(), Body: json.RawMessage(`{"a":1}`)}, now)
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != "fresh.json" {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("after prune: %v", names)
	}
	// A fresh cache instance reads it back from disk.
	c2 := &cache{mem: map[string]cacheEntry{}, dir: dir}
	if e, ok := c2.get("fresh"); !ok || string(e.Body) != `{"a":1}` {
		t.Fatalf("disk read: %v %s", ok, e.Body)
	}
}
