package hyperliquid

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

const minute = int64(60_000)

// series serves 1m candles at every minute in [lo, hi] except holes.
type series struct {
	lo, hi int64
	holes  map[int64]bool
	calls  atomic.Int32
	// extra rows returned outside the window (echoed boundary bar etc.)
	echo bool
	err  error
}

func (s *series) fetch(ctx context.Context, from, to int64) ([]domain.Candle, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	var out []domain.Candle
	for t := max(from, s.lo); t <= min(to, s.hi); t += minute {
		if t%minute != 0 || s.holes[t] {
			continue
		}
		out = append(out, domain.Candle{T: t, O: 1, H: 1, L: 1, C: float64(t / minute)})
	}
	if s.echo {
		// a server that echoes the bar after the window and one far outside
		out = append(out, domain.Candle{T: to + 1 - (to+1)%minute + minute, C: -1}, domain.Candle{T: from - 10*minute, C: -1})
	}
	return out, nil
}

func TestPageHistoryCollectsAndDedupes(t *testing.T) {
	s := &series{lo: 1000 * minute, hi: 5000 * minute, echo: true}
	res, err := PageHistory(context.Background(), s.fetch, domain.I1m, 4000*minute-1, 1500, PageOpts{PageSize: 400})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Bars) != 1500 {
		t.Fatalf("bars %d, want 1500", len(res.Bars))
	}
	if res.Bars[len(res.Bars)-1].T != 3999*minute || res.Bars[0].T != 2500*minute {
		t.Fatalf("range %d..%d", res.Bars[0].T/minute, res.Bars[len(res.Bars)-1].T/minute)
	}
	for i, c := range res.Bars {
		if c.C < 0 {
			t.Fatalf("out-of-window row kept at %d", i)
		}
		if i > 0 && c.T != res.Bars[i-1].T+minute {
			t.Fatalf("gap or dup at %d", i)
		}
	}
	if res.Exhausted {
		t.Fatal("not exhausted yet")
	}
	if res.Requests != 4 { // 400+400+400+300
		t.Fatalf("requests %d", res.Requests)
	}
}

