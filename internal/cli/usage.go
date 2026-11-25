package cli

import (
	"strings"
	"unicode"
)

// The help renderer (fang) title-cases the first word of every flag usage,
// which turns "1m 3m" into "1M 3m", "JSON" into "Json" and "0x address"
// into "0X address". safeUsage rewrites a usage so its first word survives
// that transform, and fills usages that are empty.

// defaultUsage covers tool fields without a jsonschema description.
var defaultUsage = map[string]string{
	"capital":  "Starting capital in USD (default 10000)",
	"coin":     "Market, e.g. BTC, HYPE, xyz:SP500 (case-insensitive)",
	"id":       "Session id (from `hlp paper list`)",
	"interval": "Bar interval: 1m 3m 5m 15m 30m 1h 2h 4h 8h 12h 1d 3d 1w 1M",
	"days":     "Lookback in days",
	"file":     "Strategy file",
	"vault":    "Vault address (0x…) or name",
	"usd":      "Amount in USD",
	"pct":      "Percent of the position (default 100)",
	"all":      "Every position",
	"oid":      "Order id",
}

// prefixRewrites turn a mangled opening into plain words.
var prefixRewrites = [][2]string{
	{"0x address", "Address (0x…)"},
	{"EMA periods", "Exponential moving average periods"},
	{"SMA periods", "Simple moving average periods"},
	{"USD, default", "Amount in USD, default"},
	{"tvl (default)", "Sort key: tvl (default)"},
}

// fullUsage replaces terse schema text for a few flags outright.
var fullUsage = map[string]string{
	"min-tvl": "Minimum TVL in USD (default 10000)",
	"min-age": "Minimum vault age in days",
	"bench":   "Benchmarks to compare against (default HLP,BTC,HYPE)",
}

// leadWords name a flag when its usage starts with a word the transform
// would mangle ("1m …", "0x …", "USD …", "EMA …").
var leadWords = map[string]string{
	"interval": "Interval:",
	"address":  "Address",
	"usd":      "Amount in",
	"json":     "Output",
}

// survivesTitle reports whether title-casing w (Unicode word rules: each
// letter run after a digit or hyphen restarts, the rest is lowercased) only
// uppercases its first letter. Conservative: letters, lowercase after the
// first, with trailing punctuation allowed.
func survivesTitle(w string) bool {
	w = strings.TrimRight(w, ":,.;)")
	if w == "" {
		return false
	}
	for i, r := range []rune(w) {
		if !unicode.IsLetter(r) || (i > 0 && !unicode.IsLower(r)) {
			return false
		}
	}
	return true
}

func upperFirst(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// safeUsage returns a usage string whose first word reads the same after
// the help renderer title-cases it.
func safeUsage(name, desc string) string {
	desc = strings.TrimSpace(desc)
	if u, ok := fullUsage[name]; ok {
		return u
	}
	for _, rw := range prefixRewrites {
		if strings.HasPrefix(desc, rw[0]) {
			desc = rw[1] + desc[len(rw[0]):]
		}
	}
	if desc == "" {
		desc = defaultUsage[name]
		if desc == "" {
			desc = strings.ReplaceAll(name, "-", " ")
		}
	}
	first := desc
	if i := strings.IndexFunc(desc, unicode.IsSpace); i >= 0 {
		first = desc[:i]
	}
	if survivesTitle(first) {
		return upperFirst(desc)
	}
	if lead, ok := leadWords[name]; ok {
		return lead + " " + desc
	}
	if d, ok := defaultUsage[name]; ok && survivesTitle(strings.Fields(d)[0]) {
		return d
	}
	return upperFirst(strings.ReplaceAll(name, "-", " ")) + ": " + desc
}
