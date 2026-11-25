package core

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hlpxyz"
)

func historyTime(s string) int64 { t, _ := time.Parse(time.RFC3339, s); return t.UnixMilli() }

func TestDatedMonthlyCoverage(t *testing.T) {
	point := func(s string, v float64) domain.Point { return domain.Point{T: historyTime(s), V: v} }
	s := domain.Series{
		point("2026-01-10T00:00:00Z", 1), point("2026-01-31T12:00:00Z", 1.1),
		point("2026-02-28T12:00:00Z", 1.21),
		// March is absent: April must not turn a two-month return into April's.
		point("2026-04-05T00:00:00Z", 2), point("2026-04-30T12:00:00Z", 2.2),
		point("2026-05-17T12:00:00Z", 2.42),
	}
	got := monthlyObservations(s, historyTime("2026-05-18T00:00:00Z"), 0)
	if len(got) != 4 || got[0].Status != "inception" || got[0].Complete || got[1].Status != "complete" || !got[1].Complete || got[2].Month != 4 || got[2].Status != "gap" || got[2].Return != nil || got[3].Status != "current" {
		t.Fatalf("coverage: %+v", got)
	}
	if got[0].Return == nil || math.Abs(*got[0].Return-0.1) > 1e-10 || got[1].Return == nil || math.Abs(*got[1].Return-0.1) > 1e-10 {
		t.Fatalf("fraction returns: %+v", got)
	}
	if got[1].Start != s[1].T || got[1].End != s[2].T {
		t.Fatalf("invented dates: %+v", got[1])
	}
	// A real observation exactly at April's start restores its boundary,
	// independently of the missing March observations.
	s[3].T = historyTime("2026-04-01T00:00:00Z")
	recovered := monthlyObservations(s, historyTime("2026-05-18T00:00:00Z"), 0)[2]
	if !recovered.Complete || recovered.Return == nil || math.Abs(*recovered.Return-0.1) > 1e-10 {
		t.Fatalf("real boundary ignored: %+v", recovered)
	}
}

func TestHistoryWindowDoesNotRefreshOldEquity(t *testing.T) {
	s := domain.Series{{T: 100, V: 50}, {T: 200, V: 60}}
	if got := observedWindow(s, 300); len(got) != 0 {
		t.Fatalf("old equity became current: %+v", got)
	}
	got := sampleHistory(domain.Series{{T: 1, V: 1}, {T: 2, V: 2}, {T: 3, V: 3}, {T: 4, V: 4}}, 2)
	if len(got) != 2 || got[0].T != 1 || got[1].T != 4 {
		t.Fatalf("sample endpoints lost: %+v", got)
	}
}

func TestIndexReplayRejectsMissingMemberAndIgnoresUnrelatedTail(t *testing.T) {
	t0 := historyTime("2026-01-01T00:00:00Z")
	d := int64(24 * time.Hour / time.Millisecond)
	ix := hlpxyz.Index{Allocs: []hlpxyz.IndexAlloc{{TS: t0, Weights: map[string]float64{"a": 0.5, "b": 0.5}}}}
	curves := map[string]domain.Series{"a": {{T: t0, V: 1}, {T: t0 + d, V: 2}}, "b": {{T: t0, V: 1}, {T: t0 + d, V: 1}}, "unrelated": {{T: t0 + 30*d, V: 100}}}
	nav, missing, end := replayIndex(ix, curves, t0+40*d)
	if len(missing) != 0 || end != t0+d || len(nav) != 2 || nav[1].V != 1.5 {
		t.Fatalf("replay %+v %v %d", nav, missing, end)
	}
	curves["a"][1].V = 0
	nav, missing, _ = replayIndex(ix, curves, t0+40*d)
	if len(missing) != 0 || nav[1].V != 0.5 {
		t.Fatalf("zero NAV was treated as cash: %+v %v", nav, missing)
	}
	delete(curves, "b")
	nav, missing, _ = replayIndex(ix, curves, t0+40*d)
	if len(nav) != 0 || len(missing) != 1 || missing[0] != "b" {
		t.Fatalf("missing member fabricated cash: %+v %v", nav, missing)
	}
}

func TestIndexDriftRetainsCashAndSelectsDatedAllocation(t *testing.T) {
	t0 := historyTime("2026-01-01T00:00:00Z")
	d := int64(24 * time.Hour / time.Millisecond)
	ix := hlpxyz.Index{Def: hlpxyz.IndexDef{Inception: t0}, Allocs: []hlpxyz.IndexAlloc{
		{TS: t0, Weights: map[string]float64{"a": 0.5}}, {TS: t0 + 10*d, Weights: map[string]float64{"future": 1}},
	}}
	curves := map[string]domain.Series{"a": {{T: t0, V: 1}, {T: t0 + d, V: 2}}}
	v := &IndexView{Index: ix, Members: []IndexMember{{Address: "a", Weight: 0.5}}}
	enrichIndexMembers(v, curves, t0+d, t0)
	if v.Members[0].CurrentWeight == nil || math.Abs(*v.Members[0].CurrentWeight-2.0/3.0) > 1e-10 || v.Members[0].ReturnRange == nil || *v.Members[0].ReturnRange != 1 {
		t.Fatalf("drift %+v", v.Members)
	}
	nav, missing, _ := replayIndex(ix, curves, t0+d)
	if len(missing) > 0 || len(nav) != 2 || nav[1].V != 1.5 {
		t.Fatalf("future allocation applied: %+v %v", nav, missing)
	}
}

