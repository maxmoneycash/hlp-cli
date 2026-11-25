package safe

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/evm"
	"github.com/maxmoneycash/hlp-cli/internal/hyperevm/corewriter"
)

// Agent validity bounds (days). HL parses the expiry from the name suffix;
// 90 days matches the default in Hyperliquid's own API-wallet page.
const (
	DefaultAgentDays = 90
	MaxAgentDays     = 180
)

// RegisterInput describes an agent registration through a Safe.
type RegisterInput struct {
	ChainID uint64
	Safe    evm.Address
	Agent   evm.Address
	// Name is the agent name without the valid_until suffix ("" = the
	// account's unnamed slot, which takes no expiry suffix).
	Name string
	Days int // 0 = DefaultAgentDays
	// RoleKey (label or 0x bytes32) adds the Zodiac Roles path; it needs a
	// Roles module on the Safe (or RolesModule).
	RoleKey     string
	RolesModule string
	// Nonce overrides the Safe's current nonce (to queue behind pending
	// Safe transactions).
	Nonce *uint64
}

// RefusedError lists why a registration is refused (SAFETY_REFUSED).
type RefusedError struct{ Blockers []string }

func (e *RefusedError) Error() string {
	return "agent registration refused: " + strings.Join(e.Blockers, "; ")
}

// UsageError is a bad input (USAGE).
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

// Plan is the dry-run output: everything an owner needs to propose,
// approve and verify the transaction in their own wallet. hlp signs and
// sends nothing.
type Plan struct {
	DryRun  bool   `json:"dryRun"` // always true
	ChainID uint64 `json:"chainId"`
	Safe    string `json:"safe"`
	Summary string `json:"summary"` // the account summary line

	Action PlanAction `json:"action"`
	Inner  PlanCall   `json:"inner"`
	SafeTx PlanSafeTx `json:"safeTx"`
	Roles  *PlanRoles `json:"roles,omitempty"`

	Slots    *PlanSlots `json:"slots,omitempty"`
	Next     []string   `json:"next"`
	Warnings []string   `json:"warnings,omitempty"`
}

// PlanAction is the CoreWriter action.
type PlanAction struct {
	ID         uint32 `json:"id"`
	Name       string `json:"name"`
	Agent      string `json:"agent"`
	AgentName  string `json:"agentName"`            // as sent, with the valid_until suffix
	ValidUntil int64  `json:"validUntil,omitempty"` // ms
	Raw        string `json:"raw"`                  // 0x01 ‖ uint24 id ‖ fields
	Describe   string `json:"describe"`
}

// PlanCall is the call the Safe executes.
type PlanCall struct {
	To        string `json:"to"`
	Value     string `json:"value"`
	Data      string `json:"data"`
	Operation uint8  `json:"operation"` // 0 = CALL
}

// PlanSafeTx is the SafeTx and its EIP-712 hash.
type PlanSafeTx struct {
	To              string `json:"to"`
	Value           string `json:"value"`
	Data            string `json:"data"`
	Operation       uint8  `json:"operation"`
	SafeTxGas       string `json:"safeTxGas"`
	BaseGas         string `json:"baseGas"`
	GasPrice        string `json:"gasPrice"`
	GasToken        string `json:"gasToken"`
	RefundReceiver  string `json:"refundReceiver"`
	Nonce           uint64 `json:"nonce"`
	SafeVersion     string `json:"safeVersion"`
	DomainSeparator string `json:"domainSeparator"`
	SafeTxHash      string `json:"safeTxHash"`
	// HashCheckedOnChain is set when the Safe's own getTransactionHash was
	// read: true means it agrees.
	HashCheckedOnChain *bool `json:"hashCheckedOnChain,omitempty"`
}

// PlanRoles is the Zodiac Roles path for a role member.
type PlanRoles struct {
	Module   string `json:"module"`
	RoleKey  string `json:"roleKey"`
	To       string `json:"to"` // the Roles modifier
	Calldata string `json:"calldata"`
	Note     string `json:"note"`
}

// PlanSlots is the agent-slot check.
type PlanSlots struct {
	Used        int    `json:"used"`
	Max         int    `json:"max"`
	ReplacesOld bool   `json:"replacesExisting"` // the name exists: the new agent overwrites it
	AgentRole   string `json:"agentRole"`        // userRole of the new agent (should be missing)
}

