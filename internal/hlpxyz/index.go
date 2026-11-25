package hlpxyz

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
)

const (
	curvesTries = 8
	curvesRetry = 1500 * time.Millisecond
)

// IndexCurves returns the v6 NAV curve of every vault hlp.xyz tracks for
// indexes (/api/vaults?action=idxCurves&pipeline=v6), keyed by lowercase
// address. While the server answers incomplete:true it retries (as the
// website does: 8 tries, 1.5 s apart, respecting ctx) and finally returns
// the partial set with complete=false.
func (c *Client) IndexCurves(ctx context.Context) (curves map[string]domain.Series, complete bool, err error) {
	retry := curvesRetry
	if c.opts.LoadingRetry < retry {
		retry = c.opts.LoadingRetry
	}
	for try := 0; ; try++ {
		var r struct {
			Curves     map[string][][2]float64 `json:"curves"`
			Incomplete bool                    `json:"incomplete"`
		}
		req := vaultsReq("idxCurves", ttlDetail, "pipeline", "v6")
		req.cacheable = func(b []byte) bool {
			var x struct {
				Incomplete bool `json:"incomplete"`
			}
			return json.Unmarshal(b, &x) == nil && !x.Incomplete
		}
		ctxTry := ctx
		if try > 0 {
			ctxTry = WithoutCache(ctx)
		}
		if _, err := c.getJSON(ctxTry, req, &r); err != nil {
			return nil, false, err
		}
		out := make(map[string]domain.Series, len(r.Curves))
		for k, v := range r.Curves {
			out[strings.ToLower(k)] = pairsToSeries(v)
		}
		if !r.Incomplete || try+1 >= curvesTries {
			return out, !r.Incomplete, nil
		}
		if err := sleep(ctx, retry); err != nil {
			return nil, false, err
		}
	}
}

// IndexNAV simulates an index's NAV (starting at 1.0 on its first
// allocation) from its allocation history and the member vaults' NAV
// curves, sampled daily (UTC) up to the newest curve sample.
//
// Between allocations each leg is buy-and-hold: NAV_t = NAV_k × (cash +
// Σ w_i × P_i(t) / P_i(t_k)), where cash = 1 − Σ w_i. On each allocation
// the book is reset to the new weights at the then-current NAV. A vault
// without a curve, or whose curve starts after the allocation, is held
// as cash until it has data. It returns nil when there is nothing to
// simulate.
func IndexNAV(idx Index, curves map[string]domain.Series) domain.Series {
	allocs := append([]IndexAlloc(nil), idx.Allocs...)
	sort.SliceStable(allocs, func(i, j int) bool { return allocs[i].TS < allocs[j].TS })
	if len(allocs) == 0 {
		return nil
	}
	norm := make(map[string]domain.Series, len(curves))
	var end int64
	for k, s := range curves {
		s = append(domain.Series(nil), s...)
		sort.SliceStable(s, func(i, j int) bool { return s[i].T < s[j].T })
		norm[strings.ToLower(k)] = s
		if len(s) > 0 {
			end = max(end, s[len(s)-1].T)
		}
	}
	start := allocs[0].TS
	if end <= start {
		return nil
	}
	// valueAt: latest sample at or before t (ok=false before the first).
	valueAt := func(s domain.Series, t int64) (float64, bool) {
		i := sort.Search(len(s), func(i int) bool { return s[i].T > t })
		if i == 0 {
			return 0, false
		}
		return s[i-1].V, s[i-1].V >= 0
	}
	type leg struct {
		s    domain.Series
		w    float64
		base float64
	}
	day := int64(24 * time.Hour / time.Millisecond)
	var times []int64
	for t := start; t < end; t += day {
		times = append(times, t)
	}
	times = append(times, end)

	nav, out := 1.0, make(domain.Series, 0, len(times))
	var legs []leg
	cash, navK := 1.0, 1.0
	next := 0
	value := func(t int64) float64 {
		v := cash
		for _, l := range legs {
			p, ok := valueAt(l.s, t)
			if !ok {
				v += l.w // not yet priced: still cash
				continue
			}
			v += l.w * p / l.base
		}
		return navK * v
	}
	rebalance := func(a IndexAlloc, t int64) {
		navK, cash, legs = nav, 1, legs[:0]
		for addr, w := range a.Weights {
			if w <= 0 {
				continue
			}
			s := norm[strings.ToLower(addr)]
			base, ok := valueAt(s, t)
			if !ok || base <= 0 {
				continue // no data yet: stays cash for this period
			}
			legs = append(legs, leg{s: s, w: w, base: base})
			cash -= w
		}
	}
	for _, t := range times {
		for next < len(allocs) && allocs[next].TS <= t {
			nav = value(allocs[next].TS)
			rebalance(allocs[next], allocs[next].TS)
			next++
		}
		nav = value(t)
		out = append(out, domain.Point{T: t, V: nav})
	}
	return out
}

