package core

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hyperliquid"
	"github.com/maxmoneycash/hlp/internal/hyperliquid/exchange"
)

// Every mutating tool follows the same path:
//
//	validate -> build preview -> Guard.Check -> confirm (Confirmer or yes)
//	-> exchange.Post with Gate{Live: liveTrading && ...} (dry-run in this build)
//
// With trading disabled, an explicit --dry-run still returns an unsigned
// preview (what would be sent, which caps would apply) and exit code 0;
// anything else is TRADING_DISABLED (exit 3).

// ParseSide maps buy/long/b/bid and sell/short/s/a/ask to a wire side.
func ParseSide(s string) (domain.Side, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "buy", "long", "b", "bid":
		return domain.Buy, nil
	case "sell", "short", "s", "a", "ask":
		return domain.Sell, nil
	}
	return "", Errorf(CodeUsage, "side must be buy or sell, got %q", s)
}

func maxDecimals(m domain.Market) int {
	if m.Kind == domain.Spot {
		return hyperliquid.MaxDecimalsSpot
	}
	return hyperliquid.MaxDecimalsPerp
}

// tickError builds INVALID_TICK with the two nearest valid prices.
func tickError(what string, px decimal.Decimal, m domain.Market) *Error {
	md := maxDecimals(m)
	lo := hyperliquid.RoundPx(px, m.SzDecimals, md, -1)
	hi := hyperliquid.RoundPx(px, m.SzDecimals, md, +1)
	return &Error{
		Code:    CodeInvalidTick,
		Message: fmt.Sprintf("%s %s is off the price grid for %s (5 significant figures, at most %d decimals)", what, px.String(), m.Display, md-m.SzDecimals),
		Details: map[string]string{"below": hyperliquid.ToWire(lo), "above": hyperliquid.ToWire(hi)},
		Hint:    fmt.Sprintf("use %s or %s", hyperliquid.ToWire(lo), hyperliquid.ToWire(hi)),
	}
}

// ParseDecimal parses a strict positive decimal for order sizes and prices:
// digits with an optional fraction, thousands separators allowed. Unlike
// ParseNumber it never strips "$" or "%" or expands k/m/b suffixes, so
// "$100" or "1k" can never silently become 100 or 1000 coins.
func ParseDecimal(what, s string) (decimal.Decimal, error) {
	t := strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	t = strings.ReplaceAll(t, "_", "")
	if strings.HasPrefix(t, "$") {
		return decimal.Zero, &Error{Code: CodeUsage, Message: fmt.Sprintf("%s is in coins, not dollars: got %q", what, s), Hint: "use usd for a dollar amount (--usd 100)"}
	}
	ok := t != ""
	dot := false
	for _, r := range t {
		switch {
		case r >= '0' && r <= '9':
		case r == '.' && !dot:
			dot = true
		default:
			ok = false
		}
	}
	if !ok || t == "." {
		return decimal.Zero, Errorf(CodeUsage, "%s must be a plain positive number (no $, %%, k/m/b), got %q", what, s)
	}
	d, err := decimal.NewFromString(t)
	if err != nil || !d.IsPositive() {
		return decimal.Zero, Errorf(CodeUsage, "%s must be a positive number, got %q", what, s)
	}
	return d, nil
}

// userPrice parses and validates a user-supplied price (never rounded).
func userPrice(what, s string, m domain.Market) (decimal.Decimal, error) {
	px, err := ParseDecimal(what, s)
	if err != nil {
		return decimal.Zero, err
	}
	if !hyperliquid.ValidPx(px, m.SzDecimals, maxDecimals(m)) {
		return decimal.Zero, tickError(what, px, m)
	}
	return px, nil
}

