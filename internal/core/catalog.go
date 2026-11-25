package core

import (
	"slices"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// Input structs: the public, versioned contract of every command. Field json
// names are the CLI flag names (kebab-cased by cli) and MCP argument names.
// Common output flags (--json, --select, --limit, --ndjson, --plain) are
// handled by the cli layer, not here.

// MarketsInput is the input of get_markets (`hlp markets`).
type MarketsInput struct {
	Kind   domain.MarketKind `json:"kind,omitempty" jsonschema:"filter: perp, hip3, spot or outcome; empty = perps on all dexes"`
	Dex    string            `json:"dex,omitempty" jsonschema:"HIP-3 dex name, e.g. xyz"`
	Query  string            `json:"query,omitempty" jsonschema:"filter on coin or display name (exact, prefix, then substring; fuzzy for longer queries), e.g. 'S&P' or 'nvda'"`
	Sort   string            `json:"sort,omitempty" jsonschema:"volume (default), change, funding, oi, name"`
	Limit  int               `json:"limit,omitempty" jsonschema:"max rows, default 50"`
	Offset int               `json:"offset,omitempty" jsonschema:"skip this many rows before limit (paging)"`
}

// ChartInput is the input of get_candles (`hlp chart`).
type ChartInput struct {
	Coin     string          `json:"coin" jsonschema:"market, e.g. BTC, HYPE, xyz:SP500, @107; case-insensitive"`
	Interval domain.Interval `json:"interval,omitempty" jsonschema:"1m 3m 5m 15m 30m 1h 2h 4h 8h 12h 1d 3d 1w 1M; default 15m"`
	Bars     int             `json:"bars,omitempty" jsonschema:"candles to fetch (max 5000); default fits the width"`
	Width    int             `json:"width,omitempty" jsonschema:"columns; default terminal width or 100"`
	Height   int             `json:"height,omitempty" jsonschema:"chart rows; default 30 (CLI on a terminal: min(30, terminal rows - 4))"`
	Style    string          `json:"style,omitempty" jsonschema:"classic (default), solid, hollow, ohlc, line, heikin, blocks, lines, half, mono, ascii"`
	Zoom     int             `json:"zoom,omitempty" jsonschema:"columns per candle: 1, 2, 4, 6"`
	MA       []int           `json:"ma,omitempty" jsonschema:"SMA periods to overlay, e.g. [20,50]"`
	EMA      []int           `json:"ema,omitempty" jsonschema:"EMA periods to overlay"`
	Address  string          `json:"address,omitempty" jsonschema:"overlay this account's or vault's entry, liquidation and orders"`
	Live     bool            `json:"live,omitempty" jsonschema:"stream updates (TUI/CLI only)"`
}

// BookInput is the input of get_orderbook (`hlp book`).
type BookInput struct {
	Coin     string `json:"coin" jsonschema:"market"`
	Depth    int    `json:"depth,omitempty" jsonschema:"levels per side, max 20, default 10"`
	NSigFigs int    `json:"sigfigs,omitempty" jsonschema:"price aggregation: 2-5, 0 = full precision"`
	Live     bool   `json:"live,omitempty" jsonschema:"stream updates (CLI/TUI; MCP streams are bounded)"`
}

// CoinInput is a single-market input (`hlp trades`, `hlp asset-id`).
type CoinInput struct {
	Coin    string `json:"coin" jsonschema:"market"`
	Limit   int    `json:"limit,omitempty" jsonschema:"max trades"`
	Batches bool   `json:"batches,omitempty" jsonschema:"stream each full exchange batch after the snapshot; empty WebSocket batches mark subscription readiness or a stale connection gap"`
	Live    bool   `json:"live,omitempty" jsonschema:"stream updates (CLI/TUI; MCP streams are bounded)"`
}

// AssetIDInput is the input of get_asset_id: just the market.
type AssetIDInput struct {
	Coin string `json:"coin" jsonschema:"market, e.g. BTC, HYPE, xyz:SP500"`
}

// FundingInput is the input of get_funding (`hlp funding`).
type FundingInput struct {
	Coin  string `json:"coin,omitempty" jsonschema:"one market's funding history; empty = ranking across markets"`
	Hours int    `json:"hours,omitempty" jsonschema:"history lookback hours, default 72"`
	Top   int    `json:"top,omitempty" jsonschema:"ranking size, default 20 (most positive and most negative)"`
}

// AddressInput is an account input (`hlp account`, `hlp watch`).
type AddressInput struct {
	Address string `json:"address" jsonschema:"0x address of a user, sub-account, legacy vault or HyperEVM vault contract; default = config account"`
	Live    bool   `json:"live,omitempty" jsonschema:"stream updates (CLI/TUI; MCP streams are bounded)"`
}

// FillsInput is the input of get_fills (`hlp fills`).
type FillsInput struct {
	Address string `json:"address" jsonschema:"account or vault address"`
	Hours   int    `json:"hours,omitempty" jsonschema:"lookback, default 24"`
	Coin    string `json:"coin,omitempty"`
	Limit   int    `json:"limit,omitempty" jsonschema:"max fills, newest first"`
}

// VaultsInput is the input of list_vaults (`hlp vaults`).
type VaultsInput struct {
	Query         string   `json:"query,omitempty" jsonschema:"fuzzy match on vault name or address"`
	Sort          string   `json:"sort,omitempty" jsonschema:"tvl (default), apr, ret7d, ret30d, retall, sharpe, maxdd, age"`
	MinTVL        float64  `json:"minTvl,omitempty" jsonschema:"USD, default 10000"`
	MaxDD         float64  `json:"maxDd,omitempty" jsonschema:"max drawdown cap: percent (20) or fraction (0.2)"`
	MinAgeDays    int      `json:"minAge,omitempty"`
	OpenOnly      bool     `json:"open,omitempty" jsonschema:"only vaults accepting deposits"`
	IncludeClosed bool     `json:"includeClosed,omitempty" jsonschema:"include closed vaults in directory and name results; open still excludes them"`
	IncludeHLP    bool     `json:"hlp,omitempty" jsonschema:"include HLP and its child vaults (default true in TUI)"`
	CuratedOnly   bool     `json:"curated,omitempty" jsonschema:"only vaults curated by hlp.xyz"`
	Tags          []string `json:"tag,omitempty" jsonschema:"only curated vaults with every one of these tags (vaults.toml), e.g. memes, trend, market-neutral; lifts the TVL floor"`
	ExcludeTags   []string `json:"notTag,omitempty" jsonschema:"drop curated vaults with any of these tags"`
	Kind          string   `json:"kind,omitempty" jsonschema:"curated kind: hypercore or hyperevm (indexes and strategies: use discover_vaults)"`
	Limit         int      `json:"limit,omitempty" jsonschema:"default 50"`
	Offset        int      `json:"offset,omitempty" jsonschema:"skip this many rows before limit (paging)"`
}

// VaultInput is the input of get_vault (`hlp vault`).
type VaultInput struct {
	Vault      string   `json:"vault" jsonschema:"vault address or (fuzzy) name, e.g. 'Long HYPE & BTC'"`
	Benchmarks []string `json:"bench,omitempty" jsonschema:"comparison curves, default HLP,BTC,HYPE; also SPY, NQ, ETH, SOL"`
	Window     string   `json:"window,omitempty" jsonschema:"1w, 1m, 3m, 6m, ytd, 1y, all (default)"`
	Fills      bool     `json:"fills,omitempty" jsonschema:"include recent fills"`
	Depositors bool     `json:"depositors,omitempty" jsonschema:"include the depositor list (top 100) and flows"`
}

// CompareInput is the input of compare_vaults (`hlp vault compare`).
type CompareInput struct {
	Vaults []string `json:"vaults" jsonschema:"2-6 vault addresses or names"`
	Window string   `json:"window,omitempty"`
}

// BacktestInput is the input of run_backtest (`hlp backtest`).
type BacktestInput struct {
	File     string          `json:"file,omitempty" jsonschema:"path to a .pine strategy or a thesis .yaml/.json, or example:<name> (e.g. example:ema-cross)"`
	Source   string          `json:"source,omitempty" jsonschema:"inline Pine source instead of file (max 256 KiB)"`
	Thesis   string          `json:"thesis,omitempty" jsonschema:"inline thesis, e.g. 'long xyz:SP500 1.5x, short DOGE FARTCOIN TRUMP, rebalance 1w'"`
	Coin     string          `json:"coin,omitempty" jsonschema:"market for single-asset Pine strategies"`
	Interval domain.Interval `json:"interval,omitempty"`
	Days     int             `json:"days,omitempty" jsonschema:"lookback days (bounded by Hyperliquid's ~5000-candle history)"`
	Capital  float64         `json:"capital,omitempty" jsonschema:"USD, default 10000"`
	FeesBps  float64         `json:"feesBps,omitempty" jsonschema:"taker fee bps, default 4.5"`
	Bench    []string        `json:"bench,omitempty" jsonschema:"comparison curves, default HLP,BTC,HYPE; the first one you pass is also the vsBench benchmark (default: equal weight of the strategy's coins)"`
	EngineOpt
	LLMGuard
}

// EngineOpt selects the Pine engine; embedded in every strategy input so the
// CLI flags (--engine, --accept-pineforge-license) and MCP arguments appear
// without per-tool code. Resolution lives in engine.go (WP-B).
type EngineOpt struct {
	Engine                 string `json:"engine,omitempty" jsonschema:"builtin, pyne, pineforge or auto (default builtin)"`
	AcceptPineforgeLicense bool   `json:"acceptPineforgeLicense,omitempty" jsonschema:"accept the PineForge licence notice for this run (or set pineforge.accept_license)"`
}

// PineInput is the input of pine_check (`hlp pine check`).
type PineInput struct {
	File     string          `json:"file,omitempty" jsonschema:"path to a .pine file, or example:<name> (e.g. example:ema-cross)"`
	Source   string          `json:"source,omitempty" jsonschema:"inline Pine source instead of file (max 256 KiB)"`
	Coin     string          `json:"coin,omitempty"`
	Interval domain.Interval `json:"interval,omitempty"`
	EngineOpt
}

// PineRunInput is the input of run_pine (`hlp pine run`): a Pine strategy
// backtested on one market, with its fills drawn on the chart.
type PineRunInput struct {
	File     string          `json:"file,omitempty" jsonschema:"path to a .pine strategy, or example:<name> (e.g. example:ema-cross)"`
	Source   string          `json:"source,omitempty" jsonschema:"inline Pine source instead of file (max 256 KiB)"`
	Coin     string          `json:"coin" jsonschema:"market to run on, e.g. BTC, HYPE, xyz:SP500"`
	Interval domain.Interval `json:"interval,omitempty" jsonschema:"bar interval, default 1h"`
	Days     int             `json:"days,omitempty" jsonschema:"lookback days (bounded by Hyperliquid's ~5000-candle history)"`
	Capital  float64         `json:"capital,omitempty" jsonschema:"starting capital in USD (default 10000)"`
	FeesBps  float64         `json:"feesBps,omitempty" jsonschema:"taker fee in bps (default 4.5)"`
	EngineOpt
}

// PaperInput is the input of paper (`hlp paper start|status|stop|list`).
type PaperInput struct {
	Action  string  `json:"action" jsonschema:"start, status, stop, list"`
	ID      string  `json:"id,omitempty"`
	File    string  `json:"file,omitempty" jsonschema:"strategy for start: .pine, thesis yaml, or llm:<config.yaml>"`
	Coin    string  `json:"coin,omitempty"`
	Capital float64 `json:"capital,omitempty"`
	EngineOpt
	LLMGuard
}

// ArenaInput is the input of arena (`hlp arena`).
type ArenaInput struct {
	File     string   `json:"file,omitempty" jsonschema:"arena YAML (examples/arena/*.yaml): entrants with paths relative to it"`
	Entrants []string `json:"entrants,omitempty" jsonschema:"strategy files (.pine@COIN, thesis yaml) and/or llm:<bot.yaml|provider:model> entries; llm:openrouter/<vendor>/<model> runs any OpenRouter model on one OPENROUTER_API_KEY"`
	Vaults   []string `json:"vaults,omitempty" jsonschema:"watch real vaults head to head instead"`
	Capital  float64  `json:"capital,omitempty"`
	Bench    string   `json:"bench,omitempty" jsonschema:"buy-and-hold benchmark for vsBench: ew (equal weight of the entrants' coins, default), BTC, HYPE, SP500 or any market"`
	EngineOpt
	LLMGuard
}

// IndexInput is the input of list_indexes (`hlp index`).
type IndexInput struct {
	Slug          string `json:"slug,omitempty" jsonschema:"index slug (mavericks, stillwater, hlp20); empty = list"`
	History       bool   `json:"history,omitempty" jsonschema:"include on-demand modeled history from bulk v6 vault curves"`
	HistoryMethod string `json:"historyMethod,omitempty" jsonschema:"index NAV valuation: observed (default, prior source samples) or website (public-client stitched/interpolated model); requires history"`
	Constraints   bool   `json:"constraints,omitempty" jsonschema:"include on-demand dated deposit-capacity, tracked-since and deposit-closure source observations; not a verified constraint replay"`
	Exposure      bool   `json:"exposure,omitempty" jsonschema:"include on-demand dated member-account equity, gross/net notional and margin observations in USD; not index-weighted exposure"`
	Window        string `json:"window,omitempty" jsonschema:"history lookback: 1d, 1w, 1m, 3m, 6m, ytd, 1y, all (default all)"`
	Points        int    `json:"points,omitempty" jsonschema:"maximum displayed points per history series, 2-5000 (default 400); metrics use all source points"`
}

// VaultHistoryInput requests two bulk sources, never a detail call per vault.
type VaultHistoryInput struct {
	Window string `json:"window,omitempty" jsonschema:"lookback: 1d, 1w, 1m, 3m, 6m, ytd, 1y, all (default all)"`
	Points int    `json:"points,omitempty" jsonschema:"maximum displayed points per series, 2-5000 (default 400); monthly returns use all source points"`
}

// StrategyInput is the input of get_strategy (`hlp strat`).
type StrategyInput struct {
	ID string `json:"id,omitempty" jsonschema:"Strategy id: trend, spxmeme, bluechip, dilution, lotto, memeshort (hlp.xyz quant); empty = list"`
}

// SchemaInput is the input of schema (`hlp schema`).
type SchemaInput struct {
	Tool string `json:"tool,omitempty" jsonschema:"tool or command name; empty = list all"`
}

// OrderInput is the single mutating order entry point (CLI `hlp buy/sell`,
// slash `/buy`, agent `place_order`, MCP only with --allow-trading).
type OrderInput struct {
	Coin       string  `json:"coin"`
	Side       string  `json:"side" jsonschema:"buy or sell"`
	Size       string  `json:"size,omitempty" jsonschema:"base units as a decimal string"`
	USD        float64 `json:"usd,omitempty" jsonschema:"notional in USD (alternative to size)"`
	Px         string  `json:"px,omitempty" jsonschema:"limit price; empty = market (IOC at mid +/- slippage)"`
	TP         string  `json:"tp,omitempty" jsonschema:"take profit: price or +5%"`
	SL         string  `json:"sl,omitempty" jsonschema:"stop loss: price or -2%"`
	ReduceOnly bool    `json:"reduceOnly,omitempty"`
	Leverage   int     `json:"leverage,omitempty"`
	Isolated   bool    `json:"isolated,omitempty" jsonschema:"isolated margin with --leverage (default keeps the position's mode, else cross)"`
	Vault      string  `json:"vault,omitempty" jsonschema:"trade on behalf of this vault (leader's agent)"`
	DryRun     bool    `json:"dryRun,omitempty" jsonschema:"preview only, never send; without it the order is refused unless trading is enabled"`
	Yes        bool    `json:"yes,omitempty" jsonschema:"skip the interactive confirm (risk caps still apply)"`
	// Paper routes the order to the local paper exchange (live book, no
	// keys, nothing sent); PaperAccount picks the account (default
	// "default").
	Paper        bool   `json:"paper,omitempty" jsonschema:"place it on a local paper account against the live book instead (no keys; nothing is sent)"`
	PaperAccount string `json:"paperAccount,omitempty" jsonschema:"paper account id with paper (default 'default')"`
}

// CancelInput is the input of cancel_order (`hlp cancel`, mutating).
type CancelInput struct {
	Coin   string `json:"coin,omitempty"`
	OID    uint64 `json:"oid,omitempty" jsonschema:"order id; empty with coin = cancel all on coin"`
	Vault  string `json:"vault,omitempty"`
	DryRun bool   `json:"dryRun,omitempty"`
	Yes    bool   `json:"yes,omitempty"`
}

// CloseInput is the input of close_position (`hlp close`, mutating).
type CloseInput struct {
	Coin   string  `json:"coin,omitempty" jsonschema:"market to flatten; empty with all=true closes every position"`
	Pct    float64 `json:"pct,omitempty" jsonschema:"percent of the position to close (default 100)"`
	All    bool    `json:"all,omitempty" jsonschema:"close every open position (asks twice)"`
	Vault  string  `json:"vault,omitempty" jsonschema:"close on behalf of this vault (leader's agent)"`
	DryRun bool    `json:"dryRun,omitempty" jsonschema:"sign and print, never send"`
	Yes    bool    `json:"yes,omitempty" jsonschema:"skip the interactive confirm (risk caps still apply)"`
}

// VaultTransferInput is the input of vault_deposit and vault_withdraw (mutating).
type VaultTransferInput struct {
	Vault  string  `json:"vault"`
	USD    float64 `json:"usd"`
	DryRun bool    `json:"dryRun,omitempty"`
	Yes    bool    `json:"yes,omitempty"`
}

func in[T any]() func() any { return func() any { return new(T) } }

// catalog is the launch tool set.
func catalog() []Tool {
	return append([]Tool{
		// market
		{Name: "get_markets", Command: "markets", Slash: "markets", Positional: []string{"query"}, Group: "market", NewInput: in[MarketsInput](), Run: runMarkets,
			Description: "Every live market with mark price, 24h change (fraction), hourly funding (fraction), open interest (base units) and 24h USD volume. Default: perps on all dexes sorted by volume. Use query for fuzzy search ('S&P', 'nvda').",
			Summary:     "List perps, HIP-3, spot markets with price, 24h change, funding, OI, volume",
			Examples:    []string{"hlp markets", "hlp markets nvda", "hlp markets --sort change --limit 10"}},
		{Name: "get_candles", Command: "chart", Slash: "chart", Aliases: []string{"c"}, Positional: []string{"coin", "interval"}, Group: "market", Streaming: true, NewInput: in[ChartInput](), Run: runChart, Stream: streamChart,
			Description: "OHLCV candles for one market plus a numeric summary (last, window open/high/low, change as a fraction, moving averages) and a plain-text chart. Read summary for numbers; plain is a rendered picture. Candle times are open times in ms; v is base volume.",
			Summary:     "Candlestick chart with volume, MAs and a numeric summary",
			Examples:    []string{"hlp chart btc 1h", "hlp chart hype 4h --ma 20,50 --height 40", "hlp chart eth --style ascii --zoom 1"}},
		{Name: "get_orderbook", Command: "book", Slash: "book", Aliases: []string{"b"}, Positional: []string{"coin"}, Group: "market", Streaming: true, NewInput: in[BookInput](), Run: runBook, Stream: streamBook,
			Description: "L2 order book snapshot: bids descending, asks ascending, each {px, sz (base), n (orders)}.",
			Summary:     "L2 order book with depth bars and spread",
			Examples:    []string{"hlp book btc", "hlp book eth --depth 20 -w", "hlp book btc --ndjson --max-events 3"}},
		{Name: "get_trades", Command: "trades", Slash: "trades", Positional: []string{"coin"}, Group: "market", Streaming: true, NewInput: in[CoinInput](), Run: runTrades, Stream: streamTrades,
			Description: "Recent public trades, newest first. side B = buyer was the aggressor, A = seller.",
			Summary:     "Recent public trades (tape)",
			Examples:    []string{"hlp trades btc --limit 20", "hlp trades hype -w"}},
		{Name: "get_funding", Command: "funding", Slash: "funding", Aliases: []string{"f"}, Positional: []string{"coin"}, Group: "market", NewInput: in[FundingInput](), Run: runFunding,
			Description: "Without coin: perps ranked by current hourly funding (most positive first, then most negative); APR = funding*24*365. With coin: that market's funding history (hourly rates as fractions).",
			Summary:     "Funding ranking across markets, or one market's history (hourly, APR)",
			Examples:    []string{"hlp funding", "hlp funding btc --hours 24"}},
		{Name: "get_asset_id", Command: "asset-id", Slash: "asset", Positional: []string{"coin"}, Group: "market", NewInput: in[AssetIDInput](), Run: runAssetID,
			Description: "Resolve user input (btc, SPY, HYPE/USDC, @107) to the wire coin, asset id, size decimals, dex and max leverage.",
			Summary:     "Resolve a market to its wire coin, asset id, decimals and dex"},

		// account (read-only by address; no key needed)
		{Name: "get_account", Command: "account", Slash: "account", Aliases: []string{"positions", "pos"}, Positional: []string{"address"}, Group: "account", Streaming: true, NewInput: in[AddressInput](), Run: runAccount, Stream: streamAccount,
			Description: "Read-only view of any address (user, vault, sub-account): equity = perps on every dex + spot USD + Earn supply, positions with entry, PnL, leverage and liquidation price, open orders. No key needed.",
			Summary:     "Equity, positions, orders for any address (all dexes + spot + Earn)",
			Examples:    []string{"hlp account 0x…", "hlp account 0x… --select positions.coin,positions.size", "hlp account 0x… -w"}},
		{Name: "get_fills", Command: "fills", Slash: "fills", Positional: []string{"address"}, Group: "account", NewInput: in[FillsInput](), Run: runFills,
			Description: "Executions for any address over a lookback window, newest first, with closed PnL and fees in USD.",
			Summary:     "Recent fills for any address"},
		{Name: "watch", Command: "watch", Slash: "watch", Aliases: []string{"w"}, Positional: []string{"address"}, Group: "account", Streaming: true, NewInput: in[AddressInput](), Run: runWatch, Stream: streamWatch,
			Description: "Snapshot of an account or vault; streaming surfaces then follow positions and fills live over WebSocket.",
			Summary:     "Spectate any account or vault live: positions, PnL, fills on the chart",
			Examples:    []string{"hlp watch 0x…", "hlp watch 0x… --ndjson --for 1m"}},

		// vaults
		{Name: "get_vault_history", Command: "vault-history", Slash: "vault-history", Group: "vault", NewInput: in[VaultHistoryInput](), Run: runVaultHistory,
			Description: "Dated curated-vault equity and v6 NAV from two bulk sources. Monthly returns carry UTC boundaries and partial/gap status. Historical equity is not live TVL; source ages and incomplete coverage are explicit.",
			Summary:     "Bulk dated vault histories for directory statistics and monthly returns"},
		{Name: "list_vaults", Command: "vaults", Slash: "vaults", Aliases: []string{"v"}, Positional: []string{"query"}, Group: "vault", NewInput: in[VaultsInput](), Run: runVaults,
			Description: "Vault screener over every Hyperliquid vault merged with the hlp.xyz curated list. Returns are fractions per window (1d..all); maxDD is a negative fraction; curated rows use the hlp.xyz v6 share-price model (sharePriceModel says which). Defaults: TVL >= $10k, closed and HLP child vaults hidden, sorted by TVL. A query lifts the TVL floor.",
			Summary:     "Vault screener: TVL, APR, returns, max DD, Sharpe, age, capacity, sparkline",
			Examples:    []string{"hlp vaults --sort sharpe --limit 10", "hlp vaults \"long hype\"", "hlp vaults --json --select name,tvl,apr | jq"}},
		{Name: "get_vault", Command: "vault", Slash: "vault", Positional: []string{"vault"}, Group: "vault", NewInput: in[VaultInput](), Run: runVault,
			Description: "Full tearsheet for one vault by address or fuzzy name: share-price curve (model labelled), drawdown, monthly returns, metrics (CAGR, Sharpe, Sortino, max DD, Calmar) vs benchmarks (default HLP, BTC, HYPE), due-diligence checklist, capacity under the leader 5% rule, positions.",
			Summary:     "Vault tearsheet: equity/NAV curve, drawdown, monthly returns, metrics vs HLP/BTC/HYPE, checklist",
			Examples:    []string{"hlp vault hlp", "hlp vault \"Long HYPE & BTC\" --window 3m", "hlp vault growi --select tearsheet.metrics"}},
		{Name: "compare_vaults", Command: "vault compare", Slash: "compare", Positional: []string{"vaults"}, Group: "vault", NewInput: in[CompareInput](), Run: runCompare,
			Description: "Compare 2-6 vaults: normalized curves, metrics and the correlation matrix of their period returns.",
			Summary:     "Overlay vault curves with metrics and a correlation matrix",
			Examples:    []string{"hlp vault compare hlp growi", "hlp vault compare 0x… 0x… --window 1y"}},
		{Name: "hlp_positioning", Command: "hlp", Slash: "hlp", Group: "vault", Streaming: true, NewInput: in[struct{}](), Run: runHLP, Stream: streamHLP,
			Description: "HLP net positioning: positions of the HLP parent and its child vaults netted per coin (signed size, USD value, PnL), plus combined equity.",
			Summary:     "What HLP is long and short right now, across its 7 child vaults"},
		{Name: "list_indexes", Command: "index", Slash: "index", Positional: []string{"slug"}, Group: "vault", NewInput: in[IndexInput](), Run: runIndex,
			Description: "hlp.xyz vault indexes (rules-based vault baskets). Without slug: the list; with slug: definition and allocation history.",
			Summary:     "Vault indexes from hlp.xyz (rules-based baskets of vaults)"},
		{Name: "discover_vaults", Command: "vaults discover", Slash: "discover", Aliases: []string{"tags"}, Positional: []string{"tag"}, Group: "vault", NewInput: in[DiscoverInput](), Run: runDiscover,
			Description: "The curated vault registry (vaults.toml: HyperCore vaults, HyperEVM strategy vaults, hlp.xyz indexes and quant strategies) grouped by kind and by tag: themes, styles, risk, venue. For live numbers list vaults by tag (`hlp vaults --tag <tag>`; MCP: list_vaults with tag). Snapshot numbers in entries are percents from the registry's generation date, never live data.",
			Summary:     "Browse curated vaults by thesis: tags (memes, trend, market-neutral…), kinds and counts"},
		{Name: "get_strategy", Command: "strat", Slash: "strat", Positional: []string{"id"}, Group: "vault", NewInput: in[StrategyInput](), Run: runStrat,
			Description: "An hlp.xyz quant strategy: the live target book (signed weights as fractions of NAV) and the static backtest. Without id: the list of strategies with their backtest CAGR, Sharpe and max drawdown. Ids: trend, spxmeme, bluechip, dilution, lotto, memeshort.",
			Summary:     "Quant strategy from hlp.xyz: live target book and backtest (spxmeme = long S&P, short memes)"},

		// strategy
		{Name: "run_backtest", Command: "backtest", Slash: "backtest", Aliases: []string{"bt"}, Positional: []string{"file"}, Group: "strategy", NewInput: in[BacktestInput](), Run: runBacktest,
			Description: "Backtest a Pine strategy (needs coin) or a thesis basket (YAML file or inline text) on Hyperliquid candles with fees, slippage and hourly funding. Returns a tearsheet vs benchmarks (default HLP, BTC, HYPE), fills and warnings. Paper math only; nothing is traded.",
			Summary:     "Backtest a Pine strategy or a thesis basket on Hyperliquid candles; tearsheet vs HLP",
			Examples:    []string{"hlp backtest examples/thesis/long-spy-short-memes.yaml", "hlp backtest --thesis \"long BTC, short DOGE\" --days 90", "hlp backtest examples/pine/ema-cross.pine --coin BTC --days 90 --engine auto"}},
		{Name: "pine_check", Command: "pine check", Slash: "pine", Positional: []string{"file"}, Group: "strategy", NewInput: in[PineInput](), Run: runPineCheck,
			Description: "Parse a .pine file against hlp's Pine v5/v6 subset. Errors come back as USAGE with details [{line, col, msg}]; pass coin to also compile.",
			Summary:     "Parse a Pine Script file and report unsupported features with line:col"},
		{Name: "run_pine", Command: "pine run", Positional: []string{"file"}, Group: "strategy", NewInput: in[PineRunInput](), Run: runPine,
			Description: "Run a Pine v5/v6 strategy on one Hyperliquid market (coin required, interval default 1h) with fees, slippage and funding. Same tearsheet as run_backtest plus the price chart with entry/exit markers. Paper math only.",
			Summary:     "Run a Pine strategy on one market: tearsheet plus a chart of its fills"},
		{Name: "paper", Command: "paper", Slash: "paper", Positional: []string{"action", "file"}, Group: "strategy", Streaming: true, LocalState: true, NewInput: in[PaperInput](), Run: runPaper, Stream: streamPaper,
			Description: "Paper trading on live data. action start (file + coin for Pine), status, stop, list. Sessions persist locally; no keys, no orders.",
			Summary:     "Run a strategy in paper mode on live data (no keys, no risk)"},
		{Name: "arena", Command: "arena", Slash: "arena", Positional: []string{"file"}, Group: "strategy", Streaming: true, LocalState: true, NewInput: in[ArenaInput](), Run: runArena, Stream: streamArena,
			Description: "Leaderboard of strategies running in paper on one live feed (entrants: .pine@COIN, thesis yaml, llm:<model>), or real vaults head to head over 30 days (vaults).",
			Summary:     "Arena: AI vs AI strategies or real vaults head to head on one live feed"},
		{Name: "list_llm_models", Command: "arena models", Slash: "models", Positional: []string{"query"}, Group: "strategy", NewInput: in[ModelsInput](), Run: runModels,
			Description: "OpenRouter's public model catalog (no key needed, cached 1 h): id, name, USD per 1M input and output tokens, context length, and the arena entrant spelling llm:openrouter/<id>. One OPENROUTER_API_KEY runs any of them in `hlp arena`.",
			Summary:     "LLM models for the arena (OpenRouter catalog, price per 1M tokens)",
			Examples:    []string{"hlp arena models claude", "hlp arena models --sort price --limit 20", "hlp arena models --free"}},
		{Name: "get_macro", Command: "macro", Slash: "macro", Positional: []string{"coins"}, Group: "market", NewInput: in[MacroInput](), Run: runMacro,
			Description: "Curated prediction-market odds that LLM strategies see as context in paper and arena runs: Hyperliquid HIP-4 outcome markets, Polymarket and Kalshi (public, cached >= 5 min, fails soft). prob is the implied YES probability (0..1), move24h its 24h change in probability points. Context, not a trading signal. prompt is the exact block a bot receives. Off with config macro.disabled.",
			Summary:     "Prediction-market macro context (HIP-4, Polymarket, Kalshi) fed to LLM bots",
			Examples:    []string{"hlp macro", "hlp macro BTC xyz:XYZ100", "hlp macro --json"}},

		// trade (mutating; dry-run by default; Phase-2 gate)
		{Name: "place_order", Command: "order", Slash: "buy", Aliases: []string{"sell", "long", "short"}, Positional: []string{"side", "coin", "size"}, Group: "trade", Mutating: true, NewInput: in[OrderInput](), Run: runOrder,
			Description: "Build, risk-check and sign an order. This build never sends: the result is a dry-run preview with the signed envelope. Needs trading enabled and an agent key; --dry-run alone previews unsigned. Off-grid prices fail with INVALID_TICK and the nearest valid prices. tp/sl accept prices or percent P&L (+5%, -2%).",
			Summary:     "Place an order (dry-run unless trading is enabled; risk caps always apply)",
			Examples:    []string{"hlp buy hype --usd 100 --dry-run", "hlp order sell btc 0.01 --px 99000 --dry-run"}},
		{Name: "cancel_order", Command: "cancel", Slash: "cancel", Positional: []string{"coin", "oid"}, Group: "trade", Mutating: true, NewInput: in[CancelInput](), Run: runCancel,
			Description: "Cancel one order (coin + oid) or every open order on a coin. Dry-run in this build.",
			Summary:     "Cancel one or all orders"},
		{Name: "close_position", Command: "close", Slash: "close", Aliases: []string{"flatten"}, Positional: []string{"coin"}, Group: "trade", Mutating: true, NewInput: in[CloseInput](), Run: runClose,
			Description: "Close part or all of a position with reduce-only IOC orders at the mid +/- slippage (pct defaults to 100), or every position with all=true (asks twice). Reads positions from the vault, trading.account or the default account. Dry-run in this build.",
			Summary:     "Close a position (or all) with reduce-only orders"},
		{Name: "vault_deposit", Command: "vault deposit", Slash: "deposit", Positional: []string{"vault", "usd"}, Group: "trade", Mutating: true, NewInput: in[VaultTransferInput](), Run: runVaultTransfer(true),
			Description: "Deposit USDC into a vault (vaultTransfer, minimum $5). Dry-run in this build.",
			Summary:     "Deposit USDC into a vault (agent-signed vaultTransfer)"},
		{Name: "vault_withdraw", Command: "vault withdraw", Slash: "withdraw", Positional: []string{"vault", "usd"}, Group: "trade", Mutating: true, NewInput: in[VaultTransferInput](), Run: runVaultTransfer(false),
			Description: "Withdraw USDC from a vault, subject to its lockup. Dry-run in this build.",
			Summary:     "Withdraw USDC from a vault (subject to lockup)"},

		// meta
		{Name: "schema", Command: "schema", Slash: "schema", Positional: []string{"tool"}, Group: "meta", NewInput: in[SchemaInput](), Run: runSchema(Default),
			Description: "Without tool: every tool with its command, slash and summary. With tool: its JSON Schema input, output type and output schema.",
			Summary:     "Print the JSON Schema of tool inputs and outputs for agents",
			Examples:    []string{"hlp schema", "hlp schema get_vault"}},
	}, slices.Concat(pvaultTools(), priceTools(), previewTools(), statusTools(), paperTools(), tickTools())...)
}
