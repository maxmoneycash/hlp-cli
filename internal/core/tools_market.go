package core

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sahilm/fuzzy"

	"github.com/maxmoneycash/hlp/internal/chart"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hyperliquid"
	"github.com/maxmoneycash/hlp/internal/hyperliquid/ws"
)

// ---- markets ----

func runMarkets(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*MarketsInput)
	ms, meta, err := env.HL.Markets(ctx)
	if err != nil {
		return nil, err
	}
	switch p.Kind {
	case "", domain.Perp, domain.HIP3, domain.Spot, domain.Outcome:
	default:
		return nil, Errorf(CodeUsage, "kind must be perp, hip3, spot or outcome, got %q", p.Kind)
	}
	out := make([]domain.Market, 0, len(ms))
	for _, m := range ms {
		if m.Delisted {
			continue
		}
		if p.Kind == "" && m.Kind != domain.Perp && m.Kind != domain.HIP3 {
			continue
		}
		if p.Kind != "" && m.Kind != p.Kind {
			continue
		}
		if p.Dex != "" && !strings.EqualFold(m.Dex, p.Dex) {
			continue
		}
		out = append(out, m)
	}
	if q := strings.TrimSpace(p.Query); q != "" {
		out = fuzzyMarkets(out, q)
	}
	less, err := marketSort(p.Sort)
	if err != nil {
		return nil, err
	}
	if p.Query == "" || p.Sort != "" {
		sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j]) })
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}
	if p.Offset < 0 {
		return nil, Errorf(CodeUsage, "offset must be 0 or more, got %d", p.Offset)
	}
	total := len(out)
	out = out[min(p.Offset, len(out)):]
	if len(out) > limit {
		out = out[:limit]
	}
	meta = fillMeta(meta, env, "hyperliquid")
	meta.Total = &total
	meta.Truncated = p.Offset+len(out) < total
	for i := range out {
		out[i].Derive()
	}
	return &Result{Card: CardMarkets, Title: "Markets", Data: out, Meta: meta}, nil
}

// fuzzyMarkets keeps markets whose coin or display name matches q, best
// match first: exact (case-insensitive) coin or display hits, then prefix,
// then substring hits. Short queries (4 characters or fewer) stop there when
// anything matched, so "BTC" never pulls in TRB or BRETT through a
// subsequence match; otherwise a fuzzy subsequence match fills in.
func fuzzyMarkets(ms []domain.Market, q string) []domain.Market {
	keys := make([]string, len(ms))
	uq := strings.ToUpper(q)
	var exact, prefix, sub []domain.Market
	seen := map[int]bool{}
	for i, m := range ms {
		keys[i] = m.Coin + " " + m.Display
		uc, ud := strings.ToUpper(m.Coin), strings.ToUpper(m.Display)
		ub := strings.ToUpper(strings.TrimPrefix(m.Coin, m.Dex+":"))
		switch {
		case uc == uq || ud == uq || ub == uq:
			exact = append(exact, m)
		case strings.HasPrefix(uc, uq) || strings.HasPrefix(ud, uq) || strings.HasPrefix(ub, uq):
			prefix = append(prefix, m)
		case strings.Contains(uc, uq) || strings.Contains(ud, uq):
			sub = append(sub, m)
		default:
			continue
		}
		seen[i] = true
	}
	out := append(append(exact, prefix...), sub...)
	if len([]rune(q)) <= 4 && len(out) > 0 {
		return out
	}
	for _, match := range fuzzy.Find(q, keys) {
		if !seen[match.Index] {
			out = append(out, ms[match.Index])
		}
	}
	return out
}

