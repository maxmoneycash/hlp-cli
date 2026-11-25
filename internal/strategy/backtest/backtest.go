package backtest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/metrics"
	"github.com/maxmoneycash/hlp/internal/strategy"
	"github.com/maxmoneycash/hlp/internal/strategy/sim"
)

// MaxCandles is Hyperliquid's per-interval candle history limit.
const MaxCandles = 5000

// Options for a run.
type Options struct {
	Capital    float64 // USD, default 10000
	Start, End int64   // ms; 0 = as far back as data allows / now
	Costs      strategy.Costs
	Benchmarks map[string]domain.Series // passed through to metrics.Tearsheet

	// Now is the clock used to drop the still-forming last bar when End is
	// 0 (default time.Now). Tests pin it.
	Now func() time.Time
	// NoCosts disables fees, slippage and funding. Otherwise a zero Costs
	// value means strategy.DefaultCosts (Hyperliquid base tier).
	NoCosts bool
	// MaxLogs caps Result.Logs (default 2000, oldest dropped).
	MaxLogs int
	// Markets are the exchange rules per coin (lot size, $10 minimum,
	// max leverage). Coins listed here get Hyperliquid's account model:
	// sizes floored to szDecimals, orders under $10 dropped, orders above
	// the available margin clipped and liquidation at maintenance margin
	// (pine-subset A2/A3). Nil keeps the plain broker (unit tests).
	Markets map[string]sim.Rules
	// Mode is the broker model: "" or ModeHyperliquid (hlp's costs,
	// funding and Markets), or ModeTradingView (TradingView's broker
	// emulator for parity checks: Costs carries the script's commission,
	// with no slippage, funding or exchange rules).
	Mode string
	// Progress, when set, is told how history loading is going
	// ("candles" or "funding", coins done, coins total). Cold funding
	// history is rate-limited and can take a while; a CLI shows this on
	// stderr so the run never looks hung. Calls are serialized.
	Progress func(stage string, done, total int)
}

// Broker modes.
const (
	ModeHyperliquid = "hyperliquid"
	ModeTradingView = "tradingview"
)

// fetchConcurrency bounds parallel history requests (the client's rate
// limiter still governs the total request weight).
const fetchConcurrency = 4

