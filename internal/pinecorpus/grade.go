package pinecorpus

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// Engine is anything that can backtest a corpus probe: hlp's builtin
// interpreter, PYNE, PineForge, or a recorded tape. Adapters live with
// their callers (see internal/pinecorpus/scoreboard) so this package stays
// free of engine dependencies.
type Engine interface {
	Name() string
	// Run backtests job and returns its trades. An error means the engine
	// could not run the script at all (unsupported feature, crash).
	Run(ctx context.Context, job Job) (*EngineRun, error)
}

// Job is one probe run.
type Job struct {
	Probe    Probe
	Pine     string
	Candles  []domain.Candle // 15m chart feed, oldest first
	Interval domain.Interval
}

// EngineRun is an engine's answer for one job.
type EngineRun struct {
	Trades []Trade
	// Window, when set, limits grading to trades entered inside it (an
	// engine that only saw part of the feed, such as PYNE's 5,000-bar cap).
	Window Window
	Notes  []string // caveats shown in the scoreboard (e.g. costs not mapped)
}

// Window bounds trade entry times; a zero bound is open.
type Window struct{ From, To time.Time }

func (w Window) has(t time.Time) bool {
	return (w.From.IsZero() || !t.Before(w.From)) && (w.To.IsZero() || !t.After(w.To))
}

// GradeOptions tune GradeTrades.
type GradeOptions struct {
	Slack    time.Duration // entry/exit time tolerance (default 0: same bar)
	PriceTol float64       // relative price tolerance (default 1e-4)
	PnLTol   float64       // net-PnL tolerance as a share of gross |PnL| (default 0.01)
	Window   Window
}

// Verdicts, best first.
const (
	Exact    = "exact"    // same trades, times, prices (within PriceTol) and net PnL (within PnLTol)
	Close    = "close"    // >= 90% of trades match on entry and exit time
	Partial  = "partial"  // >= 50% match
	Diverged = "diverged" // < 50% match
	NoTrades = "no-trades"
)

// Grade compares one engine's trades with TradingView's on one probe.
type Grade struct {
	Want, Got   int // trades inside the window (got: inside want's entry span)
	Matched     int // same side and entry time
	ExitTimeOK  int // matched, both closed, same exit time
	EntryPxOK   int // matched with entry price within PriceTol
	ExitPxOK    int // matched, closed, exit price within PriceTol
	ClosedPairs int // matched pairs where both sides are closed
	WantPnL     float64
	GotPnL      float64 // net PnL of got's trades in the same span
	PnLGross    float64 // sum of |want PnL|, the scale PnLTol applies to
	// StartEdge is set when TradingView's first trade was left out because
	// the engine was already holding a position from before the tape's
	// date range (a harness artefact, not an engine fault).
	StartEdge bool
	Verdict   string
}

// holding reports whether some trade in ts was entered before at and was
// still open at at.
func holding(ts []Trade, at time.Time) bool {
	for _, t := range ts {
		if t.EntryTime.Before(at) && (t.Open || t.ExitTime.After(at)) {
			return true
		}
	}
	return false
}

// GradeTrades grades got against want (TradingView) on entry and exit
// times, prices, count and net PnL.
func GradeTrades(want, got []Trade, o GradeOptions) Grade {
	if o.PriceTol == 0 {
		o.PriceTol = 1e-4
	}
	if o.PnLTol == 0 {
		o.PnLTol = 0.01
	}
	var w, g []Trade
	for _, t := range want {
		if o.Window.has(t.EntryTime) {
			w = append(w, t)
		}
	}
	for _, t := range got {
		if o.Window.has(t.EntryTime) {
			g = append(g, t)
		}
	}
	pairs, missing, _, nGot := pair(w, g, o.Slack)
	startEdge := false
	if len(w) > 0 && len(missing) > 0 {
		// TradingView's date range starts flat; an engine that traded the
		// earlier history can enter the range still holding a position and
		// so skip TradingView's first trade. Drop that trade once.
		first := sortedByEntry(w)[0]
		if missing[0] == first && holding(g, first.EntryTime) {
			startEdge = true
			rest := make([]Trade, 0, len(w)-1)
			for _, t := range w {
				if t != first {
					rest = append(rest, t)
				}
			}
			w = rest
			pairs, _, _, nGot = pair(w, g, o.Slack)
		}
	}
	gr := Grade{Want: len(w), Got: nGot, Matched: len(pairs), StartEdge: startEdge}
	for _, t := range w {
		if !t.Open && !t.RangeEnd {
			gr.WantPnL += t.PnL
			gr.PnLGross += math.Abs(t.PnL)
		}
	}
	if len(w) > 0 {
		// Net PnL over the matched pairs' got side plus got's unmatched
		// closed trades inside want's span; the range-end trade is left
		// out on both sides.
		skip := map[Trade]bool{}
		for _, p := range pairs {
			if p[0].RangeEnd {
				skip[p[1]] = true
			}
		}
		sw := sortedByEntry(w)
		lo, hi := sw[0].EntryTime.Add(-o.Slack), sw[len(sw)-1].EntryTime.Add(o.Slack)
		for _, t := range g {
			if !t.Open && !skip[t] && !t.EntryTime.Before(lo) && !t.EntryTime.After(hi) {
				gr.GotPnL += t.PnL
			}
		}
	}
	for _, p := range pairs {
		wt, gt := p[0], p[1]
		if rel(gt.EntryPrice, wt.EntryPrice) <= o.PriceTol {
			gr.EntryPxOK++
		}
		if wt.Open || gt.Open || wt.RangeEnd {
			continue
		}
		gr.ClosedPairs++
		if absDur(gt.ExitTime.Sub(wt.ExitTime)) <= o.Slack {
			gr.ExitTimeOK++
		}
		if rel(gt.ExitPrice, wt.ExitPrice) <= o.PriceTol {
			gr.ExitPxOK++
		}
	}
	gr.Verdict = gr.verdict(o)
	return gr
}