// IndexMember is one vault in an index allocation.
type IndexMember struct {
	Address string  `json:"address"`
	Weight  float64 `json:"weight"` // fraction of the index
}

// IndexMembers returns the current members (latest allocation) of an
// index with their weights, largest first.
func IndexMembers(idx Index) []IndexMember {
	if len(idx.Allocs) == 0 {
		return nil
	}
	latest := idx.Allocs[0]
	for _, a := range idx.Allocs[1:] {
		if a.TS > latest.TS {
			latest = a
		}
	}
	out := make([]IndexMember, 0, len(latest.Weights))
	for a, w := range latest.Weights {
		if w > 0 {
			out = append(out, IndexMember{strings.ToLower(a), w})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		return out[i].Address < out[j].Address
	})
	return out
}

// RulesSummary renders an index rule set in one line, e.g. "top 8 by 30d calmar,
// monthly, min age 60d, min TVL $100k, max DD 20%". Unknown rule shapes
// return "".
func (d IndexDef) RulesSummary() string {
	r, err := d.ParsedRules()
	if err != nil || r == nil {
		return ""
	}
	var parts []string
	if r.Selection.Mode == "rank" && r.Selection.TopN > 0 {
		parts = append(parts, fmt.Sprintf("top %d by %s %s", r.Selection.TopN, r.Selection.Lookback, r.Selection.Metric))
	} else if r.Selection.Metric != "" {
		parts = append(parts, fmt.Sprintf("%s %s over %s", r.Selection.Mode, r.Selection.Metric, r.Selection.Lookback))
	}
	if r.Schedule.Reconstitute != "" {
		parts = append(parts, r.Schedule.Reconstitute)
	}
	if r.Filters.MinAgeDays != nil && *r.Filters.MinAgeDays > 0 {
		parts = append(parts, fmt.Sprintf("min age %dd", *r.Filters.MinAgeDays))
	}
	if r.Filters.MinTVL != nil && *r.Filters.MinTVL > 0 {
		parts = append(parts, "min TVL "+compactUSD(*r.Filters.MinTVL))
	}
	if r.Filters.MaxDrawdownPct != nil && *r.Filters.MaxDrawdownPct > 0 {
		parts = append(parts, fmt.Sprintf("max DD %g%%", *r.Filters.MaxDrawdownPct))
	}
	return strings.Join(parts, ", ")
}

func compactUSD(v float64) string {
	switch {
	case v >= 1e6:
		return fmt.Sprintf("$%gM", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("$%gk", v/1e3)
	}
	return fmt.Sprintf("$%g", v)
}

// clean strips terminal control and bidi characters from the third-party
// text of an index (SECURITY S3): name, description, creator and notes.
func (ix *Index) clean() {
	ix.Def.Name = domain.CleanText(ix.Def.Name)
	ix.Def.Description = domain.CleanText(ix.Def.Description)
	ix.Def.Creator = domain.CleanText(ix.Def.Creator)
	for i := range ix.Allocs {
		for j, n := range ix.Allocs[i].Notes {
			ix.Allocs[i].Notes[j] = domain.CleanText(n)
		}
	}
	for i := range ix.Def.PreAllocs {
		for j, n := range ix.Def.PreAllocs[i].Notes {
			ix.Def.PreAllocs[i].Notes[j] = domain.CleanText(n)
		}
	}
}
