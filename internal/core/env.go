package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/chart"
	"github.com/maxmoneycash/hlp-cli/internal/config"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/hlpxyz"
	"github.com/maxmoneycash/hlp-cli/internal/hyperliquid"
	"github.com/maxmoneycash/hlp-cli/internal/hyperliquid/exchange"
	"github.com/maxmoneycash/hlp-cli/internal/hyperliquid/ws"
	"github.com/maxmoneycash/hlp-cli/internal/llm"
	"github.com/maxmoneycash/hlp-cli/internal/macro"
	"github.com/maxmoneycash/hlp-cli/internal/registry"
	"github.com/maxmoneycash/hlp-cli/internal/theme"
)

// HLPAddress is the Hyperliquidity Provider parent vault.
const HLPAddress = "0xdfc24b077bc1425ad1dea75bcb6f8158e10df303"

// liveTrading is the compile-time Phase-2 gate. While it
// is false no mutating tool can produce a Gate with Live=true, whatever the
// config says. The lead flips it after the owner's testnet run.
const liveTrading = false

// liveBuild reports whether this build may ever produce a live Gate. It is a
// variable only so tests can drive the live path with a fake
// exchange.Client; production code never assigns it.
var liveBuild = func() bool { return liveTrading }

func newEnv(ctx context.Context, o EnvOptions) (*Env, error) {
	paths := config.DefaultPaths()
	if o.ConfigPath != "" {
		paths.ConfigFile = o.ConfigPath
	}
	cfg, err := loadConfig(paths.ConfigFile)
	if err != nil {
		return nil, err
	}
	if o.Network != "" {
		cfg.Network = o.Network
	}
	if cfg.Network == "" {
		cfg.Network = domain.Mainnet
	}
	if cfg.Network != domain.Mainnet && cfg.Network != domain.Testnet {
		return nil, &Error{Code: CodeUsage, Message: "config: network must be mainnet or testnet, got " + string(cfg.Network)}
	}
	th, skin, err := pickTheme(o.Theme, cfg.Theme)
	if err != nil {
		return nil, err
	}
	if skin != "" {
		cfg.Chart.Theme = skin
	}
	env := &Env{
		Config:  cfg,
		Paths:   paths,
		Secrets: config.NewSecrets(),
		Theme:   th,
		Profile: o.Profile,
		Now:     time.Now,
		version: o.Version,
	}
	env.Vaults, env.VaultWarnings = registry.LoadUser(registry.UserPath(paths.ConfigFile))
	ua := "hlp-cli/" + version(o.Version) + " (+https://github.com/maxmoneycash/hlp-cli)"

	fixtures := o.FixturesDir
	if fixtures == "" && o.Offline {
		fixtures = os.Getenv("HLP_FIXTURES")
	}
	if o.Offline || fixtures != "" {
		if fixtures == "" {
			return nil, &Error{Code: CodeUsage, Message: "offline mode needs a fixtures directory", Hint: "set HLP_FIXTURES=<dir>"}
		}
		env.HL = NewFixtureReader(fixtures)
	} else {
		cache := ""
		if paths.CacheDir != "" {
			cache = filepath.Join(paths.CacheDir, string(cfg.Network))
		}
		// One weight budget per IP: the limiter bucket lives in the cache
		// dir so every hlp process on this machine shares it (PERF #3).
		var lim *hyperliquid.Limiter
		if cache != "" {
			lim = hyperliquid.NewPersistentLimiter(filepath.Join(cache, "ratelimit-"+string(cfg.Network)+".json"), 1200, time.Minute)
		}
		hl := hyperliquid.New(hyperliquid.Options{Network: cfg.Network, CacheDir: cache, UserAgent: ua, Limiter: lim})
		env.HL = hl
		ep := hl.Endpoints()
		env.Stream = ws.Dial(ws.Options{URL: ep.WS, Validate: env.validateCoin, OnState: o.OnWSState})
		if !cfg.HLPXYZ.Disabled {
			// hlpxyz appends its own "hlpxyz" subdir (no hlpxyz/hlpxyz).
			x := hlpxyz.New(hlpxyz.Options{BaseURL: cfg.HLPXYZ.BaseURL, UserAgent: ua, CacheDir: paths.CacheDir})
			// hlp.xyz vault analytics describe mainnet vaults only; its
			// strategy-vault keeper telemetry covers testnet 998 too.
			env.pvaultTelemetry = x
			if cfg.Network == domain.Mainnet {
				env.HLPXYZ = x
			}
		}
		if !cfg.Macro.Disabled {
			env.Macro = macro.New(macro.Options{Outcomes: hlOutcomes{hl}, Sources: cfg.Macro.Sources, UserAgent: ua})
		}
		catalog := ""
		if paths.CacheDir != "" {
			catalog = filepath.Join(paths.CacheDir, "openrouter-models.json")
		}
		env.LLMCatalog = llm.NewCatalog(llm.CatalogOptions{CacheFile: catalog, UserAgent: ua})
		if cfg.Trading.Enabled {
			if key, kerr := env.Secrets.Get(config.SecretAgentKey); kerr == nil && !key.Empty() {
				if signer, serr := exchange.NewSigner(key.Reveal()); serr == nil {
					if err := checkAgentAddress(signer.Address(), cfg.Trading.AgentAddress); err != nil {
						return nil, err
					}
					env.Exchange = exchange.NewClient(ep.Exchange, signer)
				}
			}
		}
	}
	env.Guard = NewGuard(env)
	env.PVault = NewPVaultService(env, PVaultOptions{Version: o.Version})
	_ = ctx
	return env, nil
}

