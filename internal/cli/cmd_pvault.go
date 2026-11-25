package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/evm"
	"github.com/maxmoneycash/hlp-cli/internal/pvault"
	"github.com/maxmoneycash/hlp-cli/internal/tui/cards"
)

// pvault commands: read-only HyperEVM strategy vaults (the hlp.xyz pvault
// protocol), added in rootCmd.
//
//	hlp fund [slug|0xaddr] [--chain 998] [--user 0x..] [--epochs 12] [--positions] [--risk] [--no-history] [--rpc url]...
//	hlp fund epochs [slug|0xaddr] [--from N] [--to N]
//	hlp fund user 0xuser [--vault slug|0xaddr]
//
// `pvault` is an alias of `fund`.

// pvaultFlags are shared by the pvault commands.
type pvaultFlags struct {
	chain int
	rpc   []string
}

func (f *pvaultFlags) bind(cmd *cobra.Command) {
	cmd.Flags().IntVar(&f.chain, "chain", 0, safeUsage("chain", "HyperEVM chain id: 998 testnet, 999 mainnet (default: the vault's chain)"))
	cmd.Flags().StringArrayVar(&f.rpc, "rpc", nil, safeUsage("rpc", "HyperEVM RPC URL to use instead of the configured list (repeat or comma-separate for failover; HLP_HYPEREVM_RPC adds endpoints in front of the list instead)"))
}

// NewPVaultService builds the strategy-vault service for an Env (see
// core.NewPVaultService): built-in chains, config [hyperevm.<id>], then
// overrides; hlp.xyz telemetry when enabled.
func NewPVaultService(env *core.Env, version string, overrides map[uint64]evm.Chain) *pvault.Service {
	return core.NewPVaultService(env, core.PVaultOptions{Version: version, Overrides: overrides})
}

// rpcOverride applies --rpc to the chain the call will use (or to every
// known chain when the chain is not yet known: the RPC's chain id check
// rejects a mismatch). --rpc replaces the configured list (RPCOnly).
func rpcOverride(chain int, rpc []string) map[uint64]evm.Chain {
	urls := evm.SplitRPCList(rpc...)
	if len(urls) == 0 {
		return nil
	}
	out := map[uint64]evm.Chain{}
	if chain > 0 {
		out[uint64(chain)] = evm.Chain{RPC: urls, RPCOnly: true}
		return out
	}
	for id := range evm.DefaultChains() {
		out[id] = evm.Chain{RPC: urls, RPCOnly: true}
	}
	return out
}

// PVaultError maps pvault/evm errors to the stable core error codes.
func PVaultError(err error) error { return core.PVaultError(err) }

// pvaultCmd is `hlp fund` (alias `pvault`), with `epochs` and `user`
// subcommands. It is hand-written rather than generated from the
// get_strategy_vault tools so it can take --rpc.
func (a *app) pvaultCmd() *cobra.Command {
	var f pvaultFlags
	var in pvault.TearsheetInput
	cmd := &cobra.Command{
		Use:     "fund [vault]",
		Aliases: []string{"pvault", "strategy-vault"},
		Short:   "Strategy vault tearsheet (HyperEVM): NAV, share price, epoch, fees, roles, bot",
		Long: pvault.Tools[0].Summary + "\n\n" + pvault.Tools[0].Description +
			"\n\nvault is a slug (trend-pyramid) or a 0x contract address; empty = the vault hlp.xyz tracks.",
		Example: "  hlp fund\n  hlp fund trend-pyramid --user 0xF2129a3b4b3594b28DEF2C4B5Cefc2d1Be3356A4\n  hlp fund --json --select navUsd,sharePrice,phase\n" +
			"  hlp fund epochs --from 1 --to 50\n  hlp fund user 0x7ffb5ea882eec736200c51209d668a6972092000",
		GroupID: "vault",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				in.Vault = args[0]
			}
			in.Chain = f.chain
			return a.runPVault(c.Context(), f, pvault.CardTearsheet, "Strategy vault", func(ctx context.Context, s *pvault.Service) (any, domain.Meta, error) {
				return s.Tearsheet(ctx, in)
			})
		},
	}
	f.bind(cmd)
	fl := cmd.Flags()
	fl.StringVar(&in.User, "user", "", safeUsage("user", "0x depositor address: shares, pending requests, redeem preview"))
	fl.IntVar(&in.Epochs, "epochs", 0, "recent settled epochs to include (default 12, max 100)")
	fl.BoolVar(&in.Positions, "positions", false, "also read HyperCore positions and API-wallet expiry (Hyperliquid /info)")
	fl.BoolVar(&in.Risk, "risk", false, "also estimate share-price manipulation risk (positions, book depth, mark vs oracle; cached 60 s)")
	fl.BoolVar(&in.NoHistory, "no-history", false, "skip the hlp.xyz share-price history")
	cmd.AddCommand(a.pvaultEpochsCmd(), a.pvaultUserCmd())
	return cmd
}

