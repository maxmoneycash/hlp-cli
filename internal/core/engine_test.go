package core_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/core/coretest"
	"github.com/maxmoneycash/hlp/internal/pineforge"
	"github.com/maxmoneycash/hlp/internal/pyne"
)

// The engine tests run stand-in binaries (shell scripts) for pyne and the
// container CLI: they log every call, so a test can assert that nothing was
// executed. No Python, Docker or network is needed.

func shellStandIn(t *testing.T, name, body string) (bin, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stand-in")
	}
	dir := t.TempDir()
	log = filepath.Join(dir, name+".log")
	bin = filepath.Join(dir, name)
	script := "#!/bin/sh\necho \"$@\" >> \"" + log + "\"\n" + body
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

// fakePyne answers --version, diagnostics and execute (the recorded
// hoox-pyne 0.7.0 envelope, with the instrumented script's nonce).
func fakePyne(t *testing.T) (log string) {
	t.Helper()
	fixture := filepath.Join(coretest.RepoRoot(), "internal", "pyne", "testdata", "execute_ok.json")
	bin, log := shellStandIn(t, "pyne", `case "$1" in
--version) echo 0.7.0 ;;
diagnostics) echo '{"ok": true, "diagnostics": [{"code":"W1","severity":"warning","message":"unused","line":3,"column":0}]}' ;;
execute)
  n=$(grep -o 'hlp\.[0-9a-f]*\.bar|' "$2" | head -1 | sed 's/^hlp\.\([0-9a-f]*\)\.bar|$/\1/')
  sed -e "s/hlp\.bar|/hlp.$n.bar|/g" -e "s/hlp\.trade|/hlp.$n.trade|/g" "`+fixture+`" ;;
*) exit 2 ;;
esac
`)
	t.Setenv(pyne.EnvBin, bin)
	return log
}

// fakeDocker has the image and prints the synthetic PineForge report.
func fakeDocker(t *testing.T) (log string) {
	t.Helper()
	fixture := filepath.Join(coretest.RepoRoot(), "internal", "pineforge", "testdata", "report_synthetic.json")
	bin, log := shellStandIn(t, "docker", `case "$1" in
image) echo sha256:abc ;;
run) cat "`+fixture+`" ;;
*) exit 1 ;;
esac
`)
	t.Setenv(pineforge.EnvDocker, bin)
	t.Setenv(pineforge.EnvImage, "")
	return log
}

func invokeAs(env *core.Env, source, tool, args string) (*core.Result, error) {
	ctx := core.WithSource(context.Background(), source)
	return core.Default().Invoke(ctx, env, core.Call{Tool: tool, Args: json.RawMessage(args)})
}

func errOf(t *testing.T, err error) *core.Error {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	return core.AsError(err)
}

func sample(name string) string {
	return filepath.Join(coretest.RepoRoot(), "testdata", "pine-samples", name)
}

func calls(log string) string {
	b, _ := os.ReadFile(log)
	return string(b)
}

