package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hyperliquid"
	"github.com/maxmoneycash/hlp/internal/paperx"
)

// Manual paper trading on the live-book paper exchange (internal/paperx):
// paper_order, paper_cancel, paper_reset and paper_account, plus
// `hlp order … --paper`, `account paper:<id>` and `fills paper:<id>`.
// Nothing here signs anything or talks to /exchange; reads are public
// /info (book, mark, 1m candles and funding for catch-up) and, with
// waitMs or a streaming account view, the public WebSocket.

// CardPaperOrder renders a paper order, cancel or reset. Data: *PaperOrderData.
const CardPaperOrder Card = "paperorder"

// CardPaperAccount renders `hlp paper account`. Data: *paperx.Summary.
const CardPaperAccount Card = "paperaccount"

// ModePaper and ModeLive are the meta.mode labels.
const (
	ModePaper = "paper"
	ModeLive  = "live"
)

// PaperOrderInput is the input of paper_order (`hlp paper order`).
type PaperOrderInput struct {
	Account      string  `json:"account,omitempty" jsonschema:"paper account id or paper:<id>; default 'default' (created with $10000 on first use)"`
	Coin         string  `json:"coin"`
	Side         string  `json:"side" jsonschema:"buy or sell"`
	Size         string  `json:"size,omitempty" jsonschema:"base units as a decimal string"`
	USD          float64 `json:"usd,omitempty" jsonschema:"notional in USD (alternative to size)"`
	Px           string  `json:"px,omitempty" jsonschema:"limit price; empty = market (Ioc at mid +/- trading.slippage_bps)"`
	TIF          string  `json:"tif,omitempty" jsonschema:"Gtc (default), Ioc or Alo for limit orders"`
	TP           string  `json:"tp,omitempty" jsonschema:"take profit: price or +5% (trigger-market on mark)"`
	SL           string  `json:"sl,omitempty" jsonschema:"stop loss: price or -2% (trigger-market on mark)"`
	PositionTpsl bool    `json:"positionTpsl,omitempty" jsonschema:"size tp/sl to the whole position (positionTpsl) instead of this order's fills (normalTpsl)"`
	ReduceOnly   bool    `json:"reduceOnly,omitempty"`
	Leverage     int     `json:"leverage,omitempty"`
	Isolated     bool    `json:"isolated,omitempty" jsonschema:"isolated margin with leverage (margin is locked)"`
	WaitMs       int     `json:"waitMs,omitempty" jsonschema:"keep following the live book and trades this long (ms, max 60000) so a resting order can queue and fill; 0 = return after the first book"`
}

// PaperCancelInput is the input of paper_cancel (`hlp paper cancel`).
type PaperCancelInput struct {
	Account string `json:"account,omitempty" jsonschema:"paper account id or paper:<id>; default 'default'"`
	Coin    string `json:"coin,omitempty" jsonschema:"cancel every live order on this coin (with no oid)"`
	OID     uint64 `json:"oid,omitempty" jsonschema:"one order id; empty with no coin = cancel everything"`
}

// PaperResetInput is the input of paper_reset (`hlp paper reset`).
type PaperResetInput struct {
	Account string  `json:"account,omitempty" jsonschema:"paper account id or paper:<id>; default 'default'"`
	Balance float64 `json:"balance,omitempty" jsonschema:"starting USDC, default 10000"`
	Yes     bool    `json:"yes,omitempty" jsonschema:"confirm: wipes the account's positions, orders and history"`
}

// PaperAccountInput is the input of paper_account (`hlp paper account`).
type PaperAccountInput struct {
	Account string `json:"account,omitempty" jsonschema:"paper account id or paper:<id>; default 'default'; 'all' lists the ids"`
}