func (g Grade) verdict(o GradeOptions) string {
	if g.Want == 0 {
		if g.Got == 0 {
			return NoTrades
		}
		return Diverged
	}
	n := max(g.Want, g.Got)
	pnlOK := math.Abs(g.GotPnL-g.WantPnL) <= o.PnLTol*math.Max(g.PnLGross, 1e-9)
	switch {
	case g.Want == g.Got && g.Matched == g.Want && g.ExitTimeOK == g.ClosedPairs &&
		g.EntryPxOK == g.Matched && g.ExitPxOK == g.ClosedPairs && pnlOK:
		return Exact
	case g.Matched*10 >= n*9 && g.ExitTimeOK*10 >= g.ClosedPairs*9:
		return Close
	case g.Matched*2 >= n:
		return Partial
	}
	return Diverged
}

// Short is a compact cell: "exact 335/335", "close 4801/4839 x97%".
func (g Grade) Short() string {
	s := fmt.Sprintf("%s %d/%d", g.Verdict, g.Matched, g.Want)
	if g.Verdict != Exact && g.Verdict != NoTrades && g.ClosedPairs > 0 {
		s += fmt.Sprintf(" x%d%%", 100*g.ExitTimeOK/g.ClosedPairs)
	}
	return s
}

// Row is one engine's result on one probe.
type Row struct {
	Probe   string
	Engine  string
	Grade   Grade
	Err     string // the engine could not run the probe
	Notes   []string
	Elapsed time.Duration
}

// Cell is the scoreboard cell for the row.
func (r Row) Cell() string {
	if r.Err != "" {
		return "error"
	}
	return r.Grade.Short()
}

// Score runs every engine on every job and grades it against the probe's
// TradingView tape.
func Score(ctx context.Context, jobs []Job, engines []Engine, o GradeOptions) ([]Row, error) {
	var rows []Row
	for _, job := range jobs {
		want, err := job.Probe.TVTrades()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", job.Probe.Name, err)
		}
		for _, e := range engines {
			if err := ctx.Err(); err != nil {
				return rows, err
			}
			row := Row{Probe: job.Probe.Name, Engine: e.Name()}
			start := time.Now()
			run, err := e.Run(ctx, job)
			row.Elapsed = time.Since(start)
			if err != nil {
				row.Err = firstLine(err.Error())
				rows = append(rows, row)
				continue
			}
			go2 := o
			if !run.Window.From.IsZero() || !run.Window.To.IsZero() {
				go2.Window = run.Window
			}
			row.Grade = GradeTrades(want, run.Trades, go2)
			row.Notes = run.Notes
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return s
}

// Markdown renders rows as a scoreboard: one line per probe with a column
// per engine, then a per-engine summary of verdict counts.
func Markdown(rows []Row) string {
	var engines, probes []string
	seenE, seenP := map[string]bool{}, map[string]bool{}
	cell := map[[2]string]Row{}
	for _, r := range rows {
		if !seenE[r.Engine] {
			seenE[r.Engine] = true
			engines = append(engines, r.Engine)
		}
		if !seenP[r.Probe] {
			seenP[r.Probe] = true
			probes = append(probes, r.Probe)
		}
		cell[[2]string{r.Probe, r.Engine}] = r
	}
	var b strings.Builder
	b.WriteString("| probe |")
	for _, e := range engines {
		b.WriteString(" " + e + " |")
	}
	b.WriteString("\n|---|")
	for range engines {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, p := range probes {
		b.WriteString("| `" + p + "` |")
		for _, e := range engines {
			r, ok := cell[[2]string{p, e}]
			c := "-"
			if ok {
				c = r.Cell()
			}
			b.WriteString(" " + c + " |")
		}
		b.WriteString("\n")
	}
	b.WriteString("\n| engine | exact | close | partial | diverged | error | no-trades |\n|---|---:|---:|---:|---:|---:|---:|\n")
	for _, e := range engines {
		n := map[string]int{}
		for _, r := range rows {
			if r.Engine != e {
				continue
			}
			if r.Err != "" {
				n["error"]++
			} else {
				n[r.Grade.Verdict]++
			}
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d |\n", e, n[Exact], n[Close], n[Partial], n[Diverged], n["error"], n[NoTrades])
	}
	return b.String()
}

// Failures groups one engine's errors by message, most common first.
func Failures(rows []Row, engine string) []string {
	n := map[string]int{}
	for _, r := range rows {
		if r.Engine == engine && r.Err != "" {
			n[r.Err]++
		}
	}
	var out []string
	for msg, c := range n {
		out = append(out, fmt.Sprintf("%3d  %s", c, msg))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}
