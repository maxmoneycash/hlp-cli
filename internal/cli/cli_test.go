package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/core/coretest"
)

// isolate keeps tests away from the user's config, cache and keychain env.
func isolate(t *testing.T) (fixtures string) {
	t.Helper()
	home := t.TempDir()
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(k, home)
	}
	for _, k := range []string{"HLP_CONFIG", "HLP_NETWORK", "HLP_THEME", "HLP_ACCOUNT", "HLP_JSON", "HLP_AGENT_KEY"} {
		t.Setenv(k, "")
	}
	return coretest.Dir(t)
}

type run struct {
	code           int
	stdout, stderr string
}

func runCLI(t *testing.T, fixtures string, tty bool, args ...string) run {
	t.Helper()
	var out, errb bytes.Buffer
	env := map[string]string{"HLP_FIXTURES": fixtures}
	pio := IO{Out: &out, Err: &errb, OutTTY: tty, ErrTTY: tty, Width: 100, Getenv: func(k string) string { return env[k] }}
	code := Run(context.Background(), args, BuildInfo{Version: "1.2.3", Commit: "abcdef0", Date: "2026-11-15"}, pio)
	return run{code, out.String(), errb.String()}
}

func decodeEnvelope(t *testing.T, s string) (data any, meta map[string]any) {
	t.Helper()
	var env struct {
		Data any            `json:"data"`
		Meta map[string]any `json:"meta"`
	}
	if err := json.Unmarshal([]byte(s), &env); err != nil {
		t.Fatalf("stdout is not one JSON envelope: %v\n%s", err, s)
	}
	return env.Data, env.Meta
}

func errCode(t *testing.T, stderr string) string {
	t.Helper()
	var e struct {
		Error core.Error `json:"error"`
	}
	if err := json.Unmarshal([]byte(stderr), &e); err != nil {
		t.Fatalf("stderr is not an error envelope: %v\n%s", err, stderr)
	}
	return e.Error.Code
}

func TestVaultsJSONSelectLimit(t *testing.T) {
	fx := isolate(t)
	r := runCLI(t, fx, false, "vaults", "--json", "--select", "name,tvl", "--limit", "3")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	data, meta := decodeEnvelope(t, r.stdout)
	rows := data.([]any)
	if len(rows) != 3 {
		t.Fatalf("limit: %d rows", len(rows))
	}
	for _, row := range rows {
		m := row.(map[string]any)
		if len(m) != 2 || m["name"] == nil || m["tvl"] == nil {
			t.Fatalf("select: %v", m)
		}
	}
	if meta["source"] == "" || meta["asOf"] == nil {
		t.Fatalf("meta %v", meta)
	}
}

func TestUnknownCoinExit2(t *testing.T) {
	fx := isolate(t)
	r := runCLI(t, fx, false, "chart", "NOPE")
	if r.code != 2 || r.stdout != "" || errCode(t, r.stderr) != core.CodeUnknownCoin {
		t.Fatalf("exit %d stdout %q stderr %s", r.code, r.stdout, r.stderr)
	}
}

func TestOrderTradingDisabledExit3(t *testing.T) {
	fx := isolate(t)
	r := runCLI(t, fx, false, "order", "buy", "BTC", "1", "--yes")
	if r.code != 3 || errCode(t, r.stderr) != core.CodeTradingDisabled {
		t.Fatalf("exit %d stderr %s", r.code, r.stderr)
	}
	// alias + dry-run preview succeeds without keys
	r = runCLI(t, fx, false, "buy", "HYPE", "--usd", "100", "--dry-run")
	if r.code != 0 {
		t.Fatalf("dry-run exit %d: %s", r.code, r.stderr)
	}
	data, meta := decodeEnvelope(t, r.stdout)
	if data.(map[string]any)["dryRun"] != true || meta["source"] != "dry-run" {
		t.Fatalf("preview: %v %v", data, meta)
	}
}

