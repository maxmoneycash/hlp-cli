package core_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/core/coretest"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hyperliquid/ws"
	"github.com/maxmoneycash/hlp/internal/ticks"
)

// fakeTradeStream replays trades frames (with socket generations) to every
// trades subscription, then closes the channel.
type fakeTradeStream struct {
	mu     sync.Mutex
	frames []ws.Event
	subs   []ws.Subscription
}

func (f *fakeTradeStream) Subscribe(_ context.Context, sub ws.Subscription) (<-chan ws.Event, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs = append(f.subs, sub)
	ch := make(chan ws.Event, len(f.frames))
	for _, e := range f.frames {
		ch <- e
	}
	close(ch)
	return ch, func() {}, nil
}

func (f *fakeTradeStream) Close() error { return nil }

func tradesFrame(t *testing.T, gen int64, ts ...domain.Trade) ws.Event {
	t.Helper()
	type wire struct {
		Coin string `json:"coin"`
		Side string `json:"side"`
		Px   string `json:"px"`
		Sz   string `json:"sz"`
		Time int64  `json:"time"`
		TID  uint64 `json:"tid"`
	}
	var w []wire
	for _, x := range ts {
		w = append(w, wire{x.Coin, string(x.Side), jsonNum(x.Px), jsonNum(x.Sz), x.Time, x.TID})
	}
	b, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	return ws.Event{Channel: "trades", Data: b, Gen: gen}
}

func jsonNum(f float64) string { b, _ := json.Marshal(f); return string(b) }

func btc(msAgo int64, tid uint64, px, sz float64, side domain.Side) domain.Trade {
	return domain.Trade{Coin: "BTC", Side: side, Px: px, Sz: sz, Time: coretest.Now.UnixMilli() - msAgo, TID: tid}
}

func TestTickCandlesSnapshotFromSeedAndStore(t *testing.T) {
	env := coretest.Env(t, nil)
	res := mustInvoke(t, env, "get_tick_candles", `{"coin":"btc","ticks":1}`)
	d := res.Data.(*core.TickChartData)
	if res.Card != core.CardTicks || d.Coin != "BTC" || d.Label != "1T" || d.Event != "snapshot" {
		t.Fatalf("%+v", d)
	}
	// the fixture's two recent trades, one bar each
	if len(d.Bars) != 2 || !d.Bars[0].Closed || d.Bars[1].O != 73499 || d.Bars[1].SellV != 0.2 || d.Bars[1].I != 1 {
		t.Fatalf("bars %+v", d.Bars)
	}
	h := d.History
	if !h.LocalOnly || h.Seeded != 2 || h.Trades != 2 || !h.Recording || h.From != coretest.Now.UnixMilli()-2000 || h.Note == "" {
		t.Fatalf("history %+v", h)
	}
	if !strings.HasPrefix(h.Path, filepath.Join(env.Paths.CacheDir, "ticks", "mainnet")) {
		t.Fatalf("path %s", h.Path)
	}
	if d.Plain == "" || !strings.Contains(d.Plain, "1T") {
		t.Fatalf("no plain chart:\n%s", d.Plain)
	}
	// a second call stores nothing new: the seed is deduped against the store
	d2 := mustInvoke(t, env, "get_tick_candles", `{"coin":"BTC","ticks":1}`).Data.(*core.TickChartData)
	if d2.History.Seeded != 0 || d2.History.Trades != 2 || len(d2.Bars) != 2 {
		t.Fatalf("second call %+v", d2.History)
	}
	// JSON names the UI reads
	b, _ := json.Marshal(d2)
	for _, k := range []string{`"bar":{"kind":"ticks","size":1}`, `"tLast"`, `"vUsd"`, `"buyV"`, `"sellV"`, `"firstTid"`, `"lastTid"`, `"closed"`, `"localOnly":true`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("JSON lacks %s: %s", k, b)
		}
	}
}

