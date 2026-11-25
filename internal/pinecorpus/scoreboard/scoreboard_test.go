// Package scoreboard grades hlp's Pine engines against TradingView on the
// PineForge corpus. It is test-only so internal/pinecorpus never depends on
// the engines. Run it with
//
//	HLP_LIVE=1 HLP_CORPUS=1 go test ./internal/pinecorpus/scoreboard -run LiveScoreboard -v -timeout 60m
//
// The first run downloads the pinned corpus (about 570 MB on disk) into
// $HLP_PINECORPUS_CACHE or the user cache dir; HLP_PINECORPUS_DIR points it at
// an existing checkout instead. HLP_CORPUS_N (default 30) sets the subset
// size, HLP_CORPUS_PROBES a comma list of probe names and HLP_CORPUS_ENGINES
// a comma list of engine name prefixes to run. PYNE runs when it
// is installed; PineForge's live image runs only with HLP_PINEFORGE=1. Set
// HLP_SCOREBOARD_OUT to write the Markdown table to a file.
package scoreboard

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/pinecorpus"
	"github.com/maxmoneycash/hlp/internal/pineforge"
	"github.com/maxmoneycash/hlp/internal/pyne"
	"github.com/maxmoneycash/hlp/internal/strategy"
	"github.com/maxmoneycash/hlp/internal/strategy/backtest"
	"github.com/maxmoneycash/hlp/internal/strategy/pine"
)

// ---- builtin ----

type builtin struct{}

func (builtin) Name() string { return "builtin" }

// feedSource serves the whole corpus feed and ignores backtest's
// Hyperliquid-sized request (n), so the builtin engine warms up over the
// same six years of history TradingView did.
type feedSource struct{ cs []domain.Candle }

func (s feedSource) Candles(_ context.Context, _ string, _ domain.Interval, _ int, end int64) ([]domain.Candle, error) {
	if end == 0 {
		return s.cs, nil
	}
	i := sort.Search(len(s.cs), func(i int) bool { return s.cs[i].T >= end })
	return s.cs[:i], nil
}

func (feedSource) Funding(context.Context, string, int64, int64) ([]domain.FundingPoint, error) {
	return nil, nil
}

var (
	commTypeRE = regexp.MustCompile(`commission_type\s*=\s*strategy\.commission\.(\w+)`)
	commValRE  = regexp.MustCompile(`commission_value\s*=\s*([0-9.]+)`)
	slipRE     = regexp.MustCompile(`slippage\s*=\s*([0-9]+)`)
)

// tvCosts maps the script's strategy() commission to hlp's cost model, the
// "TradingView broker" settings: taker fee only, no slippage, no funding.
func tvCosts(src string) (c strategy.Costs, noCosts bool, notes []string) {
	val := 0.0
	if m := commValRE.FindStringSubmatch(src); m != nil {
		val, _ = strconv.ParseFloat(m[1], 64)
	}
	typ := "percent"
	if m := commTypeRE.FindStringSubmatch(src); m != nil {
		typ = m[1]
	}
	if m := slipRE.FindStringSubmatch(src); m != nil && m[1] != "0" {
		notes = append(notes, "slippage="+m[1]+" ticks not modelled")
	}
	if val == 0 {
		return c, true, notes
	}
	if typ != "percent" {
		return c, true, append(notes, "commission "+typ+" not modelled")
	}
	return strategy.Costs{TakerBps: val * 100, MakerBps: val * 100}, false, notes
}

