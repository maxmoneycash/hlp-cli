package hyperliquid

import "encoding/json"

// Wire types: exact shapes of /info and stats-data responses (verified
// against live responses; fixtures in testdata/fixtures/hyperliquid/).
// Decimals stay as Dec strings.
// tid/oid are uint64 (they exceed 2^53).

// WireCandle is one candleSnapshot / WS candle element.
// Declare BOTH t and T: encoding/json matches keys case-insensitively and a
// struct with only `json:"t"` gets the close time written over the open time.
type WireCandle struct {
	OpenTime  int64  `json:"t"`
	CloseTime int64  `json:"T"`
	Coin      string `json:"s"`
	Interval  string `json:"i"`
	O         Dec    `json:"o"`
	C         Dec    `json:"c"`
	H         Dec    `json:"h"`
	L         Dec    `json:"l"`
	V         Dec    `json:"v"`
	N         int    `json:"n"`
}

// AssetMeta is one meta.universe entry.
type AssetMeta struct {
	Name          string `json:"name"`
	SzDecimals    int    `json:"szDecimals"`
	MaxLeverage   int    `json:"maxLeverage"`
	MarginTableID int    `json:"marginTableId"`
	OnlyIsolated  bool   `json:"onlyIsolated,omitempty"`
	IsDelisted    bool   `json:"isDelisted,omitempty"`
	MarginMode    string `json:"marginMode,omitempty"` // strictIsolated | noCross
}

// Meta is the `meta` response (native or one HIP-3 dex).
type Meta struct {
	Universe        []AssetMeta `json:"universe"`
	CollateralToken int         `json:"collateralToken"`
	// MarginTables is [[id, {description, marginTiers}]]; decode lazily.
	MarginTables []any `json:"marginTables"`
}

// AssetCtx is one perp context from metaAndAssetCtxs / allDexsAssetCtxs.
type AssetCtx struct {
	Funding      Dec   `json:"funding"`
	OpenInterest Dec   `json:"openInterest"`
	PrevDayPx    Dec   `json:"prevDayPx"`
	DayNtlVlm    Dec   `json:"dayNtlVlm"`
	Premium      Dec   `json:"premium"`
	OraclePx     Dec   `json:"oraclePx"`
	MarkPx       Dec   `json:"markPx"`
	MidPx        Dec   `json:"midPx"` // may be null
	ImpactPxs    []Dec `json:"impactPxs"`
	DayBaseVlm   Dec   `json:"dayBaseVlm"`
}

// PerpDex is one perpDexs entry (index 0 is null = native dex).
type PerpDex struct {
	Name     string `json:"name"`
	FullName string `json:"fullName"`
	Deployer string `json:"deployer"`
}

// SpotToken is one spotMeta.tokens entry.
type SpotToken struct {
	Name        string `json:"name"`
	Index       int    `json:"index"`
	SzDecimals  int    `json:"szDecimals"`
	WeiDecimals int    `json:"weiDecimals"`
	TokenID     string `json:"tokenId"`
	IsCanonical bool   `json:"isCanonical"`
	FullName    string `json:"fullName,omitempty"`
}

// SpotPair is one spotMeta.universe entry.
type SpotPair struct {
	Name        string `json:"name"` // "PURR/USDC" or "@107"
	Tokens      [2]int `json:"tokens"`
	Index       int    `json:"index"`
	IsCanonical bool   `json:"isCanonical"`
}

// SpotMeta is the spotMeta response.
type SpotMeta struct {
	Tokens   []SpotToken `json:"tokens"`
	Universe []SpotPair  `json:"universe"`
}

// WireLevel is one book level.
type WireLevel struct {
	Px Dec `json:"px"`
	Sz Dec `json:"sz"`
	N  int `json:"n"`
}

// L2Book is the l2Book response / WS payload. Levels[0]=bids, Levels[1]=asks.
type L2Book struct {
	Coin   string         `json:"coin"`
	Time   int64          `json:"time"`
	Levels [2][]WireLevel `json:"levels"`
}

