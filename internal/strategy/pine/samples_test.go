package pine_test

import (
	"encoding/json"
	"flag"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/strategy/backtest"
	"github.com/maxmoneycash/hlp/internal/strategy/internal/testkit"
	"github.com/maxmoneycash/hlp/internal/strategy/pine"
)

var update = flag.Bool("update", false, "rewrite testdata/golden from the current results")

// synth1h is a deterministic 1h candle fixture (splitmix64 random walk with
// trending regimes, 2023-12-01 onwards). It is long enough for every
// sample's warm-up (200-bar SMAs, a daily EMA(20) through
// request.security) and has trends, ranges and squeezes. The generator is
// self-contained, so the fixture never changes with the Go version.
func synth1h(n int) []domain.Candle {
	state := uint64(0x9E3779B97F4A7C15)
	rnd := func() float64 {
		state += 0x9E3779B97F4A7C15
		z := state
		z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
		z = (z ^ (z >> 27)) * 0x94D049BB133111EB
		z ^= z >> 31
		return float64(z>>11) / float64(1<<53)
	}
	gauss := func() float64 { return rnd() + rnd() + rnd() + rnd() - 2 } // var 1/3
	t0 := time.Date(2023, 12, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	out := make([]domain.Candle, n)
	px, drift, vol := 40000.0, 0.0, 0.006
	for i := 0; i < n; i++ {
		if i%240 == 0 {
			drift = (rnd() - 0.5) * 0.002
			vol = 0.002 + rnd()*0.008
		}
		o := px
		c := o * math.Exp(drift+vol*gauss())
		h := math.Max(o, c) * (1 + vol*rnd()*0.8)
		l := math.Min(o, c) * (1 - vol*rnd()*0.8)
		out[i] = domain.Candle{T: t0 + int64(i)*3600_000, O: o, H: h, L: l, C: c, V: 50 + 100*rnd(), N: 100}
		px = c
	}
	return out
}

type dataset struct {
	name string
	iv   domain.Interval
	cs   []domain.Candle
}

func datasets(t *testing.T) []dataset {
	return []dataset{
		{"btc15m", domain.I15m, candles(t)},
		{"synth1h", domain.I1h, synth1h(3000)},
	}
}

func sampleFiles(t *testing.T) []string {
	files, _ := filepath.Glob(filepath.Join(testkit.RepoRoot(), "testdata", "pine-samples", "*.pine"))
	if len(files) != 10 {
		t.Fatalf("expected the 10 research samples, found %d", len(files))
	}
	return files
}

func runSample(t *testing.T, file string, ds dataset, inputs map[string]any) *backtest.Result {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	s, err := pine.Parse(string(b))
	if err != nil {
		t.Fatalf("%s: %v", filepath.Base(file), err)
	}
	st, err := pine.Compile(s, "BTC", ds.iv, inputs)
	if err != nil {
		t.Fatalf("%s: %v", filepath.Base(file), err)
	}
	return run(t, st, ds.cs, backtest.Options{Capital: s.InitialCapital})
}

type goldenFill struct {
	T    int64   `json:"t"`
	Side string  `json:"side"`
	Dir  string  `json:"dir"`
	Sz   float64 `json:"sz"`
	Px   float64 `json:"px"`
	Fee  float64 `json:"fee"`
	PnL  float64 `json:"pnl"`
}

type golden struct {
	Sample      string       `json:"sample"`
	Dataset     string       `json:"dataset"`
	Bars        int          `json:"bars"`
	Fills       []goldenFill `json:"fills"`
	FinalEquity float64      `json:"finalEquity"`
	// Equity is the mark-to-market equity every 25 bars (and the last).
	Equity []float64 `json:"equity"`
}

func toGolden(sample string, ds dataset, res *backtest.Result) golden {
	g := golden{Sample: sample, Dataset: ds.name, Bars: len(res.Equity), Fills: []goldenFill{}, FinalEquity: res.Equity.Last()}
	for _, f := range res.Fills {
		g.Fills = append(g.Fills, goldenFill{T: f.Time, Side: string(f.Side), Dir: f.Dir, Sz: f.Sz, Px: f.Px, Fee: f.Fee, PnL: f.ClosedPnL})
	}
	for i, p := range res.Equity {
		if i%25 == 0 || i == len(res.Equity)-1 {
			g.Equity = append(g.Equity, p.V)
		}
	}
	return g
}

// encodeGolden writes one fill per line so diffs stay readable and small.
func encodeGolden(g golden) []byte {
	var b strings.Builder
	head, _ := json.Marshal(struct {
		Sample      string  `json:"sample"`
		Dataset     string  `json:"dataset"`
		Bars        int     `json:"bars"`
		FinalEquity float64 `json:"finalEquity"`
	}{g.Sample, g.Dataset, g.Bars, g.FinalEquity})
	b.WriteString(strings.TrimSuffix(string(head), "}"))
	b.WriteString(",\n\"fills\": [")
	for i, f := range g.Fills {
		if i > 0 {
			b.WriteString(",")
		}
		x, _ := json.Marshal(f)
		b.WriteString("\n  " + string(x))
	}
	eq, _ := json.Marshal(g.Equity)
	b.WriteString("\n],\n\"equity\": " + string(eq) + "\n}")
	return []byte(b.String())
}

func near(a, b float64) bool {
	if a == b || (math.IsNaN(a) && math.IsNaN(b)) {
		return true
	}
	return math.Abs(a-b) <= 1e-9+1e-7*math.Max(math.Abs(a), math.Abs(b))
}

func compareGolden(t *testing.T, want, got golden) {
	t.Helper()
	id := want.Sample + "/" + want.Dataset
	if want.Bars != got.Bars {
		t.Errorf("%s: bars %d, golden %d", id, got.Bars, want.Bars)
	}
	if len(want.Fills) != len(got.Fills) {
		t.Errorf("%s: %d fills, golden %d", id, len(got.Fills), len(want.Fills))
	}
	for i := 0; i < min(len(want.Fills), len(got.Fills)); i++ {
		w, g := want.Fills[i], got.Fills[i]
		if w.T != g.T || w.Side != g.Side || w.Dir != g.Dir || !near(w.Sz, g.Sz) || !near(w.Px, g.Px) || !near(w.Fee, g.Fee) || !near(w.PnL, g.PnL) {
			t.Errorf("%s: fill %d = %+v, golden %+v", id, i, g, w)
			break
		}
	}
	if !near(want.FinalEquity, got.FinalEquity) {
		t.Errorf("%s: final equity %.6f, golden %.6f", id, got.FinalEquity, want.FinalEquity)
	}
	if len(want.Equity) != len(got.Equity) {
		t.Errorf("%s: %d equity samples, golden %d", id, len(got.Equity), len(want.Equity))
		return
	}
	for i := range want.Equity {
		if !near(want.Equity[i], got.Equity[i]) {
			t.Errorf("%s: equity sample %d = %.6f, golden %.6f", id, i, got.Equity[i], want.Equity[i])
			break
		}
	}
}

// TestSamplesGolden runs all ten research samples on both fixtures and
// compares trades and equity with testdata/golden (go test -run
// TestSamplesGolden -update rewrites them after an intended change).
func TestSamplesGolden(t *testing.T) {
	traded := map[string]int{}
	for _, ds := range datasets(t) {
		for _, f := range sampleFiles(t) {
			name := strings.TrimSuffix(filepath.Base(f), ".pine")
			res := runSample(t, f, ds, nil)
			got := toGolden(name, ds, res)
			traded[name] += len(got.Fills)
			path := filepath.Join("testdata", "golden", name+"."+ds.name+".json")
			if *update {
				b := encodeGolden(got)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update to create)", err)
			}
			var want golden
			if err := json.Unmarshal(b, &want); err != nil {
				t.Fatal(err)
			}
			compareGolden(t, want, got)
		}
	}
	for name, n := range traded {
		if n == 0 {
			t.Errorf("%s never traded on the fixtures", name)
		}
	}
}
