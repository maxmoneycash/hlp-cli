package chart

import (
	"math"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/charmbracelet/colorprofile"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/theme"
)

// G-T1: a thin level under a wall prints a non-zero size, and the order
// count column appears once the bars keep at least 14 columns.
func TestDepthThinLevelAndOrderCount(t *testing.T) {
	th := hlpTheme(t)
	b := domain.Book{
		Coin: "BTC",
		Asks: []domain.Level{{Px: 65001, Sz: 0.0002, N: 1}, {Px: 65002, Sz: 26.14, N: 12}},
		Bids: []domain.Level{{Px: 65000, Sz: 1.5, N: 3}, {Px: 64999, Sz: 0.00031, N: 1}},
	}
	for _, tc := range []struct {
		szDec int
		want  string
	}{{5, "0.00020"}, {0, "0.000200"}} {
		g := Depth(b, DepthOpts{Width: 60, Height: 8, Ladder: true, Decimals: -1, SzDecimals: tc.szDec}, th)
		s := g.Plain()
		if !strings.Contains(s, tc.want) {
			t.Fatalf("szDecimals %d: want %q in\n%s", tc.szDec, tc.want, s)
		}
		for _, line := range g.Lines()[1:] {
			if f := strings.Fields(line); len(f) > 1 && f[1] == "0.00" {
				t.Fatalf("szDecimals %d: a level printed as 0.00:\n%s", tc.szDec, s)
			}
		}
		head := g.Lines()[0]
		if !strings.Contains(head, "#") || strings.Index(head, "#") > strings.Index(head, "Total") ||
			strings.Index(head, "#") < strings.Index(head, "Size") {
			t.Fatalf("want a # column between Size and Total:\n%s", s)
		}
		if !strings.Contains(s, "12") {
			t.Fatalf("order count missing:\n%s", s)
		}
	}
	// The cap applies: a 26.14 wall keeps 2 decimals even with szDecimals 5,
	// and szDecimals 1 caps it at one.
	if s := Depth(b, DepthOpts{Width: 60, Height: 8, Ladder: true, Decimals: -1, SzDecimals: 1}, th).Plain(); !strings.Contains(s, "26.1 ") {
		t.Fatalf("szDecimals cap:\n%s", s)
	}
	// Narrow: no room for 14 bar columns, so no # column.
	narrow := Depth(b, DepthOpts{Width: 36, Height: 8, Ladder: true, Decimals: -1}, th)
	if strings.Contains(narrow.Lines()[0], "#") {
		t.Fatalf("narrow ladder grew a # column:\n%s", narrow.Plain())
	}
	// A feed without order counts gets no empty column.
	noN := b
	noN.Asks = []domain.Level{{Px: 65001, Sz: 1}}
	noN.Bids = []domain.Level{{Px: 65000, Sz: 2}}
	if g := Depth(noN, DepthOpts{Width: 60, Height: 6, Ladder: true, Decimals: -1}, th); strings.Contains(g.Lines()[0], "#") {
		t.Fatalf("book without counts shows #:\n%s", g.Plain())
	}
}

func TestLevelSize(t *testing.T) {
	for _, tc := range []struct {
		sz        float64
		sdec, szd int
		want      string
	}{
		{26.14, 2, 5, "26.14"},
		{0.0002, 2, 5, "0.00020"},
		{0.0002, 2, 0, "0.000200"},
		{1234.6, 0, 1, "1,235"},
		{0, 2, 5, "0.00"},
		{3.25, 4, 2, "3.25"},
	} {
		if got := levelSize(tc.sz, tc.sdec, tc.szd); got != tc.want {
			t.Errorf("levelSize(%v, %d, %d) = %q, want %q", tc.sz, tc.sdec, tc.szd, got, tc.want)
		}
	}
}

