package core

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/maxmoneycash/hlp/internal/evm"
	"github.com/maxmoneycash/hlp/internal/hlpxyz"
	"github.com/maxmoneycash/hlp/internal/hyperliquid"
	"github.com/maxmoneycash/hlp/internal/pvault"
	"github.com/maxmoneycash/hlp/internal/registry"
)

// Strategy-vault cards (HyperEVM pvault protocol). The values equal
// pvault.CardTearsheet, pvault.CardEpochs and pvault.CardUser.
const (
	CardPVault       Card = pvault.CardTearsheet // Data: *pvault.Tearsheet
	CardPVaultEpochs Card = pvault.CardEpochs    // Data: *pvault.EpochTable
	CardPVaultUser   Card = pvault.CardUser      // Data: *pvault.UserView
)

// PVaultOptions configure NewPVaultService.
type PVaultOptions struct {
	Version string
	// Overrides are applied on top of the built-in chains and the config's
	// [hyperevm.<id>] tables (the CLI's --rpc).
	Overrides map[uint64]evm.Chain
	// Telemetry overrides the hlp.xyz client (nil = the Env's).
	Telemetry pvault.Telemetry
	// NewCaller overrides the HyperEVM RPC client factory (tests, offline).
	NewCaller func(evm.Chain) (evm.Caller, error)
}

// EnvHyperEVMRPC names the variable whose comma-separated endpoints are
// tried before the resolved chain's list (beside --rpc, which replaces it).
const EnvHyperEVMRPC = "HLP_HYPEREVM_RPC"

// NewPVaultService builds the strategy-vault service for env: built-in
// chains, then config [hyperevm.<id>] (its rpc list goes in front of the
// built-ins; rpc_only replaces them), then HLP_HYPEREVM_RPC in front of the
// resolved chain's list, then o.Overrides (--rpc, which also turns the
// variable off); hlp.xyz keeper telemetry when enabled (on either
// Hyperliquid network: the only strategy vault lives on testnet 998);
// curated vaults from the registry. The last endpoint that answered is
// kept in <CacheDir>/evm-rpc-<chain>.json. HyperCore reads go through the
// session's Hyperliquid client, and its rate limiter, when the chain's
// info URL is that network. It never touches the network.
func NewPVaultService(env *Env, o PVaultOptions) *pvault.Service {
	chains := evm.DefaultChains()
	po := pvault.Options{UserAgent: "hlp-cli/" + version(o.Version)}
	if len(o.Overrides) == 0 {
		po.ExtraRPC = evm.SplitRPCList(os.Getenv(EnvHyperEVMRPC))
	}
	if env != nil {
		chains = evm.MergeChains(chains, env.Config.HyperEVMChains())
		if env.Now != nil {
			po.Now = env.Now
		}
		if env.Paths.CacheDir != "" {
			po.RPCStateDir = env.Paths.CacheDir
		}
		po.NewCore = sharedCoreInfo(env)
		po.Known = knownStrategyVaults(env.Vaults)
		if env.pvaultTelemetry != nil {
			po.Telemetry = env.pvaultTelemetry
		} else if env.HLPXYZ != nil {
			po.Telemetry = env.HLPXYZ
		}
	}
	if o.Telemetry != nil {
		po.Telemetry = o.Telemetry
	}
	po.NewCaller = o.NewCaller
	po.Chains = evm.MergeChains(chains, o.Overrides)
	return pvault.NewService(po)
}

// infoClient is the part of *hyperliquid.Client that CoreInfo reads need.
type infoClient interface {
	Info(ctx context.Context, req any, out any) error
	Endpoints() hyperliquid.Endpoints
}

// sharedCoreInfo returns a CoreInfo factory that sends HyperCore reads for
// the session's own network through env.HL, so they count against the
// shared weight limiter (SECURITY S13). Other networks get a paced, capped
// HTTPCoreInfo. It returns nil when env.HL cannot post /info (fixtures),
// leaving pvault's default.
func sharedCoreInfo(env *Env) func(evm.Chain) pvault.CoreInfo {
	hl, ok := env.HL.(infoClient)
	if !ok {
		return nil
	}
	own := strings.TrimRight(hl.Endpoints().Info, "/")
	ua := "hlp-cli/" + version(env.version)
	return func(ch evm.Chain) pvault.CoreInfo {
		switch {
		case ch.Info == "":
			return nil
		case strings.EqualFold(strings.TrimRight(ch.Info, "/"), own):
			return pvault.InfoCoreInfo{Info: hl.Info}
		}
		return &pvault.HTTPCoreInfo{URL: ch.Info, UserAgent: ua}
	}
}

// knownStrategyVaults is pvault.KnownVaults plus the registry's HyperEVM
// entries (vaults.toml, including the user's overrides).
func knownStrategyVaults(reg *registry.Registry) []pvault.KnownVault {
	out := append([]pvault.KnownVault(nil), pvault.KnownVaults...)
	if reg == nil {
		return out
	}
	seen := map[string]int{}
	for i, k := range out {
		seen[strings.ToLower(k.Address.String())] = i
	}
	for _, e := range reg.Filter(registry.Filter{Kinds: []registry.Kind{registry.KindHyperEVM}, IncludeTestnet: true}) {
		addr, err := evm.ParseAddress(e.Address)
		if err != nil || e.ChainID <= 0 {
			continue
		}
		if i, ok := seen[strings.ToLower(addr.String())]; ok {
			out[i].Aliases = appendUnique(out[i].Aliases, e.ID)
			continue
		}
		seen[strings.ToLower(addr.String())] = len(out)
		out = append(out, pvault.KnownVault{Slug: e.ID, Aliases: e.Aliases, Name: e.Name, ChainID: uint64(e.ChainID), Address: addr})
	}
	return out
}

