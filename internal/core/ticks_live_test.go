package core_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/ticks"
)

// TestLiveTickCandles streams real BTC trades for a few seconds into a
// throwaway cache and checks the bars and the store. HLP_LIVE=1 runs it.
func TestLiveTickCandles(t *testing.T) {
	if os.Getenv("HLP_LIVE") != "1" {
		t.Skip("set HLP_LIVE=1 to run live tests")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	env, err := core.NewEnv(context.Background(), core.EnvOptions{ConfigPath: filepath.Join(dir, "config.toml")})
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	ctx, cancel := context.WithTimeout(core.WithSource(context.Background(), "cli"), 12*time.Second)
	defer cancel()
	var events []*core.TickChartData
	err = core.Default().RunStream(ctx, env, core.Call{Tool: "get_tick_candles", Args: json.RawMessage(`{"coin":"BTC","ticks":10,"live":true}`)},
		func(r *core.Result) {
			events = append(events, r.Data.(*core.TickChartData))
			if len(events) >= 6 {
				cancel()
			}
		})
	if err != nil && ctx.Err() == nil {
		t.Fatal(err)
	}
	if len(events) < 2 || events[0].Event != "snapshot" {
		t.Fatalf("got %d events", len(events))
	}
	last := events[len(events)-1]
	seen := map[int64]bool{}
	for _, b := range last.All {
		if b.L > b.O || b.O > b.H || b.L > b.C || b.C > b.H || b.N < 1 || b.T > b.TLast || b.V <= 0 {
			t.Fatalf("malformed bar %+v", b)
		}
		if b.Closed && !b.Partial && b.N != 10 {
			t.Fatalf("closed bar with %d trades: %+v", b.N, b)
		}
		if seen[b.I] {
			t.Fatalf("index %d twice", b.I)
		}
		seen[b.I] = true
	}
	s, err := ticks.Open(filepath.Dir(last.History.Path), "BTC", ticks.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, _ := s.Stats()
	if st.Trades < 10 || !last.History.LocalOnly {
		t.Fatalf("stored %+v history %+v", st, last.History)
	}
	t.Logf("%d events, %d bars, %d trades stored, %d gaps, seeded %d", len(events), len(last.All), st.Trades, last.History.Gaps, events[0].History.Seeded)
}
