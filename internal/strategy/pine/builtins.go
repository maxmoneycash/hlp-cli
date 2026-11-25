package pine

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/strategy"
	"github.com/maxmoneycash/hlp/internal/strategy/sim"
)

// builtin is a Pine function implemented in Go.
type builtin struct {
	params   []string // parameter names in Pine order
	required int
	variadic bool // accepts any positional args (no named-arg checking)
	stateful bool // keeps per-call-site state
	fn       func(rt *runtime, st *callState, a []value, c *callExpr) value
}

// builtinVar is a Pine builtin variable; get(rt, back) supports x[back].
type builtinVar struct {
	get func(rt *runtime, back int) value
}

var builtins map[string]*builtin
var builtinVars map[string]*builtinVar

// unsupportedNS lists namespaces outside the subset, with the reason shown
// to the user.
var unsupportedNS = map[string]string{
	"request.security_lower_tf": "request.security_lower_tf (intrabar data) is not supported; hlp runs on closed bars",
	"request.":                  "request.* other than request.security (same coin, chart or higher timeframe) is not supported yet",
	"array.":                    "arrays are not supported yet",
	"map.":                      "maps are not supported yet",
	"matrix.":                   "matrices are not supported yet",
	"table.":                    "tables are not supported (hlp renders its own cards)",
	"label.":                    "labels are not supported (use plotshape for markers)",
	"line.":                     "drawings (line.*) are not supported",
	"box.":                      "drawings (box.*) are not supported",
	"linefill.":                 "drawings (linefill.*) are not supported",
	"polyline.":                 "drawings (polyline.*) are not supported",
	"chart.point.":              "drawings (chart.point) are not supported",
	"ticker.":                   "ticker.* (other symbols) is not supported",
	"strategy.risk.":            "strategy.risk.* is not supported yet (use hlp's risk caps)",
	"session.":                  "sessions are not supported (Hyperliquid trades 24/7)",
	"input.session":             "input.session is not supported (Hyperliquid trades 24/7)",
	"input.symbol":              "input.symbol is not supported (one coin per run)",
	"timeframe.change":          "timeframe.change is not supported yet",
	"ta.pivothigh":              "ta.pivothigh is not supported yet",
	"ta.pivotlow":               "ta.pivotlow is not supported yet",
	"ta.dmi":                    "ta.dmi is not supported yet",
}

func unsupportedReason(name string) (string, bool) {
	if _, ok := builtins[name]; ok {
		return "", false
	}
	if why, ok := unsupportedNS[name]; ok {
		return why, true
	}
	best := ""
	for p := range unsupportedNS {
		if strings.HasPrefix(name, p) && len(p) > len(best) {
			best = p
		}
	}
	if best != "" {
		return unsupportedNS[best], true
	}
	for p, why := range unsupportedNS {
		if name == strings.TrimSuffix(p, ".") || strings.HasPrefix(name, p) {
			return why, true
		}
	}
	return "", false
}

func p(names ...string) []string { return names }

var colors = map[string]string{
	"aqua": "#00BCD4", "black": "#363A45", "blue": "#2196F3", "fuchsia": "#E040FB", "gray": "#787B86",
	"green": "#4CAF50", "lime": "#00E676", "maroon": "#880E4F", "navy": "#311B92", "olive": "#808000",
	"orange": "#FF9800", "purple": "#9C27B0", "red": "#F23645", "silver": "#B2B5BE", "teal": "#089981",
	"white": "#FFFFFF", "yellow": "#FDD835",
}