// triggerPrice resolves a TP/SL: an absolute price (validated) or a percent
// move measured as P&L for the position ("+5%" TP and "-2%" SL work for both
// longs and shorts). entry is the reference price: the limit price of a
// limit order, the book mid (not the slippage bound) of a market order.
// Relative prices are rounded onto the grid. An absolute price on the wrong
// side of entry (a long's TP below it, a long's SL above it) would fire at
// once, so it is a usage error.
func triggerPrice(what, s string, entry decimal.Decimal, side domain.Side, m domain.Market) (decimal.Decimal, error) {
	s = strings.TrimSpace(s)
	if !strings.HasSuffix(s, "%") {
		px, err := userPrice(what, s, m)
		if err != nil {
			return px, err
		}
		above := (what == "tp") == (side == domain.Buy) // must sit above entry
		if above && !px.GreaterThan(entry) || !above && !px.LessThan(entry) {
			dir := "below"
			if above {
				dir = "above"
			}
			return decimal.Zero, &Error{Code: CodeUsage,
				Message: fmt.Sprintf("%s %s is on the wrong side: for a %s it must be %s the entry %s, or it fires at once", what, px.String(), map[bool]string{true: "long", false: "short"}[side == domain.Buy], dir, hyperliquid.ToWire(entry)),
				Hint:    "use a percent like --tp +5% / --sl -2%"}
		}
		return px, nil
	}
	pct, err := ParseNumber(s)
	if err != nil {
		return decimal.Zero, Errorf(CodeUsage, "%s: %v", what, err)
	}
	pct = math.Abs(pct) / 100
	if pct <= 0 || pct >= 1 {
		return decimal.Zero, Errorf(CodeUsage, "%s percent must be between 0%% and 100%%, got %s", what, s)
	}
	// profit direction: up for longs, down for shorts; stops the other way
	up := (what == "tp") == (side == domain.Buy)
	f := 1 - pct
	if up {
		f = 1 + pct
	}
	px := entry.Mul(decimal.NewFromFloat(f))
	return hyperliquid.RoundPx(px, m.SzDecimals, maxDecimals(m), 0), nil
}

func opposite(s domain.Side) domain.Side {
	if s == domain.Buy {
		return domain.Sell
	}
	return domain.Buy
}

func network(env *Env) domain.Network {
	if env.Config.Network == "" {
		return domain.Mainnet
	}
	return env.Config.Network
}

// gate is the only place a Gate is built. Live sending needs the
// compile-time switch, config trading.enabled, an exchange client and no
// --dry-run request.
func gate(env *Env, dryRun bool) exchange.Gate {
	return exchange.Gate{
		Live:    liveBuild() && env.Config.Trading.Enabled && env.Exchange != nil && !dryRun,
		Mainnet: network(env) == domain.Mainnet,
	}
}

// previewChecks are the informational checks shown on an unsigned preview.
func previewChecks(ctx context.Context, env *Env, in Intent) []domain.Check {
	if rg, ok := env.Guard.(*RiskGuard); ok {
		return rg.Checks(ctx, in)
	}
	// Trading is off: show the caps without reading today's fills.
	g := newRiskGuard(env)
	g.RealizedLoss = nil
	return g.Checks(ctx, in)
}

