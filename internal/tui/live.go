package tui

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/hyperliquid/ws"
)

// Live updates follow one owner rule: hidden views never stream or poll.
// After every update the model computes the set of live targets that are
// actually on screen (the top pane, else transcript cards inside the
// viewport, plus the side panel), and starts or cancels subscriptions to
// match. Losing terminal focus empties the set.

// liveSub is one running stream.
type liveSub struct {
	key    string
	cancel context.CancelFunc
	ch     chan tea.Msg
	// owned is chart data this sub already copied, so later candles are
	// merged in place instead of copying every candle per tick (PERF #10).
	owned *core.ChartData
}

// liveMsg carries a fresh Result for an entry or pane.
type liveMsg struct {
	sub *liveSub
	res *core.Result
}

// candleMsg carries one streamed candle (ws fallback for chart cards).
type candleMsg struct {
	sub *liveSub
	c   domain.Candle
}

// midsMsg carries the latest mark map for the watchlist.
type midsMsg struct {
	sub  *liveSub
	mids map[string]float64
	// prevDay, when set, is each coin's price 24h ago (one REST snapshot
	// when the watchlist opens) for the 24h change column.
	prevDay map[string]float64
}

// acctMsg carries the configured account's positions for the side panel.
type acctMsg struct {
	sub *liveSub
	a   domain.Account
	// perp marks a WS update (perps only): spot and Earn stay from the
	// last REST snapshot.
	perp bool
}

// liveSpec describes a desired subscription.
type liveSpec struct {
	key string
	run func(ctx context.Context, sub *liveSub, emit func(tea.Msg))
}

// maxInlineLive caps how many transcript cards stream at once.
const maxInlineLive = 3

// desiredLive lists what should be streaming right now.
func (m *Model) desiredLive() []liveSpec {
	if !m.focused || m.quitting {
		return nil
	}
	var out []liveSpec
	if p := m.topPane(); p != nil && p.kind == paneDash {
		for _, t := range p.tiles {
			if s, ok := m.specFor("pane:"+strconv.Itoa(t.seq), t.res, t.tool, t.call); ok {
				out = append(out, s)
			}
		}
	} else if p != nil {
		if s, ok := m.specFor("pane:"+strconv.Itoa(p.seq), p.res, p.tool, p.call); ok {
			out = append(out, s)
		}
	} else {
		n := 0
		for i := len(m.entries) - 1; i >= 0 && n < maxInlineLive; i-- {
			e := m.entries[i]
			if e.kind != entryCard || e.slim || !m.entryVisible(e) {
				continue
			}
			if s, ok := m.specFor("entry:"+strconv.Itoa(e.id), e.result, e.tool, e.call); ok {
				out = append(out, s)
				n++
			}
		}
	}
	// The price feed (one WebSocket subscription, no polling) runs while
	// the top bar's ticker or the side panel shows prices.
	if (m.showSide || m.tickerShown()) && m.env != nil && m.env.Stream != nil {
		// Decided here, on the Update goroutine: the prevDay REST read
		// runs only while the model has none (PERF #4).
		var coins []string
		if len(m.prevDay) == 0 {
			coins = append(append(coins, m.sideCoins()...), m.welcomeCoins()...)
		}
		out = append(out, liveSpec{key: "side:mids", run: m.runMids(coins)})
	}
	if m.showSide && m.env != nil && m.env.Stream != nil {
		if addr := m.env.Config.Account; addr != "" {
			fresh := m.acct != nil && strings.EqualFold(m.acct.Address, addr) && m.acct.Time > 0 &&
				m.now().Sub(time.UnixMilli(m.acct.Time)) < seedMaxAge
			out = append(out, liveSpec{key: "side:acct", run: m.runAccount(addr, !fresh)})
		}
	}
	return out
}

// seedMaxAge is how old a result may be and still seed a restarted stream.
const seedMaxAge = 60 * time.Second

