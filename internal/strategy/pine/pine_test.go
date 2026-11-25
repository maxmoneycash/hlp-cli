package pine_test

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/chart"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/strategy"
	"github.com/maxmoneycash/hlp/internal/strategy/backtest"
	"github.com/maxmoneycash/hlp/internal/strategy/internal/testkit"
	"github.com/maxmoneycash/hlp/internal/strategy/pine"
)

func candles(t *testing.T) []domain.Candle {
	t.Helper()
	cs, err := testkit.BTC15m()
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func run(t *testing.T, st strategy.Strategy, cs []domain.Candle, o backtest.Options) *backtest.Result {
	t.Helper()
	coin := st.Spec().Universe[0]
	o.Now = func() time.Time { return time.UnixMilli(cs[len(cs)-1].T + 24*3600_000) }
	res, err := backtest.Run(context.Background(), st, &testkit.Source{Candle: map[string][]domain.Candle{coin: cs}}, o)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func compile(t *testing.T, src string, inputs map[string]any) strategy.Strategy {
	t.Helper()
	s, err := pine.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	st, err := pine.Compile(s, "BTC", domain.I15m, inputs)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return st
}

func TestExamplesParseAndRun(t *testing.T) {
	cs := candles(t)
	files, _ := filepath.Glob(filepath.Join(testkit.RepoRoot(), "examples", "pine", "*.pine"))
	if len(files) < 6 {
		t.Fatalf("expected at least 6 example strategies, found %d", len(files))
	}
	traded := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s, err := pine.Parse(string(b))
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		if !s.IsStrat || s.Title == "" || len(s.Plots) == 0 || len(s.Inputs) == 0 {
			t.Errorf("%s: script metadata %+v", filepath.Base(f), s)
		}
		st, err := pine.Compile(s, "BTC", domain.I15m, nil)
		if err != nil {
			t.Fatal(err)
		}
		res := run(t, st, cs, backtest.Options{})
		if len(res.Fills) > 0 {
			traded++
		}
	}
	if traded < len(files)-1 {
		t.Fatalf("only %d of %d examples traded on the sample", traded, len(files))
	}
}

// ema is an independent SMA-seeded EMA (TradingView/PyneCore semantics).
func ema(xs []float64, n int) []float64 {
	out := make([]float64, len(xs))
	a := 2 / float64(n+1)
	sum := 0.0
	for i, x := range xs {
		switch {
		case i < n-1:
			sum += x
			out[i] = math.NaN()
		case i == n-1:
			sum += x
			out[i] = sum / float64(n)
		default:
			out[i] = a*x + (1-a)*out[i-1]
		}
	}
	return out
}

func closes(cs []domain.Candle) []float64 {
	out := make([]float64, len(cs))
	for i, c := range cs {
		out[i] = c.C
	}
	return out
}

// goEMACross is a hand-written Go version of examples/pine/ema-cross.pine.
type goEMACross struct {
	fast, slow []float64
	idx        map[int64]int
}

func (g *goEMACross) Spec() strategy.Spec {
	return strategy.Spec{Name: "go", Universe: []string{"BTC"}, Interval: domain.I15m}
}
func (g *goEMACross) Init(strategy.Context) error { return nil }
func (g *goEMACross) OnBar(ctx strategy.Context) error {
	i := g.idx[ctx.Now()]
	if i == 0 {
		return nil
	}
	f, s, pf, ps := g.fast[i], g.slow[i], g.fast[i-1], g.slow[i-1]
	if math.IsNaN(f) || math.IsNaN(s) || math.IsNaN(pf) || math.IsNaN(ps) {
		return nil
	}
	pos := ctx.Position("BTC")
	size := ctx.Equity() / ctx.Price("BTC")
	switch {
	case f > s && pf <= ps && pos <= 0:
		return ctx.Order(strategy.Order{Coin: "BTC", Side: domain.Buy, Size: size + math.Abs(pos), Tag: "Long"})
	case f < s && pf >= ps && pos >= 0:
		return ctx.Order(strategy.Order{Coin: "BTC", Side: domain.Sell, Size: size + math.Abs(pos), Tag: "Short"})
	}
	return nil
}

func TestEMACrossMatchesGoEquivalent(t *testing.T) {
	cs := candles(t)
	b, err := os.ReadFile(filepath.Join(testkit.RepoRoot(), "examples", "pine", "ema-cross.pine"))
	if err != nil {
		t.Fatal(err)
	}
	p := run(t, compile(t, string(b), nil), cs, backtest.Options{})
	idx := map[int64]int{}
	for i, c := range cs {
		idx[c.T] = i
	}
	g := run(t, &goEMACross{fast: ema(closes(cs), 9), slow: ema(closes(cs), 21), idx: idx}, cs, backtest.Options{})
	if len(p.Fills) == 0 || len(p.Fills) != len(g.Fills) {
		t.Fatalf("pine %d fills, go %d fills", len(p.Fills), len(g.Fills))
	}
	for i := range p.Fills {
		a, b := p.Fills[i], g.Fills[i]
		if a.Time != b.Time || a.Side != b.Side || math.Abs(a.Sz-b.Sz) > 1e-9 || a.Px != b.Px {
			t.Fatalf("fill %d differs: pine %+v go %+v", i, a, b)
		}
	}
	if math.Abs(p.Equity.Last()-g.Equity.Last()) > 1e-6 {
		t.Fatalf("equity pine %.4f go %.4f", p.Equity.Last(), g.Equity.Last())
	}
	// The plotted EMAs equal the chart package's (what the chart draws).
	if ce := chart.EMA(cs, 9); ce != nil {
		for i, v := range p.Plots["Fast"] {
			if math.IsNaN(v) != math.IsNaN(ce[i]) || (!math.IsNaN(v) && math.Abs(v-ce[i]) > 1e-9) {
				t.Fatalf("bar %d: pine EMA %v, chart.EMA %v", i, v, ce[i])
			}
		}
	}
}

// Independent Wilder RSI and ATR.
func refRSI(xs []float64, n int) []float64 {
	out := make([]float64, len(xs))
	var up, dn float64
	for i := range xs {
		out[i] = math.NaN()
		if i == 0 {
			continue
		}
		ch := xs[i] - xs[i-1]
		u, d := math.Max(ch, 0), math.Max(-ch, 0)
		switch {
		case i < n:
			up += u
			dn += d
			continue
		case i == n:
			up = (up + u) / float64(n)
			dn = (dn + d) / float64(n)
		default:
			up = (up*float64(n-1) + u) / float64(n)
			dn = (dn*float64(n-1) + d) / float64(n)
		}
		switch {
		case dn == 0:
			out[i] = 100
		case up == 0:
			out[i] = 0
		default:
			out[i] = 100 - 100/(1+up/dn)
		}
	}
	return out
}

func refATR(cs []domain.Candle, n int) []float64 {
	out := make([]float64, len(cs))
	var atr float64
	for i, c := range cs {
		tr := c.H - c.L
		if i > 0 {
			p := cs[i-1].C
			tr = math.Max(tr, math.Max(math.Abs(c.H-p), math.Abs(c.L-p)))
		}
		out[i] = math.NaN()
		switch {
		case i < n-1:
			atr += tr
		case i == n-1:
			atr = (atr + tr) / float64(n)
			out[i] = atr
		default:
			atr = (atr*float64(n-1) + tr) / float64(n)
			out[i] = atr
		}
	}
	return out
}

func sameSeries(t *testing.T, name string, got, want []float64, tol float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: len %d vs %d", name, len(got), len(want))
	}
	n := 0
	for i := range got {
		if math.IsNaN(got[i]) != math.IsNaN(want[i]) {
			t.Fatalf("%s bar %d: got %v want %v", name, i, got[i], want[i])
		}
		if !math.IsNaN(got[i]) {
			n++
			if math.Abs(got[i]-want[i]) > tol*math.Max(1, math.Abs(want[i])) {
				t.Fatalf("%s bar %d: got %.12f want %.12f", name, i, got[i], want[i])
			}
		}
	}
	if n < len(got)/2 {
		t.Fatalf("%s: only %d values", name, n)
	}
}

