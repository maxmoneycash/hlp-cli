package hlpxyz

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
)

func TestDecodeT1Fixture(t *testing.T) {
	var d VaultDetail
	if err := json.Unmarshal(readFixture(t, "vault_detail_enc_t1.json"), &d); err != nil {
		t.Fatal(err)
	}
	fills, err := DecodeT1(*d.Trades)
	if err != nil {
		t.Fatal(err)
	}
	if len(fills) != 6 {
		t.Fatalf("%d fills, want 6", len(fills))
	}
	const t0 = 1744227565906
	want := []domain.Fill{
		{Coin: "HYPE", Side: domain.Buy, Dir: "Open Long", Px: 13.777, Sz: 7.26, Time: t0, StartPos: 0, Fee: 0.035007},
		{Coin: "HYPE", Side: domain.Buy, Dir: "Open Long", Px: 13.777, Sz: 58.81, Time: t0, StartPos: 7.26, Fee: 0.283578},
		{Coin: "HYPE", Side: domain.Buy, Dir: "Open Long", Px: 13.777, Sz: 14.52, Time: t0, StartPos: 66.07, Fee: 0.070014},
		{Coin: "HYPE", Side: domain.Buy, Dir: "Open Long", Px: 13.777, Sz: 616.24, Time: t0, StartPos: 109.63, Fee: 2.971478},
		{Coin: "ETH", Side: domain.Sell, Dir: "Open Short", Px: 1639.3, Sz: 1.4341, Time: t0 + 11811, StartPos: 0, Fee: 0.822822},
		{Coin: "ETH", Side: domain.Sell, Dir: "Open Short", Px: 1639.3, Sz: 0.2733, Time: t0 + 11811, StartPos: -1.4341, Fee: 0.156807},
	}
	for i, f := range fills {
		if f != want[i] {
			t.Errorf("fill %d = %+v\nwant     %+v", i, f, want[i])
		}
		if i > 0 && f.Time < fills[i-1].Time {
			t.Errorf("fill %d goes back in time", i)
		}
	}
}

func i64(v int64) *int64 { return &v }

func TestLiveOptionalPermissionAndIndependentLeverageAge(t *testing.T) {
	var live VaultLive
	if err := json.Unmarshal([]byte(`{"sharePrice":1,"totalEquity":100}`), &live); err != nil {
		t.Fatal(err)
	}
	detail := VaultDetail{}
	detail.Meta.AllowDeposits = true
	detail.Meta.LastUpdated = fixtureNow.UnixMilli()
	old := fixtureNow.Add(-72 * time.Hour).UnixMilli()
	detail.Leverage = []LeverageRow{{TS: old, Leverage: 2, AccountValue: 100}}
	d, err := ToDetail(DetailParts{Address: addrGarbage, Detail: &detail, Live: &live, Now: fixtureNow})
	if err != nil || !d.Summary.AllowDeposits || d.Freshness["leverage"].AsOf != old || !d.Freshness["leverage"].Stale || d.Freshness["history"].Stale {
		t.Fatalf("missing permission or source age conflated: %+v %v", d, err)
	}
	if err := json.Unmarshal([]byte(`{"allowDeposits":false}`), &live); err != nil {
		t.Fatal(err)
	}
	d, err = ToDetail(DetailParts{Address: addrGarbage, Detail: &detail, Live: &live, Now: fixtureNow})
	if err != nil || d.Summary.AllowDeposits {
		t.Fatalf("explicit paused state lost: %+v %v", d.Summary, err)
	}
}

