package core

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hlpxyz"
	"github.com/maxmoneycash/hlp/internal/metrics"
)

// IndexHistory is an explicitly labeled local allocation replay, not an
// investable fund NAV. Source pre-inception allocations remain a separate series.
type IndexHistory struct {
	NAV                           domain.Series           `json:"nav"`
	Drawdown                      domain.Series           `json:"drawdown"`
	Monthly                       []MonthlyObservation    `json:"monthly"`
	Metrics                       *domain.Metrics         `json:"metrics,omitempty"`
	PreInception                  domain.Series           `json:"preInception"`
	PreInceptionMonthly           []MonthlyObservation    `json:"preInceptionMonthly"`
	PreInceptionComplete          bool                    `json:"preInceptionComplete"`
	Window                        string                  `json:"window"`
	Inception                     int64                   `json:"inception"`
	AsOf                          int64                   `json:"asOf"`
	ObservedAt                    int64                   `json:"observedAt"`
	DefinitionUpdatedAt           int64                   `json:"definitionUpdatedAt"`
	Complete                      bool                    `json:"complete"`
	Sampled                       bool                    `json:"sampled"`
	MissingMembers                []string                `json:"missingMembers"`
	Method                        string                  `json:"method"`
	Warnings                      []string                `json:"warnings"`
	Coverage                      *IndexHistoryCoverage   `json:"coverage,omitempty"`
	WebsiteStatistics             *IndexWebsiteStatistics `json:"websiteStatistics,omitempty"`
	PreInceptionWebsiteStatistics *IndexWebsiteStatistics `json:"preInceptionWebsiteStatistics,omitempty"`
}

func latestIndexAlloc(allocs []hlpxyz.IndexAlloc, now int64) (hlpxyz.IndexAlloc, bool) {
	var latest hlpxyz.IndexAlloc
	for _, a := range allocs {
		if a.TS > latest.TS && a.TS <= now {
			latest = a
		}
	}
	return latest, latest.TS > 0
}

func loadIndexHistory(ctx context.Context, env *Env, x hlpxyz.API, view *IndexView, p *IndexInput) *IndexHistory {
	if p.HistoryMethod == "website" {
		return loadWebsiteIndexHistory(ctx, env, x, view, p)
	}
	now := env.now().UnixMilli()
	window, points, start, _ := historyOptions(p.Window, p.Points, now)
	h := &IndexHistory{Window: window, Inception: view.Def.Inception, ObservedAt: now, DefinitionUpdatedAt: view.Def.UpdatedAt,
		Method: "allocation-replay-v6", MissingMembers: []string{}, Coverage: &IndexHistoryCoverage{Interpolation: "none", EndpointPolicy: "unavailable", StitchedMembers: []string{}, ClampedBeforeMembers: []string{}, ClampedAfterMembers: []string{}, ExcludedNonPositiveMembers: []string{}}, Warnings: []string{
			"Modeled buy-and-hold allocation replay from v6 member NAV, before withdrawal profit share; capacity/deposit-closure constraints are not simulated, so this is not verified website index NAV",
		}}
	curves, complete, err := bulkIndexCurves(ctx, x)
	if err != nil {
		h.Warnings = append(h.Warnings, "v6 member history unavailable: "+AsError(err).Message)
		return h
	}
	for addr, s := range curves {
		curves[addr] = cleanHistory(s, now, false)
	}
	// Build post-inception and backtest separately, so their metrics and
	// monthly returns never silently combine live and hypothetical periods.
	nav, missing, asOf := replayIndex(view.Index, curves, now)
	h.MissingMembers, h.AsOf = missing, asOf
	h.Complete = complete && len(missing) == 0 && len(nav) > 1
	if !complete {
		h.Warnings = append(h.Warnings, "Bulk v6 member curves are incomplete")
	}
	if len(missing) > 0 {
		h.Warnings = append(h.Warnings, "Index history unavailable: member curves do not cover every allocation; missing members are not treated as cash")
	}
	h.Monthly = monthlyObservations(nav, now, start)
	clipped := observedWindow(nav, start)
	if len(clipped) > 1 {
		m := metrics.Compute(clipped, nil)
		h.Metrics = &m
	}
	h.Sampled = len(clipped) > points
	h.NAV, h.Drawdown = sampleHistory(clipped, points), sampleHistory(metrics.Drawdown(clipped), points)
	if len(view.Def.PreAllocs) > 0 && view.Def.Inception > 0 {
		pre := hlpxyz.Index{Def: view.Def, Allocs: view.Def.PreAllocs}
		preNAV, missingPre, _ := replayIndex(pre, curves, min(now, view.Def.Inception))
		h.PreInceptionComplete = complete && len(missingPre) == 0 && len(preNAV) > 1
		if len(missingPre) > 0 {
			h.Warnings = append(h.Warnings, "Pre-inception backtest lacks member history; it is unavailable")
		}
		h.PreInceptionMonthly = monthlyObservations(preNAV, min(now, view.Def.Inception), start)
		preNAV = observedWindow(preNAV, start)
		h.Sampled = h.Sampled || len(preNAV) > points
		h.PreInception = sampleHistory(preNAV, points)
	}
	// Current holdings can be priced even when an older, exited member has
	// missing history. Verify the latest allocation is covered independently.
	latest, ok := latestIndexAlloc(view.Allocs, now)
	if ok && h.AsOf >= latest.TS {
		enrichIndexMembers(view, curves, h.AsOf, start)
	}
	return h
}

