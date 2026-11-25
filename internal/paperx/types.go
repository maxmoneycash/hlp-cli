package paperx

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// Mode is the label every paper view carries.
const Mode = "paper"

// MinNotional is Hyperliquid's minimum order value in USD.
var MinNotional = decimal.NewFromInt(10)

// DefaultBalance is a new paper account's USDC.
var DefaultBalance = decimal.NewFromInt(10_000)

// TIF values (Hyperliquid wire spelling).
const (
	Gtc = "Gtc"
	Ioc = "Ioc"
	Alo = "Alo"
)

// Order statuses.
const (
	StatusPending  = "pending"  // waiting out the latency for its first book
	StatusOpen     = "open"     // resting on the book
	StatusTrigger  = "trigger"  // armed TP/SL, fires on mark
	StatusWaiting  = "waiting"  // normalTpsl child whose parent has not filled
	StatusFilled   = "filled"   // fully filled
	StatusCanceled = "canceled" // cancelled (by the user, Ioc remainder, OCO, position closed)
	StatusRejected = "rejected" // refused by an exchange rule
)

// Grouping of an order with TP/SL legs.
const (
	GroupNA          = "na"
	GroupNormalTpsl  = "normalTpsl"
	GroupPositionTps = "positionTpsl"
)

// Config tunes the simulator. Zero values take the defaults.
type Config struct {
	// Latency between a decision and the first book that may fill it
	// (default 200 ms).
	Latency time.Duration
	// TakerBps and MakerBps are the fee rates (default 4.5 and 1.5).
	TakerBps decimal.Decimal
	MakerBps decimal.Decimal
	// ImpactHalfLife is how fast consumed liquidity comes back when the
	// real book does not show the refill itself (default 15 s).
	ImpactHalfLife time.Duration
	// TriggerSlippage bounds a fired trigger-market order (default 0.10,
	// Hyperliquid's own).
	TriggerSlippage decimal.Decimal
	// BookStale is the age past which a book is not used for trigger or
	// liquidation walks (default 10 s).
	BookStale time.Duration
}

func (c Config) withDefaults() Config {
	if c.Latency <= 0 {
		c.Latency = 200 * time.Millisecond
	}
	if c.TakerBps.IsZero() {
		c.TakerBps = decimal.RequireFromString("4.5")
	}
	if c.MakerBps.IsZero() {
		c.MakerBps = decimal.RequireFromString("1.5")
	}
	if c.ImpactHalfLife <= 0 {
		c.ImpactHalfLife = 15 * time.Second
	}
	if c.TriggerSlippage.IsZero() {
		c.TriggerSlippage = decimal.RequireFromString("0.10")
	}
	if c.BookStale <= 0 {
		c.BookStale = 10 * time.Second
	}
	return c
}

// Tier is one row of a margin table: from LowerBound USD notional the
// maximum leverage is MaxLeverage.
type Tier struct {
	LowerBound  decimal.Decimal `json:"lowerBound"`
	MaxLeverage int             `json:"maxLeverage"`
}

// Spec is one market's exchange constraints.
type Spec struct {
	Coin         string `json:"coin"`
	AssetID      int    `json:"assetId"`
	SzDecimals   int    `json:"szDecimals"`
	MaxDecimals  int    `json:"maxDecimals"` // 6 perps, 8 spot
	MaxLeverage  int    `json:"maxLeverage"`
	OnlyIsolated bool   `json:"onlyIsolated,omitempty"`
	Tiers        []Tier `json:"tiers,omitempty"`
}

// SpecFrom builds a Spec from registry metadata.
func SpecFrom(m domain.Market) Spec {
	md := 6
	if m.Kind == domain.Spot {
		md = 8
	}
	return Spec{Coin: m.Coin, AssetID: m.AssetID, SzDecimals: m.SzDecimals, MaxDecimals: md, MaxLeverage: m.MaxLeverage, OnlyIsolated: m.OnlyIsolated}
}

func (s Spec) lot() decimal.Decimal { return decimal.New(1, int32(-s.SzDecimals)) }

