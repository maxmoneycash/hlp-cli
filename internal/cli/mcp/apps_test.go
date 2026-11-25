package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/maxmoneycash/hlp-cli/internal/cli/mcp/apps"
	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/core/coretest"
)

// uiClient declares MCP Apps support the way Claude Desktop does.
func uiClient() *sdk.ClientOptions {
	return &sdk.ClientOptions{Capabilities: &sdk.ClientCapabilities{
		Extensions: map[string]any{apps.Extension: map[string]any{"mimeTypes": []string{apps.MIME}}},
	}}
}

func uiMeta(t *testing.T, m sdk.Meta) map[string]any {
	t.Helper()
	ui, _ := m["ui"].(map[string]any)
	return ui
}

func TestAppsToolMetaAndResources(t *testing.T) {
	env := coretest.Env(t, &coretest.FakeXYZ{})
	cs := connectReg(t, env, core.Default(), Options{}, nil)
	tools := names(t, cs)
	for tool, uri := range apps.Bindings() {
		tl := tools[tool]
		if tl == nil {
			t.Fatalf("%s missing", tool)
		}
		if ui := uiMeta(t, tl.Meta); ui["resourceUri"] != uri || tl.Meta["ui/resourceUri"] != uri || tl.Meta["contract"] != core.Contract {
			t.Fatalf("%s meta: %v", tool, tl.Meta)
		}
	}
	if ui := uiMeta(t, tools["list_vaults"].Meta); ui != nil {
		t.Fatalf("list tools stay unbound: %v", tools["list_vaults"].Meta)
	}
	c := tools[apps.ConfirmTool]
	if c == nil {
		t.Fatal("confirm_order missing")
	}
	if v, _ := uiMeta(t, c.Meta)["visibility"].([]any); len(v) != 1 || v[0] != "app" {
		t.Fatalf("confirm_order must be app-only: %v", c.Meta)
	}

	res, err := cs.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Resources) != len(apps.Views) {
		t.Fatalf("resources: %d", len(res.Resources))
	}
	for _, r := range res.Resources {
		if r.MIMEType != apps.MIME || !strings.HasPrefix(r.URI, "ui://hlp/") {
			t.Fatalf("resource %+v", r)
		}
		rr, err := cs.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: r.URI})
		if err != nil {
			t.Fatal(err)
		}
		doc := rr.Contents[0]
		if doc.MIMEType != apps.MIME || !strings.Contains(doc.Text, "ui/initialize") || strings.Contains(doc.Text, "/*@BRIDGE@*/") {
			t.Fatalf("%s: bridge not inlined", r.URI)
		}
		if len(doc.Text) > 40<<10 {
			t.Fatalf("%s is %d bytes; views stay under 40 KB", r.URI, len(doc.Text))
		}
		csp := uiMeta(t, doc.Meta)["csp"].(map[string]any)
		if l, _ := csp["connectDomains"].([]any); len(l) != 0 {
			t.Fatalf("%s: CSP must be empty: %v", r.URI, csp)
		}
		if strings.Contains(doc.Text, "innerHTML") || strings.Contains(doc.Text, "<script src") || strings.Contains(doc.Text, "fetch(") {
			t.Fatalf("%s: views set text only and load nothing", r.URI)
		}
	}
}

func TestInitializeAdvertisesAppsExtension(t *testing.T) {
	env := coretest.Env(t, &coretest.FakeXYZ{})
	cs := connectReg(t, env, core.Default(), Options{}, nil)
	caps := cs.InitializeResult().Capabilities
	if caps.Extensions[apps.Extension] == nil || caps.Tools == nil || caps.Resources == nil || caps.Logging == nil {
		t.Fatalf("capabilities: %+v", caps)
	}
}

func TestNoUIHostGetsPlainResults(t *testing.T) {
	env := coretest.Env(t, &coretest.FakeXYZ{})
	cs := connectReg(t, env, core.Default(), Options{}, nil)
	res := call(t, cs, "get_candles", map[string]any{"coin": "BTC", "bars": 40})
	if _, ok := structured(t, res)["data"].(map[string]any)[apps.KeySVG]; ok {
		t.Fatal("agents must not pay for the SVG")
	}
	res = call(t, cs, "preview_order", map[string]any{"side": "buy", "coin": "BTC", "usd": 100})
	if res.Meta[apps.ConfirmMetaKey] != nil {
		t.Fatal("no confirm token without a UI host")
	}
	res = call(t, cs, apps.ConfirmTool, map[string]any{"confirmToken": "hlpc_x"})
	if !res.IsError || !strings.Contains(text(res), "SAFETY_REFUSED") {
		t.Fatalf("confirm from a non-UI host: %s", text(res))
	}
}

