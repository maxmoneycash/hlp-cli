package registry

import (
	"errors"
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp/internal/domain"
)

const hlpAddr = "0xdfc24b077bc1425ad1dea75bcb6f8158e10df303"

func mustReg(t *testing.T) *Registry {
	t.Helper()
	r, err := Default()
	if err != nil {
		t.Fatalf("embedded registry: %v", err)
	}
	return r
}

func TestEmbeddedLoads(t *testing.T) {
	r := mustReg(t)
	if r.Len() != 39 {
		t.Fatalf("entries = %d, want 39", r.Len())
	}
	counts := map[Kind]int{}
	for _, e := range r.All() {
		counts[e.Kind]++
		if e.Origin != OriginEmbedded {
			t.Errorf("%s origin = %q", e.ID, e.Origin)
		}
		if e.Description == "" || e.SourceURL == "" || len(e.Tags) == 0 || len(e.SelectedFor) == 0 {
			t.Errorf("%s missing required field", e.ID)
		}
	}
	want := map[Kind]int{KindHyperCore: 28, KindHyperEVM: 1, KindIndex: 4, KindStrategy: 6}
	for k, n := range want {
		if counts[k] != n {
			t.Errorf("kind %s = %d, want %d", k, counts[k], n)
		}
	}
	if r.GeneratedAt == "" {
		t.Error("generated_at missing")
	}
}

func TestLookups(t *testing.T) {
	r := mustReg(t)
	e, ok := r.ByAddress(strings.ToUpper(hlpAddr[:2]) + hlpAddr[2:])
	if !ok || e.ID != "hlp" {
		t.Fatalf("ByAddress(HLP) = %v %v", e.ID, ok)
	}
	// EIP-55 address of the pvault compares lowercased
	if e, ok := r.ByAddress("0x688dd654bdbde2bf8b6154685437f3f9db2eaabd"); !ok || e.Kind != KindHyperEVM {
		t.Fatalf("pvault by lowercase address = %v %v", e.ID, ok)
	}
	if e, ok := r.BySlug(KindStrategy, "spxmeme"); !ok || e.ID != "quant-spxmeme" {
		t.Fatalf("BySlug strategy = %v %v", e.ID, ok)
	}
	if e, ok := r.BySlug("", "mavericks"); !ok || e.Kind != KindIndex {
		t.Fatalf("BySlug bare = %v %v", e.ID, ok)
	}
	if kids := r.Children(hlpAddr); len(kids) != 7 {
		t.Fatalf("HLP children = %d, want 7", len(kids))
	}
	if vk, ok := KindIndex.VaultKind(); ok || vk != "" {
		t.Error("index must not map to a domain vault kind")
	}
	// returned entries must not alias internal state
	e, _ = r.ByID("hlp")
	e.Tags[0] = "mutated"
	if e2, _ := r.ByID("hlp"); e2.Tags[0] == "mutated" {
		t.Error("ByID leaked internal slice")
	}
}

func TestTagsAndFilter(t *testing.T) {
	r := mustReg(t)
	memes := r.WithTag("memes")
	ids := entryIDs(memes)
	for _, want := range []string{"quant-spxmeme", "quant-bluechip", "quant-memeshort"} {
		if !strings.Contains(ids, want) {
			t.Errorf("--tag memes missing %s (got %s)", want, ids)
		}
	}
	if got := entryIDs(r.WithTag("meme")); got != ids {
		t.Errorf("alias meme -> %s, want %s", got, ids)
	}
	if got := entryIDs(r.WithTag("#Memecoins")); got != ids {
		t.Errorf("alias #Memecoins -> %s", got)
	}
	// AND semantics
	for _, e := range r.WithTag("memes", "hip3") {
		if !e.HasTag("hip3") || !e.HasTag("memes") {
			t.Errorf("%s fails AND", e.ID)
		}
	}
	// defaults hide children, archived and testnet
	def := r.Filter(Filter{})
	for _, e := range def {
		if e.Relationship == "child" || e.Status == "archived" || e.Testnet() {
			t.Errorf("default filter kept %s", e.ID)
		}
	}
	if n := len(r.WithTag("hlp-child")); n != 7 {
		t.Errorf("--tag hlp-child = %d, want 7 (asking for the tag lifts the child filter)", n)
	}
	if n := len(r.WithTag("testnet")); n != 1 {
		t.Errorf("--tag testnet = %d, want 1", n)
	}
	idx := r.Filter(Filter{Kinds: []Kind{KindIndex}, IncludeArchived: true})
	if len(idx) != 4 {
		t.Errorf("indexes incl archived = %d", len(idx))
	}
	if n := len(r.Filter(Filter{Kinds: []Kind{KindIndex}})); n != 2 {
		t.Errorf("active indexes = %d, want 2", n)
	}
	if u := r.UnknownTags("memes", "meme", "lambo"); len(u) != 1 || u[0] != "lambo" {
		t.Errorf("UnknownTags = %v", u)
	}
	if k, err := ParseKind("quant"); err != nil || k != KindStrategy {
		t.Errorf("ParseKind quant = %v %v", k, err)
	}
	if _, err := ParseKind("nft"); err == nil {
		t.Error("ParseKind nft should fail")
	}
	if s := r.SuggestTags("me"); len(s) == 0 || s[0] != "mean-reversion" && s[0] != "memes" {
		t.Errorf("SuggestTags(me) = %v", s)
	}
}