// forEach runs f(i) for i in [0,n) on up to fetchConcurrency goroutines
// and reports progress after each one.
func forEach(n int, stage string, progress func(string, int, int), f func(i int)) {
	var (
		mu   sync.Mutex
		done int
		wg   sync.WaitGroup
		sem  = make(chan struct{}, fetchConcurrency)
	)
	if progress != nil {
		progress(stage, 0, n)
	}
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer func() { <-sem; wg.Done() }()
			f(i)
			if progress != nil {
				mu.Lock()
				done++
				progress(stage, done, n)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
}

// Result of a run.
type Result struct {
	Spec      strategy.Spec
	Tearsheet domain.Tearsheet
	Equity    domain.Series
	Fills     []domain.Fill
	Candles   map[string][]domain.Candle // what was simulated (for charts)
	Plots     map[string][]float64       // aligned with the first universe coin's candles (NaN = no value)
	Markers   []Marker
	Logs      []string
	Warnings  []string

	// Stats beyond the tearsheet.
	Fees    float64 // USD paid
	Funding float64 // USD paid (negative = received)
	Trades  int     // number of fills
	// Account counts what the exchange rules changed (clipped and
	// dropped orders, liquidations); zero without Options.Markets.
	Account sim.Stats
}

// Marker is an entry/exit marker for the chart.
type Marker struct {
	T     int64  `json:"t"`
	Coin  string `json:"coin"`
	Buy   bool   `json:"buy"`
	Label string `json:"label,omitempty"`
}

// Run executes s against ds.
//
// Timeline: every coin's candles are merged on open time. For each bar:
// queued market orders fill at that bar's open (+/- slippage, taker fee),
// resting limits/stops are matched along the intrabar path, hourly funding
// that settled during the bar is charged, the bar closes (becomes visible
// to the strategy), equity is marked, and finally the strategy's OnBar runs
// (once Spec.Warmup bars of the lead coin have closed). Orders placed in
// OnBar therefore fill on the NEXT bar: no look-ahead is possible.
func Run(ctx context.Context, s strategy.Strategy, ds strategy.DataSource, o Options) (*Result, error) {
	if s == nil || ds == nil {
		return nil, errors.New("backtest: nil strategy or data source")
	}
	spec := s.Spec()
	if len(spec.Universe) == 0 {
		return nil, errors.New("backtest: strategy has an empty universe")
	}
	iv := spec.Interval
	if iv == "" {
		iv = domain.I1h
	}
	if !iv.Valid() {
		return nil, fmt.Errorf("backtest: invalid interval %q", iv)
	}
	if o.Capital <= 0 {
		o.Capital = 10000
	}
	switch o.Mode {
	case "", ModeHyperliquid:
	case ModeTradingView:
		// The script's own commission (Costs, as the caller maps it), no
		// slippage, funding or exchange rules; zero Costs = no costs.
		o.Markets = nil
		o.Costs.SlippageBps, o.Costs.Funding = 0, false
		if o.Costs == (strategy.Costs{}) {
			o.NoCosts = true
		}
	default:
		return nil, fmt.Errorf("backtest: unknown mode %q (hyperliquid or tradingview)", o.Mode)
	}
	if o.NoCosts {
		o.Costs = strategy.Costs{}
	} else if o.Costs == (strategy.Costs{}) {
		o.Costs = strategy.DefaultCosts
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MaxLogs <= 0 {
		o.MaxLogs = 2000
	}
	dur := iv.Duration().Milliseconds()
	res := &Result{Spec: spec, Candles: map[string][]domain.Candle{}, Plots: map[string][]float64{}}

	// ---- load history ----
	n := MaxCandles
	if o.Start > 0 {
		end := o.End
		if end == 0 {
			end = o.Now().UnixMilli()
		}
		want := int((end-o.Start)/dur) + spec.Warmup + 2
		if want < n {
			n = want
		}
	}
	nowMs := o.Now().UnixMilli()
	fetched := make([][]domain.Candle, len(spec.Universe))
	ferrs := make([]error, len(spec.Universe))
	forEach(len(spec.Universe), "candles", o.Progress, func(i int) {
		if ctx.Err() != nil {
			ferrs[i] = ctx.Err()
			return
		}
		fetched[i], ferrs[i] = ds.Candles(ctx, spec.Universe[i], iv, n, o.End)
	})
	for i, coin := range spec.Universe {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := ferrs[i]; err != nil {
			return nil, fmt.Errorf("backtest: candles %s %s: %w", coin, iv, err)
		}
		cs := cleanCandles(fetched[i])
		// Drop bars after End and the still-forming last bar.
		cut := len(cs)
		for cut > 0 {
			last := cs[cut-1]
			if (o.End > 0 && last.T+dur > o.End) || (o.End == 0 && last.T+dur > nowMs) {
				cut--
				continue
			}
			break
		}
		cs = cs[:cut]
		// Keep Warmup bars before Start as history.
		if o.Start > 0 {
			i := sort.Search(len(cs), func(i int) bool { return cs[i].T >= o.Start })
			from := max(i-spec.Warmup, 0)
			cs = cs[from:]
		}
		if len(cs) == 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: no %s history in range; excluded", coin, iv))
			continue
		}
		res.Candles[coin] = cs
	}
	lead := spec.Universe[0]
	if len(res.Candles[lead]) == 0 {
		return nil, fmt.Errorf("backtest: no %s history for %s", iv, lead)
	}
	if o.Start > 0 {
		first := res.Candles[lead][0].T
		if spec.Warmup > 0 && len(res.Candles[lead]) > spec.Warmup {
			first = res.Candles[lead][spec.Warmup].T
		}
		if first > o.Start+dur {
			res.Warnings = append(res.Warnings, fmt.Sprintf("history starts %s, after the requested start %s (Hyperliquid keeps ~%d candles per interval)",
				fmtT(first), fmtT(o.Start), MaxCandles))
		}
	} else if len(res.Candles[lead]) >= MaxCandles-1 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("effective start %s: Hyperliquid keeps ~%d %s candles", fmtT(res.Candles[lead][0].T), MaxCandles, iv))
	}
	for _, coin := range spec.Universe[1:] {
		if cs := res.Candles[coin]; len(cs) > 0 && cs[0].T > res.Candles[lead][0].T {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: history starts %s; excluded from targets until listed", coin, fmtT(cs[0].T)))
		}
	}

	// ---- funding ----
	funding := map[string][]domain.FundingPoint{}
	if o.Costs.Funding {
		start := res.Candles[lead][0].T
		var end int64
		for _, cs := range res.Candles {
			if t := cs[len(cs)-1].T + dur; t > end {
				end = t
			}
		}
		var coins []string
		for _, coin := range spec.Universe {
			if len(res.Candles[coin]) > 0 {
				coins = append(coins, coin)
			}
		}
		got := make([][]domain.FundingPoint, len(coins))
		errs := make([]error, len(coins))
		forEach(len(coins), "funding", o.Progress, func(i int) {
			// a coin listed after the run starts has no funding before its
			// first candle: never page through windows that cannot exist
			from := max(start, res.Candles[coins[i]][0].T)
			got[i], errs[i] = ds.Funding(ctx, coins[i], from, end)
		})
		for i, coin := range coins {
			if errs[i] != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s: funding history unavailable (%v); funding not applied", coin, errs[i]))
				continue
			}
			fs := got[i]
			sort.SliceStable(fs, func(i, j int) bool { return fs[i].Time < fs[j].Time })
			funding[coin] = fs
		}
	}

	// ---- timeline ----
	tset := map[int64]bool{}
	for _, cs := range res.Candles {
		for _, c := range cs {
			tset[c.T] = true
		}
	}
	times := make([]int64, 0, len(tset))
	for t := range tset {
		times = append(times, t)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })

	eng := sim.NewEngine(o.Capital, o.Costs, spec.Universe)
	if len(o.Markets) > 0 {
		eng.Rules = o.Markets
	}
	leadIdx := map[int64]int{}
	for i, c := range res.Candles[lead] {
		leadIdx[c.T] = i
	}
	leadN := len(res.Candles[lead])
	plotAt := -1
	eng.OnPlot = func(name string, v float64) {
		if plotAt < 0 {
			return
		}
		p := res.Plots[name]
		if p == nil {
			p = make([]float64, leadN)
			for i := range p {
				p[i] = math.NaN()
			}
			res.Plots[name] = p
		}
		p[plotAt] = v
	}
	eng.OnMark = func(coin string, buy bool, label string) {
		res.Markers = append(res.Markers, Marker{T: eng.Now(), Coin: coin, Buy: buy, Label: label})
	}
	eng.OnLog = func(msg string) {
		res.Logs = append(res.Logs, fmt.Sprintf("%s %s", fmtT(eng.Now()), msg))
		if len(res.Logs) > o.MaxLogs {
			res.Logs = res.Logs[len(res.Logs)-o.MaxLogs:]
		}
	}
	eng.OnFill = func(f domain.Fill, tag string) {
		res.Fills = append(res.Fills, f)
		if tag == "rebalance" || tag == "" {
			return
		}
		res.Markers = append(res.Markers, Marker{T: f.Time, Coin: f.Coin, Buy: f.Side == domain.Buy, Label: tag})
	}

	if len(times) > 0 {
		eng.SetNow(times[0])
	}
	if err := s.Init(eng); err != nil {
		return nil, fmt.Errorf("backtest: init: %w", err)
	}

	idx := map[string]int{}  // next candle index per coin
	fidx := map[string]int{} // next funding index per coin
	leadClosed := 0          // closed bars of the lead coin
	started := false         // first OnBar happened
	for _, t := range times {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		eng.SetNow(t)
		// 1) open: fill market orders, then resting orders along the bar
		for _, coin := range spec.Universe {
			cs := res.Candles[coin]
			i := idx[coin]
			if i >= len(cs) || cs[i].T != t {
				continue
			}
			bar := cs[i]
			eng.FillMarketAt(coin, bar.O, t)
			eng.MatchResting(coin, bar)
			eng.CheckLiquidation(coin, bar)
		}
		// 2) funding settled during this bar, at the bar's close as mark
		if o.Costs.Funding {
			for _, coin := range spec.Universe {
				fs := funding[coin]
				j := fidx[coin]
				for j < len(fs) && fs[j].Time < t+dur {
					if fs[j].Time >= t {
						eng.ApplyFunding(coin, fs[j].Rate, barCloseAt(res.Candles[coin], idx[coin], t))
					}
					eng.SetFunding(coin, fs[j].Rate)
					j++
				}
				fidx[coin] = j
			}
		}
		// 3) close: bars become visible
		for _, coin := range spec.Universe {
			cs := res.Candles[coin]
			i := idx[coin]
			if i < len(cs) && cs[i].T == t {
				eng.AddBar(coin, cs[i])
				idx[coin] = i + 1
				if coin == lead {
					leadClosed++
				}
			}
		}
		eng.CheckWipeout(t)
		// 4) decide
		if leadClosed < max(spec.Warmup, 1) {
			continue
		}
		started = true
		res.Equity = append(res.Equity, domain.Point{T: t + dur, V: eng.Equity()})
		if eng.Halted() {
			continue
		}
		plotAt = -1
		if i, ok := leadIdx[t]; ok {
			plotAt = i
		}
		if err := s.OnBar(eng); err != nil {
			return nil, fmt.Errorf("backtest: %s at %s: %w", spec.Name, fmtT(t), err)
		}
	}
	if !started {
		return nil, fmt.Errorf("backtest: not enough history: %d %s bars of %s, warmup needs %d", leadN, iv, lead, spec.Warmup)
	}
	if p := eng.Pending(); len(p) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d order(s) still pending at the end of the data", len(p)))
	}
	if eng.Halted() {
		res.Warnings = append(res.Warnings, "account wiped out during the backtest (equity <= 0)")
	}
	res.Account = eng.Stats()
	if a := res.Account; a != (sim.Stats{}) {
		res.Warnings = append(res.Warnings, accountWarnings(a)...)
	}
	if o.Mode == ModeTradingView {
		res.Warnings = append(res.Warnings, "TradingView broker mode: script commission only; no slippage, funding, lot sizes or margin")
	}
	res.Fees = eng.L.FeesPaid()
	res.Funding = eng.L.FundingPaid()
	res.Trades = len(res.Fills)

	var closed []float64
	for _, f := range res.Fills {
		if f.ClosedPnL != 0 {
			closed = append(closed, f.ClosedPnL-f.Fee)
		}
	}
	ts := metrics.Tearsheet(res.Equity, metrics.TearsheetOpts{Title: spec.Name, Benchmarks: o.Benchmarks, ClosedPnL: closed, Now: o.Now()})
	if ts.Title == "" {
		ts.Title = spec.Name
	}
	ts.Meta.Source = "backtest"
	ts.Meta.AsOf = time.UnixMilli(res.Equity[len(res.Equity)-1].T).UTC()
	ts.Meta.Warnings = append(ts.Meta.Warnings, res.Warnings...)
	res.Tearsheet = ts
	return res, nil
}

