package registry

import (
	"strings"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// Row is a registry entry joined with live data from the vault universe
// (stats-data merged with hlp.xyz's tracked list, i.e. core.MergeVaults
// output). Live is nil for indexes and strategies, and for vaults missing
// from the universe (the HyperEVM pvault, or a source being down).
type Row struct {
	Entry   Entry                `json:"entry"`
	Live    *domain.VaultSummary `json:"live,omitempty"`
	Tracked bool                 `json:"tracked"` // hlp.xyz tracks it (Live.Curated)
}

// Join returns the entries passing f joined with their live rows, in file
// order. universe may be nil (offline); rows then have Live == nil.
func (r *Registry) Join(universe []domain.VaultSummary, f Filter) []Row {
	byAddr := indexUniverse(universe)
	var out []Row
	for _, e := range r.entries {
		if !f.Match(e) {
			continue
		}
		row := Row{Entry: cloneEntry(e)}
		if a := e.Addr(); a != "" {
			if i, ok := byAddr[a]; ok {
				v := universe[i]
				row.Live = &v
				row.Tracked = v.Curated
			}
		}
		out = append(out, row)
	}
	return out
}

// FilterSummaries narrows screener rows to vaults whose registry entry passes
// f; this is `hlp vaults --tag memes` over the live universe. Entries with
// an address that the universe lacks are appended as Stub rows so they still
// show (TVL 0, Sources ["registry"]). Indexes and strategies are skipped:
// list them with Join or Filter.
func (r *Registry) FilterSummaries(universe []domain.VaultSummary, f Filter) []domain.VaultSummary {
	f.VaultsOnly = true
	byAddr := indexUniverse(universe)
	var out []domain.VaultSummary
	for _, e := range r.entries {
		if !f.Match(e) {
			continue
		}
		if i, ok := byAddr[e.Addr()]; ok {
			out = append(out, universe[i])
			continue
		}
		out = append(out, Stub(e))
	}
	return out
}

// Stub is a minimal screener row for a registry vault with no live data.
// Numbers are zero (unknown), never the snapshot.
func Stub(e Entry) domain.VaultSummary {
	k, _ := e.Kind.VaultKind()
	return domain.VaultSummary{
		Address: e.Addr(),
		Name:    e.Name,
		Leader:  strings.ToLower(e.Leader),
		Kind:    k,
		Rel:     e.Relationship,
		Sources: []string{"registry"},
	}
}

// Annotate returns, for each universe row, the registry entry that matches
// its address (nil when uncurated). Same length and order as universe, so a
// screener can render a tags column next to its rows.
func (r *Registry) Annotate(universe []domain.VaultSummary) []*Entry {
	out := make([]*Entry, len(universe))
	for i, v := range universe {
		if j, ok := r.byAddr[strings.ToLower(v.Address)]; ok {
			e := cloneEntry(r.entries[j])
			out[i] = &e
		}
	}
	return out
}

// TagsByAddress maps every registry vault address (lowercase) to its tags.
func (r *Registry) TagsByAddress() map[string][]string {
	m := make(map[string][]string, len(r.byAddr))
	for a, i := range r.byAddr {
		m[a] = append([]string(nil), r.entries[i].Tags...)
	}
	return m
}

// Untracked returns registry vaults that are absent from hlp.xyz's tracked
// list (xyz = hlpxyz.Client.VaultList rows). These need Hyperliquid's own
// vaultDetails portfolio (SPHL) or hlp.xyz /live for numbers.
func (r *Registry) Untracked(xyz []domain.VaultSummary) []Entry {
	tracked := indexUniverse(xyz)
	var out []Entry
	for _, e := range r.entries {
		if e.Kind != KindHyperCore {
			continue
		}
		if _, ok := tracked[e.Addr()]; !ok {
			out = append(out, cloneEntry(e))
		}
	}
	return out
}

func indexUniverse(vs []domain.VaultSummary) map[string]int {
	m := make(map[string]int, len(vs))
	for i, v := range vs {
		m[strings.ToLower(v.Address)] = i
	}
	return m
}
