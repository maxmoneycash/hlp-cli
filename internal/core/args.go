package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode"
)

// Field describes one input field as the CLI, slash parser and help see it.
type Field struct {
	Name        string       // json name (camelCase), e.g. "minTvl"
	Flag        string       // CLI flag (kebab-case), e.g. "min-tvl"
	Kind        reflect.Kind // String, Int, Uint64, Float64, Bool, Slice
	Elem        reflect.Kind // element kind for slices
	Description string       // from the jsonschema tag
}

// Fields lists the input fields of t in declaration order. Embedded structs
// without a json name (EngineOpt) are flattened, as encoding/json does.
func Fields(t Tool) []Field {
	if t.NewInput == nil {
		return nil
	}
	rt := reflect.TypeOf(t.NewInput()).Elem()
	if rt.Kind() != reflect.Struct {
		return nil
	}
	return appendFields(make([]Field, 0, rt.NumField()), rt)
}

func appendFields(out []Field, rt reflect.Type) []Field {
	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		name := strings.Split(sf.Tag.Get("json"), ",")[0]
		if sf.Anonymous && name == "" && sf.Type.Kind() == reflect.Struct {
			out = appendFields(out, sf.Type)
			continue
		}
		if !sf.IsExported() || name == "-" {
			continue
		}
		if name == "" {
			name = sf.Name
		}
		f := Field{Name: name, Flag: KebabCase(name), Kind: sf.Type.Kind(), Description: sf.Tag.Get("jsonschema")}
		if f.Kind == reflect.Slice {
			f.Elem = sf.Type.Elem().Kind()
		}
		out = append(out, f)
	}
	return out
}