func init() {
	builtinVars = map[string]*builtinVar{}
	bar := func(f func(c domain.Candle) float64) *builtinVar {
		return &builtinVar{get: func(rt *runtime, back int) value {
			c, ok := rt.candleAt(back)
			if !ok {
				return naV
			}
			return num(f(c))
		}}
	}
	builtinVars["open"] = bar(func(c domain.Candle) float64 { return c.O })
	builtinVars["high"] = bar(func(c domain.Candle) float64 { return c.H })
	builtinVars["low"] = bar(func(c domain.Candle) float64 { return c.L })
	builtinVars["close"] = bar(func(c domain.Candle) float64 { return c.C })
	builtinVars["volume"] = bar(func(c domain.Candle) float64 { return c.V })
	builtinVars["hl2"] = bar(func(c domain.Candle) float64 { return (c.H + c.L) / 2 })
	builtinVars["hlc3"] = bar(func(c domain.Candle) float64 { return (c.H + c.L + c.C) / 3 })
	builtinVars["ohlc4"] = bar(func(c domain.Candle) float64 { return (c.O + c.H + c.L + c.C) / 4 })
	builtinVars["hlcc4"] = bar(func(c domain.Candle) float64 { return (c.H + c.L + 2*c.C) / 4 })
	builtinVars["time"] = bar(func(c domain.Candle) float64 { return float64(c.T) })
	builtinVars["time_close"] = &builtinVar{get: func(rt *runtime, back int) value {
		c, ok := rt.candleAt(back)
		if !ok {
			return naV
		}
		return num(float64(c.T + rt.iv.Duration().Milliseconds()))
	}}
	cal := func(f func(t time.Time) int) *builtinVar {
		return bar(func(c domain.Candle) float64 { return float64(f(time.UnixMilli(c.T).UTC())) })
	}
	builtinVars["year"] = cal(func(t time.Time) int { return t.Year() })
	builtinVars["month"] = cal(func(t time.Time) int { return int(t.Month()) })
	builtinVars["dayofmonth"] = cal(func(t time.Time) int { return t.Day() })
	builtinVars["dayofweek"] = cal(func(t time.Time) int { return int(t.Weekday()) + 1 })
	builtinVars["hour"] = cal(func(t time.Time) int { return t.Hour() })
	builtinVars["minute"] = cal(func(t time.Time) int { return t.Minute() })
	for i, d := range []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"} {
		v := float64(i + 1)
		builtinVars["dayofweek."+d] = &builtinVar{get: func(*runtime, int) value { return num(v) }}
	}
	builtinVars["bar_index"] = &builtinVar{get: func(rt *runtime, back int) value {
		if rt.bar-back < 0 {
			return naV
		}
		return num(float64(rt.bar - back))
	}}
	builtinVars["barstate.isconfirmed"] = &builtinVar{get: func(*runtime, int) value { return trueV }}
	builtinVars["barstate.islast"] = &builtinVar{get: func(rt *runtime, _ int) value { return boolean(rt.live) }}
	builtinVars["barstate.isrealtime"] = &builtinVar{get: func(rt *runtime, _ int) value { return boolean(rt.live) }}
	builtinVars["barstate.ishistory"] = &builtinVar{get: func(rt *runtime, _ int) value { return boolean(!rt.live) }}
	builtinVars["barstate.isfirst"] = &builtinVar{get: func(rt *runtime, _ int) value { return boolean(rt.bar == 0) }}
	builtinVars["barstate.isnew"] = &builtinVar{get: func(*runtime, int) value { return trueV }}
	cs := func(f func(rt *runtime) value) *builtinVar {
		return &builtinVar{get: func(rt *runtime, _ int) value { return f(rt) }}
	}
	builtinVars["syminfo.ticker"] = cs(func(rt *runtime) value { return str(rt.coin) })
	builtinVars["syminfo.tickerid"] = cs(func(rt *runtime) value { return str("HYPERLIQUID:" + rt.coin) })
	builtinVars["syminfo.root"] = cs(func(rt *runtime) value { return str(rt.coin) })
	builtinVars["syminfo.currency"] = cs(func(*runtime) value { return str("USD") })
	builtinVars["syminfo.mintick"] = cs(func(rt *runtime) value { return num(rt.mintick) })
	builtinVars["syminfo.pointvalue"] = cs(func(*runtime) value { return num(1) })
	builtinVars["syminfo.type"] = cs(func(*runtime) value { return str("crypto") })
	builtinVars["timeframe.period"] = cs(func(rt *runtime) value { return str(tvPeriod(rt.iv)) })
	builtinVars["timeframe.multiplier"] = cs(func(rt *runtime) value { return num(tvMultiplier(rt.iv)) })
	builtinVars["timeframe.isintraday"] = cs(func(rt *runtime) value { return boolean(rt.iv.Duration() < 24*time.Hour) })
	builtinVars["timeframe.isminutes"] = cs(func(rt *runtime) value { return boolean(rt.iv.Duration() < 24*time.Hour) })
	builtinVars["timeframe.isseconds"] = cs(func(*runtime) value { return falseV })
	builtinVars["timeframe.isdaily"] = cs(func(rt *runtime) value {
		d := rt.iv.Duration()
		return boolean(d >= 24*time.Hour && d < 7*24*time.Hour)
	})
	builtinVars["timeframe.isweekly"] = cs(func(rt *runtime) value { return boolean(rt.iv == domain.I1w) })
	builtinVars["timeframe.ismonthly"] = cs(func(rt *runtime) value { return boolean(rt.iv == domain.I1M) })
	builtinVars["math.pi"] = cs(func(*runtime) value { return num(math.Pi) })
	builtinVars["math.e"] = cs(func(*runtime) value { return num(math.E) })
	builtinVars["math.phi"] = cs(func(*runtime) value { return num(math.Phi) })

	for _, k := range []string{"long", "short", "fixed", "cash", "percent_of_equity", "oca.none", "oca.cancel", "oca.reduce",
		"direction.all", "direction.long", "direction.short", "commission.percent", "commission.cash_per_contract", "commission.cash_per_order"} {
		v := str(k)
		builtinVars["strategy."+k] = cs(func(*runtime) value { return v })
	}
	builtinVars["strategy.position_size"] = &builtinVar{get: func(rt *runtime, back int) value {
		if back == 0 {
			if rt.ctx == nil {
				return num(0)
			}
			return num(rt.ctx.Position(rt.coin))
		}
		return rt.posHist.get(rt.bar - back)
	}}
	builtinVars["strategy.position_avg_price"] = cs(func(rt *runtime) value {
		if rt.ctx == nil || rt.ctx.Position(rt.coin) == 0 {
			return naV
		}
		if pi, ok := rt.ctx.(strategy.PositionInfo); ok {
			return num(pi.EntryPrice(rt.coin))
		}
		return naV
	})
	builtinVars["strategy.equity"] = cs(func(rt *runtime) value {
		if rt.ctx == nil {
			return num(rt.prog.decl.capital)
		}
		return num(rt.ctx.Equity())
	})
	builtinVars["strategy.initial_capital"] = cs(func(rt *runtime) value {
		if pi, ok := rt.ctx.(strategy.PositionInfo); ok && rt.ctx != nil {
			return num(pi.InitialCapital())
		}
		return num(rt.prog.decl.capital)
	})
	builtinVars["strategy.netprofit"] = cs(func(rt *runtime) value {
		if pi, ok := rt.ctx.(strategy.PositionInfo); ok && rt.ctx != nil {
			return num(pi.RealizedPnL())
		}
		return num(0)
	})
	builtinVars["strategy.openprofit"] = cs(func(rt *runtime) value {
		if rt.ctx == nil {
			return num(0)
		}
		return num(rt.ctx.Equity() - rt.ctx.Cash())
	})
	builtinVars["strategy.opentrades"] = cs(func(rt *runtime) value {
		if rt.ctx == nil || rt.ctx.Position(rt.coin) == 0 {
			return num(0)
		}
		if rt.trades.enabled && len(rt.trades.open) > 0 {
			return num(float64(len(rt.trades.open)))
		}
		return num(float64(max(rt.entries, 1)))
	})
	builtinVars["ta.tr"] = &builtinVar{get: func(rt *runtime, back int) value { return num(trueRange(rt, back, false)) }}
	builtinVars["ta.vwap"] = cs(func(rt *runtime) value { return num(rt.vwap.val) })

	for name, hex := range colors {
		v := value{k: kColor, s: hex}
		builtinVars["color."+name] = cs(func(*runtime) value { return v })
	}
	consts := map[string][]string{
		"shape":    {"xcross", "cross", "triangleup", "triangledown", "flag", "circle", "arrowup", "arrowdown", "labelup", "labeldown", "square", "diamond"},
		"location": {"abovebar", "belowbar", "top", "bottom", "absolute"},
		"size":     {"auto", "tiny", "small", "normal", "large", "huge"},
		"plot":     {"style_line", "style_linebr", "style_stepline", "style_stepline_diamond", "style_histogram", "style_cross", "style_area", "style_areabr", "style_columns", "style_circles", "linestyle_solid", "linestyle_dashed", "linestyle_dotted"},
		"hline":    {"style_solid", "style_dotted", "style_dashed"},
		"display":  {"all", "none", "data_window", "status_line", "pane", "price_scale"},
		"format":   {"inherit", "price", "volume", "percent", "mintick"},
		"position": {"top_left", "top_center", "top_right", "middle_left", "middle_center", "middle_right", "bottom_left", "bottom_center", "bottom_right"},
		"scale":    {"right", "left", "none"},
		"text":     {"align_left", "align_center", "align_right"},
		"barmerge": {"gaps_on", "gaps_off", "lookahead_on", "lookahead_off"},
	}
	for ns, names := range consts {
		for _, n := range names {
			v := str(ns + "." + n)
			builtinVars[ns+"."+n] = cs(func(*runtime) value { return v })
		}
	}

	builtins = map[string]*builtin{}
	reg := func(name string, b *builtin) { builtins[name] = b }

	// ---- core ----
	reg("na", &builtin{params: p("x"), required: 1, fn: func(_ *runtime, _ *callState, a []value, _ *callExpr) value {
		return boolean(a[0].isNa())
	}})
	reg("nz", &builtin{params: p("source", "replacement"), required: 1, fn: func(_ *runtime, _ *callState, a []value, _ *callExpr) value {
		if a[0].isNa() {
			if a[1].missing() {
				return num(0)
			}
			return a[1]
		}
		return a[0]
	}})
	reg("fixnan", &builtin{params: p("source"), required: 1, stateful: true, fn: func(_ *runtime, st *callState, a []value, _ *callExpr) value {
		if !a[0].isNa() {
			st.data = a[0]
			return a[0]
		}
		if v, ok := st.data.(value); ok {
			return v
		}
		return naV
	}})
	math1 := func(f func(float64) float64) *builtin {
		return &builtin{params: p("number"), required: 1, fn: func(_ *runtime, _ *callState, a []value, _ *callExpr) value {
			return num(f(a[0].f))
		}}
	}
	reg("math.abs", math1(math.Abs))
	reg("math.ceil", math1(math.Ceil))
	reg("math.floor", math1(math.Floor))
	reg("math.sqrt", math1(math.Sqrt))
	reg("math.exp", math1(math.Exp))
	reg("math.log", math1(math.Log))
	reg("math.log10", math1(math.Log10))
	reg("math.sin", math1(math.Sin))
	reg("math.cos", math1(math.Cos))
	reg("math.tan", math1(math.Tan))
	reg("math.asin", math1(math.Asin))
	reg("math.acos", math1(math.Acos))
	reg("math.atan", math1(math.Atan))
	reg("math.todegrees", math1(func(x float64) float64 { return x * 180 / math.Pi }))
	reg("math.toradians", math1(func(x float64) float64 { return x * math.Pi / 180 }))
	reg("math.sign", math1(func(x float64) float64 {
		switch {
		case math.IsNaN(x):
			return x
		case x > 0:
			return 1
		case x < 0:
			return -1
		}
		return 0
	}))
	reg("math.round", &builtin{params: p("number", "precision"), required: 1, fn: func(_ *runtime, _ *callState, a []value, _ *callExpr) value {
		x := a[0].f
		if a[1].missing() || a[1].isNa() {
			return num(roundHalfUp(x))
		}
		m := math.Pow(10, math.Trunc(a[1].f))
		return num(roundHalfUp(x*m) / m)
	}})
	reg("math.pow", &builtin{params: p("base", "exponent"), required: 2, fn: func(_ *runtime, _ *callState, a []value, _ *callExpr) value {
		return num(math.Pow(a[0].f, a[1].f))
	}})
	variadicNum := func(f func([]float64) float64) *builtin {
		return &builtin{variadic: true, required: 1, fn: func(_ *runtime, _ *callState, a []value, _ *callExpr) value {
			xs := make([]float64, len(a))
			for i, v := range a {
				if v.isNa() {
					return naV
				}
				xs[i] = v.f
			}
			return num(f(xs))
		}}
	}
	reg("math.max", variadicNum(func(xs []float64) float64 {
		m := xs[0]
		for _, x := range xs[1:] {
			m = math.Max(m, x)
		}
		return m
	}))
	reg("math.min", variadicNum(func(xs []float64) float64 {
		m := xs[0]
		for _, x := range xs[1:] {
			m = math.Min(m, x)
		}
		return m
	}))
	reg("math.avg", variadicNum(func(xs []float64) float64 {
		s := 0.0
		for _, x := range xs {
			s += x
		}
		return s / float64(len(xs))
	}))
	reg("math.sum", &builtin{params: p("source", "length"), required: 2, stateful: true, fn: func(_ *runtime, st *callState, a []value, _ *callExpr) value {
		w := window(st, a[1])
		w.push(a[0].f)
		if !w.full() {
			return naV
		}
		s := 0.0
		for i := 0; i < w.n; i++ {
			s += w.at(i)
		}
		return num(s)
	}})
	reg("str.tostring", &builtin{params: p("value", "format"), required: 1, fn: func(_ *runtime, _ *callState, a []value, _ *callExpr) value {
		v := a[0]
		if v.k == kNum && !a[1].missing() && a[1].k == kStr {
			if d := strings.Count(strings.TrimPrefix(a[1].s, "#."), "#") + strings.Count(a[1].s, "0") - 1; strings.Contains(a[1].s, ".") && d >= 0 {
				return str(strconv.FormatFloat(v.f, 'f', d, 64))
			}
		}
		return str(v.String())
	}})
	reg("color.new", &builtin{params: p("color", "transp"), required: 1, fn: func(_ *runtime, _ *callState, a []value, _ *callExpr) value {
		return a[0]
	}})
	reg("color.rgb", &builtin{params: p("red", "green", "blue", "transp"), required: 3, fn: func(_ *runtime, _ *callState, a []value, _ *callExpr) value {
		c := func(x float64) int { return int(math.Max(0, math.Min(255, x))) }
		return value{k: kColor, s: "#" + hex2(c(a[0].f)) + hex2(c(a[1].f)) + hex2(c(a[2].f))}
	}})
	reg("color.from_gradient", &builtin{params: p("value", "bottom_value", "top_value", "bottom_color", "top_color"), required: 5,
		fn: func(_ *runtime, _ *callState, a []value, _ *callExpr) value {
			if a[0].f >= (a[1].f+a[2].f)/2 {
				return a[4]
			}
			return a[3]
		}})
	reg("runtime.error", &builtin{params: p("message"), required: 1, fn: func(rt *runtime, _ *callState, a []value, c *callExpr) value {
		rt.fail(c.P, "runtime.error: %s", a[0].String())
		return naV
	}})
	logf := func(level string) *builtin {
		return &builtin{variadic: true, required: 1, fn: func(rt *runtime, _ *callState, a []value, _ *callExpr) value {
			if rt.live && rt.ctx != nil {
				msg := a[0].String()
				for i, v := range a[1:] {
					msg = strings.ReplaceAll(msg, "{"+strconv.Itoa(i)+"}", v.String())
				}
				rt.ctx.Logf("%s%s", level, msg)
			}
			return naV
		}}
	}
	reg("log.info", logf(""))
	reg("log.warning", logf("warning: "))
	reg("log.error", logf("error: "))

	// ---- declarations, inputs, output ----
	noop := &builtin{variadic: true, fn: func(*runtime, *callState, []value, *callExpr) value { return naV }}
	reg("strategy", &builtin{params: p("title", "shorttitle", "overlay", "format", "precision", "scale", "pyramiding",
		"calc_on_order_fills", "calc_on_every_tick", "max_bars_back", "backtest_fill_limits_assumption", "default_qty_type",
		"default_qty_value", "initial_capital", "currency", "slippage", "commission_type", "commission_value",
		"process_orders_on_close", "close_entries_rule", "margin_long", "margin_short", "explicit_plot_zorder",
		"max_lines_count", "max_labels_count", "max_boxes_count", "calc_bars_count", "risk_free_rate", "use_bar_magnifier",
		"fill_orders_on_standard_ohlc", "max_polylines_count", "dynamic_requests", "behind_chart"), required: 1, fn: noop.fn})
	reg("indicator", &builtin{params: p("title", "shorttitle", "overlay", "format", "precision", "scale", "max_bars_back",
		"timeframe", "timeframe_gaps", "explicit_plot_zorder", "max_lines_count", "max_labels_count", "max_boxes_count",
		"calc_bars_count", "max_polylines_count", "dynamic_requests", "behind_chart"), required: 1, fn: noop.fn})
	inputParams := p("defval", "title", "minval", "maxval", "step", "tooltip", "inline", "group", "confirm", "options", "display", "active")
	inputFn := func(_ *runtime, _ *callState, a []value, _ *callExpr) value { return a[0] }
	for _, n := range []string{"input", "input.int", "input.float", "input.bool", "input.string", "input.source", "input.price", "input.color", "input.text_area", "input.time", "input.timeframe"} {
		reg(n, &builtin{params: inputParams, required: 1, fn: inputFn})
	}
	reg("plot", &builtin{params: p("series", "title", "color", "linewidth", "style", "trackprice", "histbase", "offset",
		"join", "editable", "show_last", "display", "format", "precision", "force_overlay"), required: 1,
		fn: func(rt *runtime, _ *callState, a []value, c *callExpr) value {
			if rt.live && rt.ctx != nil {
				v := a[0].f
				if a[0].k == kBool {
					v = a[0].f
				}
				rt.ctx.Plot(rt.prog.plotTitle[c.id], v)
			}
			return naV
		}})
	shapeParams := p("series", "title", "style", "location", "color", "offset", "text", "textcolor", "editable", "size",
		"show_last", "display", "format", "precision", "force_overlay")
	shapeFn := func(rt *runtime, _ *callState, a []value, c *callExpr) value {
		if rt.live && rt.ctx != nil && a[0].truthy() {
			label := ""
			if a[6].k == kStr {
				label = a[6].s
			}
			if label == "" && a[1].k == kStr {
				label = a[1].s
			}
			rt.ctx.Mark(isBuyShape(a[2], a[3]), label)
		}
		return naV
	}
	reg("plotshape", &builtin{params: shapeParams, required: 1, fn: shapeFn})
	charParams := p("series", "title", "char", "location", "color", "offset", "text", "textcolor", "editable", "size",
		"show_last", "display", "format", "precision", "force_overlay")
	reg("plotchar", &builtin{params: charParams, required: 1, fn: func(rt *runtime, st *callState, a []value, c *callExpr) value {
		// reorder to plotshape's layout: style <- missing, location <- a[3], text <- a[6]
		b := make([]value, len(shapeParams))
		for i := range b {
			b[i] = missingV
		}
		b[0], b[1], b[3], b[6] = a[0], a[1], a[3], a[6]
		return shapeFn(rt, st, b, c)
	}})
	for _, n := range []string{"hline", "bgcolor", "barcolor", "fill", "alertcondition", "alert", "plotarrow", "plotcandle", "plotbar"} {
		reg(n, noop)
	}

	// ---- ta ----
	reg("ta.sma", taWin(func(w *ring, n int) float64 {
		s := 0.0
		for i := 0; i < n; i++ {
			s += w.at(i)
		}
		return s / float64(n)
	}))
	reg("ta.wma", taWin(func(w *ring, n int) float64 {
		s, d := 0.0, 0.0
		for i := 0; i < n; i++ {
			wt := float64(n - i)
			s += w.at(i) * wt
			d += wt
		}
		return s / d
	}))
	reg("ta.ema", taSmooth(func(n int) float64 { return 2 / float64(n+1) }))
	reg("ta.rma", taSmooth(func(n int) float64 { return 1 / float64(n) }))
	reg("ta.rsi", &builtin{params: p("source", "length"), required: 2, stateful: true, fn: taRSI})
	reg("ta.atr", &builtin{params: p("length"), required: 1, stateful: true, fn: func(rt *runtime, st *callState, a []value, _ *callExpr) value {
		s := smoothState(st, a[0], 1/a[0].f, 0)
		return num(s.update(trueRange(rt, 0, true)))
	}})
	reg("ta.tr", &builtin{params: p("handle_na"), fn: func(rt *runtime, _ *callState, a []value, _ *callExpr) value {
		return num(trueRange(rt, 0, a[0].truthy()))
	}})
	devFn := func(variance bool) *builtin {
		return &builtin{params: p("source", "length", "biased"), required: 2, stateful: true, fn: func(_ *runtime, st *callState, a []value, _ *callExpr) value {
			w := window(st, a[1])
			w.push(a[0].f)
			if !w.full() {
				return naV
			}
			v := wvariance(w, a[2].missing() || a[2].truthy())
			if variance {
				return num(v)
			}
			return num(math.Sqrt(v))
		}}
	}
	reg("ta.stdev", devFn(false))
	reg("ta.variance", devFn(true))
	extreme := func(hi bool) *builtin {
		return &builtin{params: p("source", "length"), required: 1, stateful: true, fn: func(rt *runtime, st *callState, a []value, _ *callExpr) value {
			src, ln := a[0], a[1]
			if ln.missing() { // ta.highest(length)
				ln = src
				c := rt.cur
				if hi {
					src = num(c.H)
				} else {
					src = num(c.L)
				}
			}
			w := window(st, ln)
			w.push(src.f)
			if !w.full() {
				return naV
			}
			m := w.at(0)
			for i := 1; i < w.n; i++ {
				x := w.at(i)
				if math.IsNaN(m) || (hi && x > m) || (!hi && x < m) {
					m = x
				}
			}
			return num(m)
		}}
	}
	reg("ta.highest", extreme(true))
	reg("ta.lowest", extreme(false))
	crossFn := func(mode int) *builtin {
		return &builtin{params: p("source1", "source2"), required: 2, stateful: true, fn: func(_ *runtime, st *callState, a []value, _ *callExpr) value {
			prev, _ := st.data.([2]float64)
			if st.data == nil {
				prev = [2]float64{math.NaN(), math.NaN()}
			}
			x, y := a[0].f, a[1].f
			st.data = [2]float64{x, y}
			if math.IsNaN(x) || math.IsNaN(y) || math.IsNaN(prev[0]) || math.IsNaN(prev[1]) {
				return falseV
			}
			up := x > y && prev[0] <= prev[1]
			down := x < y && prev[0] >= prev[1]
			switch mode {
			case 1:
				return boolean(up)
			case -1:
				return boolean(down)
			}
			return boolean(up || down)
		}}
	}
	reg("ta.crossover", crossFn(1))
	reg("ta.crossunder", crossFn(-1))
	reg("ta.cross", crossFn(0))
	lagFn := func(f func(x, prev float64) float64) *builtin {
		return &builtin{params: p("source", "length"), required: 1, stateful: true, fn: func(_ *runtime, st *callState, a []value, _ *callExpr) value {
			n := 1
			if !a[1].missing() {
				n = int(a[1].f)
			}
			w := window(st, num(float64(n+1)))
			w.push(a[0].f)
			if !w.full() {
				return naV
			}
			return num(f(w.at(0), w.at(n)))
		}}
	}
	reg("ta.change", lagFn(func(x, p float64) float64 { return x - p }))
	reg("ta.mom", lagFn(func(x, p float64) float64 { return x - p }))
	reg("ta.roc", lagFn(func(x, p float64) float64 {
		if p == 0 {
			return math.NaN()
		}
		return 100 * (x - p) / p
	}))
	reg("ta.macd", &builtin{params: p("source", "fastlen", "slowlen", "siglen"), required: 4, stateful: true, fn: taMACD})
	reg("ta.bb", &builtin{params: p("series", "length", "mult"), required: 3, stateful: true, fn: func(_ *runtime, st *callState, a []value, _ *callExpr) value {
		w := window(st, a[1])
		w.push(a[0].f)
		if !w.full() {
			return value{k: kTuple, t: []value{naV, naV, naV}}
		}
		m := wmean(w)
		d := math.Sqrt(wvariance(w, true))
		return value{k: kTuple, t: []value{num(m), num(m + a[2].f*d), num(m - a[2].f*d)}}
	}})
	reg("ta.vwma", &builtin{params: p("source", "length"), required: 2, stateful: true, fn: func(rt *runtime, st *callState, a []value, _ *callExpr) value {
		type vw struct{ pv, v *ring }
		n := int(a[1].f)
		s, ok := st.data.(*vw)
		if !ok || s.pv.n != n {
			s = &vw{newRing(n), newRing(n)}
			st.data = s
		}
		s.pv.push(a[0].f * rt.cur.V)
		s.v.push(rt.cur.V)
		if !s.pv.full() {
			return naV
		}
		sp, sv := 0.0, 0.0
		for i := 0; i < n; i++ {
			sp += s.pv.at(i)
			sv += s.v.at(i)
		}
		if sv == 0 {
			return naV
		}
		return num(sp / sv)
	}})
	reg("ta.vwap", &builtin{params: p("source", "anchor", "stdev_mult"), required: 1, stateful: true, fn: func(rt *runtime, st *callState, a []value, c *callExpr) value {
		if !a[2].missing() {
			rt.fail(c.P, "ta.vwap stdev bands are not supported yet")
		}
		s, _ := st.data.(*vwapState)
		if s == nil {
			s = &vwapState{day: -1}
			st.data = s
		}
		var reset bool
		if a[1].missing() {
			reset = dayKey(rt.cur.T) != s.day
		} else {
			reset = a[1].truthy() || s.day == -1
		}
		s.update(rt.cur, a[0].f, reset)
		return num(s.val)
	}})
	reg("ta.supertrend", &builtin{params: p("factor", "atrPeriod"), required: 2, stateful: true, fn: taSupertrend})
	reg("ta.stoch", &builtin{params: p("source", "high", "low", "length"), required: 4, stateful: true, fn: func(_ *runtime, st *callState, a []value, _ *callExpr) value {
		type sto struct{ h, l *ring }
		n := int(a[3].f)
		s, ok := st.data.(*sto)
		if !ok || s.h.n != n {
			s = &sto{newRing(n), newRing(n)}
			st.data = s
		}
		s.h.push(a[1].f)
		s.l.push(a[2].f)
		if !s.h.full() {
			return naV
		}
		hh, ll := s.h.at(0), s.l.at(0)
		for i := 1; i < n; i++ {
			hh = math.Max(hh, s.h.at(i))
			ll = math.Min(ll, s.l.at(i))
		}
		if hh == ll {
			return naV
		}
		return num(100 * (a[0].f - ll) / (hh - ll))
	}})
	reg("ta.cci", &builtin{params: p("source", "length"), required: 2, stateful: true, fn: func(_ *runtime, st *callState, a []value, _ *callExpr) value {
		w := window(st, a[1])
		w.push(a[0].f)
		if !w.full() {
			return naV
		}
		m := wmean(w)
		md := 0.0
		for i := 0; i < w.n; i++ {
			md += math.Abs(w.at(i) - m)
		}
		md /= float64(w.n)
		if md == 0 {
			return naV
		}
		return num((a[0].f - m) / (0.015 * md))
	}})
	reg("ta.cum", &builtin{params: p("source"), required: 1, stateful: true, fn: func(_ *runtime, st *callState, a []value, _ *callExpr) value {
		s, _ := st.data.(float64)
		if !a[0].isNa() {
			s += a[0].f
		}
		st.data = s
		return num(s)
	}})
	reg("ta.barssince", &builtin{params: p("condition"), required: 1, stateful: true, fn: func(_ *runtime, st *callState, a []value, _ *callExpr) value {
		n, ok := st.data.(int)
		if a[0].truthy() {
			st.data = 0
			return num(0)
		}
		if !ok {
			return naV
		}
		st.data = n + 1
		return num(float64(n + 1))
	}})
	reg("ta.valuewhen", &builtin{params: p("condition", "source", "occurrence"), required: 3, stateful: true, fn: func(_ *runtime, st *callState, a []value, _ *callExpr) value {
		vals, _ := st.data.([]float64)
		if a[0].truthy() {
			vals = append(vals, a[1].f)
			if len(vals) > 1000 {
				vals = vals[len(vals)-1000:]
			}
			st.data = vals
		}
		k := int(a[2].f)
		if k < 0 || k >= len(vals) {
			return naV
		}
		return num(vals[len(vals)-1-k])
	}})
	trend := func(up bool) *builtin {
		return &builtin{params: p("source", "length"), required: 2, stateful: true, fn: func(_ *runtime, st *callState, a []value, _ *callExpr) value {
			n := int(a[1].f)
			w := window(st, num(float64(n+1)))
			w.push(a[0].f)
			if !w.full() {
				return falseV
			}
			for i := 0; i < n; i++ {
				x, y := w.at(i), w.at(i+1)
				if math.IsNaN(x) || math.IsNaN(y) || (up && x <= y) || (!up && x >= y) {
					return falseV
				}
			}
			return trueV
		}}
	}
	reg("ta.rising", trend(true))
	reg("ta.falling", trend(false))

	// ---- strategy.* ----
	reg("strategy.entry", &builtin{params: p("id", "direction", "qty", "limit", "stop", "oca_name", "oca_type", "comment",
		"alert_message", "disable_alert", "when"), required: 2, fn: func(rt *runtime, _ *callState, a []value, c *callExpr) value {
		rt.entry(c, a, true)
		return naV
	}})
	reg("strategy.order", &builtin{params: p("id", "direction", "qty", "limit", "stop", "oca_name", "oca_type", "comment",
		"alert_message", "disable_alert", "when"), required: 2, fn: func(rt *runtime, _ *callState, a []value, c *callExpr) value {
		rt.entry(c, a, false)
		return naV
	}})
	reg("strategy.close", &builtin{params: p("id", "comment", "qty", "qty_percent", "alert_message", "immediately",
		"disable_alert", "when"), required: 1, fn: func(rt *runtime, _ *callState, a []value, c *callExpr) value {
		rt.closeEntry(c, a)
		return naV
	}})
	reg("strategy.close_all", &builtin{params: p("comment", "alert_message", "immediately", "disable_alert", "when"),
		fn: func(rt *runtime, _ *callState, a []value, c *callExpr) value {
			if !rt.live || rt.ctx == nil || (!a[4].missing() && !a[4].truthy()) || rt.expPos == 0 {
				return naV
			}
			if err := rt.ctx.CloseAll(); err != nil {
				rt.fail(c.P, "%v", err)
			}
			rt.expPos = 0
			return naV
		}})
	reg("strategy.exit", &builtin{params: p("id", "from_entry", "qty", "qty_percent", "profit", "limit", "loss", "stop",
		"trail_price", "trail_points", "trail_offset", "oca_name", "comment", "comment_profit", "comment_loss",
		"comment_trailing", "alert_message", "alert_profit", "alert_loss", "alert_trailing", "disable_alert", "when"),
		required: 1, fn: func(rt *runtime, _ *callState, a []value, c *callExpr) value {
			rt.exit(c, a)
			return naV
		}})
	reg("strategy.cancel", &builtin{params: p("id", "when"), required: 1, fn: func(rt *runtime, _ *callState, a []value, c *callExpr) value {
		if !a[1].missing() && !a[1].truthy() {
			return naV
		}
		if rt.live {
			delete(rt.pending, a[0].s)
		}
		rt.order(c.P, strategy.Order{Kind: strategy.Cancel, Tag: a[0].s})
		return naV
	}})
	reg("strategy.cancel_all", &builtin{params: p("when"), fn: func(rt *runtime, _ *callState, a []value, c *callExpr) value {
		if !a[0].missing() && !a[0].truthy() {
			return naV
		}
		if rt.live {
			rt.pending = map[string]pendingEntry{}
		}
		rt.order(c.P, strategy.Order{Kind: strategy.Cancel})
		return naV
	}})
	registerExtras(reg)
	registerTrades(reg)
	initConstNames()
}

