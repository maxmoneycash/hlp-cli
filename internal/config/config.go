package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/evm"
	"github.com/pelletier/go-toml/v2"
)

// Config is the on-disk configuration. Zero values are safe defaults:
// mainnet, read-only, theme auto, no LLM.
type Config struct {
	Network domain.Network `toml:"network"`        // mainnet | testnet
	Theme   string         `toml:"theme"`          // "" = auto (rush on dark and light)
	Account string         `toml:"account"`        // default address to watch (no key needed)
	Watch   []string       `toml:"watch"`          // extra addresses / vaults to spectate
	Pinned  []string       `toml:"pinned_markets"` // watchlist coins

	UI      UIConfig      `toml:"ui"`
	Chart   ChartConfig   `toml:"chart"`
	HLPXYZ  HLPXYZConfig  `toml:"hlpxyz"`
	LLM     LLMConfig     `toml:"llm"`
	Trading TradingConfig `toml:"trading"`
	Risk    RiskConfig    `toml:"risk"`
	MCP     MCPConfig     `toml:"mcp"`

	Strategy  StrategyConfig  `toml:"strategy"`
	PineForge PineForgeConfig `toml:"pineforge"`
	Macro     MacroConfig     `toml:"macro"`
	Ticks     TicksConfig     `toml:"ticks"`

	// Addresses are named addresses ("me", "desk") usable wherever an
	// address is accepted: name -> 0x address.
	Addresses map[string]string `toml:"addresses,omitempty"`

	// HyperEVM overrides the built-in HyperEVM chain configs for strategy
	// vaults, keyed by chain id: [hyperevm.998] rpc, lens, usdc, info.
	HyperEVM map[string]evm.Chain `toml:"hyperevm,omitempty"`

	// Warnings are non-fatal problems Load found (a group- or
	// world-writable file). Never written back.
	Warnings []string `toml:"-"`
}

// HyperEVMChains converts the [hyperevm.<id>] tables to chain-id keys.
// Keys that are not decimal chain ids are reported by Validate.
func (c Config) HyperEVMChains() map[uint64]evm.Chain {
	if len(c.HyperEVM) == 0 {
		return nil
	}
	out := make(map[uint64]evm.Chain, len(c.HyperEVM))
	for k, ch := range c.HyperEVM {
		id, err := strconv.ParseUint(strings.TrimSpace(k), 10, 64)
		if err != nil || id == 0 {
			continue
		}
		ch.ID = id
		out[id] = ch
	}
	return out
}

// UIConfig is the TUI look (docs/DESIGN.md section 8). The theme itself is
// the top-level theme key.
type UIConfig struct {
	// PaintBackground lets the TUI set the terminal ground to the theme's BG
	// with OSC 11 (rush: #131315), restored on exit. Default true.
	PaintBackground bool `toml:"paint_background"`
	// Motion is "on" (default) or "off": off stops the ticker flaps and
	// keeps the cursor solid. HLP_REDUCED_MOTION=1 and NO_MOTION also turn
	// it off at run time.
	Motion string `toml:"motion"`
	// Floor draws the dotted floor grid in empty states. Default true.
	Floor bool `toml:"floor"`
}

// UI motion values.
const (
	MotionOn  = "on"
	MotionOff = "off"
)

// ChartConfig are the chart defaults.
type ChartConfig struct {
	Style     string          `toml:"style"`      // classic | solid | hollow | ohlc | line | heikin | blocks | lines | half | mono | ascii
	Theme     string          `toml:"theme"`      // chart skin from setup-rush; "" = the UI theme's pairing (rush in the shell, terminal elsewhere), paper, pro, neon, crt... or "none"
	Zoom      int             `toml:"zoom"`       // columns per candle: 1, 2, 4, 6
	Interval  domain.Interval `toml:"interval"`   // default 15m
	MAs       []int           `toml:"mas"`        // default [20, 50]
	VolumeUSD bool            `toml:"volume_usd"` // show volume as USD notional
	UTC       bool            `toml:"utc"`        // force UTC labels (default: local intraday, UTC >= 1d)
}

