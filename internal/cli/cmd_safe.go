package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/evm"
	"github.com/maxmoneycash/hlp/internal/hyperevm/safe"
	"github.com/maxmoneycash/hlp/internal/pvault"
)

// safe commands: read-only contract-account (Safe) support for HyperCore
// trading. Hand-written, not registry
// tools: they are CLI-only for now.
//
//	hlp safe info 0xaddr [--chain 999] [--agent 0x..]
//	hlp safe register-agent --safe 0x.. --agent 0x.. [--name hlp] [--days 90] [--role-key k] --dry-run
//	hlp safe agent-status --safe 0x.. --agent 0x..
//
// hlp holds no EVM key: nothing here signs or sends a transaction.

// Card names for the safe results (no TUI card yet).
const (
	cardSafeAccount = "safe_account"
	cardSafePlan    = "safe_register_plan"
	cardSafeAgent   = "safe_agent_status"
)

func (a *app) safeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "safe",
		Aliases: []string{"contract-account"},
		Short:   "Contract accounts (Safe) on HyperEVM: inspect, and build agent registrations as a dry run",
		Long: "Read a contract account (Safe owners, threshold, modules, HyperCore agent slots) and build, never send, " +
			"the Safe transaction that registers a HyperCore agent through CoreWriter. Owners execute it in Safe{Wallet} or Zodiac Pilot.",
		GroupID: "account",
	}
	cmd.AddCommand(a.safeInfoCmd(), a.safeRegisterCmd(), a.safeAgentStatusCmd())
	return cmd
}

type safeFlags struct {
	pvaultFlags
}

func (f *safeFlags) bind(cmd *cobra.Command) {
	f.pvaultFlags.bind(cmd)
	cmd.Flags().Lookup("chain").Usage = "Chain id on HyperEVM: 999 mainnet (default) or 998 testnet"
}

func (f *safeFlags) chainID() uint64 {
	if f.chain > 0 {
		return uint64(f.chain)
	}
	return evm.ChainMainnet
}

func parseAddr(name, s string) (evm.Address, error) {
	a, err := evm.ParseAddress(strings.TrimSpace(s))
	if err != nil {
		return a, &core.Error{Code: core.CodeUsage, Message: name + " must be a 0x address: " + err.Error()}
	}
	return a, nil
}

// runSafe builds the Env, the chain's readers and runs fn.
func (a *app) runSafe(ctx context.Context, f safeFlags, card, title string, fn func(context.Context, safe.Deps) (any, []string, error), text func(io.Writer, any)) error {
	m, err := a.mode()
	if err != nil {
		return err
	}
	env, err := a.env(ctx)
	if err != nil {
		return err
	}
	defer env.Close()
	chain := f.chainID()
	svc := env.PVault
	if len(f.rpc) > 0 || svc == nil {
		svc = NewPVaultService(env, a.bi.Version, rpcOverride(int(chain), f.rpc))
	}
	r, _, err := svc.Reader(chain)
	if err != nil {
		return PVaultError(err)
	}
	d := safe.Deps{RPC: r.RPC, Now: time.Now}
	if info := pvault.InfoFuncOf(r.Core); info != nil {
		d.Info = safe.InfoFunc(info)
	}
	if env.Now != nil {
		d.Now = env.Now
	}
	data, warns, err := fn(core.WithSource(ctx, "cli"), d)
	if err != nil {
		return safeError(err)
	}
	meta := domain.Meta{Source: "hyperevm+hyperliquid", AsOf: d.Now().UTC(), Network: domain.Mainnet, Warnings: warns}
	if chain == evm.ChainTestnet {
		meta.Network = domain.Testnet
	}
	res := &core.Result{Card: core.Card(card), Title: title, Data: data, Meta: meta}
	if m.JSON {
		return writeResult(a.io.Out, res, m)
	}
	text(a.io.Out, data)
	for _, w := range warns {
		fmt.Fprintln(a.io.Out, "  ! "+w)
	}
	return nil
}

func safeError(err error) error {
	var ue *safe.UsageError
	if errors.As(err, &ue) {
		return &core.Error{Code: core.CodeUsage, Message: ue.Msg}
	}
	var re *safe.RefusedError
	if errors.As(err, &re) {
		return &core.Error{Code: core.CodeSafety, Message: re.Error(), Hint: "nothing was built; fix the blockers first", Details: map[string]any{"blockers": re.Blockers}}
	}
	return PVaultError(err)
}