// Build is the pure part: it validates in against what Detect read (acct
// may lack Core, then the slot check is skipped with a warning) and
// returns the plan.
func Build(in RegisterInput, acct *Account, now time.Time) (*Plan, error) {
	if in.Agent.IsZero() {
		return nil, &UsageError{Msg: "agent address is required (removal with the zero address is unproven; not built here)"}
	}
	days := in.Days
	if days == 0 {
		days = DefaultAgentDays
	}
	if days < 1 || days > MaxAgentDays {
		return nil, &UsageError{Msg: fmt.Sprintf("days must be 1..%d", MaxAgentDays)}
	}
	name := strings.TrimSpace(in.Name)
	if strings.Contains(name, "valid_until") {
		return nil, &UsageError{Msg: "name must not contain valid_until (hlp adds the suffix)"}
	}
	var blockers, warns []string
	if acct == nil || acct.Kind != KindSafe || acct.Safe == nil {
		kind := "unknown"
		if acct != nil {
			kind = acct.Kind
		}
		how := "an EOA approves agents with the user-signed approveAgent instead"
		if kind == KindContract {
			how = "a contract that is not a Safe must send CoreWriter addApiWallet itself (as pvault's owner-only setApiWallet does)"
		}
		blockers = append(blockers, fmt.Sprintf("%s is not a Safe (%s): %s", in.Safe.Hex(), kind, how))
		return nil, &RefusedError{Blockers: blockers}
	}
	s := acct.Safe
	if in.Agent == in.Safe {
		blockers = append(blockers, "the agent cannot be the Safe itself")
	}
	for _, o := range s.Owners {
		if strings.EqualFold(o, in.Agent.Hex()) {
			warns = append(warns, "the agent is also a Safe owner: use a dedicated trading key")
		}
	}
	var validUntil time.Time
	agentName := name
	if name != "" {
		validUntil = now.Add(time.Duration(days) * 24 * time.Hour)
		agentName = corewriter.AgentName(name, validUntil)
	} else {
		warns = append(warns, "empty name: uses the single unnamed agent slot, with HL's default validity")
	}
	var slots *PlanSlots
	if c := acct.Core; c != nil {
		slots = &PlanSlots{Used: c.NamedSlotsUsed, Max: c.NamedSlotsMax}
		if !c.AccountExists {
			blockers = append(blockers, "the Safe has no HyperCore account yet: deposit USDC to Core first (CoreDepositWallet), or the action is dropped")
		}
		for _, a := range c.Agents {
			if base, _, _ := corewriter.ParseAgentName(a.Name); name != "" && (base == name || a.Name == name) {
				slots.ReplacesOld = true
			}
			if strings.EqualFold(a.Address, in.Agent.Hex()) {
				blockers = append(blockers, "this agent is already registered for the Safe")
			}
		}
		if name != "" && !slots.ReplacesOld && c.NamedSlotsUsed >= c.NamedSlotsMax {
			blockers = append(blockers, fmt.Sprintf("all %d named agent slots are used and %q is a new name: HyperCore would drop the action", c.NamedSlotsMax, name))
		}
		if l := c.Local; l != nil && strings.EqualFold(l.Address, in.Agent.Hex()) {
			slots.AgentRole = l.Role
			if l.Role != "" && l.Role != "missing" && !l.Registered {
				blockers = append(blockers, fmt.Sprintf("the agent address already has HL role %q: generate a fresh key (agent addresses are never reused)", l.Role))
			}
		}
		if slots.ReplacesOld {
			warns = append(warns, fmt.Sprintf("name %q exists: the new agent replaces the old one in that slot", name))
		}
	} else {
		warns = append(warns, "HyperCore not read: agent-slot and account checks were skipped")
	}
	if len(blockers) > 0 {
		return nil, &RefusedError{Blockers: blockers}
	}

	raw := corewriter.AddAPIWallet(in.Agent, agentName)
	data := corewriter.SendRawActionCalldata(raw)
	nonce := s.Nonce
	if in.Nonce != nil {
		nonce = *in.Nonce
		if nonce < s.Nonce {
			return nil, &UsageError{Msg: fmt.Sprintf("nonce %d is below the Safe's current nonce %d", nonce, s.Nonce)}
		}
	}
	tx := Tx{To: corewriter.Address, Value: new(big.Int), Data: data, Operation: Call, Nonce: nonce}
	dom := DomainSeparator(in.ChainID, in.Safe, s.Version)
	h := tx.Hash(in.ChainID, in.Safe, s.Version)
	zero := evm.Address{}.Hex()
	p := &Plan{
		DryRun: true, ChainID: in.ChainID, Safe: in.Safe.Hex(), Summary: acct.Summary,
		Action: PlanAction{ID: corewriter.ActionAddAPIWallet, Name: "addApiWallet", Agent: in.Agent.Hex(), AgentName: agentName,
			Raw: evm.EncodeHex(raw), Describe: corewriter.Describe(raw)},
		Inner: PlanCall{To: corewriter.Address.Hex(), Value: "0", Data: evm.EncodeHex(data), Operation: uint8(Call)},
		SafeTx: PlanSafeTx{To: corewriter.Address.Hex(), Value: "0", Data: evm.EncodeHex(data), Operation: uint8(Call),
			SafeTxGas: "0", BaseGas: "0", GasPrice: "0", GasToken: zero, RefundReceiver: zero, Nonce: nonce,
			SafeVersion: s.Version, DomainSeparator: evm.EncodeHex(dom[:]), SafeTxHash: evm.EncodeHex(h[:])},
		Slots: slots,
	}
	if !validUntil.IsZero() {
		p.Action.ValidUntil = validUntil.UnixMilli()
	}
	if s.Version == "" {
		warns = append(warns, "Safe VERSION() unreadable: the hash assumes the chain-bound (≥ 1.3.0) domain")
	}
	if in.Nonce != nil && *in.Nonce > s.Nonce {
		warns = append(warns, fmt.Sprintf("nonce %d queues behind %d pending Safe transaction(s)", *in.Nonce, *in.Nonce-s.Nonce))
	}
	if in.RoleKey != "" {
		key, err := ParseRoleKey(in.RoleKey)
		if err != nil {
			return nil, &UsageError{Msg: err.Error()}
		}
		mod := strings.TrimSpace(in.RolesModule)
		if mod == "" {
			for _, m := range s.Modules {
				if m.Kind == ModuleRoles {
					if mod != "" {
						return nil, &UsageError{Msg: "the Safe has several Roles modules: pass the module address"}
					}
					mod = m.Address
				}
			}
		}
		if mod == "" {
			return nil, &UsageError{Msg: "no Roles module found on the Safe: pass the module address, or drop the role key"}
		}
		ma, err := evm.ParseAddress(mod)
		if err != nil {
			return nil, &UsageError{Msg: "roles module: " + err.Error()}
		}
		p.Roles = &PlanRoles{Module: ma.Hex(), RoleKey: evm.EncodeHex(key[:]), To: ma.Hex(),
			Calldata: evm.EncodeHex(ExecTransactionWithRoleCalldata(corewriter.Address, new(big.Int), data, Call, key, true)),
			Note:     "a role member sends this to the Roles modifier; it succeeds only if the role's scope allows CoreWriter.sendRawAction with this payload (role scoping for this call is untested)"}
	}
	p.Next = []string{
		"Propose the inner call in Safe{Wallet} (or Zodiac Pilot) with the nonce above; each owner checks the safeTxHash on their device before signing.",
		"A CoreWriter call that HyperCore rejects still succeeds on HyperEVM: confirm on Core by polling userRole(agent) until role=agent and user=the Safe (every 6 s or slower).",
		"HL also drops an action it cannot apply (full slots): re-check the slots right before executing a transaction that waited.",
	}
	p.Warnings = warns
	return p, nil
}

