package core

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/chart"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/hyperliquid/ws"
	"github.com/maxmoneycash/hlp-cli/internal/ticks"
)

// CardTicks renders tick, volume and range bars. Data: *TickChartData.
const CardTicks Card = "ticks"

// TickChartInput is the input of get_tick_candles (`hlp candles`).
type TickChartInput struct {
	Coin       string  `json:"coin" jsonschema:"market, e.g. BTC, HYPE, xyz:SP500, @107; case-insensitive"`
	Ticks      int     `json:"ticks,omitempty" jsonschema:"tick bars: trades per bar, e.g. 100, 250, 500, 1000, 5000 (the default is 500 when no bar type is given)"`
	VolumeBars float64 `json:"volumeBars,omitempty" jsonschema:"volume bars: base units (contracts) per bar, e.g. 10 for 10 BTC"`
	USDBars    float64 `json:"usdBars,omitempty" jsonschema:"volume bars by USD notional per bar, e.g. 1000000 (1m also works)"`
	RangeBars  float64 `json:"rangeBars,omitempty" jsonschema:"range bars: a bar closes once high minus low reaches this price distance (quote units, e.g. 50 for BTC)"`
	Bars       int     `json:"bars,omitempty" jsonschema:"bars to return, newest last (default fits the width on a terminal, else 300; max 5000)"`
	Width      int     `json:"width,omitempty" jsonschema:"columns; default terminal width or 100"`
	Height     int     `json:"height,omitempty" jsonschema:"chart rows; default 30"`
	Style      string  `json:"style,omitempty" jsonschema:"candle style, as for get_candles"`
	Zoom       int     `json:"zoom,omitempty" jsonschema:"columns per bar: 1, 2, 4, 6"`
	Live       bool    `json:"live,omitempty" jsonschema:"stream bar updates and closed bars (CLI --live/--ndjson; MCP streams are bounded)"`
}

// TickChartData is the payload of get_tick_candles. A snapshot (event
// "snapshot") carries the newest bars and replaces the window; each live
// update (event "update") carries only the bars that changed: bars it
// closed, then the open bar. Upsert by i. A stream sends a new snapshot
// when a reconnect brings trades older than the newest bar.
type TickChartData struct {
	Coin    string        `json:"coin"`
	Bar     ticks.Spec    `json:"bar"`
	Label   string        `json:"label"` // 500T, 10 BTC, $1M, R50
	Market  domain.Market `json:"market"`
	Event   string        `json:"event"` // snapshot | update
	Bars    []ticks.Bar   `json:"bars"`
	Summary ChartSummary  `json:"summary"`
	History TickHistory   `json:"history"`
	Plain   string        `json:"plain,omitempty"` // snapshot only: plain-text chart

	// All is the whole window (the CLI draws it on every update); never in JSON.
	All []ticks.Bar `json:"-"`
	// Render hints from the input; never in JSON.
	Style  string `json:"-"`
	Zoom   int    `json:"-"`
	Height int    `json:"-"`
}

// TickHistory says how much trade history backs the bars. It is local
// only: Hyperliquid has no free trade history, so it is what this machine
// recorded while hlp ran.
type TickHistory struct {
	LocalOnly bool  `json:"localOnly"` // always true
	From      int64 `json:"from"`      // oldest trade available, ms (0 = none)
	To        int64 `json:"to"`        // newest trade, ms
	Trades    int64 `json:"trades"`    // trades stored for this coin
	Bytes     int64 `json:"bytes"`     // store size for this coin
	Days      int   `json:"days"`      // UTC day files
	Gaps      int   `json:"gaps"`      // bars in this window that start after a gap
	Seeded    int   `json:"seeded"`    // new trades taken from recentTrades on this call
	// Recording: this process stores the trades it sees. RecordedElsewhere:
	// another hlp process holds the coin's writer lease and stores them.
	Recording         bool   `json:"recording"`
	RecordedElsewhere bool   `json:"recordedElsewhere,omitempty"`
	RetentionDays     int    `json:"retentionDays"`
	MaxMB             int    `json:"maxMb"`
	Path              string `json:"path,omitempty"`
	Note              string `json:"note"`
}

