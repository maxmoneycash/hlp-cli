package pvault

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/evm"
)

// A recorded live testnet vault: testnet positions, mainnet depth10 (p10)
// as the proxy, NAV $349.12, depositCap $10,000.
func docInputs(t *testing.T) RiskInputs {
	ch, err := ParseClearinghouse(newFakeCore(t).ch, "")
	if err != nil {
		t.Fatal(err)
	}
	return RiskInputs{NavUSD: 349.12, DepositCapUSD: 10_000, MaxPriceMoveBps: 2500, Testnet: true, LateSettle: true,
		Positions: ch.Positions, DepthSource: "mainnet book (proxy for testnet)",
		Depth:           map[string]float64{riskKey("", "BTC"): 18.75e6, riskKey("", "ETH"): 3.30e6, riskKey("", "SOL"): 1.39e6},
		Ctxs:            map[string]AssetCtx{riskKey("", "BTC"): {Mark: 100_012, Oracle: 100_000}},
		HasUnvaluedSpot: true, UnvaluedSpotUSD: 0.2, Now: time.Unix(1794744506, 0)}
}

func TestBuildRiskRecordedVault(t *testing.T) {
	v := BuildRisk(docInputs(t))
	if v.Level != RiskLow || v.BindingAsset != "SOL" || v.SafeFlowUSD == nil {
		t.Fatalf("%+v", v)
	}
	if math.Abs(*v.SafeFlowUSD-2.1e6) > 0.01e6 || math.Abs(v.FlowAtRiskUSD-9650.88) > 0.01 || math.Abs(v.Ratio-0.005) > 0.0006 {
		t.Fatalf("safe %v flow %v ratio %v", *v.SafeFlowUSD, v.FlowAtRiskUSD, v.Ratio)
	}
	want := map[string][2]float64{"BTC": {5.6e6, 5060}, "ETH": {2.1e6, 890}, "SOL": {2.1e6, 375}}
	for _, a := range v.Assets {
		w := want[a.Coin]
		if a.SafeFlowUSD == nil || math.Abs(*a.SafeFlowUSD-w[0])/w[0] > 0.03 || math.Abs(a.PushCost10BpUSD-w[1])/w[1] > 0.02 {
			t.Errorf("%s: safe %v cost %v", a.Coin, a.SafeFlowUSD, a.PushCost10BpUSD)
		}
	}
	if v.NavBpPer10bpPush["BTC"] != 2.99 || v.NavBpPer10bpPush["ETH"] != 1.39 || v.NavBpPer10bpPush["SOL"] != 0.6 {
		t.Errorf("nav bp %v", v.NavBpPer10bpPush)
	}
	if v.MarkOracleGapBps["BTC"] != 1.2 {
		t.Errorf("gap %v", v.MarkOracleGapBps)
	}
	// "Today's vault": low, with these flags (unvalued spot is dust)
	if strings.Join(v.Flags, ",") != "loose_price_breaker,permissionless_late_settle,testnet_prices,unvalued_spot" {
		t.Errorf("flags %v", v.Flags)
	}
	if v.Label != "manipulation risk (estimate)" || v.NextRead-v.AsOf != RiskTTL.Milliseconds() {
		t.Error(v.Label, v.AsOf, v.NextRead)
	}
	if _, err := json.Marshal(v); err != nil {
		t.Fatal(err)
	}
}

