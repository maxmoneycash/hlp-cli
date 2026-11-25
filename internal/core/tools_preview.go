package core

import (
	"context"
	"encoding/json"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// previewTools registers preview_order: a read-only, never-signed order
// preview that MCP always exposes (AGENT-UX #6), so an agent can propose a
// trade and see what would block it without any trading permission.
func previewTools() []Tool {
	return []Tool{
		{Name: "preview_order", Command: "preview", Slash: "preview", Positional: []string{"side", "coin", "size"}, Group: "trade", NewInput: in[PreviewInput](), Run: runPreviewOrder,
			Description: "Risk-check an order without signing or sending it. Same inputs as place_order (no yes or dry-run). Returns the preview plus ok and blockers: the ids of the failed checks that would refuse the order. Exit 0 even when blocked.",
			Summary:     "Preview an order's risk checks (never signed, never sent)",
			Examples:    []string{"hlp preview buy BTC --usd 100", "hlp preview sell ETH 0.5 --px 2700 --tp +5% --sl -2%"}},
	}
}

// PreviewInput is the input of preview_order: place_order's input without
// yes and dry-run (a preview is never signed or sent).
type PreviewInput struct {
	Coin       string  `json:"coin"`
	Side       string  `json:"side" jsonschema:"buy or sell"`
	Size       string  `json:"size,omitempty" jsonschema:"base units as a decimal string"`
	USD        float64 `json:"usd,omitempty" jsonschema:"notional in USD (alternative to size)"`
	Px         string  `json:"px,omitempty" jsonschema:"limit price; empty = market (IOC at mid +/- slippage)"`
	TP         string  `json:"tp,omitempty" jsonschema:"take profit: price or +5%"`
	SL         string  `json:"sl,omitempty" jsonschema:"stop loss: price or -2%"`
	ReduceOnly bool    `json:"reduceOnly,omitempty"`
	Leverage   int     `json:"leverage,omitempty"`
	Isolated   bool    `json:"isolated,omitempty" jsonschema:"isolated margin with --leverage (default keeps the position's mode, else cross)"`
	Vault      string  `json:"vault,omitempty" jsonschema:"trade on behalf of this vault (leader's agent)"`
}

type previewOnlyKey struct{}

// previewOnly reports whether ctx belongs to preview_order: authorize then
// only evaluates the checks and never signs, whatever the config.
func previewOnly(ctx context.Context) bool {
	v, _ := ctx.Value(previewOnlyKey{}).(bool)
	return v
}

func runPreviewOrder(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*PreviewInput)
	o := &OrderInput{Coin: p.Coin, Side: p.Side, Size: p.Size, USD: p.USD, Px: p.Px, TP: p.TP, SL: p.SL,
		ReduceOnly: p.ReduceOnly, Leverage: p.Leverage, Isolated: p.Isolated, Vault: p.Vault, DryRun: true}
	res, err := runOrder(context.WithValue(ctx, previewOnlyKey{}, true), env, o)
	if err != nil {
		return nil, err
	}
	if pv, ok := res.Data.(*OrderPreview); ok {
		pv.DryRun = true
		pv.Envelope = nil
		res.Meta.Source = "preview" // orderResult already added "blocked: …"
	}
	res.Title = "Preview: " + res.Title
	return res, nil
}

// FailedChecks are the ids of the failed checks: what would refuse the
// order (AGENT-UX #10). Empty means nothing blocks it.
func (p OrderPreview) FailedChecks() []string {
	out := []string{}
	for _, c := range p.Checks {
		if c.Status == domain.CheckFail {
			out = append(out, c.ID)
		}
	}
	return out
}

// setupChecks are the checks that say trading is not configured, as
// opposed to this trade breaking a rule.
var setupChecks = map[string]bool{"trading_enabled": true, "agent_key": true, "mcp_trading": true}

// MarshalJSON fills the summary fields ok and blockers from Checks on every
// encode, so agents read one flag instead of scanning checks, and the two
// can never disagree with the checks.
func (p OrderPreview) MarshalJSON() ([]byte, error) {
	type plain OrderPreview
	q := plain(p)
	q.Blockers = p.FailedChecks()
	q.OK = len(q.Blockers) == 0
	q.SetupBlockers, q.RiskBlockers = []string{}, []string{}
	for _, id := range q.Blockers {
		if setupChecks[id] {
			q.SetupBlockers = append(q.SetupBlockers, id)
		} else {
			q.RiskBlockers = append(q.RiskBlockers, id)
		}
	}
	q.TradeOK = len(q.RiskBlockers) == 0
	return json.Marshal(q)
}
