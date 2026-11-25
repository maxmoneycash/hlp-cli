package metrics

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

const (
	dayMs  = int64(24 * time.Hour / time.Millisecond)
	yearMs = 365 * dayMs

	// dailyMinSpan is the shortest curve that Compute resamples to daily
	// samples before computing volatility-based ratios. Shorter curves (an
	// intraday paper session, a 1-day window) use their raw spacing.
	dailyMinSpan = 7 * dayMs
)

// hlpAddress is the Hyperliquidity Provider vault (unlimited capacity, no
// leader skin requirement).
const hlpAddress = "0xdfc24b077bc1425ad1dea75bcb6f8158e10df303"

// finite maps NaN and ±Inf to 0 so every number survives JSON encoding.
func finite(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return x
}

// CurveFromPortfolio builds a flow-neutral NAV curve (starts at 1.0) from
// aligned account-value and cumulative-PnL histories.
//
// Each period return is modified Dietz (domain.ModifiedDietz): the net
// external flow F = (A_i - A_{i-1}) - (P_i - P_{i-1}) is assumed to arrive
// mid-period, so r_i = (P_i - P_{i-1}) / (A_{i-1} + F/2). Deposits and
// withdrawals therefore do not move the curve, and PnL earned on money that
// arrived during the interval is not credited to the smaller starting base
// (simple Dietz, ΔP / A_{i-1}, over-credited deposit-heavy periods). An
// interval whose flow is at least half its starting equity, or that starts
// with a non-positive account value (an empty vault before its first
// deposit), is unmeasurable and contributes a zero return. Returns are
// floored at domain.DietzFloor.
//
// Points are keyed on av timestamps; the PnL used for each point is the
// latest PnL sample at or before it.
func CurveFromPortfolio(av, pnl domain.Series) domain.Series {
	if len(av) == 0 || len(pnl) == 0 {
		return nil
	}
	av = sorted(av)
	pnl = sorted(pnl)
	out := make(domain.Series, 0, len(av))
	j := -1
	pnlAt := func(t int64) (float64, bool) {
		for j+1 < len(pnl) && pnl[j+1].T <= t {
			j++
		}
		if j < 0 {
			return 0, false
		}
		return pnl[j].V, true
	}
	nav := 1.0
	var prevA, prevP float64
	started := false
	for _, p := range av {
		pv, ok := pnlAt(p.T)
		if !ok {
			continue
		}
		if started {
			if r, ok := domain.ModifiedDietz(prevA, p.V, pv-prevP); ok {
				nav *= 1 + r
			}
		}
		started = true
		out = append(out, domain.Point{T: p.T, V: nav})
		prevA, prevP = p.V, pv
	}
	return out
}

// sorted returns s ordered by time (a copy only when it is out of order).
func sorted(s domain.Series) domain.Series {
	if sort.SliceIsSorted(s, func(i, j int) bool { return s[i].T < s[j].T }) {
		return s
	}
	c := append(domain.Series(nil), s...)
	sort.SliceStable(c, func(i, j int) bool { return c[i].T < c[j].T })
	return c
}

