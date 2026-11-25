package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/maxmoneycash/hlp/internal/cli/mcp"
	"github.com/maxmoneycash/hlp/internal/cli/mcp/apps"
	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/core/coretest"
)

// fixtureCalls are the tool calls replayed by the dev host. Each is run
// offline (coretest fixtures, fixed clock) through the real MCP server with
// a client that declares MCP Apps support, so the recorded results are
// exactly what a rendering host would receive.
var fixtureCalls = []struct {
	file, tool string
	args       map[string]any
}{
	{"chart-btc-15m", "get_candles", map[string]any{"coin": "BTC", "interval": "15m", "bars": 96}},
	{"chart-btc-1d", "get_candles", map[string]any{"coin": "BTC", "interval": "1d", "bars": 90}},
	{"vault-long-hype", "get_vault", map[string]any{"vault": "Long HYPE"}},
	{"order-btc-ok", "preview_order", map[string]any{"side": "buy", "coin": "BTC", "usd": 100, "leverage": 3}},
	{"order-btc-blocked", "preview_order", map[string]any{"side": "sell", "coin": "BTC", "usd": 5}},
}

// TestFixtures regenerates tools/apphost/fixtures with HLP_APPHOST_UPDATE=1
// and otherwise checks that every fixture exists and parses.
func TestFixtures(t *testing.T) {
	update := os.Getenv("HLP_APPHOST_UPDATE") == "1"
	if !update {
		for _, c := range append(fixtureCalls, struct {
			file, tool string
			args       map[string]any
		}{file: "confirm-btc-ok"}) {
			b, err := os.ReadFile(filepath.Join("fixtures", c.file+".json"))
			if err != nil {
				t.Fatalf("%v (regenerate: HLP_APPHOST_UPDATE=1 go test ./tools/apphost)", err)
			}
			var f fixture
			if err := json.Unmarshal(b, &f); err != nil || f.Result == nil {
				t.Fatalf("%s: %v", c.file, err)
			}
		}
		return
	}
	env := coretest.Env(t, &coretest.FakeXYZ{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, ct := sdk.NewInMemoryTransports()
	go func() {
		_ = mcp.Serve(ctx, env, core.Default(), mcp.Options{Version: "fixture", Transport: st,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	}()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "apphost", Version: "0"}, &sdk.ClientOptions{
		Capabilities: &sdk.ClientCapabilities{Extensions: map[string]any{apps.Extension: map[string]any{"mimeTypes": []string{apps.MIME}}}},
	}).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	write := func(name, tool string, args map[string]any, res *sdk.CallToolResult) {
		b, err := json.MarshalIndent(fixture{Tool: tool, URI: apps.BoundURI(tool), Arguments: args, Result: res}, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join("fixtures", name+".json"), append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range fixtureCalls {
		res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: c.tool, Arguments: c.args})
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %+v", c.file, err, res)
		}
		write(c.file, c.tool, c.args, res)
		if c.file == "order-btc-ok" {
			tok := res.Meta[apps.ConfirmMetaKey].(map[string]any)["token"]
			cr, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: apps.ConfirmTool, Arguments: map[string]any{"confirmToken": tok}})
			if err != nil || cr.IsError {
				t.Fatalf("confirm: %v %+v", err, cr)
			}
			write("confirm-btc-ok", apps.ConfirmTool, map[string]any{"confirmToken": "(replayed)"}, cr)
		}
	}
}
