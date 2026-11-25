package domain

import (
	"errors"
	"time"
)

// ErrNotImplemented is returned by scaffold stubs. Every engineer removes the
// uses in their own package; `grep -rn ErrNotImplemented internal/` is the
// burn-down list.
var ErrNotImplemented = errors.New("not implemented")

// Network selects Hyperliquid mainnet or testnet.
type Network string

const (
	Mainnet Network = "mainnet"
	Testnet Network = "testnet"
)

// Interval is a Hyperliquid candle interval string.
type Interval string

const (
	I1m  Interval = "1m"
	I3m  Interval = "3m"
	I5m  Interval = "5m"
	I15m Interval = "15m"
	I30m Interval = "30m"
	I1h  Interval = "1h"
	I2h  Interval = "2h"
	I4h  Interval = "4h"
	I8h  Interval = "8h"
	I12h Interval = "12h"
	I1d  Interval = "1d"
	I3d  Interval = "3d"
	I1w  Interval = "1w"
	I1M  Interval = "1M" // 30-day buckets on Hyperliquid, not calendar months
)

// Intervals lists every interval Hyperliquid accepts, shortest first.
var Intervals = []Interval{I1m, I3m, I5m, I15m, I30m, I1h, I2h, I4h, I8h, I12h, I1d, I3d, I1w, I1M}

// Duration returns the bucket length. 1M is 30 days (Hyperliquid semantics).
// Unknown intervals return 0.
func (i Interval) Duration() time.Duration {
	switch i {
	case I1m:
		return time.Minute
	case I3m:
		return 3 * time.Minute
	case I5m:
		return 5 * time.Minute
	case I15m:
		return 15 * time.Minute
	case I30m:
		return 30 * time.Minute
	case I1h:
		return time.Hour
	case I2h:
		return 2 * time.Hour
	case I4h:
		return 4 * time.Hour
	case I8h:
		return 8 * time.Hour
	case I12h:
		return 12 * time.Hour
	case I1d:
		return 24 * time.Hour
	case I3d:
		return 72 * time.Hour
	case I1w:
		return 7 * 24 * time.Hour
	case I1M:
		return 30 * 24 * time.Hour
	}
	return 0
}

// Valid reports whether Hyperliquid accepts the interval.
func (i Interval) Valid() bool { return i.Duration() > 0 }

// Candle is one OHLCV bar. T is the OPEN time in ms. V is base-asset volume.
// (Hyperliquid wire candles carry both "t" open and "T" close.)
type Candle struct {
	T int64   `json:"t"`
	O float64 `json:"o"`
	H float64 `json:"h"`
	L float64 `json:"l"`
	C float64 `json:"c"`
	V float64 `json:"v"`
	N int     `json:"n,omitempty"` // trade count
}

// Up reports whether the candle closed at or above its open.
func (c Candle) Up() bool { return c.C >= c.O }

// Side of a trade or order: "B" buy/bid or "A" ask/sell (Hyperliquid wire letters).
type Side string

const (
	Buy  Side = "B"
	Sell Side = "A"
)

// Level is one price level of an order book.
type Level struct {
	Px float64 `json:"px"`
	Sz float64 `json:"sz"`
	N  int     `json:"n"` // order count
}

// Book is an L2 order book snapshot. Bids are descending, asks ascending.
type Book struct {
	Coin string  `json:"coin"`
	Time int64   `json:"time"`
	Bids []Level `json:"bids"`
	Asks []Level `json:"asks"`
}

// Mid returns the midpoint, or 0 if either side is empty.
func (b Book) Mid() float64 {
	if len(b.Bids) == 0 || len(b.Asks) == 0 {
		return 0
	}
	return (b.Bids[0].Px + b.Asks[0].Px) / 2
}

// Trade is a public tape print. Side is the aggressor side.
type Trade struct {
	Coin string  `json:"coin"`
	Side Side    `json:"side"`
	Px   float64 `json:"px"`
	Sz   float64 `json:"sz"`
	Time int64   `json:"time"`
	TID  uint64  `json:"tid"`
	Hash string  `json:"hash,omitempty"`
}

// MarketKind classifies a tradable instrument.
type MarketKind string

const (
	Perp    MarketKind = "perp"    // validator-operated perp, main dex
	HIP3    MarketKind = "hip3"    // builder-deployed perp, "dex:COIN"
	Spot    MarketKind = "spot"    // "@N" or "PURR/USDC"
	Outcome MarketKind = "outcome" // HIP-4 "#N"
)

