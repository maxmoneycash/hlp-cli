# PYNE: an optional second Pine engine

hlp has its own Pine Script v5/v6 interpreter (`internal/strategy/pine`,
MIT). PYNE is an optional second engine that you can install yourself.
`internal/pyne` lets hlp run your strategy through PYNE and compare the
results with the builtin engine.

## What PYNE is

[PYNE](https://github.com/hoox-sh/pyne) (PyPI package `hoox-pyne`, made by
HOOX) is a Pine Script engine written in Python. It has an ANTLR grammar, a
bar-loop runtime with a TradingView-style broker emulator, a CLI (`pyne`), a
language server (`pyne-lsp`), an MCP server (`pyne-mcp`) and an HTTP API.
hlp uses only the CLI.

## Install

```sh
uv tool install hoox-pyne     # needs Python 3.10+; uv can fetch one
pyne --version                # 0.7.0 was tested
```

The base install measured on 2026-11-15 was 9 small packages (antlr4
runtime, click, requests, tqdm and their dependencies) in a 15 MB tool
environment. It needs no numba, numpy, Flask or MCP SDK. Do not add the
`[compile]`, `[pro]` or `[mcp]` extras: hlp does not need them. Without
numba, `--mode auto` falls back to the interpreter, so hlp always asks for
`--mode interpret`.

If `pyne` is not on your `PATH`, set `HLP_PYNE_BIN=/absolute/path/to/pyne`.

## License boundary

- **hlp stays MIT.** No PYNE code is copied, ported, translated, vendored
  or linked into hlp. hlp starts the `pyne` program you installed, gives it
  files (your script and the candles) and reads the JSON it prints. That is
  the whole connection. hlp was written from PYNE's public docs and `--help`
  output, not from its source code.
- **PYNE is optional.** hlp builds, tests and runs without it. CI uses a
  fake `pyne` that replays recorded output, so it needs no Python.
- **PYNE is AGPL-3.0, and you install it separately.** Using it on your own
  machine is your choice, under PYNE's terms. HOOX also sells a commercial
  license. Whether you need one is up to you (for example, if you offer PYNE
  as a network service). hlp never buys or requires one.

## How hlp uses it

All calls are `exec.Command(pyne, args...)`, with no shell. Values that
come from the user (ticker, timeframe) are passed as `--opt=value`, so they
can never be read as another flag. Inputs are written as temporary files in
`os.TempDir()/hlp-pyne-*`, which hlp deletes after every call, including
after a timeout. Environment variables whose names look like credentials
(`*KEY*`, `*SECRET*`, `*TOKEN*`, `*PRIVATE*`, `*PASSWORD*` and similar) are
removed before PYNE starts, because it is third-party code and needs none
of them. PYNE runs as a single process with no children, so killing it at
the timeout leaves nothing behind.

| Call | Command | Output hlp reads |
| --- | --- | --- |
| Detect | `pyne --version` | `0.7.0` (must be at least 0.6.0) |
| Check | `pyne diagnostics FILE` | `{"ok","diagnostics":[{code,severity,message,line,column}]}` with a 1-based line and a 0-based column. hlp converts the column to 1-based and removes PYNE's duplicate `parse`/`E001` pair. The exit code is 1 when there are errors. |
| Execute | `pyne execute FILE --mode interpret --ohlcv BARS.json --inputs INPUTS.json --ticker=BTC --timeframe=15 --sample N --timeout S [--trades]` | `{"ok","bars","plots":{title:{len,last,na_count,sample}},"extras":{plot_meta,events,alerts,meta:{inputs,logs}}}`, or `{"ok":false,"error","kind":"parse"\|"runtime"}` with exit code 1 |

- **Bars:** `BARS.json` is a JSON array of
  `{"time":ms,"open","high","low","close","volume"}`, oldest first, with a
  maximum of 5000 rows. `--ohlcv` also accepts inline JSON, and `FILE` can be
  `-` to read stdin. hlp uses files. `pyne data` only fetches from
  providers (mock, yahoo, alphavantage, ccxt), so hlp passes Hyperliquid
  candles directly.
- **Inputs:** keyed by input title, for example `{"Fast length": 5}`. hlp
  also accepts the variable name, as the builtin engine does, and converts
  it to the title. The title is `title = "..."` or the second positional
  argument, so `input.timeframe("D", "Filter timeframe")` and
  `input.time(timestamp("..."), "Start")` resolve correctly. A key that
  matches nothing is passed through unchanged and produces a warning.
- **Timeframe:** hlp intervals are mapped to TradingView strings (`15m` →
  `15`, `1h` → `60`, `1d` → `D`).
- **Plots:** PYNE returns only the last `sample` values of each plot (at
  most 64). It drops plots whose title starts with `__`.
