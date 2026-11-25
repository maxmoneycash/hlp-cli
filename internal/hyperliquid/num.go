package hyperliquid

import (
	"encoding/json"
	"strconv"

	"github.com/shopspring/decimal"
)

// Dec is a Hyperliquid decimal kept as its exact wire string. It unmarshals
// from either a JSON string or a JSON number (the WS docs say number, live
// says string; accept both).
type Dec string

// UnmarshalJSON accepts "123.4", 123.4 and null.
func (d *Dec) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*d = ""
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*d = Dec(s)
		return nil
	}
	*d = Dec(b)
	return nil
}

// Float parses for display/analytics. Empty or invalid -> 0.
func (d Dec) Float() float64 {
	f, _ := strconv.ParseFloat(string(d), 64)
	return f
}

// Decimal parses exactly, for anything that feeds order sizing.
func (d Dec) Decimal() (decimal.Decimal, error) { return decimal.NewFromString(string(d)) }

// MaxDecimals: 6 for perps, 8 for spot (tick-and-lot-size docs).
const (
	MaxDecimalsPerp = 6
	MaxDecimalsSpot = 8
)

// MaxSigFigs is the significant-figure cap for non-integer prices.
const MaxSigFigs = 5

// magnitude returns floor(log10(|d|)) computed exactly; d must be non-zero.
func magnitude(d decimal.Decimal) int {
	a := d.Abs()
	return a.NumDigits() + int(a.Exponent()) - 1
}

// RoundPx rounds a price to at most 5 significant figures and at most
// (maxDec - szDecimals) decimals; integers are always valid. dir: +1 ceil,
// -1 floor, 0 nearest (Hyperliquid's tick and lot rules).
func RoundPx(px decimal.Decimal, szDecimals, maxDec, dir int) decimal.Decimal {
	if px.IsZero() || px.IsInteger() {
		return px
	}
	e := magnitude(px)
	decs := min(maxDec-szDecimals, MaxSigFigs-1-e)
	if decs < 0 {
		decs = 0
	}
	switch {
	case dir > 0:
		return px.RoundCeil(int32(decs))
	case dir < 0:
		return px.RoundFloor(int32(decs))
	}
	return px.Round(int32(decs))
}

// ValidPx reports whether a user-supplied price is already on the grid.
// Off-grid user prices are REFUSED (INVALID_TICK), never silently rounded.
// Non-positive prices are never valid.
func ValidPx(px decimal.Decimal, szDecimals, maxDec int) bool {
	if px.Sign() <= 0 {
		return false
	}
	if px.IsInteger() {
		return true
	}
	return px.Equal(RoundPx(px, szDecimals, maxDec, 0)) && decimalPlaces(px) <= maxDec-szDecimals
}

// decimalPlaces counts digits after the point once trailing zeros are gone.
func decimalPlaces(d decimal.Decimal) int {
	s := ToWire(d)
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			return len(s) - i - 1
		}
	}
	return 0
}

// NearestValidPx returns the closest on-grid prices at or below and at or
// above px, for INVALID_TICK hints ("did you mean 1670.1 or 1670.2?").
func NearestValidPx(px decimal.Decimal, szDecimals, maxDec int) (below, above decimal.Decimal) {
	return RoundPx(px, szDecimals, maxDec, -1), RoundPx(px, szDecimals, maxDec, +1)
}

// FloorSz floors a size to szDecimals.
func FloorSz(sz decimal.Decimal, szDecimals int) decimal.Decimal {
	return sz.RoundFloor(int32(szDecimals))
}

// ToWire formats a decimal for signing: trailing zeros stripped, "-0" -> "0".
func ToWire(d decimal.Decimal) string {
	if d.IsZero() {
		return "0"
	}
	// decimal.String never uses exponent notation and trims trailing zeros
	// after the point; guard anyway so the signed string is canonical.
	s := d.String()
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			j := len(s)
			for j > i+1 && s[j-1] == '0' {
				j--
			}
			if j == i+1 {
				j = i
			}
			return s[:j]
		}
	}
	return s
}
