package core

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hlpxyz"
	"github.com/maxmoneycash/hlp/internal/metrics"
)

func loadWebsiteIndexHistory(ctx context.Context, env *Env, x hlpxyz.API, view *IndexView, p *IndexInput) *IndexHistory {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	now := env.now().UnixMilli()
	window, points, start, _ := historyOptions(p.Window, p.Points, now)
	coverage := &IndexHistoryCoverage{Interpolation: "linear", EndpointPolicy: "clamp", StitchedMembers: []string{}, ClampedBeforeMembers: []string{}, ClampedAfterMembers: []string{}, ExcludedNonPositiveMembers: []string{}}
	h := &IndexHistory{Window: window, Inception: view.Def.Inception, ObservedAt: now, DefinitionUpdatedAt: view.Def.UpdatedAt,
		Method: "website-client-v6", Coverage: coverage, MissingMembers: []string{}, Warnings: []string{
			"Public-client NAV valuation from frozen allocations and stitched/interpolated v6 curves, before withdrawal profit share; modeled values are not observed fund NAV. Legacy metrics retain engine formulas; websiteStatistics identifies the public-client risk formula",
		}}
	history, complete, err := bulkIndexCurves(ctx, x)
	if err != nil {
		h.Warnings = append(h.Warnings, "v6 member history unavailable: "+AsError(err).Message)
		return h
	}
	sparks, err := x.V6Spark(ctx)
	if err != nil {
		h.Warnings = append(h.Warnings, "v6 spark tails unavailable: "+AsError(err).Message)
		return h
	}
	addresses := indexObservedMemberAddresses(view.Index, now)
	fallback := map[string]domain.Series{}
	needsFallback := false
	for _, a := range addresses {
		if len(cleanHistory(sparks[a], now, true)) < 2 {
			needsFallback = true
			break
		}
	}
	if needsFallback {
		summary, serr := x.Summary(ctx, false)
		if serr != nil {
			h.Warnings = append(h.Warnings, "Summary fallback unavailable: "+AsError(serr).Message)
		} else {
			for _, row := range summary.Vaults {
				a := strings.ToLower(row.Address)
				for _, point := range row.PerfSeries {
					stamp, parseErr := time.Parse(time.RFC3339, point.Date)
					if parseErr != nil {
						stamp, parseErr = time.Parse("2006-01-02", point.Date)
					}
					if parseErr == nil {
						fallback[a] = append(fallback[a], domain.Point{T: stamp.UnixMilli(), V: point.SharePrice})
					}
				}
			}
		}
	}
	// Limit diagnostics to actual historical/current members, not the source's
	// unrelated universe. The caller has already excluded future allocations.
	selectedHistory, selectedSparks, selectedFallback := map[string]domain.Series{}, map[string]domain.Series{}, map[string]domain.Series{}
	for _, a := range addresses {
		selectedHistory[a] = history[a]
		selectedSparks[a] = sparks[a]
		selectedFallback[a] = fallback[a]
	}
	curves, stitched, excluded := websiteCurveSet(selectedHistory, selectedSparks, selectedFallback, now)
	coverage.StitchedMembers, coverage.ExcludedNonPositiveMembers = stitched, excluded
	full, before, after, missing, asOf := replayWebsiteIndex(view.Index, curves, 0, now, websiteDay)
	h.AsOf, h.MissingMembers = asOf, missing
	coverage.ClampedBeforeMembers, coverage.ClampedAfterMembers = before, after
	if len(missing) > 0 {
		h.Warnings = append(h.Warnings, "Website-model history unavailable: missing member curves are not treated as cash")
		return h
	}
	clipped, _, _, _, _ := replayWebsiteIndex(view.Index, curves, max(start, view.Def.Inception), now, websiteStep(now-max(start, view.Def.Inception)))
	h.Monthly = monthlyObservations(full, now, start)
	h.WebsiteStatistics = websiteIndexStatistics(clipped)
	if len(clipped) > 1 {
		m := metrics.Compute(clipped, nil)
		h.Metrics = &m
	}
	h.Sampled = len(clipped) > points
	h.NAV, h.Drawdown = sampleHistory(clipped, points), sampleHistory(metrics.Drawdown(clipped), points)
	h.Complete = complete && len(full) > 1 && len(before) == 0 && len(after) == 0 && len(excluded) == 0
	if len(view.Def.PreAllocs) > 0 && view.Def.Inception > 0 {
		end := min(now, view.Def.Inception)
		pre := hlpxyz.Index{Def: view.Def, Allocs: view.Def.PreAllocs}
		preFull, preBefore, preAfter, preMissing, _ := replayWebsiteIndex(pre, curves, 0, end, websiteDay)
		preChart, _, _, _, _ := replayWebsiteIndex(pre, curves, start, end, websiteStep(end-start))
		h.PreInceptionComplete = complete && len(preFull) > 1 && len(preMissing) == 0 && len(preBefore) == 0 && len(preAfter) == 0 && len(excluded) == 0
		h.PreInceptionMonthly = monthlyObservations(preFull, end, start)
		h.PreInceptionWebsiteStatistics = websiteIndexStatistics(preChart)
		h.PreInception = sampleHistory(preChart, points)
		h.Sampled = h.Sampled || len(preChart) > points
		coverage.ClampedBeforeMembers = appendUnique(coverage.ClampedBeforeMembers, preBefore...)
		coverage.ClampedAfterMembers = appendUnique(coverage.ClampedAfterMembers, preAfter...)
		if len(preMissing) > 0 {
			h.Warnings = append(h.Warnings, "Recorded pre-inception allocations lack member curves; backtest is unavailable")
		}
	}
	sort.Strings(coverage.ClampedBeforeMembers)
	sort.Strings(coverage.ClampedAfterMembers)
	if len(coverage.ClampedBeforeMembers) > 0 || len(coverage.ClampedAfterMembers) > 0 {
		h.Warnings = append(h.Warnings, "Endpoint-clamped values extend beyond member observations; source asOf remains unchanged and coverage is partial")
	}
	if len(excluded) > 0 {
		h.Warnings = append(h.Warnings, "The public-client method excludes non-positive NAV samples; use observed method to retain them. Coverage is partial")
	}
	if !complete {
		h.Warnings = append(h.Warnings, "Bulk v6 member curves are incomplete")
	}
	// Current modeled weights use the same interpolation, while their date
	// remains the oldest actual source tail among current holdings.
	if asOf > 0 {
		enrichWebsiteMembers(view, curves, now, start, asOf)
	}
	return h
}

