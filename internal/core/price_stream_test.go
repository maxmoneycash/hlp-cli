package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/core/coretest"
	"github.com/maxmoneycash/hlp-cli/internal/hyperliquid/ws"
)

type priceFeed struct{ contexts, books chan ws.Event }

func (f *priceFeed) Close() error { return nil }
func (f *priceFeed) Subscribe(_ context.Context, sub ws.Subscription) (<-chan ws.Event, func(), error) {
	ch := f.books
	if sub.Type == "activeAssetCtx" {
		ch = f.contexts
	}
	return ch, func() { close(ch) }, nil
}

func TestPriceStreamFailsWhenEitherFeedCloses(t *testing.T) {
	for _, channel := range []string{"bbo", "activeAssetCtx"} {
		t.Run(channel, func(t *testing.T) {
			env := coretest.Env(t, nil)
			feed := &priceFeed{make(chan ws.Event, 1), make(chan ws.Event, 1)}
			env.Stream = feed
			seed := &core.Result{Card: core.CardPrice, Data: []core.PriceRow{{Coin: "BTC", Mark: 100, Mid: 101}}}
			ctx, cancel := context.WithTimeout(core.WithSeed(context.Background(), seed), time.Second)
			defer cancel()
			ch := feed.books
			if channel == "activeAssetCtx" {
				ch = feed.contexts
			}
			ch <- ws.Event{Err: errors.New("subscription rejected")}
			err := core.Default().RunStream(ctx, env, core.Call{Tool: "get_price", Args: json.RawMessage(`{"coins":["BTC"],"live":true}`)}, func(*core.Result) {})
			if e := core.AsError(err); e == nil || e.Code != core.CodeNetwork || !e.Retryable {
				t.Fatalf("must report a retryable disconnect, got %v", err)
			}
		})
	}
}

func TestPriceStreamRefreshesContextAndPreservesBBO(t *testing.T) {
	env := coretest.Env(t, nil)
	feed := &priceFeed{make(chan ws.Event, 1), make(chan ws.Event, 1)}
	env.Stream = feed
	seed := &core.Result{Card: core.CardPrice, Data: []core.PriceRow{{Coin: "BTC", Mark: 100, Mid: 101, Bid: 100, Ask: 102, Funding: 0.0001}}}
	ctx, cancel := context.WithTimeout(core.WithSeed(context.Background(), seed), 2*time.Second)
	defer cancel()
	feed.contexts <- ws.Event{Channel: "activeAssetCtx", Data: json.RawMessage(`{"coin":"BTC","ctx":{"markPx":"110","midPx":"109","oraclePx":"111","prevDayPx":"100","funding":"0","openInterest":"0","dayNtlVlm":"123456"}}`)}
	frames := 0
	err := core.Default().RunStream(ctx, env, core.Call{Tool: "get_price", Args: json.RawMessage(`{"coins":["BTC"],"live":true}`)}, func(result *core.Result) {
		frames++
		row := result.Data.([]core.PriceRow)[0]
		switch frames {
		case 2:
			if row.OpenInterest == nil || *row.OpenInterest != 0 || row.DayNtlVlm == nil || *row.DayNtlVlm != 123456 || row.PrevDay != 100 || row.ContextTime != coretest.Now.UnixMilli() {
				t.Errorf("context units/time lost: %+v", row)
			}
			if row.Mark != 110 || row.Oracle != 111 || row.Change24h != 0.1 || row.Funding != 0 || row.Mid != 101 || row.Bid != 100 {
				t.Errorf("context update lost fields: %+v", row)
			}
			feed.books <- ws.Event{Channel: "bbo", Data: json.RawMessage(`{"coin":"BTC","time":123,"bbo":[{"px":"112","sz":"1","n":1},{"px":"114","sz":"1","n":1}]}`)}
		case 3:
			if row.OpenInterest == nil || *row.OpenInterest != 0 || row.DayNtlVlm == nil || *row.DayNtlVlm != 123456 || row.PrevDay != 100 || row.ContextTime != coretest.Now.UnixMilli() {
				t.Errorf("BBO overwrote context units/time: %+v", row)
			}
			if row.Mark != 110 || row.Oracle != 111 || row.Mid != 113 || row.Bid != 112 || row.Ask != 114 || row.Change24h != 0.1 {
				t.Errorf("BBO update lost context: %+v", row)
			}
			cancel()
		}
	})
	if err != nil || frames != 3 {
		t.Fatalf("frames=%d err=%v", frames, err)
	}
	if seed.Data.([]core.PriceRow)[0].Mark != 100 {
		t.Fatal("stream mutated its seed")
	}
}
