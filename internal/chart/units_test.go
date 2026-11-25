package chart

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/theme"
)

func TestBodyGlyph(t *testing.T) {
	for _, c := range []struct {
		a, b     int
		up, down bool
		want     rune
		wantRev  bool
	}{
		{0, 8, false, false, '█', false},
		{0, 3, false, false, '▃', false}, // top edge, no wick: eighth precision
		{0, 3, true, false, '│', false},  // top edge, wick continues: round to the wick
		{0, 5, true, false, '█', false},  // ...or to a full block
		{5, 8, false, false, '▅', true},  // bottom edge: upper 3/8 via SGR 7 on ▅
		{4, 8, false, false, '▀', false}, // exact upper half exists
		{7, 8, false, false, '▔', false}, // exact upper eighth exists
		{3, 8, false, true, '█', false},  // bottom edge with wick below rounds to full
		{6, 8, false, true, '│', false},  // ...or to the wick
		{3, 4, true, true, '┿', false},   // doji with wicks both sides
		{3, 4, true, false, '┷', false},
		{3, 4, false, true, '┯', false},
		{3, 4, false, false, '━', false}, // doji, no wicks
		{2, 6, false, false, '▀', false}, // 4/8 centered: a+b>=8 → ▀
		{1, 5, false, false, '▄', false}, // 4/8 low: ▄
		{1, 7, false, false, '█', false}, // big inside body
	} {
		got, rev := bodyGlyph(c.a, c.b, c.up, c.down)
		if got != c.want || rev != c.wantRev {
			t.Errorf("bodyGlyph(%d,%d,%v,%v)=%q,%v want %q,%v", c.a, c.b, c.up, c.down, got, rev, c.want, c.wantRev)
		}
	}
}

func TestWickGlyph(t *testing.T) {
	for _, c := range []struct {
		a, b int
		want rune
	}{
		{0, 8, '│'}, {0, 6, '│'}, {0, 3, '╷'}, {0, 1, 0},
		{2, 8, '│'}, {5, 8, '╵'}, {7, 8, 0},
		{1, 7, '│'}, {4, 6, '╵'}, {1, 3, '╷'},
	} {
		if got := wickGlyph(c.a, c.b); got != c.want {
			t.Errorf("wickGlyph(%d,%d)=%q want %q", c.a, c.b, got, c.want)
		}
	}
}

func TestLinesGlyph(t *testing.T) {
	cases := map[[2]int]rune{{2, 2}: '┃', {1, 1}: '│', {2, 1}: '╽', {1, 2}: '╿', {2, 0}: '╻', {0, 2}: '╹', {1, 0}: '╷', {0, 1}: '╵', {0, 0}: 0}
	for k, want := range cases {
		if got := linesGlyph(k[0], k[1], '┃'); got != want {
			t.Errorf("linesGlyph%v=%q want %q", k, got, want)
		}
	}
	if got := linesGlyph(2, 1, '║'); got != '║' {
		t.Errorf("hollow half body = %q", got)
	}
}

func TestNiceStep(t *testing.T) {
	for _, c := range []struct{ raw, want float64 }{
		{0.9, 1}, {1.1, 2}, {2.2, 2.5}, {3, 5}, {7, 10}, {1700, 2000}, {2100, 2500}, {0.00037, 0.0005}, {-1, 1}, {math.NaN(), 1},
	} {
		if got := NiceStep(c.raw); math.Abs(got-c.want) > 1e-12 {
			t.Errorf("NiceStep(%v)=%v want %v", c.raw, got, c.want)
		}
	}
}

func TestStepDecimals(t *testing.T) {
	for _, c := range []struct {
		step float64
		want int
	}{{1000, 0}, {2.5, 1}, {0.25, 2}, {0.0005, 4}, {1, 0}} {
		if got := StepDecimals(c.step); got != c.want {
			t.Errorf("StepDecimals(%v)=%d want %d", c.step, got, c.want)
		}
	}
}

func TestPriceDecimals(t *testing.T) {
	for _, c := range []struct {
		p    float64
		step int
		want int
	}{
		{65462, 0, 0}, {3450.5, 0, 1}, {24.123, 0, 3}, {1.2345, 0, 4}, {0.012345, 0, 6}, {0.0000123, 0, 6},
		{65462, 2, 2}, {0, 3, 3},
	} {
		if got := PriceDecimals(c.p, c.step); got != c.want {
			t.Errorf("PriceDecimals(%v,%d)=%d want %d", c.p, c.step, got, c.want)
		}
	}
}