const tickHistoryNote = "Local history only: trades this machine recorded while hlp ran, seeded with Hyperliquid's latest trades (it has no free trade history). Keep `hlp ticks record COIN` or a live chart running to grow it."

func tickTools() []Tool {
	return []Tool{
		{Name: "get_tick_candles", Command: "candles", Slash: "candles", Positional: []string{"coin"}, Group: "market", Streaming: true, LocalState: true,
			NewInput: in[TickChartInput](), Run: runTickCandles, Stream: streamTickCandles,
			Description: "Tick charts: candles of N trades (ticks), N base units (volumeBars), N USD (usdBars) or a price range (rangeBars), built from Hyperliquid's public trades. Each bar has o/h/l/c, v (base) and vUsd, n trades, buyV/sellV by aggressor side, first/last trade time (t, tLast) and tid; closed is false for the newest bar; gap marks a bar that starts after trades may have been missed (reconnect or restart); partial marks a bar cut short by a gap or the UTC day end (bars restart at 00:00 UTC). History is local only (history.from/to/trades): Hyperliquid has no free trade history, so bars cover what this machine recorded, seeded with recent trades. Live streams emit a snapshot then updates with only the changed bars (upsert by i).",
			Summary:     "Tick, volume and range bars from live trades with local history",
			Examples:    []string{"hlp candles btc --ticks 500", "hlp candles eth --usd-bars 1m --json", "hlp candles btc --range-bars 50 --live --ndjson --max-events 5"}},
	}
}

// tickSpec picks the one bar type the input names (default 500 ticks).
func tickSpec(p *TickChartInput) (ticks.Spec, error) {
	var specs []ticks.Spec
	if p.Ticks != 0 {
		specs = append(specs, ticks.Spec{Kind: ticks.KindTicks, Size: float64(p.Ticks)})
	}
	if p.VolumeBars != 0 {
		specs = append(specs, ticks.Spec{Kind: ticks.KindVolume, Size: p.VolumeBars})
	}
	if p.USDBars != 0 {
		specs = append(specs, ticks.Spec{Kind: ticks.KindUSD, Size: p.USDBars})
	}
	if p.RangeBars != 0 {
		specs = append(specs, ticks.Spec{Kind: ticks.KindRange, Size: p.RangeBars})
	}
	switch len(specs) {
	case 0:
		return ticks.Spec{Kind: ticks.KindTicks, Size: 500}, nil
	case 1:
		if err := specs[0].Validate(); err != nil {
			return ticks.Spec{}, &Error{Code: CodeUsage, Message: err.Error(), Hint: "e.g. --ticks 500, --volume-bars 10, --usd-bars 1m, --range-bars 50"}
		}
		return specs[0], nil
	}
	return ticks.Spec{}, &Error{Code: CodeUsage, Message: "give one bar type: ticks, volumeBars, usdBars or rangeBars", Hint: "e.g. --ticks 500"}
}

type tickParams struct {
	market domain.Market
	spec   ticks.Spec
	bars   int
	width  int
	height int
	style  chart.Style
	zoom   int
	in     *TickChartInput
}

func tickSetup(ctx context.Context, env *Env, p *TickChartInput) (*tickParams, error) {
	spec, err := tickSpec(p)
	if err != nil {
		return nil, err
	}
	// reuse the chart's size, style and zoom rules
	cp, err := chartSetup(ctx, env, &ChartInput{Coin: p.Coin, Width: p.Width, Height: p.Height, Style: p.Style, Zoom: p.Zoom, Bars: max(p.Bars, 2), MA: []int{}})
	if err != nil {
		return nil, err
	}
	tp := &tickParams{market: cp.market, spec: spec, width: cp.width, height: cp.height, style: cp.style, zoom: cp.zoom, in: p}
	tp.bars = p.Bars
	if tp.bars <= 0 {
		tp.bars = 300
		if p.Width > 0 {
			tp.bars = p.Width / cp.zoom
		}
	}
	if tp.bars < 1 || tp.bars > 5000 {
		return nil, Errorf(CodeUsage, "bars must be 1-5000, got %d", tp.bars)
	}
	return tp, nil
}

