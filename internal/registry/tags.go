package registry

import (
	"sort"
	"strings"
)

// TagGroup is a section of the closed tag vocabulary.
type TagGroup string

const (
	GroupTheme TagGroup = "theme" // what the vault bets on: memes, equities, ai...
	GroupStyle TagGroup = "style" // how it trades: trend, long-short, market-making...
	GroupRisk  TagGroup = "risk"
	GroupVenue TagGroup = "venue" // protocol, hip3, testnet...
	GroupIndex TagGroup = "index" // index construction rules
	GroupOther TagGroup = "other"
	GroupUser  TagGroup = "user" // tags only found in the user's override file
)

// TagGroups lists the groups in discover order: thesis groups first.
var TagGroups = []TagGroup{GroupTheme, GroupStyle, GroupRisk, GroupVenue, GroupIndex, GroupOther, GroupUser}

// ThesisGroups are the groups `hlp vaults discover` presents as theses.
var ThesisGroups = []TagGroup{GroupTheme, GroupStyle}

// GroupTitle is the human label for a tag group.
func GroupTitle(g TagGroup) string {
	switch g {
	case GroupTheme:
		return "Themes"
	case GroupStyle:
		return "Styles"
	case GroupRisk:
		return "Risk"
	case GroupVenue:
		return "Venue and protocol"
	case GroupIndex:
		return "Index rules"
	case GroupOther:
		return "Other"
	case GroupUser:
		return "Your tags"
	}
	return string(g)
}

// TagInfo describes one tag of the vocabulary.
type TagInfo struct {
	Tag   string   `json:"tag"`
	Group TagGroup `json:"group"`
	Label string   `json:"label"`
}

// Vocabulary is the closed tag set. A tag used in vaults.toml must be
// listed here.
var Vocabulary = []TagInfo{
	// venue and protocol
	{"protocol", GroupVenue, "Protocol vaults"},
	{"flagship", GroupVenue, "Flagship"},
	{"hlp-child", GroupVenue, "HLP sub-vaults"},
	{"testnet", GroupVenue, "Testnet"},
	{"unaudited", GroupVenue, "Unaudited contracts"},
	{"hip3", GroupVenue, "HIP-3 markets"},
	// style
	{"market-making", GroupStyle, "Market making"},
	{"liquidations", GroupStyle, "Liquidations"},
	{"trend", GroupStyle, "Trend following"},
	{"momentum", GroupStyle, "Momentum"},
	{"mean-reversion", GroupStyle, "Mean reversion"},
	{"grid", GroupStyle, "Grid"},
	{"long-short", GroupStyle, "Long/short"},
	{"market-neutral", GroupStyle, "Market neutral"},
	{"short-bias", GroupStyle, "Short bias"},
	{"long-only", GroupStyle, "Long only"},
	{"discretionary", GroupStyle, "Discretionary"},
	{"systematic", GroupStyle, "Systematic"},
	{"positional", GroupStyle, "Positional"},
	{"pyramiding", GroupStyle, "Pyramiding"},
	{"convexity", GroupStyle, "Convexity"},
	{"event-driven", GroupStyle, "Event driven"},
	// theme
	{"memes", GroupTheme, "Memecoins"},
	{"equities", GroupTheme, "Equities"},
	{"commodities", GroupTheme, "Commodities"},
	{"unlocks", GroupTheme, "Token unlocks"},
	{"value", GroupTheme, "Value"},
	{"fundamental", GroupTheme, "Fundamental"},
	{"hype", GroupTheme, "HYPE"},
	{"btc", GroupTheme, "Bitcoin"},
	{"ai", GroupTheme, "AI"},
	{"ml", GroupTheme, "Machine learning"},
	{"multi-asset", GroupTheme, "Multi-asset"},
	// risk
	{"high-risk", GroupRisk, "High risk"},
	{"high-leverage", GroupRisk, "High leverage"},
	{"low-leverage", GroupRisk, "Low leverage"},
	{"low-drawdown", GroupRisk, "Low drawdown"},
	// index
	{"index", GroupIndex, "Index"},
	{"benchmark", GroupIndex, "Benchmark"},
	{"blue-chip", GroupIndex, "Blue chip"},
	{"tvl-weighted", GroupIndex, "TVL weighted"},
	{"calmar", GroupIndex, "Calmar ranked"},
	{"sharpe", GroupIndex, "Sharpe ranked"},
	{"return", GroupIndex, "Return ranked"},
	// other
	{"veteran", GroupOther, "Veteran"},
	{"community", GroupOther, "Community"},
}