func TestPriceTicks(t *testing.T) {
	ticks, step := PriceTicks(61800, 66300, 20, 4)
	if step != 1000 {
		t.Fatalf("step %v", step)
	}
	want := []float64{62000, 63000, 64000, 65000, 66000}
	if len(ticks) != len(want) {
		t.Fatalf("ticks %v", ticks)
	}
	for i := range want {
		if ticks[i] != want[i] {
			t.Fatalf("ticks %v", ticks)
		}
	}
	lt := LogTicks(1, 1000, 10)
	if len(lt) == 0 || lt[0] != 1 || lt[len(lt)-1] != 1000 {
		t.Fatalf("log ticks %v", lt)
	}
	if LogTicks(0, 10, 5) != nil {
		t.Fatal("non-positive range should give no log ticks")
	}
}

func TestFormatters(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{FormatPrice(65462, 0), "65,462"},
		{FormatPrice(1234567.891, 2), "1,234,567.89"},
		{FormatPrice(-1234.5, 1), "-1,234.5"},
		{FormatPrice(-0.0001, 2), "0.00"},
		{FormatPrice(math.NaN(), 2), "-"},
		{CompactNum(487.56), "488"},
		{CompactNum(6660), "6.66K"},
		{CompactNum(12345678), "12.3M"},
		{CompactNum(-2.5e9), "-2.50B"},
		{CompactNum(1.2e12), "1.20T"},
		{CompactNum(0.0123), "0.0123"},
		{CompactNum(0), "0"},
		{FormatPercent(0.1234, 2), "+12.34%"},
		{FormatPercent(-0.0029, 2), "-0.29%"},
		{FormatPercent(-0.00001, 2), "0.00%"},
		{FormatCountdown(9*time.Minute + 21*time.Second), "09:21"},
		{FormatCountdown(3*time.Hour + 4*time.Minute + 5*time.Second), "3:04:05"},
		{FormatCountdown(50 * time.Hour), "2d 02h"},
		{FormatCountdown(-time.Second), "00:00"},
	} {
		if c.got != c.want {
			t.Errorf("got %q want %q", c.got, c.want)
		}
	}
}

func TestTimeAxisHelpers(t *testing.T) {
	if s := TimeStep(15*time.Minute, 2, 9); s != time.Hour+0 && s != 75*time.Minute {
		// 9 cols / 2 per candle = 5 candles = 75m -> next step is 2h
		if s != 2*time.Hour {
			t.Errorf("TimeStep 15m stride 2 = %v", s)
		}
	}
	if s := TimeStep(24*time.Hour, 4, 9); s != 3*24*time.Hour && s != 7*24*time.Hour {
		t.Errorf("TimeStep 1d stride 4 = %v", s)
	}
	mid := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	if !IsTimeBoundary(mid, 2*time.Hour) || IsTimeBoundary(mid.Add(time.Hour), 2*time.Hour) {
		t.Error("2h boundary")
	}
	if !IsTimeBoundary(time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC), 7*day) { // a Monday
		t.Error("weekly boundary on Monday")
	}
	if !IsTimeBoundary(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), 90*day) || IsTimeBoundary(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), 90*day) {
		t.Error("quarterly boundary")
	}
	if got := TimeLabel(mid, time.Hour); got != "NOV 15" {
		t.Errorf("midnight label %q", got)
	}
	if got := TimeLabel(mid.Add(90*time.Minute), time.Hour); got != "01:30" {
		t.Errorf("clock label %q", got)
	}
	if got := TimeLabel(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 30*day); got != "2026" {
		t.Errorf("year label %q", got)
	}
}

