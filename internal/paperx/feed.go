package paperx

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/hyperliquid"
	"github.com/maxmoneycash/hlp-cli/internal/hyperliquid/ws"
)

// Event is one market event for the engine, stamped with its local
// receive time. A tape of Events replays deterministically through Apply.
type Event struct {
	At     int64          `json:"at"`
	Book   *domain.Book   `json:"book,omitempty"`
	Trades []domain.Trade `json:"trades,omitempty"`
	Coin   string         `json:"coin,omitempty"` // for Mark / Funding
	Mark   string         `json:"mark,omitempty"`
	Oracle string         `json:"oracle,omitempty"`
	// Funding settles one hour: FundingHour (ms) at FundingRate.
	FundingHour int64  `json:"fundingHour,omitempty"`
	FundingRate string `json:"fundingRate,omitempty"`
}

// Apply feeds one event to the engine.
func (e *Engine) Apply(ev Event) {
	switch {
	case ev.Book != nil:
		e.OnBook(*ev.Book, ev.At)
	case len(ev.Trades) > 0:
		e.OnTrades(ev.Trades, ev.At)
	case ev.FundingHour > 0:
		e.OnFunding(ev.Coin, ev.FundingHour, decimal.RequireFromString(ev.FundingRate), decimal.RequireFromString(ev.Oracle))
	case ev.Mark != "":
		e.OnMark(ev.Coin, decimal.RequireFromString(ev.Mark), ev.At)
	}
}

// Seed installs a book without running anything on it (a runner keeps
// books across flushes; a fresh engine starts from them).
func (e *Engine) Seed(b Book) { e.books[b.Coin] = &b }

// FundingSource returns settled hourly funding for coin in [start, end].
type FundingSource func(ctx context.Context, coin string, start, end int64) ([]domain.FundingPoint, error)

// Runner drives one stored paper account from the live WebSocket feed:
// full-depth l2Book, trades and activeAssetCtx (mark and oracle) per coin
// the account has orders or positions on, plus settled funding once per
// hour boundary from Funding. Events are buffered and applied under the
// account lock every Flush, then OnUpdate is called. It subscribes to new
// coins as orders appear and never sends anything to /exchange.
type Runner struct {
	Store    Store
	ID       string
	Stream   ws.Stream
	Config   Config
	Funding  FundingSource // nil = no funding while running
	Now      func() time.Time
	Flush    time.Duration // default 1 s
	OnUpdate func(e *Engine)

	mu      sync.Mutex
	buf     []Event
	books   map[string]Book
	oracle  map[string]decimal.Decimal
	subs    map[string]func()
	asked   map[string]int64 // coin -> last funding read (ms)
	prints  map[printKey]decimal.Decimal
	saved   time.Time
	lastErr error
}

// Run follows the feed until ctx ends.
func (r *Runner) Run(ctx context.Context) error {
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Flush <= 0 {
		r.Flush = time.Second
	}
	r.books, r.oracle, r.subs, r.asked = map[string]Book{}, map[string]decimal.Decimal{}, map[string]func(){}, map[string]int64{}
	defer func() {
		r.mu.Lock()
		for _, c := range r.subs {
			c()
		}
		r.mu.Unlock()
	}()
	tick := time.NewTicker(r.Flush)
	defer tick.Stop()
	if err := r.flush(ctx); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			_ = r.flush(context.Background())
			return ctx.Err()
		case <-tick.C:
			if err := r.flush(ctx); err != nil {
				return err
			}
		}
	}
}

func (r *Runner) push(ev Event) {
	r.mu.Lock()
	r.buf = append(r.buf, ev)
	r.mu.Unlock()
}