func marketSort(key string) (func(a, b domain.Market) bool, error) {
	switch strings.ToLower(key) {
	case "", "volume", "vol":
		return func(a, b domain.Market) bool { return a.DayNtlVlm > b.DayNtlVlm }, nil
	case "change":
		return func(a, b domain.Market) bool { return a.ComputeChange24h() > b.ComputeChange24h() }, nil
	case "funding":
		return func(a, b domain.Market) bool { return a.Funding > b.Funding }, nil
	case "oi":
		return func(a, b domain.Market) bool { return a.OpenInterest*a.Mark > b.OpenInterest*b.Mark }, nil
	case "name":
		return func(a, b domain.Market) bool { return strings.ToLower(a.Display) < strings.ToLower(b.Display) }, nil
	}
	return nil, Errorf(CodeUsage, "sort must be volume, change, funding, oi or name, got %q", key)
}

// fillMeta completes a meta returned by a client.
func fillMeta(m domain.Meta, env *Env, source string) domain.Meta {
	if m.Source == "" {
		m.Source = source
	}
	if m.AsOf.IsZero() {
		m.AsOf = env.now()
	}
	if m.Network == "" {
		m.Network = env.Config.Network
	}
	return m
}

// ---- chart ----

type chartParams struct {
	market  domain.Market
	iv      domain.Interval
	bars    int
	width   int
	height  int
	style   chart.Style
	zoom    int
	mas     []int
	emas    []int
	lines   []chart.HLine
	address string

	// render hints for the chart card: what the caller asked for explicitly
	// (ChartData.Style/Zoom/Height), zero when it relied on defaults.
	hintStyle  string
	hintZoom   int
	hintHeight int
}

func chartSetup(ctx context.Context, env *Env, p *ChartInput) (*chartParams, error) {
	m, err := env.Resolve(ctx, p.Coin)
	if err != nil {
		return nil, err
	}
	cp := &chartParams{market: m, iv: p.Interval, width: p.Width, height: p.Height, zoom: p.Zoom,
		hintStyle: p.Style, hintZoom: p.Zoom, hintHeight: p.Height}
	if cp.iv == "" {
		cp.iv = env.Config.Chart.Interval
	}
	if cp.iv == "" {
		cp.iv = domain.I15m
	}
	if !cp.iv.Valid() {
		return nil, &Error{Code: CodeUsage, Message: fmt.Sprintf("interval %q is not supported", cp.iv), Hint: "use one of " + intervalList()}
	}
	if cp.width <= 0 {
		cp.width = 100
	}
	if cp.height <= 0 {
		cp.height = 30
	}
	if cp.width < 20 || cp.width > 1000 || cp.height < 4 || cp.height > 400 {
		return nil, Errorf(CodeUsage, "chart size %dx%d is out of range (20-1000 x 4-400)", cp.width, cp.height)
	}
	if cp.zoom == 0 {
		cp.zoom = env.Config.Chart.Zoom
	}
	switch cp.zoom {
	case 0:
		cp.zoom = 2
	case 1, 2, 4, 6:
	default:
		return nil, Errorf(CodeUsage, "zoom must be 1, 2, 4 or 6 columns per candle, got %d", cp.zoom)
	}
	style := p.Style
	if style == "" {
		style = env.Config.Chart.Style
	}
	st, ok := chart.ParseStyle(style)
	if !ok {
		names := make([]string, len(chart.Styles))
		for i, s := range chart.Styles {
			names[i] = string(s)
		}
		return nil, Errorf(CodeUsage, "style must be one of %s, got %q", strings.Join(names, ", "), style)
	}
	cp.style = st
	cp.mas, cp.emas = p.MA, p.EMA
	if p.MA == nil && p.EMA == nil {
		cp.mas = env.Config.Chart.MAs
	}
	for _, n := range append(append([]int{}, cp.mas...), cp.emas...) {
		if n < 1 || n > 1000 {
			return nil, Errorf(CodeUsage, "moving-average periods must be 1-1000, got %d", n)
		}
	}
	if len(cp.mas)+len(cp.emas) > 3 {
		return nil, Errorf(CodeUsage, "at most 3 moving averages fit on a chart")
	}
	warm := 0
	for _, n := range append(append([]int{}, cp.mas...), cp.emas...) {
		warm = max(warm, n)
	}
	cp.bars = p.Bars
	if cp.bars <= 0 {
		cp.bars = cp.width/cp.zoom + warm
	}
	if cp.bars < 2 || cp.bars > 5000 {
		return nil, Errorf(CodeUsage, "bars must be 2-5000, got %d", cp.bars)
	}
	if p.Address != "" {
		addr, err := env.address(p.Address)
		if err != nil {
			return nil, err
		}
		cp.address = addr
	}
	return cp, nil
}