func TestIndicators(t *testing.T) {
	cs := make([]domain.Candle, 6)
	for i := range cs {
		v := float64(i + 1)
		cs[i] = domain.Candle{T: int64(i), O: v, H: v + 1, L: v - 1, C: v}
	}
	sma := SMA(cs, 3)
	if !math.IsNaN(sma[1]) || sma[2] != 2 || sma[5] != 5 {
		t.Fatalf("SMA %v", sma)
	}
	ema := EMA(cs, 3)
	// Seed SMA(1,2,3)=2, then alpha=0.5: 3, 4, 5.
	if !math.IsNaN(ema[1]) || ema[2] != 2 || ema[3] != 3 || ema[5] != 5 {
		t.Fatalf("EMA %v", ema)
	}
	if v := SMAValues([]float64{1, math.NaN(), 2, 4}, 2); !math.IsNaN(v[2]) || v[3] != 3 {
		t.Fatalf("SMAValues NaN restart %v", v)
	}
	if v := EMAValues([]float64{1, 2}, 0); !math.IsNaN(v[0]) {
		t.Fatal("period 0 must be NaN")
	}
	ha := HeikinAshi([]domain.Candle{{O: 10, H: 12, L: 9, C: 11}, {O: 11, H: 13, L: 10, C: 12}})
	if ha[0].C != 10.5 || ha[0].O != 10.5 || ha[1].O != 10.5 || ha[1].C != 11.5 || ha[1].H != 13 || ha[1].L != 10 {
		t.Fatalf("HA %+v", ha)
	}
	if len(Closes(cs)) != 6 {
		t.Fatal("Closes")
	}
}

func TestSparkline(t *testing.T) {
	if got := Sparkline([]float64{1, 2, 3, 4, 5, 6, 7, 8}, 8); got != "▁▂▃▄▅▆▇█" {
		t.Fatalf("got %q", got)
	}
	if got := Sparkline(nil, 3); got != "   " {
		t.Fatalf("empty %q", got)
	}
	if got := Sparkline([]float64{5, 5}, 3); got != "▄▄▄" {
		t.Fatalf("flat %q", got)
	}
	if got := Sparkline([]float64{1, 2}, 0); got != "" {
		t.Fatalf("zero width %q", got)
	}
	long := make([]float64, 100)
	for i := range long {
		long[i] = float64(i % 10)
	}
	long[99] = 100
	got := []rune(Sparkline(long, 10))
	if len(got) != 10 || got[9] != '█' {
		t.Fatalf("downsample keeps last: %q", string(got))
	}
	if got := []rune(Sparkline([]float64{0, 1}, 5)); len(got) != 5 || got[0] != '▁' || got[4] != '█' {
		t.Fatalf("upsample %q", string(got))
	}
	if got := Sparkline([]float64{1, math.NaN(), 3}, 3); []rune(got)[1] != ' ' {
		t.Fatalf("NaN gap %q", got)
	}
}

func TestViewportIndexAt(t *testing.T) {
	cs := loadBTC(t)
	th := hlpTheme(t)
	for _, z := range Zooms {
		for _, off := range []int{0, 7} {
			_, vp := Candles(cs, CandleOpts{Width: 120, Height: 30, Zoom: z, Offset: off, VolRows: -1, Cursor: -1}, th)
			if vp.Count == 0 || vp.Stride != z || vp.First+vp.Count != len(cs)-off {
				t.Fatalf("zoom %d off %d: viewport %+v", z, off, vp)
			}
			for i := vp.First; i < vp.First+vp.Count; i++ {
				x := vp.CenterX(i)
				if got := vp.IndexAt(x); got != i {
					t.Fatalf("zoom %d: IndexAt(CenterX(%d)=%d)=%d", z, i, x, got)
				}
			}
			if vp.IndexAt(-1) != -1 || vp.IndexAt(vp.Edge) != -1 || vp.CenterX(vp.First-1) != -1 {
				t.Fatal("out-of-range mapping")
			}
		}
	}
	if (Viewport{}).IndexAt(3) != -1 {
		t.Fatal("empty viewport")
	}
	_, vp := Candles(cs, CandleOpts{Width: 100, Height: 30, VolRows: 0, Cursor: -1}, th)
	top, bottom := vp.PriceAt(vp.PlotY), vp.PriceAt(vp.PlotY+vp.PlotH-1)
	if !(top > bottom && top < vp.Hi && bottom > vp.Lo) || !math.IsNaN(vp.PriceAt(vp.PlotY+vp.PlotH)) {
		t.Fatalf("PriceAt %v %v (%v..%v)", top, bottom, vp.Lo, vp.Hi)
	}
}

