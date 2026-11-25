package cards

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// Number formatting shared by every card. Rules:
//   - Money is compact ($151.3M) except where precision matters (prices).
//   - Percent inputs are domain fractions (0.12 = 12%).
//   - Prices follow Hyperliquid's 5-significant-figure display rule.
//   - Missing values render as a dim "-", never as a misleading 0.
//   - Negative numbers carry U+2212 (−), never the ASCII hyphen
//     (DESIGN.md section 2 rule 1). Plain output (--plain, MCP text) folds
//     it back to "-" in safeTerminal, so machine-read text is unchanged.

const dash = "-"

// minus is the display sign of a negative number (U+2212).
const minus = "−"

// USD formats a dollar amount compactly: $950, $12.4K, $151.3M, $1.21B.
// Negative values keep their sign before the dollar: −$3.4K.
func USD(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return dash
	}
	sign := ""
	if v < 0 {
		sign, v = minus, -v
	}
	if v < 0.005 {
		return "$0.00"
	}
	// Fixed decimals per magnitude keep columns of money aligned.
	switch {
	case v >= 1e9:
		return sign + "$" + strconv.FormatFloat(v/1e9, 'f', 2, 64) + "B"
	case v >= 1e6:
		return sign + "$" + strconv.FormatFloat(v/1e6, 'f', 1, 64) + "M"
	case v >= 1e4:
		return sign + "$" + strconv.FormatFloat(v/1e3, 'f', 1, 64) + "K"
	case v >= 100:
		return sign + "$" + Thousands(v, 0)
	}
	return sign + "$" + strconv.FormatFloat(v, 'f', 2, 64)
}

// SignedUSD is USD with an explicit + for gains.
func SignedUSD(v float64) string {
	if v > 0 {
		return "+" + USD(v)
	}
	return USD(v)
}

// Pct formats a fraction as a percent with the given decimals (12.3%).
func Pct(v float64, decimals int) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return dash
	}
	return strconv.FormatFloat(v*100, 'f', decimals, 64) + "%"
}

// SignedPct formats a fraction as a signed percent (+12.3%, −4.0%). Values
// that round to zero print without a sign.
func SignedPct(v float64, decimals int) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return dash
	}
	s := strconv.FormatFloat(v*100, 'f', decimals, 64)
	if strings.Trim(s, "-0.") == "" {
		return strings.TrimPrefix(s, "-") + "%"
	}
	if v > 0 {
		return "+" + s + "%"
	}
	return minus + strings.TrimPrefix(s, "-") + "%"
}

// PctDecimals is the house precision for a percent: one decimal, two
// below 1% so small moves do not round to nothing (DESIGN.md section 3).
func PctDecimals(v float64) int {
	if math.Abs(v) < 0.01 {
		return 2
	}
	return 1
}

// Chg is SignedPct at the house precision: +24.0%, −0.9%, +0.04%.
func Chg(v float64) string { return SignedPct(v, PctDecimals(v)) }

// PxDecimals returns the display decimals for a price under Hyperliquid's
// 5-significant-figure rule (97412, 3412.5, 71.76, 3.1483), capped at 8.
func PxDecimals(v float64) int {
	v = math.Abs(v)
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 2
	}
	d := 5 - (int(math.Floor(math.Log10(v))) + 1)
	return min(max(d, 0), 8)
}

// Px formats a price with thousands separators and the 5-sig-fig rule.
// Trailing zeros past two decimals are dropped (71.760 -> 71.76).
func Px(v float64) string {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return dash
	}
	d := PxDecimals(v)
	s := Thousands(v, d)
	for d > 2 && strings.HasSuffix(s, "0") {
		s = s[:len(s)-1]
		d--
	}
	return s
}

// PxFixed formats a price at exactly 5 significant figures, keeping
// trailing zeros (66.540, not 66.54), so a live ticker slot never changes
// width between updates.
func PxFixed(v float64) string {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return dash
	}
	return Thousands(v, PxDecimals(v))
}

// Size formats a base-unit size with up to 5 decimals, trailing zeros trimmed.
func Size(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return dash
	}
	a := math.Abs(v)
	dec := 5
	switch {
	case a >= 1e5:
		dec = 0
	case a >= 100:
		dec = 2
	}
	s := Thousands(v, dec)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// Num formats a plain ratio (Sharpe, beta) with the given decimals.
func Num(v float64, decimals int) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return dash
	}
	return strconv.FormatFloat(v, 'f', decimals, 64)
}

// Thousands formats v with comma grouping and fixed decimals.
func Thousands(v float64, decimals int) string {
	s := strconv.FormatFloat(math.Abs(v), 'f', decimals, 64)
	intPart, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, frac = s[:i], s[i:]
	}
	var b strings.Builder
	if v < 0 && strings.Trim(s, "0.") != "" {
		b.WriteString(minus)
	}
	for i, r := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	b.WriteString(frac)
	return b.String()
}

// ShortAddr shortens a 0x address to 0x1234…abcd.
func ShortAddr(a string) string {
	if len(a) <= 12 {
		return a
	}
	return a[:6] + "…" + a[len(a)-4:]
}

// Age formats a day count as a label-style unit: 12D, 7MO, 1.4Y
// (DESIGN.md section 3 rule 4: a number plus a unit, not a word).
func Age(days int) string {
	switch {
	case days <= 0:
		return dash
	case days < 60:
		return strconv.Itoa(days) + "D"
	case days < 365:
		return strconv.Itoa(days/30) + "MO"
	}
	return trimFloat(float64(days)/365, 1) + "Y"
}

// Ago is a compact duration for a STALE tag: 45S, 3M, 2H, 4D.
func Ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(max(int(d/time.Second), 0)) + "S"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "M"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "H"
	}
	return strconv.Itoa(int(d/(24*time.Hour))) + "D"
}

// Clock formats a millisecond timestamp as HH:MM:SS in loc.
func Clock(ms int64, loc *time.Location) string {
	if ms <= 0 {
		return dash
	}
	return time.UnixMilli(ms).In(locOr(loc)).Format("15:04:05")
}

// Stamp formats a time as "Nov 15 14:02:11", the as-of style used in footers.
func Stamp(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return ""
	}
	return t.In(locOr(loc)).Format("Jan 2 15:04:05")
}

// StampZone is Stamp with the zone named: "Nov 15 04:41:08 PST", and
// "… UTC" when loc is UTC (or nil). The CLI uses it so a footer read next
// to a web UI never hides an 8-hour skew.
func StampZone(t time.Time, loc *time.Location) string {
	s := Stamp(t, loc)
	if s == "" {
		return ""
	}
	l := locOr(loc)
	if l == time.UTC {
		return s + " UTC"
	}
	zone, _ := t.In(l).Zone()
	if zone == "" {
		return s
	}
	return s + " " + zone
}

func locOr(loc *time.Location) *time.Location {
	if loc == nil {
		return time.UTC
	}
	return loc
}

// trimFloat formats with up to dec decimals and strips trailing zeros.
func trimFloat(v float64, dec int) string {
	s := strconv.FormatFloat(v, 'f', dec, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// plural picks the singular or plural noun for n ("1 vault", "3 vaults").
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// count is n followed by its noun: count(1, "vault", "vaults") = "1 vault".
func count(n int, one, many string) string {
	return strconv.Itoa(n) + " " + plural(n, one, many)
}
