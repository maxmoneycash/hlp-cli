package cards

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/hlpxyz"
)

// Builders that turn the research fixtures into domain values by hand, so
// card tests never depend on the client packages (or the network).

func repoRoot(t testing.TB) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}

func readFixture(t testing.TB, rel string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "testdata", rel))
	if err != nil {
		t.Fatalf("fixture %s: %v", rel, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("fixture %s: %v", rel, err)
	}
}

// num decodes Hyperliquid string-or-number decimals.
type num float64

func (n *num) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		f, err := strconv.ParseFloat(s, 64)
		*n = num(f)
		return err
	}
	var f float64
	err := json.Unmarshal(b, &f)
	*n = num(f)
	return err
}

func fixtureCandles(t testing.TB) []domain.Candle {
	var raw []struct {
		T          int64 `json:"t"`
		O, H, L, C num
		V          num
		N          int `json:"n"`
	}
	readFixture(t, "candles/btc-15m.json", &raw)
	out := make([]domain.Candle, len(raw))
	for i, r := range raw {
		out[i] = domain.Candle{T: r.T, O: float64(r.O), H: float64(r.H), L: float64(r.L), C: float64(r.C), V: float64(r.V), N: r.N}
	}
	return out
}

func fixtureMarkets(t testing.TB) []domain.Market {
	var raw []json.RawMessage
	readFixture(t, "fixtures/hyperliquid/info/metaAndAssetCtxs.json", &raw)
	var meta struct {
		Universe []struct {
			Name        string `json:"name"`
			SzDecimals  int    `json:"szDecimals"`
			MaxLeverage int    `json:"maxLeverage"`
			IsDelisted  bool   `json:"isDelisted"`
		} `json:"universe"`
	}
	var ctxs []struct {
		Funding, OpenInterest, PrevDayPx, DayNtlVlm, MarkPx, MidPx, OraclePx num
	}
	if err := json.Unmarshal(raw[0], &meta); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw[1], &ctxs); err != nil {
		t.Fatal(err)
	}
	var out []domain.Market
	for i, u := range meta.Universe {
		if i >= len(ctxs) || u.IsDelisted {
			continue
		}
		c := ctxs[i]
		out = append(out, domain.Market{
			Coin: u.Name, Display: u.Name, Kind: domain.Perp, AssetID: i, SzDecimals: u.SzDecimals, MaxLeverage: u.MaxLeverage,
			Mark: float64(c.MarkPx), Mid: float64(c.MidPx), Oracle: float64(c.OraclePx), PrevDay: float64(c.PrevDayPx),
			Funding: float64(c.Funding), OpenInterest: float64(c.OpenInterest), DayNtlVlm: float64(c.DayNtlVlm),
		})
	}
	// The fixture is trimmed; add two synthetic rows so the ranking has both signs.
	out = append(out,
		domain.Market{Coin: "HYPE", Display: "HYPE", Kind: domain.Perp, Mark: 41.234, PrevDay: 39.8, Funding: 0.0000391, OpenInterest: 1.2e7, DayNtlVlm: 6.1e8},
		domain.Market{Coin: "xyz:SP500", Display: "SP500 (xyz)", Kind: domain.HIP3, Dex: "xyz", Mark: 6012.5, PrevDay: 6030.1, Funding: -0.0000082, OpenInterest: 2100, DayNtlVlm: 4.4e7},
	)
	return out
}

func fixtureChart(t testing.TB) *core.ChartData {
	cs := fixtureCandles(t)
	ms := fixtureMarkets(t)
	last := cs[len(cs)-1]
	return &core.ChartData{
		Coin: "BTC", Interval: domain.I15m, Market: ms[0], Candles: cs, MAs: []int{20, 50},
		Summary: core.ChartSummary{Last: last.C, Open: cs[0].O, High: 0, Low: 0, Change: last.C/cs[0].O - 1},
	}
}