// KebabCase turns a camelCase JSON name into its CLI flag:
// "acceptPineforgeLicense" -> "accept-pineforge-license", "minTvl" ->
// "min-tvl". Names already in kebab-case are unchanged.
func KebabCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 && s[i-1] != '-' {
				b.WriteByte('-')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// flag is the CLI spelling of f.
func (f Field) flag() string {
	if f.Flag != "" {
		return f.Flag
	}
	return KebabCase(f.Name)
}

func fieldByName(fs []Field, name string) (Field, bool) {
	norm := func(s string) string { return strings.ToLower(strings.NewReplacer("_", "-").Replace(s)) }
	n := norm(name)
	for _, f := range fs {
		if norm(f.Name) == n {
			return f, true
		}
	}
	// camelCase/kebab tolerance: "minTvl" -> "min-tvl", "dryRun" -> "dry-run"
	for _, f := range fs {
		if strings.ReplaceAll(norm(f.Name), "-", "") == strings.ReplaceAll(n, "-", "") {
			return f, true
		}
	}
	return Field{}, false
}

// freeText fields swallow extra positional words ("/vault Long HYPE & BTC").
var freeText = map[string]bool{"query": true, "vault": true, "thesis": true}

// ParseArgs turns slash/CLI-style argv ("BTC 15m --bars 200 --ma 20,50")
// into JSON args for t using Positional and the input struct's json tags.
func ParseArgs(t Tool, argv []string) (json.RawMessage, error) {
	fields := Fields(t)
	out := map[string]any{}
	var positional []string
	set := func(f Field, raw string) error {
		v, err := convert(f, raw)
		if err != nil {
			return err
		}
		if f.Kind == reflect.Slice {
			prev, _ := out[f.Name].([]any)
			out[f.Name] = append(prev, v.([]any)...)
			return nil
		}
		out[f.Name] = v
		return nil
	}
	flagsDone := false
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if flagsDone || !strings.HasPrefix(a, "--") || len(a) == 2 {
			if a == "--" && !flagsDone {
				flagsDone = true
				continue
			}
			positional = append(positional, a)
			continue
		}
		name, val, hasVal := strings.Cut(a[2:], "=")
		f, ok := fieldByName(fields, name)
		if !ok {
			if strings.HasPrefix(name, "no-") {
				if bf, ok2 := fieldByName(fields, name[3:]); ok2 && bf.Kind == reflect.Bool && !hasVal {
					out[bf.Name] = false
					continue
				}
			}
			return nil, usagef(t, "unknown flag --%s", name)
		}
		if f.Kind == reflect.Bool && !hasVal {
			out[f.Name] = true
			continue
		}
		if !hasVal {
			if i+1 >= len(argv) {
				return nil, usagef(t, "flag --%s needs a value", f.flag())
			}
			i++
			val = argv[i]
		}
		if err := set(f, val); err != nil {
			return nil, err
		}
	}
	for pi, name := range t.Positional {
		if len(positional) == 0 {
			break
		}
		f, ok := fieldByName(fields, name)
		if !ok {
			return nil, Errorf(CodeInternal, "%s: positional %q is not an input field", t.Name, name)
		}
		if _, dup := out[f.Name]; dup && f.Kind != reflect.Slice {
			// flag given explicitly; positional slot is skipped
			continue
		}
		last := pi == len(t.Positional)-1
		switch {
		case f.Kind == reflect.Slice:
			// a list positional consumes everything left (comma lists allowed)
			for _, p := range positional {
				if err := set(f, p); err != nil {
					return nil, err
				}
			}
			positional = nil
		case last && freeText[f.Name] && f.Kind == reflect.String:
			out[f.Name] = strings.Join(positional, " ")
			positional = nil
		default:
			if err := set(f, positional[0]); err != nil {
				return nil, err
			}
			positional = positional[1:]
		}
	}
	if len(positional) > 0 {
		return nil, usagef(t, "unexpected argument %q", positional[0])
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return nil, Errorf(CodeInternal, "encode args: %v", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func usagef(t Tool, format string, args ...any) *Error {
	e := Errorf(CodeUsage, format, args...)
	e.Hint = usageHint(t)
	return e
}

func usageHint(t Tool) string {
	var b strings.Builder
	b.WriteString("usage: hlp ")
	b.WriteString(t.Command)
	for _, p := range t.Positional {
		fmt.Fprintf(&b, " [%s]", p)
	}
	b.WriteString(" [--flags]; see `hlp schema ")
	b.WriteString(t.Name)
	b.WriteString("`")
	return b.String()
}

func convert(f Field, raw string) (any, error) {
	switch f.Kind {
	case reflect.String:
		return raw, nil
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, Errorf(CodeUsage, "--%s: %q is not true or false", f.flag(), raw)
		}
		return b, nil
	case reflect.Int, reflect.Int64, reflect.Int32:
		n, err := ParseNumber(raw)
		if err != nil || n != math.Trunc(n) {
			return nil, Errorf(CodeUsage, "--%s: %q is not a whole number", f.flag(), raw)
		}
		return int64(n), nil
	case reflect.Uint64, reflect.Uint, reflect.Uint32:
		n, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return nil, Errorf(CodeUsage, "--%s: %q is not a positive integer", f.flag(), raw)
		}
		return n, nil
	case reflect.Float64, reflect.Float32:
		n, err := ParseNumber(raw)
		if err != nil {
			return nil, Errorf(CodeUsage, "--%s: %v", f.flag(), err)
		}
		return n, nil
	case reflect.Slice:
		var out []any
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			v, err := convert(Field{Name: f.Name, Flag: f.Flag, Kind: f.Elem}, part)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	return nil, Errorf(CodeInternal, "--%s: unsupported field kind %s", f.Name, f.Kind)
}

// ParseNumber parses human numbers: "1500", "$100", "10k", "1.5m", "2b",
// "1,000", "20%" (returns 20). Signs are kept.
func ParseNumber(s string) (float64, error) {
	t := strings.TrimSpace(s)
	t = strings.ReplaceAll(t, ",", "")
	t = strings.ReplaceAll(t, "_", "")
	neg := false
	if strings.HasPrefix(t, "-") {
		neg, t = true, t[1:]
	} else {
		t = strings.TrimPrefix(t, "+")
	}
	t = strings.TrimPrefix(t, "$")
	t = strings.TrimSuffix(t, "%")
	mult := 1.0
	if n := len(t); n > 0 {
		switch unicode.ToLower(rune(t[n-1])) {
		case 'k':
			mult, t = 1e3, t[:n-1]
		case 'm':
			mult, t = 1e6, t[:n-1]
		case 'b':
			mult, t = 1e9, t[:n-1]
		}
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	if neg {
		v = -v
	}
	return v * mult, nil
}

// Tokenize splits a command line shell-style: whitespace separates words,
// single and double quotes group, backslash escapes the next rune.
func Tokenize(line string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inWord := false
	var quote rune
	esc := false
	for _, r := range line {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\' && quote != '\'':
			esc, inWord = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, inWord = r, true
		case unicode.IsSpace(r):
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, Errorf(CodeUsage, "unterminated %c quote", quote)
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}

// orderAliases are slash words that also carry the order side.
var orderAliases = map[string]string{"buy": "buy", "sell": "sell", "long": "buy", "short": "sell"}

// ParseLine parses one slash line ("/chart btc 4h --ma 20,50", leading slash
// optional) into a Call. Order aliases inject the side (/sell ETH 1).
func (r *Registry) ParseLine(line string) (Call, error) {
	words, err := Tokenize(strings.TrimSpace(line))
	if err != nil {
		return Call{}, err
	}
	if len(words) == 0 {
		return Call{}, Errorf(CodeUsage, "empty command")
	}
	head := strings.ToLower(strings.TrimPrefix(words[0], "/"))
	t, ok := r.BySlash(head)
	if !ok {
		t, ok = r.Get(head)
	}
	if !ok {
		e := Errorf(CodeUsage, "unknown command /%s", head)
		e.Hint = "/help (or `hlp schema`) lists the commands"
		if s := r.Suggest(head, 3); len(s) > 0 {
			e.Hint = "did you mean /" + strings.Join(s, ", /") + "?"
		}
		return Call{}, e
	}
	argv := words[1:]
	if side, isOrder := orderAliases[head]; isOrder && t.Name == "place_order" {
		argv = append([]string{side}, argv...)
	}
	args, err := ParseArgs(t, argv)
	if err != nil {
		return Call{}, err
	}
	return Call{Tool: t.Name, Args: args}, nil
}
