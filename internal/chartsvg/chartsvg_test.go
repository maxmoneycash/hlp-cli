package chartsvg

import (
	"encoding/xml"
	"flag"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp/internal/chart"
	"github.com/maxmoneycash/hlp/internal/domain"
)

var update = flag.Bool("update", false, "rewrite golden files")

func candles(n int) []domain.Candle {
	out := make([]domain.Candle, n)
	px := 64000.0
	for i := range out {
		o := px
		px += math.Sin(float64(i)/3)*180 + float64(i%5-2)*40
		c := px
		out[i] = domain.Candle{T: 1762300800000 + int64(i)*3600000, O: o, C: c,
			H: math.Max(o, c) + 60 + float64(i%7)*10, L: math.Min(o, c) - 50 - float64(i%4)*10, V: 100 + float64(i%9)*40}
	}
	return out
}

// wellFormed parses the SVG as XML end to end.
func wellFormed(t *testing.T, s string) {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(s))
	for {
		_, err := d.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("not well-formed XML: %v\n%s", err, s[:min(len(s), 400)])
		}
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/chartsvg -update)", err)
	}
	if string(want) != got {
		t.Fatalf("%s differs from golden; rerun with -update and review", name)
	}
}

func TestCandlesGolden(t *testing.T) {
	cs := candles(60)
	s := Candles(cs, Options{Coin: "BTC", Interval: "1h",
		Lines: []chart.HLine{{Price: 63500, Label: "entry", Kind: chart.LineEntry}, {Price: 61000, Label: "liq", Kind: chart.LineLiq}}})
	wellFormed(t, s)
	golden(t, "btc-1h-terminal.svg", s)
	for _, want := range []string{`data-n="60"`, `class="tag"`, "BTC · 1H", "#0a0a0b", "#ff4b3e"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(s, "<script") || strings.Contains(s, "href") {
		t.Fatal("svg must not carry scripts or links")
	}
}

func TestCandlesSkinsAndEdges(t *testing.T) {
	for _, p := range chart.Palettes {
		for _, style := range []string{"", "solid", "hollow"} {
			wellFormed(t, Candles(candles(30), Options{Skin: p.Name, Style: style, Width: 400, Height: 220}))
		}
	}
	// empty, single, flat and sub-dollar series still render
	wellFormed(t, Candles(nil, Options{}))
	wellFormed(t, Candles(candles(1), Options{}))
	flat := []domain.Candle{{T: 1, O: 1, H: 1, L: 1, C: 1}, {T: 2, O: 1, H: 1, L: 1, C: 1}}
	wellFormed(t, Candles(flat, Options{NoVolume: true}))
	tiny := []domain.Candle{{T: 1, O: 0.00012, H: 0.00013, L: 0.00011, C: 0.000125, V: 1}, {T: 2, O: 0.000125, H: 0.00014, L: 0.00012, C: 0.000121, V: 2}}
	if s := Candles(tiny, Options{}); !strings.Contains(s, "0.000121") {
		t.Fatalf("sub-cent tag lost precision: %s", s)
	}
	// hostile names are escaped
	s := Candles(candles(3), Options{Coin: `<script>"x"`, Lines: []chart.HLine{{Price: 64000, Label: "</text><a>"}}})
	wellFormed(t, s)
	if strings.Contains(s, "<script") || strings.Contains(s, "<a>") {
		t.Fatal("text not escaped")
	}
}

func TestSpark(t *testing.T) {
	var s domain.Series
	for i := range 50 {
		s = append(s, domain.Point{T: int64(i) * 86400000, V: 1 + float64(i)*0.004 - math.Mod(float64(i), 7)*0.003})
	}
	out := Spark(s, SparkOptions{Baseline: 1, Area: true})
	wellFormed(t, out)
	golden(t, "spark.svg", out)
	if !strings.Contains(out, "#42c86c") {
		t.Fatal("rising curve should use the gain ink")
	}
	down := Spark(domain.Series{{T: 0, V: 2}, {T: 1, V: 1}}, SparkOptions{Baseline: math.NaN()})
	if !strings.Contains(down, "#ff4b3e") {
		t.Fatal("falling curve should use the loss ink")
	}
	wellFormed(t, Spark(nil, SparkOptions{}))
	wellFormed(t, Spark(domain.Series{{T: 0, V: math.NaN()}, {T: 1, V: 1}}, SparkOptions{}))
}
