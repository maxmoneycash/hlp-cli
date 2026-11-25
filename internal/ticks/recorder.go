package ticks

import (
	"sync"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// seenWindow bounds the tids remembered for dedupe (a reconnect snapshot
// or a recentTrades seed is far smaller).
const seenWindow = 50_000

// tidSet is a bounded set of recent tids.
type tidSet struct {
	m    map[uint64]struct{}
	ring []uint64
	pos  int
}

func newTidSet(n int) *tidSet {
	return &tidSet{m: make(map[uint64]struct{}, n), ring: make([]uint64, 0, n)}
}

func (s *tidSet) has(t uint64) bool { _, ok := s.m[t]; return ok }

func (s *tidSet) add(t uint64) {
	if t == 0 || s.has(t) {
		return
	}
	if len(s.ring) < cap(s.ring) {
		s.ring = append(s.ring, t)
	} else {
		delete(s.m, s.ring[s.pos])
		s.ring[s.pos] = t
		s.pos = (s.pos + 1) % len(s.ring)
	}
	s.m[t] = struct{}{}
}

// Recorder turns raw prints into stored trades for one coin: it drops tids
// it has seen, marks gaps, and appends to the store when it holds the
// writer lease (otherwise it only keeps the new trades in memory for the
// caller; the process that holds the lease records them).
//
// A gap is marked on the first new trade when continuity cannot be shown:
// a seed or the first frame of a new socket that shares no tid with what
// came before. A snapshot that overlaps the known tail proves nothing was
// missed, so a quick reconnect or restart is not a gap.
type Recorder struct {
	mu        sync.Mutex
	store     *Store // nil: memory only
	seen      *tidSet
	history   bool  // some trade came before (stored or seen)
	gen       int64 // socket generation of the last live batch
	mem       []Rec // trades this process could not store (lease or write failure)
	err       error // last append failure
	retryAt   time.Time
	reconcile bool // pending records may already have been stored by another writer
}

// NewRecorder opens a recorder over store (nil = memory only), taking the
// writer lease when free and loading the newest stored day's tids.
func NewRecorder(store *Store) *Recorder {
	r := &Recorder{store: store, seen: newTidSet(seenWindow)}
	if store == nil {
		return r
	}
	r.reconcile = !store.TryLease()
	days, _ := store.Days()
	// Load oldest first so the bounded dedupe set retains the newest tail.
	for i := max(0, len(days)-2); i < len(days); i++ {
		rs, _ := store.LoadDay(days[i])
		if len(rs) > seenWindow {
			rs = rs[len(rs)-seenWindow:]
		}
		for _, x := range rs {
			r.seen.add(x.TID)
		}
		if len(rs) > 0 {
			r.history = true
		}
	}
	return r
}

// Writer reports whether this recorder stores what it admits.
func (r *Recorder) Writer() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.store != nil && r.store.Writer() && r.err == nil
}

// Err is the last store failure (nil when every append succeeded).
func (r *Recorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// Unstored returns the trades admitted but not written,
// oldest first.
func (r *Recorder) Unstored() []Rec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Rec(nil), r.mem...)
}

// Seed admits a REST snapshot (recentTrades). Order does not matter.
func (r *Recorder) Seed(ts []domain.Trade) []Rec {
	cp := make([]Rec, len(ts))
	for i, t := range ts {
		cp[i] = FromTrade(t)
	}
	// recentTrades is newest first: flip it so equal times keep their
	// execution order, then sort by time.
	if len(cp) > 1 && cp[0].Time > cp[len(cp)-1].Time {
		for i, j := 0, len(cp)-1; i < j; i, j = i+1, j-1 {
			cp[i], cp[j] = cp[j], cp[i]
		}
	}
	SortRecs(cp)
	return r.admit(cp, true)
}

// Ingest admits one live frame in arrival order. gen is the socket
// generation (ws.Event.Gen); a new one means frames may have been missed.
func (r *Recorder) Ingest(ts []domain.Trade, gen int64) []Rec {
	cp := make([]Rec, len(ts))
	for i, t := range ts {
		cp[i] = FromTrade(t)
	}
	r.mu.Lock()
	check := gen != r.gen
	r.gen = gen
	r.mu.Unlock()
	return r.admit(cp, check)
}

func (r *Recorder) admit(rs []Rec, check bool) []Rec {
	r.mu.Lock()
	defer r.mu.Unlock()
	overlap := false
	out := rs[:0]
	for _, x := range rs {
		if x.TID != 0 && r.seen.has(x.TID) {
			overlap = true
			continue
		}
		r.seen.add(x.TID)
		out = append(out, x)
	}
	if len(out) == 0 {
		r.persistLocked()
		return nil
	}
	if check && r.history && !overlap {
		out[0].Gap = true
	}
	r.history = true
	out = append([]Rec(nil), out...)
	r.mem = append(r.mem, out...)
	r.persistLocked()
	if len(r.mem) > 4*seenWindow {
		r.mem = append([]Rec(nil), r.mem[len(r.mem)-2*seenWindow:]...)
		// If storage remains unavailable, a rebuild must not join the saved
		// tail to this truncated memory buffer as if no prints were lost.
		r.mem[0].Gap = true
	}
	return out
}

// Retry on arriving trades, at most once per five seconds after a failure or
// while following another writer. No daemon or polling job is needed.
func (r *Recorder) persistLocked() {
	if r.store == nil || r.store.o.Now().Before(r.retryAt) {
		return
	}
	now := r.store.o.Now()
	if !r.store.Writer() {
		r.reconcile = true
		if !r.store.TryLease() {
			r.retryAt = now.Add(5 * time.Second)
			return
		}
	}
	if r.reconcile {
		pending, err := r.store.Missing(r.mem)
		if err != nil {
			r.err = err
			r.retryAt = now.Add(5 * time.Second)
			return
		}
		r.mem = pending
	}
	if err := r.store.Append(r.mem); err != nil {
		r.err = err
		r.reconcile = true // an I/O failure may have written part of the batch
		r.retryAt = now.Add(5 * time.Second)
		return
	}
	r.mem, r.err, r.reconcile, r.retryAt = nil, nil, false, time.Time{}
}
