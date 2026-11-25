package core

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hlpxyz"
	"github.com/maxmoneycash/hlp/internal/metrics"
)

// Expected values come from the reviewed original public ne/wt functions,
// run offline over fixed dates and captured input curves, not this Go port.
func TestWebsiteStatisticsMatchDatedPublicFunctionOutputs(t *testing.T) {
	data, err := os.ReadFile("testdata/website-index-risk-v6.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference struct {
		Cases []struct {
			Name                string                 `json:"name"`
			Start               int64                  `json:"start"`
			End                 int64                  `json:"end"`
			IncludePreInception bool                   `json:"includePreInception"`
			ValuationSamples    int                    `json:"valuationSamples"`
			Expected            IndexWebsiteStatistics `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatal(err)
	}
	f, history, spark, now := readWebsiteCapture(t)
	curves, _, _ := websiteCurveSet(history, spark, nil, now)
	for _, tt := range reference.Cases {
		t.Run(tt.Name, func(t *testing.T) {
			ix := f.Index
			if tt.IncludePreInception {
				ix.Allocs = append(append([]hlpxyz.IndexAlloc{}, ix.Def.PreAllocs...), ix.Allocs...)
			}
			nav, _, _, missing, _ := replayWebsiteIndex(ix, curves, tt.Start, tt.End, websiteStep(tt.End-tt.Start))
			if len(missing) != 0 {
				t.Fatal(missing)
			}
			got := websiteIndexStatistics(nav)
			if got == nil || got.Start != tt.Start || got.End != tt.End || got.ValuationSamples != tt.ValuationSamples {
				t.Fatalf("wrong dated grid: %+v", got)
			}
			near := func(name string, actual, expected float64) {
				t.Helper()
				if math.Abs(actual-expected) > 1e-10 {
					t.Errorf("%s got %.14g want %.14g", name, actual, expected)
				}
			}
			near("return", got.TotalReturn, tt.Expected.TotalReturn)
			near("maxDD", got.MaxDD, tt.Expected.MaxDD)
			for name, pair := range map[string][2]*float64{"cagr": {got.CAGR, tt.Expected.CAGR}, "vol": {got.Vol, tt.Expected.Vol},
				"sharpe": {got.Sharpe, tt.Expected.Sharpe}, "sortino": {got.Sortino, tt.Expected.Sortino}} {
				if pair[0] == nil || pair[1] == nil {
					if pair[0] != pair[1] {
						t.Errorf("%s availability differs: %v %v", name, pair[0], pair[1])
					}
					continue
				}
				near(name, *pair[0], *pair[1])
			}
		})
	}
}

func TestWebsiteStatisticsMissingAndZeroAreDistinct(t *testing.T) {
	day := websiteDay
	flat := domain.Series{{T: day, V: 1}, {T: 2 * day, V: 1}, {T: 3 * day, V: 1}, {T: 4 * day, V: 1}}
	got := websiteIndexStatistics(flat)
	if got.CAGR != nil || got.Vol == nil || *got.Vol != 0 || got.Sharpe == nil || *got.Sharpe != 0 || got.RiskSamples != 3 {
		t.Fatalf("measured flat series: %+v", got)
	}
	short := websiteIndexStatistics(domain.Series{{T: day, V: 1}, {T: 2 * day, V: 1.5}})
	if short.TotalReturn != .5 || short.Vol != nil || short.Sharpe != nil || short.Sortino != nil || short.CAGR != nil {
		t.Fatalf("short sample invented risk stats: %+v", short)
	}
	b, _ := json.Marshal(short)
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"cagr", "vol", "sharpe", "sortino"} {
		if value, ok := wire[key]; !ok || value != nil {
			t.Errorf("%s should explicitly be null: %s", key, b)
		}
	}
	for _, span := range []int64{182*day + day/2 - 1, 182*day + day/2} {
		m := websiteIndexStatistics(domain.Series{{T: day, V: 1}, {T: day + span, V: 1}})
		if (m.CAGR != nil) != (span >= 182*day+day/2) || (m.CAGR != nil && *m.CAGR != 0) {
			t.Fatalf("half-year CAGR boundary: %+v", m)
		}
	}
}

func TestWebsiteDrawdownUsesEveryValuationPointBeforeDailyRiskThinning(t *testing.T) {
	d := websiteDay
	curve := domain.Series{{T: d, V: 1}, {T: 2*d + d/4, V: 2}, {T: 2*d + 3*d/4, V: 1}, {T: 3*d + 3*d/4, V: 1}, {T: 4 * d, V: 1}}
	m := websiteIndexStatistics(curve)
	if m.MaxDD != -.5 || m.Vol == nil || *m.Vol != 0 || m.RiskSamples != 2 {
		t.Fatalf("intraday peak or half-day endpoint replacement lost: %+v", m)
	}
}

func TestWebsiteStatisticsIgnoreDisplaySamplingAndKeepObservedMetrics(t *testing.T) {
	f, history, spark, now := readWebsiteCapture(t)
	source := websiteSourceStub{history: history, sparks: spark}
	env := &Env{Now: func() time.Time { return time.UnixMilli(now) }}
	load := func(points int, method string) *IndexHistory {
		return loadIndexHistory(context.Background(), env, source, &IndexView{Index: f.Index},
			&IndexInput{History: true, HistoryMethod: method, Points: points})
	}
	small, large := load(2, "website"), load(400, "website")
	if len(small.NAV) != 2 || !small.Sampled {
		t.Fatal("test did not exercise display sampling")
	}
	for _, pair := range [][2]*IndexWebsiteStatistics{{small.WebsiteStatistics, large.WebsiteStatistics},
		{small.PreInceptionWebsiteStatistics, large.PreInceptionWebsiteStatistics}} {
		var values [2]map[string]any
		for i, item := range pair {
			data, _ := json.Marshal(item)
			if err := json.Unmarshal(data, &values[i]); err != nil || len(values[i]) == 0 {
				t.Fatal("missing statistics", err)
			}
		}
		for key, smallValue := range values[0] {
			largeValue := values[1][key]
			// Separate allocation replays add floating-point holdings in Go map
			// order; ignore machine rounding, never a meaningful metric change.
			if a, ok := smallValue.(float64); ok {
				b, numeric := largeValue.(float64)
				if !numeric || math.Abs(a-b) > 1e-10 {
					t.Errorf("sampling changed %s: %v vs %v", key, smallValue, largeValue)
				}
			} else if !reflect.DeepEqual(smallValue, largeValue) {
				t.Errorf("sampling changed %s: %v vs %v", key, smallValue, largeValue)
			}
		}
	}
	if large.WebsiteStatistics == nil || large.WebsiteStatistics.Start < f.Index.Def.Inception ||
		large.PreInceptionWebsiteStatistics == nil || large.PreInceptionWebsiteStatistics.End > f.Index.Def.Inception {
		t.Fatal("pre/post inception metric windows mixed")
	}
	expected := metrics.Compute(large.NAV, nil)
	if !reflect.DeepEqual(large.Metrics, &expected) {
		t.Fatal("legacy engine statistics changed")
	}
	observed := load(400, "observed")
	if observed.WebsiteStatistics != nil || observed.PreInceptionWebsiteStatistics != nil {
		t.Fatal("observed method acquired website statistics")
	}
}