func TestIndicatorsMatchReference(t *testing.T) {
	cs := candles(t)
	src := `//@version=5
indicator("ta reference")
plot(ta.rsi(close, 14), "rsi")
plot(ta.atr(14), "atr")
plot(ta.sma(close, 20), "sma")
plot(ta.ema(close, 20), "ema")
plot(ta.rma(close, 10), "rma")
plot(ta.stdev(close, 20), "stdev")
plot(ta.highest(high, 10), "hh")
plot(ta.lowest(10), "ll")
plot(ta.change(close, 3), "chg")
`
	res := run(t, compile(t, src, nil), cs, backtest.Options{})
	if len(res.Fills) != 0 {
		t.Fatal("an indicator traded")
	}
	sameSeries(t, "rsi", res.Plots["rsi"], refRSI(closes(cs), 14), 1e-9)
	sameSeries(t, "atr", res.Plots["atr"], refATR(cs, 14), 1e-9)
	sameSeries(t, "ema", res.Plots["ema"], ema(closes(cs), 20), 1e-9)
	if sma := chart.SMA(cs, 20); sma != nil {
		sameSeries(t, "sma vs chart.SMA", res.Plots["sma"], sma, 1e-9)
		sameSeries(t, "ema vs chart.EMA", res.Plots["ema"], chart.EMA(cs, 20), 1e-9)
	}
	xs := closes(cs)
	sd := make([]float64, len(xs))
	hh := make([]float64, len(xs))
	ll := make([]float64, len(xs))
	chg := make([]float64, len(xs))
	for i := range xs {
		sd[i], hh[i], ll[i], chg[i] = math.NaN(), math.NaN(), math.NaN(), math.NaN()
		if i >= 19 {
			m := 0.0
			for _, v := range xs[i-19 : i+1] {
				m += v
			}
			m /= 20
			s := 0.0
			for _, v := range xs[i-19 : i+1] {
				s += (v - m) * (v - m)
			}
			sd[i] = math.Sqrt(s / 20)
		}
		if i >= 9 {
			hh[i], ll[i] = cs[i].H, cs[i].L
			for _, c := range cs[i-9 : i+1] {
				hh[i], ll[i] = math.Max(hh[i], c.H), math.Min(ll[i], c.L)
			}
		}
		if i >= 3 {
			chg[i] = xs[i] - xs[i-3]
		}
	}
	sameSeries(t, "stdev", res.Plots["stdev"], sd, 1e-9)
	sameSeries(t, "highest", res.Plots["hh"], hh, 0)
	sameSeries(t, "lowest", res.Plots["ll"], ll, 0)
	sameSeries(t, "change", res.Plots["chg"], chg, 1e-12)
}