// HLPXYZConfig controls the hlp.xyz backend.
type HLPXYZConfig struct {
	Disabled bool   `toml:"disabled"` // stay strictly on Hyperliquid endpoints
	BaseURL  string `toml:"base_url"` // default https://www.hlp.xyz
}

// LLMConfig selects a bring-your-own model. Keys come from the env var named
// in APIKeyEnv (never from this file). Local providers need no key.
type LLMConfig struct {
	Provider  string `toml:"provider"` // ollama | lmstudio | openai-compatible | anthropic | openrouter | "" (off)
	BaseURL   string `toml:"base_url"` // e.g. http://localhost:11434/v1
	Model     string `toml:"model"`
	APIKeyEnv string `toml:"api_key_env"` // e.g. ANTHROPIC_API_KEY
	// AllowRemoteBots lets strategy/arena bots started by an agent or MCP
	// client call a non-loopback LLM endpoint (default false: local models
	// only for them). Sessions a person starts from the CLI or TUI may use
	// the configured model wherever it is.
	AllowRemoteBots bool `toml:"allow_remote_bots"`
}

// MacroConfig controls the prediction-market odds (Hyperliquid HIP-4
// outcomes, Polymarket, Kalshi) that LLM strategies see in paper and arena
// runs as labelled forward-looking context, never as a trade signal. Reads
// are public, keyless, cached for at least 5 minutes and fail soft.
// Backtests never get it (today's odds would leak into past bars).
type MacroConfig struct {
	Disabled bool     `toml:"disabled"`          // default false: on for LLM paper/arena runs
	Sources  []string `toml:"sources,omitempty"` // subset of hyperliquid, polymarket, kalshi; empty = all
}

// TicksConfig bounds the local trade store behind tick, volume and range
// bars (<cache>/ticks). Hyperliquid has no free trade history, so this is
// what hlp recorded while it ran.
type TicksConfig struct {
	RetentionDays int `toml:"retention_days"` // UTC days kept before today; 0 = 7
	MaxMB         int `toml:"max_mb"`         // cap for every coin together, oldest days go first; 0 = 512
}

// MacroSources are the valid macro.sources values.
var MacroSources = []string{"hyperliquid", "polymarket", "kalshi"}

// TradingConfig gates every /exchange call.
type TradingConfig struct {
	Enabled      bool   `toml:"enabled"`       // master switch; default false
	AgentAddress string `toml:"agent_address"` // public address of the approved API wallet
	AccountAddr  string `toml:"account"`       // master/sub-account the agent trades for
	VaultAddress string `toml:"vault"`         // optional: trade on behalf of this vault
	BuilderFee   bool   `toml:"builder_fee"`   // opt-in hlp.xyz builder code (disabled until owner sets an address)
	SlippageBps  int    `toml:"slippage_bps"`  // market-order bound, default 50
	// AllowUnattended lets risk.require_confirm pass without a TTY
	// confirmer (scripts, agents); default false refuses with CONFIRM_REQUIRED.
	AllowUnattended bool `toml:"allow_unattended"`
	// Expires ends trading.enabled at a fixed time so a forgotten switch
	// does not last forever: "2026-12-31" (trading stops at the end of that
	// UTC day) or an RFC 3339 instant. "" = never. An unparsable value is a
	// config error, so trading is refused (see TradingConfig.Expiry).
	Expires string `toml:"expires,omitempty"`
}

