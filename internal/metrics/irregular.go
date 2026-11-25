package metrics

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// SparseGapMs is the median sample spacing above which Compute estimates
// volatility, Sharpe and Sortino directly over the curve's own uneven
// intervals (IrregularStats) instead of forward-filling to daily samples,
// which would invent flat days between real samples. Hyperliquid's allTime
// portfolio window is roughly weekly; hlp.xyz NAV and paper/backtest curves
// are daily or denser and keep the daily resampling.
const SparseGapMs = 36 * 60 * 60 * 1000 // 1.5 days

// MedianGap returns the median positive spacing between samples in ms, or 0
// for fewer than two distinct samples.
func MedianGap(curve domain.Series) int64 {
	dts := make([]int64, 0, len(curve))
	for i := 1; i < len(curve); i++ {
		if dt := curve[i].T - curve[i-1].T; dt > 0 {
			dts = append(dts, dt)
		}
	}
	if len(dts) == 0 {
		return 0
	}
	sort.Slice(dts, func(i, j int) bool { return dts[i] < dts[j] })
	n := len(dts)
	if n%2 == 1 {
		return dts[n/2]
	}
	return (dts[n/2-1] + dts[n/2]) / 2
}

// IsSparse reports whether Compute uses IrregularStats for curve: at least
// a week of history whose median spacing exceeds SparseGapMs.
func IsSparse(curve domain.Series) bool {
	if len(curve) < 3 {
		return false
	}
	c := sorted(curve)
	return c[len(c)-1].T-c[0].T >= dailyMinSpan && MedianGap(c) > SparseGapMs
}

// IrregularStats estimates annualized volatility, Sharpe and Sortino from a
// curve sampled at uneven intervals, without resampling.
//
// With log returns l_i over intervals of dt_i years, T = Σ dt_i:
//
//	drift      μ  = Σ l_i / T
//	variance   σ² = Σ (l_i - μ·dt_i)² / dt_i / (n - 1)
//	downside   δ² = Σ min(l_i, 0)² / dt_i / n
//	Sharpe        = (μ + σ²/2) / σ
//	Sortino       = (μ + σ²/2) / δ
//
// μ + σ²/2 is the arithmetic annual rate, so the ratios match the daily
// convention (mean daily return / daily stdev × √365) for the same process.
// Each interval contributes in proportion to its length, so a 10-day gap is
// not mistaken for ten quiet days. ok is false with fewer than two usable
// intervals; Sharpe and Sortino are 0 when undefined (no dispersion, no
// losing interval), as in Sharpe and Sortino.
func IrregularStats(curve domain.Series) (vol, sharpe, sortino float64, ok bool) {
	c := sorted(curve)
	var ls, dts []float64
	for i := 1; i < len(c); i++ {
		p, q := c[i-1].V, c[i].V
		dt := float64(c[i].T-c[i-1].T) / float64(yearMs)
		if p <= 0 || q <= 0 || dt <= 0 {
			continue
		}
		ls = append(ls, math.Log(q/p))
		dts = append(dts, dt)
	}
	n := len(ls)
	if n < 2 {
		return 0, 0, 0, false
	}
	var sumL, sumT float64
	for i := range ls {
		sumL += ls[i]
		sumT += dts[i]
	}
	mu := sumL / sumT
	var ss, ds float64
	for i := range ls {
		e := ls[i] - mu*dts[i]
		ss += e * e / dts[i]
		if ls[i] < 0 {
			ds += ls[i] * ls[i] / dts[i]
		}
	}
	sigma := math.Sqrt(ss / float64(n-1))
	down := math.Sqrt(ds / float64(n))
	arith := mu + sigma*sigma/2
	vol = finite(sigma)
	if !negligible(sigma, arith) {
		sharpe = finite(arith / sigma)
	}
	if !negligible(down, arith) {
		sortino = finite(arith / down)
	}
	return vol, sharpe, sortino, true
}

// SparseWarning is the note Tearsheet adds when a curve's ratios come from
// IrregularStats ("" when the curve is dense enough for daily returns).
func SparseWarning(curve domain.Series) string {
	if !IsSparse(curve) {
		return ""
	}
	return fmt.Sprintf("sparse history (median gap %s): vol, Sharpe and Sortino are estimated over uneven intervals",
		humanAge(time.Duration(MedianGap(sorted(curve)))*time.Millisecond))
}
