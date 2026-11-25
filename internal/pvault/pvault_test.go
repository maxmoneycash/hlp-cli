package pvault

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/sha3"

	"github.com/maxmoneycash/hlp/internal/evm"
)

func keccakSel(sig string) evm.Selector {
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte(sig))
	var s evm.Selector
	copy(s[:], h.Sum(nil)[:4])
	return s
}

func TestSelectorsMatchKeccak(t *testing.T) {
	for sel, sig := range selectorSignatures {
		if got := keccakSel(sig); got != sel {
			t.Errorf("%s: hard-coded %s, keccak %s", sig, sel, got)
		}
	}
	for sel, sig := range errorSigs {
		if got := keccakSel(sig); got.Hex() != sel {
			t.Errorf("error %s: table %s, keccak %s", sig, sel, got)
		}
	}
}

func TestGoldenCalldata(t *testing.T) {
	v := evm.MustAddress("0x688dD654bDbDE2BF8b6154685437f3F9dB2eAABd")
	if got := evm.EncodeHex(evm.Calldata(selVaultState, evm.AddressWord(v))); got != "0x54e98549000000000000000000000000688dd654bdbde2bf8b6154685437f3f9db2eaabd" {
		t.Fatal(got)
	}
	s := loadSnapshot(t)
	got := evm.EncodeHex(evm.Calldata(selEpochRange, evm.AddressWord(v), evm.Uint64Word(438), evm.Uint64Word(442)))
	if got != s.LensRange.Calldata {
		t.Fatalf("epochRange calldata %s", got)
	}
}

