package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp/internal/domain"
)

func TestVaultSummaryMergesPreserveKnownZero(t *testing.T) {
	stale := domain.VaultSummary{Address: "0x01", TVL: 100, TVLKnown: true}
	zero := domain.VaultSummary{Address: stale.Address, TVLKnown: true}
	got := MergeVaults([]domain.VaultSummary{zero}, []domain.VaultSummary{stale})
	if len(got) != 1 || got[0].TVL != 0 || !got[0].TVLKnown {
		t.Fatalf("known zero overwritten by curated summary: %+v", got)
	}
	mergeSummary(&zero, stale)
	if zero.TVL != 0 || !zero.TVLKnown {
		t.Fatalf("known detail zero overwritten: %+v", zero)
	}
	var unknown domain.VaultSummary
	mergeSummary(&unknown, stale)
	if unknown.TVL != 100 || !unknown.TVLKnown {
		t.Fatalf("missing detail did not inherit source presence: %+v", unknown)
	}
	got = MergeVaults([]domain.VaultSummary{{Address: stale.Address}}, []domain.VaultSummary{stale})
	if len(got) != 1 || got[0].TVL != 100 || !got[0].TVLKnown {
		t.Fatalf("missing list row did not inherit source presence: %+v", got)
	}
	b, err := json.Marshal(zero)
	if err != nil || strings.Contains(string(b), "TVLKnown") || strings.Contains(string(b), "tvlKnown") {
		t.Fatalf("internal presence leaked into contract: %s %v", b, err)
	}
}
