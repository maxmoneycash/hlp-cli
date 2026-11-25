package metrics

import (
	"math"
	"strconv"
	"strings"
)

// Small formatting helpers for checklist details. They stay here (not in
// a UI package) so Check.Detail strings are identical in cards, JSON and
// MCP output.

func itoa(n int) string { return strconv.Itoa(n) }

func ftoa(x float64, prec int) string { return strconv.FormatFloat(x, 'f', prec, 64) }

// pct formats a fraction as a percentage with one decimal: -0.312 -> "-31.2%".
func pct(f float64) string { return ftoa(f*100, 1) + "%" }

// usd formats dollars compactly: 151318485 -> "$151.3M", 95000 -> "$95.0K" (the cards' casing).
func usd(v float64) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	switch {
	case v >= 1e9:
		return sign + "$" + ftoa(v/1e9, 2) + "B"
	case v >= 1e6:
		return sign + "$" + ftoa(v/1e6, 1) + "M"
	case v >= 1e3:
		return sign + "$" + ftoa(v/1e3, 1) + "K"
	}
	return sign + "$" + ftoa(math.Round(v), 0)
}

func equalFold(a, b string) bool { return strings.EqualFold(a, b) }
