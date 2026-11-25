package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/config"
	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/core/coretest"
	"github.com/maxmoneycash/hlp/internal/domain"
)

// Policy checks: network allowlist, trading expiry, allowed vaults, order
// rate, corrupt config.

func guardErr(t *testing.T, err error) *core.Error {
	t.Helper()
	var e *core.Error
	if !errors.As(err, &e) {
		t.Fatalf("want *core.Error, got %v", err)
	}
	return e
}

func failedCheck(e *core.Error) string {
	if c, ok := e.Details.(domain.Check); ok {
		return c.ID
	}
	return ""
}

func TestGuardNetworkAllowlist(t *testing.T) {
	env := tradingEnv(t, &fakeExchange{})
	env.Config.Risk.Networks = []string{"testnet"}
	env.Guard = core.NewGuard(env)
	_, err := invoke(t, env, "place_order", `{"side":"buy","coin":"BTC","usd":500,"dry-run":true}`)
	if e := guardErr(t, err); e.Code != core.CodeTradingDisabled || failedCheck(e) != "network_allowed" {
		t.Fatalf("testnet-only config on mainnet: %+v", e)
	}
	env.Config.Risk.Networks = []string{"mainnet", "testnet"}
	env.Guard = core.NewGuard(env)
	mustInvoke(t, env, "place_order", `{"side":"buy","coin":"BTC","usd":500,"dry-run":true}`)
}

func TestGuardTradingExpiry(t *testing.T) {
	env := tradingEnv(t, &fakeExchange{})
	// Already past: the guard is DenyAll with the expiry hint.
	env.Config.Trading.Expires = "2020-01-01"
	env.Guard = core.NewGuard(env)
	_, err := invoke(t, env, "place_order", `{"side":"buy","coin":"BTC","usd":500,"dry-run":true}`)
	if e := guardErr(t, err); e.Code != core.CodeTradingDisabled || !strings.Contains(e.Message, "expired") || !strings.Contains(e.Hint, "config.toml") {
		t.Fatalf("expired: %+v", e)
	}
	// Unparsable (a config built in code, bypassing Validate) refuses.
	env.Config.Trading.Expires = "soon"
	if _, ok := core.NewGuard(env).(core.DenyAll); !ok {
		t.Fatal("unparsable expiry must deny")
	}
	// Expiring mid-session: a long-running MCP server stops at the expiry.
	now := coretest.Now
	env.Now = func() time.Time { return now }
	env.Config.Trading.Expires = now.Add(time.Hour).Format(time.RFC3339)
	env.Guard = core.NewGuard(env)
	mustInvoke(t, env, "place_order", `{"side":"buy","coin":"BTC","usd":500,"dry-run":true}`)
	now = now.Add(2 * time.Hour)
	_, err = invoke(t, env, "place_order", `{"side":"buy","coin":"BTC","usd":500,"dry-run":true}`)
	if e := guardErr(t, err); e.Code != core.CodeTradingDisabled || failedCheck(e) != "trading_expiry" {
		t.Fatalf("expired mid-session: %+v", e)
	}
}