func intervalList() string {
	s := make([]string, len(domain.Intervals))
	for i, iv := range domain.Intervals {
		s[i] = string(iv)
	}
	return strings.Join(s, " ")
}

// accountLines turns an account's position and resting orders on coin into
// chart levels (entry, liquidation, orders, TP/SL triggers).
func accountLines(a domain.Account, coin string) []chart.HLine {
	var out []chart.HLine
	for _, p := range a.Positions {
		if p.Coin != coin || p.Size == 0 {
			continue
		}
		side := "long"
		if p.Size < 0 {
			side = "short"
		}
		out = append(out, chart.HLine{Price: p.Entry, Label: "entry " + side, Kind: chart.LineEntry})
		if p.LiqPx > 0 {
			out = append(out, chart.HLine{Price: p.LiqPx, Label: "liq", Kind: chart.LineLiq})
		}
	}
	for _, o := range a.OpenOrders {
		if o.Coin != coin {
			continue
		}
		px, kind, label := o.Px, chart.LineOrder, "order"
		t := strings.ToLower(o.Type)
		switch {
		case strings.Contains(t, "take profit"):
			px, kind, label = nz(o.TriggerPx, o.Px), chart.LineTP, "tp"
		case strings.Contains(t, "stop"):
			px, kind, label = nz(o.TriggerPx, o.Px), chart.LineStop, "stop"
		case o.Side == domain.Buy:
			label = "bid"
		default:
			label = "ask"
		}
		out = append(out, chart.HLine{Price: px, Label: label, Kind: kind})
	}
	return out
}

func nz(a, b float64) float64 {
	if a != 0 {
		return a
	}
	return b
}

// marketCtxer is implemented by readers that can fetch one market's live
// context cheaply (hyperliquid.Client.MarketCtx).
type marketCtxer interface {
	MarketCtx(ctx context.Context, coin string) (domain.Market, error)
}

func runChart(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*ChartInput)
	cp, err := chartSetup(ctx, env, p)
	if err != nil {
		return nil, err
	}
	// The market context (24h change, funding, OI) is a cheap best-effort
	// read that runs alongside the candle request.
	mctx := make(chan domain.Market, 1)
	go func() {
		m := cp.market
		if mc, ok := env.HL.(marketCtxer); ok {
			if x, err := mc.MarketCtx(ctx, cp.market.Coin); err == nil {
				m = x
			}
		}
		mctx <- m
	}()
	cs, err := env.HL.Candles(ctx, cp.market.Coin, cp.iv, cp.bars, 0)
	if err != nil {
		return nil, err
	}
	if len(cs) == 0 {
		return nil, &Error{Code: CodeNotFound, Message: "no candles for " + cp.market.Coin + " " + string(cp.iv), Hint: "new or delisted markets may have no history at this interval"}
	}
	cp.market = <-mctx
	meta := env.meta("hyperliquid")
	if cp.address != "" {
		if a, aerr := env.HL.Account(ctx, cp.address); aerr == nil {
			cp.lines = accountLines(a, cp.market.Coin)
			if len(cp.lines) == 0 {
				meta.Warnings = append(meta.Warnings, fmt.Sprintf("no %s position or open orders for %s (vaults like HLP trade through child vaults)", cp.market.Coin, shortAddr(cp.address)))
			}
		} else {
			meta.Partial = true
			meta.Warnings = append(meta.Warnings, "account overlay unavailable: "+AsError(aerr).Message)
		}
	}
	return chartResult(env, cp, cs, meta), nil
}

