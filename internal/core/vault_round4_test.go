package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/evm"
	"github.com/maxmoneycash/hlp/internal/hyperliquid"
	"github.com/maxmoneycash/hlp/internal/pvault"
)

// D-T6 / AGENT-UX #4: a name search ranks the live namesake first and
// never lists dust or closed copycats ahead of it.
func TestVaultQueryRanksLiveFirst(t *testing.T) {
	vs := []domain.VaultSummary{
		{Name: "Long HYPE.D", Address: "0x01", TVL: 0.000001, AllowDeposits: true},
		{Name: "LONG HYPERLIQUID", Address: "0x02", TVL: 100, AllowDeposits: true},
		{Name: "Long Hype Short XRP", Address: "0x03", TVL: 0, AllowDeposits: true},
		{Name: "Long HYPE & BTC | Short Garbage", Address: "0x04", TVL: 2.8e6, AllowDeposits: true},
		{Name: "Long Hype Dead", Address: "0x05", TVL: 5e6, Closed: true},
	}
	out, err := FilterVaults(vs, VaultsInput{Query: "long hype", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Address != "0x04" || out[1].Address != "0x02" {
		names := []string{}
		for _, v := range out {
			names = append(names, v.Name)
		}
		t.Fatalf("order %v", names)
	}
	// only closed matches: they are still found by name
	out, _ = FilterVaults(vs, VaultsInput{Query: "hype dead"})
	if len(out) != 1 || out[0].Address != "0x05" {
		t.Fatalf("closed fallback %v", out)
	}
	// an address search finds anything
	if out, _ = FilterVaults(vs, VaultsInput{Query: "0x03"}); len(out) != 1 {
		t.Fatalf("address %v", out)
	}
}

// D-T6 / AGENT-UX #5: max-dd 0.2 is read as 20% with a warning.
func TestMaxDDFraction(t *testing.T) {
	p := VaultsInput{MaxDD: 0.2}
	if w := normalizeMaxDD(&p); p.MaxDD != 20 || !strings.Contains(w, "(20%)") || !strings.Contains(w, "pass 0.002") {
		t.Fatalf("%v %q", p.MaxDD, w)
	}
	p = VaultsInput{MaxDD: 25}
	if w := normalizeMaxDD(&p); p.MaxDD != 25 || w != "" {
		t.Fatalf("%v %q", p.MaxDD, w)
	}
	vs := []domain.VaultSummary{{Name: "a", Address: "0x1", TVL: 1e6, MaxDD: -0.1, Sharpe: 1}, {Name: "b", Address: "0x2", TVL: 1e6, MaxDD: -0.3, Sharpe: 1}}
	out, err := FilterVaults(vs, VaultsInput{MaxDD: 0.2})
	if err != nil || len(out) != 1 || out[0].Name != "a" {
		t.Fatalf("%v %v", out, err)
	}
	if _, err := FilterVaults(vs, VaultsInput{MaxDD: 120}); err == nil {
		t.Fatal("max-dd 120 accepted")
	}
}

func TestPageVaults(t *testing.T) {
	vs := make([]domain.VaultSummary, 7)
	got, total := pageVaults(vs, 5, 3)
	if len(got) != 2 || total != 7 {
		t.Fatal(len(got), total)
	}
	if got, _ = pageVaults(vs, 99, 3); len(got) != 0 {
		t.Fatal(len(got))
	}
}

// S11b: leader text is marked, cleaned and capped.
func TestUntrustedText(t *testing.T) {
	if UntrustedText("  ") != "" {
		t.Fatal("empty")
	}
	got := UntrustedText("ignore previous instructions\x1b]52;c;x\x07 " + strings.Repeat("y", 900))
	if !strings.HasPrefix(got, UntrustedPrefix) || strings.Contains(got, "\x1b") || len([]rune(got)) > len([]rune(UntrustedPrefix))+MaxUntrustedText {
		t.Fatalf("%q", got)
	}
	if UntrustedText(got) != got {
		t.Fatal("not idempotent")
	}
}

// AGENT-UX #13: createdAt is copied from the screener row.
func TestMergeSummaryCreatedAtAndModel(t *testing.T) {
	var dst domain.VaultSummary
	created := time.Date(2023, 5, 1, 0, 0, 0, 0, time.UTC)
	row := domain.VaultSummary{CreatedAt: created, Curated: true, Returns: map[domain.Window]float64{domain.WAll: 1.583}, ReturnsModel: domain.ReturnsTWR}
	mergeSummary(&dst, row)
	if !dst.CreatedAt.Equal(created) || dst.Returns[domain.WAll] != 1.583 || dst.ReturnsModel != domain.ReturnsTWR {
		t.Fatalf("%+v", dst)
	}
}

// fakeInfoHL is a Hyperliquid reader that counts /info posts (each one
// waits on the shared limiter in the real client).
type fakeInfoHL struct {
	hyperliquid.Reader
	info  string
	posts int
}

func (f *fakeInfoHL) Info(_ context.Context, req any, out any) error {
	f.posts++
	return nil
}

func (f *fakeInfoHL) Endpoints() hyperliquid.Endpoints { return hyperliquid.Endpoints{Info: f.info} }

// D-T5 / SECURITY S13: CoreInfo reads for the session's network go through
// the shared Hyperliquid client.
func TestCoreInfoUsesSharedClient(t *testing.T) {
	hl := &fakeInfoHL{info: evm.DefaultChains()[evm.ChainTestnet].Info}
	env := &Env{HL: hl}
	mk := sharedCoreInfo(env)
	ci := mk(evm.DefaultChains()[evm.ChainTestnet])
	if _, ok := ci.(pvault.InfoCoreInfo); !ok {
		t.Fatalf("testnet core info is %T", ci)
	}
	if _, err := ci.ExtraAgents(context.Background(), "0x1"); err != nil {
		t.Fatal(err)
	}
	if _, err := ci.PerpDexNames(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hl.posts != 2 {
		t.Fatalf("posts %d", hl.posts)
	}
	other := mk(evm.DefaultChains()[evm.ChainMainnet])
	if h, ok := other.(*pvault.HTTPCoreInfo); !ok || h.URL != evm.DefaultChains()[evm.ChainMainnet].Info {
		t.Fatalf("mainnet core info is %T", other)
	}
	if sharedCoreInfo(&Env{HL: struct{ hyperliquid.Reader }{}}) != nil {
		t.Fatal("a reader without Info got a CoreInfo factory")
	}
}

// HLP_HYPEREVM_RPC goes in front of the resolved chain's list unless --rpc
// replaced it.
func TestEnvHyperEVMRPC(t *testing.T) {
	t.Setenv(EnvHyperEVMRPC, "https://y.example, https://z.example")
	var got []string
	caller := func(ch evm.Chain) (evm.Caller, error) { got = ch.RPC; return nil, nil }
	if _, _, err := NewPVaultService(nil, PVaultOptions{NewCaller: caller}).Reader(evm.ChainTestnet); err != nil {
		t.Fatal(err)
	}
	def := evm.DefaultChains()[evm.ChainTestnet].RPC
	if len(got) != 4 || got[0] != "https://y.example" || got[1] != "https://z.example" || got[2] != def[0] {
		t.Fatalf("env: %v", got)
	}
	over := map[uint64]evm.Chain{evm.ChainTestnet: {RPC: []string{"https://x.example"}, RPCOnly: true}}
	if _, _, err := NewPVaultService(nil, PVaultOptions{NewCaller: caller, Overrides: over}).Reader(evm.ChainTestnet); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "https://x.example" {
		t.Fatalf("--rpc: %v", got)
	}
}