// accountWarnings summarises what Hyperliquid's rules changed.
func accountWarnings(a sim.Stats) []string {
	var w []string
	if a.Clipped > 0 {
		w = append(w, fmt.Sprintf("%d order(s) clipped by margin (leverage above min(script, maxLeverage))", a.Clipped))
	}
	if a.BelowLot > 0 {
		w = append(w, fmt.Sprintf("%d order(s) dropped: qty below lot size (szDecimals)", a.BelowLot))
	}
	if a.BelowMinNotional > 0 {
		w = append(w, fmt.Sprintf("%d order(s) dropped: below the $10 minimum notional", a.BelowMinNotional))
	}
	if a.Liquidations > 0 {
		w = append(w, fmt.Sprintf("%d liquidation(s) at maintenance margin (no clearance fee modelled)", a.Liquidations))
	}
	return w
}

// barCloseAt returns the close of coin's bar at t, or the last closed bar
// before it (carry forward), or 0.
func barCloseAt(cs []domain.Candle, i int, t int64) float64 {
	if i < len(cs) && cs[i].T == t {
		return cs[i].C
	}
	if i > 0 {
		return cs[i-1].C
	}
	return 0
}

// cleanCandles sorts by open time, dedupes (last wins) and drops bars with
// non-positive or non-finite prices.
func cleanCandles(cs []domain.Candle) []domain.Candle {
	out := make([]domain.Candle, 0, len(cs))
	for _, c := range cs {
		if !(c.O > 0 && c.H > 0 && c.L > 0 && c.C > 0) || math.IsInf(c.O+c.H+c.L+c.C, 0) {
			continue
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].T < out[j].T })
	w := 0
	for i := range out {
		if w > 0 && out[w-1].T == out[i].T {
			out[w-1] = out[i]
			continue
		}
		out[w] = out[i]
		w++
	}
	return out[:w]
}

