package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/hlpxyz"
	"github.com/maxmoneycash/hlp-cli/internal/hyperliquid"
)

func tool(t *testing.T, name string) Tool {
	t.Helper()
	tl, ok := Default().Get(name)
	if !ok {
		t.Fatalf("no tool %s", name)
	}
	return tl
}

func TestParseArgs(t *testing.T) {
	cases := []struct {
		tool string
		argv []string
		want string // JSON, or "" when an error is expected
		code string
	}{
		{"get_candles", []string{"BTC", "15m", "--bars", "200", "--ma", "20,50"}, `{"bars":200,"coin":"BTC","interval":"15m","ma":[20,50]}`, ""},
		{"get_candles", []string{"--coin=eth", "4h"}, `{"coin":"eth","interval":"4h"}`, ""},
		{"get_candles", []string{"btc", "--ma", "20", "--ma", "50", "--live"}, `{"coin":"btc","live":true,"ma":[20,50]}`, ""},
		{"get_candles", []string{"btc", "--live=false"}, `{"coin":"btc","live":false}`, ""},
		{"get_candles", []string{"btc", "--bars", "1.5k"}, `{"bars":1500,"coin":"btc"}`, ""},
		{"get_candles", []string{"btc", "--bars", "1.5"}, "", CodeUsage},
		{"get_candles", []string{"btc", "--nope", "1"}, "", CodeUsage},
		{"get_candles", []string{"btc", "1h", "extra"}, "", CodeUsage},
		{"get_candles", []string{"btc", "--bars"}, "", CodeUsage},
		{"list_vaults", []string{"--min-tvl", "$100k", "--max-dd", "20%", "--open", "--sort", "sharpe"}, `{"maxDd":20,"minTvl":100000,"open":true,"sort":"sharpe"}`, ""},
		{"list_vaults", []string{"long", "hype", "&", "btc"}, `{"query":"long hype & btc"}`, ""},
		{"list_vaults", []string{"--min_tvl=1.5m"}, `{"minTvl":1500000}`, ""},
		{"list_vaults", []string{"--minTvl=2b"}, `{"minTvl":2000000000}`, ""},
		{"list_vaults", []string{"--no-open"}, `{"open":false}`, ""},
		{"get_vault", []string{"Long", "HYPE", "&", "BTC", "--bench", "HLP,BTC", "--bench", "SPY"}, `{"bench":["HLP","BTC","SPY"],"vault":"Long HYPE & BTC"}`, ""},
		{"compare_vaults", []string{"hlp", "growi,0xabc"}, `{"vaults":["hlp","growi","0xabc"]}`, ""},
		{"place_order", []string{"buy", "BTC", "0.01", "--tp", "+5%", "--sl", "-2%", "--usd", "$1,000"}, `{"coin":"BTC","side":"buy","size":"0.01","sl":"-2%","tp":"+5%","usd":1000}`, ""},
		{"place_order", []string{"sell", "ETH", "--", "--weird"}, `{"coin":"ETH","side":"sell","size":"--weird"}`, ""},
		{"cancel_order", []string{"BTC", "123456789012"}, `{"coin":"BTC","oid":123456789012}`, ""},
		{"cancel_order", []string{"BTC", "-5"}, "", CodeUsage},
		{"paper", []string{"start", "x.pine", "--coin", "BTC", "--capital", "10k"}, `{"action":"start","capital":10000,"coin":"BTC","file":"x.pine"}`, ""},
		{"hlp_positioning", nil, `{}`, ""},
		{"hlp_positioning", []string{"x"}, "", CodeUsage},
	}
	for _, c := range cases {
		t.Run(fmt.Sprint(c.tool, c.argv), func(t *testing.T) {
			got, err := ParseArgs(tool(t, c.tool), c.argv)
			if c.want == "" {
				var e *Error
				if !errors.As(err, &e) || e.Code != c.code {
					t.Fatalf("want %s error, got %v (%s)", c.code, err, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Fatalf("got  %s\nwant %s", got, c.want)
			}
			// every parse must decode strictly into the input struct
			if _, err := DecodeInput(tool(t, c.tool), got); err != nil {
				t.Fatalf("decode: %v", err)
			}
		})
	}
}

func TestParseNumber(t *testing.T) {
	cases := map[string]float64{"1500": 1500, "$100": 100, "10k": 10000, "1.5m": 1.5e6, "2B": 2e9, "1,000": 1000, "20%": 20, "-2.5": -2.5, "+5%": 5, "1_000": 1000}
	for in, want := range cases {
		got, err := ParseNumber(in)
		if err != nil || got != want {
			t.Errorf("ParseNumber(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "1.2.3", "k", "NaN", "Inf"} {
		if _, err := ParseNumber(bad); err == nil {
			t.Errorf("ParseNumber(%q) should fail", bad)
		}
	}
}

func TestTokenize(t *testing.T) {
	got, err := Tokenize(`/vault "Long HYPE & BTC" --bench 'HLP,BTC' a\ b`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/vault", "Long HYPE & BTC", "--bench", "HLP,BTC", "a b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	if _, err := Tokenize(`"open`); err == nil {
		t.Fatal("unterminated quote must fail")
	}
	if got, _ := Tokenize(`  `); len(got) != 0 {
		t.Fatalf("blank line: %q", got)
	}
}

func TestParseLine(t *testing.T) {
	r := Default()
	cases := map[string]Call{
		"/chart btc 4h":         {Tool: "get_candles", Args: json.RawMessage(`{"coin":"btc","interval":"4h"}`)},
		"c eth":                 {Tool: "get_candles", Args: json.RawMessage(`{"coin":"eth"}`)},
		"/sell ETH 1 --dry-run": {Tool: "place_order", Args: json.RawMessage(`{"coin":"ETH","dryRun":true,"side":"sell","size":"1"}`)},
		"/long HYPE --usd 100":  {Tool: "place_order", Args: json.RawMessage(`{"coin":"HYPE","side":"buy","usd":100}`)},
		"/buy BTC 0.1":          {Tool: "place_order", Args: json.RawMessage(`{"coin":"BTC","side":"buy","size":"0.1"}`)},
		"/vaults --sort sharpe": {Tool: "list_vaults", Args: json.RawMessage(`{"sort":"sharpe"}`)},
		"/get_vault growi":      {Tool: "get_vault", Args: json.RawMessage(`{"vault":"growi"}`)},
	}
	for line, want := range cases {
		got, err := r.ParseLine(line)
		if err != nil {
			t.Errorf("%s: %v", line, err)
			continue
		}
		if got.Tool != want.Tool || string(got.Args) != string(want.Args) {
			t.Errorf("%s: got %s %s", line, got.Tool, got.Args)
		}
	}
	_, err := r.ParseLine("/chrat btc")
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeUsage || e.Hint == "" {
		t.Fatalf("typo should be USAGE with a suggestion, got %v", err)
	}
	if _, err := r.ParseLine(""); err == nil {
		t.Fatal("empty line must fail")
	}
}

func TestFields(t *testing.T) {
	fs := Fields(tool(t, "list_vaults"))
	if len(fs) == 0 || fs[0].Name != "query" || fs[0].Description == "" {
		t.Fatalf("fields: %+v", fs)
	}
	if f, ok := fieldByName(fs, "MinTVL"); !ok || f.Name != "minTvl" || f.Kind != reflect.Float64 {
		t.Fatalf("fieldByName: %+v %v", f, ok)
	}
}

func TestDecodeInputStrict(t *testing.T) {
	tl := tool(t, "get_candles")
	if _, err := DecodeInput(tl, json.RawMessage(`{"coin":"BTC","bogus":1}`)); ExitCode(err) != 2 {
		t.Fatalf("unknown field must be USAGE, got %v", err)
	}
	if _, err := DecodeInput(tl, json.RawMessage(`{"coin":5}`)); ExitCode(err) != 2 {
		t.Fatalf("type mismatch must be USAGE, got %v", err)
	}
	if _, err := DecodeInput(tl, json.RawMessage(`{"coin":"BTC"} {}`)); ExitCode(err) != 2 {
		t.Fatalf("trailing data must be USAGE, got %v", err)
	}
	in, err := DecodeInput(tl, nil)
	if err != nil || in.(*ChartInput).Coin != "" {
		t.Fatalf("empty args: %v", err)
	}
}

func TestInvokeUnknownTool(t *testing.T) {
	_, err := Default().Invoke(context.Background(), &Env{}, Call{Tool: "get_candels"})
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeUsage || e.Hint == "" {
		t.Fatalf("got %v", err)
	}
	if _, err := Default().Invoke(context.Background(), nil, Call{Tool: "schema"}); ExitCode(err) != 1 {
		t.Fatalf("nil env: %v", err)
	}
}

func TestLookup(t *testing.T) {
	r := Default()
	for _, s := range []string{"get_vault", "vault", "/vault", "vault compare", "compare", "bt"} {
		if _, ok := r.Lookup(s); !ok {
			t.Errorf("Lookup(%q) failed", s)
		}
	}
	if s := r.Suggest("vaul", 2); len(s) == 0 {
		t.Error("Suggest found nothing")
	}
}

func TestAsError(t *testing.T) {
	cases := []struct {
		err  error
		code string
	}{
		{fmt.Errorf("x: %w", hyperliquid.ErrUnknownCoin), CodeUnknownCoin},
		{hyperliquid.ErrNotFound, CodeNotFound},
		{hyperliquid.ErrRateLimited, CodeRateLimited},
		{&hyperliquid.APIError{Status: 422, Code: "BAD_REQUEST"}, CodeUsage},
		{&hyperliquid.APIError{Status: 500, Code: "SERVER"}, CodeAPI},
		{hlpxyz.ErrNotAVault, CodeUnknownVault},
		{hlpxyz.ErrNoCache, CodeNotFound},
		{domain.ErrNotImplemented, CodeNotImplemented},
		{context.DeadlineExceeded, CodeNetwork},
		{&net.OpError{Op: "dial", Err: errors.New("refused")}, CodeNetwork},
		{errors.New("boom"), CodeInternal},
		{Errorf(CodeRiskLimit, "cap"), CodeRiskLimit},
	}
	for _, c := range cases {
		if got := AsError(c.err); got.Code != c.code {
			t.Errorf("%v -> %s, want %s", c.err, got.Code, c.code)
		}
	}
	if AsError(nil) != nil {
		t.Error("nil must map to nil")
	}
}

func TestSource(t *testing.T) {
	// untagged calls fail closed (audit S6): the gates treat them like agent
	if SourceFrom(context.Background()) != SourceUnknown {
		t.Fatal("default source")
	}
	if SourceFrom(WithSource(context.Background(), "mcp")) != "mcp" {
		t.Fatal("mcp source")
	}
}

func TestIsAddress(t *testing.T) {
	if !IsAddress(HLPAddress) || IsAddress("0x123") || IsAddress("hlp") {
		t.Fatal("IsAddress")
	}
}