func (builtin) Run(ctx context.Context, job pinecorpus.Job) (*pinecorpus.EngineRun, error) {
	s, err := pine.Parse(job.Pine)
	if err != nil {
		if errs := pine.Errors(err); len(errs) > 0 {
			return nil, fmt.Errorf("parse: %s", errs[0].Msg)
		}
		return nil, fmt.Errorf("parse: %v", err)
	}
	st, err := pine.Compile(s, "ETH", job.Interval, nil)
	if err != nil {
		return nil, fmt.Errorf("compile: %v", err)
	}
	costs, noCosts, notes := tvCosts(job.Pine)
	last := job.Candles[len(job.Candles)-1].T + job.Interval.Duration().Milliseconds()
	res, err := backtest.Run(ctx, st, feedSource{job.Candles}, backtest.Options{
		Capital: s.InitialCapital, Costs: costs, NoCosts: noCosts, MaxLogs: 1,
		Now: func() time.Time { return time.UnixMilli(last + 1) },
	})
	if err != nil {
		return nil, fmt.Errorf("run: %v", err)
	}
	for i, w := range res.Warnings {
		if i == 2 {
			break
		}
		notes = append(notes, "warning: "+w)
	}
	return &pinecorpus.EngineRun{Trades: pinecorpus.FillsToTrades(res.Fills), Notes: notes}, nil
}

// ---- PYNE ----

type pyneEngine struct{ e *pyne.Engine }

func (p pyneEngine) Name() string { return "pyne " + p.e.Version }

// pyneWarmup bars at the start of PYNE's window are not graded.
const pyneWarmup = 1000

func (p pyneEngine) Run(ctx context.Context, job pinecorpus.Job) (*pinecorpus.EngineRun, error) {
	want, err := job.Probe.TVTrades()
	if err != nil || len(want) == 0 {
		return nil, fmt.Errorf("no TradingView trades: %v", err)
	}
	lastEntry := want[0].EntryTime
	for _, t := range want {
		if t.EntryTime.After(lastEntry) {
			lastEntry = t.EntryTime
		}
	}
	cs := job.Candles
	end := sort.Search(len(cs), func(i int) bool { return cs[i].T > lastEntry.UnixMilli() }) + 50
	end = min(end, len(cs))
	start := max(end-pyne.MaxBars, 0)
	win := cs[start:end]
	r, err := p.e.Execute(ctx, job.Pine, win, pyne.Options{Ticker: "ETHUSDT", Interval: job.Interval})
	if err != nil {
		return nil, err
	}
	if !r.Instrumented {
		return nil, fmt.Errorf("no trade export: %s", strings.Join(r.Warnings, "; "))
	}
	run := &pinecorpus.EngineRun{
		Window: pinecorpus.Window{From: time.UnixMilli(win[min(pyneWarmup, len(win)-1)].T).UTC(), To: lastEntry},
		Notes:  []string{fmt.Sprintf("graded on the last %d bars only (PYNE caps runs at %d)", len(win)-pyneWarmup, pyne.MaxBars)},
	}
	for _, t := range r.Trades {
		run.Trades = append(run.Trades, pinecorpus.Trade{Long: t.Long, EntryTime: time.UnixMilli(t.EntryTime).UTC(), ExitTime: time.UnixMilli(t.ExitTime).UTC(),
			EntryPrice: t.EntryPx, ExitPrice: t.ExitPx, Qty: t.Size, PnL: t.Profit})
	}
	if r.Open != nil && r.Open.Size != 0 {
		run.Trades = append(run.Trades, pinecorpus.Trade{Long: r.Open.Size > 0, Open: true, EntryPrice: r.Open.AvgPx, Qty: abs(r.Open.Size),
			EntryTime: time.UnixMilli(r.Open.MarkTime).UTC()})
	}
	return run, nil
}

// ---- PineForge ----

// pfTape grades the corpus's committed engine_trades.csv: PineForge
// 1.12.0's own output for the probe (Apache-2.0), built from generated.cpp
// without running the transpiler. A fresh engine build reproduced two of
// these byte for byte (docs/PINEFORGE.md §2).
type pfTape struct{}

func (pfTape) Name() string { return "pineforge (corpus tape)" }

func (pfTape) Run(_ context.Context, job pinecorpus.Job) (*pinecorpus.EngineRun, error) {
	ts, err := job.Probe.EngineTrades()
	if err != nil {
		return nil, err
	}
	return &pinecorpus.EngineRun{Trades: ts}, nil
}

type pfLive struct{ e *pineforge.Engine }

func (p pfLive) Name() string { return "pineforge " + p.e.Version }