func fmtT(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04") }

// History loads the closed candles one coin's backtest would simulate (the
// same window, cleaning and still-forming-bar cut as Run, no warm-up), for
// external engines (PYNE, PineForge) that run their own broker over them.
func History(ctx context.Context, ds strategy.DataSource, coin string, iv domain.Interval, o Options) ([]domain.Candle, []string, error) {
	if ds == nil {
		return nil, nil, errors.New("backtest: nil data source")
	}
	if !iv.Valid() {
		return nil, nil, fmt.Errorf("backtest: invalid interval %q", iv)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	dur := iv.Duration().Milliseconds()
	n := MaxCandles
	end := o.End
	if end == 0 {
		end = o.Now().UnixMilli()
	}
	if o.Start > 0 {
		n = min(n, int((end-o.Start)/dur)+2)
	}
	raw, err := ds.Candles(ctx, coin, iv, n, o.End)
	if err != nil {
		return nil, nil, fmt.Errorf("backtest: candles %s %s: %w", coin, iv, err)
	}
	cs := cleanCandles(raw)
	cut := len(cs)
	for cut > 0 && cs[cut-1].T+dur > end {
		cut--
	}
	cs = cs[:cut]
	if o.Start > 0 {
		cs = cs[sort.Search(len(cs), func(i int) bool { return cs[i].T >= o.Start }):]
	}
	if len(cs) == 0 {
		return nil, nil, fmt.Errorf("backtest: no %s history for %s", iv, coin)
	}
	var warns []string
	if o.Start > 0 && cs[0].T > o.Start+dur {
		warns = append(warns, fmt.Sprintf("history starts %s, after the requested start %s (Hyperliquid keeps ~%d candles per interval)",
			fmtT(cs[0].T), fmtT(o.Start), MaxCandles))
	}
	return cs, warns, nil
}
