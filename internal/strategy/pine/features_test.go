package pine_test

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/backtest"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/internal/testkit"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/pine"
)

func compileIv(t *testing.T, src string, iv domain.Interval, inputs map[string]any) *pine.Script {
	t.Helper()
	s, err := pine.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := pine.Compile(s, "BTC", iv, inputs); err != nil {
		t.Fatalf("compile: %v", err)
	}
	return s
}

func runIv(t *testing.T, src string, iv domain.Interval, cs []domain.Candle, inputs map[string]any, o backtest.Options) *backtest.Result {
	t.Helper()
	s := compileIv(t, src, iv, inputs)
	st, _ := pine.Compile(s, "BTC", iv, inputs)
	return run(t, st, cs, o)
}

func TestSwitchIfExpressionsAndDefaults(t *testing.T) {
	cs := candles(t)
	src := `//@version=6
indicator("switch")
kind = input.string("B", "Kind", options = ["A", "B", "C"])
pick(k, a, b = 10, c = 20) =>
    switch k
        "A" => a
        "B" =>
            x = b * 2
            x + 1
        => c
up = switch
    close > open => 1
    close < open => -1
    => 0
v = if bar_index % 2 == 0
    100
else
    200
w = if bar_index < 0
    1
plot(pick(kind, 1), "pick")
plot(pick("A", 7), "pickA")
plot(pick("Z", 7, c = 3), "pickZ")
plot(pick(k = "B", a = 0, b = 1), "named")
plot(up, "up")
plot(v, "v")
plot(nz(w, -5), "w")
`
	res := run(t, compile(t, src, nil), cs, backtest.Options{})
	for i := range cs {
		g := 0.0
		if cs[i].C > cs[i].O {
			g = 1
		} else if cs[i].C < cs[i].O {
			g = -1
		}
		v := 200.0
		if i%2 == 0 {
			v = 100
		}
		got := []float64{res.Plots["pick"][i], res.Plots["pickA"][i], res.Plots["pickZ"][i], res.Plots["named"][i], res.Plots["up"][i], res.Plots["v"][i], res.Plots["w"][i]}
		want := []float64{21, 7, 3, 3, g, v, -5}
		for k := range want {
			if got[k] != want[k] {
				t.Fatalf("bar %d: plots %v want %v", i, got, want)
			}
		}
	}
	// The input chooses the arm.
	res = run(t, compile(t, src, map[string]any{"Kind": "C"}), cs, backtest.Options{})
	if res.Plots["pick"][5] != 20 {
		t.Fatalf("Kind=C pick %v", res.Plots["pick"][5])
	}
}

func TestSwitchKeepsTaStatePerArm(t *testing.T) {
	cs := candles(t)
	// Sample 05's helper: every call site keeps its own EMA, and only the
	// taken arm updates (as in TradingView).
	src := `//@version=6
indicator("ribbon")
ma(src, len, kind) =>
    switch kind
        "SMA" => ta.sma(src, len)
        => ta.ema(src, len)
plot(ma(close, 5, "EMA"), "e5")
plot(ma(close, 9, "EMA"), "e9")
plot(ma(close, 5, "SMA"), "s5")
plot(ta.ema(close, 5), "ref5")
plot(ta.ema(close, 9), "ref9")
plot(ta.sma(close, 5), "refs5")
`
	res := run(t, compile(t, src, nil), cs, backtest.Options{})
	sameSeries(t, "e5", res.Plots["e5"], res.Plots["ref5"], 1e-9)
	sameSeries(t, "e9", res.Plots["e9"], res.Plots["ref9"], 1e-9)
	sameSeries(t, "s5", res.Plots["s5"], res.Plots["refs5"], 1e-9)
}

func refLinreg(xs []float64, n int, off float64) []float64 {
	out := make([]float64, len(xs))
	for i := range xs {
		if i < n-1 {
			out[i] = math.NaN()
			continue
		}
		var sx, sy, sxx, sxy float64
		for j := 0; j < n; j++ {
			x, y := float64(j), xs[i-n+1+j]
			sx += x
			sy += y
			sxx += x * x
			sxy += x * y
		}
		fn := float64(n)
		slope := (fn*sxy - sx*sy) / (fn*sxx - sx*sx)
		icpt := (sy - slope*sx) / fn
		out[i] = icpt + slope*(fn-1-off)
	}
	return out
}