// checkAgentAddress refuses a key that does not sign as the configured
// trading.agent_address (SECURITY S6): a swapped key would otherwise sign
// for an account the user never approved.
func checkAgentAddress(signer, configured string) error {
	if configured == "" || strings.EqualFold(signer, configured) {
		return nil
	}
	return &Error{Code: CodeUsage,
		Message: "HLP_AGENT_KEY signs as " + signer + " but trading.agent_address is " + configured,
		Hint:    "set trading.agent_address to the key's address, or store the right key with `hlp auth set`"}
}

// loadConfig reads the config file. A missing file yields Defaults with the
// documented env overrides; a broken file is a USAGE error naming the path.
func loadConfig(path string) (config.Config, error) {
	cfg, err := config.Load(path)
	if err == nil {
		return cfg, nil
	}
	if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
		cfg = config.Defaults()
		applyEnvOverrides(&cfg)
		return cfg, nil
	}
	return config.Config{}, &Error{Code: CodeUsage, Message: "config " + path + ": " + err.Error(), Hint: "fix the file or run with --config pointing elsewhere"}
}

// applyEnvOverrides mirrors config.Load's env handling for the no-file case.
func applyEnvOverrides(cfg *config.Config) {
	if v := os.Getenv("HLP_NETWORK"); v != "" {
		cfg.Network = domain.Network(strings.ToLower(v))
	}
	if v := os.Getenv("HLP_THEME"); v != "" {
		cfg.Theme = v
	}
	if v := os.Getenv("HLP_ACCOUNT"); v != "" {
		cfg.Account = v
	}
}

// pickTheme resolves --theme / config theme. A chart skin name (setup-rush's
// themes: terminal, paper, neon...) is accepted too: it keeps the default UI
// theme and returns the skin for the charts.
func pickTheme(flag, cfg string) (theme.Theme, string, error) {
	name := flag
	if name == "" {
		name = cfg
	}
	if name == "" || name == "auto" {
		return theme.Default(true), "", nil
	}
	t, ok := theme.Get(name)
	if !ok {
		if p, ok := chart.PaletteByName(name); ok {
			return theme.Default(true), p.Name, nil
		}
		return theme.Theme{}, "", &Error{Code: CodeUsage, Message: "unknown theme " + name,
			Hint: "UI themes: " + strings.Join(theme.Names(), ", ") + " (chart skins: /theme in the TUI, or chart.theme in config)"}
	}
	return t, "", nil
}

func version(v string) string {
	if v == "" {
		return "dev"
	}
	return v
}

