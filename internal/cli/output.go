package cli

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/domain"
)

// Mode is the resolved output mode for one invocation.
type Mode struct {
	JSON   bool     // --json, HLP_JSON=1, or stdout is not a TTY (auto)
	NDJSON bool     // --ndjson (streaming tools: one Result per line)
	Plain  bool     // --plain: human layout, no ANSI
	Select []string // --select a,b.c: project fields of data (dot paths)
	Limit  int      // --limit
	Color  string   // --color auto|always|never

	// Machine output variants (each implies JSON mode: no cards).
	ResultsOnly bool // --results-only: print data without the {data, meta} envelope
	Raw         bool // -q/--raw: bare values when --select names one scalar field
	CSV         bool // --csv: the rows of data as CSV (nested fields flattened a.b)

	// Streams.
	Live      bool          // -w/--live: stream (redraw on a TTY)
	MaxEvents int           // --max-events / HLP_MAX_EVENTS: stop after N results (exit 0)
	For       time.Duration // --for: stop after this long (exit 0)
}

// ResolveMode applies the agent contract: JSON when asked (--json, --ndjson,
// HLP_JSON=1) or when stdout is not a terminal, unless --plain asks for the
// human layout without ANSI.
func ResolveMode(json, ndjson, plain bool, sel string, limit int, color string, stdoutTTY bool, getenv func(string) string) (Mode, error) {
	m := Mode{NDJSON: ndjson, Plain: plain, Limit: limit, Color: strings.ToLower(color)}
	switch m.Color {
	case "", "auto":
		m.Color = "auto"
	case "always", "never":
	default:
		return m, &core.Error{Code: core.CodeUsage, Message: "--color must be auto, always or never, got " + color}
	}
	if limit < 0 {
		return m, &core.Error{Code: core.CodeUsage, Message: "--limit must be positive"}
	}
	envJSON := false
	if v := strings.ToLower(getenv("HLP_JSON")); v == "1" || v == "true" || v == "yes" {
		envJSON = true
	}
	m.JSON = json || ndjson || envJSON || (!stdoutTTY && !plain)
	if m.Color == "never" && !m.JSON {
		m.Plain = true
	}
	for _, s := range strings.Split(sel, ",") {
		if s = strings.TrimSpace(s); s != "" {
			m.Select = append(m.Select, s)
		}
	}
	return m, nil
}

// IsTTY reports whether f is a terminal (stdlib only).
func IsTTY(f *os.File) bool {
	if f == nil {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// envelope is the success JSON shape on stdout.
type envelope struct {
	Data any `json:"data"`
	Meta any `json:"meta"`
}

// WriteJSON prints {"data":...,"meta":...} (Select/Limit applied by caller).
func WriteJSON(w io.Writer, r *core.Result) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(envelope{Data: r.Data, Meta: r.Meta})
}

// WriteError prints {"error":{code,message,details,hint,retryable}} to w
// (stderr). retryable is true for rate limits and transient network or
// server failures (exit code 4): wait details.retryAfterMs when present,
// then retry once.
func WriteError(w io.Writer, err error) {
	e := core.AsError(err)
	if e == nil {
		e = &core.Error{Code: core.CodeInternal, Message: "unknown error"}
	}
	if !e.Retryable && e.Code == core.CodeRateLimited {
		c := *e
		c.Retryable = true
		e = &c
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{"error": e})
}

// writeHumanError prints a short human error for terminals.
func writeHumanError(w io.Writer, err error) {
	// Messages carry untrusted text (paths from shared arena files, engine
	// stderr, upstream error bodies): strip terminal controls (SECURITY S3).
	e := core.AsError(err)
	fmt.Fprintf(w, "✗ %s  %s\n", e.Code, domain.CleanText(e.Message))
	if e.Hint != "" {
		fmt.Fprintf(w, "  %s\n", domain.CleanText(e.Hint))
	}
	if ds, ok := e.Details.([]core.PineDiag); ok {
		for _, d := range ds {
			fmt.Fprintf(w, "  %d:%d  %s\n", d.Line, d.Col, domain.CleanText(d.Msg))
		}
	}
}

// limitSlice truncates a top-level slice (card mode keeps typed data):
// the first rows, or the most recent ones for history (tail).
func limitSlice(data any, limit int, tail bool) any {
	if limit <= 0 || data == nil {
		return data
	}
	v := reflect.ValueOf(data)
	if v.Kind() == reflect.Slice && v.Len() > limit {
		if tail {
			return v.Slice(v.Len()-limit, v.Len()).Interface()
		}
		return v.Slice(0, limit).Interface()
	}
	return data
}

// writeResult emits one result in the resolved mode: the {data, meta}
// envelope, data alone (--results-only), bare values (-q) or CSV (--csv),
// after --select/--limit (meta gains total/truncated and select warnings).
func writeResult(w io.Writer, r *core.Result, m Mode) error {
	shaped, err := core.ShapeResult(r, m.Select, m.Limit)
	if err != nil {
		return err
	}
	var order map[string]int
	if m.CSV {
		order = keyOrder(r.Data) // typed data: shaping turns rows into maps
	}
	if m.CSV || m.Raw {
		if shaped.Data, err = generic(shaped.Data); err != nil {
			return err
		}
	}
	switch {
	case m.CSV:
		return writeCSV(w, shaped.Data, m.Select, order)
	case m.Raw:
		if vals, ok := bareValues(shaped.Data, m.Select); ok {
			_, err := io.WriteString(w, strings.Join(vals, "\n")+"\n")
			return err
		}
		return writeData(w, shaped.Data)
	case m.ResultsOnly:
		return writeData(w, shaped.Data)
	}
	return WriteJSON(w, shaped)
}

// generic turns typed data into its JSON form (maps, slices, json.Number).
func generic(data any) (any, error) {
	b, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	return v, dec.Decode(&v)
}

func writeData(w io.Writer, data any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(data)
}

// bareValues returns the selected scalar of each row (or of the object)
// when --select names exactly one path and every value is a scalar.
func bareValues(data any, sel []string) ([]string, bool) {
	if len(sel) != 1 {
		return nil, false
	}
	path := strings.Split(sel[0], ".")
	rows, ok := data.([]any)
	if !ok {
		rows = []any{data}
	}
	if len(rows) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		v, ok := dig(row, path)
		if !ok {
			return nil, false
		}
		s, ok := scalarText(v)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func dig(v any, path []string) (any, bool) {
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		var found bool
		if v, found = m[k]; !found {
			for mk, mv := range m {
				if strings.EqualFold(mk, k) {
					v, found = mv, true
					break
				}
			}
		}
		if !found {
			return nil, false
		}
	}
	return v, true
}

// scalarText prints a JSON scalar the way a shell wants it: strings bare,
// numbers as written, null empty.
func scalarText(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", true
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool, float64, int, int64:
		return fmt.Sprint(x), true
	}
	return "", false
}

