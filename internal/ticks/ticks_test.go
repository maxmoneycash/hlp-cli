package ticks

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// t0 is 2026-11-15 10:00:00 UTC.
var t0 = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC).UnixMilli()

func tr(ms int64, tid uint64, px, sz float64, side domain.Side) domain.Trade {
	return domain.Trade{Coin: "BTC", Time: t0 + ms, TID: tid, Px: px, Sz: sz, Side: side}
}

func recs(ts ...domain.Trade) []Rec {
	out := make([]Rec, len(ts))
	for i, t := range ts {
		out[i] = FromTrade(t)
	}
	return out
}

const B, S = domain.Buy, domain.Sell

func TestTickBarsOHLCVAndSplit(t *testing.T) {
	rs := recs(
		tr(0, 900, 100, 1, B),
		tr(5, 100, 102, 2, S),
		tr(9, 500, 99, 1, B), // closes bar 0 (3 ticks)
		tr(10, 700, 101, 0.5, S),
		tr(10, 300, 103, 0.5, B), // same ms: arrival order decides
		tr(12, 800, 98, 1, S),    // closes bar 1
		tr(20, 200, 97, 3, B),    // open bar
	)
	bars, _ := Build(rs, Spec{KindTicks, 3}, 0, false)
	if len(bars) != 3 {
		t.Fatalf("bars %d", len(bars))
	}
	b0 := bars[0]
	want0 := Bar{I: 0, T: t0, TLast: t0 + 9, O: 100, H: 102, L: 99, C: 99, V: 4, VUSD: 100 + 204 + 99, N: 3,
		BuyV: 2, SellV: 2, FirstTID: 900, LastTID: 500, Closed: true}
	if !reflect.DeepEqual(b0, want0) {
		t.Fatalf("bar0\n got %+v\nwant %+v", b0, want0)
	}
	b1 := bars[1]
	if b1.O != 101 || b1.C != 98 || b1.H != 103 || b1.L != 98 || b1.FirstTID != 700 || b1.LastTID != 800 || !b1.Closed || b1.I != 1 {
		t.Fatalf("bar1 %+v", b1)
	}
	if b2 := bars[2]; b2.Closed || b2.N != 1 || b2.BuyV != 3 || b2.SellV != 0 || b2.I != 2 {
		t.Fatalf("open bar %+v", b2)
	}
}

func TestSameTimestampTiesKeepArrivalOrder(t *testing.T) {
	// three prints in one block: open is the first to arrive, close the last
	rs := recs(tr(0, 3, 10, 1, B), tr(0, 1, 11, 1, B), tr(0, 2, 12, 1, B))
	bars, _ := Build(rs, Spec{KindTicks, 3}, 0, false)
	if bars[0].O != 10 || bars[0].C != 12 || bars[0].FirstTID != 3 || bars[0].LastTID != 2 {
		t.Fatalf("%+v", bars[0])
	}
}

func TestOutOfOrderLiveMatchesSortedReplay(t *testing.T) {
	in := recs(tr(10, 1, 50, 1, B), tr(5, 2, 49, 1, S), tr(20, 3, 51, 1, B), tr(15, 4, 48, 1, S))
	a := NewAggregator(Spec{KindTicks, 4}, 0)
	var live []Bar
	for _, r := range in {
		live = append(live, a.Add(r)...)
	}
	sorted, _ := Build(in, Spec{KindTicks, 4}, 0, false)
	if len(live) != 1 || !reflect.DeepEqual(live[0], sorted[0]) {
		t.Fatalf("live %+v\nsorted %+v", live, sorted)
	}
	if live[0].O != 49 || live[0].C != 51 || live[0].T != t0+5 || live[0].TLast != t0+20 {
		t.Fatalf("%+v", live[0])
	}
}

func TestDuplicateTidsDropped(t *testing.T) {
	r := NewRecorder(nil)
	batch := []domain.Trade{tr(0, 1, 10, 1, B), tr(1, 2, 11, 1, S)}
	if got := r.Ingest(batch, 1); len(got) != 2 {
		t.Fatalf("first %d", len(got))
	}
	if got := r.Ingest(batch, 1); got != nil {
		t.Fatalf("resent frame admitted: %+v", got)
	}
	mixed := []domain.Trade{tr(1, 2, 11, 1, S), tr(2, 3, 12, 1, B)}
	if got := r.Ingest(mixed, 1); len(got) != 1 || got[0].TID != 3 {
		t.Fatalf("mixed %+v", got)
	}
	// Build dedupes too (two writers in the past, or a seed in memory)
	bars, _ := Build(recs(tr(0, 1, 10, 1, B), tr(0, 1, 10, 1, B), tr(1, 2, 11, 1, B)), Spec{KindTicks, 10}, 0, false)
	if bars[0].N != 2 {
		t.Fatalf("n %d", bars[0].N)
	}
}