// tickRoot is the store directory for the env's network ("" = no cache dir).
func (e *Env) tickRoot() string {
	if e.Paths.CacheDir == "" {
		return ""
	}
	net := string(e.Config.Network)
	if net == "" {
		net = string(domain.Mainnet)
	}
	return filepath.Join(e.Paths.CacheDir, "ticks", net)
}

func (e *Env) tickOptions() ticks.Options {
	return ticks.Options{RetentionDays: e.Config.Ticks.RetentionDays, MaxBytes: int64(e.Config.Ticks.MaxMB) << 20, Now: e.now}
}

// openTickStore opens coin's store; nil (memory only) when there is no
// cache directory or it cannot be created, with the reason as a warning.
func (e *Env) openTickStore(coin string) (*ticks.Store, string) {
	root := e.tickRoot()
	if root == "" {
		return nil, "no cache directory: tick history is kept in memory only"
	}
	s, err := ticks.Open(root, coin, e.tickOptions())
	if err != nil {
		return nil, "tick history not stored: " + err.Error()
	}
	return s, ""
}

// tickSession is one coin's store, recorder and bar state.
type tickSession struct {
	env   *Env
	tp    *tickParams
	store *ticks.Store
	rec   *ticks.Recorder
	agg   *ticks.Aggregator
	all   []ticks.Bar
	hist  TickHistory
	meta  domain.Meta
}

func (s *tickSession) close() {
	if s.store != nil {
		_ = s.store.Close()
	}
}

// openTicks seeds the recorder from recentTrades and builds the snapshot.
func openTicks(ctx context.Context, env *Env, p *TickChartInput) (*tickSession, error) {
	tp, err := tickSetup(ctx, env, p)
	if err != nil {
		return nil, err
	}
	s := &tickSession{env: env, tp: tp, meta: env.meta("hyperliquid+local")}
	var warn string
	s.store, warn = env.openTickStore(tp.market.Coin)
	if warn != "" {
		s.meta.Warnings = append(s.meta.Warnings, warn)
	}
	s.rec = ticks.NewRecorder(s.store)
	seeded := 0
	if ts, err := env.HL.Trades(ctx, tp.market.Coin); err == nil {
		seeded = len(s.rec.Seed(ts))
	} else {
		s.meta.Partial = true
		s.meta.Warnings = append(s.meta.Warnings, "recent trades unavailable: "+AsError(err).Message)
	}
	if err := s.rec.Err(); err != nil {
		s.meta.Warnings = append(s.meta.Warnings, "tick history not stored: "+err.Error())
	}
	today := ticks.DayOf(env.now().UnixMilli())
	bars, agg, err := ticks.Series(s.store, s.rec.Unstored(), tp.spec, tp.bars, today)
	if err != nil {
		s.close()
		return nil, Errorf(CodeInternal, "tick history: %v", err)
	}
	s.all, s.agg = bars, agg
	s.hist = s.history(seeded)
	return s, nil
}

func (s *tickSession) history(seeded int) TickHistory {
	h := TickHistory{LocalOnly: true, Seeded: seeded, Note: tickHistoryNote,
		RetentionDays: s.env.Config.Ticks.RetentionDays, MaxMB: s.env.Config.Ticks.MaxMB}
	if h.RetentionDays <= 0 {
		h.RetentionDays = ticks.DefaultRetentionDays
	}
	if h.MaxMB <= 0 {
		h.MaxMB = ticks.DefaultMaxMB
	}
	if s.store != nil {
		h.Path = s.store.Dir()
		h.Recording = s.rec.Writer()
		h.RecordedElsewhere = !s.store.Writer()
		if st, err := s.store.Stats(); err == nil {
			h.From, h.To, h.Trades, h.Bytes, h.Days = st.From, st.To, st.Trades, st.Bytes, st.Days
		}
	}
	for _, r := range s.rec.Unstored() {
		if h.From == 0 || r.Time < h.From {
			h.From = r.Time
		}
		h.To = max(h.To, r.Time)
	}
	return h
}