// Close releases the WebSocket connection (if one was opened).
func (e *Env) Close() error {
	if e == nil || e.Stream == nil {
		return nil
	}
	return e.Stream.Close()
}

// now is the Env clock in UTC (meta.asOf is always UTC for agents).
func (e *Env) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func (e *Env) meta(source string) domain.Meta {
	return domain.Meta{Source: source, AsOf: e.now(), Network: e.Config.Network}
}

// registryMu serializes the first Registry.Refresh.
var registryMu sync.Mutex

// Resolve maps user input ("btc", "SPY", "@107") to a market, refreshing the
// registry on first use. Unknown coins are UNKNOWN_COIN errors carrying the
// registry's suggestions.
func (e *Env) Resolve(ctx context.Context, input string) (domain.Market, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return domain.Market{}, &Error{Code: CodeUsage, Message: "coin is required", Hint: "e.g. BTC, HYPE, xyz:SP500, @107"}
	}
	reg := e.HL.Registry()
	if reg == nil {
		return domain.Market{}, Errorf(CodeInternal, "no market registry")
	}
	registryMu.Lock()
	if len(reg.Markets()) == 0 {
		if err := reg.Refresh(ctx); err != nil {
			registryMu.Unlock()
			return domain.Market{}, AsError(err)
		}
	}
	registryMu.Unlock()
	m, err := reg.Resolve(input)
	if err != nil {
		ae := AsError(err)
		if errors.Is(err, hyperliquid.ErrUnknownCoin) {
			ae = &Error{Code: CodeUnknownCoin, Message: err.Error(), Hint: "list markets with `hlp markets " + input + "`"}
		}
		return domain.Market{}, ae
	}
	return m, nil
}

// ResolveTrade is Resolve for mutating tools: a bare HIP-3 name that more
// than one builder dex lists ("nvda") is refused with the qualified
// choices, so an order never lands on a market the user did not mean.
func (e *Env) ResolveTrade(ctx context.Context, input string) (domain.Market, error) {
	m, err := e.Resolve(ctx, input)
	if err != nil {
		return m, err
	}
	if alts := hyperliquid.Alternatives(e.HL.Registry(), input); len(alts) > 1 {
		return domain.Market{}, &Error{Code: CodeUsage,
			Message: fmt.Sprintf("%q is listed on %d builder dexes: %s", input, len(alts), strings.Join(alts, ", ")),
			Hint:    "name the dex, e.g. " + alts[0]}
	}
	return m, nil
}

// validateCoin is the ws.Validator: coins must resolve before they reach
// the socket (an unknown coin would otherwise silently never update).
func (e *Env) validateCoin(coin string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := e.Resolve(ctx, coin)
	return err
}

var addrRe = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// IsAddress reports whether s is a 0x-prefixed 20-byte hex address.
func IsAddress(s string) bool { return addrRe.MatchString(strings.TrimSpace(s)) }

// address resolves an address argument: explicit, else config account.
func (e *Env) address(in string) (string, error) {
	a := strings.TrimSpace(in)
	if a == "" {
		a = e.Config.Account
	}
	if a == "" {
		return "", &Error{Code: CodeUsage, Message: "address is required", Hint: "pass a 0x address or set `account` in config.toml (HLP_ACCOUNT)"}
	}
	if !IsAddress(a) {
		return "", &Error{Code: CodeUsage, Message: "not an address: " + a, Hint: "addresses look like 0x followed by 40 hex characters"}
	}
	return strings.ToLower(a), nil
}

// needHLPXYZ returns the hlp.xyz client or a NOT_FOUND error explaining
// why it is off. Offline, disabled or testnet is a runtime condition, not a
// usage error (RELEASE R9): the command was typed correctly.
func (e *Env) needHLPXYZ() (hlpxyz.API, error) {
	if e.HLPXYZ == nil {
		return nil, &Error{Code: CodeNotFound, Message: "hlp.xyz data is unavailable", Hint: "hlp.xyz is disabled in config (hlpxyz.disabled), offline, or on testnet"}
	}
	return e.HLPXYZ, nil
}