func TestGuardAllowedVaults(t *testing.T) {
	env := tradingEnv(t, &fakeExchange{})
	env.Config.MCP.AllowTrading = true
	env.Guard = core.NewGuard(env)
	mcp := func(tool, args string) error {
		_, err := invokeAs(env, "mcp", tool, args)
		return err
	}
	dep := `{"vault":"` + coretest.Growi + `","usd":100,"dry-run":true}`
	// Empty list: an MCP caller may deposit only into curated vaults.
	if e := guardErr(t, mcp("vault_deposit", dep)); e.Code != core.CodeRiskLimit || failedCheck(e) != "allowed_vault" {
		t.Fatalf("mcp deposit into an uncurated vault: %+v", e)
	}
	// A person at the CLI may deposit anywhere when the list is empty.
	mustInvoke(t, env, "vault_deposit", dep)
	// Withdrawals are never limited.
	if err := mcp("vault_withdraw", dep); err != nil && failedCheck(guardErr(t, err)) == "allowed_vault" {
		t.Fatalf("withdraw limited: %v", err)
	}
	// A listed address passes for every caller; an unlisted one fails for
	// the CLI too once the list is set.
	env.Config.Risk.AllowedVaults = []string{strings.ToUpper(coretest.Growi[:2]) + coretest.Growi[2:]}
	env.Guard = core.NewGuard(env)
	if err := mcp("vault_deposit", dep); err != nil {
		t.Fatalf("listed vault: %v", err)
	}
	env.Config.Risk.AllowedVaults = []string{"curated"}
	env.Guard = core.NewGuard(env)
	if _, err := invoke(t, env, "vault_deposit", dep); failedCheck(guardErr(t, err)) != "allowed_vault" {
		t.Fatalf("cli deposit outside the list: %v", err)
	}

	// "curated" means the curated registry; HLP is in it.
	g := &core.RiskGuard{Risk: config.RiskConfig{AllowedVaults: []string{"curated"}}, TradingOn: true, HasAgentKey: true, MCPAllowed: true,
		Curated: func(a string) bool { return a == core.HLPAddress }}
	in := core.Intent{Action: "vault_deposit", Vault: core.HLPAddress, NotionalUSD: 10, Source: "mcp"}
	if err := g.Check(context.Background(), in); err != nil {
		t.Fatalf("curated HLP: %v", err)
	}
	in.Vault = coretest.Growi
	if err := g.Check(context.Background(), in); failedCheck(guardErr(t, err)) != "allowed_vault" {
		t.Fatalf("uncurated: %v", err)
	}
}

func TestGuardOrderRateCap(t *testing.T) {
	defer core.SetLiveBuild(true)()
	ex := &fakeExchange{}
	env := tradingEnv(t, ex)
	now := coretest.Now
	env.Now = func() time.Time { return now }
	env.Config.Risk.MaxOrdersPerMin = 2
	env.Guard = core.NewGuard(env)
	env.Confirm = func(context.Context, core.OrderPreview) (bool, error) { return true, nil }
	order := `{"side":"buy","coin":"BTC","usd":500}`
	mustInvoke(t, env, "place_order", order)
	mustInvoke(t, env, "place_order", order)
	// A dry run neither uses a slot nor is refused; its preview warns.
	res := mustInvoke(t, env, "place_order", `{"side":"buy","coin":"BTC","usd":500,"dry-run":true}`)
	warned := false
	for _, c := range res.Data.(*core.OrderPreview).Checks {
		warned = warned || c.ID == "max_orders_per_min" && c.Status == domain.CheckWarn
	}
	if !warned {
		t.Fatalf("preview checks %+v", res.Data.(*core.OrderPreview).Checks)
	}
	_, err := invoke(t, env, "place_order", order)
	if e := guardErr(t, err); e.Code != core.CodeRiskLimit || failedCheck(e) != "max_orders_per_min" {
		t.Fatalf("third order in a minute: %+v", e)
	}
	live := func() int {
		n := 0
		for _, g := range ex.gates {
			if g.Live {
				n++
			}
		}
		return n
	}
	if live() != 2 {
		t.Fatalf("live sends %d, want 2", live())
	}
	// A declined confirm does not use a slot.
	now = now.Add(61 * time.Second)
	env.Confirm = func(context.Context, core.OrderPreview) (bool, error) { return false, nil }
	if _, err := invoke(t, env, "place_order", order); code(err) != core.CodeSafety {
		t.Fatalf("declined: %v", err)
	}
	env.Confirm = func(context.Context, core.OrderPreview) (bool, error) { return true, nil }
	// After the window rolls over, two more go through.
	now = now.Add(61 * time.Second)
	mustInvoke(t, env, "place_order", order)
	mustInvoke(t, env, "place_order", order)
	if live() != 4 {
		t.Fatalf("live sends %d after the window, want 4", live())
	}
	// The counter lives in the data dir, so another process (a new Env on
	// the same paths) sees the same window.
	env2 := tradingEnv(t, ex)
	env2.Paths = env.Paths
	env2.Now = env.Now
	env2.Config.Risk.MaxOrdersPerMin = 2
	env2.Confirm = env.Confirm
	env2.Guard = core.NewGuard(env2)
	if _, err := invoke(t, env2, "place_order", order); failedCheck(guardErr(t, err)) != "max_orders_per_min" {
		t.Fatalf("second process ignored the shared window: %v", err)
	}
	// Reduce-only actions are never capped.
	g := env2.Guard.(*core.RiskGuard)
	if err := g.TakeSend(core.Intent{Action: "order", ReduceOnly: true}); err != nil {
		t.Fatalf("reduce-only capped: %v", err)
	}
	if err := g.TakeSend(core.Intent{Action: "cancel"}); err != nil {
		t.Fatalf("cancel capped: %v", err)
	}
}

func TestOrderRateTakeIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rate.json")
	a, b := core.NewOrderRate(path), core.NewOrderRate(path)
	now := time.Unix(1_800_000_000, 0)
	if _, ok := a.Take(now, 2); !ok {
		t.Fatal("first")
	}
	if _, ok := b.Take(now, 2); !ok {
		t.Fatal("second")
	}
	if n, ok := a.Take(now, 2); ok || n != 2 {
		t.Fatalf("third: %d %v", n, ok)
	}
	if n := b.Count(now.Add(time.Minute)); n != 0 {
		t.Fatalf("window did not roll: %d", n)
	}
}

func TestCorruptConfigRefusesToTrade(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"syntax":        "[trading]\nenabled = true\nagent_address = \"0x1111111111111111111111111111111111111111\"\n[risk\nmax_notional_usd = 5\n",
		"bad expiry":    "[trading]\nenabled = true\nagent_address = \"0x1111111111111111111111111111111111111111\"\nexpires = \"next tuesday\"\n",
		"zero cap":      "[trading]\nenabled = true\nagent_address = \"0x1111111111111111111111111111111111111111\"\n[risk]\nmax_notional_usd = 0\n",
		"typo key":      "[trading]\nenabled = true\nagent_address = \"0x1111111111111111111111111111111111111111\"\n[risk]\nmax_order_per_min = 5\n",
		"bad network":   "[trading]\nenabled = true\nagent_address = \"0x1111111111111111111111111111111111111111\"\n[risk]\nnetworks = [\"devnet\"]\n",
		"bad vault":     "[trading]\nenabled = true\nagent_address = \"0x1111111111111111111111111111111111111111\"\n[risk]\nallowed_vaults = [\"growi\"]\n",
		"rate too high": "[trading]\nenabled = true\nagent_address = \"0x1111111111111111111111111111111111111111\"\n[risk]\nmax_orders_per_min = 100000\n",
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".toml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		env, err := core.NewEnv(context.Background(), core.EnvOptions{ConfigPath: path, Offline: true, FixturesDir: coretest.Dir(t)})
		if err == nil {
			env.Close()
			t.Errorf("%s: NewEnv accepted a corrupt config", name)
			continue
		}
		if code(err) != core.CodeUsage {
			t.Errorf("%s: %v, want USAGE", name, err)
		}
	}
}

// No tool may edit policy: a model that can lift its own limits has none.
// A set_policy_rule / clear_policy_rule style tool is exactly what must not exist.
func TestNoToolEditsPolicy(t *testing.T) {
	noun := regexp.MustCompile(`(?i)(policy|config|setting|permission)`)
	verb := regexp.MustCompile(`(?i)^(set|edit|update|clear|change|reset|raise|lift|extend)[_ ].*(risk|limit|cap|allow|expir|network|vault)`)
	for _, tool := range core.Default().All() {
		if noun.MatchString(tool.Name) || noun.MatchString(tool.Command) || verb.MatchString(tool.Name) || verb.MatchString(tool.Command) {
			t.Errorf("tool %q (%s) looks like it edits policy; risk config is edited only by a person in config.toml", tool.Name, tool.Command)
		}
		schema, _ := json.Marshal(tool.NewInput())
		if regexp.MustCompile(`(?i)"(max_?notional|max_?leverage|allowed_?(coins|vaults)|networks|max_?orders|expires|daily_?loss)`).Match(schema) {
			t.Errorf("tool %q input carries a risk-config field: %s", tool.Name, schema)
		}
	}
}
