package pine

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/sim"
)

// This file keeps the strategy's trade list from FILLS (spec A1, B6): the
// sim engine journals every fill with the tag of the order that caused it,
// and at the start of each live bar the runtime books the new fills into
// open trades (FIFO, like TradingView's broker emulator) and closed trades.
// strategy.exit(from_entry=...) and strategy.close(id) filter the open
// trades, and strategy.closedtrades / wintrades / grossprofit / ... and the
// strategy.opentrades.* / strategy.closedtrades.* functions read them.

// openTrade is one open entry (a pyramided position has several).
type openTrade struct {
	id       string
	long     bool
	qty      float64
	px       float64
	t        int64
	bar      int
	fee      float64 // entry fee still attributed to the open qty
	maxRunup float64
}

// closedTrade is one closed (part of an) entry.
type closedTrade struct {
	openTrade
	exitID  string
	exitPx  float64
	exitT   int64
	exitBar int
	profit  float64 // net of entry and exit fees
}

// trades is the runtime's trade book.
type trades struct {
	open    []openTrade
	closed  []closedTrade
	seq     uint64
	enabled bool // the Context journals fills

	grossProfit, grossLoss float64
	wins, losses, evens    int
	peak, maxDD            float64
}

// fillJournal is implemented by the sim engine.
type fillJournal interface {
	FillsAfter(seq uint64) []sim.TaggedFill
}

// syncTrades books the fills since the last bar. Called at the start of
// each live bar.
func (rt *runtime) syncTrades() {
	j, ok := rt.ctx.(fillJournal)
	if !ok {
		return
	}
	tb := &rt.trades
	tb.enabled = true
	for _, f := range j.FillsAfter(tb.seq) {
		tb.seq = f.Seq
		if f.Coin != rt.coin {
			continue
		}
		rt.bookFill(f)
	}
	if rt.ctx != nil {
		eq := rt.ctx.Equity()
		if eq > tb.peak || tb.peak == 0 {
			tb.peak = eq
		}
		tb.maxDD = math.Max(tb.maxDD, tb.peak-eq)
	}
}

// barOf maps a fill time to a bar_index (fills happen at a bar's open).
func (rt *runtime) barOf(t int64) int {
	dur := rt.iv.Duration().Milliseconds()
	if dur <= 0 || t >= rt.cur.T {
		return rt.bar
	}
	return rt.bar - int((rt.cur.T-t+dur-1)/dur)
}

func (rt *runtime) bookFill(f sim.TaggedFill) {
	tb := &rt.trades
	long := f.Side == domain.Buy
	left := f.Sz
	feePer := 0.0
	if f.Sz > 0 {
		feePer = f.Fee / f.Sz
	}
	bar := rt.barOf(f.Time)
	// Close opposite trades first, oldest first.
	for left > 1e-12 && len(tb.open) > 0 && tb.open[0].long != long {
		ot := &tb.open[0]
		q := math.Min(ot.qty, left)
		dir := 1.0
		if !ot.long {
			dir = -1
		}
		entryFee := ot.fee * q / ot.qty
		ct := closedTrade{openTrade: *ot, exitID: f.Tag, exitPx: f.Px, exitT: f.Time, exitBar: bar}
		ct.qty = q
		ct.fee = entryFee
		ct.profit = dir*q*(f.Px-ot.px) - entryFee - feePer*q
		tb.closed = append(tb.closed, ct)
		switch {
		case ct.profit > 0:
			tb.wins++
			tb.grossProfit += ct.profit
		case ct.profit < 0:
			tb.losses++
			tb.grossLoss += -ct.profit
		default:
			tb.evens++
		}
		ot.fee -= entryFee
		ot.qty -= q
		left -= q
		if ot.qty <= 1e-12 {
			tb.open = tb.open[1:]
		}
	}
	if len(tb.closed) > 5000 { // bound memory on very long paper runs
		tb.closed = tb.closed[len(tb.closed)-5000:]
	}
	if left > 1e-12 && !f.Liq && f.Tag != "liquidation" {
		id := f.Tag
		if id == "" {
			id = "entry"
		}
		tb.open = append(tb.open, openTrade{id: id, long: long, qty: left, px: f.Px, t: f.Time, bar: bar, fee: feePer * left})
	}
}