// ---- helpers ----

func roundHalfUp(x float64) float64 { return math.Floor(x + 0.5) }

func hex2(v int) string {
	const h = "0123456789ABCDEF"
	return string([]byte{h[v>>4], h[v&15]})
}

func isBuyShape(style, loc value) bool {
	s := style.s + " " + loc.s
	switch {
	case strings.Contains(s, "down"), strings.Contains(s, "abovebar"), strings.Contains(s, "location.top"):
		return false
	}
	return true
}

func tvPeriod(iv domain.Interval) string {
	switch iv {
	case domain.I1d:
		return "D"
	case domain.I3d:
		return "3D"
	case domain.I1w:
		return "W"
	case domain.I1M:
		return "M"
	}
	return strconv.Itoa(int(iv.Duration().Minutes()))
}

func tvMultiplier(iv domain.Interval) float64 {
	switch iv {
	case domain.I1d, domain.I1w, domain.I1M:
		return 1
	case domain.I3d:
		return 3
	}
	return iv.Duration().Minutes()
}

func dayKey(ms int64) int64 { return ms / 86_400_000 }

// trueRange is ta.tr; handleNA uses high-low on the first bar.
func trueRange(rt *runtime, back int, handleNA bool) float64 {
	c, ok := rt.candleAt(back)
	if !ok {
		return math.NaN()
	}
	p, ok := rt.candleAt(back + 1)
	if !ok {
		if handleNA {
			return c.H - c.L
		}
		return math.NaN()
	}
	return math.Max(c.H-c.L, math.Max(math.Abs(c.H-p.C), math.Abs(c.L-p.C)))
}

