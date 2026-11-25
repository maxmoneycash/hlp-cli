# Architecture

hlp is one Go binary with four front-ends (CLI, interactive shell, MCP server, built-in agent) over a
single tool registry. This page is the map for contributors.

## One registry, four front-ends

```
           ┌────────────── core.Registry (internal/core/catalog.go) ──────────────┐
 argv ───▶ │ cli: cobra command ─┐                                                 │
 "/chart"▶ │ tui: slash command ─┼─▶ ParseArgs ─▶ Invoke ─▶ Tool.Run(ctx, env, in) ─▶ Result{Card, Data, Meta}
 MCP ────▶ │ mcp: tools/call ────┤                  │ mutating? ─▶ Guard ─▶ Confirm ─▶ exchange (dry-run)
 LLM ────▶ │ agent: tool call ───┘                                                 │
           └───────────────────────────────────────────────────────────────────────┘
 Result ─▶ cli: JSON envelope (pipe) or a rendered card (terminal)
           tui: card in the transcript or full screen
           mcp: structuredContent {data, meta} plus card text and MCP Apps views
           agent: compact JSON back to the model
```

Adding a feature means one input struct, one `Tool` and one card renderer. It then appears as a CLI
command, a slash command, an MCP tool and an agent tool at once. The JSON shape of every tool is
pinned by a snapshot test (`internal/core/testdata/contract.json`); any breaking rename bumps the
contract version documented in [AGENTS.md](../AGENTS.md).

## Packages

```
cmd/hlp/                  main: signals, build info, cli.Execute
internal/
  domain/                 shared value types (stdlib only)
  config/                 config.toml, XDG paths, secrets (env or OS keychain), risk limits
  theme/                  palettes and Lip Gloss styles
  hyperliquid/            /info client, stats-data, market registry, rate limiter, history paging
    ws/                   WebSocket streams with ref-counted subscriptions
    exchange/             msgpack actions, EIP-712 signing, gated (dry-run) posting
  hyperevm/               HyperEVM reads: Safe and CoreWriter dry-runs
  evm/                    minimal JSON-RPC and ABI helpers
  hlpxyz/                 hlp.xyz public API client and mappers
  registry/               curated vault registry (vaults.toml), user overrides
  metrics/                performance math: TWR, modified Dietz, drawdown, Sharpe, checklist
  pvault/                 strategy-vault reads and manipulation-risk estimate
  chart/                  terminal candlesticks, lines, depth, sparklines
  chartsvg/               SVG charts for MCP Apps views
  ticks/                  tick, volume and range bars from the live trade stream, local history
  llm/                    model providers (Anthropic, OpenAI-compatible, OpenRouter, Ollama)
  agent/                  tool-calling loop and deterministic intent parser
  macro/                  prediction-market context for LLM strategies
  strategy/               strategy contract and the LLM decide loop
    pine/                 Pine Script v5/v6 interpreter
    thesis/               thesis YAML or plain English to target weights
    backtest/             historical engine producing a tearsheet
    paper/                live paper engine and the arena
  paperx/                 local paper exchange on the live book
  pyne/, pineforge/       optional external Pine engines (run as separate programs)
  pinecorpus/, pinesrc/   Pine conformance corpus and grading
  core/                   the tool registry, Env, errors and the guard
  cli/                    commands, output contract, MCP stdio server
  tui/                    Bubble Tea shell, panes and keymap
    cards/                Result to styled card (shared with CLI terminal output)
examples/                 example Pine scripts, theses and LLM strategies
skills/hlp/               agent skill with task recipes
tools/                    screenshot rig and an MCP Apps test host
testdata/                 recorded API fixtures and Pine samples used by tests
```

## Dependency rule

```
domain  <-  theme  <-  chart
domain  <-  config
domain  <-  hyperliquid (+ws, +exchange)      domain <- hlpxyz      domain <- metrics
config  <-  llm  <-  strategy (+pine, thesis, backtest, paper)
everything above  <-  core  <-  agent  <-  tui (+cards)  <-  cli  <-  cmd/hlp
```

- `strategy` never imports `core`, `agent` or `hyperliquid/exchange`, so strategies are paper-only by
  construction.
- `chart` imports only `domain` and `theme`.
- `core` is the only package that wires concrete clients together.

## Runtime data flow

- **Startup:** `cli` builds `core.Env` (config, secrets, Hyperliquid client, a lazily dialled
  WebSocket stream, the hlp.xyz client, guard and theme) and starts the shell. The first frame renders
  before any network call returns; cards show skeletons and fill in.
- **Market registry:** meta, perp dexs (HIP-3) and spot meta, cached on disk for an hour.
- **Vaults:** the stats-data snapshot (ETag aware) merged with hlp.xyz metrics, cached in
  `~/.cache/hlp/`.
- **Live data:** one market socket and one account socket. Subscriptions are ref-counted per visible
  card and dropped when hidden.
- **Rate limits:** a shared token bucket for Hyperliquid's weight budget and a client-side limit for
  hlp.xyz.

## Safety model

1. Read-only by default. No keys are needed for anything except trading.
2. Trading uses an agent (API) wallet, which can trade but cannot withdraw. hlp never stores a master
   key.
3. Keys come from the environment or the OS keychain only: never argv, config files or logs.
4. Order paths are dry-run in this build: orders are built, signed with the agent key if present,
   risk-checked and previewed, never sent.
5. Every mutating tool passes the guard (max notional, max leverage, allowed coins, daily loss) and
   then a confirm step: a card in the shell, a TTY prompt in the CLI, or an explicit `yes` over MCP.
6. MCP trading tools stay hidden unless both config and a flag allow them. The agent cannot confirm
   for itself.
7. No telemetry.

## Tests

`make check` runs gofmt, vet and the unit tests. Unit tests never touch the network: they replay
fixtures from `testdata/`. Tests that need the network or external engines are behind build tags or
environment variables and are skipped by default.
