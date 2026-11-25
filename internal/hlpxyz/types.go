package hlpxyz

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Errors with stable meaning.
var (
	ErrNoCache   = errors.New("hlp.xyz does not track this vault (no-cache)")
	ErrLoading   = errors.New("hlp.xyz is still building this vault's cache")
	ErrNotAVault = errors.New("not a vault")
)

// Wire types mirror the hlp.xyz responses exactly. Fixtures:
// testdata/fixtures/hlpxyz/*.json. Percent fields are PERCENT on the
// wire (returnYtd 291.21) and converted to fractions when mapped to domain.

// SummaryVault is one row of /api/vaults?action=summary.
type SummaryVault struct {
	Address       string             `json:"address"`
	Name          string             `json:"name"`
	Desc          string             `json:"desc"`
	Equity        float64            `json:"equity"`
	ReturnYtd     float64            `json:"returnYtd"` // percent
	ReturnAll     float64            `json:"returnAll"` // percent
	MaxDD         float64            `json:"maxDD"`     // percent, negative
	Sharpe        float64            `json:"sharpe"`
	PerfSeries    []PerfPoint        `json:"perfSeries"`
	IsClosed      bool               `json:"isClosed"`
	ClosedAt      *int64             `json:"closedAt"`
	AllowDeposits bool               `json:"allowDeposits"`
	AgeDays       int                `json:"ageDays"`
	MaxGross      float64            `json:"maxGross"`
	MaxGrossLev   float64            `json:"maxGrossLev"`
	AvgGrossLev   float64            `json:"avgGrossLev"`
	AvgNetLev     float64            `json:"avgNetLev"`
	NetFlows      float64            `json:"netFlows"`
	TradesCount   int                `json:"tradesCount"`
	MaxGrossLevBy map[string]float64 `json:"maxGrossLevBy"`
	CurLongNtl    float64            `json:"curLongNtl"`
	CurShortNtl   float64            `json:"curShortNtl"`
	CurCash       float64            `json:"curCash"`
	DataStart     int64              `json:"dataStart"`
	DataEnd       int64              `json:"dataEnd"`
	LastUpdated   int64              `json:"lastUpdated"`
	BaseSPDay     float64            `json:"baseSPDay"`
	BaseSPWeek    float64            `json:"baseSPWeek"`
	BaseSPMonth   float64            `json:"baseSPMonth"`
	BaseSP3Month  float64            `json:"baseSP3Month"`
	BaseSP6Month  float64            `json:"baseSP6Month"`
	BaseSPYtd     float64            `json:"baseSPYtd"`
	BaseSPYear    float64            `json:"baseSPYear"`
	BaseSPAll     float64            `json:"baseSPAll"`
}

// PerfPoint is a perfSeries element (date is ISO-8601).
type PerfPoint struct {
	Date       string   `json:"date"`
	SharePrice float64  `json:"sharePrice"`
	Equity     *float64 `json:"equity"`
}

// SummaryResponse is {count, vaults}.
type SummaryResponse struct {
	Count  int            `json:"count"`
	Vaults []SummaryVault `json:"vaults"`
}

// V6Status is one value of /api/vaults?action=v6status .statuses.
// Window keys: day, week, month, m3, m6, ytd, year, all. ret is PERCENT.
type V6Status struct {
	NavNow         float64             `json:"navNow"`
	AnchorCount    int                 `json:"anchorCount"`
	SampleSize     int                 `json:"sampleSize"`
	AccuracyPct    float64             `json:"accuracyPct"`
	WindowAccuracy map[string]*float64 `json:"windowAccuracy"`
	Source         string              `json:"source"` // follower-anchors | aggregate-fallback | v6-chain
	ComputedAt     int64               `json:"computedAt"`
	BaseSP         map[string]float64  `json:"baseSP"`
	MaxDD          map[string]*float64 `json:"maxDD"`
	Sharpe         map[string]*float64 `json:"sharpe"`
	Ret            map[string]float64  `json:"ret"`
}

// LiveVault is one element of the bulk /api/vaults?action=live.
type LiveVault struct {
	Address            string   `json:"address"`
	SharePrice         float64  `json:"sharePrice"` // "live" model, != v6 navNow
	TotalEquity        float64  `json:"totalEquity"`
	DepositCapacity    *float64 `json:"depositCapacity"`
	totalEquityPresent bool
}

