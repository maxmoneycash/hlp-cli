package hyperliquid

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// Loading older candles when a chart is panned left.
//
// candleSnapshot serves only the latest MaxCandles buckets of an interval, so
// a chart that opens with a few hundred bars can grow back to MaxCandles and
// no further. PageHistory walks back through that range one bounded window
// at a time. HistoryPager wraps it in the per-chart bookkeeping a UI needs:
// a single request at a time, stale results dropped after a symbol or
// interval switch, an end-of-history flag, and a growing pause after
// failures. Retries are driven by the user panning, never by a timer.

// CandleWindowFetch returns the candles whose open time lies in the
// inclusive window [from, to] (ms). Rows outside the window are tolerated:
// PageHistory clips them.
type CandleWindowFetch func(ctx context.Context, from, to int64) ([]domain.Candle, error)

// CandleSource is the part of Reader the pager needs.
type CandleSource interface {
	Candles(ctx context.Context, coin string, iv domain.Interval, n int, end int64) ([]domain.Candle, error)
}

// WindowFetch adapts a Reader (or anything with Candles) to a
// CandleWindowFetch for one coin and interval.
func WindowFetch(src CandleSource, coin string, iv domain.Interval) CandleWindowFetch {
	step := iv.Duration().Milliseconds()
	return func(ctx context.Context, from, to int64) ([]domain.Candle, error) {
		if step <= 0 {
			return nil, badInterval(iv)
		}
		buckets := max(int((to-from)/step)+1, 1)
		return src.Candles(ctx, coin, iv, buckets, to)
	}
}

// Pager defaults.
const (
	// PageSizeDefault is the most buckets one request asks for (the
	// candleSnapshot cap).
	PageSizeDefault = MaxCandles
	// PageMaxEmpty consecutive empty pages mark the history exhausted.
	PageMaxEmpty = 3
	// PageLoadMore is how many bars a chart asks for when the user pans
	// near the oldest bar.
	PageLoadMore = 1000
	// PageThresholdMin is the minimum bars-before-the-view that triggers a
	// load (the threshold is max(PageThresholdMin, visible bars)).
	PageThresholdMin = 50
	// PageBackoffMin and PageBackoffMax bound the retry backoff.
	PageBackoffMin = time.Second
	PageBackoffMax = 30 * time.Second
)

// PageOpts tunes PageHistory. Zero values take the defaults.
type PageOpts struct {
	PageSize int   // max buckets per request (default PageSizeDefault)
	MaxEmpty int   // consecutive empty pages that end the history (default PageMaxEmpty)
	Floor    int64 // oldest open time worth asking for (ms); 0 = none
}

// PageResult is what PageHistory collected.
type PageResult struct {
	Bars      []domain.Candle `json:"bars"`      // oldest first, deduplicated by T, all < toMs+1
	Exhausted bool            `json:"exhausted"` // nothing older exists (or is served)
	Requests  int             `json:"requests"`  // fetches made
}

func badInterval(iv domain.Interval) error {
	return &APIError{Code: "BAD_REQUEST", Body: "invalid interval " + string(iv)}
}

// bucketOpen rounds a non-negative time down to the open of its bucket.
func bucketOpen(t, step int64) int64 {
	if t < 0 {
		return t
	}
	return t - t%step
}

// cursor is the position of a backwards walk: the newest open time still
// wanted (hi) and how many empty windows have been seen in a row.
type cursor struct {
	step   int64
	floor  int64 // 0 = walk down to the epoch
	size   int   // full window, in buckets
	hi     int64
	misses int
}

// beyondFloor reports that nothing at or before hi can be served.
func (c *cursor) beyondFloor() bool {
	return c.hi < 0 || (c.floor > 0 && c.hi < c.floor)
}

