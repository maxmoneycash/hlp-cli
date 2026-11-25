package pvault

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp/internal/evm"
	"github.com/maxmoneycash/hlp/internal/hlpxyz"
)

// Sources of the state numbers.
const (
	SourceLens       = "lens"
	SourceVaultViews = "vault+hlinfo"
)

// Card names for front-ends (core.Card values once wired).
const (
	CardTearsheet = "pvault"
	CardEpochs    = "pvault_epochs"
	CardUser      = "pvault_user"
)

// Fees are the vault's fee rates (fixed at deploy; no setter exists).
type Fees struct {
	ManagementBps  uint64 `json:"managementBps"`
	PerformanceBps uint64 `json:"performanceBps"`
	ExitBps        uint64 `json:"exitBps"`
}

// Roles are the vault's privileged addresses ("" when unknown).
type Roles struct {
	Owner        string `json:"owner"`
	PendingOwner string `json:"pendingOwner,omitempty"`
	Keeper       string `json:"keeper"`
	Guardian     string `json:"guardian"`
	Manager      string `json:"manager"`
	FeeRecipient string `json:"feeRecipient"`
	APIWallet    string `json:"apiWallet"`
}

// ConfigView is the vault config in human units.
type ConfigView struct {
	EpochDurationSec      uint64   `json:"epochDurationSec"`
	SettleGraceSec        uint64   `json:"settleGraceSec"`
	MinDepositUSD         float64  `json:"minDepositUsd"`
	DepositCapUSD         float64  `json:"depositCapUsd"`
	CapacityUSD           float64  `json:"capacityUsd"` // depositCap − NAV − pending deposits, ≥ 0
	MinIdleBps            uint64   `json:"minIdleBps"`
	MaxPriceMoveBps       uint64   `json:"maxPriceMoveBps"`
	CoreSettleDelayBlocks uint64   `json:"coreSettleDelayBlocks"`
	MaxDeployUSD          float64  `json:"maxDeployUsd"`
	QuotaMode             bool     `json:"quotaMode"`
	DepositAllowlist      bool     `json:"depositAllowlist"`
	NavDexes              []uint32 `json:"navDexes"`
}

// TransferView is an EVM↔Core transfer in flight.
type TransferView struct {
	Kind      string  `json:"kind"` // to-core | to-evm
	AmountUSD float64 `json:"amountUsd"`
	SentAt    int64   `json:"sentAt"` // unix seconds
	Action    uint8   `json:"action"` // raw pendingAction (meaning unknown)
}

// AgentView is the bot's HyperCore API wallet. Expiry comes from HyperCore
// or hlp.xyz, never the EVM.
type AgentView struct {
	Address    string  `json:"address"`
	Name       string  `json:"name,omitempty"`
	ValidUntil int64   `json:"validUntil,omitempty"` // ms
	DaysLeft   float64 `json:"daysLeft,omitempty"`
	Source     string  `json:"source"` // hlp.xyz | hyperliquid | vault
}

// BotView is the hlp.xyz bot telemetry (lastBotRun).
type BotView struct {
	At     int64               `json:"at"` // ms (hlp.xyz sends seconds)
	Paper  bool                `json:"paper"`
	NavUSD float64             `json:"navUsd"`
	GrossX float64             `json:"grossX"`
	Book   []hlpxyz.PVaultBook `json:"book"`
}

// KeeperView is the hlp.xyz keeper telemetry (lastKeeperRun).
type KeeperView struct {
	At  int64 `json:"at"` // ms (hlp.xyz sends seconds)
	Ran bool  `json:"ran"`
}

// EpochRow is one settled epoch in human units.
type EpochRow struct {
	Epoch                 uint64  `json:"epoch"`
	SettledAt             int64   `json:"settledAt"` // unix seconds
	SharePrice            float64 `json:"sharePrice"`
	Return                float64 `json:"return"` // fraction vs the previous row's price (0 for the first)
	NavUSD                float64 `json:"navUsd"`
	SupplyShares          float64 `json:"supplyShares"`
	DepositUSD            float64 `json:"depositUsd"`
	DepositShares         float64 `json:"depositShares"`
	RedeemSharesPending   float64 `json:"redeemSharesPending"`
	RedeemSharesFulfilled float64 `json:"redeemSharesFulfilled"`
	RedeemUSD             float64 `json:"redeemUsd"`
	FeeShares             float64 `json:"feeShares"`
	FeeUSD                float64 `json:"feeUsd"` // feeShares × settle price
}