- **Strategy results:** the envelope has no list of trades and no fill
  prices. Strategy events carry the bar OHLC but not the fill price, and on
  the trailing-stop sample they skip entries. To get the results, hlp adds a
  short tail of its own Pine code to strategy scripts. On every bar, the
  tail writes `log.info("hlp.bar|…")` (equity, position, net profit) and one
  `log.info("hlp.trade|…")` line for each new closed trade, using
  `strategy.closedtrades.*`. hlp parses these lines from `extras.meta.logs`
  and removes them from the logs it shows you. If PYNE rejects the tail, hlp
  runs the script again without it and returns a warning.
- **Errors:** a parse failure from `execute` is run again through
  `diagnostics` on your unmodified source, so the error has the correct
  `line:col`. Runtime failures return `ScriptError{Kind:"runtime"}`.
- **Timeouts:** PYNE's own `--timeout` is only checked between bars. In one
  test, a 3-second timeout took 2 minutes 23 seconds. hlp therefore kills the
  process itself when `Engine.Timeout` expires (60 seconds by default) and
  returns `*TimeoutError`. Standard output is capped at 64 MB.
- **Speed:** each call takes about 2 to 3 seconds for 300 to 3000 bars on
  this Mac. About 1.4 seconds of that is parsing. The installed Python is
  x86_64, so it runs under Rosetta.

### Go API (`internal/pyne`)

```go
eng, err := pyne.Detect(ctx)              // ErrNotInstalled -> use the builtin engine
diags, err := eng.Check(ctx, src)         // []Diagnostic{Line, Col, Severity, Msg}
res, err := eng.Execute(ctx, src, candles, pyne.Options{Inputs: in, Ticker: "BTC", Interval: domain.I15m})
// res.Trades, res.Equity, res.Position, res.Stats, res.Plots, res.Alerts, res.Logs
bt, res, err := eng.Backtest(ctx, src, "BTC", domain.I15m, candles, inputs) // *backtest.Result
st, err := eng.Compile(ctx, src, "BTC", domain.I15m, inputs)               // strategy.Strategy
```

### Wiring for the integrator (not done here)

`internal/strategy` exposes Pine as `pine.Parse(src) (*Script, error)` plus
`pine.Compile(script, coin, iv, inputs) (strategy.Strategy, error)`.
`internal/pyne` provides the same two shapes:

1. **`--engine builtin|pyne|auto`** on `hlp backtest`, `hlp pine check`,
   paper and arena. Use `auto` as the default: PYNE when `pyne.Available`,
   otherwise builtin. With `pyne` and no install, fail with
   `pyne.InstallHint`.
2. **Backtest:** for the `pyne` engine, call `eng.Backtest(...)` instead of
   `backtest.Run(pine.Compile(...))`. It returns a `*backtest.Result` (equity
   stamped at bar close, fills, markers, tearsheet), so the existing report
   and chart code can display it. Each closed PYNE trade becomes an opening
   and a closing fill. A position still open at the end gets one opening
   fill at its average price. Pyramided entries remain separate trades. The fills and fees are PYNE's: the
   script's `commission_*` settings apply, and hlp's slippage and funding do
   not. Plots contain only the last 64 values. The data must be at most 5000
   bars. hlp keeps the most recent ones and adds a warning when it drops
   any.
3. **Paper / arena / live:** `eng.Compile(...)` returns a `*pyne.Follower`
   that implements `strategy.Strategy`. On each closed bar, it runs PYNE
   again over the last `Window` bars (2000 by default) plus one placeholder
   bar at the last close. It then sends a market order for the difference
   between PYNE's position and hlp's position. There is no look-ahead.
   Stop, limit and trailing exits are filled by hlp one bar later, at the
   open. Each bar costs one `pyne` process, which takes about 2 seconds.
4. **Check:** `hlp pine check --engine pyne` prints `eng.Check` diagnostics
   in the same `line:col: message` format as `pine.Errors`.
5. **Detection:** use `HLP_PYNE_BIN` or `PATH`, and show the engine in the
   status bar or report (`Result.Warnings[0]` already says
   `engine: PYNE 0.7.0`).

## Tests

```sh
go test ./internal/pyne/                              # fake pyne, no Python
HLP_PYNE=1 go test ./internal/pyne/ -run TestLivePyne -v
HLP_PYNE=1 HLP_PYNE_RECORD=1 go test ./internal/pyne/ -run TestLivePyne   # re-record testdata/
HLP_PYNE=1 go test ./internal/pyne/pynecompare -v     # the table below
```

## Builtin vs PYNE on the 10 research samples

Run with hoox-pyne 0.7.0 on 2026-11-15. Both engines start with
`initial_capital` 10,000.

