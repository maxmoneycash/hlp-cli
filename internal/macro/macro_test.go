package macro

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const polyEvents = `[
 {"title":"ECB rate cut in December?","endDate":"2026-12-12T12:00:00Z","markets":[
   {"question":"ECB cuts rates?","slug":"ecb","outcomes":"[\"Yes\",\"No\"]","outcomePrices":"[\"0.5\",\"0.5\"]","volume24hr":9000000}
 ]},
 {"title":"Fed decision in December?","endDate":"2026-12-10T19:00:00Z","markets":[
   {"question":"Fed cuts 50+ bps?","slug":"fed-50","outcomes":"[\"Yes\",\"No\"]","outcomePrices":"[\"0.04\",\"0.96\"]","volume24hr":1000},
   {"question":"Fed cuts 25 bps?","slug":"fed-25","outcomes":"[\"Yes\",\"No\"]","outcomePrices":"[\"0.62\",\"0.38\"]","oneDayPriceChange":0.035,"volume24hr":500000},
   {"question":"No change?","slug":"fed-0","outcomes":"[\"Yes\",\"No\"]","outcomePrices":"[\"0.34\",\"0.66\"]"}
 ]},
 {"title":"Japan recession?","markets":[{"question":"Japan recession in 2026?","outcomes":["Yes","No"],"outcomePrices":["0.5","0.5"]}]}
]`

const kalshiMarkets = `{"markets":[
 {"ticker":"KXCPI-26NOV-T0.1","title":"CPI above 0.1%?","yes_sub_title":"Above 0.1%","close_time":"2026-12-10T13:29:00Z","last_price_dollars":"0.9700","previous_price_dollars":"0.9600","volume_24h_fp":"9000"},
 {"ticker":"KXCPI-26NOV-T0.3","title":"CPI above 0.3%?","yes_sub_title":"Above 0.3%","close_time":"2026-12-10T13:29:00Z","last_price_dollars":"0.4100","previous_price_dollars":"0.4500","volume_24h_fp":"5000"},
 {"ticker":"KXCPI-26NOV-T0.4","title":"CPI above 0.4%?","close_time":"2026-12-10T13:29:00Z","last_price":12,"previous_price":80,"volume_24h":100},
 {"ticker":"KXCPI-26OCT-T0.3","title":"CPI above 0.3% (settling now)?","close_time":"2026-11-15T12:20:00Z","last_price_dollars":"0.5000","volume_24h_fp":"90000"},
 {"ticker":"KXCPI-27JAN-T0.3","title":"CPI above 0.3% in January?","close_time":"2027-02-11T13:29:00Z","last_price_dollars":"0.5000","previous_price_dollars":"0","volume_24h_fp":"0"}
]}`

type fakeOutcomes struct {
	n   atomic.Int32
	err error
}

func (f *fakeOutcomes) Outcomes(context.Context) ([]Outcome, error) {
	f.n.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return []Outcome{
		{ID: 34924, Name: "template:binaryPrice", Description: "perp:xyz:XYZ100|threshold:28455|time:20261120-2100", Sides: []string{"Yes", "No"}, Mids: map[int]float64{0: 0.37, 1: 0.63}},
		{ID: 1, Name: "template:binaryPrice", Description: "perp:BTC|threshold:100000|time:20261118-0000", Mids: map[int]float64{0: 0.55}},
		{ID: 33713, Name: "template:sportsContestWinner", Description: "participantA:Wizards|participantB:Rockets", Mids: map[int]float64{0: 0.4}},
		{ID: 7, Name: "template:binaryQuestion", Description: "question:Will the Fed cut rates in December?\x1b[2J|time:20261210-1900", Mids: map[int]float64{0: 0.6}},
		{ID: 8, Name: "template:binaryPrice", Description: "perp:ETH|threshold:5000", Mids: map[int]float64{}}, // no mid: skipped
	}, nil
}

type servers struct {
	poly, kalshi *httptest.Server
	polyN        atomic.Int32
	kalshiN      atomic.Int32
	failPoly     atomic.Bool
	mu           sync.Mutex
	paths        []string
}

func newServers(t *testing.T) *servers {
	s := &servers{}
	s.poly = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.polyN.Add(1)
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.RequestURI())
		s.mu.Unlock()
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("credentials sent to polymarket")
		}
		if s.failPoly.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		if r.URL.Path != "/events" || r.URL.Query().Get("closed") != "false" {
			t.Errorf("polymarket request %s", r.URL)
		}
		if r.URL.Query().Get("tag_slug") == "fed-rates" {
			fmt.Fprint(w, polyEvents)
			return
		}
		fmt.Fprint(w, `[]`)
	}))
	s.kalshi = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.kalshiN.Add(1)
		if r.URL.Path != "/markets" || r.URL.Query().Get("status") != "open" {
			t.Errorf("kalshi request %s", r.URL)
		}
		if r.URL.Query().Get("series_ticker") == "KXCPI" {
			fmt.Fprint(w, kalshiMarkets)
			return
		}
		fmt.Fprint(w, `{"markets":[]}`)
	}))
	t.Cleanup(s.poly.Close)
	t.Cleanup(s.kalshi.Close)
	return s
}

