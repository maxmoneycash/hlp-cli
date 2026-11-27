package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// Contract is the version of the agent-facing JSON contract (field names,
// envelope, error codes). It is bumped on any breaking rename and reported
// in the MCP serverInfo and tool results (AGENT-UX #27).
//
// Contract 2: every input name is camelCase (dryRun, feesBps, maxDd,
// minTvl, minAge, notTag, reduceOnly, acceptPineforgeLicense) with
// kebab-case CLI flags; the contract-1 kebab names are still accepted as
// deprecated aliases. testdata/contract.json locks the schemas.
//
// Contract 3: runs that call an LLM more than once need llm: true
// (CONFIRM_REQUIRED otherwise) and stop at maxLlmCalls; arena and backtest
// results carry vsBench, bench, model, decision and llm usage; new tools
// list_llm_models and get_macro.
//
// Contract 4 (additive): get_strategy_vault takes risk and returns
// manipulationRisk, a share-price manipulation-risk estimate.
//
// Contract 5 (additive): meta.mode ("live" | "paper") on every result;
// the paper exchange tools paper_order, paper_cancel, paper_reset and
// paper_account; place_order takes paper and paperAccount.
//
// Contract 6 (additive): get_tick_candles (`hlp candles`): tick, volume
// and range bars from live trades with local-only history.
const Contract = "6"

// Agent compaction defaults (AGENT-UX #1).
const (
	// CompactMaxPoints caps every time series ([{t,v}] list) for agents.
	CompactMaxPoints = 200
	// CompactMaxBytes caps the JSON form of one agent result.
	CompactMaxBytes = 64 << 10
)

// seriesKeys are bulky curves dropped for agents unless they ask for series.
// Only list or object values are dropped: "equity" or "pnl" numbers stay.
var seriesKeys = map[string]bool{
	"curve": true, "nav": true, "equity": true, "pnl": true, "plots": true, "markers": true,
	// a screener row's sparkline and the history series of a tearsheet:
	// list_vaults with 50 rows carried 50 of them
	"spark": true, "sharePrice": true, "estimatedEquity": true, "drawdown": true,
}

// CompactOptions tunes Compact.
type CompactOptions struct {
	// Series keeps curves, nav, equity, pnl, plots, markers, spark,
	// sharePrice, estimatedEquity and drawdown series (still
	// downsampled to MaxPoints).
	Series bool
	// MaxPoints caps each time series; 0 = CompactMaxPoints.
	MaxPoints int
	// MaxBytes caps the encoded JSON; 0 = CompactMaxBytes, <0 = no cap.
	MaxBytes int
}

func (o CompactOptions) points() int {
	if o.MaxPoints > 0 {
		return o.MaxPoints
	}
	return CompactMaxPoints
}

func (o CompactOptions) bytes() int {
	if o.MaxBytes == 0 {
		return CompactMaxBytes
	}
	return o.MaxBytes
}

// Compact returns a copy of res bounded for agents: Data becomes its JSON
// form with third-party text cleaned (domain.CleanText), series dropped or
// downsampled and the whole payload capped at MaxBytes. Anything removed is
// reported in Meta.Warnings and sets Meta.Truncated. The CLI keeps full data;
// MCP calls this on every result.
func Compact(res *Result, o CompactOptions) (*Result, error) {
	if res == nil {
		return nil, nil
	}
	v, notes, err := CompactValue(res.Data, o)
	if err != nil {
		return nil, err
	}
	out := *res
	out.Data = v
	out.Meta.Warnings = append(append([]string(nil), res.Meta.Warnings...), notes...)
	if len(notes) > 0 {
		out.Meta.Truncated = true
	}
	return &out, nil
}

// CompactValue is Compact on any value: it returns the bounded JSON form of
// v (maps, slices, json.Number, strings, bools, nil) and one note per kind of
// reduction made. No notes means nothing was dropped.
func CompactValue(v any, o CompactOptions) (any, []string, error) {
	g, err := toGeneric(v)
	if err != nil {
		return nil, nil, err
	}
	c := &compactor{opts: o, dropped: map[string]bool{}, sampled: map[string][2]int{}, cut: map[string][2]int{}}
	g = c.clean(g)
	if !o.Series {
		g = c.drop(g, "data")
	}
	g = c.sample(g, "data", o.points())
	if limit := o.bytes(); limit > 0 {
		g = c.capSize(g, limit)
	}
	return g, c.notes(), nil
}

