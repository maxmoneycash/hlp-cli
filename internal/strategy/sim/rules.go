package sim

import (
	"math"
	"strconv"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// This file is the Hyperliquid account model of the simulator (pine-subset
// spec sections 8.2 and 8.3, fixes A2 and A3): lot-size flooring, the $10
// minimum notional, the initial-margin check at fill time and liquidation
// at maintenance margin. It applies only to coins listed in Engine.Rules,
// so a strategy unit test without market metadata behaves as before.

// MinNotional is Hyperliquid's minimum order value for perps (USD).
const MinNotional = 10

// Rules are one market's exchange constraints.
type Rules struct {
	// SzDecimals is the size precision: fills are floored to
	// 10^-SzDecimals base units (BTC 5, ETH 4, DOGE 0).
	SzDecimals int
	// MaxLeverage is the exchange's maximum leverage (BTC 40, DOGE 10).
	// 0 disables the margin check and liquidation for this coin.
	MaxLeverage float64
	// MinNotional overrides the $10 minimum (0 = MinNotional, < 0 = none).
	MinNotional float64
}

// RulesFrom builds Rules from registry metadata.
func RulesFrom(m domain.Market) Rules {
	return Rules{SzDecimals: m.SzDecimals, MaxLeverage: float64(m.MaxLeverage)}
}

// Stats counts what the account model changed.
type Stats struct {
	Clipped          int `json:"clipped"`          // orders reduced to fit the initial margin
	Liquidations     int `json:"liquidations"`     // positions liquidated at maintenance margin
	BelowLot         int `json:"belowLot"`         // orders dropped: size floored to 0
	BelowMinNotional int `json:"belowMinNotional"` // orders dropped: under $10 notional
}

// SetLeverage sets the strategy's own leverage for coin (Pine's
// 100 / margin_long and 100 / margin_short). The effective leverage is
// min(script, Rules.MaxLeverage). 0 means "no script limit".
func (e *Engine) SetLeverage(coin string, long, short float64) {
	if e.lev == nil {
		e.lev = map[string][2]float64{}
	}
	e.lev[coin] = [2]float64{long, short}
}

// Stats returns the account-model counters.
func (e *Engine) Stats() Stats { return e.stats }

// leverage is L* for a position of the given sign on coin (0 = unlimited).
func (e *Engine) leverage(coin string, long bool) float64 {
	r, ok := e.Rules[coin]
	if !ok {
		return 0
	}
	l := r.MaxLeverage
	if s, ok := e.lev[coin]; ok {
		sl := s[1]
		if long {
			sl = s[0]
		}
		if sl > 0 && (l <= 0 || sl < l) {
			l = sl
		}
	}
	return l
}

// applyRules adjusts a fill size for coin's exchange rules at px. It
// returns the size to fill and false when the order must be dropped.
func (e *Engine) applyRules(r *Resting, sz, px float64) (float64, bool) {
	rules, ok := e.Rules[r.Coin]
	if !ok || px <= 0 {
		return sz, true
	}
	pos := e.L.Position(r.Coin)
	buy := r.Side == domain.Buy
	reducing := pos != 0 && (pos > 0) != buy
	closesAll := reducing && math.Abs(sz-math.Abs(pos)) <= 1e-12*math.Max(1, math.Abs(pos))
	if !closesAll {
		sz = floorTo(sz, rules.SzDecimals)
		if sz <= 0 {
			e.stats.BelowLot++
			e.logf("%s order dropped: qty below lot size (%s)", r.Coin, lotString(rules.SzDecimals))
			return 0, false
		}
	}
	// Initial margin on an order that opens, grows or flips exposure.
	after := signedAfter(pos, sz, buy)
	if opens := !reducing || (after != 0 && !sameSign(after, pos)); opens {
		if lev := e.leverage(r.Coin, after > 0); lev > 0 {
			avail := e.L.Equity(e.priceWith(r.Coin, px)) - e.marginUsed(r.Coin)
			maxAbs := math.Max(avail, 0) * lev / px
			if math.Abs(after) > maxAbs*(1+1e-12) {
				nsz := maxAbs - math.Abs(pos) // grow to the largest allowed size
				if reducing {
					nsz = math.Abs(pos) + maxAbs // flip into the largest allowed size
				}
				nsz = floorTo(math.Max(nsz, 0), rules.SzDecimals)
				e.stats.Clipped++
				e.logf("%s order clipped by margin: %.8g -> %.8g at %.4gx", r.Coin, sz, nsz, lev)
				sz = nsz
				if sz <= 0 {
					return 0, false
				}
			}
		}
	}
	minN := rules.MinNotional
	if minN == 0 {
		minN = MinNotional
	}
	onlyReduces := reducing && sz <= math.Abs(pos)+1e-12
	if minN > 0 && !r.ReduceOnly && !onlyReduces && sz*px < minN {
		e.stats.BelowMinNotional++
		e.logf("%s order dropped: $%.2f is below the $%.0f minimum notional", r.Coin, sz*px, minN)
		return 0, false
	}
	return sz, true
}

// CheckLiquidation liquidates coin's position when the account equity,
// marked at the worst price of bar (low for a long, high for a short),
// falls below the maintenance margin (notional / (2 * maxLeverage) for
// every coin with rules). The fill is at the price where equity reaches
// maintenance, or at the open when the bar gapped through it, with the
// taker fee. Hyperliquid's extra clearance fee is not charged. It reports
// whether a liquidation happened.
func (e *Engine) CheckLiquidation(coin string, bar domain.Candle) bool {
	r, ok := e.Rules[coin]
	pos := e.L.Position(coin)
	if !ok || r.MaxLeverage <= 0 || pos == 0 || e.halted {
		return false
	}
	worst := bar.L
	if pos < 0 {
		worst = bar.H
	}
	m := 1 / (2 * r.MaxLeverage)
	// Everything except this coin: cash, other unrealized PnL, other
	// maintenance.
	other := e.L.Cash()
	otherMaint := 0.0
	for _, c := range e.L.Coins() {
		if c == coin {
			continue
		}
		px := e.Price(c)
		if px <= 0 {
			px = e.L.Entry(c)
		}
		p := e.L.Position(c)
		other += p * (px - e.L.Entry(c))
		if rc, ok := e.Rules[c]; ok && rc.MaxLeverage > 0 {
			otherMaint += math.Abs(p) * px / (2 * rc.MaxLeverage)
		}
	}
	entry := e.L.Entry(coin)
	eqAt := func(px float64) float64 { return other + pos*(px-entry) }
	maintAt := func(px float64) float64 { return otherMaint + math.Abs(pos)*px*m }
	if eqAt(worst) >= maintAt(worst) {
		return false
	}
	// Solve other + pos*(p-entry) = otherMaint + |pos|*p*m for p.
	var liq float64
	if pos > 0 {
		liq = (otherMaint - other + pos*entry) / (pos * (1 - m))
	} else {
		liq = (otherMaint - other + pos*entry) / (pos * (1 + m))
	}
	if pos > 0 && bar.O <= liq {
		liq = bar.O
	}
	if pos < 0 && bar.O >= liq {
		liq = bar.O
	}
	if liq <= 0 || bad(liq) {
		liq = worst
	}
	side := domain.Sell
	if pos < 0 {
		side = domain.Buy
	}
	f := e.L.Fill(coin, side, math.Abs(pos), liq, e.Costs.TakerBps/1e4, bar.T)
	f.Liq = true
	e.stats.Liquidations++
	e.cancel(func(o *Resting) bool { return o.Coin == coin && o.ReduceOnly })
	e.logf("%s liquidated at %.6g (equity below maintenance margin at %.4gx max leverage)", coin, liq, r.MaxLeverage)
	e.emit(f, "liquidation")
	return true
}

// marginUsed is the initial margin held by every position except coin.
func (e *Engine) marginUsed(except string) float64 {
	used := 0.0
	for _, c := range e.L.Coins() {
		if c == except {
			continue
		}
		p := e.L.Position(c)
		px := e.Price(c)
		if px <= 0 {
			px = e.L.Entry(c)
		}
		if lev := e.leverage(c, p > 0); lev > 0 {
			used += math.Abs(p) * px / lev
		}
	}
	return used
}

// priceWith marks coin at px and every other coin at its last close.
func (e *Engine) priceWith(coin string, px float64) func(string) float64 {
	return func(c string) float64 {
		if c == coin {
			return px
		}
		return e.Price(c)
	}
}

func signedAfter(pos, sz float64, buy bool) float64 {
	if buy {
		return pos + sz
	}
	return pos - sz
}

// floorTo floors x to d decimals, tolerating float noise (2.9999999999 -> 3).
func floorTo(x float64, d int) float64 {
	if d < 0 {
		return x
	}
	p := math.Pow(10, float64(d))
	return math.Floor(x*p+1e-9) / p
}

func lotString(d int) string {
	if d <= 0 {
		return "1"
	}
	return strconv.FormatFloat(math.Pow(10, -float64(d)), 'f', -1, 64)
}