// TestSupertrendMatchesPineRecurrence writes the supertrend recurrence out
// by hand in Pine (ratcheting ATR bands around hl2, direction flipping when
// the close crosses the active band) and checks the ta.supertrend builtin
// against it. It exercises history on reassigned locals, nz, na, nested
// ternaries and := inside a function.
func TestSupertrendMatchesPineRecurrence(t *testing.T) {
	cs := candles(t)
	src := `//@version=5
indicator("st ref", overlay=true)
bandTrend(mult, len) =>
    a = ta.atr(len)
    lo = hl2 - mult * a
    hi = hl2 + mult * a
    loPrev = nz(lo[1])
    hiPrev = nz(hi[1])
    if not (lo > loPrev or close[1] < loPrev)
        lo := loPrev
    if not (hi < hiPrev or close[1] > hiPrev)
        hi := hiPrev
    int d = na
    float line = na
    onUpper = line[1] == hiPrev
    d := na(a[1]) ? 1 : onUpper ? (close > hi ? -1 : 1) : (close < lo ? 1 : -1)
    line := d == -1 ? lo : hi
    [line, d]

[ref, refDir] = bandTrend(3, 10)
[st, dir] = ta.supertrend(3, 10)
plot(ref, "ref")
plot(st, "builtin")
plot(refDir, "refDir")
plot(dir, "dir")
`
	res := run(t, compile(t, src, nil), cs, backtest.Options{})
	sameSeries(t, "supertrend", res.Plots["builtin"], res.Plots["ref"], 1e-9)
	sameSeries(t, "direction", res.Plots["dir"], res.Plots["refDir"], 0)
}