// toGeneric converts v to its JSON form, keeping numbers exact.
func toGeneric(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var g any
	if err := dec.Decode(&g); err != nil {
		return nil, err
	}
	return g, nil
}

type compactor struct {
	opts    CompactOptions
	dropped map[string]bool   // generalized path -> dropped
	sampled map[string][2]int // path -> {max original points, kept}
	cut     map[string][2]int // path -> {original rows, kept}
	strs    []string          // shortened string paths
	fields  []string          // fields dropped by the size cap
}

// clean strips terminal controls, bidi overrides and zero-width characters
// from every string and key (SECURITY S3): the text reaches a model and,
// through MCP hosts, often a terminal.
func (c *compactor) clean(g any) any {
	switch x := g.(type) {
	case string:
		return domain.CleanText(x)
	case []any:
		for i := range x {
			x[i] = c.clean(x[i])
		}
	case map[string]any:
		for k, v := range x {
			ck := domain.CleanText(k)
			if ck != k {
				delete(x, k)
			}
			x[ck] = c.clean(v)
		}
	}
	return g
}

func (c *compactor) drop(g any, path string) any {
	switch x := g.(type) {
	case []any:
		for i := range x {
			x[i] = c.drop(x[i], path+"[]")
		}
	case map[string]any:
		for k, v := range x {
			if seriesKeys[k] {
				switch v.(type) {
				case []any, map[string]any:
					delete(x, k)
					c.dropped[path+"."+k] = true
					continue
				}
			}
			x[k] = c.drop(v, path+"."+k)
		}
	}
	return g
}

// isSeries reports whether a is a domain.Series: objects with exactly t and v.
func isSeries(a []any) bool {
	if len(a) == 0 {
		return false
	}
	for _, e := range a {
		m, ok := e.(map[string]any)
		if !ok || len(m) != 2 {
			return false
		}
		if _, ok := m["t"]; !ok {
			return false
		}
		if _, ok := m["v"]; !ok {
			return false
		}
	}
	return true
}

// downsample keeps n evenly spaced points, always the first and the last.
func downsample(a []any, n int) []any {
	if n <= 0 || len(a) <= n {
		return a
	}
	if n == 1 {
		return []any{a[len(a)-1]}
	}
	out := make([]any, n)
	for i := range n {
		out[i] = a[i*(len(a)-1)/(n-1)]
	}
	return out
}

func (c *compactor) sample(g any, path string, n int) any {
	switch x := g.(type) {
	case []any:
		if isSeries(x) {
			if len(x) > n {
				s := c.sampled[path]
				s[0] = max(s[0], len(x))
				s[1] = n
				c.sampled[path] = s
				return downsample(x, n)
			}
			return x
		}
		for i := range x {
			x[i] = c.sample(x[i], path+"[]", n)
		}
	case map[string]any:
		for k, v := range x {
			x[k] = c.sample(v, path+"."+k, n)
		}
	}
	return g
}

// capSize shrinks g until its JSON fits limit: first by downsampling every
// series harder, then by halving the biggest list (keeping the newest rows
// of a time-ordered list, else the first rows), then by shortening long
// strings, and last by dropping the biggest field.
func (c *compactor) capSize(g any, limit int) any {
	if sizeOf(g) <= limit {
		return g
	}
	for _, n := range []int{50, 12} {
		if n >= c.opts.points() {
			continue
		}
		g = c.sample(g, "data", n)
		if sizeOf(g) <= limit {
			return g
		}
	}
	// The holder lets the root itself be cut (a top-level list of rows).
	h := map[string]any{"data": g}
	for range 200 {
		if sizeOf(h["data"]) <= limit {
			break
		}
		s := largest(h, func(_ string, v any) bool { a, ok := v.([]any); return ok && len(a) > 1 })
		if s.ok {
			a := s.get().([]any)
			keep := len(a) / 2
			rec := c.cut[s.path]
			if rec[0] == 0 {
				rec[0] = len(a)
			}
			var kept []any
			if newestLast(a) {
				kept = a[len(a)-keep:]
			} else {
				kept = a[:keep]
			}
			rec[1] = keep
			c.cut[s.path] = rec
			s.set(kept)
			continue
		}
		s = largest(h, func(_ string, v any) bool { t, ok := v.(string); return ok && len(t) > 256 })
		if s.ok {
			t := s.get().(string)
			cut := len(t) / 2
			for cut > 0 && !utf8.RuneStart(t[cut]) {
				cut--
			}
			s.set(t[:cut] + "…")
			c.strs = appendOnce(c.strs, s.path)
			continue
		}
		s = largest(h, func(p string, _ any) bool { return p != "data" && !strings.HasSuffix(p, "[]") })
		if !s.ok || s.del == nil {
			break
		}
		s.del()
		c.fields = appendOnce(c.fields, s.path)
	}
	return h["data"]
}