func fixtureBook(t testing.TB) domain.Book {
	var raw struct {
		Coin   string `json:"coin"`
		Time   int64  `json:"time"`
		Levels [2][]struct {
			Px, Sz num
			N      int `json:"n"`
		} `json:"levels"`
	}
	readFixture(t, "fixtures/hyperliquid/info/l2Book_BTC.json", &raw)
	b := domain.Book{Coin: raw.Coin, Time: raw.Time}
	for _, l := range raw.Levels[0] {
		b.Bids = append(b.Bids, domain.Level{Px: float64(l.Px), Sz: float64(l.Sz), N: l.N})
	}
	for _, l := range raw.Levels[1] {
		b.Asks = append(b.Asks, domain.Level{Px: float64(l.Px), Sz: float64(l.Sz), N: l.N})
	}
	return b
}

func fixtureTrades(t testing.TB) []domain.Trade {
	var raw []struct {
		Coin   string `json:"coin"`
		Side   string `json:"side"`
		Px, Sz num
		Time   int64  `json:"time"`
		TID    uint64 `json:"tid"`
	}
	readFixture(t, "fixtures/hyperliquid/info/recentTrades_BTC.json", &raw)
	out := make([]domain.Trade, len(raw))
	for i, r := range raw {
		out[i] = domain.Trade{Coin: r.Coin, Side: domain.Side(r.Side), Px: float64(r.Px), Sz: float64(r.Sz), Time: r.Time, TID: r.TID}
	}
	return out
}

func fixtureAccount(t testing.TB) domain.Account {
	var raw struct {
		MarginSummary struct {
			AccountValue, TotalNtlPos, TotalMarginUsed num
		} `json:"marginSummary"`
		Withdrawable   num `json:"withdrawable"`
		AssetPositions []struct {
			Position struct {
				Coin     string `json:"coin"`
				Szi      num    `json:"szi"`
				Leverage struct {
					Type  string `json:"type"`
					Value float64
				} `json:"leverage"`
				EntryPx, PositionValue, UnrealizedPnl, ReturnOnEquity, LiquidationPx, MarginUsed num
			} `json:"position"`
		} `json:"assetPositions"`
		Time int64 `json:"time"`
	}
	readFixture(t, "fixtures/hyperliquid/info/clearinghouseState.json", &raw)
	a := domain.Account{
		Address: "0x31ca8395cf837de08b24da3f660e77761dfb974b", Role: "user",
		Equity: float64(raw.MarginSummary.AccountValue), PerpEquity: float64(raw.MarginSummary.AccountValue),
		Withdrawable: float64(raw.Withdrawable), MarginUsed: float64(raw.MarginSummary.TotalMarginUsed),
		GrossNtl: float64(raw.MarginSummary.TotalNtlPos), Time: raw.Time,
	}
	for _, ap := range raw.AssetPositions {
		p := ap.Position
		a.Positions = append(a.Positions, domain.Position{
			Coin: p.Coin, Size: float64(p.Szi), Entry: float64(p.EntryPx), Value: float64(p.PositionValue),
			UPnL: float64(p.UnrealizedPnl), ROE: float64(p.ReturnOnEquity), Leverage: p.Leverage.Value,
			Cross: p.Leverage.Type == "cross", LiqPx: float64(p.LiquidationPx), MarginUsed: float64(p.MarginUsed),
		})
	}
	a.OpenOrders = []domain.Order{
		{Coin: "BTC", Side: domain.Sell, Px: 69000, Sz: 1.5, OID: 1, Type: "Limit", Time: raw.Time},
		{Coin: "ETH", Side: domain.Buy, Px: 2900, Sz: 10, OID: 2, Type: "Stop Market", TriggerPx: 2850, ReduceOnly: true, Time: raw.Time},
	}
	return a
}

func fixtureFills(t testing.TB) []domain.Fill {
	var raw []struct {
		Coin                          string `json:"coin"`
		Px, Sz                        num
		Side                          string `json:"side"`
		Time                          int64  `json:"time"`
		StartPosition, ClosedPnl, Fee num
		Dir                           string `json:"dir"`
		OID                           uint64 `json:"oid"`
		TID                           uint64 `json:"tid"`
	}
	readFixture(t, "fixtures/hyperliquid/info/userFills.json", &raw)
	out := make([]domain.Fill, len(raw))
	for i, r := range raw {
		out[i] = domain.Fill{Coin: r.Coin, Side: domain.Side(r.Side), Px: float64(r.Px), Sz: float64(r.Sz), Time: r.Time,
			Dir: r.Dir, StartPos: float64(r.StartPosition), ClosedPnL: float64(r.ClosedPnl), Fee: float64(r.Fee), OID: r.OID, TID: r.TID}
	}
	out = append(out, domain.Fill{Coin: "ETH", Side: domain.Sell, Px: 3120.5, Sz: 2, Time: raw[0].Time - 3_600_000, Dir: "Close Long", ClosedPnL: 412.37, Fee: 2.81})
	return out
}