func TestDecodeT1RunningSums(t *testing.T) {
	tr := TradesT1{
		W: "t1", N: 5,
		Coins: []string{"BTC", "ETH"},
		Dirs:  []string{"Open Long", "Close Long", "Auto-Deleveraging", "Liquidated Cross Long", "Short > Long"},
		T0:    1000,
		TS:    []int64{5, 0, 10, 1, 4},
		C:     []int{0, 1, 0, 1, 0},
		D:     []int{0, 1, 2, 3, 4},
		TID:   []*int64{i64(100), i64(5), nil, i64(-2), i64(7)},
		OID:   []*int64{nil, i64(50), i64(1), nil, i64(10)},
		Px:    []float64{1, 2, 3, 4, 5},
		Sz:    []float64{1, 1, 1, 1, 1},
		SP:    []float64{0, 3, -2, 1, -1},
		Fee:   []float64{0.1, 0.1, 0.1, 0.1, 0.1},
		PnL:   []float64{0, 1.5, -2, -3, 0},
	}
	fills, err := DecodeT1(tr)
	if err != nil {
		t.Fatal(err)
	}
	// Hand-computed: times 1005,1005,1015,1016,1020; tid 100,105,0(null),103,110;
	// oid 0(null),50,51,0(null),61.
	type exp struct {
		time     int64
		tid, oid uint64
		side     domain.Side
		liq      bool
		coin     string
	}
	want := []exp{
		{1005, 100, 0, domain.Buy, false, "BTC"},
		{1005, 105, 50, domain.Sell, false, "ETH"},
		{1015, 0, 51, domain.Buy, false, "BTC"}, // ADL of a short (sp<0) buys
		{1016, 103, 0, domain.Sell, true, "ETH"},
		{1020, 110, 61, domain.Buy, false, "BTC"},
	}
	for i, w := range want {
		f := fills[i]
		if f.Time != w.time || f.TID != w.tid || f.OID != w.oid || f.Side != w.side || f.Liq != w.liq || f.Coin != w.coin {
			t.Errorf("fill %d = %+v, want %+v", i, f, w)
		}
	}
	if fills[1].ClosedPnL != 1.5 {
		t.Error("closed pnl lost")
	}

	bad := tr
	bad.Px = bad.Px[:4]
	if _, err := DecodeT1(bad); err == nil {
		t.Error("short column accepted")
	}
	bad = tr
	bad.C = []int{0, 1, 9, 0, 0}
	if _, err := DecodeT1(bad); err == nil {
		t.Error("coin index out of range accepted")
	}
	bad = tr
	bad.N = 7
	if _, err := DecodeT1(bad); err == nil {
		t.Error("n mismatch accepted")
	}
	if f, err := DecodeT1(TradesT1{}); err != nil || f != nil {
		t.Errorf("empty = %v %v", f, err)
	}
}

func TestTradesRowsForm(t *testing.T) {
	// Non-enc responses carry row objects; numbers as strings or numbers.
	b := []byte(`[
	  {"_ts": 2000, "coin":"ETH","dir":"Close Long","px":"2000.5","sz":"0.5","startPosition":"1","fee":"0.2","closedPnl":"12.5","tid":900,"oid":40},
	  {"time": 1000, "coin":"BTC","dir":"Open Long","px":60000,"sz":0.01,"startPosition":0,"fee":0.3,"closedPnl":0,"tid":800,"oid":null}
	]`)
	var tr TradesT1
	if err := json.Unmarshal(b, &tr); err != nil {
		t.Fatal(err)
	}
	fills, err := DecodeT1(tr)
	if err != nil {
		t.Fatal(err)
	}
	if len(fills) != 2 || fills[0].Coin != "BTC" || fills[0].Time != 1000 || fills[0].TID != 800 || fills[0].OID != 0 {
		t.Fatalf("fills[0] = %+v", fills[0])
	}
	if f := fills[1]; f.Px != 2000.5 || f.TID != 900 || f.OID != 40 || f.ClosedPnL != 12.5 || f.Side != domain.Sell {
		t.Fatalf("fills[1] = %+v", f)
	}
	if err := json.Unmarshal([]byte(`[]`), &tr); err != nil || tr.N != 0 {
		t.Fatalf("empty array: %v %+v", err, tr)
	}
}

