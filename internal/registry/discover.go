package registry

// KindCount is one kind with the entries of that kind.
type KindCount struct {
	Kind  Kind     `json:"kind"`
	Title string   `json:"title"`
	Count int      `json:"count"`
	IDs   []string `json:"ids"`
}

// DiscoverGroup is one tag group with its tags, most used first.
type DiscoverGroup struct {
	Group  TagGroup   `json:"group"`
	Title  string     `json:"title"`
	Thesis bool       `json:"thesis"` // theme or style: what `discover` leads with
	Tags   []TagCount `json:"tags"`
}

// Discovery is the data behind `hlp vaults discover`: what kinds exist and
// which theses (tags) the curated set covers. Entries holds the filtered
// entries so a card can show names without another lookup.
type Discovery struct {
	GeneratedAt string          `json:"generatedAt"`
	Total       int             `json:"total"`
	Kinds       []KindCount     `json:"kinds"`
	Groups      []DiscoverGroup `json:"groups"`
	Entries     []Entry         `json:"entries"`
	// Hidden counts entries that pass the filter's tags and kinds but are
	// left out by default (testnet, HLP children, archived indexes).
	Hidden Hidden `json:"hidden"`
	// Tags is the filter's tags (normalized), for hints.
	Tags []string `json:"tags,omitempty"`
}

// Hidden is the number of entries a default filter leaves out, by reason.
type Hidden struct {
	Testnet  int `json:"testnet"`
	Children int `json:"children"`
	Archived int `json:"archived"`
}

// Discover summarizes the entries passing f by kind and by tag group.
// Groups follow TagGroups order and empty groups are dropped.
func (r *Registry) Discover(f Filter) Discovery {
	es := r.Filter(f)
	d := Discovery{GeneratedAt: r.GeneratedAt, Total: len(es), Entries: es}
	for _, t := range f.Tags {
		d.Tags = append(d.Tags, NormalizeTag(t))
	}
	wide := f
	wide.IncludeTestnet, wide.IncludeChildren, wide.IncludeArchived = true, true, true
	for _, e := range r.Filter(wide) {
		if f.Match(e) {
			continue
		}
		switch {
		case e.Testnet():
			d.Hidden.Testnet++
		case e.Relationship == "child":
			d.Hidden.Children++
		case e.Status == "archived":
			d.Hidden.Archived++
		}
	}
	for _, k := range Kinds {
		kc := KindCount{Kind: k, Title: KindTitle(k)}
		for _, e := range es {
			if e.Kind == k {
				kc.Count++
				kc.IDs = append(kc.IDs, e.ID)
			}
		}
		if kc.Count > 0 {
			d.Kinds = append(d.Kinds, kc)
		}
	}
	counts := r.TagCounts(f)
	thesis := map[TagGroup]bool{}
	for _, g := range ThesisGroups {
		thesis[g] = true
	}
	for _, g := range TagGroups {
		dg := DiscoverGroup{Group: g, Title: GroupTitle(g), Thesis: thesis[g]}
		for _, tc := range counts {
			if tc.Group == g {
				dg.Tags = append(dg.Tags, tc)
			}
		}
		if len(dg.Tags) > 0 {
			d.Groups = append(d.Groups, dg)
		}
	}
	return d
}

// Theses returns only the theme and style tags with counts, most used first:
// the short list for a TUI chip row or `hlp vaults discover --theses`.
func (r *Registry) Theses(f Filter) []TagCount {
	var out []TagCount
	for _, tc := range r.TagCounts(f) {
		for _, g := range ThesisGroups {
			if tc.Group == g {
				out = append(out, tc)
			}
		}
	}
	return out
}
