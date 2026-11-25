package core

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/config"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// Intent is a normalized mutating action, checked before anything is signed.
type Intent struct {
	Action      string // order | cancel | leverage | vault_deposit | vault_withdraw | close_all
	Coin        string
	Side        domain.Side
	NotionalUSD float64
	Leverage    int
	// LeverageFrom says where Leverage came from when the order did not set
	// one: "position" (the open position's leverage), "default" (no
	// position: the order first sends updateLeverage at this value, so the
	// account's remembered per-asset setting cannot exceed the cap) or
	// "unknown" (the account read failed; the cap check fails closed).
	// "" = explicit.
	LeverageFrom string
	ReduceOnly   bool
	Vault        string // trade on behalf of / deposit into
	Network      domain.Network
	Source       string // cli | tui | agent | mcp | bot
}

// Guard enforces config.RiskConfig (max notional, max leverage, allowed
// coins, daily loss, allowed vaults, networks, order rate, trading expiry,
// MCP trading flag). It returns *Error with CodeRiskLimit or
// CodeTradingDisabled; nil means allowed.
//
// Policy is edited only by the person, in config.toml. No tool (CLI, agent
// or MCP) may change risk config: a model that can lift its own limits has
// no limits.
type Guard interface {
	Check(ctx context.Context, in Intent) error
}

// Confirmer asks a human. The TUI shows a confirm card; the CLI prompts on a
// TTY (huh). Agents and MCP never get a Confirmer: they rely on --yes plus
// risk caps, and MCP mutating tools exist only with --allow-trading.
type Confirmer func(ctx context.Context, p OrderPreview) (bool, error)

// DenyAll is the guard used when trading is disabled or has expired.
// Reason and Hint override the default message.
type DenyAll struct {
	Reason string
	Hint   string
}

// Check always refuses.
func (d DenyAll) Check(context.Context, Intent) error {
	e := &Error{Code: CodeTradingDisabled, Message: "trading is disabled", Hint: "run `hlp auth` and set trading.enabled = true; try --testnet first; add --dry-run to preview without trading"}
	if d.Reason != "" {
		e.Message = d.Reason
	}
	if d.Hint != "" {
		e.Hint = d.Hint
	}
	return e
}

// expiredHint is shown when trading.expires has passed.
const expiredHint = "trading.expires in config.toml has passed; a person must edit config.toml to set a new date (no tool can extend it)"

// RiskGuard enforces config.RiskConfig on every mutating intent. It is the
// guard NewEnv installs when trading is enabled; DenyAll otherwise.
type RiskGuard struct {
	Risk        config.RiskConfig
	Network     domain.Network
	TradingOn   bool // config trading.enabled
	MCPAllowed  bool // config mcp.allow_trading (the --allow-trading flag is checked by the MCP server)
	HasAgentKey bool // an agent key and exchange client exist
	// Account is the master account whose fills the daily-loss halt reads
	// (trading.account, else account). A vault intent reads the
	// vault's own fills instead.
	Account string
	// RealizedLoss returns today's realized loss (UTC day, fees included)
	// of account as a positive number.
	RealizedLoss func(ctx context.Context, account string) (float64, error)
	// Trading carries trading.expires (checked on every intent, so a
	// long-running MCP server stops at the expiry too).
	Trading config.TradingConfig
	// Curated reports whether a vault address is in the curated registry
	// (risk.allowed_vaults "curated"). nil = nothing is curated.
	Curated func(addr string) bool
	// Rate counts risk-adding actions for risk.max_orders_per_min. nil =
	// the cap is not enforced (hand-built guards in tests).
	Rate *OrderRate
	// Now is the guard clock (nil = time.Now).
	Now func() time.Time
}

func (g *RiskGuard) now() time.Time {
	if g.Now != nil {
		return g.Now().UTC()
	}
	return time.Now().UTC()
}

// addsRisk reports whether an intent counts against risk.max_orders_per_min:
// orders, leverage changes and vault deposits that are not reduce-only.
func addsRisk(in Intent) bool {
	switch in.Action {
	case "order", "leverage", "vault_deposit":
		return !in.ReduceOnly
	}
	return false
}

// personSource reports whether a person typed the action (CLI or TUI), as
// opposed to MCP, agent, bot or unknown callers.
func personSource(src string) bool { return src == "cli" || src == "tui" }

// NewGuard builds the guard for env: DenyAll when trading is disabled,
// otherwise a RiskGuard over the config caps whose daily-loss check reads
// today's fills of the trading account (or the vault being traded).
func NewGuard(env *Env) Guard {
	cfg := env.Config
	if !cfg.Trading.Enabled {
		return DenyAll{}
	}
	if cfg.Trading.Expired(env.now()) {
		return DenyAll{Reason: "trading expired (trading.expires = " + cfg.Trading.Expires + ")", Hint: expiredHint}
	}
	return newRiskGuard(env)
}

