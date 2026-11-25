package pine

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// request.security, same coin only.
//
// Semantics follow the owner's setup-rush runtime (bar-close only, never
// the forming bar) and TradingView's non-repainting rules:
//
//   - The chart's own timeframe ("" or timeframe.period, or a timeframe
//     equal to the run interval) makes the call its expression, evaluated
//     on the chart's bars (setup-rush compat.ts chartSecurity).
//   - A higher timeframe aggregates the chart's closed bars into HTF bars
//     (UTC buckets; weeks start Monday, months are calendar months) and
//     evaluates the expression once per COMPLETED HTF bar, in its own
//     context (own bar_index, own ta.* state, OHLCV of the HTF bar).
//   - lookahead_off (default): every chart bar sees the value of the latest
//     completed HTF bar, which includes the HTF bar that completes on this
//     chart bar. This equals TradingView's historical output.
//   - lookahead_on is only accepted with a history offset of at least one
//     (expr[1], the documented non-repainting idiom). Chart bars in HTF
//     period P then see expr at period P-k, from the first bar of P.
//   - A first HTF bucket that the data only partly covers is skipped, so
//     no HTF bar is built from incomplete data.
//
// Lower timeframes, other symbols, tuples under lookahead_on and variables
// that change from bar to bar are errors with line:col.

// secInfo is the checker's resolution of one request.security call.
type secInfo struct {
	expr      expr // evaluated in the HTF context (lookahead_off)
	inner     expr // lookahead_on: the expression under the [k]
	offset    int  // lookahead_on: k >= 1
	lookahead bool
	gaps      bool
	slots     []secSlot
}

type secSlot struct {
	slot int
	name string
	p    Pos
}

// security validates a request.security call (checker side).
func (c *checker) security(e *callExpr) {
	if c.inFunc != nil {
		c.errf(e.P, "request.security inside a function is not supported yet; call it at the top level and pass the result in")
		return
	}
	at := func(n string) expr { return e.bound[indexOf(builtins[e.Fn].params, n)] }
	sym := at("symbol")
	if id, ok := sym.(*ident); !ok || (id.Name != "syminfo.tickerid" && id.Name != "syminfo.ticker") {
		c.errf(e.P, "request.security on another symbol is not supported yet; only syminfo.tickerid (the coin being run) works")
		return
	}
	if tf, ok := constStr(at("timeframe")); ok {
		if _, err := parseTF(tf); err != nil {
			c.errf(at("timeframe").position(), "%v", err)
			return
		}
	}
	if x := at("currency"); x != nil {
		c.errf(x.position(), "request.security currency conversion is not supported (Hyperliquid prices are USD)")
	}
	si := &secInfo{expr: at("expression")}
	if x := at("lookahead"); x != nil {
		v, ok := constStr(x)
		if !ok || (v != "barmerge.lookahead_on" && v != "barmerge.lookahead_off") {
			c.errf(x.position(), "lookahead must be barmerge.lookahead_on or barmerge.lookahead_off")
			return
		}
		si.lookahead = v == "barmerge.lookahead_on"
	}
	if x := at("gaps"); x != nil {
		v, ok := constStr(x)
		if !ok || (v != "barmerge.gaps_on" && v != "barmerge.gaps_off") {
			c.errf(x.position(), "gaps must be barmerge.gaps_on or barmerge.gaps_off")
			return
		}
		si.gaps = v == "barmerge.gaps_on"
	}
	if si.lookahead {
		ix, ok := si.expr.(*indexExpr)
		k, kok := 0.0, false
		if ok {
			k, kok = constNum(ix.N)
		}
		if !ok || !kok || k < 1 {
			c.errf(e.P, "request.security with lookahead = barmerge.lookahead_on reads the future unless the expression ends in a history offset; hlp runs the non-repainting form request.security(..., expr[1], lookahead = barmerge.lookahead_on) or lookahead_off")
			return
		}
		si.inner, si.offset = ix.X, int(k)
		if _, isTuple := ix.X.(*tupleExpr); isTuple {
			c.errf(e.P, "tuples are not supported with lookahead_on")
			return
		}
	}
	ok := true
	var walk func(x expr)
	walk = func(x expr) {
		switch x := x.(type) {
		case *ident:
			switch x.ref.kind {
			case refSlot:
				if !c.simple[x.ref.slot] {
					c.errf(x.P, "request.security cannot read %s: it changes from bar to bar on the chart's timeframe. Write the calculation inside the request.security expression instead", x.Name)
					ok = false
					return
				}
				si.slots = append(si.slots, secSlot{slot: x.ref.slot, name: x.Name, p: x.P})
			case refBuiltin:
				if strings.HasPrefix(x.Name, "strategy.") {
					c.errf(x.P, "%s cannot be read inside request.security", x.Name)
					ok = false
				}
			}
		case *indexExpr:
			walk(x.X)
			walk(x.N)
		case *unaryExpr:
			walk(x.X)
		case *binaryExpr:
			walk(x.X)
			walk(x.Y)
		case *ternaryExpr:
			walk(x.Cond)
			walk(x.A)
			walk(x.B)
		case *tupleExpr:
			for _, el := range x.Elems {
				walk(el)
			}
		case *callExpr:
			if x.fn != nil {
				for _, a := range x.bound {
					walk(a)
				}
				return
			}
			for _, a := range x.Args {
				walk(a)
			}
			for _, n := range x.Named {
				walk(n.X)
			}
		case *stmtExpr:
			c.errf(x.P, "if/switch expressions inside request.security are not supported yet; use ?: or a function")
			ok = false
		}
	}
	walk(si.expr)
	if !ok {
		return
	}
	e.sec = si
	c.secs = append(c.secs, e)
}

