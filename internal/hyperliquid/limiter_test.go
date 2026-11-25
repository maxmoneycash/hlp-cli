package hyperliquid

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func (f *fakeHL) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, v := range f.hits {
		n += v
	}
	return n
}

func TestPenalizeDrainsBucket(t *testing.T) {
	l := NewLimiter(1200, time.Minute) // 20 per second
	l.Penalize(5 * time.Second)
	if got := l.Available(); got > -99 || got < -101 {
		t.Fatalf("after 5s penalty: %v, want about -100", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := l.Wait(ctx, 2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait during penalty: %v", err)
	}
}

// E-T5: after a 429 the bucket is empty for everyone, and eight concurrent
// Account calls make at most two requests while the penalty holds.
func TestRateLimit429DrainsAndHoldsBack(t *testing.T) {
	f := newFakeHL(t)
	c := f.client(t)
	c.opts.Limiter = NewLimiter(1200, time.Minute)
	c.penalty = 2 * time.Second
	c.maxRetries = 0
	var first atomic.Bool
	f.override = func(typ string, _ map[string]any) (int, []byte, bool) {
		if typ == "exchangeStatus" && !first.Swap(true) {
			return http.StatusTooManyRequests, []byte("too many"), true
		}
		return 0, nil, false
	}
	ctx := context.Background()
	if err := c.Registry().Refresh(ctx); err != nil { // metadata is cached in practice
		t.Fatal(err)
	}
	var st map[string]any
	err := c.Info(ctx, map[string]string{"type": "exchangeStatus"}, &st)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("429: %v", err)
	}
	d, ok := RetryAfter(err)
	if !ok || d != 2*time.Second {
		t.Fatalf("RetryAfter = %v %v", d, ok)
	}
	if got := c.opts.Limiter.Available(); got > 0 {
		t.Fatalf("Available after 429 = %v, want <= 0", got)
	}
	before := f.total()
	actx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.Account(actx, addrTrader) }()
	}
	wg.Wait()
	if n := f.total() - before; n > 2 {
		t.Fatalf("%d requests during the penalty, want <= 2: %v", n, f.hits)
	}
}

func TestRetryAfterHeaderAndServerErrors(t *testing.T) {
	f := newFakeHL(t)
	c := f.client(t)
	c.maxRetries = 0
	f.override = func(typ string, _ map[string]any) (int, []byte, bool) {
		return 503, []byte("down"), typ == "exchangeStatus"
	}
	var st map[string]any
	err := c.Info(context.Background(), map[string]string{"type": "exchangeStatus"}, &st)
	if d, ok := RetryAfter(err); !ok || d <= 0 {
		t.Fatalf("5xx RetryAfter = %v %v (%v)", d, ok, err)
	}
	if _, ok := RetryAfter(&APIError{Status: 422, Code: "BAD_REQUEST"}); ok {
		t.Fatal("422 is not retryable")
	}
	h := http.Header{}
	h.Set("Retry-After", "7")
	if d := retryAfterHeader(h); d != 7*time.Second {
		t.Fatalf("Retry-After header: %v", d)
	}
}

// E-T7: two processes (two limiters on one file) draw from one bucket.
func TestPersistentLimiterSharedAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ratelimit-mainnet.json")
	a := NewPersistentLimiter(path, 1200, time.Minute)
	b := NewPersistentLimiter(path, 1200, time.Minute)
	if !a.Shared() || !b.Shared() {
		t.Fatal("not shared")
	}
	ctx := context.Background()
	if err := a.Wait(ctx, 600); err != nil {
		t.Fatal(err)
	}
	if got := b.Available(); got > 601 {
		t.Fatalf("b sees %v after a spent 600", got)
	}
	b.Penalize(time.Second)
	if got := a.Available(); got > 0 {
		t.Fatalf("a sees %v after b's penalty", got)
	}
	// A fresh "process" starts from the file, not from a full bucket.
	c := NewPersistentLimiter(path, 1200, time.Minute)
	if got := c.Available(); got > 0 {
		t.Fatalf("new limiter starts at %v", got)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("limiter file mode %v %v", fi.Mode().Perm(), err)
		}
	}
}

func TestPersistentLimiterRefills(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rl.json")
	now := time.Unix(1_800_000_000, 0)
	a := NewPersistentLimiter(path, 60, time.Minute) // 1 per second
	a.now = func() time.Time { return now }
	a.Charge(60)
	now = now.Add(30 * time.Second)
	if got := a.Available(); got < 29.9 || got > 30.1 {
		t.Fatalf("refill: %v", got)
	}
}