func refWMA(xs []float64, n int) []float64 {
	out := make([]float64, len(xs))
	for i := range xs {
		if i < n-1 || math.IsNaN(xs[i-n+1]) {
			out[i] = math.NaN()
			continue
		}
		s, d := 0.0, 0.0
		for k := 0; k < n; k++ {
			w := float64(n - k)
			s += xs[i-k] * w
			d += w
		}
		out[i] = s / d
	}
	return out
}

func TestLinregAndHMA(t *testing.T) {
	cs := candles(t)
	src := `//@version=5
indicator("lr")
plot(ta.linreg(close, 20, 0), "lr0")
plot(ta.linreg(close, 20, 3), "lr3")
plot(ta.hma(close, 16), "hma")
`
	res := run(t, compile(t, src, nil), cs, backtest.Options{})
	xs := closes(cs)
	sameSeries(t, "linreg 0", res.Plots["lr0"], refLinreg(xs, 20, 0), 1e-6)
	sameSeries(t, "linreg 3", res.Plots["lr3"], refLinreg(xs, 20, 3), 1e-6)
	a, b := refWMA(xs, 8), refWMA(xs, 16)
	raw := make([]float64, len(xs))
	for i := range xs {
		raw[i] = 2*a[i] - b[i]
	}
	sameSeries(t, "hma", res.Plots["hma"], refWMA(raw, 4), 1e-6)
}

// dailyBars aggregates UTC days (cs must start at 00:00 UTC).
func dailyBars(cs []domain.Candle) (days []domain.Candle, dayOf []int) {
	dayOf = make([]int, len(cs))
	for i, c := range cs {
		d := c.T / 86_400_000 * 86_400_000
		if len(days) == 0 || days[len(days)-1].T != d {
			days = append(days, domain.Candle{T: d, O: c.O, H: c.H, L: c.L, C: c.C})
		} else {
			x := &days[len(days)-1]
			x.H, x.L, x.C = math.Max(x.H, c.H), math.Min(x.L, c.L), c.C
		}
		dayOf[i] = len(days) - 1
	}
	return days, dayOf
}

func TestRequestSecurityHigherTimeframe(t *testing.T) {
	cs := synth1h(24 * 40)
	src := `//@version=5
indicator("htf")
tf  = input.timeframe("D", "TF")
len = input.int(20, "Len")
plot(request.security(syminfo.tickerid, tf, ta.ema(close, len)[1], lookahead = barmerge.lookahead_on), "emaOn")
plot(request.security(syminfo.tickerid, tf, close), "closeOff")
plot(request.security(syminfo.tickerid, tf, bar_index), "htfIndex")
plot(request.security(syminfo.tickerid, "", close), "same")
plot(request.security(syminfo.tickerid, "60", close), "sameIv")
`
	res := runIv(t, src, domain.I1h, cs, nil, backtest.Options{})
	days, dayOf := dailyBars(cs)
	dc := make([]float64, len(days))
	for i, d := range days {
		dc[i] = d.C
	}
	dema := ema(dc, 20)
	for i := range cs {
		d := dayOf[i]
		last := i == len(cs)-1 || dayOf[i+1] != d
		// lookahead_on + [1]: the previous day's EMA from the first hour.
		want := math.NaN()
		if d >= 1 {
			want = dema[d-1]
		}
		if got := res.Plots["emaOn"][i]; !(math.IsNaN(want) && math.IsNaN(got)) && math.Abs(got-want) > 1e-6 {
			t.Fatalf("bar %d (day %d): emaOn %v want %v", i, d, got, want)
		}
		// lookahead_off: the latest COMPLETED day, which is today on its last hour.
		wantC, wantIdx := math.NaN(), math.NaN()
		if last {
			wantC, wantIdx = dc[d], float64(d)
		} else if d >= 1 {
			wantC, wantIdx = dc[d-1], float64(d-1)
		}
		if got := res.Plots["closeOff"][i]; !(math.IsNaN(wantC) && math.IsNaN(got)) && got != wantC {
			t.Fatalf("bar %d: closeOff %v want %v", i, got, wantC)
		}
		if got := res.Plots["htfIndex"][i]; !(math.IsNaN(wantIdx) && math.IsNaN(got)) && got != wantIdx {
			t.Fatalf("bar %d: HTF bar_index %v want %v", i, got, wantIdx)
		}
		if res.Plots["same"][i] != cs[i].C || res.Plots["sameIv"][i] != cs[i].C {
			t.Fatalf("bar %d: chart-timeframe security must be the expression itself", i)
		}
	}
	// The timeframe input can be overridden; a 4h filter differs from D.
	res4 := runIv(t, src, domain.I1h, cs, map[string]any{"TF": "240"}, backtest.Options{})
	if res4.Plots["closeOff"][7] != cs[7].C || res4.Plots["closeOff"][8] != cs[7].C {
		t.Fatalf("240: closeOff %v %v want %v", res4.Plots["closeOff"][7], res4.Plots["closeOff"][8], cs[7].C)
	}
	// A partial first bucket is skipped: start the data at 05:00. Day 0 is
	// partial (k = 0..18), so the first value is day 1's close on its last
	// hour (k = 42, cs[47]).
	resP := runIv(t, src, domain.I1h, cs[5:], nil, backtest.Options{})
	p := resP.Plots["closeOff"]
	if !math.IsNaN(p[18]) || !math.IsNaN(p[41]) || p[42] != cs[47].C || resP.Plots["htfIndex"][42] != 0 {
		t.Fatalf("partial first day: %v %v %v (want %v) idx %v", p[18], p[41], p[42], cs[47].C, resP.Plots["htfIndex"][42])
	}
}

