// Package domain holds the shared, dependency-free value types that every
// other hlp package speaks: candles, books, trades, markets, positions,
// vaults, time series and tearsheets.
//
// Changes are append-only (never rename or remove a field). It imports nothing outside the standard library.
//
// Conventions:
//   - Times are Unix milliseconds (int64) unless the field is a time.Time.
//   - Prices, sizes and USD amounts are float64 here. These types are for
//     display, analytics and backtests. Anything that is signed and sent to
//     /exchange uses decimal strings in internal/hyperliquid/exchange instead.
//   - Percent-like values are FRACTIONS (0.12 = 12%) unless the field name
//     ends in Pct.
package domain
