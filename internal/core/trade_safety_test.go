package core_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/config"
	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/core/coretest"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/hyperliquid/exchange"
)

// publicTestKey is the Hyperliquid Python SDK's public test key.
const publicTestKey = "0x0123456789012345678901234567890123456789012345678901234567890123"

// fakeExchange records posts and answers like a live, sent order. It never
// touches the network.
type fakeExchange struct {
	posts   int
	gates   []exchange.Gate
	actions []any
}

func (f *fakeExchange) Post(_ context.Context, action any, vault *string, g exchange.Gate) (exchange.Result, error) {
	f.posts++
	f.gates = append(f.gates, g)
	f.actions = append(f.actions, action)
	return exchange.Result{DryRun: !g.Live, Status: "ok", Envelope: exchange.Envelope{Action: action, Nonce: 1, VaultAddress: vault,
		Signature: exchange.Signature{R: "0xaa", S: "0xbb", V: 27}}}, nil
}

func tradingEnv(t *testing.T, ex exchange.Client) *core.Env {
	t.Helper()
	env := coretest.Env(t, nil)
	env.Config.Trading.Enabled = true
	env.Config.Risk = config.RiskConfig{MaxNotionalUSD: 1e9}
	env.Exchange = ex
	env.Guard = core.NewGuard(env)
	return env
}

func TestDryRunNeverLeaksReplayableSignature(t *testing.T) {
	s, err := exchange.NewSigner(publicTestKey)
	if err != nil {
		t.Fatal(err)
	}
	env := tradingEnv(t, exchange.NewClient("http://127.0.0.1:1/exchange", s))
	res := mustInvoke(t, env, "place_order", `{"side":"buy","coin":"BTC","usd":500,"dry-run":true}`)
	p := res.Data.(*core.OrderPreview)
	if !p.DryRun || len(p.Envelope) == 0 || res.Meta.Source != "dry-run" {
		t.Fatalf("dry run preview %+v", p)
	}
	var e map[string]any
	if err := json.Unmarshal(p.Envelope, &e); err != nil {
		t.Fatal(err)
	}
	if sig, _ := e["signature"].(string); !strings.Contains(sig, "withheld") {
		t.Fatalf("signature not withheld: %s", p.Envelope)
	}
	if cid, _ := e["connectionId"].(string); !strings.HasPrefix(cid, "0x") || len(cid) != 66 {
		t.Fatalf("connection id %v", e["connectionId"])
	}
	exp, _ := e["expiresAfter"].(float64)
	nonce, _ := e["nonce"].(float64)
	if exp == 0 || exp >= nonce {
		t.Fatalf("dry run not signed expired: nonce %v expiresAfter %v", nonce, exp)
	}
}

func TestLivePreviewIsNotLabelledDryRun(t *testing.T) {
	defer core.SetLiveBuild(true)()
	ex := &fakeExchange{}
	env := tradingEnv(t, ex)
	var asked []core.OrderPreview
	env.Confirm = func(_ context.Context, p core.OrderPreview) (bool, error) {
		asked = append(asked, p)
		return true, nil
	}
	res := mustInvoke(t, env, "place_order", `{"side":"buy","coin":"BTC","usd":500}`)
	p := res.Data.(*core.OrderPreview)
	if len(asked) != 1 || asked[0].DryRun {
		t.Fatalf("confirm saw dry run for a live order: %+v", asked)
	}
	if p.DryRun || res.Meta.Source == "dry-run" || ex.posts != 1 || !ex.gates[0].Live {
		t.Fatalf("live result %+v source %s posts %d", p, res.Meta.Source, ex.posts)
	}
	// a sent envelope is recorded with its signature withheld too (audit S9)
	if strings.Contains(string(p.Envelope), `"0xaa"`) || !strings.Contains(string(p.Envelope), `"withheld"`) {
		t.Fatalf("sent envelope must withhold the signature: %s", p.Envelope)
	}
	// an explicit dry run on a live build stays a dry run
	res = mustInvoke(t, env, "place_order", `{"side":"buy","coin":"BTC","usd":500,"dry-run":true}`)
	if p := res.Data.(*core.OrderPreview); !p.DryRun || strings.Contains(string(p.Envelope), `"0xaa"`) {
		t.Fatalf("dry run on live build: %+v", p)
	}
}