func loadParts(t *testing.T) DetailParts {
	t.Helper()
	var p DetailParts
	p.Address = "0x" + strings.ToUpper(addrGarbage[2:]) // any case is accepted
	var live VaultLive
	var nav NAVCurve
	var comp Composition
	var det VaultDetail
	for name, v := range map[string]any{
		"vault_live.json": &live, "user_v6vault.json": &nav,
		"vault_composition_1m.json": &comp, "vault_detail_enc_t1.json": &det,
	} {
		if err := json.Unmarshal(readFixture(t, name), v); err != nil {
			t.Fatal(name, err)
		}
	}
	var st struct {
		Statuses map[string]V6Status `json:"statuses"`
	}
	json.Unmarshal(readFixture(t, "vaults_v6status.json"), &st)
	s := st.Statuses[addrGarbage]
	p.Live, p.NAV, p.Composition, p.Detail, p.Status = &live, &nav, &comp, &det, &s
	p.Now = fixtureNow
	return p
}

func TestToDetail(t *testing.T) {
	p := loadParts(t)
	d, err := ToDetail(p)
	if err != nil {
		t.Fatal(err)
	}
	s := d.Summary
	if s.Address != addrGarbage || s.Name != "Long HYPE & BTC | Short Garbage" || s.Leader != "0xa380444a9299df2a15bddf949dd493bb6d30d939" {
		t.Errorf("identity = %+v", s)
	}
	if s.TVL != 2783296.090273 || s.ProfitShare != 0.1 || s.AllowDeposits || s.Closed || !s.Curated || s.Kind != domain.VaultHyperCore {
		t.Errorf("summary = %+v", s)
	}
	if math.Abs(s.Returns[domain.WAll]-6.723904538419246) > 1e-12 || math.Abs(s.MaxDD+0.5064937863963572) > 1e-12 || s.Sharpe != 2.6348676784786305 {
		t.Errorf("v6 metrics = %v %v %v", s.Returns[domain.WAll], s.MaxDD, s.Sharpe)
	}
	if s.SPModel != domain.SPv6 || d.SPModel != domain.SPv6 || d.SharePrice != 8.824847687850994 || s.AccuracyPct != 1.948 {
		t.Errorf("share price = %v %s %s acc %v", d.SharePrice, d.SPModel, s.SPModel, s.AccuracyPct)
	}
	if len(d.NAV) != 6 || d.NAV[0].V != 1.132403893504151 || len(d.Equity) != 4 || len(d.PnL) != 4 {
		t.Errorf("series nav=%d equity=%d pnl=%d", len(d.NAV), len(d.Equity), len(d.PnL))
	}
	if len(d.Fills) != 6 || len(d.Flows) != 5 || len(d.Composition) != 3 {
		t.Errorf("fills=%d flows=%d composition=%d", len(d.Fills), len(d.Flows), len(d.Composition))
	}
	if f := d.Flows[3]; f.Kind != "vaultWithdraw" || f.Commission != 0.332236 || f.USD != -6.227763 || f.User == "" {
		t.Errorf("withdraw flow = %+v", f)
	}
	if e := d.Composition[0]; e.Equity != 2438761.279484 || e.ByCoin["HYPE"] != 2917394.42688 || e.ByCoin["ETH"] >= 0 {
		t.Errorf("exposure = %+v", e)
	}
	if d.LockupDays != 1 || s.MaxGrossLev != 1.3780015092632263 || d.CapacityUSD != 0 {
		t.Errorf("lockup %v lev %v cap %v", d.LockupDays, s.MaxGrossLev, d.CapacityUSD)
	}
	if !strings.HasPrefix(d.Description, "70% HYPE") || s.AgeDays < 580 {
		t.Errorf("desc/age = %q %d", d.Description[:10], s.AgeDays)
	}
	if d.Meta.Source != "hlp.xyz" || d.Meta.Stale || len(d.Meta.Warnings) != 0 || d.Meta.AsOf.UnixMilli() != 1794735534712 {
		t.Errorf("meta = %+v", d.Meta)
	}
	if _, err := json.Marshal(d); err != nil {
		t.Fatalf("detail not JSON-encodable: %v", err)
	}

	// Only /live: live share price model, warnings for the missing parts.
	only := DetailParts{Address: HLPAddress, Live: p.Live, Now: fixtureNow}
	d, err = ToDetail(only)
	if err != nil {
		t.Fatal(err)
	}
	if d.SPModel != domain.SPLive || d.CapacityUSD != -1 || d.Summary.Rel != "parent" || len(d.Meta.Warnings) != 2 {
		t.Errorf("live-only = %+v %+v", d.Summary, d.Meta)
	}

	// Stale history.
	old := *p.Detail
	old.Meta.LastUpdated = fixtureNow.Add(-72 * time.Hour).UnixMilli()
	d, _ = ToDetail(DetailParts{Address: addrGarbage, Detail: &old, Now: fixtureNow})
	if !d.Meta.Stale || !d.Summary.Stale {
		t.Error("old history not flagged stale")
	}
	if _, err := ToDetail(DetailParts{Address: "nope"}); !errors.Is(err, ErrBadAddress) {
		t.Errorf("bad address: %v", err)
	}
}

