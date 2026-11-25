package cli

import (
	"bytes"
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/core/coretest"
)

// C-T2/C-T3: the price command, -q, markets positional, -c "price btc".
func TestPriceCommand(t *testing.T) {
	fx := isolate(t)
	r := runCLI(t, fx, false, "price", "btc", "eth", "--json")
	if r.code != 0 {
		t.Fatalf("exit %d %s", r.code, r.stderr)
	}
	data, _ := decodeEnvelope(t, r.stdout)
	rows := data.([]any)
	if len(rows) != 2 {
		t.Fatalf("rows %v", rows)
	}
	for _, row := range rows {
		if row.(map[string]any)["mid"].(float64) <= 0 {
			t.Fatalf("mid %v", row)
		}
	}
	r = runCLI(t, fx, false, "price", "btc", "--select", "mid", "-q")
	if r.code != 0 || strings.TrimSpace(r.stdout) != "73500" {
		t.Fatalf("bare mid: %d %q %s", r.code, r.stdout, r.stderr)
	}
	for _, alias := range []string{"mid", "quote"} {
		if r := runCLI(t, fx, false, alias, "btc"); r.code != 0 {
			t.Fatalf("%s alias: %d %s", alias, r.code, r.stderr)
		}
	}
	r = runCLI(t, fx, false, "-c", "price btc")
	if r.code != 0 || len(r.stdout) >= 1024 {
		t.Fatalf("-c price btc: exit %d, %d bytes", r.code, len(r.stdout))
	}
	r = runCLI(t, fx, false, "-c", "chart btc 15m")
	data, _ = decodeEnvelope(t, r.stdout)
	if r.code != 0 || len(data.(map[string]any)["candles"].([]any)) == 0 {
		t.Fatalf("-c chart btc still charts: %d %s", r.code, r.stderr)
	}
	r = runCLI(t, fx, false, "markets", "BTC")
	data, meta := decodeEnvelope(t, r.stdout)
	if r.code != 0 || len(data.([]any)) != 1 || meta["total"] == nil {
		t.Fatalf("markets BTC: %d %v %v", r.code, data, meta)
	}
	r = runCLI(t, fx, true, "price", "btc", "--plain")
	if r.code != 0 || !strings.Contains(r.stdout, "73,500") {
		t.Fatalf("price card: %q", r.stdout)
	}
}

// C-T6 over the CLI and AGENT-UX #7/#8, plus the output variants.
func TestShapingAndOutputVariants(t *testing.T) {
	fx := isolate(t)
	r := runCLI(t, fx, false, "account", coretest.Account, "--select", "positions.coin")
	data, _ := decodeEnvelope(t, r.stdout)
	ps := data.(map[string]any)["positions"].([]any)
	if r.code != 0 || len(ps) != 2 || ps[0].(map[string]any)["coin"] == nil {
		t.Fatalf("positions.coin: %d %v", r.code, data)
	}
	r = runCLI(t, fx, false, "markets", "--select", "nope")
	if r.code != 2 || errCode(t, r.stderr) != core.CodeUsage || !strings.Contains(r.stderr, "fields:") {
		t.Fatalf("select nope: %d %s", r.code, r.stderr)
	}
	r = runCLI(t, fx, false, "markets", "--select", "coin,nope", "--limit", "2")
	data, meta := decodeEnvelope(t, r.stdout)
	if r.code != 0 || len(data.([]any)) != 2 || meta["warnings"] == nil {
		t.Fatalf("partial select warns: %d %v", r.code, meta)
	}
	r = runCLI(t, fx, false, "markets", "--limit", "2", "--results-only", "--select", "coin")
	if r.code != 0 || !strings.HasPrefix(r.stdout, "[{") || strings.Contains(r.stdout, "meta") {
		t.Fatalf("results-only: %q", r.stdout)
	}
	r = runCLI(t, fx, false, "markets", "--limit", "3", "--csv", "--select", "coin,mark")
	rec, err := csv.NewReader(strings.NewReader(r.stdout)).ReadAll()
	if r.code != 0 || err != nil || len(rec) != 4 || rec[0][0] != "coin" || rec[0][1] != "mark" {
		t.Fatalf("csv: %d %v %q", r.code, err, r.stdout)
	}
	// without --select, columns follow the JSON field order: coin first
	r = runCLI(t, fx, false, "markets", "--limit", "2", "--csv")
	if rec, err := csv.NewReader(strings.NewReader(r.stdout)).ReadAll(); err != nil || len(rec) == 0 || rec[0][0] != "coin" {
		t.Fatalf("csv column order: %v %q", err, r.stdout[:min(len(r.stdout), 200)])
	}
	r = runCLI(t, fx, false, "account", coretest.Account, "--csv", "--select", "positions")
	if r.code != 0 || !strings.Contains(r.stdout, "positions") {
		t.Fatalf("csv object: %q", r.stdout)
	}
}