// window returns the call site's ring sized to length (rebuilt if the
// length changes).
func window(st *callState, length value) *ring {
	n := int(length.f)
	if n < 1 || math.IsNaN(length.f) {
		n = 1
	}
	w, ok := st.data.(*ring)
	if !ok || w.n != n {
		w = newRing(n)
		st.data = w
	}
	return w
}

func wmean(w *ring) float64 {
	s := 0.0
	for i := 0; i < w.n; i++ {
		s += w.at(i)
	}
	return s / float64(w.n)
}

func wvariance(w *ring, biased bool) float64 {
	m := wmean(w)
	s := 0.0
	for i := 0; i < w.n; i++ {
		d := w.at(i) - m
		s += d * d
	}
	if biased {
		return s / float64(w.n)
	}
	if w.n < 2 {
		return math.NaN()
	}
	return s / float64(w.n-1)
}

// taWin builds a window indicator: na until full or when any value is na.
func taWin(f func(w *ring, n int) float64) *builtin {
	return &builtin{params: p("source", "length"), required: 2, stateful: true, fn: func(_ *runtime, st *callState, a []value, _ *callExpr) value {
		w := window(st, a[1])
		w.push(a[0].f)
		if !w.full() {
			return naV
		}
		for i := 0; i < w.n; i++ {
			if math.IsNaN(w.at(i)) {
				return naV
			}
		}
		return num(f(w, w.n))
	}}
}

