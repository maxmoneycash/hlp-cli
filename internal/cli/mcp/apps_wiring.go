package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/maxmoneycash/hlp/internal/cli/mcp/apps"
	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/domain"
)

// maxConfirmDrift is how far the notional may move between preview and
// confirm before confirm refuses and asks for a new preview.
const maxConfirmDrift = 0.05

// appsState is the MCP Apps wiring of one server.
type appsState struct {
	on     bool
	skin   string
	tokens *apps.Tokens
}

func newAppsState(env *core.Env, o Options) *appsState {
	u := &appsState{on: !o.NoApps, skin: apps.SkinFor(""), tokens: apps.NewTokens()}
	if env != nil {
		u.skin = apps.SkinFor(env.Config.Chart.Theme)
		if env.Now != nil {
			u.tokens.Now = env.Now
		}
	}
	return u
}

// serverOptions advertises the extension next to the SDK's default logging
// capability (setting Capabilities replaces the SDK default).
func serverOptions(o Options, logger *slog.Logger) *sdk.ServerOptions {
	so := &sdk.ServerOptions{Instructions: instructions, Logger: logger}
	if !o.NoApps {
		so.Capabilities = &sdk.ServerCapabilities{Logging: &sdk.LoggingCapabilities{}}
		so.Capabilities.AddExtension(apps.Extension, apps.ServerCapability())
	}
	return so
}

// toolMeta is a tool's _meta: the contract plus, for bound tools, _meta.ui.
func (u *appsState) toolMeta(name string, visibility ...string) sdk.Meta {
	m := sdk.Meta{"contract": core.Contract}
	if u.on {
		for k, v := range apps.ToolMeta(name, visibility...) {
			m[k] = v
		}
	}
	return m
}

// register adds the ui:// resources and the app-only confirm_order tool.
func (u *appsState) register(srv *sdk.Server, env *core.Env, reg *core.Registry) error {
	if !u.on {
		return nil
	}
	if err := apps.Register(srv); err != nil {
		return core.Errorf(core.CodeInternal, "mcp apps: %v", err)
	}
	if _, ok := reg.Get("preview_order"); !ok {
		return nil // a registry without previews has nothing to confirm
	}
	data, err := relaxedSchema(reflect.TypeFor[ConfirmData]())
	if err != nil {
		return core.Errorf(core.CodeInternal, "confirm schema: %v", err)
	}
	meta, err := relaxedSchema(reflect.TypeFor[domain.Meta]())
	if err != nil {
		return core.Errorf(core.CodeInternal, "confirm schema: %v", err)
	}
	srv.AddTool(&sdk.Tool{
		Name: apps.ConfirmTool,
		OutputSchema: &jsonschema.Schema{Type: "object", Required: []string{"data", "meta"},
			Properties: map[string]*jsonschema.Schema{"data": data, "meta": meta}},
		Title: "Confirm a previewed order (order view only)",
		Description: "Called by hlp's order view when a person presses Confirm; hidden from the model (_meta.ui.visibility app). " +
			"Takes the single-use token the view received with a preview_order result, re-runs every risk check against fresh state, " +
			"and refuses if the order changed. Dry-run in this build: never signs, never sends.",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"confirmToken": {Type: "string", Description: "token from the preview result's _meta[\"hlp/confirm\"]"},
			},
			Required:             []string{"confirmToken"},
			AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
		},
		Annotations: &sdk.ToolAnnotations{Title: "Confirm order (view only)", DestructiveHint: ptr(false), OpenWorldHint: ptr(true)},
		Meta:        u.toolMeta(apps.ConfirmTool, "app"),
	}, u.confirm(env, reg))
	return nil
}

// decorate adds what only a rendering host needs: the chart or curve SVG
// in structuredContent.data, and the confirm token in the preview result's
// _meta. Hosts without MCP Apps get the result unchanged.
func (u *appsState) decorate(req *sdk.CallToolRequest, tool string, args json.RawMessage, res *core.Result, tr *sdk.CallToolResult) {
	if !u.on || req == nil || tr == nil || res == nil || !apps.HostRendersUI(req.ClientCapabilities()) {
		return
	}
	if sc, ok := tr.StructuredContent.(map[string]any); ok {
		apps.Enrich(tool, res, sc, u.skin)
	}
	if tool != "preview_order" {
		return
	}
	pv, ok := res.Data.(*core.OrderPreview)
	if !ok || pv == nil {
		return
	}
	args = normArgs(args)
	tok, exp, err := u.tokens.Issue(req.Session, apps.Pending{Args: args, Intent: intentOf(args, pv), NotionalUSD: pv.NotionalUSD})
	if err != nil {
		return // no token: the view stays preview-only
	}
	if tr.Meta == nil {
		tr.Meta = sdk.Meta{}
	}
	tr.Meta[apps.ConfirmMetaKey] = map[string]any{
		"token": tok, "expiresAt": exp.UTC().Format(time.RFC3339Nano), "tool": apps.ConfirmTool,
	}
}

