// Package metrics holds the pure performance math shared by vault
// tearsheets and backtests, so "backtest vs vault vs HLP" is one table.
//
// No I/O, no network, no globals.
//
// Formulas follow hlp.xyz /methodology so numbers match the website, and
// curves are built from Hyperliquid portfolio data (account value and PnL):
//   - period return r_i = (P_i - P_{i-1}) / A_{i-1}  (flow-neutral, from PnL and AV)
//   - TWR curve C_k = prod(1 + r_i)
//   - MaxDD = min_t (C_t - peak_t) / peak_t           (negative fraction)
//   - Sharpe = mean(r)/std(r) * sqrt(periodsPerYear), rf = 0,
//     periodsPerYear = 365d / median(dt). Compute first resamples curves
//     spanning >= 7 days to daily UTC samples (forward-filled), so Vol,
//     Sharpe and Sortino use daily returns as on hlp.xyz and irregular
//     snapshot spacing cannot inflate them. Undefined ratios (zero
//     dispersion, no losses) are 0, never ±Inf/NaN, so results always
//     encode as JSON.
//   - Correlation/Beta use daily returns aligned on UTC days.
//   - Sortino uses downside deviation; Calmar = CAGR / |MaxDD|
//   - Monthly returns from end-of-month curve values; years compound months
//   - Capacity = TVL * (leaderFraction - 0.05) / 0.05; HLP unlimited (-1);
//     0 if leaderFraction < 0.049
package metrics