// Market is one instrument with its current context.
type Market struct {
	Coin         string     `json:"coin"`    // wire coin string: BTC, xyz:SP500, @107
	Display      string     `json:"display"` // human label: BTC, SP500 (xyz), HYPE/USDC
	Kind         MarketKind `json:"kind"`
	Dex          string     `json:"dex,omitempty"` // "" for the main dex
	AssetID      int        `json:"assetId"`
	SzDecimals   int        `json:"szDecimals"`
	MaxLeverage  int        `json:"maxLeverage,omitempty"`
	OnlyIsolated bool       `json:"onlyIsolated,omitempty"`
	Delisted     bool       `json:"delisted,omitempty"`
	Category     string     `json:"category,omitempty"` // crypto, stocks, commodities...

	Mark         float64 `json:"mark"`
	Mid          float64 `json:"mid,omitempty"`
	Oracle       float64 `json:"oracle,omitempty"`
	PrevDay      float64 `json:"prevDay,omitempty"`
	Funding      float64 `json:"funding,omitempty"`      // hourly rate (fraction)
	OpenInterest float64 `json:"openInterest,omitempty"` // base units
	DayNtlVlm    float64 `json:"dayNtlVlm,omitempty"`    // USD

	// Change24h is Mark/PrevDay-1 (fraction) and FundingAPR is Funding*24*365
	// (fraction). The mappers fill both with Derive so JSON carries them.
	Change24h  float64 `json:"change24h"`
	FundingAPR float64 `json:"fundingApr"`
}

// ComputeChange24h returns Mark/PrevDay-1, or 0 when PrevDay is unknown.
func (m Market) ComputeChange24h() float64 {
	if m.PrevDay == 0 {
		return 0
	}
	return m.Mark/m.PrevDay - 1
}

// ComputeFundingAPR annualizes the hourly funding rate.
func (m Market) ComputeFundingAPR() float64 { return m.Funding * 24 * 365 }

// Derive fills the derived fields Change24h and FundingAPR from Mark,
// PrevDay and Funding. Call it after any of those change.
func (m *Market) Derive() {
	m.Change24h = m.ComputeChange24h()
	m.FundingAPR = m.ComputeFundingAPR()
}

// FundingPoint is one historical funding print.
type FundingPoint struct {
	Coin    string  `json:"coin"`
	Time    int64   `json:"time"`
	Rate    float64 `json:"rate"` // hourly
	Premium float64 `json:"premium"`
}

// Position is an open perp position for any account (user, vault, sub-account).
type Position struct {
	Coin       string  `json:"coin"`
	Dex        string  `json:"dex,omitempty"`
	Size       float64 `json:"size"` // signed, base units (negative = short)
	Entry      float64 `json:"entry"`
	Mark       float64 `json:"mark,omitempty"`
	Value      float64 `json:"value"` // USD notional, positive
	UPnL       float64 `json:"upnl"`
	ROE        float64 `json:"roe"`
	Leverage   float64 `json:"leverage"`
	Cross      bool    `json:"cross"`
	LiqPx      float64 `json:"liqPx,omitempty"` // 0 = none / unknown
	MarginUsed float64 `json:"marginUsed"`
	FundingAll float64 `json:"fundingAllTime,omitempty"`
	// Vault is the vault address this position belongs to when it was read
	// through one vault account; empty otherwise (and for a position
	// aggregated over several, see Vaults).
	Vault string `json:"vault,omitempty"`
	// Vaults lists every account an aggregated position (hlp_positioning
	// over the HLP children) was summed from, and ByVault its signed size
	// in each (AGENT-UX #28).
	Vaults  []string           `json:"vaults,omitempty"`
	ByVault map[string]float64 `json:"byVault,omitempty"`
	// Aggregated rows are net exposure; leverage/liquidation remain per-leg.
	Aggregated bool          `json:"aggregated,omitempty"`
	GrossValue float64       `json:"grossValue,omitempty"`
	LongValue  float64       `json:"longValue,omitempty"`
	ShortValue float64       `json:"shortValue,omitempty"`
	Legs       []PositionLeg `json:"legs,omitempty"`
}

// PositionLeg preserves source account risk and PnL before cross-vault netting.
type PositionLeg struct {
	Vault      string  `json:"vault"`
	Coin       string  `json:"coin"`
	Size       float64 `json:"size"`
	Entry      float64 `json:"entry"`
	Mark       float64 `json:"mark,omitempty"`
	Value      float64 `json:"value"`
	UPnL       float64 `json:"upnl"`
	ROE        float64 `json:"roe"`
	Leverage   float64 `json:"leverage"`
	Cross      bool    `json:"cross"`
	LiqPx      float64 `json:"liqPx,omitempty"`
	MarginUsed float64 `json:"marginUsed"`
	FundingAll float64 `json:"fundingAllTime,omitempty"`
}

