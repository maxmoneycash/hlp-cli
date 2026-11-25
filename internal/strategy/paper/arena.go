package paper

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/strategy"
)

// Arena runs several strategies on the same feed with the same capital and
// streams a leaderboard. Real vaults can join as read-only entrants whose
// equity comes from the Reader (watch mode).
type Arena interface {
	Leaderboard() []Snapshot // sorted by return
	Updates() <-chan []Snapshot
	Stop() error
}

// StartArena starts every entrant as a paper Session.
//
// All entrants share one upstream subscription per coin (the feed is
// fanned out) and short-lived caches for history, books and funding, so N
// bots cost about as much as one. Each entrant gets ID "<o.ID>-<n>-<name>"
// and o.Capital. Options.Watch adds read-only entrants (real vaults) whose
// value is polled every Options.WatchEvery; their equity is
// Capital x value / first value.
func StartArena(ctx context.Context, entrants []strategy.Strategy, feed Feed, o Options) (Arena, error) {
	if len(entrants)+len(o.Watch) < 2 {
		return nil, errors.New("arena: needs at least two entrants (strategies and/or watched vaults)")
	}
	if feed == nil && len(entrants) > 0 {
		return nil, errors.New("arena: nil feed")
	}
	if o.ID == "" {
		o.ID = "arena-" + randHex(3)
	}
	if !validID(o.ID) {
		return nil, fmt.Errorf("arena: invalid id %q", o.ID)
	}
	base := withDefaults(o, strategy.Spec{})
	a := &arena{updates: make(chan []Snapshot, 1), done: make(chan struct{})}
	actx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	var hub *fanout
	if feed != nil {
		hub = newFanout(actx, feed)
	}
	for i, s := range entrants {
		if s == nil {
			cancel()
			a.stopAll()
			return nil, fmt.Errorf("arena: entrant %d is nil", i+1)
		}
		eo := base
		eo.Watch = nil
		name := slug(s.Spec().Name)
		eo.ID = fmt.Sprintf("%s-%d-%s", o.ID, i+1, name)
		if len(eo.ID) > 100 {
			eo.ID = eo.ID[:100]
		}
		sess, err := Start(actx, s, hub, eo)
		if err != nil {
			cancel()
			a.stopAll()
			return nil, fmt.Errorf("arena: start %s: %w", s.Spec().Name, err)
		}
		a.sessions = append(a.sessions, sess)
	}
	for _, w := range o.Watch {
		if w.Value == nil {
			cancel()
			a.stopAll()
			return nil, fmt.Errorf("arena: watcher %q has no value source", w.Name)
		}
		ws := newWatchSession(actx, w, base)
		a.sessions = append(a.sessions, ws)
	}
	go a.run(actx)
	return a, nil
}

type arena struct {
	sessions []Session
	updates  chan []Snapshot
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
	err      error
}

func (a *arena) run(ctx context.Context) {
	defer close(a.done)
	dirty := make(chan struct{}, 1)
	var wg sync.WaitGroup
	for _, s := range a.sessions {
		wg.Add(1)
		go func(ch <-chan Snapshot) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case _, ok := <-ch:
					if !ok {
						return
					}
					select {
					case dirty <- struct{}{}:
					default:
					}
				}
			}
		}(s.Updates())
	}
	defer wg.Wait()
	t := time.NewTicker(publishEvery)
	defer t.Stop()
	pending := true // publish the initial board
	for {
		select {
		case <-ctx.Done():
			return
		case <-dirty:
			pending = true
		case <-t.C:
			if !pending {
				continue
			}
			pending = false
			lb := a.Leaderboard()
			select {
			case <-a.updates:
			default:
			}
			select {
			case a.updates <- lb:
			default:
			}
		}
	}
}

// Leaderboard returns every entrant's snapshot, best return first.
func (a *arena) Leaderboard() []Snapshot {
	out := make([]Snapshot, 0, len(a.sessions))
	for _, s := range a.sessions {
		out = append(out, s.Snapshot())
	}
	Rank(out)
	return out
}

// Rank sorts snapshots by return (best first), ties by name.
func Rank(ss []Snapshot) {
	sort.SliceStable(ss, func(i, j int) bool {
		if ss[i].Return != ss[j].Return {
			return ss[i].Return > ss[j].Return
		}
		return ss[i].Strategy < ss[j].Strategy
	})
}

// Updates streams the leaderboard (coalesced, at most ~4/s); closed by Stop.
func (a *arena) Updates() <-chan []Snapshot { return a.updates }

