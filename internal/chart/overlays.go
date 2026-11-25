package chart

import (
	"math"
	"strconv"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/theme"
)

// Overlay lines only ever paint (and read out) the visible window, so MAs
// and EMAs are computed over that window instead of the whole series
// (PERFORMANCE #9): a frame never allocates N floats per overlay.
//
// The window is bounded before layout runs: a chart shows at most Width
// candles (stride >= 1), ending Offset candles before the newest. SMA over
// [lb, hi) needs only the period-1 closes before lb, so it is exact. EMA
// depends on the whole history; its state at lb is carried by a full pass
// that allocates nothing, and a grid reused through CandleOpts.Into
// remembers that state so a live tick or a new bar only advances it.

// overlaySeries is one resolved overlay line. vals[k] belongs to input
// candle off+k; anything outside reads as NaN.
type overlaySeries struct {
	label string
	vals  []float64
	off   int
	col   theme.Color
}

// at returns the overlay value for input candle i (NaN outside the window).
func (s overlaySeries) at(i int) float64 {
	k := i - s.off
	if k < 0 || k >= len(s.vals) {
		return math.NaN()
	}
	return s.vals[k]
}

// overlayWindow bounds the candles any layout of o can show: [lb, hi).
func overlayWindow(n int, o CandleOpts) (lb, hi int) {
	if n == 0 {
		return 0, 0
	}
	hi = n - clampi(o.Offset, 0, n-1)
	// Layout clamps the pan so the window stays full (panOffset): the
	// newest visible candle is then at least the Width-th one.
	hi = max(hi, min(n, max(o.Width, 1)))
	lb = max(hi-max(o.Width, 1), 0)
	return lb, hi
}

// buildOverlays resolves MAs, EMAs and precomputed overlays (max 3, in that order).
func (r *candleRender) buildOverlays() []overlaySeries {
	var out []overlaySeries
	color := func(c int) theme.Color { return r.th.MA[((c%3)+3)%3] }
	lb, hi := overlayWindow(len(r.real), r.o)
	var memo *overlayMemo
	if r.o.Into != nil {
		memo = r.g.overlayMemo()
	}
	for _, p := range r.o.MAs {
		if len(out) < 3 {
			out = append(out, overlaySeries{"MA" + strconv.Itoa(p), smaWindow(r.real, p, lb, hi), lb, color(len(out))})
		}
	}
	for _, p := range r.o.EMAs {
		if len(out) < 3 {
			out = append(out, overlaySeries{"EMA" + strconv.Itoa(p), emaWindow(r.real, p, lb, hi, memo), lb, color(len(out))})
		}
	}
	for _, ov := range r.o.Overlays {
		if len(out) < 3 && len(ov.Values) == len(r.real) {
			out = append(out, overlaySeries{ov.Label, ov.Values, 0, color(ov.Color)})
		}
	}
	return out
}

// smaWindow is SMA(cs, period)[lb:hi] without touching the rest of the
// series: the same running window as SMAValues (a NaN close restarts it),
// started period-1 candles before lb.
func smaWindow(cs []domain.Candle, period, lb, hi int) []float64 {
	out := make([]float64, max(hi-lb, 0))
	for k := range out {
		out[k] = math.NaN()
	}
	if period < 1 {
		return out
	}
	sum, n := 0.0, 0
	for i := max(lb-period+1, 0); i < hi; i++ {
		v := cs[i].C
		if math.IsNaN(v) {
			sum, n = 0, 0
			continue
		}
		sum += v
		n++
		if n > period {
			sum -= cs[i-period].C
			n = period
		}
		if n == period && i >= lb {
			out[i-lb] = sum / float64(period)
		}
	}
	return out
}

// emaState is EMAValues' loop state after some prefix of a series.
type emaState struct {
	prev, sum float64
	n         int
}

func newEMAState() emaState { return emaState{prev: math.NaN()} }

// step feeds one close and returns the EMA at it (EMAValues semantics:
// SMA-seeded, a NaN restarts the seed).
func (s *emaState) step(v float64, period int, alpha float64) float64 {
	if math.IsNaN(v) {
		*s = newEMAState()
		return math.NaN()
	}
	if math.IsNaN(s.prev) {
		s.sum += v
		s.n++
		if s.n == period {
			s.prev = s.sum / float64(period)
			return s.prev
		}
		return math.NaN()
	}
	s.prev = alpha*v + (1-alpha)*s.prev
	return s.prev
}

// emaWindow is EMA(cs, period)[lb:hi]. With a memo it resumes from the
// state remembered at an earlier lb instead of replaying the series.
func emaWindow(cs []domain.Candle, period, lb, hi int, memo *overlayMemo) []float64 {
	out := make([]float64, max(hi-lb, 0))
	if period < 1 {
		for k := range out {
			out[k] = math.NaN()
		}
		return out
	}
	alpha := 2 / float64(period+1)
	st, from := newEMAState(), 0
	var e *emaMemo
	if memo != nil {
		e = memo.ema(period)
		if e.valid(cs, lb) {
			st, from = e.st, e.k
		}
	}
	for i := from; i < lb; i++ {
		st.step(cs[i].C, period, alpha)
	}
	if e != nil {
		e.save(cs, lb, st)
	}
	for i := lb; i < hi; i++ {
		out[i-lb] = st.step(cs[i].C, period, alpha)
	}
	return out
}

// overlayMemo is the per-grid overlay state kept across frames when the
// caller reuses a grid (CandleOpts.Into). It is never shared between
// grids, so it needs no locking.
type overlayMemo struct {
	emas []emaMemo
}

// emaMemo is the EMA state after candles [0, k) of one series. The series
// is identified by its backing array and first open time, and the candle
// at k-1 must still match: a live chart only rewrites its newest bar and
// appends, so a matching prefix is the same prefix.
type emaMemo struct {
	period int
	base   *domain.Candle
	firstT int64
	k      int
	kT     int64
	kC     uint64 // close bits at k-1
	st     emaState
}

func (m *overlayMemo) ema(period int) *emaMemo {
	for i := range m.emas {
		if m.emas[i].period == period {
			return &m.emas[i]
		}
	}
	if len(m.emas) >= 6 {
		m.emas = m.emas[:0] // the overlay set changed wholesale
	}
	m.emas = append(m.emas, emaMemo{period: period})
	return &m.emas[len(m.emas)-1]
}

// valid reports whether the remembered state applies to cs up to lb.
func (e *emaMemo) valid(cs []domain.Candle, lb int) bool {
	if e.base == nil || len(cs) == 0 || e.base != &cs[0] || e.firstT != cs[0].T || e.k > lb || e.k == 0 {
		return false
	}
	c := cs[e.k-1]
	return c.T == e.kT && math.Float64bits(c.C) == e.kC
}

func (e *emaMemo) save(cs []domain.Candle, k int, st emaState) {
	if len(cs) == 0 || k == 0 {
		e.base = nil
		return
	}
	e.base, e.firstT, e.k, e.st = &cs[0], cs[0].T, k, st
	e.kT, e.kC = cs[k-1].T, math.Float64bits(cs[k-1].C)
}