func TestLanguageFeatures(t *testing.T) {
	cs := candles(t)
	src := `//@version=6
indicator("features")
var int count = 0
count += 1
var float acc = 0.0
acc := acc + 1
sum = 0.0
for i = 0 to 4
    sum += close[i]
k = 0
while k < 3
    k += 1
avg5 = sum / 5
smaOf(src, n) => ta.sma(src, n)
pair(a) => [a * 2, a * 3]
fast = smaOf(close, 3)
slow = smaOf(close, 6)
[d, tr3] = pair(1.5)
grade = close > open ? 1 : close < open ? -1 : 0
cat = ""
if bar_index < 10
    cat := "early"
else if bar_index < 20
    cat := "mid"
else
    cat := "late"
lag = (high - low)[2]
plot(count, "count")
plot(acc, "acc")
plot(avg5, "avg5")
plot(fast, "fast")
plot(slow, "slow")
plot(d + tr3, "tuple")
plot(grade, "grade")
plot(cat == "mid" ? 1 : 0, "mid")
plot(lag, "lag")
plot(nz(close[1000], -1), "nz")
plot(k, "while")
plot(math.max(1, 5, 3) + math.round(2.5) + math.abs(-1), "math")
plot(str.length == na ? 0 : 0, "dummy")
`
	// str.length is not supported: drop that line and check the rest.
	src = strings.Replace(src, `plot(str.length == na ? 0 : 0, "dummy")`+"\n", "", 1)
	res := run(t, compile(t, src, nil), cs, backtest.Options{})
	for i := range cs {
		if res.Plots["count"][i] != float64(i+1) || res.Plots["acc"][i] != float64(i+1) {
			t.Fatalf("bar %d: var counters %v %v", i, res.Plots["count"][i], res.Plots["acc"][i])
		}
		if res.Plots["while"][i] != 3 || res.Plots["tuple"][i] != 7.5 || res.Plots["nz"][i] != -1 || res.Plots["math"][i] != 9 {
			t.Fatalf("bar %d: while=%v tuple=%v nz=%v math=%v", i, res.Plots["while"][i], res.Plots["tuple"][i], res.Plots["nz"][i], res.Plots["math"][i])
		}
		g := 0.0
		if cs[i].C > cs[i].O {
			g = 1
		} else if cs[i].C < cs[i].O {
			g = -1
		}
		if res.Plots["grade"][i] != g {
			t.Fatalf("bar %d: grade %v want %v", i, res.Plots["grade"][i], g)
		}
		mid := 0.0
		if i >= 10 && i < 20 {
			mid = 1
		}
		if res.Plots["mid"][i] != mid {
			t.Fatalf("bar %d: if/else-if chain", i)
		}
	}
	// History: sum of the last 5 closes is na until bar 4.
	if !math.IsNaN(res.Plots["avg5"][3]) || math.Abs(res.Plots["avg5"][10]-(cs[6].C+cs[7].C+cs[8].C+cs[9].C+cs[10].C)/5) > 1e-9 {
		t.Fatalf("avg5 %v %v", res.Plots["avg5"][3], res.Plots["avg5"][10])
	}
	if math.Abs(res.Plots["lag"][10]-(cs[8].H-cs[8].L)) > 1e-9 || !math.IsNaN(res.Plots["lag"][1]) {
		t.Fatalf("lag %v", res.Plots["lag"][10])
	}
	// Each call site of a user function has its own ta state.
	if math.Abs(res.Plots["fast"][10]-(cs[8].C+cs[9].C+cs[10].C)/3) > 1e-9 {
		t.Fatalf("fast sma %v", res.Plots["fast"][10])
	}
	if math.Abs(res.Plots["slow"][10]-(cs[5].C+cs[6].C+cs[7].C+cs[8].C+cs[9].C+cs[10].C)/6) > 1e-9 {
		t.Fatalf("slow sma %v", res.Plots["slow"][10])
	}
}