func enrichWebsiteMembers(view *IndexView, curves map[string]domain.Series, now, start, asOf int64) {
	latest, ok := latestIndexAlloc(view.Allocs, now)
	if !ok {
		return
	}
	values, cash, total := map[string]float64{}, 1.0, 0.0
	for a, w := range latest.Weights {
		if w <= 0 {
			continue
		}
		a = strings.ToLower(a)
		s := curves[a]
		if len(s) < 2 {
			return
		}
		v := w * websiteValue(s, now) / websiteValue(s, latest.TS)
		values[a] = v
		cash -= w
		total += v
	}
	total += cash
	if total <= 0 || cash < -0.000001 {
		return
	}
	for i := range view.Members {
		m := &view.Members[i]
		a := strings.ToLower(m.Address)
		s := curves[a]
		if len(s) < 2 {
			continue
		}
		w := values[a] / total
		m.CurrentWeight, m.AsOf = &w, asOf
		last := websiteValue(s, now)
		for _, target := range []struct {
			at  int64
			dst **float64
		}{{max(start, s[0].T), &m.ReturnRange}, {view.Def.Inception, &m.ReturnSinceInception}} {
			base := websiteValue(s, target.at)
			if base > 0 {
				v := last/base - 1
				*target.dst = &v
			}
		}
	}
}
