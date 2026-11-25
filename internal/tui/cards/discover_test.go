package cards

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/hlpxyz"
	"github.com/maxmoneycash/hlp-cli/internal/registry"
)

func TestDiscoverCard(t *testing.T) {
	reg := registry.MustDefault()
	all := reg.Discover(registry.Filter{})
	memes := reg.Discover(registry.Filter{Tags: []string{"memes"}})
	for _, d := range []*registry.Discovery{&all, &memes} {
		for _, w := range []int{100, 72, 40} {
			for _, compact := range []bool{false, true} {
				out := Render(&core.Result{Card: core.CardDiscover, Title: "Discover vaults", Data: d}, Opts{Width: w, Plain: true, Compact: compact})
				if strings.Contains(out, "\x1b[") {
					t.Fatal("ANSI in plain mode")
				}
				for _, l := range strings.Split(out, "\n") {
					if ansi.StringWidth(l) > w {
						t.Fatalf("width %d: line too wide (%d): %q", w, ansi.StringWidth(l), l)
					}
				}
				if !strings.Contains(strings.SplitN(out, "\n", 2)[0], strconv.Itoa(d.Total)) {
					t.Fatalf("no focal count:\n%s", out)
				}
			}
		}
	}
	out := Render(&core.Result{Card: core.CardDiscover, Title: "Discover vaults", Data: &memes}, Opts{Width: 100, Plain: true})
	if memes.Total <= 12 && !strings.Contains(out, "ENTRIES") {
		t.Fatalf("narrow filter should list entries:\n%s", out)
	}
}

func TestIndexCard(t *testing.T) {
	list := []hlpxyz.Index{{Def: hlpxyz.IndexDef{Slug: "hlp20", Name: "HLP 20", Official: true}}, {Def: hlpxyz.IndexDef{Slug: "mavericks", Name: "Mavericks"}}}
	one := hlpxyz.Index{Def: hlpxyz.IndexDef{Slug: "hlp20", Name: "HLP 20", Description: "Top twenty vaults by TVL, rebalanced monthly."},
		Allocs: []hlpxyz.IndexAlloc{{Weights: map[string]float64{"0xdfc24b077bc1425ad1dea75bcb6f8158e10df303": 0.6, "0x1e37a337ed460039d1b15bd3bc489de789768d5e": 0.4}}}}
	for _, d := range []any{list, one} {
		for _, w := range []int{100, 40} {
			out := Render(&core.Result{Card: core.CardIndex, Title: "Vault indexes", Data: d}, Opts{Width: w, Plain: true})
			if strings.Contains(out, "{") {
				t.Fatalf("fell back to a dump:\n%s", out)
			}
			for _, l := range strings.Split(out, "\n") {
				if ansi.StringWidth(l) > w {
					t.Fatalf("line too wide at %d: %q", w, l)
				}
			}
		}
	}
}

