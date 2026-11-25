package domain

import "time"

// VaultKind distinguishes the two vault substrates hlp must handle.
type VaultKind string

const (
	// VaultHyperCore is a legacy HyperCore vault (createVault). Validator perps
	// only: no spot, no HIP-3. Listed in stats-data and vaultDetails.
	VaultHyperCore VaultKind = "hypercore"
	// VaultHyperEVM is an ERC-4626/7540-style contract on HyperEVM that owns a
	// HyperCore account and trades through CoreWriter or an API wallet (the
	// hlp.xyz "pvault" protocol, Buoy, ...). Not listed in stats-data.
	VaultHyperEVM VaultKind = "hyperevm"
)

// Window is an analytics lookback window.
type Window string

const (
	W1D  Window = "1d"
	W1W  Window = "1w"
	W1M  Window = "1m"
	W3M  Window = "3m"
	W6M  Window = "6m"
	WYTD Window = "ytd"
	W1Y  Window = "1y"
	WAll Window = "all"
)

// Windows lists the windows in display order.
var Windows = []Window{W1D, W1W, W1M, W3M, W6M, WYTD, W1Y, WAll}

// SharePriceModel labels which share-price model a number came from.
// hlp.xyz exposes two that can differ by ~50%; we always
// say which one we show.
type SharePriceModel string

const (
	SPv6   SharePriceModel = "hlpxyz-v6"    // anchored NAV used by the hlp.xyz vault list
	SPLive SharePriceModel = "hlpxyz-live"  // ledger model used by hlp.xyz deposits
	SPHL   SharePriceModel = "hl-portfolio" // our own TWR from vaultDetails.portfolio
	// SPSummary marks hlp.xyz's legacy summary numbers, used when a curated
	// vault has no v6 status row.
	SPSummary SharePriceModel = "hlpxyz-summary"
)

// Returns models label how VaultSummary.Returns were computed.
const (
	// ReturnsTWR are flow-neutral (time-weighted) share-price returns.
	ReturnsTWR = "twr"
	// ReturnsPnLOverTVL approximates a window return as PnL / (TVL - PnL);
	// deposits and withdrawals inside the window are ignored (stats-data).
	ReturnsPnLOverTVL = "pnl/tvl"
)

// WindowRisk contains risk statistics published for a single v6 NAV window.
type WindowRisk struct {
	MaxDD  *float64 `json:"maxDD,omitempty"` // negative fraction
	Sharpe *float64 `json:"sharpe,omitempty"`
}

// VaultSummary is one row of the vault screener. Fields that a source does
// not provide are zero; Sources says which sources were merged.
type VaultSummary struct {
	Address string    `json:"address"`
	Name    string    `json:"name"`
	Leader  string    `json:"leader"`
	Kind    VaultKind `json:"kind"`
	Rel     string    `json:"relationship,omitempty"` // normal | parent | child

	TVL            float64               `json:"tvl"`
	TVLKnown       bool                  `json:"-"`                 // internal presence: a source-reported zero must survive merges
	APR            float64               `json:"apr"`               // fraction
	Returns        map[Window]float64    `json:"returns,omitempty"` // fraction per window
	MaxDD          float64               `json:"maxDD"`             // fraction, negative (-0.25 = -25%)
	Sharpe         float64               `json:"sharpe"`
	RiskByWindow   map[Window]WindowRisk `json:"riskByWindow,omitempty"`
	AgeDays        int                   `json:"ageDays"`
	CreatedAt      time.Time             `json:"createdAt"`
	LeaderFraction float64               `json:"leaderFraction,omitempty"`
	ProfitShare    float64               `json:"profitShare,omitempty"`
	Followers      int                   `json:"followers,omitempty"` // capped at 100 by vaultDetails
	AllowDeposits  bool                  `json:"allowDeposits"`
	Closed         bool                  `json:"closed"`
	MaxGrossLev    float64               `json:"maxGrossLev,omitempty"`
	CapacityUSD    float64               `json:"capacityUsd,omitempty"`  // deposit room before the leader drops below 5%; -1 = unlimited (HLP)
	Spark          Series                `json:"spark,omitempty"`        // share price or cumulative PnL, ~100 pts
	SparkModel     SharePriceModel       `json:"sparkModel,omitempty"`   // actual spark source, independent of return/risk status
	ReturnsModel   string                `json:"returnsModel,omitempty"` // ReturnsTWR | ReturnsPnLOverTVL
	SPModel        SharePriceModel       `json:"sharePriceModel,omitempty"`
	AccuracyPct    float64               `json:"accuracyPct,omitempty"` // hlp.xyz v6 ± level error
	Curated        bool                  `json:"curated"`               // listed by hlp.xyz
	Sources        []string              `json:"sources"`
	UpdatedAt      time.Time             `json:"updatedAt"`
	MetricsAsOf    int64                 `json:"metricsAsOf,omitempty"` // source computation time, ms; independent of live TVL
	Stale          bool                  `json:"stale"`
}