func TestZoomSteps(t *testing.T) {
	if ZoomIn(2) != 4 || ZoomIn(6) != 6 || ZoomOut(2) != 1 || ZoomOut(1) != 1 || ZoomIn(0) != 4 || ZoomOut(5) != 2 {
		t.Fatal("zoom stepping")
	}
	if AutoStyle(colorprofile.TrueColor, true) != Classic || AutoStyle(colorprofile.NoTTY, true) != Mono ||
		AutoStyle(colorprofile.ASCII, true) != Mono || AutoStyle(colorprofile.ANSI, false) != ASCII {
		t.Fatal("AutoStyle")
	}
	if !Style("").Valid() || !Blocks.Valid() || Style("pixel").Valid() {
		t.Fatal("style validity")
	}
}

func TestASCIIStyleIs7Bit(t *testing.T) {
	cs := loadBTC(t)
	th := hlpTheme(t)
	for _, o := range []CandleOpts{
		{Width: 90, Height: 26, Style: ASCII, VolRows: -1, MAs: []int{20}, Cursor: 4, Header: true, Coin: "BTC", Interval: domain.I15m,
			Lines: []HLine{{Price: 64000, Kind: LineEntry}, {Price: 90000, Kind: LineTP}}, Markers: []Marker{{T: cs[len(cs)-3].T, Buy: true}}, Offset: 2},
		{Width: 30, Height: 5, Style: ASCII, Coin: "BTC"},
	} {
		g, _ := Candles(cs, o, th)
		for _, p := range []colorprofile.Profile{colorprofile.ASCII, colorprofile.NoTTY} {
			out := g.ANSI(p)
			for i := 0; i < len(out); i++ {
				if out[i] >= 0x80 {
					t.Fatalf("non-ASCII byte 0x%x at %d in %q", out[i], i, out)
				}
			}
		}
	}
}

func TestANSIProfiles(t *testing.T) {
	th := hlpTheme(t)
	g := NewGrid(4, 1)
	g.Set(0, 0, '│', th.AxisLine, LAxis)
	g.Set(1, 0, '┈', th.Grid, LGrid)
	if c := g.Set(2, 0, '▅', th.Up, LCandle); c != nil {
		c.Reverse = true
	}
	g.Set(3, 0, 'x', theme.Color{}, LLabel)

	tc := g.ANSI(colorprofile.TrueColor)
	if !strings.Contains(tc, "38;2;43;49;57") || !strings.Contains(tc, ";7;38;2;38;166;154") {
		t.Fatalf("truecolor %q", tc)
	}
	c256 := g.ANSI(colorprofile.ANSI256)
	if !strings.Contains(c256, "38;5;238") || strings.Contains(c256, "38;5;23m") {
		t.Fatalf("256: axis gray must be 238, not teal 23: %q", c256)
	}
	c16 := g.ANSI(colorprofile.ANSI)
	if !strings.Contains(c16, "\x1b[0;90m│") || strings.Contains(c16, "\x1b[0;36m") {
		t.Fatalf("16: axis gray must be 8 (90), not cyan 6: %q", c16)
	}
	if strings.ContainsRune(c16, '┈') {
		t.Fatalf("16: gridlines are omitted: %q", c16)
	}
	for _, p := range []colorprofile.Profile{colorprofile.ASCII, colorprofile.NoTTY} {
		out := g.ANSI(p)
		if strings.Contains(out, "38;") || !strings.Contains(out, "\x1b[0;7m▅") {
			t.Fatalf("%v keeps SGR 7 but no color: %q", p, out)
		}
	}
	if ansi.Strip(tc) != "│┈▅x" || g.Plain() != "│┈▅x" {
		t.Fatalf("plain %q / %q", ansi.Strip(tc), g.Plain())
	}
	// An automatically derived 256/16 color (a tint) still resolves.
	tinted := tint(th, th.Up, 0.42)
	if s := resolveSGR(tinted, colorprofile.ANSI256); !strings.HasPrefix(s.code, "38;5;") {
		t.Fatalf("tint 256 %q", s.code)
	}
	if s := resolveSGR(tinted, colorprofile.ANSI); s.code != "32" {
		t.Fatalf("tint 16 keeps the base hue: %q", s.code)
	}
	if s := resolveSGR(theme.Color{True: theme.Hex("#ffffff"), C256: -1, C16: 12}, colorprofile.ANSI); s.code != "94" || s.bgCode != "104" {
		t.Fatalf("bright 16 %+v", s)
	}
}