func (s *tickSession) result(event string, changed []ticks.Bar, meta domain.Meta) *Result {
	tp := s.tp
	h := s.hist
	h.Gaps = 0
	for _, b := range s.all {
		if b.Gap {
			h.Gaps++
		}
	}
	cs := make([]domain.Candle, len(s.all))
	for i, b := range s.all {
		cs[i] = b.Candle()
	}
	label := tp.spec.Label(baseName(tp.market))
	d := &TickChartData{Coin: tp.market.Coin, Bar: tp.spec, Label: label, Market: tp.market, Event: event,
		Bars: changed, Summary: Summarize(cs, nil, nil), History: h, All: s.all,
		Style: tp.in.Style, Zoom: tp.in.Zoom, Height: tp.in.Height}
	if d.Bars == nil {
		d.Bars = []ticks.Bar{}
	}
	if event == "snapshot" && len(cs) > 0 {
		if g, _ := chart.Candles(cs, s.chartOpts(label), s.env.Theme); g != nil {
			d.Plain = g.Plain()
		}
	}
	title := tp.market.Display
	if title == "" {
		title = tp.market.Coin
	}
	return &Result{Card: CardTicks, Title: title + " " + label, Data: d, Meta: meta}
}

func (s *tickSession) chartOpts(label string) chart.CandleOpts {
	tp := s.tp
	coin := tp.market.Display
	if coin == "" {
		coin = tp.market.Coin
	}
	loc := time.Local
	if s.env.Config.Chart.UTC {
		loc = time.UTC
	}
	return chart.CandleOpts{Width: tp.width, Height: tp.height, Style: tp.style, Zoom: tp.zoom, VolRows: -1,
		VolumeUSD: s.env.Config.Chart.VolumeUSD, Cursor: -1, Header: true, Coin: coin, Interval: domain.Interval(label),
		Location: loc, Skin: s.env.Config.Chart.Theme, TickBars: true}
}

// baseName is the base asset for volume labels: BTC, SP500, HYPE.
func baseName(m domain.Market) string {
	name := m.Display
	if name == "" {
		name = m.Coin
	}
	for i := 0; i < len(name); i++ {
		switch name[i] {
		case '/', ' ':
			return name[:i]
		case ':':
			name = name[i+1:]
			i = -1
		}
	}
	return name
}

func runTickCandles(ctx context.Context, env *Env, in any) (*Result, error) {
	s, err := openTicks(ctx, env, in.(*TickChartInput))
	if err != nil {
		return nil, err
	}
	defer s.close()
	return s.result("snapshot", s.all, s.meta), nil
}

// upsertBars applies changed bars to the window (replace by i, append
// newer) and keeps at most n.
func upsertBars(all, changed []ticks.Bar, n int) []ticks.Bar {
	for _, b := range changed {
		switch k := len(all); {
		case k > 0 && all[k-1].I == b.I:
			all[k-1] = b
		case k == 0 || b.I > all[k-1].I:
			all = append(all, b)
		}
	}
	if n > 0 && len(all) > n {
		all = append([]ticks.Bar(nil), all[len(all)-n:]...)
	}
	return all
}

