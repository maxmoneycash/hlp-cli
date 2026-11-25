package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// UserFileName is the override file's name inside the hlp config directory.
const UserFileName = "vaults.toml"

// UserPath returns the override path next to config.toml, e.g.
// UserPath(config.DefaultPaths().ConfigFile) = ~/.config/hlp/vaults.toml.
func UserPath(configFile string) string {
	return filepath.Join(filepath.Dir(configFile), UserFileName)
}

// override is one [[vault]] table in the user file. Pointer fields are only
// applied when present, so a user can change one field of a curated entry.
//
// Matching: by id, else by address. No match = a new user entry (id derived
// from name when missing, kind defaults to hypercore when an address is set).
type override struct {
	ID           string    `toml:"id"`
	Name         *string   `toml:"name"`
	Kind         *Kind     `toml:"kind"`
	Address      *string   `toml:"address"`
	Slug         *string   `toml:"slug"`
	Chain        *string   `toml:"chain"`
	ChainID      *int      `toml:"chain_id"`
	Relationship *string   `toml:"relationship"`
	Parent       *string   `toml:"parent"`
	Leader       *string   `toml:"leader"`
	Status       *string   `toml:"status"`
	Created      *string   `toml:"created"`
	Description  *string   `toml:"description"`
	Tags         *[]string `toml:"tags"`
	SelectedFor  *[]string `toml:"selected_for"`
	SourceURL    *string   `toml:"source_url"`
	AnalyticsURL *string   `toml:"analytics_url"`
	Lens         *string   `toml:"lens"`
	Notes        *string   `toml:"notes"`
	Aliases      *[]string `toml:"aliases"`
	Snapshot     *Snapshot `toml:"snapshot"`

	AddTags    []string `toml:"add_tags"`
	RemoveTags []string `toml:"remove_tags"`
	Hide       bool     `toml:"hide"`
}

type userFile struct {
	SchemaVersion int        `toml:"schema_version"`
	Vault         []override `toml:"vault"`
}

// WithOverrides applies a user override document to r and returns a new
// registry; r is unchanged. Problems with single tables are returned as
// warnings and that table is skipped; a document that does not parse is an
// error (callers should then keep r and show the error as a warning).
func (r *Registry) WithOverrides(data []byte) (*Registry, []string, error) {
	var uf userFile
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&uf); err != nil {
		return nil, nil, fmt.Errorf("vault overrides: %w", err)
	}
	if uf.SchemaVersion != 0 && uf.SchemaVersion != SchemaVersion {
		return nil, nil, fmt.Errorf("vault overrides: schema_version %d, want %d", uf.SchemaVersion, SchemaVersion)
	}
	es := cloneEntries(r.entries)
	idx := func(id, addr string) int {
		for i, e := range es {
			if id != "" && e.ID == strings.ToLower(id) {
				return i
			}
		}
		if addr != "" {
			for i, e := range es {
				if e.Addr() != "" && e.Addr() == strings.ToLower(strings.TrimSpace(addr)) {
					return i
				}
			}
		}
		return -1
	}
	var warns []string
	hidden := map[int]bool{}
	for n, o := range uf.Vault {
		addr := ""
		if o.Address != nil {
			addr = *o.Address
		}
		i := idx(o.ID, addr)
		label := o.ID
		if label == "" {
			label = addr
		}
		if i >= 0 {
			if o.Hide {
				hidden[i] = true
				continue
			}
			e := es[i]
			o.apply(&e)
			e.Origin = OriginUser
			normalizeEntry(&e)
			if err := validate(e, false); err != nil {
				warns = append(warns, fmt.Sprintf("vault override %d (%s): %v; ignored", n+1, label, err))
				continue
			}
			es[i] = e
			continue
		}
		if o.Hide {
			warns = append(warns, fmt.Sprintf("vault override %d: hide: no curated entry %q", n+1, label))
			continue
		}
		e := Entry{ID: o.ID, Origin: OriginUser, Chain: "hypercore-mainnet", SelectedFor: []string{"user"}}
		o.apply(&e)
		if e.Kind == "" && e.Address != "" {
			e.Kind = KindHyperCore
		}
		if e.Kind == KindHyperCore && e.Relationship == "" {
			e.Relationship = "normal"
		}
		if e.ID == "" {
			e.ID = slugify(e.Name)
		}
		if e.SourceURL == "" && e.Kind == KindHyperCore {
			e.SourceURL = "https://app.hyperliquid.xyz/vaults/" + strings.ToLower(e.Address)
		}
		normalizeEntry(&e)
		if err := validate(e, false); err != nil {
			warns = append(warns, fmt.Sprintf("vault override %d (%s): %v; ignored", n+1, label, err))
			continue
		}
		if idx(e.ID, "") >= 0 {
			warns = append(warns, fmt.Sprintf("vault override %d: id %q already exists; ignored", n+1, e.ID))
			continue
		}
		es = append(es, e)
	}
	kept := es[:0:0]
	for i, e := range es {
		if !hidden[i] {
			kept = append(kept, e)
		}
	}
	out, err := build(r.GeneratedAt, kept)
	if err != nil {
		return nil, warns, err
	}
	return out, warns, nil
}

