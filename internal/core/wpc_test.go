package core_test

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/core/coretest"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hyperliquid"
)

// ---- C-T6: shaping ----

func TestShapeArraysMissesAndTotal(t *testing.T) {
	acct := map[string]any{
		"address": "0xabc",
		"equity":  15000,
		"positions": []map[string]any{
			{"coin": "BTC", "size": 0.2, "leverage": map[string]any{"value": 5}},
			{"coin": "ETH", "size": -2, "leverage": map[string]any{"value": 3}},
		},
	}
	cases := []struct {
		name string
		data any
		sel  []string
		lim  int
		want string
		err  string
	}{
		{"path through an array", acct, []string{"positions.coin"}, 0, `{"positions":[{"coin":"BTC"},{"coin":"ETH"}]}`, ""},
		{"two paths through one array", acct, []string{"positions.coin", "positions.leverage.value"}, 0,
			`{"positions":[{"coin":"BTC","leverage":{"value":5}},{"coin":"ETH","leverage":{"value":3}}]}`, ""},
		{"parent and child", acct, []string{"equity", "positions"}, 0, "", ""},
		{"partial miss keeps the hits", acct, []string{"equity", "nope"}, 0, `{"equity":15000}`, ""},
		{"total miss is a usage error", acct, []string{"nope"}, 0, "", "matches no field"},
		{"list total miss", []map[string]any{{"a": 1}}, []string{"b"}, 0, "", "matches no field"},
		{"empty list passes", []map[string]any{}, []string{"b"}, 0, `[]`, ""},
		{"null passes", nil, []string{"b"}, 0, `null`, ""},
		{"case-insensitive key", map[string]any{"Last": 1.5}, []string{"last"}, 0, `{"last":1.5}`, ""},
		{"limit", []int{1, 2, 3}, nil, 2, `[1,2]`, ""},
	}
	for _, c := range cases {
		got, err := core.Shape(c.data, c.sel, c.lim)
		if c.err != "" {
			e := core.AsError(err)
			if e == nil || e.Code != core.CodeUsage || !strings.Contains(e.Message, c.err) {
				t.Errorf("%s: want USAGE %q, got %v", c.name, c.err, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if c.want == "" {
			continue
		}
		b, _ := json.Marshal(got)
		if string(b) != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.name, b, c.want)
		}
	}
	// the usage error lists the fields
	_, err := core.Shape(acct, []string{"nope"}, 0)
	if e := core.AsError(err); e == nil || !strings.Contains(e.Hint, "positions") {
		t.Fatalf("hint: %+v", e)
	}
}

func TestShapeResultMeta(t *testing.T) {
	r := &core.Result{Data: []map[string]any{{"a": 1}, {"a": 2}, {"a": 3}}, Meta: domain.Meta{Warnings: []string{"w0"}}}
	out, err := core.ShapeResult(r, []string{"a", "zzz"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if out.Meta.Total == nil || *out.Meta.Total != 3 || !out.Meta.Truncated {
		t.Fatalf("total/truncated: %+v", out.Meta)
	}
	if len(out.Meta.Warnings) != 2 || !strings.Contains(out.Meta.Warnings[1], `"zzz"`) {
		t.Fatalf("warnings: %v", out.Meta.Warnings)
	}
	if len(r.Meta.Warnings) != 1 {
		t.Fatal("ShapeResult must not mutate the input meta")
	}
	// a tool-set total wins
	n := 40
	r.Meta.Total = &n
	out, _ = core.ShapeResult(r, nil, 1)
	if *out.Meta.Total != 40 {
		t.Fatalf("tool total overwritten: %d", *out.Meta.Total)
	}
	// no limit hit: no total, no truncated
	out, _ = core.ShapeResult(&core.Result{Data: []int{1}}, nil, 5)
	if out.Meta.Total != nil || out.Meta.Truncated {
		t.Fatalf("untruncated: %+v", out.Meta)
	}
}

// ---- C-T2: price ----

func wpcInvoke(t *testing.T, env *core.Env, tool string, args any) (*core.Result, error) {
	t.Helper()
	raw, _ := json.Marshal(args)
	return core.Default().Invoke(context.Background(), env, core.Call{Tool: tool, Args: raw})
}

func TestPriceRows(t *testing.T) {
	env := coretest.Env(t, nil)
	res, err := wpcInvoke(t, env, "get_price", map[string]any{"coins": []string{"btc", "eth"}})
	if err != nil {
		t.Fatal(err)
	}
	rows := res.Data.([]core.PriceRow)
	if len(rows) != 2 || rows[0].Coin != "BTC" || rows[1].Coin != "ETH" {
		t.Fatalf("rows %+v", rows)
	}
	for _, r := range rows {
		if r.Mid <= 0 || r.Mark <= 0 {
			t.Fatalf("mid/mark: %+v", r)
		}
	}
	// BTC has a fixture book: top of book and the context's mid
	if rows[0].Bid != 73499 || rows[0].Ask != 73501 || rows[0].Funding == 0 {
		t.Fatalf("btc %+v", rows[0])
	}
	if want := 73500.0/72000 - 1; math.Abs(rows[0].Change24h-want) > 1e-12 {
		t.Fatalf("change24h %v want %v", rows[0].Change24h, want)
	}
	// ETH has no fixture book: still a row, with a warning
	if !res.Meta.Partial || len(res.Meta.Warnings) == 0 {
		t.Fatalf("missing book should warn: %+v", res.Meta)
	}
	if res.Card != core.CardPrice {
		t.Fatalf("card %q", res.Card)
	}
	// errors
	if _, err := wpcInvoke(t, env, "get_price", map[string]any{"coins": []string{}}); core.AsError(err).Code != core.CodeUsage {
		t.Fatalf("no coins: %v", err)
	}
	if _, err := wpcInvoke(t, env, "get_price", map[string]any{"coins": []string{"NOPECOIN"}}); core.AsError(err).Code != core.CodeUnknownCoin {
		t.Fatalf("unknown coin: %v", err)
	}
	// comma lists and duplicates
	res, err = wpcInvoke(t, env, "get_price", map[string]any{"coins": []string{"btc,BTC", "hype"}})
	if err != nil || len(res.Data.([]core.PriceRow)) != 2 {
		t.Fatalf("dedupe: %v %+v", err, res)
	}
}

// ---- markets: query, offset, total ----

func TestMarketsQueryOffsetTotal(t *testing.T) {
	env := coretest.Env(t, nil)
	res, err := wpcInvoke(t, env, "get_markets", map[string]any{"query": "BTC"})
	if err != nil {
		t.Fatal(err)
	}
	ms := res.Data.([]domain.Market)
	if len(ms) != 1 || ms[0].Coin != "BTC" {
		t.Fatalf("query BTC: %v", ms)
	}
	if ms[0].Change24h == 0 || ms[0].FundingAPR == 0 {
		t.Fatalf("derived fields not filled: %+v", ms[0])
	}
	all, _ := wpcInvoke(t, env, "get_markets", map[string]any{})
	n := len(all.Data.([]domain.Market))
	page, err := wpcInvoke(t, env, "get_markets", map[string]any{"offset": 1, "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	pm := page.Data.([]domain.Market)
	if len(pm) != 2 || pm[0].Coin != all.Data.([]domain.Market)[1].Coin {
		t.Fatalf("offset page %v", pm)
	}
	if page.Meta.Total == nil || *page.Meta.Total != n || !page.Meta.Truncated {
		t.Fatalf("meta %+v (n=%d)", page.Meta, n)
	}
	if _, err := wpcInvoke(t, env, "get_markets", map[string]any{"offset": -1}); core.AsError(err).Code != core.CodeUsage {
		t.Fatalf("negative offset: %v", err)
	}
}

func TestTradesLimit(t *testing.T) {
	env := coretest.Env(t, nil)
	res, err := wpcInvoke(t, env, "get_trades", map[string]any{"coin": "BTC", "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if ts := res.Data.([]domain.Trade); len(ts) != 1 || ts[0].TID != 2 {
		t.Fatalf("newest first, limited: %v", ts)
	}
	if res.Meta.Total == nil || *res.Meta.Total != 2 || !res.Meta.Truncated {
		t.Fatalf("meta %+v", res.Meta)
	}
}

// ---- C-T1 (core side): chart render hints ----

func TestChartRenderHints(t *testing.T) {
	env := coretest.Env(t, nil)
	res, err := wpcInvoke(t, env, "get_candles", map[string]any{"coin": "BTC", "interval": "15m", "height": 40, "style": "ascii", "zoom": 1})
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*core.ChartData)
	if d.Height != 40 || d.Style != "ascii" || d.Zoom != 1 {
		t.Fatalf("hints %d %q %d", d.Height, d.Style, d.Zoom)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), `"Height"`) || strings.Contains(string(b), `"Style"`) {
		t.Fatal("render hints must not reach JSON")
	}
	res, _ = wpcInvoke(t, env, "get_candles", map[string]any{"coin": "BTC", "interval": "15m"})
	if d := res.Data.(*core.ChartData); d.Height != 0 || d.Zoom != 0 || d.Style != "" {
		t.Fatalf("defaults must leave hints empty: %+v", d)
	}
}

// ---- C-T5: seeded streams make no REST call ----

type countingHL struct {
	hyperliquid.Reader
	mu                    sync.Mutex
	candles, books, trade int
}

func (c *countingHL) Candles(ctx context.Context, coin string, iv domain.Interval, n int, end int64) ([]domain.Candle, error) {
	c.mu.Lock()
	c.candles++
	c.mu.Unlock()
	return c.Reader.Candles(ctx, coin, iv, n, end)
}

func (c *countingHL) Book(ctx context.Context, coin string, o hyperliquid.BookOpts) (domain.Book, error) {
	c.mu.Lock()
	c.books++
	c.mu.Unlock()
	return c.Reader.Book(ctx, coin, o)
}

func (c *countingHL) Trades(ctx context.Context, coin string) ([]domain.Trade, error) {
	c.mu.Lock()
	c.trade++
	c.mu.Unlock()
	return c.Reader.Trades(ctx, coin)
}

func TestSeededStreamsSkipREST(t *testing.T) {
	env := coretest.Env(t, nil)
	hl := &countingHL{Reader: env.HL}
	env.HL = hl
	reg := core.Default()
	run := func(ctx context.Context, tool string, args any) int {
		raw, _ := json.Marshal(args)
		n := 0
		if err := reg.RunStream(ctx, env, core.Call{Tool: tool, Args: raw}, func(*core.Result) { n++ }); err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		return n
	}
	now := coretest.Now
	fresh := &core.Result{Card: core.CardChart, Data: &core.ChartData{Coin: "BTC", Interval: domain.I15m,
		Candles: []domain.Candle{{T: now.Add(-10 * time.Minute).UnixMilli(), C: 1}}}}
	if n := run(core.WithSeed(context.Background(), fresh), "get_candles", map[string]any{"coin": "BTC", "interval": "15m"}); n != 0 || hl.candles != 0 {
		t.Fatalf("fresh seed: %d emits, %d candle calls", n, hl.candles)
	}
	stale := &core.Result{Card: core.CardChart, Data: &core.ChartData{Coin: "BTC", Interval: domain.I15m,
		Candles: []domain.Candle{{T: now.Add(-2 * time.Hour).UnixMilli(), C: 1}}}}
	if n := run(core.WithSeed(context.Background(), stale), "get_candles", map[string]any{"coin": "BTC", "interval": "15m"}); n != 1 || hl.candles != 1 {
		t.Fatalf("stale seed must refetch: %d emits, %d calls", n, hl.candles)
	}
	other := &core.Result{Card: core.CardChart, Data: &core.ChartData{Coin: "ETH", Interval: domain.I15m,
		Candles: []domain.Candle{{T: now.UnixMilli(), C: 1}}}}
	run(core.WithSeed(context.Background(), other), "get_candles", map[string]any{"coin": "BTC", "interval": "15m"})
	if hl.candles != 2 {
		t.Fatalf("another coin's seed must refetch: %d", hl.candles)
	}

	book := &core.Result{Card: core.CardBook, Data: domain.Book{Coin: "BTC"}}
	run(core.WithSeed(context.Background(), book), "get_orderbook", map[string]any{"coin": "BTC"})
	if hl.books != 0 {
		t.Fatalf("seeded book fetched %d times", hl.books)
	}
	run(context.Background(), "get_orderbook", map[string]any{"coin": "BTC"})
	if hl.books != 1 {
		t.Fatalf("unseeded book: %d", hl.books)
	}

	tape := &core.Result{Card: core.CardTrades, Data: []domain.Trade{{Coin: "BTC", TID: 9}}}
	run(core.WithSeed(context.Background(), tape), "get_trades", map[string]any{"coin": "BTC"})
	if hl.trade != 0 {
		t.Fatalf("seeded trades fetched %d times", hl.trade)
	}
}
