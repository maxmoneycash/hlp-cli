package pine

import (
	"math"
	"sort"
)

// registerTA adds the B3 built-ins of the pine-subset spec (section 6.3
// formulas, TradingView's reference implementations): ta.sar, ta.mfi,
// ta.percentrank, ta.median, ta.alma, ta.highestbars, ta.lowestbars,
// ta.kc, ta.dmi, ta.pivothigh and ta.pivotlow.
func registerTA(reg func(string, *builtin)) {
	reg("ta.sar", &builtin{params: p("start", "inc", "max"), required: 3, stateful: true, fn: taSAR})
	reg("ta.mfi", &builtin{params: p("series", "length"), required: 2, stateful: true, fn: func(rt *runtime, st *callState, a []value, _ *callExpr) value {
		type mfi struct {
			prev     float64
			up, down *ring
		}
		n := max(int(a[1].f), 1)
		s, ok := st.data.(*mfi)
		if !ok || s.up.n != n {
			s = &mfi{prev: math.NaN(), up: newRing(n), down: newRing(n)}
			st.data = s
		}
		x, vol := a[0].f, rt.cur.V
		ch := x - s.prev
		s.prev = x
		u, d := vol*x, vol*x // na change: Pine's ternary takes the else branch
		if !math.IsNaN(ch) {
			if ch <= 0 {
				u = 0
			}
			if ch >= 0 {
				d = 0
			}
		}
		s.up.push(u)
		s.down.push(d)
		if !s.up.full() {
			return naV
		}
		su, sd := 0.0, 0.0
		for i := 0; i < n; i++ {
			su += s.up.at(i)
			sd += s.down.at(i)
		}
		if sd == 0 {
			return num(100)
		}
		return num(100 - 100/(1+su/sd))
	}})
	reg("ta.percentrank", &builtin{params: p("source", "length"), required: 2, stateful: true, fn: func(_ *runtime, st *callState, a []value, _ *callExpr) value {
		n := max(int(a[1].f), 1)
		w := window(st, num(float64(n+1)))
		w.push(a[0].f)
		if !w.full() || math.IsNaN(w.at(0)) {
			return naV
		}
		c := 0
		for i := 1; i <= n; i++ {
			if w.at(i) <= w.at(0) {
				c++
			}
		}
		return num(100 * float64(c) / float64(n))
	}})
	reg("ta.median", &builtin{params: p("source", "length"), required: 2, stateful: true, fn: func(_ *runtime, st *callState, a []value, _ *callExpr) value {
		w := window(st, a[1])
		w.push(a[0].f)
		if !w.full() {
			return naV
		}
		xs := make([]float64, 0, w.n)
		for i := 0; i < w.n; i++ {
			if x := w.at(i); !math.IsNaN(x) {
				xs = append(xs, x)
			}
		}
		if len(xs) == 0 {
			return naV
		}
		sort.Float64s(xs)
		m := len(xs) / 2
		if len(xs)%2 == 1 {
			return num(xs[m])
		}
		return num((xs[m-1] + xs[m]) / 2)
	}})
	reg("ta.alma", &builtin{params: p("series", "length", "offset", "sigma", "floor"), required: 4, stateful: true, fn: func(_ *runtime, st *callState, a []value, _ *callExpr) value {
		w := window(st, a[1])
		w.push(a[0].f)
		if !w.full() {
			return naV
		}
		n := float64(w.n)
		m := a[2].f * (n - 1)
		if !a[4].missing() && a[4].truthy() {
			m = math.Floor(m)
		}
		s := n / a[3].f
		sum, norm := 0.0, 0.0
		for i := 0; i < w.n; i++ {
			wt := math.Exp(-(float64(i) - m) * (float64(i) - m) / (2 * s * s))
			x := w.at(w.n - 1 - i)
			if math.IsNaN(x) {
				return naV
			}
			sum += x * wt
			norm += wt
		}
		return num(sum / norm)
	}})
	bars := func(hi bool) *builtin {
		return &builtin{params: p("source", "length"), required: 1, stateful: true, fn: func(rt *runtime, st *callState, a []value, _ *callExpr) value {
			src, ln := a[0], a[1]
			if ln.missing() { // ta.highestbars(length)
				ln = src
				if hi {
					src = num(rt.cur.H)
				} else {
					src = num(rt.cur.L)
				}
			}
			w := window(st, ln)
			w.push(src.f)
			if !w.full() {
				return naV
			}
			best, k := w.at(0), 0
			for i := 1; i < w.n; i++ {
				x := w.at(i)
				if math.IsNaN(best) || (hi && x > best) || (!hi && x < best) {
					best, k = x, i
				}
			}
			return num(float64(-k))
		}}
	}
	reg("ta.highestbars", bars(true))
	reg("ta.lowestbars", bars(false))
	reg("ta.kc", &builtin{params: p("series", "length", "mult", "useTrueRange"), required: 3, stateful: true, fn: func(rt *runtime, st *callState, a []value, _ *callExpr) value {
		type kc struct{ basis, rng *smoother }
		n := max(int(a[1].f), 1)
		s, ok := st.data.(*kc)
		if !ok || s.basis.n != n {
			alpha := 2 / float64(n+1)
			s = &kc{newSmoother(n, alpha), newSmoother(n, alpha)}
			st.data = s
		}
		r := rt.cur.H - rt.cur.L
		if a[3].missing() || a[3].truthy() {
			r = trueRange(rt, 0, true)
		}
		b := s.basis.update(a[0].f)
		re := s.rng.update(r)
		if math.IsNaN(b) || math.IsNaN(re) {
			return value{k: kTuple, t: []value{naV, naV, naV}}
		}
		return value{k: kTuple, t: []value{num(b), num(b + re*a[2].f), num(b - re*a[2].f)}}
	}})
	reg("ta.dmi", &builtin{params: p("diLength", "adxSmoothing"), required: 2, stateful: true, fn: taDMI})
	pivot := func(hi bool) *builtin {
		return &builtin{params: p("source", "leftbars", "rightbars"), required: 2, stateful: true, fn: func(rt *runtime, st *callState, a []value, _ *callExpr) value {
			src, left, right := a[0], a[1], a[2]
			if right.missing() { // ta.pivothigh(leftbars, rightbars)
				src, left, right = num(rt.cur.H), a[0], a[1]
				if !hi {
					src = num(rt.cur.L)
				}
			}
			l, r := max(int(left.f), 0), max(int(right.f), 0)
			w := window(st, num(float64(l+r+1)))
			w.push(src.f)
			if !w.full() {
				return naV
			}
			c := w.at(r)
			if math.IsNaN(c) {
				return naV
			}
			for i := 0; i < w.n; i++ {
				if i == r {
					continue
				}
				x := w.at(i)
				if math.IsNaN(x) || (hi && x >= c && (i > r || x > c)) || (!hi && x <= c && (i > r || x < c)) {
					// left side must be strictly lower (higher); right
					// side may not exceed the pivot either (TradingView
					// keeps the leftmost of equal highs)
					return naV
				}
			}
			return num(c)
		}}
	}
	reg("ta.pivothigh", pivot(true))
	reg("ta.pivotlow", pivot(false))
	for _, n := range []string{"ta.pivothigh", "ta.pivotlow", "ta.dmi"} {
		delete(unsupportedNS, n)
	}
}