// PaperOrderData is a paper order, cancel or reset result.
type PaperOrderData struct {
	Mode     string             `json:"mode"`    // always "paper"
	Account  string             `json:"account"` // paper:<id>
	Order    *paperx.OrderView  `json:"order,omitempty"`
	Children []paperx.OrderView `json:"children,omitempty"` // TP/SL legs
	Canceled []uint64           `json:"canceled,omitempty"`
	Checks   []domain.Check     `json:"checks,omitempty"` // risk caps, as for live
	Sync     *PaperSync         `json:"sync,omitempty"`
	Summary  paperx.Summary     `json:"summary"`
	Warnings []string           `json:"warnings,omitempty"`
}

// PaperSync reports the catch-up done when the account was loaded: fills
// from 1m candles while no feed was attached, funding settled.
type PaperSync struct {
	Coins   []string `json:"coins"`
	Fills   int      `json:"fills"`
	Funding int      `json:"funding"`
}

func paperTools() []Tool {
	return []Tool{
		{Name: "paper_order", Command: "paper order", Slash: "paperorder", Positional: []string{"side", "coin", "size"}, Group: "trade", LocalState: true, NewInput: in[PaperOrderInput](), Run: runPaperOrder,
			Description: "Place an order on a local paper account against the LIVE Hyperliquid book: Hyperliquid's tick, lot, $10-minimum, Alo and reduce-only rules with its own rejection text; ~200 ms latency; taker fills walk the real depth minus liquidity this account already took (never past it); resting orders queue behind the size shown at their price and fill by traded volume; TP/SL fire on mark. Every result is labelled mode \"paper\". No keys; nothing is sent to the exchange.",
			Summary:     "Paper order on the live book (queue, impact, latency; no keys, nothing sent)",
			Examples:    []string{"hlp paper order buy BTC 0.01", "hlp paper order sell ETH 0.5 --px 2700 --tif Alo --wait-ms 30000", "hlp order buy hype --usd 100 --paper"}},
		{Name: "paper_cancel", Command: "paper cancel", Slash: "papercancel", Positional: []string{"coin", "oid"}, Group: "trade", LocalState: true, NewInput: in[PaperCancelInput](), Run: runPaperCancel,
			Description: "Cancel one paper order (oid), every live paper order on a coin, or all of them.",
			Summary:     "Cancel paper orders"},
		{Name: "paper_reset", Command: "paper reset", Slash: "paperreset", Positional: []string{"account"}, Group: "trade", LocalState: true, NewInput: in[PaperResetInput](), Run: runPaperReset,
			Description: "Create or wipe a paper account with a starting USDC balance (default 10000). Needs yes when the account exists.",
			Summary:     "Create or reset a paper account"},
		{Name: "paper_account", Command: "paper account", Slash: "paperaccount", Positional: []string{"account"}, Group: "account", LocalState: true, NewInput: in[PaperAccountInput](), Run: runPaperAccount,
			Description: "A paper account brought up to date (resting orders replayed from 1m candles while no feed was attached, settled funding): equity, P&L split into realized, unrealized, fees and funding, positions with liquidation prices, live orders with their queue. `hlp account paper:<id>` and `hlp fills paper:<id>` read the same account like any address.",
			Summary:     "Paper account summary: P&L, fees, funding, orders with queue position"},
	}
}

func paperStore(env *Env) (paperx.Store, error) {
	if env.Paths.DataDir == "" {
		return paperx.Store{}, &Error{Code: CodeInternal, Message: "no data directory for paper accounts"}
	}
	return paperx.Store{Dir: filepath.Join(env.Paths.DataDir, "paper", "accounts")}, nil
}

// paperID normalizes "", "paper", "paper:<id>" and "<id>".
func paperID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if id, ok := paperx.ParseAddress(s); ok {
		s = id
	}
	if s == "" {
		s = "default"
	}
	if err := paperx.ValidID(s); err != nil {
		return "", &Error{Code: CodeUsage, Message: err.Error(), Hint: "e.g. default, scalp-1"}
	}
	return s, nil
}

func paperConfig(env *Env) paperx.Config { return paperx.Config{} }

func paperMeta(env *Env) domain.Meta {
	m := env.meta("paper")
	m.Mode = ModePaper
	return m
}

