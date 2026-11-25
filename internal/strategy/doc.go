// Package strategy defines the strategy runtime contract shared by
// backtests, paper trading, the AI-vs-AI arena and (Phase 2) live vault
// bots: one Strategy interface, one Context, three engines.
//
// Subpackages: pine (Pine Script v5/v6 subset interpreter), thesis (basket
// YAML like "long SPY, short memecoins"), backtest, paper (+ arena), sim
// (the simulated broker and Context shared by backtest and paper) and
// loader (strategy references -> strategies, arena files).
//
// This package also holds the LLM strategy (NewLLM): an "AI vault" bot
// that asks a model for target weights each bar, validates them and
// rebalances. examples/llm/*.yaml configure bots; examples/arena/*.yaml pit
// them against each other in paper mode.
//
// Rules:
//   - Paper mode is the default everywhere. Nothing here imports
//     hyperliquid/exchange. Live execution is a Phase-2 adapter owned by core.
//   - Indicators must match internal/chart's SMA/EMA numbers exactly (reuse them).
//   - Pine: our own MIT interpreter; no third-party interpreter code is
//     vendored. Unsupported features fail loudly with line:col.
//   - No network in tests: use the BTC 15m candle sample (testkit.BTC15m)
//     and fixtures under testdata/fixtures/.
package strategy