func TestRequestSecurityErrors(t *testing.T) {
	cases := map[string]string{
		"other symbol":  `x = request.security("BINANCE:BTCUSDT", "D", close)`,
		"lookahead":     `x = request.security(syminfo.tickerid, "D", close, lookahead = barmerge.lookahead_on)`,
		"series var":    "m = close * 2\nx = request.security(syminfo.tickerid, \"D\", m)",
		"mutated var":   "n = 5\nn := 6\nx = request.security(syminfo.tickerid, \"D\", ta.sma(close, n))",
		"seconds":       `x = request.security(syminfo.tickerid, "30S", close)`,
		"bad tf":        `x = request.security(syminfo.tickerid, "fortnight", close)`,
		"in function":   "f() => request.security(syminfo.tickerid, \"D\", close)\nx = f()",
		"strategy var":  `x = request.security(syminfo.tickerid, "D", strategy.position_size)`,
		"lower_tf":      `x = request.security_lower_tf(syminfo.tickerid, "1", close)`,
		"trail no off":  `strategy.exit("x", trail_points = 10)`,
		"two defaults":  "x = switch close\n    1 => 1\n    => 2\n    => 3",
		"bad tf input":  `t = input.timeframe("fortnight", "TF")`,
		"bad timestamp": `t = input.time(timestamp("tomorrow"), "Start")`,
	}
	for name, body := range cases {
		src := "//@version=5\nstrategy(\"x\")\n" + body + "\n"
		_, err := pine.Parse(src)
		es := pine.Errors(err)
		if len(es) == 0 {
			t.Errorf("%s: expected a positioned error, got %v", name, err)
			continue
		}
		if es[0].Line < 3 || es[0].Col < 1 {
			t.Errorf("%s: error position %d:%d", name, es[0].Line, es[0].Col)
		}
	}
	// A timeframe below the run interval fails on the first bar with line:col.
	s, err := pine.Parse("//@version=5\nindicator(\"x\")\nplot(request.security(syminfo.tickerid, \"5\", close))\n")
	if err != nil {
		t.Fatal(err)
	}
	st, err := pine.Compile(s, "BTC", domain.I15m, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs := candles(t)
	_, err = backtest.Run(context.Background(), st, &testkit.Source{Candle: map[string][]domain.Candle{"BTC": cs}},
		backtest.Options{Now: func() time.Time { return time.UnixMilli(cs[len(cs)-1].T + 86_400_000) }})
	if err == nil || !strings.Contains(err.Error(), "3:") || !strings.Contains(err.Error(), "lower than the run interval") {
		t.Fatalf("lower timeframe: %v", err)
	}
	// Bad overrides for the new input kinds.
	s, err = pine.Parse("//@version=5\nindicator(\"x\")\ntf = input.timeframe(\"D\", \"TF\")\nst = input.time(timestamp(2024, 1, 1), \"Start\")\nplot(close)\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pine.Compile(s, "BTC", domain.I1h, map[string]any{"TF": "10S"}); err == nil {
		t.Fatal("seconds timeframe override accepted")
	}
	if _, err := pine.Compile(s, "BTC", domain.I1h, map[string]any{"Start": "someday"}); err == nil {
		t.Fatal("bad time override accepted")
	}
}

func TestTimestampAndInputTime(t *testing.T) {
	cs := synth1h(24 * 5) // from 2023-12-01 00:00 UTC
	src := `//@version=5
indicator("time")
start = input.time(timestamp("2023-12-03T00:00:00"), "Start")
plot(time >= start ? 1 : 0, "in")
plot(timestamp(2023, 12, 2, 6, 30), "ts1")
plot(timestamp("UTC", 2023, 12, 2), "ts2")
plot(timestamp("GMT+2", 2023, 12, 2, 2, 0), "ts3")
plot(timestamp("02 Dec 2023 00:00 +0000"), "ts4")
plot(timestamp(year, month, dayofmonth), "dayStart")
`
	s := compileIv(t, src, domain.I1h, nil)
	if s.Inputs[0].Kind != "time" || s.Inputs[0].Default != float64(time.Date(2023, 12, 3, 0, 0, 0, 0, time.UTC).UnixMilli()) {
		t.Fatalf("input.time %+v", s.Inputs[0])
	}
	res := runIv(t, src, domain.I1h, cs, nil, backtest.Options{})
	d2 := float64(time.Date(2023, 12, 2, 0, 0, 0, 0, time.UTC).UnixMilli())
	if res.Plots["ts1"][0] != d2+6.5*3600_000 || res.Plots["ts2"][0] != d2 || res.Plots["ts3"][0] != d2 || res.Plots["ts4"][0] != d2 {
		t.Fatalf("timestamp forms %v %v %v %v", res.Plots["ts1"][0], res.Plots["ts2"][0], res.Plots["ts3"][0], res.Plots["ts4"][0])
	}
	if res.Plots["in"][47] != 0 || res.Plots["in"][48] != 1 || res.Plots["dayStart"][30] != d2 {
		t.Fatalf("window %v %v dayStart %v", res.Plots["in"][47], res.Plots["in"][48], res.Plots["dayStart"][30])
	}
	res = runIv(t, src, domain.I1h, cs, map[string]any{"Start": "2023-12-04"}, backtest.Options{})
	if res.Plots["in"][71] != 0 || res.Plots["in"][72] != 1 {
		t.Fatal("input.time override by date string")
	}
}

// path builds candles from closes with a tiny wick (H/L = max/min(o,c) +/- w).
func path(closes []float64, w float64) []domain.Candle {
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	out := make([]domain.Candle, len(closes))
	prev := closes[0]
	for i, c := range closes {
		o := prev
		out[i] = domain.Candle{T: t0 + int64(i)*3600_000, O: o, H: math.Max(o, c) + w, L: math.Min(o, c) - w, C: c, V: 1, N: 1}
		prev = c
	}
	return out
}

func TestTrailingStopExit(t *testing.T) {
	// Flat at 100, up 1/bar to 130, then down 1/bar (mirrored around 150
	// for the short). mintick is 0.01 on the whole path.
	var up []float64
	for i := 0; i < 10; i++ {
		up = append(up, 100)
	}
	for p := 101.0; p <= 130; p++ {
		up = append(up, p)
	}
	for p := 129.0; p >= 90; p-- {
		up = append(up, p)
	}
	down := make([]float64, len(up))
	for i, p := range up {
		down[i] = 300 - p // stays above 100: mintick 0.01 throughout
	}
	for _, tc := range []struct {
		name   string
		closes []float64
		dir    string
		long   bool
	}{{"long", up, "strategy.long", true}, {"short", down, "strategy.short", false}} {
		src := `//@version=5
strategy("trail", default_qty_type = strategy.fixed, default_qty_value = 1)
if bar_index == 5
    strategy.entry("E", ` + tc.dir + `)
strategy.exit("X", "E", trail_points = 500, trail_offset = 300)
`
		cs := path(tc.closes, 0.2)
		res := runIv(t, src, domain.I1h, cs, nil, backtest.Options{NoCosts: true})
		if len(res.Fills) != 2 {
			t.Fatalf("%s: fills %+v", tc.name, res.Fills)
		}
		entry := res.Fills[0].Px // bar 6 open
		// Replay the client-side trail: from the entry bar on, the stop for
		// bar j is best(6..j-1) -/+ 3 once best passed entry +/- 5.
		best := math.NaN()
		wantPx, wantT := 0.0, int64(0)
		for j := 6; j < len(cs); j++ {
			if !math.IsNaN(best) {
				var stop float64
				active := false
				if tc.long {
					active, stop = best >= entry+5, best-3
				} else {
					active, stop = best <= entry-5, best+3
				}
				if active {
					c := cs[j]
					if tc.long && c.O <= stop {
						wantPx, wantT = c.O, c.T
					} else if tc.long && c.L <= stop {
						wantPx, wantT = stop, c.T
					} else if !tc.long && c.O >= stop {
						wantPx, wantT = c.O, c.T
					} else if !tc.long && c.H >= stop {
						wantPx, wantT = stop, c.T
					}
					if wantT != 0 {
						break
					}
				}
			}
			if tc.long {
				best = math.Max(nz(best, cs[j].H), cs[j].H)
			} else {
				best = math.Min(nz(best, cs[j].L), cs[j].L)
			}
		}
		x := res.Fills[1]
		if x.Time != wantT || math.Abs(x.Px-wantPx) > 1e-9 {
			t.Fatalf("%s: trailing exit at %d %.4f, want %d %.4f (entry %.2f)", tc.name, x.Time, x.Px, wantT, wantPx, entry)
		}
		if tc.long && (x.Px < 126 || x.Px > 128) || !tc.long && (x.Px > 174 || x.Px < 172) {
			t.Fatalf("%s: exit %.2f is not ~3 behind the extreme", tc.name, x.Px)
		}
	}
}

func nz(x, r float64) float64 {
	if math.IsNaN(x) {
		return r
	}
	return x
}

func TestEntryIdComesFromFills(t *testing.T) {
	// Spec A1 repro: a marketable limit entry reverses the short; the exit
	// for "L" must then be placed and fire.
	cs := candles(t)
	src := `//@version=5
strategy("posEntry repro", default_qty_type = strategy.fixed, default_qty_value = 0.01)
if bar_index == 50
    strategy.entry("S", strategy.short)
if bar_index == 52
    strategy.entry("L", strategy.long, limit = close * 1.5)
if bar_index >= 56 and strategy.position_size > 0
    strategy.exit("Lx", "L", stop = close * 2)
`
	res := run(t, compile(t, src, nil), cs, backtest.Options{NoCosts: true})
	var dirs []string
	for _, f := range res.Fills {
		dirs = append(dirs, f.Dir)
	}
	if len(dirs) != 3 || dirs[0] != "Open Short" || dirs[1] != "Short > Long" || dirs[2] != "Close Long" {
		t.Fatalf("fills %v", dirs)
	}
	if res.Fills[2].Time != cs[57].T {
		t.Fatalf("exit at %d, want bar 57", res.Fills[2].Time)
	}
}

func TestOCACancelGroup(t *testing.T) {
	var closes []float64
	for i := 0; i < 12; i++ {
		closes = append(closes, 100)
	}
	for p := 99.0; p >= 80; p-- {
		closes = append(closes, p)
	}
	cs := path(closes, 0.1)
	for _, tc := range []struct {
		oca   string
		fills int
	}{{"strategy.oca.cancel", 1}, {"strategy.oca.none", 2}} {
		src := `//@version=5
strategy("oca", default_qty_type = strategy.fixed, default_qty_value = 1)
if bar_index == 10
    strategy.entry("A", strategy.long, stop = 50, oca_name = "g", oca_type = ` + tc.oca + `)
    strategy.entry("B", strategy.short, stop = 95, oca_name = "g", oca_type = ` + tc.oca + `)
`
		res := runIv(t, src, domain.I1h, cs, nil, backtest.Options{NoCosts: true})
		if len(res.Fills) != tc.fills {
			t.Fatalf("%s: %d fills %+v", tc.oca, len(res.Fills), res.Fills)
		}
	}
}

func TestMintickFollowsPrice(t *testing.T) {
	cs := path([]float64{0.5, 0.9, 2, 5, 9.5, 15, 150, 1500, 15000, 150000}, 0)
	src := "//@version=5\nindicator(\"tick\")\nplot(syminfo.mintick, \"tick\")\n"
	res := runIv(t, src, domain.I1h, cs, nil, backtest.Options{})
	want := []float64{1e-5, 1e-5, 1e-4, 1e-4, 1e-4, 1e-3, 1e-2, 1e-1, 1, 1}
	for i, w := range want {
		if g := res.Plots["tick"][i]; math.Abs(g-w) > w*1e-9 {
			t.Fatalf("bar %d (px %v): mintick %v want %v", i, cs[i].C, g, w)
		}
	}
}