func TestPageHistoryExhaustsAfterEmptyPages(t *testing.T) {
	s := &series{lo: 1000 * minute, hi: 1200 * minute}
	res, err := PageHistory(context.Background(), s.fetch, domain.I1m, 1200*minute, 1000, PageOpts{PageSize: 100, MaxEmpty: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Exhausted || len(res.Bars) != 201 {
		t.Fatalf("exhausted=%v bars=%d", res.Exhausted, len(res.Bars))
	}
	// 3 full pages (100,100,1) then 3 empty probes
	if got := s.calls.Load(); got != 6 {
		t.Fatalf("calls %d", got)
	}
}

func TestPageHistorySkipsHoles(t *testing.T) {
	holes := map[int64]bool{}
	for t := int64(1500); t < 1650; t++ { // a 150-bar outage
		holes[t*minute] = true
	}
	s := &series{lo: 1000 * minute, hi: 2000 * minute, holes: holes}
	res, err := PageHistory(context.Background(), s.fetch, domain.I1m, 2000*minute, 600, PageOpts{PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Bars) != 600 || res.Exhausted {
		t.Fatalf("bars %d exhausted %v", len(res.Bars), res.Exhausted)
	}
	if res.Bars[0].T >= 1500*minute {
		t.Fatalf("did not page past the hole: oldest %d", res.Bars[0].T/minute)
	}
}

func TestPageHistoryFloorStopsWithoutProbing(t *testing.T) {
	s := &series{lo: 0, hi: 2000 * minute}
	res, err := PageHistory(context.Background(), s.fetch, domain.I1m, 2000*minute, 5000, PageOpts{PageSize: 1000, Floor: 1500 * minute})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Exhausted || res.Bars[0].T != 1500*minute || len(res.Bars) != 501 {
		t.Fatalf("exhausted %v oldest %d n %d", res.Exhausted, res.Bars[0].T/minute, len(res.Bars))
	}
	if s.calls.Load() != 1 {
		t.Fatalf("calls %d", s.calls.Load())
	}
}

func TestPageHistoryAbortMidPage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &series{lo: 0, hi: 5000 * minute}
	fetch := func(c context.Context, from, to int64) ([]domain.Candle, error) {
		rows, err := s.fetch(c, from, to)
		cancel() // the user switched symbol while the page was in flight
		return rows, err
	}
	res, err := PageHistory(ctx, fetch, domain.I1m, 5000*minute, 3000, PageOpts{PageSize: 1000})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if s.calls.Load() != 1 || len(res.Bars) != 0 { // the aborted page is discarded
		t.Fatalf("calls %d bars %d", s.calls.Load(), len(res.Bars))
	}
}

func TestHistoryPagerLifecycle(t *testing.T) {
	s := &series{lo: 0, hi: 3000 * minute}
	p := NewHistoryPagerFetch(s.fetch, "BTC", domain.I1m)
	p.LoadMore = 1000
	now := time.UnixMilli(3000 * minute)
	var cs []domain.Candle
	for t := int64(2501); t <= 3000; t++ {
		cs = append(cs, domain.Candle{T: t * minute})
	}
	// plenty of bars before the view: no request
	if _, ok := p.Want(cs, 300, 100, now); ok {
		t.Fatal("requested with 300 bars of slack")
	}
	req, ok := p.Want(cs, 20, 100, now)
	if !ok || req.ToMs != 2501*minute-1 {
		t.Fatalf("want: ok=%v to=%d", ok, req.ToMs)
	}
	if !p.State().Loading {
		t.Fatal("not loading")
	}
	if _, ok := p.Want(cs, 0, 100, now); ok {
		t.Fatal("second request while one is in flight")
	}
	page := req.Run(context.Background())
	cs2, ok := p.Apply(page, cs, now)
	if !ok || len(cs2) != 1500 || cs2[0].T != 1501*minute {
		t.Fatalf("apply ok=%v n=%d", ok, len(cs2))
	}
	if p.State().Loading || p.State().Exhausted {
		t.Fatalf("state %+v", p.State())
	}

	// a symbol switch makes the in-flight result stale
	req, ok = p.Want(cs2, 0, 100, now)
	if !ok {
		t.Fatal("no second request")
	}
	p.Reset("ETH", domain.I1m)
	if _, applied := p.Apply(req.Run(context.Background()), cs2, now); applied {
		t.Fatal("stale page applied")
	}
}

func TestHistoryPagerBackoffAndExhaustion(t *testing.T) {
	s := &series{lo: 0, hi: 600 * minute, err: errors.New("boom")}
	p := NewHistoryPagerFetch(s.fetch, "BTC", domain.I1m)
	now := time.UnixMilli(600 * minute)
	cs := []domain.Candle{{T: 500 * minute}, {T: 501 * minute}}
	req, _ := p.Want(cs, 0, 50, now)
	if _, ok := p.Apply(req.Run(context.Background()), cs, now); ok {
		t.Fatal("failure applied")
	}
	st := p.State()
	if st.RetryAt.Sub(now) != time.Second || st.LastErr == "" {
		t.Fatalf("backoff %v", st.RetryAt.Sub(now))
	}
	if _, ok := p.Want(cs, 0, 50, now.Add(500*time.Millisecond)); ok {
		t.Fatal("retried during backoff")
	}
	req, ok := p.Want(cs, 0, 50, now.Add(time.Second))
	if !ok {
		t.Fatal("no retry after backoff")
	}
	p.Apply(req.Run(context.Background()), cs, now.Add(time.Second))
	if d := p.State().RetryAt.Sub(now.Add(time.Second)); d != 2*time.Second {
		t.Fatalf("second backoff %v", d)
	}
	// recover: the series starts at minute 0, so the next page exhausts it
	s.err = nil
	later := now.Add(10 * time.Second)
	req, ok = p.Want(cs, 0, 50, later)
	if !ok {
		t.Fatal("no request after recovery")
	}
	out, ok := p.Apply(req.Run(context.Background()), cs, later)
	if !ok || len(out) != 502 || out[0].T != 0 {
		t.Fatalf("n=%d", len(out))
	}
	if !p.State().Exhausted {
		t.Fatal("not exhausted at time 0")
	}
	if _, ok := p.Want(out, 0, 50, later); ok {
		t.Fatal("requested after exhaustion")
	}
}

func TestHistoryPagerCapsAtMaxCandles(t *testing.T) {
	s := &series{lo: 0, hi: 10000 * minute}
	p := NewHistoryPagerFetch(s.fetch, "BTC", domain.I1m)
	now := time.UnixMilli(10000 * minute)
	var cs []domain.Candle
	for t := int64(9501); t <= 10000; t++ {
		cs = append(cs, domain.Candle{T: t * minute})
	}
	for i := 0; i < 10; i++ {
		req, ok := p.Want(cs, 0, 100, now)
		if !ok {
			break
		}
		cs, _ = p.Apply(req.Run(context.Background()), cs, now)
	}
	if len(cs) != MaxCandles || !p.State().Exhausted {
		t.Fatalf("n=%d exhausted=%v", len(cs), p.State().Exhausted)
	}
}

func TestHistoryFloorAndWindowFetchViaClient(t *testing.T) {
	now := time.UnixMilli(10000 * minute)
	if f := HistoryFloor(domain.I1m, now); f != (10000-4999)*minute {
		t.Fatalf("floor %d", f/minute)
	}
	f := newFakeHL(t)
	var gotStart, gotEnd float64
	f.override = func(typ string, body map[string]any) (int, []byte, bool) {
		if typ != "candleSnapshot" {
			return 0, nil, false
		}
		req := body["req"].(map[string]any)
		gotStart, gotEnd = req["startTime"].(float64), req["endTime"].(float64)
		return 200, []byte(`[{"t":60000,"T":119999,"s":"BTC","i":"1m","o":"1","c":"2","h":"3","l":"0.5","v":"10","n":3},{"t":120000,"T":179999,"s":"BTC","i":"1m","o":"2","c":"2","h":"2","l":"2","v":"1","n":1}]`), true
	}
	c := f.client(t)
	fetch := WindowFetch(c, "BTC", domain.I1m)
	rows, err := fetch(context.Background(), 60000, 120000)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || gotEnd != 120000 || gotStart > 60000 {
		t.Fatalf("rows %d start %v end %v", len(rows), gotStart, gotEnd)
	}
}