// authorize runs the guard and the confirmation step. A nil error means the
// action may be signed (and, only when the gate is live, sent).
//
// It also settles preview.DryRun before anyone sees the preview: the confirm
// card must say "this will be sent" when it will be.
//
// Checks already on the preview (the vault a deposit targets) are kept
// ahead of the risk checks.
//
// Only a person at the CLI or TUI, or an MCP client, may pre-approve with
// yes. Agent (LLM), bot and untagged calls (SourceUnknown) have their yes
// ignored and need a human Confirmer (audit S6). MCP clients rely on yes
// plus the risk caps and the --allow-trading switch. When
// risk.require_confirm applies (a live mainnet send), a missing Confirmer
// is refused unless trading.allow_unattended is set (and, for MCP,
// mcp.allow_unattended too) (audit S4).
func authorize(ctx context.Context, env *Env, in Intent, preview *OrderPreview, dryRun, yes bool) (signable bool, err error) {
	live := gate(env, dryRun).Live
	preview.DryRun = !live
	pre := preview.Checks
	withPre := func(cs []domain.Check) []domain.Check {
		return append(append([]domain.Check(nil), pre...), cs...)
	}
	if previewOnly(ctx) {
		// preview_order: report every check (ok/blockers), never sign.
		preview.DryRun = true
		preview.Checks = withPre(previewChecks(ctx, env, in))
		return false, nil
	}
	if env.Exchange == nil || !env.Config.Trading.Enabled {
		if dryRun {
			preview.Checks = withPre(previewChecks(ctx, env, in))
			return false, nil
		}
		if err := env.Guard.Check(ctx, in); err != nil {
			return false, err
		}
		return false, &Error{Code: CodeTradingDisabled, Message: "trading needs an agent key", Hint: "set HLP_AGENT_KEY to an API-wallet key and trading.enabled = true; add --dry-run to preview"}
	}
	if err := env.Guard.Check(ctx, in); err != nil {
		return false, err
	}
	if rg, ok := env.Guard.(*RiskGuard); ok {
		preview.Checks = withPre(rg.Checks(ctx, in))
	}
	if dryRun {
		// Dry-run envelopes are signed already expired and their
		// signature is withheld (see post), so nothing replayable leaks.
		return true, nil
	}
	if src := SourceFrom(ctx); !yesTrustedSource(src) {
		yes = false
		if env.Confirm == nil {
			msg := "orders from the assistant need a person to confirm"
			if src != "agent" {
				msg = fmt.Sprintf("orders from source %q need a person to confirm", src)
			}
			return false, &Error{Code: CodeConfirmRequired, Message: msg, Hint: "run the command yourself, or use the TUI where a confirm card is shown"}
		}
	}
	mustAsk := env.Config.Risk.RequireConfirm && network(env) == domain.Mainnet && live
	// trading.allow_unattended is for the owner's bots: it does not cover
	// MCP unless mcp.allow_unattended is also set. The refusal does not
	// name the keys, since the caller here is a program, not the person
	// who edits config.toml.
	unattended := env.Config.Trading.AllowUnattended && (SourceFrom(ctx) != "mcp" || env.Config.MCP.AllowUnattended)
	if mustAsk && env.Confirm == nil && !unattended {
		return false, &Error{Code: CodeConfirmRequired, Message: "mainnet orders need a person at a terminal (risk.require_confirm)",
			Hint: "ask the person to run the command on a terminal (a TTY, without --json) and confirm it there"}
	}
	switch {
	case env.Confirm != nil && (!yes || mustAsk):
		ok, err := env.Confirm(ctx, *preview)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, &Error{Code: CodeSafety, Message: "cancelled: not confirmed"}
		}
	case !yes:
		return false, &Error{Code: CodeConfirmRequired, Message: "this action needs confirmation", Hint: "re-run with --yes (risk caps still apply) or --dry-run to preview"}
	}
	// The order-rate cap counts what is actually sent, after the person
	// confirmed: dry runs and declined confirms never use a slot.
	if rg, ok := env.Guard.(*RiskGuard); ok && live {
		if err := rg.TakeSend(in); err != nil {
			return false, err
		}
	}
	return true, nil
}

// post signs (and in a live build, sends) one action and records the result.
// The envelope is always recorded without its signature (audit S9): callers
// (MCP clients, the model, scripts, logs) see what was or would be sent and
// its connection id, never a signature they could replay while the outcome
// is unknown. A sent order is identified by its oid in the response.
func post(ctx context.Context, env *Env, action any, vault *string, dryRun bool, preview *OrderPreview) error {
	g := gate(env, dryRun)
	res, err := env.Exchange.Post(ctx, action, vault, g)
	if err != nil {
		return err
	}
	preview.Envelope = appendEnvelope(preview.Envelope, withheldEnvelope(res.Envelope, res.DryRun))
	preview.DryRun = res.DryRun
	if res.Response != nil {
		preview.Response, _ = json.Marshal(res.Response)
	}
	if res.Unknown {
		return &Error{Code: CodeOrderPending, Message: "the exchange did not answer; the order may or may not be live", Hint: "check `hlp account` before retrying; do NOT resend blindly"}
	}
	if res.Status == "err" {
		return &Error{Code: CodeExchange, Message: fmt.Sprint(res.Response)}
	}
	return nil
}

// withheldEnvelope is the public form of an envelope: the action, nonce,
// vault and expiry plus the EIP-712 connection id that was signed, with the
// signature itself withheld.
func withheldEnvelope(e exchange.Envelope, dryRun bool) json.RawMessage {
	sig := "withheld"
	if dryRun {
		sig = "withheld (dry run)"
	}
	out := map[string]any{
		"action":       e.Action,
		"nonce":        e.Nonce,
		"vaultAddress": e.VaultAddress,
		"signature":    sig,
	}
	if e.ExpiresAfter != nil {
		out["expiresAfter"] = *e.ExpiresAfter
	}
	if cid, err := exchange.ConnectionID(e.Action, e.Nonce, e.VaultAddress, e.ExpiresAfter); err == nil {
		out["connectionId"] = "0x" + hex.EncodeToString(cid[:])
	}
	b, _ := json.Marshal(out)
	return b
}

