package core

import (
	"context"
	"sort"

	"github.com/maxmoneycash/hlp-cli/internal/hlpxyz"
)

type indexExposureSource interface {
	IndexExposure(context.Context, []string) (hlpxyz.IndexExposure, error)
}

func loadIndexExposure(ctx context.Context, env *Env, x hlpxyz.API, ix hlpxyz.Index, p *IndexInput) *hlpxyz.IndexExposure {
	now := env.now().UnixMilli()
	window, points, start, _ := historyOptions(p.Window, p.Points, now)
	result := &hlpxyz.IndexExposure{Window: window, ObservedAt: now, Members: []hlpxyz.IndexExposureMember{}, Source: hlpxyz.IndexConstraintSource{Missing: []string{}}}
	source, ok := x.(indexExposureSource)
	if !ok {
		result.Warnings = []string{"Index member exposure is unavailable from this provider"}
		return result
	}
	d, err := source.IndexExposure(ctx, indexObservedMemberAddresses(ix, now))
	if err != nil {
		result.Warnings = []string{"Index member exposure unavailable: " + AsError(err).Message}
		return result
	}
	d.Window = window
	for i := range d.Members {
		m := &d.Members[i]
		first := sort.Search(len(m.Observations), func(j int) bool { return m.Observations[j].T >= start })
		last := sort.Search(len(m.Observations), func(j int) bool { return m.Observations[j].T > now })
		m.Observations = m.Observations[first:last]
		if len(m.Observations) > points {
			out := make([]hlpxyz.IndexExposurePoint, points)
			for n := range out {
				out[n] = m.Observations[n*(len(m.Observations)-1)/(points-1)]
			}
			m.Observations = out
			d.Sampled = true
		}
	}
	d.Warnings = append(d.Warnings, "Actual member-account observations in USD, not index-weighted exposure. Missing dates are not filled; completeness describes source response coverage, not a continuous time series")
	return &d
}