func TestTickCandlesUsage(t *testing.T) {
	env := coretest.Env(t, nil)
	for _, args := range []string{
		`{"coin":"BTC","ticks":100,"usdBars":1000}`,
		`{"coin":"BTC","ticks":-5}`,
		`{"coin":"BTC","rangeBars":-1}`,
		`{"coin":"BTC","bars":9000}`,
		`{"coin":"NOPE"}`,
	} {
		if _, err := invoke(t, env, "get_tick_candles", args); code(err) != core.CodeUsage && code(err) != "UNKNOWN_COIN" {
			t.Fatalf("%s: %v", args, err)
		}
	}
	// default bar type is 500 ticks
	d := mustInvoke(t, env, "get_tick_candles", `{"coin":"BTC"}`).Data.(*core.TickChartData)
	if d.Bar.Kind != ticks.KindTicks || d.Bar.Size != 500 || len(d.Bars) != 1 || d.Bars[0].Closed {
		t.Fatalf("default %+v", d)
	}
	u := mustInvoke(t, env, "get_tick_candles", `{"coin":"BTC","usdBars":1000000}`).Data.(*core.TickChartData)
	if u.Bar.Kind != ticks.KindUSD || u.Bar.Size != 1e6 || u.Label != "$1M" {
		t.Fatalf("usd %+v", u.Bar)
	}
}

