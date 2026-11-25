package registry

import (
	"bytes"
	_ "embed"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

//go:embed vaults.toml
var embeddedTOML []byte

// SchemaVersion is the vaults.toml schema this package reads.
const SchemaVersion = 1

// Kind is what a registry entry is. Only KindHyperCore and KindHyperEVM are
// real vaults with an address; indexes and strategies are hlp.xyz baskets and
// paper books keyed by Slug.
type Kind string

const (
	KindHyperCore Kind = "hypercore" // legacy HyperCore vault
	KindHyperEVM  Kind = "hyperevm"  // pvault / ERC-7540 contract on HyperEVM
	KindIndex     Kind = "index"     // hlp.xyz basket of vaults (no pooled address)
	KindStrategy  Kind = "strategy"  // hlp.xyz quant strategy (paper/backtest)
)

// Kinds lists the kinds in display order.
var Kinds = []Kind{KindHyperCore, KindHyperEVM, KindIndex, KindStrategy}

// KindTitle is the human label for a kind.
func KindTitle(k Kind) string {
	switch k {
	case KindHyperCore:
		return "HyperCore vaults"
	case KindHyperEVM:
		return "HyperEVM strategy vaults"
	case KindIndex:
		return "Indexes"
	case KindStrategy:
		return "Quant strategies"
	}
	return string(k)
}

// VaultKind maps a registry kind onto domain.VaultKind. ok is false for
// indexes and strategies, which are not vaults.
func (k Kind) VaultKind() (domain.VaultKind, bool) {
	switch k {
	case KindHyperCore:
		return domain.VaultHyperCore, true
	case KindHyperEVM:
		return domain.VaultHyperEVM, true
	}
	return "", false
}

// IsVault reports whether entries of this kind have a vault address.
func (k Kind) IsVault() bool { _, ok := k.VaultKind(); return ok }

// Origin says where an entry came from.
const (
	OriginEmbedded = "embedded"
	OriginUser     = "user"
)

// Snapshot is the point-in-time evidence for picking an entry. NEVER display
// it as current data. Percent fields are percents (10.37 = 10.37%), unlike
// domain fractions. Which fields are set depends on the kind.
type Snapshot struct {
	TVLUSD      float64 `toml:"tvl_usd" json:"tvlUsd,omitempty"`
	TVLSource   string  `toml:"tvl_source" json:"tvlSource,omitempty"`
	Ret1YPct    float64 `toml:"ret_1y_pct" json:"ret1yPct,omitempty"`
	Ret3MPct    float64 `toml:"ret_3m_pct" json:"ret3mPct,omitempty"`
	RetAllPct   float64 `toml:"ret_all_pct" json:"retAllPct,omitempty"`
	Sharpe1Y    float64 `toml:"sharpe_1y" json:"sharpe1y,omitempty"`
	MaxDDPct    float64 `toml:"max_dd_pct" json:"maxDDPct,omitempty"`
	AccuracyPct float64 `toml:"accuracy_pct" json:"accuracyPct,omitempty"`
	Model       string  `toml:"model" json:"model,omitempty"`

	// strategy
	OOSStart     string  `toml:"oos_start" json:"oosStart,omitempty"`
	OOSCAGRPct   float64 `toml:"oos_cagr_pct" json:"oosCagrPct,omitempty"`
	OOSSharpe    float64 `toml:"oos_sharpe" json:"oosSharpe,omitempty"`
	OOSMaxDDPct  float64 `toml:"oos_max_dd_pct" json:"oosMaxDDPct,omitempty"`
	Liquidations int     `toml:"liquidations" json:"liquidations,omitempty"`

	// hyperevm
	NAVUSD     float64 `toml:"nav_usd" json:"navUsd,omitempty"`
	SharePrice float64 `toml:"share_price" json:"sharePrice,omitempty"`
	Phase      string  `toml:"phase" json:"phase,omitempty"`
}

// Entry is one curated vault, index or strategy.
type Entry struct {
	ID           string    `toml:"id" json:"id"`
	Name         string    `toml:"name" json:"name"`
	Kind         Kind      `toml:"kind" json:"kind"`
	Address      string    `toml:"address" json:"address,omitempty"` // as written; compare with Addr()
	Slug         string    `toml:"slug" json:"slug,omitempty"`
	Chain        string    `toml:"chain" json:"chain"`
	ChainID      int       `toml:"chain_id" json:"chainId,omitempty"`
	Relationship string    `toml:"relationship" json:"relationship,omitempty"` // parent | child | normal
	Parent       string    `toml:"parent" json:"parent,omitempty"`
	Leader       string    `toml:"leader" json:"leader,omitempty"`
	Status       string    `toml:"status" json:"status,omitempty"` // active | archived | backtest
	Created      string    `toml:"created" json:"created,omitempty"`
	Description  string    `toml:"description" json:"description"`
	Tags         []string  `toml:"tags" json:"tags"`
	SelectedFor  []string  `toml:"selected_for" json:"selectedFor"`
	SourceURL    string    `toml:"source_url" json:"sourceUrl"`
	AnalyticsURL string    `toml:"analytics_url" json:"analyticsUrl,omitempty"`
	Lens         string    `toml:"lens" json:"lens,omitempty"`
	Notes        string    `toml:"notes" json:"notes,omitempty"`
	Aliases      []string  `toml:"aliases" json:"aliases,omitempty"`
	Snapshot     *Snapshot `toml:"snapshot" json:"snapshot,omitempty"`

	Origin string `toml:"-" json:"origin"` // OriginEmbedded | OriginUser
}

// Addr is the lowercased address ("" for indexes and strategies).
func (e Entry) Addr() string { return strings.ToLower(e.Address) }

// HasTag reports whether the entry carries tag (normalized, aliases applied).
func (e Entry) HasTag(tag string) bool {
	t := NormalizeTag(tag)
	for _, x := range e.Tags {
		if x == t {
			return true
		}
	}
	return false
}

// Key is how the CLI should refer to the entry: address for vaults, slug
// otherwise.
func (e Entry) Key() string {
	if e.Address != "" {
		return e.Addr()
	}
	return e.Slug
}

// Testnet reports whether the entry lives on a testnet chain.
func (e Entry) Testnet() bool { return strings.Contains(e.Chain, "testnet") }

// file is the on-disk shape of vaults.toml.
type file struct {
	SchemaVersion int     `toml:"schema_version"`
	GeneratedAt   string  `toml:"generated_at"`
	SnapshotModel string  `toml:"snapshot_model"`
	Vault         []Entry `toml:"vault"`
}

// Registry is an immutable, validated set of entries. Safe for concurrent use.
type Registry struct {
	GeneratedAt string
	entries     []Entry
	byID        map[string]int
	byAddr      map[string]int
	bySlug      map[string]int // "<kind>:<slug>" and bare slug when unique
}

// Load parses and validates a vaults.toml document. Strict: unknown keys,
// unknown tags and duplicate ids or addresses are errors.
func Load(data []byte) (*Registry, error) {
	var f file
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	if f.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("registry: schema_version %d, want %d", f.SchemaVersion, SchemaVersion)
	}
	for i := range f.Vault {
		f.Vault[i].Origin = OriginEmbedded
		normalizeEntry(&f.Vault[i])
		if err := validate(f.Vault[i], true); err != nil {
			return nil, fmt.Errorf("registry: vault %d (%s): %w", i+1, f.Vault[i].ID, err)
		}
	}
	return build(f.GeneratedAt, f.Vault)
}

