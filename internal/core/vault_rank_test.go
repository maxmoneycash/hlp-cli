package core

import (
	"testing"

	"github.com/maxmoneycash/hlp/internal/domain"
)

func TestRankVaultMatchesPrefersLiveVaults(t *testing.T) {
	ms := []domain.VaultSummary{
		{Name: "GrowiHF", Address: "0x36cc", Closed: true},
		{Name: "Growi HF", Address: "0xaaaa", TVL: 12.2e6},
		{Name: "Growth Vault", Address: "0xbbbb", TVL: 50e6},
	}
	got := rankVaultMatches(ms, "growi")
	if got[0].Address != "0xaaaa" || got[1].Address != "0x36cc" || got[2].Address != "0xbbbb" {
		t.Fatalf("order %v %v %v", got[0].Name, got[1].Name, got[2].Name)
	}
	// an exact name still wins over a bigger namesake
	got = rankVaultMatches(ms, "GrowiHF")
	if got[0].Address != "0x36cc" {
		t.Fatalf("exact name lost: %v", got[0].Name)
	}
	if normName("Growi HF!") != "growihf" {
		t.Fatal("normName")
	}
}

func TestFilterVaultsUnknownRiskMetrics(t *testing.T) {
	vs := []domain.VaultSummary{
		{Name: "curated", Address: "0x1", TVL: 1e6, MaxDD: -0.10, Sharpe: 2, AllowDeposits: true},
		{Name: "deep", Address: "0x2", TVL: 1e6, MaxDD: -0.50, Sharpe: 1, AllowDeposits: true},
		{Name: "unknown", Address: "0x3", TVL: 5e6, AllowDeposits: true},
	}
	out, unknown, err := filterVaults(vs, VaultsInput{MaxDD: 20})
	if err != nil || len(out) != 1 || out[0].Name != "curated" || unknown != 1 {
		t.Fatalf("max-dd filter: %v unknown=%d %v", out, unknown, err)
	}
	for _, sortKey := range []string{"sharpe", "maxdd"} {
		out, _, _ = filterVaults(vs, VaultsInput{Sort: sortKey})
		if out[len(out)-1].Name != "unknown" {
			t.Fatalf("%s: unknown metrics must sort last, got %v", sortKey, out)
		}
	}
}