func TestSnapshotCuratesCachesAndFailsSoft(t *testing.T) {
	srv := newServers(t)
	hl := &fakeOutcomes{}
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	c := New(Options{PolymarketURL: srv.poly.URL, KalshiURL: srv.kalshi.URL, Outcomes: hl, TTL: time.Minute, Spacing: time.Millisecond, Now: clock})
	if c.o.TTL != MinTTL {
		t.Fatalf("TTL below 5 min must clamp, got %v", c.o.TTL)
	}
	ctx := context.Background()
	snap := c.Snapshot(ctx)
	if len(snap.Errors) != 0 {
		t.Fatalf("errors %v", snap.Errors)
	}
	by := map[string][]Signal{}
	for _, s := range snap.Signals {
		by[s.Source] = append(by[s.Source], s)
	}
	// HIP-4: price questions and the macro question, soonest first; sports
	// and unpriced outcomes dropped; control characters stripped.
	if h := by[SourceHyperliquid]; len(h) != 3 || h[0].Coin != "BTC" || h[1].Coin != "xyz:XYZ100" || h[1].Prob != 0.37 || h[1].Market != "#349240" {
		t.Fatalf("hip4 %+v", h)
	} else if q := h[2].Question; !strings.Contains(q, "Fed cut") || strings.ContainsRune(q, 0x1b) || strings.Contains(q, "[2J") || h[2].Coin != "" {
		t.Fatalf("hip4 macro question %q", q)
	}
	// Polymarket: the best-scoring Fed market (unsure, traded, near); the
	// ECB event is filtered out and the untraded "no change" leg skipped.
	if p := by[SourcePolymarket]; len(p) != 1 || p[0].Question != "Fed cuts 25 bps?" || p[0].Prob != 0.62 || p[0].Move24h == nil || *p[0].Move24h != 0.035 {
		t.Fatalf("polymarket %+v", p)
	}
	// Kalshi: the best-scoring strike; one settling within the hour and an
	// untraded one are not eligible.
	if k := by[SourceKalshi]; len(k) != 1 || k[0].Market != "KXCPI-26NOV-T0.3" || k[0].Prob != 0.41 || k[0].Move24h == nil || *k[0].Move24h != -0.04 {
		t.Fatalf("kalshi %+v", k)
	}
	if snap.Fetched != now.UnixMilli() {
		t.Fatalf("fetched %d", snap.Fetched)
	}
	p0, k0, h0 := srv.polyN.Load(), srv.kalshiN.Load(), hl.n.Load()
	tags := map[string]bool{}
	for _, tp := range polyTopics {
		tags[tp.tag] = true
	}
	if p0 != int32(len(tags)) || k0 != int32(len(kalshiSeries)) || h0 != 1 {
		t.Fatalf("requests poly %d kalshi %d hl %d", p0, k0, h0)
	}

	// Within the TTL nothing is fetched again.
	mu.Lock()
	now = now.Add(4 * time.Minute)
	mu.Unlock()
	c.Snapshot(ctx)
	if srv.polyN.Load() != p0 || srv.kalshiN.Load() != k0 || hl.n.Load() != h0 {
		t.Fatal("refetched inside the TTL")
	}

	// After the TTL a failing source keeps its last good answer, reports
	// the error, and is not retried until the backoff passes.
	srv.failPoly.Store(true)
	hl.err = errors.New("info down")
	mu.Lock()
	now = now.Add(2 * time.Minute)
	mu.Unlock()
	snap = c.Snapshot(ctx)
	if len(snap.Errors) != 2 || !strings.Contains(strings.Join(snap.Errors, ";"), "polymarket: HTTP 502") {
		t.Fatalf("errors %v", snap.Errors)
	}
	if len(snap.Signals) != 5 {
		t.Fatalf("stale signals must be kept: %d", len(snap.Signals))
	}
	n := srv.polyN.Load()
	c.Snapshot(ctx)
	if srv.polyN.Load() != n {
		t.Fatal("failing source retried inside the backoff")
	}
}

