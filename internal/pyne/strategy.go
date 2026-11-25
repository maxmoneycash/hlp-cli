package pyne

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/strategy"
)

// This file adapts PYNE to strategy.Strategy, the interface hlp's builtin
// Pine engine returns from pine.Compile, so backtest.Run, paper sessions
// and the arena can drive a PYNE-evaluated script unchanged.
//
// Model ("position follower"): PYNE is a batch engine, so on every closed
// bar the adapter re-runs the script over the trailing Window bars plus one
// phantom bar (open = high = low = close = the last close, one interval
// later). PYNE's broker fills the market orders the script placed on the
// real last bar at the phantom open, so the position PYNE reports on the
// phantom bar is the position the script wants for the next bar. The
// adapter sends hlp a market order for the difference; hlp's own broker
// fills it at the next bar open with hlp's fees and slippage. No look-ahead:
// the phantom bar only repeats the last close.
//
// Consequences, documented for the integrator:
//   - stop / limit / trailing exits that would fill INSIDE a future bar are
//     not resting orders on hlp's side; they become a market order on the
//     bar after PYNE's emulator sees them trigger (one bar late, at the open).
//   - sizes are PYNE's (from the script's initial_capital / qty settings),
//     like the builtin engine; set Follower.ScaleToEquity to rescale by
//     hlp equity / PYNE equity instead.
//   - scripts that fill orders at the close of the bar that placed them
//     (process_orders_on_close=true, calc_on_order_fills=true or
//     close/close_all(immediately=true)) would also fill orders placed ON
//     the phantom bar, a made-up bar. For them the target is the position
//     after the real last bar's close fills (Position[len-2]), so the
//     phantom bar can never create a signal.
//   - each bar costs one pyne process (~1.5 s parse on hoox-pyne 0.7.0),
//     fine for paper sessions; for backtests prefer Backtest (one run).

// Follower is a strategy.Strategy driven by PYNE.
type Follower struct {
	eng    *Engine
	src    string
	name   string
	coin   string
	iv     domain.Interval
	inputs map[string]any

	// Window is how many trailing bars each run sees (default 2000, max
	// MaxBars-1). Scripts with long warm-ups need more.
	Window int
	// ScaleToEquity rescales PYNE's position by ctx.Equity()/PYNE equity.
	ScaleToEquity bool
	// MinOrder ignores deltas smaller than this (base units, default 1e-9).
	MinOrder float64

	lastT      int64
	last       *Result
	closeFills bool // script fills at bar close: ignore the phantom bar
}

var titleRE = regexp.MustCompile(`(?m)^strategy\s*\(\s*(?:title\s*=\s*)?"((?:[^"\\]|\\.)*)"`)

// Compile mirrors pine.Compile for the PYNE engine: it checks src with
// `pyne diagnostics` and returns a strategy for one coin and interval.
// Only strategy() scripts can trade; indicators are rejected.
func (e *Engine) Compile(ctx context.Context, src, coin string, iv domain.Interval, inputs map[string]any) (*Follower, error) {
	if !isStrategy(src) {
		return nil, errors.New("pyne: only strategy() scripts can be run as a strategy")
	}
	ds, err := e.Check(ctx, src)
	if err != nil {
		return nil, err
	}
	if errs := Errors(ds); len(errs) > 0 {
		return nil, &ScriptError{Kind: "parse", Msg: errs[0].Msg, Diagnostics: errs}
	}
	name := "pine"
	if m := titleRE.FindStringSubmatch(src); m != nil {
		name = m[1]
	}
	return &Follower{eng: e, src: src, name: name, coin: coin, iv: iv, inputs: inputs, closeFills: fillsOnClose(src)}, nil
}

// Spec implements strategy.Strategy.
func (f *Follower) Spec() strategy.Spec {
	return strategy.Spec{Name: f.name, Kind: "pine", Universe: []string{f.coin}, Interval: f.iv}
}

// Init implements strategy.Strategy.
func (f *Follower) Init(strategy.Context) error {
	f.lastT, f.last = 0, nil
	return nil
}

// LastResult is the most recent PYNE run (for debugging / UI).
func (f *Follower) LastResult() *Result { return f.last }

