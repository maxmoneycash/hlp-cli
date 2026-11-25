package ticks

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

func TestSharedCapacityIncludesActiveDayAndConcurrentWriters(t *testing.T) {
	root := t.TempDir()
	const capBytes = 2*int64(len(magic)) + 5*recSize
	var stores []*Store
	for _, coin := range []string{"BTC", "ETH"} {
		s, err := Open(root, coin, Options{Now: clock(time.UnixMilli(t0)), MaxBytes: capBytes})
		if err != nil || !s.TryLease() {
			t.Fatalf("open %s: %v", coin, err)
		}
		defer s.Close()
		stores = append(stores, s)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for _, s := range stores {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			for i := uint64(1); i <= 10; i++ {
				if err := s.Append(recs(tr(int64(i), i, 100, 1, B))); err != nil {
					errs <- err
				}
			}
		}(s)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, ErrCapacity) {
			t.Fatal(err)
		}
	}
	var bytes, count int64
	for _, s := range stores {
		st, err := s.Stats()
		if err != nil {
			t.Fatal(err)
		}
		bytes += st.Bytes
		count += st.Trades
	}
	if bytes > capBytes || count != 5 {
		t.Fatalf("budget %d: stored %d bytes / %d trades", capBytes, bytes, count)
	}
}

func TestRecorderPreservesRefusedWritesAndResumes(t *testing.T) {
	now := time.UnixMilli(t0)
	s, _ := Open(t.TempDir(), "BTC", Options{Now: func() time.Time { return now }, MaxBytes: int64(len(magic)) + recSize})
	defer s.Close()
	r := NewRecorder(s)
	r.Ingest([]domain.Trade{tr(0, 1, 100, 1, B)}, 1)
	// This print has a real socket gap, which must survive the failed append.
	r.Ingest([]domain.Trade{tr(1, 2, 101, 1, S)}, 2)
	if !errors.Is(r.Err(), ErrCapacity) || r.Writer() || !s.Writer() {
		t.Fatalf("full store: writer=%v lease=%v err=%v", r.Writer(), s.Writer(), r.Err())
	}
	pending := r.Unstored()
	if len(pending) != 1 || pending[0].TID != 2 || !pending[0].Gap {
		t.Fatalf("lost refused trade/gap: %+v", pending)
	}
	// A rebuild still contains both actual prints while persistence is blocked.
	bars, _, err := Series(s, pending, Spec{KindTicks, 10}, 0, DayOf(t0))
	if err != nil || len(bars) != 2 || bars[1].LastTID != 2 || !bars[1].Gap {
		t.Fatalf("memory rebuild: %+v, %v", bars, err)
	}
	// Simulate the owner raising the budget. An overlapping frame can drive
	// retry/takeover without inventing or admitting another print.
	s.o.MaxBytes += 2 * recSize
	now = now.Add(5 * time.Second)
	r.Ingest([]domain.Trade{tr(1, 2, 101, 1, S)}, 2)
	stored, _ := s.LoadDay(DayOf(t0))
	if r.Err() != nil || !r.Writer() || len(r.Unstored()) != 0 || len(stored) != 2 || !stored[1].Gap {
		t.Fatalf("recovery: stored=%+v pending=%+v err=%v", stored, r.Unstored(), r.Err())
	}
}

func TestRecorderTakesOverLeaseWithoutDuplicatingOtherWriter(t *testing.T) {
	root := t.TempDir()
	now := time.UnixMilli(t0)
	opts := Options{Now: func() time.Time { return now }}
	a, _ := Open(root, "BTC", opts)
	defer a.Close()
	writer := NewRecorder(a)
	writer.Ingest([]domain.Trade{tr(0, 1, 100, 1, B)}, 1)
	b, _ := Open(root, "BTC", opts)
	defer b.Close()
	follower := NewRecorder(b)
	shared := []domain.Trade{tr(0, 1, 100, 1, B), tr(1, 2, 101, 1, S)}
	follower.Ingest(shared, 1)
	writer.Ingest(shared, 1)
	if follower.Writer() || len(follower.Unstored()) != 1 {
		t.Fatal("follower must retain the observed print while lease is held")
	}
	a.Close()
	now = now.Add(5 * time.Second)
	follower.Ingest([]domain.Trade{tr(2, 3, 102, 1, B)}, 1)
	stored, _ := b.LoadDay(DayOf(t0))
	if !follower.Writer() || follower.Err() != nil || len(follower.Unstored()) != 0 || len(stored) != 3 {
		t.Fatalf("takeover: writer=%v stored=%+v err=%v", follower.Writer(), stored, follower.Err())
	}
	for i, r := range stored {
		if r.TID != uint64(i+1) || r.Gap {
			t.Fatalf("takeover duplicated/reordered or invented a gap: %+v", stored)
		}
	}
}

