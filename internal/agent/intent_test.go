package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/core"
)

// registryWithClose is the default catalog, which now ships close_position.
func registryWithClose() *core.Registry { return core.Default() }

type intentCase struct {
	in   string
	tool string
	args string // JSON object; compared structurally
}

// docExamples are the examples listed in ParseIntent's doc comment.
var docExamples = []intentCase{
	{"chart btc 4h", "get_candles", `{"coin":"BTC","interval":"4h"}`},
	{"btc", "get_candles", `{"coin":"BTC"}`},
	{"how's hype", "get_candles", `{"coin":"HYPE"}`},
	{"book eth", "get_orderbook", `{"coin":"ETH"}`},
	{"top vaults by sharpe", "list_vaults", `{"sort":"sharpe"}`},
	{"vaults under 20% drawdown", "list_vaults", `{"maxDd":20}`},
	{"vault growi", "get_vault", `{"vault":"growi"}`},
	{"compare hlp growi", "compare_vaults", `{"vaults":["hlp","growi"]}`},
	{"funding", "get_funding", `{}`},
	{"watch 0xabc...", "watch", `{"address":"0xabc..."}`},
	{"buy $100 hype", "place_order", `{"side":"buy","coin":"HYPE","usd":100,"dryRun":true}`},
	{"close 50% btc", CloseTool, `{"coin":"BTC","pct":50,"dryRun":true}`},
	{"nuke", CloseTool, `{"all":true,"dryRun":true}`},
}