// Stop stops every entrant and closes Updates.
func (a *arena) Stop() error {
	a.stopOnce.Do(func() {
		a.cancel()
		<-a.done
		a.err = a.stopAll()
		lb := a.Leaderboard()
		select {
		case <-a.updates:
		default:
		}
		a.updates <- lb
		close(a.updates)
	})
	return a.err
}

func (a *arena) stopAll() error {
	var errs []error
	for _, s := range a.sessions {
		if err := s.Stop(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ---- watched (read-only) entrants ----

type watchSession struct {
	w       Watcher
	o       Options
	mu      sync.Mutex
	snap    Snapshot
	first   float64
	updates chan Snapshot
	cancel  context.CancelFunc
	done    chan struct{}
	once    sync.Once
}

func newWatchSession(ctx context.Context, w Watcher, o Options) *watchSession {
	kind := w.Kind
	if kind == "" {
		kind = "vault"
	}
	ws := &watchSession{w: w, o: o, updates: make(chan Snapshot, 1), done: make(chan struct{}),
		snap: Snapshot{ID: "watch-" + slug(w.Name), Strategy: w.Name, Kind: kind, Started: o.Now().UnixMilli(),
			Capital: o.Capital, Running: true}}
	cctx, cancel := context.WithCancel(ctx)
	ws.cancel = cancel
	go ws.run(cctx)
	return ws
}

func (ws *watchSession) run(ctx context.Context) {
	defer close(ws.done)
	t := time.NewTicker(ws.o.WatchEvery)
	defer t.Stop()
	ws.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ws.poll(ctx)
		}
	}
}

func (ws *watchSession) poll(ctx context.Context) {
	v, err := ws.w.Value(ctx)
	ws.mu.Lock()
	defer ws.mu.Unlock()
	now := ws.o.Now().UnixMilli()
	ws.snap.Updated = now
	if err != nil || v <= 0 {
		if err != nil && ctx.Err() == nil {
			ws.snap.Err = err.Error()
		}
		return
	}
	ws.snap.Err = ""
	if ws.first == 0 {
		ws.first = v
	}
	eq := ws.o.Capital * v / ws.first
	ws.snap.Equity = append(ws.snap.Equity, domain.Point{T: now, V: eq})
	ws.snap.Return = eq/ws.o.Capital - 1
	ws.snap.Account = domain.Account{Address: ws.w.Name, Role: "vault", Equity: eq, PerpEquity: eq, Time: now}
	snap := ws.snap
	snap.Equity = append(domain.Series(nil), ws.snap.Equity...)
	select {
	case <-ws.updates:
	default:
	}
	select {
	case ws.updates <- snap:
	default:
	}
}

func (ws *watchSession) Snapshot() Snapshot {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	s := ws.snap
	s.Equity = append(domain.Series(nil), ws.snap.Equity...)
	return s
}

func (ws *watchSession) Updates() <-chan Snapshot { return ws.updates }

func (ws *watchSession) Stop() error {
	ws.once.Do(func() {
		ws.cancel()
		<-ws.done
		ws.mu.Lock()
		ws.snap.Running = false
		ws.mu.Unlock()
		close(ws.updates)
	})
	return nil
}

// ---- feed fan-out ----

// fanout shares one upstream Bars subscription per coin/interval between
// arena entrants and caches history, books and funding briefly.
type fanout struct {
	ctx  context.Context
	up   Feed
	mu   sync.Mutex
	subs map[string]*bcast
	hist map[string]cached[[]domain.Candle]
	book map[string]cached[domain.Book]
	fund map[string]cached[[]domain.FundingPoint]
}

type cached[T any] struct {
	at  time.Time
	v   T
	err error
}

type bcast struct {
	mu   sync.Mutex
	subs []*subQueue
}

// subQueue decouples one entrant from the upstream pump: bars queue up
// (bounded) and are delivered in order, so a slow entrant (an LLM thinking)
// neither blocks the others nor loses bars.
type subQueue struct {
	mu  sync.Mutex
	q   []domain.Candle
	sig chan struct{}
	out chan domain.Candle
}

const maxQueued = 10000

func (s *subQueue) push(c domain.Candle) {
	s.mu.Lock()
	s.q = append(s.q, c)
	if len(s.q) > maxQueued {
		s.q = s.q[len(s.q)-maxQueued:]
	}
	s.mu.Unlock()
	select {
	case s.sig <- struct{}{}:
	default:
	}
}

func (s *subQueue) deliver(ctx context.Context) {
	defer close(s.out)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.sig:
		}
		for {
			s.mu.Lock()
			if len(s.q) == 0 {
				s.mu.Unlock()
				break
			}
			c := s.q[0]
			s.q = s.q[1:]
			s.mu.Unlock()
			select {
			case s.out <- c:
			case <-ctx.Done():
				return
			}
		}
	}
}