// HistoryPoint is one hlp.xyz history sample.
type HistoryPoint struct {
	T             int64   `json:"t"` // ms (hlp.xyz sends seconds)
	SharePrice    float64 `json:"sharePrice"`
	NavUSD        float64 `json:"navUsd"`
	SupplyShares  float64 `json:"supplyShares"`
	KeeperGasHYPE float64 `json:"keeperGasHype"`
}

// Tearsheet is the strategy-vault tearsheet: on-chain truth (lens or vault
// views) plus hlp.xyz keeper/bot telemetry and history. Money is USD floats
// for display; Raw keeps exact integers.
type Tearsheet struct {
	ChainID uint64 `json:"chainId"`
	Testnet bool   `json:"testnet"`
	Vault   string `json:"vault"`
	Name    string `json:"name"`
	Symbol  string `json:"symbol"`
	Source  string `json:"source"` // lens | vault+hlinfo
	Lens    string `json:"lens,omitempty"`
	// RPC is the host of the HyperEVM endpoint that answered last;
	// failovers are in meta.warnings.
	RPC string `json:"rpc,omitempty"`

	Epoch           uint64 `json:"epoch"`
	Phase           string `json:"phase"`
	CutoffAt        int64  `json:"cutoffAt"`
	SettleDeadline  int64  `json:"settleDeadline"`
	SecondsToCutoff int64  `json:"secondsToCutoff"`

	NavUSD        float64 `json:"navUsd"`
	SharePrice    float64 `json:"sharePrice"`
	HighWaterMark float64 `json:"highWaterMark"`
	DrawdownVsHWM float64 `json:"drawdownVsHwm"`     // fraction, ≤ 0
	SinceLaunch   float64 `json:"returnSinceLaunch"` // sharePrice − 1 (launch price is 1.0)
	SupplyShares  float64 `json:"supplyShares"`

	IdleUSD             float64       `json:"idleUsd"`
	IdlePct             float64       `json:"idlePct"`     // fraction of NAV
	DeployedPct         float64       `json:"deployedPct"` // (corePerpEquity + coreSpot) / NAV
	CoreSpotUSD         float64       `json:"coreSpotUsd"`
	CorePerpEquityUSD   float64       `json:"corePerpEquityUsd"`
	CoreWithdrawableUSD float64       `json:"coreWithdrawableUsd"`
	InFlightToCoreUSD   float64       `json:"inFlightToCoreUsd"`
	DeployLimit         float64       `json:"deployLimit"` // USD headroom, from the lens; never recomputed
	RedeemReserveUSD    float64       `json:"redeemReserveUsd"`
	PendingDepositsUSD  float64       `json:"pendingDepositsUsd"`
	PendingRedeemShares float64       `json:"pendingRedeemShares"`
	PendingTransfer     *TransferView `json:"pendingTransfer,omitempty"`

	CoreAccountExists bool   `json:"coreAccountExists"`
	CoreLinkProven    bool   `json:"coreLinkProven"`
	QuotaMode         bool   `json:"quotaMode"`
	Paused            bool   `json:"paused"`
	PausedUntil       *int64 `json:"pausedUntil,omitempty"` // nil: view absent (older deployment)

	Fees            Fees        `json:"fees"`
	FeesPaidShares  float64     `json:"feesPaidShares,omitempty"` // over the epochs read
	FeesPaidUSD     float64     `json:"feesPaidUsd,omitempty"`
	Config          *ConfigView `json:"config,omitempty"`
	Roles           Roles       `json:"roles"`
	ManagerShares   float64     `json:"managerShares"`
	ManagerStakeUSD float64     `json:"managerStakeUsd"`

	Agent     *AgentView     `json:"agent,omitempty"`
	Bot       *BotView       `json:"bot,omitempty"`
	Keeper    *KeeperView    `json:"keeper,omitempty"`
	Positions []CorePosition `json:"positions,omitempty"`
	User      *UserView      `json:"user,omitempty"`

	// ManipulationRisk is the share-price manipulation-risk estimate (only
	// with the risk input).
	ManipulationRisk *RiskView `json:"manipulationRisk,omitempty"`

	Epochs  []EpochRow     `json:"epochs,omitempty"`  // most recent last
	History []HistoryPoint `json:"history,omitempty"` // hlp.xyz samples, oldest first

	// Unknown lists fields that could not be read (reverted views, newer
	// ABI, missing lens).
	Unknown []string    `json:"unknown,omitempty"`
	Raw     *VaultState `json:"raw,omitempty"`
}