func TestDetailPreservesIndependentAnalyticsAndFreshness(t *testing.T) {
	p := loadParts(t)
	p.NAV.WindowAccuracy["1d"] = nil
	zero := 0.0
	p.NAV.WindowAccuracy["1w"] = &zero
	p.Live.DepositCapacity = &zero
	p.Live.RecentAV = []Snapshot{{TS: 2, AV: 20, PnL: 3}, {TS: 1, AV: 19, PnL: 2}}
	p.Detail.Meta.LastUpdated = p.Now.Add(-72 * time.Hour).UnixMilli()
	p.NAV.BuiltAt = p.Now.Add(-3 * time.Hour).UnixMilli()
	d, err := ToDetail(p)
	if err != nil {
		t.Fatal(err)
	}
	a := d.Analytics
	if a == nil || a.NAV == nil || a.NAV.SampleSize != 52 || a.NAV.AnchorCount != 52 || a.NAV.LastRosterTs != nil || a.NAV.Source != "v6-chain" {
		t.Fatalf("NAV provenance: %+v", a)
	}
	if a.NAV.WindowAccuracy["1d"] != nil || a.NAV.WindowAccuracy["1w"] == nil || *a.NAV.WindowAccuracy["1w"] != 0 {
		t.Fatalf("unknown vs measured zero accuracy: %+v", a.NAV.WindowAccuracy)
	}
	if a.NAV.TotalReturn != p.NAV.TotalReturnPct/100 || len(a.Funding) != len(p.Detail.Funding) || len(a.Leverage) != len(p.Detail.Leverage) {
		t.Fatalf("analytics lost values: %+v", a)
	}
	if a.Funding[0].Payment != p.Detail.Funding[0].Payment || a.Leverage[0].Gross != p.Detail.Leverage[0].Leverage {
		t.Fatalf("funding/leverage units: %+v", a)
	}
	if a.Live.DepositCapacity == nil || *a.Live.DepositCapacity != 0 || a.Live.RecentAV[0].T != 1 || a.Composition.BucketMs != p.Composition.BucketMs {
		t.Fatalf("live/composition: %+v", a)
	}
	if !d.Freshness["history"].Stale || d.Freshness["nav"].Stale || d.Freshness["nav"].AsOf != p.NAV.BuiltAt || d.Freshness["live"].AsOf != p.Live.CachedAt {
		t.Fatalf("mixed source ages: %+v", d.Freshness)
	}
}