// pvaultService returns the Env's shared strategy-vault service (built here
// only for an Env assembled by hand, as in tests).
func (e *Env) pvaultService() *pvault.Service {
	if e.PVault == nil {
		e.PVault = NewPVaultService(e, PVaultOptions{Version: e.version})
	}
	return e.PVault
}

// PVaultError maps pvault/evm errors to the stable error codes: bad input
// and chain mismatches are USAGE, contract reverts API_ERROR, all RPC
// endpoints failing NETWORK.
func PVaultError(err error) error {
	if err == nil {
		return nil
	}
	var ie *pvault.InputError
	if errors.As(err, &ie) {
		return &Error{Code: CodeUsage, Message: ie.Msg, Hint: ie.Hint}
	}
	var cm *evm.ChainMismatchError
	if errors.As(err, &cm) {
		return &Error{Code: CodeUsage, Message: cm.Error(), Hint: "check --rpc and --chain"}
	}
	if errors.Is(err, evm.ErrNoEndpoints) {
		return &Error{Code: CodeUsage, Message: err.Error(), Hint: "pass --rpc or set [hyperevm.<chain>] rpc in config"}
	}
	if re, ok := evm.AsRevert(err); ok {
		return &Error{Code: CodeAPI, Message: "vault call " + re.Error(), Hint: "the contract may be a different version; try --chain or report it"}
	}
	if strings.Contains(err.Error(), "all HyperEVM RPC endpoints failed") {
		return &Error{Code: CodeNetwork, Message: err.Error(), Hint: "public RPCs rate-limit; retry, or pass --rpc"}
	}
	return AsError(err)
}

func runPVault(ctx context.Context, env *Env, in any) (*Result, error) {
	d, m, err := env.pvaultService().Tearsheet(ctx, *in.(*pvault.TearsheetInput))
	if err != nil {
		return nil, PVaultError(err)
	}
	title := "Strategy vault"
	if d != nil && d.Name != "" {
		title = d.Name
	}
	return &Result{Card: CardPVault, Title: title, Data: d, Meta: m}, nil
}

func runPVaultEpochs(ctx context.Context, env *Env, in any) (*Result, error) {
	d, m, err := env.pvaultService().Epochs(ctx, *in.(*pvault.EpochsInput))
	if err != nil {
		return nil, PVaultError(err)
	}
	return &Result{Card: CardPVaultEpochs, Title: "Epochs", Data: d, Meta: m}, nil
}

// FundUser fills in the depositor of a strategy-vault user query: the
// explicit address, else the configured account, else a USAGE error.
func FundUser(env *Env, in *pvault.UserInput) error {
	in.User = strings.TrimSpace(in.User)
	if in.User == "" && env != nil {
		in.User = strings.TrimSpace(env.Config.Account)
	}
	if in.User == "" {
		return &Error{Code: CodeUsage, Message: "depositor address is required", Hint: "pass a depositor 0x address, e.g. `hlp fund user 0x…`, or set `account` in config.toml"}
	}
	return nil
}

func runPVaultUser(ctx context.Context, env *Env, in any) (*Result, error) {
	ui := in.(*pvault.UserInput)
	if err := FundUser(env, ui); err != nil {
		return nil, err
	}
	d, m, err := env.pvaultService().User(ctx, *ui)
	if err != nil {
		return nil, PVaultError(err)
	}
	return &Result{Card: CardPVaultUser, Title: "Depositor", Data: d, Meta: m}, nil
}

// pvaultTools are the read-only strategy-vault tools. `hlp fund` is the
// on-chain tearsheet (it replaced the hlp.xyz-only get_fund); `pvault` is
// kept as an alias.
func pvaultTools() []Tool {
	spec := func(name string) pvault.ToolSpec {
		for _, t := range pvault.Tools {
			if t.Name == name {
				return t
			}
		}
		panic("core: missing pvault tool " + name)
	}
	ts, ep, us := spec("get_strategy_vault"), spec("get_strategy_vault_epochs"), spec("get_strategy_vault_user")
	return []Tool{
		{Name: ts.Name, Command: "fund", Slash: "fund", Aliases: []string{"pvault"}, Positional: ts.Positional, Group: "vault",
			NewInput: in[pvault.TearsheetInput](), Run: runPVault, Summary: ts.Summary, Description: ts.Description},
		{Name: ep.Name, Command: "fund epochs", Slash: "fund-epochs", Aliases: []string{"pvault-epochs", "epochs"}, Positional: ep.Positional, Group: "vault",
			NewInput: in[pvault.EpochsInput](), Run: runPVaultEpochs, Summary: ep.Summary, Description: ep.Description},
		{Name: us.Name, Command: "fund user", Slash: "fund-user", Aliases: []string{"pvault-user"}, Positional: us.Positional, Group: "vault",
			NewInput: in[pvault.UserInput](), Run: runPVaultUser, Summary: us.Summary, Description: us.Description},
	}
}

// ensure hlpxyz.API satisfies the telemetry interface.
var _ pvault.Telemetry = (hlpxyz.API)(nil)
