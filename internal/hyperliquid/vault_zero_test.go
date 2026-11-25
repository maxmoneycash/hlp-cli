package hyperliquid

import (
	"testing"
	"time"
)

func TestVaultDetailMarksObservedZeroEquity(t *testing.T) {
	vd := VaultDetails{Portfolio: Portfolio{"allTime": {AccountValueHistory: [][2]any{{float64(1000), "0"}}}}}
	got := VaultDetailFrom(vd, time.Now())
	if got.Summary.TVL != 0 || !got.Summary.TVLKnown {
		t.Fatalf("observed zero treated as absent: %+v", got.Summary)
	}
	got = VaultDetailFrom(VaultDetails{}, time.Now())
	if got.Summary.TVLKnown {
		t.Fatal("absent portfolio marked as known equity")
	}
}