// specFor builds the stream for a card: the tool's own Stream when it has
// one, else (charts) the WebSocket candle feed merged into the card.
func (m *Model) specFor(key string, r *core.Result, t core.Tool, call core.Call) (liveSpec, bool) {
	if r == nil || t.Mutating || !t.Streaming {
		return liveSpec{}, false
	}
	if t.Stream != nil && t.NewInput != nil && len(call.Args) > 0 {
		env := m.env
		// A card fetched moments ago seeds its stream, so regaining focus
		// or scrolling back does not repeat the REST snapshot (PERF #4).
		seed := m.now().Sub(r.Meta.AsOf) < seedMaxAge
		return liveSpec{key: key, run: func(ctx context.Context, sub *liveSub, emit func(tea.Msg)) {
			in := t.NewInput()
			if err := json.Unmarshal(call.Args, in); err != nil {
				return
			}
			sctx := ctx
			if seed {
				sctx = core.WithSeed(ctx, r)
			}
			_ = t.Stream(sctx, env, in, func(r *core.Result) { emit(liveMsg{sub: sub, res: r}) })
		}}, true
	}
	if d, ok := asChart(r.Data); ok && m.env != nil && m.env.Stream != nil {
		stream, coin, iv := m.env.Stream, d.Coin, d.Interval
		return liveSpec{key: key, run: func(ctx context.Context, sub *liveSub, emit func(tea.Msg)) {
			ch, cancel, err := ws.Candles(ctx, stream, coin, iv)
			if err != nil {
				return
			}
			defer cancel()
			for {
				select {
				case <-ctx.Done():
					return
				case c, ok := <-ch:
					if !ok {
						return
					}
					emit(candleMsg{sub: sub, c: c})
				}
			}
		}}, true
	}
	if b, ok := asBook(r.Data); ok && m.env != nil && m.env.Stream != nil && b.Coin != "" {
		stream, coin := m.env.Stream, b.Coin
		return liveSpec{key: key, run: func(ctx context.Context, sub *liveSub, emit func(tea.Msg)) {
			ch, cancel, err := ws.Books(ctx, stream, coin, 0)
			if err != nil {
				return
			}
			defer cancel()
			for {
				select {
				case <-ctx.Done():
					return
				case nb, ok := <-ch:
					if !ok {
						return
					}
					nr := *r
					nr.Data = nb
					nr.Meta.AsOf = time.UnixMilli(nb.Time)
					emit(liveMsg{sub: sub, res: &nr})
				}
			}
		}}, true
	}
	return liveSpec{}, false
}

func asBook(v any) (domain.Book, bool) {
	switch x := v.(type) {
	case domain.Book:
		return x, true
	case *domain.Book:
		if x != nil {
			return *x, true
		}
	}
	return domain.Book{}, false
}

// marketCtxReader reads one market's context (one dex, weight 20,
// memoized per dex): *hyperliquid.Client.
type marketCtxReader interface {
	MarketCtx(ctx context.Context, coin string) (domain.Market, error)
}

// runMids subscribes to the price feed first, so prices show at once, and
// (when prevCoins is set) reads those coins' prevDay prices beside it: one
// metaAndAssetCtxs per dex involved (~40 weight) instead of all markets
// (240). prevDay rides on every later message, so a latest-wins drop
// cannot lose it.
func (m *Model) runMids(prevCoins []string) func(context.Context, *liveSub, func(tea.Msg)) {
	env := m.env
	return func(ctx context.Context, sub *liveSub, emit func(tea.Msg)) {
		ch, cancel, err := ws.Mids(ctx, env.Stream)
		if err != nil {
			return
		}
		defer cancel()
		prevCh := make(chan map[string]float64, 1)
		if len(prevCoins) > 0 && env.HL != nil {
			go func() { prevCh <- fetchPrevDay(ctx, env.HL, prevCoins) }()
		}
		var prev, last map[string]float64
		for {
			select {
			case <-ctx.Done():
				return
			case p := <-prevCh:
				if len(p) == 0 {
					continue
				}
				prev = p
				emit(midsMsg{sub: sub, mids: last, prevDay: prev})
			case mm, ok := <-ch:
				if !ok {
					return
				}
				last = mm
				emit(midsMsg{sub: sub, mids: mm, prevDay: prev})
			}
		}
	}
}

