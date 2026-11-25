package core

import (
	"context"
	"strings"
	"sync"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hyperliquid"
	"github.com/maxmoneycash/hlp/internal/hyperliquid/ws"
)

// PriceInput is the input of get_price (`hlp price`).
type PriceInput struct {
	Coins []string `json:"coins" jsonschema:"one or more markets, e.g. BTC ETH xyz:SP500"`
	Live  bool     `json:"live,omitempty" jsonschema:"stream updates (CLI/TUI; MCP streams are bounded)"`
}

// PriceRow is one coin of get_price. Fractions as everywhere (change24h,
// hourly funding); bid/ask are the top of book.
type PriceRow struct {
	Coin         string   `json:"coin"`
	Mid          float64  `json:"mid"`
	Mark         float64  `json:"mark"`
	Oracle       float64  `json:"oracle,omitempty"`
	Bid          float64  `json:"bid,omitempty"`
	Ask          float64  `json:"ask,omitempty"`
	Change24h    float64  `json:"change24h"`
	Funding      float64  `json:"funding,omitempty"`
	PrevDay      float64  `json:"prevDay,omitempty"`      // rolling 24h reference price
	OpenInterest *float64 `json:"openInterest,omitempty"` // base units; absent for spot
	DayNtlVlm    *float64 `json:"dayNtlVlm,omitempty"`    // rolling 24h USD notional, not candle volume
	ContextTime  int64    `json:"contextTime"`            // when this engine observed the context, ms; not an exchange event timestamp
}

// maxPriceCoins bounds one get_price call (each coin costs one l2Book read).
const maxPriceCoins = 20

// priceTools registers the quote tools.
func priceTools() []Tool {
	return []Tool{
		{Name: "get_price", Command: "price", Slash: "price", Aliases: []string{"mid", "quote"}, Positional: []string{"coins"}, Group: "market", Streaming: true, NewInput: in[PriceInput](), Run: runPrice, Stream: streamPrice,
			Description: "Mid, mark, oracle, best bid/ask, 24h change (fraction) and hourly funding (fraction) for one or more markets, one row per coin in input order. The cheapest way to read a price: no candles.",
			Summary:     "Current price of one or more markets (mid, bid/ask, 24h change, funding)",
			Examples:    []string{"hlp price btc", "hlp price btc eth hype", "hlp price btc --select mid -q", "hlp price btc -w"}},
	}
}

// perpCtxer reads one perp dex's contexts in one request
// (hyperliquid.Client.MetaAndAssetCtxs).
type perpCtxer interface {
	MetaAndAssetCtxs(ctx context.Context, dex string) (hyperliquid.Meta, []hyperliquid.AssetCtx, error)
}

func priceSetup(ctx context.Context, env *Env, p *PriceInput) ([]domain.Market, error) {
	var coins []string
	for _, c := range p.Coins {
		for _, f := range strings.FieldsFunc(c, func(r rune) bool { return r == ',' || r == ' ' }) {
			coins = append(coins, f)
		}
	}
	if len(coins) == 0 {
		return nil, &Error{Code: CodeUsage, Message: "price needs at least one coin", Hint: "hlp price btc eth"}
	}
	if len(coins) > maxPriceCoins {
		return nil, Errorf(CodeUsage, "price takes at most %d coins, got %d", maxPriceCoins, len(coins))
	}
	ms := make([]domain.Market, 0, len(coins))
	seen := map[string]bool{}
	for _, c := range coins {
		m, err := env.Resolve(ctx, c)
		if err != nil {
			return nil, err
		}
		if seen[m.Coin] {
			continue
		}
		seen[m.Coin] = true
		ms = append(ms, m)
	}
	return ms, nil
}

func runPrice(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*PriceInput)
	ms, err := priceSetup(ctx, env, p)
	if err != nil {
		return nil, err
	}
	ms, err = priceContexts(ctx, env, ms)
	if err != nil {
		return nil, err
	}
	contextTime := env.now().UnixMilli()
	rows := make([]PriceRow, len(ms))
	books := make([]domain.Book, len(ms))
	berrs := make([]error, len(ms))
	parallel(len(ms), func(i int) {
		books[i], berrs[i] = env.HL.Book(ctx, ms[i].Coin, hyperliquid.BookOpts{})
	})
	meta := env.meta("hyperliquid")
	for i, m := range ms {
		rows[i] = priceRow(m, books[i])
		rows[i].ContextTime = contextTime
		if berrs[i] != nil && ctx.Err() == nil {
			meta.Partial = true
			meta.Warnings = append(meta.Warnings, "top of book unavailable for "+m.Coin+": "+AsError(berrs[i]).Message)
		}
	}
	return &Result{Card: CardPrice, Title: priceTitle(ms), Data: rows, Meta: meta}, nil
}

func priceTitle(ms []domain.Market) string {
	if len(ms) == 1 {
		if ms[0].Display != "" {
			return ms[0].Display
		}
		return ms[0].Coin
	}
	return "Prices"
}