func chartResult(env *Env, cp *chartParams, cs []domain.Candle, meta domain.Meta) *Result {
	loc := time.Local
	if env.Config.Chart.UTC || cp.iv.Duration() >= 24*time.Hour {
		loc = time.UTC
	}
	opts := chart.CandleOpts{
		Width: cp.width, Height: cp.height, Style: cp.style, Zoom: cp.zoom, VolRows: -1,
		VolumeUSD: env.Config.Chart.VolumeUSD, MAs: cp.mas, EMAs: cp.emas, Lines: cp.lines,
		Cursor: -1, Header: true, Coin: cp.market.Display, Interval: cp.iv, Location: loc,
		Skin: env.Config.Chart.Theme,
	}
	if opts.Coin == "" {
		opts.Coin = cp.market.Coin
	}
	g, _ := chart.Candles(cs, opts, env.Theme)
	data := &ChartData{
		Coin:     cp.market.Coin,
		Interval: cp.iv,
		Market:   cp.market,
		Candles:  cs,
		MAs:      cp.mas,
		EMAs:     cp.emas,
		Lines:    cp.lines,
		Summary:  Summarize(cs, cp.mas, cp.emas),
	}
	if cp.market.PrevDay > 0 && cp.market.Mark > 0 {
		data.Summary.Change24h = cp.market.ComputeChange24h()
	}
	if g != nil {
		data.Plain = g.Plain()
	}
	data.Style, data.Zoom, data.Height = cp.hintStyle, cp.hintZoom, cp.hintHeight
	return &Result{Card: CardChart, Title: fmt.Sprintf("%s %s", opts.Coin, cp.iv), Data: data, Meta: meta}
}

// Summarize is the numeric digest of a candle window that agents read
// instead of pixels: last, window open/high/low, change over the window as a
// FRACTION (0.012 = +1.2%, per PLAN D20 despite the field name) and MA
// values (keys "sma20", "ema50").
func Summarize(cs []domain.Candle, mas, emas []int) ChartSummary {
	s := ChartSummary{MA: map[string]float64{}}
	if len(cs) == 0 {
		return s
	}
	s.Open, s.Last = cs[0].O, cs[len(cs)-1].C
	s.High, s.Low = cs[0].H, cs[0].L
	for _, c := range cs {
		s.High = math.Max(s.High, c.H)
		s.Low = math.Min(s.Low, c.L)
	}
	if s.Open != 0 {
		s.Change = s.Last/s.Open - 1 // a fraction, like every rate (PLAN D20)
	}
	closes := make([]float64, len(cs))
	for i, c := range cs {
		closes[i] = c.C
	}
	for _, n := range mas {
		if v, ok := lastSMA(closes, n); ok {
			s.MA["sma"+strconv.Itoa(n)] = v
		}
	}
	for _, n := range emas {
		if v, ok := lastEMA(closes, n); ok {
			s.MA["ema"+strconv.Itoa(n)] = v
		}
	}
	return s
}

func lastSMA(x []float64, n int) (float64, bool) {
	if n <= 0 || len(x) < n {
		return 0, false
	}
	sum := 0.0
	for _, v := range x[len(x)-n:] {
		sum += v
	}
	return sum / float64(n), true
}

// lastEMA seeds with the SMA of the first n values (TradingView convention).
func lastEMA(x []float64, n int) (float64, bool) {
	if n <= 0 || len(x) < n {
		return 0, false
	}
	e, _ := lastSMA(x[:n], n)
	k := 2 / float64(n+1)
	for _, v := range x[n:] {
		e = v*k + e*(1-k)
	}
	return e, true
}

