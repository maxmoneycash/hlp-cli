package safe

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp/internal/evm"
)

// Account kinds.
const (
	KindEOA      = "eoa"      // no code
	KindEIP7702  = "eip7702"  // EOA with a delegation designator: signs with its own key
	KindContract = "contract" // code, but not a Safe we recognise
	KindSafe     = "safe"
)

// Module kinds (heuristic: which Zodiac views answer).
const (
	ModuleRoles   = "roles"
	ModuleDelay   = "delay"
	ModuleUnknown = "unknown"
)

// MaxNamedAgents is HL's limit of named API wallets per account. A
// CoreWriter addApiWallet that cannot be applied is dropped silently.
const MaxNamedAgents = 3

// ExpiryWarnDays flags an agent that expires within this many days.
const ExpiryWarnDays = 14

// InfoFunc posts one Hyperliquid /info body and decodes the answer
// (hyperliquid.Client.Info and pvault.HTTPCoreInfo.Info have this shape).
type InfoFunc func(ctx context.Context, req any, out any) error

// Deps are the read clients. Info may be nil (Core side skipped).
type Deps struct {
	RPC  evm.Caller
	Info InfoFunc
	Now  func() time.Time
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Account is what hlp knows about an address used as a HyperCore account.
type Account struct {
	Address  string     `json:"address"`
	ChainID  uint64     `json:"chainId"`
	Kind     string     `json:"kind"` // eoa | eip7702 | contract | safe
	CodeSize int        `json:"codeSize"`
	Delegate string     `json:"delegate,omitempty"` // EIP-7702 delegate
	Safe     *SafeInfo  `json:"safe,omitempty"`
	Core     *CoreState `json:"core,omitempty"`
	// Summary is one line for a card ("Safe 1.4.1 2/3, modules: Roles, Delay").
	Summary  string   `json:"summary"`
	Warnings []string `json:"warnings,omitempty"`
}

// SafeInfo is a Safe's configuration.
type SafeInfo struct {
	Version         string   `json:"version"`
	Owners          []string `json:"owners"`
	Threshold       uint64   `json:"threshold"`
	Nonce           uint64   `json:"nonce"`
	DomainSeparator string   `json:"domainSeparator,omitempty"` // as the Safe reports it
	DomainMatches   *bool    `json:"domainMatches,omitempty"`   // our computation agrees
	Modules         []Module `json:"modules"`
	ModulesMore     bool     `json:"modulesMore,omitempty"` // more than the first page
}

// Module is one enabled Safe module.
type Module struct {
	Address string `json:"address"`
	Kind    string `json:"kind"` // roles | delay | unknown (heuristic)
	Avatar  string `json:"avatar,omitempty"`
	Target  string `json:"target,omitempty"`
	Owner   string `json:"owner,omitempty"`
	// Delay modifier settings (seconds).
	CooldownSec   *uint64 `json:"cooldownSec,omitempty"`
	ExpirationSec *uint64 `json:"expirationSec,omitempty"`
}

// CoreState is the HyperCore side of the account.
type CoreState struct {
	Role           string       `json:"role"` // userRole: user | agent | vault | subAccount | missing
	AccountExists  bool         `json:"accountExists"`
	Agents         []AgentSlot  `json:"agents"`
	NamedSlotsUsed int          `json:"namedSlotsUsed"`
	NamedSlotsMax  int          `json:"namedSlotsMax"`
	Local          *AgentStatus `json:"localAgent,omitempty"`
}

// AgentSlot is one approved API wallet (extraAgents).
type AgentSlot struct {
	Name        string  `json:"name"`
	Address     string  `json:"address"`
	ValidUntil  int64   `json:"validUntil"` // ms
	DaysLeft    float64 `json:"daysLeft"`
	ExpiresSoon bool    `json:"expiresSoon"` // under ExpiryWarnDays
}

// AgentStatus is userRole of one agent address.
type AgentStatus struct {
	Address string `json:"address"`
	Role    string `json:"role"`
	User    string `json:"user,omitempty"` // the master it trades for
	// Registered is true when the agent trades for this account.
	Registered bool `json:"registered"`
}

// IsDelegation reports an EIP-7702 designator (0xef0100 ‖ address).
func IsDelegation(code []byte) (evm.Address, bool) {
	var a evm.Address
	if len(code) != 23 || !bytes.HasPrefix(code, []byte{0xef, 0x01, 0x00}) {
		return a, false
	}
	copy(a[:], code[3:])
	return a, true
}

var sentinel = evm.MustAddress("0x0000000000000000000000000000000000000001")

// Detect classifies addr and, for a Safe, reads owners, threshold, nonce
// and modules; with Info it adds the HyperCore role, the agent slots and,
// when localAgent is set, whether that agent trades for addr. Read-only.
func Detect(ctx context.Context, d Deps, chainID uint64, addr evm.Address, localAgent *evm.Address) (*Account, error) {
	code, err := d.RPC.GetCode(ctx, addr)
	if err != nil {
		return nil, err
	}
	a := &Account{Address: addr.Hex(), ChainID: chainID, CodeSize: len(code)}
	switch dl, ok := IsDelegation(code); {
	case len(code) == 0:
		a.Kind = KindEOA
	case ok:
		a.Kind, a.Delegate = KindEIP7702, dl.Hex()
	default:
		a.Kind = KindContract
		s, err := readSafe(ctx, d.RPC, chainID, addr)
		if err != nil {
			return nil, err
		}
		if s != nil {
			a.Kind, a.Safe = KindSafe, s
			if s.DomainMatches != nil && !*s.DomainMatches {
				a.Warnings = append(a.Warnings, "the Safe's domainSeparator differs from the computed one: do not trust hashes printed here")
			}
		} else {
			a.Warnings = append(a.Warnings, "contract is not a Safe hlp recognises: it can still be a HyperCore account if it calls CoreWriter itself")
		}
	}
	if d.Info != nil {
		c, err := readCore(ctx, d, addr, localAgent)
		if err != nil {
			a.Warnings = append(a.Warnings, "HyperCore side unavailable: "+err.Error())
		} else {
			a.Core = c
			if !c.AccountExists {
				a.Warnings = append(a.Warnings, "no HyperCore account yet: CoreWriter actions from it are dropped until it holds funds on Core")
			}
			for _, s := range c.Agents {
				if s.ExpiresSoon {
					a.Warnings = append(a.Warnings, fmt.Sprintf("agent %q expires in %.1f days", s.Name, s.DaysLeft))
				}
			}
			if c.NamedSlotsUsed >= MaxNamedAgents {
				a.Warnings = append(a.Warnings, "all named agent slots are used: a new name is dropped by HyperCore")
			}
		}
	}
	a.Summary = summary(a)
	return a, nil
}

func summary(a *Account) string {
	switch a.Kind {
	case KindEOA:
		return "EOA"
	case KindEIP7702:
		return "EOA with EIP-7702 delegation (signs with its own key)"
	case KindContract:
		return "Contract account"
	}
	s := a.Safe
	out := fmt.Sprintf("Contract account (Safe %s %d/%d", s.Version, s.Threshold, len(s.Owners))
	var kinds []string
	for _, m := range s.Modules {
		switch m.Kind {
		case ModuleRoles:
			kinds = append(kinds, "Roles")
		case ModuleDelay:
			kinds = append(kinds, "Delay")
		default:
			kinds = append(kinds, "module "+short(m.Address))
		}
	}
	if len(kinds) > 0 {
		out += ", modules: " + strings.Join(kinds, ", ")
	}
	return out + ")"
}

func short(a string) string {
	if len(a) < 10 {
		return a
	}
	return a[:6] + "…" + a[len(a)-4:]
}

func readSafe(ctx context.Context, rpc evm.Caller, chainID uint64, addr evm.Address) (*SafeInfo, error) {
	msgs := []evm.CallMsg{
		{To: addr, Data: evm.Calldata(selVersion)},
		{To: addr, Data: evm.Calldata(selGetOwners)},
		{To: addr, Data: evm.Calldata(selGetThreshold)},
		{To: addr, Data: evm.Calldata(selNonce)},
		{To: addr, Data: evm.Calldata(selGetModulesPaginated, evm.AddressWord(sentinel), evm.Uint64Word(10))},
		{To: addr, Data: evm.Calldata(selDomainSeparator)},
	}
	res, err := rpc.BatchCall(ctx, msgs)
	if err != nil {
		return nil, err
	}
	if res[1].Err != nil || res[2].Err != nil {
		return nil, nil // not a Safe
	}
	s := &SafeInfo{}
	if w, err := evm.NewWords(res[0].Data); res[0].Err == nil && err == nil {
		s.Version, _ = w.String(0)
	}
	w, err := evm.NewWords(res[1].Data)
	if err != nil {
		return nil, nil
	}
	owners, err := w.TupleArray(0, 1)
	if err != nil {
		return nil, nil
	}
	for _, o := range owners {
		a, err := o.Address(0)
		if err != nil {
			return nil, nil
		}
		s.Owners = append(s.Owners, a.Hex())
	}
	if w, err := evm.NewWords(res[2].Data); err == nil {
		s.Threshold, _ = w.Uint64(0)
	}
	if s.Threshold == 0 || len(s.Owners) == 0 {
		return nil, nil
	}
	if w, err := evm.NewWords(res[3].Data); res[3].Err == nil && err == nil {
		s.Nonce, _ = w.Uint64(0)
	}
	var mods []evm.Address
	if w, err := evm.NewWords(res[4].Data); res[4].Err == nil && err == nil {
		if arr, err := w.TupleArray(0, 1); err == nil {
			for _, e := range arr {
				if m, err := e.Address(0); err == nil {
					mods = append(mods, m)
				}
			}
		}
		if next, err := w.Address(1); err == nil && !next.IsZero() && next != sentinel {
			s.ModulesMore = true
		}
	}
	if w, err := evm.NewWords(res[5].Data); res[5].Err == nil && err == nil && w.Len() == 1 {
		s.DomainSeparator = evm.EncodeHex(w)
		ours := DomainSeparator(chainID, addr, s.Version)
		ok := bytes.Equal(ours[:], w)
		s.DomainMatches = &ok
	}
	s.Modules = make([]Module, 0, len(mods))
	if len(mods) > 0 {
		ms, err := readModules(ctx, rpc, mods)
		if err != nil {
			return nil, err
		}
		s.Modules = ms
	}
	return s, nil
}

func readModules(ctx context.Context, rpc evm.Caller, mods []evm.Address) ([]Module, error) {
	sels := []evm.Selector{selAvatar, selTarget, selOwner, selTxCooldown, selTxExpiration, selDefaultRoles}
	var msgs []evm.CallMsg
	for _, m := range mods {
		for _, s := range sels {
			if s == selDefaultRoles {
				msgs = append(msgs, evm.CallMsg{To: m, Data: evm.Calldata(s, evm.AddressWord(evm.Address{}))})
				continue
			}
			msgs = append(msgs, evm.CallMsg{To: m, Data: evm.Calldata(s)})
		}
	}
	res, err := rpc.BatchCall(ctx, msgs)
	if err != nil {
		return nil, err
	}
	out := make([]Module, len(mods))
	for i, m := range mods {
		r := res[i*len(sels) : (i+1)*len(sels)]
		mod := Module{Address: m.Hex(), Kind: ModuleUnknown}
		addr := func(k int) string {
			if r[k].Err != nil {
				return ""
			}
			w, err := evm.NewWords(r[k].Data)
			if err != nil {
				return ""
			}
			a, err := w.Address(0)
			if err != nil {
				return ""
			}
			return a.Hex()
		}
		u64 := func(k int) *uint64 {
			if r[k].Err != nil {
				return nil
			}
			w, err := evm.NewWords(r[k].Data)
			if err != nil {
				return nil
			}
			v, err := w.Uint64(0)
			if err != nil {
				return nil
			}
			return &v
		}
		mod.Avatar, mod.Target, mod.Owner = addr(0), addr(1), addr(2)
		mod.CooldownSec, mod.ExpirationSec = u64(3), u64(4)
		switch {
		case mod.CooldownSec != nil && mod.ExpirationSec != nil:
			mod.Kind = ModuleDelay
		case r[5].Err == nil && len(r[5].Data) == 32 && mod.Avatar != "":
			mod.Kind = ModuleRoles
		}
		out[i] = mod
	}
	return out, nil
}

// UserRole is Hyperliquid's userRole answer.
type UserRole struct {
	Role string `json:"role"`
	Data struct {
		User   string `json:"user,omitempty"`
		Master string `json:"master,omitempty"`
	} `json:"data"`
}

// ReadUserRole posts userRole (weight 60 of 1,200 per minute per IP: poll
// it every 6 s or more slowly).
func ReadUserRole(ctx context.Context, info InfoFunc, addr evm.Address) (*UserRole, error) {
	var r UserRole
	if err := info(ctx, map[string]string{"type": "userRole", "user": addr.Hex()}, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ReadAgents posts extraAgents.
func ReadAgents(ctx context.Context, info InfoFunc, addr evm.Address) ([]AgentSlotRaw, error) {
	var out []AgentSlotRaw
	err := info(ctx, map[string]string{"type": "extraAgents", "user": addr.Hex()}, &out)
	return out, err
}

// AgentSlotRaw is one extraAgents row.
type AgentSlotRaw struct {
	Name       string `json:"name"`
	Address    string `json:"address"`
	ValidUntil int64  `json:"validUntil"`
}

// AgentCheck returns userRole(agent) judged against account.
func AgentCheck(ctx context.Context, info InfoFunc, agent, account evm.Address) (*AgentStatus, error) {
	r, err := ReadUserRole(ctx, info, agent)
	if err != nil {
		return nil, err
	}
	st := &AgentStatus{Address: agent.Hex(), Role: r.Role, User: r.Data.User}
	st.Registered = r.Role == "agent" && strings.EqualFold(r.Data.User, account.Hex())
	return st, nil
}

func readCore(ctx context.Context, d Deps, addr evm.Address, local *evm.Address) (*CoreState, error) {
	r, err := ReadUserRole(ctx, d.Info, addr)
	if err != nil {
		return nil, err
	}
	c := &CoreState{Role: r.Role, AccountExists: r.Role != "" && r.Role != "missing", NamedSlotsMax: MaxNamedAgents, Agents: []AgentSlot{}}
	raw, err := ReadAgents(ctx, d.Info, addr)
	if err != nil {
		return nil, err
	}
	now := d.now()
	for _, s := range raw {
		left := float64(s.ValidUntil-now.UnixMilli()) / 86_400_000
		left = math.Round(left*10) / 10
		c.Agents = append(c.Agents, AgentSlot{Name: s.Name, Address: s.Address, ValidUntil: s.ValidUntil, DaysLeft: left, ExpiresSoon: left < ExpiryWarnDays})
		if s.Name != "" {
			c.NamedSlotsUsed++
		}
	}
	if local != nil && !local.IsZero() {
		st, err := AgentCheck(ctx, d.Info, *local, addr)
		if err != nil {
			return nil, err
		}
		c.Local = st
	}
	return c, nil
}

// PollSchedule is the userRole polling interval after elapsed time waiting
// for a registration. A CoreWriter action normally lands within a few
// blocks, so the first minute checks every 5 s; after that the wait is
// probably on the Safe's owners, and the interval backs off to 20 s and,
// past 10 min, to a minute. Each userRole call weighs 60 against the
// shared rate budget, and nothing here runs in the background.
func PollSchedule(elapsed time.Duration) time.Duration {
	switch {
	case elapsed < time.Minute:
		return 5 * time.Second
	case elapsed < 10*time.Minute:
		return 20 * time.Second
	}
	return time.Minute
}