func TestGridHelpers(t *testing.T) {
	g := NewGrid(5, 2)
	if g.At(5, 0) != nil || g.At(-1, 0) != nil {
		t.Fatal("bounds")
	}
	g.Set(0, 0, 'a', theme.Color{}, LLabel)
	if g.Set(0, 0, 'b', theme.Color{}, LGrid) != nil || g.At(0, 0).Ch != 'a' {
		t.Fatal("lower layer must not overwrite")
	}
	g.TintBG(1, 0, theme.C("#123456", 1, 1))
	g.Set(1, 0, 'c', theme.Color{}, LLabel)
	if g.At(1, 0).BG.C256 != 1 {
		t.Fatal("Set must keep the background tint")
	}
	if n := g.Text(2, 1, "hey", theme.Color{}, LLabel); n != 3 {
		t.Fatal("Text count")
	}
	big := NewGrid(7, 3)
	big.Blit(g, 1, 1)
	if big.At(1, 1).Ch != 'a' || big.At(3, 2).Ch != 'h' {
		t.Fatalf("Blit\n%s", big.Plain())
	}
	if l := big.Lines(); len(l) != 3 {
		t.Fatal("Lines")
	}
}

func TestCandlesEdgeCases(t *testing.T) {
	th := hlpTheme(t)
	g, vp := Candles(nil, CandleOpts{Width: 50, Height: 14, Coin: "BTC"}, th)
	if vp.Count != 0 || !strings.Contains(g.Plain(), "no candles") {
		t.Fatal("empty input")
	}
	cs := loadBTC(t)
	g, _ = Candles(cs, CandleOpts{Width: 38, Height: 6, Coin: "BTC"}, th)
	if s := g.Plain(); !strings.Contains(s, "BTC ") || !strings.Contains(s, "65,462") || !strings.ContainsAny(s, "▁▂▃▄▅▆▇█") {
		t.Fatalf("tiny frame should be a sparkline:\n%s", s)
	}
	// A flat series and a single candle must not panic or divide by zero.
	flat := []domain.Candle{{T: 0, O: 1, H: 1, L: 1, C: 1, V: 0}}
	g, vp = Candles(flat, CandleOpts{Width: 60, Height: 20, VolRows: -1, Cursor: 0, Header: true}, th)
	assertWidths(t, "flat", g.ANSI(colorprofile.TrueColor), 60, 20)
	if vp.Count != 1 {
		t.Fatal("single candle")
	}
	// Heikin-Ashi readout keeps real prices.
	g, _ = Candles(cs, CandleOpts{Width: 90, Height: 20, Style: Heikin, Header: true, Cursor: -1, Coin: "BTC"}, th)
	if !strings.Contains(g.Lines()[0], "C 65,462") {
		t.Fatalf("heikin header %q", g.Lines()[0])
	}
}

func TestCandlesHysteresis(t *testing.T) {
	cs := loadBTC(t)
	th := hlpTheme(t)
	o := CandleOpts{Width: 100, Height: 30, VolRows: -1, Cursor: -1}
	_, vp := Candles(cs, o, th)
	o.Range = [2]float64{vp.Lo - 50, vp.Hi + 50}
	_, kept := Candles(cs, o, th)
	if kept.Lo != o.Range[0] || kept.Hi != o.Range[1] {
		t.Fatalf("range should be kept: %v..%v", kept.Lo, kept.Hi)
	}
	o.Range = [2]float64{vp.Lo - 5000, vp.Hi + 5000} // data fills too little of it
	_, re := Candles(cs, o, th)
	if re.Lo != vp.Lo || re.Hi != vp.Hi {
		t.Fatal("range should rescale when the data fills < 70% of it")
	}
	o.Range = [2]float64{vp.Lo + 1000, vp.Hi} // data escapes it
	_, re = Candles(cs, o, th)
	if re.Lo != vp.Lo {
		t.Fatal("range should rescale when price escapes")
	}
}

func TestCountdownAndLive(t *testing.T) {
	cs := loadBTC(t)
	th := hlpTheme(t)
	last := time.UnixMilli(cs[len(cs)-1].T)
	o := CandleOpts{Width: 100, Height: 30, VolRows: -1, Cursor: -1, Header: true, Interval: domain.I15m, Now: last.Add(5*time.Minute + 39*time.Second)}
	g, _ := Candles(cs, o, th)
	if !strings.Contains(g.Plain(), " 09:21") {
		t.Fatal("countdown missing")
	}
	o.Now = last.Add(20 * time.Minute) // bar already closed
	g, _ = Candles(cs, o, th)
	if strings.Contains(g.Plain(), "09:21") {
		t.Fatal("countdown after close")
	}
	o.Offset = 5
	g, _ = Candles(cs, o, th)
	if !strings.Contains(g.Lines()[0], "▶ live") {
		t.Fatalf("live hint missing: %q", g.Lines()[0])
	}
}

