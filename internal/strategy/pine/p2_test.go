package pine_test

import (
	"math"
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/backtest"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/pine"
)

// B8: v5 const int / const int truncates; v6 returns the quotient.
func TestIntDivisionByVersion(t *testing.T) {
	cs := path([]float64{1, 2, 3}, 0)
	for v, want := range map[int]float64{5: 2, 6: 2.5} {
		src := "//@version=" + string(rune('0'+v)) + "\nindicator(\"d\")\nplot(5/2, \"q\")\nplot(-7/2 + 0.0, \"n\")\nplot(close/2, \"c\")\n"
		res := runIv(t, src, domain.I1h, cs, nil, backtest.Options{})
		if g := res.Plots["q"][0]; g != want {
			t.Fatalf("v%d: 5/2 = %v, want %v", v, g, want)
		}
		if g := res.Plots["c"][0]; g != 0.5 {
			t.Fatalf("v%d: close/2 = %v", v, g)
		}
		if v == 5 && res.Plots["n"][0] != -3 {
			t.Fatalf("v5: -7/2 = %v, want -3 (truncation toward zero)", res.Plots["n"][0])
		}
	}
}

// B7: casts and str.format.
func TestCastsAndFormat(t *testing.T) {
	cs := path([]float64{1.75, 2.25}, 0)
	src := `//@version=6
indicator("c")
plot(int(close), "i")
plot(float(true), "f")
plot(bool(0) ? 1 : 0, "b")
s = str.format("{0} / {1,number,#.##} / {2,number,percent}", "x", close, 0.25)
plot(s == "x / 2.25 / 25%" ? 1 : 0, "fmt")
`
	res := runIv(t, src, domain.I1h, cs, nil, backtest.Options{})
	if res.Plots["i"][1] != 2 || res.Plots["f"][1] != 1 || res.Plots["b"][1] != 0 || res.Plots["fmt"][1] != 1 {
		t.Fatalf("plots %v", res.Plots)
	}
}

// B6: trade statistics come from fills.
func TestTradeStatistics(t *testing.T) {
	var closes []float64
	for i := 0; i < 20; i++ {
		closes = append(closes, 100+float64(i))
	}
	cs := path(closes, 0.5)
	src := `//@version=6
strategy("stats", default_qty_type = strategy.fixed, default_qty_value = 1)
if bar_index == 2
    strategy.entry("L", strategy.long)
if bar_index == 5
    strategy.close("L")
if bar_index == 7
    strategy.entry("S", strategy.short)
if bar_index == 10
    strategy.close("S")
plot(strategy.closedtrades, "closed")
plot(strategy.wintrades, "wins")
plot(strategy.losstrades, "losses")
plot(strategy.grossprofit, "gp")
plot(strategy.grossloss, "gl")
plot(strategy.max_drawdown, "dd")
plot(strategy.opentrades.entry_price(0), "entry")
plot(strategy.closedtrades.exit_price(0), "exit0")
plot(strategy.closedtrades.profit(1), "p1")
`
	res := runIv(t, src, domain.I1h, cs, nil, backtest.Options{NoCosts: true})
	last := func(n string) float64 { return res.Plots[n][len(cs)-1] }
	if last("closed") != 2 || last("wins") != 1 || last("losses") != 1 {
		t.Fatalf("closed %v wins %v losses %v", last("closed"), last("wins"), last("losses"))
	}
	// long 3 -> 6 opens: +3; short 8 -> 11 opens: -3
	if math.Abs(last("gp")-3) > 1e-9 || math.Abs(last("gl")-3) > 1e-9 || math.Abs(last("p1")+3) > 1e-9 {
		t.Fatalf("gp %v gl %v p1 %v", last("gp"), last("gl"), last("p1"))
	}
	if e := res.Plots["entry"][4]; e != cs[3].O {
		t.Fatalf("open trade entry %v, want %v", e, cs[3].O)
	}
	if x := last("exit0"); x != cs[6].O {
		t.Fatalf("exit price %v, want %v", x, cs[6].O)
	}
	if last("dd") <= 0 {
		t.Fatalf("max drawdown %v", last("dd"))
	}
}

// B6: strategy.exit(from_entry) and strategy.close(id) work for a pyramided
// entry that is not the position's first (known from the trade list).
func TestPyramidedEntryClose(t *testing.T) {
	var closes []float64
	for i := 0; i < 15; i++ {
		closes = append(closes, 100+float64(i))
	}
	cs := path(closes, 0.5)
	src := `//@version=6
strategy("pyr", pyramiding = 2, default_qty_type = strategy.fixed, default_qty_value = 1)
if bar_index == 2
    strategy.entry("A", strategy.long)
if bar_index == 4
    strategy.entry("B", strategy.long, qty = 2)
if bar_index == 7
    strategy.close("B")
plot(strategy.position_size, "pos")
`
	res := runIv(t, src, domain.I1h, cs, nil, backtest.Options{NoCosts: true})
	if p := res.Plots["pos"][len(cs)-1]; p != 1 {
		t.Fatalf("after closing B the position is %v, want 1 (A stays)", p)
	}
}