func TestVocabularyClosed(t *testing.T) {
	seen := map[string]bool{}
	for _, ti := range Vocabulary {
		if seen[ti.Tag] {
			t.Errorf("duplicate tag %s", ti.Tag)
		}
		seen[ti.Tag] = true
	}
	for a, to := range tagAliases {
		if !seen[to] {
			t.Errorf("alias %s -> %s targets an unknown tag", a, to)
		}
		if seen[a] {
			t.Errorf("alias %s shadows a real tag", a)
		}
	}
	bad := strings.Replace(string(Embedded()), `tags = ["memes", "short-bias"]`, `tags = ["memes", "lambo"]`, 1)
	if _, err := Load([]byte(bad)); err == nil || !strings.Contains(err.Error(), "lambo") {
		t.Errorf("unknown tag accepted: %v", err)
	}
}

func TestLoadRejects(t *testing.T) {
	head := "schema_version = 1\n"
	cases := map[string]string{
		"schema":    "schema_version = 2\n",
		"unknown":   head + "[[vault]]\nid='a'\nname='A'\nkind='hypercore'\naddress='0x" + strings.Repeat("a", 40) + "'\ntags=['trend']\nselected_for=['tvl']\nbogus=1\n",
		"noaddr":    head + "[[vault]]\nid='a'\nname='A'\nkind='hypercore'\ntags=['trend']\nselected_for=['tvl']\n",
		"badkind":   head + "[[vault]]\nid='a'\nname='A'\nkind='nft'\ntags=['trend']\nselected_for=['tvl']\n",
		"duplicate": head + strings.Repeat("[[vault]]\nid='a'\nname='A'\nkind='index'\nslug='a'\ntags=['index']\nselected_for=['index']\n", 2),
	}
	for name, doc := range cases {
		if _, err := Load([]byte(doc)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestResolve(t *testing.T) {
	r := mustReg(t)
	ok := map[string]string{
		"hlp":                      "hlp",
		"HLP":                      "hlp",
		"Hyperliquidity Provider":  "hlp",
		hlpAddr:                    "hlp",
		"0xdfc24b":                 "hlp",
		"spxmeme":                  "quant-spxmeme",
		"mavericks":                "idx-mavericks",
		"Long HYPE & BTC":          "", // several: the vault and the strategy; checked below
		"trend pyramid":            "trend-pyramid",
		"69 jump":                  "69-jump-street",
		"0x688DD654BDBDE2BF8B6154": "trend-pyramid",
	}
	for q, want := range ok {
		if want == "" {
			continue
		}
		e, err := r.Resolve(q)
		if err != nil || e.ID != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", q, e.ID, err, want)
		}
	}
	// ambiguous: four liquidators
	_, err := r.Resolve("liquidator")
	var amb *AmbiguousError
	if !errors.As(err, &amb) || len(amb.Candidates) < 4 {
		t.Fatalf("Resolve(liquidator) = %v", err)
	}
	// "Long HYPE & BTC" prefixes both the real vault and the quant model:
	// the CLI must ask, and the real vault ranks first.
	_, err = r.Resolve("Long HYPE & BTC")
	if !errors.As(err, &amb) || !amb.Candidates[0].Entry.Kind.IsVault() {
		t.Fatalf("Resolve(Long HYPE & BTC) = %v", err)
	}
	var nf *NotFoundError
	if _, err := r.Resolve("zzzzqqqq"); !errors.As(err, &nf) {
		t.Fatalf("Resolve(nonsense) = %v", err)
	}
	// a tag alone never resolves: "hip3" is a tag on three entries but in no name
	if _, err := r.Resolve("hip3"); !errors.As(err, &nf) {
		t.Errorf("Resolve(tag hip3) = %v, want not found", err)
	}
	// Search includes tag hits at the lowest tier
	ms := r.Search("hip3", 0)
	if len(ms) == 0 || ms[len(ms)-1].Field != "tag" {
		t.Errorf("Search(hip3) tag hits = %+v", ms)
	}
	if ms := r.Search("hlp strat", 2); len(ms) != 2 {
		t.Errorf("Search limit = %d", len(ms))
	}
}

func universeFixture() []domain.VaultSummary {
	return []domain.VaultSummary{
		{Address: strings.ToUpper(hlpAddr[:4]) + hlpAddr[4:], Name: "Hyperliquidity Provider (HLP)", TVL: 151e6, Curated: true, Sources: []string{"stats-data", "hlp.xyz"}},
		{Address: "0x" + strings.Repeat("1", 40), Name: "Random vault", TVL: 5e4, Sources: []string{"stats-data"}},
		{Address: "0x9d4e2b1ccb8eac5d0ea4b4e2a3a8a1a6c6b2a1aa", Name: "Unrelated", TVL: 1e6},
	}
}

func TestJoinAndFilterSummaries(t *testing.T) {
	r := mustReg(t)
	u := universeFixture()
	rows := r.Join(u, Filter{Tags: []string{"flagship"}})
	if len(rows) != 1 || rows[0].Live == nil || rows[0].Live.TVL != 151e6 || !rows[0].Tracked {
		t.Fatalf("Join flagship = %+v", rows)
	}
	rows = r.Join(nil, Filter{Kinds: []Kind{KindStrategy}})
	if len(rows) != 6 || rows[0].Live != nil {
		t.Fatalf("Join strategies offline = %d", len(rows))
	}
	sums := r.FilterSummaries(u, Filter{Tags: []string{"protocol"}})
	// HLP (live) only: children hidden, pvault is testnet, strategies are not vaults
	if len(sums) != 1 || sums[0].TVL != 151e6 {
		t.Fatalf("FilterSummaries protocol = %+v", sums)
	}
	sums = r.FilterSummaries(u, Filter{Tags: []string{"ai"}})
	for _, s := range sums {
		if s.TVL != 0 || len(s.Sources) != 1 || s.Sources[0] != "registry" {
			t.Errorf("stub for %s carries numbers: %+v", s.Name, s)
		}
	}
	ann := r.Annotate(u)
	if len(ann) != 3 || ann[0] == nil || ann[0].ID != "hlp" || ann[1] != nil {
		t.Fatalf("Annotate = %v", ann)
	}
	if tags := r.TagsByAddress()[hlpAddr]; len(tags) == 0 {
		t.Error("TagsByAddress missing HLP")
	}
	if un := r.Untracked(u[:1]); len(un) != 27 {
		t.Errorf("Untracked = %d, want 27", len(un))
	}
}

func TestDiscover(t *testing.T) {
	r := mustReg(t)
	d := r.Discover(Filter{})
	if d.Total == 0 || len(d.Kinds) == 0 || len(d.Groups) == 0 {
		t.Fatalf("Discover = %+v", d)
	}
	if d.Groups[0].Group != GroupTheme || !d.Groups[0].Thesis {
		t.Errorf("first group = %s", d.Groups[0].Group)
	}
	var memes *TagCount
	for i, tc := range d.Groups[0].Tags {
		if tc.Tag == "memes" {
			memes = &d.Groups[0].Tags[i]
		}
	}
	if memes == nil || memes.Count < 3 || memes.Label != "Memecoins" {
		t.Errorf("memes count = %+v", memes)
	}
	for _, g := range d.Groups {
		for i := 1; i < len(g.Tags); i++ {
			if g.Tags[i].Count > g.Tags[i-1].Count {
				t.Errorf("group %s not sorted by count", g.Group)
			}
		}
	}
	th := r.Theses(Filter{})
	for _, tc := range th {
		if tc.Group != GroupTheme && tc.Group != GroupStyle {
			t.Errorf("thesis %s in group %s", tc.Tag, tc.Group)
		}
	}
	if sum := 0; true {
		for _, k := range d.Kinds {
			sum += k.Count
		}
		if sum != d.Total {
			t.Errorf("kind counts %d != total %d", sum, d.Total)
		}
	}
}

func entryIDs(es []Entry) string {
	ids := make([]string, len(es))
	for i, e := range es {
		ids[i] = e.ID
	}
	return strings.Join(ids, ",")
}
