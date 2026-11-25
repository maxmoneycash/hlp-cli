package paper_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/strategy"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/internal/testkit"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/paper"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/thesis"
)

// feed is a fake paper.Feed: history from a testkit.Source, live bars pushed
// by the test, a fixed two-sided book.
type feed struct {
	*testkit.Source
	mu   sync.Mutex
	subs map[string][]chan domain.Candle
	nsub map[string]int
	book map[string]domain.Book
}

func newFeed(src *testkit.Source) *feed {
	return &feed{Source: src, subs: map[string][]chan domain.Candle{}, nsub: map[string]int{}, book: map[string]domain.Book{}}
}

func (f *feed) Bars(ctx context.Context, coin string, _ domain.Interval) (<-chan domain.Candle, error) {
	ch := make(chan domain.Candle, 256)
	f.mu.Lock()
	f.subs[coin] = append(f.subs[coin], ch)
	f.nsub[coin]++
	f.mu.Unlock()
	return ch, nil
}

func (f *feed) Book(_ context.Context, coin string) (domain.Book, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.book[coin]; ok {
		return b, nil
	}
	return domain.Book{}, nil
}

func (f *feed) push(coin string, c domain.Candle) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ch := range f.subs[coin] {
		ch <- c
	}
}

func (f *feed) subscribers(coin string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nsub[coin]
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// flipper alternates long/short every 10 bars and logs why.
type flipper struct{ name string }

func (f *flipper) Spec() strategy.Spec {
	return strategy.Spec{Name: f.name, Kind: "rules", Universe: []string{"BTC"}, Interval: domain.I15m, Warmup: 5}
}
func (f *flipper) Init(strategy.Context) error { return nil }
func (f *flipper) OnBar(ctx strategy.Context) error {
	n := len(ctx.Bars("BTC"))
	if n%10 != 0 {
		return nil
	}
	w := 0.5
	if (n/10)%2 == 1 {
		w = -0.5
	}
	ctx.Logf("bar %d target %+.1f", n, w)
	return ctx.SetTargets(map[string]float64{"BTC": w})
}

func TestSessionRunsPersistsAndResumes(t *testing.T) {
	cs, err := testkit.BTC15m()
	if err != nil {
		t.Fatal(err)
	}
	hist, live := cs[:50], cs[50:150]
	f := newFeed(&testkit.Source{Candle: map[string][]domain.Candle{"BTC": hist}})
	f.book["BTC"] = domain.Book{Bids: []domain.Level{{Px: hist[49].C - 1, Sz: 100}}, Asks: []domain.Level{{Px: hist[49].C + 1, Sz: 100}}}
	dir := t.TempDir()
	now := time.UnixMilli(hist[49].T + 15*60_000 + 1000)
	o := paper.Options{ID: "flip-test", Capital: 10000, DataDir: dir, Now: func() time.Time { return now }}
	s, err := paper.Start(context.Background(), &flipper{"flipper"}, f, o)
	if err != nil {
		t.Fatal(err)
	}
	if snap := s.Snapshot(); !snap.Running || snap.ID != "flip-test" || len(snap.Equity) != 1 || snap.Kind != "rules" {
		t.Fatalf("initial snapshot %+v", snap)
	}
	for _, c := range live {
		f.push("BTC", c)
	}
	f.push("BTC", live[10]) // a replayed bar is ignored
	waitFor(t, "100 live bars", func() bool { return len(s.Snapshot().Equity) == 101 })
	snap := s.Snapshot()
	if len(snap.Fills) < 5 || snap.Account.Equity == 0 || snap.Note == "" || !strings.Contains(strings.Join(snap.Logs, "\n"), "target") {
		t.Fatalf("snapshot fills=%d note=%q", len(snap.Fills), snap.Note)
	}
	// Market orders fill against the book (ask/bid), stamped with wall time.
	for _, fl := range snap.Fills {
		want := hist[49].C + 1
		if fl.Side == domain.Sell {
			want = hist[49].C - 1
		}
		if fl.Px != want || fl.Time != now.UnixMilli() {
			t.Fatalf("fill %+v, want px %v at %d", fl, want, now.UnixMilli())
		}
	}
	select {
	case u := <-s.Updates():
		if u.ID != "flip-test" {
			t.Fatal("bad update")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no coalesced update")
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-s.Updates(); ok {
		// drain the final snapshot, then the channel must be closed
		if _, ok := <-s.Updates(); ok {
			t.Fatal("updates not closed")
		}
	}
	if err := s.Stop(); err != nil {
		t.Fatal("second Stop", err)
	}

	list, err := paper.List(dir)
	if err != nil || len(list) != 1 || list[0].Running || list[0].ID != "flip-test" || len(list[0].Equity) != 101 {
		t.Fatalf("list %+v %v", list, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "paper", "flip-test.json")); err != nil {
		t.Fatal(err)
	}

	// Resume: same ID picks up the ledger, curve and fills.
	f2 := newFeed(&testkit.Source{Candle: map[string][]domain.Candle{"BTC": cs[:150]}})
	o.Now = func() time.Time { return time.UnixMilli(cs[149].T + 15*60_000 + 1000) }
	s2, err := paper.Start(context.Background(), &flipper{"flipper"}, f2, o)
	if err != nil {
		t.Fatal(err)
	}
	r := s2.Snapshot()
	if len(r.Fills) != len(snap.Fills) || len(r.Equity) != 101 || !strings.Contains(strings.Join(r.Logs, "\n"), "resumed session") {
		t.Fatalf("resume: fills %d/%d equity %d", len(r.Fills), len(snap.Fills), len(r.Equity))
	}
	if _, err := paper.Start(context.Background(), &flipper{"flipper"}, f2, o); err == nil {
		t.Fatal("started the same session twice")
	}
	if err := paper.Remove(dir, "flip-test"); err == nil {
		t.Fatal("removed a running session")
	}
	_ = s2.Stop()
	if _, err := paper.Start(context.Background(), &flipper{"other"}, f2, o); err == nil {
		t.Fatal("resumed a session that belongs to another strategy")
	}
	if one, err := paper.Load(dir, "flip-test"); err != nil || one.ID != "flip-test" {
		t.Fatalf("load %v", err)
	}
	if err := paper.Remove(dir, "flip-test"); err != nil {
		t.Fatal(err)
	}
	if l, _ := paper.List(dir); len(l) != 0 {
		t.Fatal("remove")
	}
	for _, bad := range []string{"../x", ""} {
		if _, err := paper.Load(dir, bad); err == nil {
			t.Errorf("Load(%q) accepted", bad)
		}
	}
	if l, err := paper.List(filepath.Join(dir, "nope")); err != nil || len(l) != 0 {
		t.Fatal("missing dir")
	}
}

func TestMultiCoinGraceAndContextCancel(t *testing.T) {
	t0 := int64(1_800_000_000_000) / 3600_000 * 3600_000
	btc := testkit.Synthetic(t0, domain.I1h, 60, func(i int) float64 { return 100 + float64(i) })
	eth := testkit.Synthetic(t0, domain.I1h, 60, func(i int) float64 { return 10 })
	f := newFeed(&testkit.Source{Candle: map[string][]domain.Candle{"BTC": btc[:40], "ETH": eth[:40]}})
	spec, _ := thesis.ParseText("long BTC, short ETH, rebalance bar, interval 1h")
	ctx, cancel := context.WithCancel(context.Background())
	s, err := paper.Start(ctx, spec.Strategy(), f, paper.Options{Grace: 30 * time.Millisecond,
		Now: func() time.Time { return time.UnixMilli(btc[39].T + 3600_000 + 1) }})
	if err != nil {
		t.Fatal(err)
	}
	f.push("BTC", btc[40]) // ETH never reports bar 40: decide after the grace period
	waitFor(t, "grace decision", func() bool { return len(s.Snapshot().Equity) == 2 })
	f.push("ETH", eth[40]) // straggler for a decided bar: no second decision
	f.push("BTC", btc[41])
	f.push("ETH", eth[41])
	waitFor(t, "joint decision", func() bool { return len(s.Snapshot().Equity) == 3 })
	cancel()
	waitFor(t, "context stop", func() bool { return !s.Snapshot().Running })
	_ = s.Stop()
}

func TestArenaRanksEntrants(t *testing.T) {
	t0 := int64(1_800_000_000_000) / 3600_000 * 3600_000
	up := testkit.Synthetic(t0, domain.I1h, 120, func(i int) float64 { return 100 * (1 + 0.01*float64(i)) })
	f := newFeed(&testkit.Source{Candle: map[string][]domain.Candle{"BTC": up[:20]}})
	mk := func(text string) strategy.Strategy {
		s, err := thesis.ParseText(text)
		if err != nil {
			t.Fatal(err)
		}
		return s.Strategy()
	}
	var mu sync.Mutex
	val := 1.0
	watch := paper.Watcher{Name: "HLP", Value: func(context.Context) (float64, error) {
		mu.Lock()
		defer mu.Unlock()
		val *= 1.001
		return val, nil
	}}
	a, err := paper.StartArena(context.Background(), []strategy.Strategy{
		mk("name Bear, short BTC, interval 1h, rebalance bar"),
		mk("name Bull, long BTC, interval 1h, rebalance bar"),
		mk("name Half, long BTC 0.5x, interval 1h, rebalance bar"),
	}, f, paper.Options{ID: "arena-test", Capital: 5000, Watch: []paper.Watcher{watch}, WatchEvery: time.Hour,
		Now: func() time.Time { return time.UnixMilli(up[19].T + 3600_000 + 1) }})
	if err != nil {
		t.Fatal(err)
	}
	if n := f.subscribers("BTC"); n != 1 {
		t.Fatalf("%d upstream subscriptions for three entrants, want 1 (fan-out)", n)
	}
	for _, c := range up[20:120] {
		f.push("BTC", c)
	}
	waitFor(t, "arena bars", func() bool {
		for _, s := range a.Leaderboard() {
			if s.Kind != "vault" && len(s.Equity) < 101 {
				return false
			}
		}
		return true
	})
	lb := a.Leaderboard()
	if len(lb) != 4 || lb[0].Strategy != "Bull" || lb[1].Strategy != "Half" || lb[len(lb)-1].Strategy != "Bear" {
		names := []string{}
		for _, s := range lb {
			names = append(names, s.Strategy)
		}
		t.Fatalf("leaderboard %v", names)
	}
	if lb[0].Capital != 5000 || lb[0].Return <= 0 || lb[3].Return >= 0 || !strings.HasPrefix(lb[0].ID, "arena-test-") {
		t.Fatalf("top entrant %+v", lb[0])
	}
	select {
	case board := <-a.Updates():
		if len(board) != 4 {
			t.Fatal("update size")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no leaderboard update")
	}
	if err := a.Stop(); err != nil {
		t.Fatal(err)
	}
	if final, ok := <-a.Updates(); !ok || len(final) != 4 {
		t.Fatal("final board")
	}
	if _, err := paper.StartArena(context.Background(), []strategy.Strategy{mk("long BTC")}, f, paper.Options{}); err == nil {
		t.Fatal("arena with one entrant")
	}
	ss := []paper.Snapshot{{Strategy: "b", Return: 0.1}, {Strategy: "a", Return: 0.1}, {Strategy: "c", Return: 0.2}}
	paper.Rank(ss)
	if ss[0].Strategy != "c" || ss[1].Strategy != "a" {
		t.Fatalf("rank %+v", ss)
	}
}

func TestStartErrors(t *testing.T) {
	f := newFeed(&testkit.Source{Candle: map[string][]domain.Candle{}})
	if _, err := paper.Start(context.Background(), nil, f, paper.Options{}); err == nil {
		t.Error("nil strategy")
	}
	if _, err := paper.Start(context.Background(), &flipper{"x"}, f, paper.Options{}); err == nil {
		t.Error("no history")
	}
	if _, err := paper.Start(context.Background(), &flipper{"x"}, f, paper.Options{ID: "bad id!"}); err == nil {
		t.Error("bad id")
	}
}
