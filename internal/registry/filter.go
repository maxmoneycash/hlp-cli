package registry

import (
	"fmt"
	"strings"
)

// Filter selects entries. Zero value = every entry except HLP child vaults,
// archived indexes and testnet entries.
type Filter struct {
	Tags            []string // all must match (AND); aliases are resolved
	AnyTags         []string // at least one must match (OR)
	ExcludeTags     []string
	Kinds           []Kind   // empty = all kinds
	SelectedFor     []string // any of
	VaultsOnly      bool     // only kinds with an address (hypercore, hyperevm)
	IncludeChildren bool     // include HLP child vaults
	IncludeArchived bool     // include archived indexes
	IncludeTestnet  bool     // include testnet entries (Trend Pyramid)
}

// ParseKind parses a user-typed kind ("vaults", "evm", "indexes", "quant"...).
func ParseKind(s string) (Kind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "hypercore", "core", "vault", "vaults", "hl":
		return KindHyperCore, nil
	case "hyperevm", "evm", "pvault", "pvaults", "strategy-vault", "strategy-vaults":
		return KindHyperEVM, nil
	case "index", "indexes", "indices", "idx", "basket", "baskets":
		return KindIndex, nil
	case "strategy", "strategies", "quant", "quants", "paper":
		return KindStrategy, nil
	}
	return "", fmt.Errorf("kind must be hypercore, hyperevm, index or strategy, got %q", s)
}

// UnknownTags returns the tags in ts that neither the vocabulary nor any
// entry in r knows, so the CLI can fail with a usage error and suggestions.
func (r *Registry) UnknownTags(ts ...string) []string {
	present := map[string]bool{}
	for _, e := range r.entries {
		for _, t := range e.Tags {
			present[t] = true
		}
	}
	var out []string
	for _, t := range ts {
		n := NormalizeTag(t)
		if !KnownTag(n) && !present[n] {
			out = append(out, t)
		}
	}
	return out
}

// Match reports whether e passes f.
func (f Filter) Match(e Entry) bool {
	if e.Relationship == "child" && !f.IncludeChildren && !f.asksFor("hlp-child") {
		return false
	}
	if e.Status == "archived" && !f.IncludeArchived {
		return false
	}
	if e.Testnet() && !f.IncludeTestnet && !f.asksFor("testnet") {
		return false
	}
	if f.VaultsOnly && !e.Kind.IsVault() {
		return false
	}
	if len(f.Kinds) > 0 {
		ok := false
		for _, k := range f.Kinds {
			ok = ok || e.Kind == k
		}
		if !ok {
			return false
		}
	}
	for _, t := range f.Tags {
		if !e.HasTag(t) {
			return false
		}
	}
	if len(f.AnyTags) > 0 {
		ok := false
		for _, t := range f.AnyTags {
			ok = ok || e.HasTag(t)
		}
		if !ok {
			return false
		}
	}
	for _, t := range f.ExcludeTags {
		if e.HasTag(t) {
			return false
		}
	}
	if len(f.SelectedFor) > 0 {
		ok := false
		for _, s := range f.SelectedFor {
			for _, x := range e.SelectedFor {
				ok = ok || strings.EqualFold(s, x)
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func (f Filter) asksFor(tag string) bool {
	for _, t := range f.Tags {
		if NormalizeTag(t) == tag {
			return true
		}
	}
	for _, t := range f.AnyTags {
		if NormalizeTag(t) == tag {
			return true
		}
	}
	return false
}

// Filter returns the entries passing f, in file order.
func (r *Registry) Filter(f Filter) []Entry {
	var out []Entry
	for _, e := range r.entries {
		if f.Match(e) {
			out = append(out, cloneEntry(e))
		}
	}
	return out
}

// WithTag is the common case behind `hlp vaults --tag memes`: every entry
// carrying all the given tags, any kind.
func (r *Registry) WithTag(tags ...string) []Entry { return r.Filter(Filter{Tags: tags}) }