// fetchPrevDay returns coin -> price 24h ago for coins.
func fetchPrevDay(ctx context.Context, hl interface {
	Markets(context.Context) ([]domain.Market, domain.Meta, error)
}, coins []string) map[string]float64 {
	out := map[string]float64{}
	if mc, ok := hl.(marketCtxReader); ok {
		seen := map[string]bool{}
		for _, c := range coins {
			if seen[c] {
				continue
			}
			seen[c] = true
			if mk, err := mc.MarketCtx(ctx, c); err == nil && mk.PrevDay > 0 {
				out[c] = mk.PrevDay
			}
		}
		return out
	}
	if ms, _, err := hl.Markets(ctx); err == nil {
		for _, mk := range ms {
			if mk.PrevDay > 0 {
				out[mk.Coin] = mk.PrevDay
			}
		}
	}
	return out
}

// runAccount streams the configured account for the side panel. The REST
// snapshot (spot and Earn included) is skipped when the panel already
// holds one under a minute old.
func (m *Model) runAccount(addr string, snapshot bool) func(context.Context, *liveSub, func(tea.Msg)) {
	return func(ctx context.Context, sub *liveSub, emit func(tea.Msg)) {
		if snapshot && m.env.HL != nil {
			if a, err := m.env.HL.Account(ctx, addr); err == nil {
				emit(acctMsg{sub: sub, a: a})
			}
		}
		ch, cancel, err := ws.Positions(ctx, m.env.Stream, addr)
		if err != nil {
			return
		}
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case a, ok := <-ch:
				if !ok {
					return
				}
				emit(acctMsg{sub: sub, a: a, perp: true})
			}
		}
	}
}

// reconcileLive starts and stops subscriptions to match desiredLive.
func (m *Model) reconcileLive() tea.Cmd {
	want := map[string]liveSpec{}
	for _, s := range m.desiredLive() {
		want[s.key] = s
	}
	for k, s := range m.live {
		if _, ok := want[k]; !ok {
			s.cancel()
			delete(m.live, k)
		}
	}
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var cmds []tea.Cmd
	for _, k := range keys {
		if _, ok := m.live[k]; ok {
			continue
		}
		cmds = append(cmds, m.startLive(want[k]))
	}
	return tea.Batch(cmds...)
}

func (m *Model) startLive(spec liveSpec) tea.Cmd {
	ctx, cancel := context.WithCancel(m.ctx)
	sub := &liveSub{key: spec.key, cancel: cancel, ch: make(chan tea.Msg, 4)}
	m.live[spec.key] = sub
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer close(sub.ch)
		spec.run(ctx, sub, func(msg tea.Msg) { sendLatest(ctx, sub.ch, msg) })
	}()
	return waitOn(sub.ch)
}

// sendLatest delivers msg without blocking a stream: when the buffer is
// full the oldest pending update is dropped (market data is latest-wins).
func sendLatest(ctx context.Context, ch chan tea.Msg, msg tea.Msg) {
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case ch <- msg:
			return
		default:
			select {
			case <-ch:
			default:
			}
		}
	}
}

// active reports whether sub is still the current subscription for its key.
func (m *Model) active(sub *liveSub) bool {
	return sub != nil && m.live[sub.key] == sub
}

