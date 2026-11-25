package core

import "testing"

// TestCatalogContract guards the shared registry: every tool must be
// reachable from every surface without collisions, and every input must
// produce a JSON Schema (MCP, agent and `hlp schema` depend on it).
func TestCatalogContract(t *testing.T) {
	r := Default()
	seenSlash := map[string]string{}
	seenCmd := map[string]string{}
	for _, tool := range r.All() {
		if tool.NewInput == nil || tool.Run == nil {
			t.Errorf("%s: NewInput and Run are required", tool.Name)
			continue
		}
		if _, err := tool.InputSchema(); err != nil {
			t.Errorf("%s: schema: %v", tool.Name, err)
		}
		for _, s := range append([]string{tool.Slash}, tool.Aliases...) {
			if prev, dup := seenSlash[s]; dup {
				t.Errorf("slash/alias %q used by %s and %s", s, prev, tool.Name)
			}
			seenSlash[s] = tool.Name
		}
		if prev, dup := seenCmd[tool.Command]; dup {
			t.Errorf("command %q used by %s and %s", tool.Command, prev, tool.Name)
		}
		seenCmd[tool.Command] = tool.Name
		if tool.Mutating && tool.Group != "trade" {
			t.Errorf("%s: mutating tools must be in group trade", tool.Name)
		}
	}
}

func TestExitCodes(t *testing.T) {
	cases := map[string]int{CodeUsage: 2, CodeUnknownCoin: 2, CodeRiskLimit: 3, CodeTradingDisabled: 3, CodeAPI: 1, CodeNotImplemented: 1}
	for code, want := range cases {
		if got := ExitCode(Errorf(code, "x")); got != want {
			t.Errorf("%s: exit %d, want %d", code, got, want)
		}
	}
	if ExitCode(nil) != 0 {
		t.Error("nil error must exit 0")
	}
}
