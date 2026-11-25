package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// ShapeInfo reports what Shape did: the row count before the limit (lists
// only), whether rows were cut, and the --select paths that matched nothing.
type ShapeInfo struct {
	Total     int  // rows before limit; valid when List
	List      bool // data was a top-level list
	Truncated bool
	Missing   []string // select paths that matched no field
	Fields    []string // top-level fields of the (first) object, for hints
	// LimitPath is the nested list limit applied to when data is an object
	// (account positions, hlp_positioning): the list the select paths go
	// through, or the object's only list. Total counts that list.
	LimitPath string
	// LimitIgnored: limit was given for an object with no single list.
	LimitIgnored bool
}

// Shape applies --limit (to a top-level list) and --select (to each object
// of a list, or to the object itself) on the JSON form of data. Dot paths
// ("summary.Last", "market.coin") select nested fields and the output keeps
// the nesting; a path through an array maps over its elements, so
// "positions.coin" gives positions:[{coin},…]. When no path matches any
// field of an object the result is a USAGE error listing the fields; paths
// that match nothing while others match are dropped (ShapeWith reports
// them). Data with no object to select from (null, scalars, empty lists)
// passes through.
func Shape(data any, sel []string, limit int) (any, error) {
	v, _, err := ShapeWith(data, sel, limit)
	return v, err
}

// ShapeWith is Shape plus what it did.
func ShapeWith(data any, sel []string, limit int) (any, ShapeInfo, error) {
	var info ShapeInfo
	if len(sel) == 0 && limit <= 0 {
		return data, info, nil
	}
	b, err := json.Marshal(data)
	if err != nil {
		return nil, info, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, info, err
	}
	paths := splitPaths(sel)
	matched := make([]bool, len(paths))
	if arr, ok := v.([]any); ok {
		info.List, info.Total = true, len(arr)
		if limit > 0 && len(arr) > limit {
			arr = arr[:limit]
			info.Truncated = true
		}
		if len(paths) > 0 {
			if len(arr) > 0 {
				info.Fields = fieldsOf(arr[0])
			}
			for i, x := range arr {
				arr[i] = project(x, paths, matched)
			}
		}
		v = arr
		if len(arr) == 0 {
			return v, info, nil // nothing to match against
		}
	} else {
		if m, ok := v.(map[string]any); ok && limit > 0 {
			if key := limitKey(m, paths); key != "" {
				arr := m[key].([]any)
				info.LimitPath, info.Total = key, len(arr)
				if len(arr) > limit {
					m[key] = arr[:limit]
					info.Truncated = true
				}
			} else {
				info.LimitIgnored = true
			}
		}
		if len(paths) > 0 {
			info.Fields = fieldsOf(v)
			v = project(v, paths, matched)
		}
	}
	if len(paths) == 0 {
		return v, info, nil
	}
	hit := false
	for i, ok := range matched {
		if ok {
			hit = true
		} else {
			info.Missing = append(info.Missing, sel[i])
		}
	}
	if !hit && info.Fields != nil {
		e := &Error{Code: CodeUsage, Message: "select " + strings.Join(sel, ",") + " matches no field"}
		e.Hint = "fields: " + strings.Join(info.Fields, ", ")
		return nil, info, e
	}
	return v, info, nil
}

// ShapeResult applies Shape to r.Data and returns a copy of r whose meta
// carries total/truncated (unless the tool set them) and a warning for each
// select path that matched nothing.
func ShapeResult(r *Result, sel []string, limit int) (*Result, error) {
	src, tailTotal := r.Data, -1
	if r.Tail && limit > 0 {
		if v := reflect.ValueOf(src); v.IsValid() && v.Kind() == reflect.Slice && v.Len() > limit {
			tailTotal = v.Len()
			src = v.Slice(v.Len()-limit, v.Len()).Interface()
		}
	}
	data, info, err := ShapeWith(src, sel, limit)
	if tailTotal >= 0 {
		info.List, info.Total, info.Truncated = true, tailTotal, true
	}
	if err != nil {
		return nil, err
	}
	out := *r
	out.Data = data
	if info.List && info.Truncated && out.Meta.Total == nil {
		n := info.Total
		out.Meta.Total = &n
	}
	if info.LimitPath != "" && info.Truncated && out.Meta.Total == nil {
		n := info.Total
		out.Meta.Total = &n
		out.Meta.Warnings = append(append([]string(nil), out.Meta.Warnings...), fmt.Sprintf("limit applied to %s (%d of %d)", info.LimitPath, limit, n))
	}
	if info.LimitIgnored {
		out.Meta.Warnings = append(append([]string(nil), out.Meta.Warnings...), "limit applies to lists; this result is an object (select one of its lists to bound it)")
	}
	if info.Truncated {
		out.Meta.Truncated = true
	}
	if len(info.Missing) > 0 {
		w := fmt.Sprintf("select: no field %s", strings.Join(quoteAll(info.Missing), ", "))
		if len(info.Fields) > 0 {
			w += " (fields: " + strings.Join(info.Fields, ", ") + ")"
		}
		out.Meta.Warnings = append(append([]string(nil), out.Meta.Warnings...), w)
	}
	return &out, nil
}