func (env *Env) nowMs() int64 { return env.now().UnixMilli() }

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// paperMark reads a coin's live mark (falls back to the registry's).
func paperMark(ctx context.Context, env *Env, m domain.Market) decimal.Decimal {
	if r, ok := env.HL.(interface {
		MarketCtx(ctx context.Context, coin string) (domain.Market, error)
	}); ok {
		if mc, err := r.MarketCtx(ctx, m.Coin); err == nil && mc.Mark > 0 {
			return decimal.NewFromFloat(mc.Mark)
		}
	}
	if m.Mark > 0 {
		return decimal.NewFromFloat(m.Mark)
	}
	return decimal.Zero
}

// syncPaper brings coins up to date on e: specs, catch-up from 1m candles
// since the coin was last synced, settled funding, the mark and (unless
// skipBook) a fresh book.
func syncPaper(ctx context.Context, env *Env, e *paperx.Engine, coins []string, skipBook map[string]bool) (*PaperSync, []string) {
	var warn []string
	sync := &PaperSync{Coins: coins}
	for _, coin := range coins {
		m, err := env.Resolve(ctx, coin)
		if err != nil {
			warn = append(warn, fmt.Sprintf("%s: %v", coin, err))
			continue
		}
		e.SetSpec(paperx.SpecFrom(m))
		now := env.nowMs()
		var cs []domain.Candle
		if since := e.SyncedAt(coin); since > 0 && now-since > 60_000 {
			n := min(int((now-since)/60_000)+2, hyperliquid.MaxCandles)
			cs, err = env.HL.Candles(ctx, coin, domain.I1m, n, now)
			if err != nil {
				warn = append(warn, fmt.Sprintf("%s: catch-up candles: %v", coin, err))
			} else {
				if len(cs) > 0 && cs[0].T > since+60_000 {
					warn = append(warn, fmt.Sprintf("%s: detached longer than Hyperliquid's 1m history; resting orders were replayed from %s", coin, time.UnixMilli(cs[0].T).UTC().Format(time.RFC3339)))
				}
				sync.Fills += e.CatchUp(coin, cs, 60_000, now-now%60_000)
			}
		}
		if from := e.FundingFrom(coin); from > 0 && now-from > 3_600_000 {
			pts, err := env.HL.Funding(ctx, coin, from, now)
			if err != nil {
				warn = append(warn, fmt.Sprintf("%s: funding: %v", coin, err))
			} else {
				sync.Funding += e.CatchUpFunding(coin, pts, func(t int64) decimal.Decimal { return candleAt(cs, t) })
			}
		}
		if mark := paperMark(ctx, env, m); mark.Sign() > 0 {
			e.OnMark(coin, mark, env.nowMs())
		}
		if !skipBook[coin] {
			if b, err := env.HL.Book(ctx, coin, hyperliquid.BookOpts{}); err == nil {
				e.OnBook(b, env.nowMs())
			} else {
				warn = append(warn, fmt.Sprintf("%s: book: %v", coin, err))
			}
		}
	}
	return sync, warn
}

// candleAt is the open of the 1m candle at t (zero when not covered): the
// oracle proxy for funding settled during catch-up.
func candleAt(cs []domain.Candle, t int64) decimal.Decimal {
	i := sort.Search(len(cs), func(i int) bool { return cs[i].T >= t })
	if i < len(cs) && cs[i].T-t < 60_000 {
		return decimal.NewFromFloat(cs[i].O)
	}
	return decimal.Zero
}

func newPaperState(env *Env, id string, balance decimal.Decimal) *paperx.State {
	st := paperx.NewState(id, balance, env.nowMs())
	st.Network = network(env)
	return st
}

func paperNotFound(id string) error {
	return &Error{Code: CodeNotFound, Message: "no paper account " + id,
		Hint: "place an order with `hlp paper order buy BTC 0.001` (creates it with $10000) or run `hlp paper reset " + id + "`"}
}

