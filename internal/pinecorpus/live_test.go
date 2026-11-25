package pinecorpus_test

import (
	"context"
	"os"
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/pinecorpus"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/pine"
)

// These tests read a real corpus checkout and skip without one:
//
//	HLP_LIVE=1 HLP_CORPUS=1 go test ./internal/pinecorpus -run Live -v   # fetches the pinned corpus into the cache
//	HLP_LIVE=1 HLP_PINECORPUS_DIR="$DIR" go test ./internal/pinecorpus -run Live -v   # or use an existing checkout
//
// See docs/PINEFORGE.md for the pinned commit and why the corpus is fetched
// at test time instead of vendored.

func corpusOrSkip(t *testing.T) []pinecorpus.Probe {
	t.Helper()
	if os.Getenv("HLP_LIVE") != "1" {
		t.Skip("set HLP_LIVE=1 (plus a corpus, see below) to run corpus tests")
	}
	dir := pinecorpus.Dir()
	if dir == "" {
		if os.Getenv(pinecorpus.EnvEnable) != "1" {
			t.Skipf("set %s to a pineforge-corpus checkout, or %s=1 to download the pinned one", pinecorpus.EnvDir, pinecorpus.EnvEnable)
		}
		var err error
		if dir, err = pinecorpus.Root(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	probes, err := pinecorpus.Probes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(probes) == 0 {
		t.Fatalf("no probes under %s/validation", dir)
	}
	return probes
}

// TestLiveTapesParse checks every TradingView tape in the corpus parses.
func TestLiveTapesParse(t *testing.T) {
	total := 0
	for _, p := range corpusOrSkip(t) {
		trades, err := p.TVTrades()
		if err != nil {
			t.Errorf("%s: %v", p.Name, err)
			continue
		}
		total += len(trades)
	}
	t.Logf("parsed %d TradingView trades", total)
}

// TestLivePineCoverage reports how many corpus strategies hlp's builtin Pine
// engine accepts. It is a coverage report, not a pass/fail gate: the corpus
// deliberately exercises Pine v6 features the builtin subset leaves out.
func TestLivePineCoverage(t *testing.T) {
	probes := corpusOrSkip(t)
	ok := 0
	reasons := map[string]int{}
	for _, p := range probes {
		src, err := p.Pine()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pine.Parse(src); err != nil {
			errs := pine.Errors(err)
			if len(errs) > 0 {
				reasons[errs[0].Msg]++
			} else {
				reasons[err.Error()]++
			}
			continue
		}
		ok++
	}
	t.Logf("builtin engine parses %d of %d corpus strategies", ok, len(probes))
	for msg, n := range reasons {
		if n >= 3 {
			t.Logf("  %3d  %s", n, msg)
		}
	}
}

// TestLiveEngineTapesAgree checks hlp's comparer against the corpus's own
// PineForge engine tapes (engine_trades.csv, Apache-2.0): they are published
// as matching TradingView, so a large disagreement here means the comparer
// or the tape parser is wrong, not the engine.
func TestLiveEngineTapesAgree(t *testing.T) {
	exact, total := 0, 0
	for _, p := range corpusOrSkip(t) {
		if p.Anomaly() {
			continue
		}
		want, err := p.TVTrades()
		if err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		got, err := p.EngineTrades()
		if err != nil {
			continue // probe without an engine tape
		}
		total++
		d := pinecorpus.Compare(want, got, 0)
		// The tapes cover slightly different windows (TradingView's range
		// starts and ends mid-trade), so a few unmatched trades at the
		// edges are expected; PineForge's own grader trims them.
		if d.Want-d.Matched <= 5 && len(d.Extra) <= 5 && d.MaxEntryRel <= 1e-6 {
			exact++
		} else if testing.Verbose() {
			t.Logf("%s: %s", p.Name, d)
		}
	}
	t.Logf("engine tapes agreeing with TradingView: %d of %d", exact, total)
	if total > 0 && exact*10 < total*9 {
		t.Errorf("only %d of %d engine tapes agree; check the comparer", exact, total)
	}
}
