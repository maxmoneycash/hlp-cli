package apps

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHostRendersUI(t *testing.T) {
	for _, c := range []struct {
		caps *sdk.ClientCapabilities
		want bool
	}{
		{nil, false},
		{&sdk.ClientCapabilities{}, false},
		{&sdk.ClientCapabilities{Extensions: map[string]any{"other": map[string]any{}}}, false},
		{&sdk.ClientCapabilities{Extensions: map[string]any{Extension: map[string]any{"mimeTypes": []any{MIME}}}}, true},
		{&sdk.ClientCapabilities{Extensions: map[string]any{Extension: map[string]any{"mimeTypes": []string{MIME}}}}, true},
		{&sdk.ClientCapabilities{Extensions: map[string]any{Extension: map[string]any{"mimeTypes": []any{"text/html"}}}}, false},
		{&sdk.ClientCapabilities{Extensions: map[string]any{Extension: map[string]any{}}}, true},
	} {
		if got := HostRendersUI(c.caps); got != c.want {
			t.Errorf("%+v: got %v", c.caps, got)
		}
	}
}

func TestViewsAreSmallAndSafe(t *testing.T) {
	for _, v := range Views {
		s, err := HTML(v.URI)
		if err != nil {
			t.Fatal(err)
		}
		if len(s) > 40<<10 {
			t.Errorf("%s: %d bytes", v.URI, len(s))
		}
		for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "eval(", "new Function", "http://", "https://", "<script src", "<link"} {
			if strings.Contains(s, bad) && !(bad == "http://" && strings.Count(s, bad) == strings.Count(s, "http://www.w3.org/")) {
				t.Errorf("%s contains %q", v.URI, bad)
			}
		}
	}
	if _, err := HTML("ui://hlp/nope.html"); err == nil {
		t.Fatal("unknown view")
	}
}

func TestToolMeta(t *testing.T) {
	m := ToolMeta("get_candles")
	if m["ui/resourceUri"] != ChartURI || m["ui"].(map[string]any)["resourceUri"] != ChartURI {
		t.Fatalf("%v", m)
	}
	if ToolMeta("list_vaults") != nil {
		t.Fatal("unbound")
	}
	m = ToolMeta(ConfirmTool, "app")
	if m["ui/resourceUri"] != nil || m["ui"].(map[string]any)["visibility"].([]string)[0] != "app" {
		t.Fatalf("%v", m)
	}
	b, _ := json.Marshal(ResourceMeta())
	if !strings.Contains(string(b), `"connectDomains":[]`) {
		t.Fatalf("csp: %s", b)
	}
}

func TestTokens(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ts := NewTokens()
	ts.Now = func() time.Time { return now }
	sessA, sessB := new(int), new(int)
	p := Pending{Args: json.RawMessage(`{"coin":"BTC"}`), Intent: "x", NotionalUSD: 100}

	tok, exp, err := ts.Issue(sessA, p)
	if err != nil || !exp.Equal(now.Add(ConfirmTTL)) || len(tok) != len("hlpc_")+32 {
		t.Fatalf("%q %v %v", tok, exp, err)
	}
	got, err := ts.Redeem(sessA, " "+tok+" ")
	if err != nil || got.Intent != "x" || string(got.Args) != `{"coin":"BTC"}` {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := ts.Redeem(sessA, tok); !errors.Is(err, ErrTokenUnknown) {
		t.Fatalf("reuse: %v", err)
	}

	tok, _, _ = ts.Issue(sessA, p)
	if _, err := ts.Redeem(sessB, tok); !errors.Is(err, ErrTokenSession) {
		t.Fatalf("other session: %v", err)
	}
	if _, err := ts.Redeem(sessA, tok); !errors.Is(err, ErrTokenUnknown) {
		t.Fatal("a wrong-session try burns the token")
	}

	tok, _, _ = ts.Issue(sessA, p)
	now = now.Add(ConfirmTTL)
	if _, err := ts.Redeem(sessA, tok); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expired: %v", err)
	}

	for range maxTokens + 10 {
		_, _, _ = ts.Issue(sessA, p)
	}
	if ts.Len() > maxTokens {
		t.Fatalf("store unbounded: %d", ts.Len())
	}
}

func TestIntentHash(t *testing.T) {
	a := IntentHash(json.RawMessage(`{"coin":"BTC","usd":100,"side":"buy"}`), "order", "BTC", "buy", "", "mainnet")
	b := IntentHash(json.RawMessage(`{"side":"buy","usd":100,"coin":"BTC"}`), "order", "btc", "buy", "", "mainnet")
	if a != b {
		t.Fatal("key order and coin case must not matter")
	}
	for _, c := range []string{
		IntentHash(json.RawMessage(`{"coin":"BTC","usd":100,"side":"buy"}`), "order", "BTC", "buy", "", "testnet"),
		IntentHash(json.RawMessage(`{"coin":"BTC","usd":100,"side":"buy"}`), "order", "BTC", "sell", "", "mainnet"),
		IntentHash(json.RawMessage(`{"coin":"BTC","usd":101,"side":"buy"}`), "order", "BTC", "buy", "", "mainnet"),
	} {
		if c == a {
			t.Fatal("network, side and args must change the hash")
		}
	}
}
