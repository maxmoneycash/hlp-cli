package core_test

// Regression tests for the trading safety checks. Each test pins one check
// and fails if that check is deleted.
// Nothing here touches the network: exchanges are fakes, data comes from
// coretest fixtures, and live sending is only ever switched on together
// with a fake exchange.Client.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/config"
	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/core/coretest"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/hyperliquid"
	"github.com/maxmoneycash/hlp-cli/internal/hyperliquid/exchange"
	"github.com/maxmoneycash/hlp-cli/internal/registry"
)

// upExchange records every Post and answers with res (Status "ok" unless
// set). It never touches the network.
type upExchange struct {
	posts   int
	gates   []exchange.Gate
	actions []any
	res     exchange.Result
}

func (f *upExchange) Post(_ context.Context, action any, vault *string, g exchange.Gate) (exchange.Result, error) {
	f.posts++
	f.gates = append(f.gates, g)
	f.actions = append(f.actions, action)
	r := f.res
	r.DryRun = !g.Live
	if r.Status == "" {
		r.Status = "ok"
	}
	r.Envelope = exchange.Envelope{Action: action, Nonce: 1, VaultAddress: vault}
	return r, nil
}

// upReader overrides Fills and Account of the fixture reader.
type upReader struct {
	hyperliquid.Reader
	fills    map[string][]domain.Fill // lowercased address -> today's fills
	fillsErr error
	asked    []string // addresses whose fills were read
	accounts map[string]domain.Account
}

func (r *upReader) Fills(_ context.Context, addr string, _, _ int64) ([]domain.Fill, error) {
	a := strings.ToLower(addr)
	r.asked = append(r.asked, a)
	if r.fillsErr != nil {
		return nil, r.fillsErr
	}
	return r.fills[a], nil
}

func (r *upReader) Account(ctx context.Context, addr string) (domain.Account, error) {
	if a, ok := r.accounts[strings.ToLower(addr)]; ok {
		return a, nil
	}
	return r.Reader.Account(ctx, addr)
}

// upEnv is an offline Env with trading enabled, a fake exchange and the
// config-backed guard. Call core.NewGuard again after changing config.
func upEnv(t *testing.T, ex exchange.Client) *core.Env {
	t.Helper()
	env := coretest.Env(t, nil)
	env.Config.Trading.Enabled = true
	env.Config.Risk = config.RiskConfig{MaxNotionalUSD: 1e9}
	env.Exchange = ex
	env.Guard = core.NewGuard(env)
	return env
}

func upInvoke(env *core.Env, source, tool, args string) (*core.Result, error) {
	return core.Default().Invoke(core.WithSource(context.Background(), source), env, core.Call{Tool: tool, Args: json.RawMessage(args)})
}