// secState is one request.security call site's runtime state.
type secState struct {
	tfKey    string
	same     bool // chart timeframe: the call is its expression
	tf       tfSpec
	child    *runtime
	frame    *frame
	bucket   int64
	started  bool
	partial  bool
	agg      domain.Candle
	vals     []value // expression (or inner) at each completed HTF bar
	newAt    int     // chart bar on which the latest HTF value arrived
	newStart int     // chart bar on which the current HTF period started
}

const secKeep = 64 // completed HTF values kept per call site

func (rt *runtime) security(f *frame, c *callExpr) value {
	si := c.sec
	tfv := rt.eval(f, c.bound[1])
	tfStr := ""
	if tfv.k == kStr {
		tfStr = tfv.s
	} else if !tfv.isNa() {
		rt.fail(c.bound[1].position(), "request.security timeframe must be a string such as \"D\" or \"240\"")
	}
	st := f.state[c.id]
	if st == nil {
		st = &callState{}
		f.state[c.id] = st
	}
	ss, _ := st.data.(*secState)
	if ss == nil || ss.tfKey != tfStr {
		ss = &secState{tfKey: tfStr, newAt: -1, newStart: -1}
		tf, err := parseTF(tfStr)
		if err != nil {
			rt.fail(c.bound[1].position(), "%v", err)
		}
		chart := rt.iv.Duration()
		switch {
		case tf.chart:
			ss.same = true
		case tf.approx() < chart:
			rt.fail(c.bound[1].position(), "request.security timeframe %q is lower than the run interval %s; lower timeframes are not supported", tfStr, rt.iv)
		case tf.approx() == chart && tf.unit != 'M':
			ss.same = true
		default:
			ss.tf = tf
			ss.child = newRuntime(rt.prog, rt.coin, tf.interval(rt.iv), rt.inputs, rt.srcInput)
			ss.frame = newFrame(&rt.prog.layout)
			ss.child.root = ss.frame
		}
		st.data = ss
	}
	if ss.same {
		return rt.eval(f, si.expr)
	}
	cur := rt.cur
	b := ss.tf.start(cur.T)
	if !ss.started || b != ss.bucket {
		if !ss.started && cur.T != b {
			ss.partial = true
		} else if ss.started {
			ss.partial = false
		}
		ss.started = true
		ss.bucket = b
		ss.agg = domain.Candle{T: b, O: cur.O, H: cur.H, L: cur.L, C: cur.C, V: cur.V, N: cur.N}
		ss.newStart = rt.bar
	} else {
		ss.agg.H = math.Max(ss.agg.H, cur.H)
		ss.agg.L = math.Min(ss.agg.L, cur.L)
		ss.agg.C = cur.C
		ss.agg.V += cur.V
		ss.agg.N += cur.N
	}
	complete := cur.T+rt.iv.Duration().Milliseconds() >= ss.tf.end(b)
	if complete && !ss.partial {
		ch := ss.child
		ch.pushCandle(ss.agg)
		for _, sl := range si.slots {
			ss.frame.slots[sl.slot].set(ch.bar, rt.root.slots[sl.slot].get(rt.bar))
		}
		x := si.expr
		if si.lookahead {
			x = si.inner
		}
		v := ch.eval(ss.frame, x)
		ss.vals = append(ss.vals, v)
		if len(ss.vals) > secKeep+si.offset {
			ss.vals = ss.vals[len(ss.vals)-secKeep-si.offset:]
		}
		ss.newAt = rt.bar
	}
	if complete && ss.partial {
		ss.partial = false
		ss.started = false // the next bucket starts clean
	}
	n := len(ss.vals)
	if !si.lookahead {
		if n == 0 || (si.gaps && ss.newAt != rt.bar) {
			return naV
		}
		return ss.vals[n-1]
	}
	// lookahead_on with expr[k]: period P sees inner at P-k. When this chart
	// bar completed P, P is vals[n-1]; otherwise P is still forming.
	pIdx := n
	if complete && ss.newAt == rt.bar {
		pIdx = n - 1
	}
	i := pIdx - si.offset
	if i < 0 || i >= n || (si.gaps && ss.newStart != rt.bar) {
		return naV
	}
	return ss.vals[i]
}

