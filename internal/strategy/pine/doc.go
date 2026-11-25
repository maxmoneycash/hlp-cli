// Package pine is hlp's own Pine Script v5/v6 SUBSET interpreter (MIT).
//
// Semantics follow TradingView's Pine reference manual and are cross-checked
// against the PyneCore engine (internal/pyne). No third-party interpreter
// code is vendored or copied.
//
// Supported subset:
//   - //@version=5|6, strategy(), indicator(), input / input.int / float /
//     bool / string / source / price (overridable by title or variable name)
//   - series math, var/varip, typed declarations, if / else if / else,
//     switch (with or without a subject, default arm), if and switch used
//     as expressions, ternary, for ... to ... by, while, break/continue,
//     :=, += -= *= /= %=, na / nz / fixnan, the history operator [n] on any
//     expression, tuples, user functions (single-line and block, default
//     parameter values, named arguments, own ta state per call site)
//   - input.time and timestamp() (UTC by default, "GMT+N" zones),
//     input.timeframe, request.security on the same coin at the chart's
//     or a higher timeframe (bar-close values only; lookahead_on only with
//     expr[1]; see security.go)
//   - ta.sma ema rma wma vwma rsi atr tr stdev variance crossover crossunder
//     cross highest lowest change mom roc macd bb vwap supertrend stoch cci
//     cum barssince valuewhen rising falling linreg hma sar mfi percentrank
//     median alma highestbars lowestbars kc dmi pivothigh pivotlow, ta.obv;
//     math.*; str.tostring, str.format; int() float() bool(); color.*;
//     v5 "const int / const int" truncates (v6 does not)
//   - strategy.entry/order (limit, stop, oca_name with oca.cancel)/close/
//     close_all/exit (stop, limit, profit, loss, qty, qty_percent and
//     trailing trail_price/trail_points/trail_offset)/cancel/cancel_all,
//     strategy.position_size (with history), position_avg_price, equity,
//     netprofit, openprofit, default_qty_type fixed|cash|percent_of_equity,
//     pyramiding, margin_long/margin_short (script leverage, 1x by default),
//     the trade list from fills: closedtrades, wintrades, losstrades,
//     eventrades, grossprofit, grossloss, max_drawdown,
//     strategy.opentrades.* and strategy.closedtrades.* (trades.go)
//   - plot(), plotshape(), plotchar() -> chart overlays and markers; log.*
//     -> the decision log; hline/bgcolor/fill/alert* are accepted no-ops
//
// Execution model: the script runs once per closed bar; orders fill at the
// next bar open (backtest) or against the live book (paper), resting
// limits/stops walk the intrabar path like TradingView's broker emulator.
// The entry id of an open position comes from the entry that filled, not
// the one last placed. Trailing stops are client-side: the stop is moved
// once per closed bar from the best high/low since the position opened
// (TradingView moves it intrabar). OCA-cancel siblings are cancelled at the
// next bar close after a fill. syminfo.mintick follows the price every bar
// (Hyperliquid's five significant figures, at most 6 - szDecimals
// decimals; CompileWith passes szDecimals). With market rules
// (backtest.Options.Markets) the simulator floors sizes to szDecimals,
// drops orders under $10, clips orders above the margin available at
// min(script leverage, maxLeverage) and liquidates at maintenance margin.
// Positions are net (Hyperliquid has no hedge mode), so strategy.close(id)
// closes the net position opened by entry id. Tick-based exits placed on
// the entry bar use the decision close as the entry estimate and are
// re-anchored to the real fill on the next bar.
//
// Anything else (other symbols, lower timeframes, arrays, maps, matrix,
// tables, labels, strategy.risk.*) is a compile error with line:col and a
// clear message, never a silent wrong result.
package pine
