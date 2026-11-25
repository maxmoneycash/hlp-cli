# PineForge: how hlp can use it

Checked on 2026-11-15. This note covers the six projects named "PineForge",
their real licences, what we built and ran, and how hlp should use each one.

**Recommendation in brief**

1. **Use the PineForge corpus as ground truth now.** It is Apache-2.0
   Pine code with TradingView trade lists. Fetch it when tests run and never
   commit it. `internal/pinecorpus` fetches it, runs any engine and grades
   its trades against TradingView; the scoreboard is in §4.
2. **Offer PineForge as an optional third engine that runs as a
   subprocess,** in the same way as PYNE. The user installs the
   `pineforge-release` container. hlp runs `docker run` and reads its JSON
   report (`internal/pineforge`). The user must opt in and see the licence
   notice first. Vault workflows must not use it by default, because its
   transpiler needs a commercial licence for other people's capital.
3. **Do not link the engine with cgo.** The engine is Apache-2.0, so linking
   it would be legal. It would still break hlp's `CGO_ENABLED=0` six-target
   goreleaser build. It would also do nothing on its own, because every Pine
   strategy has to pass through the noncommercial transpiler first.
4. **Ignore the two unrelated projects that share the name.**
   `lomashs09/PineForge` and `shubhamtaywade82/pineforge-platform` have no
   connection to these, and nothing in them is worth taking.

No PineForge code was copied into hlp, so hlp needs no NOTICE entry. If
Apache-2.0 engine code is ever reused, add the engine's NOTICE text (Eigen
MPL-2.0 attribution included) to a NOTICE file in hlp.

---

## 1. The six projects