func TestDecodeLensVaultState(t *testing.T) {
	s := loadSnapshot(t)
	raw, _ := evm.DecodeHex(s.LensVault.Result)
	st, err := DecodeVaultState(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(st)
	var g, want map[string]any
	_ = json.Unmarshal(got, &g)
	_ = json.Unmarshal(s.LensVault.Named, &want)
	for k, w := range want {
		gv := g[k]
		if strings.TrimSuffix(jsonStr(gv), ".0") != strings.TrimSuffix(jsonStr(w), ".0") {
			t.Errorf("%s: got %v want %v", k, gv, w)
		}
	}
	// identity: totalAssets = idle + coreSpot + corePerpEquity + inFlight
	sum := new(big.Int).Add(st.IdleAssets.V(), st.CoreSpot.V())
	sum.Add(sum, st.CorePerpEquity.V()).Add(sum, st.InFlightToCore.V())
	if sum.Cmp(st.TotalAssets.V()) != 0 {
		t.Fatalf("NAV identity: %s != %s", sum, st.TotalAssets)
	}
	if st.Phase.String() != "open" {
		t.Fatal(st.Phase)
	}
	if _, err := DecodeVaultState(raw[:len(raw)-32]); err == nil {
		t.Fatal("27 words accepted")
	}
	if _, err := DecodeVaultState(append(raw, make([]byte, 32)...)); err == nil {
		t.Fatal("29 words accepted")
	}
}

func jsonStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return big.NewFloat(x).Text('f', -1)
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func TestNegativeCorePerpEquity(t *testing.T) {
	raw := make([]byte, 28*32)
	for i := 13 * 32; i < 14*32; i++ {
		raw[i] = 0xff
	}
	raw[14*32-1] = 0x9c // -100
	st, err := DecodeVaultState(raw)
	if err != nil {
		t.Fatal(err)
	}
	if st.CorePerpEquity.Int64() != -100 {
		t.Fatal(st.CorePerpEquity)
	}
	ts := BuildTearsheet(Inputs{State: st})
	if ts.CorePerpEquityUSD != -0.0001 {
		t.Fatal(ts.CorePerpEquityUSD)
	}
}

func TestDecodeUserStates(t *testing.T) {
	s := loadSnapshot(t)
	for who, c := range s.LensUser {
		raw, _ := evm.DecodeHex(c.Result)
		u, err := DecodeUserState(raw)
		if err != nil {
			t.Fatal(who, err)
		}
		var want struct {
			Shares     json.Number `json:"shares"`
			MayDeposit bool        `json:"mayDeposit"`
		}
		d := json.NewDecoder(strings.NewReader(string(c.Named)))
		d.UseNumber()
		_ = d.Decode(&want)
		if u.Shares.String() != want.Shares.String() || u.MayDeposit != want.MayDeposit {
			t.Errorf("%s: %s/%v want %s/%v", who, u.Shares, u.MayDeposit, want.Shares, want.MayDeposit)
		}
	}
	if _, err := DecodeUserState(make([]byte, 5*32)); err == nil {
		t.Fatal("short userState accepted")
	}
}

func TestDecodeEpochRangeAndData(t *testing.T) {
	s := loadSnapshot(t)
	raw, _ := evm.DecodeHex(s.LensRange.Result)
	es, err := DecodeEpochRange(438, raw)
	if err != nil || len(es) != 5 {
		t.Fatal(len(es), err)
	}
	var named []EpochData
	_ = json.Unmarshal(s.LensRange.Named, &named)
	for i := range es {
		if es[i].SharePrice.Cmp(named[i].SharePrice.V()) != 0 || es[i].SettledAt != named[i].SettledAt || es[i].Epoch != uint64(438+i) {
			t.Errorf("epoch %d mismatch", 438+i)
		}
	}
	if _, err := DecodeEpochRange(438, raw[:len(raw)-32]); err == nil {
		t.Fatal("truncated range accepted")
	}
	raw3, _ := evm.DecodeHex(s.Calls["epochData(3)"].Result)
	e3, err := DecodeEpochData(3, raw3)
	if err != nil {
		t.Fatal(err)
	}
	if e3.RedeemAssets.Int64() != 251003000 || e3.FeeShares.String() != "10949503167989944" || e3.SettledAt != 1794447215 {
		t.Fatalf("%+v", e3)
	}
	name, _ := evm.DecodeHex(s.Calls["name()"].Result)
	if n, err := DecodeString(name); err != nil || n != "Trend Pyramid (testnet)" {
		t.Fatal(n, err)
	}
	pt, _ := evm.DecodeHex(s.Calls["pendingTransfer()"].Result)
	if p, err := DecodePendingTransfer(pt); err != nil || p.Kind != TransferNone {
		t.Fatal(p, err)
	}
}

// TestFeeFormulaOnHistory replays the performance fee over all 443 epochs:
// S = previous supply, A = NAV before settle implied by the settle price,
// H ratchets on fee epochs. Fee assets must match within 2 micro-USDC on
// at least 95% of fee-bearing settles (the research saw 415/441 exact).
func TestFeeFormulaOnHistory(t *testing.T) {
	es := loadEpochs(t)
	H := new(big.Int).Set(evm.Pow10(18))
	checked, ok := 0, 0
	for i := 1; i < len(es); i++ {
		prev, e := es[i-1], es[i]
		S := prev.TotalSupplyAfter.V()
		fs := e.FeeShares.V()
		// A = price × (S + fee) / 1e30, the pre-flow NAV at settle.
		A := new(big.Int).Mul(e.SharePrice.V(), new(big.Int).Add(S, fs))
		A.Quo(A, conv)
		r := PerformanceFee(A, S, H, 1000)
		if fs.Sign() > 0 || r.FeeShares.Sign() > 0 {
			checked++
			// compare fee in USDC terms: shares × price / 1e30
			got := new(big.Int).Quo(new(big.Int).Mul(r.FeeShares, e.SharePrice.V()), conv)
			want := new(big.Int).Quo(new(big.Int).Mul(fs, e.SharePrice.V()), conv)
			if d := new(big.Int).Sub(got, want); d.CmpAbs(big.NewInt(2)) <= 0 {
				ok++
			}
		}
		if fs.Sign() > 0 {
			H = new(big.Int).Set(e.SharePrice.V())
		}
	}
	if checked < 60 {
		t.Fatalf("only %d fee epochs checked", checked)
	}
	if float64(ok) < 0.95*float64(checked) {
		t.Fatalf("fee formula matched %d/%d", ok, checked)
	}
	t.Logf("fee formula matched %d/%d fee-bearing settles", ok, checked)
	if H.String() != "1061179551356238847" {
		t.Errorf("replayed HWM %s, live 1061179551356238847", H)
	}
}

func TestPreviews(t *testing.T) {
	// epoch 2: 50 shares at 0.999940010376212697 paid 49.997000 (no exit fee)
	px := bigStr(t, "999940010376212697")
	got := PreviewRedeem(bigStr(t, "50000000000000000000"), px, 0)
	if got.Int64() != 49997000 {
		t.Fatal(got)
	}
	if v := PreviewRedeem(bigStr(t, "50000000000000000000"), px, 100); v.Int64() != 49497030 {
		t.Fatal(v)
	}
	if s := PreviewDeposit(big.NewInt(1_000_000), evm.Pow10(18)); s.String() != "1000000000000000000" {
		t.Fatal(s)
	}
	if PreviewDeposit(big.NewInt(1), new(big.Int)).Sign() != 0 {
		t.Fatal("zero price")
	}
}

func testService(t *testing.T, fc *fakeCaller, chains map[uint64]evm.Chain) *Service {
	return NewService(Options{
		Chains:    chains,
		Telemetry: newFakeTelemetry(t),
		Now:       func() time.Time { return time.Unix(1794744506, 0) },
		NewCaller: func(evm.Chain) (evm.Caller, error) { return fc, nil },
		NewCore:   func(evm.Chain) CoreInfo { return newFakeCore(t) },
	})
}

func TestServiceTearsheetLens(t *testing.T) {
	fc := newFakeCaller(t)
	s := testService(t, fc, nil)
	ts, m, err := s.Tearsheet(context.Background(), TearsheetInput{Vault: "trend-pyramid", User: "0xF2129a3b4b3594b28DEF2C4B5Cefc2d1Be3356A4", Positions: true})
	if err != nil {
		t.Fatal(err)
	}
	if ts.Source != SourceLens || ts.ChainID != 998 || ts.Name != "Trend Pyramid (testnet)" || ts.Symbol != "hlpTP" {
		t.Fatalf("%+v", ts)
	}
	if math.Abs(ts.NavUSD-349.118344) > 1e-9 || math.Abs(ts.SharePrice-1.026117759562622343) > 1e-12 {
		t.Fatal(ts.NavUSD, ts.SharePrice)
	}
	if ts.Fees.PerformanceBps != 1000 || ts.Roles.Owner != "0xf2129a3b4b3594b28def2c4b5cefc2d1be3356a4" || ts.Roles.Keeper == "" {
		t.Fatalf("%+v %+v", ts.Fees, ts.Roles)
	}
	if ts.DeployLimit != 174.366791 || ts.DrawdownVsHWM >= 0 || ts.SecondsToCutoff != 238 {
		t.Fatal(ts.DeployLimit, ts.DrawdownVsHWM, ts.SecondsToCutoff)
	}
	if len(ts.Epochs) != 12 || ts.Epochs[11].Epoch != 443 {
		t.Fatalf("epochs %d", len(ts.Epochs))
	}
	if ts.Config == nil || ts.Config.EpochDurationSec != 600 || len(ts.Config.NavDexes) != 2 {
		t.Fatalf("%+v", ts.Config)
	}
	if !contains(ts.Unknown, "pausedUntil()") || ts.PausedUntil != nil {
		t.Fatal("pausedUntil should be unknown", ts.Unknown)
	}
	if ts.Bot == nil || len(ts.Bot.Book) != 4 || ts.Keeper == nil || ts.Agent == nil || ts.Agent.Source != "hyperliquid" {
		t.Fatalf("telemetry %+v %+v %+v", ts.Bot, ts.Keeper, ts.Agent)
	}
	if len(ts.History) < 2000 || len(ts.Positions) != 3 {
		t.Fatal(len(ts.History), len(ts.Positions))
	}
	if ts.User == nil || math.Abs(ts.User.Shares-14.993484508119393721) > 1e-9 || ts.User.ValueUSD <= 15 {
		t.Fatalf("%+v", ts.User)
	}
	if m.Network != "testnet" || m.Source != "hyperevm+hlp.xyz" || m.Partial {
		t.Fatalf("%+v", m)
	}
	if fc.calls > 80 {
		t.Fatalf("%d calls", fc.calls)
	}
	b, err := json.Marshal(ts)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"navUsd"`, `"sharePrice"`, `"highWaterMark"`, `"cutoffAt"`, `"settleDeadline"`, `"deployLimit"`, `"idleUsd"`, `"corePerpEquityUsd"`, `"managementBps"`, `"feeRecipient"`, `"source":"lens"`, `"chainId":998`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("json missing %s", k)
		}
	}
}

func TestServiceTearsheetWithoutLens(t *testing.T) {
	fc := newFakeCaller(t)
	fc.noLens = true
	chains := evm.DefaultChains()
	c := chains[998]
	c.Lens = ""
	chains[998] = c
	s := testService(t, fc, chains)
	ts, _, err := s.Tearsheet(context.Background(), TearsheetInput{Vault: "0x688dD654bDbDE2BF8b6154685437f3F9dB2eAABd", NoHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	if ts.Source != SourceVaultViews {
		t.Fatal(ts.Source)
	}
	if math.Abs(ts.NavUSD-349.116744) > 1e-9 || math.Abs(ts.CorePerpEquityUSD-306.751387) > 1e-9 {
		t.Fatal(ts.NavUSD, ts.CorePerpEquityUSD)
	}
	if !contains(ts.Unknown, "deployLimit") || ts.Epoch != 444 || ts.SettleDeadline != ts.CutoffAt+600 {
		t.Fatal(ts.Unknown, ts.Epoch)
	}
	if ts.ManagerShares != 200 {
		t.Fatal(ts.ManagerShares)
	}
	if len(ts.Epochs) == 0 {
		t.Fatal("no epochs via epochData")
	}
}

func TestServiceEpochsAndUser(t *testing.T) {
	s := testService(t, newFakeCaller(t), nil)
	tab, _, err := s.Epochs(context.Background(), EpochsInput{From: 1, To: 443})
	if err != nil {
		t.Fatal(err)
	}
	if len(tab.Epochs) != 443 || tab.MedianGapSec != 660 || tab.FeeShares < 2.2 || tab.FeeShares > 2.3 {
		t.Fatal(len(tab.Epochs), tab.MedianGapSec, tab.FeeShares)
	}
	tab, _, err = s.Epochs(context.Background(), EpochsInput{})
	if err != nil || len(tab.Epochs) != 50 || tab.To != 443 {
		t.Fatal(err, len(tab.Epochs), tab.To)
	}
	u, _, err := s.User(context.Background(), UserInput{User: "0x7ffb5ea882eec736200c51209d668a6972092000"})
	if err != nil || math.Abs(u.Shares-124.238755622576931285) > 1e-9 || !u.MayDeposit {
		t.Fatal(u, err)
	}
	if _, _, err := s.User(context.Background(), UserInput{User: "nope"}); err == nil {
		t.Fatal("bad user accepted")
	}
}

func TestResolve(t *testing.T) {
	s := testService(t, newFakeCaller(t), nil)
	ctx := context.Background()
	c, a, err := s.Resolve(ctx, "", 0)
	if err != nil || c != 998 || a.Hex() != "0x688dd654bdbde2bf8b6154685437f3f9db2eaabd" {
		t.Fatal(c, a, err)
	}
	if _, _, err := s.Resolve(ctx, "hlptp", 0); err != nil {
		t.Fatal(err)
	}
	var ie *InputError
	if _, _, err := s.Resolve(ctx, "nosuch", 0); !errors.As(err, &ie) {
		t.Fatal(err)
	}
	if _, _, err := s.Resolve(ctx, "0x12", 0); !errors.As(err, &ie) {
		t.Fatal(err)
	}
	if _, _, err := s.Tearsheet(ctx, TearsheetInput{Vault: "0x0000000000000000000000000000000000000001"}); !errors.As(err, &ie) {
		t.Fatal("codeless address should be a usage error", err)
	}
	if _, _, err := s.Tearsheet(ctx, TearsheetInput{Chain: 12345}); !errors.As(err, &ie) {
		t.Fatal("unknown chain", err)
	}
}

func TestUSDCUnits(t *testing.T) {
	for in, want := range map[string]int64{"306.751387": 306751387, "0.0": 0, "-1.5": -1500000, "1.0000005": 1000001} {
		if got := usdcUnits(in); got.Int64() != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

// TestLiveTearsheet makes a handful of public read-only requests
// (testnet RPC batch + hlp.xyz). Run with HLP_LIVE=1.
func TestLiveTearsheet(t *testing.T) {
	if os.Getenv("HLP_LIVE") != "1" {
		t.Skip("set HLP_LIVE=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s := NewService(Options{UserAgent: "hlp-cli/test"})
	ts, m, err := s.Tearsheet(ctx, TearsheetInput{Vault: "trend-pyramid", Epochs: 3, NoHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	if ts.Source != SourceLens || ts.NavUSD <= 0 || ts.SharePrice <= 0 || ts.Symbol == "" {
		t.Fatalf("%+v", ts)
	}
	t.Logf("nav %.2f price %.6f epoch %d phase %s unknown %v warnings %v", ts.NavUSD, ts.SharePrice, ts.Epoch, ts.Phase, ts.Unknown, m.Warnings)
}