func fixtureFunding(t testing.TB) []domain.FundingPoint {
	var raw []struct {
		Coin        string `json:"coin"`
		FundingRate num    `json:"fundingRate"`
		Premium     num    `json:"premium"`
		Time        int64  `json:"time"`
	}
	readFixture(t, "fixtures/hyperliquid/info/fundingHistory_BTC.json", &raw)
	out := make([]domain.FundingPoint, len(raw))
	for i, r := range raw {
		out[i] = domain.FundingPoint{Coin: r.Coin, Time: r.Time, Rate: float64(r.FundingRate), Premium: float64(r.Premium)}
	}
	return out
}

// fixtureVaults merges the hlp.xyz curated list with the stats-data sample.
func fixtureVaults(t testing.TB) []domain.VaultSummary {
	var hx struct {
		Vaults []struct {
			Address       string  `json:"address"`
			Name          string  `json:"name"`
			Equity        float64 `json:"equity"`
			ReturnAll     float64 `json:"returnAll"`
			ReturnYtd     float64 `json:"returnYtd"`
			MaxDD         float64 `json:"maxDD"`
			Sharpe        float64 `json:"sharpe"`
			AgeDays       float64 `json:"ageDays"`
			AllowDeposits bool    `json:"allowDeposits"`
			IsClosed      bool    `json:"isClosed"`
			PerfSeries    []struct {
				Date       time.Time `json:"date"`
				SharePrice float64   `json:"sharePrice"`
			} `json:"perfSeries"`
		} `json:"vaults"`
	}
	readFixture(t, "fixtures/hlpxyz/vaults_summary.json", &hx)
	var out []domain.VaultSummary
	seen := map[string]bool{}
	for _, v := range hx.Vaults {
		s := domain.VaultSummary{
			Address: v.Address, Name: v.Name, Kind: domain.VaultHyperCore, TVL: v.Equity,
			Returns: map[domain.Window]float64{domain.WAll: v.ReturnAll / 100, domain.WYTD: v.ReturnYtd / 100},
			MaxDD:   v.MaxDD / 100, Sharpe: v.Sharpe, AgeDays: int(v.AgeDays), AllowDeposits: v.AllowDeposits,
			Closed: v.IsClosed, Curated: true, SPModel: domain.SPv6, Sources: []string{"hlp.xyz"},
			LeaderFraction: 0.08,
		}
		for _, p := range v.PerfSeries {
			s.Spark = append(s.Spark, domain.Point{T: p.Date.UnixMilli(), V: p.SharePrice})
		}
		if v.Name == "Hyperliquidity Provider (HLP)" {
			s.Rel = "parent"
		}
		out = append(out, s)
		seen[v.Address] = true
	}
	var st []struct {
		APR     float64 `json:"apr"`
		Summary struct {
			Name         string `json:"name"`
			VaultAddress string `json:"vaultAddress"`
			Leader       string `json:"leader"`
			TVL          num    `json:"tvl"`
			IsClosed     bool   `json:"isClosed"`
			Relationship struct {
				Type string `json:"type"`
			} `json:"relationship"`
		} `json:"summary"`
	}
	readFixture(t, "fixtures/hyperliquid/stats/Mainnet_vaults_sample.json", &st)
	for _, v := range st {
		if seen[v.Summary.VaultAddress] {
			continue
		}
		out = append(out, domain.VaultSummary{
			Address: v.Summary.VaultAddress, Name: v.Summary.Name, Leader: v.Summary.Leader, Kind: domain.VaultHyperCore,
			Rel: v.Summary.Relationship.Type, TVL: float64(v.Summary.TVL), APR: v.APR, Closed: v.Summary.IsClosed,
			AllowDeposits: !v.Summary.IsClosed, Sources: []string{"stats-data"}, Stale: v.Summary.Name == "Snipe Trading",
			Returns: map[domain.Window]float64{domain.W1W: v.APR / 52, domain.W1M: v.APR / 12},
		})
	}
	return out
}