// smoother is an SMA-seeded exponential smoother (ta.ema / ta.rma).
type smoother struct {
	n     int
	alpha float64
	cnt   int
	sum   float64
	val   float64
}

func newSmoother(n int, alpha float64) *smoother {
	return &smoother{n: n, alpha: alpha, val: math.NaN()}
}

// update feeds x; na inputs return na and leave the state unchanged.
func (s *smoother) update(x float64) float64 {
	if math.IsNaN(x) {
		return math.NaN()
	}
	if s.cnt < s.n {
		s.cnt++
		s.sum += x
		if s.cnt == s.n {
			s.val = s.sum / float64(s.n)
		}
		return s.val
	}
	s.val = s.alpha*x + (1-s.alpha)*s.val
	return s.val
}

// smoothState returns the call site's smoother (slot k of a multi-smoother
// state is not needed here; one per call site).
func smoothState(st *callState, length value, alpha float64, _ int) *smoother {
	n := int(length.f)
	if n < 1 || math.IsNaN(length.f) {
		n = 1
		alpha = 1
	}
	s, ok := st.data.(*smoother)
	if !ok || s.n != n {
		s = newSmoother(n, alpha)
		st.data = s
	}
	return s
}

func taSmooth(alpha func(n int) float64) *builtin {
	return &builtin{params: p("source", "length"), required: 2, stateful: true, fn: func(_ *runtime, st *callState, a []value, _ *callExpr) value {
		n := int(a[1].f)
		s := smoothState(st, a[1], alpha(max(n, 1)), 0)
		return num(s.update(a[0].f))
	}}
}

