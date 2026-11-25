package metrics

import (
	"encoding/json"
	"testing"

	"github.com/maxmoneycash/hlp/internal/domain"
)

func TestTradeMetricZeroPresenceSurvivesJSON(t *testing.T) {
	for _, tc := range []struct {
		name        string
		pnl         []float64
		win, factor *float64
	}{
		{"unavailable", nil, nil, nil},
		{"all losses", []float64{-10, -20}, ptr(0), ptr(0)},
		{"all wins", []float64{10, 20}, ptr(1), nil},
		{"mixed", []float64{-10, 20}, ptr(0.5), ptr(2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Compute(nil, tc.pnl)
			for round := 0; round < 2; round++ {
				b, err := json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]float64
				if err := json.Unmarshal(b, &fields); err != nil {
					t.Fatal(err)
				}
				for name, want := range map[string]*float64{"winRate": tc.win, "profitFactor": tc.factor} {
					got, present := fields[name]
					if present != (want != nil) || want != nil && got != *want {
						t.Fatalf("round %d %s got %v (present=%v), want %v", round, name, got, present, want)
					}
				}
				m = domain.Metrics{}
				if err := json.Unmarshal(b, &m); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func ptr(v float64) *float64 { return &v }
