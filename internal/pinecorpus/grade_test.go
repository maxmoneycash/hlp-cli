package pinecorpus

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
)

var t0 = time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)

func bar(i int) time.Time { return t0.Add(time.Duration(i) * 15 * time.Minute) }

func tr(long bool, in, out int, inPx, outPx, pnl float64) Trade {
	return Trade{Long: long, EntryTime: bar(in), ExitTime: bar(out), EntryPrice: inPx, ExitPrice: outPx, Qty: 1, PnL: pnl}
}

func TestFillsToTrades(t *testing.T) {
	ms := func(i int) int64 { return bar(i).UnixMilli() }
	fills := []domain.Fill{
		{Side: domain.Buy, Px: 100, Sz: 1, Time: ms(1), Fee: 0.1},  // open long 1
		{Side: domain.Buy, Px: 102, Sz: 1, Time: ms(2), Fee: 0.1},  // pyramid add
		{Side: domain.Sell, Px: 110, Sz: 3, Time: ms(3), Fee: 0.3}, // close both, flip short 1
		{Side: domain.Buy, Px: 105, Sz: 0.5, Time: ms(4)},          // partial cover
	}
	got := FillsToTrades(fills)
	if len(got) != 4 {
		t.Fatalf("got %d trades: %+v", len(got), got)
	}
	a, b, c, d := got[0], got[1], got[2], got[3]
	if !a.Long || a.EntryPrice != 100 || a.ExitPrice != 110 || !a.ExitTime.Equal(bar(3)) || math.Abs(a.PnL-(10-0.1-0.1)) > 1e-9 {
		t.Errorf("first lot = %+v", a)
	}
	if !b.Long || b.EntryPrice != 102 || math.Abs(b.PnL-(8-0.1-0.1)) > 1e-9 {
		t.Errorf("second lot = %+v", b)
	}
	// The flip's short: 0.5 covered at 105, 0.5 still open.
	if c.Long || c.Qty != 0.5 || c.ExitPrice != 105 || math.Abs(c.PnL-(2.5-0.05)) > 1e-9 || c.Open {
		t.Errorf("covered short = %+v", c)
	}
	if d.Long || !d.Open || d.Qty != 0.5 || !d.EntryTime.Equal(bar(3)) {
		t.Errorf("open short = %+v", d)
	}
	for i, x := range got {
		if x.N != i+1 {
			t.Errorf("trade %d numbered %d", i, x.N)
		}
	}
}

func TestResample(t *testing.T) {
	var m1 []Bar
	for i := 0; i < 31; i++ { // 00:00 .. 00:30, one gap at 00:20
		if i == 20 {
			continue
		}
		px := float64(100 + i)
		m1 = append(m1, Bar{Time: t0.Add(time.Duration(i) * time.Minute), Open: px, High: px + 2, Low: px - 1, Close: px + 1, Volume: 1})
	}
	got := Resample(m1, 15*time.Minute)
	if len(got) != 3 {
		t.Fatalf("got %d bars", len(got))
	}
	b := got[1] // 00:15 .. 00:29 minus 00:20
	if !b.Time.Equal(t0.Add(15*time.Minute)) || b.Open != 115 || b.Close != 130 || b.High != 131 || b.Low != 114 || b.Volume != 14 {
		t.Errorf("bar 2 = %+v", b)
	}
	if c := Candles(got[:1])[0]; c.T != t0.UnixMilli() || c.O != 100 || c.C != 115 {
		t.Errorf("candle = %+v", c)
	}
}

func TestGradeTrades(t *testing.T) {
	want := []Trade{tr(true, 1, 3, 100, 110, 10), tr(false, 3, 5, 110, 105, 5), tr(true, 5, 8, 105, 107, 2)}
	if g := GradeTrades(want, want, GradeOptions{}); g.Verdict != Exact || g.Matched != 3 || g.ExitTimeOK != 3 {
		t.Fatalf("self grade = %+v", g)
	}
	// One exit a bar late at another price: still matched on entries.
	late := append([]Trade(nil), want...)
	late[2] = tr(true, 5, 9, 105, 108, 3)
	g := GradeTrades(want, late, GradeOptions{})
	if g.Verdict != Partial || g.ExitTimeOK != 2 || g.ExitPxOK != 2 || g.GotPnL != 18 || g.WantPnL != 17 {
		t.Fatalf("late exit = %+v", g)
	}
	if g := GradeTrades(want, late, GradeOptions{Slack: 15 * time.Minute, PriceTol: 0.01, PnLTol: 0.1}); g.Verdict != Exact {
		t.Fatalf("with slack = %+v", g)
	}
	// Wrong side everywhere.
	flipped := []Trade{tr(false, 1, 3, 100, 110, -10)}
	if g := GradeTrades(want, flipped, GradeOptions{}); g.Verdict != Diverged || g.Matched != 0 {
		t.Fatalf("flipped = %+v", g)
	}
	// A window keeps only the last trade.
	if g := GradeTrades(want, want[2:], GradeOptions{Window: Window{From: bar(4)}}); g.Want != 1 || g.Verdict != Exact {
		t.Fatalf("window = %+v", g)
	}
	if g := GradeTrades(nil, nil, GradeOptions{}); g.Verdict != NoTrades {
		t.Fatalf("empty = %+v", g)
	}
}