func normArgs(a json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(a)) == 0 || string(a) == "null" {
		return json.RawMessage(`{}`)
	}
	return a
}

func intentOf(args json.RawMessage, pv *core.OrderPreview) string {
	return apps.IntentHash(args, pv.Action, pv.Coin, string(pv.Side), pv.Vault, string(pv.Network))
}

// ConfirmData is confirm_order's result data.
type ConfirmData struct {
	// Status: checked (every check passes again; would be sent in a live
	// build), not_enabled (the trade passes the risk rules but trading is
	// not configured), rejected (a risk check fails, the order changed, or
	// the price moved).
	Status  string             `json:"status"`
	Message string             `json:"message"`
	DryRun  bool               `json:"dryRun"` // always true in this build
	Signed  bool               `json:"signed"` // always false in this build
	Sent    bool               `json:"sent"`   // always false in this build
	Drift   float64            `json:"drift"`  // notional change since the preview, fraction
	Preview *core.OrderPreview `json:"preview,omitempty"`
}

func confirmRefusal(msg string) *sdk.CallToolResult {
	return ErrorResult(&core.Error{Code: core.CodeSafety, Message: msg, Hint: "ask for a new preview_order; a person confirms it in the order view"})
}

func (u *appsState) confirm(env *core.Env, reg *core.Registry) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		if req == nil || req.Params == nil {
			return confirmRefusal("confirm_order: no request"), nil
		}
		if !apps.HostRendersUI(req.ClientCapabilities()) {
			return confirmRefusal("confirm_order is only callable from hlp's order view in an MCP Apps host"), nil
		}
		var in struct {
			ConfirmToken string `json:"confirmToken"`
		}
		dec := json.NewDecoder(bytes.NewReader(req.Params.Arguments))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil || strings.TrimSpace(in.ConfirmToken) == "" {
			return ErrorResult(&core.Error{Code: core.CodeUsage, Message: "confirm_order takes {confirmToken}", Hint: "the order view passes it"}), nil
		}
		pend, err := u.tokens.Redeem(req.Session, in.ConfirmToken)
		if err != nil {
			return confirmRefusal(err.Error()), nil
		}
		// Re-run the guard: preview_order evaluates every check against
		// fresh account and market state and never signs.
		res, err := reg.Invoke(core.WithSource(ctx, "mcp"), env, core.Call{Tool: "preview_order", Args: pend.Args})
		if err != nil {
			return ErrorResult(err), nil
		}
		pv, ok := res.Data.(*core.OrderPreview)
		if !ok || pv == nil {
			return ErrorResult(core.Errorf(core.CodeInternal, "confirm: preview returned %T", res.Data)), nil
		}
		d := &ConfirmData{DryRun: true, Preview: pv}
		if pend.NotionalUSD > 0 {
			d.Drift = (pv.NotionalUSD - pend.NotionalUSD) / pend.NotionalUSD
		}
		blockers := pv.FailedChecks()
		risk := []string{}
		for _, id := range blockers {
			if !setupCheck(id) {
				risk = append(risk, id)
			}
		}
		switch {
		case intentOf(pend.Args, pv) != pend.Intent:
			d.Status, d.Message = "rejected", "The order changed since the preview (market, side, vault or network). Preview again."
		case math.Abs(d.Drift) > maxConfirmDrift:
			d.Status, d.Message = "rejected", fmt.Sprintf("The notional moved %+.1f%% since the preview. Preview again.", d.Drift*100)
		case len(risk) > 0:
			d.Status, d.Message = "rejected", "Blocked by risk checks: "+strings.Join(risk, ", ")+"."
		case len(blockers) > 0:
			d.Status, d.Message = "not_enabled", "The trade passes the risk rules, but trading is not enabled here ("+strings.Join(blockers, ", ")+"). Nothing was signed or sent."
		default:
			d.Status, d.Message = "checked", "Every check passes again. Dry-run build: the order was not signed or sent."
		}
		meta := res.Meta
		meta.Source = "confirm"
		tr, err := toolResult(env, &core.Result{Card: core.CardOrder, Title: "Confirm", Data: d, Meta: meta}, nil, core.CompactOptions{}, false)
		if err != nil {
			return ErrorResult(err), nil
		}
		return tr, nil
	}
}

// setupCheck mirrors core's split of blockers: trading not configured, as
// opposed to this trade breaking a rule.
func setupCheck(id string) bool {
	switch id {
	case "trading_enabled", "agent_key", "mcp_trading":
		return true
	}
	return false
}
