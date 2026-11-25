# Examples

Original strategies written for hlp (MIT). Every one runs on paper or in a backtest; none of them
places an order, and none of them is a recommendation. Several lose money over some windows; that is
part of the point of backtesting them on Hyperliquid fees and funding before anything else.

Run the commands from the repository root.

## Theses (`thesis/`)

A thesis is a weighted long/short basket in YAML: legs, optional named baskets, leverage, bar
interval and rebalance period. `hlp backtest` prints a tearsheet against HLP and BTC; `hlp paper
start` runs the same file on the live feed.

| File | Idea | Try |
|---|---|---|
| [`long-spy-short-memes.yaml`](thesis/long-spy-short-memes.yaml) | Long the S&P 500 (HIP-3 `xyz:SP500`) 1.5x, short a memecoin basket. Mirrors hlp.xyz's spxmeme book | `hlp backtest examples/thesis/long-spy-short-memes.yaml --days 180` |
| [`spxmeme-falling.yaml`](thesis/spxmeme-falling.yaml) | The same idea closer to how hlp.xyz runs it: short only memecoins already in a downtrend | `hlp backtest examples/thesis/spxmeme-falling.yaml --bench HLP,BTC` |
| [`long-hype-btc-short-garbage.yaml`](thesis/long-hype-btc-short-garbage.yaml) | Our reading of the "Long HYPE & BTC \| Short Garbage" vault, not its actual book | `hlp backtest examples/thesis/long-hype-btc-short-garbage.yaml`, then `hlp vault "Long HYPE & BTC"` |
| [`long-majors-short-memes.yaml`](thesis/long-majors-short-memes.yaml) | Long majors, short memes, equal gross. Validator perps only, so it fits a legacy vault | `hlp backtest examples/thesis/long-majors-short-memes.yaml` |
| [`hype-vs-alts.yaml`](thesis/hype-vs-alts.yaml) | Relative value: long HYPE, short an L1/L2 alt basket, daily rebalance on 4h bars | `hlp backtest examples/thesis/hype-vs-alts.yaml` |
| [`equal-weight-majors.yaml`](thesis/equal-weight-majors.yaml) | The benchmark a crypto vault should beat: equal-weight majors, 1x, monthly | `hlp backtest examples/thesis/equal-weight-majors.yaml` |

The same thing works inline, no file needed:

```sh
hlp backtest --thesis "long BTC, short DOGE FARTCOIN, rebalance 1w" --days 90
```

`xyz:SP500` is a HIP-3 market, so theses that use it cannot run inside a legacy HyperCore vault;
they need a HyperEVM strategy vault.

## Pine strategies (`pine/`)

Pine v5/v6 strategies that `hlp pine check` passes. Single-market strategies take `--coin`.

| File | Idea | Try |
|---|---|---|
| [`ema-cross.pine`](pine/ema-cross.pine) | EMA crossover, long and short | `hlp backtest examples/pine/ema-cross.pine --coin HYPE --interval 1h` |
| [`supertrend.pine`](pine/supertrend.pine) | Supertrend: always in the market, flips on the ATR band | `hlp pine run examples/pine/supertrend.pine --coin HYPE --interval 1h` |
| [`rsi-mean-revert.pine`](pine/rsi-mean-revert.pine) | Buy washed-out dips in an uptrend, long only, hard stop | `hlp backtest examples/pine/rsi-mean-revert.pine --coin ETH --interval 1h` |
| [`donchian-breakout.pine`](pine/donchian-breakout.pine) | Turtle-style channel breakout | `hlp backtest examples/pine/donchian-breakout.pine --coin BTC --interval 4h` |
| [`bb-squeeze.pine`](pine/bb-squeeze.pine) | Trade the breakout after the narrowest Bollinger bands | `hlp backtest examples/pine/bb-squeeze.pine --coin SOL --interval 1h` |
| [`macd-trend.pine`](pine/macd-trend.pine) | MACD signal crosses, only with the trend | `hlp backtest examples/pine/macd-trend.pine --coin ETH --interval 4h` |
| [`tradingview-import.pine`](pine/tradingview-import.pine) | Written like a script exported from TradingView's Pine Editor (v6, grouped inputs, a helper function, `var` state, bracket exit). Use it as the template for importing your own | `hlp pine check examples/pine/tradingview-import.pine` |

Importing your own script: save it as a `.pine` file, run `hlp pine check my-strategy.pine` to see
anything unsupported with `line:col`, then `hlp backtest my-strategy.pine --coin BTC --interval 1h`.

## LLM bots (`llm/`)

An LLM bot is a prompt, a model, a coin universe and a decision interval. Once per closed bar the
model sees an OHLCV digest, funding and its own book, and returns target weights as JSON with a
one-line reason. Invalid replies keep the book. The first two examples use local Ollama models, so
they cost nothing to run. `scout.yaml` is the two-step bot: a scout call nominates up to 4 coins, then
a decide call returns BUY / SELL / CLOSE / HOLD decisions with confidence, rationale and key signal,
inside limits enforced in code. Runs that call a model more than once need `--llm` (see the
estimate it prints) and stop at `--max-llm-calls` (default 200).

| File | Personality | Try |
|---|---|---|
| [`macro-trader.yaml`](llm/macro-trader.yaml) | Macro book with the S&P 500 perp as the risk-on/risk-off gauge; 2 to 4 positions | `hlp paper start llm:examples/llm/macro-trader.yaml --llm` |
| [`contrarian.yaml`](llm/contrarian.yaml) | Fades stretched moves and funding extremes | `hlp paper start llm:examples/llm/contrarian.yaml --llm` |
| [`scout.yaml`](llm/scout.yaml) | Scout then decide over majors and HIP-3 equity perps, via OpenRouter | `hlp paper start llm:examples/llm/scout.yaml --llm` |

Pull the model first (`ollama pull qwen3:8b` or `ollama pull llama3.1:8b`, as named in the file's
`model:` line).

## Arenas (`arena/`)

An arena runs several entrants on one live feed with the same paper capital and ranks them.
Entrant paths are relative to the arena file.

| File | Entrants | Try |
|---|---|---|
| [`ai-vs-thesis.yaml`](arena/ai-vs-thesis.yaml) | Two LLM bots, Supertrend on HYPE, and the long SPY / short memes thesis | `hlp arena examples/arena/ai-vs-thesis.yaml --llm` |
| [`llm-vs-llm.yaml`](arena/llm-vs-llm.yaml) | The same prompt on two local models: only the model differs | `hlp arena examples/arena/llm-vs-llm.yaml --llm` |
| [`openrouter-models.yaml`](arena/openrouter-models.yaml) | The scout bot on four vendors' models through one OpenRouter key | `hlp arena examples/arena/openrouter-models.yaml --llm` |

No model installed? Pine and thesis entrants need none:

```sh
hlp arena --entrants examples/pine/ema-cross.pine@BTC,examples/pine/rsi-mean-revert.pine@ETH
```