// appendEnvelope keeps a single envelope as an object and several as an array.
func appendEnvelope(prev, next json.RawMessage) json.RawMessage {
	if len(prev) == 0 {
		return next
	}
	var arr []json.RawMessage
	if prev[0] == '[' {
		_ = json.Unmarshal(prev, &arr)
	} else {
		arr = []json.RawMessage{prev}
	}
	b, _ := json.Marshal(append(arr, next))
	return b
}

func orderResult(env *Env, p *OrderPreview, title string) *Result {
	meta := env.meta("exchange")
	if p.DryRun {
		meta.Source = "dry-run"
		// the same summary preview_order gives (AGENT-UX #10)
		if b := p.FailedChecks(); len(b) > 0 {
			meta.Warnings = append(meta.Warnings, "blocked: "+strings.Join(b, ", "))
		}
	}
	return &Result{Card: CardOrder, Title: title, Data: p, Meta: meta}
}

func vaultPtr(env *Env, v string) (*string, error) {
	if v == "" {
		v = env.Config.Trading.VaultAddress
	}
	if v == "" {
		return nil, nil
	}
	if !IsAddress(v) {
		return nil, Errorf(CodeUsage, "vault must be a 0x address, got %q", v)
	}
	l := strings.ToLower(v)
	return &l, nil
}

// ---- place_order ----