// B-T1: --engine pyne runs the fake pyne and says so; with no binary it is
// a usage error carrying the install hint.
func TestEnginePyneBacktest(t *testing.T) {
	log := fakePyne(t)
	env := coretest.Env(t, nil)
	res, err := invokeAs(env, "cli", "run_backtest", `{"file":"`+sample("02-macd-cross.pine")+`","coin":"BTC","interval":"15m","bench":["BTC"],"engine":"pyne"}`)
	if err != nil {
		t.Fatalf("pyne backtest: %v", err)
	}
	d := res.Data.(*core.BacktestData)
	if d.Engine != core.EnginePyne || d.EngineVersion != "0.7.0" || res.Meta.Engine != core.EnginePyne {
		t.Fatalf("engine %q %q meta %q", d.Engine, d.EngineVersion, res.Meta.Engine)
	}
	// AGENT-UX #24: the short-window warning applies to external engines.
	if !containsPrefix(res.Meta.Warnings, "annualized metrics over") {
		t.Errorf("no short-window warning on a pyne backtest: %v", res.Meta.Warnings)
	}
	if !contains(res.Meta.Warnings, "engine: PYNE 0.7.0") || !containsPrefix(d.Warnings, "hlp fees, slippage, funding") {
		t.Fatalf("warnings meta=%v data=%v", res.Meta.Warnings, d.Warnings)
	}
	if len(d.Tearsheet.Curve) < 2 || len(d.Fills) == 0 {
		t.Fatalf("no curve or fills: %d %d", len(d.Tearsheet.Curve), len(d.Fills))
	}
	if b, _ := json.Marshal(d); !strings.Contains(string(b), `"engine":"pyne"`) {
		t.Fatalf("json %s", b[:200])
	}
	if !strings.Contains(calls(log), "execute ") {
		t.Fatalf("pyne not executed: %q", calls(log))
	}

	// pine check through pyne: warnings listed, engine reported.
	res, err = invokeAs(env, "cli", "pine_check", `{"file":"`+sample("02-macd-cross.pine")+`","engine":"pyne"}`)
	if err != nil {
		t.Fatalf("pyne check: %v", err)
	}
	pc := res.Data.(*core.PineCheckData)
	if pc.Engine != core.EnginePyne || pc.Kind != "strategy" || pc.Version != "v6" || len(pc.Warnings) != 1 || !strings.HasPrefix(pc.Warnings[0], "3:1:") {
		t.Fatalf("check %+v", pc)
	}

	t.Setenv(pyne.EnvBin, filepath.Join(t.TempDir(), "no-pyne"))
	_, err = invokeAs(env, "cli", "run_backtest", `{"file":"`+sample("02-macd-cross.pine")+`","coin":"BTC","engine":"pyne"}`)
	e := errOf(t, err)
	if e.Code != core.CodeUsage || core.ExitCode(err) != 2 || e.Hint != pyne.InstallHint {
		t.Fatalf("missing pyne: %+v", e)
	}
}

// B-T2: PineForge needs the licence (exit 3, notice in details), then runs
// with the fake docker; paper refuses it (exit 2).
func TestEnginePineForge(t *testing.T) {
	log := fakeDocker(t)
	env := coretest.Env(t, nil)
	args := `{"file":"` + sample("02-macd-cross.pine") + `","coin":"BTC","interval":"15m","bench":["BTC"],"engine":"pineforge"}`
	_, err := invokeAs(env, "cli", "run_backtest", args)
	e := errOf(t, err)
	det, _ := e.Details.(map[string]any)
	if e.Code != core.CodeConfirmRequired || core.ExitCode(err) != 3 || det["licenseNotice"] != pineforge.LicenseNotice {
		t.Fatalf("no licence: %+v", e)
	}
	if calls(log) != "" {
		t.Fatalf("docker ran before the licence was accepted: %q", calls(log))
	}
	accepted := strings.TrimSuffix(args, "}") + `,"acceptPineforgeLicense":true}`
	res, err := invokeAs(env, "cli", "run_backtest", accepted)
	if err != nil {
		t.Fatalf("pineforge backtest: %v", err)
	}
	d := res.Data.(*core.BacktestData)
	if d.Engine != core.EnginePineForge || res.Meta.Engine != core.EnginePineForge || d.EngineVersion != "1.12.0" {
		t.Fatalf("engine %q %q", d.Engine, d.EngineVersion)
	}
	if !strings.Contains(calls(log), "--pull=never --network=none") {
		t.Fatalf("docker argv %q", calls(log))
	}
	// config acceptance works too
	env.Config.PineForge.AcceptLicense = true
	if _, err := invokeAs(env, "cli", "run_backtest", args); err != nil {
		t.Fatalf("config acceptance: %v", err)
	}
	for _, c := range []struct{ tool, args string }{
		{"paper", `{"action":"start","file":"` + sample("02-macd-cross.pine") + `","coin":"BTC","engine":"pineforge"}`},
		{"arena", `{"entrants":["` + sample("02-macd-cross.pine") + `@BTC","` + sample("03-rsi2-mean-reversion.pine") + `@BTC"],"engine":"pineforge"}`},
		{"pine_check", `{"file":"` + sample("02-macd-cross.pine") + `","engine":"pineforge"}`},
	} {
		_, err := invokeAs(env, "cli", c.tool, c.args)
		if e := errOf(t, err); e.Code != core.CodeUsage || core.ExitCode(err) != 2 {
			t.Fatalf("%s with pineforge: %+v", c.tool, e)
		}
	}
}

