package core

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/google/jsonschema-go/jsonschema"

	"github.com/maxmoneycash/hlp/internal/chart"
	"github.com/maxmoneycash/hlp/internal/config"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hlpxyz"
	"github.com/maxmoneycash/hlp/internal/hyperliquid"
	"github.com/maxmoneycash/hlp/internal/hyperliquid/exchange"
	"github.com/maxmoneycash/hlp/internal/hyperliquid/ws"
	"github.com/maxmoneycash/hlp/internal/llm"
	"github.com/maxmoneycash/hlp/internal/macro"
	"github.com/maxmoneycash/hlp/internal/pvault"
	"github.com/maxmoneycash/hlp/internal/registry"
	"github.com/maxmoneycash/hlp/internal/strategy"
	"github.com/maxmoneycash/hlp/internal/strategy/backtest"
	"github.com/maxmoneycash/hlp/internal/strategy/sim"
	"github.com/maxmoneycash/hlp/internal/theme"
)

// Env is the dependency bundle handed to every tool. Built once per process
// by NewEnv (cli engineer) and shared by TUI, MCP and agent.
type Env struct {
	Config  config.Config
	Paths   config.Paths
	Secrets config.Secrets

	HL       hyperliquid.Reader
	Stream   ws.Stream       // nil in one-shot CLI mode until first use
	HLPXYZ   hlpxyz.API      // nil when config hlpxyz.disabled
	Exchange exchange.Client // nil unless trading is enabled AND an agent key exists

	Guard   Guard     // risk caps; never nil (NewEnv installs config-backed guard)
	Confirm Confirmer // nil = non-interactive: mutating tools refuse unless input.Yes

	Theme   theme.Theme
	Profile colorprofile.Profile
	Now     func() time.Time

	// Progress, when set, receives short status lines from slow tools
	// ("fetching funding 3/7"). The CLI shows them on a terminal's stderr;
	// nil is silent (JSON, MCP, agents, tests).
	Progress func(status string)
	// ProgressN, when set, receives counted progress (stage, done, total)
	// in place of Progress's text line: NDJSON and MCP report the counts
	// as numbers (AGENT-UX #20).
	ProgressN func(stage string, done, total int)

	// Vaults is the curated vault registry (embedded vaults.toml plus the
	// user's ~/.config/hlp/vaults.toml). Never nil after NewEnv.
	Vaults *registry.Registry
	// VaultWarnings are problems found in the user's vaults.toml; vault
	// tools add them to Meta.Warnings.
	VaultWarnings []string
	// PVault reads HyperEVM strategy vaults (built by NewEnv without
	// touching the network; replace it to fake one in tests).
	PVault *pvault.Service

	// Macro is the prediction-market context for LLM paper and arena runs
	// (nil = off: offline mode, config macro.disabled, tests).
	Macro *macro.Client
	// LLMCatalog is OpenRouter's public model list and prices (hlp arena
	// models, the --llm cost estimate). Nil = prices unknown.
	LLMCatalog *llm.Catalog

	pvaultTelemetry pvault.Telemetry // hlp.xyz client for keeper data, on either network
	version         string
}

// EnvOptions are process-level switches from global flags.
type EnvOptions struct {
	ConfigPath string
	Network    domain.Network // overrides config (--testnet)
	Theme      string
	Profile    colorprofile.Profile
	Offline    bool // fixtures/replay only (hlp --demo)

	// Version is the binary version, sent in User-Agent headers.
	Version string
	// FixturesDir serves Hyperliquid data from domain JSON files instead of
	// the network (offline mode; HLP_FIXTURES sets it). See NewFixtureReader.
	FixturesDir string
	// OnWSState receives WebSocket connection states ("connecting", "live",
	// "reconnecting", "closed") for a status bar. Optional.
	OnWSState func(state string)
}