// window is the next inclusive range to request, sized for need more bars.
// After an empty window the full size is used so a gap in the data (a
// weekend, an outage) is crossed in as few requests as possible. edge is
// true when the window was clamped to the floor or the epoch, so nothing
// older remains after it.
func (c *cursor) window(need int) (lo, hi int64, edge bool) {
	buckets := c.size
	if c.misses == 0 {
		buckets = min(buckets, need)
	}
	lo = bucketOpen(c.hi, c.step) - c.step*int64(buckets-1)
	if lo <= 0 {
		lo, edge = 0, true
	}
	if c.floor > 0 && lo <= c.floor {
		lo, edge = c.floor, true
	}
	return lo, c.hi, edge
}

// collector holds the bars gathered so far, unique by open time.
type collector struct {
	bars []domain.Candle
	at   map[int64]int // open time -> index in bars
}

// take keeps the rows of one window that really fall inside [lo, hi] and
// returns the oldest open time kept, or false when none was. Clipping means
// a server that echoes neighbouring bars can never stall the walk.
func (k *collector) take(rows []domain.Candle, lo, hi int64) (int64, bool) {
	oldest, found := hi, false
	for _, r := range rows {
		if r.T < lo || r.T > hi {
			continue
		}
		if i, dup := k.at[r.T]; dup {
			k.bars[i] = r
		} else {
			k.at[r.T] = len(k.bars)
			k.bars = append(k.bars, r)
		}
		if !found || r.T < oldest {
			oldest, found = r.T, true
		}
	}
	return oldest, found
}

func (k *collector) sorted() []domain.Candle {
	out := slices.Clone(k.bars)
	if out == nil {
		out = []domain.Candle{}
	}
	slices.SortFunc(out, func(a, b domain.Candle) int {
		switch {
		case a.T < b.T:
			return -1
		case a.T > b.T:
			return 1
		}
		return 0
	})
	return out
}

// PageHistory pages backwards from toMs (inclusive) until it holds
// countBack bars or the history ends. Each request covers at most PageSize
// buckets ending at the bucket that holds the current position; rows outside
// that window are dropped, and the next window ends 1 ms before the oldest
// bar kept. An empty window is treated as a gap and the walk continues just
// below it. MaxEmpty empty windows in a row, or a window that reaches Floor
// or time 0, end the history. The context is checked before and after every
// fetch; on error the bars gathered so far are returned with it.
func PageHistory(ctx context.Context, fetch CandleWindowFetch, iv domain.Interval, toMs int64, countBack int, o PageOpts) (PageResult, error) {
	step := iv.Duration().Milliseconds()
	if step <= 0 {
		return PageResult{}, badInterval(iv)
	}
	if countBack <= 0 {
		return PageResult{}, nil
	}
	if o.PageSize <= 0 || o.PageSize > PageSizeDefault {
		o.PageSize = PageSizeDefault
	}
	if o.MaxEmpty <= 0 {
		o.MaxEmpty = PageMaxEmpty
	}

	cur := cursor{step: step, floor: o.Floor, size: o.PageSize, hi: toMs}
	got := collector{at: map[int64]int{}}
	var res PageResult
	result := func(err error) (PageResult, error) {
		res.Bars = got.sorted()
		return res, err
	}

	for len(got.bars) < countBack {
		if err := ctx.Err(); err != nil {
			return result(err)
		}
		if cur.beyondFloor() {
			res.Exhausted = true
			break
		}
		lo, hi, edge := cur.window(countBack - len(got.bars))
		rows, err := fetch(ctx, lo, hi)
		res.Requests++
		if cerr := ctx.Err(); cerr != nil {
			return result(cerr) // a cancelled page is never kept
		}
		if err != nil {
			return result(err)
		}
		oldest, found := got.take(rows, lo, hi)
		if found {
			cur.misses = 0
			cur.hi = oldest - 1
		} else {
			cur.misses++
			cur.hi = lo - 1
		}
		if edge || cur.misses >= o.MaxEmpty {
			res.Exhausted = true
			break
		}
	}
	return result(nil)
}

// HistoryFloor is the oldest candle open time Hyperliquid still serves for
// iv at now: the most recent MaxCandles buckets.
func HistoryFloor(iv domain.Interval, now time.Time) int64 {
	step := iv.Duration().Milliseconds()
	if step <= 0 {
		return 0
	}
	return max(bucketOpen(now.UnixMilli(), step)-step*int64(MaxCandles-1), 0)
}