const (
	histTTL = 30 * time.Second
	bookTTL = time.Second
	fundTTL = 30 * time.Second
)

func newFanout(ctx context.Context, up Feed) *fanout {
	return &fanout{ctx: ctx, up: up, subs: map[string]*bcast{}, hist: map[string]cached[[]domain.Candle]{},
		book: map[string]cached[domain.Book]{}, fund: map[string]cached[[]domain.FundingPoint]{}}
}

// Candles serves arena warm-up from one upstream call per coin/interval.
func (f *fanout) Candles(ctx context.Context, coin string, iv domain.Interval, n int, end int64) ([]domain.Candle, error) {
	key := fmt.Sprintf("%s|%s|%d|%d", coin, iv, n, end)
	f.mu.Lock()
	if c, ok := f.hist[key]; ok && time.Since(c.at) < histTTL {
		f.mu.Unlock()
		return append([]domain.Candle(nil), c.v...), c.err
	}
	f.mu.Unlock()
	v, err := f.up.Candles(ctx, coin, iv, n, end)
	f.mu.Lock()
	f.hist[key] = cached[[]domain.Candle]{time.Now(), v, err}
	f.mu.Unlock()
	return append([]domain.Candle(nil), v...), err
}

// Funding caches identical funding queries.
func (f *fanout) Funding(ctx context.Context, coin string, start, end int64) ([]domain.FundingPoint, error) {
	key := fmt.Sprintf("%s|%d|%d", coin, start, end)
	f.mu.Lock()
	if c, ok := f.fund[key]; ok && time.Since(c.at) < fundTTL {
		f.mu.Unlock()
		return append([]domain.FundingPoint(nil), c.v...), c.err
	}
	f.mu.Unlock()
	v, err := f.up.Funding(ctx, coin, start, end)
	f.mu.Lock()
	f.fund[key] = cached[[]domain.FundingPoint]{time.Now(), v, err}
	f.mu.Unlock()
	return append([]domain.FundingPoint(nil), v...), err
}

// Book caches the book for one second (every entrant fills against the same
// snapshot on a bar close).
func (f *fanout) Book(ctx context.Context, coin string) (domain.Book, error) {
	f.mu.Lock()
	if c, ok := f.book[coin]; ok && time.Since(c.at) < bookTTL {
		f.mu.Unlock()
		return c.v, c.err
	}
	f.mu.Unlock()
	v, err := f.up.Book(ctx, coin)
	f.mu.Lock()
	f.book[coin] = cached[domain.Book]{time.Now(), v, err}
	f.mu.Unlock()
	return v, err
}

// Bars subscribes upstream once per coin/interval and broadcasts.
func (f *fanout) Bars(ctx context.Context, coin string, iv domain.Interval) (<-chan domain.Candle, error) {
	key := coin + "|" + string(iv)
	f.mu.Lock()
	b, ok := f.subs[key]
	if !ok {
		up, err := f.up.Bars(f.ctx, coin, iv)
		if err != nil {
			f.mu.Unlock()
			return nil, err
		}
		b = &bcast{}
		f.subs[key] = b
		go b.pump(f.ctx, up)
	}
	f.mu.Unlock()
	sq := &subQueue{sig: make(chan struct{}, 1), out: make(chan domain.Candle, 16)}
	b.mu.Lock()
	b.subs = append(b.subs, sq)
	b.mu.Unlock()
	sctx, cancel := context.WithCancel(f.ctx)
	go func() {
		select {
		case <-ctx.Done():
		case <-sctx.Done():
		}
		cancel()
		b.mu.Lock()
		for i, x := range b.subs {
			if x == sq {
				b.subs = append(b.subs[:i], b.subs[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
	}()
	go sq.deliver(sctx)
	return sq.out, nil
}

func (b *bcast) pump(ctx context.Context, up <-chan domain.Candle) {
	for {
		select {
		case <-ctx.Done():
			return
		case c, ok := <-up:
			if !ok {
				return
			}
			b.mu.Lock()
			for _, sq := range b.subs {
				sq.push(c)
			}
			b.mu.Unlock()
		}
	}
}