// RiskConfig caps apply to every mutating action from CLI, TUI, agent and MCP
// (deliverator pattern). Zero means "no cap" only for fields documented so.
// See risk.go for Validate and CheckOrder.
type RiskConfig struct {
	MaxNotionalUSD float64  `toml:"max_notional_usd"` // per order, default 1000
	MaxLeverage    int      `toml:"max_leverage"`     // default 5
	AllowedCoins   []string `toml:"allowed_coins"`    // empty = all
	DailyLossUSD   float64  `toml:"daily_loss_usd"`   // halt after this realized loss, default 200
	DeadManSeconds int      `toml:"dead_man_seconds"` // scheduleCancel heartbeat for bots, 0 = off
	RequireConfirm bool     `toml:"require_confirm"`  // TTY confirm even with --yes on mainnet (default true)
	// AllowedVaults bounds where vault_deposit may send money: 0x vault
	// addresses and/or "curated" (any vault in the curated registry). Empty
	// = curated vaults only for automated callers (MCP, agent, bot) and any
	// vault for a person at the CLI or TUI. Withdrawals are never limited.
	AllowedVaults []string `toml:"allowed_vaults,omitempty"`
	// Networks lists the networks this config may trade on ("mainnet",
	// "testnet"). Empty = both. A testnet config run without --testnet is
	// refused when it says ["testnet"].
	Networks []string `toml:"networks,omitempty"`
	// MaxOrdersPerMin caps risk-adding actions (orders, leverage changes,
	// vault deposits) actually sent per rolling minute, across every hlp
	// process on the machine. 0 = off; default 60. Reduce-only orders and
	// cancels are never capped.
	MaxOrdersPerMin int `toml:"max_orders_per_min,omitempty"`
}

// MCPConfig controls `hlp mcp`.
type MCPConfig struct {
	AllowTrading bool `toml:"allow_trading"` // also requires --allow-trading on the command line
	// AllowUnattended extends trading.allow_unattended to MCP callers
	// (default false: MCP orders under risk.require_confirm need a person).
	AllowUnattended bool `toml:"allow_unattended,omitempty"`
}

// StrategyConfig selects how Pine strategies run.
type StrategyConfig struct {
	Engine string `toml:"engine"` // builtin (default) | pyne | pineforge | auto
	// AllowExternalEngines lets agent, MCP and unknown callers run the
	// external pyne/pineforge subprocesses (default false: builtin only).
	AllowExternalEngines bool `toml:"allow_external_engines"`
	// Roots are extra directories agent, MCP and unknown callers may read
	// strategy files from (besides DataDir/strategies, the embedded
	// examples and a narrow working directory).
	Roots []string `toml:"roots,omitempty"`
}

// PineForgeConfig holds the PineForge licence acceptance.
type PineForgeConfig struct {
	AcceptLicense bool `toml:"accept_license"` // the user has read pineforge.LicenseNotice
}

// Defaults returns the configuration used when no file exists.
func Defaults() Config {
	return Config{
		Network:  domain.Mainnet,
		UI:       UIConfig{PaintBackground: true, Motion: MotionOn, Floor: true},
		Chart:    ChartConfig{Style: "classic", Theme: "", Zoom: 2, Interval: domain.I15m, MAs: []int{20, 50}, VolumeUSD: true},
		HLPXYZ:   HLPXYZConfig{BaseURL: "https://www.hlp.xyz"},
		Trading:  TradingConfig{SlippageBps: 50},
		Risk:     RiskConfig{MaxNotionalUSD: 1000, MaxLeverage: 5, DailyLossUSD: 200, RequireConfirm: true, MaxOrdersPerMin: DefaultMaxOrdersPerMin},
		Strategy: StrategyConfig{Engine: "builtin"},
	}
}

// Paths are the XDG locations hlp uses.
type Paths struct {
	ConfigFile string // ~/.config/hlp/config.toml   ($HLP_CONFIG overrides)
	CacheDir   string // ~/.cache/hlp                 (stats-data vaults, meta, candles)
	DataDir    string // ~/.local/share/hlp           (paper ledgers, sessions, arena logs)
}