// NewEnv loads config, builds clients and the guard. It never touches the
// network: clients and the WebSocket connect lazily on first use.
func NewEnv(ctx context.Context, o EnvOptions) (*Env, error) {
	return newEnv(ctx, o)
}

// Card tells front-ends how to present a Result. Rendering lives in
// internal/tui/cards (shared by the TUI scrollback and CLI human output).
type Card string

const (
	CardText     Card = "text"     // Data: string (markdown)
	CardChart    Card = "chart"    // Data: *ChartData
	CardBook     Card = "book"     // Data: domain.Book
	CardTrades   Card = "trades"   // Data: []domain.Trade
	CardMarkets  Card = "markets"  // Data: []domain.Market
	CardFunding  Card = "funding"  // Data: []domain.Market (sorted by funding) or []domain.FundingPoint
	CardAccount  Card = "account"  // Data: domain.Account
	CardFills    Card = "fills"    // Data: []domain.Fill
	CardVaults   Card = "vaults"   // Data: []domain.VaultSummary
	CardVault    Card = "vault"    // Data: domain.VaultDetail (with Tearsheet)
	CardCompare  Card = "compare"  // Data: *CompareData
	CardBacktest Card = "backtest" // Data: *BacktestData
	CardPaper    Card = "paper"    // Data: *PaperData
	CardArena    Card = "arena"    // Data: *ArenaData
	CardOrder    Card = "order"    // Data: *OrderPreview (dry-run or result)
	CardFund     Card = "fund"     // Data: *FundData (hlpxyz.PVaultStatus + history)
	CardStrategy Card = "strategy" // Data: *StrategyData (hlpxyz.QuantLive + static)
	CardKV       Card = "kv"       // Data: [][2]string (asset-id, schema summaries)
	CardIndex    Card = "index"    // Data: hlpxyz.Index (one index) or []hlpxyz.Index (list)
	CardSchema   Card = "schema"   // Data: []ToolSchema
	CardPrice    Card = "price"    // Data: []PriceRow (one line per coin)
	CardStatus   Card = "status"   // Data: *StatusData (health checks)
)

// Result is what every tool returns.
type Result struct {
	Card  Card        `json:"-"`
	Title string      `json:"-"`
	Data  any         `json:"data"`
	Meta  domain.Meta `json:"meta"`
	// Tail: Data is oldest-first history, so --limit keeps the most
	// recent rows instead of the first ones.
	Tail bool `json:"-"`
}

// ChartData is the payload of CardChart: candles + everything the chart
// renderer needs. JSON output omits nothing an agent needs to reason.
// Market carries live context (mark, prevDay, funding, OI, volume) when the
// reader could fetch it.
type ChartData struct {
	Coin     string          `json:"coin"`
	Interval domain.Interval `json:"interval"`
	Market   domain.Market   `json:"market"`
	Candles  []domain.Candle `json:"candles"`
	MAs      []int           `json:"mas,omitempty"`
	EMAs     []int           `json:"emas,omitempty"`
	Lines    []chart.HLine   `json:"lines,omitempty"` // entry/liq/order levels from ChartInput.Address
	Summary  ChartSummary    `json:"summary"`
	Plain    string          `json:"plain,omitempty"` // --plain/MCP text rendering

	// Render hints from ChartInput for the chart card; never in JSON.
	Style  string `json:"-"` // candle style; "" = config chart.style
	Zoom   int    `json:"-"` // columns per candle; 0 = config chart.zoom
	Height int    `json:"-"` // rows; 0 = card default
}

// ChartSummary is the numeric digest agents read instead of pixels. Change
// is a FRACTION (PLAN D20) over the candle window; Change24h is the 24h
// change from the market context when known.
type ChartSummary struct {
	Last      float64            `json:"last"`
	Open      float64            `json:"open"`
	High      float64            `json:"high"`
	Low       float64            `json:"low"`
	Change    float64            `json:"change"`
	Change24h float64            `json:"change24h,omitempty"`
	MA        map[string]float64 `json:"ma,omitempty"`
}

