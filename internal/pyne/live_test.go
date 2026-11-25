package pyne

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// repoRoot locates the repository root from this file.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// btc15m loads the 301-bar BTC 15m candle sample.
func btc15m(t testing.TB) []domain.Candle {
	t.Helper()
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

func sampleFiles(t testing.TB) []string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(repoRoot(), "testdata", "pine-samples", "*.pine"))
	if len(files) != 10 {
		t.Fatalf("expected 10 research samples, found %d", len(files))
	}
	return files
}

func liveEngine(t *testing.T) *Engine {
	t.Helper()
	if os.Getenv("HLP_PYNE") != "1" {
		t.Skip("set HLP_PYNE=1 to run against a real pyne install")
	}
	e, err := Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	e.Timeout = 120 * time.Second
	return e
}

// TestLivePyne runs the ten research samples through the real pyne binary.
// HLP_PYNE_RECORD=1 also rewrites the fake-binary fixtures in testdata/.
func TestLivePyne(t *testing.T) {
	e := liveEngine(t)
	cs := btc15m(t)
	record := os.Getenv("HLP_PYNE_RECORD") == "1"
	t.Logf("pyne %s at %s", e.Version, e.Bin)
	for _, f := range sampleFiles(t) {
		name := strings.TrimSuffix(filepath.Base(f), ".pine")
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var raw []byte
			if record && strings.HasPrefix(name, "02-") {
				e.rawHook = func(args []string, out []byte) {
					if len(args) > 0 && args[0] == "execute" {
						raw = append([]byte(nil), out...)
					}
				}
				defer func() { e.rawHook = nil }()
			}
			r, err := e.Execute(context.Background(), string(src), cs, Options{Ticker: "BTC", Interval: domain.I15m})
			if err != nil {
				t.Fatal(err)
			}
			if !r.Instrumented {
				t.Fatalf("not instrumented: %v", r.Warnings)
			}
			if len(r.Equity) != len(cs) || len(r.Position) != len(cs) {
				t.Errorf("equity %d / position %d points, want %d", len(r.Equity), len(r.Position), len(cs))
			}
			if len(r.Trades) != r.Stats.ClosedTrades {
				t.Errorf("%d trades exported, closedtrades %d", len(r.Trades), r.Stats.ClosedTrades)
			}
			var sum float64
			for _, tr := range r.Trades {
				sum += tr.Profit
			}
			if math.Abs(sum-r.Stats.NetProfit) > 1e-6*math.Max(1, math.Abs(sum)) {
				t.Errorf("sum of trade profit %.6f != netprofit %.6f", sum, r.Stats.NetProfit)
			}
			t.Logf("%-28s trades=%-3d wins=%-3d net=%10.2f equity=%10.2f open=%8.4f mode=%s %v",
				r.Script.Name, r.Stats.ClosedTrades, r.Stats.WinTrades, r.Stats.NetProfit, r.Stats.FinalEquity, r.Position.Last(), r.Mode, r.Warnings)
			if raw != nil {
				writeFixture(t, "execute_ok.json", raw)
			}
		})
	}

	t.Run("check", func(t *testing.T) {
		var raw []byte
		if record {
			e.rawHook = func(args []string, out []byte) { raw = append([]byte(nil), out...) }
			defer func() { e.rawHook = nil }()
		}
		ds, err := e.Check(context.Background(), "//@version=6\nindicator(\"x\")\nx = )close\nplot(x)\n")
		if err != nil {
			t.Fatal(err)
		}
		errs := Errors(ds)
		if len(errs) == 0 || errs[0].Line != 3 || errs[0].Col != 5 {
			t.Fatalf("diagnostics = %+v, want an error at 3:5", ds)
		}
		if raw != nil {
			writeFixture(t, "diagnostics_error.json", raw)
		}
	})

	t.Run("parse_error", func(t *testing.T) {
		var raw []byte
		if record {
			e.rawHook = func(args []string, out []byte) {
				if args[0] == "execute" && raw == nil {
					raw = append([]byte(nil), out...)
				}
			}
			defer func() { e.rawHook = nil }()
		}
		_, err := e.Execute(context.Background(), "//@version=6\nstrategy(\"x\")\nx = ta.sma(close,\nplot(x)\n", cs[:50], Options{})
		se, ok := err.(*ScriptError)
		if !ok || se.Kind != "parse" || len(se.Diagnostics) == 0 || se.Diagnostics[0].Line > 4 {
			t.Fatalf("err = %#v", err)
		}
		t.Logf("parse error: %v", err)
		if raw != nil {
			writeFixture(t, "execute_parse_error.json", raw)
		}
	})

	t.Run("inputs", func(t *testing.T) {
		src, _ := os.ReadFile(sampleFiles(t)[1]) // 02-macd-cross
		a, err := e.Execute(context.Background(), string(src), cs, Options{Inputs: map[string]any{"fastLength": 5}})
		if err != nil {
			t.Fatal(err)
		}
		if got := a.Script.Inputs[0].Value; got != float64(5) {
			t.Fatalf("Fast length = %v, want 5 (%v)", got, a.Warnings)
		}
	})

	t.Run("follower", func(t *testing.T) {
		src, _ := os.ReadFile(sampleFiles(t)[1]) // 02-macd-cross
		fl, err := e.Compile(context.Background(), string(src), "BTC", domain.I15m, nil)
		if err != nil {
			t.Fatal(err)
		}
		full, err := e.Execute(context.Background(), string(src), cs, Options{})
		if err != nil {
			t.Fatal(err)
		}
		// Bar 72 signals a reversal that fills at bar 73's open.
		ctx := &fakeCtx{bars: cs[:73], pos: full.Position[72].V}
		if err := fl.OnBar(ctx); err != nil {
			t.Fatal(err)
		}
		want := full.Position[73].V
		if len(ctx.orders) != 1 || math.Abs(ctx.pos-want) > 1e-9 {
			t.Fatalf("orders %+v, position %.8f, want %.8f", ctx.orders, ctx.pos, want)
		}
	})
}

var tempPathRE = regexp.MustCompile(`"file": "[^"]*/script\.pine"`)

func writeFixture(t *testing.T, name string, b []byte) {
	t.Helper()
	b = tempPathRE.ReplaceAll(b, []byte(`"file": "script.pine"`)) // no local temp paths in testdata
	if err := os.WriteFile(filepath.Join("testdata", name), b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded testdata/%s (%d bytes)", name, len(b))
}
