package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/maxmoneycash/hlp/internal/domain"
)

const fxDir = "../../../testdata/fixtures/hyperliquid/ws"

func fx(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fxDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeWS replays fixtures over a real WebSocket.
type fakeWS struct {
	t   testing.TB
	srv *httptest.Server

	mu       sync.Mutex
	conns    int
	subs     []string // canonical keys of subscribe requests
	unsubs   []string
	pings    int
	reject   map[string]bool
	dropOnce bool // close the first connection after its first data frame
	fast     json.RawMessage
}

func newFakeWS(t testing.TB) *fakeWS {
	f := &fakeWS{t: t, reject: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeWS) url() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") }

func (f *fakeWS) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.subs {
		if s == key {
			n++
		}
	}
	return n
}

// fixtureFrame returns the data frame to replay for a subscription.
func (f *fakeWS) fixtureFrame(sub map[string]any) []byte {
	switch sub["type"] {
	case "l2Book":
		return fx(f.t, "l2Book_BTC.json")
	case "candle":
		return fx(f.t, "candle.json")
	case "trades":
		return fx(f.t, "trades_BTC.json")
	case "bbo":
		return fx(f.t, "bbo_BTC.json")
	case "userFills":
		return fx(f.t, "userFills.json")
	case "allDexsClearinghouseState":
		return fx(f.t, "allDexsClearinghouseState.json")
	case "allMids":
		if sub["dex"] == "xyz" {
			return fx(f.t, "allMids_dex_xyz.json")
		}
		return fx(f.t, "allMids.json")
	case "fastAssetCtxs":
		b, _ := json.Marshal(map[string]any{"channel": "fastAssetCtxs", "data": f.fast})
		return b
	case "userEvents":
		return fx(f.t, "user.json")
	case "activeAssetCtx":
		if sub["coin"] == "@107" {
			return fx(f.t, "activeSpotAssetCtx_107.json")
		}
		return fx(f.t, "activeAssetCtx_BTC.json")
	}
	return nil
}

// serverForm adds the defaults the real server echoes in acks.
func serverForm(sub map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range sub {
		out[k] = v
	}
	switch sub["type"] {
	case "l2Book":
		for _, k := range []string{"nSigFigs", "mantissa"} {
			if _, ok := out[k]; !ok {
				out[k] = nil
			}
		}
		if _, ok := out["fast"]; !ok {
			out["fast"] = false
		}
	case "userFills":
		out["aggregateByTime"] = false
	case "clearinghouseState", "openOrders":
		if _, ok := out["dex"]; !ok {
			out["dex"] = ""
		}
	}
	return out
}

func (f *fakeWS) serve(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow()
	f.mu.Lock()
	f.conns++
	connN := f.conns
	f.mu.Unlock()
	ctx := r.Context()
	write := func(b []byte) { _ = c.Write(ctx, websocket.MessageText, b) }
	write([]byte(`[]`)) // frames without a channel must be ignored
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			return
		}
		var m struct {
			Method       string          `json:"method"`
			Subscription json.RawMessage `json:"subscription"`
		}
		_ = json.Unmarshal(msg, &m)
		var sub map[string]any
		_ = json.Unmarshal(m.Subscription, &sub)
		switch m.Method {
		case "ping":
			f.mu.Lock()
			f.pings++
			f.mu.Unlock()
			write([]byte(`{"channel":"pong"}`))
		case "unsubscribe":
			f.mu.Lock()
			f.unsubs = append(f.unsubs, canonical(m.Subscription))
			f.mu.Unlock()
		case "subscribe":
			f.mu.Lock()
			f.subs = append(f.subs, canonical(m.Subscription))
			reject := f.reject[fmt.Sprint(sub["type"])]
			drop := f.dropOnce && connN == 1
			f.mu.Unlock()
			if reject {
				e, _ := json.Marshal(map[string]any{"channel": "error", "data": "Error parsing JSON into valid websocket request: " + string(msg)})
				write(e)
				continue
			}
			ack, _ := json.Marshal(map[string]any{"channel": "subscriptionResponse", "data": map[string]any{"method": "subscribe", "subscription": serverForm(sub)}})
			write(ack)
			if fr := f.fixtureFrame(sub); fr != nil {
				write(fr)
				if fr2 := f.fixtureFrame(sub); sub["type"] == "userFills" {
					write(fr2) // a resent snapshot must be deduped
				}
			}
			if drop {
				time.Sleep(20 * time.Millisecond)
				c.Close(websocket.StatusGoingAway, "test drop")
				return
			}
		}
	}
}