// CompareData backs `hlp vault compare`.
type CompareData struct {
	Vaults []domain.VaultDetail `json:"vaults"`
	Corr   [][]float64          `json:"correlation"`
}

// BacktestData backs `hlp backtest` and `hlp pine run`.
type BacktestData struct {
	Strategy string `json:"strategy"`
	// Engine is the engine that ran the strategy: builtin, pyne or pineforge.
	Engine        string                     `json:"engine"`
	EngineVersion string                     `json:"engineVersion,omitempty"`
	Tearsheet     domain.Tearsheet           `json:"tearsheet"`
	Fills         []domain.Fill              `json:"fills"`
	Candles       map[string][]domain.Candle `json:"-"` // for the chart card with markers
	Markers       []backtest.Marker          `json:"markers,omitempty"`
	Plots         Plots                      `json:"plots,omitempty"`
	Logs          []string                   `json:"logs,omitempty"`
	Warnings      []string                   `json:"warnings,omitempty"`
	// Account counts what Hyperliquid's rules changed: orders clipped by
	// margin, liquidations, and orders dropped below the lot size or the
	// $10 minimum. Nil when nothing changed or the engine is external.
	Account *sim.Stats `json:"account,omitempty"`
	// Model is the LLM that drove the strategy ("" for Pine and theses);
	// every decision and order it made is tagged with it.
	Model string `json:"model,omitempty"`
	// VsBench is the strategy's growth relative to Bench's buy-and-hold
	// over the same window, (1+return)/(1+bench) - 1 (fraction); Bench
	// names the yardstick.
	VsBench *float64   `json:"vsBench,omitempty"`
	Bench   *BenchInfo `json:"bench,omitempty"`
	// LLM is the run's model usage (calls, tokens, list-price cost).
	LLM *llm.BudgetStats `json:"llm,omitempty"`
}

// Plots are named per-bar series aligned with the lead coin's candles. Bars
// without a value hold NaN (the chart overlay convention); JSON writes them
// as null because encoding/json cannot represent NaN.
type Plots map[string][]float64

// MarshalJSON writes NaN and ±Inf as null.
func (p Plots) MarshalJSON() ([]byte, error) {
	if p == nil {
		return []byte("null"), nil
	}
	out := make(map[string][]*float64, len(p))
	for k, xs := range p {
		ys := make([]*float64, len(xs))
		for i := range xs {
			if !math.IsNaN(xs[i]) && !math.IsInf(xs[i], 0) {
				ys[i] = &xs[i]
			}
		}
		out[k] = ys
	}
	return json.Marshal(out)
}

// UnmarshalJSON reads null as NaN.
func (p *Plots) UnmarshalJSON(b []byte) error {
	var in map[string][]*float64
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	*p = make(Plots, len(in))
	for k, ys := range in {
		xs := make([]float64, len(ys))
		for i, y := range ys {
			xs[i] = math.NaN()
			if y != nil {
				xs[i] = *y
			}
		}
		(*p)[k] = xs
	}
	return nil
}

// PaperData backs `hlp paper status`.
type PaperData struct {
	ID       string         `json:"id"`
	Strategy string         `json:"strategy"`
	Equity   domain.Series  `json:"equity"`
	Account  domain.Account `json:"account"`
	Fills    []domain.Fill  `json:"fills"`
	Running  bool           `json:"running"`
	Error    string         `json:"error,omitempty"`    // last failure (model server unreachable, ...)
	LastNote string         `json:"lastNote,omitempty"` // latest decision note
	Capital  float64        `json:"capital,omitempty"`
}

// ArenaData backs `hlp arena`.
type ArenaData struct {
	Entrants []ArenaEntrant `json:"entrants"`
	// Bench is the buy-and-hold yardstick every entrant's vsBench is
	// measured against (equal weight of the entrants' coins by default).
	Bench *BenchInfo `json:"bench,omitempty"`
	// LLM is the arena's model usage against --max-llm-calls.
	LLM *llm.BudgetStats `json:"llm,omitempty"`
}