type sarState struct {
	n                    int
	result, maxMin, acc  float64
	below                bool
	h1, l1, h2, l2, prev float64
}

// taSAR is TradingView's reference ta.sar.
func taSAR(rt *runtime, st *callState, a []value, _ *callExpr) value {
	s, _ := st.data.(*sarState)
	if s == nil {
		s = &sarState{result: math.NaN()}
		st.data = s
	}
	start, inc, mx := a[0].f, a[1].f, a[2].f
	c := rt.cur
	defer func() {
		s.h2, s.l2 = s.h1, s.l1
		s.h1, s.l1, s.prev = c.H, c.L, c.C
		s.n++
	}()
	if s.n == 0 {
		return naV
	}
	first := false
	if s.n == 1 {
		if c.C > s.prev {
			s.below, s.maxMin, s.result = true, c.H, s.l1
		} else {
			s.below, s.maxMin, s.result = false, c.L, s.h1
		}
		first = true
		s.acc = start
	}
	s.result += s.acc * (s.maxMin - s.result)
	if s.below {
		if s.result > c.L {
			first, s.below = true, false
			s.result = math.Max(c.H, s.maxMin)
			s.maxMin, s.acc = c.L, start
		}
	} else if s.result < c.H {
		first, s.below = true, true
		s.result = math.Min(c.L, s.maxMin)
		s.maxMin, s.acc = c.H, start
	}
	if !first {
		if s.below && c.H > s.maxMin {
			s.maxMin, s.acc = c.H, math.Min(s.acc+inc, mx)
		} else if !s.below && c.L < s.maxMin {
			s.maxMin, s.acc = c.L, math.Min(s.acc+inc, mx)
		}
	}
	if s.below {
		s.result = math.Min(s.result, s.l1)
		if s.n > 1 {
			s.result = math.Min(s.result, s.l2)
		}
	} else {
		s.result = math.Max(s.result, s.h1)
		if s.n > 1 {
			s.result = math.Max(s.result, s.h2)
		}
	}
	return num(s.result)
}