func runOrder(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*OrderInput)
	if p.Paper && !previewOnly(ctx) {
		return orderViaPaper(ctx, env, p)
	}
	side, err := ParseSide(p.Side)
	if err != nil {
		return nil, err
	}
	m, err := env.ResolveTrade(ctx, p.Coin)
	if err != nil {
		return nil, err
	}
	if m.Delisted {
		return nil, Errorf(CodeUsage, "%s is delisted", m.Display)
	}
	if (p.Size == "") == (p.USD == 0) {
		return nil, Errorf(CodeUsage, "give exactly one of size (base units) or usd (notional)")
	}
	if p.USD < 0 {
		return nil, Errorf(CodeUsage, "usd must be positive")
	}
	if p.Leverage < 0 || (m.MaxLeverage > 0 && p.Leverage > m.MaxLeverage) {
		return nil, Errorf(CodeUsage, "leverage must be 1-%d for %s", m.MaxLeverage, m.Display)
	}
	vault, err := vaultPtr(env, p.Vault)
	if err != nil {
		return nil, err
	}
	md := maxDecimals(m)

	// reference and limit price; entry is what TP/SL percents and sides
	// are measured from (the limit price, or the mid for a market order)
	var px, entry decimal.Decimal
	tif := "Gtc"
	if p.Px != "" {
		if px, err = userPrice("px", p.Px, m); err != nil {
			return nil, err
		}
		entry = px
	} else {
		ref := m.Mid
		if b, berr := env.HL.Book(ctx, m.Coin, hyperliquid.BookOpts{}); berr == nil && b.Mid() > 0 {
			ref = b.Mid()
		}
		if ref <= 0 {
			ref = m.Mark
		}
		if ref <= 0 {
			return nil, &Error{Code: CodeStale, Message: "no reference price for " + m.Display, Hint: "pass --px for a limit order"}
		}
		slip := float64(env.Config.Trading.SlippageBps)
		if slip <= 0 {
			slip = 50
		}
		f := 1 + slip/10_000
		dir := +1
		if side == domain.Sell {
			f, dir = 1-slip/10_000, -1
		}
		px = hyperliquid.RoundPx(decimal.NewFromFloat(ref*f), m.SzDecimals, md, dir)
		entry = decimal.NewFromFloat(ref)
		tif = "Ioc"
	}

	// size
	var sz decimal.Decimal
	if p.Size != "" {
		if sz, err = ParseDecimal("size", p.Size); err != nil {
			return nil, err
		}
		if !hyperliquid.FloorSz(sz, m.SzDecimals).Equal(sz) {
			return nil, Errorf(CodeUsage, "size %s has more than %d decimals for %s", p.Size, m.SzDecimals, m.Display)
		}
	} else {
		sz = hyperliquid.FloorSz(decimal.NewFromFloat(p.USD).Div(px), m.SzDecimals)
		if !sz.IsPositive() {
			return nil, Errorf(CodeUsage, "$%.2f is below one size increment of %s at %s", p.USD, m.Display, hyperliquid.ToWire(px))
		}
	}
	notional, _ := sz.Mul(px).Float64()
	var minCheck *domain.Check
	if notional < 10 && !p.ReduceOnly {
		minSz := hyperliquid.ToWire(decimal.NewFromInt(10).Div(px).RoundUp(int32(m.SzDecimals)))
		if !previewOnly(ctx) {
			return nil, &Error{Code: CodeUsage, Message: fmt.Sprintf("order value $%.2f is below Hyperliquid's $10 minimum", notional),
				Hint: fmt.Sprintf("use at least size %s %s (or --usd 10)", minSz, m.Display)}
		}
		// preview_order reports it as a blocker, like every other refusal
		minCheck = &domain.Check{ID: "min_notional", Label: "Hyperliquid minimum", Status: domain.CheckFail,
			Detail: fmt.Sprintf("$%.2f of $10 minimum; size at least %s", notional, minSz)}
	}

	orders := []exchange.OrderWire{{
		A: m.AssetID, B: side == domain.Buy, P: hyperliquid.ToWire(px), S: hyperliquid.ToWire(sz),
		R: p.ReduceOnly, T: exchange.OrderType{Limit: &exchange.LimitType{Tif: tif}},
	}}
	var legs []TriggerLeg
	var legWarn []string
	for _, leg := range []struct{ kind, val string }{{"tp", p.TP}, {"sl", p.SL}} {
		if leg.val == "" {
			continue
		}
		tpx, terr := triggerPrice(leg.kind, leg.val, entry, side, m)
		if terr != nil {
			return nil, terr
		}
		tl := TriggerLeg{Kind: leg.kind, TriggerPx: hyperliquid.ToWire(tpx), Size: hyperliquid.ToWire(sz), Side: opposite(side)}
		if mark := m.Mark; mark > 0 {
			tf, _ := tpx.Float64()
			// tp of a long and sl of a short fire when the price rises to them
			if (leg.kind == "tp") == (side == domain.Buy) {
				tl.Through = mark >= tf
			} else {
				tl.Through = mark <= tf
			}
			if tl.Through {
				legWarn = append(legWarn, fmt.Sprintf("%s %s is already through the mark %s: it fires as soon as the entry fills", strings.ToUpper(leg.kind), tl.TriggerPx, hyperliquid.ToWire(decimal.NewFromFloat(mark))))
			}
		}
		legs = append(legs, tl)
		orders = append(orders, exchange.OrderWire{
			A: m.AssetID, B: opposite(side) == domain.Buy, P: hyperliquid.ToWire(tpx), S: hyperliquid.ToWire(sz), R: true,
			T: exchange.OrderType{Trigger: &exchange.TriggerType{IsMarket: true, TriggerPx: hyperliquid.ToWire(tpx), Tpsl: leg.kind}},
		})
	}
	grouping := "na"
	if len(orders) > 1 {
		grouping = "normalTpsl"
	}
	action := exchange.OrderAction{Type: "order", Orders: orders, Grouping: grouping}

	preview := &OrderPreview{
		Action: "order", Coin: m.Coin, Side: side, Size: hyperliquid.ToWire(sz), Px: hyperliquid.ToWire(px),
		NotionalUSD: notional, Network: network(env), DryRun: true,
		Triggers: legs, Grouping: grouping, Warnings: legWarn,
	}
	if minCheck != nil {
		preview.Checks = append(preview.Checks, *minCheck)
	}
	if vault != nil {
		preview.Vault = *vault
	}
	intent := Intent{Action: "order", Coin: m.Coin, Side: side, NotionalUSD: notional, Leverage: p.Leverage,
		ReduceOnly: p.ReduceOnly, Network: network(env), Source: SourceFrom(ctx)}
	if p.Leverage == 0 && !p.ReduceOnly {
		// Without --leverage the order runs at whatever leverage the
		// account already has on this coin; the cap must see it (audit S6).
		intent.Leverage, intent.LeverageFrom = currentLeverage(ctx, env, vault, m)
	}
	if vault != nil {
		intent.Vault = *vault
	}
	signable, err := authorize(ctx, env, intent, preview, p.DryRun, p.Yes)
	if err != nil {
		return nil, err
	}
	title := fmt.Sprintf("%s %s %s @ %s", strings.ToUpper(sideWord(side)), preview.Size, m.Display, preview.Px)
	if !signable {
		return orderResult(env, preview, title), nil
	}
	setLev := p.Leverage
	if setLev == 0 && intent.LeverageFrom == "default" && env.Config.Risk.MaxLeverage > 0 {
		// No position: Hyperliquid would use the account's remembered
		// per-asset leverage (activeAssetData), which may be above the
		// value the cap checked. With a cap set, set it explicitly first
		// (audit S6).
		setLev = intent.Leverage
	}
	if setLev > 0 {
		lev := exchange.UpdateLeverageAction{Type: "updateLeverage", Asset: m.AssetID, IsCross: crossMode(ctx, env, vault, m, p.Isolated), Leverage: setLev}
		if err := post(ctx, env, lev, vault, p.DryRun, preview); err != nil {
			return nil, err
		}
	}
	if err := post(ctx, env, action, vault, p.DryRun, preview); err != nil {
		return nil, err
	}
	return orderResult(env, preview, title), nil
}