// B-T3: for MCP and agent callers auto is builtin and nothing is executed;
// an explicit external engine needs strategy.allow_external_engines.
func TestEngineSubprocessPolicy(t *testing.T) {
	log := fakePyne(t)
	dlog := fakeDocker(t)
	env := coretest.Env(t, nil)
	env.Config.PineForge.AcceptLicense = true
	file := strategyCopy(t, env, "02-macd-cross.pine") // agents read DataDir/strategies
	for _, src := range []string{"mcp", "agent", "unknown"} {
		res, err := invokeAs(env, src, "run_backtest", `{"file":"`+file+`","coin":"BTC","interval":"15m","bench":["BTC"],"engine":"auto"}`)
		if err != nil {
			t.Fatalf("%s auto: %v", src, err)
		}
		if d := res.Data.(*core.BacktestData); d.Engine != core.EngineBuiltin {
			t.Fatalf("%s auto resolved to %q", src, d.Engine)
		}
		for _, eng := range []string{"pyne", "pineforge"} {
			_, err := invokeAs(env, src, "run_backtest", `{"file":"`+file+`","coin":"BTC","engine":"`+eng+`"}`)
			if e := errOf(t, err); e.Code != core.CodeSafety {
				t.Fatalf("%s %s: %+v", src, eng, e)
			}
		}
	}
	if calls(log) != "" || calls(dlog) != "" {
		t.Fatalf("an external engine ran for an agent: pyne %q docker %q", calls(log), calls(dlog))
	}
	// The CLI's auto picks the installed PYNE.
	res, err := invokeAs(env, "cli", "run_backtest", `{"file":"`+file+`","coin":"BTC","interval":"15m","bench":["BTC"],"engine":"auto"}`)
	if err != nil || res.Data.(*core.BacktestData).Engine != core.EnginePyne {
		t.Fatalf("cli auto: %v", err)
	}
	// Opt-in config lets MCP use them.
	env.Config.Strategy.AllowExternalEngines = true
	res, err = invokeAs(env, "mcp", "run_backtest", `{"file":"`+file+`","coin":"BTC","interval":"15m","bench":["BTC"],"engine":"pyne"}`)
	if err != nil || res.Data.(*core.BacktestData).Engine != core.EnginePyne {
		t.Fatalf("mcp pyne with opt-in: %v", err)
	}
	// Default and config engine.
	env.Config.Strategy.Engine = "nope"
	_, err = invokeAs(env, "cli", "run_backtest", `{"file":"`+file+`","coin":"BTC"}`)
	if e := errOf(t, err); e.Code != core.CodeUsage {
		t.Fatalf("bad config engine: %+v", e)
	}
}

