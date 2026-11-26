package cli

import (
	"strings"

	"github.com/maxmoneycash/hlp-cli/internal/core"
)

// agentEnv are variables coding agents set in the shells they run. Under
// one of them tool results come out the size MCP sends (core.Compact):
// bulky series dropped, long lists downsampled, the payload capped at
// 64 KB, every cut named in meta.warnings. --full or HLP_COMPACT=0 turns
// it off.
var agentEnv = []string{"AI_AGENT", "CLAUDECODE", "CODEX_SANDBOX", "CURSOR_AGENT", "GEMINI_CLI", "HLP_AGENT"}

func agentCompact(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("HLP_COMPACT"))) {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	}
	for _, k := range agentEnv {
		if v := strings.TrimSpace(getenv(k)); v != "" && v != "0" {
			return true
		}
	}
	return false
}

// compactResult applies --select/--limit and then the agent bounds. An
// explicit --select keeps the series it names.
// Schemas are never compacted: a property named curve or markers is a
// field description, not a series.
func compactResult(r *core.Result, m Mode) (*core.Result, error) {
	if r == nil || r.Card == core.CardSchema {
		return core.ShapeResult(r, m.Select, m.Limit)
	}
	shaped, err := core.ShapeResult(r, m.Select, m.Limit)
	if err != nil {
		return nil, err
	}
	c, err := core.Compact(shaped, core.CompactOptions{Series: len(m.Select) > 0})
	if err != nil {
		return nil, err
	}
	if c.Meta.Truncated && len(c.Meta.Warnings) > len(shaped.Meta.Warnings) {
		c.Meta.Warnings = append(c.Meta.Warnings, "agent-sized output; --select the fields you need or pass --full")
	}
	return c, nil
}
