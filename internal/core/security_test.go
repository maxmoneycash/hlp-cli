package core_test

// Acceptance tests for the security gates. Nothing here touches the
// network: exchanges are fakes or sign-only clients, and data comes from
// coretest fixtures.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/config"
	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/core/coretest"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hlpxyz"
	"github.com/maxmoneycash/hlp/internal/hyperliquid"
	"github.com/maxmoneycash/hlp/internal/hyperliquid/exchange"
	"github.com/maxmoneycash/hlp/internal/strategy"
)

// A-T1 (S1): a bot YAML may not pick the endpoint or the key env var, and
// config may not name the trading key as the LLM key.
func TestSecurityBotYAMLCannotStealKeys(t *testing.T) {
	y := "name: x\nmodel: {provider: openai-compatible, model: m, base_url: https://evil.example/v1, api_key_env: HLP_AGENT_KEY}\nuniverse: [BTC]\nprompt: p\n"
	if _, err := strategy.ParseLLMConfig([]byte(y)); err == nil {
		t.Fatal("bot YAML with base_url/api_key_env accepted")
	}
	if err := config.CheckLLMKeyEnv("HLP_AGENT_KEY"); err == nil {
		t.Fatal("HLP_AGENT_KEY accepted as an LLM key env")
	}
}

// A-T2 (S7): a remote plain-http endpoint fails validation everywhere it
// can be configured; loopback http is fine.
func TestSecurityRemoteHTTPRefused(t *testing.T) {
	cfg := config.Defaults()
	cfg.LLM.Provider = "openai-compatible"
	cfg.LLM.BaseURL = "http://remote.example/v1"
	if err := cfg.Validate(); err == nil {
		t.Fatal("llm.base_url=http://remote passed Validate")
	}
	cfg = config.Defaults()
	cfg.HLPXYZ.BaseURL = "http://remote.example"
	if err := cfg.Validate(); err == nil {
		t.Fatal("hlpxyz.base_url=http://remote passed Validate")
	}
	cfg.HLPXYZ.BaseURL = "http://127.0.0.1:8080"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("loopback http refused: %v", err)
	}
}

