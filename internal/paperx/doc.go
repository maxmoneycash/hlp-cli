// Package paperx is hlp's live-book paper exchange.
//
// One Engine is one paper account. It is driven by market events — L2
// books, public trades, mark prices and settled funding — and never by
// wall-clock timers, so a fixture tape replays the same fills every time.
//
// What it models, and how:
//
//   - Exact arithmetic: prices, sizes, balances and fees are
//     shopspring/decimal values; floats appear only in output views.
//   - Hyperliquid's order rules with its own rejection strings: prices on
//     the 5-significant-figure / MAX_DECIMALS-szDecimals grid, sizes on the
//     lot, the $10 minimum (reduce-only exempt), Alo rejected when it would
//     cross, Ioc remainders cancelled, Gtc remainders rested, reduce-only
//     orders that would increase a position rejected, initial margin.
//   - Latency: an order becomes active only on the first book received at
//     or after placement + Config.Latency, so it never fills against a book
//     the decision could not have seen.
//   - Taker fills walk the effective book: the real book minus the
//     liquidity this account already took (self-impact overlay). The
//     overlay decays with a half-life and is released as soon as the real
//     level grows back (refill). A walk never fills past real depth: an Ioc
//     or market remainder is cancelled, never filled at a synthetic price.
//   - Resting orders get a queue position: at activation the queue ahead is
//     the size shown at that price. Prints at the price consume the queue
//     first and fill the order only with what is left; a print through the
//     price fills up to that print's size; level shrinkage that prints do
//     not explain (cancels) shrinks the queue pro rata. Resting fills are
//     maker.
//   - TP/SL triggers fire on mark, then trade as market orders with 10%
//     slippage through the book. normalTpsl children are sized by the
//     parent's fills; siblings are one-cancels-other; triggers die with the
//     position.
//   - Funding on settled hourly rates times oracle-price notional, charged
//     once per hour boundary a position was open across.
//   - Liquidation at mark against maintenance margin (1/(2*maxLeverage), or
//     tiered tables): through the book first, the remainder by a backstop at
//     mark; isolated margin is locked and limits that position's loss.
//
// Every view this package returns carries Mode "paper".
package paperx