// newRiskGuard builds the config-backed RiskGuard for env (also used for
// the informational checks on a preview while trading is off).
func newRiskGuard(env *Env) *RiskGuard {
	cfg := env.Config
	reg := env.vaultRegistry()
	g := &RiskGuard{
		Risk:        cfg.Risk,
		Network:     cfg.Network,
		TradingOn:   cfg.Trading.Enabled,
		MCPAllowed:  cfg.MCP.AllowTrading,
		HasAgentKey: env.Exchange != nil,
		Trading:     cfg.Trading,
		Now:         env.now,
		Curated: func(addr string) bool {
			e, ok := reg.ByAddress(addr)
			return ok && e.Kind.IsVault()
		},
	}
	ratePath := ""
	if env.Paths.DataDir != "" {
		ratePath = filepath.Join(env.Paths.DataDir, "order-rate-"+string(network(env))+".json")
	}
	g.Rate = NewOrderRate(ratePath)
	g.Account = cfg.Trading.AccountAddr
	if g.Account == "" {
		g.Account = cfg.Account
	}
	if env.HL != nil {
		g.RealizedLoss = func(ctx context.Context, acct string) (float64, error) {
			now := env.now().UTC()
			midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
			fills, err := env.HL.Fills(ctx, acct, midnight.UnixMilli(), now.UnixMilli())
			if err != nil {
				return 0, err
			}
			pnl := 0.0
			for _, f := range fills {
				pnl += f.ClosedPnL - f.Fee
			}
			return math.Max(0, -pnl), nil
		}
	}
	return g
}

// Checks evaluates every cap and returns one domain.Check per rule (shown on
// the order preview). Check returns the first failure as an *Error.
func (g *RiskGuard) Checks(ctx context.Context, in Intent) []domain.Check {
	var out []domain.Check
	add := func(id, label string, ok bool, detail string) {
		st := domain.CheckPass
		if !ok {
			st = domain.CheckFail
		}
		out = append(out, domain.Check{ID: id, Label: label, Status: st, Detail: detail})
	}
	add("trading_enabled", "Trading enabled", g.TradingOn, "config trading.enabled")
	if g.Trading.Expires != "" {
		at, err := g.Trading.Expiry()
		switch {
		case err != nil:
			add("trading_expiry", "Trading not expired", false, "trading.expires is unreadable: "+err.Error())
		default:
			add("trading_expiry", "Trading not expired", g.now().Before(at),
				"trading.expires "+at.Format("2006-01-02 15:04 UTC"))
		}
	}
	add("agent_key", "Agent key present", g.HasAgentKey, "API wallet key in env/keychain (never the master key)")
	if in.Source == "mcp" {
		add("mcp_trading", "MCP trading allowed", g.MCPAllowed, "config mcp.allow_trading and --allow-trading")
	}
	if len(g.Risk.Networks) > 0 {
		net := in.Network
		if net == "" {
			net = g.Network
		}
		add("network_allowed", "Network allowed", g.Risk.AllowsNetwork(net),
			string(net)+" in risk.networks ["+strings.Join(g.Risk.Networks, ", ")+"]")
	}
	if in.Action == "vault_deposit" {
		curated := g.Curated != nil && g.Curated(in.Vault)
		automated := !personSource(in.Source)
		detail := shortAddr(in.Vault)
		switch {
		case len(g.Risk.AllowedVaults) > 0:
			detail += " in risk.allowed_vaults"
		case automated:
			detail += " must be a curated vault (risk.allowed_vaults is empty, caller is " + in.Source + ")"
		default:
			detail += " (risk.allowed_vaults is empty: any vault from the CLI or TUI)"
		}
		add("allowed_vault", "Vault allowed", g.Risk.AllowsVault(in.Vault, curated, automated), detail)
	}
	risky := in.Action == "order" || in.Action == "vault_deposit" || in.Action == "leverage"
	if addsRisk(in) && g.Risk.MaxOrdersPerMin > 0 && g.Rate != nil {
		// Informational here (a warn, so dry runs and previews still
		// work); TakeSend enforces it atomically when the action is sent.
		n := g.Rate.Count(g.now())
		st := domain.CheckPass
		if n >= g.Risk.MaxOrdersPerMin {
			st = domain.CheckWarn
		}
		out = append(out, domain.Check{ID: "max_orders_per_min", Label: "Order rate", Status: st,
			Detail: fmt.Sprintf("%d sent in the last minute of %d max", n, g.Risk.MaxOrdersPerMin)})
	}
	if risky && !in.ReduceOnly && g.Risk.MaxNotionalUSD > 0 {
		add("max_notional", "Notional cap", in.NotionalUSD <= g.Risk.MaxNotionalUSD,
			fmt.Sprintf("$%.2f of $%.2f max", in.NotionalUSD, g.Risk.MaxNotionalUSD))
	}
	if in.LeverageFrom == "unknown" && g.Risk.MaxLeverage > 0 {
		// The account could not be read, so the leverage the order would
		// use is unknown: fail closed (audit S6).
		add("max_leverage", "Leverage cap", false, fmt.Sprintf("current leverage unknown (account read failed) of %dx max", g.Risk.MaxLeverage))
	} else if in.Leverage > 0 && g.Risk.MaxLeverage > 0 {
		src := ""
		switch in.LeverageFrom {
		case "position":
			src = " (current position)"
		case "default":
			src = " (set before the order)"
		}
		add("max_leverage", "Leverage cap", in.Leverage <= g.Risk.MaxLeverage,
			fmt.Sprintf("%dx%s of %dx max", in.Leverage, src, g.Risk.MaxLeverage))
	}
	if in.Coin != "" && len(g.Risk.AllowedCoins) > 0 {
		ok := false
		for _, c := range g.Risk.AllowedCoins {
			if strings.EqualFold(c, in.Coin) {
				ok = true
			}
		}
		add("allowed_coins", "Coin allowed", ok, in.Coin+" in risk.allowed_coins")
	}
	if risky && !in.ReduceOnly && g.Risk.DailyLossUSD > 0 {
		acct := g.Account
		if in.Vault != "" {
			acct = in.Vault
		}
		var loss float64
		var err error
		if acct != "" && g.RealizedLoss != nil {
			loss, err = g.RealizedLoss(ctx, acct)
		}
		switch {
		case (acct == "" || g.RealizedLoss == nil) && g.TradingOn:
			// Fail closed: a halt that cannot read today's fills is no
			// halt at all (audit S6).
			add("daily_loss", "Daily loss halt", false, "set trading.account so the daily loss halt can read today's fills")
		case acct == "" || g.RealizedLoss == nil:
			out = append(out, domain.Check{ID: "daily_loss", Label: "Daily loss halt", Status: domain.CheckWarn,
				Detail: "not tracked: set trading.account so today's fills can be read"})
		case err != nil:
			add("daily_loss", "Daily loss halt", false, "could not read today's fills: "+err.Error())
		default:
			add("daily_loss", "Daily loss halt", loss < g.Risk.DailyLossUSD,
				fmt.Sprintf("$%.2f realized loss today of $%.2f max", loss, g.Risk.DailyLossUSD))
		}
	}
	return out
}

