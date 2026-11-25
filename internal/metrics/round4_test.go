package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

func checkDetail(cs []domain.Check, id string) string {
	for _, c := range cs {
		if c.ID == id {
			return c.Detail
		}
	}
	return ""
}

// COMP #7a: the staleness line names the source's age, not the fetch time.
// COMP #7e: the Sharpe comparison fits a side-by-side column.
func TestChecklistStaleAgeAndShortSharpe(t *testing.T) {
	asOf := time.Date(2026, 11, 15, 12, 39, 0, 0, time.UTC)
	d := domain.VaultDetail{
		Summary: domain.VaultSummary{Address: "0x1", TVL: 1e6, AllowDeposits: true, Stale: true, UpdatedAt: asOf.Add(-3 * 24 * time.Hour)},
		Meta:    domain.Meta{AsOf: asOf, Stale: true},
		NAV:     domain.Series{{T: 0, V: 1}, {T: dayMs, V: 1.01}, {T: 2 * dayMs, V: 1.0}},
	}
	cs := Checklist(d, &domain.Metrics{Sharpe: 1.91})
	if got := checkDetail(cs, CheckDataFresh); got != "hlp.xyz metrics are stale (last updated 3d ago)" {
		t.Fatalf("fresh detail %q", got)
	}
	got := checkDetail(cs, CheckSharpeVsHLP)
	if !strings.HasSuffix(got, " Sharpe") || strings.Contains(got, "HLP") || len(got) > 24 {
		t.Fatalf("sharpe detail %q", got)
	}
}

// AGENT-UX #24: annualized numbers over a short window carry a warning.
func TestTearsheetShortWindowWarning(t *testing.T) {
	curve := domain.Series{}
	for i := range 30 {
		curve = append(curve, domain.Point{T: int64(i) * dayMs, V: 1 + float64(i)*0.01})
	}
	ts := Tearsheet(curve, TearsheetOpts{})
	if len(ts.Meta.Warnings) != 1 || !strings.Contains(ts.Meta.Warnings[0], "unreliable") {
		t.Fatalf("warnings %v", ts.Meta.Warnings)
	}
	long := domain.Series{{T: 0, V: 1}, {T: 200 * dayMs, V: 1.2}}
	if ts := Tearsheet(long, TearsheetOpts{}); len(ts.Meta.Warnings) != 0 {
		t.Fatalf("long window warned: %v", ts.Meta.Warnings)
	}
}