func upCode(err error) string {
	var e *core.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func upFailedCheck(err error) string {
	var e *core.Error
	if !errors.As(err, &e) {
		return ""
	}
	if c, ok := e.Details.(domain.Check); ok {
		return c.ID
	}
	return ""
}

func upLastOrder(t *testing.T, ex *upExchange) exchange.OrderAction {
	t.Helper()
	if len(ex.actions) == 0 {
		t.Fatal("nothing was posted")
	}
	a, ok := ex.actions[len(ex.actions)-1].(exchange.OrderAction)
	if !ok {
		t.Fatalf("last action is %T, want exchange.OrderAction", ex.actions[len(ex.actions)-1])
	}
	return a
}

func upGuardIntent() core.Intent {
	return core.Intent{Action: "order", Coin: "BTC", Side: domain.Buy, NotionalUSD: 50, Leverage: 1, Source: "cli"}
}

// ---- Checks nothing else backs up ----

// When today's fills cannot be read, the daily-loss halt fails closed.
func TestRegressDailyLossReadErrorFailsClosed(t *testing.T) {
	env := upEnv(t, &upExchange{})
	env.Config.Trading.AccountAddr = coretest.Account
	env.Config.Risk.DailyLossUSD = 100
	env.HL = &upReader{Reader: env.HL, fillsErr: errors.New("429 rate limited")}
	env.Guard = core.NewGuard(env)
	err := env.Guard.Check(context.Background(), upGuardIntent())
	if upCode(err) != core.CodeRiskLimit || upFailedCheck(err) != "daily_loss" {
		t.Fatalf("fills read error must fail the daily-loss halt: %v", err)
	}
}

// Realized loss includes fees, so a fee-heavy day trips the halt.
func TestRegressDailyLossCountsFees(t *testing.T) {
	ctx := context.Background()
	acct := strings.ToLower(coretest.Account)
	env := upEnv(t, &upExchange{})
	env.Config.Trading.AccountAddr = coretest.Account
	env.Config.Risk.DailyLossUSD = 50
	r := &upReader{Reader: env.HL, fills: map[string][]domain.Fill{
		acct: {{Coin: "BTC", ClosedPnL: 0, Fee: 30}, {Coin: "ETH", ClosedPnL: 0, Fee: 30}},
	}}
	env.HL = r
	env.Guard = core.NewGuard(env)
	if err := env.Guard.Check(ctx, upGuardIntent()); upCode(err) != core.CodeRiskLimit || upFailedCheck(err) != "daily_loss" {
		t.Fatalf("$60 of fees on a $50 halt: %v", err)
	}
	r.fills[acct] = []domain.Fill{{Coin: "BTC", ClosedPnL: 0, Fee: 30}}
	if err := env.Guard.Check(ctx, upGuardIntent()); err != nil {
		t.Fatalf("$30 of fees on a $50 halt: %v", err)
	}
}

// An order for a vault checks the vault's daily loss, not the master's.
func TestRegressVaultOrderReadsVaultDailyLoss(t *testing.T) {
	vault := strings.ToLower(coretest.Growi)
	ex := &upExchange{}
	env := upEnv(t, ex)
	env.Config.Trading.AccountAddr = coretest.Account
	env.Config.Risk.DailyLossUSD = 100
	r := &upReader{Reader: env.HL, fills: map[string][]domain.Fill{
		vault: {{Coin: "BTC", ClosedPnL: -500}},
	}}
	env.HL = r
	env.Guard = core.NewGuard(env)
	_, err := upInvoke(env, "cli", "place_order", `{"side":"buy","coin":"BTC","usd":100,"leverage":2,"vault":"`+vault+`","dry-run":true}`)
	if upCode(err) != core.CodeRiskLimit || upFailedCheck(err) != "daily_loss" {
		t.Fatalf("losing vault must halt its orders: %v (fills read for %v)", err, r.asked)
	}
	if ex.posts != 0 {
		t.Fatal("refused order was signed")
	}
}

// TP/SL trigger legs are reduce-only, so they can never open a position.
func TestRegressTPSLLegsReduceOnly(t *testing.T) {
	for _, args := range []string{
		`{"side":"buy","coin":"BTC","size":"0.01","px":"60000","tp":"66000","sl":"-2%","dry-run":true}`,
		`{"side":"sell","coin":"BTC","size":"0.01","tp":"-5%","sl":"+3%","dry-run":true}`,
	} {
		ex := &upExchange{}
		env := upEnv(t, ex)
		if _, err := upInvoke(env, "cli", "place_order", args); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		a := upLastOrder(t, ex)
		triggers := 0
		for _, o := range a.Orders {
			if o.T.Trigger == nil {
				if o.R {
					t.Errorf("%s: entry leg became reduce-only", args)
				}
				continue
			}
			triggers++
			if !o.R {
				t.Errorf("%s: %s trigger leg is not reduce-only: %+v", args, o.T.Trigger.Tpsl, o)
			}
		}
		if triggers != 2 || a.Grouping != "normalTpsl" {
			t.Fatalf("%s: %d trigger legs, grouping %q", args, triggers, a.Grouping)
		}
	}
}

// Every close leg is reduce-only, so a close can never flip a position.
func TestRegressCloseLegsReduceOnly(t *testing.T) {
	for args, want := range map[string]int{
		`{"coin":"BTC","dry-run":true}`:          1,
		`{"coin":"ETH","pct":50,"dry-run":true}`: 1,
		`{"all":true,"dry-run":true}`:            2,
	} {
		ex := &upExchange{}
		env := upEnv(t, ex)
		env.Config.Trading.AccountAddr = coretest.Account
		if _, err := upInvoke(env, "cli", "close_position", args); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		a := upLastOrder(t, ex)
		if len(a.Orders) != want {
			t.Fatalf("%s: %d legs, want %d", args, len(a.Orders), want)
		}
		for _, o := range a.Orders {
			if !o.R {
				t.Errorf("%s: close leg is not reduce-only: %+v", args, o)
			}
		}
	}
}

// A trusted source (MCP, CLI) with no Confirmer and no yes is refused
// with CONFIRM_REQUIRED; nothing is signed.
func TestRegressNoYesNeedsConfirmation(t *testing.T) {
	ex := &upExchange{}
	env := upEnv(t, ex)
	env.Config.MCP.AllowTrading = true
	env.Guard = core.NewGuard(env)
	env.Config.Trading.AccountAddr = coretest.Account
	for _, src := range []string{"mcp", "cli", "tui"} {
		for tool, args := range map[string]string{
			"place_order":    `{"side":"buy","coin":"BTC","usd":500}`,
			"close_position": `{"coin":"BTC"}`,
			"cancel_order":   `{"coin":"BTC","oid":42}`,
		} {
			if _, err := upInvoke(env, src, tool, args); upCode(err) != core.CodeConfirmRequired {
				t.Errorf("%s %s without yes: %v", src, tool, err)
			}
		}
	}
	if ex.posts != 0 {
		t.Fatalf("signed %d actions without a confirmation", ex.posts)
	}
}

// risk.require_confirm asks the person even when yes is passed.
func TestRegressRequireConfirmAsksEvenWithYes(t *testing.T) {
	defer core.SetLiveBuild(true)()
	ex := &upExchange{}
	env := upEnv(t, ex)
	env.Config.Network = domain.Mainnet
	env.Config.Risk.RequireConfirm = true
	env.Guard = core.NewGuard(env)
	asked := 0
	env.Confirm = func(context.Context, core.OrderPreview) (bool, error) { asked++; return false, nil }
	_, err := upInvoke(env, "cli", "place_order", `{"side":"buy","coin":"BTC","usd":500,"yes":true}`)
	if asked != 1 || upCode(err) != core.CodeSafety {
		t.Fatalf("require_confirm with yes: asked %d, err %v", asked, err)
	}
	if ex.posts != 0 {
		t.Fatal("declined order was signed")
	}
}

// An unknown outcome (timeout, 5xx) is ORDER_PENDING, never success.
func TestRegressUnknownOutcomeIsPending(t *testing.T) {
	defer core.SetLiveBuild(true)()
	ex := &upExchange{res: exchange.Result{Unknown: true}}
	env := upEnv(t, ex)
	_, err := upInvoke(env, "cli", "place_order", `{"side":"buy","coin":"BTC","usd":500,"yes":true}`)
	if upCode(err) != core.CodeOrderPending {
		t.Fatalf("unknown outcome: %v", err)
	}
	if h := core.AsError(err).Hint; !strings.Contains(h, "NOT resend") {
		t.Errorf("hint must say not to resend: %q", h)
	}
}

// Vault transfer amounts must be in (0, 1e9]; a negative withdrawal
// would reach uint64 conversion.
func TestRegressVaultTransferUSDBounds(t *testing.T) {
	env := coretest.Env(t, nil)
	for _, tc := range []struct{ tool, usd string }{
		{"vault_withdraw", "-5"},
		{"vault_withdraw", "2e9"},
		{"vault_deposit", "2e9"},
	} {
		args := `{"vault":"` + coretest.Growi + `","usd":` + tc.usd + `,"dry-run":true}`
		if _, err := upInvoke(env, "cli", tc.tool, args); upCode(err) != core.CodeUsage {
			t.Errorf("%s usd=%s: %v, want USAGE", tc.tool, tc.usd, err)
		}
	}
	// in range still works
	if _, err := upInvoke(env, "cli", "vault_withdraw", `{"vault":"`+coretest.Growi+`","usd":5,"dry-run":true}`); err != nil {
		t.Fatalf("withdraw $5: %v", err)
	}
}

// Deposits into a closed vault, or one not accepting deposits, are
// refused.
func TestRegressDepositClosedVaultRefused(t *testing.T) {
	env := coretest.Env(t, nil)
	for _, addr := range []string{coretest.Closed, coretest.HLPChild1} {
		_, err := upInvoke(env, "cli", "vault_deposit", `{"vault":"`+addr+`","usd":100,"dry-run":true}`)
		if upCode(err) != core.CodeSafety {
			t.Errorf("deposit into %s: %v, want SAFETY_REFUSED", addr, err)
		}
	}
	// withdrawing from a closed vault is still allowed
	if _, err := upInvoke(env, "cli", "vault_withdraw", `{"vault":"`+coretest.Closed+`","usd":100,"dry-run":true}`); err != nil {
		t.Fatalf("withdraw from closed vault: %v", err)
	}
}

// Two curated entries with the same exact name never resolve to
// either: money never goes to a guessed vault.
func TestRegressCuratedSameNameIsAmbiguous(t *testing.T) {
	reg, err := registry.Load([]byte(`schema_version = 1
generated_at = "2026-11-15T00:00:00Z"

[[vault]]
id = "twin-alpha-one"
name = "Twin Alpha"
kind = "hypercore"
address = "0x5555555555555555555555555555555555555555"
chain = "hypercore-mainnet"

[[vault]]
id = "twin-alpha-two"
name = "Twin Alpha"
kind = "hypercore"
address = "0x6666666666666666666666666666666666666666"
chain = "hypercore-mainnet"
`))
	if err != nil {
		t.Fatal(err)
	}
	env := coretest.Env(t, nil)
	env.Vaults = reg
	for _, tool := range []string{"vault_deposit", "vault_withdraw"} {
		res, err := upInvoke(env, "cli", tool, `{"vault":"Twin Alpha","usd":100,"dry-run":true}`)
		if err == nil {
			t.Errorf("%s resolved an ambiguous curated name to %s", tool, res.Data.(*core.OrderPreview).Vault)
		}
	}
	// a unique curated name still resolves
	reg1, err := registry.Load([]byte(`schema_version = 1
[[vault]]
id = "solo-alpha"
name = "Solo Alpha"
kind = "hypercore"
address = "0x5555555555555555555555555555555555555555"
chain = "hypercore-mainnet"
`))
	if err != nil {
		t.Fatal(err)
	}
	env.Vaults = reg1
	res, err := upInvoke(env, "cli", "vault_deposit", `{"vault":"Solo Alpha","usd":100,"dry-run":true}`)
	if err != nil || res.Data.(*core.OrderPreview).Vault != "0x5555555555555555555555555555555555555555" {
		t.Fatalf("unique curated name: %v", err)
	}
}

// Cancel-all on a coin cancels only that coin's orders.
func TestRegressCancelAllOnlyThatCoin(t *testing.T) {
	ex := &upExchange{}
	env := upEnv(t, ex)
	env.Config.Trading.AccountAddr = coretest.Account
	acct := coretest.AccountData()
	acct.OpenOrders = []domain.Order{
		{Coin: "BTC", Side: domain.Buy, Px: 70000, Sz: 0.1, OID: 42},
		{Coin: "ETH", Side: domain.Sell, Px: 2700, Sz: 1, OID: 43},
		{Coin: "BTC", Side: domain.Sell, Px: 80000, Sz: 0.1, OID: 44},
	}
	env.HL = &upReader{Reader: env.HL, accounts: map[string]domain.Account{strings.ToLower(coretest.Account): acct}}
	if _, err := upInvoke(env, "cli", "cancel_order", `{"coin":"BTC","dry-run":true}`); err != nil {
		t.Fatal(err)
	}
	if len(ex.actions) != 1 {
		t.Fatalf("posts %d", len(ex.actions))
	}
	a, ok := ex.actions[0].(exchange.CancelAction)
	if !ok {
		t.Fatalf("action %T", ex.actions[0])
	}
	var oids []uint64
	for _, c := range a.Cancels {
		oids = append(oids, c.O)
		if c.A != 0 {
			t.Errorf("cancel carries asset %d, want BTC (0)", c.A)
		}
	}
	if len(oids) != 2 || oids[0] != 42 || oids[1] != 44 {
		t.Fatalf("cancel BTC sent oids %v, want [42 44]", oids)
	}
}

// With only `account` set, the daily-loss halt reads that account.
func TestRegressDailyLossFallsBackToAccount(t *testing.T) {
	env := upEnv(t, &upExchange{})
	env.Config.Account = coretest.Account
	env.Config.Trading.AccountAddr = ""
	env.Config.Risk.DailyLossUSD = 100
	r := &upReader{Reader: env.HL, fills: map[string][]domain.Fill{}}
	env.HL = r
	env.Guard = core.NewGuard(env)
	if rg, ok := env.Guard.(*core.RiskGuard); !ok || rg.Account != coretest.Account {
		t.Fatalf("guard account %+v", env.Guard)
	}
	if err := env.Guard.Check(context.Background(), upGuardIntent()); err != nil {
		t.Fatalf("account-only config cannot trade: %v", err)
	}
	if len(r.asked) != 1 || r.asked[0] != strings.ToLower(coretest.Account) {
		t.Fatalf("fills read for %v", r.asked)
	}
}

// ---- Core hardening ----

// The gate's Mainnet flag follows the configured network.
func TestRegressGateMainnetFollowsNetwork(t *testing.T) {
	for net, want := range map[domain.Network]bool{domain.Testnet: false, domain.Mainnet: true} {
		ex := &upExchange{}
		env := upEnv(t, ex)
		env.Config.Network = net
		env.Guard = core.NewGuard(env)
		if _, err := upInvoke(env, "cli", "place_order", `{"side":"buy","coin":"BTC","usd":500,"dry-run":true}`); err != nil {
			t.Fatalf("%s: %v", net, err)
		}
		if len(ex.gates) == 0 || ex.gates[0].Mainnet != want {
			t.Fatalf("%s: gates %+v", net, ex.gates)
		}
	}
}

// An "err" status from the exchange is EXCHANGE_REJECTED.
func TestRegressExchangeErrStatus(t *testing.T) {
	ex := &upExchange{res: exchange.Result{Status: "err", Response: "Insufficient margin"}}
	env := upEnv(t, ex)
	_, err := upInvoke(env, "cli", "place_order", `{"side":"buy","coin":"BTC","usd":500,"dry-run":true}`)
	if upCode(err) != core.CodeExchange {
		t.Fatalf("err status: %v", err)
	}
}

// ---- Input validation ----

func TestRegressOrderInputValidation(t *testing.T) {
	env := coretest.Env(t, nil)
	for _, tc := range []struct {
		name, args, code, msg string
	}{
		{"delisted", `{"side":"buy","coin":"OLD","size":"20","px":"1","dry-run":true}`, core.CodeUsage, "delisted"},
		{"usd negative", `{"side":"buy","coin":"BTC","usd":-50,"dry-run":true}`, core.CodeUsage, "usd must be positive"},
		{"size decimals", `{"side":"sell","coin":"BTC","size":"0.000001","reduceOnly":true,"dry-run":true}`, core.CodeUsage, "decimals"},
		{"usd below one lot", `{"side":"sell","coin":"BTC","usd":0.0001,"reduceOnly":true,"dry-run":true}`, core.CodeUsage, "size increment"},
		{"tp percent >= 100", `{"side":"buy","coin":"BTC","size":"0.01","px":"60000","tp":"+150%","dry-run":true}`, core.CodeUsage, "between 0% and 100%"},
		{"sl percent = 100", `{"side":"buy","coin":"BTC","size":"0.01","px":"60000","sl":"-100%","dry-run":true}`, core.CodeUsage, "between 0% and 100%"},
		{"tp percent 0", `{"side":"buy","coin":"BTC","size":"0.01","px":"60000","tp":"0%","dry-run":true}`, core.CodeUsage, "between 0% and 100%"},
		{"px off grid", `{"side":"buy","coin":"BTC","size":"0.01","px":"60000.5","dry-run":true}`, core.CodeInvalidTick, "off the price grid"},
		{"tp off grid", `{"side":"buy","coin":"BTC","size":"0.01","px":"60000","tp":"66000.5","dry-run":true}`, core.CodeInvalidTick, "off the price grid"},
		{"size in dollars", `{"side":"buy","coin":"BTC","size":"$100","dry-run":true}`, core.CodeUsage, "not dollars"},
	} {
		_, err := upInvoke(env, "cli", "place_order", tc.args)
		if upCode(err) != tc.code || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: %v, want %s containing %q", tc.name, err, tc.code, tc.msg)
		}
	}
}