func streamChart(ctx context.Context, env *Env, in any, emit func(*Result)) error {
	p := in.(*ChartInput)
	cp, err := chartSetup(ctx, env, p)
	if err != nil {
		return err
	}
	res, seeded := SeedFrom(ctx)
	if seeded && !chartSeedFresh(res, cp, env.now().UnixMilli()) {
		seeded = false
	}
	if !seeded {
		if res, err = runChart(ctx, env, in); err != nil {
			return err
		}
		emit(res)
	}
	if env.Stream == nil {
		return nil
	}
	data := res.Data.(*ChartData)
	cs := append([]domain.Candle(nil), data.Candles...)
	ch, cancel, err := ws.Candles(ctx, env.Stream, cp.market.Coin, cp.iv)
	if err != nil {
		return err
	}
	defer cancel()
	cp.market, cp.lines = data.Market, data.Lines
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case c, ok := <-ch:
			if !ok {
				return nil
			}
			cs = MergeCandle(cs, c, cp.bars)
			if cp.market.Mark > 0 && c.C > 0 {
				cp.market.Mark = c.C // keeps the 24h change live
			}
			emit(chartResult(env, cp, cs, env.meta("hyperliquid-ws")))
		}
	}
}

// chartSeedFresh accepts a seed for this chart only when it is the same
// coin and interval and its newest bar is within one interval of now, so a
// stale seed never leaves a gap the WebSocket will not fill.
func chartSeedFresh(r *Result, cp *chartParams, nowMs int64) bool {
	d, ok := r.Data.(*ChartData)
	if !ok || r.Card != CardChart || d == nil || len(d.Candles) == 0 {
		return false
	}
	if d.Coin != cp.market.Coin || d.Interval != cp.iv {
		return false
	}
	last := d.Candles[len(d.Candles)-1].T
	return nowMs-last <= cp.iv.Duration().Milliseconds()
}

// MergeCandle applies a live bar: replace when the open time matches the
// newest bar, append when newer (trimming to max bars), ignore when older.
// cs must be owned by the caller (copy it once before the first merge): the
// open bar is replaced in place. A new bar at maxBars appends into spare
// capacity and slides the window; only when the array is exhausted does it
// copy, once, into a fresh array with maxBars of headroom. Bars already
// handed out are never shifted, and a tick costs no copy of the history.
func MergeCandle(cs []domain.Candle, c domain.Candle, maxBars int) []domain.Candle {
	n := len(cs)
	switch {
	case n > 0 && cs[n-1].T == c.T:
		cs[n-1] = c
	case n == 0 || c.T > cs[n-1].T:
		if maxBars <= 0 || n < maxBars {
			return append(cs, c)
		}
		if cap(cs) > n {
			cs = append(cs, c)
			return cs[len(cs)-maxBars:]
		}
		nb := make([]domain.Candle, maxBars, 2*maxBars)
		copy(nb, cs[n-maxBars+1:])
		nb[maxBars-1] = c
		return nb
	}
	return cs
}

// ---- book ----

func bookSetup(ctx context.Context, env *Env, p *BookInput) (domain.Market, int, error) {
	m, err := env.Resolve(ctx, p.Coin)
	if err != nil {
		return m, 0, err
	}
	depth := p.Depth
	if depth == 0 {
		depth = 10
	}
	if depth < 1 || depth > 20 {
		return m, 0, Errorf(CodeUsage, "depth must be 1-20 levels per side, got %d", depth)
	}
	if p.NSigFigs != 0 && (p.NSigFigs < 2 || p.NSigFigs > 5) {
		return m, 0, Errorf(CodeUsage, "sigfigs must be 2-5 (or 0 for full precision), got %d", p.NSigFigs)
	}
	return m, depth, nil
}

func seedBookFor(r *Result, coin string) bool {
	switch b := r.Data.(type) {
	case domain.Book:
		return b.Coin == coin
	case *domain.Book:
		return b != nil && b.Coin == coin
	}
	return false
}