func streamTickCandles(ctx context.Context, env *Env, in any, emit func(*Result)) error {
	s, err := openTicks(ctx, env, in.(*TickChartInput))
	if err != nil {
		return err
	}
	defer s.close()
	emit(s.result("snapshot", s.all, s.meta))
	if env.Stream == nil {
		return nil
	}
	ch, cancel, err := ws.TradeBatches(ctx, env.Stream, s.tp.market.Coin)
	if err != nil {
		return err
	}
	defer cancel()
	var changed []ticks.Bar
	// newest is the latest trade time folded into bars. A trade older than
	// it (a reconnect snapshot reaching further back than the seed) cannot
	// be placed by appending, so the window is rebuilt from the store and
	// sent as a new snapshot: live bars always equal a rebuild.
	newest := int64(0)
	for _, b := range s.all {
		newest = max(newest, b.TLast)
	}
	dirty, rebuild := false, false
	ingest := func(b ws.TradeBatch) {
		for _, r := range s.rec.Ingest(b.Trades, b.Gen) {
			dirty = true
			if r.Time < newest {
				rebuild = true
			}
			if !rebuild {
				changed = append(changed, s.agg.Add(r)...)
			}
			newest = max(newest, r.Time)
		}
	}
	flush := func() {
		if !dirty {
			return
		}
		meta := env.meta("hyperliquid-ws")
		s.hist = s.history(s.hist.Seeded)
		if err := s.rec.Err(); err != nil {
			meta.Warnings = append(meta.Warnings, "tick history not stored: "+err.Error())
		}
		event := "update"
		if rebuild {
			today := ticks.DayOf(env.now().UnixMilli())
			bars, agg, err := ticks.Series(s.store, s.rec.Unstored(), s.tp.spec, s.tp.bars, today)
			if err != nil {
				meta.Warnings = append(meta.Warnings, "tick history: "+err.Error())
			} else {
				s.all, s.agg, changed, event = bars, agg, bars, "snapshot"
			}
		} else {
			if open, ok := s.agg.Open(); ok {
				changed = append(changed, open)
			}
			s.all = upsertBars(s.all, changed, s.tp.bars)
		}
		emit(s.result(event, changed, meta))
		changed, dirty, rebuild = nil, false, false
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case b, ok := <-ch:
			if !ok {
				return nil
			}
			ingest(b)
			// coalesce whatever else is queued into one update
		drain:
			for {
				select {
				case b, ok := <-ch:
					if !ok {
						flush()
						return nil
					}
					ingest(b)
				default:
					break drain
				}
			}
			flush()
		}
	}
}

// ReplayTicks reads a recorded interval without resolving a market or opening a feed.
func ReplayTicks(ctx context.Context, env *Env, coin string, spec ticks.Spec, from, to int64, count int) (*Result, error) {
	d, err := ticks.Replay(ctx, env.tickRoot(), coin, spec, from, to, count)
	if errors.Is(err, ticks.ErrReplayInput) {
		return nil, Errorf(CodeUsage, "%v", err)
	}
	if err != nil {
		return nil, Errorf(CodeInternal, "tick replay: %v", err)
	}
	meta := env.meta("local")
	meta.Truncated = d.Truncated
	if len(d.Frames) > 0 {
		meta.AsOf = time.UnixMilli(d.To).UTC()
	}
	return &Result{Data: d, Meta: meta}, nil
}

// TickStatusRow is one coin's stored tick history (`hlp ticks`, and each
// line of `hlp ticks record`).
type TickStatusRow struct {
	Coin string `json:"coin"`
	ticks.Stats
	// Session counts while recording: trades stored and gaps marked.
	Session   int    `json:"session,omitempty"`
	GapsSeen  int    `json:"gapsSeen,omitempty"`
	Recording bool   `json:"recording"`
	Error     string `json:"error,omitempty"`
}

// TickStatusData is the payload of `hlp ticks` and `hlp ticks record`.
type TickStatusData struct {
	Coins     []TickStatusRow `json:"coins"`
	Path      string          `json:"path"`
	LocalOnly bool            `json:"localOnly"`
	Note      string          `json:"note"`
}

// TickStatus lists stored tick history (coins nil = every stored coin).
func TickStatus(ctx context.Context, env *Env, coins []string) (*Result, error) {
	root := env.tickRoot()
	if len(coins) == 0 && root != "" {
		var err error
		if coins, err = ticks.Coins(root); err != nil {
			return nil, Errorf(CodeInternal, "tick history: %v", err)
		}
	}
	d := &TickStatusData{Coins: []TickStatusRow{}, Path: root, LocalOnly: true, Note: tickHistoryNote}
	for _, c := range coins {
		coin := c
		if m, err := env.Resolve(ctx, c); err == nil {
			coin = m.Coin
		} else if len(coins) > 0 && root == "" {
			return nil, err
		}
		row := TickStatusRow{Coin: coin}
		if root != "" {
			if s, err := ticks.Open(root, coin, env.tickOptions()); err == nil {
				row.Stats, _ = s.Stats()
				_ = s.Close()
			} else {
				row.Error = err.Error()
			}
		}
		d.Coins = append(d.Coins, row)
	}
	return &Result{Card: CardKV, Title: "Tick history", Data: d, Meta: env.meta("local")}, nil
}

