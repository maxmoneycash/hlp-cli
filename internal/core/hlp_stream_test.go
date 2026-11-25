package core

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hyperliquid/ws"
)

type hlpStreamFixture struct {
	channels  map[string]chan ws.Event
	ready     chan string
	cancelled chan string
	fail      string
}

func (f *hlpStreamFixture) Close() error { return nil }
func (f *hlpStreamFixture) Subscribe(_ context.Context, sub ws.Subscription) (<-chan ws.Event, func(), error) {
	if sub.User == f.fail {
		return nil, nil, errors.New("subscription failed")
	}
	ch := f.channels[sub.User]
	f.ready <- sub.User
	var once sync.Once
	return ch, func() { once.Do(func() { f.cancelled <- sub.User }) }, nil
}

func seededHLPStream(t *testing.T, fail string) (*Env, *hlpStreamFixture, context.Context, context.CancelFunc) {
	t.Helper()
	f := &hlpStreamFixture{channels: map[string]chan ws.Event{"parent": make(chan ws.Event, 1), "child": make(chan ws.Event, 1)},
		ready: make(chan string, 2), cancelled: make(chan string, 2), fail: fail}
	env := &Env{Stream: f, Now: func() time.Time { return time.UnixMilli(1000) }}
	storeHLPSnap(env, []string{"parent", "child"}, map[string]domain.Account{
		"parent": {Address: "parent", Equity: 100, PerpEquity: 100},
		"child":  {Address: "child", Equity: 200, PerpEquity: 200},
	})
	t.Cleanup(func() { hlpSnaps.Delete(env) })
	ctx, cancel := context.WithTimeout(WithSeed(context.Background(), &Result{Card: CardAccount}), time.Second)
	return env, f, ctx, cancel
}

func TestHLPStreamFailsWhenSubscriptionOrChildFeedCloses(t *testing.T) {
	for _, mode := range []string{"subscribe", "closed", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			fail := ""
			if mode == "subscribe" {
				fail = "child"
			}
			env, f, ctx, cancel := seededHLPStream(t, fail)
			defer cancel()
			if mode == "closed" {
				close(f.channels["child"])
			}
			if mode == "rejected" {
				f.channels["child"] <- ws.Event{Err: errors.New("denied")}
			}
			err := streamHLP(ctx, env, nil, func(*Result) {})
			if e := AsError(err); e.Code != CodeNetwork || !e.Retryable {
				t.Fatalf("dead feed must return retryable network failure: %v", err)
			}
			want := 2
			if mode == "subscribe" {
				want = 1
			}
			for i := 0; i < want; i++ {
				select {
				case <-f.cancelled:
				case <-time.After(time.Second):
					t.Fatal("sibling subscription was not released")
				}
			}
		})
	}
}

func TestHLPStreamRemainsPartialUntilEveryChildUpdates(t *testing.T) {
	env, f, ctx, cancel := seededHLPStream(t, "")
	defer cancel()
	results := make(chan *Result, 2)
	done := make(chan error, 1)
	go func() { done <- streamHLP(ctx, env, nil, func(r *Result) { results <- r }) }()
	for i := 0; i < 2; i++ {
		select {
		case <-f.ready:
		case <-ctx.Done():
			t.Fatal("subscriptions did not start")
		}
	}
	for i, update := range []struct {
		addr string
		gen  int64
	}{
		{"parent", 1}, {"child", 1}, {"child", 2}, {"parent", 2},
	} {
		address := update.addr
		if i == 3 {
			// A late gen-1 child cannot complete the gen-2 aggregate.
			f.channels["parent"] <- ws.Event{Channel: "allDexsClearinghouseState", Gen: 1, Data: json.RawMessage(`{"user":"parent","clearinghouseStates":[["",{"assetPositions":[],"time":999}]]}`)}
		}
		f.channels[address] <- ws.Event{Channel: "allDexsClearinghouseState", Gen: update.gen, Data: json.RawMessage(`{"user":"` + address + `","clearinghouseStates":[["",{"marginSummary":{"accountValue":"100","totalNtlPos":"0","totalMarginUsed":"0"},"assetPositions":[],"time":1000}]]}`)}
		select {
		case result := <-results:
			wantGeneration := update.gen
			if i%2 == 0 {
				wantGeneration = 0
			}
			if result.Data.(domain.Account).StreamGeneration != wantGeneration || result.Meta.Source != "hyperliquid-ws" || result.Meta.Partial != (i%2 == 0) {
				t.Fatalf("child %s readiness: %+v", address, result.Meta)
			}
			if i == 0 && len(result.Meta.Warnings) == 0 {
				t.Fatal("pending child has no source warning")
			}
		case <-ctx.Done():
			t.Fatal("no streamed aggregate")
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestAccountStreamPreservesPartialSnapshotAndReportsClosure(t *testing.T) {
	const address = "0x1111111111111111111111111111111111111111"
	ch := make(chan ws.Event, 4)
	f := &hlpStreamFixture{channels: map[string]chan ws.Event{address: ch}, ready: make(chan string, 1), cancelled: make(chan string, 1)}
	env := &Env{Stream: f}
	seed := &Result{Card: CardAccount, Data: domain.Account{Address: address, Partial: true, Warnings: []string{"spot balance unavailable"}}}
	ctx, cancel := context.WithTimeout(WithSeed(context.Background(), seed), time.Second)
	defer cancel()
	ch <- ws.Event{Channel: "allDexsClearinghouseState", Gen: 7, Data: json.RawMessage(`{"user":"` + address + `","clearinghouseStates":[["",{"assetPositions":[{"position":{"coin":"ETH","szi":"bad"}}],"time":997}]]}`)}
	ch <- ws.Event{Channel: "allDexsClearinghouseState", Gen: 7, Data: json.RawMessage(`{"user":"` + address + `","clearinghouseStates":[["",{"time":998}]]}`)}
	ch <- ws.Event{Channel: "allDexsClearinghouseState", Gen: 7, Data: json.RawMessage(`{"user":"other","clearinghouseStates":[["",{"assetPositions":[],"time":999}]]}`)}
	ch <- ws.Event{Channel: "allDexsClearinghouseState", Gen: 7, Data: json.RawMessage(`{"user":"` + address + `","clearinghouseStates":[["",{"marginSummary":{"accountValue":"100","totalNtlPos":"0","totalMarginUsed":"0"},"assetPositions":[],"time":1000}]]}`)}
	var received *Result
	err := watchAddress(ctx, env, &AddressInput{Address: address}, false, func(r *Result) { received = r; close(ch) })
	if received == nil || received.Data.(domain.Account).Time != 1000 || received.Data.(domain.Account).StreamGeneration != 7 || !received.Meta.Partial || len(received.Meta.Warnings) != 1 || received.Meta.Warnings[0] != "spot balance unavailable" {
		t.Fatalf("WS update erased snapshot caveat: %+v", received)
	}
	if e := AsError(err); e.Code != CodeNetwork || !e.Retryable {
		t.Fatalf("closed account feed: %v", err)
	}
}
