package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/config"
	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/core/coretest"
	"github.com/maxmoneycash/hlp/internal/domain"
)

func invoke(t *testing.T, env *core.Env, tool string, args string) (*core.Result, error) {
	t.Helper()
	// tests model the CLI (a trusted source) unless they set one
	return core.Default().Invoke(core.WithSource(context.Background(), "cli"), env, core.Call{Tool: tool, Args: json.RawMessage(args)})
}

func mustInvoke(t *testing.T, env *core.Env, tool string, args string) *core.Result {
	t.Helper()
	res, err := invoke(t, env, tool, args)
	if err != nil {
		t.Fatalf("%s %s: %v", tool, args, err)
	}
	if res.Meta.AsOf.IsZero() || res.Meta.Source == "" || res.Meta.Network == "" {
		t.Fatalf("%s: incomplete meta %+v", tool, res.Meta)
	}
	return res
}

func code(err error) string {
	var e *core.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestMarkets(t *testing.T) {
	env := coretest.Env(t, nil)
	res := mustInvoke(t, env, "get_markets", `{}`)
	ms := res.Data.([]domain.Market)
	if res.Card != core.CardMarkets || len(ms) != 5 {
		t.Fatalf("default = live perps on all dexes, got %d", len(ms))
	}
	if ms[0].Coin != "BTC" || ms[1].Coin != "ETH" {
		t.Fatalf("sorted by volume: %s %s", ms[0].Coin, ms[1].Coin)
	}
	for _, m := range ms {
		if m.Kind == domain.Spot || m.Delisted {
			t.Fatalf("unexpected %s", m.Coin)
		}
	}
	ms = mustInvoke(t, env, "get_markets", `{"kind":"spot"}`).Data.([]domain.Market)
	if len(ms) != 1 || ms[0].Coin != "@107" {
		t.Fatalf("spot: %+v", ms)
	}
	ms = mustInvoke(t, env, "get_markets", `{"query":"sp500"}`).Data.([]domain.Market)
	if len(ms) == 0 || ms[0].Coin != "xyz:SP500" {
		t.Fatalf("fuzzy: %+v", ms)
	}
	ms = mustInvoke(t, env, "get_markets", `{"sort":"change","limit":2}`).Data.([]domain.Market)
	if len(ms) != 2 || ms[0].Coin != "HYPE" {
		t.Fatalf("change sort: %+v", ms)
	}
	ms = mustInvoke(t, env, "get_markets", `{"dex":"xyz"}`).Data.([]domain.Market)
	if len(ms) != 1 {
		t.Fatalf("dex filter: %d", len(ms))
	}
	if _, err := invoke(t, env, "get_markets", `{"sort":"nope"}`); code(err) != core.CodeUsage {
		t.Fatalf("bad sort: %v", err)
	}
}

func TestChart(t *testing.T) {
	env := coretest.Env(t, nil)
	res := mustInvoke(t, env, "get_candles", `{"coin":"btc","interval":"15m","ma":[20],"ema":[50],"width":90,"height":26}`)
	d := res.Data.(*core.ChartData)
	if res.Card != core.CardChart || d.Coin != "BTC" || d.Interval != domain.I15m {
		t.Fatalf("chart: %+v", d)
	}
	// default bars fit the width: 90/2 + 50 warmup
	if len(d.Candles) != 95 {
		t.Fatalf("bars = %d, want 95", len(d.Candles))
	}
	last := d.Candles[len(d.Candles)-1]
	if d.Summary.Last != last.C || d.Summary.Open != d.Candles[0].O {
		t.Fatalf("summary %+v", d.Summary)
	}
	want := last.C/d.Candles[0].O - 1 // fraction (PLAN D20)
	if math.Abs(d.Summary.Change-want) > 1e-9 {
		t.Fatalf("change %v want %v", d.Summary.Change, want)
	}
	sum := 0.0
	for _, c := range d.Candles[len(d.Candles)-20:] {
		sum += c.C
	}
	if math.Abs(d.Summary.MA["sma20"]-sum/20) > 1e-9 {
		t.Fatalf("sma20 %v want %v", d.Summary.MA["sma20"], sum/20)
	}
	if _, ok := d.Summary.MA["ema50"]; !ok {
		t.Fatal("ema50 missing")
	}
	for _, c := range d.Candles {
		if c.T%(15*60*1000) != 0 {
			t.Fatalf("candle T must be the open time: %d", c.T)
		}
	}
	// config default MAs apply when none are given
	d = mustInvoke(t, env, "get_candles", `{"coin":"BTC","bars":60}`).Data.(*core.ChartData)
	if len(d.Candles) != 60 || len(d.MAs) != 2 {
		t.Fatalf("bars/MAs: %d %v", len(d.Candles), d.MAs)
	}

	for args, want := range map[string]string{
		`{"coin":"NOPE"}`:                 core.CodeUnknownCoin,
		`{"coin":"BTC","interval":"7m"}`:  core.CodeUsage,
		`{"coin":"BTC","zoom":3}`:         core.CodeUsage,
		`{"coin":"BTC","style":"neon"}`:   core.CodeUsage,
		`{"coin":"BTC","ma":[1,2,3,4]}`:   core.CodeUsage,
		`{"coin":"BTC","bars":6000}`:      core.CodeUsage,
		`{"coin":"BTC","address":"0x12"}`: core.CodeUsage,
		`{}`:                              core.CodeUsage,
		`{"coin":"ETH"}`:                  core.CodeNotFound, // no ETH 15m candles in fixtures
	} {
		_, err := invoke(t, env, "get_candles", args)
		if code(err) != want {
			t.Errorf("%s: got %v want %s", args, err, want)
		}
	}
	_, err := invoke(t, env, "get_candles", `{"coin":"NOPE"}`)
	if e := core.AsError(err); !strings.Contains(e.Message, "NOPE") || e.Hint == "" {
		t.Errorf("unknown coin error should name the input and hint: %+v", e)
	}
}

func TestChartAddressOverlay(t *testing.T) {
	env := coretest.Env(t, nil)
	res := mustInvoke(t, env, "get_candles", `{"coin":"BTC","bars":50,"address":"`+coretest.Account+`"}`)
	if res.Meta.Partial {
		t.Fatalf("overlay should load: %+v", res.Meta)
	}
	res = mustInvoke(t, env, "get_candles", `{"coin":"BTC","bars":50,"address":"0x9999999999999999999999999999999999999999"}`)
	if !res.Meta.Partial || len(res.Meta.Warnings) == 0 {
		t.Fatalf("missing account should warn: %+v", res.Meta)
	}
}

func TestBookTradesFundingAssetID(t *testing.T) {
	env := coretest.Env(t, nil)
	b := mustInvoke(t, env, "get_orderbook", `{"coin":"BTC","depth":2}`).Data.(domain.Book)
	if len(b.Bids) != 2 || len(b.Asks) != 2 || b.Mid() != 73500 {
		t.Fatalf("book %+v", b)
	}
	if _, err := invoke(t, env, "get_orderbook", `{"coin":"BTC","depth":30}`); code(err) != core.CodeUsage {
		t.Fatal("depth > 20 must be USAGE")
	}
	if _, err := invoke(t, env, "get_orderbook", `{"coin":"BTC","sigfigs":1}`); code(err) != core.CodeUsage {
		t.Fatal("sigfigs 1 must be USAGE")
	}
	ts := mustInvoke(t, env, "get_trades", `{"coin":"BTC"}`).Data.([]domain.Trade)
	if len(ts) != 2 || ts[0].TID != 2 {
		t.Fatalf("trades newest first: %+v", ts)
	}
	fp := mustInvoke(t, env, "get_funding", `{"coin":"BTC","hours":24}`).Data.([]domain.FundingPoint)
	if len(fp) != 2 {
		t.Fatalf("funding window: %d", len(fp))
	}
	rank := mustInvoke(t, env, "get_funding", `{"top":2}`).Data.([]domain.Market)
	if len(rank) != 4 || rank[0].Coin != "HYPE" || rank[len(rank)-1].Coin != "ETH" || rank[2].Coin != "DOGE" {
		got := []string{}
		for _, m := range rank {
			got = append(got, m.Coin)
		}
		t.Fatalf("ranking: %v", got)
	}
	if _, err := invoke(t, env, "get_funding", `{"coin":"@107"}`); code(err) != core.CodeUsage {
		t.Fatal("spot funding must be USAGE")
	}
	kv := mustInvoke(t, env, "get_asset_id", `{"coin":"sp500 (xyz)"}`).Data.(*core.AssetIDData)
	m := map[string]string{}
	for _, r := range kv.Pairs() {
		m[r[0]] = r[1]
	}
	if m["coin"] != "xyz:SP500" || m["assetId"] != "110052" || m["dex"] != "xyz" {
		t.Fatalf("asset-id %v", m)
	}
}

func TestAccountFillsWatch(t *testing.T) {
	env := coretest.Env(t, nil)
	acct := mustInvoke(t, env, "get_account", `{"address":"`+coretest.Account+`"}`).Data.(domain.Account)
	if acct.Equity != 15000 || len(acct.Positions) != 2 || acct.Positions[0].Coin != "BTC" {
		t.Fatalf("account sorted by value: %+v", acct.Positions)
	}
	if _, err := invoke(t, env, "get_account", `{}`); code(err) != core.CodeUsage {
		t.Fatal("no address and no config account must be USAGE")
	}
	env.Config.Account = coretest.Account
	if _, err := invoke(t, env, "get_account", `{}`); err != nil {
		t.Fatalf("config account fallback: %v", err)
	}
	fills := mustInvoke(t, env, "get_fills", `{"address":"`+coretest.Account+`"}`).Data.([]domain.Fill)
	if len(fills) != 2 || fills[0].TID != 7 {
		t.Fatalf("fills 24h newest first: %+v", fills)
	}
	fills = mustInvoke(t, env, "get_fills", `{"address":"`+coretest.Account+`","hours":100,"coin":"eth"}`).Data.([]domain.Fill)
	if len(fills) != 2 {
		t.Fatalf("fills coin filter: %+v", fills)
	}
	w := mustInvoke(t, env, "watch", `{"address":"`+coretest.Account+`"}`)
	if w.Card != core.CardAccount || !strings.HasPrefix(w.Title, "Watching") {
		t.Fatalf("watch: %+v", w)
	}
	// streaming with no WebSocket (offline) emits the snapshot and ends
	var n int
	err := core.Default().RunStream(context.Background(), env, core.Call{Tool: "watch", Args: json.RawMessage(`{"address":"` + coretest.Account + `"}`)}, func(*core.Result) { n++ })
	if err != nil || n != 1 {
		t.Fatalf("offline stream: %v %d", err, n)
	}
}

func TestHLPPositioning(t *testing.T) {
	env := coretest.Env(t, nil)
	res := mustInvoke(t, env, "hlp_positioning", `{}`)
	a := res.Data.(domain.Account)
	// parent (BTC +0.2, ETH -2) + child (BTC -0.1)
	var btc, eth domain.Position
	for _, p := range a.Positions {
		switch p.Coin {
		case "BTC":
			btc = p
		case "ETH":
			eth = p
		}
	}
	if math.Abs(btc.Size-0.1) > 1e-12 || eth.Size != -2 {
		t.Fatalf("net sizes: btc %v eth %v", btc.Size, eth.Size)
	}
	if want := (70000*0.2 + 74000*-0.1) / 0.1; math.Abs(btc.Entry-want) > 1e-6 {
		t.Fatalf("weighted entry %v want %v", btc.Entry, want)
	}
	if math.Abs(btc.Value-(14700-7350)) > 1e-9 || a.Equity != 30000 {
		t.Fatalf("value %v equity %v", btc.Value, a.Equity)
	}
	if math.Abs(a.NetNtl-(14700-7350-5200)) > 1e-9 {
		t.Fatalf("net notional %v", a.NetNtl)
	}
}

func TestMergePerp(t *testing.T) {
	base := coretest.AccountData()
	live := domain.Account{PerpEquity: 13000, Positions: []domain.Position{{Coin: "SOL", Size: 1, Value: 200}}}
	got := core.MergePerp(base, live)
	if got.Equity != 13000+2000+1000 || len(got.Positions) != 1 || got.SpotUSD != 2000 || len(got.OpenOrders) != 1 {
		t.Fatalf("merge: %+v", got)
	}
}

func TestStreamHelpers(t *testing.T) {
	cs := []domain.Candle{{T: 1, C: 1}, {T: 2, C: 2}}
	cs = core.MergeCandle(cs, domain.Candle{T: 2, C: 3}, 2)
	if len(cs) != 2 || cs[1].C != 3 {
		t.Fatalf("replace: %+v", cs)
	}
	cs = core.MergeCandle(cs, domain.Candle{T: 3, C: 4}, 2)
	if len(cs) != 2 || cs[0].T != 2 || cs[1].T != 3 {
		t.Fatalf("append+trim: %+v", cs)
	}
	cs = core.MergeCandle(cs, domain.Candle{T: 1, C: 9}, 2)
	if cs[0].T != 2 {
		t.Fatalf("old bar must be ignored: %+v", cs)
	}
	tape := []domain.Trade{{TID: 2, Time: 2}, {TID: 1, Time: 1}}
	tape = core.PrependTrades(tape, []domain.Trade{{TID: 3, Time: 3}, {TID: 2, Time: 2}, {TID: 4, Time: 4}}, 3)
	if len(tape) != 3 || tape[0].TID != 4 || tape[1].TID != 3 || tape[2].TID != 2 {
		t.Fatalf("tape: %+v", tape)
	}
	b := core.TrimBook(domain.Book{Bids: make([]domain.Level, 5), Asks: make([]domain.Level, 1)}, 2)
	if len(b.Bids) != 2 || len(b.Asks) != 1 {
		t.Fatal("TrimBook")
	}
	s := core.Summarize(nil, []int{20}, nil)
	if s.Last != 0 || s.MA == nil {
		t.Fatal("Summarize(nil)")
	}
}

func TestVaultsMerge(t *testing.T) {
	x := &coretest.FakeXYZ{}
	env := coretest.Env(t, x)
	res := mustInvoke(t, env, "list_vaults", `{}`)
	vs := res.Data.([]domain.VaultSummary)
	names := []string{}
	for _, v := range vs {
		names = append(names, v.Name)
	}
	// default: TVL >= 10k, no closed, no HLP children, by TVL
	want := []string{"Hyperliquidity Provider (HLP)", "Growi HF", "Long HYPE & BTC | Short Garbage", "Curated Only"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v", names)
	}
	if res.Meta.Source != "stats-data+hlp.xyz" || res.Meta.Partial {
		t.Fatalf("meta %+v", res.Meta)
	}
	g := vs[2]
	if !g.Curated || g.Address != coretest.Garbage || g.Returns[domain.W1M] != 0.1234 || g.MaxDD != -0.25 || g.SPModel != domain.SPv6 || g.TVL != 2.78e6 {
		t.Fatalf("curated merge: %+v", g)
	}
	if len(g.Sources) != 2 {
		t.Fatalf("sources %v", g.Sources)
	}
	if vs[1].Curated || vs[1].SPModel == domain.SPv6 {
		t.Fatal("uncurated row must keep its own metrics")
	}
	if !vs[3].Stale {
		t.Fatal("stale flag lost")
	}

	vs = mustInvoke(t, env, "list_vaults", `{"curated":true}`).Data.([]domain.VaultSummary)
	if len(vs) != 2 {
		t.Fatalf("curated filter: %d", len(vs))
	}
	vs = mustInvoke(t, env, "list_vaults", `{"max-dd":20}`).Data.([]domain.VaultSummary)
	for _, v := range vs {
		if v.MaxDD < -0.2 {
			t.Fatalf("max-dd filter let %s through", v.Name)
		}
	}
	vs = mustInvoke(t, env, "list_vaults", `{"sort":"sharpe","limit":1}`).Data.([]domain.VaultSummary)
	if len(vs) != 1 || vs[0].Name != "Growi HF" {
		t.Fatalf("sharpe sort: %+v", vs)
	}
	vs = mustInvoke(t, env, "list_vaults", `{"hlp":true,"min-age":500}`).Data.([]domain.VaultSummary)
	if len(vs) != 2 {
		t.Fatalf("hlp children + age: %d", len(vs))
	}
	vs = mustInvoke(t, env, "list_vaults", `{"query":"tiny"}`).Data.([]domain.VaultSummary)
	if len(vs) != 1 || vs[0].Address != coretest.Tiny {
		t.Fatalf("query lifts the TVL floor: %+v", vs)
	}
	vs = mustInvoke(t, env, "list_vaults", `{"query":"retired"}`).Data.([]domain.VaultSummary)
	if len(vs) != 1 {
		t.Fatalf("query finds closed vaults: %+v", vs)
	}
	vs = mustInvoke(t, env, "list_vaults", `{"query":"0xbbbb"}`).Data.([]domain.VaultSummary)
	if len(vs) != 1 || vs[0].Name != "Growi HF" {
		t.Fatalf("address prefix: %+v", vs)
	}
	vs = mustInvoke(t, env, "list_vaults", `{"open":true}`).Data.([]domain.VaultSummary)
	for _, v := range vs {
		if !v.AllowDeposits {
			t.Fatalf("open filter: %s", v.Name)
		}
	}
	if _, err := invoke(t, env, "list_vaults", `{"sort":"vibes"}`); code(err) != core.CodeUsage {
		t.Fatal("bad sort")
	}
	if _, err := invoke(t, env, "list_vaults", `{"max-dd":150}`); code(err) != core.CodeUsage {
		t.Fatal("bad max-dd")
	}

	// hlp.xyz down: Hyperliquid rows still answer, flagged partial
	x.Fail = errors.New("503")
	res = mustInvoke(t, env, "list_vaults", `{}`)
	if !res.Meta.Partial || len(res.Meta.Warnings) == 0 {
		t.Fatalf("partial meta: %+v", res.Meta)
	}
	// hlp.xyz disabled
	env.HLPXYZ = nil
	vs = mustInvoke(t, env, "list_vaults", `{}`).Data.([]domain.VaultSummary)
	if len(vs) != 3 {
		t.Fatalf("hyperliquid only: %d", len(vs))
	}
}

func TestMergeVaultsPure(t *testing.T) {
	out := core.MergeVaults(
		[]domain.VaultSummary{{Address: "0xAB", Name: "A", TVL: 10, APR: 0.1}},
		[]domain.VaultSummary{{Address: "0xab", Name: "A v6", Sharpe: 3, Returns: map[domain.Window]float64{domain.WAll: 1}}, {Address: "0xcd"}},
	)
	if len(out) != 2 || !out[0].Curated || out[0].Sharpe != 3 || out[0].Name != "A" || out[0].APR != 0.1 || !out[1].Curated {
		t.Fatalf("%+v", out)
	}
}

func TestVaultTearsheet(t *testing.T) {
	x := &coretest.FakeXYZ{}
	env := coretest.Env(t, x)

	// curated vault by fuzzy name: v6 NAV curve from hlp.xyz
	res := mustInvoke(t, env, "get_vault", `{"vault":"long hype btc"}`)
	d := res.Data.(domain.VaultDetail)
	if res.Card != core.CardVault || d.Summary.Address != coretest.Garbage {
		t.Fatalf("resolved %s", d.Summary.Address)
	}
	if d.SPModel != domain.SPv6 || len(d.NAV) != 6 || d.SharePrice != 8.824847687850994 {
		t.Fatalf("v6 curve: model %s, %d points, sp %v", d.SPModel, len(d.NAV), d.SharePrice)
	}
	if d.Tearsheet == nil {
		t.Fatal("tearsheet missing")
	}
	if d.LockupDays != 1 || len(d.Composition) == 0 {
		t.Fatalf("live + composition enrichment: lockup %v comp %d", d.LockupDays, len(d.Composition))
	}
	if !strings.Contains(res.Meta.Source, "hlp.xyz") {
		t.Fatalf("source %s", res.Meta.Source)
	}
	if d.Followers != nil || d.Fills != nil {
		t.Fatal("depositors and fills are opt-in")
	}

	// uncurated vault by address: hl-portfolio curve, benchmarks HLP/BTC/HYPE
	res = mustInvoke(t, env, "get_vault", `{"vault":"`+coretest.Growi+`","fills":true,"depositors":true}`)
	d = res.Data.(domain.VaultDetail)
	if d.SPModel != domain.SPHL {
		t.Fatalf("model %s", d.SPModel)
	}
	if len(d.Followers) != 2 || len(d.Fills) != 1 {
		t.Fatalf("opt-in sections: %d followers %d fills", len(d.Followers), len(d.Fills))
	}
	ts := d.Tearsheet
	if ts == nil {
		t.Fatal("tearsheet missing")
	}
	if len(ts.Curve) > 0 {
		// metrics implemented: benchmarks and checklist must be populated
		got := map[string]bool{}
		for _, b := range ts.Benchmarks {
			got[b.Name] = true
		}
		for _, n := range []string{"HLP", "BTC", "HYPE"} {
			if !got[n] {
				t.Errorf("benchmark %s missing (warnings %v)", n, res.Meta.Warnings)
			}
		}
		ids := map[string]bool{}
		for _, c := range ts.Checklist {
			ids[c.ID] = true
		}
		for _, id := range []string{"max_dd", "age", "tvl", "capacity"} {
			if !ids[id] {
				t.Errorf("checklist %s missing: %+v", id, ts.Checklist)
			}
		}
	} else {
		t.Log("metrics package not implemented in this tree; tearsheet contents unchecked")
	}

	// window clipping and an equity benchmark through hlp.xyz
	res = mustInvoke(t, env, "get_vault", `{"vault":"growi","window":"1m","bench":["SPY"]}`)
	if d := res.Data.(domain.VaultDetail); len(d.NAV) > 1 && x.Calls("Benchmark:SPY") == 0 {
		t.Fatal("SPY must come from hlp.xyz")
	}

	for args, want := range map[string]string{
		`{"vault":"zzzzqqq"}`:                  core.CodeUnknownVault,
		`{"vault":"` + coretest.Account + `"}`: core.CodeUnknownVault,
		`{"vault":"growi","window":"2y"}`:      core.CodeUsage,
		`{"vault":""}`:                         core.CodeUsage,
	} {
		if _, err := invoke(t, env, "get_vault", args); code(err) != want {
			t.Errorf("%s: %v, want %s", args, err, want)
		}
	}
}

func TestCompare(t *testing.T) {
	env := coretest.Env(t, &coretest.FakeXYZ{})
	res := mustInvoke(t, env, "compare_vaults", `{"vaults":["growi","`+coretest.Garbage+`"]}`)
	cd := res.Data.(*core.CompareData)
	if len(cd.Vaults) != 2 || len(cd.Corr) != 2 || cd.Corr[0][0] != 1 || cd.Corr[0][1] != cd.Corr[1][0] {
		t.Fatalf("compare: %+v", cd.Corr)
	}
	if _, err := invoke(t, env, "compare_vaults", `{"vaults":["growi"]}`); code(err) != core.CodeUsage {
		t.Fatal("one vault must be USAGE")
	}
	if _, err := invoke(t, env, "compare_vaults", `{"vaults":["growi","nothing-like-this-zzz"]}`); code(err) != core.CodeUnknownVault {
		t.Fatal("unknown vault in compare")
	}
}

func TestWindows(t *testing.T) {
	end := coretest.Now.UnixMilli()
	for w, days := range map[string]int64{"1d": 1, "1w": 7, "1m": 30, "3m": 91, "6m": 182, "1y": 365} {
		got, err := core.WindowStart(w, end)
		if err != nil || got != end-days*86400000 {
			t.Errorf("%s: %d %v", w, got, err)
		}
	}
	if got, _ := core.WindowStart("all", end); got != 0 {
		t.Error("all")
	}
	ytd, _ := core.WindowStart("ytd", end)
	if ytd != time.Date(coretest.Now.Year(), 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli() {
		t.Errorf("ytd %d", ytd)
	}
	s := domain.Series{{T: 10, V: 1}, {T: 20, V: 2}, {T: 30, V: 3}}
	if c := core.ClipSeries(s, 25); len(c) != 2 || c[0].T != 20 {
		t.Fatalf("clip keeps the base point: %+v", c)
	}
	if c := core.ClipSeries(s, 0); len(c) != 3 {
		t.Fatal("clip 0")
	}
}

func TestHLPXYZTools(t *testing.T) {
	env := coretest.Env(t, &coretest.FakeXYZ{})
	res := mustInvoke(t, env, "get_strategy", `{"id":"spxmeme"}`)
	sd := res.Data.(*core.StrategyData)
	if sd.Live.ID != "spxmeme" || sd.Live.Gross != 1.95 || sd.Static == nil {
		t.Fatalf("strat: %+v", sd)
	}
	if _, err := invoke(t, env, "get_strategy", `{"id":"moon"}`); code(err) != core.CodeUsage {
		t.Fatal("unknown strategy")
	}
	// strategy vaults: input errors are USAGE before any RPC call
	if _, err := invoke(t, env, "get_strategy_vault", `{"vault":"0x12"}`); code(err) != core.CodeUsage {
		t.Fatalf("bad strategy vault address: %v", err)
	}
	if _, err := invoke(t, env, "get_strategy_vault", `{"vault":"trend-pyramid","chain":12345}`); code(err) != core.CodeUsage {
		t.Fatalf("unknown chain: %v", err)
	}
	ix := mustInvoke(t, env, "list_indexes", `{}`)
	if ix.Card != core.CardIndex {
		t.Fatal("index card")
	}
	if _, err := invoke(t, env, "list_indexes", `{"slug":"mavericks"}`); err != nil {
		t.Fatalf("index mavericks: %v", err)
	}
	env.HLPXYZ = nil
	if _, err := invoke(t, env, "get_fund", `{}`); code(err) != core.CodeUsage {
		t.Fatal("hlp.xyz disabled must be USAGE")
	}
}

func TestSchemaTool(t *testing.T) {
	env := coretest.Env(t, nil)
	all := mustInvoke(t, env, "schema", `{}`).Data.([]core.ToolSchema)
	if len(all) != len(core.Default().All()) || all[0].Input != nil {
		t.Fatalf("list: %d", len(all))
	}
	for _, tl := range core.Default().All() {
		s := core.Describe(tl)
		if s.Input == nil || s.Card == "" {
			t.Errorf("%s: input schema or card missing", tl.Name)
		}
		if s.OutputType != "" && s.Output == nil {
			t.Errorf("%s: output schema for %s failed", tl.Name, s.OutputType)
		}
	}
	one := mustInvoke(t, env, "schema", `{"tool":"chart"}`).Data.([]core.ToolSchema)
	if len(one) != 1 || one[0].Name != "get_candles" || one[0].Input == nil {
		t.Fatalf("one: %+v", one)
	}
	if _, err := invoke(t, env, "schema", `{"tool":"nope"}`); code(err) != core.CodeUsage {
		t.Fatal("unknown schema tool")
	}
}

func TestStrategyFileGuards(t *testing.T) {
	env := coretest.Env(t, nil)
	for args, want := range map[string]string{
		`{}`:                                    core.CodeUsage,
		`{"file":"/etc/passwd"}`:                core.CodeUsage,
		`{"file":"missing.pine","coin":"BTC"}`:  core.CodeUsage,
		`{"file":"x.yaml","thesis":"long BTC"}`: core.CodeUsage,
		`{"capital":-1,"thesis":"long BTC"}`:    core.CodeUsage,
	} {
		if _, err := invoke(t, env, "run_backtest", args); code(err) != want {
			t.Errorf("%s: %v", args, err)
		}
	}
	if _, err := invoke(t, env, "pine_check", `{"file":"notes.txt"}`); code(err) != core.CodeUsage {
		t.Error("pine_check needs .pine")
	}
	if _, err := invoke(t, env, "paper", `{"action":"explode"}`); code(err) != core.CodeUsage {
		t.Error("bad paper action")
	}
	if _, err := invoke(t, env, "paper", `{"action":"stop"}`); code(err) != core.CodeUsage {
		t.Error("stop needs id")
	}
	if _, err := invoke(t, env, "arena", `{"entrants":["a.pine"]}`); code(err) != core.CodeUsage {
		t.Error("arena needs 2 entrants")
	}
	res := mustInvoke(t, env, "arena", `{"vaults":["growi","hlp"]}`)
	ad := res.Data.(*core.ArenaData)
	if len(ad.Entrants) != 2 || ad.Entrants[0].Kind != "vault" {
		t.Fatalf("vault arena: %+v", ad)
	}
}

func TestPineDiags(t *testing.T) {
	err := errors.Join(errors.New("plain"), errors.Join(errors.New("nested")))
	ds := core.PineDiags(err)
	if len(ds) != 2 || ds[1].Msg != "nested" {
		t.Fatalf("%+v", ds)
	}
	if core.PineDiags(nil) != nil {
		t.Fatal("nil")
	}
}

func TestOrderSafety(t *testing.T) {
	env := coretest.Env(t, nil)
	// trading disabled: refused with exit code 3, even with yes
	_, err := invoke(t, env, "place_order", `{"side":"buy","coin":"BTC","size":"1","yes":true}`)
	if core.ExitCode(err) != 3 || code(err) != core.CodeTradingDisabled {
		t.Fatalf("disabled trading: %v", err)
	}
	// explicit dry-run previews unsigned with the caps that would apply
	res := mustInvoke(t, env, "place_order", `{"side":"buy","coin":"BTC","usd":500,"dry-run":true}`)
	p := res.Data.(*core.OrderPreview)
	if !p.DryRun || p.Envelope != nil || p.Coin != "BTC" || p.Side != domain.Buy || len(p.Checks) == 0 {
		t.Fatalf("preview: %+v", p)
	}
	if res.Meta.Source != "dry-run" {
		t.Fatalf("meta source %s", res.Meta.Source)
	}
	ids := map[string]domain.CheckStatus{}
	for _, c := range p.Checks {
		ids[c.ID] = c.Status
	}
	if ids["trading_enabled"] != domain.CheckFail || ids["max_notional"] != domain.CheckPass {
		t.Fatalf("checks %+v", p.Checks)
	}
	for args, want := range map[string]string{
		`{"side":"hold","coin":"BTC","size":"1","dry-run":true}`:              core.CodeUsage,
		`{"side":"buy","coin":"BTC","dry-run":true}`:                          core.CodeUsage,
		`{"side":"buy","coin":"BTC","size":"1","usd":5,"dry-run":true}`:       core.CodeUsage,
		`{"side":"buy","coin":"NOPE","size":"1","dry-run":true}`:              core.CodeUnknownCoin,
		`{"side":"buy","coin":"BTC","size":"0.000001","dry-run":true}`:        core.CodeUsage,
		`{"side":"buy","coin":"BTC","usd":1,"dry-run":true}`:                  core.CodeUsage,
		`{"side":"buy","coin":"BTC","size":"1","leverage":99,"dry-run":true}`: core.CodeUsage,
		`{"side":"buy","coin":"BTC","size":"1","vault":"me","dry-run":true}`:  core.CodeUsage,
	} {
		if _, err := invoke(t, env, "place_order", args); code(err) != want {
			t.Errorf("%s: %v, want %s", args, err, want)
		}
	}
	// cancel and vault transfer obey the same gate
	if _, err := invoke(t, env, "cancel_order", `{"coin":"BTC","oid":42,"yes":true}`); core.ExitCode(err) != 3 {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := invoke(t, env, "vault_deposit", `{"vault":"`+coretest.Growi+`","usd":100,"yes":true}`); core.ExitCode(err) != 3 {
		t.Fatalf("deposit: %v", err)
	}
	if _, err := invoke(t, env, "vault_deposit", `{"vault":"`+coretest.Growi+`","usd":1,"dry-run":true}`); code(err) != core.CodeUsage {
		t.Fatalf("deposit minimum: %v", err)
	}
	res = mustInvoke(t, env, "vault_withdraw", `{"vault":"`+coretest.Growi+`","usd":100,"dry-run":true}`)
	if p := res.Data.(*core.OrderPreview); p.Action != "vault_withdraw" || p.Vault != coretest.Growi {
		t.Fatalf("withdraw preview: %+v", p)
	}
}

func TestRiskGuard(t *testing.T) {
	g := &core.RiskGuard{
		Risk:        config.RiskConfig{MaxNotionalUSD: 1000, MaxLeverage: 5, AllowedCoins: []string{"BTC", "eth"}, DailyLossUSD: 200},
		TradingOn:   true,
		HasAgentKey: true,
		Account:     coretest.Account,
		RealizedLoss: func(context.Context, string) (float64, error) {
			return 50, nil
		},
	}
	ctx := context.Background()
	ok := core.Intent{Action: "order", Coin: "ETH", NotionalUSD: 500, Leverage: 3, Source: "cli"}
	if err := g.Check(ctx, ok); err != nil {
		t.Fatalf("allowed: %v", err)
	}
	for name, in := range map[string]core.Intent{
		"notional": {Action: "order", Coin: "BTC", NotionalUSD: 5000},
		"leverage": {Action: "order", Coin: "BTC", NotionalUSD: 10, Leverage: 10},
		"coin":     {Action: "order", Coin: "DOGE", NotionalUSD: 10},
	} {
		if err := g.Check(ctx, in); code(err) != core.CodeRiskLimit {
			t.Errorf("%s: %v", name, err)
		}
	}
	// reduce-only and cancels skip the notional cap
	if err := g.Check(ctx, core.Intent{Action: "order", Coin: "BTC", NotionalUSD: 5000, ReduceOnly: true}); err != nil {
		t.Errorf("reduce-only: %v", err)
	}
	if err := g.Check(ctx, core.Intent{Action: "cancel", Coin: "BTC"}); err != nil {
		t.Errorf("cancel: %v", err)
	}
	// daily loss halt
	g.RealizedLoss = func(context.Context, string) (float64, error) { return 250, nil }
	if err := g.Check(ctx, ok); code(err) != core.CodeRiskLimit {
		t.Errorf("daily loss: %v", err)
	}
	// a vault intent reads the vault's fills, not the master account's
	var read string
	g.RealizedLoss = func(_ context.Context, a string) (float64, error) { read = a; return 0, nil }
	if err := g.Check(ctx, core.Intent{Action: "order", Coin: "ETH", NotionalUSD: 10, Vault: coretest.Growi}); err != nil || read != coretest.Growi {
		t.Errorf("vault daily loss read %q: %v", read, err)
	}
	// no account with trading on: the halt cannot work, so it fails (audit S6)
	g.Account = ""
	var failed bool
	for _, c := range g.Checks(ctx, ok) {
		failed = failed || (c.ID == "daily_loss" && c.Status == domain.CheckFail)
	}
	if !failed {
		t.Error("daily_loss must fail without an account")
	}
	g.Account = coretest.Account
	// MCP needs its own permission
	g.RealizedLoss = nil
	if err := g.Check(ctx, core.Intent{Action: "cancel", Source: "mcp"}); code(err) != core.CodeTradingDisabled {
		t.Errorf("mcp: %v", err)
	}
	g.MCPAllowed = true
	if err := g.Check(ctx, core.Intent{Action: "cancel", Source: "mcp"}); err != nil {
		t.Errorf("mcp allowed: %v", err)
	}
	// no key
	g.HasAgentKey = false
	if err := g.Check(ctx, ok); code(err) != core.CodeTradingDisabled {
		t.Errorf("no key: %v", err)
	}
	if err := (core.DenyAll{}).Check(ctx, ok); core.ExitCode(err) != 3 {
		t.Error("DenyAll")
	}
}

func TestNewGuard(t *testing.T) {
	env := coretest.Env(t, nil)
	if _, ok := core.NewGuard(env).(core.DenyAll); !ok {
		t.Fatal("trading disabled -> DenyAll")
	}
	env.Config.Trading.Enabled = true
	env.Config.Trading.AccountAddr = coretest.Account
	g, ok := core.NewGuard(env).(*core.RiskGuard)
	if !ok || g.HasAgentKey || g.RealizedLoss == nil {
		t.Fatalf("risk guard: %+v", g)
	}
	loss, err := g.RealizedLoss(context.Background(), g.Account)
	if err != nil || loss != 0 {
		t.Fatalf("realized loss %v %v", loss, err)
	}
}

func TestParseSide(t *testing.T) {
	for in, want := range map[string]domain.Side{"buy": domain.Buy, "LONG": domain.Buy, "b": domain.Buy, "sell": domain.Sell, "short": domain.Sell, "a": domain.Sell} {
		if got, err := core.ParseSide(in); err != nil || got != want {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	if _, err := core.ParseSide("hodl"); err == nil {
		t.Error("hodl")
	}
}

func TestFundingRanking(t *testing.T) {
	r := core.FundingRanking(coretest.Markets(), 1)
	if len(r) != 2 || r[0].Coin != "HYPE" || r[1].Coin != "DOGE" {
		t.Fatalf("%+v", r)
	}
}

func TestNewEnvOffline(t *testing.T) {
	dir := coretest.Dir(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HLP_NETWORK", "")
	env, err := core.NewEnv(context.Background(), core.EnvOptions{FixturesDir: dir, ConfigPath: dir + "/none.toml", Network: domain.Testnet, Theme: "hlp"})
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	if env.Config.Network != domain.Testnet || env.HLPXYZ != nil || env.Exchange != nil || env.Guard == nil {
		t.Fatalf("env: %+v", env.Config)
	}
	if _, err := env.Resolve(context.Background(), "btc"); err != nil {
		t.Fatal(err)
	}
	if _, err := core.NewEnv(context.Background(), core.EnvOptions{FixturesDir: dir, ConfigPath: dir + "/none.toml", Theme: "no-such-theme"}); code(err) != core.CodeUsage {
		t.Fatalf("bad theme: %v", err)
	}
	t.Setenv("HLP_FIXTURES", "")
	if _, err := core.NewEnv(context.Background(), core.EnvOptions{Offline: true, ConfigPath: dir + "/none.toml"}); code(err) != core.CodeUsage {
		t.Fatalf("offline without dir: %v", err)
	}
}

func TestBacktestAndPineCheck(t *testing.T) {
	env := coretest.Env(t, nil)
	pineFile := coretest.RepoRoot() + "/examples/pine/ema-cross.pine"
	res, err := invoke(t, env, "pine_check", `{"file":"`+pineFile+`","coin":"BTC","interval":"15m"}`)
	if err != nil {
		t.Fatalf("pine check: %v", err)
	}
	pc := res.Data.(*core.PineCheckData)
	if pc.Status != "ok" || pc.Kind != "strategy" || pc.Compile == "" || pc.Inputs == nil {
		t.Fatalf("pine check: %+v", pc)
	}
	if b, _ := json.Marshal(pc); !strings.HasPrefix(string(b), `{"file":`) {
		t.Fatalf("pine check JSON must be an object: %s", b)
	}
	if len(pc.Pairs()) < 4 {
		t.Fatalf("pairs %v", pc.Pairs())
	}
	// pine run = backtest on one market, titled after the strategy
	res, err = invoke(t, env, "run_pine", `{"file":"`+pineFile+`","coin":"BTC","interval":"15m"}`)
	if err != nil || res.Card != core.CardBacktest || !strings.HasPrefix(res.Title, "Pine ") {
		t.Fatalf("pine run: %v %+v", err, res)
	}
	if _, err := invoke(t, env, "run_pine", `{"file":"`+pineFile+`"}`); code(err) != core.CodeUsage {
		t.Fatalf("pine run without coin: %v", err)
	}
	res, err = invoke(t, env, "run_backtest", `{"file":"`+pineFile+`","coin":"BTC","interval":"15m","bench":["BTC"]}`)
	if err != nil {
		t.Fatalf("backtest: %v", err)
	}
	bd := res.Data.(*core.BacktestData)
	if res.Card != core.CardBacktest || res.Meta.Source != "backtest" || bd.Strategy == "" || len(bd.Tearsheet.Curve) < 2 {
		t.Fatalf("backtest result: %s %+v", bd.Strategy, bd.Tearsheet.Metrics)
	}
	// a Pine file with an unsupported construct reports line:col details
	// (arrays are outside hlp's Pine subset; request.security is not)
	bad := t.TempDir() + "/bad.pine"
	if err := os.WriteFile(bad, []byte("//@version=5\nstrategy(\"x\")\nx = array.new_float(10)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = invoke(t, env, "pine_check", `{"file":"`+bad+`"}`)
	if err == nil {
		t.Fatal("pine check accepted an unsupported construct")
	}
	e := core.AsError(err)
	ds, _ := e.Details.([]core.PineDiag)
	if e.Code != core.CodeUsage || len(ds) == 0 || ds[0].Line != 3 {
		t.Fatalf("pine diagnostics: %+v", e)
	}
}