// tfSpec is a TradingView timeframe string.
type tfSpec struct {
	chart bool // "" = the chart's timeframe
	unit  byte // 'm' minutes, 'D' days, 'W' weeks, 'M' months
	n     int
}

// parseTF parses TradingView timeframe strings: "" (chart), "1".."1440"
// (minutes), "60", "240", "D", "1D", "3D", "W", "1W", "M", "1M", "3M",
// "12M". Seconds are rejected (hlp's smallest bar is 1 minute).
func parseTF(s string) (tfSpec, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return tfSpec{chart: true}, nil
	}
	bad := fmt.Errorf("timeframe %q is not a TradingView timeframe (use \"60\", \"240\", \"D\", \"W\" or \"M\")", s)
	u := strings.ToUpper(t)
	unit := byte('m')
	switch last := u[len(u)-1]; last {
	case 'D', 'W', 'M':
		unit = last
		u = u[:len(u)-1]
	case 'H':
		unit = 'h'
		u = u[:len(u)-1]
	case 'S':
		return tfSpec{}, fmt.Errorf("timeframe %q is in seconds; hlp's smallest bar is 1 minute", s)
	}
	n := 1
	if u != "" {
		v, err := strconv.Atoi(u)
		if err != nil || v < 1 {
			return tfSpec{}, bad
		}
		n = v
	} else if unit == 'm' {
		return tfSpec{}, bad
	}
	if unit == 'h' {
		unit, n = 'm', n*60
	}
	if unit == 'm' && n%1440 == 0 {
		unit, n = 'D', n/1440
	}
	return tfSpec{unit: unit, n: n}, nil
}

const dayMs = 86_400_000

// approx is the timeframe's nominal length.
func (t tfSpec) approx() time.Duration {
	switch t.unit {
	case 'm':
		return time.Duration(t.n) * time.Minute
	case 'D':
		return time.Duration(t.n) * 24 * time.Hour
	case 'W':
		return time.Duration(t.n) * 7 * 24 * time.Hour
	case 'M':
		return time.Duration(t.n) * 30 * 24 * time.Hour
	}
	return 0
}

// interval is the closest domain interval (timeframe.* inside the HTF
// context); fallback is used when none matches.
func (t tfSpec) interval(fallback domain.Interval) domain.Interval {
	for _, iv := range domain.Intervals {
		if iv.Duration() == t.approx() && (t.unit == 'M') == (iv == domain.I1M) {
			return iv
		}
	}
	return fallback
}

// mondayMs is 1970-01-05 00:00 UTC, the first Monday after the epoch.
const mondayMs = 4 * dayMs