// C-T4 (offline part): JSON watch exits after one snapshot, the bounds end a
// stream with exit 0, -w on a non-streaming tool is a usage error.
func TestStreamBounds(t *testing.T) {
	fx := isolate(t)
	done := make(chan run, 1)
	go func() { done <- runCLI(t, fx, false, "watch", coretest.Account, "--json") }()
	select {
	case r := <-done:
		if r.code != 0 || strings.Count(strings.TrimSpace(r.stdout), "\n") != 0 {
			t.Fatalf("watch --json: %d %q", r.code, r.stdout)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("watch --json did not exit")
	}
	r := runCLI(t, fx, false, "book", "BTC", "--ndjson", "--max-events", "1")
	if r.code != 0 || strings.Count(strings.TrimSpace(r.stdout), "\n") != 0 {
		t.Fatalf("max-events 1: %d %q %s", r.code, r.stdout, r.stderr)
	}
	r = runCLI(t, fx, false, "book", "BTC", "-w", "--for", "5s")
	if r.code != 0 || r.stdout == "" {
		t.Fatalf("-w --for: %d %s", r.code, r.stderr)
	}
	r = runCLI(t, fx, false, "markets", "-w")
	if r.code != 2 || errCode(t, r.stderr) != core.CodeUsage {
		t.Fatalf("-w on markets: %d %s", r.code, r.stderr)
	}
	r = runCLI(t, fx, false, "book", "BTC", "--max-events", "-1")
	if r.code != 2 {
		t.Fatalf("negative max-events: %d", r.code)
	}
}

func TestStreamDecision(t *testing.T) {
	reg := core.Default()
	get := func(n string) core.Tool { tl, _ := reg.Lookup(n); return tl }
	cases := []struct {
		tool string
		args string
		m    Mode
		tty  bool
		want bool
	}{
		{"watch", `{}`, Mode{JSON: true}, false, false},
		{"watch", `{}`, Mode{}, true, true},
		{"watch", `{}`, Mode{JSON: true}, true, false},
		{"watch", `{}`, Mode{JSON: true, NDJSON: true}, false, true},
		{"get_orderbook", `{"live":true}`, Mode{JSON: true}, false, true},
		{"get_orderbook", `{}`, Mode{Live: true}, true, true},
		{"get_orderbook", `{}`, Mode{}, true, false},
		{"paper", `{"action":"start"}`, Mode{JSON: true}, false, false},
		{"paper", `{"action":"start"}`, Mode{}, true, true},
		{"place_order", `{}`, Mode{NDJSON: true}, false, false},
	}
	for _, c := range cases {
		if got := wantsStream(get(c.tool), []byte(c.args), c.m, c.tty); got != c.want {
			t.Errorf("%s %s %+v tty=%v: got %v", c.tool, c.args, c.m, c.tty, got)
		}
	}
}

// AGENT-UX #20: --ndjson progress goes to stderr as JSON lines.
// An offline call is too fast to report progress; this pins that --ndjson
// never gets the terminal's carriage-return progress line on stderr.
func TestNDJSONProgressStaysJSON(t *testing.T) {
	r := runCLI(t, isolate(t), false, "markets", "--ndjson", "--limit", "1")
	if r.code != 0 || strings.Contains(r.stderr, "\r") {
		t.Fatalf("ndjson stderr must stay JSON: %q", r.stderr)
	}
}

// C-T1: --height/--style/--zoom reach the card; default height follows the
// terminal.
func TestChartFlagsOnTTY(t *testing.T) {
	fx := isolate(t)
	r := runCLI(t, fx, true, "chart", "BTC", "15m", "--height", "40", "--style", "ascii", "--zoom", "1", "--plain")
	if r.code != 0 {
		t.Fatalf("exit %d %s", r.code, r.stderr)
	}
	n := len(strings.Split(strings.TrimRight(r.stdout, "\n"), "\n"))
	if n < 41 || n > 45 {
		t.Fatalf("--height 40 printed %d lines", n)
	}
	if strings.ContainsAny(r.stdout, "█▌▐▀▄│┃") {
		t.Fatal("--style ascii printed block glyphs")
	}
	// default on a 60-row terminal: min(30, 60-4) chart rows
	var out, errb bytes.Buffer
	env := map[string]string{"HLP_FIXTURES": fx}
	pio := IO{Out: &out, Err: &errb, OutTTY: true, ErrTTY: true, Width: 120, Height: 60, Getenv: func(k string) string { return env[k] }}
	if code := Run(context.Background(), []string{"chart", "BTC", "15m", "--plain"}, BuildInfo{}, pio); code != 0 {
		t.Fatalf("exit %d %s", code, errb.String())
	}
	if n := len(strings.Split(strings.TrimRight(out.String(), "\n"), "\n")); n < 31 || n > 35 {
		t.Fatalf("default height on 60 rows printed %d lines", n)
	}
	// the book fits a 24-row terminal, title included
	out.Reset()
	pio.Height = 24
	if code := Run(context.Background(), []string{"book", "BTC", "--plain"}, BuildInfo{}, pio); code != 0 {
		t.Fatalf("book exit %d %s", code, errb.String())
	}
	if n := len(strings.Split(strings.TrimRight(out.String(), "\n"), "\n")); n > 23 {
		t.Fatalf("book on 24 rows printed %d lines:\n%s", n, out.String())
	}
}

// C-T7: footers name their zone; chart.utc switches to UTC.
func TestCLITimesLabelled(t *testing.T) {
	fx := isolate(t)
	r := runCLI(t, fx, true, "price", "btc", "--plain")
	zone, _ := time.Now().Zone()
	if r.code != 0 || !strings.Contains(r.stdout, " "+zone) {
		t.Fatalf("local zone %q missing: %q", zone, r.stdout)
	}
	cfg := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfg, []byte("[chart]\nutc = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = runCLI(t, fx, true, "price", "btc", "--plain", "--config", cfg)
	if r.code != 0 || !strings.Contains(r.stdout, " UTC") {
		t.Fatalf("utc footer: %d %q %s", r.code, r.stdout, r.stderr)
	}
}

// COMP #13 / AGENT-UX #29: help has examples and exit codes, no trailing
// spaces, and no output-shaping flags on mutating tools.
func TestHelpTrimmedWithExamples(t *testing.T) {
	fx := isolate(t)
	r := runCLI(t, fx, false, "--help")
	if r.code != 0 || !strings.Contains(r.stdout, "Exit codes") {
		t.Fatalf("root help: %d %q", r.code, r.stdout)
	}
	for _, args := range [][]string{{"--help"}, {"price", "--help"}, {"order", "--help"}, {"vault", "--help"}} {
		r := runCLI(t, fx, false, args...)
		for i, l := range strings.Split(r.stdout, "\n") {
			if strings.HasSuffix(l, " ") || strings.HasSuffix(l, "\t") {
				t.Fatalf("%v line %d has trailing space: %q", args, i, l)
			}
		}
	}
	r = runCLI(t, fx, false, "price", "--help")
	if !strings.Contains(r.stdout, "hlp price btc eth") || !strings.Contains(r.stdout, "--max-events") {
		t.Fatalf("price help: %s", r.stdout)
	}
	r = runCLI(t, fx, false, "order", "--help")
	for _, f := range []string{"--select", "--limit", "--ndjson", "--max-events"} {
		if strings.Contains(r.stdout, f) {
			t.Fatalf("order help shows %s:\n%s", f, r.stdout)
		}
	}
	r = runCLI(t, fx, false, "vault", "growi", "--help")
	if !strings.Contains(r.stdout, "HLP,BTC,HYPE") {
		t.Fatalf("bench default: %s", r.stdout)
	}
}

func TestWriteErrorRetryable(t *testing.T) {
	var b bytes.Buffer
	WriteError(&b, &core.Error{Code: core.CodeRateLimited, Message: "429"})
	if !strings.Contains(b.String(), `"retryable":true`) {
		t.Fatal(b.String())
	}
	if core.ExitCode(&core.Error{Code: core.CodeRateLimited, Retryable: true}) != 4 {
		t.Fatal("retryable exit code")
	}
}