// limitKey picks the nested list a limit applies to on an object: the key
// every select path starts with when it holds a list, or with no select
// the object's only non-empty list. "" when there is no single list.
func limitKey(m map[string]any, paths [][]string) string {
	if len(paths) > 0 {
		first := paths[0][0]
		for _, p := range paths[1:] {
			if !strings.EqualFold(p[0], first) {
				return ""
			}
		}
		for k, v := range m {
			if strings.EqualFold(k, first) {
				if _, ok := v.([]any); ok {
					return k
				}
			}
		}
		return ""
	}
	key := ""
	for k, v := range m {
		if a, ok := v.([]any); ok && len(a) > 0 {
			if key != "" {
				return ""
			}
			key = k
		}
	}
	return key
}

func quoteAll(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = fmt.Sprintf("%q", x)
	}
	return out
}

func splitPaths(sel []string) [][]string {
	out := make([][]string, len(sel))
	for i, s := range sel {
		out[i] = strings.Split(s, ".")
	}
	return out
}

func fieldsOf(v any) []string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// project keeps the selected paths of v. matched[i] is set when paths[i]
// reached a value anywhere in v.
func project(v any, paths [][]string, matched []bool) any {
	idx := make([]int, len(paths))
	for i := range idx {
		idx[i] = i
	}
	out, _ := projectAt(v, paths, idx, 0, matched)
	if out == nil {
		if _, ok := v.(map[string]any); ok {
			return map[string]any{}
		}
		return v
	}
	return out
}

// projectAt projects v on the suffixes paths[i][depth:] for i in idx. It
// returns nil, false when nothing matched.
func projectAt(v any, paths [][]string, idx []int, depth int, matched []bool) (any, bool) {
	switch x := v.(type) {
	case []any:
		out := make([]any, 0, len(x))
		hit := false
		for _, el := range x {
			p, ok := projectAt(el, paths, idx, depth, matched)
			if ok {
				hit = true
				out = append(out, p)
			} else {
				out = append(out, map[string]any{})
			}
		}
		if !hit {
			return nil, false
		}
		return out, true
	case map[string]any:
		out := map[string]any{}
		// group by the selector's spelling of the next segment, keeping order
		var keys []string
		groups := map[string][]int{}
		for _, i := range idx {
			k := paths[i][depth]
			if _, ok := groups[k]; !ok {
				keys = append(keys, k)
			}
			groups[k] = append(groups[k], i)
		}
		for _, k := range keys {
			val, ok := lookupKey(x, k)
			if !ok {
				continue
			}
			var whole []int
			var deeper []int
			for _, i := range groups[k] {
				if len(paths[i]) == depth+1 {
					whole = append(whole, i)
				} else {
					deeper = append(deeper, i)
				}
			}
			if len(whole) > 0 {
				out[k] = val
				for _, i := range whole {
					matched[i] = true
				}
				for _, i := range deeper {
					// a parent was taken whole; its children are inside it
					if _, ok := walk(val, paths[i][depth+1:]); ok {
						matched[i] = true
					}
				}
				continue
			}
			if p, ok := projectAt(val, paths, deeper, depth+1, matched); ok {
				out[k] = p
			}
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	}
	return nil, false
}

// walk reports whether path reaches a value under v (through arrays too).
func walk(v any, path []string) (any, bool) {
	if len(path) == 0 {
		return v, true
	}
	switch x := v.(type) {
	case map[string]any:
		val, ok := lookupKey(x, path[0])
		if !ok {
			return nil, false
		}
		return walk(val, path[1:])
	case []any:
		for _, el := range x {
			if r, ok := walk(el, path); ok {
				return r, true
			}
		}
	}
	return nil, false
}

// lookupKey finds k in m, tolerating case differences (ChartSummary has
// Go-cased keys).
func lookupKey(m map[string]any, k string) (any, bool) {
	if val, ok := m[k]; ok {
		return val, true
	}
	for mk, mv := range m {
		if strings.EqualFold(mk, k) {
			return mv, true
		}
	}
	return nil, false
}
