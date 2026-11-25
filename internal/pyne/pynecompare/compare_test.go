// Package pynecompare holds the builtin-vs-PYNE comparison on the ten
// research samples. It lives in its own package so internal/pyne's unit
// tests never depend on the builtin interpreter compiling. Run it with
//
//	HLP_PYNE=1 go test ./internal/pyne/pynecompare -v
//
// It prints the table used in docs/PYNE.md.
package pynecompare

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/pyne"
	"github.com/maxmoneycash/hlp/internal/strategy/backtest"
	"github.com/maxmoneycash/hlp/internal/strategy/pine"
)

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func btc15m(t *testing.T) []domain.Candle {
	b, err := os.ReadFile(filepath.Join(repoRoot(), "testdata", "candles", "btc-15m.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	f := func(v any) float64 { s, _ := v.(string); x, _ := strconv.ParseFloat(s, 64); return x }
	out := make([]domain.Candle, len(raw))
	for i, r := range raw {
		out[i] = domain.Candle{T: int64(r["t"].(float64)), O: f(r["o"]), H: f(r["h"]), L: f(r["l"]), C: f(r["c"]), V: f(r["v"])}
	}
	return out
}

// synth1h is the deterministic 1h fixture used by the builtin engine's
// golden tests (internal/strategy/pine/samples_test.go).
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
	gauss := func() float64 { return rnd() + rnd() + rnd() + rnd() - 2 }
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

type source struct{ cs []domain.Candle }

func (s source) Candles(_ context.Context, _ string, _ domain.Interval, n int, end int64) ([]domain.Candle, error) {
	var out []domain.Candle
	for _, c := range s.cs {
		if end == 0 || c.T < end {
			out = append(out, c)
		}
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}

func (source) Funding(context.Context, string, int64, int64) ([]domain.FundingPoint, error) {
	return nil, nil
}

type builtinRow struct {
	ok     bool
	err    string
	closes int // fills that realised PnL
	net    float64
}

func runBuiltin(src string, iv domain.Interval, cs []domain.Candle, noCosts bool) builtinRow {
	s, err := pine.Parse(src)
	if err != nil {
		return builtinRow{err: strings.SplitN(err.Error(), "\n", 2)[0]}
	}
	st, err := pine.Compile(s, "BTC", iv, nil)
	if err != nil {
		return builtinRow{err: err.Error()}
	}
	end := cs[len(cs)-1].T + iv.Duration().Milliseconds()
	res, err := backtest.Run(context.Background(), st, source{cs}, backtest.Options{
		Capital: s.InitialCapital, NoCosts: noCosts, Now: func() time.Time { return time.UnixMilli(end + 1) },
	})
	if err != nil {
		return builtinRow{err: err.Error()}
	}
	r := builtinRow{ok: true, net: res.Equity.Last() - s.InitialCapital}
	for _, f := range res.Fills {
		if f.ClosedPnL != 0 {
			r.closes++
		}
	}
	return r
}

func TestCompareEngines(t *testing.T) {
	if os.Getenv("HLP_PYNE") != "1" {
		t.Skip("set HLP_PYNE=1 (needs a real pyne install)")
	}
	eng, err := pyne.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	eng.Timeout = 120 * time.Second
	files, _ := filepath.Glob(filepath.Join(repoRoot(), "testdata", "pine-samples", "*.pine"))
	sets := []struct {
		name string
		iv   domain.Interval
		cs   []domain.Candle
	}{{"btc15m", domain.I15m, btc15m(t)}, {"synth1h", domain.I1h, synth1h(3000)}}
	var b strings.Builder
	fmt.Fprintf(&b, "\n| sample | data | builtin closes | builtin net (no costs) | builtin net (HL costs) | PYNE closed trades | PYNE closed net | PYNE net incl. open | PYNE s |\n|---|---|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, ds := range sets {
		for _, f := range files {
			name := strings.TrimSuffix(filepath.Base(f), ".pine")
			src, _ := os.ReadFile(f)
			bi := runBuiltin(string(src), ds.iv, ds.cs, true)
			bc := runBuiltin(string(src), ds.iv, ds.cs, false)
			start := time.Now()
			r, perr := eng.Execute(context.Background(), string(src), ds.cs, pyne.Options{Ticker: "BTC", Interval: ds.iv})
			el := time.Since(start).Seconds()
			cell := func(r builtinRow, v string) string {
				if !r.ok {
					return "error: " + r.err
				}
				return v
			}
			pc, pn, pe := "", "", ""
			if perr != nil {
				pc = "error: " + perr.Error()
			} else {
				pc = strconv.Itoa(r.Stats.ClosedTrades)
				pn = fmt.Sprintf("%.2f", r.Stats.NetProfit)
				pe = fmt.Sprintf("%.2f", r.Stats.FinalEquity-r.Stats.InitialCapital)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %.1f |\n", name, ds.name,
				cell(bi, strconv.Itoa(bi.closes)), cell(bi, fmt.Sprintf("%.2f", bi.net)), cell(bc, fmt.Sprintf("%.2f", bc.net)),
				pc, pn, pe, el)
		}
	}
	t.Log(b.String())
}