// ArenaEntrant is one bot or real vault in the arena leaderboard.
type ArenaEntrant struct {
	Name     string         `json:"name"`
	Kind     string         `json:"kind"` // pine | thesis | llm | vault
	Equity   domain.Series  `json:"equity"`
	Metrics  domain.Metrics `json:"metrics"`
	LastNote string         `json:"lastNote,omitempty"` // "why I bought"
	// Error is the entrant's last failure (model server unreachable, ...).
	Error string `json:"error,omitempty"`
	// Waiting is set until the entrant has seen its first closed bar.
	Waiting bool `json:"waiting,omitempty"`
	// Interval is the bar interval the entrant decides on.
	Interval domain.Interval `json:"interval,omitempty"`
	// VsBench is (1+Metrics.TotalReturn)/(1+ArenaData.Bench.Return) - 1.
	VsBench *float64 `json:"vsBench,omitempty"`
	// Model is the LLM driving the entrant ("openrouter:anthropic/claude-sonnet-5").
	Model string `json:"model,omitempty"`
	// Decision is the entrant's latest structured decision (decisions-mode
	// LLM bots): scout picks, reasoning, actions with confidence, rationale
	// and key signal, and what the risk limits made of each.
	Decision *strategy.Decision `json:"decision,omitempty"`
}

// OrderPreview is shown before (and after) any mutating action.
type OrderPreview struct {
	Action      string          `json:"action"` // order | cancel | leverage | vault_deposit | vault_withdraw
	Coin        string          `json:"coin,omitempty"`
	Side        domain.Side     `json:"side,omitempty"`
	Size        string          `json:"size,omitempty"` // wire string
	Px          string          `json:"px,omitempty"`
	NotionalUSD float64         `json:"notionalUsd"`
	Vault       string          `json:"vault,omitempty"`
	Network     domain.Network  `json:"network"`
	DryRun      bool            `json:"dryRun"`
	Envelope    json.RawMessage `json:"envelope,omitempty"` // signed, unsent (dry-run)
	Response    json.RawMessage `json:"response,omitempty"`
	Checks      []domain.Check  `json:"checks"` // risk-cap results
	// Triggers are the TP/SL legs sent with an order (grouping normalTpsl).
	Triggers []TriggerLeg `json:"triggers,omitempty"`
	Grouping string       `json:"grouping,omitempty"` // na | normalTpsl
	Warnings []string     `json:"warnings,omitempty"`
	// Vault deposits and withdrawals name their target (audit S2).
	VaultName    string  `json:"vaultName,omitempty"`
	VaultLeader  string  `json:"vaultLeader,omitempty"`
	VaultTVL     float64 `json:"vaultTvl,omitempty"`
	VaultAgeDays int     `json:"vaultAgeDays,omitempty"`
	// OK and Blockers summarize Checks (AGENT-UX #10). MarshalJSON
	// recomputes them from Checks on every encode.
	OK       bool     `json:"ok"`
	Blockers []string `json:"blockers"`
	// Blockers split in two: SetupBlockers are trading not being
	// configured (trading_enabled, agent_key, mcp_trading); RiskBlockers
	// are about this order (caps, minimum, vault checks). TradeOK is true
	// when only setup blocks it: the trade itself passes the risk rules.
	SetupBlockers []string `json:"setupBlockers"`
	RiskBlockers  []string `json:"riskBlockers"`
	TradeOK       bool     `json:"tradeOk"`
	// Paper is set when the order went to the paper exchange (`order
	// --paper`): the simulated result, labelled mode "paper".
	Paper *PaperOrderData `json:"paper,omitempty"`
}

