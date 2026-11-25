package domain

import "math"

// Modified Dietz period returns.
//
// Hyperliquid's portfolio history gives account value A and cumulative PnL P
// at each sample, but not the deposits and withdrawals in between. The net
// external flow of an interval is what moved A without being PnL:
//
//	F = (A1 - A0) - (P1 - P0)
//
// Simple Dietz divides the interval's PnL by A0, which over-credits an
// interval with a large deposit: PnL earned on the new money is divided by
// the old, smaller base. Modified Dietz assumes the flow arrived mid-period
// and puts half of it in the denominator:
//
//	r = (P1 - P0) / (A0 + F/2)
//
// When the flow is large relative to the starting capital (|F| >= half of
// A0) the timing assumption dominates the answer, so the interval is
// reported as unmeasurable and callers count it as a zero return.
const (
	// DietzLargeFlow is the |F| / A0 ratio at and above which an interval
	// is unmeasurable.
	DietzLargeFlow = 0.5
	// DietzFloor bounds a period return from below (-99.99%): a wiped-out
	// interval leaves a small positive share price, so later returns and
	// log returns stay defined instead of dividing by zero.
	DietzFloor = -0.9999
)

// ModifiedDietz returns the period return of an interval that starts at
// account value a0, ends at a1 and earned pnl (the change in cumulative
// PnL). ok is false when the interval cannot be measured: no starting
// capital (a0 <= 0), a non-finite input, a net flow of at least
// DietzLargeFlow × a0, or a non-positive denominator. Callers treat an
// unmeasurable interval as a zero return.
func ModifiedDietz(a0, a1, pnl float64) (r float64, ok bool) {
	if !(a0 > 0) || math.IsInf(a0, 0) || math.IsNaN(a1) || math.IsInf(a1, 0) || math.IsNaN(pnl) || math.IsInf(pnl, 0) {
		return 0, false
	}
	flow := (a1 - a0) - pnl
	if math.Abs(flow) >= DietzLargeFlow*a0 {
		return 0, false
	}
	den := a0 + flow/2
	if !(den > 0) {
		return 0, false
	}
	r = pnl / den
	if math.IsNaN(r) || math.IsInf(r, 0) {
		return 0, false
	}
	return math.Max(r, DietzFloor), true
}
