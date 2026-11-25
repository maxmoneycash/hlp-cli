package cards

import (
	"regexp"
	"strings"
)

// Warning tags (DESIGN.md section 5.1 rule 5): inline, a card's warnings
// collapse to one gold tag of 4 words or fewer. A renderer may set
// card.tag itself; otherwise autoTag names the first warning it knows.
// Warnings it does not know count toward "! N NOTES". The full text is in
// the full card's NOTES section.

var (
	reClipped = regexp.MustCompile(`(\d+) order\(s\) clipped`)
	reDaysAgo = regexp.MustCompile(`(\d+)\s*(?:d|days?)\b`)
	rePending = regexp.MustCompile(`(\d+) order\(s\) still pending`)
	reExclude = regexp.MustCompile(`^(\d+) vaults? without drawdown`)
)

// autoTag returns the tag for the first recognised warning, or "".
func autoTag(warns []string) string {
	for _, w := range warns {
		if t := tagFor(w); t != "" {
			return t
		}
	}
	return ""
}

func tagFor(w string) string {
	l := strings.ToLower(w)
	switch {
	case strings.Contains(l, "not investable"):
		return "NOT INVESTABLE"
	case reClipped.MatchString(w):
		return reClipped.FindStringSubmatch(w)[1] + " ORDERS CLIPPED"
	case rePending.MatchString(w):
		return rePending.FindStringSubmatch(w)[1] + " ORDERS PENDING"
	case strings.Contains(l, "wiped out"):
		return "WIPED OUT"
	case strings.Contains(l, "out of date") || strings.Contains(l, "last updated"):
		if m := reDaysAgo.FindStringSubmatch(w); m != nil {
			return "HISTORY " + m[1] + "D OLD"
		}
		return "HISTORY OLD"
	case reExclude.MatchString(w):
		return reExclude.FindStringSubmatch(w)[1] + " EXCLUDED"
	case strings.Contains(l, "broker mode"):
		return "BROKER MODE"
	case strings.Contains(l, "history limited") || strings.Contains(l, "history starts"):
		return "SHORT HISTORY"
	case strings.Contains(l, "unavailable") || strings.Contains(l, "unreachable"):
		return "PARTIAL DATA"
	}
	return ""
}

// resolved reports a warning about a problem the data already worked
// around: a vault whose hlp.xyz history is old is charted from the live
// Hyperliquid portfolio, so its curve is fresh. Inline it gets no tag (the
// card would contradict the pane's DATA FRESH check); the pane's NOTES
// keep the full text.
func resolved(w string) bool {
	return strings.Contains(strings.ToLower(w), "curve from the hyperliquid portfolio")
}