// follow subscribes to coin's book, trades and asset context.
func (r *Runner) follow(ctx context.Context, coin string) error {
	sctx, cancel := context.WithCancel(ctx)
	books, c1, err := r.raw(sctx, ws.Subscription{Type: "l2Book", Coin: coin})
	if err != nil {
		cancel()
		return err
	}
	trades, c2, err := ws.Trades(sctx, r.Stream, coin)
	if err != nil {
		c1()
		cancel()
		return err
	}
	ctxs, c3, err := r.raw(sctx, ws.Subscription{Type: "activeAssetCtx", Coin: coin})
	if err != nil {
		c1()
		c2()
		cancel()
		return err
	}
	r.subs[coin] = func() { c1(); c2(); c3(); cancel() }
	go func() {
		for raw := range books {
			var b hyperliquid.L2Book
			if json.Unmarshal(raw, &b) == nil {
				db := hyperliquid.ToBook(b)
				r.push(Event{At: r.Now().UnixMilli(), Book: &db})
			}
		}
	}()
	go func() {
		for ts := range trades {
			r.push(Event{At: r.Now().UnixMilli(), Trades: ts})
		}
	}()
	go func() {
		for raw := range ctxs {
			var w struct {
				Coin string `json:"coin"`
				Ctx  struct {
					MarkPx   hyperliquid.Dec `json:"markPx"`
					OraclePx hyperliquid.Dec `json:"oraclePx"`
				} `json:"ctx"`
			}
			if json.Unmarshal(raw, &w) != nil || w.Ctx.MarkPx == "" {
				continue
			}
			r.push(Event{At: r.Now().UnixMilli(), Coin: coin, Mark: string(w.Ctx.MarkPx), Oracle: string(w.Ctx.OraclePx)})
		}
	}()
	return nil
}

// raw subscribes and yields each frame's data.
func (r *Runner) raw(ctx context.Context, sub ws.Subscription) (<-chan json.RawMessage, func(), error) {
	evs, cancel, err := r.Stream.Subscribe(ctx, sub)
	if err != nil {
		return nil, func() {}, err
	}
	out := make(chan json.RawMessage, 64)
	go func() {
		defer close(out)
		for ev := range evs {
			if ev.Err != nil {
				return
			}
			select {
			case out <- ev.Data:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, cancel, nil
}

func (r *Runner) flush(ctx context.Context) error {
	r.mu.Lock()
	buf := r.buf
	r.buf = nil
	r.mu.Unlock()
	if len(buf) == 0 && !r.saved.IsZero() && r.Now().Sub(r.saved) < time.Minute {
		return nil // nothing new: no disk write (funding is still checked each minute)
	}
	r.saved = r.Now()
	var coins []string
	var eng *Engine
	_, err := r.Store.Update(r.ID, nil, func(st *State) error {
		e := New(st, r.Config)
		for _, b := range r.books {
			e.Seed(b)
		}
		if r.prints != nil {
			e.prints = r.prints
		}
		for _, ev := range buf {
			if ev.Oracle != "" {
				if o, err := decimal.NewFromString(ev.Oracle); err == nil {
					r.oracle[ev.Coin] = o
				}
			}
			e.Apply(ev)
			if ev.Book != nil {
				if b, ok := e.Book(ev.Book.Coin); ok {
					r.books[b.Coin] = b
				}
			}
		}
		r.prints = e.prints
		r.settleFunding(ctx, e)
		coins = e.Coins()
		eng = e
		return nil
	})
	if err != nil {
		return err
	}
	for _, c := range coins {
		if _, ok := r.subs[c]; !ok {
			if err := r.follow(ctx, c); err != nil {
				r.lastErr = err
			}
		}
	}
	if r.OnUpdate != nil && eng != nil {
		r.OnUpdate(eng)
	}
	return nil
}

// settleFunding asks for settled funding once an hour boundary has passed
// (a minute later, so the print exists) for every open position.
func (r *Runner) settleFunding(ctx context.Context, e *Engine) {
	if r.Funding == nil {
		return
	}
	now := r.Now().UnixMilli()
	hour := now - now%3_600_000
	if now-hour < 60_000 {
		return
	}
	for coin, p := range e.State().Positions {
		if max(p.FundedTo, p.Opened) >= hour || now-r.asked[coin] < 5*60_000 {
			continue // settled, or asked within 5 minutes (at most 12 reads an hour)
		}
		r.asked[coin] = now
		pts, err := r.Funding(ctx, coin, e.FundingFrom(coin), now)
		if err != nil {
			r.lastErr = err
			continue
		}
		ox := r.oracle[coin]
		e.CatchUpFunding(coin, pts, func(int64) decimal.Decimal { return ox })
	}
}

// Err is the last non-fatal feed error (a failed subscription or funding
// read), for a status line.
func (r *Runner) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastErr
}