// MergeCandles merges older bars into cur (both oldest first): the result
// is sorted, deduplicated by T, and cur wins on a duplicate (it is the
// fresher copy of a bar, possibly the live one).
func MergeCandles(older, cur []domain.Candle) []domain.Candle {
	if len(older) == 0 {
		return cur
	}
	limit := int64(1<<63 - 1)
	if len(cur) > 0 {
		limit = cur[0].T
	}
	prefix := make([]domain.Candle, 0, len(older))
	for _, c := range older {
		if c.T < limit {
			prefix = append(prefix, c)
		}
	}
	slices.SortStableFunc(prefix, func(a, b domain.Candle) int {
		switch {
		case a.T < b.T:
			return -1
		case a.T > b.T:
			return 1
		}
		return 0
	})
	prefix = slices.CompactFunc(prefix, func(a, b domain.Candle) bool { return a.T == b.T })
	return append(prefix, cur...)
}

// HistoryKey identifies the series a request was made for. A result whose
// key is not the pager's current key is stale and is discarded.
type HistoryKey struct {
	Coin     string          `json:"coin"`
	Interval domain.Interval `json:"interval"`
	Gen      uint64          `json:"gen"`
}

// HistoryRequest is one load-more request. Run it off the UI thread (a
// tea.Cmd) and hand its HistoryPage to HistoryPager.Apply.
type HistoryRequest struct {
	Key   HistoryKey
	ToMs  int64
	Count int
	fetch CandleWindowFetch
	floor int64
}

// HistoryPage is the outcome of a HistoryRequest.
type HistoryPage struct {
	Key       HistoryKey
	Bars      []domain.Candle
	Exhausted bool
	Err       error
}

// Run fetches the page. It never touches pager state, so it is safe to run
// on any goroutine.
func (r HistoryRequest) Run(ctx context.Context) HistoryPage {
	res, err := PageHistory(ctx, r.fetch, r.Key.Interval, r.ToMs, r.Count, PageOpts{PageSize: r.Count, Floor: r.floor})
	partial := err != nil && len(res.Bars) > 0 && !errors.Is(err, context.Canceled)
	if partial {
		err = nil // keep what arrived; the next pan asks for the rest
	}
	return HistoryPage{Key: r.Key, Bars: res.Bars, Exhausted: res.Exhausted && err == nil, Err: err}
}

// HistoryState is what a chart shows at its left edge.
type HistoryState struct {
	Loading   bool      `json:"loading"`             // a request is in flight ("◂ loading")
	Exhausted bool      `json:"exhausted"`           // start of history reached
	RetryAt   time.Time `json:"retryAt,omitzero"`    // backing off after a failure until then
	LastErr   string    `json:"lastError,omitempty"` // the last failure, for a status line
}

// cooldown tracks failed loads: each failure in a row doubles the pause,
// within PageBackoffMin..PageBackoffMax, unless the server asks for longer.
type cooldown struct {
	streak int
	until  time.Time
	reason string
}

func (c *cooldown) fail(err error, now time.Time) {
	c.streak++
	wait := PageBackoffMin * time.Duration(1<<min(c.streak-1, 5))
	if hint, ok := RetryAfter(err); ok {
		wait = max(wait, hint)
	}
	c.until = now.Add(min(wait, PageBackoffMax))
	c.reason = err.Error()
}

func (c *cooldown) active(now time.Time) bool {
	return !c.until.IsZero() && now.Before(c.until)
}

// HistoryPager is the per-pane paging state. It is safe for concurrent use
// (Apply and Want hold a mutex), but it is meant to be driven from one UI
// loop: Want on every pan, Run in a command, Apply on its message.
type HistoryPager struct {
	mu    sync.Mutex
	src   CandleSource
	fetch CandleWindowFetch // overrides src (tests)
	key   HistoryKey
	busy  bool
	done  bool
	cool  cooldown
	// MaxBars caps the series (default MaxCandles: Hyperliquid serves no
	// more). LoadMore is the request size (default PageLoadMore).
	MaxBars  int
	LoadMore int
	// UseFloor stops at HistoryFloor instead of probing empty pages past
	// it (default true via NewHistoryPager).
	UseFloor bool
}