// start returns the open time of the bucket containing ms.
func (t tfSpec) start(ms int64) int64 {
	switch t.unit {
	case 'm':
		d := int64(t.n) * 60_000
		return floorDiv(ms, d) * d
	case 'D':
		d := int64(t.n) * dayMs
		return floorDiv(ms, d) * d
	case 'W':
		d := int64(t.n) * 7 * dayMs
		return floorDiv(ms-mondayMs, d)*d + mondayMs
	case 'M':
		tm := time.UnixMilli(ms).UTC()
		m := (tm.Year()*12 + int(tm.Month()) - 1)
		m -= m % t.n
		return time.Date(m/12, time.Month(m%12+1), 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	}
	return ms
}

// end returns the open time of the next bucket after the one starting at b.
func (t tfSpec) end(b int64) int64 {
	switch t.unit {
	case 'm':
		return b + int64(t.n)*60_000
	case 'D':
		return b + int64(t.n)*dayMs
	case 'W':
		return b + int64(t.n)*7*dayMs
	case 'M':
		return time.UnixMilli(b).UTC().AddDate(0, t.n, 0).UnixMilli()
	}
	return b
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// ---- timestamp() ----

var dateLayouts = []string{
	"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02",
	"02 Jan 2006 15:04:05", "02 Jan 2006 15:04", "02 Jan 2006", "Jan 02 2006 15:04:05", "Jan 02 2006 15:04", "Jan 02 2006",
	"2 Jan 2006 15:04:05", "2 Jan 2006 15:04", "2 Jan 2006",
}

// parseTZ accepts "UTC", "GMT", "GMT+N", "UTC-N", "GMT+05:30", "Etc/UTC" and
// IANA names (when the system has tzdata).
func parseTZ(s string) (*time.Location, error) {
	t := strings.TrimSpace(s)
	switch strings.ToUpper(t) {
	case "", "UTC", "GMT", "ETC/UTC", "Z":
		return time.UTC, nil
	}
	u := strings.ToUpper(t)
	for _, p := range []string{"UTC", "GMT"} {
		if strings.HasPrefix(u, p) && len(u) > len(p) && (u[len(p)] == '+' || u[len(p)] == '-') {
			sign := 1
			if u[len(p)] == '-' {
				sign = -1
			}
			rest := u[len(p)+1:]
			h, m := rest, "0"
			if i := strings.Index(rest, ":"); i >= 0 {
				h, m = rest[:i], rest[i+1:]
			}
			hv, err1 := strconv.Atoi(h)
			mv, err2 := strconv.Atoi(m)
			if err1 != nil || err2 != nil || hv > 14 || mv > 59 {
				break
			}
			return time.FixedZone(t, sign*(hv*3600+mv*60)), nil
		}
	}
	if loc, err := time.LoadLocation(t); err == nil {
		return loc, nil
	}
	return nil, fmt.Errorf("unknown timezone %q (use \"UTC\" or \"GMT+N\")", s)
}

// parseDate parses timestamp()'s date-string form, with an optional
// trailing zone ("+0000", "GMT+2", "UTC").
func parseDate(s string, loc *time.Location) (int64, error) {
	t := strings.TrimSpace(s)
	if tm, err := time.Parse(time.RFC3339, t); err == nil {
		return tm.UnixMilli(), nil
	}
	if i := strings.LastIndex(t, " "); i > 0 {
		if z, ok := zoneToken(t[i+1:]); ok {
			loc, t = z, strings.TrimSpace(t[:i])
		}
	}
	for _, l := range dateLayouts {
		if tm, err := time.ParseInLocation(l, t, loc); err == nil {
			return tm.UnixMilli(), nil
		}
	}
	return 0, fmt.Errorf("timestamp: cannot read date %q (use \"2024-01-01T00:00:00\")", s)
}

// zoneToken reads a trailing zone: "+0530", "-0000", "UTC", "GMT", "GMT+2".
func zoneToken(z string) (*time.Location, bool) {
	if len(z) == 5 && (z[0] == '+' || z[0] == '-') {
		h, e1 := strconv.Atoi(z[1:3])
		m, e2 := strconv.Atoi(z[3:])
		if e1 != nil || e2 != nil {
			return nil, false
		}
		sign := 1
		if z[0] == '-' {
			sign = -1
		}
		return time.FixedZone(z, sign*(h*3600+m*60)), true
	}
	u := strings.ToUpper(z)
	if strings.HasPrefix(u, "UTC") || strings.HasPrefix(u, "GMT") {
		if loc, err := parseTZ(z); err == nil {
			return loc, true
		}
	}
	return nil, false
}

// timestampOf implements timestamp(...) for constant or runtime args:
// (dateString), (tz, dateString), (y, mo, d[, h, mi, s]) and
// (tz, y, mo, d[, h, mi, s]). The default zone is UTC (Hyperliquid's
// exchange time).
func timestampOf(args []value) (float64, error) {
	loc := time.UTC
	if len(args) >= 2 && args[0].k == kStr {
		l, err := parseTZ(args[0].s)
		if err != nil {
			return 0, err
		}
		loc = l
		args = args[1:]
	}
	if len(args) == 1 && args[0].k == kStr {
		ms, err := parseDate(args[0].s, loc)
		return float64(ms), err
	}
	if len(args) < 3 || len(args) > 6 {
		return 0, fmt.Errorf("timestamp takes (year, month, day[, hour, minute, second]) or a date string")
	}
	var f [6]int
	for i, a := range args {
		if a.isNa() {
			return math.NaN(), nil
		}
		if a.k != kNum {
			return 0, fmt.Errorf("timestamp: argument %d must be a number", i+1)
		}
		f[i] = int(a.f)
	}
	return float64(time.Date(f[0], time.Month(f[1]), f[2], f[3], f[4], f[5], 0, loc).UnixMilli()), nil
}

// parseTimeInput reads an input.time override: epoch ms (number or digits)
// or a date string.
func parseTimeInput(raw any) (float64, bool) {
	switch v := raw.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, !math.IsNaN(v) && !math.IsInf(v, 0)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f, true
		}
		ms, err := parseDate(v, time.UTC)
		return float64(ms), err == nil
	}
	return 0, false
}