func TestUnsupportedFeaturesFailWithPosition(t *testing.T) {
	// request.security on another exchange's symbol stays unsupported
	// (same-coin higher timeframes are supported, see features_test.go).
	src := `//@version=5
strategy("mtf")
htf = request.security("BINANCE:ETHUSDT", "D", close)
plot(htf)
`
	_, err := pine.Parse(src)
	es := pine.Errors(err)
	if len(es) != 1 || es[0].Line != 3 || !strings.Contains(es[0].Msg, "request.security") {
		t.Fatalf("errors %v", err)
	}
	if es[0].Error() != "3:7: "+es[0].Msg {
		t.Fatal(es[0].Error())
	}
	cases := map[string]string{
		"array":     "//@version=5\nstrategy(\"x\")\na = array.new_float(10)\n",
		"label":     "//@version=5\nstrategy(\"x\")\nlabel.new(bar_index, high, \"hi\")\n",
		"v4":        "//@version=4\nstudy(\"x\")\n",
		"no decl":   "//@version=5\nx = close\n",
		"undecl":    "//@version=5\nstrategy(\"x\")\nplot(foo)\n",
		"trail":     "//@version=5\nstrategy(\"x\")\nstrategy.exit(\"x\", trail_points=10)\n",
		"switch":    "//@version=5\nstrategy(\"x\")\nswitch close\n",
		"reassign":  "//@version=5\nstrategy(\"x\")\ny := 1\n",
		"dup":       "//@version=5\nstrategy(\"x\")\ny = 1\ny = 2\n",
		"badarg":    "//@version=5\nstrategy(\"x\")\nplot(close, nope=1)\n",
		"missing":   "//@version=5\nstrategy(\"x\")\nplot()\n",
		"type":      "//@version=5\nstrategy(\"x\")\nline l = na\n",
		"break":     "//@version=5\nstrategy(\"x\")\nbreak\n",
		"indent":    "//@version=5\nstrategy(\"x\")\nx = 1\n        y = 2\n",
		"string":    "//@version=5\nstrategy(\"x)\n",
		"tickonly":  "//@version=5\nstrategy(\"x\", calc_on_every_tick=true)\n",
		"recursion": "//@version=5\nstrategy(\"x\")\nf(x) => f(x)\n",
		"empty":     "",
	}
	for name, src := range cases {
		if _, err := pine.Parse(src); err == nil || len(pine.Errors(err)) == 0 {
			t.Errorf("%s: expected positioned errors, got %v", name, err)
		}
	}
	// Several problems are all reported, in source order.
	_, err = pine.Parse("//@version=5\nstrategy(\"x\")\na = array.new_int(1)\nb = request.security(\"\", \"D\", close)\nplot(zz)\n")
	if es := pine.Errors(err); len(es) != 3 || es[0].Line != 3 || es[2].Line != 5 {
		t.Fatalf("joined errors: %v", err)
	}
}

func TestInputsOverride(t *testing.T) {
	b, _ := os.ReadFile(filepath.Join(testkit.RepoRoot(), "examples", "pine", "tradingview-import.pine"))
	s, err := pine.Parse(string(b))
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != 6 || s.QtyType != "percent_of_equity" || s.QtyValue != 100 || !s.Overlay || s.InitialCapital != 10000 {
		t.Fatalf("declaration %+v", s)
	}
	byName := map[string]pine.Input{}
	for _, in := range s.Inputs {
		byName[in.Name] = in
	}
	if in := byName["Fast EMA"]; in.Kind != "int" || in.Default != 21 || in.Min == nil || *in.Min != 1 {
		t.Fatalf("Fast EMA input %+v", in)
	}
	if in := byName["Source"]; in.Kind != "source" || in.Default != "close" {
		t.Fatalf("source input %+v", in)
	}
	if in := byName["Allow shorts"]; in.Kind != "bool" || in.Default != true {
		t.Fatalf("bool input %+v", in)
	}
	good := map[string]any{"Fast EMA": 10, "slowLen": "40", "source": "HL2", "Allow shorts": "false", "Stop (x ATR)": 1.5}
	if _, err := pine.Compile(s, "BTC", domain.I15m, good); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]map[string]any{
		"unknown":  {"Nope": 1},
		"type":     {"Fast EMA": "fast"},
		"min":      {"Fast EMA": 0},
		"source":   {"Source": "vwap"},
		"bool":     {"Allow shorts": 3},
		"coin":     nil,
		"interval": nil,
	} {
		coin, iv := "BTC", domain.I15m
		if name == "coin" {
			coin = ""
		}
		if name == "interval" {
			iv = "9m"
		}
		if _, err := pine.Compile(s, coin, iv, bad); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := pine.Compile(nil, "BTC", domain.I1h, nil); err == nil {
		t.Error("nil script")
	}
	// Overriding inputs changes behaviour.
	cs := candles(t)
	base := run(t, compile(t, string(b), nil), cs, backtest.Options{})
	alt := run(t, compile(t, string(b), map[string]any{"Allow shorts": false}), cs, backtest.Options{})
	for _, f := range alt.Fills {
		if f.Dir == "Open Short" {
			t.Fatal("shorts disabled but a short was opened")
		}
	}
	if len(base.Fills) == len(alt.Fills) && base.Equity.Last() == alt.Equity.Last() {
		t.Fatal("input override had no effect")
	}
}

