<div align="center">

# hlp cli

**The Hyperliquid terminal. Live charts, every vault, backtests and AI agents, in your shell.**

[![MCP: 35 tools](docs/badges/mcp.svg)](#ai-agents)
[![macOS · Linux · Windows](docs/badges/platforms.svg)](#install)
[![no keys needed](docs/badges/keys.svg)](#safety)
[![license: MIT](docs/badges/license.svg)](LICENSE)

<img src="docs/screenshots/hero.gif" alt="hlp in a terminal: the live markets dashboard for BTC, then tab through the vaults, HLP, strategies and research dashboards" width="100%">

```sh
curl -fsSL https://raw.githubusercontent.com/maxmoneycash/hlp-cli/main/install.sh | sh
```

No keys. No account. No sign-up. Then run `hlp` and type `/dash`.

Plug it into Claude Code, Cursor or Codex: `claude mcp add hlp -- hlp mcp`

[Screenshots](#screenshots) · [Try it](#try-it) · [AI agents](#ai-agents) · [Install](#install) · [Guide](docs/GUIDE.md) · [Safety](#safety)

</div>

## Screenshots

| | |
|---|---|
| <img src="docs/screenshots/desktop/01-dash-markets.png" alt="Markets dashboard: BTC chart, order book, trades, markets and funding" width="430"><br>**Markets** | <img src="docs/screenshots/desktop/02-dash-vaults.png" alt="Vaults dashboard: screener, HLP tearsheet, vault comparison and curated themes" width="430"><br>**Vaults** |
| <img src="docs/screenshots/desktop/03-dash-hlp.png" alt="HLP dashboard: HLP's live positions, a vault arena, an index and a strategy vault" width="430"><br>**HLP** | <img src="docs/screenshots/desktop/04-dash-strategies.png" alt="Strategies dashboard: a Pine and a thesis backtest with fills, a quant strategy and a dry-run order preview" width="430"><br>**Strategies** |
| <img src="docs/screenshots/desktop/05-dash-research.png" alt="Research dashboard: HYPE chart, prediction-market odds, book, trades, funding and HIP-3 markets" width="430"><br>**Research** | <img src="docs/screenshots/desktop/06-dash-markets-eth-hyperliquid.png" alt="The markets dashboard for ETH in the hyperliquid theme" width="430"><br>**Hyperliquid theme** |
| <img src="docs/screenshots/desktop/07-dash-vaults-paper.png" alt="The vaults dashboard in the paper theme" width="430"><br>**Paper theme** | <img src="docs/screenshots/desktop/08-dash-research-sol-heikin.png" alt="The research dashboard for SOL with Heikin-Ashi candles" width="430"><br>**Heikin-Ashi** |
| <img src="docs/screenshots/desktop/09-shell.png" alt="The chat shell: chart, vault and price cards, the watchlist and slash completion" width="430"><br>**Shell** | <img src="docs/screenshots/desktop/10-warp.jpg" alt="hlp in Warp over a wallpaper: BTC order book, funding, vault screener, vault arena and a multi-year BTC chart" width="430"><br>**In Warp** |

## Try it

```sh
hlp                                            # the shell: /dash, /chart HYPE 15m, /vault hlp
hlp price btc eth hype                         # mid, bid/ask, 24h change, funding
hlp chart BTC 1h                               # candles in your terminal
hlp vaults --min-tvl 100k --sort sharpe        # every vault over $100k, ranked
hlp vault compare hlp "Long HYPE & BTC"        # curves, metrics, correlation
hlp backtest --thesis "long BTC, short DOGE FARTCOIN, rebalance 1w" --days 90
hlp arena --vaults hlp,"Long HYPE & BTC"       # real vaults head to head
```

Pipe anything and it prints JSON: `hlp vaults --select name,tvl,sharpe --limit 5 | jq`

| | |
|---|---|
| [Charts](docs/GUIDE.md#charts) | Any perp, spot or HIP-3 market (`xyz:SP500`), ten themes, live order book |
| [Vaults](docs/GUIDE.md#vault-analytics) | Screener over every vault, tearsheets, compare, what HLP holds right now |
| [Backtests](docs/GUIDE.md#strategy-vaults-and-thesis-backtests) | A thesis in plain English or YAML, with Hyperliquid fees and hourly funding |
| [Pine](docs/GUIDE.md#pine-import) | Paste a TradingView strategy and run it on Hyperliquid candles |
| [Arena](docs/GUIDE.md#ai-vs-ai-arena) | LLMs, Pine scripts and theses trading one live feed on paper |

## AI agents

<img src="docs/screenshots/agent.gif" alt="A real Claude Code session with hlp over MCP: asked which vaults over $1M beat HLP risk-adjusted, it calls the vault screener and answers with a table" width="100%">

35 tools over MCP, none of which can trade. Answers come back sized for an agent's context.

```sh
claude mcp add hlp -- hlp mcp        # Claude Code
codex mcp add hlp -- hlp mcp         # Codex
```

Cursor: `{"mcpServers": {"hlp": {"command": "hlp", "args": ["mcp"]}}}` in `~/.cursor/mcp.json`.

Then ask: *"Find Hyperliquid vaults over $1M with drawdown under 20% that beat HLP risk-adjusted."*

No MCP? The CLI is the API: JSON on stdout, typed errors with hints, `hlp schema` for every input and
output. `hlp skill --install claude` adds the [skill](skills/hlp/SKILL.md). Contract: [AGENTS.md](AGENTS.md).

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/maxmoneycash/hlp-cli/main/install.sh | sh  # macOS and Linux, verifies SHA-256
go install github.com/maxmoneycash/hlp-cli/cmd/hlp@latest                               # Go 1.27+
```

<details>
<summary>Binaries, checksums, provenance, from source</summary>

Download a binary from [Releases](https://github.com/maxmoneycash/hlp-cli/releases) and check it against
`checksums.txt`. Every archive carries signed build provenance:
`gh attestation verify hlp_<version>_<os>_<arch>.tar.gz -R maxmoneycash/hlp-cli`, or run `install.sh` with
`HLP_INSTALL_VERIFY=1`. Archives include shell completions and the licences of every bundled dependency.
Binaries are not notarized yet; the Windows `.exe` is unsigned, so SmartScreen may warn on first run.

```sh
git clone https://github.com/maxmoneycash/hlp-cli && cd hlp-cli
make build          # writes bin/hlp
```

</details>

## Safety

Pre-release. All data is live mainnet data, read from public APIs. **Trading is dry-run in this
build:** orders are built, risk-checked and previewed, never sent (sending is compiled out).
Backtests, paper sessions and the arena never place orders and need no keys. No telemetry, nothing
custodial, no builder fee. Details: [Safety](docs/GUIDE.md#safety).

## More

[Guide](docs/GUIDE.md) · [AGENTS.md](AGENTS.md) · [Architecture](docs/ARCHITECTURE.md) · [Changelog](CHANGELOG.md) · [Contributing](CONTRIBUTING.md) · [Security](SECURITY.md)

Built on [Charm](https://charm.sh)'s Bubble Tea and Lip Gloss, with data from
[Hyperliquid](https://hyperliquid.xyz) and [hlp.xyz](https://www.hlp.xyz), by the hlp.xyz team.
MIT licensed. Not financial advice.