// defaultLeverage is Hyperliquid's leverage for a coin the account has not
// set one on: 20x, or the market maximum when lower.
const defaultLeverage = 20

// currentLeverage is the leverage an order without --leverage would use:
// the open position's leverage on the coin ("position"); with no position,
// the market default ("default"), which runOrder then sets explicitly with
// updateLeverage before the order. An account read error is "unknown" at
// the market maximum, and the cap check fails closed.
func currentLeverage(ctx context.Context, env *Env, vault *string, m domain.Market) (int, string) {
	if owner := closeOwner(env, vault); owner != "" && env.HL != nil {
		a, err := env.HL.Account(ctx, owner)
		if err != nil {
			return max(m.MaxLeverage, defaultLeverage), "unknown"
		}
		for _, pos := range a.Positions {
			if pos.Coin == m.Coin && pos.Size != 0 && pos.Leverage > 0 {
				return int(math.Ceil(pos.Leverage)), "position"
			}
		}
	}
	if m.MaxLeverage > 0 {
		return min(defaultLeverage, m.MaxLeverage), "default"
	}
	return defaultLeverage, "default"
}

// crossMode decides the margin mode sent with updateLeverage. An explicit
// isolated request or an isolated-only market is isolated; otherwise an
// existing position keeps its current mode (updateLeverage would silently
// switch it), and a new position is cross.
func crossMode(ctx context.Context, env *Env, vault *string, m domain.Market, isolated bool) bool {
	if isolated || m.OnlyIsolated {
		return false
	}
	if owner := closeOwner(env, vault); owner != "" && env.HL != nil {
		if a, err := env.HL.Account(ctx, owner); err == nil {
			for _, pos := range a.Positions {
				if pos.Coin == m.Coin && pos.Size != 0 {
					return pos.Cross
				}
			}
		}
	}
	return true
}

func sideWord(s domain.Side) string {
	if s == domain.Buy {
		return "buy"
	}
	return "sell"
}

// ---- cancel_order ----

func runCancel(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*CancelInput)
	if p.Coin == "" {
		return nil, Errorf(CodeUsage, "cancel needs a coin (and an oid, or no oid to cancel all on that coin)")
	}
	m, err := env.ResolveTrade(ctx, p.Coin)
	if err != nil {
		return nil, err
	}
	vault, err := vaultPtr(env, p.Vault)
	if err != nil {
		return nil, err
	}
	var cancels []exchange.Cancel
	if p.OID != 0 {
		cancels = []exchange.Cancel{{A: m.AssetID, O: p.OID}}
	} else {
		owner := env.Config.Trading.AccountAddr
		if vault != nil {
			owner = *vault
		}
		if owner == "" {
			owner = env.Config.Account
		}
		if owner == "" {
			return nil, Errorf(CodeUsage, "cancel-all needs trading.account (or a vault) in config to list open orders")
		}
		a, err := env.HL.Account(ctx, owner)
		if err != nil {
			return nil, err
		}
		for _, o := range a.OpenOrders {
			if o.Coin == m.Coin {
				cancels = append(cancels, exchange.Cancel{A: m.AssetID, O: o.OID})
			}
		}
		if len(cancels) == 0 {
			return nil, &Error{Code: CodeNotFound, Message: "no open orders on " + m.Display}
		}
	}
	preview := &OrderPreview{Action: "cancel", Coin: m.Coin, Network: network(env), DryRun: true, Size: fmt.Sprintf("%d order(s)", len(cancels))}
	intent := Intent{Action: "cancel", Coin: m.Coin, Network: network(env), Source: SourceFrom(ctx)}
	if vault != nil {
		preview.Vault, intent.Vault = *vault, *vault
	}
	signable, err := authorize(ctx, env, intent, preview, p.DryRun, p.Yes)
	if err != nil {
		return nil, err
	}
	title := fmt.Sprintf("Cancel %d %s order(s)", len(cancels), m.Display)
	if signable {
		if err := post(ctx, env, exchange.CancelAction{Type: "cancel", Cancels: cancels}, vault, p.DryRun, preview); err != nil {
			return nil, err
		}
	}
	return orderResult(env, preview, title), nil
}

