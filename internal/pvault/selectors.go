package pvault

import "github.com/maxmoneycash/hlp/internal/evm"

// Function selectors of the strategy-vault contracts. Each one is checked
// against keccak256(signature)[:4] in selectors_test.go.
var (
	// VaultLens
	selVaultState = evm.MustSelector("0x54e98549") // vaultState(address)
	selUserState  = evm.MustSelector("0x7281fb33") // userState(address,address)
	selEpochRange = evm.MustSelector("0x9b9ce627") // epochRange(address,uint64,uint64)

	// Vault views
	selName                     = evm.MustSelector("0x06fdde03") // name()
	selSymbol                   = evm.MustSelector("0x95d89b41") // symbol()
	selDecimals                 = evm.MustSelector("0x313ce567") // decimals()
	selAsset                    = evm.MustSelector("0x38d52e0f") // asset()
	selTotalAssets              = evm.MustSelector("0x01e1d114") // totalAssets()
	selTotalSupply              = evm.MustSelector("0x18160ddd") // totalSupply()
	selSharePrice               = evm.MustSelector("0x87269729") // sharePrice()
	selHighWaterMark            = evm.MustSelector("0x1e8410da") // highWaterMark()
	selCurrentEpoch             = evm.MustSelector("0x76671808") // currentEpoch()
	selPhase                    = evm.MustSelector("0xb1c9fe6e") // phase()
	selCutoffAt                 = evm.MustSelector("0x56b6f88f") // cutoffAt()
	selEpochDuration            = evm.MustSelector("0x4ff0876a") // epochDuration()
	selSettleGrace              = evm.MustSelector("0x12ae6491") // settleGrace()
	selMinDeposit               = evm.MustSelector("0x41b3d185") // minDeposit()
	selDepositCap               = evm.MustSelector("0xdbd5edc7") // depositCap()
	selManagementFeeBps         = evm.MustSelector("0x3813c35a") // managementFeeBps()
	selPerformanceFeeBps        = evm.MustSelector("0xb9d4e879") // performanceFeeBps()
	selExitFeeBps               = evm.MustSelector("0x57b17a52") // exitFeeBps()
	selMinIdleBps               = evm.MustSelector("0x39a56673") // minIdleBps()
	selMaxPriceMoveBps          = evm.MustSelector("0x8326b184") // maxPriceMoveBps()
	selMaxDeploy                = evm.MustSelector("0xcf823c1a") // maxDeploy()
	selQuotaMode                = evm.MustSelector("0x9f4cd252") // quotaMode()
	selIdleAssets               = evm.MustSelector("0xe16b03a3") // idleAssets()
	selRedeemReserve            = evm.MustSelector("0x5a111a5e") // redeemReserve()
	selTotalPendingDeposits     = evm.MustSelector("0x0a881082") // totalPendingDeposits()
	selTotalPendingRedeemShares = evm.MustSelector("0x8eff0106") // totalPendingRedeemShares()
	selEpochData                = evm.MustSelector("0x8acd321d") // epochData(uint64)
	selKeeper                   = evm.MustSelector("0xaced1661") // keeper()
	selGuardian                 = evm.MustSelector("0x452a9320") // guardian()
	selManager                  = evm.MustSelector("0x481c6a75") // manager()
	selOwner                    = evm.MustSelector("0x8da5cb5b") // owner()
	selPendingOwner             = evm.MustSelector("0xe30c3978") // pendingOwner()
	selFeeRecipient             = evm.MustSelector("0x46904840") // feeRecipient()
	selAPIWallet                = evm.MustSelector("0x57848ae9") // apiWallet()
	selPaused                   = evm.MustSelector("0x5c975abb") // paused()
	selDepositAllowlistEnabled  = evm.MustSelector("0x90d6f21d") // depositAllowlistEnabled()
	selCoreLinkProven           = evm.MustSelector("0x12a7979c") // coreLinkProven()
	selNavDexCount              = evm.MustSelector("0x80cf9ab5") // navDexCount()
	selNavDexes                 = evm.MustSelector("0xb5524ea4") // navDexes(uint256)
	selPendingTransfer          = evm.MustSelector("0xa6c5a6bc") // pendingTransfer()
	selLastSharePrice           = evm.MustSelector("0x5a05958b") // lastSharePrice()
	selLastFeeAccrual           = evm.MustSelector("0xbc0bdf50") // lastFeeAccrual()
	selCoreSettleDelayBlocks    = evm.MustSelector("0xec2d8586") // coreSettleDelayBlocks()
	selConfirmedToCore          = evm.MustSelector("0x1fd58380") // confirmedToCore()
	selConfirmedToEvm           = evm.MustSelector("0xa3411e27") // confirmedToEvm()
	selBalanceOf                = evm.MustSelector("0x70a08231") // balanceOf(address)
	selPendingDepositRequest    = evm.MustSelector("0xc3702989") // pendingDepositRequest(address)
	selPendingRedeemRequest     = evm.MustSelector("0x53dc1dd3") // pendingRedeemRequest(address)
	selClaimableDepositRequest  = evm.MustSelector("0xaff947c4") // claimableDepositRequest(address)
	selClaimableRedeemRequest   = evm.MustSelector("0x0088d3e5") // claimableRedeemRequest(address)
	selDepositorAllowed         = evm.MustSelector("0x329c29fb") // depositorAllowed(address)

	// Newer ABI only: absent on the testnet deployment, a revert means "n/a".
	selPausedUntil = evm.MustSelector("0xda748b10") // pausedUntil()
)