func validator(coin string) error {
	switch coin {
	case "BTC", "ETH", "xyz:NVDA", "@107":
		return nil
	}
	return fmt.Errorf("unknown coin %q", coin)
}

func testConn(f *fakeWS) *Conn {
	return NewConn(Options{URL: f.url(), Validate: validator, BackoffMin: 5 * time.Millisecond, BackoffMax: 20 * time.Millisecond})
}

func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v, ok := <-ch:
		if !ok {
			t.Fatal("channel closed")
		}
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
	var zero T
	return zero
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestKeyNormalization(t *testing.T) {
	ours := Subscription{Type: "l2Book", Coin: "BTC"}
	server := `{"type":"l2Book","coin":"BTC","nSigFigs":null,"mantissa":null,"fast":false}`
	if ours.Key() != canonical([]byte(server)) {
		t.Fatalf("%s != %s", ours.Key(), canonical([]byte(server)))
	}
	u := Subscription{Type: "clearinghouseState", User: "0xF5D81A135F756CA16544E53C20FC20643EC3AD53"}
	if u.Key() != canonical([]byte(`{"type":"clearinghouseState","dex":"","user":"0xf5d81a135f756ca16544e53c20fc20643ec3ad53"}`)) {
		t.Fatal("user/dex normalization")
	}
	if (Subscription{Type: "l2Book", Coin: "BTC", Fast: true}).Key() == ours.Key() {
		t.Fatal("fast must be part of the key")
	}
	// Every ack in the fixture normalizes to a key built from a Subscription.
	var acks []struct {
		Data struct {
			Subscription json.RawMessage `json:"subscription"`
		} `json:"data"`
	}
	if err := json.Unmarshal(fx(t, "_subscriptionResponses.json"), &acks); err != nil {
		t.Fatal(err)
	}
	for _, a := range acks {
		var s Subscription
		if err := json.Unmarshal(a.Data.Subscription, &s); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(a.Data.Subscription), "ignorePortfolioMargin") || strings.Contains(string(a.Data.Subscription), "aggregateByTime") {
			continue // flags we never set; they normalize away as false
		}
		if s.Key() != canonical(a.Data.Subscription) {
			t.Errorf("ack %s: %s != %s", a.Data.Subscription, s.Key(), canonical(a.Data.Subscription))
		}
	}
}