// ---- vault deposit / withdraw ----

func runVaultTransfer(deposit bool) func(context.Context, *Env, any) (*Result, error) {
	return func(ctx context.Context, env *Env, in any) (*Result, error) {
		p := in.(*VaultTransferInput)
		if p.USD <= 0 || p.USD > 1e9 {
			return nil, Errorf(CodeUsage, "usd must be a positive amount")
		}
		if deposit && p.USD < 5 {
			return nil, Errorf(CodeUsage, "Hyperliquid vault deposits are at least $5")
		}
		// Money moves only to an unambiguous vault (audit S2).
		addr, row, err := resolveVaultStrict(ctx, env, p.Vault, deposit)
		if err != nil {
			return nil, err
		}
		action := "vault_withdraw"
		if deposit {
			action = "vault_deposit"
			if row != nil && (row.Closed || !row.AllowDeposits) {
				return nil, &Error{Code: CodeSafety, Message: row.Name + " is not accepting deposits"}
			}
		}
		micro := uint64(math.Round(p.USD * 1e6))
		preview := &OrderPreview{Action: action, Vault: addr, NotionalUSD: p.USD, Network: network(env), DryRun: true,
			Checks: vaultChecks(addr, row, deposit, env.now())}
		if row != nil {
			preview.VaultName = domain.CleanText(row.Name)
			preview.VaultLeader = strings.ToLower(domain.CleanText(row.Leader))
			preview.VaultTVL = row.TVL
			preview.VaultAgeDays = vaultAge(*row, env.now())
		}
		intent := Intent{Action: action, Vault: addr, NotionalUSD: p.USD, Network: network(env), Source: SourceFrom(ctx)}
		signable, err := authorize(ctx, env, intent, preview, p.DryRun, p.Yes)
		if err != nil {
			return nil, err
		}
		name := addr
		if row != nil && domain.CleanText(row.Name) != "" {
			name = domain.CleanText(row.Name) + " (" + shortAddr(addr) + ")"
		}
		verb := "Withdraw"
		if deposit {
			verb = "Deposit"
		}
		title := fmt.Sprintf("%s $%.2f %s %s", verb, p.USD, map[bool]string{true: "into", false: "from"}[deposit], name)
		if signable {
			// vaultTransfer is about the signer's own account: signed with vault=nil
			act := exchange.VaultTransferAction{Type: "vaultTransfer", VaultAddress: addr, IsDeposit: deposit, USD: micro}
			if err := post(ctx, env, act, nil, p.DryRun, preview); err != nil {
				return nil, err
			}
		}
		return orderResult(env, preview, title), nil
	}
}

// ---- close_position ----

// closeOwner is the account whose positions close_position reads: the vault
// when trading for one, else trading.account, else the default
// account.
func closeOwner(env *Env, vault *string) string {
	switch {
	case vault != nil:
		return *vault
	case env.Config.Trading.AccountAddr != "":
		return env.Config.Trading.AccountAddr
	}
	return env.Config.Account
}

// closeLeg builds one reduce-only IOC order that flattens pct of p, priced
// at the book mid (or mark) +/- slippage towards the fill.
func closeLeg(ctx context.Context, env *Env, p domain.Position, pct float64) (exchange.OrderWire, domain.Market, decimal.Decimal, decimal.Decimal, error) {
	m, err := env.ResolveTrade(ctx, p.Coin)
	if err != nil {
		return exchange.OrderWire{}, m, decimal.Zero, decimal.Zero, err
	}
	side := domain.Sell
	if p.Size < 0 {
		side = domain.Buy
	}
	sz := hyperliquid.FloorSz(decimal.NewFromFloat(math.Abs(p.Size)*pct/100), m.SzDecimals)
	if !sz.IsPositive() {
		return exchange.OrderWire{}, m, decimal.Zero, decimal.Zero, Errorf(CodeUsage, "%.4g%% of %s %s is below one size increment", pct, hyperliquid.ToWire(decimal.NewFromFloat(p.Size)), m.Display)
	}
	ref := p.Mark
	if b, berr := env.HL.Book(ctx, m.Coin, hyperliquid.BookOpts{}); berr == nil && b.Mid() > 0 {
		ref = b.Mid()
	}
	if ref <= 0 {
		ref = m.Mark
	}
	if ref <= 0 {
		return exchange.OrderWire{}, m, decimal.Zero, decimal.Zero, &Error{Code: CodeStale, Message: "no reference price for " + m.Display}
	}
	slip := float64(env.Config.Trading.SlippageBps)
	if slip <= 0 {
		slip = 50
	}
	f, dir := 1+slip/10_000, +1
	if side == domain.Sell {
		f, dir = 1-slip/10_000, -1
	}
	px := hyperliquid.RoundPx(decimal.NewFromFloat(ref*f), m.SzDecimals, maxDecimals(m), dir)
	w := exchange.OrderWire{A: m.AssetID, B: side == domain.Buy, P: hyperliquid.ToWire(px), S: hyperliquid.ToWire(sz), R: true,
		T: exchange.OrderType{Limit: &exchange.LimitType{Tif: "Ioc"}}}
	return w, m, sz, px, nil
}