func TestLineChart(t *testing.T) {
	th := hlpTheme(t)
	nav, btc := syntheticSeries(120, 1, 0.01, 3), syntheticSeries(120, 60000, 0.02, 9)
	for _, o := range []LineOpts{
		{Width: 80, Height: 16, Title: "NAV", Area: true, Baseline: 1, Series: []Named{{Name: "vault", Series: nav}}},
		{Width: 80, Height: 16, Percent: true, Baseline: math.NaN(), Series: []Named{{Name: "vault", Series: nav}, {Name: "BTC", Series: btc, Color: 2}}},
		{Width: 30, Height: 5, Baseline: math.NaN(), Series: []Named{{Name: "x", Series: nav[:2]}}},
	} {
		g := LineChart(o, th)
		assertWidths(t, "line", g.ANSI(colorprofile.TrueColor), o.Width, o.Height)
		if !strings.ContainsAny(g.Plain(), "⠁⠂⠄⡀⠈⠐⠠⢀⣀⠉⠒⠤") {
			t.Fatalf("no braille:\n%s", g.Plain())
		}
	}
	if s := LineChart(LineOpts{Width: 40, Height: 8, Title: "NAV"}, th).Plain(); !strings.Contains(s, "no data") {
		t.Fatal("empty line chart")
	}
	g := LineChart(LineOpts{Width: 80, Height: 16, Percent: true, Baseline: math.NaN(), Series: []Named{{Name: "vault", Series: nav}, {Name: "BTC", Series: btc, Color: 2}}}, th)
	golden(t, "line-percent-80x16", g.Plain())
}

func TestHeatmap(t *testing.T) {
	th := hlpTheme(t)
	g := Heatmap(syntheticMonths(), 100, th)
	if g.H != 3 {
		t.Fatalf("rows %d", g.H)
	}
	s := g.Plain()
	if !strings.Contains(s, "YTD") || !strings.Contains(s, "+11.2") || !strings.Contains(s, "+26.8") || !strings.Contains(s, "·") {
		t.Fatalf("heatmap:\n%s", s)
	}
	assertWidths(t, "heatmap", g.ANSI(colorprofile.ANSI), 100, 3)
	golden(t, "heatmap-100", s)
	if narrow := Heatmap(syntheticMonths(), 50, th).Plain(); !strings.Contains(narrow, "██") {
		t.Fatalf("narrow heatmap:\n%s", narrow)
	}
	if Heatmap(nil, 40, th).H != 1 {
		t.Fatal("empty heatmap")
	}
}

func TestBars(t *testing.T) {
	th := hlpTheme(t)
	g := Bars([]string{"BTC", "ETH", "HYPE"}, []float64{4e6, 1e6, -2e6}, 60, th)
	s := g.Plain()
	if !strings.Contains(s, "+4.00M") || !strings.Contains(s, "-2.00M") || !strings.Contains(s, "│") {
		t.Fatalf("bars:\n%s", s)
	}
	assertWidths(t, "bars", g.ANSI(colorprofile.TrueColor), 60, 3)
	golden(t, "bars-60", s)
	pos := BarsWith([]string{"a", "b"}, []float64{2, 1}, BarsOpts{Width: 30, Format: func(v float64) string { return FormatPercent(v/10, 0) }}, th)
	if ps := pos.Plain(); strings.Contains(ps, "│") || !strings.Contains(ps, "+20%") {
		t.Fatalf("positive-only bars:\n%s", ps)
	}
	if r, rev := rightBlock(3); r != '▋' || !rev {
		t.Fatal("rightBlock reverse trick")
	}
}