// OrderReq is a paper order as an agent or person sends it.
type OrderReq struct {
	Coin       string
	Side       domain.Side
	Sz         decimal.Decimal
	Px         decimal.Decimal // limit price (a market order passes its slippage bound)
	TIF        string          // Gtc (default), Ioc, Alo
	Market     bool            // a market order: Ioc at Px, reported as market
	ReduceOnly bool
	// TP and SL attach trigger-market legs (zero = none). Grouping is
	// normalTpsl (sized by this order's fills) unless PositionTpsl.
	TP, SL       decimal.Decimal
	PositionTpsl bool
	// Leverage (0 = keep the account's setting for the coin) and margin
	// mode for a new position.
	Leverage int
	Isolated bool
	CLOID    string
}

// Trigger is a TP/SL leg.
type Trigger struct {
	Px   decimal.Decimal `json:"px"`
	Kind string          `json:"tpsl"` // tp | sl
}

// Order is a paper order in any state.
type Order struct {
	OID        uint64          `json:"oid"`
	CLOID      string          `json:"cloid,omitempty"`
	Coin       string          `json:"coin"`
	Side       domain.Side     `json:"side"`
	Px         decimal.Decimal `json:"px"`
	Sz         decimal.Decimal `json:"sz"` // remaining
	OrigSz     decimal.Decimal `json:"origSz"`
	Filled     decimal.Decimal `json:"filled"`
	Notional   decimal.Decimal `json:"filledNotional"`
	Fees       decimal.Decimal `json:"fees"`
	TIF        string          `json:"tif"`
	Market     bool            `json:"market,omitempty"`
	ReduceOnly bool            `json:"reduceOnly,omitempty"`
	Leverage   int             `json:"leverage,omitempty"`
	Isolated   bool            `json:"isolated,omitempty"`
	Status     string          `json:"status"`
	Error      string          `json:"error,omitempty"`
	Placed     int64           `json:"placed"`   // ms
	ActiveAt   int64           `json:"activeAt"` // ms: placed + latency
	Updated    int64           `json:"updated"`
	// Queue is the size ahead of a resting order at its price; LevelSeen
	// the real level size at the last book (for cancel attribution).
	Queue     decimal.Decimal `json:"queue"`
	LevelSeen decimal.Decimal `json:"levelSeen"`
	Rested    int64           `json:"rested,omitempty"` // ms it started resting
	// ArrivalMid is the mid of the book that activated the order.
	ArrivalMid decimal.Decimal `json:"arrivalMid"`
	Trigger    *Trigger        `json:"trigger,omitempty"`
	Parent     uint64          `json:"parent,omitempty"`
	Group      string          `json:"group,omitempty"`    // OCO group key
	Grouping   string          `json:"grouping,omitempty"` // normalTpsl | positionTpsl
	Triggered  bool            `json:"triggered,omitempty"`
}

// Live reports whether the order can still trade.
func (o *Order) Live() bool {
	switch o.Status {
	case StatusPending, StatusOpen, StatusTrigger, StatusWaiting:
		return true
	}
	return false
}

// Position is a paper perp position.
type Position struct {
	Coin     string          `json:"coin"`
	Szi      decimal.Decimal `json:"szi"` // signed
	Entry    decimal.Decimal `json:"entry"`
	Leverage int             `json:"leverage"`
	Isolated bool            `json:"isolated,omitempty"`
	Margin   decimal.Decimal `json:"margin"`  // isolated: locked USDC
	Funding  decimal.Decimal `json:"funding"` // cumulative paid (+) / received (-)
	Opened   int64           `json:"opened"`
	FundedTo int64           `json:"fundedTo"` // last funding hour charged (ms)
}

// Fill is one paper execution.
type Fill struct {
	TID         uint64          `json:"tid"`
	OID         uint64          `json:"oid"`
	Coin        string          `json:"coin"`
	Side        domain.Side     `json:"side"`
	Px          decimal.Decimal `json:"px"`
	Sz          decimal.Decimal `json:"sz"`
	Fee         decimal.Decimal `json:"fee"`
	Time        int64           `json:"time"`
	Dir         string          `json:"dir"`
	StartPos    decimal.Decimal `json:"startPosition"`
	ClosedPnL   decimal.Decimal `json:"closedPnl"`
	Maker       bool            `json:"maker"`
	Liquidation bool            `json:"liquidation,omitempty"`
	Backstop    bool            `json:"backstop,omitempty"` // closed at mark by the backstop, not the book
	// Model says how the fill was decided: book (live book walk or
	// queue), catchup (candle trade-through while no feed was attached).
	Model string `json:"model"`
	// ArrivalMid, SlippageBps (VWAP vs arrival mid, adverse positive) and
	// ImpactBps (extra cost from liquidity this account had already taken)
	// for the report; QueueWaitMs for maker fills.
	ArrivalMid  decimal.Decimal `json:"arrivalMid"`
	SlippageBps float64         `json:"slippageBps"`
	ImpactBps   float64         `json:"impactBps"`
	QueueWaitMs int64           `json:"queueWaitMs,omitempty"`
}