var morePhrasings = []intentCase{
	{"Chart ETH 1h", "get_candles", `{"coin":"ETH","interval":"1h"}`},
	{"show me the sol chart on the daily", "get_candles", `{"coin":"SOL","interval":"1d"}`},
	{"what's btc doing?", "get_candles", `{"coin":"BTC"}`},
	{"hype 15m", "get_candles", `{"coin":"HYPE","interval":"15m"}`},
	{"btc 4 hours", "get_candles", `{"coin":"BTC","interval":"4h"}`},
	{"chart xyz:nvda 1d", "get_candles", `{"coin":"xyz:NVDA","interval":"1d"}`},
	{"chart btc 1M", "get_candles", `{"coin":"BTC","interval":"1M"}`},
	{"chart btc 1m", "get_candles", `{"coin":"BTC","interval":"1m"}`},
	{"chart eth with ma 20 50", "get_candles", `{"coin":"ETH","ma":[20,50]}`},
	{"chart sol heikin ashi weekly", "get_candles", `{"coin":"SOL","style":"heikin","interval":"1w"}`},
	{"chart sol heikin weekly", "get_candles", `{"coin":"SOL","style":"heikin","interval":"1w"}`},
	{"chart pengu", "get_candles", `{"coin":"PENGU"}`},
	{"chart @107", "get_candles", `{"coin":"@107"}`},
	{"price of hype/usdc", "get_price", `{"coins":["HYPE/USDC"]}`},
	{"btc price", "get_price", `{"coins":["BTC"]}`},
	{"prices btc eth and sol", "get_price", `{"coins":["BTC","ETH","SOL"]}`},
	{"price btc 4h", "get_candles", `{"coin":"BTC","interval":"4h"}`},
	{"btc price chart", "get_candles", `{"coin":"BTC"}`},
	{"orderbook for btc", "get_orderbook", `{"coin":"BTC"}`},
	{"eth depth 20", "get_orderbook", `{"coin":"ETH","depth":20}`},
	{"tape sol", "get_trades", `{"coin":"SOL"}`},
	{"btc trades", "get_trades", `{"coin":"BTC"}`},
	{"asset id hype", "get_asset_id", `{"coin":"HYPE"}`},
	{"funding rates", "get_funding", `{}`},
	{"funding btc", "get_funding", `{"coin":"BTC"}`},
	{"btc funding last 24h", "get_funding", `{"coin":"BTC","hours":24}`},
	{"top 10 funding", "get_funding", `{"top":10}`},
	{"markets", "get_markets", `{}`},
	{"spot markets", "get_markets", `{"kind":"spot"}`},
	{"top gainers", "get_markets", `{"sort":"change"}`},
	{"xyz markets", "get_markets", `{"dex":"xyz"}`},
	{"markets by open interest", "get_markets", `{"sort":"oi"}`},
	{"best vaults", "list_vaults", `{"sort":"sharpe"}`},
	{"top 10 vaults by tvl", "list_vaults", `{"limit":10,"sort":"tvl"}`},
	{"open vaults over $1m tvl", "list_vaults", `{"open":true,"minTvl":1000000}`},
	{"curated vaults", "list_vaults", `{"curated":true}`},
	{"ai vaults", "list_vaults", `{"query":"ai"}`},
	{"vaults older than 90 days", "list_vaults", `{"minAge":90}`},
	{"vaults by 30d return", "list_vaults", `{"sort":"ret30d"}`},
	{"biggest vaults including hlp", "list_vaults", `{"sort":"tvl","hlp":true}`},
	{"vaults with max dd 15", "list_vaults", `{"maxDd":15}`},
	{"vault Long HYPE & BTC", "get_vault", `{"vault":"Long HYPE & BTC"}`},
	{"growi vault", "get_vault", `{"vault":"growi"}`},
	{"tearsheet for hlp", "get_vault", `{"vault":"hlp"}`},
	{"due diligence on growi", "get_vault", `{"vault":"growi"}`},
	{"compare hlp vs growi vs Long HYPE & BTC", "compare_vaults", `{"vaults":["hlp","growi","Long HYPE & BTC"]}`},
	{"hlp vault vs growi", "compare_vaults", `{"vaults":["hlp","growi"]}`},
	{"hlp", "hlp_positioning", `{}`},
	{"what is hlp long?", "hlp_positioning", `{}`},
	{"hlp positioning", "hlp_positioning", `{}`},
	{"my positions", "get_account", `{}`},
	{"positions 0x1111111111111111111111111111111111111111", "get_account", `{"address":"0x1111111111111111111111111111111111111111"}`},
	{"0x1111111111111111111111111111111111111111", "get_account", `{"address":"0x1111111111111111111111111111111111111111"}`},
	{"my fills", "get_fills", `{}`},
	{"fills 0x2222222222222222222222222222222222222222 btc 48h", "get_fills", `{"address":"0x2222222222222222222222222222222222222222","coin":"BTC","hours":48}`},
	{"spectate 0x3333333333333333333333333333333333333333", "watch", `{"address":"0x3333333333333333333333333333333333333333"}`},
	{"sell 0.10 eth at 3500", "place_order", `{"side":"sell","coin":"ETH","size":"0.1","px":"3500","dryRun":true}`},
	{"long 3x btc $500 tp 5% sl 2%", "place_order", `{"side":"buy","coin":"BTC","usd":500,"leverage":3,"tp":"+5%","sl":"-2%","dryRun":true}`},
	{"short hype 200 usd @ 41.5 reduce only", "place_order", `{"side":"sell","coin":"HYPE","usd":200,"px":"41.5","reduceOnly":true,"dryRun":true}`},
	{"buy $1.5k of sol", "place_order", `{"side":"buy","coin":"SOL","usd":1500,"dryRun":true}`},
	{"buy 10 hype", "place_order", `{"side":"buy","coin":"HYPE","size":"10","dryRun":true}`},
	{"close half my eth", CloseTool, `{"coin":"ETH","pct":50,"dryRun":true}`},
	{"close all", CloseTool, `{"all":true,"dryRun":true}`},
	{"close btc", CloseTool, `{"coin":"BTC","dryRun":true}`},
	{"cancel all btc", "cancel_order", `{"coin":"BTC","dryRun":true}`},
	{"cancel 123456 eth", "cancel_order", `{"oid":123456,"coin":"ETH","dryRun":true}`},
	{"deposit $500 into growi", "vault_deposit", `{"vault":"growi","usd":500,"dryRun":true}`},
	{"withdraw 200 from hlp", "vault_withdraw", `{"vault":"hlp","usd":200,"dryRun":true}`},
	{"backtest examples/pine/ema-cross.pine on btc 1h over 90 days", "run_backtest", `{"file":"examples/pine/ema-cross.pine","coin":"BTC","interval":"1h","days":90}`},
	{"backtest long xyz:SP500 1.5x, short DOGE FARTCOIN TRUMP, rebalance 1w", "run_backtest", `{"thesis":"long xyz:SP500 1.5x, short DOGE FARTCOIN TRUMP, rebalance 1w"}`},
	{"long HYPE BTC short DOGE PUMP", "run_backtest", `{"thesis":"long HYPE BTC short DOGE PUMP"}`},
	{"bt thesis.yaml $5000", "run_backtest", `{"file":"thesis.yaml","capital":5000}`},
	{"backtest long HYPE, short memes over 180 days with $20k", "run_backtest", `{"thesis":"long HYPE, short memes","days":180,"capital":20000}`},
	{"pine check strat.pine", "pine_check", `{"file":"strat.pine"}`},
	{"check my.pine", "pine_check", `{"file":"my.pine"}`},
	{"pine run ema.pine btc 4h", "run_backtest", `{"file":"ema.pine","coin":"BTC","interval":"4h"}`},
	{"paper", "paper", `{"action":"list"}`},
	{"paper start ema.pine on btc", "paper", `{"action":"start","file":"ema.pine","coin":"BTC"}`},
	{"paper status p1", "paper", `{"action":"status","id":"p1"}`},
	{"paper stop p1", "paper", `{"action":"stop","id":"p1"}`},
	{"arena a.pine b.yaml llm:qwen3", "arena", `{"entrants":["a.pine","b.yaml","llm:qwen3"]}`},
	{"arena vaults hlp vs growi", "arena", `{"vaults":["hlp","growi"]}`},
	{"index", "list_indexes", `{}`},
	{"index mavericks", "list_indexes", `{"slug":"mavericks"}`},
	{"fund", "get_strategy_vault", `{}`},
	{"pvault trend-pyramid", "get_strategy_vault", `{"vault":"trend-pyramid"}`},
	{"fund epochs", "get_strategy_vault_epochs", `{}`},
	{"fund user 0x7ffb5ea882eec736200c51209d668a6972092000", "get_strategy_vault_user", `{"user":"0x7ffb5ea882eec736200c51209d668a6972092000"}`},
	{"discover memes", "discover_vaults", `{"tag":["memes"]}`},
	{"strat spxmeme", "get_strategy", `{"id":"spxmeme"}`},
	{"bluechip", "get_strategy", `{"id":"bluechip"}`},
	{"schema get_candles", "schema", `{"tool":"get_candles"}`},
	{"please show me the eth chart", "get_candles", `{"coin":"ETH"}`},
}