// priceContexts fills mark, mid, oracle, prevDay and funding: one
// metaAndAssetCtxs per perp dex involved when the reader exposes it,
// MarketCtx for spot, else the (cached) full market list.
func priceContexts(ctx context.Context, env *Env, ms []domain.Market) ([]domain.Market, error) {
	out := append([]domain.Market(nil), ms...)
	pc, okPerp := env.HL.(perpCtxer)
	mc, okOne := env.HL.(marketCtxer)
	if !okPerp || !okOne {
		all, _, err := env.HL.Markets(ctx)
		if err != nil {
			return nil, err
		}
		by := make(map[string]domain.Market, len(all))
		for _, m := range all {
			by[m.Coin] = m
		}
		for i, m := range out {
			if x, ok := by[m.Coin]; ok {
				out[i] = x
			}
		}
		return out, nil
	}
	dexes := map[string][]int{}
	for i, m := range out {
		if m.Kind == domain.Spot || m.Kind == domain.Outcome {
			x, err := mc.MarketCtx(ctx, m.Coin)
			if err != nil {
				return nil, err
			}
			out[i] = x
			continue
		}
		dexes[m.Dex] = append(dexes[m.Dex], i)
	}
	var mu sync.Mutex
	var firstErr error
	keys := make([]string, 0, len(dexes))
	for d := range dexes {
		keys = append(keys, d)
	}
	parallel(len(keys), func(k int) {
		dex := keys[k]
		meta, cx, err := pc.MetaAndAssetCtxs(ctx, dex)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return
		}
		for _, i := range dexes[dex] {
			for j, a := range meta.Universe {
				if a.Name != out[i].Coin || j >= len(cx) {
					continue
				}
				x := cx[j]
				out[i].Mark, out[i].Mid, out[i].Oracle = x.MarkPx.Float(), x.MidPx.Float(), x.OraclePx.Float()
				out[i].PrevDay, out[i].Funding = x.PrevDayPx.Float(), x.Funding.Float()
				out[i].OpenInterest, out[i].DayNtlVlm = x.OpenInterest.Float(), x.DayNtlVlm.Float()
				break
			}
		}
	})
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// priceRow merges a market context with its top of book. Mid falls back to
// the book midpoint, then to the mark.
func priceRow(m domain.Market, b domain.Book) PriceRow {
	volume := m.DayNtlVlm
	r := PriceRow{Coin: m.Coin, Mark: m.Mark, Mid: m.Mid, Oracle: m.Oracle, Change24h: m.ComputeChange24h(), PrevDay: m.PrevDay, DayNtlVlm: &volume}
	if m.Kind != domain.Spot {
		r.Funding = m.Funding
		oi := m.OpenInterest
		r.OpenInterest = &oi
	}
	if len(b.Bids) > 0 {
		r.Bid = b.Bids[0].Px
	}
	if len(b.Asks) > 0 {
		r.Ask = b.Asks[0].Px
	}
	if r.Mid == 0 {
		r.Mid = b.Mid()
	}
	if r.Mid == 0 {
		r.Mid = r.Mark
	}
	return r
}

// streamPrice emits the snapshot (or the seed), then follows each coin's
// best bid/offer and asset context over WebSocket; mid follows the BBO
// midpoint while mark, oracle, funding and day change follow the context.
func streamPrice(ctx context.Context, env *Env, in any, emit func(*Result)) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	p := in.(*PriceInput)
	res, ok := SeedFrom(ctx)
	if !ok || res.Card != CardPrice {
		var err error
		if res, err = runPrice(ctx, env, in); err != nil {
			return err
		}
	}
	emit(res)
	if env.Stream == nil {
		return nil
	}
	ms, err := priceSetup(ctx, env, p)
	if err != nil {
		return err
	}
	rows := append([]PriceRow(nil), res.Data.([]PriceRow)...)
	type tick struct {
		i     int
		b     domain.Book
		asset *hyperliquid.AssetCtx
	}
	ticks := make(chan tick, 16)
	closed := make(chan string, 2*len(ms))
	for i, m := range ms {
		ch, cancel, err := ws.BBO(ctx, env.Stream, m.Coin)
		if err != nil {
			return err
		}
		defer cancel()
		go func(i int, ch <-chan domain.Book) {
			defer func() { closed <- ms[i].Coin + " best bid/offer" }()
			for b := range ch {
				select {
				case ticks <- tick{i: i, b: b}:
				case <-ctx.Done():
					return
				}
			}
		}(i, ch)
		contexts, cancelContext, err := ws.AssetContexts(ctx, env.Stream, m.Coin)
		if err != nil {
			return err
		}
		defer cancelContext()
		go func(i int, ch <-chan hyperliquid.AssetCtx) {
			defer func() { closed <- ms[i].Coin + " asset context" }()
			for asset := range ch {
				select {
				case ticks <- tick{i: i, asset: &asset}:
				case <-ctx.Done():
					return
				}
			}
		}(i, contexts)
	}
	title := priceTitle(ms)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case name := <-closed:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &Error{Code: CodeNetwork, Message: name + " stream closed", Retryable: true}
		case t := <-ticks:
			if t.i >= len(rows) {
				continue
			}
			r := &rows[t.i]
			if t.asset != nil {
				x := t.asset
				r.ContextTime = env.now().UnixMilli()
				r.Mark = x.MarkPx.Float()
				if x.OraclePx != "" {
					r.Oracle = x.OraclePx.Float()
				}
				if x.Funding != "" {
					r.Funding = x.Funding.Float()
				}
				if previous := x.PrevDayPx.Float(); previous > 0 {
					r.PrevDay = previous
					r.Change24h = (r.Mark - previous) / previous
				}
				if x.OpenInterest != "" {
					v := x.OpenInterest.Float()
					r.OpenInterest = &v
				}
				if x.DayNtlVlm != "" {
					v := x.DayNtlVlm.Float()
					r.DayNtlVlm = &v
				}
			}
			if len(t.b.Bids) > 0 {
				r.Bid = t.b.Bids[0].Px
			}
			if len(t.b.Asks) > 0 {
				r.Ask = t.b.Asks[0].Px
			}
			if mid := t.b.Mid(); mid > 0 {
				r.Mid = mid
			}
			emit(&Result{Card: CardPrice, Title: title, Data: append([]PriceRow(nil), rows...), Meta: env.meta("hyperliquid-ws")})
		}
	}
}
