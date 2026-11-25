package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/sahilm/fuzzy"
)

// Lookup finds a tool by name, slash command, alias or CLI command path.
func (r *Registry) Lookup(s string) (Tool, bool) {
	if t, ok := r.Get(s); ok {
		return t, true
	}
	if t, ok := r.BySlash(strings.TrimPrefix(s, "/")); ok {
		return t, true
	}
	for _, t := range r.tools {
		if t.Command == s {
			return t, true
		}
	}
	return Tool{}, false
}

// Suggest returns up to n slash commands close to s: typos within two
// edits first (of the slash word or the tool name), then fuzzy matches.
func (r *Registry) Suggest(s string, n int) []string {
	s = strings.ToLower(strings.TrimPrefix(s, "/"))
	type cand struct {
		slash string
		dist  int
	}
	var close []cand
	var words []string
	for _, t := range r.All() {
		words = append(words, t.Slash)
		d := min(editDistance(s, t.Slash), editDistance(s, t.Name))
		for _, a := range t.Aliases {
			d = min(d, editDistance(s, a))
		}
		if d <= 2 {
			close = append(close, cand{t.Slash, d})
		}
	}
	sort.SliceStable(close, func(i, j int) bool { return close[i].dist < close[j].dist })
	var out []string
	seen := map[string]bool{}
	add := func(w string) {
		if !seen[w] && len(out) < n {
			seen[w] = true
			out = append(out, w)
		}
	}
	for _, c := range close {
		add(c.slash)
	}
	for _, m := range fuzzy.Find(s, words) {
		add(m.Str)
	}
	return out
}

// editDistance is the Levenshtein distance (with adjacent transpositions
// counted as one edit, which catches "chrat").
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev2 := make([]int, len(rb)+1)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(rb)]
}

// DecodeInput strictly decodes JSON args into a fresh input for t. Unknown
// fields, trailing data and type mismatches are USAGE errors. Before that,
// normalizeArgs accepts what agents naturally send: kebab-case names (the
// contract-1 spellings, deprecated), a single string for a list field, and
// a number for a decimal-string field (size, px) or a numeric string for a
// number field.
func DecodeInput(t Tool, args json.RawMessage) (any, error) {
	in := t.NewInput()
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return in, nil
	}
	trimmed = normalizeArgs(t, trimmed)
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(in); err != nil {
		e := Errorf(CodeUsage, "%s: invalid arguments: %s", t.Name, cleanJSONErr(err))
		e.Hint = "see `hlp schema " + t.Name + "` for the accepted fields"
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			e.Hint = typeHint(te) + "; " + e.Hint
		}
		return nil, e
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, Errorf(CodeUsage, "%s: trailing data after arguments", t.Name)
	}
	return in, nil
}

// normalizeArgs rewrites a JSON object's keys to the canonical field names
// and coerces scalar shapes (see DecodeInput). Anything it does not
// understand is left for the strict decoder to reject.
func normalizeArgs(t Tool, raw []byte) []byte {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return raw
	}
	fields := Fields(t)
	out := make(map[string]json.RawMessage, len(obj))
	changed := false
	for k, v := range obj {
		// Only the exact name and its kebab or snake spelling are aliases
		// (no looser folding: "y-e-s" must not become yes).
		var f Field
		ok := false
		for _, x := range fields {
			if k == x.Name || k == x.Flag || k == strings.ReplaceAll(x.Flag, "-", "_") {
				f, ok = x, true
				break
			}
		}
		if !ok {
			out[k] = v
			continue
		}
		if f.Name != k {
			changed = true
			if _, dup := obj[f.Name]; dup {
				continue // the canonical spelling wins
			}
		}
		if nv, ok := coerceArg(f, v); ok {
			v, changed = nv, true
		}
		out[f.Name] = v
	}
	if !changed {
		return raw
	}
	b, err := json.Marshal(out)
	if err != nil {
		return raw
	}
	return b
}

// coerceArg converts a scalar to the field's shape: "x" -> ["x"] for
// lists, 1 -> "1" for strings, "1.5" -> 1.5 for numbers.
func coerceArg(f Field, v json.RawMessage) (json.RawMessage, bool) {
	v = bytes.TrimSpace(v)
	if len(v) == 0 {
		return v, false
	}
	switch f.Kind {
	case reflect.Slice:
		if v[0] == '"' || (f.Elem != reflect.String && (v[0] == '-' || v[0] >= '0' && v[0] <= '9')) {
			return append(append(json.RawMessage{'['}, v...), ']'), true
		}
	case reflect.String:
		if decimalFields[f.Name] && (v[0] == '-' || v[0] >= '0' && v[0] <= '9') {
			var n json.Number
			if json.Unmarshal(v, &n) == nil {
				b, _ := json.Marshal(n.String())
				return b, true
			}
		}
	case reflect.Int, reflect.Int64, reflect.Int32, reflect.Uint64, reflect.Uint, reflect.Uint32, reflect.Float64, reflect.Float32:
		if v[0] == '"' {
			var s string
			if json.Unmarshal(v, &s) == nil {
				if _, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
					return json.RawMessage(strings.TrimSpace(s)), true
				}
			}
		}
	case reflect.Bool:
		if v[0] == '"' {
			var s string
			if json.Unmarshal(v, &s) == nil {
				if b, err := strconv.ParseBool(s); err == nil {
					return json.RawMessage(strconv.FormatBool(b)), true
				}
			}
		}
	}
	return v, false
}

