package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/core/coretest"
)

// readmeCommands returns every `hlp …` line inside the README's sh code
// blocks, with trailing comments and pipes removed.
func readmeCommands(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	comment := regexp.MustCompile(`\s+#\s.*$`)
	var out []string
	in := false
	for _, line := range strings.Split(string(b), "\n") {
		trim := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trim, "```sh"):
			in = true
			continue
		case strings.HasPrefix(trim, "```"):
			in = false
			continue
		}
		if !in {
			continue
		}
		cmd := strings.TrimSpace(comment.ReplaceAllString(trim, ""))
		if i := strings.Index(cmd, " | "); i >= 0 {
			cmd = cmd[:i]
		}
		if strings.HasPrefix(cmd, "hlp ") {
			out = append(out, cmd)
		}
	}
	return out
}

// Every command a reader can paste from the README must at least get past
// argument parsing (exit code 2 = usage error is a broken README). Data the
// offline fixtures lack (HIP-3 markets, named vaults) may fail otherwise.
func TestREADMECommandsParse(t *testing.T) {
	fx := isolate(t)
	cmds := readmeCommands(t, filepath.Join(coretest.RepoRoot(), "README.md"))
	if len(cmds) < 15 {
		t.Fatalf("found only %d README commands", len(cmds))
	}
	// long-running, streaming or slow commands: flags are parsed, not run
	parseOnly := map[string]bool{"paper": true, "arena": true, "mcp": true, "backtest": true, "pine": true}
	for _, line := range cmds {
		words, err := core.Tokenize(line)
		if err != nil {
			t.Errorf("%s: %v", line, err)
			continue
		}
		args := words[1:]
		if parseOnly[args[0]] {
			a := &app{io: IO{Getenv: func(string) string { return "" }}, g: &globals{}, reg: core.Default()}
			cmd, rest, err := a.rootCmd().Find(args)
			if err != nil || cmd.Name() == "hlp" {
				t.Errorf("%s: unknown command (%v)", line, err)
				continue
			}
			if err := cmd.ParseFlags(rest); err != nil {
				t.Errorf("%s: %v", line, err)
			}
			continue
		}
		r := runCLI(t, fx, false, args...)
		if r.code != 2 {
			continue
		}
		var e struct {
			Error core.Error `json:"error"`
		}
		_ = json.Unmarshal([]byte(strings.TrimSpace(r.stderr)), &e)
		switch e.Error.Code {
		case core.CodeUnknownCoin, core.CodeUnknownVault, core.CodeNotFound:
			// the offline fixtures do not carry every market and vault
		case core.CodeUsage:
			if !strings.Contains(e.Error.Message, "hlp.xyz data is unavailable") {
				t.Errorf("README command fails to parse: %s\n%s", line, r.stderr)
			}
			// offline fixtures have no hlp.xyz backend; the arguments parsed
		default:
			t.Errorf("README command fails to parse: %s\n%s", line, r.stderr)
		}
	}
}

// Every local image or link in the README must point at a file git tracks,
// or it renders broken on GitHub. HTML comments (placeholders for GIFs not
// rendered yet) are ignored.
func TestREADMELocalLinksTracked(t *testing.T) {
	root := coretest.RepoRoot()
	out, err := exec.Command("git", "-C", root, "ls-files").Output()
	if err != nil {
		t.Skipf("git ls-files unavailable: %v", err)
	}
	tracked := map[string]bool{}
	for _, f := range strings.Split(string(out), "\n") {
		tracked[f] = true
		for d := filepath.Dir(f); d != "." && d != "/"; d = filepath.Dir(d) {
			tracked[d] = true
		}
	}
	b, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := regexp.MustCompile(`(?s)<!--.*?-->`).ReplaceAllString(string(b), "")
	refs := regexp.MustCompile(`(?:src|href)="([^"]+)"|\]\(([^)\s]+)\)`).FindAllStringSubmatch(text, -1)
	checked := 0
	for _, m := range refs {
		ref := m[1] + m[2]
		if ref == "" || strings.Contains(ref, "://") || strings.HasPrefix(ref, "#") || strings.HasPrefix(ref, "mailto:") {
			continue
		}
		ref, _, _ = strings.Cut(ref, "#")
		ref = strings.TrimPrefix(filepath.Clean(ref), "./")
		checked++
		if !tracked[ref] {
			t.Errorf("README references %s, which git does not track", ref)
		}
	}
	if checked == 0 {
		t.Fatal("no local references found; the pattern is broken")
	}
}
