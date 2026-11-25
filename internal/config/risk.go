package config

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// Hard ceilings for risk caps. A config may tighten them, never exceed them.
const (
	MaxLeverageCeiling = 50     // Hyperliquid's highest max leverage on any asset
	MaxDeadManSeconds  = 86_400 // scheduleCancel more than a day out is not a dead-man switch
	// MaxOrdersPerMinCeiling bounds risk.max_orders_per_min: more than ten
	// risk-adding actions a second is a loop, not a strategy.
	MaxOrdersPerMinCeiling = 600
	// DefaultMaxOrdersPerMin is the default order-rate cap.
	DefaultMaxOrdersPerMin = 60
	// VaultsCurated is the risk.allowed_vaults entry meaning "any vault in
	// the curated registry".
	VaultsCurated = "curated"
)

// Validate checks the caps. Fields documented as "0 = off" (DeadManSeconds)
// and "empty = all" (AllowedCoins) may be zero; MaxNotionalUSD, MaxLeverage
// and DailyLossUSD must be positive, because a zero cap would mean "no cap" by
// accident.
func (r RiskConfig) Validate() error {
	var errs []error
	bad := func(key, format string, a ...any) {
		errs = append(errs, &Error{Key: "risk." + key, Msg: fmt.Sprintf(format, a...)})
	}
	if !(r.MaxNotionalUSD > 0) || math.IsInf(r.MaxNotionalUSD, 0) {
		bad("max_notional_usd", "must be a positive number of USD (got %v)", r.MaxNotionalUSD)
	}
	if r.MaxLeverage < 1 || r.MaxLeverage > MaxLeverageCeiling {
		bad("max_leverage", "must be 1..%d (got %d)", MaxLeverageCeiling, r.MaxLeverage)
	}
	if !(r.DailyLossUSD > 0) || math.IsInf(r.DailyLossUSD, 0) {
		bad("daily_loss_usd", "must be a positive number of USD (got %v)", r.DailyLossUSD)
	}
	if r.DeadManSeconds < 0 || r.DeadManSeconds > MaxDeadManSeconds {
		bad("dead_man_seconds", "must be 0 (off) or up to %d (got %d)", MaxDeadManSeconds, r.DeadManSeconds)
	} else if r.DeadManSeconds > 0 && r.DeadManSeconds < 5 {
		// Hyperliquid requires scheduleCancel at least 5 s in the future.
		bad("dead_man_seconds", "must be at least 5 when set (got %d)", r.DeadManSeconds)
	}
	for i, c := range r.AllowedCoins {
		if strings.TrimSpace(c) == "" {
			bad(fmt.Sprintf("allowed_coins[%d]", i), "empty coin")
		}
	}
	for i, v := range r.AllowedVaults {
		if v = strings.TrimSpace(v); !strings.EqualFold(v, VaultsCurated) && !IsAddress(v) {
			bad(fmt.Sprintf("allowed_vaults[%d]", i), "%q is not a 0x vault address or %q", v, VaultsCurated)
		}
	}
	for i, n := range r.Networks {
		if nn := domain.Network(strings.ToLower(strings.TrimSpace(n))); nn != domain.Mainnet && nn != domain.Testnet {
			bad(fmt.Sprintf("networks[%d]", i), "%q is not mainnet or testnet", n)
		}
	}
	if r.MaxOrdersPerMin < 0 || r.MaxOrdersPerMin > MaxOrdersPerMinCeiling {
		bad("max_orders_per_min", "must be 0 (off) or up to %d (got %d)", MaxOrdersPerMinCeiling, r.MaxOrdersPerMin)
	}
	return errors.Join(errs...)
}

// AllowsNetwork reports whether risk.networks lets this config trade on n
// (an empty list allows both networks).
func (r RiskConfig) AllowsNetwork(n domain.Network) bool {
	if len(r.Networks) == 0 {
		return true
	}
	for _, x := range r.Networks {
		if domain.Network(strings.ToLower(strings.TrimSpace(x))) == n {
			return true
		}
	}
	return false
}

// AllowsVault reports whether risk.allowed_vaults lets a deposit go to addr.
// curated says whether addr is in the curated registry. automated is true
// for callers no person typed (MCP, agent, bot, unknown): with an empty list
// they may deposit only into curated vaults, while a person at the CLI or
// TUI may deposit anywhere. A non-empty list applies to every caller.
func (r RiskConfig) AllowsVault(addr string, curated, automated bool) bool {
	addr = strings.TrimSpace(addr)
	if len(r.AllowedVaults) == 0 {
		return curated || !automated
	}
	for _, v := range r.AllowedVaults {
		v = strings.TrimSpace(v)
		if strings.EqualFold(v, VaultsCurated) && curated {
			return true
		}
		if strings.EqualFold(v, addr) && IsAddress(addr) {
			return true
		}
	}
	return false
}

