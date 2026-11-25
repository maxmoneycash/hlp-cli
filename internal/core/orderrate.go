package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// OrderRate counts risk-adding actions sent in a rolling minute for
// risk.max_orders_per_min, the brake on a runaway agent loop. With a path
// the timestamps live in a small file under the data dir,
// read-modify-written under an exclusive lock, so an agent that runs
// `hlp order` in a shell loop meets the same cap as one long MCP session.
// An unreadable file counts as empty; an unwritable one degrades to an
// in-process counter.
type OrderRate struct {
	Window time.Duration // default one minute

	mu    sync.Mutex
	times []time.Time
	path  string
}

// NewOrderRate returns a counter persisted at path ("" = in-process only).
func NewOrderRate(path string) *OrderRate {
	r := &OrderRate{Window: time.Minute}
	if path != "" && os.MkdirAll(filepath.Dir(path), 0o700) == nil {
		r.path = path
	}
	return r
}

func (r *OrderRate) window() time.Duration {
	if r.Window > 0 {
		return r.Window
	}
	return time.Minute
}

// update runs f over the timestamps still inside the window at now, under
// the in-process mutex and (when persisted) the file lock. f returns whether
// it changed the list.
func (r *OrderRate) update(now time.Time, f func() bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var fh *os.File
	if r.path != "" {
		if h, err := os.OpenFile(r.path, os.O_RDWR|os.O_CREATE, 0o600); err == nil {
			fh = h
			defer fh.Close()
			defer lockFile(fh)()
			var ms []int64
			if b, err := readSmall(fh); err == nil && len(b) > 0 && json.Unmarshal(b, &ms) == nil {
				r.times = r.times[:0]
				for _, m := range ms {
					r.times = append(r.times, time.UnixMilli(m))
				}
			}
		}
	}
	cut := now.Add(-r.window())
	kept := r.times[:0]
	for _, t := range r.times {
		if t.After(cut) && !t.After(now.Add(r.window())) { // drop stale and absurd future stamps
			kept = append(kept, t)
		}
	}
	r.times = kept
	if !f() || fh == nil {
		return
	}
	ms := make([]int64, len(r.times))
	for i, t := range r.times {
		ms[i] = t.UnixMilli()
	}
	b, _ := json.Marshal(ms)
	if err := fh.Truncate(0); err == nil {
		_, _ = fh.WriteAt(b, 0)
	}
}

// Count returns how many actions were taken in the window ending at now.
func (r *OrderRate) Count(now time.Time) int {
	if r == nil {
		return 0
	}
	n := 0
	r.update(now, func() bool { n = len(r.times); return false })
	return n
}

// Take records one action at now when fewer than limit were taken in the
// window, atomically. It returns the count before the attempt and whether
// the action was recorded. limit <= 0 always records.
func (r *OrderRate) Take(now time.Time, limit int) (count int, ok bool) {
	if r == nil {
		return 0, true
	}
	r.update(now, func() bool {
		count = len(r.times)
		if limit > 0 && count >= limit {
			return false
		}
		r.times = append(r.times, now)
		ok = true
		return true
	})
	return count, ok
}

func readSmall(f *os.File) ([]byte, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	n := min(fi.Size(), 64<<10)
	b := make([]byte, n)
	m, err := f.ReadAt(b, 0)
	if int64(m) == n {
		return b, nil
	}
	return b[:m], err
}
