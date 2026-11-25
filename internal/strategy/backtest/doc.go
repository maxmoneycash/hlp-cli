// Package backtest runs a strategy over historical Hyperliquid candles with
// fees, slippage and hourly funding, and produces the same domain.Tearsheet
// as vault analytics (via internal/metrics).
//
// Semantics: decisions on bar close, fills at the NEXT bar open +/- slippage
// (no look-ahead); limits fill if the next bar trades through; multi-coin
// strategies align bars by open time (missing bars carry forward, coins
// without history before their listing are excluded until listed).
// History is bounded by Hyperliquid's ~5000 candles per interval: report
// the effective start in Result.Warnings.
package backtest
