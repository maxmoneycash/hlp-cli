package hyperliquid

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Limiter is a weight-aware token bucket for Hyperliquid's rate limits.
// Capacity 1200 weight per minute per IP, refilling continuously. It is safe
// for concurrent use and is meant to be shared by every client in a process.
//
// A persistent limiter (NewPersistentLimiter) also shares its bucket with
// other hlp processes through a small state file, because Hyperliquid
// counts weight per IP: an agent running `hlp` ten times in a row must not
// believe it has ten fresh minutes of budget (PERFORMANCE #3).
type Limiter struct {
	capacity float64
	per      time.Duration

	mu     sync.Mutex
	tokens float64
	last   time.Time
	now    func() time.Time

	// path is the shared state file ("" = in-process only).
	path string
}

// NewLimiter returns a bucket of `capacity` weight refilled over `per`.
// The bucket starts full.
func NewLimiter(capacity int, per time.Duration) *Limiter {
	if capacity <= 0 {
		capacity = 1
	}
	if per <= 0 {
		per = time.Minute
	}
	return &Limiter{capacity: float64(capacity), per: per, tokens: float64(capacity), now: time.Now}
}

// NewPersistentLimiter is NewLimiter whose bucket lives in path (for
// example <CacheDir>/ratelimit-mainnet.json), so every hlp process on the
// machine draws from one budget. Each change is read-modify-written under
// an exclusive file lock (flock on Unix). An unreadable or missing file
// starts full; an unwritable one degrades to an in-process bucket.
func NewPersistentLimiter(path string, capacity int, per time.Duration) *Limiter {
	l := NewLimiter(capacity, per)
	if path == "" {
		return l
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return l
	}
	l.path = path
	return l
}

// limiterState is the on-disk bucket.
type limiterState struct {
	Tokens float64 `json:"tokens"`
	Last   int64   `json:"last"` // unix nanoseconds
}

// rate is tokens per nanosecond.
func (l *Limiter) rate() float64 { return l.capacity / float64(l.per) }

// refill must be called with mu held.
func (l *Limiter) refill() {
	now := l.now()
	if !l.last.IsZero() {
		l.tokens += float64(now.Sub(l.last)) * l.rate()
		if l.tokens > l.capacity {
			l.tokens = l.capacity
		}
	}
	l.last = now
}

// update runs f on the refilled bucket with mu held. For a persistent
// limiter the bucket is first loaded from the shared file (other processes
// may have spent from it) and written back afterwards, all under the file
// lock. f returns whether it changed the bucket.
func (l *Limiter) update(f func() bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.path == "" {
		l.refill()
		f()
		return
	}
	fh, err := os.OpenFile(l.path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		l.refill()
		f()
		return
	}
	defer fh.Close()
	unlock := lockFile(fh)
	defer unlock()
	var st limiterState
	if b, err := readAll(fh); err == nil && len(b) > 0 && json.Unmarshal(b, &st) == nil && st.Last > 0 {
		l.tokens = min(st.Tokens, l.capacity)
		l.last = time.Unix(0, st.Last)
	}
	l.refill()
	if !f() {
		return
	}
	b, _ := json.Marshal(limiterState{Tokens: l.tokens, Last: l.last.UnixNano()})
	if err := fh.Truncate(0); err == nil {
		_, _ = fh.WriteAt(b, 0)
	}
}

func readAll(f *os.File) ([]byte, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	n := min(fi.Size(), 4096)
	b := make([]byte, n)
	m, err := f.ReadAt(b, 0)
	if m == int(n) {
		return b, nil
	}
	return b[:m], err
}

// Wait blocks until `weight` tokens are available or ctx ends. A weight
// larger than the capacity is clamped to the capacity so it can still run.
func (l *Limiter) Wait(ctx context.Context, weight int) error {
	if l == nil || weight <= 0 {
		return ctx.Err()
	}
	w := float64(weight)
	if w > l.capacity {
		w = l.capacity
	}
	for {
		var wait time.Duration
		took := false
		l.update(func() bool {
			if l.tokens >= w {
				l.tokens -= w
				took = true
				return true
			}
			wait = time.Duration((w - l.tokens) / l.rate())
			return false
		})
		if took {
			return nil
		}
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		if err := sleepCtx(ctx, wait); err != nil {
			return err
		}
	}
}

// Penalize empties the bucket and pushes it negative, so every goroutine
// (and, for a persistent limiter, every process) waits about d before the
// next request. Client.Info calls it on an HTTP 429: the server's view of
// our budget is the truth, and the local estimate was wrong.
func (l *Limiter) Penalize(d time.Duration) {
	if l == nil || d <= 0 {
		return
	}
	l.update(func() bool {
		l.tokens = min(l.tokens, 0) - float64(d)*l.rate()
		return true
	})
}

// Weight returns the documented request weight for an info request type,
// before the per-item surcharge. Items-based surcharges (candles: +1 per 60,
// fills/funding/trades: +1 per 20) are charged after the response via Charge.
func Weight(infoType string) int {
	switch infoType {
	case "l2Book", "allMids", "clearinghouseState", "orderStatus", "spotClearinghouseState", "exchangeStatus":
		return 2
	case "userRole":
		return 60
	}
	return 20
}

// Charge deducts a post-response surcharge without blocking (may go negative).
func (l *Limiter) Charge(weight int) {
	if l == nil || weight <= 0 {
		return
	}
	l.update(func() bool {
		l.tokens -= float64(weight)
		return true
	})
}

// Available reports the current number of tokens (may be negative after
// Charge or Penalize). Useful for status bars and tests.
func (l *Limiter) Available() float64 {
	if l == nil {
		return 0
	}
	var v float64
	l.update(func() bool { v = l.tokens; return false })
	return v
}

// Capacity reports the bucket size (weight per period).
func (l *Limiter) Capacity() float64 {
	if l == nil {
		return 0
	}
	return l.capacity
}

// Shared reports whether the bucket is shared with other processes.
func (l *Limiter) Shared() bool { return l != nil && l.path != "" }