// UserView is one depositor's position with previews at the current price.
type UserView struct {
	Address              string     `json:"address"`
	Shares               float64    `json:"shares"`
	ValueUSD             float64    `json:"valueUsd"`         // shares at the current share price, before exit fee
	RedeemPreviewUSD     float64    `json:"redeemPreviewUsd"` // after exit fee
	PendingDepositUSD    float64    `json:"pendingDepositUsd"`
	DepositPreviewShares float64    `json:"depositPreviewShares"` // pending deposit at the current price
	ClaimableShares      float64    `json:"claimableShares"`
	PendingRedeemShares  float64    `json:"pendingRedeemShares"`
	ClaimableUSD         float64    `json:"claimableUsd"`
	MayDeposit           bool       `json:"mayDeposit"`
	Vault                string     `json:"vault,omitempty"`     // vault contract address
	VaultName            string     `json:"vaultName,omitempty"` // on-chain or curated name
	ChainID              uint64     `json:"chainId,omitempty"`
	RPC                  string     `json:"rpc,omitempty"` // HyperEVM endpoint host that answered
	Raw                  *UserState `json:"raw,omitempty"`
}

// Inputs is everything BuildTearsheet combines. Only State is required.
type Inputs struct {
	ChainID   uint64
	Vault     evm.Address
	Lens      evm.Address
	Source    string
	State     *VaultState
	Details   *Details // nil: identity/config/roles unknown
	Epochs    []EpochData
	Status    *hlpxyz.PVaultStatus // only when it is this vault's telemetry
	History   []hlpxyz.PVaultSample
	Agents    []CoreAgent
	Positions []CorePosition
	User      *UserState
	UserAddr  evm.Address
	Unknown   []string
	Now       time.Time
}