// replayIndex refuses missing baselines instead of IndexNAV's cash fallback.
// End at the oldest last observation among current members, so unrelated or
// newer curves cannot extend a stale holding's NAV into an invented present.
func replayIndex(ix hlpxyz.Index, curves map[string]domain.Series, now int64) (domain.Series, []string, int64) {
	latest, ok := latestIndexAlloc(ix.Allocs, now)
	if !ok {
		return nil, []string{}, 0
	}
	end := now
	for a, w := range latest.Weights {
		if w > 0 {
			end = min(end, historyEnd(observedBefore(curves[strings.ToLower(a)], now)))
		}
	}
	missing := map[string]bool{}
	norm := map[string]domain.Series{}
	var allocs []hlpxyz.IndexAlloc
	for _, a := range ix.Allocs {
		if a.TS <= 0 || a.TS > now {
			continue
		}
		allocs = append(allocs, a)
		weight := 0.0
		for addr, w := range a.Weights {
			if w <= 0 {
				continue
			}
			weight += w
			addr = strings.ToLower(addr)
			s := observedBefore(curves[addr], now)
			if base, ok := observedValue(s, a.TS); !ok || base <= 0 {
				missing[addr] = true
			}
			if a.TS == latest.TS && historyEnd(s) < a.TS {
				missing[addr] = true
			}
			norm[addr] = s
		}
		if weight > 1.000001 {
			missing[fmt.Sprintf("allocation:%d:weights-over-100%%", a.TS)] = true
		}
	}
	keys := make([]string, 0, len(missing))
	for addr := range missing {
		keys = append(keys, addr)
	}
	sort.Strings(keys)
	if len(keys) > 0 || end <= 0 {
		return nil, keys, end
	}
	for addr, s := range norm {
		norm[addr] = observedBefore(s, end)
	}
	// Cash-only periods are valid source allocations. A private clock series
	// bounds the simulation without inventing a member or an observed price.
	norm["__model_clock"] = domain.Series{{T: end, V: 1}}
	ix.Allocs = allocs
	return hlpxyz.IndexNAV(ix, norm), keys, end
}

func observedBefore(s domain.Series, end int64) domain.Series {
	i := sort.Search(len(s), func(i int) bool { return s[i].T > end })
	return s[:i]
}

func observedValue(s domain.Series, at int64) (float64, bool) {
	i := sort.Search(len(s), func(i int) bool { return s[i].T > at })
	if i == 0 || s[i-1].V < 0 {
		return 0, false
	}
	return s[i-1].V, true
}

func enrichIndexMembers(v *IndexView, curves map[string]domain.Series, asOf, start int64) {
	latest, ok := latestIndexAlloc(v.Allocs, asOf)
	if !ok {
		return
	}
	values := map[string]float64{}
	cash, total := 1.0, 0.0
	for a, w := range latest.Weights {
		if w <= 0 {
			continue
		}
		s := curves[strings.ToLower(a)]
		base, bOK := observedValue(s, latest.TS)
		last, lOK := observedValue(s, asOf)
		if !bOK || !lOK || base <= 0 {
			return
		}
		values[strings.ToLower(a)] = w * last / base
		total += w * last / base
		cash -= w
	}
	total += cash
	if total <= 0 || cash < -0.000001 {
		return
	}
	for i := range v.Members {
		m := &v.Members[i]
		a := strings.ToLower(m.Address)
		w := values[a] / total
		m.CurrentWeight, m.AsOf = &w, asOf
		s := observedBefore(curves[a], asOf)
		if len(s) == 0 {
			continue
		}
		last := s[len(s)-1].V
		for _, target := range []struct {
			at  int64
			out **float64
		}{{start, &m.ReturnRange}, {v.Def.Inception, &m.ReturnSinceInception}} {
			at := target.at
			if at == 0 {
				at = v.Def.Inception
			}
			if base, ok := observedValue(s, at); ok && base > 0 {
				r := last/base - 1
				*target.out = &r
			}
		}
	}
}