// G-T2: with NoTitle the header carries no coin or interval, with or
// without a skin, and the skinned chart drops its watermark too.
func TestCandlesNoTitle(t *testing.T) {
	th := hlpTheme(t)
	cs := synth(300)
	for _, skin := range []string{"", SkinNone} {
		for _, header := range []bool{true, false} {
			o := CandleOpts{Width: 100, Height: 30, Cursor: -1, Header: header, Coin: "ZETA", Interval: domain.I4h, Skin: skin}
			with, _ := Candles(cs, o, th)
			o.NoTitle = true
			without, _ := Candles(cs, o, th)
			if header && skin == SkinNone && !strings.Contains(with.Lines()[0], "ZETA") {
				t.Fatalf("skin %q: control header lacks the coin:\n%s", skin, with.Plain())
			}
			if s := without.Plain(); strings.Contains(strings.ToUpper(s), "ZETA") || strings.Contains(strings.ToUpper(s), "4H") {
				t.Fatalf("skin %q header %v: NoTitle still names the chart:\n%s", skin, header, s)
			}
			if header && !strings.Contains(without.Lines()[0], "O ") {
				t.Fatalf("skin %q: NoTitle dropped the OHLC readout:\n%s", skin, without.Plain())
			}
		}
	}
}

// G-T3: at 200x60 with 5,000 candles a fresh frame allocates at most half
// the PERFORMANCE table's 1.2 MB / 384 allocations, and a reused grid far
// less.
func TestCandlesFrameAllocations(t *testing.T) {
	if testing.Short() {
		t.Skip("allocation budget")
	}
	th, _ := theme.Get("hlp")
	cs := synth(5000)
	o := benchOpts(200, 60)
	measure := func(frame func()) (bytes, allocs float64) {
		frame() // warm up lazy package state
		const runs = 20
		var a, b runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&a)
		for range runs {
			frame()
		}
		runtime.ReadMemStats(&b)
		return float64(b.TotalAlloc-a.TotalAlloc) / runs, float64(b.Mallocs-a.Mallocs) / runs
	}
	bytes, allocs := measure(func() {
		g, _ := Candles(cs, o, th)
		_ = g.ANSI(colorprofile.TrueColor)
	})
	t.Logf("fresh frame: %.0f B, %.0f allocs", bytes, allocs)
	if bytes > 600_000 || allocs > 192 {
		t.Errorf("fresh 200x60 frame: %.0f B and %.0f allocs, budget 600,000 B and 192", bytes, allocs)
	}
	var into *Grid
	bytes, allocs = measure(func() {
		lo := o
		lo.Into = into
		g, _ := Candles(cs, lo, th)
		into = g
		_ = g.ANSI(colorprofile.TrueColor)
	})
	t.Logf("reused grid: %.0f B, %.0f allocs", bytes, allocs)
	if bytes > 300_000 {
		t.Errorf("reused 200x60 frame: %.0f B, budget 300,000", bytes)
	}
}

func TestColorAndCellSize(t *testing.T) {
	if s := unsafe.Sizeof(theme.Color{}); s != 8 {
		t.Errorf("theme.Color is %d bytes, want 8", s)
	}
	if s := unsafe.Sizeof(Cell{}); s != 24 {
		t.Errorf("Cell is %d bytes, want 24", s)
	}
}