var (
	defaultOnce sync.Once
	defaultReg  *Registry
	defaultErr  error
)

// Default returns the embedded registry. The embedded file is validated by
// tests, so an error here is a build defect.
func Default() (*Registry, error) {
	defaultOnce.Do(func() { defaultReg, defaultErr = Load(embeddedTOML) })
	return defaultReg, defaultErr
}

// MustDefault is Default that panics on a broken embedded file.
func MustDefault() *Registry {
	r, err := Default()
	if err != nil {
		panic(err)
	}
	return r
}

// Embedded returns the raw embedded vaults.toml (for `hlp vaults registry --raw`
// or for writing a starter override file).
func Embedded() []byte { return append([]byte(nil), embeddedTOML...) }

func build(generatedAt string, es []Entry) (*Registry, error) {
	r := &Registry{
		GeneratedAt: generatedAt,
		entries:     es,
		byID:        make(map[string]int, len(es)),
		byAddr:      make(map[string]int, len(es)),
		bySlug:      make(map[string]int, len(es)),
	}
	slugCount := map[string]int{}
	for i, e := range es {
		if _, dup := r.byID[e.ID]; dup {
			return nil, fmt.Errorf("registry: duplicate id %q", e.ID)
		}
		r.byID[e.ID] = i
		if a := e.Addr(); a != "" {
			if j, dup := r.byAddr[a]; dup {
				return nil, fmt.Errorf("registry: %q and %q share address %s", es[j].ID, e.ID, a)
			}
			r.byAddr[a] = i
		}
		if e.Slug != "" {
			r.bySlug[string(e.Kind)+":"+e.Slug] = i
			slugCount[e.Slug]++
		}
	}
	for i, e := range es {
		if e.Slug != "" && slugCount[e.Slug] == 1 {
			r.bySlug[e.Slug] = i
		}
	}
	return r, nil
}

// Len is the number of entries.
func (r *Registry) Len() int { return len(r.entries) }

// All returns a copy of every entry in file order.
func (r *Registry) All() []Entry { return cloneEntries(r.entries) }

// ByID looks an entry up by its id.
func (r *Registry) ByID(id string) (Entry, bool) {
	i, ok := r.byID[strings.ToLower(strings.TrimSpace(id))]
	if !ok {
		return Entry{}, false
	}
	return cloneEntry(r.entries[i]), true
}

