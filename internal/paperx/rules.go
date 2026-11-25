package paperx

import (
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/hyperliquid"
)

// Reject is an order refusal carrying Hyperliquid's own error text.
type Reject struct {
	Code string // TICK, SIZE, MIN_VALUE, ALO_CROSS, IOC_NO_MATCH, REDUCE_ONLY, MARGIN, LEVERAGE, TPSL, UNKNOWN_MARKET, NOT_FOUND
	Msg  string
}

func (r *Reject) Error() string { return r.Msg }

func reject(code string, s Spec, format string, a ...any) *Reject {
	return &Reject{Code: code, Msg: fmt.Sprintf(format, a...) + fmt.Sprintf(" asset=%d", s.AssetID)}
}

// Rejection texts (Hyperliquid's).
const (
	msgTick     = "Price must be divisible by tick size."
	msgSize     = "Order has invalid size."
	msgMin      = "Order must have minimum value of $10."
	msgAlo      = "Post only order would have immediately matched, bbo was %s@%s."
	msgIocNone  = "Order could not immediately match against any resting orders."
	msgReduce   = "Reduce only order would increase position."
	msgMargin   = "Insufficient margin to place order."
	msgLeverage = "Invalid leverage value."
	msgTpsl     = "Invalid TP/SL price."
	msgIsolated = "Cross margin is not allowed for this asset."
)

// validStatic runs the checks that need no book: price grid, lot, $10
// minimum at the limit price (reduce-only exempt), TIF, leverage, TP/SL side.
func validStatic(s Spec, r OrderReq, curLev int) *Reject {
	if r.Px.Sign() <= 0 || !hyperliquid.ValidPx(r.Px, s.SzDecimals, s.MaxDecimals) {
		return reject("TICK", s, msgTick)
	}
	if r.Sz.Sign() <= 0 || !r.Sz.Equal(hyperliquid.FloorSz(r.Sz, s.SzDecimals)) {
		return reject("SIZE", s, msgSize)
	}
	if !r.ReduceOnly && r.Sz.Mul(r.Px).LessThan(MinNotional) {
		return reject("MIN_VALUE", s, msgMin)
	}
	switch r.TIF {
	case Gtc, Ioc, Alo:
	default:
		return &Reject{Code: "TIF", Msg: fmt.Sprintf("Invalid TIF %q (Gtc, Ioc or Alo).", r.TIF)}
	}
	lev := r.Leverage
	if lev == 0 {
		lev = curLev
	}
	if lev < 1 || (s.MaxLeverage > 0 && lev > s.MaxLeverage) {
		return reject("LEVERAGE", s, msgLeverage)
	}
	if s.OnlyIsolated && !r.Isolated && r.Leverage > 0 {
		return reject("ISOLATED", s, msgIsolated)
	}
	for _, leg := range []struct {
		px   decimal.Decimal
		kind string
	}{{r.TP, "tp"}, {r.SL, "sl"}} {
		if leg.px.IsZero() {
			continue
		}
		if !hyperliquid.ValidPx(leg.px, s.SzDecimals, s.MaxDecimals) {
			return reject("TICK", s, msgTick)
		}
		// a long's TP sits above the entry, its SL below (shorts mirror)
		above := (leg.kind == "tp") == (r.Side == domain.Buy)
		if r.Market { // Px is the slippage bound; the caller checked against the mid
			continue
		}
		if above && !leg.px.GreaterThan(r.Px) || !above && !leg.px.LessThan(r.Px) {
			return reject("TPSL", s, msgTpsl)
		}
	}
	return nil
}

// roundPx puts a computed price on the grid (dir +1 up, -1 down).
func roundPx(s Spec, px decimal.Decimal, dir int) decimal.Decimal {
	return hyperliquid.RoundPx(px, s.SzDecimals, s.MaxDecimals, dir)
}

// mmr returns the maintenance margin of a notional under s: half the
// initial margin at the tier's max leverage, with Hyperliquid's tier
// deduction so the margin is continuous across tiers.
func maintenance(s Spec, notional decimal.Decimal) decimal.Decimal {
	tiers := s.Tiers
	if len(tiers) == 0 {
		ml := s.MaxLeverage
		if ml <= 0 {
			ml = 1
		}
		tiers = []Tier{{LowerBound: decimal.Zero, MaxLeverage: ml}}
	}
	two := decimal.NewFromInt(2)
	deduction := decimal.Zero
	prev := decimal.Zero
	rate := decimal.Zero
	for i, t := range tiers {
		if i > 0 && notional.LessThan(t.LowerBound) {
			break
		}
		r := decimal.NewFromInt(1).Div(two.Mul(decimal.NewFromInt(int64(max(t.MaxLeverage, 1)))))
		if i > 0 {
			deduction = deduction.Add(t.LowerBound.Mul(r.Sub(prev)))
		}
		prev, rate = r, r
	}
	return notional.Mul(rate).Sub(deduction)
}

// mmRate is the marginal maintenance rate at notional (for liquidation
// price solving).
func mmRate(s Spec, notional decimal.Decimal) decimal.Decimal {
	if notional.IsZero() {
		return maintenance(s, decimal.NewFromInt(1))
	}
	return maintenance(s, notional).Div(notional)
}