// paperRiskChecks applies the order caps a live order would face
// (max_notional, max_leverage, allowed_coins). Setup checks (keys,
// trading enabled) do not apply to paper; neither does the daily loss
// halt, which reads the live account's fills.
func paperRiskChecks(ctx context.Context, env *Env, in Intent) ([]domain.Check, error) {
	var out []domain.Check
	var failed []string
	for _, c := range previewChecks(ctx, env, in) {
		switch c.ID {
		case "max_notional", "max_leverage", "allowed_coins":
			out = append(out, c)
			if c.Status == domain.CheckFail {
				failed = append(failed, c.ID+" ("+c.Detail+")")
			}
		}
	}
	if len(failed) > 0 {
		return out, &Error{Code: CodeSafety, Message: "paper order refused by risk caps: " + strings.Join(failed, ", "),
			Hint: "paper orders obey the same risk caps as live ones (config [risk])"}
	}
	return out, nil
}

// paperOrder places a paper order and waits for the first book after the
// latency. It is the shared core of paper_order and `order --paper`.
func paperOrder(ctx context.Context, env *Env, p *PaperOrderInput) (*PaperOrderData, *Result, error) {
	id, err := paperID(p.Account)
	if err != nil {
		return nil, nil, err
	}
	store, err := paperStore(env)
	if err != nil {
		return nil, nil, err
	}
	side, err := ParseSide(p.Side)
	if err != nil {
		return nil, nil, err
	}
	m, err := env.ResolveTrade(ctx, p.Coin)
	if err != nil {
		return nil, nil, err
	}
	if m.Kind != domain.Perp && m.Kind != domain.HIP3 {
		return nil, nil, &Error{Code: CodeUsage, Message: "paper trading covers perps (main dex and HIP-3) only; " + m.Display + " is " + string(m.Kind)}
	}
	if m.Delisted {
		return nil, nil, Errorf(CodeUsage, "%s is delisted", m.Display)
	}
	if (p.Size == "") == (p.USD == 0) {
		return nil, nil, Errorf(CodeUsage, "give exactly one of size (base units) or usd (notional)")
	}
	if p.USD < 0 {
		return nil, nil, Errorf(CodeUsage, "usd must be positive")
	}
	if p.WaitMs < 0 || p.WaitMs > 60_000 {
		return nil, nil, Errorf(CodeUsage, "waitMs must be 0-60000, got %d", p.WaitMs)
	}
	tif := strings.TrimSpace(p.TIF)
	switch strings.ToLower(tif) {
	case "", "gtc":
		tif = paperx.Gtc
	case "ioc":
		tif = paperx.Ioc
	case "alo", "post", "postonly", "post-only":
		tif = paperx.Alo
	default:
		return nil, nil, Errorf(CodeUsage, "tif must be Gtc, Ioc or Alo, got %q", p.TIF)
	}
	md := maxDecimals(m)
	req := paperx.OrderReq{Coin: m.Coin, Side: side, TIF: tif, ReduceOnly: p.ReduceOnly, Leverage: p.Leverage, Isolated: p.Isolated, PositionTpsl: p.PositionTpsl}
	var entry decimal.Decimal
	var decision domain.Book
	if p.Px != "" {
		// limit prices are validated by the engine with Hyperliquid's own
		// text, but a malformed number is still a usage error here
		if req.Px, err = ParseDecimal("px", p.Px); err != nil {
			return nil, nil, err
		}
		entry = req.Px
	} else {
		if p.TIF != "" && tif != paperx.Ioc {
			return nil, nil, Errorf(CodeUsage, "a market order is Ioc; pass px for a %s limit order", tif)
		}
		decision, err = env.HL.Book(ctx, m.Coin, hyperliquid.BookOpts{})
		if err != nil {
			return nil, nil, err
		}
		ref := decision.Mid()
		if ref <= 0 {
			return nil, nil, &Error{Code: CodeStale, Message: "no two-sided book for " + m.Display, Hint: "pass --px for a limit order"}
		}
		slip := float64(env.Config.Trading.SlippageBps)
		if slip <= 0 {
			slip = 50
		}
		f, dir := 1+slip/10_000, +1
		if side == domain.Sell {
			f, dir = 1-slip/10_000, -1
		}
		req.Px = hyperliquid.RoundPx(decimal.NewFromFloat(ref*f), m.SzDecimals, md, dir)
		req.Market = true
		entry = decimal.NewFromFloat(ref)
	}
	if p.Size != "" {
		if req.Sz, err = ParseDecimal("size", p.Size); err != nil {
			return nil, nil, err
		}
	} else {
		req.Sz = hyperliquid.FloorSz(decimal.NewFromFloat(p.USD).Div(req.Px), m.SzDecimals)
	}
	if p.TP != "" {
		if req.TP, err = triggerPrice("tp", p.TP, entry, side, m); err != nil {
			return nil, nil, err
		}
	}
	if p.SL != "" {
		if req.SL, err = triggerPrice("sl", p.SL, entry, side, m); err != nil {
			return nil, nil, err
		}
	}
	notional, _ := req.Sz.Mul(req.Px).Float64()
	checks, err := paperRiskChecks(ctx, env, Intent{Action: "order", Coin: m.Coin, Side: side, NotionalUSD: notional,
		Leverage: p.Leverage, ReduceOnly: p.ReduceOnly, Network: network(env), Source: SourceFrom(ctx)})
	if err != nil {
		return nil, nil, err
	}
	data := &PaperOrderData{Mode: ModePaper, Account: paperx.AddressPrefix + id, Checks: checks}
	var rejErr error
	cfg := paperConfig(env)
	st, err := store.Update(id, func() *paperx.State { return newPaperState(env, id, paperx.DefaultBalance) }, func(st *paperx.State) error {
		e := paperx.New(st, cfg)
		e.SetSpec(paperx.SpecFrom(m))
		coins := e.Coins()
		if !containsString(coins, m.Coin) {
			coins = append(coins, m.Coin)
		}
		sync, warn := syncPaper(ctx, env, e, coins, map[string]bool{m.Coin: true})
		data.Sync, data.Warnings = sync, append(data.Warnings, warn...)
		at := env.nowMs()
		o, err := e.Submit(req, at)
		if err != nil {
			var rej *paperx.Reject
			if !errors.As(err, &rej) {
				return err
			}
			rejErr = rej
			if o != nil {
				v := e.ViewOrder(o)
				data.Order = &v
			}
			return nil // the rejection is recorded on the account like the exchange's
		}
		// the fill book must postdate the decision by the latency
		if err := sleepCtx(ctx, e.Config().Latency); err != nil {
			return err
		}
		b, err := env.HL.Book(ctx, m.Coin, hyperliquid.BookOpts{})
		if err != nil {
			return err
		}
		e.OnBook(b, max(env.nowMs(), o.ActiveAt))
		if o.Status == paperx.StatusRejected { // a rule that needs the book (Alo, margin, reduce-only)
			rejErr = &paperx.Reject{Code: rejectCodeOf(o.Error), Msg: o.Error}
		}
		v := e.ViewOrder(o)
		data.Order = &v
		for _, c := range st.Orders {
			if c.Parent == o.OID {
				data.Children = append(data.Children, e.ViewOrder(c))
			}
		}
		data.Summary = e.Summarize()
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if p.WaitMs > 0 && data.Order != nil && data.Order.Status == paperx.StatusOpen {
		w := waitPaper(ctx, env, store, id, time.Duration(p.WaitMs)*time.Millisecond)
		data.Warnings = append(data.Warnings, w...)
		if back, err := store.Load(id); err == nil {
			st = back
		}
	}
	e := paperx.New(st, cfg)
	if data.Order != nil {
		if o, ok := e.Order(data.Order.OID); ok {
			v := e.ViewOrder(o)
			data.Order = &v
		}
	}
	data.Summary = e.Summarize()
	res := &Result{Card: CardPaperOrder, Data: data, Meta: paperMeta(env)}
	res.Meta.Warnings = append(res.Meta.Warnings, data.Warnings...)
	if data.Order != nil {
		res.Title = fmt.Sprintf("PAPER %s %s %s @ %s · %s", strings.ToUpper(data.Order.Side), data.Order.Sz, m.Display, data.Order.Px, data.Order.Status)
	}
	if rejErr != nil {
		return data, res, &Error{Code: CodeUsage, Message: "paper order rejected: " + rejErr.Error(),
			Details: map[string]string{"mode": ModePaper, "account": data.Account}, Hint: rejectHint(rejErr, m)}
	}
	return data, res, nil
}

// rejectCodeOf maps a book-time rejection text back to its code.
func rejectCodeOf(msg string) string {
	switch {
	case strings.HasPrefix(msg, "Post only"):
		return "ALO_CROSS"
	case strings.HasPrefix(msg, "Insufficient margin"):
		return "MARGIN"
	case strings.HasPrefix(msg, "Reduce only"):
		return "REDUCE_ONLY"
	}
	return ""
}

func rejectHint(err error, m domain.Market) string {
	var rej *paperx.Reject
	if !errors.As(err, &rej) {
		return ""
	}
	switch rej.Code {
	case "TICK":
		return fmt.Sprintf("prices have at most 5 significant figures and %d decimals for %s", maxDecimals(m)-m.SzDecimals, m.Display)
	case "SIZE":
		return fmt.Sprintf("sizes have at most %d decimals for %s", m.SzDecimals, m.Display)
	case "MIN_VALUE":
		return "orders must be worth at least $10 (reduce-only orders are exempt)"
	case "ALO_CROSS":
		return "an Alo (post-only) order must not cross the book; move the price away or use Gtc"
	case "MARGIN":
		return "lower the size or raise leverage; `hlp paper account` shows the free margin"
	case "REDUCE_ONLY":
		return "there is no position for a reduce-only order to reduce on this side"
	}
	return ""
}

// waitPaper follows the live feed for d so resting orders can queue and
// fill (no-op without a WebSocket).
func waitPaper(ctx context.Context, env *Env, store paperx.Store, id string, d time.Duration) []string {
	if env.Stream == nil {
		return []string{"waitMs ignored: no live feed in this mode"}
	}
	wctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	r := &paperx.Runner{Store: store, ID: id, Stream: env.Stream, Config: paperConfig(env), Now: env.now, Funding: env.HL.Funding}
	if err := r.Run(wctx); err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return []string{"live feed: " + err.Error()}
	}
	if err := r.Err(); err != nil {
		return []string{"live feed: " + err.Error()}
	}
	return nil
}

func runPaperOrder(ctx context.Context, env *Env, in any) (*Result, error) {
	_, res, err := paperOrder(ctx, env, in.(*PaperOrderInput))
	if err != nil {
		return nil, err
	}
	return res, nil
}

// orderViaPaper serves `hlp order … --paper`: place_order's input on the
// paper exchange, returned as an OrderPreview with Paper set.
func orderViaPaper(ctx context.Context, env *Env, p *OrderInput) (*Result, error) {
	if p.Vault != "" {
		return nil, Errorf(CodeUsage, "paper orders have no vault; use paperAccount to pick a paper account")
	}
	pi := &PaperOrderInput{Account: p.PaperAccount, Coin: p.Coin, Side: p.Side, Size: p.Size, USD: p.USD, Px: p.Px, TP: p.TP, SL: p.SL,
		ReduceOnly: p.ReduceOnly, Leverage: p.Leverage, Isolated: p.Isolated}
	data, res, err := paperOrder(ctx, env, pi)
	if err != nil {
		return nil, err
	}
	pv := &OrderPreview{Action: "order", Network: network(env), DryRun: true, Checks: data.Checks, Paper: data,
		Warnings: []string{"paper order: filled against the live book in a local simulator; nothing was sent"}}
	if o := data.Order; o != nil {
		pv.Coin, pv.Size, pv.Px = o.Coin, o.Sz, o.Px
		pv.Side = domain.Buy
		if o.Side == "sell" {
			pv.Side = domain.Sell
		}
		sz, _ := decimal.NewFromString(o.Sz)
		px, _ := decimal.NewFromString(o.Px)
		pv.NotionalUSD, _ = sz.Mul(px).Float64()
	}
	return &Result{Card: CardOrder, Title: res.Title, Data: pv, Meta: res.Meta}, nil
}

func runPaperCancel(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*PaperCancelInput)
	id, err := paperID(p.Account)
	if err != nil {
		return nil, err
	}
	store, err := paperStore(env)
	if err != nil {
		return nil, err
	}
	coin := ""
	if p.Coin != "" {
		m, err := env.Resolve(ctx, p.Coin)
		if err != nil {
			return nil, err
		}
		coin = m.Coin
	}
	data := &PaperOrderData{Mode: ModePaper, Account: paperx.AddressPrefix + id}
	st, err := store.Update(id, nil, func(st *paperx.State) error {
		e := paperx.New(st, paperConfig(env))
		// fills that happened while detached come first: a cancel must not
		// undo an order that already filled
		sync, warn := syncPaper(ctx, env, e, e.Coins(), nil)
		data.Sync, data.Warnings = sync, warn
		ids, err := e.Cancel(coin, p.OID, env.nowMs())
		data.Canceled = ids
		return err
	})
	if errors.Is(err, paperx.ErrNoAccount) {
		return nil, paperNotFound(id)
	}
	var rej *paperx.Reject
	if errors.As(err, &rej) {
		return nil, &Error{Code: CodeNotFound, Message: rej.Msg, Details: map[string]string{"mode": ModePaper}}
	}
	if err != nil {
		return nil, err
	}
	data.Summary = paperx.New(st, paperConfig(env)).Summarize()
	res := &Result{Card: CardPaperOrder, Title: fmt.Sprintf("PAPER cancel · %d order(s)", len(data.Canceled)), Data: data, Meta: paperMeta(env)}
	res.Meta.Warnings = append(res.Meta.Warnings, data.Warnings...)
	return res, nil
}