// FundingEntry is one settled funding payment.
type FundingEntry struct {
	Coin    string          `json:"coin"`
	Time    int64           `json:"time"`
	Rate    decimal.Decimal `json:"rate"`
	Oracle  decimal.Decimal `json:"oracle"`
	Szi     decimal.Decimal `json:"szi"`
	Payment decimal.Decimal `json:"payment"` // + paid, - received
}

// Consumed is liquidity this account took from one level.
type Consumed struct {
	Coin string          `json:"coin"`
	Ask  bool            `json:"ask"` // taken from the asks (a buy)
	Px   decimal.Decimal `json:"px"`
	Sz   decimal.Decimal `json:"sz"`   // at At
	At   int64           `json:"at"`   // ms
	Seen decimal.Decimal `json:"seen"` // real level size right after the take
}

// Stats counts what the simulator did.
type Stats struct {
	Orders       int `json:"orders"`
	Rejections   int `json:"rejections"`
	Fills        int `json:"fills"`
	Liquidations int `json:"liquidations"`
	Backstops    int `json:"backstops"`
}

// State is a paper account, persisted as JSON.
type State struct {
	ID        string                     `json:"id"`
	Network   domain.Network             `json:"network,omitempty"`
	Created   int64                      `json:"created"`
	Updated   int64                      `json:"updated"`
	Initial   decimal.Decimal            `json:"initial"`
	Balance   decimal.Decimal            `json:"balance"` // cross USDC (isolated margin is moved out)
	Positions map[string]*Position       `json:"positions"`
	Orders    []*Order                   `json:"orders"` // live orders
	Closed    []*Order                   `json:"closed"` // recent finished orders (capped)
	Fills     []Fill                     `json:"fills"`  // capped
	Funding   []FundingEntry             `json:"funding"`
	Leverage  map[string]LevSetting      `json:"leverage"`
	Overlay   []Consumed                 `json:"overlay,omitempty"`
	Marks     map[string]decimal.Decimal `json:"marks"`
	MarkAt    map[string]int64           `json:"markAt"`
	Synced    map[string]int64           `json:"synced"` // per coin: events applied up to (ms)
	Specs     map[string]Spec            `json:"specs"`
	NextOID   uint64                     `json:"nextOid"`
	NextTID   uint64                     `json:"nextTid"`
	Stats     Stats                      `json:"stats"`
}

// LevSetting is the account's leverage choice for one coin.
type LevSetting struct {
	Leverage int  `json:"leverage"`
	Isolated bool `json:"isolated,omitempty"`
}

// NewState returns a fresh account with balance USDC.
func NewState(id string, balance decimal.Decimal, now int64) *State {
	st := &State{ID: id, Created: now, Updated: now, Initial: balance, Balance: balance, NextOID: 1, NextTID: 1}
	st.init()
	return st
}

func (s *State) init() {
	if s.Positions == nil {
		s.Positions = map[string]*Position{}
	}
	if s.Leverage == nil {
		s.Leverage = map[string]LevSetting{}
	}
	if s.Marks == nil {
		s.Marks = map[string]decimal.Decimal{}
	}
	if s.MarkAt == nil {
		s.MarkAt = map[string]int64{}
	}
	if s.Synced == nil {
		s.Synced = map[string]int64{}
	}
	if s.Specs == nil {
		s.Specs = map[string]Spec{}
	}
	if s.NextOID == 0 {
		s.NextOID = 1
	}
	if s.NextTID == 0 {
		s.NextTID = 1
	}
}

// Caps on the persisted history.
const (
	MaxFills   = 2000
	MaxClosed  = 500
	MaxFunding = 2000
)