type historySource struct {
	hlpxyz.API
	summary                  hlpxyz.SummaryResponse
	curves                   map[string]domain.Series
	complete                 bool
	summaryCalls, curveCalls int
}

func (s *historySource) Summary(context.Context, bool) (hlpxyz.SummaryResponse, error) {
	s.summaryCalls++
	return s.summary, nil
}
func (s *historySource) IndexCurves(context.Context) (map[string]domain.Series, bool, error) {
	s.curveCalls++
	return s.curves, s.complete, nil
}

func TestVaultHistoryUsesTwoBulkReadsAndIndependentAges(t *testing.T) {
	now := time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC)
	s := &historySource{summary: hlpxyz.SummaryResponse{Vaults: []hlpxyz.SummaryVault{
		{Address: "a", Name: "A", LastUpdated: 100, PerfSeries: []hlpxyz.PerfPoint{{Date: "2026-01-01T00:00:00Z", Equity: new(100.0)}}},
		{Address: "b", Name: "B"},
	}}, curves: map[string]domain.Series{"a": {{T: now.Add(-24 * time.Hour).UnixMilli(), V: 1}, {T: now.Add(-time.Hour).UnixMilli(), V: 1.1}}}, complete: true}
	env := &Env{HLPXYZ: s, Now: func() time.Time { return now }}
	r, err := runVaultHistory(context.Background(), env, &VaultHistoryInput{Window: "1m"})
	if err != nil {
		t.Fatal(err)
	}
	d := r.Data.(*VaultHistoryData)
	if s.summaryCalls != 1 || s.curveCalls != 1 || len(d.Vaults) != 2 || d.Complete || !r.Meta.Partial || !r.Meta.Stale {
		t.Fatalf("bulk/coverage: %+v %+v calls %d/%d", d, r.Meta, s.summaryCalls, s.curveCalls)
	}
	a := d.Vaults[0]
	if len(a.Equity) != 0 || a.EquityAsOf != historyTime("2026-01-01T00:00:00Z") || a.NAVAsOf != now.Add(-time.Hour).UnixMilli() || a.EquityUpdatedAt != 100 {
		t.Fatalf("source age overwritten: %+v", a)
	}
	if _, err := runVaultHistory(context.Background(), env, &VaultHistoryInput{Points: 1}); err == nil || s.summaryCalls != 1 {
		t.Fatal("invalid bounded request reached source")
	}
}

func TestIndexHistoryKeepsPreInceptionSeparateAndScoresBeforeSampling(t *testing.T) {
	now := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	d := int64(24 * time.Hour / time.Millisecond)
	t0 := now.UnixMilli() - 2*d
	source := &historySource{complete: true, curves: map[string]domain.Series{"a": {{T: t0 - d, V: 1}, {T: t0, V: 2}, {T: t0 + d, V: 4}, {T: t0 + 2*d, V: 8}}}}
	view := &IndexView{Index: hlpxyz.Index{
		Def:    hlpxyz.IndexDef{Inception: t0, UpdatedAt: t0, PreAllocs: []hlpxyz.IndexAlloc{{TS: t0 - d, Weights: map[string]float64{"a": 1}}}},
		Allocs: []hlpxyz.IndexAlloc{{TS: t0, Weights: map[string]float64{"a": 0.5}}},
	}, Members: []IndexMember{{Address: "a", Weight: 0.5}}}
	env := &Env{HLPXYZ: source, Now: func() time.Time { return now }}
	h := loadIndexHistory(context.Background(), env, source, view, &IndexInput{History: true, Window: "all", Points: 2})
	if !h.Complete || !h.PreInceptionComplete || !h.Sampled || len(h.NAV) != 2 || len(h.PreInception) != 2 || h.PreInception[0].V != 1 || h.PreInception[1].V != 2 || h.PreInception[1].T != t0 {
		t.Fatalf("pre-inception boundary: %+v", h)
	}
	if h.Metrics == nil || h.Metrics.Start != t0 || h.Metrics.TotalReturn != 1.5 || h.Metrics.End != now.UnixMilli() {
		t.Fatalf("prehistory contaminated metrics: %+v", h.Metrics)
	}
	h = loadIndexHistory(context.Background(), env, source, view, &IndexInput{History: true, Window: "1d", Points: 2})
	if h.Metrics == nil || h.Metrics.Start != t0+d || math.Abs(h.Metrics.TotalReturn-2.0/3.0) > 1e-10 || len(h.PreInception) != 0 {
		t.Fatalf("window metrics: %+v", h)
	}
}

func TestVaultHistorySeparatesObservedZeroAndSharePriceEstimate(t *testing.T) {
	now := time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC)
	source := &historySource{summary: hlpxyz.SummaryResponse{Vaults: []hlpxyz.SummaryVault{
		{Address: "a", Equity: 300, PerfSeries: []hlpxyz.PerfPoint{
			{Date: "2026-05-01", SharePrice: 1.5},
			{Date: "2026-05-02", SharePrice: 2, Equity: new(0.0)},
		}},
	}}}
	r, err := runVaultHistory(context.Background(), &Env{HLPXYZ: source, Now: func() time.Time { return now }}, &VaultHistoryInput{})
	if err != nil {
		t.Fatal(err)
	}
	row := r.Data.(*VaultHistoryData).Vaults[0]
	if row.SummaryEquity == nil || *row.SummaryEquity != 300 || len(row.Equity) != 1 || row.Equity[0].V != 0 || len(row.EstimatedEquity) != 1 || row.EstimatedEquity[0].V != 225 || row.EstimatedEquity[0].T >= row.Equity[0].T {
		t.Fatalf("summary history: %+v", row)
	}
}
