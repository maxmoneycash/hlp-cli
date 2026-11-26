package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func runEnv(t *testing.T, fixtures string, env map[string]string, args ...string) run {
	t.Helper()
	var out, errb bytes.Buffer
	e := map[string]string{"HLP_FIXTURES": fixtures}
	for k, v := range env {
		e[k] = v
	}
	pio := IO{Out: &out, Err: &errb, Width: 100, Getenv: func(k string) string { return e[k] }}
	code := Run(context.Background(), args, BuildInfo{Version: "1.2.3"}, pio)
	return run{code, out.String(), errb.String()}
}

// Under a coding agent a tearsheet comes out agent-sized, every cut named;
// --full and HLP_COMPACT=0 give the whole payload back.
func TestAgentModeCompactsUnderAnAgent(t *testing.T) {
	fx := isolate(t)
	agent := map[string]string{"CLAUDECODE": "1"}
	small := runEnv(t, fx, agent, "vault", "hlp")
	full := runEnv(t, fx, agent, "vault", "hlp", "--full")
	off := runEnv(t, fx, map[string]string{"CLAUDECODE": "1", "HLP_COMPACT": "0"}, "vault", "hlp")
	plain := runEnv(t, fx, nil, "vault", "hlp")
	flag := runEnv(t, fx, nil, "vault", "hlp", "--compact")
	for name, r := range map[string]run{"agent": small, "full": full, "off": off, "plain": plain, "flag": flag} {
		if r.code != 0 {
			t.Fatalf("%s: exit %d: %s", name, r.code, r.stderr)
		}
	}
	if len(small.stdout) >= len(full.stdout) {
		t.Fatalf("agent output %d bytes, full %d: nothing was cut", len(small.stdout), len(full.stdout))
	}
	if dataOf(t, off) != dataOf(t, full) || dataOf(t, plain) != dataOf(t, full) {
		t.Error("HLP_COMPACT=0 or no agent should print the full payload")
	}
	if dataOf(t, flag) != dataOf(t, small) {
		t.Error("--compact should match agent mode")
	}
	_, meta := decodeEnvelope(t, small.stdout)
	if meta["truncated"] != true || !strings.Contains(strings.Join(toStrings(meta["warnings"]), "\n"), "--full") {
		t.Errorf("cuts not reported: %v", meta)
	}
	if r := runEnv(t, fx, nil, "vault", "hlp", "--compact", "--full"); r.code != 2 {
		t.Errorf("--compact --full: exit %d, want 2", r.code)
	}
}

// Schemas are never compacted: their properties named curve or markers
// are descriptions, not series.
func TestAgentModeKeepsSchemas(t *testing.T) {
	fx := isolate(t)
	r := runEnv(t, fx, map[string]string{"CLAUDECODE": "1"}, "schema", "run_backtest")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, `"markers"`) {
		t.Error("schema lost its markers property under agent mode")
	}
}

func TestHelpJSONIsTheSchema(t *testing.T) {
	fx := isolate(t)
	for _, args := range [][]string{{"price", "--help", "--json"}, {"help", "price", "--json"}} {
		r := runEnv(t, fx, nil, args...)
		data, _ := decodeEnvelope(t, r.stdout)
		rows, _ := data.([]any)
		if len(rows) != 1 || rows[0].(map[string]any)["name"] != "get_price" {
			t.Errorf("%v: want the get_price schema, got %.200s", args, r.stdout)
		}
	}
	if r := runEnv(t, fx, nil, "price", "--help"); strings.HasPrefix(strings.TrimSpace(r.stdout), "{") {
		t.Error("plain --help became JSON")
	}
}

func TestUnknownCommandSuggests(t *testing.T) {
	fx := isolate(t)
	for in, want := range map[string]string{"chrat": "hlp chart", "backtst": "hlp backtest", "/vaults": "drop the slash"} {
		r := runEnv(t, fx, nil, in)
		if r.code != 2 {
			t.Errorf("%s: exit %d", in, r.code)
		}
		var e struct {
			Error struct{ Hint string } `json:"error"`
		}
		_ = json.Unmarshal([]byte(r.stderr), &e)
		if !strings.Contains(e.Error.Hint, want) {
			t.Errorf("%s: hint %q lacks %q", in, e.Error.Hint, want)
		}
	}
}

func toStrings(v any) []string {
	xs, _ := v.([]any)
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

var asOfRe = regexp.MustCompile(`"asOf":"[^"]*"`)

// dataOf is a result's data as JSON (meta carries a fresh asOf per run).
func dataOf(t *testing.T, r run) string {
	t.Helper()
	data, _ := decodeEnvelope(t, r.stdout)
	b, _ := json.Marshal(data)
	return asOfRe.ReplaceAllString(string(b), `"asOf":""`)
}