func TestTickCandlesLiveStreamGapsAndDedupe(t *testing.T) {
	env := coretest.Env(t, nil)
	fs := &fakeTradeStream{frames: []ws.Event{
		// first socket: the snapshot overlaps the seed (tid 2), then new prints
		tradesFrame(t, 1, btc(1000, 2, 73499, 0.2, domain.Sell), btc(900, 3, 73510, 0.5, domain.Buy)),
		tradesFrame(t, 1, btc(800, 4, 73520, 0.1, domain.Buy), btc(800, 4, 73520, 0.1, domain.Buy)), // duplicate tid
		// reconnect with no overlap: a gap
		tradesFrame(t, 2, btc(100, 10, 73400, 1, domain.Sell), btc(50, 11, 73390, 1, domain.Sell)),
	}}
	env.Stream = fs
	var events []*core.TickChartData
	err := core.Default().RunStream(core.WithSource(context.Background(), "cli"), env,
		core.Call{Tool: "get_tick_candles", Args: json.RawMessage(`{"coin":"BTC","ticks":3,"live":true}`)},
		func(r *core.Result) { events = append(events, r.Data.(*core.TickChartData)) })
	if err != nil {
		t.Fatal(err)
	}
	if len(fs.subs) != 1 || fs.subs[0].Type != "trades" || !fs.subs[0].Lossless {
		t.Fatalf("subscription %+v", fs.subs)
	}
	if len(events) < 2 || events[0].Event != "snapshot" {
		t.Fatalf("events %d", len(events))
	}
	last := events[len(events)-1]
	if last.Event != "update" {
		t.Fatalf("event %s", last.Event)
	}
	all := last.All
	// seed (tids 1,2) + 3 + 4 = one full bar of 3, then a partial bar of 1
	// cut by the gap, then the gap bar (10, 11) still open
	if len(all) != 3 {
		t.Fatalf("bars %+v", all)
	}
	if !all[0].Closed || all[0].N != 3 || all[0].Partial {
		t.Fatalf("bar0 %+v", all[0])
	}
	if !all[1].Partial || all[1].N != 1 || all[1].FirstTID != 4 {
		t.Fatalf("bar1 (cut by the gap) %+v", all[1])
	}
	if !all[2].Gap || all[2].Closed || all[2].N != 2 || all[2].SellV != 2 || all[2].I != 2 {
		t.Fatalf("bar2 (after the gap) %+v", all[2])
	}
	// updates carry only changed bars, newest last, upsertable by i
	for _, ev := range events[1:] {
		if len(ev.Bars) == 0 || ev.Bars[len(ev.Bars)-1].I != ev.All[len(ev.All)-1].I {
			t.Fatalf("update %+v", ev.Bars)
		}
	}
	if last.History.Gaps != 1 || last.History.Trades != 6 || last.History.Bytes != 8+6*33 || !last.History.Recording {
		t.Fatalf("history %+v", last.History)
	}
	// the store holds every trade once, the gap marked on tid 10
	s, err := ticks.Open(filepath.Dir(last.History.Path), "BTC", ticks.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rs, _ := s.LoadDay(ticks.DayOf(coretest.Now.UnixMilli()))
	if len(rs) != 6 {
		t.Fatalf("stored %d trades", len(rs))
	}
	for _, r := range rs {
		if r.Gap != (r.TID == 10) {
			t.Fatalf("gap flags %+v", rs)
		}
	}
	// a rebuild from the store gives the same bars as the live stream
	again := mustInvoke(t, env, "get_tick_candles", `{"coin":"BTC","ticks":3}`).Data.(*core.TickChartData)
	if len(again.Bars) != 3 || again.Bars[1].Partial != true || !again.Bars[2].Gap || again.Bars[2].N != 2 {
		t.Fatalf("rebuild %+v", again.Bars)
	}
}

func TestTickStatusAndRecord(t *testing.T) {
	env := coretest.Env(t, nil)
	env.Stream = &fakeTradeStream{frames: []ws.Event{tradesFrame(t, 1, btc(10, 77, 73600, 1, domain.Buy))}}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var last *core.TickStatusData
	err := core.RecordTicks(ctx, env, []string{"btc"}, 50*time.Millisecond, func(r *core.Result) { last = r.Data.(*core.TickStatusData) })
	if err == nil || ctx.Err() == nil {
		t.Fatalf("record ended early: %v", err)
	}
	if len(last.Coins) != 1 || !last.Coins[0].Recording || last.Coins[0].Trades != 3 || last.Coins[0].Session != 3 {
		t.Fatalf("record status %+v", last.Coins)
	}
	res, err := core.TickStatus(context.Background(), env, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := res.Data.(*core.TickStatusData)
	if len(st.Coins) != 1 || st.Coins[0].Coin != "BTC" || st.Coins[0].Trades != 3 || !st.LocalOnly {
		t.Fatalf("status %+v", st)
	}
	// a coin someone else records is refused, not double-written
	s, _ := ticks.Open(env.Paths.CacheDir+"/ticks/mainnet", "BTC", ticks.Options{})
	if !s.TryLease() {
		t.Fatal("lease")
	}
	defer s.Close()
	err = core.RecordTicks(context.Background(), env, []string{"BTC"}, time.Second, func(*core.Result) {})
	if code(err) != core.CodeUsage {
		t.Fatalf("double recorder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.Paths.CacheDir, "ticks", "mainnet", "BTC", "writer.lock")); err != nil {
		t.Fatal(err)
	}
}

func TestTickCandlesStreamResnapshotsOnOlderTrades(t *testing.T) {
	env := coretest.Env(t, nil)
	// the socket's first frame reaches further back than the seed (tids 1, 2)
	env.Stream = &fakeTradeStream{frames: []ws.Event{
		tradesFrame(t, 1, btc(5000, 50, 73000, 1, domain.Buy), btc(4000, 51, 73100, 1, domain.Buy), btc(1000, 2, 73499, 0.2, domain.Sell)),
	}}
	var events []*core.TickChartData
	err := core.Default().RunStream(core.WithSource(context.Background(), "cli"), env,
		core.Call{Tool: "get_tick_candles", Args: json.RawMessage(`{"coin":"BTC","ticks":2,"live":true}`)},
		func(r *core.Result) { events = append(events, r.Data.(*core.TickChartData)) })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Event != "snapshot" {
		t.Fatalf("want a second snapshot, got %d events", len(events))
	}
	bars := events[1].Bars
	// rebuilt in time order: (50, 51) then (1, 2)
	if len(bars) != 2 || bars[0].FirstTID != 50 || bars[0].O != 73000 || bars[1].FirstTID != 1 || bars[1].I != 1 {
		t.Fatalf("rebuilt %+v", bars)
	}
	if events[1].History.From != coretest.Now.UnixMilli()-5000 {
		t.Fatalf("from %d", events[1].History.From)
	}
}