// PlanRegister reads the Safe and HyperCore (Detect), builds the plan and,
// when the RPC allows, cross-checks the hash with the Safe's own
// getTransactionHash. Read-only.
func PlanRegister(ctx context.Context, d Deps, in RegisterInput) (*Plan, error) {
	acct, err := Detect(ctx, d, in.ChainID, in.Safe, &in.Agent)
	if err != nil {
		return nil, err
	}
	p, err := Build(in, acct, d.now())
	if err != nil {
		return nil, err
	}
	p.Warnings = append(p.Warnings, acct.Warnings...)
	data, _ := evm.DecodeHex(p.Inner.Data)
	tx := Tx{To: corewriter.Address, Value: new(big.Int), Data: data, Operation: Call, Nonce: p.SafeTx.Nonce}
	if b, err := d.RPC.Call(ctx, in.Safe, tx.GetTransactionHashCalldata()); err == nil && len(b) == 32 {
		ok := strings.EqualFold(evm.EncodeHex(b), p.SafeTx.SafeTxHash)
		p.SafeTx.HashCheckedOnChain = &ok
		if !ok {
			p.Warnings = append(p.Warnings, "the Safe's getTransactionHash differs from the computed safeTxHash: do not sign")
		}
	}
	return p, nil
}

// IsRefused reports a RefusedError.
func IsRefused(err error) bool {
	var r *RefusedError
	return errors.As(err, &r)
}