// Expiry parses trading.expires. A bare date ("2026-12-31") means trading
// stops at the end of that UTC day, so the returned instant is the next
// midnight UTC; an RFC 3339 time is used as written. The zero time means no
// expiry is set. An unparsable value is an error, never "no expiry".
func (t TradingConfig) Expiry() (at time.Time, err error) {
	s := strings.TrimSpace(t.Expires)
	if s == "" {
		return time.Time{}, nil
	}
	if d, err := time.Parse(time.DateOnly, s); err == nil {
		return d.UTC().AddDate(0, 0, 1), nil
	}
	if ts, err := time.Parse(time.RFC3339, s); err == nil {
		return ts.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("%q is not a date (2026-12-31) or RFC 3339 time (2026-12-31T18:00:00Z)", t.Expires)
}

// Expired reports whether trading has expired at now. An unparsable
// trading.expires counts as expired (refuse, never assume "no expiry").
func (t TradingConfig) Expired(now time.Time) bool {
	at, err := t.Expiry()
	if err != nil {
		return true
	}
	return !at.IsZero() && !now.Before(at)
}

// AllowsCoin reports whether coin passes AllowedCoins (case-insensitive; an
// empty list allows every coin). HIP-3 coins match with their dex prefix
// ("xyz:SP500").
func (r RiskConfig) AllowsCoin(coin string) bool {
	if len(r.AllowedCoins) == 0 {
		return true
	}
	for _, c := range r.AllowedCoins {
		if strings.EqualFold(strings.TrimSpace(c), strings.TrimSpace(coin)) {
			return true
		}
	}
	return false
}

// OrderCheck is a mutating action reduced to the numbers the caps care about.
// core's RiskGuard builds it from core.Intent.
type OrderCheck struct {
	Coin            string
	NotionalUSD     float64 // |size| * price; 0 for actions without size (cancel)
	Leverage        int     // requested leverage, 0 = unchanged
	RealizedLossUSD float64 // realized loss so far today in USD, as a positive number
	ReduceOnly      bool    // reduce-only orders and cancels skip notional/leverage/coin caps
}

// RiskViolation is the error CheckOrder returns. Rule is a stable id
// ("max_notional_usd", "max_leverage", "allowed_coins", "daily_loss_usd");
// core maps it to CodeRiskLimit.
type RiskViolation struct {
	Rule  string
	Limit string
	Got   string
}

func (v *RiskViolation) Error() string {
	return fmt.Sprintf("risk cap %s: %s exceeds limit %s", v.Rule, v.Got, v.Limit)
}

// CheckOrder applies the caps to one action and returns a *RiskViolation for
// the first cap it breaks, or nil. The daily-loss halt applies to everything
// except reduce-only actions (closing risk is always allowed).
func (r RiskConfig) CheckOrder(o OrderCheck) error {
	if o.ReduceOnly {
		return nil
	}
	if r.DailyLossUSD > 0 && o.RealizedLossUSD >= r.DailyLossUSD {
		return &RiskViolation{Rule: "daily_loss_usd", Limit: usd(r.DailyLossUSD), Got: usd(o.RealizedLossUSD)}
	}
	if !r.AllowsCoin(o.Coin) {
		return &RiskViolation{Rule: "allowed_coins", Limit: strings.Join(r.AllowedCoins, ","), Got: o.Coin}
	}
	if math.IsNaN(o.NotionalUSD) || o.NotionalUSD < 0 {
		return &RiskViolation{Rule: "max_notional_usd", Limit: usd(r.MaxNotionalUSD), Got: fmt.Sprint(o.NotionalUSD)}
	}
	if r.MaxNotionalUSD > 0 && o.NotionalUSD > r.MaxNotionalUSD {
		return &RiskViolation{Rule: "max_notional_usd", Limit: usd(r.MaxNotionalUSD), Got: usd(o.NotionalUSD)}
	}
	if r.MaxLeverage > 0 && o.Leverage > r.MaxLeverage {
		return &RiskViolation{Rule: "max_leverage", Limit: fmt.Sprintf("%dx", r.MaxLeverage), Got: fmt.Sprintf("%dx", o.Leverage)}
	}
	return nil
}

func usd(v float64) string { return fmt.Sprintf("$%.2f", v) }
