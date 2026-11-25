// Package coretest builds offline fixtures for core, cli and mcp tests: a
// FixtureReader directory (domain JSON plus the real BTC 15m candle sample)
// and a fake hlp.xyz API decoding the recorded hlp.xyz fixtures. Nothing
// here touches the network.
package coretest

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/config"
	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hlpxyz"
	"github.com/maxmoneycash/hlp/internal/theme"
)

// Addresses used by the fixtures.
const (
	Account   = "0x1111111111111111111111111111111111111111"
	Garbage   = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // "Long HYPE & BTC | Short Garbage" (curated)
	Growi     = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" // "Growi HF" (uncurated)
	Tiny      = "0xcccccccccccccccccccccccccccccccccccccccc"
	Closed    = "0xdddddddddddddddddddddddddddddddddddddddd"
	CuratedX  = "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" // hlp.xyz only
	HLPChild1 = "0x469f690213c467c39a23efacfd2816896009d7d8"
)

// Now is the fixed clock of the fixtures (after the BTC sample ends).
var Now = time.UnixMilli(1794735600000).UTC()

// RepoRoot finds the repository root from this source file.
func RepoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

// Research returns a path under testdata.
func Research(parts ...string) string {
	return filepath.Join(append([]string{RepoRoot(), "testdata"}, parts...)...)
}