func TestAutoJSONWhenPiped(t *testing.T) {
	fx := isolate(t)
	r := runCLI(t, fx, false, "markets", "--limit", "2")
	data, _ := decodeEnvelope(t, r.stdout)
	if r.code != 0 || len(data.([]any)) != 2 {
		t.Fatalf("exit %d %s", r.code, r.stdout)
	}
	// HLP_JSON forces JSON even on a terminal
	var out bytes.Buffer
	pio := IO{Out: &out, Err: &out, OutTTY: true, ErrTTY: true, Getenv: func(k string) string {
		return map[string]string{"HLP_FIXTURES": fx, "HLP_JSON": "1"}[k]
	}}
	if code := Run(context.Background(), []string{"markets", "--limit", "1"}, BuildInfo{}, pio); code != 0 {
		t.Fatalf("exit %d", code)
	}
	decodeEnvelope(t, out.String())
}

func TestTTYCardsAndHumanErrors(t *testing.T) {
	fx := isolate(t)
	r := runCLI(t, fx, true, "asset-id", "btc", "--plain")
	if r.code != 0 || strings.HasPrefix(strings.TrimSpace(r.stdout), "{") || r.stdout == "" {
		t.Fatalf("card output expected: %d %q %s", r.code, r.stdout, r.stderr)
	}
	r = runCLI(t, fx, true, "chart", "NOPE")
	if r.code != 2 || !strings.Contains(r.stderr, "UNKNOWN_COIN") || strings.HasPrefix(r.stderr, "{") {
		t.Fatalf("human error expected: %q", r.stderr)
	}
}

func TestUsageErrors(t *testing.T) {
	fx := isolate(t)
	for _, args := range [][]string{
		{"nosuchcommand"},
		{"chart", "BTC", "--nosuchflag"},
		{"chart", "BTC", "15m", "extra"},
		{"vaults", "--color", "purple"},
		{"chart", "BTC", "--bars", "lots"},
	} {
		r := runCLI(t, fx, false, args...)
		if r.code != 2 || errCode(t, r.stderr) != core.CodeUsage {
			t.Errorf("%v: exit %d %s", args, r.code, r.stderr)
		}
	}
	// the shell needs a terminal
	r := runCLI(t, fx, false)
	if r.code != 2 {
		t.Fatalf("no-TTY shell: %d %s", r.code, r.stderr)
	}
}

func TestCommandLineFlag(t *testing.T) {
	fx := isolate(t)
	r := runCLI(t, fx, false, "-c", "/chart btc --bars 30 --ma 5")
	if r.code != 0 {
		t.Fatalf("exit %d %s", r.code, r.stderr)
	}
	data, _ := decodeEnvelope(t, r.stdout)
	if c := data.(map[string]any)["candles"].([]any); len(c) != 30 {
		t.Fatalf("candles %d", len(c))
	}
	r = runCLI(t, fx, false, "-c", "vaults --sort sharpe", "--select", "name")
	if r.code != 0 {
		t.Fatalf("free text: %d %s", r.code, r.stderr)
	}
	r = runCLI(t, fx, false, "-c", "/chrat btc")
	if r.code != 2 || !strings.Contains(r.stderr, "chart") {
		t.Fatalf("typo hint: %s", r.stderr)
	}
}

func TestSchemaVersionConfig(t *testing.T) {
	fx := isolate(t)
	r := runCLI(t, fx, false, "schema", "vault")
	data, _ := decodeEnvelope(t, r.stdout)
	s := data.([]any)[0].(map[string]any)
	if r.code != 0 || s["name"] != "get_vault" || s["input"] == nil {
		t.Fatalf("schema: %v", s)
	}
	r = runCLI(t, fx, false, "version")
	data, _ = decodeEnvelope(t, r.stdout)
	if data.(map[string]any)["version"] != "1.2.3" {
		t.Fatalf("version: %s", r.stdout)
	}
	r = runCLI(t, fx, true, "version")
	if !strings.Contains(r.stdout, "hlp 1.2.3") {
		t.Fatalf("version tty: %s", r.stdout)
	}
	t.Setenv("HLP_AGENT_KEY", "0xdeadbeefsecret")
	r = runCLI(t, fx, false, "config")
	if r.code != 0 || strings.Contains(r.stdout, "deadbeef") {
		t.Fatalf("config must never print secrets: %s", r.stdout)
	}
	data, _ = decodeEnvelope(t, r.stdout)
	cv := data.(map[string]any)
	if cv["path"] == "" || cv["config"].(map[string]any)["network"] != "mainnet" {
		t.Fatalf("config view: %v", cv)
	}
	r = runCLI(t, fx, true, "config")
	if r.code != 0 || !strings.Contains(r.stdout, "network") || strings.Contains(r.stdout, "deadbeef") {
		t.Fatalf("config tty: %s", r.stdout)
	}
}