// Follower is one vault depositor (vaultDetails caps the list at 100).
type Follower struct {
	User          string  `json:"user"`
	Equity        float64 `json:"equity"`
	PnL           float64 `json:"pnl"`
	AllTimePnL    float64 `json:"allTimePnl"`
	DaysFollowing int     `json:"daysFollowing"`
	EntryTime     int64   `json:"entryTime"`
	LockupUntil   int64   `json:"lockupUntil"`
}

// Flow is a deposit (+) or withdrawal (-) into a vault.
type Flow struct {
	Time       int64   `json:"time"`
	User       string  `json:"user,omitempty"`
	USD        float64 `json:"usd"`
	Kind       string  `json:"kind,omitempty"` // vaultCreate | vaultDeposit | vaultWithdraw | ...
	Commission float64 `json:"commission,omitempty"`
}

// Exposure is signed USD notional per coin at a time.
type Exposure struct {
	T      int64              `json:"t"`
	Equity float64            `json:"equity"`
	ByCoin map[string]float64 `json:"byCoin"`
}

// VaultSourceFreshness dates one independent source. AsOf is its own stamp,
// never replaced by the read time; zero means the source did not supply it.
type VaultSourceFreshness struct {
	Source     string `json:"source"`
	AsOf       int64  `json:"asOf"`
	ObservedAt int64  `json:"observedAt"`
	Stale      bool   `json:"stale"`
	Cached     bool   `json:"cached"`
}

type VaultNAVAnalytics struct {
	NavNow         float64             `json:"navNow"`
	TotalReturn    float64             `json:"totalReturn"` // fraction, converted from upstream totalReturnPct
	BuiltAt        int64               `json:"builtAt"`
	AnchorCount    int                 `json:"anchorCount"`
	AccuracyPct    float64             `json:"accuracyPct"` // upstream NAV level-error percentage, not win rate
	SampleSize     int                 `json:"sampleSize"`
	WindowAccuracy map[string]*float64 `json:"windowAccuracy,omitempty"` // null is unavailable, zero is measured zero
	Source         string              `json:"source"`
	Pipeline       string              `json:"pipeline"`
	ForwardPoints  int                 `json:"forwardPoints"`
	LastRosterTs   *int64              `json:"lastRosterTs"`
}

type VaultValuePoint struct {
	T      int64   `json:"t"`
	Equity float64 `json:"equity"`
	PnL    float64 `json:"pnl"`
}

type VaultLiveAnalytics struct {
	SharePrice      float64           `json:"sharePrice"`
	TotalEquity     float64           `json:"totalEquity"`
	CachedAt        int64             `json:"cachedAt"`
	Cached          bool              `json:"cached"`
	DepositCapacity *float64          `json:"depositCapacity,omitempty"`
	RecentAV        []VaultValuePoint `json:"recentAv,omitempty"`
}

type VaultCompositionAnalytics struct {
	UpdatedAt int64    `json:"updatedAt"`
	Range     string   `json:"range"`
	BucketMs  int64    `json:"bucketMs"`
	Coins     []string `json:"coins"`
}

type VaultHistoryAnalytics struct {
	UpdatedAt int64          `json:"updatedAt"`
	DataStart int64          `json:"dataStart"`
	DataEnd   int64          `json:"dataEnd"`
	Counts    map[string]int `json:"counts"`
	Light     bool           `json:"light"` // full funding/fills not fetched when true
}

type VaultFunding struct {
	T       int64   `json:"t"`
	Coin    string  `json:"coin"`
	Size    float64 `json:"size"`
	Side    string  `json:"side"`
	Payment float64 `json:"payment"` // USD
	Rate    float64 `json:"rate"`    // fraction
}

type VaultLeverage struct {
	T             int64   `json:"t"`
	Gross         float64 `json:"gross"`
	Net           float64 `json:"net"`
	Equity        float64 `json:"equity"`
	GrossNotional float64 `json:"grossNotional"`
	NetNotional   float64 `json:"netNotional"`
	MarginUsed    float64 `json:"marginUsed"`
}

// VaultAnalytics preserves source-specific data without conflating the live
// ledger share price with v6 NAV, or recomputing a historical figure as live.
type VaultAnalytics struct {
	NAV         *VaultNAVAnalytics         `json:"nav,omitempty"`
	Live        *VaultLiveAnalytics        `json:"live,omitempty"`
	Composition *VaultCompositionAnalytics `json:"composition,omitempty"`
	History     *VaultHistoryAnalytics     `json:"history,omitempty"`
	Funding     []VaultFunding             `json:"funding,omitempty"`
	Leverage    []VaultLeverage            `json:"leverage,omitempty"`
}