// RecordTicks records coins' trades to the local store in the foreground
// until ctx ends, emitting a status every `every` (and once at the start).
// A coin another hlp process already records is reported, not recorded.
func RecordTicks(ctx context.Context, env *Env, coins []string, every time.Duration, emit func(*Result)) error {
	if len(coins) == 0 {
		return &Error{Code: CodeUsage, Message: "name at least one coin to record", Hint: "e.g. hlp ticks record BTC ETH"}
	}
	if env.tickRoot() == "" {
		return Errorf(CodeInternal, "no cache directory to record into")
	}
	if every <= 0 {
		every = 10 * time.Second
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	type coinRec struct {
		row   TickStatusRow
		store *ticks.Store
		rec   *ticks.Recorder
	}
	var recs []*coinRec
	defer func() {
		for _, c := range recs {
			if c.store != nil {
				_ = c.store.Close()
			}
		}
	}()
	for _, in := range coins {
		m, err := env.Resolve(ctx, in)
		if err != nil {
			return err
		}
		c := &coinRec{row: TickStatusRow{Coin: m.Coin}}
		recs = append(recs, c)
		s, warn := env.openTickStore(m.Coin)
		if s == nil {
			c.row.Error = warn
			continue
		}
		c.store, c.rec = s, ticks.NewRecorder(s)
		if !c.rec.Writer() {
			c.row.Error = "another hlp process is recording " + m.Coin
			continue
		}
		c.row.Recording = true
		if ts, err := env.HL.Trades(ctx, m.Coin); err == nil {
			c.row.Session += len(c.rec.Seed(ts))
		}
	}
	status := func() *Result {
		d := &TickStatusData{Path: env.tickRoot(), LocalOnly: true, Note: tickHistoryNote}
		for _, c := range recs {
			row := c.row
			if c.store != nil {
				row.Stats, _ = c.store.Stats()
			}
			if c.rec != nil {
				row.Recording = c.rec.Writer()
				if err := c.rec.Err(); err != nil {
					row.Error = err.Error()
				}
			}
			d.Coins = append(d.Coins, row)
		}
		return &Result{Card: CardKV, Title: "Recording ticks", Data: d, Meta: env.meta("hyperliquid-ws")}
	}
	emit(status())
	active := 0
	for _, c := range recs {
		if c.row.Recording {
			active++
		}
	}
	if active == 0 {
		return &Error{Code: CodeUsage, Message: "nothing to record", Hint: "every coin is already recorded by another hlp process, or the store could not be opened"}
	}
	if env.Stream == nil {
		return nil
	}
	type batch struct {
		c *coinRec
		b ws.TradeBatch
	}
	merged := make(chan batch)
	for _, c := range recs {
		if !c.row.Recording {
			continue
		}
		ch, cancel, err := ws.TradeBatches(ctx, env.Stream, c.row.Coin)
		if err != nil {
			return err
		}
		defer cancel()
		go func(c *coinRec, ch <-chan ws.TradeBatch) {
			for b := range ch {
				select {
				case merged <- batch{c, b}:
				case <-ctx.Done():
					return
				}
			}
		}(c, ch)
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			emit(status())
			return ctx.Err()
		case <-tick.C:
			emit(status())
		case x := <-merged:
			for _, r := range x.c.rec.Ingest(x.b.Trades, x.b.Gen) {
				x.c.row.Session++
				if r.Gap {
					x.c.row.GapsSeen++
				}
			}
		}
	}
}