func (o override) apply(e *Entry) {
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	set(&e.Name, o.Name)
	if o.Kind != nil {
		e.Kind = *o.Kind
	}
	set(&e.Address, o.Address)
	set(&e.Slug, o.Slug)
	set(&e.Chain, o.Chain)
	if o.ChainID != nil {
		e.ChainID = *o.ChainID
	}
	set(&e.Relationship, o.Relationship)
	set(&e.Parent, o.Parent)
	set(&e.Leader, o.Leader)
	set(&e.Status, o.Status)
	set(&e.Created, o.Created)
	set(&e.Description, o.Description)
	set(&e.SourceURL, o.SourceURL)
	set(&e.AnalyticsURL, o.AnalyticsURL)
	set(&e.Lens, o.Lens)
	set(&e.Notes, o.Notes)
	if o.Tags != nil {
		e.Tags = append([]string(nil), (*o.Tags)...)
	}
	if o.SelectedFor != nil {
		e.SelectedFor = append([]string(nil), (*o.SelectedFor)...)
	}
	if o.Aliases != nil {
		e.Aliases = append([]string(nil), (*o.Aliases)...)
	}
	if o.Snapshot != nil {
		s := *o.Snapshot
		e.Snapshot = &s
	}
	e.Tags = append(e.Tags, o.AddTags...)
	if len(o.RemoveTags) > 0 {
		rm := map[string]bool{}
		for _, t := range o.RemoveTags {
			rm[NormalizeTag(t)] = true
		}
		kept := e.Tags[:0:0]
		for _, t := range e.Tags {
			if !rm[NormalizeTag(t)] {
				kept = append(kept, t)
			}
		}
		e.Tags = kept
	}
}

// LoadUser returns the embedded registry with the user's override file at
// path applied. A missing file is not an error. Any problem with the file
// is reported in warnings and the embedded registry (or the valid part of
// the overrides) is used, so a bad user file never breaks a command.
func LoadUser(path string) (*Registry, []string) {
	base, err := Default()
	if err != nil {
		return &Registry{byID: map[string]int{}, byAddr: map[string]int{}, bySlug: map[string]int{}}, []string{err.Error()}
	}
	if path == "" {
		return base, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return base, nil
	}
	if err != nil {
		return base, []string{fmt.Sprintf("vault overrides %s: %v", path, err)}
	}
	r, warns, err := base.WithOverrides(data)
	if err != nil {
		return base, append(warns, fmt.Sprintf("%s: %v (using the built-in list)", path, err))
	}
	for i, w := range warns {
		warns[i] = path + ": " + w
	}
	return r, warns
}

func slugify(s string) string {
	return strings.Join(tokens(s), "-")
}
