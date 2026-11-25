package chart

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/theme"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

const samplePath = "../../testdata/candles/btc-15m.json"

// samplePathOverride swaps the candle file loadBTC reads (previews only).
var samplePathOverride string

// loadBTC decodes the raw Hyperliquid candleSnapshot sample. Both "t" and
// "T" are declared: encoding/json matches keys case-insensitively, so a
// struct with only "t" would get the close time.
func loadBTC(t testing.TB) []domain.Candle {
	t.Helper()
	path := samplePath
	if samplePathOverride != "" {
		path = samplePathOverride
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var wire []struct {
		T      int64  `json:"t"`
		TClose int64  `json:"T"`
		O      string `json:"o"`
		H      string `json:"h"`
		L      string `json:"l"`
		C      string `json:"c"`
		V      string `json:"v"`
		N      int    `json:"n"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	f := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
	out := make([]domain.Candle, len(wire))
	for i, w := range wire {
		out[i] = domain.Candle{T: w.T, O: f(w.O), H: f(w.H), L: f(w.L), C: f(w.C), V: f(w.V), N: w.N}
	}
	return out
}

func hlpTheme(t testing.TB) theme.Theme {
	th, ok := theme.Get("hlp")
	if !ok {
		t.Fatal("hlp theme missing")
	}
	return th
}

// golden compares got with testdata/name.golden (rewritten with -update).
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/chart -update)", err)
	}
	if strings.TrimSuffix(string(want), "\n") != got {
		t.Errorf("%s differs from golden; run with -update after checking the output:\n%s", name, got)
	}
}

func assertWidths(t *testing.T, name string, out string, w, h int) {
	t.Helper()
	lines := strings.Split(out, "\n")
	if len(lines) != h {
		t.Fatalf("%s: %d lines, want %d", name, len(lines), h)
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != w {
			t.Fatalf("%s: line %d width %d, want %d: %q", name, i, got, w, l)
		}
	}
}

type goldenCase struct {
	name string
	o    CandleOpts
}

func goldenCases() []goldenCase {
	base := func(w, h int, s Style) CandleOpts {
		return CandleOpts{Width: w, Height: h, Style: s, VolRows: -1, MAs: []int{20, 50},
			Cursor: -1, Header: true, Coin: "BTC", Interval: domain.I15m, Skin: SkinNone}
	}
	var cs []goldenCase
	for _, size := range [][2]int{{90, 26}, {140, 40}} {
		for _, s := range []Style{Blocks, Lines, Half, Mono, ASCII, Heikin} {
			cs = append(cs, goldenCase{fmt.Sprintf("%s-%dx%d", s, size[0], size[1]), base(size[0], size[1], s)})
		}
	}
	z4 := base(140, 40, Blocks)
	z4.Zoom = 4
	z6 := base(140, 40, Blocks)
	z6.Zoom = 6
	z1 := base(90, 26, Blocks)
	z1.Zoom = 1
	cur := base(90, 26, Blocks)
	cur.Cursor = 7
	off := base(90, 26, Blocks)
	off.Offset = 10
	curWide := base(140, 40, Blocks)
	curWide.Zoom, curWide.Cursor = 4, 3
	lines := base(140, 40, Blocks)
	lines.Zoom = 4
	lines.Lines = []HLine{{Price: 73000, Kind: LineEntry, Label: "entry"}, {Price: 60000, Kind: LineLiq, Label: "liq"}}
	lines.VolumeUSD = true
	logc := base(90, 26, Blocks)
	logc.Log = true
	return append(cs,
		goldenCase{"blocks-zoom4-140x40", z4},
		goldenCase{"blocks-zoom6-140x40", z6},
		goldenCase{"blocks-zoom1-90x26", z1},
		goldenCase{"blocks-cursor-90x26", cur},
		goldenCase{"blocks-offset10-90x26", off},
		goldenCase{"blocks-zoom4-cursor-140x40", curWide},
		goldenCase{"blocks-lines-usd-140x40", lines},
		goldenCase{"blocks-log-90x26", logc},
	)
}

func TestCandlesGolden(t *testing.T) {
	cs := loadBTC(t)
	th := hlpTheme(t)
	for _, c := range goldenCases() {
		t.Run(c.name, func(t *testing.T) {
			g, vp := Candles(cs, c.o, th)
			if vp.Count == 0 {
				t.Fatal("no candles visible")
			}
			plain := g.Plain()
			assertWidths(t, c.name, plain, c.o.Width, c.o.Height)
			for _, p := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256, colorprofile.ANSI, colorprofile.ASCII, colorprofile.NoTTY} {
				assertWidths(t, c.name+"/"+p.String(), g.ANSI(p), c.o.Width, c.o.Height)
			}
			golden(t, c.name, plain)
		})
	}
}

// TestPreview writes ANSI frames to $HLP_CHART_PREVIEW for eyeballing in a
// real terminal (cat the file). Skipped unless the variable is set.
func TestPreview(t *testing.T) {
	out := os.Getenv("HLP_CHART_PREVIEW")
	if out == "" {
		t.Skip("set HLP_CHART_PREVIEW=/path/to/file")
	}
	cs := loadBTC(t)
	th := hlpTheme(t)
	if n := os.Getenv("HLP_CHART_THEME"); n != "" {
		th, _ = theme.Get(n)
	}
	prof := colorprofile.TrueColor
	switch os.Getenv("HLP_CHART_PROFILE") {
	case "256":
		prof = colorprofile.ANSI256
	case "16":
		prof = colorprofile.ANSI
	case "ascii":
		prof = colorprofile.ASCII
	}
	w, _ := strconv.Atoi(os.Getenv("HLP_CHART_W"))
	h, _ := strconv.Atoi(os.Getenv("HLP_CHART_H"))
	zoom, _ := strconv.Atoi(os.Getenv("HLP_CHART_ZOOM"))
	cursor := -1
	if v := os.Getenv("HLP_CHART_CURSOR"); v != "" {
		cursor, _ = strconv.Atoi(v)
	}
	o := CandleOpts{Width: max(w, 120), Height: max(h, 36), Style: Style(os.Getenv("HLP_CHART_STYLE")), Zoom: zoom,
		VolRows: -1, MAs: []int{20, 50}, Cursor: cursor, Header: true, Coin: "BTC", Interval: domain.I15m,
		Now: time.UnixMilli(cs[len(cs)-1].T).Add(5*time.Minute + 39*time.Second)}
	if os.Getenv("HLP_CHART_LINES") != "" {
		o.Lines = []HLine{{Price: 73000, Kind: LineEntry, Label: "entry"}, {Price: 60000, Kind: LineLiq, Label: "liq"}}
		o.Markers = []Marker{{T: cs[len(cs)-30].T, Buy: true}, {T: cs[len(cs)-12].T}}
	}
	var buf bytes.Buffer
	g, _ := Candles(cs, o, th)
	buf.WriteString(g.ANSI(prof) + "\n")
	if os.Getenv("HLP_CHART_EXTRA") != "" {
		buf.WriteString("\n" + previewExtras(t, th, prof, o.Width) + "\n")
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if p := os.Getenv("HLP_CHART_PNG"); p != "" {
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := g.PNG(f, th, 0, 0); err != nil {
			t.Fatal(err)
		}
	}
}

func previewExtras(t *testing.T, th theme.Theme, prof colorprofile.Profile, w int) string {
	var parts []string
	nav, btc := syntheticSeries(200, 1.0, 0.004, 1), syntheticSeries(200, 1.0, 0.02, 7)
	parts = append(parts, LineChart(LineOpts{Width: w, Height: 14, Title: "NAV", Area: true, Percent: true, Baseline: math.NaN(),
		Series: []Named{{Name: "Strategy X", Series: nav}, {Name: "BTC", Series: btc, Color: 2}}}, th).ANSI(prof))
	parts = append(parts, Heatmap(syntheticMonths(), w, th).ANSI(prof))
	parts = append(parts, Bars([]string{"BTC", "ETH", "HYPE", "SOL", "DOGE"}, []float64{4.2e6, 1.1e6, -2.5e6, 3e5, -7e5}, 70, th).ANSI(prof))
	parts = append(parts, Depth(loadBook(t), DepthOpts{Width: 70, Height: 23, Ladder: true, Decimals: -1}, th).ANSI(prof))
	parts = append(parts, Depth(loadBook(t), DepthOpts{Width: 70, Height: 10, Decimals: -1}, th).ANSI(prof))
	return strings.Join(parts, "\n\n")
}

// syntheticSeries is a deterministic daily random walk.
func syntheticSeries(n int, start, vol float64, seed uint64) domain.Series {
	s := make(domain.Series, n)
	v := start
	x := seed*2654435761 + 1
	t0 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	for i := range s {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		r := float64(x%10000)/10000 - 0.47
		v *= 1 + r*vol
		s[i] = domain.Point{T: t0 + int64(i)*86400000, V: v}
	}
	return s
}

func syntheticMonths() []domain.MonthlyReturn {
	var m []domain.MonthlyReturn
	vals := []float64{0.021, -0.034, 0.008, 0.112, -0.004, 0.035, 0.019, -0.081, 0.044, 0.0, 0.062, -0.013, 0.027, 0.051}
	for i, v := range vals {
		m = append(m, domain.MonthlyReturn{Year: 2025 + (i+9)/12, Month: (i+9)%12 + 1, Ret: v})
	}
	return m
}

func loadBook(t testing.TB) domain.Book {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/fixtures/hyperliquid/info/l2Book_BTC.json")
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Coin   string `json:"coin"`
		Time   int64  `json:"time"`
		Levels [2][]struct {
			Px string `json:"px"`
			Sz string `json:"sz"`
			N  int    `json:"n"`
		} `json:"levels"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	b := domain.Book{Coin: wire.Coin, Time: wire.Time}
	for side, lv := range wire.Levels {
		for _, l := range lv {
			px, _ := strconv.ParseFloat(l.Px, 64)
			sz, _ := strconv.ParseFloat(l.Sz, 64)
			if side == 0 {
				b.Bids = append(b.Bids, domain.Level{Px: px, Sz: sz, N: l.N})
			} else {
				b.Asks = append(b.Asks, domain.Level{Px: px, Sz: sz, N: l.N})
			}
		}
	}
	return b
}

func TestPNG(t *testing.T) {
	cs := loadBTC(t)
	th := hlpTheme(t)
	g, _ := Candles(cs, CandleOpts{Width: 60, Height: 20, VolRows: -1, Cursor: 2, Header: true, Coin: "BTC", Interval: domain.I15m, MAs: []int{20}}, th)
	var buf bytes.Buffer
	if err := g.PNG(&buf, th, 0, 0); err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 60*9 || b.Dy() != 20*18 {
		t.Fatalf("PNG size %v", b)
	}
}

// TestTickBarsAxisByIndex: tick bars have irregular times, so the axis is
// by bar index with the first trade's time every few bars (seconds for a
// short span), a date where the day changes, and no countdown.
func TestTickBarsAxisByIndex(t *testing.T) {
	base := time.Date(2026, 11, 15, 23, 59, 0, 0, time.UTC).UnixMilli()
	var cs []domain.Candle
	at := base
	for i := 0; i < 40; i++ {
		at += int64(1+i%7) * 1300 // irregular spacing, crosses midnight
		px := 100 + float64(i%5)
		cs = append(cs, domain.Candle{T: at, O: px, H: px + 1, L: px - 1, C: px + 0.5, V: 1})
	}
	opts := CandleOpts{Width: 100, Height: 16, Zoom: 2, VolRows: -1, Cursor: -1, Header: true, Coin: "BTC",
		Interval: domain.Interval("500T"), Location: time.UTC, TickBars: true, Now: time.UnixMilli(at), Skin: SkinNone}
	g, _ := Candles(cs, opts, theme.Default(true))
	out := g.Plain()
	if !strings.Contains(out, "500T") {
		t.Fatalf("no label:\n%s", out)
	}
	if !strings.Contains(out, "NOV 16") {
		t.Fatalf("no date at the day change:\n%s", out)
	}
	if !regexp.MustCompile(`23:59:\d\d`).MatchString(out) {
		t.Fatalf("no second-resolution labels:\n%s", out)
	}
	// the rush skin draws the same marks
	opts.Skin = ""
	if g, _ := Candles(cs, opts, theme.Default(true)); !regexp.MustCompile(`\d\d:\d\d:\d\d`).MatchString(g.Plain()) {
		t.Fatalf("rush skin lost tick labels:\n%s", g.Plain())
	}
}
