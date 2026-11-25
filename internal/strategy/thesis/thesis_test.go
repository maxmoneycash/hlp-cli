package thesis_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/backtest"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/internal/testkit"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/thesis"
)

func read(t *testing.T, rel ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{testkit.RepoRoot()}, rel...)...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func gross(w map[string]float64) float64 {
	g := 0.0
	for _, v := range w {
		g += math.Abs(v)
	}
	return g
}

// The owner's thesis must equal hlp.xyz's live spxmeme book.
func TestSpxmemeMatchesHlpxyzLiveBook(t *testing.T) {
	s, err := thesis.Parse(read(t, "examples", "thesis", "long-spy-short-memes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	w := s.Weights()
	want := map[string]float64{"xyz:SP500": 1.5, "DOGE": -0.075, "FARTCOIN": -0.075, "TRUMP": -0.075,
		"PUMP": -0.075, "CASHCAT": -0.075, "PONS": -0.075}
	if len(w) != len(want) {
		t.Fatalf("weights %v", w)
	}
	for c, v := range want {
		if !near(w[c], v) {
			t.Errorf("%s = %v, want %v", c, w[c], v)
		}
	}
	var live struct {
		Gross     float64 `json:"gross"`
		Positions []struct {
			Coin   string  `json:"coin"`
			Weight float64 `json:"weight"`
		} `json:"positions"`
	}
	if err := json.Unmarshal(read(t, "testdata", "fixtures", "hlpxyz", "quant_live_spxmeme.json"), &live); err != nil {
		t.Fatal(err)
	}
	if !near(gross(w), live.Gross) {
		t.Errorf("gross %v, hlp.xyz says %v", gross(w), live.Gross)
	}
	for _, p := range live.Positions {
		if !near(w[p.Coin], p.Weight) {
			t.Errorf("%s: ours %v, hlp.xyz %v", p.Coin, w[p.Coin], p.Weight)
		}
	}
	if s.Rebalance != "1w" || s.Interval != domain.I1d || s.Name != "Long SPY / Short memes" {
		t.Errorf("spec %+v", s)
	}
}

func TestParseTextRoundTrip(t *testing.T) {
	s, err := thesis.ParseText("long xyz:SP500 1.5x, short DOGE FARTCOIN TRUMP 0.45x, rebalance 1w, 2x")
	if err != nil {
		t.Fatal(err)
	}
	w := s.Weights()
	if !near(gross(w), 2) || !near(w["xyz:SP500"]/w["DOGE"], -1.5/0.15) || s.Rebalance != "1w" {
		t.Fatalf("weights %v spec %+v", w, s)
	}
	cases := []string{
		"long xyz:SP500 1.5x, short DOGE FARTCOIN TRUMP, rebalance 1w",
		"long majors and short memes, 2x",
		"long BTC 60%; short falling memes each 0.05x lookback 14; rebalance 1d; interval 4h",
		"name Pair trade, long HYPE, short basket:alts, leverage 1",
		"long BTC",
	}
	for _, text := range cases {
		a, err := thesis.ParseText(text)
		if err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		b, err := thesis.ParseText(a.String())
		if err != nil {
			t.Fatalf("%q -> %q: %v", text, a.String(), err)
		}
		wa, wb := a.Weights(), b.Weights()
		if len(wa) != len(wb) || a.Rebalance != b.Rebalance || a.Interval != b.Interval || !near(a.Leverage, b.Leverage) {
			t.Fatalf("round trip %q -> %q: %+v vs %+v", text, a.String(), a, b)
		}
		for c := range wa {
			if !near(wa[c], wb[c]) {
				t.Fatalf("round trip %q: %s %v vs %v", text, c, wa[c], wb[c])
			}
		}
	}
	// The YAML thesis survives the one-line form too.
	y, _ := thesis.Parse(read(t, "examples", "thesis", "long-spy-short-memes.yaml"))
	back, err := thesis.ParseText(y.String())
	if err != nil {
		t.Fatalf("%q: %v", y.String(), err)
	}
	for c, v := range y.Weights() {
		if !near(back.Weights()[c], v) {
			t.Fatalf("yaml round trip %s: %v vs %v (%q)", c, back.Weights()[c], v, y.String())
		}
	}
	if lc := mustText(t, "long btc, short xyz:sp500 kPEPE"); lc.Long[0].Coin != "BTC" || lc.Baskets["short1"][0] != "xyz:SP500" || lc.Baskets["short1"][1] != "kPEPE" {
		t.Fatalf("coin normalization: %+v", lc)
	}
	for _, bad := range []string{"", "buy", "the dip", "long", "long BTC, leverage lots", "long BTC, rebalance sometimes", "long BTC, interval 7m", "long BTC, 99x"} {
		if _, err := thesis.ParseText(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestParseValidationAndExamples(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(testkit.RepoRoot(), "examples", "thesis", "*.yaml"))
	if len(files) < 5 {
		t.Fatalf("expected the thesis examples, found %d", len(files))
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		s, err := thesis.Parse(b)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		if len(s.Weights()) == 0 || s.Strategy() == nil || len(s.Coins()) == 0 {
			t.Errorf("%s: empty thesis", filepath.Base(f))
		}
	}
	j, err := thesis.Parse([]byte(`{"name":"j","long":[{"coin":"BTC"}],"short":[{"coin":"ETH","weight":0.5}],"leverage":3}`))
	if err != nil || !near(j.Weights()["BTC"], 2) || !near(j.Weights()["ETH"], -1) {
		t.Fatalf("json thesis %v %v", j.Weights(), err)
	}
	bad := []string{
		"name: x\n",
		"long: [{coin: BTC, basket: memes}]",
		"long: [{basket: nope}]",
		"long: [{coin: BTC, weight: -1}]",
		"long: [{coin: BTC}]\nleverage: 80",
		"long: [{coin: BTC}]\nrebalance: fortnight",
		"long: [{coin: BTC}]\nunknown_field: 1",
		"long: [{basket: memes, filter: sideways}]",
	}
	for _, b := range bad {
		if _, err := thesis.Parse([]byte(b)); err == nil {
			t.Errorf("%q: expected an error", b)
		}
	}
	if got := thesis.BasketNames(); len(got) != len(thesis.Builtin) || got[0] != "alts" {
		t.Errorf("basket names %v", got)
	}
}

func resolver(m map[string]domain.Market) func(string) (domain.Market, error) {
	return func(s string) (domain.Market, error) {
		if v, ok := m[s]; ok {
			return v, nil
		}
		return domain.Market{}, errors.New("unknown coin")
	}
}

func TestValidateAndResolved(t *testing.T) {
	s, _ := thesis.ParseText("long SPY 1.5x, short memes 0.5x")
	reg := map[string]domain.Market{
		"SPY":  {Coin: "xyz:SP500", Kind: domain.HIP3, Dex: "xyz"},
		"DOGE": {Coin: "DOGE", Kind: domain.Perp}, "TRUMP": {Coin: "TRUMP", Kind: domain.Perp},
		"WIF": {Coin: "WIF", Kind: domain.Perp, Delisted: true}, "PUMP": {Coin: "PUMP", Kind: domain.Spot},
	}
	r, warns, err := s.Resolved(resolver(reg))
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(warns, "\n")
	for _, want := range []string{"FARTCOIN not listed", "WIF is delisted", "PUMP is spot", "HIP-3 legs (xyz:SP500)"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing warning %q in:\n%s", want, all)
		}
	}
	w := r.Weights()
	if !near(w["xyz:SP500"], 1.5) || !near(w["DOGE"], -0.25) || !near(w["TRUMP"], -0.25) || len(w) != 3 {
		t.Fatalf("resolved weights %v", w)
	}
	if v, err := s.Validate(resolver(reg)); err != nil || len(v) != len(warns) {
		t.Fatalf("Validate %v %v", v, err)
	}
	// A single-coin leg that does not resolve is fatal; so is shorting spot.
	if _, err := mustText(t, "long NOPE").Validate(resolver(reg)); err == nil {
		t.Error("unresolvable coin leg accepted")
	}
	if _, err := mustText(t, "short PUMP").Validate(resolver(reg)); err == nil {
		t.Error("spot short accepted")
	}
	if _, err := mustText(t, "long BTC, 20x").Validate(resolver(map[string]domain.Market{"BTC": {Coin: "BTC"}})); err != nil {
		t.Error(err)
	}
	if _, err := s.Validate(nil); err == nil {
		t.Error("nil resolver accepted")
	}
}

func mustText(t *testing.T, s string) thesis.Spec {
	t.Helper()
	sp, err := thesis.ParseText(s)
	if err != nil {
		t.Fatal(err)
	}
	return sp
}

// TestStrategyRebalancesWeeklyWithFilter backtests the dynamic spxmeme
// variant on synthetic daily bars: SPX rises, two memes fall, one rises.
func TestStrategyRebalancesWeeklyWithFilter(t *testing.T) {
	t0 := int64(1_700_000_000_000) / 86_400_000 * 86_400_000
	days := 120
	src := &testkit.Source{Candle: map[string][]domain.Candle{
		"xyz:SP500": testkit.Synthetic(t0, domain.I1d, days, func(i int) float64 { return 5000 * (1 + 0.001*float64(i)) }),
		"DOGE":      testkit.Synthetic(t0, domain.I1d, days, func(i int) float64 { return 0.3 * math.Pow(0.99, float64(i)) }),
		"TRUMP":     testkit.Synthetic(t0, domain.I1d, days, func(i int) float64 { return 10 * math.Pow(0.995, float64(i)) }),
		"PUMP":      testkit.Synthetic(t0, domain.I1d, days, func(i int) float64 { return 0.005 * math.Pow(1.01, float64(i)) }),
	}}
	s := mustText(t, "long xyz:SP500 1.5x, short falling DOGE TRUMP PUMP each 0.075x lookback 30, rebalance 1w")
	st := s.Strategy()
	if sp := st.Spec(); sp.Kind != "thesis" || sp.Warmup != 31 || sp.Rebalance != 7*24*time.Hour || len(sp.Universe) != 4 {
		t.Fatalf("spec %+v", sp)
	}
	res, err := backtest.Run(context.Background(), st, src, backtest.Options{NoCosts: true,
		Now: func() time.Time { return time.UnixMilli(t0 + int64(days+2)*86_400_000) }})
	if err != nil {
		t.Fatal(err)
	}
	reb := 0
	for _, l := range res.Logs {
		if strings.Contains(l, "rebalance:") {
			reb++
			if strings.Contains(l, "PUMP") {
				t.Fatalf("rising PUMP was shorted: %s", l)
			}
			if !strings.Contains(l, "DOGE -0.075") || !strings.Contains(l, "xyz:SP500 +1.500") {
				t.Fatalf("unexpected book: %s", l)
			}
			// Unix weeks start on Thursday.
			ts, _ := time.Parse("2006-01-02 15:04", l[:16])
			if reb > 1 && ts.Weekday() != time.Thursday {
				t.Fatalf("weekly rebalance on %s (%s)", ts.Weekday(), l)
			}
		}
	}
	// (days - warmup) / 7 periods, give or take the partial first week.
	if want := (days - 31) / 7; reb < want || reb > want+2 {
		t.Fatalf("%d rebalances, want about %d", reb, want)
	}
	if res.Equity.Last() <= 10000 {
		t.Fatalf("long-up/short-down thesis lost money on a friendly path: %.2f", res.Equity.Last())
	}
	if f := thesis.FormatWeights(map[string]float64{}); f != "flat" {
		t.Fatal(f)
	}
}

func TestRebalanceNeverAndMonthly(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	src := &testkit.Source{Candle: map[string][]domain.Candle{
		"BTC": testkit.Synthetic(t0, domain.I1d, 100, func(i int) float64 { return 100 + float64(i%9) }),
	}}
	now := func() time.Time { return time.UnixMilli(t0 + 200*86_400_000) }
	for _, tc := range []struct {
		text string
		want int
	}{{"long BTC, rebalance never", 1}, {"long BTC, rebalance 1M", 4}, {"long BTC, rebalance 1d", 100}} {
		res, err := backtest.Run(context.Background(), mustText(t, tc.text).Strategy(), src, backtest.Options{NoCosts: true, Now: now})
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, l := range res.Logs {
			if strings.Contains(l, "rebalance:") {
				n++
			}
		}
		if n != tc.want {
			t.Errorf("%s: %d rebalances, want %d", tc.text, n, tc.want)
		}
	}
}

func TestAutoNameInlineBasket(t *testing.T) {
	s, err := thesis.ParseText("long BTC, short DOGE FARTCOIN, rebalance 1w")
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "Long BTC / Short DOGE+FARTCOIN" {
		t.Fatalf("name %q", s.Name)
	}
}