// synthCurve is a deterministic daily NAV curve with a drawdown in the middle.
func synthCurve(days int, start int64, drift, wobble float64) domain.Series {
	s := make(domain.Series, days)
	v := 1.0
	for i := range s {
		r := drift + wobble*math.Sin(float64(i)/6)
		if i > days/2 && i < days/2+20 {
			r -= 0.012
		}
		v *= 1 + r
		s[i] = domain.Point{T: start + int64(i)*86_400_000, V: v}
	}
	return s
}

func drawdown(s domain.Series) domain.Series {
	out := make(domain.Series, len(s))
	peak := 0.0
	for i, p := range s {
		peak = math.Max(peak, p.V)
		out[i] = domain.Point{T: p.T, V: p.V/peak - 1}
	}
	return out
}

func fixtureTearsheet() *domain.Tearsheet {
	start := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	curve := synthCurve(420, start, 0.0021, 0.006)
	return &domain.Tearsheet{
		Title:    "Long HYPE & BTC | Short Garbage",
		Curve:    curve,
		Drawdown: drawdown(curve),
		Metrics: domain.Metrics{TotalReturn: curve.Last() - 1, CAGR: 0.884, Vol: 0.41, Sharpe: 2.21, Sortino: 3.4, MaxDD: -0.214,
			Calmar: 4.13, WinRate: 0.58, ProfitFactor: 1.9, BestMonth: 0.31, WorstMonth: -0.12, Days: 420, Start: start, End: curve[len(curve)-1].T},
		Monthly: []domain.MonthlyReturn{
			{Year: 2025, Month: 9, Ret: 0.081}, {Year: 2025, Month: 10, Ret: -0.034}, {Year: 2025, Month: 11, Ret: 0.122},
			{Year: 2025, Month: 12, Ret: 0.044}, {Year: 2026, Month: 1, Ret: 0.31}, {Year: 2026, Month: 2, Ret: -0.12},
		},
		Benchmarks: []domain.Benchmark{
			{Name: "HLP", Curve: synthCurve(420, start, 0.0004, 0.001), Metrics: domain.Metrics{TotalReturn: 0.18, CAGR: 0.155, Vol: 0.06, Sharpe: 2.6, Sortino: 3.9, MaxDD: -0.043, Calmar: 3.6}, Corr: 0.12, Beta: 0.05},
			{Name: "BTC", Curve: synthCurve(420, start, 0.0008, 0.01), Metrics: domain.Metrics{TotalReturn: 0.31, CAGR: 0.26, Vol: 0.48, Sharpe: 0.71, Sortino: 1.0, MaxDD: -0.28, Calmar: 0.93}, Corr: 0.44, Beta: 0.61},
			{Name: "HYPE", Curve: synthCurve(420, start, 0.0019, 0.02), Metrics: domain.Metrics{TotalReturn: 1.9, CAGR: 1.52, Vol: 0.95, Sharpe: 1.4, Sortino: 2.1, MaxDD: -0.52, Calmar: 2.9}, Corr: 0.71, Beta: 0.38},
		},
		Checklist: []domain.Check{
			{ID: "track_record", Label: "Track record over 180 days", Status: domain.CheckPass, Detail: "420d"},
			{ID: "max_dd", Label: "Max drawdown under 30%", Status: domain.CheckPass, Detail: "-21.4%"},
			{ID: "leader_skin", Label: "Leader owns at least 10%", Status: domain.CheckWarn, Detail: "8.0%"},
			{ID: "concentration", Label: "No single coin over 60% of gross", Status: domain.CheckFail, Detail: "HYPE 64%"},
			{ID: "beats_hlp", Label: "Beats HLP risk-adjusted", Status: domain.CheckNA, Detail: "Sharpe 2.21 vs 2.60"},
		},
		Meta: domain.Meta{Source: "hlp.xyz", AsOf: time.Date(2026, 11, 15, 14, 2, 11, 0, time.UTC)},
	}
}