// TriggerLeg is one take-profit or stop-loss leg of an order.
type TriggerLeg struct {
	Kind      string      `json:"kind"`      // tp | sl
	TriggerPx string      `json:"triggerPx"` // wire string
	Size      string      `json:"size"`      // reduce-only, market when triggered
	Side      domain.Side `json:"side"`      // the closing side
	// Through is true when the current mark already sits past the trigger,
	// so the leg would fire as soon as the entry fills.
	Through bool `json:"through"`
}

// Strategy engine names (EngineOpt.Engine, BacktestData.Engine, Meta.Engine).
const (
	EngineBuiltin   = "builtin"   // hlp's own simulator: HL fees, slippage, funding, szDecimals, leverage
	EnginePyne      = "pyne"      // external PYNE install (internal/pyne)
	EnginePineForge = "pineforge" // external PineForge install (internal/pineforge); licence gate
	EngineAuto      = "auto"      // pyne when available, else builtin; never pineforge
)

// Tool is one capability exposed on every surface.
type Tool struct {
	Name        string   // MCP/agent name, snake_case: get_candles, list_vaults
	Command     string   // CLI path: "chart", "vault compare"
	Aliases     []string // CLI/slash aliases: "c" for chart
	Slash       string   // TUI slash command without "/": "chart"
	Positional  []string // json field names accepted positionally: ["coin","interval"]
	Summary     string   // one line (help, autocomplete)
	Description string   // agent/MCP quality: when to use it, units, caveats
	Group       string   // help grouping: market | vault | account | strategy | trade | meta
	Mutating    bool     // places orders / moves funds: guarded, dry-run default, hidden from MCP unless --allow-trading
	Streaming   bool     // supports Stream (watch, live chart, paper status)
	Examples    []string // CLI usage lines for help EXAMPLES, e.g. "hlp price btc eth"
	LocalState  bool     // reads or writes local files/state (paper sessions, caches); MCP hint

	NewInput func() any // pointer to a zero input struct
	Run      func(ctx context.Context, env *Env, in any) (*Result, error)
	// Stream emits successive Results until ctx ends (NDJSON in CLI).
	Stream func(ctx context.Context, env *Env, in any, emit func(*Result)) error
}

// InputSchema returns the JSON Schema of the tool input.
func (t Tool) InputSchema() (*jsonschema.Schema, error) {
	in := t.NewInput()
	return jsonschema.ForType(reflect.TypeOf(in).Elem(), &jsonschema.ForOptions{})
}

// Call is a tool invocation (from MCP, the agent, the intent parser, or a slash command).
type Call struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

// Registry holds every tool.
type Registry struct{ tools map[string]Tool }

// NewRegistry returns an empty registry; Default() returns the full catalog.
func NewRegistry() *Registry { return &Registry{tools: map[string]Tool{}} }

// Register adds a tool; duplicate names panic (programming error).
func (r *Registry) Register(t Tool) {
	if _, dup := r.tools[t.Name]; dup {
		panic(fmt.Sprintf("core: duplicate tool %q", t.Name))
	}
	r.tools[t.Name] = t
}

// Get finds a tool by Name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// BySlash finds a tool by slash command or alias.
func (r *Registry) BySlash(s string) (Tool, bool) {
	for _, t := range r.tools {
		if t.Slash == s {
			return t, true
		}
		for _, a := range t.Aliases {
			if a == s {
				return t, true
			}
		}
	}
	return Tool{}, false
}

// All returns tools sorted by Name.
func (r *Registry) All() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Invoke decodes args into the tool input (unknown fields are a USAGE
// error), applies the mutating-tool gates, and runs it.
func (r *Registry) Invoke(ctx context.Context, env *Env, c Call) (*Result, error) {
	return r.invoke(ctx, env, c)
}

// Default returns the full tool catalog (see catalog.go).
func Default() *Registry {
	r := NewRegistry()
	for _, t := range catalog() {
		r.Register(t)
	}
	return r
}