func TestFetchDetail(t *testing.T) {
	f := newFakeServer(t)
	c := f.client()
	d, err := FetchDetail(context.Background(), c, addrGarbage, FetchOpts{WithStatus: true})
	if err != nil {
		t.Fatal(err)
	}
	if d.Meta.Partial || len(d.Fills) != 6 || len(d.NAV) != 6 || len(d.Composition) != 3 || d.Summary.Sharpe != 2.6348676784786305 {
		t.Fatalf("detail: partial=%v fills=%d nav=%d comp=%d sharpe=%v warnings=%v", d.Meta.Partial, len(d.Fills), len(d.NAV), len(d.Composition), d.Summary.Sharpe, d.Meta.Warnings)
	}

	// Untracked vault: /live works, the history says no-cache.
	f.set("/api/vault/"+addrBredo, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"status":"no-cache"}`)) })
	f.set("/api/user?v6vault", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"navNow":0,"curve":[]}`))
	})
	d, err = FetchDetail(context.Background(), c, addrBredo, FetchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Meta.Partial || d.Fills != nil || d.NAV != nil || d.SPModel != domain.SPLive || len(d.Meta.Warnings) != 2 {
		t.Fatalf("untracked: %+v", d.Meta)
	}

	// A failing part is Partial with a warning.
	f.set("/api/vault/"+addrGarbage+"/composition", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", 500) })
	// (A fresh client: an existing one would serve its cached copy as stale.)
	d, err = FetchDetail(context.Background(), f.client(), addrGarbage, FetchOpts{})
	if err != nil || !d.Meta.Partial || !strings.Contains(strings.Join(d.Meta.Warnings, ";"), "composition") {
		t.Fatalf("partial: %v %+v", err, d.Meta)
	}

	// Non-vault address.
	f.set("/api/vault/"+HLPAddress+"/live", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"error":"Vault not found"}`)) })
	if _, err := FetchDetail(context.Background(), c, HLPAddress, FetchOpts{}); !errors.Is(err, ErrNotAVault) {
		t.Fatalf("err = %v", err)
	}
}

func TestIndexHelpers(t *testing.T) {
	var idx Index
	if err := json.Unmarshal(readFixture(t, "idx_get_mavericks.json"), &idx); err != nil {
		t.Fatal(err)
	}
	if got := idx.Def.RulesSummary(); got != "top 8 by 30d calmar, monthly, min age 60d, min TVL $100k, max DD 20%" {
		t.Errorf("RulesSummary = %q", got)
	}
	m := IndexMembers(idx)
	if len(m) != 8 || m[0].Weight != 0.125 || m[0].Address > m[1].Address {
		t.Errorf("members = %+v", m)
	}
	if (IndexDef{Kind: "manual"}).RulesSummary() != "" {
		t.Error("manual index has no rules summary")
	}

	// Synthetic NAV: two vaults, 50/50, then all-in on B after day 2.
	day := int64(24 * time.Hour / time.Millisecond)
	a := "0x" + strings.Repeat("a", 40)
	b := "0x" + strings.Repeat("b", 40)
	curves := map[string]domain.Series{
		a: {{T: 0, V: 1}, {T: day, V: 1.1}, {T: 2 * day, V: 1.2}, {T: 4 * day, V: 1.2}},
		b: {{T: 0, V: 2}, {T: day, V: 2}, {T: 2 * day, V: 1}, {T: 3 * day, V: 2}, {T: 4 * day, V: 2}},
	}
	syn := Index{Allocs: []IndexAlloc{
		{TS: 2 * day, Weights: map[string]float64{b: 1}},
		{TS: 0, Weights: map[string]float64{a: 0.5, "0x" + strings.ToUpper(b[2:]): 0.5}},
	}}
	nav := IndexNAV(syn, curves)
	// day0 1.0; day1 .5*1.1+.5*1 = 1.05; day2 .5*1.2+.5*.5 = 0.85 then all-in B
	// at price 1; day3 B doubles -> 1.7; day4 flat.
	want := []float64{1, 1.05, 0.85, 1.7, 1.7}
	if len(nav) != len(want) {
		t.Fatalf("nav = %+v", nav)
	}
	for i, w := range want {
		if math.Abs(nav[i].V-w) > 1e-12 || nav[i].T != int64(i)*day {
			t.Errorf("nav[%d] = %+v, want %v", i, nav[i], w)
		}
	}
	// Missing curve = cash.
	cash := IndexNAV(Index{Allocs: []IndexAlloc{{TS: 0, Weights: map[string]float64{a: 0.5, "0x" + strings.Repeat("c", 40): 0.5}}}}, curves)
	if math.Abs(cash[len(cash)-1].V-1.1) > 1e-12 {
		t.Errorf("half cash, half A (+20%%) = %v, want 1.1", cash[len(cash)-1].V)
	}
	if IndexNAV(Index{}, curves) != nil {
		t.Error("no allocations -> nil")
	}
}

func TestIndexCurvesRetry(t *testing.T) {
	f := newFakeServer(t)
	n := 0
	f.set("/api/vaults?idxCurves", func(w http.ResponseWriter, r *http.Request) {
		n++
		if r.URL.Query().Get("pipeline") != "v6" {
			t.Errorf("pipeline = %q", r.URL.Query().Get("pipeline"))
		}
		if n == 1 {
			w.Write([]byte(`{"curves":{},"incomplete":true}`))
			return
		}
		w.Write(readFixture(t, "idx_curves.json"))
	})
	c := f.client()
	curves, complete, err := c.IndexCurves(context.Background())
	if err != nil || !complete || n != 2 || len(curves[addrGarbage]) != 4 {
		t.Fatalf("curves=%d complete=%v n=%d err=%v", len(curves), complete, n, err)
	}
	// The complete answer is cached; the incomplete one was not.
	if _, _, err := c.IndexCurves(context.Background()); err != nil || n != 2 {
		t.Fatalf("cache: n=%d err=%v", n, err)
	}
}

// TestLiveHLPXYZ is an opt-in smoke test against production (HLP_LIVE=1):
// four public GETs (summary, v6status, v6spark, bulk live) plus HLP /live.
func TestLiveHLPXYZ(t *testing.T) {
	if os.Getenv("HLP_LIVE") != "1" {
		t.Skip("set HLP_LIVE=1 to run against www.hlp.xyz")
	}
	c := New(Options{
		UserAgent: "hlp-cli/test (+https://github.com/maxmoneycash/hlp)",
		HTTP:      &http.Client{Timeout: 30 * time.Second, Transport: logTransport{t}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rows, meta, err := c.VaultList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 10 || meta.Source != "hlp.xyz" {
		t.Fatalf("%d rows, meta %+v", len(rows), meta)
	}
	v, err := c.VaultLive(ctx, HLPAddress)
	if err != nil || v.TotalEquity < 1e6 {
		t.Fatalf("HLP live = %+v %v", v, err)
	}
	for _, r := range rows {
		if r.Address == HLPAddress && math.Abs(r.TVL-v.TotalEquity)/v.TotalEquity > 0.05 {
			t.Errorf("HLP list TVL %v vs live %v", r.TVL, v.TotalEquity)
		}
	}
}

// logTransport logs each live request's status, latency and size.
type logTransport struct{ t *testing.T }

func (l logTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		l.t.Logf("%s %s: %v (%s)", r.Method, r.URL.Path+"?"+r.URL.RawQuery[:min(60, len(r.URL.RawQuery))], err, time.Since(start).Round(time.Millisecond))
		return nil, err
	}
	l.t.Logf("%d %s?%s (%s)", resp.StatusCode, r.URL.Path, r.URL.RawQuery[:min(60, len(r.URL.RawQuery))], time.Since(start).Round(time.Millisecond))
	return resp, nil
}