var notUnderstood = []string{
	"", "/chart btc", "hello", "what should I buy?", "explain funding to me like I'm five",
	"is hlp a good investment compared to holding btc for the long term", "buy hype", "cancel",
	"vault", "watch", "compare hlp", "thanks!", "random gibberish words", "strategy",
	// Free-form questions go to the model, not to a vault name search.
	"which vaults have the best risk-adjusted returns over the last 90 days?",
	"which vaults have the best risk-adjusted returns right now?",
	"vaults that have been safest this year",
	"where should I park USDC for steady returns with shallow drawdowns?",
	"why is hlp down this week?",
	"should I buy hype",
}

func TestParseIntentNameQueriesStillWork(t *testing.T) {
	for in, want := range map[string]string{
		"ai vaults":                             `{"query":"ai"}`,
		"vaults named long hype btc":            `{"query":"long hype btc"}`,
		"best vaults for risk-adjusted returns": `{"sort":"sharpe"}`,
	} {
		call, ok := ParseIntent(in, nil)
		if !ok || call.Tool != "list_vaults" || string(call.Args) != want {
			t.Errorf("%q -> %v %s %s, want list_vaults %s", in, ok, call.Tool, call.Args, want)
		}
	}
}

func TestSuggest(t *testing.T) {
	r := core.Default()
	cases := map[string]string{
		"which vaults have the best risk-adjusted returns over the last 90 days?": "/vaults --sort sharpe",
		"top 10 vaults by tvl":       "/vaults --sort tvl --limit 10",
		"vaults named long hype btc": `/vaults --query "long hype btc"`,
	}
	for in, want := range cases {
		got, ok := Suggest(in, r)
		if !ok {
			t.Errorf("%q: no suggestion, want %s", in, want)
			continue
		}
		// The suggestion must parse back to the same call.
		back, err := r.ParseLine(got)
		if err != nil {
			t.Errorf("%q: suggestion %q does not parse: %v", in, got, err)
			continue
		}
		wantCall, err := r.ParseLine(want)
		if err != nil {
			t.Fatalf("bad want %q: %v", want, err)
		}
		var a, b any
		_ = json.Unmarshal(back.Args, &a)
		_ = json.Unmarshal(wantCall.Args, &b)
		if back.Tool != wantCall.Tool || !reflect.DeepEqual(a, b) {
			t.Errorf("%q: suggestion %q (%s), want %q (%s)", in, got, back.Args, want, wantCall.Args)
		}
	}
	// Trades are never suggested from loose text.
	if s, ok := Suggest("should I buy $100 hype", r); ok {
		t.Errorf("trade suggested: %s", s)
	}
}