func runPaperReset(_ context.Context, env *Env, in any) (*Result, error) {
	p := in.(*PaperResetInput)
	id, err := paperID(p.Account)
	if err != nil {
		return nil, err
	}
	store, err := paperStore(env)
	if err != nil {
		return nil, err
	}
	bal := paperx.DefaultBalance
	if p.Balance < 0 || p.Balance > 1e12 {
		return nil, Errorf(CodeUsage, "balance must be 0-1e12 USDC")
	}
	if p.Balance > 0 {
		bal = decimal.NewFromFloat(p.Balance).Round(6)
	}
	_, loadErr := store.Load(id)
	if loadErr == nil && !p.Yes {
		return nil, &Error{Code: CodeConfirmRequired, Message: "paper account " + id + " exists; reset wipes its positions, orders and history",
			Hint: "re-run with --yes", Details: map[string]string{"mode": ModePaper}}
	}
	st, err := store.Update(id, func() *paperx.State { return newPaperState(env, id, bal) }, func(st *paperx.State) error {
		*st = *newPaperState(env, id, bal)
		return nil
	})
	if err != nil {
		return nil, err
	}
	data := &PaperOrderData{Mode: ModePaper, Account: paperx.AddressPrefix + id, Summary: paperx.New(st, paperConfig(env)).Summarize()}
	return &Result{Card: CardPaperOrder, Title: fmt.Sprintf("PAPER reset %s · $%s", id, bal.StringFixed(2)), Data: data, Meta: paperMeta(env)}, nil
}