// hasOpenTrade reports whether an open trade has the entry id and the
// direction of the expected position (a trade about to be closed by a
// queued reversal does not count).
func (rt *runtime) hasOpenTrade(id string) bool {
	return rt.openQty(id) > 0
}

// openQty is the open quantity of the trades with entry id in the
// direction of the expected position.
func (rt *runtime) openQty(id string) float64 {
	if rt.expPos == 0 {
		return 0
	}
	long := rt.expPos > 0
	q := 0.0
	for _, t := range rt.trades.open {
		if t.id == id && t.long == long {
			q += t.qty
		}
	}
	return q
}

func (rt *runtime) openTradeAt(i value) (openTrade, bool) {
	k := int(i.f)
	if i.isNa() || k < 0 || k >= len(rt.trades.open) {
		return openTrade{}, false
	}
	return rt.trades.open[k], true
}

func (rt *runtime) closedTradeAt(i value) (closedTrade, bool) {
	k := int(i.f)
	if i.isNa() || k < 0 || k >= len(rt.trades.closed) {
		return closedTrade{}, false
	}
	return rt.trades.closed[k], true
}

// registerTrades adds B6 (trade statistics), B7 (casts, str.format), B3
// (more ta.*) and the ta.obv variable.
func registerTrades(reg func(string, *builtin)) {
	v := func(f func(rt *runtime) value) *builtinVar {
		return &builtinVar{get: func(rt *runtime, _ int) value { return f(rt) }}
	}
	builtinVars["strategy.closedtrades"] = v(func(rt *runtime) value { return num(float64(len(rt.trades.closed))) })
	builtinVars["strategy.wintrades"] = v(func(rt *runtime) value { return num(float64(rt.trades.wins)) })
	builtinVars["strategy.losstrades"] = v(func(rt *runtime) value { return num(float64(rt.trades.losses)) })
	builtinVars["strategy.eventrades"] = v(func(rt *runtime) value { return num(float64(rt.trades.evens)) })
	builtinVars["strategy.grossprofit"] = v(func(rt *runtime) value { return num(rt.trades.grossProfit) })
	builtinVars["strategy.grossloss"] = v(func(rt *runtime) value { return num(rt.trades.grossLoss) })
	builtinVars["strategy.max_drawdown"] = v(func(rt *runtime) value { return num(rt.trades.maxDD) })
	builtinVars["strategy.position_entry_name"] = v(func(rt *runtime) value {
		if len(rt.trades.open) > 0 {
			return str(rt.trades.open[0].id)
		}
		return str(rt.posEntry)
	})
	builtinVars["ta.obv"] = &builtinVar{get: func(rt *runtime, back int) value { return rt.obvHist.get(rt.bar - back) }}

	oneIdx := func(name string, f func(t openTrade) value) {
		reg("strategy.opentrades."+name, &builtin{params: p("trade_num"), required: 1, fn: func(rt *runtime, _ *callState, a []value, _ *callExpr) value {
			t, ok := rt.openTradeAt(a[0])
			if !ok {
				return naV
			}
			return f(t)
		}})
	}
	oneIdx("entry_price", func(t openTrade) value { return num(t.px) })
	oneIdx("entry_id", func(t openTrade) value { return str(t.id) })
	oneIdx("entry_time", func(t openTrade) value { return num(float64(t.t)) })
	oneIdx("entry_bar_index", func(t openTrade) value { return num(float64(t.bar)) })
	oneIdx("size", func(t openTrade) value {
		if t.long {
			return num(t.qty)
		}
		return num(-t.qty)
	})
	oneIdx("commission", func(t openTrade) value { return num(t.fee) })
	reg("strategy.opentrades.profit", &builtin{params: p("trade_num"), required: 1, fn: func(rt *runtime, _ *callState, a []value, _ *callExpr) value {
		t, ok := rt.openTradeAt(a[0])
		if !ok {
			return naV
		}
		dir := 1.0
		if !t.long {
			dir = -1
		}
		return num(dir*t.qty*(rt.cur.C-t.px) - t.fee)
	}})
	closedIdx := func(name string, f func(t closedTrade) value) {
		reg("strategy.closedtrades."+name, &builtin{params: p("trade_num"), required: 1, fn: func(rt *runtime, _ *callState, a []value, _ *callExpr) value {
			t, ok := rt.closedTradeAt(a[0])
			if !ok {
				return naV
			}
			return f(t)
		}})
	}
	closedIdx("entry_price", func(t closedTrade) value { return num(t.px) })
	closedIdx("exit_price", func(t closedTrade) value { return num(t.exitPx) })
	closedIdx("entry_id", func(t closedTrade) value { return str(t.id) })
	closedIdx("exit_id", func(t closedTrade) value { return str(t.exitID) })
	closedIdx("entry_time", func(t closedTrade) value { return num(float64(t.t)) })
	closedIdx("exit_time", func(t closedTrade) value { return num(float64(t.exitT)) })
	closedIdx("entry_bar_index", func(t closedTrade) value { return num(float64(t.bar)) })
	closedIdx("exit_bar_index", func(t closedTrade) value { return num(float64(t.exitBar)) })
	closedIdx("profit", func(t closedTrade) value { return num(t.profit) })
	closedIdx("size", func(t closedTrade) value {
		if t.long {
			return num(t.qty)
		}
		return num(-t.qty)
	})

	// ---- B7: casts and str.format ----
	reg("int", &builtin{params: p("x"), required: 1, fn: func(_ *runtime, _ *callState, a []value, _ *callExpr) value {
		if a[0].isNa() || a[0].k != kNum {
			return naV
		}
		return num(math.Trunc(a[0].f))
	}})
	reg("float", &builtin{params: p("x"), required: 1, fn: func(_ *runtime, _ *callState, a []value, _ *callExpr) value {
		if a[0].k == kBool {
			if a[0].truthy() {
				return num(1)
			}
			return num(0)
		}
		if a[0].isNa() {
			return num(math.NaN())
		}
		return a[0]
	}})
	reg("bool", &builtin{params: p("x"), required: 1, fn: func(_ *runtime, _ *callState, a []value, _ *callExpr) value {
		return boolean(a[0].truthy())
	}})
	reg("str.format", &builtin{variadic: true, required: 1, fn: func(rt *runtime, _ *callState, a []value, c *callExpr) value {
		if a[0].k != kStr {
			rt.fail(c.P, "str.format needs a format string")
		}
		return str(formatPine(a[0].s, a[1:]))
	}})

	// ---- B3: more ta.* ----
	registerTA(reg)
}

