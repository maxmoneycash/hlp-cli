package pine

import (
	"math"
)

// registerExtras adds the built-ins that came with the 10-sample import
// work: request.security (security.go), timestamp, ta.linreg and ta.hma.
func registerExtras(reg func(string, *builtin)) {
	reg("request.security", &builtin{params: p("symbol", "timeframe", "expression", "gaps", "lookahead",
		"ignore_invalid_symbol", "currency", "calc_bars_count"), required: 3,
		fn: func(rt *runtime, _ *callState, _ []value, c *callExpr) value {
			rt.fail(c.P, "request.security was not resolved by the checker")
			return naV
		}})
	reg("timestamp", &builtin{variadic: true, required: 1, fn: func(rt *runtime, _ *callState, a []value, c *callExpr) value {
		ms, err := timestampOf(a)
		if err != nil {
			rt.fail(c.P, "%v", err)
		}
		return num(ms)
	}})
	reg("ta.linreg", &builtin{params: p("source", "length", "offset"), required: 3, stateful: true,
		fn: func(_ *runtime, st *callState, a []value, _ *callExpr) value {
			w := window(st, a[1])
			w.push(a[0].f)
			if !w.full() {
				return naV
			}
			return num(linreg(w, a[2].f))
		}})
	reg("ta.hma", &builtin{params: p("source", "length"), required: 2, stateful: true,
		fn: func(_ *runtime, st *callState, a []value, _ *callExpr) value {
			n := int(a[1].f)
			if n < 2 || math.IsNaN(a[1].f) {
				return naV
			}
			hs, ok := st.data.(*hmaState)
			if !ok || hs.n != n {
				hs = &hmaState{n: n, half: newRing(n / 2), full: newRing(n), out: newRing(int(math.Floor(math.Sqrt(float64(n)))))}
				st.data = hs
			}
			hs.half.push(a[0].f)
			hs.full.push(a[0].f)
			if !hs.full.full() {
				return naV
			}
			hs.out.push(2*wmaRing(hs.half) - wmaRing(hs.full))
			if !hs.out.full() {
				return naV
			}
			return num(wmaRing(hs.out))
		}})
}

type hmaState struct {
	n               int
	half, full, out *ring
}

// wmaRing is ta.wma over a full ring (na when any value is na).
func wmaRing(w *ring) float64 {
	s, d := 0.0, 0.0
	for i := 0; i < w.n; i++ {
		x := w.at(i)
		if math.IsNaN(x) {
			return math.NaN()
		}
		wt := float64(w.n - i)
		s += x * wt
		d += wt
	}
	return s / d
}

// linreg is TradingView's ta.linreg: the least-squares line through the
// window (x = 0 oldest .. n-1 current), evaluated at x = n-1-offset.
func linreg(w *ring, offset float64) float64 {
	n := w.n
	var sx, sy, sxx, sxy float64
	for j := 0; j < n; j++ {
		y := w.at(n - 1 - j)
		if math.IsNaN(y) {
			return math.NaN()
		}
		x := float64(j)
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	fn := float64(n)
	den := fn*sxx - sx*sx
	slope := 0.0
	if den != 0 {
		slope = (fn*sxy - sx*sy) / den
	}
	intercept := sy/fn - slope*sx/fn
	return intercept + slope*(fn-1-offset)
}