func TestRecorderRetriesPartialAppendWithoutDuplicateTrades(t *testing.T) {
	root := t.TempDir()
	now := time.UnixMilli(t0)
	s, _ := Open(root, "BTC", Options{Now: func() time.Time { return now }})
	defer s.Close()
	r := NewRecorder(s)
	// Force the second day's append to fail after the first day's succeeds.
	blocked := filepath.Join(s.Dir(), dayName(DayOf(t0)+1))
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	input := []domain.Trade{tr(0, 1, 100, 1, B), tr(DayMs, 2, 101, 1, S)}
	r.Ingest(input, 1)
	if r.Err() == nil || len(r.Unstored()) != 2 {
		t.Fatalf("partial failure lost pending records: %v %+v", r.Err(), r.Unstored())
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Second)
	r.Ingest(input, 1)
	st, _ := s.Stats()
	if r.Err() != nil || st.Trades != 2 || len(r.Unstored()) != 0 {
		t.Fatalf("retry duplicated partial write: %+v, %v", st, r.Err())
	}
}

func TestAppendReopensDayRemovedBySharedBudgetPrune(t *testing.T) {
	root := t.TempDir()
	now := time.UnixMilli(t0)
	a, _ := Open(root, "BTC", Options{Now: clock(now), MaxBytes: 100})
	defer a.Close()
	a.TryLease()
	if err := a.Append(recs(tr(-DayMs, 1, 100, 1, B))); err != nil {
		t.Fatal(err)
	}
	b, _ := Open(root, "ETH", Options{Now: clock(now), MaxBytes: 100})
	defer b.Close()
	b.TryLease()
	if err := b.Append(recs(tr(0, 2, 100, 1, B), tr(1, 3, 101, 1, B))); err != nil {
		t.Fatal(err)
	}
	// BTC's previous append handle now points at an unlinked file. A late
	// print must reopen its path rather than disappear into that descriptor.
	if err := a.Append(recs(tr(-DayMs+1, 4, 102, 1, B))); !errors.Is(err, ErrCapacity) {
		t.Fatalf("new old-day file must still obey the budget: %v", err)
	}
	a.o.MaxBytes = 150
	if err := a.Append(recs(tr(-DayMs+1, 4, 102, 1, B))); err != nil {
		t.Fatal(err)
	}
	stored, _ := a.LoadDay(DayOf(t0) - 1)
	if len(stored) != 1 || stored[0].TID != 4 {
		t.Fatalf("append wrote into an unlinked day: %+v", stored)
	}
}

func TestStatsRangeIncludesLateOlderTradesAndNewAppend(t *testing.T) {
	s, _ := Open(t.TempDir(), "BTC", Options{Now: clock(time.UnixMilli(t0))})
	defer s.Close()
	s.TryLease()
	for i, offset := range []int64{10, 0, 20, 5} {
		if err := s.Append(recs(tr(offset, uint64(i+1), 100, 1, B))); err != nil {
			t.Fatal(err)
		}
		st, err := s.Stats()
		wantFrom, wantTo := t0, t0+20
		if i == 0 {
			wantFrom = t0 + 10
		}
		if i < 2 {
			wantTo = t0 + 10
		}
		if err != nil || st.From != wantFrom || st.To != wantTo || st.Trades != int64(i+1) {
			t.Fatalf("after append %d: %+v %v", i, st, err)
		}
	}
}