func TestStreamingNDJSON(t *testing.T) {
	fx := isolate(t)
	// offline there is no WebSocket: the stream emits the snapshot and ends
	r := runCLI(t, fx, false, "watch", coretest.Account, "--select", "equity")
	if r.code != 0 {
		t.Fatalf("exit %d %s", r.code, r.stderr)
	}
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) != 1 {
		t.Fatalf("ndjson lines: %q", r.stdout)
	}
	data, _ := decodeEnvelope(t, lines[0])
	if data.(map[string]any)["equity"].(float64) != 15000 {
		t.Fatalf("data %v", data)
	}
}

func TestResolveMode(t *testing.T) {
	none := func(string) string { return "" }
	cases := []struct {
		json, ndjson, plain, tty bool
		env                      string
		wantJSON, wantPlain      bool
	}{
		{tty: true},
		{tty: false, wantJSON: true},
		{tty: false, plain: true, wantPlain: true},
		{tty: true, json: true, wantJSON: true},
		{tty: true, ndjson: true, wantJSON: true},
		{tty: true, env: "1", wantJSON: true},
	}
	for i, c := range cases {
		get := none
		if c.env != "" {
			get = func(k string) string { return map[string]string{"HLP_JSON": c.env}[k] }
		}
		m, err := ResolveMode(c.json, c.ndjson, c.plain, " a , b.c ,", 5, "auto", c.tty, get)
		if err != nil || m.JSON != c.wantJSON || m.Plain != c.wantPlain || len(m.Select) != 2 || m.Limit != 5 {
			t.Errorf("case %d: %+v %v", i, m, err)
		}
	}
	if m, _ := ResolveMode(false, false, false, "", 0, "never", true, none); !m.Plain {
		t.Error("--color never on a TTY means plain")
	}
	if _, err := ResolveMode(false, false, false, "", -1, "auto", true, none); err == nil {
		t.Error("negative limit")
	}
}

func TestShape(t *testing.T) {
	type inner struct {
		Last float64
		Coin string `json:"coin"`
	}
	type row struct {
		Name    string `json:"name"`
		TVL     uint64 `json:"tvl"`
		Summary inner  `json:"summary"`
	}
	rows := []row{{"a", 18446744073709551615, inner{1, "BTC"}}, {"b", 2, inner{2, "ETH"}}, {"c", 3, inner{3, "SOL"}}}
	got, err := core.Shape(rows, []string{"name", "tvl", "summary.last", "missing"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	want := `[{"name":"a","summary":{"last":1},"tvl":18446744073709551615},{"name":"b","summary":{"last":2},"tvl":2}]`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
	obj, _ := core.Shape(rows[0], []string{"summary.coin"}, 0)
	if b, _ := json.Marshal(obj); string(b) != `{"summary":{"coin":"BTC"}}` {
		t.Fatalf("object: %s", b)
	}
	same, _ := core.Shape(rows, nil, 0)
	if len(same.([]row)) != 3 {
		t.Fatal("no-op shape must return data unchanged")
	}
	if got := limitSlice([]int{1, 2, 3}, 2, false).([]int); len(got) != 2 {
		t.Fatal("limitSlice")
	}
	if got := limitSlice([]int{1, 2, 3}, 2, true).([]int); got[0] != 2 || got[1] != 3 {
		t.Fatalf("tail limit kept %v", got)
	}
	if got := limitSlice("x", 2, false); got != "x" {
		t.Fatal("limitSlice non-slice")
	}
}

func TestWriteError(t *testing.T) {
	var b bytes.Buffer
	WriteError(&b, &core.Error{Code: core.CodeRiskLimit, Message: "cap", Hint: "lower size"})
	if !strings.Contains(b.String(), `"code":"RISK_LIMIT"`) || !strings.Contains(b.String(), `"hint":"lower size"`) {
		t.Fatal(b.String())
	}
	b.Reset()
	writeHumanError(&b, &core.Error{Code: core.CodeUsage, Message: "bad", Details: []core.PineDiag{{Line: 3, Col: 4, Msg: "nope"}}})
	if !strings.Contains(b.String(), "3:4") {
		t.Fatal(b.String())
	}
}

func TestIsTTY(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if IsTTY(f) || IsTTY(nil) {
		t.Fatal("a regular file is not a TTY")
	}
}

// ---- end to end: the built binary ----

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

func hlpBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the binary")
	}
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "hlp-e2e")
		if err != nil {
			buildErr = err
			return
		}
		binPath = filepath.Join(dir, "hlp")
		gobin := filepath.Join(runtime.GOROOT(), "bin", "go")
		cmd := exec.Command(gobin, "build", "-o", binPath, "./cmd/hlp")
		cmd.Dir = coretest.RepoRoot()
		out, err := cmd.CombinedOutput()
		if err != nil {
			buildErr = &core.Error{Code: core.CodeInternal, Message: string(out)}
		}
	})
	if buildErr != nil {
		t.Fatalf("build: %v", buildErr)
	}
	return binPath
}