// decimalFields are string inputs that hold a decimal (exact on the wire):
// a JSON number is accepted and kept as written.
var decimalFields = map[string]bool{"size": true, "px": true, "tp": true, "sl": true, "trigger": true, "triggerPx": true}

// typeHint names the shape a mistyped field takes.
func typeHint(te *json.UnmarshalTypeError) string {
	switch te.Type.Kind() {
	case reflect.Slice:
		return fmt.Sprintf("%s takes a list, e.g. [\"a\",\"b\"]", te.Field)
	case reflect.String:
		return fmt.Sprintf("%s takes a string, e.g. \"1.5\"", te.Field)
	case reflect.Bool:
		return te.Field + " takes true or false"
	case reflect.Struct, reflect.Map:
		return te.Field + " takes an object"
	}
	return te.Field + " takes a number"
}

func cleanJSONErr(err error) string {
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		return fmt.Sprintf("field %q must be %s, got %s", te.Field, te.Type, te.Value)
	}
	return strings.TrimPrefix(err.Error(), "json: ")
}

func (r *Registry) invoke(ctx context.Context, env *Env, c Call) (*Result, error) {
	t, ok := r.Lookup(c.Tool)
	if !ok {
		e := Errorf(CodeUsage, "unknown tool %q", c.Tool)
		if s := r.Suggest(c.Tool, 3); len(s) > 0 {
			e.Hint = "did you mean: " + strings.Join(s, ", ") + "?"
		}
		return nil, e
	}
	if env == nil {
		return nil, Errorf(CodeInternal, "%s: no environment", t.Name)
	}
	in, err := DecodeInput(t, c.Args)
	if err != nil {
		return nil, err
	}
	if t.Mutating && env.Guard == nil {
		return nil, &Error{Code: CodeSafety, Message: t.Name + ": no risk guard installed"}
	}
	res, err := t.Run(ctx, env, in)
	if err != nil {
		return nil, AsError(err)
	}
	return env.finish(res), nil
}

// RunStream runs t's Stream with strictly decoded args, normalizing every
// result's meta and every error like Invoke. Tools without Stream emit one
// Run result.
func (r *Registry) RunStream(ctx context.Context, env *Env, c Call, emit func(*Result)) error {
	t, ok := r.Lookup(c.Tool)
	if !ok {
		return Errorf(CodeUsage, "unknown tool %q", c.Tool)
	}
	if env == nil {
		return Errorf(CodeInternal, "%s: no environment", t.Name)
	}
	in, err := DecodeInput(t, c.Args)
	if err != nil {
		return err
	}
	if t.Stream == nil || t.Mutating {
		res, err := t.Run(ctx, env, in)
		if err != nil {
			return AsError(err)
		}
		emit(env.finish(res))
		return nil
	}
	err = t.Stream(ctx, env, in, func(res *Result) { emit(env.finish(res)) })
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil && errors.Is(err, ctx.Err()):
		return nil // normal end of a stream
	}
	return AsError(err)
}

// finish fills defaults every Result must carry.
func (e *Env) finish(res *Result) *Result {
	if res == nil {
		return &Result{Card: CardText, Data: "", Meta: e.meta("hlp")}
	}
	if res.Meta.AsOf.IsZero() {
		res.Meta.AsOf = e.now()
	}
	res.Meta.AsOf = res.Meta.AsOf.UTC() // meta.asOf is always UTC
	if res.Meta.Network == "" {
		res.Meta.Network = e.Config.Network
	}
	if res.Meta.Source == "" {
		res.Meta.Source = "hlp"
	}
	if res.Meta.Mode == "" {
		res.Meta.Mode = ModeLive
	}
	// Config problems Load found (a group- or world-writable file, audit
	// S14) ride on every result so a person or agent sees them.
	for _, w := range e.Config.Warnings {
		if !containsString(res.Meta.Warnings, w) {
			res.Meta.Warnings = append(res.Meta.Warnings, w)
		}
	}
	return res
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