// VaultLive is /api/vault/<addr>/live (works for ANY HL vault).
type VaultLive struct {
	Name               string     `json:"name"`
	Leader             string     `json:"leader"`
	ProfitShare        float64    `json:"profitShare"`
	SharePrice         float64    `json:"sharePrice"`
	TotalEquity        float64    `json:"totalEquity"`
	LockupDays         float64    `json:"lockupDays"`
	CachedAt           int64      `json:"cachedAt"`
	AllowDeposits      *bool      `json:"allowDeposits,omitempty"` // absent on some /live responses; do not infer paused
	Cached             bool       `json:"cached"`
	DepositCapacity    *float64   `json:"depositCapacity,omitempty"`
	RecentAV           []Snapshot `json:"recentAv,omitempty"`
	Error              string     `json:"error,omitempty"` // "Vault not found"
	totalEquityPresent bool
}

// DetailOpts selects /api/vault/<addr> variants.
type DetailOpts struct {
	Light  bool // light=1: no fills/funding (fast)
	Lev    bool // lev=1: include leverage series (with Light)
	Trader bool // kind=trader
}

// VaultDetail is /api/vault/<addr>?enc=t1 (or light).
type VaultDetail struct {
	Trades    *TradesT1    `json:"trades,omitempty"`
	Funding   []FundingRow `json:"funding,omitempty"`
	Deps      []DepRow     `json:"deps"`
	Portfolio struct {
		Snapshots []Snapshot `json:"snapshots"`
		Flows     []struct {
			Time   int64   `json:"time"`
			Amount float64 `json:"amount"`
		} `json:"flows"`
	} `json:"portfolio"`
	VaultInfo struct {
		Name        string  `json:"name"`
		Desc        string  `json:"desc"`
		Leader      string  `json:"leader"`
		ProfitShare float64 `json:"profitShare"`
	} `json:"vaultInfo"`
	Leverage []LeverageRow `json:"leverage,omitempty"`
	Light    bool          `json:"_light,omitempty"`
	Meta     DetailMeta    `json:"meta"`
	Status   string        `json:"status,omitempty"` // loading | no-cache
}

// TradesT1 is the columnar "t1" trades encoding. Decode with DecodeT1.
type TradesT1 struct {
	W     string    `json:"_w"` // "t1"
	N     int       `json:"n"`
	Coins []string  `json:"coins"`
	Dirs  []string  `json:"dirs"`
	T0    int64     `json:"t0"`
	TS    []int64   `json:"ts"`  // delta-encoded ms
	C     []int     `json:"c"`   // coin index
	D     []int     `json:"d"`   // dir index
	TID   []*int64  `json:"tid"` // running-sum deltas, null allowed
	OID   []*int64  `json:"oid"`
	Px    []float64 `json:"px"`
	Sz    []float64 `json:"sz"`
	SP    []float64 `json:"sp"` // start position
	Fee   []float64 `json:"fee"`
	PnL   []float64 `json:"pnl"` // closed PnL
}

// FundingRow is a funding payment.
type FundingRow struct {
	Time    string  `json:"time"`
	TS      int64   `json:"_ts"`
	Coin    string  `json:"coin"`
	Sz      float64 `json:"sz"`
	Side    string  `json:"side"`
	Payment float64 `json:"payment"`
	Rate    float64 `json:"rate"`
}

// DepRow is a vault ledger row (all depositors).
type DepRow struct {
	Time               string  `json:"time"`
	TS                 int64   `json:"_ts"`
	AccountValueChange float64 `json:"accountValueChange"`
	Type               string  `json:"type,omitempty"`
	Vault              string  `json:"vault,omitempty"`
	Hash               string  `json:"hash,omitempty"`
	User               string  `json:"user,omitempty"`
	Commission         float64 `json:"commission,omitempty"`
	Basis              float64 `json:"basis,omitempty"`
}

// Snapshot is an equity snapshot.
type Snapshot struct {
	TS  int64   `json:"ts"`
	AV  float64 `json:"av"`
	PnL float64 `json:"pnl"`
	Raw bool    `json:"raw"`
}

// LeverageRow is one leverage sample.
type LeverageRow struct {
	TS              int64   `json:"ts"`
	Leverage        float64 `json:"leverage"`
	NetLeverage     float64 `json:"netLeverage"`
	AccountValue    float64 `json:"accountValue"`
	TotalNtlPos     float64 `json:"totalNtlPos"`
	NetNtlPos       float64 `json:"netNtlPos"`
	TotalMarginUsed float64 `json:"totalMarginUsed"`
}

// DetailMeta is the detail response meta block.
type DetailMeta struct {
	LastUpdated   int64 `json:"lastUpdated"`
	DataStart     int64 `json:"dataStart"`
	DataEnd       int64 `json:"dataEnd"`
	Cached        bool  `json:"cached"`
	Stale         bool  `json:"stale"`
	TradesCount   int   `json:"tradesCount"`
	FundingCount  int   `json:"fundingCount"`
	LedgerCount   int   `json:"ledgerCount"`
	SnapshotCount int   `json:"snapshotCount"`
	IsClosed      bool  `json:"isClosed"`
	AllowDeposits bool  `json:"allowDeposits"`
}