| Project | What it is | Licence (verified from the files) | Maturity |
|---|---|---|---|
| [pineforge-4pass/pineforge-engine](https://github.com/pineforge-4pass/pineforge-engine) | A C++17 backtest and forward-execution kernel with a Pine/TradingView parity adapter. It has a versioned C ABI (`PF_ABI_VERSION 5`) and a native live runner. | `LICENSE` is Apache-2.0 and `NOTICE` names PineForge contributors. Eigen is linked in under MPL-2.0. The optional live runner links SQLite, libcurl and OpenSSL. The benchmark tooling uses PineTS (AGPL) and vectorbt (Commons Clause), but these are optional and not linked. | 211 stars, 53 forks, 2 contributors, about 1,880 commits since 2026-05. v1.12.0 came out 2026-11-12, with a release every few days. About 108k lines of C++ in `src` and about 1,480 test files. Prebuilt archives are Linux only (x86_64 and aarch64, glibc 2.35 or later). macOS builds from source but is "not tested or supported". |
| [tzutoo/pineforge-codegen-oss](https://github.com/tzutoo/pineforge-codegen-oss) | A **fork** of [pineforge-4pass/pineforge-codegen-oss](https://github.com/pineforge-4pass/pineforge-codegen-oss) (upstream has 9 stars). It is a pure-Python transpiler from Pine v6 to C++ and is published on PyPI as `pineforge-codegen` (1.12.0). | **PineForge Source License 1.2**. It is source-available, not open source. GitHub reports it as NOASSERTION and PyPI classifies it as "Other/Proprietary". Free for noncommercial use and for Personal Trading. Investment Management and other Commercial Use need a paid licence. Releases up to 1.1.0 were PolyForm Noncommercial 1.0.0 and 1.2.0 was PFSL 1.1. | 3 contributors, about 415 commits, 541 test files. Python 3.11 to 3.14 with no runtime dependencies. There is no CLI, only `transpile()` / `transpile_full()`. Each version must be paired with the same engine version. |
| [pineforge-4pass/pineforge-corpus](https://github.com/pineforge-4pass/pineforge-corpus) | 312 clean-room Pine v6 probes in 33 categories. Each one has `strategy.pine`, `generated.cpp`, `tv_trades.csv` and `engine_trades.csv`. It also ships one OHLCV feed, Binance ETH-USDT perp 1m bars from 2020-01 to 2026-05 (Git LFS, 168 MB). | `LICENSE` and `NOTICE` are Apache-2.0 for **PineForge-authored files**: `.pine`, `generated.cpp`, `engine_trades.csv` and scripts. The TradingView `tv_trades.csv` exports are explicitly **not** licensed works (see §3). The OHLCV is described as factual public market data. | 3 stars, last commit 2026-11-10. 431,244 TradingView trades in 311 tapes (96 MB). A full shallow clone is about 580 MB. |
| [pineforge-4pass/pineforge-release](https://github.com/pineforge-4pass/pineforge-release) | Builds the `ghcr.io/pineforge-4pass/pineforge-release` image: engine static library, g++, Python and codegen. You give it Pine and a CSV and it prints a JSON report. | The repo itself is Apache-2.0. **The image also contains codegen**, so running it to transpile Pine is governed by PFSL 1.2. | 0 stars, last push 2026-11-14, VERSION 1.12.0. Multi-arch Linux (amd64 and arm64), about 144 to 151 MB compressed. |
| [lomashs09/PineForge](https://github.com/lomashs09/PineForge) | Unrelated. A tree-walking **Pine v5** interpreter in Python (about 5.6k lines, 14 indicators) with Yahoo data and a MetaTrader 5 live bridge through MetaAPI Cloud. | **Unclear.** It has no LICENSE file and GitHub detects none. The README's "License" section just says "MIT", with no copyright line or licence text. | 0 stars, last commit 2026-05-17. Needs Python 3.11 or later, pandas, yfinance and metaapi-cloud-sdk. |
| [shubhamtaywade82/pineforge-platform](https://github.com/shubhamtaywade82/pineforge-platform) | Unrelated. A Rails 8, React and Ollama web app that writes and repairs Pine v6 with local LLMs (Qwen, DeepSeek). | Apache-2.0 (`LICENSE`). | 0 stars, last commit 2026-08-16. Needs Postgres, Redis and Sidekiq. |

### What the PineForge Source License means for hlp

Quoted points from `pineforge-codegen-oss/LICENSE` (PFSL 1.2):

- **Permitted:** any noncommercial purpose, personal research, and
  **Personal Trading**. Personal Trading means one natural person backtesting
  or trading *their own account with their own capital*, and it is allowed
  even when that is their livelihood.
- **Investment Management is always Commercial Use**, including for
  noncommercial organisations. It covers managing, advising on or trading
  other people's capital, and backtesting strategies for that capital. **A
  hlp.xyz strategy vault with depositors is exactly this.** So is a prop-firm
  or funded account, even a simulated one.
- **Commercial Use** also covers any use by or for a company (2); embedding
  the software **or its Output** in a product or service made available to
  others, including using the software to generate Output for that product
  (3); and running a hosted or public-facing service with it (4).
- **"Output"** is the generated C++ and any program built from it. **Backtest
  results, charts, reports and trade signals are not Output**, so hlp may
  display and store a PineForge JSON report.
- The violation clause allows 32 days to cure after written notice. There is
  a patent-defence termination clause.

What follows for hlp:

- hlp (MIT) must not ship codegen, generated C++ or compiled strategy
  libraries, and must not run codegen in CI or on a hlp.xyz server.
- hlp may start a container that **the user** installed. The user's licence
  then decides whether their use is allowed. hlp should show the notice,
  require an explicit opt-in, and refuse or warn when the strategy is going
  into a vault.
- Commercial use of PineForge codegen needs a commercial licence from its
  licensor. That applies to whoever runs it, not to hlp's code.

---

## 2. What we built and ran

All of this ran in scratch space on an Apple-silicon Mac on 2026-11-15, and
the scratch space was deleted afterwards. **We never ran codegen.**
Transpiling for a project tied to a commercial protocol is close enough to
"use for a company" that we avoided it. Every strategy was built from the
corpus's Apache-2.0 `generated.cpp`. That route is fully open: engine, corpus
and a C++17 compiler.

1. **Engine library.** We ran `cmake -DCMAKE_BUILD_TYPE=Release
   -DCMAKE_OSX_ARCHITECTURES=arm64 -DPINEFORGE_BUILD_TESTS=OFF
   -DPINEFORGE_BUILD_TUTORIAL=OFF -DPINEFORGE_BUILD_EXAMPLES=OFF`, then
   `cmake --build build --target pineforge`. Configuring took about 90 s,
   because it fetches Eigen 3.4.0, and building took 75 s on 8 cores. The
   result was `libpineforge.a` (13 MB) and `libpineforge_kernel.a` (7 MB).
   We had to work around three macOS problems:
   - The system Eigen 5.0.1 does not satisfy `find_package(Eigen3 3.3)`, so
     the build fetches 3.4.0. Eigen's BLAS subproject then probes an x86_64
     gfortran. Fix: add `-DCMAKE_Fortran_COMPILER=OFF`.
   - The Intel (Rosetta) `cmake` builds x86_64 unless you set
     `CMAKE_OSX_ARCHITECTURES`.
   - The full `all` target fails to link the `pineforge-selected-admit`
     helper on macOS. The library target alone builds cleanly.
2. **Strategy.** We built it with `c++ -std=c++17 -O2 -ffp-contract=off
   -fbracket-depth=1024 -shared -fPIC -Iinclude -Ibuild/include
   -Ibuild/_deps/eigen-src generated.cpp -Wl,-force_load,build/lib/libpineforge.a
   -o strategy.so`. Each compile took about 4 s and produced a 6.5 MB module.
   Corpus C++ generated by codegen 1.7.0 still compiled against engine 1.12.0.
3. **Runs**, using `scripts/run_strategy.py` (ctypes over the C ABI) with
   Python 3.12:

   | Probe | Bars | Trades | Wall time | Result |
   |---|---|---|---|---|
   | `composite-4emarsi-integration-01` | 221,734 | 335 | 22 s (includes building the 15m feed from 1m once) | Byte-identical to the committed `engine_trades.csv`. PineForge's grader rates it **excellent** against TradingView: 0% entry and exit deltas, 0 unmatched. |
   | `ta-hma-55-close-cross-01` | 221,947 | 4,839 | 2.9 s | Byte-identical tape and **excellent**. |

4. **JSON contract.** `docker/run_json.py --so strategy.so --ohlcv feed.csv
   --input-tf 15` is the harness the release image runs. It printed a 4.5 MB
   report with these keys: `engine`, `input`, `applied_inputs`,
   `applied_overrides`, `applied_runtime`, `summary`, `diagnostics`, `trades`,
   `metrics`, `equity_curve` and `fingerprint`. Each trade carries `n`,
   `side`, `entry_time` and `exit_time` (Unix ms), `entry_price`,
   `exit_price`, `qty`, `pnl`, `pnl_pct`, `max_runup`, `max_drawdown`,
   `commission`, `entry_bar_index`, `exit_bar_index` and `open_at_end`.
   `internal/pineforge` parses this layout.
5. **We did not pull the release image.** The Docker daemon was not running.
   The image is under 1 GB (about 150 MB compressed), so pulling it is
   allowed when someone wants to test `internal/pineforge` against it for
   real.

---

## 3. Can hlp redistribute the corpus's TradingView traces?

**No. Download them at test time.**

- Both the corpus's and the engine's `LEGAL.md` say the `tv_trades.csv`
  files are "not a PineForge-licensed work". They are included as "factual
  parity reference records". The authors argue the facts are not
  copyrightable. However, "whether public redistribution of the exports is
  permitted under TradingView's Terms of Service" is still **open**, with
  counsel's opinion "targeted Q3 2026". That opinion has not been published.
- The corpus's `generated.cpp` files are codegen "Output". The corpus
  republishes them under Apache-2.0 as the authors' own output from their own
  `.pine` files, so building them, as we did in §2, needs no codegen licence.
  One small inconsistency: the corpus's `LEGAL.md` still names the older
  PolyForm licence for the transpiler, and its copyright line reads
  "PineForge contributors" rather than the licensor "PINEFORGE LLC". If hlp
  ever ships anything built from `generated.cpp`, get a lawyer to look at
  this first.
- The engine's own closed test set (680 community scripts) is kept private
  "under TradingView's Terms of Service".
- Parity does not depend on the tapes. `.pine`, `generated.cpp` and the
  OHLCV regenerate `engine_trades.csv`, which is Apache-2.0.

So hlp commits **none** of the corpus. `internal/pinecorpus` reads a checkout
from `$HLP_PINECORPUS_DIR`, or with `HLP_CORPUS=1` fetches the pinned
revision into the user cache dir (§4); without either, and without
`HLP_LIVE=1`, its corpus tests skip. The
fixtures in `internal/pinecorpus/testdata` are synthetic: invented numbers in
TradingView's column layout.

```sh
DIR=$(mktemp -d)/pineforge-corpus
git clone --depth 1 https://github.com/pineforge-4pass/pineforge-corpus "$DIR"   # ~580 MB with LFS
# pin it for reproducible CI: git -C "$DIR" fetch --depth 1 origin <sha> && git -C "$DIR" checkout <sha>
HLP_LIVE=1 HLP_PINECORPUS_DIR="$DIR" go test ./internal/pinecorpus -run Live -v
```

Format details that `internal/pinecorpus` handles:

- TradingView tapes are written in the **chart time zone of the export**.
  That is Asia/Taipei (UTC+8) unless `inputs.json` sets `tv_trades_csv_tz`
  (`asia_taipei` or `utc_plus_8`).
- Engine tapes are UTC.
- Rows are newest first, with two rows (entry and exit) per trade number.
- An open trade is marked by the exit Signal `Open`.
- Headers carry the currency, for example `Price USDT`.
- Some files start with a UTF-8 byte-order mark.
- Negative numbers may use U+2212.

---

## 4. How hlp should use each project

### (3) The corpus as ground truth: do this first

`internal/pinecorpus` is now a full validation harness. It fetches the
corpus at test time, builds the 15m chart feed, runs any engine adapter on
the probes and grades its trades against TradingView's.

**How to run it.** Nothing runs in the default `go test ./...`; every
corpus test skips unless `HLP_LIVE=1` and either `HLP_CORPUS=1` (download; check `df -h` first, it needs about 2 GB free and Fetch refuses below that) or `HLP_PINECORPUS_DIR`
(existing checkout) is set.

```sh
# 30-probe scoreboard: builtin, PYNE (if installed), PineForge's tapes
HLP_LIVE=1 HLP_CORPUS=1 go test ./internal/pinecorpus/scoreboard -run 'TestLiveScoreboard$' -v -timeout 60m
# every probe the 15m harness can run fairly, builtin and PineForge only (~2 min)
HLP_LIVE=1 HLP_CORPUS=1 HLP_CORPUS_N=400 HLP_CORPUS_ENGINES=builtin,pineforge \
  go test ./internal/pinecorpus/scoreboard -run 'TestLiveScoreboard$' -v
# unmatched trades and exit mismatches for chosen probes
HLP_LIVE=1 HLP_CORPUS=1 HLP_CORPUS_PROBES=order-close-all-cancel-all-01 \
  go test ./internal/pinecorpus/scoreboard -run ScoreboardDebug -v
```

Other knobs: `HLP_CORPUS_PROBES` (comma list), `HLP_SCOREBOARD_OUT` (write
the table to a file), `HLP_PINECORPUS_CACHE` (cache directory),
`HLP_PINEFORGE=1` (also run the live PineForge image).

**What it does.**

- `pinecorpus.Root` / `Fetch` make a shallow, sparse, blob-filtered clone
  of the corpus pinned at `dc8027362ff1` (2026-11-09) into
  `<user cache dir>/hlp/pinecorpus/`, skipping `generated.cpp`, and pull
  the Git LFS 1m feed. All git calls are argv-only (no shell). The first
  fetch took about 4 minutes and uses about 570 MB on disk, including the
  derived 15m feed. Nothing is committed: the TradingView tapes stay in the
  cache.
- `ChartFeed` resamples the 1m feed to 15m bars aligned to epoch multiples
  (222,295 bars, 2020-01-01 to 2026-05-04) and caches the CSV in the
  corpus's own git-ignored `data/derived/`.
- `Probe.Plain` picks the 232 probes whose `inputs.json` changes nothing
  about the run: no other feed, timeframe, magnifier, runtime or strategy
  overrides, and no declared anomaly. The other 80 need harness support
  that has not been written.
- `Engine` is the adapter interface (`Name`, `Run(ctx, Job) → trades`).
  The scoreboard test has four adapters:
  - **builtin:** `pine.Parse` → `pine.Compile` → `backtest.Run` over the
    whole six-year feed (a data source that ignores Hyperliquid's
    5,000-candle cap), then `FillsToTrades` rebuilds TradingView-style
    trades (FIFO lots, flips split, fees shared per unit). Costs are the
    "TradingView broker" settings: the script's percent commission as the
    taker fee, no slippage, no funding.
  - **pyne:** `pyne.Engine.Execute` on the last 5,000 bars ending at the
    tape's last entry (PYNE's cap); the first 1,000 bars are warm-up and
    only trades entered after them are graded.
  - **pineforge (corpus tape):** the corpus's committed
    `engine_trades.csv`, which is PineForge 1.12.0's own output built from
    `generated.cpp` (we reproduced two of these byte for byte, §2).
  - **pineforge (live):** `pineforge.Engine.Execute` on the release image,
    only with `HLP_PINEFORGE=1`. Not run for this report (§2 and below).
- `GradeTrades` pairs trades by side and entry time and checks entry and
  exit times, prices (relative 1e-4), count, and net PnL (within 1 % of
  gross |PnL|). Verdicts: **exact** (all of those), **close** (≥ 90 % of
  trades match on entry and exit), **partial** (≥ 50 %), **diverged**,
  **error** (the engine could not run the script), and **no-trades**
  (TradingView and the engine both have no trades in the window). Two TradingView export
  artefacts are not counted: the last trade, which TradingView force-closes
  with no signal at the end of the backtest date range (`Trade.RangeEnd`),
  and the first trade when the engine entered the range already holding a
  position from the earlier history (`Grade.StartEdge`).
- `Markdown` and `Failures` render the scoreboard; unit tests on invented
  data cover the fill-to-trade rebuild, resampling, grading, both
  artefacts and the table.

**Scoreboard: 30 probes, one round-robin pick per category** (corpus
`dc8027362ff1`, 2026-11-15). Cells read "verdict matched/TradingView
trades" plus the share of matched trades whose exit time also matches.
PYNE's counts are smaller because only its 4,000-bar window is graded.

| probe | builtin | pyne 0.7.0 | pineforge (corpus tape) |
|---|---|---|---|
| `analyzer-anvil-percent-costs-01` | exact 325/325 | close 34/35 x100% | exact 325/325 |
| `analyzer-parity-choch-bos-isolator-01` | error | exact 107/107 | close 1026/1027 x100% |
| `array-atlas-momentum-rotation-01` | error | partial 106/106 x100% | exact 930/930 |
| `bands-constellation-mean-reversion-01` | close 779/779 x100% | close 87/87 x100% | exact 779/779 |
| `bracket-atr-trail-series-int-points-01` | error | diverged 0/83 | exact 792/792 |
| `bracket-atr-trailing-stop-state-01` | exact 5074/5074 | exact 571/571 | exact 5074/5074 |
| `calendar-waypoint-weekday-macd-01` | close 2165/2165 x99% | close 226/229 x97% | exact 2165/2165 |
| `cap-risk-gates-allow-max-intraday-01` | error | partial 84/84 x0% | exact 732/732 |
| `composite-4emarsi-integration-01` | exact 335/335 | exact 49/49 | exact 335/335 |
| `composite-4emarsi-rsi-pullback-latch-01` | exact 815/815 | close 79/80 x100% | exact 816/816 |
| `drawing-chartpoint-trendline` | error | exact 256/256 | exact 2379/2379 |
| `drawing-delete-halt` | error | exact 275/275 | exact 2504/2504 |
| `input-source-subscript-hl2-01` | exact 16347/16347 | close 1765/1765 x100% | exact 16347/16347 |
| `map-mosaic-regime-weight-01` | error | partial 81/81 x100% | exact 836/836 |
| `math-forge-log-channel-01` | close 812/812 x100% | close 84/84 x96% | close 813/813 x100% |
| `matrix-covariance-eigen-pca-01` | error | error | exact 2851/2851 |
| `matrix-eigen-rank-deficient-cov-01` | error | error | exact 871/871 |
| `mtf-daily-ema26-warmup-01` | exact 30/30 | diverged 0/7 | exact 31/31 |
| `mtf-daily-prev-high-break-01` | exact 147/147 | partial 19/23 x63% | exact 147/147 |
| `na-deep-history-int-na-01` | error | diverged 3/12 x0% | exact 106/106 |
| `na-ledger-history-anchor-01` | exact 228/228 | partial 18/18 x100% | exact 228/228 |
| `oca-exit-bracket-internal-cancel-01` | exact 421/421 | exact 51/51 | exact 421/421 |
| `order-close-all-cancel-all-01` | partial 732/732 x0% | partial 84/84 x0% | exact 732/732 |
| `pyramid-cash-fractional-commission-01` | close 786/790 x100% | close 74/74 x100% | exact 790/790 |
| `recompute-alma-sar-corr-magnifier-01` | error | exact 72/72 | exact 582/582 |
| `session-hour-minute-pulse-filter-01` | exact 366/366 | exact 42/42 | exact 366/366 |
| `syntax-caliper-scalar-switch-01` | error | close 37/37 x94% | exact 341/341 |
| `ta-accdist-ema-cross-01` | error | exact 572/572 | exact 5770/5770 |
| `udt-method-calls-sibling-cumulative-01` | error | exact 15/15 | exact 173/173 |
| `volume-tide-vwap-mfi-01` | error | partial 55/55 x90% | exact 524/524 |

| engine | exact | close | partial | diverged | error |
|---|---:|---:|---:|---:|---:|
| builtin | 10 | 4 | 1 | 0 | 15 |
| pyne 0.7.0 (4,000-bar window) | 10 | 8 | 7 | 3 | 2 |
| pineforge 1.12.0 (corpus tape) | 28 | 2 | 0 | 0 | 0 |

**All 232 plain probes, builtin vs PineForge** (same harness, ~2 min):

| engine | exact | close | partial | diverged | error |
|---|---:|---:|---:|---:|---:|
| builtin | 78 | 39 | 16 | 3 | 96 |
| pineforge 1.12.0 (corpus tape) | 200 | 32 | 0 | 0 | 0 |

How to read this:

- **When the builtin engine accepts a script, it mostly reproduces
  TradingView:** 117 of the 136 scripts it runs are exact or close, often
  trade for trade over 13 months (16,347 of 16,347 trades on
  `input-source-subscript-hl2-01`). Its main gap is coverage: 96 of 232
  plain probes (41 %) are rejected at parse time.
- **PYNE runs far more of the corpus** (28 of 30 here), but on its
  4,000-bar window it matches TradingView less often than the builtin
  engine does on the scripts both run. Several "partial" rows have every
  TradingView trade matched plus extra PYNE trades, which points at its
  trade export or pyramiding rather than its signal maths. The window
  start also makes stateful scripts (`mtf-*`, `na-deep-history`) unfair to
  PYNE; treat its column as indicative.
- **PineForge's tapes are near-perfect,** as published. The 2 "close"
  rows here are one-trade or small fill-price differences its own grader
  accepts. This column is PineForge's committed output, not a fresh run.

**Builtin engine: top failure causes, for the `internal/strategy` team.**
Counts are over the 232 plain probes; probe names are the corpus's
isolators, so each one is a ready-made regression test.

1. **Unsupported at parse time (96 probes).** Largest groups:
   user-defined types and methods (23), `ta.pivothigh`/`pivotlow` (14),
   `strategy.exit` with `trail_points` but no `trail_offset` (5, TradingView
   accepts it), drawings used as data (`line`, 4), `time()` (3),
   `strategy.opentrades.entry_bar_index` (3), `input.session` (3),
   arrays/maps/matrices (8 together), `process_orders_on_close` and
   `calc_on_every_tick` (2 each), `strategy.risk.*` (3), `request.security`
   on a chart-series expression (2), `alert.freq_*` constants (2), and one
   probe each (two for `ta.mfi` and `ta.dmi`) for `ta.accdist`, `ta.obv`, `ta.pvt`,
   `ta.pvi`, `ta.wpr`, `ta.tsi`, `ta.sar`, `ta.rci`, `ta.cog`, `ta.cmo`,
   `ta.kc`, `ta.median`, `ta.percentrank`, `ta.highestbars`,
   `ta.pivot_point_levels`, `str.lower`, `int()` casts and stop-limit
   entries. The single biggest coverage win is the missing `ta.*` set plus
   `ta.pivothigh`: about 35 probes, all pure maths.
2. **Two pending stop entries on opposite sides (8 probes, all
   "partial" at 75–80 % of entries):** `order-dual-stop-*`
   (`both-touch-priority`, `far-only`, `near-only`, `open-high-first-path`,
   `open-low-first-path`, `open-tie`, `source-order-*`). When both stops
   can trigger in one bar, or the bar opens through one, the builtin engine
   fills a different side or bar from TradingView.
3. **`immediately=true` on `strategy.close`/`close_all` is ignored (2
   probes):** `order-close-all-cancel-all-01` (0 of 732 exit times match)
   and `order-close-immediate-vs-next-bar-01` (half match). TradingView
   fills these at the signal bar's close; hlp fills at the next bar's open,
   one bar late (same price on this gap-free feed, so PnL agrees).
4. **Raw `strategy.order` brackets with `oca.reduce` (2 probes,
   diverged 0 trades in the window):** `oca-raw-strategy-order-reduce-01`
   and `order-one-side-four-bar-far-opposite-01`. The builtin engine makes
   74 trades in six years where TradingView makes 366 in 13 months: the
   position never returns flat, so the `position_size == 0` gate never
   reopens. Likely cause: a reducing `strategy.order` opposite an open
   position is not reduced/cancelled the TradingView way.
5. **Trailing exits (2 probes, exit times match 11–38 %):**
   `bracket-trail-points-with-offset-only-01`,
   `bracket-trailing-activation-offset-path-01`. Trail activation and offset
   semantics differ from TradingView's.
6. **Same-bar entry/close and repeated entries (5 probes):**
   `order-same-id-market-entry-repeat-01` (hlp makes exactly twice
   TradingView's trades: a repeated `strategy.entry` with the same id is
   added instead of ignored), `order-cross-entry-close-same-pass-01` (1,463
   vs 732 trades), `order-opposite-entry-close-same-pass-01` and
   `pyramid-flip-stop-pyramiding-2-01` (19 extra trades each),
   `pyramid-cash-fractional-commission-01` (4 adds missing, all on the
   bar where `close_all` also fills; likely the add is netted away).
7. **OCA and partial exits (3 probes):** `oca-multi-bracket-isolation-01`
   (65 % exit times), `bracket-tp-sl-oca-reduce-isolate-01` (1,525 extra
   trades), `bracket-partial-exit-qty-percent-01` (4,129 vs 725 trades:
   `qty_percent` exits split differently).
8. **A stale `strategy.exit` stop fills on a reversal bar (7 of 2,165
   trades in `calendar-waypoint-weekday-macd-01`):** the previous
   position's guard stop triggers in the bar the new position opens, where
   TradingView exits one bar later.
9. **Costs not modelled.** Six probes have every trade, entry and exit
   time right and are only "close" on PnL: five use `slippage=1` tick
   (`bands-constellation`, `bracket-aurora-market-atr`, `math-forge`,
   `order-horizon-next-bar-close`, `order-keystone-limit-replace`) and one
   is a stop-limit fill price (`analyzer-parity-stop-limit-timing`).
   Slippage also moves PnL in `calendar-waypoint` (−6,611 vs −13,233) and
   `commission.cash_per_contract` in `pyramid-cash-fractional`. A
   TradingView broker mode in `backtest.Options` needs tick slippage and the
   two cash commission types.

The harness is ready for the strategy team to add a CI job (opt-in, needs
network and ~570 MB) that fails when the builtin "exact + close" count
drops.

### (1) PineForge as an optional external engine: recommended, opt-in only

- **Contract.** You give it `/in/strategy.pine` (it must be `//@version=6`,
  otherwise exit 5) and `/in/ohlcv.csv`
  (`timestamp,open,high,low,close,volume`, Unix ms). It prints the JSON
  report on stdout and progress on stderr. Exit codes: 2 means an input is
  missing, 3 a C++ compile error, 4 a backtest error (with
  `{"engine":"pineforge","error":…}` on stdout), and 5 a transpile failure.
  It is configured through `PINEFORGE_INPUTS`, `PINEFORGE_OVERRIDES`,
  `PINEFORGE_INPUT_TF`, `PINEFORGE_SCRIPT_TF`, `PINEFORGE_BAR_MAGNIFIER*` and
  `PINEFORGE_SYMBOL_FEEDS` (other symbols for `request.security`).
- **Implementation.** `internal/pineforge` is new MIT code written from the
  public README and the report we produced, not from PineForge source. It
  has the same adapter shape as `internal/pyne`:
  - `Detect(ctx) (*Engine, error)` finds the container CLI
    (`$HLP_PINEFORGE_DOCKER`, else `docker`, else `podman`) and checks the
    image (`$HLP_PINEFORGE_IMAGE`, else `…/pineforge-release:1.12.0`) is
    present locally with `image inspect`. It never pulls. Errors:
    `ErrNotInstalled`, `ErrImageMissing` (with the `docker pull` command to
    show), or the CLI's own message when the daemon is down.
    `Available(ctx)` wraps it.
  - `(*Engine).Execute(ctx, src, candles, Options) (*Result, error)` writes
    `strategy.pine` and `ohlcv.csv` to a temp directory (always removed),
    runs `docker run --rm --pull=never --network=none --cap-drop=ALL
    --security-opt=no-new-privileges --memory=2g --memory-swap=2g
    --pids-limit=256 --cpus=2 --tmpfs /tmp:size=512m --name
    hlp-pineforge-<rand>` with read-only mounts and no shell (an image
    reference that starts with `-` is refused), caps stdout (128 MB) and stderr, and
    times out after 5 minutes. On timeout or cancellation it also runs
    `docker rm -f` on the named container, because killing the client does
    not stop the container. `Result` has `Trades`, `Equity`
    (`domain.Series`), `Stats`, `Bars`, `Elapsed` and the raw `Report`.
    `Options` takes `Inputs` keyed by input title or variable name (as the
    builtin engine and PYNE do), strategy `Overrides`, `Interval` /
    `ScriptInterval` (→ `PINEFORGE_INPUT_TF`/`SCRIPT_TF`) and `Vault`.
  - `(*Engine).Backtest(ctx, src, coin, iv, candles, Options)` returns the
    same `*backtest.Result` as `pyne.Backtest` (equity, fills, markers,
    tearsheet with source `backtest (pineforge)`), so `hlp backtest` can
    reuse its report and chart code. `Fills` converts trades to hlp fills.
  - Licence gates are in the adapter, not only in the CLI: `Execute`
    returns `ErrLicenseNotAccepted` unless `Engine.LicenseAccepted` is set,
    and `ErrCommercialUse` for `Options.Vault` unless
    `Engine.CommercialLicense` is set. Non-v6 scripts are refused before
    Docker starts.
  - Failures come back as `*Error` with `Kind` from the exit code
    (`missing-input`, `compile`, `backtest`, `transpile`, or `container`
    for Docker's own exit 125), `*TimeoutError` or `ErrOutputTooLarge`.
  - Unit tests use a fake shell-script `docker` that records its argv:
    detection, the gates, argv and input mapping, temp-dir cleanup, the
    timeout path including `rm -f`, the output cap, exit-code
    classification and `Backtest`. `TestLivePineforge` runs the real image
    only with `HLP_LIVE=1 HLP_PINEFORGE=1` (which also stands for the runner accepting
    the notice for that noncommercial test). **It has not been run:** the
    image was not pulled, and running it would run codegen (see §2).
- **CLI wiring (for the integrator; `internal/cli` was not edited).**
  1. `--engine builtin|pyne|pineforge|auto`. `auto` = builtin, falling back
     to PYNE when the builtin parser rejects the script and
     `pyne.Available`; **never PineForge**.
  2. `--engine pineforge`: call `pineforge.Detect`; on `ErrImageMissing`
     print the pull command it carries (hlp must not pull it). On first
     use print `pineforge.LicenseNotice` and require
     `--accept-pineforge-license` or `pineforge.license_accepted = true` in
     config, then set `Engine.LicenseAccepted`.
  3. Vault flows (`hlp pvault …`, strategy import into a vault, vault
     backtests): pass `Options{Vault: true}` and set
     `Engine.CommercialLicense` only from an explicit
     `--pineforge-commercial-license` flag; otherwise the adapter refuses
     with `ErrCommercialUse`.
  4. `hlp backtest --engine pineforge` → `Engine.Backtest`, same renderer
     as builtin/PYNE; show `Engine.Version`/`Image` in the report header
     (the adapter already adds a warning line with it).
  5. Paper/live trading: do not offer PineForge (one container run per
     bar is 5–10 s of compile). Use it as the "check against TradingView"
     engine.
  6. `hlp pine check --engine pineforge` can map `*Error{Kind:"transpile"}`
     stderr to diagnostics; the exit code is already classified.
  7. Env vars to document in `--help`: `HLP_PINEFORGE_IMAGE`,
     `HLP_PINEFORGE_DOCKER`.
- **Why a container rather than a local toolchain.** A local setup needs:
  - CMake 3.16 or later, a C++17 compiler, Eigen and Python 3.11 or later
    with codegen;
  - matching engine and codegen versions;
  - Linux as the only supported platform.

  The image packages all of that and runs on macOS and Windows through
  Docker Desktop. Advanced users can point `HLP_PINEFORGE_IMAGE` at an image
  they built themselves.
- **What it adds.** Parity with TradingView on Pine v6 is far beyond hlp's
  subset or PYNE: 7,983 of 7,989 graded probes are rated excellent by
  PineForge's own grader. It also handles UDTs, matrices, maps, the bar
  magnifier and `request.security`. Speed is a claimed median of about 807k bars/s once compiled (their benchmark),
  but each new script costs roughly 5 to 10 s to compile. Use it as the
  "check against TradingView" engine, not for live trading inside the TUI.

### (2) cgo linking to the Apache engine: not recommended

- **Legal:** fine. It is Apache-2.0 plus MPL-2.0 for Eigen; hlp would add
  their notices.
- **Practical:** no.
  - hlp's `.goreleaser.yaml` builds `CGO_ENABLED=0` for darwin, linux and
    windows on amd64 and arm64. That gives single static binaries,
    reproducible builds and a Homebrew cask.
  - cgo would need a C++17 cross-toolchain for every target, either zig-cc
    or osxcross. PineForge itself supports only Linux, ships no Windows or
    macOS archives, and relies on libstdc++/libc++ `to_chars`.
  - It would add about 20 MB of static libraries and a fast-moving ABI to
    track. Every engine release says "regenerate and relink".
- **Pointless without codegen.** The library runs *compiled strategies*.
  Turning Pine into a strategy needs the PFSL transpiler, which cannot ship
  with hlp. The only way to use the library without the transpiler is to
  hand-write strategies against the native C API (`strategy_native_*`). That
  would be a second strategy SDK, which hlp does not need.
- **Revisit only if** hlp later wants a hand-written native kernel for
  backtests. Even then, prefer a separate helper binary that hlp calls as a
  subprocess over cgo in `hlp` itself.

### lomashs09/PineForge and pineforge-platform

- **lomashs09/PineForge:** do not use it or copy from it. There is no LICENSE
  file, it only targets Pine v5, it has a small built-in library, and hlp's
  own engine already covers more.
- **pineforge-platform:** this is an LLM Pine-writing web app. hlp already
  has `internal/llm` and `internal/strategy/llm.go`. Its Apache-2.0 licence
  would allow reading it for ideas, but its Rails and Ollama stack has
  nothing hlp can reuse directly.

---

## 5. Files added by this investigation

- `internal/pinecorpus/` contains the corpus reader, fetcher and grader:
  - `corpus.go`: `Probes`, `Probe.Plain`, `Probe.TVTrades`,
    `Probe.EngineTrades`, `ReadTVTrades` (with `RangeEnd`), `ReadOHLCV`,
    `Compare` / `Diff`, `Location`
  - `fetch.go`: `Root`, `Fetch`, `CacheDir`, the pinned `Commit`
  - `feed.go`: `Resample`, `Candles`, `ChartFeed`
  - `fills.go`: `FillsToTrades`
  - `grade.go`: `Engine`, `Job`, `GradeTrades`, `Score`, `Markdown`,
    `Failures`
  - unit tests on synthetic data; live tests gated on `HLP_LIVE=1` plus `HLP_CORPUS=1` or
    `$HLP_PINECORPUS_DIR`
- `internal/pinecorpus/scoreboard/` (test-only): the builtin, PYNE and
  PineForge adapters and `TestLiveScoreboard` / `TestLiveScoreboardDebug` (both need `HLP_LIVE=1`).
- `internal/pineforge/` contains the optional engine adapter:
  - `engine.go`: `Detect`, `Available`, `Engine.Execute`, the licence
    gates and the subprocess runner
  - `backtest.go`: `Engine.Backtest`, `Fills`
  - `pineforge.go`: `Request`, `Args`, `WriteOHLCV`, `ParseReport`,
    `Error`, `LicenseNotice`
  - tests with a fake `docker`; `TestLivePineforge` gated on
    `HLP_LIVE=1 HLP_PINEFORGE=1`
- `docs/PINEFORGE.md` is this file.

Nothing else in the repository was changed, and nothing was committed.