- **Builtin net** is the final equity minus the starting capital, with
  `NoCosts` or with hlp's Hyperliquid costs (4.5 bps taker and 2 bps
  slippage).
- **Builtin closes** counts the fills that realised PnL. The builtin engine
  uses net positions, so a pyramided position closes in one fill.
- **PYNE** reports `strategy.closedtrades` (one per entry), closed
  `netprofit`, and net PnL including the open position.

| sample | data | builtin closes | builtin net (no costs) | builtin net (HL costs) | PYNE closed trades | PYNE closed net | PYNE net incl. open |
|---|---|---:|---:|---:|---:|---:|---:|
| 01-supertrend | btc15m | 3 | 1056.25 | 1008.52 | 3 | 861.57 | 1023.19 |
| 02-macd-cross | btc15m | 16 | -315.52 | -422.46 | 16 | -333.84 | **-315.52** |
| 03-rsi2-mean-reversion | btc15m | 0 | 0.00 | 0.00 | 0 | 0.00 | 0.00 |
| 04-bollinger-breakout | btc15m | 3 | -884.86 | -927.02 | 3 | -665.16 | -7545.54 |
| 05-ema-ribbon | btc15m | 1 | 781.38 | 768.88 | 1 | 781.38 | **781.38** |
| 06-donchian-turtle | btc15m | 8 | 9764.36 | 8672.15 | 24 | 10257.16 | 10168.90 |
| 07-golden-cross-htf | btc15m | 0 | 0.00 | 0.00 | 0 | 0.00 | 0.00 |
| 08-atr-trailing-stop | btc15m | 5 | 156.74 | 91.10 | 6 | -56.33 | -56.33 |
| 09-stoch-rsi-bracket | btc15m | 19 | 739.73 | 481.16 | 21 | 1358.40 | 1391.79 |
| 10-squeeze-momentum | btc15m | 11 | -27.30 | -168.96 | 11 | -27.30 | **-27.30** |
| 01-supertrend | synth1h | 27 | 21551.10 | 20500.12 | 27 | 10181.97 | 20820.01 |
| 02-macd-cross | synth1h | 240 | 154.16 | -1409.18 | 240 | 131.30 | **154.16** |
| 03-rsi2-mean-reversion | synth1h | 29 | -217.84 | -400.56 | 29 | -217.84 | **-217.84** |
| 04-bollinger-breakout | synth1h | 37 | -7691.23 | -7807.45 | 18 | -39.95 | 3058.42 |
| 05-ema-ribbon | synth1h | 21 | 19022.52 | 18257.15 | 21 | 9690.11 | **19022.54** |
| 06-donchian-turtle | synth1h | 84 | 555968.53 | 351322.35 | 127 | 197127.19 | 44084.18 |
| 07-golden-cross-htf | synth1h | 2 | -324.34 | -349.70 | 0 | 0.00 | 0.00 |
| 08-atr-trailing-stop | synth1h | 127 | -571.27 | -1888.76 | 141 | -2950.16 | -2950.16 |
| 09-stoch-rsi-bracket | synth1h | 183 | 77.22 | -2007.94 | 202 | -552.27 | -287.53 |
| 10-squeeze-momentum | synth1h | 78 | 1996.40 | 841.49 | 79 | 2002.88 | 2002.88 |

Bold values match the builtin no-cost result to the cent.

The datasets are `btc15m`, the 301 recorded Hyperliquid BTC 15m bars in
`testdata/candles/btc-15m.json`, and `synth1h`, the builtin
golden-test generator with 3000 1h bars.

### Which engine is right, sample by sample

An independent verifier checked these by hand on 2026-11-15. It dumped
both engines' per-bar positions and fills, found the first bar where they
disagree, and replayed that bar against the candles under TradingView's
broker rules: market orders fill at the next bar's open, a stop that the
open has already passed fills at the open, `pyramiding` caps same-direction
entries (default 1), and the opposite `strategy.entry` reverses the whole
position.