func TestFormatSelectAndContext(t *testing.T) {
	m := 0.05
	sigs := []Signal{
		{Source: SourcePolymarket, Theme: "Fed decision", Question: "Fed cuts 25 bps?", Prob: 0.62, Move24h: &m},
		{Source: SourceHyperliquid, Theme: "ETH price", Question: "ETH above 5000", Prob: 0.2, Coin: "ETH"},
		{Source: SourceHyperliquid, Theme: "BTC price", Question: "BTC above 100000", Prob: 0.55, Coin: "BTC"},
	}
	got := Select(sigs, []string{"btc"}, 2)
	if len(got) != 2 || got[0].Coin != "BTC" || got[1].Source != SourcePolymarket {
		t.Fatalf("select %+v", got)
	}
	f := Format(got, time.Date(2026, 11, 15, 9, 30, 0, 0, time.UTC))
	for _, want := range []string{Header, "not trade signals", "2026-11-15 09:30 UTC", `[hyperliquid] BTC price: "BTC above 100000" YES 55%`, "(24h +5.0pp)"} {
		if !strings.Contains(f, want) {
			t.Errorf("format lacks %q:\n%s", want, f)
		}
	}
	if Format(nil, time.Time{}) != "" {
		t.Error("empty format")
	}
	if got := clean("a\u202eb\x1b[31m\nc"+strings.Repeat("x", 200), 20); strings.ContainsAny(got, "\u202e\x1b\n") || len([]rune(got)) != 20 {
		t.Errorf("clean %q", got)
	}

	// A client with no sources reachable still answers (empty, no panic),
	// and only enabled sources are asked.
	srv := newServers(t)
	c := New(Options{PolymarketURL: srv.poly.URL, KalshiURL: srv.kalshi.URL, Sources: []string{SourceKalshi}, Spacing: time.Millisecond})
	if b := c.MacroContext(context.Background(), []string{"BTC"}); !strings.Contains(b, "[kalshi]") || srv.polyN.Load() != 0 {
		t.Fatalf("context %q poly=%d", b, srv.polyN.Load())
	}
	var nilClient *Client
	if s := nilClient.Snapshot(context.Background()); len(s.Signals) != 0 {
		t.Fatal("nil client")
	}
}

func TestSpacingPerHost(t *testing.T) {
	var times []time.Time
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		fmt.Fprint(w, `{"markets":[]}`)
	}))
	defer srv.Close()
	c := New(Options{KalshiURL: srv.URL, Sources: []string{SourceKalshi}, Spacing: 40 * time.Millisecond})
	c.Snapshot(context.Background())
	if len(times) != len(kalshiSeries) {
		t.Fatalf("%d requests", len(times))
	}
	for i := 1; i < len(times); i++ {
		if d := times[i].Sub(times[i-1]); d < 30*time.Millisecond {
			t.Fatalf("requests %v apart", d)
		}
	}
}

// TestLiveMacro reads the public Polymarket and Kalshi APIs once each
// (keyless, free). Run with HLP_LIVE=1.
func TestLiveMacro(t *testing.T) {
	if os.Getenv("HLP_LIVE") != "1" {
		t.Skip("set HLP_LIVE=1 to call the public Polymarket and Kalshi APIs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := New(Options{Sources: []string{SourcePolymarket, SourceKalshi}})
	snap := c.Snapshot(ctx)
	t.Logf("errors: %v", snap.Errors)
	for _, s := range snap.Signals {
		t.Logf("[%s] %s: %q %.0f%%", s.Source, s.Theme, s.Question, s.Prob*100)
	}
	if len(snap.Errors) == 2 {
		t.Fatalf("both live sources failed: %v", snap.Errors)
	}
}

func TestScoreAndDedupe(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	week := now.Add(7 * 24 * time.Hour)
	coin, _ := score(0.5, 1000, week, now)
	tail, _ := score(0.05, 1000, week, now)
	far, _ := score(0.5, 1000, now.Add(90*24*time.Hour), now)
	if !(coin > tail && coin > far) {
		t.Fatalf("score: coin flip %v, tail %v, far %v", coin, tail, far)
	}
	for _, c := range []struct {
		p, v float64
		at   time.Time
	}{{0.5, 0, week}, {1, 100, week}, {0.5, 100, now.Add(30 * time.Minute)}, {0.5, 100, now.Add(400 * 24 * time.Hour)}} {
		if _, ok := score(c.p, c.v, c.at, now); ok {
			t.Errorf("score(%v, %v, %v) must be ineligible", c.p, c.v, c.at)
		}
	}
	got := dedupe([]Signal{
		{Source: SourceKalshi, Question: "Fed cuts 25 bps?", Market: "A"},
		{Source: SourcePolymarket, Question: "fed cuts 25bps", Market: "B"},
		{Source: SourceKalshi, Question: "Something else", Market: "A"},
		{Source: SourcePolymarket, Question: "Other", Market: "C"},
	})
	if len(got) != 2 || got[1].Market != "C" {
		t.Fatalf("dedupe %+v", got)
	}
}
