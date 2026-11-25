package core

// Public-client valuation rules audited in data-BmYXk3Ng.js (T, Nt/Ft,
// gt, Xt and yt). See docs/verification/index-client-methodology.md in the UI
// checkout for asset hashes and captured monthly outputs. The engine retains
// strict missing-member disclosure instead of the client's silent cash fallback.

import (
	"math"
	"sort"
	"strings"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hlpxyz"
)

const websiteDay = int64(86400000)

type IndexHistoryCoverage struct {
	Interpolation              string   `json:"interpolation"`
	EndpointPolicy             string   `json:"endpointPolicy"`
	StitchedMembers            []string `json:"stitchedMembers"`
	ClampedBeforeMembers       []string `json:"clampedBeforeMembers"`
	ClampedAfterMembers        []string `json:"clampedAfterMembers"`
	ExcludedNonPositiveMembers []string `json:"excludedNonPositiveMembers"`
}

func websiteValue(s domain.Series, at int64) float64 {
	if len(s) == 0 {
		return math.NaN()
	}
	if at <= s[0].T {
		return s[0].V
	}
	if at >= s[len(s)-1].T {
		return s[len(s)-1].V
	}
	i := sort.Search(len(s), func(i int) bool { return s[i].T > at })
	a, b := s[i-1], s[i]
	return a.V + (b.V-a.V)*float64(at-a.T)/float64(b.T-a.T)
}

// The client uses the oldest contiguous suffix of 30-day windows with at
// least 70% represented UTC dates to choose the dense daily source segment.
func websiteDenseStart(s domain.Series) int64 {
	if len(s) < 2 {
		if len(s) == 1 {
			return s[0].T
		}
		return 0
	}
	days := map[int64]bool{}
	for _, p := range s {
		days[p.T/websiteDay] = true
	}
	first, last := s[0].T/websiteDay, s[len(s)-1].T/websiteDay
	start := last
	for end := last; end >= first; end -= 30 {
		begin, count := max(first, end-29), 0
		for d := begin; d <= end; d++ {
			if days[d] {
				count++
			}
		}
		if float64(count)/float64(end-begin+1) < 0.7 {
			break
		}
		start = begin
	}
	return start * websiteDay
}

func websiteStitch(spark, history domain.Series) (domain.Series, bool) {
	if len(history) < 2 {
		return spark, false
	}
	join := spark[len(spark)-1].T
	last := history[len(history)-1].T
	if join-last <= 3*websiteDay && websiteDenseStart(history) <= last-28*websiteDay {
		i := len(spark) - 1
		for i > 0 && spark[i].T-spark[i-1].T <= 8*3600000 {
			i--
		}
		join = min(last, spark[i].T)
	} else {
		for i := len(spark) - 1; i > 0; i-- {
			if spark[i].T-spark[i-1].T > 3*websiteDay {
				join = spark[i].T
				break
			}
			join = spark[i-1].T
		}
	}
	factor := websiteValue(spark, join) / websiteValue(history, join)
	out := make(domain.Series, 0, len(spark)+len(history))
	for _, p := range history {
		if p.T < join {
			out = append(out, domain.Point{T: p.T, V: p.V * factor})
		}
	}
	stitched := len(out) > 0
	for _, p := range spark {
		if p.T >= join {
			out = append(out, p)
		}
	}
	return out, stitched
}

func websiteCurveSet(history, sparks, fallback map[string]domain.Series, now int64) (map[string]domain.Series, []string, []string) {
	keys := map[string]bool{}
	for _, source := range []map[string]domain.Series{history, sparks, fallback} {
		for a := range source {
			keys[strings.ToLower(a)] = true
		}
	}
	out, stitched, excluded := map[string]domain.Series{}, []string{}, []string{}
	for a := range keys {
		hadNonPositive := false
		clean := func(s domain.Series) domain.Series {
			for _, p := range s {
				if p.T > 0 && p.T <= now && p.V <= 0 {
					hadNonPositive = true
				}
			}
			return cleanHistory(s, now, true)
		}
		spark, hist := clean(sparks[a]), clean(history[a])
		if len(spark) >= 2 {
			var joined bool
			out[a], joined = websiteStitch(spark, hist)
			if joined {
				stitched = append(stitched, a)
			}
		} else {
			out[a] = clean(fallback[a])
		}
		if hadNonPositive {
			excluded = append(excluded, a)
		}
	}
	sort.Strings(stitched)
	sort.Strings(excluded)
	return out, stitched, excluded
}

func websiteStep(span int64) int64 {
	switch {
	case span <= 2*websiteDay:
		return 30 * 60000
	case span <= 8*websiteDay:
		return 2 * 3600000
	case span <= 35*websiteDay:
		return 6 * 3600000
	default:
		return websiteDay
	}
}

// Returned samples are explicitly modeled. Before/after sets disclose every
// endpoint clamp, including allocation baselines, without promoting source age.
func replayWebsiteIndex(ix hlpxyz.Index, curves map[string]domain.Series, start, end, step int64) (domain.Series, []string, []string, []string, int64) {
	allocs := make([]hlpxyz.IndexAlloc, 0, len(ix.Allocs))
	missing, before, after := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, a := range ix.Allocs {
		if a.TS <= 0 || a.TS > end {
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
			if len(curves[addr]) < 2 {
				missing[addr] = true
			}
		}
		if weight > 1.000001 {
			missing["allocation:weights-over-100%"] = true
		}
	}
	keys := func(set map[string]bool) []string {
		out := make([]string, 0, len(set))
		for k := range set {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	if len(allocs) == 0 || len(missing) > 0 {
		return nil, keys(before), keys(after), keys(missing), 0
	}
	sort.SliceStable(allocs, func(i, j int) bool { return allocs[i].TS < allocs[j].TS })
	start = max(start, allocs[0].TS)
	if end <= start {
		return nil, keys(before), keys(after), keys(missing), 0
	}
	asOf := end
	for a, w := range allocs[len(allocs)-1].Weights {
		if w > 0 {
			asOf = min(asOf, historyEnd(curves[strings.ToLower(a)]))
		}
	}
	valueAt := func(addr string, at int64) float64 {
		s := curves[addr]
		if at < s[0].T {
			before[addr] = true
		}
		if at > s[len(s)-1].T {
			after[addr] = true
		}
		return websiteValue(s, at)
	}
	units, cash := map[string]float64{}, 1.0
	value := func(at int64) float64 {
		nav := cash
		for a, u := range units {
			nav += u * valueAt(a, at)
		}
		return nav
	}
	times := map[int64]bool{end: true}
	first := start
	if step >= websiteDay {
		first = start / websiteDay * websiteDay
	}
	for t := first; t < end; t += step {
		if t >= start {
			times[t] = true
		}
	}
	for _, a := range allocs {
		if a.TS >= start {
			times[a.TS] = true
		}
	}
	ordered := make([]int64, 0, len(times))
	for t := range times {
		ordered = append(ordered, t)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	out, next := make(domain.Series, 0, len(ordered)), 0
	for _, t := range ordered {
		for next < len(allocs) && allocs[next].TS <= t {
			a := allocs[next]
			nav := value(a.TS)
			units, cash = map[string]float64{}, nav
			for addr, w := range a.Weights {
				if w <= 0 {
					continue
				}
				addr = strings.ToLower(addr)
				base := valueAt(addr, a.TS)
				units[addr] = w * nav / base
				cash -= w * nav
			}
			next++
		}
		out = append(out, domain.Point{T: t, V: value(t)})
	}
	return out, keys(before), keys(after), keys(missing), asOf
}