var vocab = func() map[string]TagInfo {
	m := make(map[string]TagInfo, len(Vocabulary))
	for _, t := range Vocabulary {
		m[t.Tag] = t
	}
	return m
}()

// tagAliases maps common spellings users type onto vocabulary tags.
var tagAliases = map[string]string{
	"meme": "memes", "memecoin": "memes", "memecoins": "memes", "garbage": "memes",
	"stocks": "equities", "stock": "equities", "equity": "equities", "spy": "equities", "sp500": "equities",
	"commodity": "commodities", "gold": "commodities", "oil": "commodities",
	"unlock": "unlocks", "dilution": "unlocks",
	"bitcoin":          "btc",
	"machine-learning": "ml",
	"trend-following":  "trend", "trends": "trend",
	"mm": "market-making", "marketmaking": "market-making", "market-maker": "market-making",
	"liquidation": "liquidations", "liquidator": "liquidations",
	"longshort": "long-short", "long/short": "long-short", "ls": "long-short",
	"neutral": "market-neutral", "delta-neutral": "market-neutral",
	"short": "short-bias", "shorts": "short-bias",
	"long":        "long-only",
	"mean-revert": "mean-reversion", "meanrev": "mean-reversion",
	"quant": "systematic", "algo": "systematic", "bot": "systematic",
	"hip-3":   "hip3",
	"indexes": "index", "indices": "index", "basket": "index",
	"degen": "high-risk", "risky": "high-risk",
	"safe": "low-drawdown",
	"hlp":  "protocol",
}

// NormalizeTag lowercases, trims, kebab-cases and resolves aliases.
func NormalizeTag(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	t = strings.TrimPrefix(t, "#")
	t = strings.Join(strings.FieldsFunc(t, func(r rune) bool { return r == ' ' || r == '_' }), "-")
	if a, ok := tagAliases[t]; ok {
		return a
	}
	return t
}

// KnownTag reports whether t (after NormalizeTag) is in the vocabulary.
func KnownTag(t string) bool { _, ok := vocab[NormalizeTag(t)]; return ok }

// TagMeta returns the vocabulary entry for t, or a GroupUser entry for tags
// that only user overrides use.
func TagMeta(t string) TagInfo {
	n := NormalizeTag(t)
	if ti, ok := vocab[n]; ok {
		return ti
	}
	return TagInfo{Tag: n, Group: GroupUser, Label: n}
}

// SuggestTags returns vocabulary tags (and tags present in r) that start
// with or contain prefix, for shell/slash completion. Sorted, prefix hits first.
func (r *Registry) SuggestTags(prefix string) []string {
	p := NormalizeTag(prefix)
	all := map[string]bool{}
	for _, t := range Vocabulary {
		all[t.Tag] = true
	}
	for _, e := range r.entries {
		for _, t := range e.Tags {
			all[t] = true
		}
	}
	var pre, sub []string
	for _, t := range sortedKeys(all) {
		switch {
		case strings.HasPrefix(t, p):
			pre = append(pre, t)
		case strings.Contains(t, p):
			sub = append(sub, t)
		}
	}
	// The most used tag first ("mark" completes to market-neutral, the
	// tag on 4 entries, before market-making on 1); ties stay alphabetical.
	uses := map[string]int{}
	for _, e := range r.entries {
		for _, t := range e.Tags {
			uses[t]++
		}
	}
	byUse := func(xs []string) {
		sort.SliceStable(xs, func(i, j int) bool { return uses[xs[i]] > uses[xs[j]] })
	}
	byUse(pre)
	byUse(sub)
	return append(pre, sub...)
}

// TagCount is a tag with the ids of entries carrying it.
type TagCount struct {
	TagInfo
	Count int      `json:"count"`
	IDs   []string `json:"ids"`
}

// TagCounts counts tags over entries passing f, most used first.
func (r *Registry) TagCounts(f Filter) []TagCount {
	m := map[string]*TagCount{}
	for _, e := range r.Filter(f) {
		for _, t := range e.Tags {
			tc, ok := m[t]
			if !ok {
				tc = &TagCount{TagInfo: TagMeta(t)}
				m[t] = tc
			}
			tc.Count++
			tc.IDs = append(tc.IDs, e.ID)
		}
	}
	out := make([]TagCount, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, *m[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}