// S8: agent and MCP callers read strategies only under the working
// directory or DataDir/strategies, and only regular files.
func TestStrategyFileRoots(t *testing.T) {
	env := coretest.Env(t, nil)
	outside := filepath.Join(t.TempDir(), "x.pine")
	src, _ := os.ReadFile(sample("02-macd-cross.pine"))
	if err := os.WriteFile(outside, src, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"mcp", "agent"} {
		_, err := invokeAs(env, s, "pine_check", `{"file":"`+outside+`"}`)
		if e := errOf(t, err); e.Code != core.CodeSafety {
			t.Fatalf("%s outside roots: %+v", s, e)
		}
	}
	if _, err := invokeAs(env, "cli", "pine_check", `{"file":"`+outside+`"}`); err != nil {
		t.Fatalf("cli may read anywhere: %v", err)
	}
	// DataDir/strategies is allowed.
	dir := filepath.Join(env.Paths.DataDir, "strategies")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(dir, "x.pine")
	if err := os.WriteFile(inside, src, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := invokeAs(env, "mcp", "pine_check", `{"file":"`+inside+`"}`); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	// A symlink inside a root that points outside is refused.
	link := filepath.Join(dir, "escape.pine")
	if err := os.Symlink(outside, link); err == nil {
		_, err := invokeAs(env, "mcp", "pine_check", `{"file":"`+link+`"}`)
		if e := errOf(t, err); e.Code != core.CodeSafety {
			t.Fatalf("symlink escape: %+v", e)
		}
	}
	// Not a regular file.
	if _, err := os.Stat("/dev/zero"); err == nil {
		_, err := invokeAs(env, "cli", "pine_check", `{"file":"/dev/zero.pine"}`)
		if e := errOf(t, err); e.Code != core.CodeUsage {
			t.Fatalf("missing file: %+v", e)
		}
	}
}

// strategyCopy copies a research sample into DataDir/strategies, where
// agent and MCP callers may read it.
func strategyCopy(t *testing.T, env *core.Env, name string) string {
	t.Helper()
	src, err := os.ReadFile(sample(name))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(env.Paths.DataDir, "strategies")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, src, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func containsPrefix(xs []string, p string) bool {
	for _, x := range xs {
		if strings.HasPrefix(x, p) {
			return true
		}
	}
	return false
}

// S8 follow-up: a broad working directory (/, $HOME) is not a root for
// untrusted callers, refusals are one generic error (no existence or size
// oracle), parse errors do not echo file content, and the embedded
// examples and inline source work from anywhere.
func TestStrategyFileRootsBroadCwd(t *testing.T) {
	env := coretest.Env(t, nil)
	home := t.TempDir()
	t.Setenv("HOME", home)
	secret := filepath.Join(home, "secret")
	if err := os.MkdirAll(secret, 0o700); err != nil {
		t.Fatal(err)
	}
	creds := filepath.Join(secret, "creds.json")
	if err := os.WriteFile(creds, []byte(`{"client_secret":"hunter2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(home)
	var msgs []string
	for _, s := range []string{"mcp", "agent", ""} {
		for _, f := range []string{creds, filepath.Join(secret, "missing.json"), "/etc/hosts.json"} {
			_, err := invokeAs(env, s, "run_backtest", `{"file":"`+f+`"}`)
			e := errOf(t, err)
			if e.Code != core.CodeSafety || strings.Contains(e.Message, "client_secret") || strings.Contains(e.Message, "hunter2") {
				t.Fatalf("%q %s: %+v", s, f, e)
			}
			msgs = append(msgs, strings.ReplaceAll(e.Message, f, "F"))
		}
	}
	for _, m := range msgs[1:] {
		if m != msgs[0] {
			t.Fatalf("refusals differ: %q vs %q", m, msgs[0])
		}
	}
	// The embedded examples and inline source need no file access.
	if _, err := invokeAs(env, "mcp", "pine_check", `{"file":"example:ema-cross"}`); err != nil {
		t.Fatalf("example: %v", err)
	}
	src, _ := json.Marshal(string(mustRead(t, sample("02-macd-cross.pine"))))
	if _, err := invokeAs(env, "mcp", "pine_check", `{"source":`+string(src)+`}`); err != nil {
		t.Fatalf("inline source: %v", err)
	}
	if _, err := invokeAs(env, "mcp", "pine_check", `{"file":"example:../../etc/passwd.pine"}`); err == nil {
		t.Fatal("example path escape")
	}
	// A parse error inside an allowed root does not echo content to agents.
	dir := filepath.Join(env.Paths.DataDir, "strategies")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"client_secret":"hunter2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := invokeAs(env, "mcp", "run_backtest", `{"file":"`+bad+`"}`)
	if e := errOf(t, err); strings.Contains(e.Message, "client_secret") {
		t.Fatalf("parse error leaks content: %+v", e)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