func TestBuildRiskLevels(t *testing.T) {
	// The doc's worked example: NAV $100K at 3x HYPE, depth10 $380K.
	in := RiskInputs{NavUSD: 100_000, DepositCapUSD: 200_000,
		Positions: []CorePosition{{Coin: "HYPE", Size: 1, PositionValue: 300_000}},
		Depth:     map[string]float64{riskKey("", "HYPE"): 380_000}}
	v := BuildRisk(in)
	if v.Level != RiskHigh || v.Assets[0].PushCost10BpUSD != 102.6 || v.NavBpPer10bpPush["HYPE"] != 30 {
		t.Fatalf("%+v", v)
	}
	in.DepositCapUSD = 100_000 + 0.5*11_400 // ratio 0.5
	if v := BuildRisk(in); v.Level != RiskMedium {
		t.Errorf("medium: %v %v", v.Level, v.Ratio)
	}
	// HIP-3 above 10% of NAV is critical whatever the ratio.
	in.Positions = append(in.Positions, CorePosition{Dex: "xyz", Coin: "xyz:TSLA", Size: -1, PositionValue: 20_000})
	in.Depth[riskKey("xyz", "xyz:TSLA")] = 600_000
	v = BuildRisk(in)
	if v.Level != RiskCritical || !contains(v.Flags, FlagHIP3Oracle) || v.Assets[1].NotionalUSD != -20_000 {
		t.Errorf("hip3: %+v", v)
	}
	// unknown depth: flagged, the asset gives no safe flow
	in = RiskInputs{NavUSD: 100, Positions: []CorePosition{{Coin: "X", Size: 1, PositionValue: 50}}}
	v = BuildRisk(in)
	if v.SafeFlowUSD != nil || !contains(v.Flags, FlagDepthUnavailable) || !contains(v.Flags, FlagNoDepositCap) || v.Level != RiskLow {
		t.Errorf("unknown depth: %+v", v)
	}
	// material unvalued spot is critical
	in.HasUnvaluedSpot, in.UnvaluedSpotUSD = true, 5
	if v := BuildRisk(in); v.Level != RiskCritical {
		t.Errorf("spot: %v", v.Level)
	}
	// no positions at all: nothing to push
	if v := BuildRisk(RiskInputs{NavUSD: 100, DepositCapUSD: 1000}); v.Level != RiskLow || v.Ratio != 0 || v.SafeFlowUSD != nil {
		t.Errorf("flat: %+v", v)
	}
}

func TestDepth10(t *testing.T) {
	b := &Book{Bids: [][2]float64{{99.95, 10}, {99.9, 5}, {99.8, 100}}, Asks: [][2]float64{{100.05, 1}, {100.1, 20}, {100.2, 100}}}
	// mid 100; window 99.9..100.1
	got, ok := Depth10(b)
	if !ok || math.Abs(got-(99.95*10+99.9*5)) > 1e-6 {
		t.Fatal(got, ok)
	}
	if _, ok := Depth10(&Book{Bids: b.Bids}); ok {
		t.Fatal("one-sided book has depth")
	}
}

func TestXYZWeekend(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	cases := map[string]bool{"2026-11-13T19:59": false, "2026-11-13T20:00": true, "2026-11-14T12:00": true, "2026-11-15T20:04": true, "2026-11-15T20:05": false, "2026-11-16T10:00": false}
	for s, want := range cases {
		tm, _ := time.ParseInLocation("2006-01-02T15:04", s, ny)
		if xyzWeekend(tm) != want {
			t.Errorf("%s: want %v", s, want)
		}
	}
}

// marketCore is fakeCore plus books and contexts, counting reads.
type marketCore struct {
	*fakeCore
	books map[string]*Book
	ctxs  map[string]map[string]AssetCtx
	reads int
}

func (m *marketCore) L2Book(_ context.Context, coin string) (*Book, error) {
	m.reads++
	if b, ok := m.books[coin]; ok {
		return b, nil
	}
	return nil, errors.New("no book")
}

func (m *marketCore) AssetCtxs(_ context.Context, dex string) (map[string]AssetCtx, error) {
	m.reads++
	return m.ctxs[dex], nil
}

func flatBook(mid, perSide float64) *Book {
	return &Book{Bids: [][2]float64{{mid * 0.9999, perSide / (mid * 0.9999)}}, Asks: [][2]float64{{mid * 1.0001, perSide / (mid * 1.0001)}}}
}