// TrimBook keeps at most depth levels per side.
func TrimBook(b domain.Book, depth int) domain.Book {
	if len(b.Bids) > depth {
		b.Bids = b.Bids[:depth]
	}
	if len(b.Asks) > depth {
		b.Asks = b.Asks[:depth]
	}
	return b
}

func runBook(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*BookInput)
	m, depth, err := bookSetup(ctx, env, p)
	if err != nil {
		return nil, err
	}
	b, err := env.HL.Book(ctx, m.Coin, hyperliquid.BookOpts{NSigFigs: p.NSigFigs})
	if err != nil {
		return nil, err
	}
	if b.Coin == "" {
		b.Coin = m.Coin
	}
	return &Result{Card: CardBook, Title: m.Display + " book", Data: TrimBook(b, depth), Meta: env.meta("hyperliquid")}, nil
}

func streamBook(ctx context.Context, env *Env, in any, emit func(*Result)) error {
	p := in.(*BookInput)
	m, depth, err := bookSetup(ctx, env, p)
	if err != nil {
		return err
	}
	if r, ok := SeedFrom(ctx); !ok || r.Card != CardBook || !seedBookFor(r, m.Coin) {
		res, err := runBook(ctx, env, in)
		if err != nil {
			return err
		}
		emit(res)
	}
	if env.Stream == nil {
		return nil
	}
	ch, cancel, err := ws.BooksDepth(ctx, env.Stream, m.Coin, p.NSigFigs, depth)
	if err != nil {
		return err
	}
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case b, ok := <-ch:
			if !ok {
				return nil
			}
			emit(&Result{Card: CardBook, Title: m.Display + " book", Data: TrimBook(b, depth), Meta: env.meta("hyperliquid-ws")})
		}
	}
}

// ---- trades ----

func runTrades(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*CoinInput)
	m, err := env.Resolve(ctx, p.Coin)
	if err != nil {
		return nil, err
	}
	ts, err := env.HL.Trades(ctx, m.Coin)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].Time > ts[j].Time })
	if p.Limit < 0 {
		return nil, Errorf(CodeUsage, "limit must be positive, got %d", p.Limit)
	}
	meta := env.meta("hyperliquid")
	if p.Limit > 0 && len(ts) > p.Limit {
		total := len(ts)
		meta.Total, meta.Truncated = &total, true
		ts = ts[:p.Limit]
	}
	return &Result{Card: CardTrades, Title: m.Display + " trades", Data: ts, Meta: meta}, nil
}

const tapeLen = 50

func streamTrades(ctx context.Context, env *Env, in any, emit func(*Result)) error {
	p := in.(*CoinInput)
	m, err := env.Resolve(ctx, p.Coin)
	if err != nil {
		return err
	}
	var tape []domain.Trade
	seeded := false
	if r, ok := SeedFrom(ctx); ok && r.Card == CardTrades {
		if ts, ok := r.Data.([]domain.Trade); ok && (len(ts) == 0 || ts[0].Coin == m.Coin) {
			tape, seeded = append([]domain.Trade(nil), ts...), true
		}
	}
	if !seeded {
		res, err := runTrades(ctx, env, in)
		if err != nil {
			return err
		}
		emit(res)
		tape = res.Data.([]domain.Trade)
	}
	if env.Stream == nil {
		return nil
	}
	if p.Batches {
		ch, cancel, err := ws.TradeBatchesWithStatus(ctx, env.Stream, m.Coin)
		if err != nil {
			return err
		}
		defer cancel()
		for batch := range ch {
			meta := env.meta("hyperliquid-ws")
			if batch.State != "" {
				batch.Trades = []domain.Trade{}
				if batch.State == ws.StateReconnecting {
					meta.Stale = true
					meta.Warnings = []string{"trade subscription disconnected"}
				}
			}
			emit(&Result{Card: CardTrades, Title: m.Display + " trades", Data: batch.Trades, Meta: meta})
		}
		return ctx.Err()
	}
	n := tapeLen
	if p.Limit > 0 {
		n = p.Limit
	}
	ch, cancel, err := ws.Trades(ctx, env.Stream, m.Coin)
	if err != nil {
		return err
	}
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case batch, ok := <-ch:
			if !ok {
				return nil
			}
			tape = PrependTrades(tape, batch, n)
			emit(&Result{Card: CardTrades, Title: m.Display + " trades", Data: tape, Meta: env.meta("hyperliquid-ws")})
		}
	}
}

