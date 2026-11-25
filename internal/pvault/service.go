package pvault

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/evm"
	"github.com/maxmoneycash/hlp-cli/internal/hlpxyz"
)

// Telemetry is hlp.xyz's /api/pvault (keeper and bot state, history).
// hlpxyz.API satisfies it, with its rate limiting and caching.
type Telemetry interface {
	PVaultStatus(ctx context.Context) (hlpxyz.PVaultStatus, error)
	PVaultHistory(ctx context.Context, chainID int, vault string) ([]hlpxyz.PVaultSample, error)
}

// KnownVault is a curated strategy vault (until a registry exists).
type KnownVault struct {
	Slug    string      `json:"slug"`
	Aliases []string    `json:"aliases,omitempty"`
	Name    string      `json:"name"`
	ChainID uint64      `json:"chainId"`
	Address evm.Address `json:"address"`
}

// KnownVaults are the built-in strategy vaults. The integrator should also
// merge vaults.toml entries here.
var KnownVaults = []KnownVault{
	{Slug: "trend-pyramid", Aliases: []string{"tp", "hlptp", "trend"}, Name: "Trend Pyramid (testnet)", ChainID: evm.ChainTestnet,
		Address: evm.MustAddress("0x688dD654bDbDE2BF8b6154685437f3F9dB2eAABd")},
}

// Options configure a Service.
type Options struct {
	// Chains defaults to evm.DefaultChains().
	Chains map[uint64]evm.Chain
	// Telemetry is optional (nil when hlp.xyz is disabled).
	Telemetry Telemetry
	HTTP      *http.Client
	UserAgent string
	Now       func() time.Time
	// NewCaller overrides the RPC client factory (tests, offline fixtures).
	NewCaller func(evm.Chain) (evm.Caller, error)
	// NewCore overrides the HyperCore /info client factory. Core passes an
	// adapter over the shared Hyperliquid client when the chain's info URL
	// is the session's network, so these reads share its rate limiter.
	NewCore func(evm.Chain) CoreInfo
	// ExtraRPC is tried before the resolved chain's configured endpoints
	// (HLP_HYPEREVM_RPC).
	ExtraRPC []string
	// RPCStateDir, when set, keeps the last endpoint that answered per chain
	// in <RPCStateDir>/evm-rpc-<chain>.json for the next process.
	RPCStateDir string
	// Known overrides KnownVaults.
	Known []KnownVault
}

// Service is the MCP/CLI/TUI entry point: one per process, safe for
// concurrent use. All reads are public and read-only.
type Service struct {
	o       Options
	mu      sync.Mutex
	readers map[uint64]*Reader

	riskMu    sync.Mutex
	riskCache map[string]*RiskView // chain|vault → last read (RiskTTL)
	depth     map[uint64]MarketInfo
}