// A market order with no book, mid or mark is STALE, not sized from 0.
func TestRegressOrderNoReferencePriceIsStale(t *testing.T) {
	dir := coretest.Dir(t)
	ms := append(coretest.Markets(), domain.Market{Coin: "NOPX", Display: "NOPX", Kind: domain.Perp, AssetID: 99, SzDecimals: 2, MaxLeverage: 5})
	b, _ := json.Marshal(ms)
	if err := os.WriteFile(filepath.Join(dir, "markets.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	env := coretest.Env(t, nil)
	env.HL = core.NewFixtureReader(dir)
	for _, args := range []string{
		`{"side":"buy","coin":"NOPX","size":"100","dry-run":true}`,
		`{"side":"sell","coin":"NOPX","size":"100","reduceOnly":true,"dry-run":true}`,
	} {
		if _, err := upInvoke(env, "cli", "place_order", args); upCode(err) != core.CodeStale {
			t.Errorf("%s: %v, want STALE_DATA", args, err)
		}
	}
}

func TestRegressCancelValidation(t *testing.T) {
	env := coretest.Env(t, nil)
	env.Config.Trading.AccountAddr = coretest.Account
	if _, err := upInvoke(env, "cli", "cancel_order", `{"oid":42,"dry-run":true}`); upCode(err) != core.CodeUsage || !strings.Contains(err.Error(), "needs a coin") {
		t.Errorf("cancel without coin: %v", err)
	}
	// the fixture account has only a BTC order
	if _, err := upInvoke(env, "cli", "cancel_order", `{"coin":"ETH","dry-run":true}`); upCode(err) != core.CodeNotFound {
		t.Errorf("cancel-all with nothing open: %v, want NOT_FOUND", err)
	}
}

// A close smaller than one size increment is refused, not sent as size 0.
func TestRegressCloseBelowOneLot(t *testing.T) {
	env := coretest.Env(t, nil)
	env.Config.Trading.AccountAddr = coretest.Account
	if _, err := upInvoke(env, "cli", "close_position", `{"coin":"BTC","pct":0.001,"dry-run":true}`); upCode(err) != core.CodeUsage {
		t.Fatalf("close below one lot: %v", err)
	}
}

// Closed vaults do not count when resolving a deposit name, so the one
// open vault of that name resolves; a withdrawal still sees both.
func TestRegressDepositNameSkipsClosedVaults(t *testing.T) {
	const open, closed = "0x8888888888888888888888888888888888888888", "0x9999999999999999999999999999999999999999"
	dir := coretest.Dir(t)
	vs := append(coretest.Vaults(),
		domain.VaultSummary{Address: open, Name: "Zeta Prime", Kind: domain.VaultHyperCore, Rel: "normal", TVL: 2e6, AgeDays: 200, AllowDeposits: true},
		domain.VaultSummary{Address: closed, Name: "Zeta Prime Legacy", Kind: domain.VaultHyperCore, Rel: "normal", TVL: 1e5, AgeDays: 500, Closed: true},
	)
	b, _ := json.Marshal(vs)
	if err := os.WriteFile(filepath.Join(dir, "vaults.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	env := coretest.Env(t, nil)
	env.HL = core.NewFixtureReader(dir)
	res, err := upInvoke(env, "cli", "vault_deposit", `{"vault":"zeta prime","usd":100,"dry-run":true}`)
	if err != nil || res.Data.(*core.OrderPreview).Vault != open {
		t.Fatalf("deposit by name of the only open match: %v", err)
	}
	// withdrawals may name a closed vault, so the same name is ambiguous
	if _, err := upInvoke(env, "cli", "vault_withdraw", `{"vault":"zeta prime","usd":100,"dry-run":true}`); upCode(err) != core.CodeUsage {
		t.Fatalf("withdraw by an ambiguous name: %v", err)
	}
	// and the full name of a closed vault is not a deposit target at all
	if _, err := upInvoke(env, "cli", "vault_deposit", `{"vault":"Zeta Prime Legacy","usd":100,"dry-run":true}`); upCode(err) != core.CodeUnknownVault {
		t.Fatalf("deposit into a closed vault by name: %v", err)
	}
}

// ---- Checks another check backs up; pinned for their own reason ----

// An expired trading window says so, even without an agent key.
func TestRegressExpiredTradingSaysExpired(t *testing.T) {
	env := coretest.Env(t, nil)
	env.Config.Trading.Enabled = true
	env.Config.Trading.Expires = coretest.Now.Add(-48 * time.Hour).Format("2006-01-02")
	env.Exchange = nil
	env.Guard = core.NewGuard(env)
	_, err := upInvoke(env, "cli", "place_order", `{"side":"buy","coin":"BTC","usd":500,"yes":true}`)
	if upCode(err) != core.CodeTradingDisabled || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired trading without a key: %v", err)
	}
}

// An agent without a Confirmer is told a person must confirm, never to
// re-run with --yes (its yes is ignored).
func TestRegressAgentRefusalNeverSuggestsYes(t *testing.T) {
	ex := &upExchange{}
	env := upEnv(t, ex)
	for _, src := range []string{"agent", "bot", core.SourceUnknown} {
		_, err := upInvoke(env, src, "place_order", `{"side":"buy","coin":"BTC","usd":500,"yes":true}`)
		e := core.AsError(err)
		if upCode(err) != core.CodeConfirmRequired || !strings.Contains(e.Message, "person") || strings.Contains(e.Hint, "--yes") {
			t.Errorf("%s: %v (hint %q)", src, err, e.Hint)
		}
	}
	if ex.posts != 0 {
		t.Fatal("signed without a person")
	}
}

// A Confirmer error aborts, even if it also returned true.
func TestRegressConfirmErrorAborts(t *testing.T) {
	ex := &upExchange{}
	env := upEnv(t, ex)
	boom := errors.New("terminal closed")
	for _, ok := range []bool{true, false} {
		env.Confirm = func(context.Context, core.OrderPreview) (bool, error) { return ok, boom }
		_, err := upInvoke(env, "cli", "place_order", `{"side":"buy","coin":"BTC","usd":500}`)
		if err == nil || !strings.Contains(err.Error(), boom.Error()) {
			t.Errorf("confirm (%v, err): got %v, want the confirmer's error", ok, err)
		}
	}
	if ex.posts != 0 {
		t.Fatal("signed after a failed confirmation")
	}
}