func TestAgentCannotPreApprove(t *testing.T) {
	ex := &fakeExchange{}
	env := tradingEnv(t, ex)
	ctx := core.WithSource(context.Background(), "agent")
	call := core.Call{Tool: "place_order", Args: json.RawMessage(`{"side":"buy","coin":"BTC","usd":500,"yes":true}`)}
	if _, err := core.Default().Invoke(ctx, env, call); code(err) != core.CodeConfirmRequired {
		t.Fatalf("agent yes without a confirmer: %v", err)
	}
	confirmed := 0
	env.Confirm = func(context.Context, core.OrderPreview) (bool, error) { confirmed++; return false, nil }
	if _, err := core.Default().Invoke(ctx, env, call); code(err) != core.CodeSafety || confirmed != 1 {
		t.Fatalf("agent yes must still ask: %v (asked %d)", err, confirmed)
	}
	if ex.posts != 0 {
		t.Fatal("nothing should be signed")
	}
}

func TestOrderInputStrictness(t *testing.T) {
	env := coretest.Env(t, nil)
	for _, args := range []string{
		`{"side":"buy","coin":"BTC","size":"$100","dry-run":true}`,
		`{"side":"buy","coin":"BTC","size":"1k","dry-run":true}`,
		`{"side":"buy","coin":"BTC","size":"0.01","px":"5%","dry-run":true}`,
		`{"side":"buy","coin":"BTC","size":"0.01","px":"60000","tp":"50000","dry-run":true}`,
		`{"side":"buy","coin":"BTC","size":"0.01","px":"60000","sl":"70000","dry-run":true}`,
		`{"side":"sell","coin":"BTC","size":"0.01","px":"60000","tp":"70000","dry-run":true}`,
	} {
		if _, err := invoke(t, env, "place_order", args); code(err) != core.CodeUsage {
			t.Errorf("%s: %v, want USAGE", args, err)
		}
	}
	res := mustInvoke(t, env, "place_order", `{"side":"buy","coin":"BTC","size":"0.01","px":"60000","tp":"66000","sl":"-2%","dry-run":true}`)
	p := res.Data.(*core.OrderPreview)
	if p.Side != domain.Buy || p.Px != "60000" {
		t.Fatalf("valid tp/sl %+v", p)
	}
	// The preview (card and JSON) carries both trigger legs.
	if len(p.Triggers) != 2 || p.Grouping != "normalTpsl" || p.Triggers[0].Kind != "tp" || p.Triggers[0].TriggerPx != "66000" ||
		p.Triggers[1].Kind != "sl" || p.Triggers[1].TriggerPx != "58800" || p.Triggers[1].Side != domain.Sell || p.Triggers[0].Size != "0.01" {
		t.Fatalf("trigger legs %+v grouping %q", p.Triggers, p.Grouping)
	}
	b, _ := json.Marshal(p)
	if !strings.Contains(string(b), `"triggers":[{"kind":"tp","triggerPx":"66000"`) {
		t.Errorf("preview JSON lacks the legs: %s", b)
	}
	// A short far above the market with a percent TP: the TP sits above
	// the mark, so it would fire as soon as the entry fills. Say so.
	res = mustInvoke(t, env, "place_order", `{"side":"sell","coin":"BTC","size":"0.01","px":"120000","tp":"-5%","dry-run":true}`)
	p = res.Data.(*core.OrderPreview)
	if len(p.Triggers) != 1 || !p.Triggers[0].Through || len(p.Warnings) == 0 {
		t.Fatalf("through-mark TP not flagged: %+v warnings %v", p.Triggers, p.Warnings)
	}
}

func TestParseDecimal(t *testing.T) {
	for in, want := range map[string]string{"0.01": "0.01", " 1,000.5 ": "1000.5", "2_000": "2000"} {
		d, err := core.ParseDecimal("size", in)
		if err != nil || d.String() != want {
			t.Errorf("%q: %v %v", in, d, err)
		}
	}
	for _, in := range []string{"", ".", "-1", "0", "$5", "5%", "1k", "1e3", "1.2.3"} {
		if _, err := core.ParseDecimal("size", in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}
