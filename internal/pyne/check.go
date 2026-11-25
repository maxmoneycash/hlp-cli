package pyne

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Diagnostic is one positioned finding. Line and Col are 1-based (PYNE's
// 0-based column is converted).
type Diagnostic struct {
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Severity string `json:"severity"` // error | warning | info
	Code     string `json:"code,omitempty"`
	Msg      string `json:"message"`
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%d:%d: %s: %s", d.Line, d.Col, d.Severity, d.Msg)
}

// ScriptError is a script that PYNE rejected (parse) or failed to run.
type ScriptError struct {
	Kind        string // parse | runtime | timeout
	Msg         string
	Diagnostics []Diagnostic // errors with line:col when known
}

func (e *ScriptError) Error() string {
	for _, d := range e.Diagnostics {
		if d.Severity == "error" {
			return fmt.Sprintf("pyne: %d:%d: %s", d.Line, d.Col, d.Msg)
		}
	}
	return fmt.Sprintf("pyne: %s error: %s", e.Kind, firstLine(e.Msg))
}

// Errors returns only error-severity diagnostics.
func Errors(ds []Diagnostic) []Diagnostic {
	var out []Diagnostic
	for _, d := range ds {
		if d.Severity == "error" {
			out = append(out, d)
		}
	}
	return out
}

// Check runs `pyne diagnostics` (parse errors + lint) on src. A script with
// errors is not a Go error: inspect Errors(diags). err is reserved for
// pyne itself failing.
func (e *Engine) Check(ctx context.Context, src string) ([]Diagnostic, error) {
	if len(src) > MaxSourceBytes {
		return nil, fmt.Errorf("pyne: script is %d bytes, limit %d", len(src), MaxSourceBytes)
	}
	dir, err := os.MkdirTemp("", "hlp-pyne-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "script.pine")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		return nil, err
	}
	out, _, err := e.run(ctx, nil, "diagnostics", path)
	var ee *ExitError
	if err != nil && !errors.As(err, &ee) {
		return nil, err
	}
	var env struct {
		OK          bool   `json:"ok"`
		Error       string `json:"error"`
		Diagnostics []struct {
			Code     string `json:"code"`
			Severity string `json:"severity"`
			Message  string `json:"message"`
			Line     int    `json:"line"`
			Column   int    `json:"column"`
		} `json:"diagnostics"`
	}
	if jerr := json.Unmarshal(out, &env); jerr != nil {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("pyne: diagnostics: unexpected output %q", firstLine(string(out)))
	}
	if !env.OK && len(env.Diagnostics) == 0 {
		return nil, fmt.Errorf("pyne: diagnostics: %s", firstLine(env.Error))
	}
	type key struct {
		line, col int
		sev, msg  string
	}
	seen := map[key]bool{}
	// PYNE reports "unexpected end of input" one line past the last one.
	nLines := max(len(strings.Split(strings.TrimRight(src, "\n"), "\n")), 1)
	var ds []Diagnostic
	for _, d := range env.Diagnostics {
		msg := strings.TrimPrefix(d.Message, "Syntax error: ")
		k := key{d.Line, d.Column, d.Severity, msg}
		if seen[k] { // PYNE reports a parse error twice ("parse" + lint "E001")
			continue
		}
		seen[k] = true
		line := min(max(d.Line, 1), nLines)
		ds = append(ds, Diagnostic{Line: line, Col: d.Column + 1, Severity: d.Severity, Code: d.Code, Msg: msg})
	}
	sort.SliceStable(ds, func(i, j int) bool {
		if ds[i].Line != ds[j].Line {
			return ds[i].Line < ds[j].Line
		}
		return ds[i].Col < ds[j].Col
	})
	return ds, nil
}