// WireTrade is a recentTrades / WS trades element.
type WireTrade struct {
	Coin  string    `json:"coin"`
	Side  string    `json:"side"` // aggressor: B | A
	Px    Dec       `json:"px"`
	Sz    Dec       `json:"sz"`
	Time  int64     `json:"time"`
	Hash  string    `json:"hash"`
	TID   uint64    `json:"tid"`
	Users [2]string `json:"users"`
}

// WireFunding is a fundingHistory element.
type WireFunding struct {
	Coin        string `json:"coin"`
	FundingRate Dec    `json:"fundingRate"`
	Premium     Dec    `json:"premium"`
	Time        int64  `json:"time"`
}

// MarginSummary is part of clearinghouseState.
type MarginSummary struct {
	AccountValue    Dec `json:"accountValue"`
	TotalNtlPos     Dec `json:"totalNtlPos"`
	TotalRawUsd     Dec `json:"totalRawUsd"`
	TotalMarginUsed Dec `json:"totalMarginUsed"`
}

// WirePosition is assetPositions[].position.
type WirePosition struct {
	Coin     string `json:"coin"`
	Szi      Dec    `json:"szi"`
	Leverage struct {
		Type   string `json:"type"` // cross | isolated
		Value  int    `json:"value"`
		RawUsd Dec    `json:"rawUsd,omitempty"`
	} `json:"leverage"`
	EntryPx        Dec `json:"entryPx"`
	PositionValue  Dec `json:"positionValue"`
	UnrealizedPnl  Dec `json:"unrealizedPnl"`
	ReturnOnEquity Dec `json:"returnOnEquity"`
	LiquidationPx  Dec `json:"liquidationPx"` // null ok
	MarginUsed     Dec `json:"marginUsed"`
	MaxLeverage    int `json:"maxLeverage"`
	CumFunding     struct {
		AllTime     Dec `json:"allTime"`
		SinceOpen   Dec `json:"sinceOpen"`
		SinceChange Dec `json:"sinceChange"`
	} `json:"cumFunding"`
}

// ClearinghouseState is the per-dex perp account state.
type ClearinghouseState struct {
	MarginSummary              MarginSummary `json:"marginSummary"`
	CrossMarginSummary         MarginSummary `json:"crossMarginSummary"`
	CrossMaintenanceMarginUsed Dec           `json:"crossMaintenanceMarginUsed"`
	Withdrawable               Dec           `json:"withdrawable"`
	AssetPositions             []struct {
		Type     string       `json:"type"`
		Position WirePosition `json:"position"`
	} `json:"assetPositions"`
	Time int64 `json:"time"`
}

// SpotBalance is one spotClearinghouseState balance.
type SpotBalance struct {
	Coin     string `json:"coin"`
	Token    int    `json:"token"`
	Total    Dec    `json:"total"`
	Hold     Dec    `json:"hold"`
	EntryNtl Dec    `json:"entryNtl"`
}

// WireFill is a userFills element.
type WireFill struct {
	Coin          string `json:"coin"`
	Px            Dec    `json:"px"`
	Sz            Dec    `json:"sz"`
	Side          string `json:"side"`
	Time          int64  `json:"time"`
	StartPosition Dec    `json:"startPosition"`
	Dir           string `json:"dir"`
	ClosedPnl     Dec    `json:"closedPnl"`
	Hash          string `json:"hash"`
	OID           uint64 `json:"oid"`
	Crossed       bool   `json:"crossed"`
	Fee           Dec    `json:"fee"`
	TID           uint64 `json:"tid"`
	FeeToken      string `json:"feeToken"`
	Cloid         string `json:"cloid,omitempty"`
	BuilderFee    Dec    `json:"builderFee,omitempty"`
	// Liquidation is present (an object) only on liquidation fills.
	Liquidation json.RawMessage `json:"liquidation,omitempty"`
}