func (p pfLive) Run(ctx context.Context, job pinecorpus.Job) (*pinecorpus.EngineRun, error) {
	r, err := p.e.Execute(ctx, job.Pine, job.Candles, pineforge.Options{Interval: job.Interval})
	if err != nil {
		return nil, err
	}
	run := &pinecorpus.EngineRun{}
	for _, t := range r.Trades {
		run.Trades = append(run.Trades, pinecorpus.Trade{Long: t.Long(), Open: t.OpenAtEnd,
			EntryTime: time.UnixMilli(t.EntryTimeMS).UTC(), ExitTime: time.UnixMilli(t.ExitTimeMS).UTC(),
			EntryPrice: t.EntryPrice, ExitPrice: t.ExitPrice, Qty: t.Qty, PnL: t.PnL})
	}
	return run, nil
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// ---- subset ----

// subset picks n plain probes round-robin across categories (the name up to
// the first dash), so every category is represented before any repeats.
func subset(all []pinecorpus.Probe, n int) []pinecorpus.Probe {
	byCat := map[string][]pinecorpus.Probe{}
	var cats []string
	for _, p := range all {
		if !p.Plain() {
			continue
		}
		c := strings.SplitN(p.Name, "-", 2)[0]
		if _, ok := byCat[c]; !ok {
			cats = append(cats, c)
		}
		byCat[c] = append(byCat[c], p)
	}
	sort.Strings(cats)
	var out []pinecorpus.Probe
	for round := 0; len(out) < n; round++ {
		added := false
		for _, c := range cats {
			if round < len(byCat[c]) && len(out) < n {
				out = append(out, byCat[c][round])
				added = true
			}
		}
		if !added {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func TestLiveScoreboard(t *testing.T) {
	if os.Getenv("HLP_LIVE") != "1" || os.Getenv(pinecorpus.EnvEnable) != "1" {
		t.Skipf("set HLP_LIVE=1 and %s=1 (downloads the PineForge corpus, ~570 MB; see docs/PINEFORGE.md)", pinecorpus.EnvEnable)
	}
	ctx := context.Background()
	root, err := pinecorpus.Root(ctx)
	if err != nil {
		t.Fatal(err)
	}
	all, err := pinecorpus.Probes(root)
	if err != nil {
		t.Fatal(err)
	}
	n := 30
	if v, err := strconv.Atoi(os.Getenv("HLP_CORPUS_N")); err == nil && v > 0 {
		n = v
	}
	probes := subset(all, n)
	if names := os.Getenv("HLP_CORPUS_PROBES"); names != "" {
		want := map[string]bool{}
		for _, s := range strings.Split(names, ",") {
			want[strings.TrimSpace(s)] = true
		}
		probes = probes[:0]
		for _, p := range all {
			if want[p.Name] {
				probes = append(probes, p)
			}
		}
	}
	start := time.Now()
	bars, err := pinecorpus.ChartFeed(root, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cs := pinecorpus.Candles(bars)
	t.Logf("feed: %d 15m bars (%s)", len(cs), time.Since(start).Round(time.Millisecond))

	engines := []pinecorpus.Engine{builtin{}}
	if e, err := pyne.Detect(ctx); err == nil {
		e.Timeout = 3 * time.Minute
		engines = append(engines, pyneEngine{e})
	} else {
		t.Logf("pyne: %v", err)
	}
	engines = append(engines, pfTape{})
	if os.Getenv("HLP_PINEFORGE") == "1" {
		if e, err := pineforge.Detect(ctx); err == nil {
			e.LicenseAccepted = true // HLP_PINEFORGE=1 is the runner's acceptance
			engines = append(engines, pfLive{e})
		} else {
			t.Logf("pineforge: %v", err)
		}
	}

	if only := os.Getenv("HLP_CORPUS_ENGINES"); only != "" { // e.g. "builtin,pineforge"
		kept := engines[:0]
		for _, e := range engines {
			for _, k := range strings.Split(only, ",") {
				if k = strings.TrimSpace(k); k != "" && strings.HasPrefix(e.Name(), k) {
					kept = append(kept, e)
					break
				}
			}
		}
		engines = kept
	}

	var jobs []pinecorpus.Job
	for _, p := range probes {
		src, err := p.Pine()
		if err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, pinecorpus.Job{Probe: p, Pine: src, Candles: cs, Interval: domain.I15m})
	}
	rows, err := pinecorpus.Score(ctx, jobs, engines, pinecorpus.GradeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Corpus %s, %d probes, %s\n\n", pinecorpus.Commit[:12], len(jobs), time.Now().UTC().Format("2006-01-02"))
	b.WriteString(pinecorpus.Markdown(rows))
	b.WriteString("\nbuiltin errors:\n")
	for _, l := range pinecorpus.Failures(rows, "builtin") {
		b.WriteString("    " + l + "\n")
	}
	b.WriteString("\ndetail:\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "    %-55s %-26s %-28s want=%d got=%d entryPx=%d exitT=%d/%d exitPx=%d pnl tv=%.2f got=%.2f %s %s %s\n",
			r.Probe, r.Engine, r.Cell(), r.Grade.Want, r.Grade.Got, r.Grade.EntryPxOK, r.Grade.ExitTimeOK, r.Grade.ClosedPairs,
			r.Grade.ExitPxOK, r.Grade.WantPnL, r.Grade.GotPnL, r.Elapsed.Round(time.Millisecond), r.Err, strings.Join(r.Notes, "; "))
	}
	t.Log("\n" + b.String())
	if out := os.Getenv("HLP_SCOREBOARD_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestLiveScoreboardDebug prints the unmatched trades and exit-time
// mismatches of one engine on HLP_CORPUS_PROBES (default builtin).
func TestLiveScoreboardDebug(t *testing.T) {
	names := os.Getenv("HLP_CORPUS_PROBES")
	if os.Getenv("HLP_LIVE") != "1" || os.Getenv(pinecorpus.EnvEnable) != "1" || names == "" {
		t.Skip("set HLP_LIVE=1, HLP_CORPUS=1 and HLP_CORPUS_PROBES")
	}
	ctx := context.Background()
	root, err := pinecorpus.Root(ctx)
	if err != nil {
		t.Fatal(err)
	}
	all, _ := pinecorpus.Probes(root)
	bars, err := pinecorpus.ChartFeed(root, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cs := pinecorpus.Candles(bars)
	var eng pinecorpus.Engine = builtin{}
	if os.Getenv("HLP_CORPUS_ENGINE") == "pineforge-tape" {
		eng = pfTape{}
	}
	for _, p := range all {
		if !strings.Contains(","+names+",", ","+p.Name+",") {
			continue
		}
		src, _ := p.Pine()
		run, err := eng.Run(ctx, pinecorpus.Job{Probe: p, Pine: src, Candles: cs, Interval: domain.I15m})
		if err != nil {
			t.Logf("%s: %v", p.Name, err)
			continue
		}
		want, _ := p.TVTrades()
		d := pinecorpus.Compare(want, run.Trades, 0)
		t.Logf("%s: %s", p.Name, d)
		f := func(tr pinecorpus.Trade) string {
			return fmt.Sprintf("long=%v %s@%.2f -> %s@%.2f pnl=%.2f open=%v sig=%q/%q", tr.Long, tr.EntryTime.Format("2006-01-02 15:04"), tr.EntryPrice,
				tr.ExitTime.Format("2006-01-02 15:04"), tr.ExitPrice, tr.PnL, tr.Open, tr.EntrySignal, tr.ExitSignal)
		}
		for i, m := range d.Missing {
			if i < 5 {
				t.Logf("  missing %s", f(m))
			}
		}
		for i, m := range d.Extra {
			if i < 5 {
				t.Logf("  extra   %s", f(m))
			}
		}
		byEntry := map[time.Time]pinecorpus.Trade{}
		for _, g := range run.Trades {
			byEntry[g.EntryTime] = g
		}
		shown := 0
		for _, w := range want {
			g, ok := byEntry[w.EntryTime]
			if ok && !w.Open && !g.Open && !w.ExitTime.Equal(g.ExitTime) && shown < 4 {
				shown++
				t.Logf("  exit tv  %s\n             got %s", f(w), f(g))
			}
		}
	}
}