func write(t testing.TB, dir, name string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Markets is the fixture market universe.
func Markets() []domain.Market {
	return []domain.Market{
		{Coin: "BTC", Display: "BTC", Kind: domain.Perp, AssetID: 0, SzDecimals: 5, MaxLeverage: 40, Mark: 73500, Mid: 73500, PrevDay: 72000, Funding: 0.0000125, OpenInterest: 30000, DayNtlVlm: 2e9, Category: "crypto"},
		{Coin: "ETH", Display: "ETH", Kind: domain.Perp, AssetID: 1, SzDecimals: 4, MaxLeverage: 25, Mark: 2600, Mid: 2600, PrevDay: 2700, Funding: -0.00002, OpenInterest: 500000, DayNtlVlm: 1e9, Category: "crypto"},
		{Coin: "HYPE", Display: "HYPE", Kind: domain.Perp, AssetID: 159, SzDecimals: 2, MaxLeverage: 10, Mark: 40, Mid: 40, PrevDay: 38, Funding: 0.00005, OpenInterest: 2e7, DayNtlVlm: 5e8, Category: "crypto"},
		{Coin: "DOGE", Display: "DOGE", Kind: domain.Perp, AssetID: 12, SzDecimals: 0, MaxLeverage: 10, Mark: 0.2, PrevDay: 0.21, Funding: -0.0001, OpenInterest: 1e9, DayNtlVlm: 1e8, Category: "memes"},
		{Coin: "OLD", Display: "OLD", Kind: domain.Perp, AssetID: 50, Delisted: true},
		{Coin: "xyz:SP500", Display: "SP500 (xyz)", Kind: domain.HIP3, Dex: "xyz", AssetID: 110052, SzDecimals: 3, MaxLeverage: 20, Mark: 6800, PrevDay: 6750, Funding: 0.00001, OpenInterest: 100, DayNtlVlm: 3e7, Category: "stocks"},
		{Coin: "@107", Display: "HYPE/USDC", Kind: domain.Spot, AssetID: 10107, SzDecimals: 2, Mark: 40, DayNtlVlm: 4e7},
	}
}

// daily builds n daily points ending at end with value f(i).
func daily(n int, end time.Time, f func(i int) float64) domain.Series {
	out := make(domain.Series, n)
	start := end.Add(-time.Duration(n-1) * 24 * time.Hour)
	for i := range out {
		out[i] = domain.Point{T: start.Add(time.Duration(i) * 24 * time.Hour).UnixMilli(), V: f(i)}
	}
	return out
}

func dailyCandles(n int, base float64) []domain.Candle {
	out := make([]domain.Candle, n)
	day := Now.Truncate(24 * time.Hour)
	for i := range out {
		c := base * (1 + 0.1*math.Sin(float64(i)/9) + 0.001*float64(i))
		out[i] = domain.Candle{T: day.Add(-time.Duration(n-1-i) * 24 * time.Hour).UnixMilli(), O: c * 0.99, H: c * 1.02, L: c * 0.97, C: c, V: 1000}
	}
	return out
}

// Vaults is the stats-data style universe.
func Vaults() []domain.VaultSummary {
	return []domain.VaultSummary{
		{Address: core.HLPAddress, Name: "Hyperliquidity Provider (HLP)", Leader: "0x677d", Kind: domain.VaultHyperCore, Rel: "parent", TVL: 151.3e6, APR: 0.08, AgeDays: 900, AllowDeposits: true, Returns: map[domain.Window]float64{domain.W1M: 0.01}},
		{Address: HLPChild1, Name: "HLP Strategy X", Kind: domain.VaultHyperCore, Rel: "child", TVL: 100.8e6, AgeDays: 600},
		{Address: strings.ToUpper(Garbage[:2]) + Garbage[2:], Name: "Long HYPE & BTC | Short Garbage", Leader: "0xa380444a9299df2a15bddf949dd493bb6d30d939", Kind: domain.VaultHyperCore, Rel: "normal", TVL: 2.78e6, APR: 0.9, AgeDays: 220, AllowDeposits: true, MaxDD: -0.4, Sharpe: 1.0, Returns: map[domain.Window]float64{domain.W1M: 0.02}},
		{Address: Growi, Name: "Growi HF", Kind: domain.VaultHyperCore, Rel: "normal", TVL: 5e6, APR: 0.3, AgeDays: 400, AllowDeposits: true, MaxDD: -0.1, Sharpe: 2.5, Returns: map[domain.Window]float64{domain.W1M: 0.04}},
		{Address: Tiny, Name: "Tiny Bot", Kind: domain.VaultHyperCore, Rel: "normal", TVL: 500, APR: 3, AgeDays: 10, AllowDeposits: true},
		{Address: Closed, Name: "Retired Alpha", Kind: domain.VaultHyperCore, Rel: "normal", TVL: 50000, AgeDays: 300, Closed: true},
	}
}

// VaultDetail is the Growi HF tearsheet input (uncurated: hl-portfolio model).
func VaultDetail() domain.VaultDetail {
	eq := daily(120, Now, func(i int) float64 { return 4e6 + float64(i)*8000 + 50000*math.Sin(float64(i)/5) })
	pnl := daily(120, Now, func(i int) float64 { return float64(i)*8000 + 50000*math.Sin(float64(i)/5) })
	return domain.VaultDetail{
		Summary:     Vaults()[3],
		LockupDays:  1,
		Equity:      eq,
		PnL:         pnl,
		Followers:   []domain.Follower{{User: Account, Equity: 1e6}, {User: Tiny, Equity: 4e6}},
		Flows:       []domain.Flow{{Time: Now.Add(-48 * time.Hour).UnixMilli(), USD: 10000, Kind: "vaultDeposit"}},
		Fills:       []domain.Fill{{Coin: "BTC", Side: domain.Buy, Px: 73000, Sz: 1, Time: Now.Add(-time.Hour).UnixMilli(), ClosedPnL: 120}},
		CapacityUSD: 1e6,
	}
}

// AccountData is the fixture account.
func AccountData() domain.Account {
	return domain.Account{
		Address: Account, Role: "user", Equity: 15000, PerpEquity: 12000, SpotUSD: 2000, EarnUSD: 1000, Withdrawable: 8000, MarginUsed: 4000,
		Positions: []domain.Position{
			{Coin: "ETH", Size: -2, Entry: 2650, Mark: 2600, Value: 5200, UPnL: 100, Leverage: 5, MarginUsed: 1040},
			{Coin: "BTC", Size: 0.2, Entry: 70000, Mark: 73500, Value: 14700, UPnL: 700, Leverage: 5, LiqPx: 60000, MarginUsed: 2940},
		},
		OpenOrders: []domain.Order{{Coin: "BTC", Side: domain.Buy, Px: 70000, Sz: 0.1, OID: 42}},
	}
}

// Dir writes the FixtureReader directory and returns it.
func Dir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "markets.json", Markets())
	src, err := os.ReadFile(Research("candles", "btc-15m.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "candles_BTC_15m.json"), src, 0o600); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "candles_BTC_1d.json", dailyCandles(400, 70000))
	write(t, dir, "candles_HYPE_1d.json", dailyCandles(400, 35))
	write(t, dir, "candles_ETH_1d.json", dailyCandles(400, 2500))
	write(t, dir, "book_BTC.json", domain.Book{Coin: "BTC", Time: Now.UnixMilli(),
		Bids: []domain.Level{{Px: 73499, Sz: 1, N: 3}, {Px: 73498, Sz: 2, N: 4}, {Px: 73497, Sz: 3, N: 1}},
		Asks: []domain.Level{{Px: 73501, Sz: 1, N: 2}, {Px: 73502, Sz: 2, N: 5}, {Px: 73503, Sz: 4, N: 1}}})
	write(t, dir, "trades_BTC.json", []domain.Trade{
		{Coin: "BTC", Side: domain.Buy, Px: 73500, Sz: 0.1, Time: Now.UnixMilli() - 2000, TID: 1},
		{Coin: "BTC", Side: domain.Sell, Px: 73499, Sz: 0.2, Time: Now.UnixMilli() - 1000, TID: 2},
	})
	write(t, dir, "funding_BTC.json", []domain.FundingPoint{
		{Coin: "BTC", Time: Now.Add(-2 * time.Hour).UnixMilli(), Rate: 0.00001},
		{Coin: "BTC", Time: Now.Add(-1 * time.Hour).UnixMilli(), Rate: 0.0000125},
		{Coin: "BTC", Time: Now.Add(-200 * time.Hour).UnixMilli(), Rate: 0.00002},
	})
	write(t, dir, "account_"+Account+".json", AccountData())
	write(t, dir, "fills_"+Account+".json", []domain.Fill{
		{Coin: "BTC", Side: domain.Buy, Px: 73000, Sz: 0.1, Time: Now.Add(-time.Hour).UnixMilli(), TID: 7},
		{Coin: "ETH", Side: domain.Sell, Px: 2650, Sz: 2, Time: Now.Add(-2 * time.Hour).UnixMilli(), TID: 8},
		{Coin: "ETH", Side: domain.Sell, Px: 2700, Sz: 1, Time: Now.Add(-72 * time.Hour).UnixMilli(), TID: 9},
	})
	write(t, dir, "vaults.json", Vaults())
	write(t, dir, "vault_"+Growi+".json", VaultDetail())
	g := VaultDetail()
	g.Summary = Vaults()[2]
	g.Summary.Address = Garbage
	write(t, dir, "vault_"+Garbage+".json", g)
	h := VaultDetail()
	h.Summary = Vaults()[0]
	h.Children = []string{HLPChild1}
	write(t, dir, "vault_"+core.HLPAddress+".json", h)
	acct := AccountData()
	acct.Address = core.HLPAddress
	write(t, dir, "account_"+core.HLPAddress+".json", acct)
	acct.Address = HLPChild1
	acct.Positions = []domain.Position{{Coin: "BTC", Size: -0.1, Entry: 74000, Mark: 73500, Value: 7350, UPnL: 50, MarginUsed: 700}}
	write(t, dir, "account_"+HLPChild1+".json", acct)
	return dir
}

// Env returns an offline Env over Dir with the fake hlp.xyz API (pass nil
// for hlp.xyz disabled), trading disabled, and the fixed clock.
func Env(t testing.TB, x hlpxyz.API) *core.Env {
	t.Helper()
	dir := Dir(t)
	cfg := config.Defaults()
	env := &core.Env{
		Config: cfg,
		Paths:  config.Paths{ConfigFile: filepath.Join(dir, "config.toml"), CacheDir: filepath.Join(dir, "cache"), DataDir: filepath.Join(dir, "data")},
		HL:     core.NewFixtureReader(dir),
		HLPXYZ: x,
		Guard:  core.DenyAll{},
		Theme:  theme.Default(true),
		Now:    func() time.Time { return Now },
	}
	return env
}

// FakeXYZ is an hlpxyz.API over the recorded hlp.xyz fixtures. Garbage and
// CuratedX are curated; everything else answers ErrNoCache.
type FakeXYZ struct {
	mu    sync.Mutex
	calls map[string]int
	Fail  error // when set, VaultList fails with it
}

// Calls returns how often a method was called ("NAV", "Benchmark:SPY").
func (f *FakeXYZ) Calls(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

var _ hlpxyz.API = (*FakeXYZ)(nil)

func (f *FakeXYZ) hit(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[name]++
}

func decode(name string, out any) error {
	b, err := os.ReadFile(Research("fixtures", "hlpxyz", name))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// VaultList implements hlpxyz.API.
func (f *FakeXYZ) VaultList(context.Context) ([]domain.VaultSummary, domain.Meta, error) {
	f.hit("VaultList")
	if f.Fail != nil {
		return nil, domain.Meta{}, f.Fail
	}
	return []domain.VaultSummary{
		{Address: Garbage, Name: "Long HYPE & BTC | Short Garbage", TVL: 2.7e6, Returns: map[domain.Window]float64{domain.W1M: 0.1234, domain.WAll: 7.9}, MaxDD: -0.25, Sharpe: 2.2, SPModel: domain.SPv6, AccuracyPct: 1.95, Curated: true, Sources: []string{"hlp.xyz"}, Spark: domain.Series{{T: 1, V: 1}, {T: 2, V: 1.1}}},
		{Address: CuratedX, Name: "Curated Only", TVL: 80000, Returns: map[domain.Window]float64{domain.W1M: 0.01}, SPModel: domain.SPv6, Curated: true, Sources: []string{"hlp.xyz"}, AllowDeposits: true, Stale: true},
	}, domain.Meta{Source: "hlp.xyz", AsOf: Now}, nil
}

// Summary implements hlpxyz.API.
func (f *FakeXYZ) Summary(context.Context, bool) (hlpxyz.SummaryResponse, error) {
	var s hlpxyz.SummaryResponse
	return s, decode("vaults_summary.json", &s)
}

// V6Status implements hlpxyz.API.
func (f *FakeXYZ) V6Status(context.Context) (map[string]hlpxyz.V6Status, error) {
	return map[string]hlpxyz.V6Status{}, nil
}

// V6Spark implements hlpxyz.API.
func (f *FakeXYZ) V6Spark(context.Context) (map[string]domain.Series, error) {
	return map[string]domain.Series{}, nil
}

// Live implements hlpxyz.API.
func (f *FakeXYZ) Live(context.Context, []string) ([]hlpxyz.LiveVault, error) { return nil, nil }

// VaultLive implements hlpxyz.API.
func (f *FakeXYZ) VaultLive(_ context.Context, addr string) (hlpxyz.VaultLive, error) {
	f.hit("VaultLive")
	if !strings.EqualFold(addr, Garbage) {
		return hlpxyz.VaultLive{}, hlpxyz.ErrNotAVault
	}
	var v hlpxyz.VaultLive
	return v, decode("vault_live.json", &v)
}

// VaultDetail implements hlpxyz.API.
func (f *FakeXYZ) VaultDetail(context.Context, string, hlpxyz.DetailOpts) (hlpxyz.VaultDetail, error) {
	return hlpxyz.VaultDetail{}, hlpxyz.ErrNoCache
}

// Composition implements hlpxyz.API.
func (f *FakeXYZ) Composition(_ context.Context, addr, _ string) (hlpxyz.Composition, error) {
	f.hit("Composition")
	if !strings.EqualFold(addr, Garbage) {
		return hlpxyz.Composition{}, hlpxyz.ErrNoCache
	}
	var c hlpxyz.Composition
	return c, decode("vault_composition_1m.json", &c)
}

// NAV implements hlpxyz.API (user_v6vault.json for curated vaults).
func (f *FakeXYZ) NAV(_ context.Context, addr string) (hlpxyz.NAVCurve, error) {
	f.hit("NAV")
	if !strings.EqualFold(addr, Garbage) {
		return hlpxyz.NAVCurve{}, hlpxyz.ErrNoCache
	}
	var n hlpxyz.NAVCurve
	return n, decode("user_v6vault.json", &n)
}

// Benchmark implements hlpxyz.API (a synthetic daily SPY series).
func (f *FakeXYZ) Benchmark(_ context.Context, sym string, start, end time.Time) (domain.Series, error) {
	f.hit("Benchmark:" + sym)
	var out domain.Series
	for t, i := start, 0; !t.After(end); t, i = t.Add(24*time.Hour), i+1 {
		out = append(out, domain.Point{T: t.UnixMilli(), V: 500 + float64(i)})
	}
	return out, nil
}

// Indexes implements hlpxyz.API.
func (f *FakeXYZ) IndexCurves(context.Context) (map[string]domain.Series, bool, error) {
	f.hit("IndexCurves")
	var r struct {
		Curves     map[string][][2]float64 `json:"curves"`
		Incomplete bool                    `json:"incomplete"`
	}
	if err := decode("idx_curves.json", &r); err != nil {
		return nil, false, err
	}
	out := map[string]domain.Series{}
	for a, s := range r.Curves {
		for _, p := range s {
			out[strings.ToLower(a)] = append(out[strings.ToLower(a)], domain.Point{T: int64(p[0]), V: p[1]})
		}
	}
	return out, !r.Incomplete, nil
}

// Indexes implements hlpxyz.API.
func (f *FakeXYZ) Indexes(context.Context, bool) ([]hlpxyz.Index, error) {
	var v struct {
		Indexes []hlpxyz.Index `json:"indexes"`
	}
	return v.Indexes, decode("idx_list.json", &v)
}

// Index implements hlpxyz.API.
func (f *FakeXYZ) Index(ctx context.Context, slug string) (hlpxyz.Index, error) {
	ix, err := f.Indexes(ctx, true)
	for _, x := range ix {
		if x.Def.Slug == slug {
			return x, err
		}
	}
	return hlpxyz.Index{}, fmt.Errorf("index %s: %w", slug, hlpxyz.ErrNoCache)
}

// PVaultStatus implements hlpxyz.API.
func (f *FakeXYZ) PVaultStatus(context.Context) (hlpxyz.PVaultStatus, error) {
	var s hlpxyz.PVaultStatus
	return s, decode("pvault_status.json", &s)
}

// PVaultHistory implements hlpxyz.API.
func (f *FakeXYZ) PVaultHistory(context.Context, int, string) ([]hlpxyz.PVaultSample, error) {
	return []hlpxyz.PVaultSample{
		{TSec: 1794700000, SharePrice: 1.0, NavUSD: 1000, SupplyShares: 1000, KeeperGasHYPE: 0.06},
		{TSec: 1794710000, SharePrice: 1.01, NavUSD: 1010, SupplyShares: 1000, KeeperGasHYPE: 0.059},
	}, nil
}

// Quant implements hlpxyz.API.
func (f *FakeXYZ) Quant(_ context.Context, id string) (hlpxyz.QuantLive, error) {
	var q hlpxyz.QuantLive
	return q, decode("quant_live_"+id+".json", &q)
}

// QuantBacktest implements hlpxyz.API.
func (f *FakeXYZ) QuantBacktest(_ context.Context, id string) (hlpxyz.QuantStatic, error) {
	var q hlpxyz.QuantStatic
	return q, decode("quant_"+id+"_static.json", &q)
}