// VaultDetail is everything the tearsheet needs.
type VaultDetail struct {
	Summary     VaultSummary                    `json:"summary"`
	Description string                          `json:"description,omitempty"`
	LockupDays  float64                         `json:"lockupDays,omitempty"`
	SharePrice  float64                         `json:"sharePrice,omitempty"`
	SPModel     SharePriceModel                 `json:"sharePriceModel,omitempty"`
	NAV         Series                          `json:"nav,omitempty"`    // share price curve, starts at 1.0
	Equity      Series                          `json:"equity,omitempty"` // account value USD
	PnL         Series                          `json:"pnl,omitempty"`    // cumulative PnL USD
	Account     *Account                        `json:"account,omitempty"`
	Followers   []Follower                      `json:"followers,omitempty"`
	Flows       []Flow                          `json:"flows,omitempty"`
	Fills       []Fill                          `json:"fills,omitempty"`
	Composition []Exposure                      `json:"composition,omitempty"`
	Children    []string                        `json:"children,omitempty"` // HLP parent -> child addresses
	CapacityUSD float64                         `json:"capacityUsd"`        // room left under the leader 5% rule; -1 = unlimited
	MaxWithdraw float64                         `json:"maxWithdrawable,omitempty"`
	Tearsheet   *Tearsheet                      `json:"tearsheet,omitempty"`
	Meta        Meta                            `json:"meta"`
	Analytics   *VaultAnalytics                 `json:"analytics,omitempty"`
	Freshness   map[string]VaultSourceFreshness `json:"freshness,omitempty"`
}

// Metrics is the standard performance block used by vault tearsheets and
// backtests alike, so "your backtest vs vault X vs HLP" is one table.
type Metrics struct {
	TotalReturn       float64 `json:"totalReturn"` // fraction
	CAGR              float64 `json:"cagr"`
	Vol               float64 `json:"vol"` // annualized stdev of period returns
	Sharpe            float64 `json:"sharpe"`
	Sortino           float64 `json:"sortino"`
	MaxDD             float64 `json:"maxDD"` // negative fraction
	Calmar            float64 `json:"calmar"`
	WinRate           float64 `json:"winRate,omitempty"`
	ProfitFactor      float64 `json:"profitFactor,omitempty"`
	WinRateKnown      bool    `json:"-"` // internal presence; zero winning trades is a real result
	ProfitFactorKnown bool    `json:"-"` // false when no losing trades make the ratio undefined
	BestMonth         float64 `json:"bestMonth,omitempty"`
	WorstMonth        float64 `json:"worstMonth,omitempty"`
	Days              float64 `json:"days"`
	Start             int64   `json:"start"`
	End               int64   `json:"end"`
}

// MonthlyReturn is one cell of the monthly heatmap.
type MonthlyReturn struct {
	Year  int     `json:"year"`
	Month int     `json:"month"` // 1-12
	Ret   float64 `json:"ret"`   // fraction
}

// CheckStatus is the outcome of a due-diligence check.
type CheckStatus string

const (
	CheckPass CheckStatus = "pass"
	CheckWarn CheckStatus = "warn"
	CheckFail CheckStatus = "fail"
	CheckNA   CheckStatus = "n/a"
)

// Check is one line of the due-diligence checklist on a tearsheet.
type Check struct {
	ID     string      `json:"id"` // stable id, e.g. "max_dd", "leader_skin", "capacity"
	Label  string      `json:"label"`
	Status CheckStatus `json:"status"`
	Detail string      `json:"detail"`
}

// Benchmark is a named comparison curve and its metrics.
type Benchmark struct {
	Name    string  `json:"name"` // HLP, BTC, HYPE, SPY...
	Curve   Series  `json:"curve"`
	Metrics Metrics `json:"metrics"`
	Corr    float64 `json:"corr"` // correlation of period returns with the subject
	Beta    float64 `json:"beta"`
}

// Tearsheet is the shared output of vault analytics and backtests.
type Tearsheet struct {
	Title      string          `json:"title"`
	Curve      Series          `json:"curve"`    // normalized to 1.0 at start
	Drawdown   Series          `json:"drawdown"` // negative fractions
	Metrics    Metrics         `json:"metrics"`
	Monthly    []MonthlyReturn `json:"monthly"`
	Benchmarks []Benchmark     `json:"benchmarks,omitempty"`
	Checklist  []Check         `json:"checklist,omitempty"`
	Meta       Meta            `json:"meta"`
}