// DefaultPaths resolves Paths from XDG env vars with HOME fallbacks.
func DefaultPaths() Paths {
	home, _ := os.UserHomeDir()
	get := func(env, fallback string) string {
		if v := os.Getenv(env); v != "" {
			return filepath.Join(v, "hlp")
		}
		return filepath.Join(home, fallback, "hlp")
	}
	p := Paths{
		ConfigFile: filepath.Join(get("XDG_CONFIG_HOME", ".config"), "config.toml"),
		CacheDir:   get("XDG_CACHE_HOME", ".cache"),
		DataDir:    get("XDG_DATA_HOME", filepath.Join(".local", "share")),
	}
	if v := os.Getenv("HLP_CONFIG"); v != "" {
		p.ConfigFile = v
	}
	return p
}

// Environment variables that override the file.
const (
	EnvNetwork = "HLP_NETWORK"
	EnvTheme   = "HLP_THEME"
	EnvAccount = "HLP_ACCOUNT"
)

// Error is a configuration problem tied to one key. Key is the dotted TOML
// path ("risk.max_leverage"); Line is 1-based, or 0 when unknown (env
// overrides, validation of defaults).
type Error struct {
	Path string // file path, or "" for env/validation errors
	Key  string
	Line int
	Msg  string
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("config")
	if e.Path != "" {
		b.WriteString(" ")
		b.WriteString(e.Path)
		if e.Line > 0 {
			fmt.Fprintf(&b, ":%d", e.Line)
		}
	}
	b.WriteString(": ")
	if e.Key != "" {
		fmt.Fprintf(&b, "%s: ", e.Key)
	}
	b.WriteString(e.Msg)
	return b.String()
}

// Load reads the config file at path. A missing file returns Defaults() and
// no error. Unknown keys are an error (typos in risk caps must not be silent).
//
// Policy loading has three states: an absent file permits
// the defaults (trading off), a file that cannot be read, decoded or
// validated refuses, and a valid file is enforced. Whenever Load returns an
// error the returned Config is disarmed (Disarm): a caller that ignores the
// error still cannot trade on a corrupt config.
// Env overrides applied after the file: HLP_NETWORK, HLP_THEME, HLP_ACCOUNT.
// Keys the file omits keep their defaults. The result is validated; errors are
// *Error values (joined when there are several) naming the offending key.
func Load(path string) (Config, error) {
	cfg := Defaults()
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return Disarm(Defaults()), fmt.Errorf("config: %w", err)
	default:
		if err := decode(path, data, &cfg); err != nil {
			return Disarm(Defaults()), err
		}
		if err := checkFileMode(path, &cfg); err != nil {
			return Disarm(cfg), err
		}
	}
	if err := ApplyEnv(&cfg, os.Getenv); err != nil {
		return Disarm(cfg), err
	}
	if err := cfg.Validate(); err != nil {
		return Disarm(cfg), err
	}
	return cfg, nil
}

// Disarm returns cfg with every switch that lets hlp trade or act
// unattended turned off. Load applies it to any config it returns with an
// error.
func Disarm(cfg Config) Config {
	cfg.Trading.Enabled = false
	cfg.Trading.AllowUnattended = false
	cfg.MCP.AllowTrading = false
	cfg.MCP.AllowUnattended = false
	return cfg
}