// B3: the added ta.* built-ins against direct computations.
func TestMoreTA(t *testing.T) {
	closes := []float64{10, 11, 15, 12, 11, 13, 17, 16, 14, 18, 19, 15, 13, 16, 20, 22, 21, 19, 23, 24}
	cs := path(closes, 0.5)
	for i := range cs {
		cs[i].V = float64(10 + i)
	}
	src := `//@version=6
indicator("ta")
plot(ta.percentrank(close, 4), "pr")
plot(ta.median(close, 5), "med")
plot(ta.alma(close, 5, 0.85, 6), "alma")
plot(ta.highestbars(close, 5), "hb")
plot(ta.lowestbars(close, 5), "lb")
plot(ta.pivothigh(close, 2, 2), "ph")
plot(ta.pivotlow(close, 2, 2), "pl")
plot(ta.obv, "obv")
plot(ta.sar(0.02, 0.02, 0.2), "sar")
plot(ta.mfi(hlc3, 5), "mfi")
[kb, ku, kl] = ta.kc(close, 5, 2)
plot(kb, "kb")
plot(ku - kl, "kw")
plot(ta.ema(close, 5), "ema")
[dp, dm, adx] = ta.dmi(5, 5)
plot(dp, "dp")
plot(adx, "adx")
`
	res := runIv(t, src, domain.I1h, cs, nil, backtest.Options{})
	P := res.Plots
	i := 9 // close 18; previous 4: 17 16 14 18? -> window 13,17,16,14 then 18
	if P["pr"][i] != 100 {
		t.Fatalf("percentrank %v", P["pr"][i])
	}
	if P["med"][i] != 16 { // 13 17 16 14 18
		t.Fatalf("median %v", P["med"][i])
	}
	// alma by hand
	n, m, s := 5.0, 0.85*4, 5.0/6
	sum, norm := 0.0, 0.0
	for k := 0; k < 5; k++ {
		w := math.Exp(-(float64(k) - m) * (float64(k) - m) / (2 * s * s))
		sum += closes[i-4+k] * w
		norm += w
	}
	_ = n
	if math.Abs(P["alma"][i]-sum/norm) > 1e-9 {
		t.Fatalf("alma %v want %v", P["alma"][i], sum/norm)
	}
	if P["hb"][i] != 0 || P["lb"][i] != -4 { // 18 is the high now; 13 four bars ago
		t.Fatalf("highestbars %v lowestbars %v", P["hb"][i], P["lb"][i])
	}
	// pivot high 17 at bar 6 is confirmed at bar 8; pivot low 11 at 4 at bar 6
	if P["ph"][8] != 17 || !math.IsNaN(P["ph"][7]) || P["pl"][6] != 11 {
		t.Fatalf("pivots ph8=%v ph7=%v pl6=%v", P["ph"][8], P["ph"][7], P["pl"][6])
	}
	obv := 0.0
	for k := 1; k <= i; k++ {
		switch {
		case closes[k] > closes[k-1]:
			obv += cs[k].V
		case closes[k] < closes[k-1]:
			obv -= cs[k].V
		}
	}
	if P["obv"][i] != obv {
		t.Fatalf("obv %v want %v", P["obv"][i], obv)
	}
	if !math.IsNaN(P["sar"][0]) || math.IsNaN(P["sar"][5]) {
		t.Fatalf("sar %v", P["sar"][:6])
	}
	for k, v := range P["mfi"] {
		if !math.IsNaN(v) && (v < 0 || v > 100) {
			t.Fatalf("mfi[%d] = %v", k, v)
		}
	}
	if P["kb"][i] != P["ema"][i] || !(P["kw"][i] > 0) {
		t.Fatalf("kc basis %v ema %v width %v", P["kb"][i], P["ema"][i], P["kw"][i])
	}
	last := len(cs) - 1
	if math.IsNaN(P["dp"][last]) || math.IsNaN(P["adx"][last]) || P["adx"][last] < 0 || P["adx"][last] > 100 {
		t.Fatalf("dmi %v adx %v", P["dp"][last], P["adx"][last])
	}
	if _, err := pine.Parse("//@version=5\nindicator(\"x\")\n[a, b, c] = ta.dmi(14, 14)\nplot(a)\n"); err != nil {
		t.Fatalf("ta.dmi still unsupported: %v", err)
	}
}