type rsiState struct {
	prev   float64
	up, dn *smoother
}

func taRSI(_ *runtime, st *callState, a []value, _ *callExpr) value {
	n := int(a[1].f)
	if n < 1 {
		n = 1
	}
	s, ok := st.data.(*rsiState)
	if !ok || s.up.n != n {
		s = &rsiState{prev: math.NaN(), up: newSmoother(n, 1/float64(n)), dn: newSmoother(n, 1/float64(n))}
		st.data = s
	}
	x := a[0].f
	ch := x - s.prev
	if !math.IsNaN(x) {
		s.prev = x
	}
	if math.IsNaN(ch) {
		return naV
	}
	u := s.up.update(math.Max(ch, 0))
	d := s.dn.update(math.Max(-ch, 0))
	if math.IsNaN(u) || math.IsNaN(d) {
		return naV
	}
	return num(rsiFrom(u, d))
}

func rsiFrom(u, d float64) float64 {
	switch {
	case d == 0:
		return 100
	case u == 0:
		return 0
	}
	return 100 - 100/(1+u/d)
}

type macdState struct{ fast, slow, sig *smoother }

func taMACD(_ *runtime, st *callState, a []value, _ *callExpr) value {
	f, sl, sg := max(int(a[1].f), 1), max(int(a[2].f), 1), max(int(a[3].f), 1)
	s, ok := st.data.(*macdState)
	if !ok || s.fast.n != f || s.slow.n != sl || s.sig.n != sg {
		s = &macdState{newSmoother(f, 2/float64(f+1)), newSmoother(sl, 2/float64(sl+1)), newSmoother(sg, 2/float64(sg+1))}
		st.data = s
	}
	m := s.fast.update(a[0].f) - s.slow.update(a[0].f)
	sig := s.sig.update(m)
	return value{k: kTuple, t: []value{num(m), num(sig), num(m - sig)}}
}

