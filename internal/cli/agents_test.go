package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/core/coretest"
	hlpskill "github.com/maxmoneycash/hlp/skills/hlp"
)

// agentsTools reads the tool table of AGENTS.md: rows of
// | `tool` [†] | `hlp command` | use |.
func agentsTools(t *testing.T) map[string]struct {
	command  string
	mutating bool
} {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(coretest.RepoRoot(), "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile("^\\| `([a-z_]+)`( †)? \\| `hlp ([^`]+)` \\|")
	out := map[string]struct {
		command  string
		mutating bool
	}{}
	for _, line := range strings.Split(string(b), "\n") {
		m := row.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if _, dup := out[m[1]]; dup {
			t.Errorf("AGENTS.md lists %s twice", m[1])
		}
		out[m[1]] = struct {
			command  string
			mutating bool
		}{m[3], m[2] != ""}
	}
	return out
}

// The AGENTS.md tool table must match the registry: every tool once, with
// its command and its trading mark (F-T5, AGENT-UX #31).
func TestAgentsToolTableMatchesRegistry(t *testing.T) {
	listed := agentsTools(t)
	if len(listed) == 0 {
		t.Fatal("no tool rows found in AGENTS.md; the pattern is broken")
	}
	reg := core.Default()
	for _, tool := range reg.All() {
		row, ok := listed[tool.Name]
		switch {
		case !ok:
			t.Errorf("AGENTS.md does not list %s (`hlp %s`)", tool.Name, tool.Command)
		case row.command != tool.Command:
			t.Errorf("AGENTS.md: %s is `hlp %s`, the registry says `hlp %s`", tool.Name, row.command, tool.Command)
		case row.mutating != tool.Mutating:
			t.Errorf("AGENTS.md: %s trading mark is %v, want %v", tool.Name, row.mutating, tool.Mutating)
		}
	}
	for name := range listed {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("AGENTS.md lists %s, which is not a tool", name)
		}
	}
}

// Every command in the agent docs' sh blocks must name a real command and
// parse its flags. They are not run: the docs use live vault names.
func TestAgentDocCommandsParse(t *testing.T) {
	root := coretest.RepoRoot()
	for _, doc := range []string{"AGENTS.md", filepath.Join("skills", "hlp", "SKILL.md")} {
		cmds := readmeCommands(t, filepath.Join(root, doc))
		if doc != "AGENTS.md" && len(cmds) < 8 {
			t.Fatalf("%s: found only %d commands", doc, len(cmds))
		}
		for _, line := range cmds {
			words, err := core.Tokenize(line)
			if err != nil {
				t.Errorf("%s: %s: %v", doc, line, err)
				continue
			}
			a := &app{io: IO{Getenv: func(string) string { return "" }}, g: &globals{}, reg: core.Default()}
			cmd, rest, err := a.rootCmd().Find(words[1:])
			if err != nil || (cmd.Name() == "hlp" && words[1] != "hlp") {
				t.Errorf("%s: %s: unknown command (%v)", doc, line, err)
				continue
			}
			if err := cmd.ParseFlags(rest); err != nil {
				t.Errorf("%s: %s: %v", doc, line, err)
			}
		}
	}
}

// The embedded skill is SKILL.md, with the front matter skill loaders need,
// and it carries the rules agents must not miss.
func TestEmbeddedSkill(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(coretest.RepoRoot(), "skills", "hlp", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if hlpskill.Markdown != string(b) {
		t.Fatal("embedded skill differs from skills/hlp/SKILL.md")
	}
	if !strings.HasPrefix(hlpskill.Markdown, "---\nname: hlp\ndescription: ") {
		t.Fatal("SKILL.md front matter missing")
	}
	for _, want := range []string{"retryAfterMs", "--select", "maxEvents", "--max-events", "--engine", "hlp price", "third parties", "private key"} {
		if !strings.Contains(hlpskill.Markdown, want) {
			t.Errorf("SKILL.md does not mention %q", want)
		}
	}
	agents, _ := os.ReadFile(filepath.Join(coretest.RepoRoot(), "AGENTS.md"))
	for _, want := range []string{"Contract (version " + core.Contract + ")", "retryAfterMs", "series: true", "maxEvents", "--engine"} {
		if !strings.Contains(string(agents), want) {
			t.Errorf("AGENTS.md does not mention %q", want)
		}
	}
}

// `hlp skill` prints the embedded skill and installs it under $HOME.
func TestSkillCommand(t *testing.T) {
	fx := isolate(t)
	r := runCLI(t, fx, false, "skill")
	if r.code != 0 || r.stdout != hlpskill.Markdown {
		t.Fatalf("hlp skill: code %d, %d bytes", r.code, len(r.stdout))
	}
	home := t.TempDir()
	var out, errb bytes.Buffer
	env := map[string]string{"HOME": home}
	pio := IO{Out: &out, Err: &errb, Width: 100, Getenv: func(k string) string { return env[k] }}
	if code := Run(context.Background(), []string{"skill", "--install", "codex"}, BuildInfo{}, pio); code != 0 {
		t.Fatalf("install: code %d: %s", code, errb.String())
	}
	b, err := os.ReadFile(filepath.Join(home, ".codex", "skills", "hlp", "SKILL.md"))
	if err != nil || string(b) != hlpskill.Markdown {
		t.Fatalf("installed skill: %v", err)
	}
	if code := Run(context.Background(), []string{"skill", "--install", "vim"}, BuildInfo{}, pio); code != 2 {
		t.Fatalf("unknown agent: code %d", code)
	}
}

// Every SKILL.md recipe that selects fields must select fields that exist:
// run each one-shot recipe against the fixtures and fail on a
// "select: no field" warning (AGENT-UX docs drift). Recipes the fixtures
// cannot serve are skipped; streams are not run.
func TestSkillRecipesSelectRealFields(t *testing.T) {
	fx := isolate(t)
	cmds := readmeCommands(t, filepath.Join(coretest.RepoRoot(), "skills", "hlp", "SKILL.md"))
	ran := 0
	for _, line := range cmds {
		if !strings.Contains(line, "--select") || strings.Contains(line, "--live") {
			continue
		}
		words, err := core.Tokenize(line)
		if err != nil {
			t.Fatal(err)
		}
		r := runCLI(t, fx, false, words[1:]...)
		if r.code != 0 {
			continue // the fixtures do not cover this recipe
		}
		ran++
		// composition comes from hlp.xyz only (the fixtures have none)
		if i := strings.Index(r.stdout, "select: no field"); i >= 0 && !strings.HasPrefix(r.stdout[i:], `select: no field \"composition\" `) {
			t.Errorf("%s: %s", line, r.stdout[strings.Index(r.stdout, "select: no field"):][:120])
		}
	}
	if ran < 3 {
		t.Fatalf("only %d recipes ran against the fixtures", ran)
	}
}