func (a *app) safeInfoCmd() *cobra.Command {
	var f safeFlags
	var agent string
	cmd := &cobra.Command{
		Use:     "info <0xaddress>",
		Short:   "Classify an address (EOA, EIP-7702, contract, Safe) with owners, threshold, modules and HyperCore agent slots",
		Example: "  hlp safe info 0x… --chain 999\n  hlp safe info 0x… --agent 0x…   # is this agent registered for it?",
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			addr, err := parseAddr("address", args[0])
			if err != nil {
				return err
			}
			var local *evm.Address
			if agent != "" {
				l, err := parseAddr("--agent", agent)
				if err != nil {
					return err
				}
				local = &l
			}
			return a.runSafe(c.Context(), f, cardSafeAccount, "Contract account", func(ctx context.Context, d safe.Deps) (any, []string, error) {
				acct, err := safe.Detect(ctx, d, f.chainID(), addr, local)
				if err != nil {
					return nil, nil, err
				}
				return acct, acct.Warnings, nil
			}, func(w io.Writer, v any) { writeAccountText(w, v.(*safe.Account)) })
		},
	}
	f.bind(cmd)
	cmd.Flags().StringVar(&agent, "agent", "", "Agent address (0x…) to check against this account (userRole)")
	return cmd
}

func (a *app) safeRegisterCmd() *cobra.Command {
	var f safeFlags
	var safeAddr, agent, roleKey, rolesModule string
	var in safe.RegisterInput
	var dry bool
	var nonce int64 = -1
	cmd := &cobra.Command{
		Use:   "register-agent",
		Short: "Build (dry run) the Safe transaction that registers a HyperCore agent via CoreWriter addApiWallet",
		Long: "Prints the CoreWriter call, the SafeTx with its EIP-712 safeTxHash for the Safe's current nonce, and with --role-key the " +
			"Zodiac Roles execTransactionWithRole calldata. It refuses when the agent slots are full, the agent address was used before, " +
			"or the Safe has no HyperCore account. hlp signs and sends nothing: owners execute it in Safe{Wallet} or Zodiac Pilot, then " +
			"`hlp safe agent-status` confirms it on HyperCore.",
		Example: "  hlp safe register-agent --safe 0x… --agent 0x… --name hlp --days 90 --dry-run\n" +
			"  hlp safe register-agent --safe 0x… --agent 0x… --role-key trader --dry-run --json",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if !dry {
				return &core.Error{Code: core.CodeSafety, Message: "register-agent only builds a dry run: hlp has no EVM signer", Hint: "pass --dry-run and execute the printed transaction from the Safe"}
			}
			var err error
			if in.Safe, err = parseAddr("--safe", safeAddr); err != nil {
				return err
			}
			if in.Agent, err = parseAddr("--agent", agent); err != nil {
				return err
			}
			in.ChainID, in.RoleKey, in.RolesModule = f.chainID(), roleKey, rolesModule
			if nonce >= 0 {
				n := uint64(nonce)
				in.Nonce = &n
			}
			return a.runSafe(c.Context(), f, cardSafePlan, "Agent registration (dry run)", func(ctx context.Context, d safe.Deps) (any, []string, error) {
				p, err := safe.PlanRegister(ctx, d, in)
				if err != nil {
					return nil, nil, err
				}
				return p, p.Warnings, nil
			}, func(w io.Writer, v any) { writePlanText(w, v.(*safe.Plan)) })
		},
	}
	f.bind(cmd)
	fl := cmd.Flags()
	fl.StringVar(&safeAddr, "safe", "", "Safe address (0x…): the HyperCore account")
	fl.StringVar(&agent, "agent", "", "Agent (API wallet) address (0x…) to register; use a fresh key")
	fl.StringVar(&in.Name, "name", "hlp", "agent name (\"\" = the unnamed slot); hlp appends valid_until <ms>")
	fl.IntVar(&in.Days, "days", safe.DefaultAgentDays, fmt.Sprintf("validity in days (1..%d)", safe.MaxAgentDays))
	fl.StringVar(&roleKey, "role-key", "", "Zodiac Roles role key (label or 0x bytes32): adds execTransactionWithRole calldata")
	fl.StringVar(&rolesModule, "roles-module", "", "Roles modifier address (0x…; default: the Safe's only Roles module)")
	fl.Int64Var(&nonce, "nonce", -1, "Safe nonce to build for (default: the current nonce)")
	fl.BoolVar(&dry, "dry-run", false, "required: build and print only")
	return cmd
}