func TestExitBracketAndClose(t *testing.T) {
	cs := candles(t)
	src := `//@version=5
strategy("bracket", default_qty_type=strategy.cash, default_qty_value=5000)
if bar_index == 5
    strategy.entry("L", strategy.long)
    strategy.exit("TP/SL", "L", profit=3000, loss=3000)
if bar_index == 200
    strategy.entry("S", strategy.short, qty=0.01)
if bar_index == 210
    strategy.close("S", comment="done")
`
	res := run(t, compile(t, src, nil), cs, backtest.Options{NoCosts: true})
	if len(res.Fills) < 4 {
		t.Fatalf("fills %+v", res.Fills)
	}
	entry := res.Fills[0]
	if entry.Time != cs[6].T || math.Abs(entry.Sz-5000/cs[5].C) > 1e-9 {
		t.Fatalf("entry %+v", entry)
	}
	exit := res.Fills[1]
	// Ticks are anchored to the real fill price once the entry has filled
	// (on the entry bar itself the decision close is the estimate).
	tick := math.Pow(10, math.Floor(math.Log10(cs[0].C))-4)
	ref := entry.Px
	if exit.Time == entry.Time {
		ref = cs[5].C
	}
	tp, sl := ref+3000*tick, ref-3000*tick
	if exit.Dir != "Close Long" || (math.Abs(exit.Px-tp) > 1e-6 && math.Abs(exit.Px-sl) > 1e-6 && exit.Px != cs[idxOf(cs, exit.Time)].O) {
		t.Fatalf("exit %+v want tp %v or sl %v", exit, tp, sl)
	}
	last := res.Fills[len(res.Fills)-1]
	if last.Time != cs[211].T || last.Dir != "Close Short" || last.Sz != 0.01 {
		t.Fatalf("close %+v", last)
	}
	labels := map[string]bool{}
	for _, m := range res.Markers {
		labels[m.Label] = true
	}
	if !labels["L"] || !labels["TP/SL"] || !labels["done"] {
		t.Fatalf("markers %v", labels)
	}
}

func idxOf(cs []domain.Candle, t int64) int {
	for i, c := range cs {
		if c.T == t {
			return i
		}
	}
	return 0
}

// TestRuntimeErrorAndResume: a runtime error carries line:col; a Pine
// strategy that starts mid-history replays earlier bars silently.
func TestRuntimeErrorAndReplay(t *testing.T) {
	cs := candles(t)
	st := compile(t, "//@version=5\nstrategy(\"x\")\nif bar_index == 3\n    runtime.error(\"stop here\")\n", nil)
	_, err := backtest.Run(context.Background(), st, &testkit.Source{Candle: map[string][]domain.Candle{"BTC": cs}},
		backtest.Options{Now: func() time.Time { return time.Now() }})
	var pe *pine.Error
	if !errors.As(err, &pe) || pe.Line != 4 || !strings.Contains(pe.Msg, "stop here") {
		t.Fatalf("runtime error %v", err)
	}
	// Warmup > 0: the first OnBar sees 50 bars; Pine replays them, and the
	// var counter still counts every bar.
	ws := &warm{compile(t, "//@version=5\nindicator(\"x\")\nvar n = 0\nn += 1\nplot(n, \"n\")\n", nil)}
	res := run(t, ws, cs, backtest.Options{})
	if got := res.Plots["n"][49]; got != 50 {
		t.Fatalf("replayed counter = %v, want 50", got)
	}
	if !math.IsNaN(res.Plots["n"][10]) {
		t.Fatal("replayed bars must not plot")
	}
}

type warm struct{ strategy.Strategy }

func (w *warm) Spec() strategy.Spec {
	s := w.Strategy.Spec()
	s.Warmup = 50
	return s
}