// copycatEnv is the fixture universe plus an open $100 vault named exactly
// "Growi" next to "Growi HF" ($5M), the S2 scenario.
func copycatEnv(t *testing.T) *core.Env {
	t.Helper()
	dir := coretest.Dir(t)
	vs := append(coretest.Vaults(), domain.VaultSummary{Address: copycat, Name: "Growi", Leader: "0x9999999999999999999999999999999999999999",
		Kind: domain.VaultHyperCore, Rel: "normal", TVL: 100, AgeDays: 5, AllowDeposits: true})
	b, _ := json.Marshal(vs)
	if err := os.WriteFile(filepath.Join(dir, "vaults.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	env := coretest.Env(t, nil)
	env.HL = core.NewFixtureReader(dir)
	return env
}

const copycat = "0x7777777777777777777777777777777777777777"

func checkByID(p *core.OrderPreview, id string) (domain.Check, bool) {
	for _, c := range p.Checks {
		if c.ID == id {
			return c, true
		}
	}
	return domain.Check{}, false
}

// A-T3 (S2): an ambiguous name never picks a vault for a deposit; the error
// lists every candidate. Exact, unique names and addresses still work, and
// the preview names the vault and flags small or young ones.
func TestSecurityVaultDepositStrict(t *testing.T) {
	env := copycatEnv(t)
	_, err := invoke(t, env, "vault_deposit", `{"vault":"growi","usd":100,"dry-run":true}`)
	if core.ExitCode(err) != 2 || code(err) != core.CodeUsage {
		t.Fatalf("ambiguous deposit: exit %d %v", core.ExitCode(err), err)
	}
	e := core.AsError(err)
	b, _ := json.Marshal(e.Details)
	for _, want := range []string{coretest.Growi, copycat, "Growi HF"} {
		if !strings.Contains(string(b), want) || !strings.Contains(e.Message, want) {
			t.Errorf("candidate %s missing: %s / %s", want, e.Message, b)
		}
	}
	// withdrawals are strict too
	if _, err := invoke(t, env, "vault_withdraw", `{"vault":"growi","usd":100,"dry-run":true}`); code(err) != core.CodeUsage {
		t.Errorf("ambiguous withdraw: %v", err)
	}
	if _, err := invoke(t, env, "vault_deposit", `{"vault":"nothing like it","usd":100,"dry-run":true}`); code(err) != core.CodeUnknownVault {
		t.Errorf("no match: %v", err)
	}

	// an exact curated name (vaults.toml, reviewed addresses) resolves to
	// the curated address, whatever namesakes the live list holds
	res := mustInvoke(t, env, "vault_deposit", `{"vault":"Growi HF","usd":100,"dry-run":true}`)
	if p := res.Data.(*core.OrderPreview); p.Vault != "0x1e37a337ed460039d1b15bd3bc489de789768d5e" {
		t.Fatalf("curated exact name: %s", p.Vault)
	}

	// a partial name only one vault contains is refused with that vault as
	// the candidate (a namesake could be called exactly what users type)
	if _, err := invoke(t, env, "vault_deposit", `{"vault":"growi h","usd":100,"dry-run":true}`); code(err) != core.CodeUsage || !strings.Contains(err.Error(), coretest.Growi) {
		t.Fatalf("partial name: %v", err)
	}
	// the address resolves, and the card says who it is
	res = mustInvoke(t, env, "vault_deposit", `{"vault":"`+coretest.Growi+`","usd":100,"dry-run":true}`)
	p := res.Data.(*core.OrderPreview)
	c, ok := checkByID(p, "vault_target")
	if p.Vault != coretest.Growi || !ok || !strings.Contains(c.Detail, "Growi HF") || !strings.Contains(c.Detail, coretest.Growi) ||
		!strings.Contains(c.Detail, "TVL $5M") || !strings.Contains(c.Detail, "400 days") {
		t.Fatalf("growi hf preview %+v", p)
	}
	if _, warn := checkByID(p, "vault_tvl"); warn {
		t.Error("a $5M vault should not get the TVL warning")
	}
	if !strings.Contains(res.Title, "Growi HF") {
		t.Errorf("title %q", res.Title)
	}

	// the copycat by address: allowed (the user named it), but flagged
	res = mustInvoke(t, env, "vault_deposit", `{"vault":"`+copycat+`","usd":100,"dry-run":true}`)
	p = res.Data.(*core.OrderPreview)
	tvl, ok1 := checkByID(p, "vault_tvl")
	age, ok2 := checkByID(p, "vault_age")
	if p.Vault != copycat || !ok1 || tvl.Status != domain.CheckWarn || !ok2 || age.Status != domain.CheckWarn {
		t.Fatalf("copycat warnings %+v", p.Checks)
	}
	// warnings never block on their own
	if bl := p.FailedChecks(); slices.Contains(bl, "vault_tvl") || slices.Contains(bl, "vault_age") {
		t.Errorf("warnings became blockers: %v", bl)
	}
	// hlp keeps working
	res = mustInvoke(t, env, "vault_deposit", `{"vault":"hlp","usd":100,"dry-run":true}`)
	if p := res.Data.(*core.OrderPreview); p.Vault != core.HLPAddress {
		t.Fatalf("hlp deposit %+v", p)
	}
}

// A-T4 (S4): risk.require_confirm with no confirmer (no TTY, --json, MCP)
// refuses a live mainnet order even with yes, unless allow_unattended.
func TestSecurityRequireConfirmWithoutTTY(t *testing.T) {
	defer core.SetLiveBuild(true)()
	ex := &fakeExchange{}
	env := tradingEnv(t, ex)
	env.Config.Risk.RequireConfirm = true
	env.Config.Network = domain.Mainnet
	ctx := core.WithSource(context.Background(), "cli")
	call := core.Call{Tool: "place_order", Args: json.RawMessage(`{"side":"buy","coin":"BTC","size":"1","yes":true}`)}
	_, err := core.Default().Invoke(ctx, env, call)
	if code(err) != core.CodeConfirmRequired || core.ExitCode(err) != 3 {
		t.Fatalf("require_confirm without a TTY: %v", err)
	}
	if ex.posts != 0 {
		t.Fatal("signed without a confirmation")
	}
	env.Config.Trading.AllowUnattended = true
	// allow_unattended is for bots: it does not cover MCP on its own, and
	// the refusal does not tell the caller which key to flip
	mcpCtx := core.WithSource(context.Background(), "mcp")
	if rg, ok := env.Guard.(*core.RiskGuard); ok {
		rg.MCPAllowed = true
	}
	if _, err := core.Default().Invoke(mcpCtx, env, call); code(err) != core.CodeConfirmRequired || strings.Contains(core.AsError(err).Hint, "allow_unattended") {
		t.Fatalf("mcp with only trading.allow_unattended: %v", err)
	}
	if _, err := core.Default().Invoke(ctx, env, call); err != nil || ex.posts != 1 {
		t.Fatalf("allow_unattended: %v (posts %d)", err, ex.posts)
	}
	env.Config.MCP.AllowUnattended = true
	if _, err := core.Default().Invoke(mcpCtx, env, call); err != nil || ex.posts != 2 {
		t.Fatalf("mcp.allow_unattended: %v (posts %d)", err, ex.posts)
	}
}

// A-T5 (S6): a ctx nobody tagged fails closed, like an agent: yes is
// ignored and a person must confirm.
func TestSecurityUntaggedSourceIsUntrusted(t *testing.T) {
	if core.SourceFrom(context.Background()) != core.SourceUnknown {
		t.Fatal("untagged source is not unknown")
	}
	ex := &fakeExchange{}
	env := tradingEnv(t, ex)
	call := core.Call{Tool: "place_order", Args: json.RawMessage(`{"side":"buy","coin":"BTC","size":"1","yes":true}`)}
	if _, err := core.Default().Invoke(context.Background(), env, call); code(err) != core.CodeConfirmRequired {
		t.Fatalf("untagged yes: %v", err)
	}
	asked := 0
	env.Confirm = func(context.Context, core.OrderPreview) (bool, error) { asked++; return false, nil }
	if _, err := core.Default().Invoke(context.Background(), env, call); code(err) != core.CodeSafety || asked != 1 {
		t.Fatalf("untagged yes must still ask: %v (asked %d)", err, asked)
	}
	if ex.posts != 0 {
		t.Fatal("nothing should be signed")
	}
	// the CLI itself keeps --yes
	if _, err := core.Default().Invoke(core.WithSource(context.Background(), "cli"), env, call); err != nil || ex.posts != 1 {
		t.Fatalf("cli yes: %v", err)
	}
}

// S6: without --leverage the cap sees the account's current leverage.
func TestSecurityLeverageCapReadsCurrentLeverage(t *testing.T) {
	env := coretest.Env(t, nil)
	env.Config.Trading.AccountAddr = coretest.Account
	// fixture account: BTC at 5x (= cap), no HYPE position (market default)
	res := mustInvoke(t, env, "place_order", `{"side":"buy","coin":"BTC","usd":100,"dry-run":true}`)
	if c, ok := checkByID(res.Data.(*core.OrderPreview), "max_leverage"); !ok || c.Status != domain.CheckPass || !strings.Contains(c.Detail, "5x (current position)") {
		t.Fatalf("btc leverage check %+v", c)
	}
	res = mustInvoke(t, env, "place_order", `{"side":"buy","coin":"HYPE","usd":100,"dry-run":true}`)
	if c, ok := checkByID(res.Data.(*core.OrderPreview), "max_leverage"); !ok || c.Status != domain.CheckFail || !strings.Contains(c.Detail, "10x (set before the order)") {
		t.Fatalf("hype default leverage check %+v", c)
	}
	// an explicit leverage is what the cap checks
	res = mustInvoke(t, env, "place_order", `{"side":"buy","coin":"HYPE","usd":100,"leverage":3,"dry-run":true}`)
	if c, _ := checkByID(res.Data.(*core.OrderPreview), "max_leverage"); c.Status != domain.CheckPass {
		t.Fatalf("explicit leverage check %+v", c)
	}
}

// S6 follow-up: with no position, Hyperliquid would use the account's
// remembered per-asset leverage, so the order first sets the checked
// value; an account read error fails the cap closed.
func TestSecurityLeverageSetBeforeOrder(t *testing.T) {
	defer core.SetLiveBuild(true)()
	ex := &fakeExchange{}
	env := tradingEnv(t, ex)
	env.Config.Trading.AccountAddr = coretest.Account
	env.Config.Risk.MaxLeverage = 20
	env.Confirm = func(context.Context, core.OrderPreview) (bool, error) { return true, nil }
	mustInvoke(t, env, "place_order", `{"side":"buy","coin":"HYPE","usd":100}`)
	if ex.posts != 2 {
		t.Fatalf("posts %d, want updateLeverage then order", ex.posts)
	}
	if lev, ok := ex.actions[0].(exchange.UpdateLeverageAction); !ok || lev.Leverage != 10 {
		t.Fatalf("first action %+v", ex.actions[0])
	}
	// an open position keeps its leverage: no updateLeverage
	ex.posts, ex.actions = 0, nil
	mustInvoke(t, env, "place_order", `{"side":"buy","coin":"BTC","usd":100}`)
	if ex.posts != 1 {
		t.Fatalf("position order posts %d", ex.posts)
	}
	// unreadable account: the cap fails closed
	env.Config.Risk.MaxLeverage = 50
	env.Config.Trading.AccountAddr = "0x00000000000000000000000000000000000000ee"
	env.HL = failingAccount{env.HL}
	env.Guard = core.NewGuard(env)
	if _, err := invoke(t, env, "place_order", `{"side":"buy","coin":"HYPE","usd":100}`); code(err) != core.CodeRiskLimit {
		t.Fatalf("account error must fail the leverage cap: %v", err)
	}
}

// failingAccount is a reader whose Account call always fails.
type failingAccount struct{ hyperliquid.Reader }

func (failingAccount) Account(context.Context, string) (domain.Account, error) {
	return domain.Account{}, errors.New("upstream down")
}

// S6: with trading on, a daily-loss cap that cannot read fills fails.
func TestSecurityDailyLossNeedsAccount(t *testing.T) {
	g := &core.RiskGuard{Risk: config.RiskConfig{DailyLossUSD: 100}, TradingOn: true, HasAgentKey: true}
	err := g.Check(context.Background(), core.Intent{Action: "order", Coin: "BTC", NotionalUSD: 50})
	if code(err) != core.CodeRiskLimit || !strings.Contains(err.Error(), "trading.account") {
		t.Fatalf("daily loss without account: %v", err)
	}
}

// A-T7 (S9): envelopes never carry a signature in output, and live
// envelopes expire.
func TestSecurityEnvelopeExpiryAndSignature(t *testing.T) {
	s, err := exchange.NewSigner(publicTestKey)
	if err != nil {
		t.Fatal(err)
	}
	c := exchange.NewHTTPClient("http://127.0.0.1:1/exchange", s, nil)
	if c.ExpiresAfter == 0 {
		t.Fatal("live envelopes have no expiry")
	}
	e, err := c.Sign(exchange.CancelAction{Type: "cancel", Cancels: []exchange.Cancel{{A: 0, O: 1}}}, nil, false)
	if err != nil || e.ExpiresAfter == nil || *e.ExpiresAfter <= e.Nonce {
		t.Fatalf("live envelope expiry %v %v", e.ExpiresAfter, err)
	}
	env := tradingEnv(t, exchange.NewClient("http://127.0.0.1:1/exchange", s))
	res := mustInvoke(t, env, "place_order", `{"side":"buy","coin":"BTC","usd":500,"dry-run":true}`)
	var m map[string]any
	if err := json.Unmarshal(res.Data.(*core.OrderPreview).Envelope, &m); err != nil {
		t.Fatal(err)
	}
	if sig, _ := m["signature"].(string); !strings.HasPrefix(sig, "withheld") {
		t.Fatalf("signature in dry-run output: %v", m["signature"])
	}
	if exp, _ := m["expiresAfter"].(float64); exp == 0 {
		t.Fatal("dry-run expiresAfter is zero")
	}
}

// A-T8 (AGENT-UX #6/#10): preview_order is read-only (so MCP lists it
// without --allow-trading), never signs, and summarizes with ok/blockers.
func TestSecurityPreviewOrder(t *testing.T) {
	tool, ok := core.Default().Get("preview_order")
	if !ok || tool.Mutating {
		t.Fatalf("preview_order missing or mutating: %+v", tool)
	}
	env := coretest.Env(t, nil)
	res := mustInvoke(t, env, "preview_order", `{"side":"buy","coin":"BTC","usd":100}`)
	p := res.Data.(*core.OrderPreview)
	if !p.DryRun || p.Envelope != nil || !slices.Contains(p.FailedChecks(), "trading_enabled") {
		t.Fatalf("preview %+v", p)
	}
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), `"ok":false`) || !strings.Contains(string(b), `"blockers":["trading_enabled"`) {
		t.Fatalf("preview JSON: %s", b)
	}
	// even with trading on and a signer, the preview signs nothing
	ex := &fakeExchange{}
	env = tradingEnv(t, ex)
	res = mustInvoke(t, env, "preview_order", `{"side":"buy","coin":"BTC","usd":100}`)
	p = res.Data.(*core.OrderPreview)
	if ex.posts != 0 || p.Envelope != nil {
		t.Fatalf("preview signed: posts %d", ex.posts)
	}
	b, _ = json.Marshal(p)
	if !strings.Contains(string(b), `"ok":true`) || !strings.Contains(string(b), `"blockers":[]`) {
		t.Fatalf("clean preview JSON: %s", b)
	}
	// the place_order dry-run carries the same summary
	res = mustInvoke(t, coretest.Env(t, nil), "place_order", `{"side":"buy","coin":"BTC","usd":100,"dry-run":true}`)
	if b, _ := json.Marshal(res.Data); !strings.Contains(string(b), `"ok":false`) {
		t.Fatalf("dry-run JSON lacks ok: %s", b)
	}
}

