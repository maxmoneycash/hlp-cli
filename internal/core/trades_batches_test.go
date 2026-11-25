package core_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/core/coretest"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/hyperliquid/ws"
)

func TestTradeBatchesPreserveWholeExchangeFramesAndMarkAQuietReconnect(t *testing.T) {
	env := coretest.Env(t, nil)
	var burst []domain.Trade
	for i := 1; i <= 320; i++ {
		burst = append(burst, btc(1, uint64(i), 100, 1, domain.Buy))
	}
	feed := &fakeTradeStream{frames: []ws.Event{{State: ws.StateLive, Gen: 1}, tradesFrame(t, 1, burst...), {State: ws.StateReconnecting, Gen: 1}, {State: ws.StateLive, Gen: 2}}}
	env.Stream = feed
	var got []*core.Result
	err := core.Default().RunStream(core.WithSource(context.Background(), "cli"), env,
		core.Call{Tool: "get_trades", Args: json.RawMessage(`{"coin":"BTC","batches":true}`)}, func(r *core.Result) { got = append(got, r) })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[0].Meta.Source != "hyperliquid" || !feed.subs[0].Lossless || !feed.subs[0].Status {
		t.Fatalf("events=%d subscription=%+v", len(got), feed.subs)
	}
	for i, want := range []int{0, 320, 0, 0} {
		r := got[i+1]
		if r.Meta.Source != "hyperliquid-ws" || r.Meta.Stale != (i == 2) || len(r.Data.([]domain.Trade)) != want {
			t.Fatalf("frame %d: %+v", i, r)
		}
	}
}