func (a *app) pvaultEpochsCmd() *cobra.Command {
	var f pvaultFlags
	var in pvault.EpochsInput
	cmd := &cobra.Command{
		Use:   "epochs [vault]",
		Short: pvault.Tools[1].Summary,
		Long:  pvault.Tools[1].Summary + "\n\n" + pvault.Tools[1].Description,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				in.Vault = args[0]
			}
			in.Chain = f.chain
			return a.runPVault(c.Context(), f, pvault.CardEpochs, "Epochs", func(ctx context.Context, s *pvault.Service) (any, domain.Meta, error) {
				return s.Epochs(ctx, in)
			})
		},
	}
	f.bind(cmd)
	cmd.Flags().IntVar(&in.From, "from", 0, "first epoch (default: to − 49)")
	cmd.Flags().IntVar(&in.To, "to", 0, "last epoch (default: the last settled one)")
	return cmd
}

func (a *app) pvaultUserCmd() *cobra.Command {
	var f pvaultFlags
	var in pvault.UserInput
	cmd := &cobra.Command{
		Use:   "user [0xuser]",
		Short: pvault.Tools[2].Summary,
		Long:  pvault.Tools[2].Summary + "\n\n" + pvault.Tools[2].Description + "\n\nWithout an address it uses the configured account (`account` in config.toml, HLP_ACCOUNT).",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.User, in.Chain = "", f.chain
			if len(args) > 0 {
				in.User = args[0]
			}
			return a.runPVault(c.Context(), f, pvault.CardUser, "Depositor", func(ctx context.Context, s *pvault.Service) (any, domain.Meta, error) {
				return s.User(ctx, in)
			}, func(env *core.Env) error { return core.FundUser(env, &in) })
		},
	}
	f.bind(cmd)
	cmd.Flags().StringVar(&in.Vault, "vault", "", "strategy vault slug or 0x address (default: the vault hlp.xyz tracks)")
	return cmd
}

// runPVault builds the Env and service, runs fn and emits the result as
// JSON (envelope, --select/--limit) or the pvault card.
func (a *app) runPVault(ctx context.Context, f pvaultFlags, card, title string, fn func(context.Context, *pvault.Service) (any, domain.Meta, error), prep ...func(*core.Env) error) error {
	m, err := a.mode()
	if err != nil {
		return err
	}
	env, err := a.env(ctx)
	if err != nil {
		return err
	}
	defer env.Close()
	for _, p := range prep {
		if err := p(env); err != nil {
			return err
		}
	}
	svc := env.PVault
	if len(f.rpc) > 0 || svc == nil {
		svc = NewPVaultService(env, a.bi.Version, rpcOverride(f.chain, f.rpc))
	}
	data, meta, err := fn(core.WithSource(ctx, "cli"), svc)
	if err != nil {
		return PVaultError(err)
	}
	r := &core.Result{Card: core.Card(card), Title: title, Data: data, Meta: meta}
	if m.JSON {
		return writeResult(a.io.Out, r, m)
	}
	out := cards.RenderPVault(r, a.cardOpts(env, m)) // local time, like every CLI card
	_, err = fmt.Fprintln(a.io.Out, strings.TrimRight(out, "\n"))
	return err
}
