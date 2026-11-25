package cards

import (
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/hlpxyz"
)

// COMP #8: HLP's ∞ alone does not keep a column of dashes.
func TestRoomColumnNeedsHalfKnown(t *testing.T) {
	vs := []domain.VaultSummary{{Name: "Hyperliquidity Provider (HLP)", Rel: "parent", TVL: 1e8}}
	for i := range 7 {
		vs = append(vs, domain.VaultSummary{Name: "Vault " + string(rune('A'+i)), Address: "0x" + string(rune('a'+i)), TVL: 1e6})
	}
	if out := VaultTable(vs, Opts{Width: 160, Plain: true}, VaultTableOpts{Selected: -1}); strings.Contains(out, "ROOM") {
		t.Fatalf("room column kept:\n%s", out)
	}
	for i := 1; i < 5; i++ {
		vs[i].LeaderFraction = 0.2
	}
	if out := VaultTable(vs, Opts{Width: 160, Plain: true}, VaultTableOpts{Selected: -1}); !strings.Contains(out, "ROOM") {
		t.Fatalf("room column dropped:\n%s", out)
	}
}

// COMP #7d: "Other" sorts last and is always kept.
func TestExposureOtherLast(t *testing.T) {
	p := newPainter(Opts{Width: 80, Plain: true}.normalized())
	by := map[string]float64{"Other": 5.18e6, "BTC": 2e6, "ETH": 1e6, "SOL": 0.5e6}
	lines := p.exposureBars(by, 1e7, 80, 3)
	body := strings.Join(lines, "\n")
	iBTC, iOther := strings.Index(body, "BTC"), strings.Index(body, "Other")
	if iBTC < 0 || iOther < 0 || iOther < iBTC || strings.Contains(body, "SOL") {
		t.Fatalf("order:\n%s", body)
	}
}

// COMP #7c: a benchmark far outside the vault's range leaves the overlay.
func TestCurveDropsOffScaleBenchmark(t *testing.T) {
	p := newPainter(Opts{Width: 80, Plain: true}.normalized())
	curve := domain.Series{{T: 0, V: 1}, {T: 1, V: 1.78}}
	benches := []domain.Benchmark{
		{Name: "HLP", Curve: domain.Series{{T: 0, V: 1}, {T: 1, V: 1.2}}},
		{Name: "HYPE", Curve: domain.Series{{T: 0, V: 1}, {T: 1, V: 7.64}, {T: 2, V: 5.36}}},
		{Name: "BTC", Curve: domain.Series{{T: 0, V: 1}, {T: 1, V: 1.4}}},
	}
	out := strings.Join(p.curveBlock("Growi", curve, benches, 8), "\n")
	if !strings.Contains(out, "OFF SCALE HYPE +436%") {
		t.Fatalf("no off-scale note:\n%s", out)
	}
	// the note uses the end return (the table's number), not the peak
	if strings.Contains(out, "664") {
		t.Fatalf("off-scale note:\n%s", out)
	}
	p.o.Compact = true
	if out := strings.Join(p.curveBlock("Growi", curve, benches, 8), "\n"); strings.Contains(out, "OFF SCALE") {
		t.Fatalf("compact card keeps the note:\n%s", out)
	}
	if swing(curve) != 0.78 || farthest(benches[1].Curve) != 7.64 {
		t.Fatal(swing(curve))
	}
}

func TestIndexDataCard(t *testing.T) {
	list := &core.IndexData{Indexes: []hlpxyz.Index{{Def: hlpxyz.IndexDef{Slug: "hlp20", Name: "HLP 20"}}}}
	out := Render(&core.Result{Card: core.CardIndex, Title: "Vault indexes", Data: list}, Opts{Width: 80, Plain: true})
	if !strings.Contains(out, "HLP 20") || !strings.Contains(strings.SplitN(out, "\n", 2)[0], "1") {
		t.Fatalf("list:\n%s", out)
	}
	one := &core.IndexData{Index: &core.IndexView{Index: hlpxyz.Index{Def: hlpxyz.IndexDef{Slug: "m", Description: "Top vaults."}},
		Members: []core.IndexMember{{Address: "0xabc", Name: "Growi HF", Weight: 0.5}}}}
	out = Render(&core.Result{Card: core.CardIndex, Title: "Mavericks", Data: one}, Opts{Width: 80, Plain: true})
	if !strings.Contains(out, "Growi HF") || !strings.Contains(out, "Top vaults.") {
		t.Fatalf("one:\n%s", out)
	}
}

func TestVaultDescriptionHidesMarker(t *testing.T) {
	d := domain.VaultDetail{Summary: domain.VaultSummary{Name: "X", Address: "0x1"}, Description: core.UntrustedText("Momentum on majors.")}
	out := Render(&core.Result{Card: core.CardVault, Title: "X", Data: d}, Opts{Width: 100, Plain: true})
	if !strings.Contains(out, "Momentum on majors.") || strings.Contains(out, core.UntrustedPrefix) {
		t.Fatalf("\n%s", out)
	}
}