func appendOnce(xs []string, s string) []string {
	for _, x := range xs {
		if x == s {
			return xs
		}
	}
	return append(xs, s)
}

// newestLast reports whether a is a list of rows ordered oldest first (by a
// t or time field), so a cut should keep its tail.
func newestLast(a []any) bool {
	first, ok1 := rowTime(a[0])
	last, ok2 := rowTime(a[len(a)-1])
	return ok1 && ok2 && first < last
}

func rowTime(v any) (float64, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return 0, false
	}
	for _, k := range []string{"t", "time"} {
		if n, ok := m[k].(json.Number); ok {
			f, err := n.Float64()
			return f, err == nil
		}
	}
	return 0, false
}

// slot addresses one value in the tree so it can be replaced or removed.
type slot struct {
	ok   bool
	path string
	size int
	get  func() any
	set  func(any)
	del  func() // nil for list elements and the root
}

// largest finds the biggest value under the holder h matching pred; paths
// start at "data".
func largest(h map[string]any, pred func(path string, v any) bool) slot {
	var best slot
	var walk func(v any, path string) int
	consider := func(s slot, v any) {
		if pred(s.path, v) && (!best.ok || s.size > best.size) {
			best = s
		}
	}
	walk = func(v any, path string) int {
		switch x := v.(type) {
		case []any:
			n := 2
			for i := range x {
				p := path + "[]"
				sz := walk(x[i], p)
				consider(slot{ok: true, path: p, size: sz, get: func() any { return x[i] }, set: func(nv any) { x[i] = nv }}, x[i])
				n += sz + 1
			}
			return n
		case map[string]any:
			n := 2
			for k, e := range x {
				p := k
				if path != "" {
					p = path + "." + k
				}
				sz := walk(e, p)
				consider(slot{ok: true, path: p, size: sz, get: func() any { return x[k] }, set: func(nv any) { x[k] = nv }, del: func() { delete(x, k) }}, e)
				n += sz + len(k) + 4
			}
			return n
		}
		return scalarSize(v)
	}
	walk(h, "")
	return best
}

// sizeOf approximates len(json.Marshal(g)) for a generic tree.
func sizeOf(g any) int {
	switch x := g.(type) {
	case []any:
		n := 2
		for _, e := range x {
			n += sizeOf(e) + 1
		}
		return n
	case map[string]any:
		n := 2
		for k, e := range x {
			n += sizeOf(e) + len(k) + 4
		}
		return n
	}
	return scalarSize(g)
}

func scalarSize(v any) int {
	switch x := v.(type) {
	case nil:
		return 4
	case bool:
		if x {
			return 4
		}
		return 5
	case json.Number:
		return len(x)
	case string:
		b, _ := json.Marshal(x)
		return len(b)
	case float64:
		return len(strconv.FormatFloat(x, 'g', -1, 64))
	}
	b, _ := json.Marshal(v)
	return len(b)
}

func (c *compactor) notes() []string {
	var out []string
	if len(c.dropped) > 0 {
		out = append(out, "compacted for agents: dropped "+strings.Join(sortedKeys(c.dropped), ", ")+" (pass series:true to keep them)")
	}
	if len(c.sampled) > 0 {
		var parts []string
		for _, p := range sortedKeys(c.sampled) {
			s := c.sampled[p]
			parts = append(parts, fmt.Sprintf("%s %d→%d", p, s[0], s[1]))
		}
		out = append(out, "series downsampled (points): "+strings.Join(parts, ", "))
	}
	if len(c.cut) > 0 {
		var parts []string
		for _, p := range sortedKeys(c.cut) {
			s := c.cut[p]
			parts = append(parts, fmt.Sprintf("%s kept %d of %d rows", p, s[1], s[0]))
		}
		out = append(out, "size cap: "+strings.Join(parts, ", ")+"; use select/limit to ask for less")
	}
	if len(c.strs) > 0 {
		out = append(out, "size cap: shortened text in "+strings.Join(c.strs, ", "))
	}
	if len(c.fields) > 0 {
		out = append(out, "size cap: dropped "+strings.Join(c.fields, ", ")+"; use select to ask for them alone")
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
