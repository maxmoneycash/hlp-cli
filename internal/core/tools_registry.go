package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/registry"
)

// CardDiscover renders `hlp vaults discover`. Data: *registry.Discovery.
const CardDiscover Card = "discover"

// DiscoverInput is the input of discover_vaults (`hlp vaults discover`).
type DiscoverInput struct {
	Tag    []string `json:"tag,omitempty" jsonschema:"only entries with every one of these tags, e.g. memes, trend, market-neutral"`
	Kind   string   `json:"kind,omitempty" jsonschema:"hypercore, hyperevm, index or strategy (vaults, evm, indexes, quant also work)"`
	Theses bool     `json:"theses,omitempty" jsonschema:"only the theme and style tags (the investment theses) with counts"`
}

// vaultRegistry returns the Env's curated registry (the embedded list when
// the Env was built by hand, as in tests).
func (e *Env) vaultRegistry() *registry.Registry {
	if e.Vaults == nil {
		e.Vaults = registry.MustDefault()
	}
	return e.Vaults
}

// registryFilter builds a registry filter from user tags and kind, with
// USAGE errors for unknown tags (with suggestions) and kinds.
func registryFilter(reg *registry.Registry, tags, exclude []string, kind string) (registry.Filter, error) {
	f := registry.Filter{Tags: splitList(tags), ExcludeTags: splitList(exclude)}
	if bad := reg.UnknownTags(append(append([]string{}, f.Tags...), f.ExcludeTags...)...); len(bad) > 0 {
		quoted := make([]string, len(bad))
		for i, b := range bad {
			quoted[i] = fmt.Sprintf("%q", b)
		}
		e := Errorf(CodeUsage, "unknown tag %s", strings.Join(quoted, ", "))
		var sug []string
		for _, b := range bad {
			// a typo ("memez") still finds its stem
			for q := strings.ToLower(b); len(q) >= 3 && len(sug) == 0; q = q[:len(q)-1] {
				sug = append(sug, reg.SuggestTags(q)...)
			}
		}
		if len(sug) > 0 {
			e.Hint = "did you mean " + strings.Join(firstN(sug, 5), ", ") + "? (`hlp vaults discover` lists every tag)"
		} else {
			e.Hint = "`hlp vaults discover` lists every tag"
		}
		return f, e
	}
	if strings.TrimSpace(kind) != "" {
		k, err := registry.ParseKind(kind)
		if err != nil {
			return f, &Error{Code: CodeUsage, Message: err.Error(), Hint: "kinds: hypercore, hyperevm, index, strategy"}
		}
		f.Kinds = []registry.Kind{k}
	}
	return f, nil
}

// splitList flattens comma lists ("memes,trend") and trims blanks.
func splitList(in []string) []string {
	var out []string
	for _, s := range in {
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func firstN(s []string, n int) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
		if len(out) == n {
			break
		}
	}
	return out
}

// tagVaults narrows the screener universe to registry entries matching the
// tags/kind of p. Indexes and strategies have no screener row: the caller
// gets a hint to use discover.
func tagVaults(env *Env, vs []domain.VaultSummary, p VaultsInput) ([]domain.VaultSummary, error) {
	reg := env.vaultRegistry()
	f, err := registryFilter(reg, p.Tags, p.ExcludeTags, p.Kind)
	if err != nil {
		return nil, err
	}
	if len(f.Kinds) == 1 && !f.Kinds[0].IsVault() {
		return nil, &Error{Code: CodeUsage, Message: fmt.Sprintf("%s entries are not vaults with a screener row", f.Kinds[0]),
			Hint: "list them with `hlp vaults discover --kind " + string(f.Kinds[0]) + "`"}
	}
	if p.IncludeHLP {
		f.IncludeChildren = true
	}
	return reg.FilterSummaries(vs, f), nil
}

func runDiscover(_ context.Context, env *Env, in any) (*Result, error) {
	p := in.(*DiscoverInput)
	reg := env.vaultRegistry()
	f, err := registryFilter(reg, p.Tag, nil, p.Kind)
	if err != nil {
		return nil, err
	}
	d := reg.Discover(f)
	if p.Theses {
		var gs []registry.DiscoverGroup
		for _, g := range d.Groups {
			if g.Thesis {
				gs = append(gs, g)
			}
		}
		d.Groups = gs
	}
	meta := env.meta("registry")
	meta.Warnings = append(meta.Warnings, env.VaultWarnings...)
	title := "Discover vaults"
	if len(f.Tags) > 0 {
		title += " · " + strings.Join(f.Tags, " + ")
	}
	return &Result{Card: CardDiscover, Title: title, Data: &d, Meta: meta}, nil
}

// resolveCurated looks a vault name up in the curated registry before the
// universe-wide fuzzy search. Only strong matches count (exact or prefix on
// id, slug, alias or name): it returns the address when exactly one curated
// HyperCore vault matches, a USAGE error pointing at the right command when
// the query is the id of an index, strategy or HyperEVM vault, and ("", nil)
// otherwise so the caller falls through to the fuzzy search.
func resolveCurated(env *Env, q string) (string, error) {
	var top []registry.Entry
	var tier registry.Tier
	for _, m := range env.vaultRegistry().Search(q, 0) {
		if m.Tier < registry.TierPrefix {
			break
		}
		if len(top) == 0 {
			tier = m.Tier
		}
		if m.Tier != tier {
			break
		}
		top = append(top, m.Entry)
	}
	var vaults []registry.Entry
	for _, e := range top {
		if e.Kind == registry.KindHyperCore {
			vaults = append(vaults, e)
		}
	}
	if len(vaults) == 1 {
		return vaults[0].Addr(), nil
	}
	if len(top) != 1 || tier != registry.TierExact || !strings.EqualFold(strings.TrimSpace(q), top[0].ID) {
		return "", nil // a word like "trend" may still name a HyperCore vault
	}
	e := top[0]
	switch e.Kind {
	case registry.KindHyperEVM:
		return "", &Error{Code: CodeUsage, Message: e.Name + " is a HyperEVM strategy vault", Hint: "use `hlp fund " + e.ID + "`"}
	case registry.KindIndex:
		return "", &Error{Code: CodeUsage, Message: e.Name + " is a vault index, not a vault", Hint: "use `hlp index " + e.Slug + "`"}
	case registry.KindStrategy:
		return "", &Error{Code: CodeUsage, Message: e.Name + " is an hlp.xyz quant strategy, not a vault", Hint: "use `hlp strat " + e.Slug + "`"}
	}
	return "", nil
}