func TestGapsOnReconnectAndSeed(t *testing.T) {
	r := NewRecorder(nil)
	seed := r.Seed([]domain.Trade{tr(2, 2, 10, 1, B), tr(1, 1, 10, 1, B)}) // newest first, like recentTrades
	if len(seed) != 2 || seed[0].TID != 1 || seed[0].Gap {
		t.Fatalf("seed %+v", seed)
	}
	// the first socket's snapshot overlaps the seed: continuous
	got := r.Ingest([]domain.Trade{tr(2, 2, 10, 1, B), tr(3, 3, 10, 1, B)}, 1)
	if len(got) != 1 || got[0].Gap {
		t.Fatalf("overlapping snapshot marked a gap: %+v", got)
	}
	// same socket: never a gap
	if got := r.Ingest([]domain.Trade{tr(4, 4, 10, 1, B)}, 1); got[0].Gap {
		t.Fatal("gap on the same socket")
	}
	// reconnect whose first frame shares nothing: gap on the first new trade
	got = r.Ingest([]domain.Trade{tr(50, 9, 10, 1, B), tr(51, 10, 10, 1, B)}, 2)
	if !got[0].Gap || got[1].Gap {
		t.Fatalf("reconnect gap %+v", got)
	}
	// reconnect that overlaps: no gap
	if got := r.Ingest([]domain.Trade{tr(51, 10, 10, 1, B), tr(52, 11, 10, 1, B)}, 3); got[0].Gap {
		t.Fatal("overlapping reconnect marked a gap")
	}
}

func TestGapAndDayEndCloseBarsPartial(t *testing.T) {
	rs := recs(tr(0, 1, 10, 1, B), tr(1, 2, 11, 1, B))
	g := FromTrade(tr(100, 3, 12, 1, B))
	g.Gap = true
	rs = append(rs, g, FromTrade(tr(101, 4, 13, 1, B)))
	midnight := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC).UnixMilli() - t0
	rs = append(rs, FromTrade(tr(midnight-1, 5, 14, 1, B)), FromTrade(tr(midnight, 6, 15, 1, B)))
	bars, _ := Build(rs, Spec{KindTicks, 5}, 0, false)
	if len(bars) != 3 {
		t.Fatalf("%d bars: %+v", len(bars), bars)
	}
	if !bars[0].Partial || !bars[0].Closed || bars[0].N != 2 || bars[0].Gap {
		t.Fatalf("pre-gap bar %+v", bars[0])
	}
	if !bars[1].Gap || !bars[1].Partial || bars[1].N != 3 {
		t.Fatalf("post-gap bar (closed at midnight) %+v", bars[1])
	}
	if bars[2].Closed || bars[2].N != 1 || bars[2].O != 15 {
		t.Fatalf("new day bar %+v", bars[2])
	}
}

func TestVolumeUSDAndRangeBars(t *testing.T) {
	rs := recs(tr(0, 1, 100, 0.1, B), tr(1, 2, 100, 0.2, S), tr(2, 3, 100, 0.7, B), tr(3, 4, 100, 5, B))
	vol, _ := Build(rs, Spec{KindVolume, 0.3}, 0, false)
	// 0.1+0.2 reaches 0.3 despite float drift; 0.7 overshoots (never split)
	if len(vol) != 3 || vol[0].N != 2 || vol[1].V != 0.7 || vol[2].V != 5 || !vol[2].Closed {
		t.Fatalf("volume bars %+v", vol)
	}
	usd, _ := Build(rs, Spec{KindUSD, 100}, 0, false)
	if len(usd) != 2 || usd[0].VUSD < 100 || usd[0].N != 3 {
		t.Fatalf("usd bars %+v", usd)
	}
	rr := recs(tr(0, 1, 100, 1, B), tr(1, 2, 101, 1, B), tr(2, 3, 99.5, 1, S), tr(3, 4, 100, 1, B), tr(4, 5, 101, 1, B))
	rng, _ := Build(rr, Spec{KindRange, 1.5}, 0, false)
	if len(rng) != 2 || rng[0].H-rng[0].L != 1.5 || rng[0].N != 3 || rng[1].O != 100 || rng[1].Closed {
		t.Fatalf("range bars %+v", rng)
	}
}