func fixtureVaultDetail(t testing.TB) domain.VaultDetail {
	vs := fixtureVaults(t)
	var s domain.VaultSummary
	for _, v := range vs {
		if v.Name == "Long HYPE & BTC | Short Garbage" {
			s = v
		}
	}
	s.AgeDays, s.ProfitShare, s.Followers, s.AllowDeposits = 420, 0.10, 100, true
	ts := fixtureTearsheet()
	return domain.VaultDetail{
		Summary: s, Description: "Long HYPE and BTC against a basket of low-quality alts. Rebalanced weekly.",
		SharePrice: ts.Curve.Last(), SPModel: domain.SPv6, NAV: ts.Curve, Tearsheet: ts,
		Composition: []domain.Exposure{{T: ts.Metrics.End, Equity: s.TVL, ByCoin: map[string]float64{
			"HYPE": 2.1e6, "BTC": 1.4e6, "FARTCOIN": -6.2e5, "DOGE": -5.1e5, "PUMP": -4.4e5, "TRUMP": -3.0e5,
		}}},
		CapacityUSD: 1.6e6,
		Meta:        ts.Meta,
	}
}

func fixturePVault(t testing.TB) hlpxyz.PVaultStatus {
	var s hlpxyz.PVaultStatus
	readFixture(t, "fixtures/hlpxyz/pvault_status.json", &s)
	return s
}

func fixtureQuant(t testing.TB) hlpxyz.QuantLive {
	var q hlpxyz.QuantLive
	readFixture(t, "fixtures/hlpxyz/quant_live_spxmeme.json", &q)
	return q
}

// fixtureStrategy is what get_strategy returns: live book + backtest.
func fixtureStrategy(t testing.TB) *core.StrategyData {
	var st hlpxyz.QuantStatic
	readFixture(t, "fixtures/hlpxyz/quant_spxmeme_static.json", &st)
	return &core.StrategyData{Live: fixtureQuant(t), Static: &st}
}

var fixedAsOf = time.Date(2026, 11, 15, 14, 2, 11, 0, time.UTC)

