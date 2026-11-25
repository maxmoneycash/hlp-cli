package core

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hlpxyz"
)

func TestWebsiteInterpolationClampsAndMissingCoverage(t *testing.T) {
	s := domain.Series{{T: 20, V: 1}, {T: 30, V: 2}}
	for at, want := range map[int64]float64{10: 1, 20: 1, 25: 1.5, 30: 2, 40: 2} {
		if got := websiteValue(s, at); got != want {
			t.Fatalf("at%d got%v want%v", at, got, want)
		}
	}
	ix := hlpxyz.Index{Allocs: []hlpxyz.IndexAlloc{{TS: 10, Weights: map[string]float64{"a": 1}}}}
	nav, before, after, missing, asOf := replayWebsiteIndex(ix, map[string]domain.Series{"a": s}, 10, 40, 10)
	if len(nav) != 4 || nav.Last() != 2 || len(before) != 1 || before[0] != "a" || len(after) != 1 || after[0] != "a" || len(missing) != 0 || asOf != 30 {
		t.Fatalf("clamp disclosure/source age: nav=%v before=%v after=%v missing=%v asOf=%d", nav, before, after, missing, asOf)
	}
	nav, _, _, missing, _ = replayWebsiteIndex(ix, map[string]domain.Series{}, 10, 40, 10)
	if len(nav) != 0 || len(missing) != 1 || missing[0] != "a" {
		t.Fatalf("missing leg became cash: %v %v", nav, missing)
	}
}

func TestWebsiteSparkStitchPreservesTailAndRescalesOlderSegment(t *testing.T) {
	var history domain.Series
	for i := int64(1); i <= 40; i++ {
		history = append(history, domain.Point{T: i * websiteDay, V: 1 + float64(i)/100})
	}
	spark := domain.Series{{T: 40 * websiteDay, V: 100}, {T: 41 * websiteDay, V: 110}}
	got, joined := websiteStitch(spark, history)
	if !joined || len(got) != 41 || got[len(got)-2] != spark[0] || got[len(got)-1] != spark[1] || math.Abs(got[0].V-history[0].V*100/1.4) > 1e-10 {
		t.Fatalf("stitch changed source tail or failed scale: joined=%v points=%v", joined, got)
	}
	curves, _, excluded := websiteCurveSet(map[string]domain.Series{}, map[string]domain.Series{"a": {{T: 10, V: 1}, {T: 20, V: 0}, {T: 30, V: 2}}}, nil, 40)
	if len(curves["a"]) != 2 || len(excluded) != 1 || excluded[0] != "a" {
		t.Fatalf("nonpositive exclusion undisclosed: %v %v", curves, excluded)
	}
}

type websiteCapture struct {
	Index                 hlpxyz.Index            `json:"index"`
	Curves                map[string][][2]float64 `json:"curves"`
	Sparks                map[string][][2]float64 `json:"sparks"`
	WebsiteMonthlyPercent map[string]float64      `json:"websiteMonthlyPercent"`
}

func readWebsiteCapture(t *testing.T) (websiteCapture, map[string]domain.Series, map[string]domain.Series, int64) {
	t.Helper()
	b, err := os.ReadFile("testdata/website-index-v6.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture websiteCapture
	if err := json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	convert := func(rows map[string][][2]float64) map[string]domain.Series {
		out := map[string]domain.Series{}
		for a, pts := range rows {
			for _, p := range pts {
				out[a] = append(out[a], domain.Point{T: int64(p[0]), V: p[1]})
			}
		}
		return out
	}
	return fixture, convert(fixture.Curves), convert(fixture.Sparks), time.Date(2026, 11, 17, 23, 0, 0, 0, time.UTC).UnixMilli()
}

// Inputs are the existing public capture; expectations are the separately
// captured rendered website monthly table, not computed from engine output.
func TestWebsiteModelMatchesFiveCapturedDatedReturns(t *testing.T) {
	f, history, sparks, now := readWebsiteCapture(t)
	curves, stitched, _ := websiteCurveSet(history, sparks, nil, now)
	if len(stitched) == 0 {
		t.Fatal("capture did not exercise stitched sources")
	}
	combined := f.Index
	combined.Allocs = append(append([]hlpxyz.IndexAlloc{}, f.Index.Def.PreAllocs...), f.Index.Allocs...)
	nav, _, _, missing, _ := replayWebsiteIndex(combined, curves, 0, now, websiteDay)
	if len(missing) > 0 {
		t.Fatalf("capture lacks members: %v", missing)
	}
	for month, want := range f.WebsiteMonthlyPercent {
		start, err := time.Parse("2006-01", month)
		if err != nil {
			t.Fatal(err)
		}
		end := start.AddDate(0, 1, 0)
		got := (websiteValue(nav, end.UnixMilli())/websiteValue(nav, start.UnixMilli()) - 1) * 100
		if math.Abs(math.Round(got*10)/10-want) > 1e-8 {
			t.Errorf("%s got %.8f%% (display %.1f%%), website %.1f%%", month, got, got, want)
		}
	}
}

type websiteSourceStub struct {
	hlpxyz.API
	history, sparks map[string]domain.Series
}

func (s websiteSourceStub) IndexCurves(context.Context) (map[string]domain.Series, bool, error) {
	return s.history, true, nil
}
func (s websiteSourceStub) V6Spark(context.Context) (map[string]domain.Series, error) {
	return s.sparks, nil
}
func (s websiteSourceStub) Summary(context.Context, bool) (hlpxyz.SummaryResponse, error) {
	return hlpxyz.SummaryResponse{}, nil
}

func TestWebsiteHistoryLabelsModelAndKeepsPreInceptionSeparate(t *testing.T) {
	f, history, sparks, now := readWebsiteCapture(t)
	source := websiteSourceStub{history: history, sparks: sparks}
	env := &Env{Now: func() time.Time { return time.UnixMilli(now) }}
	view := &IndexView{Index: f.Index}
	h := loadIndexHistory(context.Background(), env, source, view, &IndexInput{History: true, HistoryMethod: "website", Points: 400})
	if h.Method != "website-client-v6" || h.Coverage == nil || h.Coverage.Interpolation != "linear" || h.Coverage.EndpointPolicy != "clamp" || len(h.Coverage.StitchedMembers) == 0 || h.AsOf >= h.ObservedAt {
		t.Fatalf("model/source metadata missing: %+v", h)
	}
	if len(h.NAV) < 2 || h.NAV[0].T < f.Index.Def.Inception || len(h.PreInception) < 2 || h.PreInception[len(h.PreInception)-1].T > f.Index.Def.Inception {
		t.Fatalf("pre-inception mixed into live history: live=%d pre=%d", len(h.NAV), len(h.PreInception))
	}
	if h.Metrics == nil || h.Metrics.Start < f.Index.Def.Inception {
		t.Fatal("live metrics include backtest")
	}
	if len(h.Coverage.ClampedAfterMembers) == 0 || h.Complete {
		t.Fatal("clamped source tails hidden as complete observed data")
	}
}