// NewHistoryPager returns a pager for coin/iv reading from src.
func NewHistoryPager(src CandleSource, coin string, iv domain.Interval) *HistoryPager {
	return &HistoryPager{src: src, key: HistoryKey{Coin: coin, Interval: iv, Gen: 1}, UseFloor: true}
}

// NewHistoryPagerFetch is NewHistoryPager with an explicit window fetch.
func NewHistoryPagerFetch(fetch CandleWindowFetch, coin string, iv domain.Interval) *HistoryPager {
	return &HistoryPager{fetch: fetch, key: HistoryKey{Coin: coin, Interval: iv, Gen: 1}}
}

// Reset switches the pager to a new series (symbol or interval change).
// Requests in flight become stale and their results are discarded.
func (p *HistoryPager) Reset(coin string, iv domain.Interval) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.key = HistoryKey{Coin: coin, Interval: iv, Gen: p.key.Gen + 1}
	p.busy, p.done, p.cool = false, false, cooldown{}
}

// Key is the current series key.
func (p *HistoryPager) Key() HistoryKey {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.key
}

// State reports loading/exhausted/backoff for the chart's edge marker.
func (p *HistoryPager) State() HistoryState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return HistoryState{Loading: p.busy, Exhausted: p.done, RetryAt: p.cool.until, LastErr: p.cool.reason}
}

// Threshold is the bars-before-the-view below which Want asks for more.
func Threshold(visible int) int { return max(PageThresholdMin, visible) }

func (p *HistoryPager) capacity() int {
	if p.MaxBars > 0 {
		return p.MaxBars
	}
	return MaxCandles
}

// Want decides whether to load older bars. candles is the series on screen
// (oldest first), barsBefore the number of bars left of the viewport and
// visible the bars in it. It returns a request and marks it in flight, or
// false when nothing should be fetched (enough bars, a request already in
// flight, exhausted, the series is at MaxBars, or backing off until
// RetryAt; a retry happens only on a later pan, never on a timer).
func (p *HistoryPager) Want(candles []domain.Candle, barsBefore, visible int, now time.Time) (HistoryRequest, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(candles) == 0 || p.busy || p.done || barsBefore >= Threshold(visible) || p.cool.active(now) {
		return HistoryRequest{}, false
	}
	room := p.capacity() - len(candles)
	if room <= 0 {
		p.done = true
		return HistoryRequest{}, false
	}
	count := p.LoadMore
	if count <= 0 {
		count = PageLoadMore
	}
	req := HistoryRequest{Key: p.key, ToMs: candles[0].T - 1, Count: min(count, room), fetch: p.fetch}
	if req.fetch == nil {
		req.fetch = WindowFetch(p.src, p.key.Coin, p.key.Interval)
	}
	if p.UseFloor {
		req.floor = HistoryFloor(p.key.Interval, now)
	}
	p.busy = true
	return req, true
}

// Apply merges a finished page into candles. It returns the new series and
// true when the page belonged to the current series, or candles unchanged
// and false for a stale page (superseded reads never touch the chart). A
// failure arms the backoff (1 s doubling to 30 s) and returns false.
func (p *HistoryPager) Apply(pg HistoryPage, candles []domain.Candle, now time.Time) ([]domain.Candle, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pg.Key != p.key {
		return candles, false
	}
	p.busy = false
	switch {
	case pg.Err == nil:
	case errors.Is(pg.Err, context.Canceled):
		return candles, false
	default:
		p.cool.fail(pg.Err, now)
		return candles, false
	}
	p.cool = cooldown{}
	merged := MergeCandles(pg.Bars, candles)
	if pg.Exhausted || len(pg.Bars) == 0 || len(merged) >= p.capacity() {
		p.done = true
	}
	return merged, true
}