func TestSpecValidateAndLabel(t *testing.T) {
	for _, bad := range []Spec{{KindTicks, 0}, {KindTicks, 2.5}, {"bogus", 1}, {KindRange, -1}} {
		if bad.Validate() == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
	cases := map[string]Spec{"500T": {KindTicks, 500}, "10 BTC": {KindVolume, 10}, "$1M": {KindUSD, 1e6}, "$250K": {KindUSD, 250000}, "R50": {KindRange, 50}}
	for want, s := range cases {
		if got := s.Label("BTC"); got != want {
			t.Fatalf("%+v label %q want %q", s, got, want)
		}
	}
}

func clock(at time.Time) func() time.Time { return func() time.Time { return at } }

func TestStoreRoundTripTornTailAndStats(t *testing.T) {
	root := t.TempDir()
	now := time.UnixMilli(t0 + 3600_000)
	s, err := Open(root, "xyz:SP500", Options{Now: clock(now)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if filepath.Base(s.Dir()) != "xyz%3ASP500" {
		t.Fatalf("dir %s", s.Dir())
	}
	if err := s.Append(recs(tr(0, 1, 1, 1, B))); err == nil {
		t.Fatal("append without the lease")
	}
	if !s.TryLease() {
		t.Fatal("lease")
	}
	g := FromTrade(tr(1, 2, 2.5, 0.25, S))
	g.Gap = true
	in := append(recs(tr(0, 1, 1.5, 3, B)), g)
	yesterday := FromTrade(tr(-11*3600_000, 3, 7, 1, B))
	if err := s.Append(append(in, yesterday)); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadDay(DayOf(t0))
	if err != nil || !reflect.DeepEqual(got, in) {
		t.Fatalf("load %+v %v", got, err)
	}
	// a torn record at the tail is ignored, then cut by the next append
	p := filepath.Join(s.Dir(), dayName(DayOf(t0)))
	s.Close()
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.Write([]byte{1, 2, 3})
	f.Close()
	if got, _ := s.LoadDay(DayOf(t0)); len(got) != 2 {
		t.Fatalf("torn tail read %d", len(got))
	}
	if !s.TryLease() {
		t.Fatal("relock")
	}
	if err := s.Append(recs(tr(2, 4, 3, 1, B))); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.LoadDay(DayOf(t0)); len(got) != 3 || got[2].TID != 4 {
		t.Fatalf("after torn tail %+v", got)
	}
	st, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Trades != 4 || st.Days != 2 || st.From != yesterday.Time || st.To != t0+2 {
		t.Fatalf("stats %+v", st)
	}
	coins, _ := Coins(root)
	if !reflect.DeepEqual(coins, []string{"xyz:SP500"}) {
		t.Fatalf("coins %v", coins)
	}
}

func TestWriterLeaseIsExclusive(t *testing.T) {
	root := t.TempDir()
	a, _ := Open(root, "BTC", Options{})
	b, _ := Open(root, "BTC", Options{})
	defer a.Close()
	defer b.Close()
	if !a.TryLease() {
		t.Fatal("a")
	}
	if b.TryLease() {
		t.Fatal("two writers")
	}
	a.Close()
	if !b.TryLease() {
		t.Fatal("lease not released")
	}
}

func TestPruneRetentionAndSizeCap(t *testing.T) {
	root := t.TempDir()
	now := time.UnixMilli(t0)
	s, _ := Open(root, "BTC", Options{Now: clock(now), RetentionDays: 2, MaxBytes: 1 << 30})
	s.TryLease()
	for d := int64(0); d <= 4; d++ {
		if err := s.Append(recs(tr(-d*DayMs, uint64(d+1), 1, 1, B))); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Prune(); err != nil {
		t.Fatal(err)
	}
	days, _ := s.Days()
	if len(days) != 3 || days[0] != DayOf(t0)-2 {
		t.Fatalf("retention kept %v", days)
	}
	// size cap: one record files are 41 bytes; cap at 90 keeps today + 1
	other, _ := Open(root, "ETH", Options{Now: clock(now), RetentionDays: 30, MaxBytes: 90})
	other.TryLease()
	if err := other.Prune(); err != nil {
		t.Fatal(err)
	}
	days, _ = s.Days()
	if len(days) != 2 || days[1] != DayOf(t0) {
		t.Fatalf("size cap kept %v", days)
	}
	s.Close()
	other.Close()
}

func TestSeriesStableAcrossLoadsAndContinuesLive(t *testing.T) {
	root := t.TempDir()
	now := time.UnixMilli(t0 + 3600_000)
	s, _ := Open(root, "BTC", Options{Now: clock(now)})
	s.TryLease()
	defer s.Close()
	var all []Rec
	tid := uint64(1)
	for d := int64(2); d >= 0; d-- { // two past days and today, 7 trades each
		for k := int64(0); k < 7; k++ {
			all = append(all, FromTrade(tr(-d*DayMs+k*1000, tid, float64(100+k), 1, B)))
			tid++
		}
	}
	if err := s.Append(all); err != nil {
		t.Fatal(err)
	}
	spec := Spec{KindTicks, 3}
	today := DayOf(now.UnixMilli())
	full, _, err := Series(s, nil, spec, 0, today)
	if err != nil {
		t.Fatal(err)
	}
	// per day: 3 + 3 + 1 (partial at day end; open today)
	if len(full) != 9 || !full[2].Partial || full[8].Closed || full[8].N != 1 {
		t.Fatalf("full %+v", full)
	}
	last4, agg, _ := Series(s, nil, spec, 4, today)
	if len(last4) != 4 {
		t.Fatalf("want 4 got %d", len(last4))
	}
	for i := range last4 {
		a, b := last4[i], full[len(full)-4+i]
		a.I, b.I = 0, 0
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("bar %d differs with less history:\n%+v\n%+v", i, a, b)
		}
	}
	// live trades continue the open bar with matching indexes
	closed := agg.Add(FromTrade(tr(8000, 99, 120, 1, B)))
	closed = append(closed, agg.Add(FromTrade(tr(9000, 100, 121, 1, S)))...)
	if len(closed) != 1 || closed[0].I != 3 || closed[0].N != 3 || closed[0].C != 121 {
		t.Fatalf("closed %+v", closed)
	}
	if agg.Next() != 4 {
		t.Fatalf("next %d", agg.Next())
	}
	// memory-only trades (not the writer) join their day
	extra := recs(tr(9500, 101, 122, 1, B))
	withExtra, _, _ := Series(s, extra, spec, 1, today)
	if withExtra[0].N != 2 || withExtra[0].LastTID != 101 || withExtra[0].Closed {
		t.Fatalf("extra %+v", withExtra)
	}
}

func TestRecorderStoresAndSeesStoredTail(t *testing.T) {
	root := t.TempDir()
	// Pin the clock near t0: TryLease prunes days older than the retention
	// window, so a wall clock deletes the t0 day once it is a week old.
	opts := Options{Now: clock(time.UnixMilli(t0 + 3600_000))}
	s, _ := Open(root, "BTC", opts)
	r := NewRecorder(s)
	if !r.Writer() {
		t.Fatal("first recorder should write")
	}
	r.Ingest([]domain.Trade{tr(0, 1, 10, 1, B), tr(1, 2, 10, 1, B)}, 1)
	s.Close()
	// a restart: the seed overlaps the stored tail, so no gap; new trades stored
	s2, _ := Open(root, "BTC", opts)
	defer s2.Close()
	r2 := NewRecorder(s2)
	got := r2.Seed([]domain.Trade{tr(1, 2, 10, 1, B), tr(2, 3, 10, 1, B)})
	if len(got) != 1 || got[0].Gap {
		t.Fatalf("restart seed %+v", got)
	}
	// a second process reads only and keeps its trades in memory
	s3, _ := Open(root, "BTC", opts)
	defer s3.Close()
	r3 := NewRecorder(s3)
	if r3.Writer() {
		t.Fatal("second writer")
	}
	r3.Ingest([]domain.Trade{tr(3, 4, 10, 1, B)}, 1)
	if u := r3.Unstored(); len(u) != 1 || u[0].TID != 4 {
		t.Fatalf("unstored %+v", u)
	}
	// a restart with no overlap is a gap
	s2.Close()
	s4, _ := Open(root, "BTC", opts)
	defer s4.Close()
	r4 := NewRecorder(s4)
	if got := r4.Seed([]domain.Trade{tr(500, 77, 10, 1, B)}); !got[0].Gap {
		t.Fatal("non-overlapping restart not marked")
	}
	rs, _ := s4.LoadDay(DayOf(t0))
	if len(rs) != 4 || !rs[3].Gap {
		t.Fatalf("stored %+v", rs)
	}
}

func TestRecorderConcurrentIngest(t *testing.T) {
	s, _ := Open(t.TempDir(), "BTC", Options{Now: clock(time.UnixMilli(t0))})
	defer s.Close()
	r := NewRecorder(s)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for k := 0; k < 50; k++ {
				r.Ingest([]domain.Trade{tr(int64(k), uint64(k+1), 10, 1, B)}, 1)
			}
		}(g)
	}
	wg.Wait()
	rs, _ := s.LoadDay(DayOf(t0))
	if len(rs) != 50 {
		t.Fatalf("stored %d (dupes or losses)", len(rs))
	}
}

func TestCoinDirRoundTrip(t *testing.T) {
	for _, c := range []string{"BTC", "xyz:SP500", "@107", "kPEPE", "a/b", ".."} {
		d := CoinDir(c)
		if filepath.Base(d) != d || d == "." || d == ".." {
			t.Fatalf("%q -> unsafe %q", c, d)
		}
		if back, ok := decodeCoinDir(d); !ok || back != c {
			t.Fatalf("%q -> %q -> %q", c, d, back)
		}
	}
}