func TestSubscribeAckAndBooks(t *testing.T) {
	f := newFakeWS(t)
	var states []string
	var smu sync.Mutex
	c := NewConn(Options{URL: f.url(), Validate: validator, OnState: func(s string) { smu.Lock(); states = append(states, s); smu.Unlock() }})
	defer c.Close()
	books, cancel, err := Books(context.Background(), c, "BTC", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	b := recv(t, books)
	if b.Coin != "BTC" || len(b.Bids) != 5 || len(b.Asks) != 5 || b.Bids[0].Px != 65457 || b.Mid() == 0 {
		t.Fatalf("book %+v", b)
	}
	sub := Subscription{Type: "l2Book", Coin: "BTC", Fast: true}
	eventually(t, "ack", func() bool { return c.Acked(sub) })
	if c.State() != StateLive {
		t.Fatalf("state %s", c.State())
	}
	smu.Lock()
	if len(states) < 2 || states[0] != StateConnecting || states[1] != StateLive {
		t.Fatalf("states %v", states)
	}
	smu.Unlock()
}

func TestBooksDepthKeepsFullDepthSubscription(t *testing.T) {
	f := newFakeWS(t)
	c := testConn(f)
	defer c.Close()
	books, cancel, err := BooksDepth(context.Background(), c, "BTC", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	_ = recv(t, books)
	sub := Subscription{Type: "l2Book", Coin: "BTC"}
	eventually(t, "full-depth ack", func() bool { return c.Acked(sub) })
	if f.count((Subscription{Type: "l2Book", Coin: "BTC", Fast: true}).Key()) != 0 {
		t.Fatal("a deep book must not subscribe to the five-level fast feed")
	}
}

func TestUnknownCoinRefusedLocally(t *testing.T) {
	f := newFakeWS(t)
	c := testConn(f)
	defer c.Close()
	if _, _, err := c.Subscribe(context.Background(), Subscription{Type: "trades", Coin: "NOPE"}); err == nil {
		t.Fatal("unknown coin accepted")
	}
	f.mu.Lock()
	conns := f.conns
	f.mu.Unlock()
	if conns != 0 {
		t.Fatal("refused subscription must not even connect")
	}
	nv := NewConn(Options{URL: f.url()})
	if _, _, err := nv.Subscribe(context.Background(), Subscription{Type: "trades", Coin: "BTC"}); !errors.Is(err, ErrNoValidator) {
		t.Fatalf("no validator: %v", err)
	}
	if _, _, err := c.Subscribe(context.Background(), Subscription{Type: "webData2", User: "0x1"}); !errors.Is(err, ErrUnsupported) {
		t.Fatal("webData2 accepted")
	}
	if _, _, err := c.Subscribe(context.Background(), Subscription{}); err == nil {
		t.Fatal("empty type accepted")
	}
}

func TestReconnectResubscribes(t *testing.T) {
	f := newFakeWS(t)
	f.dropOnce = true
	c := testConn(f)
	defer c.Close()
	trades, cancel, err := Trades(context.Background(), c, "BTC")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	tr := recv(t, trades)
	if len(tr) != 2 || tr[0].Coin != "BTC" || tr[0].Side != domain.Sell {
		t.Fatalf("trades %+v", tr)
	}
	key := Subscription{Type: "trades", Coin: "BTC"}.Key()
	eventually(t, "resubscribe", func() bool { return f.count(key) >= 2 })
	tr2 := recv(t, trades) // replayed on the new socket
	if len(tr2) != 2 {
		t.Fatal("no data after reconnect")
	}
	if c.Dials() < 2 {
		t.Fatalf("dials %d", c.Dials())
	}
}

func TestRefCountingAndUnsubscribe(t *testing.T) {
	f := newFakeWS(t)
	c := testConn(f)
	defer c.Close()
	sub := Subscription{Type: "candle", Coin: "BTC", Interval: "1m"}
	ch1, cancel1, err := c.Subscribe(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	ev := recv(t, ch1)
	if ev.Channel != "candle" {
		t.Fatalf("%+v", ev)
	}
	ch2, cancel2, err := c.Subscribe(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if f.count(sub.Key()) != 1 {
		t.Fatal("second subscriber must not resubscribe")
	}
	cancel1()
	cancel1() // idempotent
	if _, ok := <-ch1; ok {
		t.Fatal("ch1 should be closed")
	}
	f.mu.Lock()
	n := len(f.unsubs)
	f.mu.Unlock()
	if n != 0 {
		t.Fatal("unsubscribed while a subscriber remains")
	}
	cancel2()
	eventually(t, "unsubscribe", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.unsubs) == 1 })
	for range ch2 {
	}
	// ctx cancellation also unsubscribes.
	ctx, cc := context.WithCancel(context.Background())
	ch3, _, err := c.Subscribe(ctx, Subscription{Type: "bbo", Coin: "ETH"})
	if err != nil {
		t.Fatal(err)
	}
	cc()
	eventually(t, "ctx close", func() bool {
		select {
		case _, ok := <-ch3:
			return !ok
		default:
			return false
		}
	})
}

func TestCandlesBBOAndRouting(t *testing.T) {
	f := newFakeWS(t)
	c := testConn(f)
	defer c.Close()
	ctx := context.Background()
	cs, cancel, err := Candles(ctx, c, "BTC", domain.I1m)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	k := recv(t, cs)
	if k.T != 1794735480000 || k.C != 65457 || k.N != 310 {
		t.Fatalf("candle %+v", k)
	}
	if _, _, err := Candles(ctx, c, "BTC", "6h"); err == nil {
		t.Fatal("bad interval")
	}
	bb, cancelB, err := BBO(ctx, c, "BTC")
	if err != nil {
		t.Fatal(err)
	}
	defer cancelB()
	b := recv(t, bb)
	if len(b.Bids) != 1 || b.Bids[0].Px != 65457 || b.Asks[0].Px != 65458 {
		t.Fatalf("bbo %+v", b)
	}
	// allMids native vs xyz are routed by dex.
	native, cn, _ := c.Subscribe(ctx, Subscription{Type: "allMids"})
	defer cn()
	xyz, cx, _ := c.Subscribe(ctx, Subscription{Type: "allMids", Dex: "xyz"})
	defer cx()
	if ev := recv(t, native); strings.Contains(string(ev.Data), "xyz:") {
		t.Fatal("xyz mids routed to the native subscriber")
	}
	if ev := recv(t, xyz); !strings.Contains(string(ev.Data), "xyz:AAPL") {
		t.Fatal("xyz mids missing")
	}
	// Spot ctx arrives on activeSpotAssetCtx and routes to activeAssetCtx.
	sp, cs2, err := c.Subscribe(ctx, Subscription{Type: "activeAssetCtx", Coin: "@107"})
	if err != nil {
		t.Fatal(err)
	}
	defer cs2()
	if ev := recv(t, sp); ev.Channel != "activeSpotAssetCtx" {
		t.Fatalf("spot ctx %+v", ev)
	}
}

func TestFastAssetCtxsRoundTrip(t *testing.T) {
	var dec struct {
		SnapshotSample map[string]FastCtx `json:"snapshotSample"`
		SnapshotXyz    map[string]FastCtx `json:"snapshotXyz"`
		DeltaSample    map[string]FastCtx `json:"deltaSample"`
	}
	if err := json.Unmarshal(fx(t, "fastAssetCtxs_decoded.json"), &dec); err != nil {
		t.Fatal(err)
	}
	snap := map[string]FastCtx{}
	for k, v := range dec.SnapshotSample {
		snap[k] = v
	}
	for k, v := range dec.SnapshotXyz {
		snap[k] = v
	}
	enc, err := EncodeFastAssetCtxs(snap)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeFastAssetCtxs(enc)
	if err != nil || len(back) != len(snap) || back["io:EWY"].MarkPx != "139.95" {
		t.Fatalf("round trip %v %+v", err, back)
	}
	if _, err := DecodeFastAssetCtxs(json.RawMessage(`"!!notbase64"`)); err == nil {
		t.Fatal("garbage accepted")
	}
	if m, err := DecodeFastAssetCtxs(json.RawMessage(`{"BTC":{"markPx":"1"}}`)); err != nil || m["BTC"].MarkPx != "1" {
		t.Fatal("plain object form")
	}

	f := newFakeWS(t)
	f.fast = enc
	c := testConn(f)
	defer c.Close()
	mids, cancel, err := Mids(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	m := recv(t, mids)
	if m["xyz:GBP"] != 1.3207 || m["#237381"] != 0.86327 || len(m) != 3 {
		t.Fatalf("mids %+v", m)
	}
}

func TestAssetContextsPerpAndSpot(t *testing.T) {
	f := newFakeWS(t)
	c := testConn(f)
	defer c.Close()
	for _, tc := range []struct {
		coin string
		mark float64
	}{{"BTC", 65454}, {"@107", 67.833}} {
		ch, cancel, err := AssetContexts(context.Background(), c, tc.coin)
		if err != nil {
			t.Fatal(err)
		}
		x := recv(t, ch)
		cancel()
		if x.MarkPx.Float() != tc.mark || x.PrevDayPx.Float() <= 0 {
			t.Fatalf("%s context: %+v", tc.coin, x)
		}
		if tc.coin == "BTC" && (x.Funding.Float() != 0.0000125 || x.OraclePx.Float() != 65468) {
			t.Fatalf("missing perp context: %+v", x)
		}
	}
}

func TestUserFillsDedupedAndPositions(t *testing.T) {
	f := newFakeWS(t)
	c := testConn(f)
	defer c.Close()
	user := "0xF5D81A135F756CA16544E53C20FC20643EC3AD53"
	fills, cancel, err := UserFills(context.Background(), c, user)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	first := recv(t, fills)
	if len(first) == 0 || first[0].Coin != "XRP" {
		t.Fatalf("fills %+v", first)
	}
	select {
	case dup := <-fills:
		t.Fatalf("resent snapshot leaked %d fills", len(dup))
	case <-time.After(150 * time.Millisecond):
	}
	pos, cancelP, err := Positions(context.Background(), c, user)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelP()
	a := recv(t, pos)
	if a.Address != strings.ToLower(user) || a.PerpEquity < 1e6 || len(a.Positions) == 0 || a.Positions[0].Coin != "BTC" {
		t.Fatalf("positions %+v", a)
	}
}

func TestServerErrorAndUserLimit(t *testing.T) {
	f := newFakeWS(t)
	f.reject["userEvents"] = true
	c := testConn(f)
	defer c.Close()
	ch, _, err := c.Subscribe(context.Background(), Subscription{Type: "userEvents", User: "0x01"})
	if err != nil {
		t.Fatal(err)
	}
	ev := recv(t, ch)
	if ev.Err == nil || !strings.Contains(ev.Err.Error(), "rejected") {
		t.Fatalf("expected error event, got %+v", ev)
	}
	if _, ok := <-ch; ok {
		t.Fatal("channel should close after the error")
	}
	var cancels []func()
	for i := 0; i < MaxUsers; i++ {
		_, cl, err := c.Subscribe(context.Background(), Subscription{Type: "webData3", User: fmt.Sprintf("0x%040d", i)})
		if err != nil {
			t.Fatal(err)
		}
		cancels = append(cancels, cl)
	}
	if _, _, err := c.Subscribe(context.Background(), Subscription{Type: "webData3", User: "0xffffffffffffffffffffffffffffffffffffffff"}); !errors.Is(err, ErrTooManyUsers) {
		t.Fatalf("11th user: %v", err)
	}
	// A known user may add more subscription types.
	if _, cl, err := c.Subscribe(context.Background(), Subscription{Type: "notification", User: fmt.Sprintf("0x%040d", 3)}); err != nil {
		t.Fatal(err)
	} else {
		cl()
	}
	for _, cl := range cancels {
		cl()
	}
}

func TestPingAndClose(t *testing.T) {
	f := newFakeWS(t)
	c := NewConn(Options{URL: f.url(), Validate: validator, PingInterval: 40 * time.Millisecond})
	if _, _, err := c.Subscribe(context.Background(), Subscription{Type: "allMids"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "ping", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.pings >= 2 })
	ch, _, _ := c.Subscribe(context.Background(), Subscription{Type: "allMids", Dex: "xyz"})
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if c.State() != StateClosed {
		t.Fatalf("state %s", c.State())
	}
	if _, _, err := c.Subscribe(context.Background(), Subscription{Type: "allMids"}); !errors.Is(err, ErrClosed) {
		t.Fatal("subscribe after close")
	}
	_ = c.Close()
	var _ Stream = Dial(Options{})
}

func TestLossyListenerKeepsLatest(t *testing.T) {
	l := newListener(false)
	for i := 0; i < 100; i++ {
		l.deliver(Event{Channel: fmt.Sprint(i)})
	}
	time.Sleep(20 * time.Millisecond)
	var got []string
	for {
		select {
		case ev := <-l.ch:
			got = append(got, ev.Channel)
			continue
		case <-time.After(50 * time.Millisecond):
		}
		break
	}
	// Queue (16) + the buffered channel (1) + the event in the pump's hand.
	if got[len(got)-1] != "99" || len(got) > marketQueue+2 {
		t.Fatalf("got %v", got)
	}
	l.close()
	lossless := newListener(true)
	for i := 0; i < 100; i++ {
		lossless.deliver(Event{Channel: fmt.Sprint(i)})
	}
	for i := 0; i < 100; i++ {
		if ev := <-lossless.ch; ev.Channel != fmt.Sprint(i) {
			t.Fatalf("lossless order broken at %d: %s", i, ev.Channel)
		}
	}
	lossless.close()
}

func TestLiveWS(t *testing.T) {
	if os.Getenv("HLP_LIVE") != "1" {
		t.Skip("set HLP_LIVE=1 to run live tests")
	}
	c := NewConn(Options{Validate: validator})
	defer c.Close()
	bb, cancel, err := BBO(context.Background(), c, "BTC")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	b := recv(t, bb)
	if b.Mid() <= 0 {
		t.Fatalf("%+v", b)
	}
}

func TestTradeBatchesLosslessWithGenerations(t *testing.T) {
	f := newFakeWS(t)
	f.dropOnce = true
	c := testConn(f)
	defer c.Close()
	ch, cancel, err := TradeBatches(context.Background(), c, "BTC")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	b1 := recv(t, ch)
	if len(b1.Trades) != 2 || b1.Gen != 1 {
		t.Fatalf("first %+v", b1)
	}
	b2 := recv(t, ch) // replayed on the reconnected socket
	if b2.Gen != 2 || len(b2.Trades) != 2 {
		t.Fatalf("after reconnect %+v", b2)
	}
	// the server never sees the local-only flag
	if n := f.count(Subscription{Type: "trades", Coin: "BTC"}.Key()); n < 2 {
		t.Fatalf("subscribes %d", n)
	}
}

func TestLosslessSubscriptionKeepsEveryFrame(t *testing.T) {
	l := newListener(Subscription{Type: "trades", Lossless: true}.Lossless)
	for i := 0; i < 100; i++ {
		l.deliver(Event{Channel: fmt.Sprint(i)})
	}
	for i := 0; i < 100; i++ {
		if ev := <-l.ch; ev.Channel != fmt.Sprint(i) {
			t.Fatalf("frame %d: %s", i, ev.Channel)
		}
	}
	l.close()
	if (Subscription{Type: "trades", Coin: "BTC", Lossless: true}).Key() != (Subscription{Type: "trades", Coin: "BTC"}).Key() {
		t.Fatal("Lossless changed the key")
	}
}

func TestTradeBatchesWithStatusOrdersReadinessAndOutageAroundPrints(t *testing.T) {
	f := newFakeWS(t)
	f.dropOnce = true
	c := testConn(f)
	defer c.Close()
	ch, stop, err := TradeBatchesWithStatus(context.Background(), c, "BTC")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for _, want := range []struct {
		state  string
		gen    int64
		prints int
	}{{StateLive, 1, 0}, {"", 1, 2}, {StateReconnecting, 1, 0}, {StateLive, 2, 0}, {"", 2, 2}} {
		got := recv(t, ch)
		if got.State != want.state || got.Gen != want.gen || len(got.Trades) != want.prints {
			t.Fatalf("want %+v got %+v", want, got)
		}
	}
}
