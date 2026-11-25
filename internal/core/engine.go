package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/maxmoneycash/hlp-cli/internal/pineforge"
	"github.com/maxmoneycash/hlp-cli/internal/pyne"
)

// Pine engine selection (`--engine builtin|pyne|pineforge|auto`).
//
// builtin is the default (config strategy.engine): it is the only engine
// that models Hyperliquid's fees, slippage, funding, szDecimals and
// leverage. PYNE (AGPL-3.0) and PineForge (PineForge Source License) are
// optional programs the user installs; hlp only starts them as
// subprocesses, so they are new attack surface:
//
//   - auto picks PYNE when it is installed, else builtin, and never
//     PineForge;
//   - for agent, MCP and unknown callers auto is builtin and an explicit
//     pyne/pineforge needs strategy.allow_external_engines = true;
//   - PineForge needs the licence acceptance (flag or
//     pineforge.accept_license) and runs backtests only: paper and arena
//     would compile for 5-10 s per bar, and vault-bound runs are
//     Commercial Use under its licence.

// Engine purposes: what the resolved engine will be used for.
const (
	purposeBacktest = "backtest"
	purposeCheck    = "check"
	purposePaper    = "paper"
	purposeArena    = "arena"
	purposeVault    = "vault"
)

// engineSel is a resolved engine.
type engineSel struct {
	Kind    string            // EngineBuiltin | EnginePyne | EnginePineForge
	Pyne    *pyne.Engine      // set for EnginePyne
	Forge   *pineforge.Engine // set for EnginePineForge
	Version string            // engine version for data.engineVersion ("" for builtin)
}

// Hooks for tests: detection of the external engines.
var (
	detectPyne      = pyne.Cached
	detectPineForge = pineforge.Detect
)

// trustedSource reports whether ctx comes from a person at hlp's own
// front-ends (CLI, TUI). Agents, MCP clients and untagged calls are not.
func trustedSource(ctx context.Context) bool {
	switch SourceFrom(ctx) {
	case "cli", "tui":
		return true
	}
	return false
}

// engineName normalises the requested engine: the flag, then config
// strategy.engine, then builtin.
func engineName(env *Env, opt EngineOpt) (string, error) {
	name := strings.ToLower(strings.TrimSpace(opt.Engine))
	if name == "" && env != nil {
		name = strings.ToLower(strings.TrimSpace(env.Config.Strategy.Engine))
	}
	if name == "" {
		name = EngineBuiltin
	}
	switch name {
	case EngineBuiltin, EnginePyne, EnginePineForge, EngineAuto:
		return name, nil
	}
	return "", &Error{Code: CodeUsage, Message: fmt.Sprintf("unknown engine %q", name),
		Hint: "use --engine builtin (default), pyne, pineforge or auto"}
}

// resolveEngine applies the engine rules for one call. warns are notes for
// meta.warnings (auto falling back, the engine version).
func resolveEngine(ctx context.Context, env *Env, opt EngineOpt, purpose string) (engineSel, []string, error) {
	name, err := engineName(env, opt)
	if err != nil {
		return engineSel{}, nil, err
	}
	trusted := trustedSource(ctx)
	allowExternal := trusted || (env != nil && env.Config.Strategy.AllowExternalEngines)
	var warns []string
	switch name {
	case EngineBuiltin:
		return engineSel{Kind: EngineBuiltin}, nil, nil
	case EngineAuto:
		if !allowExternal {
			return engineSel{Kind: EngineBuiltin}, []string{"engine auto: builtin (external engines are off for " + SourceFrom(ctx) + " calls; set strategy.allow_external_engines)"}, nil
		}
		e, err := detectPyne(ctx)
		if err != nil {
			return engineSel{Kind: EngineBuiltin}, []string{"engine auto: PYNE not installed, using builtin"}, nil
		}
		warns = append(warns, "engine auto: PYNE "+e.Version)
		return engineSel{Kind: EnginePyne, Pyne: e, Version: e.Version}, warns, nil
	}
	// Explicit external engine.
	if name == EnginePineForge {
		switch purpose {
		case purposeCheck:
			return engineSel{}, nil, &Error{Code: CodeUsage, Message: "PineForge has no check mode",
				Hint: "use backtest --engine pineforge, or pine check --engine builtin|pyne"}
		case purposePaper, purposeArena:
			return engineSel{}, nil, &Error{Code: CodeUsage, Message: "PineForge runs backtests only (it compiles each script for 5-10 s, too slow for live bars)",
				Hint: "use --engine builtin or pyne for paper and arena"}
		case purposeVault:
			return engineSel{}, nil, &Error{Code: CodeUsage, Message: "PineForge cannot be used for vault-bound runs (other people's capital is Commercial Use under its licence)",
				Hint: "use --engine builtin or pyne"}
		}
	}
	if !allowExternal {
		return engineSel{}, nil, &Error{Code: CodeSafety,
			Message: "the " + name + " engine runs an external program and is off for " + SourceFrom(ctx) + " calls",
			Hint:    "set strategy.allow_external_engines = true in config.toml, or use --engine builtin"}
	}
	switch name {
	case EnginePyne:
		e, err := detectPyne(ctx)
		if err != nil {
			msg := "PYNE is not installed"
			if !errors.Is(err, pyne.ErrNotInstalled) {
				msg = err.Error()
			}
			return engineSel{}, nil, &Error{Code: CodeUsage, Message: msg, Hint: pyne.InstallHint}
		}
		return engineSel{Kind: EnginePyne, Pyne: e, Version: e.Version}, nil, nil
	default: // pineforge
		// Licence acceptance is a human act: the per-run input counts only
		// from the CLI or TUI. Agent, MCP and unknown callers need the
		// person to set pineforge.accept_license in config.toml.
		accepted := (opt.AcceptPineforgeLicense && trustedSource(ctx)) || (env != nil && env.Config.PineForge.AcceptLicense)
		if !accepted {
			hint := "read the notice, then pass --accept-pineforge-license or set pineforge.accept_license = true"
			if !trustedSource(ctx) {
				hint = "a person must read the notice and set pineforge.accept_license = true in config.toml; agents cannot accept it"
			}
			return engineSel{}, nil, &Error{Code: CodeConfirmRequired,
				Message: "PineForge needs its licence notice accepted first",
				Details: map[string]any{"licenseNotice": pineforge.LicenseNotice},
				Hint:    hint}
		}
		e, err := detectPineForge(ctx)
		if err != nil {
			hint := "install Docker or Podman and pull " + pineforge.DefaultImage + " yourself (hlp never pulls it)"
			return engineSel{}, nil, &Error{Code: CodeUsage, Message: err.Error(), Hint: hint}
		}
		e.LicenseAccepted = true
		v := e.Version
		if v == "" {
			v = e.Image
		}
		return engineSel{Kind: EnginePineForge, Forge: e, Version: v}, nil, nil
	}
}
