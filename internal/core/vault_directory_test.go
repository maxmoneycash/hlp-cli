package core

import (
	"testing"

	"github.com/maxmoneycash/hlp/internal/domain"
)

func TestVaultDirectoryFiltersAreExplicit(t *testing.T) {
	rows := []domain.VaultSummary{
		{Address: "open", Name: "Shared open", TVL: 100_000, AllowDeposits: true},
		{Address: "paused", Name: "Paused", TVL: 20_000},
		{Address: "small", Name: "Small", TVL: 5, AllowDeposits: true},
		{Address: "closed", Name: "Shared closed", TVL: 90_000, Closed: true, AllowDeposits: true, Curated: true},
		{Address: "child", Name: "Child", TVL: 50_000, Rel: "child", AllowDeposits: true},
		{Address: "empty", Name: "Empty curated", Closed: true, Curated: true},
	}
	cases := []struct {
		name  string
		input VaultsInput
		want  []string
	}{
		{"unchanged CLI defaults", VaultsInput{}, []string{"open", "paused"}},
		{"explicit directory", VaultsInput{MinTVL: -1, IncludeClosed: true, IncludeHLP: true}, []string{"open", "closed", "child", "paused", "small", "empty"}},
		{"open wins", VaultsInput{MinTVL: -1, IncludeClosed: true, IncludeHLP: true, OpenOnly: true}, []string{"open", "child", "small"}},
		{"floor remains independent", VaultsInput{IncludeClosed: true}, []string{"open", "closed", "paused"}},
		{"curated includes closed and empty", VaultsInput{MinTVL: -1, IncludeClosed: true, IncludeHLP: true, CuratedOnly: true}, []string{"closed", "empty"}},
		{"name default prefers open", VaultsInput{Query: "Shared"}, []string{"open"}},
		{"name explicitly includes closed", VaultsInput{Query: "Shared", IncludeClosed: true, Sort: "tvl"}, []string{"open", "closed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FilterVaults(rows, tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d rows, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, want := range tc.want {
				if got[i].Address != want {
					t.Fatalf("row %d: got %s, want %s", i, got[i].Address, want)
				}
			}
		})
	}
}

func TestVaultDirectoryRetainsSparkProvenance(t *testing.T) {
	got := MergeVaults([]domain.VaultSummary{{Address: "a", TVL: 100}}, []domain.VaultSummary{{Address: "a",
		Spark: domain.Series{{T: 1, V: 1}}, SparkModel: domain.SPSummary, SPModel: domain.SPv6}})
	if len(got) != 1 || got[0].SparkModel != domain.SPSummary || got[0].SPModel != domain.SPv6 {
		t.Fatalf("merged row lost independent spark source: %+v", got)
	}
	detail := domain.VaultSummary{SPModel: domain.SPv6}
	mergeSummary(&detail, got[0])
	if detail.SparkModel != domain.SPSummary || len(detail.Spark) != 1 {
		t.Fatalf("detail fallback lost independent spark source: %+v", detail)
	}
	detail.SparkModel = domain.SPv6
	mergeSummary(&detail, got[0])
	if detail.SparkModel != domain.SPv6 {
		t.Fatalf("summary source replaced the detail's existing spark source: %+v", detail)
	}
}

func TestVaultDirectoryRiskSurvivesMerges(t *testing.T) {
	zero, month := 0.0, -0.003
	old := domain.VaultSummary{Address: "x", MaxDD: -0.5, Sharpe: 2}
	measured := domain.VaultSummary{Address: "x", Curated: true, MetricsAsOf: 123, RiskByWindow: map[domain.Window]domain.WindowRisk{
		domain.WAll: {MaxDD: &zero, Sharpe: &zero}, domain.W1M: {MaxDD: &month},
	}}
	list := MergeVaults([]domain.VaultSummary{old}, []domain.VaultSummary{measured})[0]
	named, direct := old, domain.VaultDetail{Summary: old}
	mergeSummary(&named, measured)
	mergeXYZ(&direct, domain.VaultDetail{Summary: measured})
	for _, row := range []domain.VaultSummary{list, named, direct.Summary} {
		if row.MaxDD != 0 || row.Sharpe != 0 || row.MetricsAsOf != 123 || row.RiskByWindow[domain.W1M].MaxDD == nil || *row.RiskByWindow[domain.W1M].MaxDD != month {
			t.Fatalf("lost measured risk: %+v", row)
		}
	}
}

func TestVaultScreeningUsesTheRequestedAllTimeRisk(t *testing.T) {
	zero := 0.0
	rows := []domain.VaultSummary{
		{Address: "unmeasured", TVL: 1},
		{Address: "sharpe-only", TVL: 1, Sharpe: 0.3},
		{Address: "zero-drawdown", TVL: 1, RiskByWindow: map[domain.Window]domain.WindowRisk{domain.WAll: {MaxDD: &zero}}},
		{Address: "zero-sharpe", TVL: 1, RiskByWindow: map[domain.Window]domain.WindowRisk{domain.WAll: {Sharpe: &zero}}},
	}
	filtered, unknown, err := filterVaults(rows, VaultsInput{MinTVL: -1, MaxDD: 10})
	if err != nil || unknown != 3 || len(filtered) != 1 || filtered[0].Address != "zero-drawdown" {
		t.Fatalf("max drawdown: %v unknown %d error %v", filtered, unknown, err)
	}
	sorted, _, err := filterVaults(rows, VaultsInput{MinTVL: -1, Sort: "sharpe"})
	if err != nil || sorted[0].Address != "sharpe-only" || sorted[1].Address != "zero-sharpe" {
		t.Fatalf("sharpe: %v error %v", sorted, err)
	}
}
