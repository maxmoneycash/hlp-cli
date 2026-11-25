package pineforge

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/metrics"
	"github.com/maxmoneycash/hlp/internal/strategy"
	"github.com/maxmoneycash/hlp/internal/strategy/backtest"
)

// NotAppliedWarning is attached to every PineForge backtest: the numbers
// are PineForge's broker, not hlp's. Funding is reported as 0 because it
// was not modelled, not because it was zero.
const NotAppliedWarning = "hlp fees, slippage, funding, szDecimals and leverage limits not applied; script commission_* only (funding not modelled)"

// Backtest runs src once over candles with PineForge's broker and returns
// the *backtest.Result shape the builtin engine and pyne.Backtest produce,
// so `hlp backtest --engine pineforge` can reuse the report, chart and
// tearsheet code. Fills, fees and PnL are PineForge's (the script's
// commission and slippage settings; no hlp slippage or funding). Plots are
// empty: the report carries no plot series.
func (e *Engine) Backtest(ctx context.Context, src, coin string, iv domain.Interval, candles []domain.Candle, o Options) (*backtest.Result, *Result, error) {
	if o.Interval == "" {
		o.Interval = iv
	}
	r, err := e.Execute(ctx, src, candles, o)
	if err != nil {
		return nil, nil, err
	}
	if len(r.Equity) == 0 {
		return nil, r, errors.New("pineforge: report has no equity curve")
	}
	name := "pine"
	if m := titleRE.FindStringSubmatch(src); m != nil {
		name = m[1]
	}
	ver := e.Version
	if ver == "" {
		ver = e.Image
	}
	res := &backtest.Result{
		Spec:     strategy.Spec{Name: name, Kind: "pine", Universe: []string{coin}, Interval: iv},
		Candles:  map[string][]domain.Candle{coin: candles},
		Plots:    map[string][]float64{},
		Warnings: append([]string{"engine: PineForge " + ver + " (external; transpiler under the PineForge Source License)", NotAppliedWarning}, r.Warnings...),
	}
	// PineForge stamps equity at bar open (time_ms = the bar's timestamp);
	// the builtin engine and pyne.Backtest stamp at bar close.
	dur := iv.Duration().Milliseconds()
	for _, p := range r.Equity {
		res.Equity = append(res.Equity, domain.Point{T: p.T + dur, V: p.V})
	}
	res.Fills = Fills(r, coin)
	res.Trades = len(res.Fills)
	res.Fees = r.Stats.Commission
	var closed []float64
	for _, t := range r.Trades {
		if t.OpenAtEnd {
			res.Markers = append(res.Markers, backtest.Marker{T: t.EntryTimeMS, Coin: coin, Buy: t.Long(), Label: "entry"})
			continue
		}
		closed = append(closed, t.PnL)
		res.Markers = append(res.Markers,
			backtest.Marker{T: t.EntryTimeMS, Coin: coin, Buy: t.Long(), Label: "entry"},
			backtest.Marker{T: t.ExitTimeMS, Coin: coin, Buy: !t.Long(), Label: "exit"})
	}
	last := time.UnixMilli(res.Equity[len(res.Equity)-1].T).UTC()
	ts := metrics.Tearsheet(res.Equity, metrics.TearsheetOpts{Title: name, ClosedPnL: closed, Now: last})
	if ts.Title == "" {
		ts.Title = name
	}
	ts.Meta.Source = "backtest (pineforge)"
	ts.Meta.AsOf = last
	ts.Meta.Warnings = append(ts.Meta.Warnings, res.Warnings...)
	res.Tearsheet = ts
	return res, r, nil
}

// Fills turns PineForge trades into hlp fills (one open and one close per
// closed trade; an open trade contributes only its opening fill), in the
// same layout as pyne.Fills.
func Fills(r *Result, coin string) []domain.Fill {
	var fs []domain.Fill
	for _, t := range r.Trades {
		openSide, closeSide, dir := domain.Buy, domain.Sell, "Long"
		if !t.Long() {
			openSide, closeSide, dir = domain.Sell, domain.Buy, "Short"
		}
		fs = append(fs, domain.Fill{Coin: coin, Side: openSide, Px: t.EntryPrice, Sz: t.Qty, Time: t.EntryTimeMS, Dir: "Open " + dir, Fee: t.Commission / 2})
		if !t.OpenAtEnd {
			fs = append(fs, domain.Fill{Coin: coin, Side: closeSide, Px: t.ExitPrice, Sz: t.Qty, Time: t.ExitTimeMS, Dir: "Close " + dir,
				ClosedPnL: t.PnL + t.Commission, Fee: t.Commission / 2})
		}
	}
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Time < fs[j].Time })
	return fs
}