| sample | verdict | why |
|---|---|---|
| 02, 03, 05, 10 (btc15m) | **both right** | Sample 02 was recomputed from scratch in Python (MACD with SMA-seeded EMAs, fill at the next open, `strategy.cash` size = 5000 / signal-bar close; for example, the bar-141 entry is 5000 / 69,052 (the bar-140 close) = 0.0724092, not 5000 / 69,053 (the bar-141 open) in both engines). All 16 trades have the same entry and exit bars and prices in both engines, and the per-trade PnL agrees to within a cent. |
| 01 | **both right** | The only difference is that PYNE applies the script's `commission_value = 0.045` (percent). After that, the entry and exit bars and prices are the same. |
| 04 BB stop entry | **builtin** | PYNE ignores the default `pyramiding = 1`. On btc15m, the long stop entry fills again on bars 23, 25, 28, 34, 36, 70, 74, 76 and so on, while the position is already long, and it reaches 1.47 BTC (about 96k of notional, roughly 9.6x the 10k starting capital). An opposite `BBandSE` fill then only trims the long instead of reversing it. On synth1h it doubles to 0.506 at bar 53. The builtin engine fills the first entry at the bar-23 open (73,050, which had already gapped through the 72,987 stop) and reverses at 69,267, as TradingView would. |
| 06 turtle | **builtin** | The script asks for up to 5 entries (one breakout plus 4 adds) with `pyramiding = 4`. The builtin engine stops at 4. PYNE takes a 5th unit (btc15m bars 33, 176, 224 and 267; synth1h bar 66). Every later difference comes from that extra unit, because `unitQty` depends on equity and the error compounds. Bars where both engines entered have identical fill bars and prices. Counting trades differently is not a disagreement: PYNE lists one trade per entry, while the builtin engine counts one closing fill per net position. |
| 07 HTF | **builtin** | `request.security(..., "D", ...)` gets no daily series from the bars hlp passes in, so PYNE leaves `htfEma` as `na` and the filter never passes. The builtin engine resamples the 1h bars to daily, and `[1]` with `lookahead_on` is the usual non-repainting idiom, so its 2 trades are legitimate. |
| 08 trailing stop | **neither exactly; builtin is the safer approximation** | TradingView moves a trailing stop within the bar. Short at 69,331 (bar 127, ATR 292): on bar 128 the low of 68,861 activates the trail, the stop sits at about 69,153, and the bar's later rise to 69,470 should stop it out on bar 128. The builtin engine moves trails once per closed bar (as documented in `internal/strategy/pine/doc.go`), so it exits one bar late at 69,158. PYNE's records are inconsistent: it shows two `S` trades for one 0.143 position, with an entry price of 69,176.82 that is not the bar's open, and an exit at 69,299.33 that leaves the position unchanged. Do not trust PYNE's figures for `trail_*` exits. |
| 09 OCA bracket | **builtin** | At btc15m bar 83, a single long signal while flat makes PYNE hold 0.285 BTC (2x of 100% equity). The trade is labelled entry `SR S` / exit `SR S x:limit` and has an averaged entry price of 69,398.54, while the bar opened at 69,763. The builtin engine opens 0.145 at the 69,763 open and is stopped out on bar 90. |
| 10 squeeze, synth1h | **builtin** | PYNE enters on bar 36 from a bar-35 signal. At bar 35, `val = ta.linreg(close - mid, 20, 0)` cannot have 20 valid inputs yet: `mid` is first valid at bar 19, so `val` is first valid at bar 38. PYNE produces a value during the warm-up where TradingView returns `na`. After that, both engines trade the same bars. |

### Guidance for the strategy engine owner

- **No look-ahead on either side.** Both engines decide on the bar close
  and fill at the next open (or on the intrabar path for resting orders).
  None of the differences above comes from future data.
- **Treat PYNE as a second opinion only for plain market-order scripts.**
  It is reliable for `strategy.entry`/`strategy.close` with market orders,
  stop-and-reverse, `commission_*` and indicator math once warmed up. Do not
  use it as a reference for stop/limit entries, `pyramiding`, `strategy.exit`
  brackets or trails, `oca_*`, or `request.security`. In 0.7.0 these give
  over-sized positions or trade records that do not add up.
- **Builtin gaps that the comparison found:**
  1. Trailing stops are moved once per closed bar, so a trail that
     activates and is hit within the same bar exits one bar late. To match
     TradingView, walk the intrabar path (O→L→H→C or O→H→L→C) for the
     activation and the stop together.
  2. Both engines size `strategy.cash` orders from the signal bar's close
     (checked on sample 02), not from the fill price, as TradingView does.
     Keep it that way.
- **What to compare.** PYNE reports one closed trade per entry
  (pyramids are separate), while the builtin engine reports net-position
  fills. Compare per-bar position and equity, not trade counts. The
  comparison test prints both.

## Known PYNE behaviours to remember

- An unknown function (`foo.bar(close)`) and an out-of-range `array.get`
  return `na` silently. `diagnostics` does not report them.
- Parse errors at the end of the file point one line past the last line.
  hlp limits the line number to the length of the script.
- `closedtrades.size()` is always positive. hlp gets the direction from the
  position sign on the entry bar.
- The `--trades` events are incomplete. For example, `strategy.entry` calls
  in the trailing-stop sample produce no `entry` events. Do not use events
  to rebuild positions.
- `pyne execute` is about 0.6 seconds of Python startup plus about 1.4
  seconds of parsing per call. There is no persistent process on the CLI
  path. The MCP server (`pyne mcp`, which needs the `[mcp]` extra) would
  keep one process alive but returns the same truncated envelope.