// Composition is /api/vault/<addr>/composition?range=.
type Composition struct {
	UpdatedAt int64    `json:"updatedAt"`
	Range     string   `json:"range"`
	BucketMs  int64    `json:"bucketMs"`
	Coins     []string `json:"coins"`
	Cached    bool     `json:"cached"`
	Points    []struct {
		TS int64              `json:"ts"`
		AV float64            `json:"av"`
		V  map[string]float64 `json:"v"`
	} `json:"points"`
}

// NAVCurve is /api/user?action=v6vault. windowReturns are FRACTIONS.
type NAVCurve struct {
	NavNow         float64 `json:"navNow"`
	TotalReturnPct float64 `json:"totalReturnPct"`
	Curve          []struct {
		TS  int64   `json:"ts"`
		NAV float64 `json:"nav"`
	} `json:"curve"`
	AnchorCount    int                 `json:"anchorCount"`
	AccuracyPct    float64             `json:"accuracyPct"`
	Source         string              `json:"source"`
	WindowReturns  map[string]float64  `json:"windowReturns"`
	Pipeline       string              `json:"pipeline"`
	BuiltAt        int64               `json:"builtAt"`
	SampleSize     int                 `json:"sampleSize"`
	WindowAccuracy map[string]*float64 `json:"windowAccuracy"`
	ForwardPoints  int                 `json:"forwardPoints"`
	LastRosterTs   *int64              `json:"lastRosterTs"`
	navNowPresent  bool
}

// Index is {def, allocs} from idxList/idxGet. Rules kept raw for display.
type Index struct {
	Def    IndexDef     `json:"def"`
	Allocs []IndexAlloc `json:"allocs"`
}

// IndexDef is the index definition (rules v1 schema).
type IndexDef struct {
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Creator     string          `json:"creator"`
	Official    bool            `json:"official"`
	Inception   int64           `json:"inception"`
	Kind        string          `json:"kind"` // rules | manual
	Rules       json.RawMessage `json:"rules"`
	CreatedAt   int64           `json:"createdAt,omitempty"`
	UpdatedAt   int64           `json:"updatedAt,omitempty"`
	PreAllocs   []IndexAlloc    `json:"preAllocs,omitempty"`
}

// IndexAlloc is one allocation event.
type IndexAlloc struct {
	TS      int64              `json:"ts"`
	Kind    string             `json:"kind"`
	Weights map[string]float64 `json:"weights"`
	Scores  map[string]float64 `json:"scores"`
	Notes   []string           `json:"notes"`
}

// PVaultStatus is /api/pvault?action=status (the owner's new strategy-vault
// protocol prototype on HyperEVM testnet). `at` fields are SECONDS.
type PVaultStatus struct {
	Configured    bool   `json:"configured"`
	ChainID       int    `json:"chainId"`
	Vault         string `json:"vault"`
	Keeper        bool   `json:"keeper"`
	Bot           bool   `json:"bot"`
	Paper         bool   `json:"paper"`
	LastKeeperRun struct {
		At    int64           `json:"at"`
		Ran   bool            `json:"ran"`
		State PVaultState     `json:"state"`
		Raw   json.RawMessage `json:"-"`
	} `json:"lastKeeperRun"`
	LastBotRun struct {
		At     int64        `json:"at"`
		Paper  bool         `json:"paper"`
		NavUSD float64      `json:"navUsd"`
		GrossX float64      `json:"grossX"`
		Book   []PVaultBook `json:"book"`
		Agent  struct {
			Registered bool    `json:"registered"`
			ValidUntil int64   `json:"validUntil"`
			DaysLeft   float64 `json:"daysLeft"`
			Usable     bool    `json:"usable"`
		} `json:"agent"`
	} `json:"lastBotRun"`
}

// PVaultState is lastKeeperRun.state.
type PVaultState struct {
	Phase              string  `json:"phase"`
	SecondsToCutoff    int     `json:"secondsToCutoff"`
	NAV                float64 `json:"nav"`
	Idle               float64 `json:"idle"`
	PendingDeposits    float64 `json:"pendingDeposits"`
	PendingRedeemShare float64 `json:"pendingRedeemShares"`
	SharePrice         float64 `json:"sharePrice"`
	CorePerpEquity     float64 `json:"corePerpEquity"`
	CoreWithdrawable   float64 `json:"coreWithdrawable"`
	CoreLinkProven     bool    `json:"coreLinkProven"`
	DeployLimit        float64 `json:"deployLimit"`
}

// PVaultBook is one bot position (dir -1/0/1, units = pyramid level).
type PVaultBook struct {
	Coin    string  `json:"coin"`
	Dir     int     `json:"dir"`
	Units   int     `json:"units"`
	Stop    float64 `json:"stop"`
	Target  float64 `json:"target"`
	Current float64 `json:"current"`
	Mark    float64 `json:"mark"`
}