func TestGradeIgnoresRangeEnd(t *testing.T) {
	want := []Trade{tr(true, 1, 3, 100, 110, 10), tr(false, 3, 6, 110, 100, 10)}
	want[1].RangeEnd = true
	got := []Trade{tr(true, 1, 3, 100, 110, 10), tr(false, 3, 9, 110, 90, 20)}
	if g := GradeTrades(want, got, GradeOptions{}); g.Verdict != Exact || g.ClosedPairs != 1 || g.GotPnL != 10 {
		t.Fatalf("range end = %+v", g)
	}
}

func TestRangeEndDetection(t *testing.T) {
	csv := "Trade number,Type,Date and time,Signal,Price USDT,Net PnL USDT\n" +
		"2,Exit long,2025-01-02 00:00,,105,5\n" +
		"2,Entry long,2025-01-01 12:00,L,100,5\n" +
		"1,Exit short,2025-01-01 12:00,L,100,1\n" +
		"1,Entry short,2025-01-01 00:00,S,101,1\n"
	ts, err := ReadTVTrades(strings.NewReader(csv), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if ts[0].RangeEnd || !ts[1].RangeEnd {
		t.Fatalf("trades = %+v", ts)
	}
}

func TestPlain(t *testing.T) {
	for keys, want := range map[string]bool{"": true, "_comment,tv_trades_csv_tz": true, "_comment,input_tf": false, "runtime_overrides": false} {
		p := Probe{}
		if keys != "" {
			p.Keys = strings.Split(keys, ",")
		}
		if p.Plain() != want {
			t.Errorf("Plain(%q) = %v", keys, !want)
		}
	}
	if (Probe{Meta: Meta{ExpectedTier: "anomaly"}}).Plain() {
		t.Error("anomaly probe is plain")
	}
}

type fakeEngine struct {
	name   string
	trades []Trade
	err    error
}

func (f fakeEngine) Name() string { return f.name }
func (f fakeEngine) Run(context.Context, Job) (*EngineRun, error) {
	return &EngineRun{Trades: f.trades}, f.err
}

func TestScoreAndMarkdown(t *testing.T) {
	p := Probe{Name: "synthetic-01", Dir: "testdata"}
	p.Meta.TVTradesCSV = "tv_trades_synthetic.csv"
	p.Meta.TVTradesTZ = "utc"
	want, err := p.TVTrades()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Score(context.Background(), []Job{{Probe: p}}, []Engine{
		fakeEngine{name: "perfect", trades: want},
		fakeEngine{name: "broken", err: errors.New("parse: arrays are not supported\nmore")},
	}, GradeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Grade.Verdict != Exact || rows[1].Err != "parse: arrays are not supported" {
		t.Fatalf("rows = %+v", rows)
	}
	md := Markdown(rows)
	for _, s := range []string{"| probe | perfect | broken |", "| `synthetic-01` | exact 3/3 | error |", "| perfect | 1 | 0 | 0 | 0 | 0 | 0 |", "| broken | 0 | 0 | 0 | 0 | 1 | 0 |"} {
		if !strings.Contains(md, s) {
			t.Errorf("markdown missing %q:\n%s", s, md)
		}
	}
	if f := Failures(rows, "broken"); len(f) != 1 || !strings.Contains(f[0], "arrays") {
		t.Errorf("failures = %v", f)
	}
}

func TestGradeStartEdge(t *testing.T) {
	want := []Trade{tr(true, 2, 4, 100, 101, 1), tr(false, 4, 6, 101, 100, 1)}
	// The engine was already short from bar 0 and only flips at bar 4.
	got := []Trade{tr(false, 0, 4, 99, 101, -2), tr(false, 4, 6, 101, 100, 1)}
	g := GradeTrades(want, got, GradeOptions{})
	if !g.StartEdge || g.Want != 1 || g.Verdict != Exact {
		t.Fatalf("grade = %+v", g)
	}
	// Flat before the first trade: the miss counts.
	if g := GradeTrades(want, got[1:], GradeOptions{}); g.StartEdge || g.Verdict == Exact {
		t.Fatalf("flat grade = %+v", g)
	}
}
