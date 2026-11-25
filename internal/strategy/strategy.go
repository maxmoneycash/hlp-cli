package strategy

import (
	"context"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// Spec declares what a strategy needs.
type Spec struct {
	Name      string
	Kind      string          // pine | thesis | llm | rules
	Universe  []string        // wire coins, e.g. ["BTC"] or ["xyz:SP500","DOGE",...]
	Interval  domain.Interval // bar interval driving OnBar
	Warmup    int             // bars needed before the first OnBar
	Rebalance time.Duration   // for target-weight strategies (0 = every bar)
}

// OrderKind for strategy orders.
type OrderKind string

const (
	Market OrderKind = "market"
	Limit  OrderKind = "limit"
	Stop   OrderKind = "stop"
	// Cancel removes pending (unfilled) orders whose Tag equals Order.Tag,
	// or every pending order for Order.Coin when Tag is empty (every coin
	// when Coin is empty too). Size, Side and Px are ignored.
	Cancel OrderKind = "cancel"
)

// Order is a strategy's order intent. Exactly one of Size or NotionalUSD.
//
// Engine conventions shared by backtest, paper and (Phase 2) live:
//   - Market orders fill at the next bar open (backtest) or against the
//     live book (paper), +/- slippage.
//   - Limit and Stop orders rest until the price trades through Px.
//   - A new resting order with the same coin, Kind and non-empty Tag
//     replaces the old one (Pine strategy.exit / entry ids), and resting
//     orders that share a Tag are one-cancels-other: when one fills, its
//     siblings are cancelled.
//   - ReduceOnly orders never flip or grow a position; a ReduceOnly order
//     with neither Size nor NotionalUSD closes the whole position. Resting
//     ReduceOnly orders are cancelled once the position is flat.
type Order struct {
	Coin        string
	Side        domain.Side
	Size        float64 // base units
	NotionalUSD float64
	Kind        OrderKind
	Px          float64 // limit/stop price
	ReduceOnly  bool
	Tag         string // Pine strategy.entry id, shown on chart markers
}

// Context is what a strategy sees on each bar. Implemented by the backtest
// engine, the paper engine and (Phase 2) the live bot runner, so the same
// strategy runs unchanged in all three.
type Context interface {
	Now() int64                       // current bar open time (ms)
	Bars(coin string) []domain.Candle // closed bars up to now, oldest first
	Price(coin string) float64        // current close / mark
	Position(coin string) float64     // signed base size
	Equity() float64                  // USD, mark-to-market
	Cash() float64
	Order(o Order) error
	// SetTargets rebalances to signed NAV weights (thesis baskets):
	// {"xyz:SP500": 1.5, "DOGE": -0.075, ...}. Weights sum of |w| = gross leverage.
	SetTargets(weights map[string]float64) error
	CloseAll() error
	Plot(name string, v float64)     // Pine plot() -> chart overlay
	Mark(buy bool, label string)     // Pine plotshape / entry markers
	Logf(format string, args ...any) // decision log ("why I bought"), shown in arena
}

// PositionInfo is optionally implemented by a Context (the backtest and
// paper engines do) to expose ledger details Pine needs
// (strategy.position_avg_price, strategy.netprofit).
type PositionInfo interface {
	EntryPrice(coin string) float64 // average entry of the open position, 0 when flat
	RealizedPnL() float64           // closed PnL net of fees and funding since start
	InitialCapital() float64
}

// FundingInfo is optionally implemented by a Context that knows the latest
// hourly funding rate per coin (fraction; positive = longs pay).
type FundingInfo interface {
	FundingRate(coin string) float64
}

// StdContexter is optionally implemented by a Context that carries a
// cancellation context (paper sessions do), so strategies that call out
// (LLM providers) stop promptly when the run is stopped.
type StdContexter interface {
	StdContext() context.Context
}

// StdContext returns c's cancellation context when it has one, else
// context.Background().
func StdContext(c Context) context.Context {
	if sc, ok := c.(StdContexter); ok {
		if x := sc.StdContext(); x != nil {
			return x
		}
	}
	return context.Background()
}

// Strategy is any decision maker: Pine, thesis basket, LLM, rules.
type Strategy interface {
	Spec() Spec
	Init(ctx Context) error
	OnBar(ctx Context) error
}

// DataSource supplies history. hyperliquid.Reader satisfies it as-is.
type DataSource interface {
	Candles(ctx context.Context, coin string, iv domain.Interval, n int, end int64) ([]domain.Candle, error)
	Funding(ctx context.Context, coin string, start, end int64) ([]domain.FundingPoint, error)
}

// Costs model fees, slippage and funding (hyperliquid base taker 4.5 bps,
// maker 1.5 bps).
type Costs struct {
	TakerBps    float64
	MakerBps    float64
	SlippageBps float64
	Funding     bool // apply hourly funding from FundingHistory
}

// DefaultCosts are Hyperliquid base tier.
var DefaultCosts = Costs{TakerBps: 4.5, MakerBps: 1.5, SlippageBps: 2, Funding: true}
