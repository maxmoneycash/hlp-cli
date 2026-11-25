package core

import (
	"context"
	"sort"

	"github.com/maxmoneycash/hlp/internal/hlpxyz"
)

type indexConstraintSource interface {
	IndexConstraints(context.Context, []string) (hlpxyz.IndexConstraints, error)
}

func loadIndexConstraints(ctx context.Context, env *Env, x hlpxyz.API, ix hlpxyz.Index, p *IndexInput) *hlpxyz.IndexConstraints {
	window, points, start, _ := historyOptions(p.Window, p.Points, env.now().UnixMilli())
	result := &hlpxyz.IndexConstraints{Window: window, ObservedAt: env.now().UnixMilli(), Sources: map[string]hlpxyz.IndexConstraintSource{}, Members: []hlpxyz.IndexConstraintMember{}}
	source, ok := x.(indexConstraintSource)
	if !ok {
		result.Warnings = []string{"Index constraint sources are unavailable from this provider"}
		return result
	}
	keys := indexObservedMemberAddresses(ix, env.now().UnixMilli())
	d, err := source.IndexConstraints(ctx, keys)
	if err != nil {
		result.Warnings = []string{"Index constraint sources unavailable: " + AsError(err).Message}
		return result
	}
	d.Window = window
	for i := range d.Members {
		m := &d.Members[i]
		j := sort.Search(len(m.Capacity), func(j int) bool { return m.Capacity[j].T >= start })
		m.Capacity = m.Capacity[j:]
		if len(m.Capacity) > points {
			out := make([]hlpxyz.IndexCapacityPoint, points)
			for n := range out {
				out[n] = m.Capacity[n*(len(m.Capacity)-1)/(points-1)]
			}
			m.Capacity = out
			d.Sampled = true
		}
	}
	d.Warnings = append(d.Warnings, "Dated source observations only; capacity limits, historical eligibility and deposit-closure decisions are not applied to the NAV replay. Missing closure dates do not imply deposits are open")
	return &d
}

func indexObservedMemberAddresses(ix hlpxyz.Index, now int64) []string {
	addresses := map[string]bool{}
	allocs := append(append([]hlpxyz.IndexAlloc{}, ix.Def.PreAllocs...), ix.Allocs...)
	for _, a := range allocs {
		if a.TS > now {
			continue
		}
		for addr, w := range a.Weights {
			if w > 0 {
				addresses[addr] = true
			}
		}
	}
	keys := make([]string, 0, len(addresses))
	for addr := range addresses {
		keys = append(keys, addr)
	}
	sort.Strings(keys)
	return keys
}