var formatArgRE = regexp.MustCompile(`\{(\d+)(?:,\s*number\s*(?:,\s*([^}]*))?)?\}`)

// formatPine is str.format's subset: {0}, {1,number,#.##} and
// {0,number,percent}. Values without a pattern print like str.tostring.
func formatPine(f string, args []value) string {
	return formatArgRE.ReplaceAllStringFunc(f, func(m string) string {
		sub := formatArgRE.FindStringSubmatch(m)
		i, _ := strconv.Atoi(sub[1])
		if i >= len(args) {
			return m
		}
		v := args[i]
		pat := strings.TrimSpace(sub[2])
		if v.k != kNum || v.isNa() {
			return v.String()
		}
		switch {
		case pat == "percent":
			return strconv.FormatFloat(v.f*100, 'f', 0, 64) + "%"
		case pat == "integer":
			return strconv.FormatFloat(math.Round(v.f), 'f', 0, 64)
		case strings.Contains(pat, "."):
			d := len(pat) - strings.Index(pat, ".") - 1
			return strconv.FormatFloat(v.f, 'f', d, 64)
		case pat != "":
			return strconv.FormatFloat(math.Round(v.f), 'f', 0, 64)
		}
		return v.String()
	})
}

// fmtTrade is a debug string for a trade (used in tests' failure output).
func (t closedTrade) String() string {
	return fmt.Sprintf("%s %v %.6g@%.6g -> %s @%.6g pnl %.4f", t.id, t.long, t.qty, t.px, t.exitID, t.exitPx, t.profit)
}
