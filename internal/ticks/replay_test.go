package ticks

import (
	"os"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestReplayMatchesRecordedAggregation(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root, "xyz:TEST", Options{Now: clock(time.UnixMilli(t0 + DayMs))})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !store.TryLease() {
		t.Fatal("lease")
	}
	const largeID = 1<<53 + 1
	rs := recs(
		tr(1, largeID, 100, 1, B), tr(1, 8, 105, 2, S),
		tr(4, 3, 97, 3, S), tr(3, 7, 103, 1, B), // persisted out of time order
		tr(5, 5, 101, 1, B), tr(6, 0, 101, 1, S), tr(6, 0, 101, 1, S),
		tr(5, 5, 101, 1, B), // duplicate includes a newly identified gap
		tr(DayMs, 9, 110, 2, S),
	)
	rs[7].Gap = true
	if err := store.Append(rs); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []Spec{{KindTicks, 3}, {KindVolume, 4}, {KindUSD, 450}, {KindRange, 4}} {
		t.Run(string(spec.Kind), func(t *testing.T) {
			d, err := Replay(t.Context(), root, "XYZ:test", spec, 0, 0, 8)
			if err != nil {
				t.Fatal(err)
			}
			if d.Coin != "xyz:TEST" || len(d.Frames) != 8 || d.Truncated || d.From != t0+1 || d.To != t0+DayMs {
				t.Fatalf("replay bounds or coin: %+v", d)
			}
			var bars []Bar
			var ids []string
			for _, f := range d.Frames {
				ids = append(ids, f.TID)
				for _, b := range f.Bars {
					if int(b.I) == len(bars) {
						bars = append(bars, b)
					} else {
						bars[b.I] = b
					}
				}
			}
			if !slices.Equal(ids, []string{"9007199254740993", "8", "7", "3", "5", "0", "0", "9"}) ||
				!d.Frames[4].Gap || d.Frames[7].Gap || d.Frames[0].Side != "buy" || d.Frames[1].Side != "sell" {
				t.Fatalf("unstable sequence, gap, or side: %+v", d.Frames)
			}
			want, _, err := Series(store, nil, spec, 0, DayOf(d.To))
			if err != nil || !reflect.DeepEqual(want, bars) {
				t.Fatalf("replay %+v and snapshot %+v differ, %v", bars, want, err)
			}
			again, err := Replay(t.Context(), root, d.Coin, spec, d.From, d.To, 8)
			if err != nil || !reflect.DeepEqual(d, again) {
				t.Fatalf("same interval changed: %+v, %v", again, err)
			}
			// Keep two same-ms trades. Warm-up includes prints preceding --from
			// and those clipped by --count, leaving the true candle intact.
			tail, err := Replay(t.Context(), root, d.Coin, spec, t0+4, t0+6, 2)
			if err != nil || !tail.Truncated || tail.From != t0+6 || tail.To != t0+6 ||
				!reflect.DeepEqual(tail.Frames, d.Frames[5:7]) {
				t.Fatalf("clipped tail differs: %+v, %v", tail, err)
			}
			// Full capacity on the latest day: look back across the file boundary
			// to report truncation, unless the requested interval excludes it.
			for _, from := range []int64{0, t0 + DayMs} {
				one, err := Replay(t.Context(), root, d.Coin, spec, from, 0, 1)
				if err != nil || one.Truncated != (from == 0) || len(one.Frames) != 1 ||
					one.Frames[0].TID != "9" || one.Frames[0].Bars[0].N != 1 {
					t.Fatalf("last day: %+v, %v", one, err)
				}
			}
		})
	}
}

func TestReplayEmptyIsReadOnly(t *testing.T) {
	root := t.TempDir()
	spec := Spec{KindTicks, 5}
	d, err := Replay(t.Context(), root, "MISSING", spec, t0, t0, 1)
	if err != nil || d.Frames == nil || len(d.Frames) != 0 || d.From != 0 || d.To != 0 || d.Truncated {
		t.Fatalf("empty replay %+v, %v", d, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) > 0 {
		t.Fatalf("empty query changed the store: %v, %v", entries, err)
	}
}