func TestDepth(t *testing.T) {
	th := hlpTheme(t)
	b := loadBook(t)
	g := Depth(b, DepthOpts{Width: 70, Height: 23, Ladder: true, Decimals: -1}, th)
	s := g.Plain()
	if !strings.Contains(s, "65,370.5") || !strings.Contains(s, "spread 1") || !strings.Contains(s, "65,371") {
		t.Fatalf("ladder:\n%s", s)
	}
	lines := g.Lines()
	if !strings.Contains(lines[10], "65,371") || !strings.Contains(lines[12], "65,370") {
		t.Fatalf("best ask must sit right above the spread row, best bid right below:\n%s", s)
	}
	assertWidths(t, "depth", g.ANSI(colorprofile.TrueColor), 70, 23)
	golden(t, "depth-ladder-70x23", s)
	curve := Depth(b, DepthOpts{Width: 60, Height: 10, Decimals: -1, SzUSD: true}, th)
	if cs := curve.Plain(); !strings.Contains(cs, "$") || !strings.Contains(cs, "█") {
		t.Fatalf("curve:\n%s", cs)
	}
	usd := Depth(b, DepthOpts{Width: 40, Height: 12, Ladder: true, SzUSD: true, Decimals: 0}, th)
	assertWidths(t, "depth-usd", usd.ANSI(colorprofile.ANSI256), 40, 12)
	if !strings.Contains(Depth(domain.Book{}, DepthOpts{Width: 20, Height: 3}, th).Plain(), "no book") {
		t.Fatal("empty book")
	}
	if bookDecimals(b) != 0 {
		t.Fatalf("BTC book decimals %d", bookDecimals(b))
	}
}

func BenchmarkCandles(b *testing.B) {
	cs := make([]domain.Candle, 500)
	p := 100.0
	for i := range cs {
		cs[i] = domain.Candle{T: int64(i) * 60000, O: p, H: p + 2, L: p - 2, C: p + float64(i%5-2), V: float64(i % 17)}
		p = cs[i].C
	}
	th, _ := theme.Get("hlp")
	o := CandleOpts{Width: 200, Height: 60, VolRows: -1, MAs: []int{20, 50}, Cursor: -1, Header: true, Coin: "X", Interval: domain.I1m}
	b.ReportAllocs()
	for b.Loop() {
		g, _ := Candles(cs, o, th)
		_ = g.ANSI(colorprofile.TrueColor)
	}
}

// A 0.6-cell body split 0.3/0.3 across a cell boundary with wicks past both
// ends must still draw a body glyph, never a bare wick.
func TestDrawBlocksNeverLosesBody(t *testing.T) {
	const plotH = 4
	g := NewGrid(3, plotH)
	cp := &candlePainter{g: g, style: Blocks, bodyW: 3, plotH: plotH,
		units: func(p float64, per int) int { return int(math.Round(p * float64(per))) },
		rowY:  func(r int) int { return plotH - 1 - r },
		th:    theme.Default(true)}
	// rows are 1 price unit; body 1.625..2.375 straddles the 2.0 boundary
	cp.draw(0, domain.Candle{O: 2.375, C: 1.625, H: 3.5, L: 0.5}, cp.th.Down)
	var center, sides []rune
	for y := 0; y < plotH; y++ {
		center = append(center, g.At(1, y).Ch)
		sides = append(sides, g.At(0, y).Ch)
	}
	if !strings.ContainsRune(string(center), '┿') || !strings.ContainsRune(string(sides), '━') {
		t.Fatalf("body lost: center %q sides %q", string(center), string(sides))
	}
}

func TestHeatmapZeroIsNeutral(t *testing.T) {
	th := theme.Default(true)
	g := Heatmap([]domain.MonthlyReturn{{Year: 2026, Month: 6, Ret: 0.0002}, {Year: 2026, Month: 7, Ret: -0.0003}, {Year: 2026, Month: 8, Ret: 0.05}}, 100, th)
	var row []rune
	for x := 0; x < g.W; x++ {
		row = append(row, g.At(x, 1).Ch)
	}
	line := string(row)
	zeros := 0
	for i := 0; i+4 <= len(row); i++ {
		if string(row[i:i+4]) != " 0.0" {
			continue
		}
		zeros++
		for x := i + 1; x < i+4; x++ {
			if c := g.At(x, 1); c.BG != (theme.Color{}) || c.FG != th.Dim {
				t.Fatalf("zero month colored at %d: %+v (%q)", x, c, line)
			}
		}
	}
	if zeros != 2 {
		t.Fatalf("expected two neutral 0.0 cells, found %d in %q", zeros, line)
	}
}

func TestVolumeTintKeepsHueAt256(t *testing.T) {
	th := theme.Default(true)
	c := tint(th, th.Down, 0.42)
	if c.C256 < 16 || c.C256 > 231 {
		t.Fatalf("down volume tint at 256 colors: %d (gray or derived)", c.C256)
	}
}