// NewService returns a Service. It does not touch the network.
func NewService(o Options) *Service {
	if o.Chains == nil {
		o.Chains = evm.DefaultChains()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Known == nil {
		o.Known = KnownVaults
	}
	return &Service{o: o, readers: map[uint64]*Reader{}, riskCache: map[string]*RiskView{}, depth: map[uint64]MarketInfo{}}
}

// Reader returns (and caches) the chain's reader.
func (s *Service) Reader(chainID uint64) (*Reader, evm.Chain, error) {
	ch, ok := s.o.Chains[chainID]
	if !ok {
		return nil, ch, &InputError{Msg: fmt.Sprintf("unknown HyperEVM chain %d", chainID), Hint: "use --chain 998 (testnet) or 999 (mainnet), or add [hyperevm." + strconv.FormatUint(chainID, 10) + "] to hlp.toml"}
	}
	ch.ID = chainID
	if len(s.o.ExtraRPC) > 0 {
		ch.RPC = evm.PrependRPC(ch.RPC, s.o.ExtraRPC...)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.readers[chainID]; r != nil {
		return r, ch, nil
	}
	var caller evm.Caller
	var err error
	if s.o.NewCaller != nil {
		caller, err = s.o.NewCaller(ch)
	} else {
		eo := evm.Options{Endpoints: ch.RPC, ChainID: chainID, HTTP: s.o.HTTP, UserAgent: s.o.UserAgent, Errors: Errors, Now: s.o.Now}
		if s.o.RPCStateDir != "" {
			eo.StateFile = filepath.Join(s.o.RPCStateDir, "evm-rpc-"+strconv.FormatUint(chainID, 10)+".json")
		}
		caller, err = evm.NewClient(eo)
	}
	if err != nil {
		return nil, ch, err
	}
	r := &Reader{RPC: caller}
	if ch.Lens != "" {
		if r.Lens, err = evm.ParseAddress(ch.Lens); err != nil {
			return nil, ch, fmt.Errorf("chain %d lens: %w", chainID, err)
		}
	}
	switch {
	case s.o.NewCore != nil:
		r.Core = s.o.NewCore(ch)
	case ch.Info != "":
		r.Core = &HTTPCoreInfo{URL: ch.Info, HTTP: s.o.HTTP, UserAgent: s.o.UserAgent}
	}
	s.readers[chainID] = r
	return r, ch, nil
}

// InputError is a usage problem (bad address, unknown slug or chain).
// Front-ends map it to their USAGE error code.
type InputError struct{ Msg, Hint string }

func (e *InputError) Error() string { return e.Msg }

// TearsheetInput is the input of get_strategy_vault (`hlp fund`).
type TearsheetInput struct {
	Vault     string `json:"vault,omitempty" jsonschema:"strategy vault slug (trend-pyramid) or 0x contract address; empty = the vault hlp.xyz tracks"`
	Chain     int    `json:"chain,omitempty" jsonschema:"HyperEVM chain id: 998 testnet or 999 mainnet; default = the vault's known chain, else 998"`
	User      string `json:"user,omitempty" jsonschema:"0x depositor address: adds their shares, pending requests and redeem/deposit previews"`
	Epochs    int    `json:"epochs,omitempty" jsonschema:"recent settled epochs to include (default 12, max 100, 0 = default)"`
	Positions bool   `json:"positions,omitempty" jsonschema:"also read the vault's HyperCore positions and API-wallet expiry from Hyperliquid /info"`
	Risk      bool   `json:"risk,omitempty" jsonschema:"also estimate share-price manipulation risk (manipulationRisk) from free public reads: positions, order-book depth within 10 bp, mark vs oracle; cached 60 s per vault"`
	NoHistory bool   `json:"noHistory,omitempty" jsonschema:"skip the hlp.xyz share-price history"`
}

// EpochsInput is the input of get_strategy_vault_epochs (`hlp fund epochs`).
type EpochsInput struct {
	Vault string `json:"vault,omitempty" jsonschema:"strategy vault slug or 0x contract address; empty = the vault hlp.xyz tracks"`
	Chain int    `json:"chain,omitempty" jsonschema:"HyperEVM chain id (998 testnet, 999 mainnet)"`
	From  int    `json:"from,omitempty" jsonschema:"first epoch (default: to − 49)"`
	To    int    `json:"to,omitempty" jsonschema:"last epoch (default: the last settled one)"`
}

// UserInput is the input of get_strategy_vault_user (`hlp fund user`).
type UserInput struct {
	Vault string `json:"vault,omitempty" jsonschema:"strategy vault slug or 0x contract address"`
	Chain int    `json:"chain,omitempty" jsonschema:"HyperEVM chain id (998 testnet, 999 mainnet)"`
	User  string `json:"user" jsonschema:"0x depositor address"`
}

// Resolve turns a slug, address or "" into (chain, address).
func (s *Service) Resolve(ctx context.Context, vault string, chain int) (uint64, evm.Address, error) {
	v := strings.TrimSpace(vault)
	cid := uint64(0)
	if chain < 0 {
		return 0, evm.Address{}, &InputError{Msg: "chain must be positive"}
	}
	if chain > 0 {
		cid = uint64(chain)
	}
	if v == "" {
		if s.o.Telemetry != nil {
			if st, err := s.o.Telemetry.PVaultStatus(ctx); err == nil && evm.IsAddress(st.Vault) {
				if cid == 0 {
					cid = uint64(st.ChainID)
				}
				return orChain(cid, evm.ChainTestnet), evm.MustAddress(st.Vault), nil
			}
		}
		if len(s.o.Known) > 0 {
			k := s.o.Known[0]
			return orChain(cid, k.ChainID), k.Address, nil
		}
		return 0, evm.Address{}, &InputError{Msg: "no strategy vault given", Hint: "pass a 0x vault address"}
	}
	if strings.HasPrefix(strings.ToLower(v), "0x") {
		a, err := evm.ParseAddress(v)
		if err != nil {
			return 0, a, &InputError{Msg: "vault must be a 0x contract address: " + err.Error()}
		}
		for _, k := range s.o.Known {
			if k.Address == a {
				return orChain(cid, k.ChainID), a, nil
			}
		}
		return orChain(cid, evm.ChainTestnet), a, nil
	}
	low := strings.ToLower(v)
	for _, k := range s.o.Known {
		if k.Slug == low || contains(k.Aliases, low) {
			return orChain(cid, k.ChainID), k.Address, nil
		}
	}
	var slugs []string
	for _, k := range s.o.Known {
		slugs = append(slugs, k.Slug)
	}
	return 0, evm.Address{}, &InputError{Msg: fmt.Sprintf("unknown strategy vault %q", v), Hint: "known: " + strings.Join(slugs, ", ") + "; or pass a 0x address"}
}

func orChain(c, d uint64) uint64 {
	if c != 0 {
		return c
	}
	return d
}

func (s *Service) meta(chain uint64) domain.Meta {
	m := domain.Meta{Source: "hyperevm", AsOf: s.o.Now().UTC(), Network: domain.Mainnet}
	if chain == evm.ChainTestnet {
		m.Network = domain.Testnet
	}
	return m
}

// rpcReporter is the part of *evm.Client that says which endpoint answered
// and which failed first.
type rpcReporter interface {
	Endpoint() string
	TakeFailovers() []evm.Failover
}

// rpcReport adds one warning per endpoint that failed before another
// answered, and returns the host that answered last ("" when the caller
// cannot say, e.g. fixtures).
func rpcReport(r *Reader, m *domain.Meta) string {
	rep, ok := r.RPC.(rpcReporter)
	if !ok {
		return ""
	}
	seen := map[string]bool{}
	for _, f := range rep.TakeFailovers() {
		w := "rpc " + EndpointHost(f.Endpoint) + " failed (" + f.Reason + "); used " + EndpointHost(f.Used)
		if !seen[w] {
			seen[w] = true
			m.Warnings = append(m.Warnings, w)
		}
	}
	return EndpointHost(rep.Endpoint())
}

// EndpointHost is the host of an RPC URL (the path and any key in it are
// dropped), or the input when it does not parse.
func EndpointHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host
}

func warn(m *domain.Meta, format string, a ...any) {
	m.Partial = true
	m.Warnings = append(m.Warnings, fmt.Sprintf(format, a...))
}

// state reads the vault state via the lens or, without one, the vault's
// views; it returns the details too (always read: identity, fees, roles).
func (s *Service) state(ctx context.Context, r *Reader, vault evm.Address, m *domain.Meta) (*VaultState, *Details, string, []string, error) {
	if err := r.CheckCode(ctx, vault); err != nil {
		if errors.Is(err, ErrNotAVault) {
			return nil, nil, "", nil, &InputError{Msg: vault.Hex() + " has no contract on this chain", Hint: "check --chain (998 testnet, 999 mainnet)"}
		}
		return nil, nil, "", nil, err
	}
	d, derr := r.Details(ctx, vault)
	if derr != nil {
		warn(m, "vault views unavailable: %v", derr)
		d = nil
	}
	if r.HasLens() {
		st, err := r.LensVaultState(ctx, vault)
		if err == nil {
			return st, d, SourceLens, nil, nil
		}
		if d == nil {
			return nil, nil, "", nil, err
		}
		warn(m, "lens read failed (%v); assembled from vault views", err)
	}
	if d == nil {
		return nil, nil, "", nil, derr
	}
	st, unknown, err := r.StateFromViews(ctx, vault, d)
	if err != nil {
		return nil, nil, "", nil, err
	}
	return st, d, SourceVaultViews, unknown, nil
}

// telemetry fetches hlp.xyz status (only if it is this vault) and history.
func (s *Service) telemetry(ctx context.Context, chain uint64, vault evm.Address, history bool, m *domain.Meta) (*hlpxyz.PVaultStatus, []hlpxyz.PVaultSample) {
	if s.o.Telemetry == nil {
		return nil, nil
	}
	var stp *hlpxyz.PVaultStatus
	st, err := s.o.Telemetry.PVaultStatus(ctx)
	switch {
	case err != nil:
		warn(m, "hlp.xyz keeper/bot status unavailable: %v", err)
	case strings.EqualFold(st.Vault, vault.Hex()) && uint64(st.ChainID) == chain:
		stp = &st
	}
	var hist []hlpxyz.PVaultSample
	if history {
		h, err := s.o.Telemetry.PVaultHistory(ctx, int(chain), vault.Hex())
		if err != nil {
			warn(m, "hlp.xyz history unavailable: %v", err)
		}
		hist = h
	}
	if stp != nil || len(hist) > 0 {
		m.Source = "hyperevm+hlp.xyz"
	}
	return stp, hist
}

// Tearsheet is the MCP-ready strategy-vault tearsheet: lens/vault views,
// recent epochs, hlp.xyz telemetry and history, and optionally a user's
// position and the vault's Core positions. Partial failures become meta
// warnings, not errors.
func (s *Service) Tearsheet(ctx context.Context, in TearsheetInput) (*Tearsheet, domain.Meta, error) {
	chain, vault, err := s.Resolve(ctx, in.Vault, in.Chain)
	if err != nil {
		return nil, domain.Meta{}, err
	}
	var user evm.Address
	if in.User != "" {
		if user, err = evm.ParseAddress(in.User); err != nil {
			return nil, domain.Meta{}, &InputError{Msg: "user must be a 0x address: " + err.Error()}
		}
	}
	r, ch, err := s.Reader(chain)
	if err != nil {
		return nil, domain.Meta{}, err
	}
	m := s.meta(chain)
	st, d, source, unknown, err := s.state(ctx, r, vault, &m)
	if err != nil {
		return nil, m, err
	}
	n := in.Epochs
	if n <= 0 {
		n = 12
	}
	n = min(n, MaxEpochsPerCall)
	in2 := Inputs{ChainID: chain, Vault: vault, Lens: r.Lens, Source: source, State: st, Details: d, Unknown: unknown, Now: s.o.Now(), UserAddr: user}
	if last := lastSettled(st); last > 0 {
		from := uint64(1)
		if last > uint64(n) {
			from = last - uint64(n) + 1
		}
		es, err := r.Epochs(ctx, vault, from, last)
		if err != nil {
			warn(&m, "epochs unavailable: %v", err)
		}
		in2.Epochs = es
	}
	if !user.IsZero() {
		u, err := r.UserState(ctx, vault, user)
		if err != nil {
			warn(&m, "user state unavailable: %v", err)
		}
		in2.User = u
	}
	if in.Positions && r.Core != nil {
		in2.Positions, in2.Agents = s.corePositions(ctx, r, vault, d, true, &m)
	}
	in2.Status, in2.History = s.telemetry(ctx, chain, vault, !in.NoHistory, &m)
	t := BuildTearsheet(in2)
	if in.Risk {
		t.ManipulationRisk = s.risk(ctx, r, ch, vault, t, d, in2.Positions, in.Positions, &m)
	}
	t.RPC = rpcReport(r, &m)
	if t.Name == "" {
		for _, k := range s.o.Known {
			if k.Address == vault {
				t.Name = k.Name
			}
		}
	}
	if ch.Testnet() {
		m.Warnings = append(m.Warnings, "HyperEVM testnet (chain 998) prototype; not investable")
	}
	return t, m, nil
}

func lastSettled(st *VaultState) uint64 {
	if st.Epoch <= 1 {
		return 0
	}
	return st.Epoch - 1 // currentEpoch is open
}

func (s *Service) corePositions(ctx context.Context, r *Reader, vault evm.Address, d *Details, withAgents bool, m *domain.Meta) ([]CorePosition, []CoreAgent) {
	dexes := []uint32{0}
	if d != nil && len(d.NavDexes) > 0 {
		dexes = d.NavDexes
	}
	names, err := r.dexNames(ctx, dexes)
	if err != nil {
		warn(m, "perpDexs unavailable: %v", err)
	}
	var pos []CorePosition
	for _, dx := range dexes {
		name := ""
		if dx != 0 {
			if int(dx) >= len(names) {
				continue
			}
			name = names[dx]
		}
		ch, err := r.Core.ClearinghouseState(ctx, vault.Hex(), name)
		if err != nil {
			warn(m, "positions on dex %d unavailable: %v", dx, err)
			continue
		}
		pos = append(pos, ch.Positions...)
	}
	if !withAgents {
		return pos, nil
	}
	agents, err := r.Core.ExtraAgents(ctx, vault.Hex())
	if err != nil {
		warn(m, "API wallet expiry unavailable: %v", err)
	}
	return pos, agents
}

// Epochs is the MCP-ready epoch table (VaultLens.epochRange, paged).
func (s *Service) Epochs(ctx context.Context, in EpochsInput) (*EpochTable, domain.Meta, error) {
	chain, vault, err := s.Resolve(ctx, in.Vault, in.Chain)
	if err != nil {
		return nil, domain.Meta{}, err
	}
	if in.From < 0 || in.To < 0 || (in.To > 0 && in.From > in.To) {
		return nil, domain.Meta{}, &InputError{Msg: fmt.Sprintf("bad epoch range %d..%d", in.From, in.To)}
	}
	r, _, err := s.Reader(chain)
	if err != nil {
		return nil, domain.Meta{}, err
	}
	m := s.meta(chain)
	to := uint64(in.To)
	var name string
	if to == 0 || in.From == 0 {
		st, d, _, _, err := s.state(ctx, r, vault, &m)
		if err != nil {
			return nil, m, err
		}
		if d != nil {
			name = d.Name
		}
		if last := lastSettled(st); to == 0 || to > last {
			to = last
		}
	}
	from := uint64(in.From)
	if from == 0 {
		from = 1
		if to > 50 {
			from = to - 49
		}
	}
	if to-from+1 > 1000 {
		return nil, m, &InputError{Msg: "at most 1000 epochs per request", Hint: "narrow --from/--to"}
	}
	es, err := r.Epochs(ctx, vault, from, to)
	if err != nil && len(es) == 0 {
		return nil, m, err
	}
	if err != nil {
		warn(&m, "some epochs unavailable: %v", err)
	}
	t := BuildEpochTable(chain, vault, name, from, to, es)
	t.RPC = rpcReport(r, &m)
	return t, m, nil
}

// User is the MCP-ready depositor view with previews at the current price.
func (s *Service) User(ctx context.Context, in UserInput) (*UserView, domain.Meta, error) {
	if !evm.IsAddress(in.User) {
		return nil, domain.Meta{}, &InputError{Msg: "user must be a 0x address"}
	}
	chain, vault, err := s.Resolve(ctx, in.Vault, in.Chain)
	if err != nil {
		return nil, domain.Meta{}, err
	}
	r, _, err := s.Reader(chain)
	if err != nil {
		return nil, domain.Meta{}, err
	}
	m := s.meta(chain)
	st, d, _, _, err := s.state(ctx, r, vault, &m)
	if err != nil {
		return nil, m, err
	}
	user := evm.MustAddress(in.User)
	u, err := r.UserState(ctx, vault, user)
	if err != nil {
		return nil, m, err
	}
	var exit uint64
	if d != nil {
		exit = d.ExitFeeBps
	}
	v := BuildUserView(user, u, st.SharePrice, exit)
	v.Vault, v.ChainID = vault.Hex(), chain
	v.RPC = rpcReport(r, &m)
	if d != nil {
		v.VaultName = d.Name
	}
	for _, k := range s.o.Known {
		if v.VaultName == "" && k.Address == vault {
			v.VaultName = k.Name
		}
	}
	return v, m, nil
}

// ToolSpec describes an MCP/CLI tool for the integrator's registry.
type ToolSpec struct {
	Name, Command, Slash, Group, Summary, Description string
	Positional                                        []string
	Card                                              string
}

// Tools are the read-only strategy-vault tools.
var Tools = []ToolSpec{
	{Name: "get_strategy_vault", Command: "fund", Slash: "fund", Group: "vault", Positional: []string{"vault"}, Card: CardTearsheet,
		Summary: "HyperEVM strategy vault tearsheet: NAV, share price, epoch, fees, roles, bot",
		Description: "Read-only tearsheet of an hlp.xyz strategy vault (HyperEVM async-deposit vault, e.g. Trend Pyramid on testnet 998) from the VaultLens " +
			"(or the vault's own views when the chain has no lens): NAV (USD), share price (USDC per share; also the next settle's deposit price), high-water mark, " +
			"drawdown vs HWM (fraction), epoch phase and cutoff, idle vs deployed-to-HyperCore, deploy headroom, fees in bps, roles, recent settled epochs, " +
			"hlp.xyz keeper/bot telemetry and share-price history. With user: that address's shares and redeem preview. Never signs or sends anything."},
	{Name: "get_strategy_vault_epochs", Command: "fund epochs", Slash: "fund-epochs", Group: "vault", Positional: []string{"vault"}, Card: CardEpochs,
		Summary: "Settled epochs of a strategy vault: price, NAV, deposits, redemptions, fees",
		Description: "Per-epoch settlement history from VaultLens.epochRange (authoritative, 100 epochs per call): settle time, share price, NAV after, " +
			"deposits, redemptions (requested vs filled; fills are pro-rata when idle cash is short) and performance-fee shares. Returns are fractions."},
	{Name: "get_strategy_vault_user", Command: "fund user", Slash: "fund-user", Group: "vault", Positional: []string{"user"}, Card: CardUser,
		Summary:     "A depositor's strategy-vault position with redeem and deposit previews",
		Description: "Shares, value at the current share price, redeem preview after the exit fee, pending and claimable deposit/redeem requests, and whether the address may deposit."},
}