// Check implements Guard.
func (g *RiskGuard) Check(ctx context.Context, in Intent) error {
	for _, c := range g.Checks(ctx, in) {
		if c.Status != domain.CheckFail {
			continue
		}
		code := CodeRiskLimit
		hint := "risk caps live in config.toml [risk]; they apply to CLI, TUI, agent and MCP alike"
		switch c.ID {
		case "trading_enabled", "agent_key", "mcp_trading", "network_allowed":
			code = CodeTradingDisabled
		case "trading_expiry":
			code = CodeTradingDisabled
			hint = expiredHint
		case "allowed_vault":
			hint = "deposits go only to vaults in risk.allowed_vaults (\"curated\" = the curated registry); a person edits config.toml to add one"
		case "max_leverage":
			if in.LeverageFrom != "" {
				hint = "the order would use the account's current leverage; pass --leverage within risk.max_leverage to set it first"
			}
		}
		return &Error{Code: code, Message: c.Label + " failed: " + c.Detail, Details: c, Hint: hint}
	}
	return nil
}

// TakeSend reserves one slot of risk.max_orders_per_min for an intent that
// is about to be sent. It is atomic across goroutines and hlp processes,
// so two concurrent callers cannot both take the last slot; Checks only
// reports the count. Intents that do not add risk always pass.
func (g *RiskGuard) TakeSend(in Intent) error {
	if !addsRisk(in) || g.Risk.MaxOrdersPerMin <= 0 || g.Rate == nil {
		return nil
	}
	n, ok := g.Rate.Take(g.now(), g.Risk.MaxOrdersPerMin)
	if ok {
		return nil
	}
	c := domain.Check{ID: "max_orders_per_min", Label: "Order rate", Status: domain.CheckFail,
		Detail: fmt.Sprintf("%d sent in the last minute of %d max", n, g.Risk.MaxOrdersPerMin)}
	return &Error{Code: CodeRiskLimit, Message: c.Label + " failed: " + c.Detail, Details: c,
		Hint: "too many orders in the last minute (risk.max_orders_per_min); wait and check the account before sending more"}
}