// loadPaper locks, syncs and saves account id, returning its engine.
func loadPaper(ctx context.Context, env *Env, id string) (*paperx.Engine, *PaperSync, []string, error) {
	store, err := paperStore(env)
	if err != nil {
		return nil, nil, nil, err
	}
	var sync *PaperSync
	var warn []string
	st, err := store.Update(id, nil, func(st *paperx.State) error {
		e := paperx.New(st, paperConfig(env))
		sync, warn = syncPaper(ctx, env, e, e.Coins(), nil)
		return nil
	})
	if errors.Is(err, paperx.ErrNoAccount) {
		return nil, nil, nil, paperNotFound(id)
	}
	if err != nil {
		return nil, nil, nil, err
	}
	return paperx.New(st, paperConfig(env)), sync, warn, nil
}

func runPaperAccount(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*PaperAccountInput)
	if strings.EqualFold(strings.TrimSpace(p.Account), "all") {
		store, err := paperStore(env)
		if err != nil {
			return nil, err
		}
		ids, err := store.List()
		if err != nil {
			return nil, err
		}
		s := paperx.Summary{Mode: ModePaper, ID: "all", Orders: []paperx.OrderView{}}
		s.FeedNote = "accounts: " + strings.Join(ids, ", ")
		return &Result{Card: CardPaperAccount, Title: "Paper accounts", Data: &s, Meta: paperMeta(env)}, nil
	}
	id, err := paperID(p.Account)
	if err != nil {
		return nil, err
	}
	e, _, warn, err := loadPaper(ctx, env, id)
	if err != nil {
		return nil, err
	}
	s := e.Summarize()
	s.FeedNote = "resting orders progress by queue and traded volume only while a feed is attached (paper order --wait-ms, `hlp account paper:" + id + " -w`, the TUI); in between they are replayed from 1m candles and fill only when the price trades through them"
	res := &Result{Card: CardPaperAccount, Title: "Paper account " + id, Data: &s, Meta: paperMeta(env)}
	res.Meta.Warnings = append(res.Meta.Warnings, warn...)
	return res, nil
}

