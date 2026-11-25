package pvault

import (
	"strconv"

	"github.com/maxmoneycash/hlp-cli/internal/evm"
)

// Decimals and fixed-point scales used by the strategy-vault contracts.
const (
	USDCDecimals  = 6  // asset amounts
	ShareDecimals = 18 // vault shares
	PriceDecimals = 18 // sharePrice / highWaterMark: USDC per share × 1e18
	// ConvDecimals is the shares↔assets conversion factor exponent:
	// shares = assets × 1e30 / sharePrice, assets = shares × sharePrice / 1e30.
	ConvDecimals = 30
)

// Phase is the epoch phase.
type Phase uint8

// Phases.
const (
	PhaseOpen               Phase = 0
	PhaseSettling           Phase = 1
	PhaseAwaitingSettlement Phase = 2
)

func (p Phase) String() string {
	switch p {
	case PhaseOpen:
		return "open"
	case PhaseSettling:
		return "settling"
	case PhaseAwaitingSettlement:
		return "awaiting-settlement"
	}
	return "phase-" + strconv.Itoa(int(p))
}

// TransferKind is pendingTransfer.kind.
type TransferKind uint8

// Transfer kinds.
const (
	TransferNone   TransferKind = 0
	TransferToCore TransferKind = 1
	TransferToEVM  TransferKind = 2
)

func (k TransferKind) String() string {
	switch k {
	case TransferNone:
		return "none"
	case TransferToCore:
		return "to-core"
	case TransferToEVM:
		return "to-evm"
	}
	return "kind-" + strconv.Itoa(int(k))
}

// VaultState is VaultLens.vaultState(vault): 28 static words, raw units.
// Without a lens the same struct is assembled from direct vault views and
// HyperCore /info (Source "vault+hlinfo").
type VaultState struct {
	Epoch          uint64 `json:"epoch"`
	Phase          Phase  `json:"phase"`
	CutoffAt       uint64 `json:"cutoffAt"`
	SettleDeadline uint64 `json:"settleDeadline"`

	TotalAssets   evm.Big `json:"totalAssets"`   // USDC 6 dp (NAV)
	TotalSupply   evm.Big `json:"totalSupply"`   // shares 18 dp
	SharePrice    evm.Big `json:"sharePrice"`    // 1e18 fp
	HighWaterMark evm.Big `json:"highWaterMark"` // 1e18 fp

	TotalPendingDeposits     evm.Big `json:"totalPendingDeposits"`     // USDC
	TotalPendingRedeemShares evm.Big `json:"totalPendingRedeemShares"` // shares
	RedeemReserve            evm.Big `json:"redeemReserve"`            // USDC
	IdleAssets               evm.Big `json:"idleAssets"`               // USDC

	CoreSpot             evm.Big `json:"coreSpot"`             // USDC
	CorePerpEquity       evm.Big `json:"corePerpEquity"`       // USDC, signed
	CorePerpWithdrawable evm.Big `json:"corePerpWithdrawable"` // USDC
	InFlightToCore       evm.Big `json:"inFlightToCore"`       // USDC

	PendingKind   TransferKind `json:"pendingKind"`
	PendingAmount evm.Big      `json:"pendingAmount"` // USDC
	PendingAction uint8        `json:"pendingAction"` // meaning unknown; shown raw
	PendingSentAt uint64       `json:"pendingSentAt"`

	CoreAccountExists bool    `json:"coreAccountExists"`
	CoreLinkProven    bool    `json:"coreLinkProven"`
	DeployLimit       evm.Big `json:"deployLimit"` // USDC; display it, never recompute
	QuotaMode         bool    `json:"quotaMode"`
	Paused            bool    `json:"paused"`

	APIWallet     evm.Address `json:"apiWallet"`
	Manager       evm.Address `json:"manager"`
	ManagerShares evm.Big     `json:"managerShares"`
}

// UserState is VaultLens.userState(vault, user).
type UserState struct {
	Shares               evm.Big `json:"shares"`
	PendingDepositAssets evm.Big `json:"pendingDepositAssets"`
	ClaimableShares      evm.Big `json:"claimableShares"`
	PendingRedeemShares  evm.Big `json:"pendingRedeemShares"`
	ClaimableAssets      evm.Big `json:"claimableAssets"`
	MayDeposit           bool    `json:"mayDeposit"`
}