// BuildTearsheet is pure: it turns raw reads into the tearsheet.
func BuildTearsheet(in Inputs) *Tearsheet {
	s := in.State
	t := &Tearsheet{
		ChainID: in.ChainID, Testnet: in.ChainID == evm.ChainTestnet, Vault: in.Vault.Hex(), Source: in.Source,
		Epoch: s.Epoch, Phase: s.Phase.String(), CutoffAt: int64(s.CutoffAt), SettleDeadline: int64(s.SettleDeadline),
		NavUSD: usd(s.TotalAssets), SharePrice: price(s.SharePrice), HighWaterMark: price(s.HighWaterMark),
		SupplyShares: shares(s.TotalSupply), IdleUSD: usd(s.IdleAssets), CoreSpotUSD: usd(s.CoreSpot),
		CorePerpEquityUSD: usd(s.CorePerpEquity), CoreWithdrawableUSD: usd(s.CorePerpWithdrawable),
		InFlightToCoreUSD: usd(s.InFlightToCore), DeployLimit: usd(s.DeployLimit), RedeemReserveUSD: usd(s.RedeemReserve),
		PendingDepositsUSD: usd(s.TotalPendingDeposits), PendingRedeemShares: shares(s.TotalPendingRedeemShares),
		CoreAccountExists: s.CoreAccountExists, CoreLinkProven: s.CoreLinkProven, QuotaMode: s.QuotaMode, Paused: s.Paused,
		ManagerShares: shares(s.ManagerShares), Raw: s,
		Unknown: append([]string(nil), in.Unknown...),
	}
	if !in.Lens.IsZero() {
		t.Lens = in.Lens.Hex()
	}
	if !in.Now.IsZero() && s.CutoffAt > 0 {
		t.SecondsToCutoff = int64(s.CutoffAt) - in.Now.Unix()
	}
	if t.HighWaterMark > 0 && t.SharePrice > 0 {
		t.DrawdownVsHWM = math.Min(0, t.SharePrice/t.HighWaterMark-1)
	}
	if t.SharePrice > 0 {
		t.SinceLaunch = t.SharePrice - 1
	}
	if t.NavUSD > 0 {
		t.IdlePct = t.IdleUSD / t.NavUSD
		t.DeployedPct = (t.CorePerpEquityUSD + t.CoreSpotUSD) / t.NavUSD
	}
	t.ManagerStakeUSD = t.ManagerShares * t.SharePrice
	if s.PendingKind != TransferNone {
		t.PendingTransfer = &TransferView{Kind: s.PendingKind.String(), AmountUSD: usd(s.PendingAmount), SentAt: int64(s.PendingSentAt), Action: s.PendingAction}
	}
	t.Roles.Manager, t.Roles.APIWallet = addrOr(s.Manager), addrOr(s.APIWallet)
	if d := in.Details; d != nil {
		t.Name, t.Symbol = d.Name, d.Symbol
		t.Fees = Fees{ManagementBps: d.ManagementFeeBps, PerformanceBps: d.PerformanceFeeBps, ExitBps: d.ExitFeeBps}
		t.Roles = Roles{Owner: addrOr(d.Owner), PendingOwner: addrOr(d.PendingOwner), Keeper: addrOr(d.Keeper),
			Guardian: addrOr(d.Guardian), Manager: addrOr(d.Manager), FeeRecipient: addrOr(d.FeeRecipient), APIWallet: addrOr(d.APIWallet)}
		if t.Roles.Manager == "" {
			t.Roles.Manager = addrOr(s.Manager)
		}
		if t.Roles.APIWallet == "" {
			t.Roles.APIWallet = addrOr(s.APIWallet)
		}
		cap := usd(d.DepositCap)
		t.Config = &ConfigView{EpochDurationSec: d.EpochDuration, SettleGraceSec: d.SettleGrace,
			MinDepositUSD: usd(d.MinDeposit), DepositCapUSD: cap, CapacityUSD: math.Max(0, cap-t.NavUSD-t.PendingDepositsUSD),
			MinIdleBps: d.MinIdleBps, MaxPriceMoveBps: d.MaxPriceMoveBps, CoreSettleDelayBlocks: d.CoreSettleDelayBlocks,
			MaxDeployUSD: usd(d.MaxDeploy), QuotaMode: d.QuotaMode, DepositAllowlist: d.DepositAllowlist, NavDexes: d.NavDexes}
		if d.PausedUntil != nil {
			v := int64(*d.PausedUntil)
			t.PausedUntil = &v
		}
		t.Unknown = append(t.Unknown, d.Unknown...)
	}
	t.Epochs = EpochRows(in.Epochs)
	for _, e := range t.Epochs {
		t.FeesPaidShares += e.FeeShares
		t.FeesPaidUSD += e.FeeUSD
	}
	if st := in.Status; st != nil {
		b := st.LastBotRun
		if b.At > 0 {
			t.Bot = &BotView{At: b.At * 1000, Paper: b.Paper || st.Paper, NavUSD: b.NavUSD, GrossX: b.GrossX, Book: b.Book}
		}
		if st.LastKeeperRun.At > 0 {
			t.Keeper = &KeeperView{At: st.LastKeeperRun.At * 1000, Ran: st.LastKeeperRun.Ran}
		}
		if b.Agent.Registered {
			t.Agent = &AgentView{Address: t.Roles.APIWallet, ValidUntil: b.Agent.ValidUntil, DaysLeft: b.Agent.DaysLeft, Source: "hlp.xyz"}
		}
	}
	if t.Agent == nil && t.Roles.APIWallet != "" {
		t.Agent = &AgentView{Address: t.Roles.APIWallet, Source: "vault"}
	}
	for _, a := range in.Agents {
		if t.Agent != nil && strings.EqualFold(a.Address, t.Agent.Address) {
			t.Agent.Name, t.Agent.ValidUntil, t.Agent.Source = a.Name, a.ValidUntil, "hyperliquid"
			if !in.Now.IsZero() {
				t.Agent.DaysLeft = float64(a.ValidUntil-in.Now.UnixMilli()) / 86_400_000
			}
		}
	}
	t.Positions = in.Positions
	for _, h := range in.History {
		t.History = append(t.History, HistoryPoint{T: h.TSec * 1000, SharePrice: h.SharePrice, NavUSD: h.NavUSD, SupplyShares: h.SupplyShares, KeeperGasHYPE: h.KeeperGasHYPE})
	}
	if in.User != nil {
		t.User = BuildUserView(in.UserAddr, in.User, s.SharePrice, t.Fees.ExitBps)
	}
	sort.Strings(t.Unknown)
	t.Unknown = dedupe(t.Unknown)
	return t
}

