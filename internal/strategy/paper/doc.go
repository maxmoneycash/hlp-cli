// Package paper runs strategies on LIVE market data with a simulated ledger
// (no keys, no risk), and hosts the AI-vs-AI arena.
//
// Fills: market orders fill against the current book (walk levels from a
// Book snapshot, else mid +/- slippage); limit orders fill when the live
// price trades through. Fees and funding as in backtest. Sessions persist to
// <DataDir>/paper/<id>.json after every fill and every 60 s so they survive
// restarts (resume on next `hlp paper status`).
package paper