// Normalize rescales a price/NAV series to start at 1.0. Leading points
// with a non-positive value are dropped (they cannot be a base).
func Normalize(s domain.Series) domain.Series {
	start := -1
	for i, p := range s {
		if p.V > 0 {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	base := s[start].V
	out := make(domain.Series, 0, len(s)-start)
	for _, p := range s[start:] {
		out = append(out, domain.Point{T: p.T, V: p.V / base})
	}
	return out
}

// Returns returns simple period returns of a curve: c_i / c_{i-1} - 1.
// Periods starting at a non-positive value are skipped.
func Returns(curve domain.Series) []float64 {
	if len(curve) < 2 {
		return nil
	}
	out := make([]float64, 0, len(curve)-1)
	for i := 1; i < len(curve); i++ {
		prev := curve[i-1].V
		if prev <= 0 {
			continue
		}
		out = append(out, curve[i].V/prev-1)
	}
	return out
}

// PeriodsPerYear infers the sampling frequency from the median spacing:
// 365 days / median(dt). It returns 0 for fewer than two distinct samples.
func PeriodsPerYear(curve domain.Series) float64 {
	if len(curve) < 2 {
		return 0
	}
	dts := make([]int64, 0, len(curve)-1)
	for i := 1; i < len(curve); i++ {
		if dt := curve[i].T - curve[i-1].T; dt > 0 {
			dts = append(dts, dt)
		}
	}
	if len(dts) == 0 {
		return 0
	}
	sort.Slice(dts, func(i, j int) bool { return dts[i] < dts[j] })
	var med float64
	if n := len(dts); n%2 == 1 {
		med = float64(dts[n/2])
	} else {
		med = float64(dts[n/2-1]+dts[n/2]) / 2
	}
	return float64(yearMs) / med
}

// Daily resamples a curve to one point per UTC day (the last value of each
// day, stamped at that day's midnight), forward-filling days without
// samples so that sparse history (weekly snapshots) spreads its variance
// over the days it covers instead of masquerading as one daily move.
func Daily(curve domain.Series) domain.Series {
	if len(curve) == 0 {
		return nil
	}
	curve = sorted(curve)
	first := floorDay(curve[0].T)
	last := floorDay(curve[len(curve)-1].T)
	out := make(domain.Series, 0, (last-first)/dayMs+1)
	j := 0
	v := curve[0].V
	for d := first; d <= last; d += dayMs {
		for j < len(curve) && curve[j].T < d+dayMs {
			v = curve[j].V
			j++
		}
		out = append(out, domain.Point{T: d, V: v})
	}
	return out
}

func floorDay(t int64) int64 {
	d := t - t%dayMs
	if t < 0 && t%dayMs != 0 {
		d -= dayMs
	}
	return d
}

// Drawdown returns the drawdown series (<= 0 fractions): (c - peak) / peak.
func Drawdown(curve domain.Series) domain.Series {
	if len(curve) == 0 {
		return nil
	}
	out := make(domain.Series, len(curve))
	peak := math.Inf(-1)
	for i, p := range curve {
		if p.V > peak {
			peak = p.V
		}
		dd := 0.0
		if peak > 0 {
			dd = (p.V - peak) / peak
		}
		out[i] = domain.Point{T: p.T, V: finite(dd)}
	}
	return out
}

// MaxDrawdown returns the worst drawdown and its peak/trough times.
// A curve that never falls returns (0, 0, 0).
func MaxDrawdown(curve domain.Series) (dd float64, peakT, troughT int64) {
	peak := math.Inf(-1)
	var curPeakT int64
	for _, p := range curve {
		if p.V > peak {
			peak, curPeakT = p.V, p.T
		}
		if peak <= 0 {
			continue
		}
		if d := (p.V - peak) / peak; d < dd {
			dd, peakT, troughT = d, curPeakT, p.T
		}
	}
	return dd, peakT, troughT
}

func meanStd(x []float64) (mean, std float64) {
	n := float64(len(x))
	if n == 0 {
		return 0, 0
	}
	for _, v := range x {
		mean += v
	}
	mean /= n
	if n < 2 {
		return mean, 0
	}
	var ss float64
	for _, v := range x {
		ss += (v - mean) * (v - mean)
	}
	return mean, math.Sqrt(ss / (n - 1))
}

// negligible reports whether a dispersion is floating-point noise relative
// to the mean (a perfectly constant return stream).
func negligible(std, mean float64) bool {
	return std <= 1e-12*math.Max(1, math.Abs(mean))
}

// Sharpe is mean(r)/std(r) * sqrt(periodsPerYear) with rf = 0 and the
// sample standard deviation. It returns 0 when undefined: fewer than two
// returns, or zero dispersion (a constant-growth curve would be +Inf).
func Sharpe(rets []float64, periodsPerYear float64) float64 {
	if len(rets) < 2 || periodsPerYear <= 0 {
		return 0
	}
	m, s := meanStd(rets)
	if negligible(s, m) {
		return 0
	}
	return finite(m / s * math.Sqrt(periodsPerYear))
}

// Sortino is mean(r)/downsideDev(r) * sqrt(periodsPerYear) with a 0 target;
// downsideDev = sqrt(Σ min(r,0)² / n). It returns 0 when there are no
// losing periods (undefined) or fewer than two returns.
func Sortino(rets []float64, periodsPerYear float64) float64 {
	if len(rets) < 2 || periodsPerYear <= 0 {
		return 0
	}
	m, _ := meanStd(rets)
	var ss float64
	for _, r := range rets {
		if r < 0 {
			ss += r * r
		}
	}
	dd := math.Sqrt(ss / float64(len(rets)))
	if negligible(dd, m) {
		return 0
	}
	return finite(m / dd * math.Sqrt(periodsPerYear))
}

// CAGR from first to last point: (last/first)^(365d/elapsed) - 1. It
// returns 0 for curves shorter than one day or with a non-positive start,
// where annualizing is meaningless.
func CAGR(curve domain.Series) float64 {
	if len(curve) < 2 {
		return 0
	}
	first, last := curve[0], curve[len(curve)-1]
	elapsed := last.T - first.T
	if first.V <= 0 || elapsed < dayMs {
		return 0
	}
	if last.V <= 0 {
		return -1
	}
	return finite(math.Pow(last.V/first.V, float64(yearMs)/float64(elapsed)) - 1)
}

// Monthly returns end-of-month returns (UTC months), oldest first. Each
// month's return is EOM value / previous EOM value - 1; the first month is
// measured from the curve's first point. The current (partial) month is
// included.
func Monthly(curve domain.Series) []domain.MonthlyReturn {
	if len(curve) < 2 {
		return nil
	}
	curve = sorted(curve)
	type ym struct{ y, m int }
	var keys []ym
	eom := map[ym]float64{}
	for _, p := range curve {
		t := time.UnixMilli(p.T).UTC()
		k := ym{t.Year(), int(t.Month())}
		if _, ok := eom[k]; !ok {
			keys = append(keys, k)
		}
		eom[k] = p.V
	}
	out := make([]domain.MonthlyReturn, 0, len(keys))
	base := curve[0].V
	for _, k := range keys {
		v := eom[k]
		r := 0.0
		if base > 0 {
			r = v/base - 1
		}
		out = append(out, domain.MonthlyReturn{Year: k.y, Month: k.m, Ret: finite(r)})
		base = v
	}
	return out
}

// windowStart returns the start time of window w for a curve ending at end
// (ms). ok is false for WAll and unknown windows.
func windowStart(end int64, w domain.Window) (int64, bool) {
	t := time.UnixMilli(end).UTC()
	switch w {
	case domain.W1D:
		return end - dayMs, true
	case domain.W1W:
		return end - 7*dayMs, true
	case domain.W1M:
		return t.AddDate(0, -1, 0).UnixMilli(), true
	case domain.W3M:
		return t.AddDate(0, -3, 0).UnixMilli(), true
	case domain.W6M:
		return t.AddDate(0, -6, 0).UnixMilli(), true
	case domain.W1Y:
		return t.AddDate(-1, 0, 0).UnixMilli(), true
	case domain.WYTD:
		return time.Date(t.Year(), 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), true
	}
	return 0, false
}

// valueAt returns the latest value at or before t, or the first value when
// t precedes the curve (the window covers the whole history).
func valueAt(curve domain.Series, t int64) float64 {
	i := sort.Search(len(curve), func(i int) bool { return curve[i].T > t })
	if i == 0 {
		return curve[0].V
	}
	return curve[i-1].V
}

// WindowReturn returns curve(end)/curve(start of window)-1 for a window
// ending at the last point (ytd = since Jan 1 UTC). The base is the latest
// sample at or before the window start; a window longer than the history
// measures from the first point. WAll measures from the first point.
func WindowReturn(curve domain.Series, w domain.Window) float64 {
	if len(curve) < 2 {
		return 0
	}
	curve = sorted(curve)
	end := curve[len(curve)-1]
	base := curve[0].V
	if start, ok := windowStart(end.T, w); ok {
		base = valueAt(curve, start)
	}
	if base <= 0 {
		return 0
	}
	return finite(end.V/base - 1)
}

// Compute fills a domain.Metrics from a curve and optional closed-trade PnLs
// (for win rate and profit factor; zero PnLs are ignored, as on hlp.xyz).
//
// Dense curves spanning at least 7 days are resampled to daily samples (see
// Daily) before Vol, Sharpe and Sortino, matching hlp.xyz's "daily returns"
// methodology. Sparse curves (median gap over 1.5 days, IsSparse) are
// estimated directly over their uneven intervals (IrregularStats) instead,
// because forward-filling weekly snapshots invents quiet days. ProfitFactor
// is unavailable when there are no losing trades; WinRate is then 1.
func Compute(curve domain.Series, closedPnL []float64) domain.Metrics {
	var m domain.Metrics
	c := Normalize(sorted(curve))
	if len(c) > 0 {
		m.Start, m.End = c[0].T, c[len(c)-1].T
		m.Days = float64(m.End-m.Start) / float64(dayMs)
	}
	if len(c) >= 2 {
		m.TotalReturn = finite(c[len(c)-1].V/c[0].V - 1)
		m.CAGR = CAGR(c)
		irregular := false
		if IsSparse(c) {
			m.Vol, m.Sharpe, m.Sortino, irregular = IrregularStats(c)
		}
		if !irregular {
			sample := c
			if m.End-m.Start >= dailyMinSpan {
				sample = Daily(c)
			}
			rets := Returns(sample)
			ppy := PeriodsPerYear(sample)
			_, sd := meanStd(rets)
			m.Vol = finite(sd * math.Sqrt(ppy))
			m.Sharpe = Sharpe(rets, ppy)
			m.Sortino = Sortino(rets, ppy)
		}
		m.MaxDD, _, _ = MaxDrawdown(c)
		if m.MaxDD < 0 {
			m.Calmar = finite(m.CAGR / math.Abs(m.MaxDD))
		}
		months := Monthly(c)
		for i, mr := range months {
			if i == 0 || mr.Ret > m.BestMonth {
				m.BestMonth = mr.Ret
			}
			if i == 0 || mr.Ret < m.WorstMonth {
				m.WorstMonth = mr.Ret
			}
		}
	}
	var wins, losses int
	var gw, gl float64
	for _, p := range closedPnL {
		switch {
		case p > 0:
			wins++
			gw += p
		case p < 0:
			losses++
			gl += -p
		}
	}
	if n := wins + losses; n > 0 {
		m.WinRate = float64(wins) / float64(n)
		m.WinRateKnown = true
	}
	if gl > 0 {
		m.ProfitFactor = finite(gw / gl)
		m.ProfitFactorKnown = true
	}
	return m
}

// alignedDailyReturns resamples both series to daily samples and returns
// the paired daily returns over the days both cover.
func alignedDailyReturns(a, b domain.Series) (ra, rb []float64) {
	da, db := Daily(a), Daily(b)
	if len(da) < 2 || len(db) < 2 {
		return nil, nil
	}
	start := max(da[0].T, db[0].T)
	end := min(da[len(da)-1].T, db[len(db)-1].T)
	if end-start < dayMs {
		return nil, nil
	}
	ia := int((start - da[0].T) / dayMs)
	ib := int((start - db[0].T) / dayMs)
	n := int((end-start)/dayMs) + 1
	for k := 1; k < n; k++ {
		pa, ca := da[ia+k-1].V, da[ia+k].V
		pb, cb := db[ib+k-1].V, db[ib+k].V
		if pa <= 0 || pb <= 0 {
			continue
		}
		ra = append(ra, ca/pa-1)
		rb = append(rb, cb/pb-1)
	}
	return ra, rb
}

// Correlation is the Pearson correlation of daily returns after aligning
// both series on UTC days (nearest-previous sample, see Daily). It returns
// 0 with fewer than three overlapping returns or zero variance.
func Correlation(a, b domain.Series) float64 {
	ra, rb := alignedDailyReturns(a, b)
	if len(ra) < 3 {
		return 0
	}
	ma, sa := meanStd(ra)
	mb, sb := meanStd(rb)
	if negligible(sa, ma) || negligible(sb, mb) {
		return 0
	}
	var cov float64
	for i := range ra {
		cov += (ra[i] - ma) * (rb[i] - mb)
	}
	cov /= float64(len(ra) - 1)
	return finite(cov / (sa * sb))
}

// Beta is cov(subject, bench) / var(bench) of aligned daily returns. It
// returns 0 with fewer than three overlapping returns or a flat benchmark.
func Beta(subject, bench domain.Series) float64 {
	rs, rb := alignedDailyReturns(subject, bench)
	if len(rs) < 3 {
		return 0
	}
	ms, _ := meanStd(rs)
	mb, sb := meanStd(rb)
	if negligible(sb, mb) {
		return 0
	}
	var cov float64
	for i := range rs {
		cov += (rs[i] - ms) * (rb[i] - mb)
	}
	cov /= float64(len(rs) - 1)
	return finite(cov / (sb * sb))
}

// Capacity implements the leader-5% deposit-room rule (USD; -1 = unlimited).
// Hyperliquid requires a vault leader to keep at least 5% of the vault, so
// room = tvl × (leaderFraction − 0.05) / 0.05. HLP is unlimited, a leader
// below 4.9% leaves no room, and the result is never negative.
func Capacity(tvl, leaderFraction float64, isHLP bool) float64 {
	if isHLP {
		return -1
	}
	if leaderFraction < 0.049 || tvl <= 0 {
		return 0
	}
	return finite(math.Max(0, tvl*(leaderFraction-0.05)/0.05))
}

// TearsheetOpts controls Tearsheet.
type TearsheetOpts struct {
	Title      string
	Benchmarks map[string]domain.Series // HLP, BTC, HYPE... raw price/NAV curves
	ClosedPnL  []float64
	Now        time.Time
}

// benchmarkOrder fixes the display order of well-known benchmarks; others
// follow alphabetically.
var benchmarkOrder = map[string]int{"HLP": 0, "BTC": 1, "HYPE": 2, "ETH": 3, "SPY": 4, "SOL": 5}

// Clip returns the part of s inside [start, end]. When s has a sample before
// start, its value is carried to a point at exactly start so the clipped
// curve begins where the subject begins.
func Clip(s domain.Series, start, end int64) domain.Series {
	s = sorted(s)
	var out domain.Series
	i := sort.Search(len(s), func(i int) bool { return s[i].T >= start })
	if i > 0 && (i == len(s) || s[i].T > start) {
		out = append(out, domain.Point{T: start, V: s[i-1].V})
	}
	for ; i < len(s) && s[i].T <= end; i++ {
		out = append(out, s[i])
	}
	return out
}

// Tearsheet assembles curve, drawdown, metrics, monthly table and benchmark
// comparisons (each benchmark clipped to the subject's date range,
// normalized to 1.0 and scored with Corr/Beta against the subject).
// Benchmarks with fewer than two points inside the range are omitted.
func Tearsheet(curve domain.Series, o TearsheetOpts) domain.Tearsheet {
	c := Normalize(sorted(curve))
	ts := domain.Tearsheet{
		Title:    o.Title,
		Curve:    c,
		Drawdown: Drawdown(c),
		Metrics:  Compute(c, o.ClosedPnL),
		Monthly:  Monthly(c),
	}
	if !o.Now.IsZero() {
		ts.Meta.AsOf = o.Now
	}
	if w := ShortWindowWarning(ts.Metrics.Days); w != "" {
		ts.Meta.Warnings = append(ts.Meta.Warnings, w)
	}
	if w := SparseWarning(c); w != "" {
		ts.Meta.Warnings = append(ts.Meta.Warnings, w)
	}
	if len(c) < 2 {
		return ts
	}
	start, end := c[0].T, c[len(c)-1].T
	names := make([]string, 0, len(o.Benchmarks))
	for n := range o.Benchmarks {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		oi, iok := benchmarkOrder[names[i]]
		oj, jok := benchmarkOrder[names[j]]
		switch {
		case iok && jok:
			return oi < oj
		case iok != jok:
			return iok
		}
		return names[i] < names[j]
	})
	for _, n := range names {
		bc := Normalize(Clip(o.Benchmarks[n], start, end))
		if len(bc) < 2 {
			continue
		}
		ts.Benchmarks = append(ts.Benchmarks, domain.Benchmark{
			Name:    n,
			Curve:   bc,
			Metrics: Compute(bc, nil),
			Corr:    Correlation(c, bc),
			Beta:    Beta(c, bc),
		})
	}
	return ts
}

// MinAnnualizedDays is the shortest span whose annualized metrics (CAGR,
// Calmar, Sharpe, Sortino, Vol) are reported without a warning.
const MinAnnualizedDays = 90

// ShortWindowWarning returns the warning for annualized metrics computed
// over fewer than MinAnnualizedDays days ("" when days is enough or 0).
// Tearsheet adds it to Meta.Warnings; callers that build their own meta
// (backtests) should copy it.
func ShortWindowWarning(days float64) string {
	// A day of slack: a "--days 90" run spans a little under 90 days of
	// closed bars and should not warn that it is under 90.
	if days <= 0 || days > MinAnnualizedDays-1 {
		return ""
	}
	return fmt.Sprintf("annualized metrics over %.0fd (<%dd) are unreliable", math.Floor(days), MinAnnualizedDays)
}

// humanAge renders a duration as "45m", "7h" or "3d".
func humanAge(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(int(d.Minutes()), 0))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// Checklist IDs (stable; cards, JSON consumers and tests key on them).
const (
	CheckMaxDD         = "max_dd"
	CheckAge           = "age"
	CheckTVL           = "tvl"
	CheckLeaderSkin    = "leader_skin"
	CheckCapacity      = "capacity"
	CheckConcentration = "concentration"
	CheckLeverage      = "leverage"
	CheckDepositsOpen  = "deposits_open"
	CheckDataFresh     = "data_fresh"
	CheckSharpeVsHLP   = "sharpe_vs_hlp"
)

// Checklist encodes the due-diligence rules:
// max_dd (warn < -25%, fail < -50%), age (warn < 90d), tvl (warn < $100k),
// leader_skin (warn < 10%, fail < 5%), capacity (fail when 0), concentration
// (warn when top-10 followers > 80% of equity), leverage (warn max gross > 5x),
// deposits_open, data_fresh (warn when stale), sharpe_vs_hlp.
//
// Inputs the detail does not carry yield CheckNA rather than a guess:
// unknown leader fraction (0), no followers, no leverage data, no HLP
// metrics. Performance numbers come from d.Tearsheet when present, else
// from d.NAV, else from d.Summary. The leader is excluded from the
// concentration check (leader_skin covers it). The order is fixed.
func Checklist(d domain.VaultDetail, hlp *domain.Metrics) []domain.Check {
	s := d.Summary
	isHLP := s.Address == hlpAddress || d.CapacityUSD < 0
	out := make([]domain.Check, 0, 10)
	add := func(id, label string, st domain.CheckStatus, detail string) {
		out = append(out, domain.Check{ID: id, Label: label, Status: st, Detail: detail})
	}

	// Performance source.
	var perf *domain.Metrics
	switch {
	case d.Tearsheet != nil && d.Tearsheet.Metrics.End > 0:
		perf = &d.Tearsheet.Metrics
	case len(d.NAV) >= 2:
		m := Compute(d.NAV, nil)
		perf = &m
	}
	maxDD, sharpe, haveDD := s.MaxDD, s.Sharpe, s.MaxDD != 0 || s.Sharpe != 0
	if perf != nil {
		maxDD, sharpe, haveDD = perf.MaxDD, perf.Sharpe, true
	}

	// max_dd
	switch {
	case !haveDD:
		add(CheckMaxDD, "Max drawdown", domain.CheckNA, "no performance history")
	case maxDD < -0.50:
		add(CheckMaxDD, "Max drawdown", domain.CheckFail, pct(maxDD)+" (fails below -50%)")
	case maxDD < -0.25:
		add(CheckMaxDD, "Max drawdown", domain.CheckWarn, pct(maxDD)+" (warns below -25%)")
	default:
		add(CheckMaxDD, "Max drawdown", domain.CheckPass, pct(maxDD))
	}

	// age
	age := s.AgeDays
	if age == 0 && !s.CreatedAt.IsZero() && !d.Meta.AsOf.IsZero() {
		age = int(d.Meta.AsOf.Sub(s.CreatedAt) / (24 * time.Hour))
	}
	switch {
	case age <= 0 && s.CreatedAt.IsZero():
		add(CheckAge, "Track record", domain.CheckNA, "age unknown")
	case age < 90:
		add(CheckAge, "Track record", domain.CheckWarn, itoa(age)+" days (warns under 90)")
	default:
		add(CheckAge, "Track record", domain.CheckPass, itoa(age)+" days")
	}

	// tvl
	switch {
	case s.TVL < 100_000:
		add(CheckTVL, "TVL", domain.CheckWarn, usd(s.TVL)+" (warns under $100k)")
	default:
		add(CheckTVL, "TVL", domain.CheckPass, usd(s.TVL))
	}

	// leader_skin
	lf := s.LeaderFraction
	switch {
	case isHLP:
		add(CheckLeaderSkin, "Leader stake", domain.CheckNA, "protocol vault")
	case lf <= 0:
		add(CheckLeaderSkin, "Leader stake", domain.CheckNA, "leader share unknown")
	case lf < 0.05:
		add(CheckLeaderSkin, "Leader stake", domain.CheckFail, pct(lf)+" of the vault (fails under 5%)")
	case lf < 0.10:
		add(CheckLeaderSkin, "Leader stake", domain.CheckWarn, pct(lf)+" of the vault (warns under 10%)")
	default:
		add(CheckLeaderSkin, "Leader stake", domain.CheckPass, pct(lf)+" of the vault")
	}

	// capacity
	switch {
	case s.Closed || (!s.AllowDeposits && !isHLP):
		add(CheckCapacity, "Deposit room", domain.CheckNA, "n/a: deposits are closed")
	case isHLP || d.CapacityUSD < 0:
		add(CheckCapacity, "Deposit room", domain.CheckPass, "unlimited")
	case lf <= 0 && d.CapacityUSD == 0:
		add(CheckCapacity, "Deposit room", domain.CheckNA, "leader share unknown")
	case d.CapacityUSD <= 0:
		add(CheckCapacity, "Deposit room", domain.CheckFail, "no deposit room (leader 5% rule)")
	default:
		add(CheckCapacity, "Deposit room", domain.CheckPass, usd(d.CapacityUSD)+" before the leader hits 5%")
	}

	// concentration
	if len(d.Followers) == 0 {
		add(CheckConcentration, "Concentration", domain.CheckNA, "no depositor list")
	} else {
		eq := make([]float64, 0, len(d.Followers))
		var total float64
		for _, f := range d.Followers {
			if f.Equity <= 0 || (s.Leader != "" && equalFold(f.User, s.Leader)) {
				continue
			}
			eq = append(eq, f.Equity)
		}
		sort.Sort(sort.Reverse(sort.Float64Slice(eq)))
		var top float64
		for i, e := range eq {
			if i < 10 {
				top += e
			}
		}
		total = s.TVL
		if total <= 0 {
			for _, f := range d.Followers {
				total += math.Max(0, f.Equity)
			}
		}
		share := 0.0
		if total > 0 {
			share = top / total
		}
		if share > 0.80 {
			add(CheckConcentration, "Concentration", domain.CheckWarn, "top 10 depositors hold "+pct(share)+" (warns over 80%)")
		} else {
			add(CheckConcentration, "Concentration", domain.CheckPass, "top 10 depositors hold "+pct(share))
		}
	}

	// leverage
	switch lev := s.MaxGrossLev; {
	case lev <= 0:
		add(CheckLeverage, "Peak leverage", domain.CheckNA, "no leverage history")
	case lev > 5:
		add(CheckLeverage, "Peak leverage", domain.CheckWarn, ftoa(lev, 1)+"x gross (warns over 5x)")
	default:
		add(CheckLeverage, "Peak leverage", domain.CheckPass, ftoa(lev, 1)+"x gross")
	}

	// deposits_open
	switch {
	case s.Closed:
		add(CheckDepositsOpen, "Deposits", domain.CheckFail, "vault is closed")
	case !s.AllowDeposits:
		add(CheckDepositsOpen, "Deposits", domain.CheckWarn, "leader has closed deposits")
	default:
		add(CheckDepositsOpen, "Deposits", domain.CheckPass, "accepting deposits")
	}

	// data_fresh
	if d.Meta.Stale || s.Stale {
		what := "hlp.xyz metrics"
		if d.Meta.Stale && !s.Stale {
			what = SourceLabel(d.Meta.Source)
		}
		detail := what + " are stale"
		// the source's own stamp, never the fetch time (AsOf)
		if !s.UpdatedAt.IsZero() && !d.Meta.AsOf.IsZero() && d.Meta.AsOf.After(s.UpdatedAt) {
			detail += " (last updated " + humanAge(d.Meta.AsOf.Sub(s.UpdatedAt)) + " ago)"
		}
		add(CheckDataFresh, "Freshness", domain.CheckWarn, detail)
	} else {
		add(CheckDataFresh, "Freshness", domain.CheckPass, "fresh")
	}

	// sharpe_vs_hlp
	switch {
	case isHLP:
		add(CheckSharpeVsHLP, "vs HLP", domain.CheckNA, "this is HLP")
	case hlp == nil || !haveDD:
		add(CheckSharpeVsHLP, "vs HLP", domain.CheckNA, "no comparison data")
	case sharpe >= hlp.Sharpe:
		add(CheckSharpeVsHLP, "vs HLP", domain.CheckPass, ftoa(sharpe, 2)+" vs "+ftoa(hlp.Sharpe, 2)+" Sharpe")
	default:
		add(CheckSharpeVsHLP, "vs HLP", domain.CheckWarn, ftoa(sharpe, 2)+" vs "+ftoa(hlp.Sharpe, 2)+" Sharpe")
	}
	return out
}

// SourceLabel maps an internal data source name ("hyperliquid-ws",
// "stats-data+hlp.xyz", "hlpxyz-v6") to what a person calls it
// ("Hyperliquid", "hlp.xyz").
func SourceLabel(src string) string {
	var parts []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(src, func(r rune) bool { return r == '+' || r == ',' || r == ' ' }) {
		l := strings.ToLower(f)
		name := f
		switch {
		case strings.Contains(l, "hlpxyz"), strings.Contains(l, "hlp.xyz"):
			name = "hlp.xyz"
		case strings.Contains(l, "hyperliquid"), strings.Contains(l, "stats-data"), l == "hl", l == "ws":
			name = "Hyperliquid"
		case l == "dry-run":
			name = "dry run"
		}
		if !seen[name] {
			seen[name] = true
			parts = append(parts, name)
		}
	}
	if len(parts) == 0 {
		return src
	}
	return strings.Join(parts, " + ")
}