// A-T9 (AGENT-UX #18/#19): hlp.xyz HTTP errors and rate limits map to
// stable codes with retry timing.
func TestSecurityErrorMapping(t *testing.T) {
	e := core.AsError(&hlpxyz.APIError{Status: 404, Message: "Index not found", Path: "/api/vaults"})
	if e.Code != core.CodeUsage || !strings.Contains(e.Hint, "mavericks") {
		t.Fatalf("unknown index: %+v", e)
	}
	if e := core.AsError(&hlpxyz.APIError{Status: 404, Message: "Vault not found", Path: "/api/vaults"}); e.Code != core.CodeNotFound {
		t.Fatalf("vault 404: %+v", e)
	}
	for _, err := range []error{
		&hyperliquid.RateLimitError{Type: "metaAndAssetCtxs", RetryAfter: 5 * time.Second},
		&hlpxyz.APIError{Status: 429, Message: "slow down", Path: "/api/vaults"},
		hlpxyz.ErrRateLimited,
	} {
		e := core.AsError(err)
		if e.Code != core.CodeRateLimited || !e.Retryable || core.ExitCode(e) != 4 {
			t.Fatalf("%v: %+v", err, e)
		}
		b, _ := json.Marshal(e)
		if !strings.Contains(string(b), `"retryable":true`) || !strings.Contains(string(b), `"retryAfterMs":`) {
			t.Fatalf("%v JSON: %s", err, b)
		}
	}
	if e := core.AsError(&hyperliquid.RateLimitError{RetryAfter: 5 * time.Second}); e.Details.(map[string]any)["retryAfterMs"] != int64(5000) {
		t.Fatalf("retryAfterMs: %+v", e.Details)
	}
	for _, err := range []error{
		&hyperliquid.APIError{Status: 502, Code: "SERVER"},
		&hlpxyz.APIError{Status: 503, Path: "/api/vaults"},
	} {
		if e := core.AsError(err); e.Code != core.CodeAPI || !e.Retryable || core.ExitCode(e) != 4 {
			t.Fatalf("%v: %+v", err, e)
		}
	}
	if e := core.AsError(&hyperliquid.APIError{Status: 400, Code: "BAD_REQUEST"}); e.Retryable || core.ExitCode(e) != 2 {
		t.Fatalf("400 retryable: %+v", e)
	}
}