type vwapState struct {
	day     int64
	pv, vol float64
	val     float64
}

func (s *vwapState) update(c domain.Candle, src float64, reset bool) {
	if reset {
		s.pv, s.vol = 0, 0
	}
	s.day = dayKey(c.T)
	s.pv += src * c.V
	s.vol += c.V
	if s.vol > 0 {
		s.val = s.pv / s.vol
	} else {
		s.val = math.NaN()
	}
}

type stState struct {
	atr                        *smoother
	prevLower, prevUpper       float64
	prevST, prevClose, prevATR float64
}

// taSupertrend follows TradingView's published reference implementation:
// direction -1 = uptrend (line below price), 1 = downtrend.
func taSupertrend(rt *runtime, st *callState, a []value, _ *callExpr) value {
	n := max(int(a[1].f), 1)
	s, ok := st.data.(*stState)
	if !ok || s.atr.n != n {
		nan := math.NaN()
		s = &stState{atr: newSmoother(n, 1/float64(n)), prevLower: nan, prevUpper: nan, prevST: nan, prevClose: nan, prevATR: nan}
		st.data = s
	}
	c := rt.cur
	atr := s.atr.update(trueRange(rt, 0, true))
	src := (c.H + c.L) / 2
	upper := src + a[0].f*atr
	lower := src - a[0].f*atr
	nz := func(x float64) float64 {
		if math.IsNaN(x) {
			return 0
		}
		return x
	}
	pl, pu := nz(s.prevLower), nz(s.prevUpper)
	if !(lower > pl || s.prevClose < pl) {
		lower = pl
	}
	if !(upper < pu || s.prevClose > pu) {
		upper = pu
	}
	var dir float64
	switch {
	case math.IsNaN(s.prevATR):
		dir = 1
	case s.prevST == s.prevUpper:
		if c.C > upper {
			dir = -1
		} else {
			dir = 1
		}
	default:
		if c.C < lower {
			dir = 1
		} else {
			dir = -1
		}
	}
	stv := upper
	if dir == -1 {
		stv = lower
	}
	s.prevLower, s.prevUpper, s.prevST, s.prevClose, s.prevATR = lower, upper, stv, c.C, atr
	return value{k: kTuple, t: []value{num(stv), num(dir)}}
}

// ---- order helpers ----

func (rt *runtime) whenFalse(a []value, i int) bool { return !a[i].missing() && !a[i].truthy() }

// entry implements strategy.entry (reversing) and strategy.order (plain).
func (rt *runtime) entry(c *callExpr, a []value, reversing bool) {
	if !rt.live || rt.ctx == nil || rt.whenFalse(a, 10) {
		return
	}
	id := a[0].s
	var long bool
	switch a[1].s {
	case "long":
		long = true
	case "short":
	default:
		rt.fail(c.P, "direction must be strategy.long or strategy.short")
	}
	size := rt.sizeFor(a[2])
	if size <= 0 || math.IsNaN(size) {
		return
	}
	exp := rt.expPos
	same := (exp > 0 && long) || (exp < 0 && !long)
	if reversing && same && rt.entries >= max(rt.prog.decl.pyramiding, 1) {
		return
	}
	total := size
	if reversing && exp != 0 && !same {
		total += math.Abs(exp)
	}
	side := domain.Sell
	if long {
		side = domain.Buy
	}
	o := strategy.Order{Side: side, Size: total, Kind: strategy.Market, Tag: id}
	lim, stp := a[3], a[4]
	switch {
	case !lim.missing() && !lim.isNa() && !stp.missing() && !stp.isNa():
		rt.fail(c.P, "stop-limit entries are not supported yet; use limit or stop")
	case !lim.missing() && !lim.isNa():
		o.Kind, o.Px = strategy.Limit, lim.f
	case !stp.missing() && !stp.isNa():
		o.Kind, o.Px = strategy.Stop, stp.f
	}
	rt.order(c.P, o)
	if o.Kind == strategy.Market {
		delete(rt.pending, id)
		if same {
			rt.entries++
		} else {
			rt.entries = 1
			rt.posEntry = id
		}
		return
	}
	rt.pseq++
	pe := pendingEntry{long: long, seq: rt.pseq, entry: reversing}
	if a[5].k == kStr && a[5].s != "" {
		pe.oca = a[5].s
		pe.ocaType = "strategy.oca.none"
		if a[6].k == kStr {
			pe.ocaType = "strategy." + strings.TrimPrefix(a[6].s, "strategy.")
		}
	}
	rt.pending[id] = pe
}

// restingLister is implemented by the sim engine behind backtest and paper.
type restingLister interface{ Pending() []sim.Resting }

// restingEntries returns the tags of this coin's resting non-reduce-only
// limit/stop orders, when the Context can list them.
func (rt *runtime) restingEntries() (map[string]bool, bool) {
	rl, ok := rt.ctx.(restingLister)
	if !ok {
		return nil, false
	}
	out := map[string]bool{}
	for _, r := range rl.Pending() {
		if r.Coin == rt.coin && !r.ReduceOnly && (r.Kind == strategy.Limit || r.Kind == strategy.Stop) {
			out[r.Tag] = true
		}
	}
	return out, true
}

// settleEntries runs at the start of each live bar. Resting entries the
// broker no longer holds have filled: OCA-cancel groups drop their
// siblings, and when the position opened or flipped (opened, in direction
// long) the newest filled entry in that direction becomes the position's
// entry id (spec A1: from fills, not placement). Without a broker that can
// list resting orders, the newest pending entry in that direction is
// assumed to be the one that filled.
func (rt *runtime) settleEntries(opened, long bool) {
	if len(rt.pending) == 0 {
		return
	}
	resting, known := rt.restingEntries()
	var filled []string
	for id := range rt.pending {
		if known && !resting[id] {
			filled = append(filled, id)
		}
	}
	if opened {
		cands := filled
		if !known {
			for id := range rt.pending {
				cands = append(cands, id)
			}
		}
		best, bestSeq := "", -1
		for _, id := range cands {
			if pe := rt.pending[id]; pe.long == long && pe.seq > bestSeq {
				best, bestSeq = id, pe.seq
			}
		}
		if best != "" {
			rt.posEntry, rt.entries = best, 1
			if !known {
				filled = append(filled, best)
			}
		}
	}
	sort.Slice(filled, func(i, j int) bool { return rt.pending[filled[i]].seq < rt.pending[filled[j]].seq })
	for _, id := range filled {
		pe, ok := rt.pending[id]
		if !ok {
			continue
		}
		delete(rt.pending, id)
		if pe.oca == "" || pe.ocaType != "strategy.oca.cancel" {
			continue
		}
		var sibs []string
		for sid, o := range rt.pending {
			if o.oca == pe.oca {
				sibs = append(sibs, sid)
			}
		}
		sort.Strings(sibs)
		for _, sid := range sibs {
			delete(rt.pending, sid)
			_ = rt.ctx.Order(strategy.Order{Coin: rt.coin, Kind: strategy.Cancel, Tag: sid})
		}
	}
}