// EpochData is epochData(e) / one epochRange element.
type EpochData struct {
	Epoch                 uint64  `json:"epoch"` // set by the reader, not in the tuple
	SettledAt             uint64  `json:"settledAt"`
	SharePrice            evm.Big `json:"sharePrice"`
	TotalAssetsAfter      evm.Big `json:"totalAssetsAfter"`
	TotalSupplyAfter      evm.Big `json:"totalSupplyAfter"`
	DepositAssets         evm.Big `json:"depositAssets"`
	DepositShares         evm.Big `json:"depositShares"`
	RedeemSharesPending   evm.Big `json:"redeemSharesPending"`
	RedeemSharesFulfilled evm.Big `json:"redeemSharesFulfilled"`
	RedeemAssets          evm.Big `json:"redeemAssets"`
	FeeShares             evm.Big `json:"feeShares"`
}

// Settled reports whether the epoch has settled (the open epoch is zeros).
func (e EpochData) Settled() bool { return e.SettledAt != 0 }

// PendingTransfer is pendingTransfer(): one EVM↔Core transfer in flight.
type PendingTransfer struct {
	Kind       TransferKind `json:"kind"`
	SentBlock  uint64       `json:"sentBlock"`
	SentAt     uint64       `json:"sentAt"`
	Amount     evm.Big      `json:"amount"`
	SpotBefore evm.Big      `json:"spotBefore"`
}

// Details are the direct vault views the lens does not return: identity,
// config, fees, roles and transfer bookkeeping. Fields whose view reverted
// (newer-ABI views on an older deployment) are listed in Unknown and left
// zero.
type Details struct {
	Name     string      `json:"name"`
	Symbol   string      `json:"symbol"`
	Decimals uint8       `json:"decimals"`
	Asset    evm.Address `json:"asset"`

	EpochDuration         uint64   `json:"epochDuration"` // seconds
	SettleGrace           uint64   `json:"settleGrace"`   // seconds
	MinDeposit            evm.Big  `json:"minDeposit"`    // USDC
	DepositCap            evm.Big  `json:"depositCap"`    // USDC
	MinIdleBps            uint64   `json:"minIdleBps"`
	MaxPriceMoveBps       uint64   `json:"maxPriceMoveBps"`
	CoreSettleDelayBlocks uint64   `json:"coreSettleDelayBlocks"`
	MaxDeploy             evm.Big  `json:"maxDeploy"` // USDC
	QuotaMode             bool     `json:"quotaMode"`
	DepositAllowlist      bool     `json:"depositAllowlistEnabled"`
	NavDexes              []uint32 `json:"navDexes"`

	ManagementFeeBps  uint64 `json:"managementFeeBps"`
	PerformanceFeeBps uint64 `json:"performanceFeeBps"`
	ExitFeeBps        uint64 `json:"exitFeeBps"`

	Owner        evm.Address `json:"owner"`
	PendingOwner evm.Address `json:"pendingOwner"`
	Keeper       evm.Address `json:"keeper"`
	Guardian     evm.Address `json:"guardian"`
	Manager      evm.Address `json:"manager"`
	FeeRecipient evm.Address `json:"feeRecipient"`
	APIWallet    evm.Address `json:"apiWallet"`

	LastSharePrice  evm.Big         `json:"lastSharePrice"`
	LastFeeAccrual  uint64          `json:"lastFeeAccrual"`
	ConfirmedToCore evm.Big         `json:"confirmedToCore"`
	ConfirmedToEVM  evm.Big         `json:"confirmedToEvm"`
	PendingTransfer PendingTransfer `json:"pendingTransfer"`
	Paused          bool            `json:"paused"`
	// PausedUntil is nil when the view does not exist (older deployment).
	PausedUntil *uint64 `json:"pausedUntil,omitempty"`

	// Unknown lists views that reverted or failed ("pausedUntil()").
	Unknown []string `json:"unknown,omitempty"`
}