// PrependTrades puts new prints first (newest first), dedupes by tid and
// keeps at most n.
func PrependTrades(tape, batch []domain.Trade, n int) []domain.Trade {
	seen := make(map[uint64]bool, len(tape))
	for _, t := range tape {
		if t.TID != 0 {
			seen[t.TID] = true
		}
	}
	fresh := make([]domain.Trade, 0, len(batch))
	for _, t := range batch {
		if t.TID != 0 && seen[t.TID] {
			continue
		}
		fresh = append(fresh, t)
	}
	sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].Time > fresh[j].Time })
	out := append(fresh, tape...)
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// ---- funding ----

func runFunding(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*FundingInput)
	if p.Coin != "" {
		m, err := env.Resolve(ctx, p.Coin)
		if err != nil {
			return nil, err
		}
		if m.Kind == domain.Spot {
			return nil, Errorf(CodeUsage, "%s is a spot market; funding exists only on perps", m.Display)
		}
		hours := p.Hours
		if hours == 0 {
			hours = 72
		}
		if hours < 1 || hours > 24*365 {
			return nil, Errorf(CodeUsage, "hours must be 1-8760, got %d", hours)
		}
		end := env.now()
		pts, err := env.HL.Funding(ctx, m.Coin, end.Add(-time.Duration(hours)*time.Hour).UnixMilli(), end.UnixMilli())
		if err != nil {
			return nil, err
		}
		return &Result{Card: CardFunding, Title: m.Display + " funding", Data: pts, Meta: env.meta("hyperliquid"), Tail: true}, nil
	}
	top := p.Top
	if top == 0 {
		top = 20
	}
	if top < 1 || top > 200 {
		return nil, Errorf(CodeUsage, "top must be 1-200, got %d", top)
	}
	ms, meta, err := env.HL.Markets(ctx)
	if err != nil {
		return nil, err
	}
	return &Result{Card: CardFunding, Title: "Funding", Data: FundingRanking(ms, top), Meta: fillMeta(meta, env, "hyperliquid")}, nil
}

// FundingRanking returns the top most-positive funding perps (highest first)
// followed by the top most-negative (lowest first). Spot, delisted and
// zero-volume markets are skipped.
func FundingRanking(ms []domain.Market, top int) []domain.Market {
	var perps []domain.Market
	for _, m := range ms {
		if (m.Kind == domain.Perp || m.Kind == domain.HIP3) && !m.Delisted && m.DayNtlVlm > 0 {
			perps = append(perps, m)
		}
	}
	sort.SliceStable(perps, func(i, j int) bool { return perps[i].Funding > perps[j].Funding })
	var pos, neg []domain.Market
	for _, m := range perps {
		if m.Funding > 0 && len(pos) < top {
			pos = append(pos, m)
		}
	}
	for i := len(perps) - 1; i >= 0 && len(neg) < top; i-- {
		if perps[i].Funding < 0 {
			neg = append(neg, perps[i])
		}
	}
	return append(pos, neg...)
}

// ---- asset-id ----

func runAssetID(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*AssetIDInput)
	m, err := env.Resolve(ctx, p.Coin)
	if err != nil {
		return nil, err
	}
	// Typed fields for agents (AGENT-UX #11); the KV card renders Pairs().
	return &Result{Card: CardKV, Title: m.Display, Data: AssetIDFromMarket(m), Meta: env.meta("hyperliquid")}, nil
}