// BuildUserView converts a userState with previews at sharePrice.
func BuildUserView(addr evm.Address, u *UserState, sharePrice evm.Big, exitBps uint64) *UserView {
	return &UserView{
		Address: addr.Hex(), Shares: shares(u.Shares),
		ValueUSD:             usd(evm.NewBig(PreviewRedeem(u.Shares.V(), sharePrice.V(), 0))),
		RedeemPreviewUSD:     usd(evm.NewBig(PreviewRedeem(u.Shares.V(), sharePrice.V(), exitBps))),
		PendingDepositUSD:    usd(u.PendingDepositAssets),
		DepositPreviewShares: shares(evm.NewBig(PreviewDeposit(u.PendingDepositAssets.V(), sharePrice.V()))),
		ClaimableShares:      shares(u.ClaimableShares), PendingRedeemShares: shares(u.PendingRedeemShares),
		ClaimableUSD: usd(u.ClaimableAssets), MayDeposit: u.MayDeposit, Raw: u,
	}
}

// EpochRows converts settled epochs (ascending) to rows with per-epoch
// share-price returns.
func EpochRows(es []EpochData) []EpochRow {
	out := make([]EpochRow, 0, len(es))
	prev := 0.0
	for _, e := range es {
		if !e.Settled() {
			continue
		}
		px := price(e.SharePrice)
		r := EpochRow{Epoch: e.Epoch, SettledAt: int64(e.SettledAt), SharePrice: px, NavUSD: usd(e.TotalAssetsAfter),
			SupplyShares: shares(e.TotalSupplyAfter), DepositUSD: usd(e.DepositAssets), DepositShares: shares(e.DepositShares),
			RedeemSharesPending: shares(e.RedeemSharesPending), RedeemSharesFulfilled: shares(e.RedeemSharesFulfilled),
			RedeemUSD: usd(e.RedeemAssets), FeeShares: shares(e.FeeShares)}
		r.FeeUSD = r.FeeShares * px
		if prev > 0 {
			r.Return = px/prev - 1
		}
		prev = px
		out = append(out, r)
	}
	return out
}

// EpochTable is the payload of the epochs view.
type EpochTable struct {
	ChainID uint64     `json:"chainId"`
	Vault   string     `json:"vault"`
	Name    string     `json:"name,omitempty"`
	RPC     string     `json:"rpc,omitempty"` // HyperEVM endpoint host that answered
	From    uint64     `json:"from"`
	To      uint64     `json:"to"`
	Epochs  []EpochRow `json:"epochs"`
	// Summary over the rows.
	Return       float64 `json:"return"` // last price / first price − 1
	FeeShares    float64 `json:"feeShares"`
	FeeUSD       float64 `json:"feeUsd"`
	DepositUSD   float64 `json:"depositUsd"`
	RedeemUSD    float64 `json:"redeemUsd"`
	MedianGapSec int64   `json:"medianGapSec"` // between settles
}

// BuildEpochTable summarizes rows.
func BuildEpochTable(chain uint64, vault evm.Address, name string, from, to uint64, es []EpochData) *EpochTable {
	t := &EpochTable{ChainID: chain, Vault: vault.Hex(), Name: name, From: from, To: to, Epochs: EpochRows(es)}
	var gaps []int64
	for i, r := range t.Epochs {
		t.FeeShares += r.FeeShares
		t.FeeUSD += r.FeeUSD
		t.DepositUSD += r.DepositUSD
		t.RedeemUSD += r.RedeemUSD
		if i > 0 {
			gaps = append(gaps, r.SettledAt-t.Epochs[i-1].SettledAt)
		}
	}
	if n := len(t.Epochs); n > 1 && t.Epochs[0].SharePrice > 0 {
		t.Return = t.Epochs[n-1].SharePrice/t.Epochs[0].SharePrice - 1
	}
	if len(gaps) > 0 {
		sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
		t.MedianGapSec = gaps[len(gaps)/2]
	}
	return t
}

func addrOr(a evm.Address) string {
	if a.IsZero() {
		return ""
	}
	return a.Hex()
}

func dedupe(xs []string) []string {
	out := xs[:0]
	for i, x := range xs {
		if i == 0 || x != xs[i-1] {
			out = append(out, x)
		}
	}
	return out
}