func (rt *runtime) closeEntry(c *callExpr, a []value) {
	if !rt.live || rt.ctx == nil || rt.whenFalse(a, 7) || rt.expPos == 0 {
		return
	}
	id := a[0].s
	size := 0.0
	if rt.posEntry != "" && rt.posEntry != id {
		// Not the position's first entry: close just the open trades with
		// this id (a pyramided entry), known from fills.
		q := rt.openQty(id)
		if q <= 0 {
			return
		}
		size = math.Min(q, math.Abs(rt.expPos))
	}
	side := domain.Buy
	if rt.expPos > 0 {
		side = domain.Sell
	}
	switch {
	case !a[2].missing() && !a[2].isNa():
		size = math.Min(a[2].f, math.Abs(rt.expPos))
	case !a[3].missing() && !a[3].isNa():
		size = math.Abs(rt.expPos) * a[3].f / 100
	}
	tag := "Close " + id
	if a[1].k == kStr && a[1].s != "" {
		tag = a[1].s
	}
	rt.order(c.P, strategy.Order{Side: side, Size: size, Kind: strategy.Market, ReduceOnly: true, Tag: tag})
	if rt.expPos == 0 {
		rt.entries = 0
	}
}

func (rt *runtime) exit(c *callExpr, a []value) {
	if !rt.live || rt.ctx == nil || rt.whenFalse(a, 21) || rt.expPos == 0 {
		return
	}
	req := exitReq{id: a[0].s, pos: c.P, profit: math.NaN(), loss: math.NaN(), limit: math.NaN(), stop: math.NaN(),
		qty: math.NaN(), qtyPct: math.NaN(), trailPrice: math.NaN(), trailPoints: math.NaN(), trailOffset: math.NaN()}
	if from := a[1]; from.k == kStr && from.s != "" && rt.posEntry != "" && from.s != rt.posEntry && !rt.hasOpenTrade(from.s) {
		return
	}
	set := func(dst *float64, v value) {
		if !v.missing() && !v.isNa() {
			*dst = v.f
		}
	}
	set(&req.qty, a[2])
	set(&req.qtyPct, a[3])
	set(&req.profit, a[4])
	set(&req.limit, a[5])
	set(&req.loss, a[6])
	set(&req.stop, a[7])
	set(&req.trailPrice, a[8])
	set(&req.trailPoints, a[9])
	set(&req.trailOffset, a[10])
	trailing := !math.IsNaN(req.trailOffset) && (!math.IsNaN(req.trailPrice) || !math.IsNaN(req.trailPoints))
	if !trailing && (!math.IsNaN(req.trailPrice) || !math.IsNaN(req.trailPoints)) {
		rt.fail(c.P, "strategy.exit trailing stop needs trail_offset as well as trail_price or trail_points")
	}
	if trailing {
		rt.trails[req.id] = req
	} else {
		delete(rt.trails, req.id)
	}
	if math.IsNaN(req.limit) && math.IsNaN(req.stop) && math.IsNaN(req.profit) && math.IsNaN(req.loss) && !trailing {
		return
	}
	estimated := rt.placeExit(req)
	// Tick-based exits placed before the entry filled used the close as the
	// entry estimate; re-anchor them to the real fill on the next bar.
	if estimated && (!math.IsNaN(req.profit) || !math.IsNaN(req.loss)) {
		rt.reanchor = append(rt.reanchor, req)
	}
}

// exitReq is one strategy.exit call.
type exitReq struct {
	id                        string
	pos                       Pos
	profit, loss, limit, stop float64
	qty, qtyPct               float64
	// trailing (B4): activation at trailPrice or entry +/- trailPoints
	// ticks, then a stop trailOffset ticks behind the best price.
	trailPrice, trailPoints, trailOffset float64
}

func (r exitReq) trailing() bool {
	return !math.IsNaN(r.trailOffset) && (!math.IsNaN(r.trailPrice) || !math.IsNaN(r.trailPoints))
}

// placeExit (re)places the bracket for req. It reports whether the
// reference entry price was estimated (position not filled yet).
func (rt *runtime) placeExit(req exitReq) bool {
	long := rt.expPos > 0
	ref, estimated := rt.entryRef()
	dir := 1.0
	if !long {
		dir = -1
	}
	lim, stp := req.limit, req.stop
	if math.IsNaN(lim) && !math.IsNaN(req.profit) {
		lim = ref + dir*req.profit*rt.mintick
	}
	if math.IsNaN(stp) && !math.IsNaN(req.loss) {
		stp = ref - dir*req.loss*rt.mintick
	}
	if req.trailing() && !estimated && !math.IsNaN(rt.bestHi) {
		act := req.trailPrice
		if math.IsNaN(act) {
			act = ref + dir*req.trailPoints*rt.mintick
		}
		off := req.trailOffset * rt.mintick
		if long && rt.bestHi >= act {
			if t := rt.bestHi - off; math.IsNaN(stp) || t > stp {
				stp = t
			}
		}
		if !long && rt.bestLo <= act {
			if t := rt.bestLo + off; math.IsNaN(stp) || t < stp {
				stp = t
			}
		}
	}
	size := 0.0
	switch {
	case !math.IsNaN(req.qty):
		size = req.qty
	case !math.IsNaN(req.qtyPct):
		size = math.Abs(rt.expPos) * req.qtyPct / 100
	}
	side := domain.Sell
	if !long {
		side = domain.Buy
	}
	rt.order(req.pos, strategy.Order{Kind: strategy.Cancel, Tag: req.id})
	if !math.IsNaN(lim) {
		rt.order(req.pos, strategy.Order{Side: side, Size: size, Kind: strategy.Limit, Px: lim, ReduceOnly: true, Tag: req.id})
	}
	if !math.IsNaN(stp) {
		rt.order(req.pos, strategy.Order{Side: side, Size: size, Kind: strategy.Stop, Px: stp, ReduceOnly: true, Tag: req.id})
	}
	return estimated
}

// updateTrails re-places every active trailing exit at the start of a live
// bar, after the bar's high/low moved the best price. This is the
// client-side trailing stop Hyperliquid needs (its trailingStop action is
// unverified): the stop moves once per closed bar, not intrabar.
func (rt *runtime) updateTrails() {
	if rt.expPos == 0 || len(rt.trails) == 0 {
		return
	}
	ids := make([]string, 0, len(rt.trails))
	for id := range rt.trails {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		rt.placeExit(rt.trails[id])
	}
}

// reanchorExits re-places tick-based exits against the actual entry price
// once the entry has filled. Called at the start of each live bar.
func (rt *runtime) reanchorExits() {
	if len(rt.reanchor) == 0 {
		return
	}
	reqs := rt.reanchor
	rt.reanchor = nil
	if rt.expPos == 0 {
		return // the bracket already closed the position (or it never opened)
	}
	for _, r := range reqs {
		rt.placeExit(r)
	}
}