// keyOrder is the first-seen position of every flattened (dot) key in the
// JSON form of data: struct field order, which puts identifiers (coin,
// name, address) first the way the JSON and the tables do.
func keyOrder(data any) map[string]int {
	b, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	order := map[string]int{}
	var walk func(prefix string) error
	walk = func(prefix string) error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('{'):
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return err
				}
				k, _ := kt.(string)
				if prefix != "" {
					k = prefix + "." + k
				}
				if _, ok := order[k]; !ok {
					order[k] = len(order)
				}
				if err := walk(k); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		case json.Delim('['):
			for dec.More() {
				// rows of a top-level list share one key space; nested
				// arrays are written as JSON and keep only their own key
				p := prefix
				if prefix != "" {
					p = "\x00" // skip keys inside nested arrays
				}
				if err := walk(p); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		}
		return nil
	}
	_ = walk("")
	return order
}

// writeCSV writes the rows of data (or the object as one row). Nested
// objects flatten to dot keys; arrays are written as compact JSON. Columns
// follow --select when given, else the JSON field order (struct order,
// identifiers first); keys the order does not know sort last by name.
func writeCSV(w io.Writer, data any, sel []string, order map[string]int) error {
	rows, ok := data.([]any)
	if !ok {
		rows = []any{data}
	}
	flat := make([]map[string]string, 0, len(rows))
	seen := map[string]bool{}
	var keys []string
	for _, row := range rows {
		f := map[string]string{}
		flatten("", row, f)
		for k := range f {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
		flat = append(flat, f)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		oi, iok := order[keys[i]]
		oj, jok := order[keys[j]]
		switch {
		case iok && jok:
			return oi < oj
		case iok != jok:
			return iok
		}
		return keys[i] < keys[j]
	})
	if len(sel) > 0 {
		// selection order first; a selected parent ("summary") keeps its
		// flattened children in sorted order
		var ordered []string
		used := map[string]bool{}
		for _, s := range sel {
			for _, k := range keys {
				if !used[k] && (strings.EqualFold(k, s) || strings.HasPrefix(strings.ToLower(k), strings.ToLower(s)+".")) {
					ordered = append(ordered, k)
					used[k] = true
				}
			}
		}
		for _, k := range keys {
			if !used[k] {
				ordered = append(ordered, k)
			}
		}
		keys = ordered
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(keys); err != nil {
		return err
	}
	for _, f := range flat {
		rec := make([]string, len(keys))
		for i, k := range keys {
			rec[i] = f[k]
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func flatten(prefix string, v any, out map[string]string) {
	join := func(k string) string {
		if prefix == "" {
			return k
		}
		return prefix + "." + k
	}
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 && prefix != "" {
			out[prefix] = ""
		}
		for k, val := range x {
			flatten(join(k), val, out)
		}
	case []any:
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(x)
		out[prefixOr(prefix)] = strings.TrimSpace(b.String())
	default:
		s, _ := scalarText(x)
		out[prefixOr(prefix)] = s
	}
}

func prefixOr(p string) string {
	if p == "" {
		return "value"
	}
	return p
}