// A grid passed back through Into gives the same frame as a fresh one, on
// live ticks, appended bars, pans, resizes and a replaced series.
func TestCandlesIntoMatchesFresh(t *testing.T) {
	th := hlpTheme(t)
	base := synth(800)
	cs := make([]domain.Candle, len(base), len(base)+16)
	copy(cs, base)
	var into *Grid
	check := func(step string, series []domain.Candle, o CandleOpts) {
		t.Helper()
		fresh, fv := Candles(series, o, th)
		o.Into = into
		got, gv := Candles(series, o, th)
		if into != nil && got != into {
			t.Fatalf("%s: Into grid was not reused", step)
		}
		into = got
		if got.ANSI(colorprofile.TrueColor) != fresh.ANSI(colorprofile.TrueColor) || gv != fv {
			t.Fatalf("%s: reused frame differs\nfresh:\n%s\nreused:\n%s", step, fresh.Plain(), got.Plain())
		}
	}
	for _, skin := range []string{"", SkinNone} {
		into = nil
		o := CandleOpts{Width: 120, Height: 36, VolRows: -1, MAs: []int{20}, EMAs: []int{50, 200}, Cursor: -1,
			Header: true, Coin: "BTC", Interval: domain.I1m, Skin: skin}
		check(skin+" first", cs, o)
		for i := range 5 { // live ticks on the open bar
			cs[len(cs)-1].C += float64(i+1) * 0.37
			check(skin+" tick", cs, o)
		}
		for i := range 3 { // new bars appended in place
			last := cs[len(cs)-1]
			cs = append(cs, domain.Candle{T: last.T + 60000, O: last.C, H: last.C + 1, L: last.C - 1, C: last.C + float64(i) - 1, V: 3})
			check(skin+" append", cs, o)
		}
		o.Offset = 40
		check(skin+" pan", cs, o)
		o.Offset = 0
		o.Width, o.Height = 90, 26
		check(skin+" resize", cs, o)
		other := synth(600)
		for i := range other {
			other[i].C *= 2
		}
		check(skin+" new series", other, o)
		check(skin+" back", cs, o)
	}
}

// The windowed overlays equal the full-series indicators, NaNs included.
func TestOverlayWindows(t *testing.T) {
	cs := synth(400)
	cs[150].C = math.NaN()
	memo := &overlayMemo{}
	for _, p := range []int{1, 5, 20, 200, 0} {
		sma, ema := SMA(cs, p), EMA(cs, p)
		for _, w := range [][2]int{{0, 400}, {100, 180}, {140, 160}, {250, 400}, {399, 400}, {0, 0}} {
			gs := smaWindow(cs, p, w[0], w[1])
			ge := emaWindow(cs, p, w[0], w[1], memo)
			for i := w[0]; i < w[1]; i++ {
				if !nearRel(gs[i-w[0]], sma[i]) {
					t.Fatalf("SMA%d window %v at %d: %v want %v", p, w, i, gs[i-w[0]], sma[i])
				}
				if !nearRel(ge[i-w[0]], ema[i]) {
					t.Fatalf("EMA%d window %v at %d: %v want %v", p, w, i, ge[i-w[0]], ema[i])
				}
			}
		}
	}
}

func nearRel(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.IsNaN(a) && math.IsNaN(b)
	}
	return math.Abs(a-b) <= 1e-9*math.Max(1, math.Abs(b))
}

func TestAutoStyleLegacyWindows(t *testing.T) {
	if autoStyle(colorprofile.TrueColor, true, "windows", "") != ASCII {
		t.Error("conhost without Windows Terminal should get ascii")
	}
	if autoStyle(colorprofile.TrueColor, true, "windows", "0b1c-guid") != Classic {
		t.Error("Windows Terminal keeps the default style")
	}
	if autoStyle(colorprofile.TrueColor, true, "darwin", "") != Classic {
		t.Error("macOS keeps the default style")
	}
}

func TestSGRParams(t *testing.T) {
	c := theme.Color{True: theme.Hex("#0a80ff"), C256: 33, C16: 12}
	for _, tc := range []struct {
		p      colorprofile.Profile
		fg, bg string
	}{
		{colorprofile.TrueColor, "38;2;10;128;255", "48;2;10;128;255"},
		{colorprofile.ANSI256, "38;5;33", "48;5;33"},
		{colorprofile.ANSI, "94", "104"},
	} {
		got := resolveSGR(c, tc.p)
		if got.code != tc.fg || got.bgCode != tc.bg {
			t.Errorf("%v: %q/%q, want %q/%q", tc.p, got.code, got.bgCode, tc.fg, tc.bg)
		}
	}
	if got := resolveSGR(theme.Color{True: theme.Hex("#000000"), C16: 3}, colorprofile.ANSI); got.code != "33" || got.bgCode != "43" {
		t.Errorf("basic color: %+v", got)
	}
}