type dmiState struct {
	n, sig               int
	prevH, prevL, prevC  float64
	tr, plus, minus, adx *smoother
	lastPlus, lastMinus  float64
	started              bool
}

// taDMI is TradingView's reference ta.dmi: [+DI, -DI, ADX].
func taDMI(rt *runtime, st *callState, a []value, _ *callExpr) value {
	n, sig := max(int(a[0].f), 1), max(int(a[1].f), 1)
	s, ok := st.data.(*dmiState)
	if !ok || s.n != n || s.sig != sig {
		r := func(k int) *smoother { return newSmoother(k, 1/float64(k)) }
		s = &dmiState{n: n, sig: sig, tr: r(n), plus: r(n), minus: r(n), adx: r(sig),
			lastPlus: math.NaN(), lastMinus: math.NaN()}
		st.data = s
	}
	c := rt.cur
	na3 := value{k: kTuple, t: []value{naV, naV, naV}}
	if !s.started {
		s.started = true
		s.prevH, s.prevL, s.prevC = c.H, c.L, c.C
		return na3
	}
	up, down := c.H-s.prevH, -(c.L - s.prevL)
	tr := math.Max(c.H-c.L, math.Max(math.Abs(c.H-s.prevC), math.Abs(c.L-s.prevC)))
	s.prevH, s.prevL, s.prevC = c.H, c.L, c.C
	pdm, mdm := 0.0, 0.0
	if up > down && up > 0 {
		pdm = up
	}
	if down > up && down > 0 {
		mdm = down
	}
	trur := s.tr.update(tr)
	pr, mr := s.plus.update(pdm), s.minus.update(mdm)
	plus, minus := 100*pr/trur, 100*mr/trur
	if math.IsNaN(plus) || math.IsInf(plus, 0) {
		plus = s.lastPlus // fixnan
	}
	if math.IsNaN(minus) || math.IsInf(minus, 0) {
		minus = s.lastMinus
	}
	s.lastPlus, s.lastMinus = plus, minus
	if math.IsNaN(plus) || math.IsNaN(minus) {
		return na3
	}
	sum := plus + minus
	if sum == 0 {
		sum = 1
	}
	adx := 100 * s.adx.update(math.Abs(plus-minus)/sum)
	return value{k: kTuple, t: []value{num(plus), num(minus), num(adx)}}
}