// ByAddress looks a vault up by address (any case).
func (r *Registry) ByAddress(addr string) (Entry, bool) {
	i, ok := r.byAddr[strings.ToLower(strings.TrimSpace(addr))]
	if !ok {
		return Entry{}, false
	}
	return cloneEntry(r.entries[i]), true
}

// BySlug looks an index or strategy up by its hlp.xyz slug. kind may be ""
// when the slug is unique across kinds.
func (r *Registry) BySlug(kind Kind, slug string) (Entry, bool) {
	k := strings.ToLower(strings.TrimSpace(slug))
	if kind != "" {
		k = string(kind) + ":" + k
	}
	i, ok := r.bySlug[k]
	if !ok {
		return Entry{}, false
	}
	return cloneEntry(r.entries[i]), true
}

// Children returns HLP's child vaults (or any parent's) from the registry.
// The live list from vaultDetails(HLP) is authoritative; this is a fallback.
func (r *Registry) Children(parentAddr string) []Entry {
	p := strings.ToLower(parentAddr)
	var out []Entry
	for _, e := range r.entries {
		if strings.ToLower(e.Parent) == p {
			out = append(out, cloneEntry(e))
		}
	}
	return out
}

// ---- validation ----

var (
	idRe   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	addrRe = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	tagRe  = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

func normalizeEntry(e *Entry) {
	e.ID = strings.ToLower(strings.TrimSpace(e.ID))
	e.Name = strings.TrimSpace(e.Name)
	e.Slug = strings.ToLower(strings.TrimSpace(e.Slug))
	e.Address = strings.TrimSpace(e.Address)
	e.Tags = normalizeTags(e.Tags)
	for i, a := range e.Aliases {
		e.Aliases[i] = strings.ToLower(strings.TrimSpace(a))
	}
}

func normalizeTags(ts []string) []string {
	out := make([]string, 0, len(ts))
	seen := map[string]bool{}
	for _, t := range ts {
		n := NormalizeTag(t)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// validate checks one entry. strictTags requires tags from the closed
// vocabulary (embedded file); user entries may carry any kebab-case tag.
func validate(e Entry, strictTags bool) error {
	if !idRe.MatchString(e.ID) {
		return fmt.Errorf("id %q must be kebab-case", e.ID)
	}
	if e.Name == "" {
		return fmt.Errorf("name is required")
	}
	switch e.Kind {
	case KindHyperCore, KindHyperEVM:
		if !addrRe.MatchString(e.Address) {
			return fmt.Errorf("kind %s needs a 0x address, got %q", e.Kind, e.Address)
		}
	case KindIndex, KindStrategy:
		if e.Slug == "" {
			return fmt.Errorf("kind %s needs a slug", e.Kind)
		}
		if e.Address != "" {
			return fmt.Errorf("kind %s has no address", e.Kind)
		}
	default:
		return fmt.Errorf("kind %q must be one of hypercore, hyperevm, index, strategy", e.Kind)
	}
	switch e.Relationship {
	case "", "parent", "child", "normal":
	default:
		return fmt.Errorf("relationship %q must be parent, child or normal", e.Relationship)
	}
	if e.Relationship == "child" && !addrRe.MatchString(e.Parent) {
		return fmt.Errorf("child vault needs a parent address")
	}
	switch e.Status {
	case "", "active", "archived", "backtest":
	default:
		return fmt.Errorf("status %q must be active, archived or backtest", e.Status)
	}
	if e.Leader != "" && !addrRe.MatchString(e.Leader) {
		return fmt.Errorf("leader %q is not an address", e.Leader)
	}
	for _, t := range e.Tags {
		if !tagRe.MatchString(t) {
			return fmt.Errorf("tag %q must be kebab-case", t)
		}
		if strictTags && !KnownTag(t) {
			return fmt.Errorf("tag %q is not in the vocabulary (registry.Vocabulary)", t)
		}
	}
	for _, s := range e.SelectedFor {
		if strictTags && !validSelectedFor[s] {
			return fmt.Errorf("selected_for %q is unknown", s)
		}
	}
	return nil
}

var validSelectedFor = map[string]bool{
	"hlp": true, "tvl": true, "return": true, "sharpe": true, "index": true, "strategy": true, "protocol": true, "user": true,
}

// ---- copies (entries are handed out by value; slices must not alias) ----

func cloneEntry(e Entry) Entry {
	e.Tags = append([]string(nil), e.Tags...)
	e.SelectedFor = append([]string(nil), e.SelectedFor...)
	e.Aliases = append([]string(nil), e.Aliases...)
	if e.Snapshot != nil {
		s := *e.Snapshot
		e.Snapshot = &s
	}
	return e
}

func cloneEntries(es []Entry) []Entry {
	out := make([]Entry, len(es))
	for i, e := range es {
		out[i] = cloneEntry(e)
	}
	return out
}

func sortedKeys[M ~map[string]V, V any](m M) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
