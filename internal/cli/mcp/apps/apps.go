// Package apps serves hlp's MCP Apps views (extension
// io.modelcontextprotocol/ui): static HTML documents under ui:// that a
// rendering host (Claude Desktop) shows in a sandboxed iframe when the model
// calls a bound tool.
//
//   - Views are hand-written HTML in views/, embedded with go:embed. Each
//     carries the shared postMessage bridge (bridge.js, no SDK), inlined at
//     load time where the view says /*@BRIDGE@*/. No Node, no bundler.
//   - Templates are static: per-call data reaches a view only through
//     ui/notifications/tool-result (structuredContent {data, meta}).
//   - CSP is empty: a view cannot fetch; it calls hlp's tools through the
//     host (tools/call), so hlp keeps its rate budget and caching.
//   - Hosts that do not render UI get the ordinary result, unchanged.
package apps

import (
	"context"
	"embed"
	"fmt"
	"slices"
	"strings"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed bridge.js views/*.html
var files embed.FS

const (
	// Extension is the MCP Apps extension id, in client and server
	// capabilities.
	Extension = "io.modelcontextprotocol/ui"
	// MIME is the resource MIME type hosts render as an app.
	MIME = "text/html;profile=mcp-app"

	ChartURI = "ui://hlp/chart.html"
	VaultURI = "ui://hlp/vault.html"
	OrderURI = "ui://hlp/order.html"

	// ConfirmTool is the app-only tool behind the order view's Confirm button.
	ConfirmTool = "confirm_order"
	// ConfirmMetaKey is the result _meta key that carries the single-use
	// confirm token on a preview_order result (never in structuredContent
	// or text, so the model does not read it in hosts that hide _meta).
	ConfirmMetaKey = "hlp/confirm"

	bridgeMarker = "/*@BRIDGE@*/"
)

// View is one ui:// resource.
type View struct {
	URI, Name, Title, Description, File string
}

// Views are the documents hlp serves, one resource per view (each stays
// small because the bridge is hand-written, unlike the ext-apps SDK).
var Views = []View{
	{ChartURI, "hlp-chart", "hlp chart", "Candle chart card (setup-rush look) with hover readout and timeframe switch", "views/chart.html"},
	{VaultURI, "hlp-vault", "hlp vault tearsheet", "Vault tearsheet: TVL, share price, equity curve, metrics, positions", "views/vault.html"},
	{OrderURI, "hlp-order", "hlp order preview", "Order preview with every risk check and a human Confirm button (dry-run in this build)", "views/order.html"},
}

// bindings maps a tool to the view the host renders for its results. List
// tools stay unbound: tables read fine as text, and one turn should show at
// most one widget.
var bindings = map[string]string{
	"get_candles":        ChartURI,
	"get_vault":          VaultURI,
	"get_strategy_vault": VaultURI,
	"preview_order":      OrderURI,
}

// BoundURI returns the view bound to a tool, or "".
func BoundURI(tool string) string { return bindings[tool] }

// Bindings returns a copy of the tool -> view map.
func Bindings() map[string]string {
	out := make(map[string]string, len(bindings))
	for k, v := range bindings {
		out[k] = v
	}
	return out
}

var (
	htmlOnce  sync.Once
	htmlCache map[string]string
	htmlErr   error
)

// HTML returns a view's document with the bridge inlined.
func HTML(uri string) (string, error) {
	htmlOnce.Do(func() {
		bridge, err := files.ReadFile("bridge.js")
		if err != nil {
			htmlErr = err
			return
		}
		htmlCache = map[string]string{}
		for _, v := range Views {
			b, err := files.ReadFile(v.File)
			if err != nil {
				htmlErr = err
				return
			}
			s := string(b)
			if !strings.Contains(s, bridgeMarker) {
				htmlErr = fmt.Errorf("%s: no %s marker", v.File, bridgeMarker)
				return
			}
			htmlCache[v.URI] = strings.Replace(s, bridgeMarker, string(bridge), 1)
		}
	})
	if htmlErr != nil {
		return "", htmlErr
	}
	s, ok := htmlCache[uri]
	if !ok {
		return "", fmt.Errorf("unknown view %q", uri)
	}
	return s, nil
}

// ResourceMeta is the resource _meta.ui: an empty CSP (no network, no
// external scripts or fonts) and a border. Hosts read it from the
// resources/read content item, falling back to the resources/list entry,
// so it goes on both.
func ResourceMeta() sdk.Meta {
	return sdk.Meta{"ui": map[string]any{
		"csp":           map[string]any{"connectDomains": []string{}, "resourceDomains": []string{}},
		"prefersBorder": true,
	}}
}

// Register adds every view as a resource.
func Register(srv *sdk.Server) error {
	for _, v := range Views {
		doc, err := HTML(v.URI)
		if err != nil {
			return err
		}
		uri := v.URI
		srv.AddResource(&sdk.Resource{
			URI: uri, Name: v.Name, Title: v.Title, Description: v.Description,
			MIMEType: MIME, Size: int64(len(doc)), Meta: ResourceMeta(),
		}, func(_ context.Context, _ *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
			return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{
				URI: uri, MIMEType: MIME, Text: doc, Meta: ResourceMeta(),
			}}}, nil
		})
	}
	return nil
}

// ToolMeta returns the _meta entries that bind a tool to its view:
// _meta.ui.resourceUri (plus the deprecated flat "ui/resourceUri" that older
// hosts read) and, when given, _meta.ui.visibility (["app"] hides a tool
// from the model). Nil when the tool has no view and no visibility.
func ToolMeta(tool string, visibility ...string) sdk.Meta {
	uri := bindings[tool]
	if uri == "" && len(visibility) == 0 {
		return nil
	}
	ui := map[string]any{}
	m := sdk.Meta{"ui": ui}
	if uri != "" {
		ui["resourceUri"] = uri
		m["ui/resourceUri"] = uri
	}
	if len(visibility) > 0 {
		ui["visibility"] = slices.Clone(visibility)
	}
	return m
}

// ServerCapability is the settings object hlp advertises under
// capabilities.extensions[Extension].
func ServerCapability() map[string]any {
	return map[string]any{"mimeTypes": []string{MIME}}
}

// HostRendersUI reports whether the client advertised MCP Apps support for
// hlp's MIME type (capabilities.extensions["io.modelcontextprotocol/ui"]).
// A settings object without mimeTypes counts as support for the default
// type.
func HostRendersUI(caps *sdk.ClientCapabilities) bool {
	if caps == nil || caps.Extensions == nil {
		return false
	}
	v, ok := caps.Extensions[Extension]
	if !ok || v == nil {
		return false
	}
	settings, ok := v.(map[string]any)
	if !ok {
		return true // present but opaque: the client claims the extension
	}
	types, ok := settings["mimeTypes"].([]any)
	if !ok {
		if ss, ok := settings["mimeTypes"].([]string); ok {
			return slices.Contains(ss, MIME)
		}
		return settings["mimeTypes"] == nil
	}
	for _, t := range types {
		if s, _ := t.(string); s == MIME {
			return true
		}
	}
	return false
}