func TestServiceRiskUsesMainnetDepthAndCaches(t *testing.T) {
	fc := newFakeCaller(t)
	now := time.Unix(1794744506, 0)
	testnet := &marketCore{fakeCore: newFakeCore(t), books: map[string]*Book{"BTC": flatBook(100_000, 1)},
		ctxs: map[string]map[string]AssetCtx{"": {"BTC": {Mark: 72_000, Oracle: 100_000}, "HYPE": {Mark: 40, Oracle: 40}}}}
	mainnet := &marketCore{fakeCore: newFakeCore(t), books: map[string]*Book{
		"BTC": flatBook(100_000, 18.75e6), "ETH": flatBook(3_000, 3.3e6), "SOL": flatBook(150, 1.39e6)}}
	s := NewService(Options{
		Telemetry: newFakeTelemetry(t),
		Now:       func() time.Time { return now },
		NewCaller: func(evm.Chain) (evm.Caller, error) { return fc, nil },
		NewCore: func(c evm.Chain) CoreInfo {
			if c.ID == evm.ChainMainnet {
				return mainnet
			}
			return testnet
		},
	})
	ts, m, err := s.Tearsheet(context.Background(), TearsheetInput{Vault: "trend-pyramid", Risk: true, NoHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	v := ts.ManipulationRisk
	if v == nil || v.Level != RiskLow || v.BindingAsset != "SOL" || v.Cached || v.Partial {
		t.Fatalf("%+v %v", v, m.Warnings)
	}
	if v.DepthSource != "mainnet book (proxy for testnet)" || v.MarkOracleGapBps["BTC"] != -2800 {
		t.Errorf("source %q gap %v", v.DepthSource, v.MarkOracleGapBps)
	}
	if v.UnvaluedSpotUSD != 0.2 || !contains(v.Flags, FlagUnvaluedSpot) {
		t.Errorf("spot %v %v", v.UnvaluedSpotUSD, v.Flags)
	}
	if len(ts.Positions) != 0 {
		t.Error("risk alone must not add positions to the tearsheet")
	}
	if mainnet.reads != 3 || testnet.reads != 1 {
		t.Errorf("reads mainnet %d testnet %d", mainnet.reads, testnet.reads)
	}
	// within RiskTTL: served from cache, no new reads
	now = now.Add(59 * time.Second)
	ts, _, _ = s.Tearsheet(context.Background(), TearsheetInput{Vault: "trend-pyramid", Risk: true, NoHistory: true})
	if !ts.ManipulationRisk.Cached || mainnet.reads != 3 {
		t.Errorf("cache: %v reads %d", ts.ManipulationRisk.Cached, mainnet.reads)
	}
	now = now.Add(2 * time.Second)
	ts, _, _ = s.Tearsheet(context.Background(), TearsheetInput{Vault: "trend-pyramid", Risk: true, NoHistory: true})
	if ts.ManipulationRisk.Cached || mainnet.reads != 6 {
		t.Errorf("refresh: %v reads %d", ts.ManipulationRisk.Cached, mainnet.reads)
	}
	// without risk the block is absent
	ts, _, _ = s.Tearsheet(context.Background(), TearsheetInput{Vault: "trend-pyramid", NoHistory: true})
	if ts.ManipulationRisk != nil {
		t.Error("risk block without the risk input")
	}
}

func TestServiceRiskPartialWithoutMarketInfo(t *testing.T) {
	fc := newFakeCaller(t)
	s := testService(t, fc, nil) // fakeCore has no MarketInfo
	ts, m, err := s.Tearsheet(context.Background(), TearsheetInput{Vault: "trend-pyramid", Risk: true, NoHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	v := ts.ManipulationRisk
	if v == nil || !v.Partial || !contains(v.Flags, FlagDepthUnavailable) || !m.Partial {
		t.Fatalf("%+v %+v", v, m)
	}
}