// get_strategy returns *core.StrategyData; the card must render it (it once
// fell back to a raw JSON dump), and the no-id list too.
func TestStrategyCardData(t *testing.T) {
	list := &core.StrategyData{Strategies: []core.StrategySummary{
		{ID: "trend", Name: "Six-Market Trend", CAGR: 0.41, MaxDD: -0.70, Sharpe: 0.91, OK: true},
		{ID: "lotto"},
	}}
	for _, d := range []any{fixtureStrategy(t), *fixtureStrategy(t), list} {
		for _, w := range []int{100, 60} {
			for _, compact := range []bool{false, true} {
				out := Render(&core.Result{Card: core.CardStrategy, Title: "Strategy", Data: d}, Opts{Width: w, Plain: true, Compact: compact})
				if strings.Contains(out, "{") {
					t.Fatalf("fell back to a dump:\n%s", out)
				}
				for _, l := range strings.Split(out, "\n") {
					if ansi.StringWidth(l) > w {
						t.Fatalf("line too wide at %d: %q", w, l)
					}
				}
			}
		}
	}
	out := Render(&core.Result{Card: core.CardStrategy, Title: "Long SPY", Data: fixtureStrategy(t)}, Opts{Width: 100, Plain: true})
	for _, want := range []string{"BOOK", "CAGR +53.1%", "MAX DD -30.2%", "SHARPE 1.36"} {
		if !strings.Contains(out, want) {
			t.Errorf("strategy card lacks %q:\n%s", want, out)
		}
	}
	out = Render(&core.Result{Card: core.CardStrategy, Title: "Quant strategies", Data: list}, Opts{Width: 100, Plain: true, Compact: true})
	for _, want := range []string{"Six-Market Trend", "+41.0%", "lotto"} {
		if !strings.Contains(out, want) {
			t.Errorf("list card lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "/strat <id>") {
		t.Errorf("list card keeps a hint line:\n%s", out)
	}
}

func TestErrorHintSlashForms(t *testing.T) {
	// Inline, the hint line is the runnable command alone.
	for hint, want := range map[string]string{
		"`hlp vaults discover` lists every tag":   "\n    /discover",
		"pass one, e.g. `hlp fund user 0x…`":      "\n    /fund-user 0x…",
		"try `hlp fund epochs`":                   "\n    /fund-epochs",
		"list markets with `hlp markets --query`": "\n    /markets --query",
	} {
		out := Error(&core.Error{Code: core.CodeUsage, Message: "x", Hint: hint}, Opts{Width: 100, Plain: true, Compact: true})
		if !strings.HasSuffix(out, want) {
			t.Errorf("hint %q -> %q, want %q", hint, out, want)
		}
	}
	out := Error(&core.Error{Code: core.CodeNotFound, Message: "hlp.xyz: index not found"}, Opts{Width: 100, Plain: true})
	if !strings.Contains(out, "■ hlp.xyz: index not found") {
		t.Errorf("brand capitalized: %q", out)
	}
}

func TestDiscoverHints(t *testing.T) {
	reg := registry.MustDefault()
	memes := reg.Discover(registry.Filter{Tags: []string{"memes"}})
	out := Render(&core.Result{Card: core.CardDiscover, Title: "Discover vaults", Data: &memes}, Opts{Width: 140, Plain: true, Compact: true})
	// No hint walls inline (DESIGN.md section 5.1 rule 6).
	for _, bad := range []string{"--tag", "for strategies", "hidden"} {
		if strings.Contains(out, bad) {
			t.Errorf("memes discover keeps %q:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "1 VAULT ") {
		t.Errorf("memes discover lacks its kind count:\n%s", out)
	}
	all := reg.Discover(registry.Filter{})
	out = Render(&core.Result{Card: core.CardDiscover, Title: "Discover vaults", Data: &all}, Opts{Width: 140, Plain: true})
	if all.Hidden.Testnet > 0 && !strings.Contains(out, "HIDDEN   TESTNET") {
		t.Errorf("hidden testnet entries not in the full card's NOTES:\n%s", out)
	}
}

// The Sharpe footnote spans the card below the metrics and checklist
// columns; it must never share a line with a checklist row.
func TestVaultFootnoteBelowColumns(t *testing.T) {
	stubCharts(t)
	r := fixtureResults(t)["vault"]
	for _, w := range []int{100, 140} {
		out := Render(r, Opts{Width: w, Plain: true})
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "Sharpe above") && (strings.Contains(l, "✓") || strings.Contains(l, "✗")) {
				t.Fatalf("width %d: footnote collides with the checklist: %q", w, l)
			}
		}
	}
}

func TestFitCandles(t *testing.T) {
	cs := make([]domain.Candle, 1000)
	for i := range cs {
		cs[i] = domain.Candle{T: int64(i) * 60_000, O: float64(i), H: float64(i) + 1, L: float64(i) - 1, C: float64(i) + 0.5, V: 1}
	}
	out, per := fitCandles(cs, 43)
	if len(out) > 43 || per != 24 || out[0].T != 0 || out[0].H != 24 || out[0].L != -1 || out[0].C != 23.5 || out[0].V != 24 {
		t.Fatalf("len %d per %d first %+v", len(out), per, out[0])
	}
	if last := out[len(out)-1]; last.C != cs[999].C {
		t.Fatalf("last close %v", last.C)
	}
}