// checkFileMode guards the file that controls llm.base_url, the trading
// switch, mcp.allow_trading and the other safety switches (audit S14). A
// group- or world-writable file is a warning, and it may not turn any of
// them on: anyone else on the machine could have. On a refusal cfg has all
// of them off.
func checkFileMode(path string, cfg *Config) error {
	if runtime.GOOS == "windows" {
		return nil // no unix permission bits
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	perm := fi.Mode().Perm()
	if perm&0o022 == 0 {
		return nil
	}
	who := "group"
	if perm&0o002 != 0 {
		who = "world"
	}
	cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("%s is %s-writable (mode %04o); run chmod 600 %s", path, who, perm, path))
	// Every switch that widens what runs unattended or what agents may do
	// is refused from a file someone else could have edited.
	var keys []string
	for _, k := range []struct {
		name string
		on   *bool
	}{
		{"trading.enabled", &cfg.Trading.Enabled},
		{"trading.allow_unattended", &cfg.Trading.AllowUnattended},
		{"mcp.allow_trading", &cfg.MCP.AllowTrading},
		{"mcp.allow_unattended", &cfg.MCP.AllowUnattended},
		{"strategy.allow_external_engines", &cfg.Strategy.AllowExternalEngines},
		{"pineforge.accept_license", &cfg.PineForge.AcceptLicense},
		{"llm.allow_remote_bots", &cfg.LLM.AllowRemoteBots},
	} {
		if *k.on {
			keys = append(keys, k.name)
			*k.on = false
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return &Error{Path: path, Key: keys[0], Msg: fmt.Sprintf("refusing %s: the file is %s-writable (mode %04o), so another user could have set it; run chmod 600 %s",
		strings.Join(keys, " and "), who, perm, path)}
}

// Parse decodes TOML bytes over Defaults() and validates the result without
// applying env overrides. name labels errors (usually the file path). Like
// Load, a config returned with an error is disarmed.
func Parse(name string, data []byte) (Config, error) {
	cfg := Defaults()
	if err := decode(name, data, &cfg); err != nil {
		return Disarm(Defaults()), err
	}
	if err := cfg.Validate(); err != nil {
		return Disarm(cfg), err
	}
	return cfg, nil
}

func decode(path string, data []byte, cfg *Config) error {
	dec := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	err := dec.Decode(cfg)
	if err == nil {
		return nil
	}
	var strict *toml.StrictMissingError
	if errors.As(err, &strict) {
		errs := make([]error, 0, len(strict.Errors))
		for i := range strict.Errors {
			de := &strict.Errors[i]
			line, _ := de.Position()
			key := strings.Join(de.Key(), ".")
			msg := "unknown key"
			if s := suggestKey(key); s != "" {
				msg += fmt.Sprintf(" (did you mean %q?)", s)
			}
			errs = append(errs, &Error{Path: path, Key: key, Line: line, Msg: msg})
		}
		return errors.Join(errs...)
	}
	var de *toml.DecodeError
	if errors.As(err, &de) {
		line, _ := de.Position()
		return &Error{Path: path, Key: strings.Join(de.Key(), "."), Line: line, Msg: friendlyDecodeMsg(strings.TrimPrefix(de.Error(), "toml: "))}
	}
	return &Error{Path: path, Msg: err.Error()}
}

var typeErrRe = regexp.MustCompile(`cannot decode TOML (\w+) into .* of type (\S+)`)

// friendlyDecodeMsg turns go-toml's "cannot decode TOML string into struct
// field config.RiskConfig.MaxLeverage of type int" into "want a number, got a
// string": users never see Go type names.
func friendlyDecodeMsg(msg string) string {
	m := typeErrRe.FindStringSubmatch(msg)
	if m == nil {
		return msg
	}
	want := map[string]string{
		"int": "a whole number", "float64": "a number", "bool": "true or false", "string": "a quoted string",
		"[]string": "a list of strings", "[]int": "a list of whole numbers",
		"domain.Network": "a quoted string", "domain.Interval": "a quoted string",
	}[m[2]]
	if want == "" {
		want = m[2]
	}
	got := map[string]string{"string": "a string", "integer": "a number", "float": "a number", "bool": "a boolean", "array": "a list", "table": "a table"}[m[1]]
	if got == "" {
		got = m[1]
	}
	return "want " + want + ", got " + got
}

// ApplyEnv applies HLP_NETWORK, HLP_THEME and HLP_ACCOUNT from getenv. Empty
// values are ignored. An invalid HLP_NETWORK is an error (a typo must not
// silently trade on mainnet).
func ApplyEnv(cfg *Config, getenv func(string) string) error {
	if v := strings.TrimSpace(getenv(EnvNetwork)); v != "" {
		n := domain.Network(strings.ToLower(v))
		if n != domain.Mainnet && n != domain.Testnet {
			return &Error{Key: EnvNetwork, Msg: fmt.Sprintf("%q is not mainnet or testnet", v)}
		}
		cfg.Network = n
	}
	if v := strings.TrimSpace(getenv(EnvTheme)); v != "" {
		cfg.Theme = v
	}
	if v := strings.TrimSpace(getenv(EnvAccount)); v != "" {
		cfg.Account = v
	}
	return nil
}

// Valid enumerations.
var (
	ChartStyles = []string{"classic", "solid", "hollow", "ohlc", "line", "heikin", "blocks", "lines", "half", "mono", "ascii"}
	// ChartThemes are the chart skins (chart.PaletteNames, setup-rush's
	// themes) plus "none" for the plain theme-colored chart. Kept in sync
	// by config_test.
	ChartThemes = []string{"terminal", "rush", "paper", "pro", "exchange", "candy", "crt", "neon", "amber", "blueprint",
		"ink", "graphite", "ocean", "violet", "mint", "gold", "glacier", "ember", "parchment", "lagoon", "cobalt",
		"rose", "phosphor", "contrast", "midnight", "bordeaux", "sunset", "forest", "gunmetal", "acid",
		"moonlight", "thermal", "none"}
	ChartZooms   = []int{1, 2, 4, 6}
	LLMProviders = []string{"", "ollama", "lmstudio", "openai-compatible", "anthropic", "openrouter"}
)

var (
	addrRe   = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	envVarRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// IsAddress reports whether s is a 0x-prefixed 20-byte hex address.
func IsAddress(s string) bool { return addrRe.MatchString(s) }

// Validate checks every field and returns all problems joined, each an
// *Error naming its key. The theme name is not checked here (config does not
// import theme); callers resolve it with theme.Select.
func (c Config) Validate() error {
	var errs []error
	bad := func(key, format string, a ...any) {
		errs = append(errs, &Error{Key: key, Msg: fmt.Sprintf(format, a...)})
	}
	if c.Network != domain.Mainnet && c.Network != domain.Testnet {
		bad("network", "%q is not mainnet or testnet", c.Network)
	}
	if c.Account != "" && !IsAddress(c.Account) {
		bad("account", "%q is not a 0x address", c.Account)
	}
	for i, w := range c.Watch {
		if !IsAddress(w) {
			bad(fmt.Sprintf("watch[%d]", i), "%q is not a 0x address", w)
		}
	}
	for i, p := range c.Pinned {
		if strings.TrimSpace(p) == "" {
			bad(fmt.Sprintf("pinned_markets[%d]", i), "empty coin")
		}
	}

	switch strings.ToLower(strings.TrimSpace(c.UI.Motion)) {
	case "", MotionOn, MotionOff:
	default:
		bad("ui.motion", "%q is not on or off", c.UI.Motion)
	}

	if c.Chart.Style == "bars" {
		c.Chart.Style = "ohlc"
	}
	if !slices.Contains(ChartStyles, c.Chart.Style) {
		bad("chart.style", "%q is not one of %s", c.Chart.Style, strings.Join(ChartStyles, ", "))
	}
	if c.Chart.Theme != "" && !slices.Contains(ChartThemes, strings.ToLower(c.Chart.Theme)) {
		bad("chart.theme", "%q is not one of %s", c.Chart.Theme, strings.Join(ChartThemes, ", "))
	}
	if !slices.Contains(ChartZooms, c.Chart.Zoom) {
		bad("chart.zoom", "%d is not one of 1, 2, 4, 6", c.Chart.Zoom)
	}
	if !c.Chart.Interval.Valid() {
		bad("chart.interval", "%q is not a Hyperliquid interval", c.Chart.Interval)
	}
	if len(c.Chart.MAs) > 3 {
		bad("chart.mas", "at most 3 moving averages (got %d)", len(c.Chart.MAs))
	}
	for i, n := range c.Chart.MAs {
		if n < 2 || n > 1000 {
			bad(fmt.Sprintf("chart.mas[%d]", i), "period %d outside 2..1000", n)
		}
	}

	if c.HLPXYZ.BaseURL != "" {
		if err := CheckRemoteURL(c.HLPXYZ.BaseURL); err != nil {
			bad("hlpxyz.base_url", "%v", err)
		}
	}

	for k, ch := range c.HyperEVM {
		key := "hyperevm." + k
		if id, err := strconv.ParseUint(strings.TrimSpace(k), 10, 64); err != nil || id == 0 {
			bad(key, "%q is not a chain id (998 testnet, 999 mainnet)", k)
		}
		for i, u := range ch.RPC {
			if err := CheckRemoteURL(u); err != nil {
				bad(fmt.Sprintf("%s.rpc[%d]", key, i), "%v", err)
			}
		}
		if ch.Info != "" {
			if err := CheckRemoteURL(ch.Info); err != nil {
				bad(key+".info", "%v", err)
			}
		}
		for name, a := range map[string]string{"lens": ch.Lens, "usdc": ch.USDC} {
			if a != "" && !IsAddress(a) {
				bad(key+"."+name, "%q is not a 0x address", a)
			}
		}
	}

	if !slices.Contains(LLMProviders, c.LLM.Provider) {
		bad("llm.provider", "%q is not one of ollama, lmstudio, openai-compatible, anthropic, openrouter", c.LLM.Provider)
	}
	for _, src := range c.Macro.Sources {
		if !slices.Contains(MacroSources, src) {
			bad("macro.sources", "%q is not one of %s", src, strings.Join(MacroSources, ", "))
		}
	}
	if c.Ticks.RetentionDays < 0 || c.Ticks.RetentionDays > 366 {
		bad("ticks.retention_days", "must be 0 (default 7) to 366 days, got %d", c.Ticks.RetentionDays)
	}
	if c.Ticks.MaxMB < 0 {
		bad("ticks.max_mb", "must be 0 (default 512) or more, got %d", c.Ticks.MaxMB)
	}
	if c.LLM.BaseURL != "" {
		if err := checkURL(c.LLM.BaseURL); err != nil {
			bad("llm.base_url", "%v", err)
		} else if err := CheckLLMBaseURL(c.LLM.BaseURL); err != nil {
			bad("llm.base_url", "%v", err)
		}
	}
	if c.LLM.APIKeyEnv != "" {
		switch {
		case !envVarRe.MatchString(c.LLM.APIKeyEnv):
			bad("llm.api_key_env", "must name an environment variable, not hold a key")
		case looksLikeKey(c.LLM.APIKeyEnv):
			bad("llm.api_key_env", "looks like a key; put the key in the environment and name the variable here")
		default:
			if err := CheckLLMKeyEnv(c.LLM.APIKeyEnv); err != nil {
				bad("llm.api_key_env", "%v", err)
			}
		}
	}

	t := c.Trading
	for key, v := range map[string]string{"trading.agent_address": t.AgentAddress, "trading.account": t.AccountAddr, "trading.vault": t.VaultAddress} {
		if v != "" && !IsAddress(v) {
			if len(strings.TrimPrefix(v, "0x")) == 64 {
				bad(key, "looks like a private key; keys never go in config.toml (use HLP_AGENT_KEY or `hlp auth`)")
			} else {
				bad(key, "%q is not a 0x address", v)
			}
		}
	}
	if t.SlippageBps < 0 || t.SlippageBps > 1000 {
		bad("trading.slippage_bps", "%d outside 0..1000", t.SlippageBps)
	}
	if t.Enabled && t.AgentAddress == "" {
		bad("trading.agent_address", "required when trading.enabled = true")
	}
	if _, err := t.Expiry(); err != nil {
		bad("trading.expires", "%v", err)
	}

	if err := c.Risk.Validate(); err != nil {
		errs = append(errs, err)
	}
	// Stable order for messages (map iteration above is random).
	slices.SortStableFunc(errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
	return errors.Join(errs...)
}

func checkURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%q is not an http(s) URL", s)
	}
	return nil
}

// CheckLLMBaseURL refuses plain-http LLM endpoints on other hosts: the API
// key and every prompt would travel in cleartext (audit S7). http is fine
// for this machine (Ollama, LM Studio on localhost / 127.0.0.1 / ::1).
func CheckLLMBaseURL(s string) error { return CheckRemoteURL(s) }

// CheckRemoteURL accepts https anywhere and plain http only for this
// machine. It covers every endpoint config can point at (llm.base_url,
// hlpxyz.base_url, hyperevm.*.rpc and .info): over http on a network a
// third party could read keys and prompts or rewrite the data (audit S7,
// S14).
func CheckRemoteURL(s string) error {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Host == "" {
		return fmt.Errorf("%q is not an http(s) URL", s)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if IsLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("%q: use https for remote endpoints (http is only allowed for localhost)", s)
	default:
		return fmt.Errorf("%q is not an http(s) URL", s)
	}
}

// IsLoopbackHost reports whether h (a URL hostname, no port) is this machine.
func IsLoopbackHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// looksLikeKey flags values that are probably a pasted API key rather than an
// env var name: long, mixed-case and digit-heavy, or with a known key prefix.
func looksLikeKey(s string) bool {
	if strings.HasPrefix(s, "sk") && len(s) > 20 {
		return true
	}
	return len(s) >= 32 && strings.ToUpper(s) != s
}

// Save writes cfg to path atomically (temp file + rename, mode 0600). It
// creates the parent directory (0700). Config holds no secrets by design;
// Save refuses a cfg that does not validate.
func Save(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	body, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	var buf bytes.Buffer
	buf.WriteString(fileHeader)
	buf.Write(body)

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	f, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	tmp := f.Name()
	cleanup := func(err error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("config: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		return cleanup(err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		return cleanup(err)
	}
	if err := f.Sync(); err != nil {
		return cleanup(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

const fileHeader = `# hlp configuration. https://github.com/maxmoneycash/hlp-cli
# Keys never go in this file: use HLP_AGENT_KEY / your LLM key env var, or the
# OS keychain via ` + "`hlp auth`" + `. Unknown keys are an error.

`

// knownKeys lists every dotted key Config accepts, for typo suggestions.
var knownKeys = []string{
	"network", "theme", "account", "watch", "pinned_markets",
	"ui.paint_background", "ui.motion", "ui.floor",
	"chart.style", "chart.theme", "chart.zoom", "chart.interval", "chart.mas", "chart.volume_usd", "chart.utc",
	"hlpxyz.disabled", "hlpxyz.base_url",
	"llm.provider", "llm.base_url", "llm.model", "llm.api_key_env",
	"macro.disabled", "macro.sources",
	"trading.enabled", "trading.agent_address", "trading.account", "trading.vault", "trading.builder_fee", "trading.slippage_bps",
	"trading.allow_unattended", "trading.expires",
	"risk.max_notional_usd", "risk.max_leverage", "risk.allowed_coins", "risk.daily_loss_usd", "risk.dead_man_seconds", "risk.require_confirm",
	"risk.allowed_vaults", "risk.networks", "risk.max_orders_per_min",
	"mcp.allow_trading", "mcp.allow_unattended",
}

// suggestKey returns the known key closest to key, or "" when nothing is close
// (edit distance above a third of the key's length, minimum 2).
func suggestKey(key string) string {
	best, bestD := "", max(2, len(key)/3)+1
	for _, k := range knownKeys {
		if d := editDistance(key, k); d < bestD {
			best, bestD = k, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