// selectorSignatures backs the keccak test and debugging output.
var selectorSignatures = map[evm.Selector]string{
	selVaultState: "vaultState(address)", selUserState: "userState(address,address)", selEpochRange: "epochRange(address,uint64,uint64)",
	selName: "name()", selSymbol: "symbol()", selDecimals: "decimals()", selAsset: "asset()",
	selTotalAssets: "totalAssets()", selTotalSupply: "totalSupply()", selSharePrice: "sharePrice()", selHighWaterMark: "highWaterMark()",
	selCurrentEpoch: "currentEpoch()", selPhase: "phase()", selCutoffAt: "cutoffAt()", selEpochDuration: "epochDuration()",
	selSettleGrace: "settleGrace()", selMinDeposit: "minDeposit()", selDepositCap: "depositCap()",
	selManagementFeeBps: "managementFeeBps()", selPerformanceFeeBps: "performanceFeeBps()", selExitFeeBps: "exitFeeBps()",
	selMinIdleBps: "minIdleBps()", selMaxPriceMoveBps: "maxPriceMoveBps()", selMaxDeploy: "maxDeploy()", selQuotaMode: "quotaMode()",
	selIdleAssets: "idleAssets()", selRedeemReserve: "redeemReserve()", selTotalPendingDeposits: "totalPendingDeposits()",
	selTotalPendingRedeemShares: "totalPendingRedeemShares()", selEpochData: "epochData(uint64)", selKeeper: "keeper()",
	selGuardian: "guardian()", selManager: "manager()", selOwner: "owner()", selPendingOwner: "pendingOwner()",
	selFeeRecipient: "feeRecipient()", selAPIWallet: "apiWallet()", selPaused: "paused()",
	selDepositAllowlistEnabled: "depositAllowlistEnabled()", selCoreLinkProven: "coreLinkProven()", selNavDexCount: "navDexCount()",
	selNavDexes: "navDexes(uint256)", selPendingTransfer: "pendingTransfer()", selLastSharePrice: "lastSharePrice()",
	selLastFeeAccrual: "lastFeeAccrual()", selCoreSettleDelayBlocks: "coreSettleDelayBlocks()",
	selConfirmedToCore: "confirmedToCore()", selConfirmedToEvm: "confirmedToEvm()", selBalanceOf: "balanceOf(address)",
	selPendingDepositRequest: "pendingDepositRequest(address)", selPendingRedeemRequest: "pendingRedeemRequest(address)",
	selClaimableDepositRequest: "claimableDepositRequest(address)", selClaimableRedeemRequest: "claimableRedeemRequest(address)",
	selDepositorAllowed: "depositorAllowed(address)", selPausedUntil: "pausedUntil()",
}

