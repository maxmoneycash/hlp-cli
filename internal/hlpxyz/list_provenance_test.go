package hlpxyz

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
)

func TestVaultSparkProvenanceIsIndependentOfStatus(t *testing.T) {
	const address = "0x01"
	now := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	summary := SummaryResponse{Vaults: []SummaryVault{{Address: address, PerfSeries: []PerfPoint{
		{Date: "2026-11-01", SharePrice: 1}, {Date: "2026-11-16T12:00:00.123Z", SharePrice: 1.2},
	}}}}
	status := map[string]V6Status{address: {}}
	got := MapVaultList(summary, status, nil, nil, now)[0]
	if got.SPModel != domain.SPv6 || got.SparkModel != domain.SPSummary || len(got.Spark) != 2 {
		t.Fatalf("summary spark hidden by v6 status or date-only sample dropped: %+v", got)
	}
	if got.Spark[0].T != time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("date-only source timestamp changed: %+v", got.Spark)
	}
	got = MapVaultList(summary, nil, map[string]domain.Series{address: {{T: now.Add(-time.Hour).UnixMilli(), V: 2}, {T: now.UnixMilli(), V: 2.1}}}, nil, now)[0]
	if got.SPModel != domain.SPSummary || got.SparkModel != domain.SPv6 || got.Spark[0].V != 2 {
		t.Fatalf("v6 spark mislabeled by missing status: %+v", got)
	}
}

func TestVaultSparkUsesSummaryHistoryWhenV6HasOnePoint(t *testing.T) {
	const address = "0x01"
	now := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	fresh := domain.Series{{T: now.UnixMilli(), V: 2}}
	cases := []struct {
		name    string
		points  []PerfPoint
		model   domain.SharePriceModel
		wantLen int
	}{
		{"dated history", []PerfPoint{{Date: "2026-11-01", SharePrice: 1}, {Date: "2026-11-16T12:00:00Z", SharePrice: 1.2}}, domain.SPSummary, 2},
		{"missing history", nil, domain.SPv6, 1},
		{"single older sample", []PerfPoint{{Date: "2026-11-01", SharePrice: 1}}, domain.SPv6, 1},
		{"unusable samples", []PerfPoint{{Date: "invalid", SharePrice: 1}, {Date: "2026-11-01", SharePrice: 0}}, domain.SPv6, 1},
		{"duplicate date is not history", []PerfPoint{{Date: "2026-11-01", SharePrice: 1}, {Date: "2026-11-01", SharePrice: 1.1}}, domain.SPv6, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			summary := SummaryResponse{Vaults: []SummaryVault{{Address: address, PerfSeries: tc.points}}}
			got := MapVaultList(summary, map[string]V6Status{address: {}}, map[string]domain.Series{address: fresh}, nil, now)[0]
			if got.SparkModel != tc.model || len(got.Spark) != tc.wantLen || got.SPModel != domain.SPv6 {
				t.Fatalf("spark selection/provenance changed: %+v", got)
			}
			if tc.model == domain.SPv6 && got.Spark[0] != fresh[0] {
				t.Fatalf("lost the original v6 observation: %+v", got.Spark)
			}
			if tc.model == domain.SPSummary && (got.Spark[1].V != 1.2 || got.Spark[1].T >= fresh[0].T) {
				t.Fatalf("summary fallback was spliced with the live observation: %+v", got.Spark)
			}
		})
	}
}

func TestV6WindowRiskFromCapturedStatus(t *testing.T) {
	var fixture struct {
		Statuses map[string]V6Status `json:"statuses"`
	}
	if err := json.Unmarshal(readFixture(t, "vaults_v6status.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	status := fixture.Statuses[HLPAddress]
	rows := MapVaultList(SummaryResponse{Vaults: []SummaryVault{{Address: HLPAddress}}}, fixture.Statuses, nil, nil, time.UnixMilli(status.ComputedAt))
	detail, err := ToDetail(DetailParts{Address: HLPAddress, Status: &status})
	if err != nil {
		t.Fatal(err)
	}
	row := rows[0]
	if !reflect.DeepEqual(row.RiskByWindow, detail.Summary.RiskByWindow) || len(row.RiskByWindow) != 8 || row.MetricsAsOf != status.ComputedAt {
		t.Fatalf("risk differs between directory and detail: %+v %+v", row, detail.Summary)
	}
	for _, tc := range []struct {
		window     domain.Window
		dd, sharpe float64
	}{
		{domain.W1D, -0.000008029759978944727, 0},
		{domain.W1M, -0.003120802946344408, 4.207514822521431},
		{domain.WAll, -0.05768302282132077, 7.171523811378771},
	} {
		risk := row.RiskByWindow[tc.window]
		if risk.MaxDD == nil || math.Abs(*risk.MaxDD-tc.dd) > 1e-12 || risk.Sharpe == nil || *risk.Sharpe != tc.sharpe {
			t.Fatalf("window %s: %+v", tc.window, risk)
		}
	}
	if row.MaxDD != *row.RiskByWindow[domain.WAll].MaxDD || row.Sharpe != *row.RiskByWindow[domain.WAll].Sharpe {
		t.Fatal("legacy scalars do not match all time")
	}
}

func TestV6WindowRiskKeepsZeroAndOmitsMissingValues(t *testing.T) {
	var status V6Status
	if err := json.Unmarshal([]byte(`{"maxDD":{"month":null,"week":0,"m3":null,"bad":10},"sharpe":{"month":0,"week":null,"m3":null}}`), &status); err != nil {
		t.Fatal(err)
	}
	row := domain.VaultSummary{}
	applyV6Risk(&row, status)
	b, err := json.Marshal(row.RiskByWindow)
	if err != nil || string(b) != `{"1m":{"sharpe":0},"1w":{"maxDD":0}}` {
		t.Fatalf("serialized risk %s (%v)", b, err)
	}
}