func (a *app) safeAgentStatusCmd() *cobra.Command {
	var f safeFlags
	var safeAddr, agent string
	cmd := &cobra.Command{
		Use:   "agent-status",
		Short: "Check once whether an agent is registered for a Safe on HyperCore (userRole; poll no faster than every 5 s)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			s, err := parseAddr("--safe", safeAddr)
			if err != nil {
				return err
			}
			ag, err := parseAddr("--agent", agent)
			if err != nil {
				return err
			}
			return a.runSafe(c.Context(), f, cardSafeAgent, "Agent status", func(ctx context.Context, d safe.Deps) (any, []string, error) {
				if d.Info == nil {
					return nil, nil, &safe.UsageError{Msg: "no HyperCore /info for this chain"}
				}
				st, err := safe.AgentCheck(ctx, d.Info, ag, s)
				return st, nil, err
			}, func(w io.Writer, v any) {
				st := v.(*safe.AgentStatus)
				state := "not registered for this Safe"
				if st.Registered {
					state = "registered"
				}
				fmt.Fprintf(w, "agent %s  role %s  %s\n", st.Address, st.Role, state)
			})
		},
	}
	f.bind(cmd)
	cmd.Flags().StringVar(&safeAddr, "safe", "", "Safe address (0x…)")
	cmd.Flags().StringVar(&agent, "agent", "", "Agent address (0x…)")
	return cmd
}

// Plain-text output (the TUI has no safe card yet).

func writeAccountText(w io.Writer, acct *safe.Account) {
	fmt.Fprintf(w, "%s  chain %d\n%s\n", acct.Address, acct.ChainID, acct.Summary)
	if s := acct.Safe; s != nil {
		fmt.Fprintf(w, "  owners (%d of %d sign): %s\n  nonce %d\n", s.Threshold, len(s.Owners), strings.Join(s.Owners, ", "), s.Nonce)
		for _, m := range s.Modules {
			fmt.Fprintf(w, "  module %s  %s", m.Address, m.Kind)
			if m.CooldownSec != nil {
				fmt.Fprintf(w, "  cooldown %ds", *m.CooldownSec)
			}
			fmt.Fprintln(w)
		}
	}
	if c := acct.Core; c != nil {
		fmt.Fprintf(w, "  HyperCore role %s  agent slots %d/%d named\n", c.Role, c.NamedSlotsUsed, c.NamedSlotsMax)
		for _, g := range c.Agents {
			fmt.Fprintf(w, "    %-30s %s  %.1f days left\n", g.Name, g.Address, g.DaysLeft)
		}
		if l := c.Local; l != nil {
			fmt.Fprintf(w, "  agent %s: role %s, registered for this account: %v\n", l.Address, l.Role, l.Registered)
		}
	}
}

func writePlanText(w io.Writer, p *safe.Plan) {
	fmt.Fprintf(w, "DRY RUN: nothing signed or sent\n%s  chain %d\n%s\n\n", p.Safe, p.ChainID, p.Summary)
	fmt.Fprintf(w, "CoreWriter action  %s\n  raw %s\n\n", p.Action.Describe, p.Action.Raw)
	fmt.Fprintf(w, "Inner call\n  to %s  value 0  operation 0 (CALL)\n  data %s\n\n", p.Inner.To, p.Inner.Data)
	fmt.Fprintf(w, "SafeTx (Safe %s, nonce %d)\n  domainSeparator %s\n  safeTxHash      %s\n", p.SafeTx.SafeVersion, p.SafeTx.Nonce, p.SafeTx.DomainSeparator, p.SafeTx.SafeTxHash)
	if c := p.SafeTx.HashCheckedOnChain; c != nil {
		fmt.Fprintf(w, "  matches the Safe's getTransactionHash: %v\n", *c)
	}
	if r := p.Roles; r != nil {
		fmt.Fprintf(w, "\nRoles path (role %s via %s)\n  calldata %s\n  %s\n", r.RoleKey, r.Module, r.Calldata, r.Note)
	}
	if s := p.Slots; s != nil {
		fmt.Fprintf(w, "\nAgent slots %d/%d named; agent's current role %q\n", s.Used, s.Max, s.AgentRole)
	}
	fmt.Fprintln(w, "\nNext:")
	for _, n := range p.Next {
		fmt.Fprintln(w, "  - "+n)
	}
}