// paperAccountResult serves `account paper:<id>`.
func paperAccountResult(ctx context.Context, env *Env, id string) (*Result, error) {
	e, _, warn, err := loadPaper(ctx, env, id)
	if err != nil {
		return nil, err
	}
	res := &Result{Card: CardAccount, Title: "Paper account " + id, Data: e.Account(), Meta: paperMeta(env)}
	res.Meta.Warnings = append(res.Meta.Warnings, warn...)
	return res, nil
}

// paperFillsResult serves `fills paper:<id>`.
func paperFillsResult(ctx context.Context, env *Env, id string, p *FillsInput, hours int) (*Result, error) {
	e, _, warn, err := loadPaper(ctx, env, id)
	if err != nil {
		return nil, err
	}
	coin := ""
	if p.Coin != "" {
		m, err := env.Resolve(ctx, p.Coin)
		if err != nil {
			return nil, err
		}
		coin = m.Coin
	}
	from := env.now().Add(-time.Duration(hours) * time.Hour).UnixMilli()
	var out []domain.Fill
	for _, f := range paperx.DomainFills(e.State().Fills) {
		if f.Time >= from && (coin == "" || f.Coin == coin) {
			out = append(out, f)
		}
	}
	if out == nil {
		out = []domain.Fill{}
	}
	res := &Result{Card: CardFills, Title: "Paper fills " + id, Data: out, Meta: paperMeta(env)}
	res.Meta.Warnings = append(res.Meta.Warnings, warn...)
	return res, nil
}

// streamPaperAccount follows a paper account live: the first snapshot,
// then one per flush while the WebSocket feed drives its orders.
func streamPaperAccount(ctx context.Context, env *Env, id string, emit func(*Result)) error {
	res, err := paperAccountResult(ctx, env, id)
	if err != nil {
		return err
	}
	emit(res)
	if env.Stream == nil {
		return nil
	}
	store, err := paperStore(env)
	if err != nil {
		return err
	}
	r := &paperx.Runner{Store: store, ID: id, Stream: env.Stream, Config: paperConfig(env), Now: env.now, Funding: env.HL.Funding,
		OnUpdate: func(e *paperx.Engine) {
			emit(&Result{Card: CardAccount, Title: "Paper account " + id, Data: e.Account(), Meta: paperMeta(env)})
		}}
	return r.Run(ctx)
}