func TestParseIntentTable(t *testing.T) {
	r := registryWithClose()
	all := append(append([]intentCase(nil), docExamples...), morePhrasings...)
	for _, c := range all {
		call, ok := ParseIntent(c.in, r)
		if c.tool == "" {
			if ok {
				t.Errorf("%q: want not understood, got %s %s", c.in, call.Tool, call.Args)
			}
			continue
		}
		if !ok {
			t.Errorf("%q: not understood, want %s %s", c.in, c.tool, c.args)
			continue
		}
		if call.Tool != c.tool {
			t.Errorf("%q: tool %s, want %s (args %s)", c.in, call.Tool, c.tool, call.Args)
			continue
		}
		var got, want any
		_ = json.Unmarshal(call.Args, &got)
		if err := json.Unmarshal([]byte(c.args), &want); err != nil {
			t.Fatalf("bad test args %q: %v", c.args, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: args %s, want %s", c.in, call.Args, c.args)
		}
	}
	if len(morePhrasings) < 20 {
		t.Fatal("need at least 20 extra phrasings")
	}
}

// Every understood phrase must decode strictly into the tool's input, so
// Invoke never fails on a USAGE error caused by the parser.
func TestParseIntentArgsDecodeStrictly(t *testing.T) {
	r := registryWithClose()
	for _, c := range append(append([]intentCase(nil), docExamples...), morePhrasings...) {
		call, ok := ParseIntent(c.in, r)
		if !ok {
			continue
		}
		tool, _ := r.Get(call.Tool)
		if _, err := core.DecodeInput(tool, call.Args); err != nil {
			t.Errorf("%q -> %s %s: %v", c.in, call.Tool, call.Args, err)
		}
		if tool.Mutating {
			if strings.Contains(string(call.Args), `"yes"`) || !strings.Contains(string(call.Args), `"dryRun":true`) {
				t.Errorf("%q: mutating call must be dry-run without yes: %s", c.in, call.Args)
			}
		}
	}
}

func TestParseIntentNotUnderstood(t *testing.T) {
	for _, in := range notUnderstood {
		if call, ok := ParseIntent(in, nil); ok {
			t.Errorf("%q: want ok=false, got %s %s", in, call.Tool, call.Args)
		}
	}
}

func TestParseIntentRespectsRegistry(t *testing.T) {
	// The default catalog ships close_position; a registry without it does
	// not understand close phrases.
	if call, ok := ParseIntent("nuke", core.Default()); !ok || call.Tool != CloseTool {
		t.Fatalf("nuke -> %v %v, want %s", call.Tool, ok, CloseTool)
	}
	if _, ok := ParseIntent("nuke", core.NewRegistry()); ok {
		t.Fatal("close_position absent from registry")
	}
	// nil registry = default catalog.
	if call, ok := ParseIntent("btc", nil); !ok || call.Tool != "get_candles" {
		t.Fatal("nil registry should use the default catalog")
	}
	// A registry without get_candles cannot answer chart phrases.
	if _, ok := ParseIntent("chart btc", core.NewRegistry()); ok {
		t.Fatal("tool absent from registry")
	}
}

func TestParseIntentIsCoin(t *testing.T) {
	listed := map[string]bool{"MOODENG": true}
	o := IntentOptions{IsCoin: func(s string) bool { return listed[strings.ToUpper(s)] }}
	if call, ok := ParseIntentWith("moodeng 1h", nil, o); !ok || string(call.Args) != `{"coin":"MOODENG","interval":"1h"}` {
		t.Fatalf("registry coin: %v %s", ok, call.Args)
	}
	if _, ok := ParseIntentWith("moodeng", nil, IntentOptions{}); ok {
		t.Fatal("unknown bare word without a registry must not be a chart")
	}
	if _, ok := ParseIntentWith("btc", nil, IntentOptions{IsCoin: func(string) bool { return false }}); ok {
		t.Fatal("IsCoin=false must reject bare words")
	}
	// Keyword phrases accept unknown coins and let the tool error.
	if call, ok := ParseIntentWith("chart nopecoin", nil, IntentOptions{IsCoin: func(string) bool { return false }}); !ok || call.Tool != "get_candles" {
		t.Fatal("keyword phrase should pass through")
	}
}

func TestTokenizeAndHelpers(t *testing.T) {
	ts := tokenize(`  "Show"  me  BTC, 4 hours…  `)
	var ws []string
	for _, x := range ts {
		ws = append(ws, x.w)
	}
	if strings.Join(ws, "|") != "show|me|btc|4hours" {
		t.Fatalf("tokens %v", ws)
	}
	if v, usd, ok := money("$2.5m"); !ok || !usd || v != 2.5e6 {
		t.Fatal("money")
	}
	if v, usd, ok := money("300"); !ok || usd || v != 300 {
		t.Fatal("plain money")
	}
	if _, _, ok := money("abc"); ok {
		t.Fatal("money abc")
	}
	if d, ok := durationDays("6months"); !ok || d != 180 {
		t.Fatal("months")
	}
	if h, ok := durationHours("3days"); !ok || h != 72 {
		t.Fatal("hours")
	}
	if signedTarget("5%", "-") != "-5%" || signedTarget("+5%", "-") != "+5%" || signedTarget("31.50", "+") != "31.5" {
		t.Fatal("signedTarget")
	}
	if parseInterval(tok{o: "1M", w: "1m"}) != "1M" || parseInterval(tok{o: "weekly", w: "weekly"}) != "1w" || parseInterval(tok{o: "7h", w: "7h"}) != "" {
		t.Fatal("interval")
	}
}