// Balance is one spot token balance.
type Balance struct {
	Coin  string  `json:"coin"`
	Total float64 `json:"total"`
	Hold  float64 `json:"hold"`
	USD   float64 `json:"usd,omitempty"`
}

// Account is an aggregated account view across every perp dex, spot and Earn.
// Equity = Σ perp account values + spot USD + Earn supply.
type Account struct {
	Address      string     `json:"address"`
	Role         string     `json:"role,omitempty"` // user | vault | subAccount | agent | missing
	Equity       float64    `json:"equity"`
	PerpEquity   float64    `json:"perpEquity"`
	SpotUSD      float64    `json:"spotUsd"`
	EarnUSD      float64    `json:"earnUsd"`
	Withdrawable float64    `json:"withdrawable"`
	MarginUsed   float64    `json:"marginUsed"`
	GrossNtl     float64    `json:"grossNtl"`
	NetNtl       float64    `json:"netNtl"`
	LongNtl      float64    `json:"longNtl,omitempty"`
	ShortNtl     float64    `json:"shortNtl,omitempty"`
	Aggregated   bool       `json:"aggregated,omitempty"`
	Positions    []Position `json:"positions"`
	Balances     []Balance  `json:"balances,omitempty"`
	OpenOrders   []Order    `json:"openOrders,omitempty"`
	Time         int64      `json:"time"`
	// The WS connection that supplied perp positions. Scoped to this engine
	// stream only. Omitted on REST and while HLP members span connections.
	StreamGeneration int64 `json:"streamGeneration,omitempty"`
	// Partial is set when part of the account could not be read (a HIP-3
	// dex, spot valuation or the Earn balance); Equity then undercounts and
	// Warnings says which part is missing.
	Partial  bool     `json:"partial,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Order is a resting order.
type Order struct {
	Coin       string  `json:"coin"`
	Side       Side    `json:"side"`
	Px         float64 `json:"px"`
	Sz         float64 `json:"sz"`
	OrigSz     float64 `json:"origSz,omitempty"`
	OID        uint64  `json:"oid"`
	CLOID      string  `json:"cloid,omitempty"`
	Type       string  `json:"type,omitempty"` // Limit, Stop Market, Take Profit Market...
	TIF        string  `json:"tif,omitempty"`
	ReduceOnly bool    `json:"reduceOnly,omitempty"`
	TriggerPx  float64 `json:"triggerPx,omitempty"`
	Time       int64   `json:"time"`
}

// Fill is one execution for an account (user, vault or paper/backtest broker).
type Fill struct {
	Coin      string  `json:"coin"`
	Side      Side    `json:"side"`
	Px        float64 `json:"px"`
	Sz        float64 `json:"sz"`
	Time      int64   `json:"time"`
	Dir       string  `json:"dir,omitempty"` // Open Long, Close Short, ...
	StartPos  float64 `json:"startPosition"`
	ClosedPnL float64 `json:"closedPnl"`
	Fee       float64 `json:"fee"`
	OID       uint64  `json:"oid,omitempty"`
	TID       uint64  `json:"tid,omitempty"`
	Hash      string  `json:"hash,omitempty"`
	Liq       bool    `json:"liquidation,omitempty"`
}

// Point is one sample of a time series.
type Point struct {
	T int64   `json:"t"` // ms
	V float64 `json:"v"`
}

// Series is a time-ordered list of points.
type Series []Point

// Last returns the final value, or 0 for an empty series.
func (s Series) Last() float64 {
	if len(s) == 0 {
		return 0
	}
	return s[len(s)-1].V
}

// Values returns just the values.
func (s Series) Values() []float64 {
	out := make([]float64, len(s))
	for i, p := range s {
		out[i] = p.V
	}
	return out
}

// Meta travels with every result shown to humans or agents so they can judge
// freshness (the HyperData "LIVE/STALE/PARTIAL" pattern).
type Meta struct {
	Source   string    `json:"source"` // hyperliquid | hlp.xyz | stats-data | paper | backtest | cache
	AsOf     time.Time `json:"asOf"`
	Stale    bool      `json:"stale"`
	Partial  bool      `json:"partial,omitempty"`
	Network  Network   `json:"network,omitempty"`
	Warnings []string  `json:"warnings,omitempty"`
	// Total is the row count before --limit/offset, when the data is a list.
	Total *int `json:"total,omitempty"`
	// Truncated is set when rows or fields were dropped (limit, size caps).
	Truncated bool `json:"truncated,omitempty"`
	// Engine names the strategy engine that produced the result: builtin,
	// pyne or pineforge.
	Engine string `json:"engine,omitempty"`
	// Mode says which exchange the result is about: "live" (Hyperliquid,
	// the default on every result) or "paper" (a local paper account).
	Mode string `json:"mode,omitempty"`
}
