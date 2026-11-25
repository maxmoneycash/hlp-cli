package registry

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/sahilm/fuzzy"
)

// Tier ranks how a query matched. Higher is stronger.
type Tier int

const (
	TierTag       Tier = 1 // query is a tag the entry carries (Search only, never Resolve)
	TierFuzzy     Tier = 2 // subsequence match on name or id
	TierSubstring Tier = 3 // whole words of the query appear in name, id, slug or alias
	TierPrefix    Tier = 4 // name, id, slug or alias starts with the query; address prefix
	TierExact     Tier = 5 // id, slug, alias, address or name equal (ignoring case/punctuation)
)

// Match is one search hit.
type Match struct {
	Entry Entry  `json:"entry"`
	Tier  Tier   `json:"tier"`
	Score int    `json:"score"` // within a tier; higher is better
	Field string `json:"field"` // id | name | slug | alias | address | tag
}

// NotFoundError means nothing in the registry matched. The CLI should fall
// back to the full vault universe (core's fuzzy screener search).
type NotFoundError struct{ Query string }

func (e *NotFoundError) Error() string { return fmt.Sprintf("no curated vault matches %q", e.Query) }

// AmbiguousError means several entries matched equally well.
type AmbiguousError struct {
	Query      string
	Candidates []Match
}

func (e *AmbiguousError) Error() string {
	names := make([]string, 0, len(e.Candidates))
	for _, c := range e.Candidates {
		names = append(names, fmt.Sprintf("%s (%s)", c.Entry.Name, c.Entry.ID))
	}
	return fmt.Sprintf("%q matches %d curated entries: %s", e.Query, len(e.Candidates), strings.Join(names, ", "))
}

// Resolve finds the single entry for a user-typed handle: id ("hlp"),
// slug ("spxmeme"), alias, address or address prefix (>= 6 hex chars), exact
// name, or a fuzzy name ("long hype btc"). It returns *NotFoundError or
// *AmbiguousError (with ranked candidates) otherwise. Tags never resolve.
func (r *Registry) Resolve(q string) (Entry, error) {
	ms := r.search(q, false)
	if len(ms) == 0 {
		return Entry{}, &NotFoundError{Query: q}
	}
	top := ms[0]
	n := 1
	for n < len(ms) && ms[n].Tier == top.Tier {
		n++
	}
	if n == 1 || top.Tier == TierExact {
		// several exact hits only happen when a name equals another's id; prefer file order (ms is stable).
		return top.Entry, nil
	}
	if top.Tier == TierFuzzy && ms[0].Score > 0 && ms[1].Score*2 <= ms[0].Score {
		return top.Entry, nil // clear fuzzy winner
	}
	if n > 8 {
		n = 8
	}
	return Entry{}, &AmbiguousError{Query: q, Candidates: ms[:n]}
}

// Search returns ranked matches for q, including tag hits, at most limit
// (<= 0 = all). Useful for autocomplete and "did you mean".
func (r *Registry) Search(q string, limit int) []Match {
	ms := r.search(q, true)
	if limit > 0 && len(ms) > limit {
		ms = ms[:limit]
	}
	return ms
}

func (r *Registry) search(q string, withTags bool) []Match {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil
	}
	lq := strings.ToLower(q)
	nq := squash(q)
	words := tokens(q)
	best := make([]Match, len(r.entries))
	bump := func(i int, t Tier, score int, field string) {
		if t > best[i].Tier || (t == best[i].Tier && score > best[i].Score) {
			best[i] = Match{Tier: t, Score: score, Field: field}
		}
	}

	isAddr := strings.HasPrefix(lq, "0x")
	for i, e := range r.entries {
		if isAddr {
			a := e.Addr()
			switch {
			case a == "":
			case a == lq:
				bump(i, TierExact, 100, "address")
			case len(lq) >= 6 && strings.HasPrefix(a, lq):
				bump(i, TierPrefix, len(lq), "address")
			}
			continue
		}
		handles := []struct{ v, field string }{{e.ID, "id"}, {e.Slug, "slug"}, {e.Name, "name"}}
		for _, a := range e.Aliases {
			handles = append(handles, struct{ v, field string }{a, "alias"})
		}
		for _, h := range handles {
			if h.v == "" {
				continue
			}
			hv := strings.ToLower(h.v)
			hn := squash(h.v)
			switch {
			case hv == lq || (nq != "" && hn == nq):
				bump(i, TierExact, 100, h.field)
			case nq != "" && strings.HasPrefix(hn, nq):
				bump(i, TierPrefix, 100-(len(hn)-len(nq)), h.field)
			case len(words) > 0 && containsWords(tokens(h.v), words):
				bump(i, TierSubstring, 100-len(hn), h.field)
			}
		}
		if withTags {
			if t := NormalizeTag(q); e.HasTag(t) {
				bump(i, TierTag, 0, "tag")
			}
		}
	}

	if !isAddr {
		// fuzzy subsequence over names and ids
		names := make([]string, len(r.entries))
		ids := make([]string, len(r.entries))
		for i, e := range r.entries {
			names[i] = e.Name
			ids[i] = e.ID
		}
		for _, set := range []struct {
			data  []string
			field string
		}{{names, "name"}, {ids, "id"}} {
			for _, m := range fuzzy.Find(q, set.data) {
				bump(m.Index, TierFuzzy, m.Score, set.field)
			}
		}
	}

	var out []Match
	for i, m := range best {
		if m.Tier == 0 {
			continue
		}
		m.Entry = cloneEntry(r.entries[i])
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Tier != out[j].Tier {
			return out[i].Tier > out[j].Tier
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		// prefer real vaults, then file order
		vi, vj := out[i].Entry.Kind.IsVault(), out[j].Entry.Kind.IsVault()
		if vi != vj {
			return vi
		}
		return false
	})
	return out
}

// squash lowercases and keeps only letters and digits ("Long HYPE & BTC" -> "longhypebtc").
func squash(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// tokens splits on anything that is not a letter or digit.
func tokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// containsWords reports whether every query word is a prefix of some word of
// the handle ("hype btc" in "Long HYPE & BTC, Short Garbage").
func containsWords(have, want []string) bool {
	for _, w := range want {
		ok := false
		for _, h := range have {
			if strings.HasPrefix(h, w) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}