// fixtureResults is one Result per card kind, all from fixtures.
func fixtureResults(t testing.TB) map[string]*core.Result {
	meta := domain.Meta{Source: "hyperliquid", AsOf: fixedAsOf, Network: domain.Mainnet}
	ts := fixtureTearsheet()
	cs := fixtureCandles(t)
	fills := []domain.Fill{
		{Coin: "BTC", Side: domain.Buy, Px: cs[40].C, Sz: 0.1, Time: cs[40].T + 60_000},
		{Coin: "BTC", Side: domain.Sell, Px: cs[90].C, Sz: 0.1, Time: cs[90].T + 120_000, ClosedPnL: 12.4},
	}
	acct := fixtureAccount(t)
	detail := fixtureVaultDetail(t)
	other := detail
	other.Summary.Name = "Hyperliquidity Provider (HLP)"
	ots := *ts
	ots.Curve = ts.Benchmarks[0].Curve
	ots.Metrics = ts.Benchmarks[0].Metrics
	other.Tearsheet = &ots
	return map[string]*core.Result{
		"chart":           {Card: core.CardChart, Data: fixtureChart(t), Meta: meta},
		"book":            {Card: core.CardBook, Data: fixtureBook(t), Meta: meta},
		"trades":          {Card: core.CardTrades, Title: "BTC", Data: fixtureTrades(t), Meta: meta},
		"markets":         {Card: core.CardMarkets, Data: fixtureMarkets(t), Meta: meta},
		"funding":         {Card: core.CardFunding, Data: fixtureMarkets(t), Meta: meta},
		"funding_history": {Card: core.CardFunding, Data: fixtureFunding(t), Meta: meta},
		"account":         {Card: core.CardAccount, Data: acct, Meta: meta},
		"fills":           {Card: core.CardFills, Data: fixtureFills(t), Meta: meta},
		"vaults": {Card: core.CardVaults, Data: fixtureVaults(t),
			Meta: domain.Meta{Source: "stats-data + hlp.xyz", AsOf: fixedAsOf, Partial: true, Warnings: []string{"hlp.xyz summary for HLP is stale; TVL from /live"}}},
		"vault":   {Card: core.CardVault, Data: detail, Meta: domain.Meta{Source: "hlp.xyz + hyperliquid", AsOf: fixedAsOf}},
		"compare": {Card: core.CardCompare, Data: &core.CompareData{Vaults: []domain.VaultDetail{detail, other}, Corr: [][]float64{{1, 0.12}, {0.12, 1}}}, Meta: meta},
		"backtest": {Card: core.CardBacktest, Data: &core.BacktestData{
			Strategy: "ema-cross.pine", Tearsheet: *ts, Fills: fills, Candles: map[string][]domain.Candle{"BTC": cs},
			Warnings: []string{"history limited to 5000 candles"},
		}, Meta: domain.Meta{Source: "backtest", AsOf: fixedAsOf}},
		"paper": {Card: core.CardPaper, Data: &core.PaperData{ID: "p-7f3a", Strategy: "supertrend.pine", Equity: scale(ts.Curve[:60], 10_000), Account: acct, Fills: fills, Running: true},
			Meta: domain.Meta{Source: "paper", AsOf: fixedAsOf}},
		"arena": {Card: core.CardArena, Data: &core.ArenaData{Entrants: []core.ArenaEntrant{
			{Name: "ema-cross", Kind: "pine", Equity: ts.Benchmarks[1].Curve, Metrics: ts.Benchmarks[1].Metrics},
			{Name: "qwen3 (local)", Kind: "llm", Equity: ts.Curve, Metrics: ts.Metrics, LastNote: "HYPE broke the weekly range on rising OI; adding to the long."},
			{Name: "long-spy-short-memes", Kind: "thesis", Equity: ts.Benchmarks[0].Curve, Metrics: ts.Benchmarks[0].Metrics},
		}}, Meta: domain.Meta{Source: "paper", AsOf: fixedAsOf}},
		"order": {Card: core.CardOrder, Data: &core.OrderPreview{Action: "order", Coin: "HYPE", Side: domain.Buy, Size: "2.43", Px: "41.12",
			NotionalUSD: 99.92, Network: domain.Testnet, DryRun: true, Envelope: json.RawMessage(`{"action":{"type":"order"},"nonce":1,"signature":{"r":"0x1","s":"0x2","v":27}}`),
			Checks: []domain.Check{{ID: "max_notional", Label: "Notional under $1,000", Status: domain.CheckPass, Detail: "$99.92"}, {ID: "max_leverage", Label: "Leverage under 5x", Status: domain.CheckPass, Detail: "1x"}}},
			Meta: domain.Meta{Source: "dry-run", AsOf: fixedAsOf, Network: domain.Testnet}},
		"fund":     {Card: core.CardFund, Data: fixturePVault(t), Meta: domain.Meta{Source: "hlp.xyz", AsOf: fixedAsOf, Network: domain.Testnet}},
		"strategy": {Card: core.CardStrategy, Title: "Long SPY, Short Memecoins", Data: fixtureStrategy(t), Meta: domain.Meta{Source: "hlp.xyz", AsOf: fixedAsOf}},
		"kv": {Card: core.CardKV, Title: "HYPE", Data: [][2]string{{"coin", "HYPE"}, {"asset id", "159"}, {"sz decimals", "2"}, {"max leverage", "10x"}, {"dex", "main"}},
			Meta: meta},
		"text":     {Card: core.CardText, Title: "Note", Data: "**HLP** is the protocol vault.\n\n- market making\n- liquidations", Meta: domain.Meta{}},
		"fallback": {Card: core.Card("mystery"), Title: "Mystery", Data: map[string]any{"a": 1, "b": []int{1, 2}}, Meta: meta},
	}
}

func scale(s domain.Series, k float64) domain.Series {
	out := make(domain.Series, len(s))
	for i, p := range s {
		out[i] = domain.Point{T: p.T, V: p.V * k}
	}
	return out
}