// PVaultSample is one /api/pvault?action=history row. On the wire it is
// the array [tsSec, sharePrice, navUsd, totalSupplyShares, keeperGasHYPE]
// (the fourth column is the vault's total
// supply in shares, the fifth the keeper EOA's HYPE gas balance).
type PVaultSample struct {
	TSec          int64   // unix seconds
	SharePrice    float64 // USDC per share
	NavUSD        float64 // total assets, USD
	SupplyShares  float64 // total supply, shares
	KeeperGasHYPE float64 // keeper gas balance, HYPE
}

// UnmarshalJSON decodes the wire array. Short rows leave the missing
// columns zero; extra columns are ignored.
func (s *PVaultSample) UnmarshalJSON(b []byte) error {
	var a []float64
	if err := json.Unmarshal(b, &a); err != nil {
		return fmt.Errorf("hlpxyz: pvault sample: %w", err)
	}
	*s = PVaultSample{}
	cols := []*float64{nil, &s.SharePrice, &s.NavUSD, &s.SupplyShares, &s.KeeperGasHYPE}
	for i, v := range a {
		switch {
		case i == 0:
			s.TSec = int64(v)
		case i < len(cols):
			*cols[i] = v
		}
	}
	return nil
}

// MarshalJSON writes the wire array back (caches and fixtures round-trip).
func (s PVaultSample) MarshalJSON() ([]byte, error) {
	return json.Marshal([5]float64{float64(s.TSec), s.SharePrice, s.NavUSD, s.SupplyShares, s.KeeperGasHYPE})
}

// QuantLive is /api/quant?id= (live target book).
type QuantLive struct {
	ID         string  `json:"id"`
	Source     string  `json:"source"`
	ComputedAt int64   `json:"computedAt"`
	Gross      float64 `json:"gross"`
	Positions  []struct {
		Coin   string  `json:"coin"`
		Label  string  `json:"label,omitempty"`
		Weight float64 `json:"weight"` // signed fraction of NAV
	} `json:"positions"`
}

// QuantStatic is /assets/quant/<id>.json (backtest). Kept partly raw.
type QuantStatic struct {
	ID      string                     `json:"id"`
	Name    string                     `json:"name"`
	Blurb   string                     `json:"blurb"`
	Status  string                     `json:"status"`
	Periods map[string]json.RawMessage `json:"periods"`
	NAV     json.RawMessage            `json:"nav"`
	Book    json.RawMessage            `json:"book"`
}

// IndexRules is the rules-v1 schema of an index definition
// returned by hlp.xyz. Nullable numbers are pointers.
type IndexRules struct {
	Version  int `json:"version"`
	Universe struct {
		Whitelist []string `json:"whitelist"`
		Blacklist []string `json:"blacklist"`
	} `json:"universe"`
	Selection struct {
		Mode     string   `json:"mode"`     // rank | threshold
		Metric   string   `json:"metric"`   // calmar | sharpe | sortino | return | tvl
		Lookback string   `json:"lookback"` // 30d | 90d | 365d | ytd
		TopN     int      `json:"topN"`
		RefVault *string  `json:"refVault"`
		Manual   []string `json:"manual"`
	} `json:"selection"`
	Filters struct {
		MinAgeDays     *int     `json:"minAgeDays"`
		MinTVL         *float64 `json:"minTvl"`
		Frequency      []string `json:"frequency"`
		MinSharpe      *float64 `json:"minSharpe"`
		MaxDrawdownPct *float64 `json:"maxDrawdownPct"` // percent
		MinReturnPct   *float64 `json:"minReturnPct"`   // percent
		MaxAvgGrossLev *float64 `json:"maxAvgGrossLev"`
	} `json:"filters"`
	Weighting struct {
		Mode         string   `json:"mode"` // equal | ...
		MaxWeightPct *float64 `json:"maxWeightPct"`
	} `json:"weighting"`
	Schedule struct {
		Reconstitute string   `json:"reconstitute"` // monthly | ...
		Rebalance    string   `json:"rebalance"`
		DriftBandPct *float64 `json:"driftBandPct"`
	} `json:"schedule"`
	Risk struct {
		MaxGrossLev *float64 `json:"maxGrossLev"`
		CashPct     float64  `json:"cashPct"`
	} `json:"risk"`
}

// ParsedRules decodes Rules. It returns (nil, nil) for manual indexes or
// empty rules.
func (d IndexDef) ParsedRules() (*IndexRules, error) {
	if len(d.Rules) == 0 || string(d.Rules) == "null" {
		return nil, nil
	}
	var r IndexRules
	if err := json.Unmarshal(d.Rules, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
