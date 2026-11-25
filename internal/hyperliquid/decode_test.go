package hyperliquid

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// TestDecodeInfoFixtures decodes every info fixture into its wire type. A
// new fixture without an entry fails the test so coverage stays complete.
func TestDecodeInfoFixtures(t *testing.T) {
	type spotState struct {
		Balances []SpotBalance `json:"balances"`
	}
	type role struct {
		Role string `json:"role"`
	}
	table := map[string]func() any{
		"activeAssetData.json":                          func() any { return new(map[string]any) },
		"allBorrowLendReserveStates.json":               func() any { return new([]json.RawMessage) },
		"allMids.json":                                  func() any { return new(map[string]Dec) },
		"allPerpMetas.json":                             func() any { return new([]Meta) },
		"borrowLendUserState_HLP_StrategyX.json":        func() any { return new(BorrowLendUserState) },
		"candleSnapshot_BTC_1h.json":                    func() any { return new([]WireCandle) },
		"clearinghouseState_dex_xyz_empty.json":         func() any { return new(ClearinghouseState) },
		"clearinghouseState_HLP_parent.json":            func() any { return new(ClearinghouseState) },
		"clearinghouseState.json":                       func() any { return new(ClearinghouseState) },
		"delegatorSummary.json":                         func() any { return new(map[string]any) },
		"exchangeStatus.json":                           func() any { return new(map[string]any) },
		"extraAgents.json":                              func() any { return new([]map[string]any) },
		"frontendOpenOrders.json":                       func() any { return new([]FrontendOrder) },
		"fundingHistory_BTC.json":                       func() any { return new([]WireFunding) },
		"historicalOrders.json":                         func() any { return new([]map[string]any) },
		"l2Book_BTC_nSigFigs3.json":                     func() any { return new(L2Book) },
		"l2Book_BTC.json":                               func() any { return new(L2Book) },
		"l2Book_xyz_NVDA.json":                          func() any { return new(L2Book) },
		"leadingVaults_HLPleader.json":                  func() any { return new([]map[string]any) },
		"maxBuilderFee_none.json":                       func() any { return new(int) },
		"maxMarketOrderNtls.json":                       func() any { return new([][2]any) },
		"meta_dex_xyz.json":                             func() any { return new(Meta) },
		"meta.json":                                     func() any { return new(Meta) },
		"metaAndAssetCtxs.json":                         func() any { return new([2]json.RawMessage) },
		"openOrders.json":                               func() any { return new([]FrontendOrder) },
		"orderStatus_unknown.json":                      func() any { return new(map[string]any) },
		"orderStatus.json":                              func() any { return new(map[string]any) },
		"outcomeMeta.json":                              func() any { return new(OutcomeMeta) },
		"perpAnnotation_xyz_NVDA.json":                  func() any { return new(PerpAnnotation) },
		"perpCategories.json":                           func() any { return new([]PerpCategory) },
		"perpDexLimits_xyz.json":                        func() any { return new(map[string]any) },
		"perpDexs.json":                                 func() any { return new([]*PerpDex) },
		"perpDexStatus_xyz.json":                        func() any { return new(map[string]any) },
		"perpsAtOpenInterestCap.json":                   func() any { return new([]string) },
		"portfolio.json":                                func() any { return new(Portfolio) },
		"predictedFundings.json":                        func() any { return new([][2]json.RawMessage) },
		"recentTrades_BTC.json":                         func() any { return new([]WireTrade) },
		"referral.json":                                 func() any { return new(map[string]any) },
		"spotClearinghouseState.json":                   func() any { return new(spotState) },
		"spotMeta.json":                                 func() any { return new(SpotMeta) },
		"spotMetaAndAssetCtxs.json":                     func() any { return new([2]json.RawMessage) },
		"subAccounts_none_null.json":                    func() any { return new([]map[string]any) },
		"subAccounts.json":                              func() any { return new([]map[string]any) },
		"tokenDetails_HYPE.json":                        func() any { return new(map[string]any) },
		"userAbstraction.json":                          func() any { return new(string) },
		"userFees.json":                                 func() any { return new(map[string]any) },
		"userFills.json":                                func() any { return new([]WireFill) },
		"userFunding.json":                              func() any { return new([]map[string]any) },
		"userNonFundingLedgerUpdates_HLPdepositor.json": func() any { return new([]map[string]any) },
		"userRateLimit.json":                            func() any { return new(map[string]any) },
		"userRole_user.json":                            func() any { return new(role) },
		"userRole_vault.json":                           func() any { return new(role) },
		"userVaultEquities.json":                        func() any { return new([]UserVaultEquity) },
		"vaultDetails_HLP_child_StrategyX.json":         func() any { return new(VaultDetails) },
		"vaultDetails_HLP_withUser_followerState.json":  func() any { return new(VaultDetails) },
		"vaultDetails_HLP.json":                         func() any { return new(VaultDetails) },
		"vaultSummaries_EMPTY.json":                     func() any { return new([]StatsVault) },
		"webData2_info_POST.json":                       func() any { return new(map[string]json.RawMessage) },
	}
	files, err := filepath.Glob(filepath.Join(fxDir, "info", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, f := range files {
		name := filepath.Base(f)
		mk, ok := table[name]
		if !ok {
			t.Errorf("fixture %s has no wire type in the table", name)
			continue
		}
		b, _ := os.ReadFile(f)
		v := mk()
		if err := json.Unmarshal(b, v); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Composite tuples decode into their parts too.
	var mac [2]json.RawMessage
	_ = json.Unmarshal(readFx(t, "info/metaAndAssetCtxs.json"), &mac)
	var ctxs []AssetCtx
	if err := json.Unmarshal(mac[1], &ctxs); err != nil || len(ctxs) != 4 || ctxs[3].MidPx != "" {
		t.Errorf("assetCtxs: %v %+v", err, ctxs)
	}
	var smac [2]json.RawMessage
	_ = json.Unmarshal(readFx(t, "info/spotMetaAndAssetCtxs.json"), &smac)
	var sctx []SpotAssetCtx
	if err := json.Unmarshal(smac[1], &sctx); err != nil || sctx[0].Coin != "PURR/USDC" {
		t.Errorf("spot ctxs: %v", err)
	}
}

func TestDecodeStatsSampleAndWS(t *testing.T) {
	sv, err := DecodeStatsVaults(strings.NewReader(string(readFx(t, "stats/Mainnet_vaults_sample.json"))))
	if err != nil || len(sv) != 4 {
		t.Fatalf("stats: %v %d", err, len(sv))
	}
	// ws payloads that reuse info wire types.
	var cs struct {
		Data struct {
			ClearinghouseStates [][2]json.RawMessage `json:"clearinghouseStates"`
		} `json:"data"`
	}
	if err := json.Unmarshal(readFx(t, "ws/allDexsClearinghouseState.json"), &cs); err != nil {
		t.Fatal(err)
	}
	var st ClearinghouseState
	if err := json.Unmarshal(cs.Data.ClearinghouseStates[0][1], &st); err != nil || len(st.AssetPositions) == 0 {
		t.Fatalf("ws clearinghouseState: %v", err)
	}
}

func TestWireCandleKeepsOpenTime(t *testing.T) {
	for _, rel := range []string{"info/candleSnapshot_BTC_1h.json", "../../candles/btc-15m.json"} {
		var wc []WireCandle
		if err := json.Unmarshal(readFx(t, rel), &wc); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if len(wc) == 0 {
			t.Fatalf("%s: empty", rel)
		}
		iv := domain.Interval(wc[0].Interval)
		step := iv.Duration().Milliseconds()
		for _, w := range wc {
			c := ToCandle(w)
			if c.T != w.OpenTime {
				t.Fatalf("%s: T %d != open %d", rel, c.T, w.OpenTime)
			}
			if c.T%step != 0 {
				t.Fatalf("%s: T %d not a multiple of %s", rel, c.T, iv)
			}
			if w.CloseTime != w.OpenTime+step-1 {
				t.Fatalf("%s: close %d", rel, w.CloseTime)
			}
		}
	}
}

func TestPortfolioUnmarshal(t *testing.T) {
	var p Portfolio
	if err := json.Unmarshal(readFx(t, "info/portfolio.json"), &p); err != nil {
		t.Fatal(err)
	}
	day, ok := p["day"]
	if !ok || len(day.AccountValues()) != 3 || day.AccountValues()[0].T != 1794723600052 {
		t.Fatalf("day: %+v", day)
	}
	if got := day.PnLs()[2].V; got != -222369.690162 {
		t.Fatalf("pnl %v", got)
	}
	// Object form also accepted.
	var q Portfolio
	if err := json.Unmarshal([]byte(`{"day":{"vlm":"1"}}`), &q); err != nil || q["day"].Vlm != "1" {
		t.Fatalf("object form: %v", err)
	}
	if err := json.Unmarshal([]byte(`[["day", 5]]`), &q); err == nil {
		t.Fatal("bad window accepted")
	}
}

func TestTupleRoundTrip(t *testing.T) {
	tb := TokenBorrowLend{Token: 3, State: BorrowLendTokenState{Supply: BorrowLendSide{Value: "1.5"}}}
	b, _ := json.Marshal(tb)
	var back TokenBorrowLend
	if err := json.Unmarshal(b, &back); err != nil || back.Token != 3 || back.State.Supply.Value != "1.5" {
		t.Fatalf("%s %v %+v", b, err, back)
	}
	pc := PerpCategory{Coin: "xyz:NVDA", Category: "stocks"}
	b, _ = json.Marshal(pc)
	var pc2 PerpCategory
	if err := json.Unmarshal(b, &pc2); err != nil || pc2 != pc {
		t.Fatalf("%s %v", b, err)
	}
}

func TestDecUnmarshal(t *testing.T) {
	var v struct{ A, B, C Dec }
	if err := json.Unmarshal([]byte(`{"A":"1.50","B":2.25,"C":null}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.A != "1.50" || v.B.Float() != 2.25 || v.C != "" || v.C.Float() != 0 {
		t.Fatalf("%+v", v)
	}
	d, err := v.A.Decimal()
	if err != nil || d.String() != "1.5" {
		t.Fatalf("%v %v", d, err)
	}
}