// errorSigs are every custom error of the vault and lens ABIs
// (fixtures/pvault/selectors.json).
var errorSigs = map[string]string{
	"0x46897dfd": "ApiWalletReused()",
	"0x96ec8e54": "BelowMinDeposit()",
	"0x306e6d85": "CannotRescue()",
	"0xb59d76f6": "CoreAccountMissing()",
	"0xec7cf42c": "CoreLinkNotProven()",
	"0x3fc6aef6": "CoreStateNotSettled()",
	"0x857ccb8e": "CoreWriterLib__CoreAmountTooLarge(uint256)",
	"0x45641ba2": "CoreWriterLib__EvmAmountTooSmall(uint256)",
	"0x5d7292dc": "DeployTooLarge()",
	"0x935d630c": "DepositCapExceeded()",
	"0xce99c54d": "DexNotAllowed()",
	"0xfb8f41b2": "ERC20InsufficientAllowance(address,uint256,uint256)",
	"0xe450d38c": "ERC20InsufficientBalance(address,uint256,uint256)",
	"0xe602df05": "ERC20InvalidApprover(address)",
	"0xec442f05": "ERC20InvalidReceiver(address)",
	"0x96c6fd1e": "ERC20InvalidSender(address)",
	"0x94280d62": "ERC20InvalidSpender(address)",
	"0xd93c0665": "EnforcedPause()",
	"0x4c39b1d5": "EpochNotOpen()",
	"0xa993769a": "EpochStillOpen()",
	"0x8dfc202b": "ExpectedPause()",
	"0x0023afb4": "HLConversions__InvalidToken(uint64)",
	"0xff669004": "InsufficientIdle()",
	"0xb89c56c5": "InsufficientRequest()",
	"0x35be3ac8": "InvalidConfig()",
	"0xd5e80fb4": "NoPendingTransfer()",
	"0x06fb10a9": "NotAllowlisted()",
	"0xef6d0f02": "NotGuardian()",
	"0xf512b278": "NotKeeper()",
	"0x969bf728": "NothingToClaim()",
	"0x1e4fbdf7": "OwnableInvalidOwner(address)",
	"0x118cdaa7": "OwnableUnauthorizedAccount(address)",
	"0x7ae90bc6": "PauseCooldown()",
	"0x7cf4fff2": "PrecompileLib__AccountMarginSummaryPrecompileFailed()",
	"0xafa95720": "PrecompileLib__CoreUserExistsPrecompileFailed()",
	"0x062879da": "PrecompileLib__SpotBalancePrecompileFailed()",
	"0xee6b81c5": "PrecompileLib__TokenInfoPrecompileFailed()",
	"0x70a5e2e6": "PrecompileLib__WithdrawablePrecompileFailed()",
	"0x786c0592": "PriceMoveTooLarge(uint256,uint256)",
	"0x3ee5aeb5": "ReentrancyGuardReentrantCall()",
	"0x66eeda28": "RequestsLocked()",
	"0x6dfcc650": "SafeCastOverflowedUintDowncast(uint8,uint256)",
	"0x5274afe7": "SafeERC20FailedOperation(address)",
	"0xd10e849b": "TransferNotArrived()",
	"0x5ee186e1": "TransferNotCancellable()",
	"0x3a9c1084": "TransferPending()",
	"0xd92e233d": "ZeroAddress()",
	"0x1f2a2005": "ZeroAmount()",
	"0xd205582d": "ZeroSharesIssued()",
}

// Errors maps custom-error selectors to signatures, for evm.Options.Errors.
var Errors = func() map[evm.Selector]string {
	m := make(map[evm.Selector]string, len(errorSigs))
	for sel, sig := range errorSigs {
		m[evm.MustSelector(sel)] = sig
	}
	return m
}()

// ErrorName returns the signature of a custom error selector, or "".
func ErrorName(sel evm.Selector) string { return Errors[sel] }