// FrontendOrder is a frontendOpenOrders element.
type FrontendOrder struct {
	Coin             string `json:"coin"`
	Side             string `json:"side"`
	LimitPx          Dec    `json:"limitPx"`
	Sz               Dec    `json:"sz"`
	OID              uint64 `json:"oid"`
	Timestamp        int64  `json:"timestamp"`
	OrigSz           Dec    `json:"origSz"`
	Cloid            string `json:"cloid,omitempty"`
	TriggerCondition string `json:"triggerCondition"`
	IsTrigger        bool   `json:"isTrigger"`
	TriggerPx        Dec    `json:"triggerPx"`
	IsPositionTpsl   bool   `json:"isPositionTpsl"`
	ReduceOnly       bool   `json:"reduceOnly"`
	OrderType        string `json:"orderType"`
	Tif              string `json:"tif"`
}

// PortfolioWindow is one window of `portfolio` / vaultDetails.portfolio.
// History entries are [ms, "value"] pairs.
type PortfolioWindow struct {
	AccountValueHistory [][2]any `json:"accountValueHistory"`
	PnlHistory          [][2]any `json:"pnlHistory"`
	Vlm                 Dec      `json:"vlm"`
}

// Portfolio is [[window, PortfolioWindow]] keyed by day|week|month|allTime|perp*.
type Portfolio map[string]PortfolioWindow

// WireFollower is one vaultDetails follower (list capped at 100).
type WireFollower struct {
	User           string `json:"user"`
	VaultEquity    Dec    `json:"vaultEquity"`
	Pnl            Dec    `json:"pnl"`
	AllTimePnl     Dec    `json:"allTimePnl"`
	DaysFollowing  int    `json:"daysFollowing"`
	VaultEntryTime int64  `json:"vaultEntryTime"`
	LockupUntil    int64  `json:"lockupUntil"`
}

// VaultDetails is the vaultDetails response (null for non-vaults -> ErrNotFound).
type VaultDetails struct {
	Name                  string            `json:"name"`
	VaultAddress          string            `json:"vaultAddress"`
	Leader                string            `json:"leader"`
	Description           string            `json:"description"`
	Portfolio             Portfolio         `json:"-"` // decoded from the [[k,v]] array by the client
	RawPortfolio          [][2]any          `json:"portfolio"`
	APR                   float64           `json:"apr"`
	FollowerState         *WireFollower     `json:"followerState"`
	LeaderFraction        float64           `json:"leaderFraction"`
	LeaderCommission      float64           `json:"leaderCommission"`
	Followers             []WireFollower    `json:"followers"`
	MaxDistributable      float64           `json:"maxDistributable"`
	MaxWithdrawable       float64           `json:"maxWithdrawable"`
	IsClosed              bool              `json:"isClosed"`
	Relationship          VaultRelationship `json:"relationship"`
	AllowDeposits         bool              `json:"allowDeposits"`
	AlwaysCloseOnWithdraw bool              `json:"alwaysCloseOnWithdraw"`
}

// VaultRelationship: {type: normal|parent|child, data?: {childAddresses}}.
type VaultRelationship struct {
	Type string `json:"type"`
	Data *struct {
		ChildAddresses []string `json:"childAddresses"`
	} `json:"data,omitempty"`
}

// UserVaultEquity is a userVaultEquities element.
type UserVaultEquity struct {
	VaultAddress         string `json:"vaultAddress"`
	Equity               Dec    `json:"equity"`
	LockedUntilTimestamp int64  `json:"lockedUntilTimestamp"`
}

// StatsVault is one element of GET stats-data/<Net>/vaults (14 MB array).
// pnls is [[window, [strings...]]].
type StatsVault struct {
	APR     float64  `json:"apr"`
	PNLs    [][2]any `json:"pnls"`
	Summary struct {
		Name             string            `json:"name"`
		VaultAddress     string            `json:"vaultAddress"`
		Leader           string            `json:"leader"`
		TVL              Dec               `json:"tvl"`
		IsClosed         bool              `json:"isClosed"`
		Relationship     VaultRelationship `json:"relationship"`
		CreateTimeMillis int64             `json:"createTimeMillis"`
	} `json:"summary"`
}
