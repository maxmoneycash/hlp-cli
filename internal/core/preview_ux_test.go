package core_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/core/coretest"
)

// AGENT-UX preview: a small order is a min_notional blocker (not a USAGE
// error), setup and risk blockers are split, and order --dry-run carries
// the same "blocked:" warning as preview.
func TestPreviewBlockersSplit(t *testing.T) {
	env := coretest.Env(t, nil)
	res := mustInvoke(t, env, "preview_order", `{"side":"buy","coin":"BTC","usd":5}`)
	b, _ := json.Marshal(res.Data)
	var v struct {
		OK            bool     `json:"ok"`
		TradeOK       bool     `json:"tradeOk"`
		SetupBlockers []string `json:"setupBlockers"`
		RiskBlockers  []string `json:"riskBlockers"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if v.OK || v.TradeOK || !slices.Contains(v.RiskBlockers, "min_notional") || !slices.Contains(v.SetupBlockers, "trading_enabled") {
		t.Fatalf("%s", b)
	}
	res = mustInvoke(t, env, "preview_order", `{"side":"buy","coin":"BTC","usd":100,"leverage":3}`)
	b, _ = json.Marshal(res.Data)
	_ = json.Unmarshal(b, &v)
	if !v.TradeOK || len(v.RiskBlockers) != 0 {
		t.Fatalf("a $100 order should pass the risk rules: %s", b)
	}
	if _, err := invoke(t, env, "place_order", `{"side":"buy","coin":"BTC","usd":5,"dryRun":true}`); code(err) != core.CodeUsage || !strings.Contains(core.AsError(err).Hint, "size") {
		t.Fatalf("order below minimum: %v", err)
	}
	res = mustInvoke(t, env, "place_order", `{"side":"buy","coin":"BTC","usd":100,"dryRun":true}`)
	found := false
	for _, w := range res.Meta.Warnings {
		found = found || strings.HasPrefix(w, "blocked: ")
	}
	if !found {
		t.Fatalf("dry-run lacks the blocked warning: %v", res.Meta.Warnings)
	}
}