// onLive routes a live message to its entry or pane and re-arms the reader.
func (m *Model) onLive(msg tea.Msg) tea.Cmd {
	var sub *liveSub
	switch x := msg.(type) {
	case liveMsg:
		sub = x.sub
		if m.active(sub) && x.res != nil {
			m.applyLive(sub.key, func(*core.Result) *core.Result { return x.res })
		}
	case candleMsg:
		sub = x.sub
		if m.active(sub) {
			m.applyLive(sub.key, func(r *core.Result) *core.Result { return mergeCandleOwned(r, x.c, &sub.owned) })
		}
	case midsMsg:
		sub = x.sub
		if m.active(sub) {
			for k, v := range x.prevDay {
				m.prevDay[k] = v
			}
			// Keep only the coins the shell shows (ticker, side panel): the
			// feed carries every market, a tick should not copy them all.
			keep := func(k string) {
				v, ok := x.mids[k]
				if !ok {
					return
				}
				if old, ok := m.mids[k]; ok && old != v {
					m.prev[k] = old
				}
				m.mids[k] = v
			}
			for _, k := range m.welcomeCoins() {
				keep(k)
			}
			for _, k := range m.sideCoins() {
				keep(k)
			}
			m.setFlaps()
		}
	case acctMsg:
		sub = x.sub
		if m.active(sub) {
			a := x.a
			if x.perp && m.acct != nil {
				a = core.MergePerp(*m.acct, a)
			}
			m.acct = &a
		}
	}
	if !m.active(sub) {
		return nil
	}
	return waitOn(sub.ch)
}

// applyLive replaces the result behind a live key.
func (m *Model) applyLive(key string, f func(*core.Result) *core.Result) {
	for _, p := range m.panes {
		for _, t := range append([]*pane{p}, p.tiles...) {
			if "pane:"+strconv.Itoa(t.seq) == key {
				if r := f(t.res); r != nil {
					t.setResult(r)
				}
				return
			}
		}
	}
	for _, e := range m.entries {
		if "entry:"+strconv.Itoa(e.id) == key && e.kind == entryCard {
			if r := f(e.result); r != nil {
				e.result, e.cache = r, ""
			}
			return
		}
	}
}

// mergeCandle folds one streamed candle into a chart result (replace the
// open bar when T matches, append when newer) without mutating the old one.
func mergeCandle(r *core.Result, c domain.Candle) *core.Result {
	return mergeCandleOwned(r, c, nil)
}

// mergeCandleOwned is mergeCandle that copies the candles once per sub:
// when r's chart data is the copy this sub made (*owned), the open bar is
// replaced in place and new bars append with amortized growth (PERF #10).
// A new Result is still returned, so caches keyed on it refresh.
func mergeCandleOwned(r *core.Result, c domain.Candle, owned **core.ChartData) *core.Result {
	d, ok := asChart(r.Data)
	if !ok || c.T == 0 {
		return nil
	}
	if owned != nil && *owned != nil && *owned == d {
		n := len(d.Candles)
		switch {
		case n > 0 && d.Candles[n-1].T == c.T:
			d.Candles[n-1] = c
		case n == 0 || c.T > d.Candles[n-1].T:
			d.Candles = append(d.Candles, c)
		default:
			return nil
		}
		d.Summary.Last = c.C
		if d.Market.Mark != 0 {
			d.Market.Mark = c.C
		}
		nr := *r
		return &nr
	}
	nd := *d
	cs := make([]domain.Candle, len(d.Candles), len(d.Candles)+len(d.Candles)/8+1)
	copy(cs, d.Candles)
	switch n := len(cs); {
	case n > 0 && cs[n-1].T == c.T:
		cs[n-1] = c
	case n == 0 || c.T > cs[n-1].T:
		cs = append(cs, c)
	default:
		return nil // stale frame
	}
	nd.Candles = cs
	nd.Summary.Last = c.C
	if nd.Market.Mark != 0 {
		nd.Market.Mark = c.C
	}
	if owned != nil {
		*owned = &nd
	}
	nr := *r
	nr.Data = &nd
	return &nr
}

// entryVisible reports whether any line of e is inside the transcript viewport.
func (m *Model) entryVisible(e *entry) bool {
	lo, hi := m.viewLo, m.viewHi
	return e.top < hi && e.top+max(e.lines, 1) > lo
}
