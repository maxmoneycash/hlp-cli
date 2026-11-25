package pyne

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/metrics"
	"github.com/maxmoneycash/hlp/internal/strategy"
	"github.com/maxmoneycash/hlp/internal/strategy/backtest"
)

// NotAppliedWarning is attached to every external-engine backtest: the
// numbers are the engine's own broker, not hlp's. Funding is reported as 0
// because it was not modelled, not because it was zero.
const NotAppliedWarning = "hlp fees, slippage, funding, szDecimals and leverage limits not applied; script commission_* only (funding not modelled)"

// Backtest runs src once over candles with PYNE's own broker emulator and
// returns the same *backtest.Result shape the builtin engine produces, so
// `hlp backtest --engine pyne` can reuse the existing report, chart and
// tearsheet code. Fills, fees and PnL are PYNE's (the script's
// commission_* settings; no hlp slippage or funding). Plots carry only
// PYNE's trailing samples (NaN before them).
func (e *Engine) Backtest(ctx context.Context, src, coin string, iv domain.Interval, candles []domain.Candle, inputs map[string]any) (*backtest.Result, *Result, error) {
	if !isStrategy(src) {
		return nil, nil, errors.New("pyne: backtest needs a strategy() script")
	}
	var trimmed string
	if len(candles) > MaxBars {
		trimmed = fmt.Sprintf("PYNE takes at most %d bars: ran the most recent %d of %d", MaxBars, MaxBars, len(candles))
		candles = candles[len(candles)-MaxBars:]
	}
	r, err := e.Execute(ctx, src, candles, Options{Inputs: inputs, Ticker: coin, Interval: iv, Sample: 64})
	if err != nil {
		return nil, nil, err
	}
	if !r.Instrumented || len(r.Equity) == 0 {
		return nil, r, errors.New("pyne: no equity export (see Result.Warnings)")
	}
	name := r.Script.Name
	if name == "" {
		name = "pine"
	}
	dur := iv.Duration().Milliseconds()
	res := &backtest.Result{
		Spec:     strategy.Spec{Name: name, Kind: "pine", Universe: []string{coin}, Interval: iv},
		Candles:  map[string][]domain.Candle{coin: candles},
		Plots:    map[string][]float64{},
		Warnings: append([]string{"engine: PYNE " + e.Version + " (AGPL-3.0, external)", NotAppliedWarning}, r.Warnings...),
	}
	if trimmed != "" {
		res.Warnings = append(res.Warnings, trimmed)
	}
	for _, p := range r.Equity { // builtin convention: stamp at bar close
		res.Equity = append(res.Equity, domain.Point{T: p.T + dur, V: p.V})
	}
	res.Fills = Fills(r, coin)
	res.Trades = len(res.Fills)
	res.Fees = r.Stats.Commission + r.Stats.OpenCommission
	var closed []float64
	for _, t := range r.Trades {
		closed = append(closed, t.Profit)
		res.Markers = append(res.Markers,
			backtest.Marker{T: t.EntryTime, Coin: coin, Buy: t.Long, Label: t.EntryID},
			backtest.Marker{T: t.ExitTime, Coin: coin, Buy: !t.Long, Label: t.ExitID})
	}
	if o := r.Open; o != nil {
		for i := len(res.Fills) - 1; i >= 0; i-- {
			if f := res.Fills[i]; f.Px == o.AvgPx && f.Sz == math.Abs(o.Size) && f.ClosedPnL == 0 {
				res.Markers = append(res.Markers, backtest.Marker{T: f.Time, Coin: coin, Buy: f.Side == domain.Buy, Label: "open"})
				break
			}
		}
	}
	n := len(candles)
	for title, p := range r.Plots {
		if p.Kind != "plot" {
			continue
		}
		s := make([]float64, n)
		for i := range s {
			s[i] = math.NaN()
		}
		for i, v := range p.Tail {
			if j := n - len(p.Tail) + i; j >= 0 {
				s[j] = v
			}
		}
		res.Plots[title] = s
	}
	res.Logs = r.Logs
	ts := metrics.Tearsheet(res.Equity, metrics.TearsheetOpts{Title: name, ClosedPnL: closed, Now: time.UnixMilli(res.Equity[len(res.Equity)-1].T)})
	if ts.Title == "" {
		ts.Title = name
	}
	ts.Meta.Source = "backtest (pyne)"
	ts.Meta.AsOf = time.UnixMilli(res.Equity[len(res.Equity)-1].T).UTC()
	ts.Meta.Warnings = append(ts.Meta.Warnings, res.Warnings...)
	res.Tearsheet = ts
	return res, r, nil
}
