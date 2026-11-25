package core

import (
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// C-T2: "BTC" must not pull in TRB or BRETT through a subsequence match on
// coin+display+category ("crypto" supplied the C).
func TestFuzzyMarketsShortQuery(t *testing.T) {
	ms := []domain.Market{
		{Coin: "TRB", Display: "TRB", Category: "crypto"},
		{Coin: "BRETT", Display: "BRETT", Category: "crypto"},
		{Coin: "WBTC", Display: "WBTC", Category: "crypto"},
		{Coin: "BTC", Display: "BTC", Category: "crypto"},
		{Coin: "xyz:NVDA", Display: "NVDA (xyz)", Dex: "xyz", Category: "stocks"},
	}
	coins := func(xs []domain.Market) []string {
		out := make([]string, len(xs))
		for i, m := range xs {
			out[i] = m.Coin
		}
		return out
	}
	got := coins(fuzzyMarkets(ms, "BTC"))
	if len(got) != 2 || got[0] != "BTC" || got[1] != "WBTC" {
		t.Fatalf("BTC: %v", got)
	}
	if got := coins(fuzzyMarkets(ms, "nvda")); len(got) != 1 || got[0] != "xyz:NVDA" {
		t.Fatalf("nvda: %v", got)
	}
	if got := coins(fuzzyMarkets(ms, "stocks")); len(got) != 0 {
		t.Fatalf("category must not match: %v", got)
	}
	// short query with no substring hit falls back to the fuzzy match
	if got := coins(fuzzyMarkets(ms, "brt")); len(got) != 1 || got[0] != "BRETT" {
		t.Fatalf("fuzzy fallback: %v", got)
	}
}

// PERF #10: a full window slides without copying the history on every bar,
// and bars already handed out are never shifted.
func TestMergeCandleAmortized(t *testing.T) {
	const maxBars = 100
	cs := make([]domain.Candle, maxBars)
	for i := range cs {
		cs[i] = domain.Candle{T: int64(i)}
	}
	handed := cs // a frame emitted earlier still points here
	reallocs := 0
	for i := maxBars; i < maxBars*5; i++ {
		before := &cs[0]
		cs = MergeCandle(cs, domain.Candle{T: int64(i)}, maxBars)
		if len(cs) != maxBars || cs[0].T != int64(i-maxBars+1) || cs[maxBars-1].T != int64(i) {
			t.Fatalf("bar %d: len %d first %d last %d", i, len(cs), cs[0].T, cs[len(cs)-1].T)
		}
		if &cs[0] != before && cap(cs) == 2*maxBars && cs[0].T == int64(i-maxBars+1) {
			reallocs++
		}
	}
	if reallocs > 5 {
		t.Fatalf("%d reallocations for %d bars", reallocs, maxBars*4)
	}
	for i := range handed {
		if handed[i].T != int64(i) {
			t.Fatalf("an emitted frame was shifted at %d: %d", i, handed[i].T)
		}
	}
	// replacing the open bar stays in place
	last := &cs[len(cs)-1]
	cs = MergeCandle(cs, domain.Candle{T: cs[len(cs)-1].T, C: 7}, maxBars)
	if &cs[len(cs)-1] != last || cs[len(cs)-1].C != 7 {
		t.Fatal("open bar not replaced in place")
	}
	// older bars are ignored
	if got := MergeCandle(cs, domain.Candle{T: 0}, maxBars); len(got) != maxBars || got[0].T == 0 {
		t.Fatal("older bar merged")
	}
}