func runClose(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*CloseInput)
	if p.Pct == 0 {
		p.Pct = 100
	}
	if p.Pct < 0 || p.Pct > 100 {
		return nil, Errorf(CodeUsage, "pct must be between 0 and 100, got %v", p.Pct)
	}
	if (p.Coin == "") == !p.All {
		return nil, Errorf(CodeUsage, "give a coin, or all=true to close every position")
	}
	vault, err := vaultPtr(env, p.Vault)
	if err != nil {
		return nil, err
	}
	owner := closeOwner(env, vault)
	if owner == "" {
		return nil, &Error{Code: CodeUsage, Message: "close needs an account to read positions from", Hint: "set trading.account (or account) in config, or pass --vault"}
	}
	coin := ""
	if p.Coin != "" {
		m, err := env.ResolveTrade(ctx, p.Coin)
		if err != nil {
			return nil, err
		}
		coin = m.Coin
	}
	acct, err := env.HL.Account(ctx, owner)
	if err != nil {
		return nil, err
	}
	var legs []exchange.OrderWire
	var notional float64
	var sz, px string
	for _, pos := range acct.Positions {
		if pos.Size == 0 || (coin != "" && pos.Coin != coin) {
			continue
		}
		w, _, s, x, err := closeLeg(ctx, env, pos, p.Pct)
		if err != nil {
			return nil, err
		}
		legs = append(legs, w)
		v, _ := s.Mul(x).Float64()
		notional += v
		sz, px = hyperliquid.ToWire(s), hyperliquid.ToWire(x)
	}
	if len(legs) == 0 {
		what := "any open positions"
		if coin != "" {
			what = "a " + coin + " position"
		}
		return nil, &Error{Code: CodeNotFound, Message: shortAddr(owner) + " does not have " + what}
	}
	action := "order"
	if p.All {
		action = "close_all"
	}
	preview := &OrderPreview{Action: action, Coin: coin, NotionalUSD: notional, Network: network(env), DryRun: true}
	if len(legs) == 1 {
		preview.Size, preview.Px = sz, px
		preview.Side = domain.Sell
		if legs[0].B {
			preview.Side = domain.Buy
		}
	} else {
		preview.Size = fmt.Sprintf("%d positions", len(legs))
	}
	intent := Intent{Action: action, Coin: coin, Side: preview.Side, NotionalUSD: notional, ReduceOnly: true,
		Network: network(env), Source: SourceFrom(ctx)}
	if vault != nil {
		preview.Vault, intent.Vault = *vault, *vault
	}
	signable, err := authorize(ctx, env, intent, preview, p.DryRun, p.Yes)
	if err != nil {
		return nil, err
	}
	// Flattening everything asks twice when a person is at the keyboard:
	// the TUI and CLI confirmers run two stages for Action "close_all".
	title := fmt.Sprintf("Close %s%% of %s", trimFloat(p.Pct), coinOrAll(coin, len(legs)))
	if signable {
		act := exchange.OrderAction{Type: "order", Orders: legs, Grouping: "na"}
		if err := post(ctx, env, act, vault, p.DryRun, preview); err != nil {
			return nil, err
		}
	}
	return orderResult(env, preview, title), nil
}

func coinOrAll(coin string, n int) string {
	if coin != "" {
		return coin
	}
	return fmt.Sprintf("all %d positions", n)
}

func trimFloat(f float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", f), "0"), ".")
}