func TestUIHostChartAndConfirm(t *testing.T) {
	env := coretest.Env(t, &coretest.FakeXYZ{})
	cs := connectReg(t, env, core.Default(), Options{}, uiClient())
	tools := names(t, cs)

	res := call(t, cs, "get_candles", map[string]any{"coin": "BTC", "bars": 40})
	if res.IsError {
		t.Fatal(text(res))
	}
	svg, _ := structured(t, res)["data"].(map[string]any)[apps.KeySVG].(string)
	if !strings.HasPrefix(svg, "<svg") || !strings.Contains(svg, `data-n="40"`) {
		t.Fatalf("svg: %.200s", svg)
	}
	validates(t, tools["get_candles"], res)
	if strings.Contains(text(res), "<svg") {
		t.Fatal("the text content stays unchanged")
	}

	res = call(t, cs, "get_vault", map[string]any{"vault": "Long HYPE"})
	if res.IsError {
		t.Fatal(text(res))
	}
	if s, _ := structured(t, res)["data"].(map[string]any)[apps.KeySparkSVG].(string); !strings.HasPrefix(s, "<svg") {
		t.Fatalf("vault spark missing")
	}

	token := func() string {
		r := call(t, cs, "preview_order", map[string]any{"side": "buy", "coin": "BTC", "usd": 100, "leverage": 3})
		if r.IsError {
			t.Fatal(text(r))
		}
		c, _ := r.Meta[apps.ConfirmMetaKey].(map[string]any)
		tok, _ := c["token"].(string)
		if !strings.HasPrefix(tok, "hlpc_") || c["tool"] != apps.ConfirmTool || c["expiresAt"] == nil {
			t.Fatalf("confirm meta: %v", r.Meta)
		}
		if strings.Contains(text(r), tok) || strings.Contains(mustJSON(r.StructuredContent), tok) {
			t.Fatal("the token must stay out of text and structuredContent")
		}
		return tok
	}
	tok := token()
	res = call(t, cs, apps.ConfirmTool, map[string]any{"confirmToken": tok})
	if res.IsError {
		t.Fatal(text(res))
	}
	var d ConfirmData
	b, _ := json.Marshal(structured(t, res)["data"])
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	// coretest has trading off: the trade passes the risk rules, setup blocks it.
	if d.Status != "not_enabled" || d.Sent || d.Signed || !d.DryRun || d.Preview == nil {
		t.Fatalf("confirm: %s", b)
	}
	// single use
	if res = call(t, cs, apps.ConfirmTool, map[string]any{"confirmToken": tok}); !res.IsError || !strings.Contains(text(res), "already used") {
		t.Fatalf("reuse: %s", text(res))
	}
	if res = call(t, cs, apps.ConfirmTool, map[string]any{"confirmToken": token(), "extra": 1}); !res.IsError {
		t.Fatal("unknown fields must be refused")
	}
	// a blocked trade is rejected on confirm
	r := call(t, cs, "preview_order", map[string]any{"side": "buy", "coin": "BTC", "usd": 5})
	c, _ := r.Meta[apps.ConfirmMetaKey].(map[string]any)
	res = call(t, cs, apps.ConfirmTool, map[string]any{"confirmToken": c["token"]})
	b, _ = json.Marshal(structured(t, res)["data"])
	_ = json.Unmarshal(b, &d)
	if d.Status != "rejected" || !strings.Contains(d.Message, "min_notional") {
		t.Fatalf("blocked confirm: %s", b)
	}
}

func TestTokenBoundToSession(t *testing.T) {
	env := coretest.Env(t, &coretest.FakeXYZ{})
	reg := core.Default()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv, err := NewServer(env, reg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	connectTo := func() *sdk.ClientSession {
		st, ct := sdk.NewInMemoryTransports()
		if _, err := srv.Connect(ctx, st, nil); err != nil {
			t.Fatal(err)
		}
		cs, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, uiClient()).Connect(ctx, ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cs.Close() })
		return cs
	}
	a, b := connectTo(), connectTo()
	r := call(t, a, "preview_order", map[string]any{"side": "buy", "coin": "BTC", "usd": 100})
	c, _ := r.Meta[apps.ConfirmMetaKey].(map[string]any)
	res := call(t, b, apps.ConfirmTool, map[string]any{"confirmToken": c["token"]})
	if !res.IsError || !strings.Contains(text(res), "another session") {
		t.Fatalf("cross-session confirm: %s", text(res))
	}
}

func TestNoAppsOption(t *testing.T) {
	env := coretest.Env(t, &coretest.FakeXYZ{})
	cs := connectReg(t, env, core.Default(), Options{NoApps: true}, uiClient())
	tools := names(t, cs)
	if tools[apps.ConfirmTool] != nil || tools["get_candles"].Meta["ui"] != nil {
		t.Fatal("NoApps must drop views")
	}
	if res, err := cs.ListResources(context.Background(), nil); err == nil && len(res.Resources) > 0 {
		t.Fatal("NoApps must not register resources")
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