func TestMain(m *testing.M) {
	code := m.Run()
	if binPath != "" {
		_ = os.RemoveAll(filepath.Dir(binPath))
	}
	os.Exit(code)
}

func exe(t *testing.T, fx string, args ...string) run {
	t.Helper()
	cmd := exec.Command(hlpBinary(t), args...)
	home := t.TempDir()
	cmd.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + home, "XDG_CACHE_HOME=" + home, "XDG_DATA_HOME=" + home, "HLP_FIXTURES=" + fx, "TZ=UTC"}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return run{code, out.String(), errb.String()}
}

func TestE2EBinary(t *testing.T) {
	fx := isolate(t)
	r := exe(t, fx, "vaults", "--json", "--select", "name,tvl", "--limit", "3")
	data, _ := decodeEnvelope(t, r.stdout)
	if r.code != 0 || len(data.([]any)) != 3 || len(data.([]any)[0].(map[string]any)) != 2 {
		t.Fatalf("vaults: %d %s %s", r.code, r.stdout, r.stderr)
	}
	r = exe(t, fx, "chart", "NOPE")
	if r.code != 2 || r.stdout != "" || errCode(t, r.stderr) != core.CodeUnknownCoin {
		t.Fatalf("chart NOPE: %d %q %s", r.code, r.stdout, r.stderr)
	}
	r = exe(t, fx, "order", "buy", "BTC", "1", "--yes")
	if r.code != 3 || errCode(t, r.stderr) != core.CodeTradingDisabled {
		t.Fatalf("order: %d %s", r.code, r.stderr)
	}
	r = exe(t, fx, "markets") // piped stdout: JSON without --json
	if r.code != 0 {
		t.Fatalf("markets: %d %s", r.code, r.stderr)
	}
	decodeEnvelope(t, r.stdout)
}

func TestAuthStatusNeverPrintsKey(t *testing.T) {
	fx := isolate(t)
	// the Hyperliquid Python SDK's public test key
	const testKey = "0x0123456789012345678901234567890123456789012345678901234567890123"
	t.Setenv("HLP_AGENT_KEY", testKey)
	r := runCLI(t, fx, false, "auth", "status", "--json")
	if r.code != 0 || strings.Contains(r.stdout, "0123456789012345678901234567890123456789012345678901234567890123") {
		t.Fatalf("auth status leaked or failed: %d %s %s", r.code, r.stdout, r.stderr)
	}
	data, _ := decodeEnvelope(t, r.stdout)
	st := data.(map[string]any)
	if st["agentKey"] != "set" || st["source"] != "env" || !strings.HasPrefix(st["address"].(string), "0x") {
		t.Fatalf("status %v", st)
	}
	// set reads stdin, refuses empty input before touching the keychain
	var out, errb bytes.Buffer
	pio := IO{In: strings.NewReader("\n"), Out: &out, Err: &errb, Getenv: func(string) string { return "" }}
	if code := Run(context.Background(), []string{"auth", "set"}, BuildInfo{}, pio); code != 2 {
		t.Fatalf("empty key exit %d: %s", code, errb.String())
	}
}