// OnBar implements strategy.Strategy.
func (f *Follower) OnBar(ctx strategy.Context) error {
	bars := ctx.Bars(f.coin)
	if len(bars) == 0 {
		return nil
	}
	now := bars[len(bars)-1].T
	if now == f.lastT {
		return nil
	}
	f.lastT = now
	win := f.Window
	if win <= 0 {
		win = 2000
	}
	win = min(win, MaxBars-1)
	if len(bars) > win {
		bars = bars[len(bars)-win:]
	}
	dur := f.iv.Duration().Milliseconds()
	if dur <= 0 && len(bars) > 1 {
		dur = bars[len(bars)-1].T - bars[len(bars)-2].T
	}
	lc := bars[len(bars)-1].C
	run := make([]domain.Candle, 0, len(bars)+1)
	run = append(run, bars...)
	run = append(run, domain.Candle{T: now + max(dur, 1), O: lc, H: lc, L: lc, C: lc})

	res, err := f.eng.Execute(strategy.StdContext(ctx), f.src, run, Options{Inputs: f.inputs, Ticker: f.coin, Interval: f.iv, Sample: 2})
	if err != nil {
		return err
	}
	f.last = res
	if !res.Instrumented || len(res.Position) < 2 {
		return fmt.Errorf("pyne: no position export for %s", f.name)
	}
	for title, p := range res.Plots { // value on the real last bar
		if n := len(p.Tail); n >= 2 && !math.IsNaN(p.Tail[n-2]) && p.Kind == "plot" {
			ctx.Plot(title, p.Tail[n-2])
		}
	}
	// Orders placed on the real last bar fill at the phantom open; with
	// close fills they already filled at the real bar's close, and orders
	// placed on the phantom bar itself must not count.
	target := res.Position[len(res.Position)-1].V
	if f.closeFills {
		target = res.Position[len(res.Position)-2].V
	}
	if f.ScaleToEquity {
		if pe := res.Equity[len(res.Equity)-2].V; pe > 0 {
			target *= ctx.Equity() / pe
		}
	}
	cur := ctx.Position(f.coin)
	delta := target - cur
	minOrder := f.MinOrder
	if minOrder <= 0 {
		minOrder = 1e-9
	}
	if math.IsNaN(delta) || math.Abs(delta) < minOrder {
		return nil
	}
	side := domain.Buy
	if delta < 0 {
		side = domain.Sell
	}
	reduce := target == 0 || (cur != 0 && math.Signbit(target) == math.Signbit(cur) && math.Abs(target) < math.Abs(cur))
	o := strategy.Order{Coin: f.coin, Side: side, Size: math.Abs(delta), Kind: strategy.Market, ReduceOnly: reduce, Tag: "pyne"}
	if reduce && target == 0 {
		o.Size = 0 // close the whole position
	}
	if err := ctx.Order(o); err != nil {
		return err
	}
	ctx.Mark(delta > 0, fmt.Sprintf("pyne %+.6g", target))
	ctx.Logf("pyne: target position %.8g %s (was %.8g)", target, f.coin, cur)
	return nil
}

// Signals turns a one-shot Result into per-bar target positions keyed by
// bar open time: the position PYNE held after each bar's fills. Useful for
// charts and for comparing engines; not used by Follower.
func Signals(r *Result) map[int64]float64 {
	out := make(map[int64]float64, len(r.Position))
	for _, p := range r.Position {
		out[p.T] = p.V
	}
	return out
}

// Fills converts PYNE's closed trades and the open position into hlp
// fills: one opening and one closing fill per closed trade, plus one
// opening fill (at the average price) for a position still open, in time
// order. Pyramided entries stay separate trades, as PYNE reports them.
func Fills(r *Result, coin string) []domain.Fill {
	var fs []domain.Fill
	for _, t := range r.Trades {
		openSide, closeSide, dir := domain.Buy, domain.Sell, "Long"
		if !t.Long {
			openSide, closeSide, dir = domain.Sell, domain.Buy, "Short"
		}
		fs = append(fs,
			domain.Fill{Coin: coin, Side: openSide, Px: t.EntryPx, Sz: t.Size, Time: t.EntryTime, Dir: "Open " + dir, Fee: t.Commission / 2},
			domain.Fill{Coin: coin, Side: closeSide, Px: t.ExitPx, Sz: t.Size, Time: t.ExitTime, Dir: "Close " + dir, ClosedPnL: t.Profit + t.Commission, Fee: t.Commission / 2},
		)
	}
	if o := r.Open; o != nil && o.Size != 0 {
		// One opening fill at the average price, stamped at the bar where
		// the current position (same sign, from flat or a flip) began.
		t := o.MarkTime
		for i := len(r.Position) - 1; i >= 0; i-- {
			if p := r.Position[i].V; p == 0 || math.Signbit(p) != math.Signbit(o.Size) {
				break
			}
			t = r.Position[i].T
		}
		side, dir := domain.Buy, "Open Long"
		if o.Size < 0 {
			side, dir = domain.Sell, "Open Short"
		}
		fs = append(fs, domain.Fill{Coin: coin, Side: side, Px: o.AvgPx, Sz: math.Abs(o.Size), Time: t, Dir: dir})
	}
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Time < fs[j].Time })
	return fs
}
