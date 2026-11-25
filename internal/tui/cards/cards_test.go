package cards

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/maxmoneycash/hlp/internal/chart"
	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/theme"
)

var update = flag.Bool("update", false, "rewrite golden files")

// stubCharts swaps the chart renderers for deterministic placeholders that
// record their inputs, so card goldens test card layout only. The chart
// package has its own goldens.
func stubCharts(t *testing.T) {
	t.Helper()
	oc, ol, os_, od, oh, ob := candlesFn, lineFn, sparkFn, depthFn, heatmapFn, barsFn
	t.Cleanup(func() { candlesFn, lineFn, sparkFn, depthFn, heatmapFn, barsFn = oc, ol, os_, od, oh, ob })
	box := func(w, h int, label string) *chart.Grid {
		g := chart.NewGrid(max(w, 1), max(h, 1))
		for x := 0; x < g.W; x++ {
			g.Set(x, 0, '·', theme.Color{}, chart.LGrid)
			g.Set(x, g.H-1, '·', theme.Color{}, chart.LGrid)
		}
		g.Text(1, min(1, g.H-1), label, theme.Color{}, chart.LLabel)
		return g
	}
	candlesFn = func(cs []domain.Candle, o chart.CandleOpts, _ theme.Theme) (*chart.Grid, chart.Viewport) {
		label := fmt.Sprintf("[candles %s %s n=%d %dx%d zoom=%d mas=%v markers=%d header=%v]", o.Coin, o.Interval, len(cs), o.Width, o.Height, o.Zoom, o.MAs, len(o.Markers), o.Header)
		return box(o.Width, o.Height, label), chart.Viewport{}
	}
	lineFn = func(o chart.LineOpts, _ theme.Theme) *chart.Grid {
		names := make([]string, len(o.Series))
		for i, s := range o.Series {
			names[i] = fmt.Sprintf("%s:%d", s.Name, len(s.Series))
		}
		return box(o.Width, o.Height, fmt.Sprintf("[line %v %dx%d area=%v pct=%v]", names, o.Width, o.Height, o.Area, o.Percent))
	}
	sparkFn = func(vals []float64, width int) string {
		if len(vals) == 0 || width <= 0 {
			return strings.Repeat(" ", max(width, 0))
		}
		lo, hi := vals[0], vals[0]
		for _, v := range vals {
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
		bars := []rune("▁▂▃▄▅▆▇█")
		out := make([]rune, width)
		for i := range out {
			v := vals[min(i*len(vals)/width, len(vals)-1)]
			k := 0
			if hi > lo {
				k = int((v - lo) / (hi - lo) * 7)
			}
			out[i] = bars[k]
		}
		return string(out)
	}
	depthFn = func(b domain.Book, o chart.DepthOpts, _ theme.Theme) *chart.Grid {
		return box(o.Width, o.Height, fmt.Sprintf("[depth %s bids=%d asks=%d levels=%d ladder=%v]", b.Coin, len(b.Bids), len(b.Asks), o.Levels, o.Ladder))
	}
	heatmapFn = func(m []domain.MonthlyReturn, width int, _ theme.Theme) *chart.Grid {
		return box(width, 3, fmt.Sprintf("[heatmap months=%d]", len(m)))
	}
	barsFn = func(labels []string, values []float64, width int, _ theme.Theme) *chart.Grid {
		return box(width, max(len(labels), 2), fmt.Sprintf("[bars %v]", labels))
	}
}

func plainOpts(w int) Opts {
	th, _ := theme.Get("hlp")
	return Opts{Width: w, Theme: th, Profile: colorprofile.TrueColor, Plain: true, Location: time.UTC}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
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
		t.Fatalf("missing golden %s (run go test ./internal/tui/cards -update): %v", path, err)
	}
	if string(want) != got {
		t.Errorf("%s mismatch\n--- want\n%s\n--- got\n%s", path, want, got)
	}
}

func sortedKeys(m map[string]*core.Result) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestGoldenPlain renders every card kind from fixtures (full and compact).
func TestGoldenPlain(t *testing.T) {
	stubCharts(t)
	rs := fixtureResults(t)
	for _, name := range sortedKeys(rs) {
		r := rs[name]
		t.Run(name, func(t *testing.T) {
			golden(t, name, Render(r, plainOpts(100)))
			o := plainOpts(80)
			o.Compact = true
			golden(t, name+"_compact", Render(r, o))
		})
	}
}

func TestPlainHasNoANSI(t *testing.T) {
	stubCharts(t)
	for name, r := range fixtureResults(t) {
		if s := Render(r, plainOpts(100)); strings.Contains(s, "\x1b") {
			t.Errorf("%s: plain output contains escape codes", name)
		}
	}
}

// Every line of every card fits the requested width, in every mode, with
// the real chart renderer and with the stubs.
func TestWidthNeverExceeded(t *testing.T) {
	check := func(t *testing.T, label string) {
		for name, r := range fixtureResults(t) {
			for _, w := range []int{40, 72, 100, 141} {
				for _, compact := range []bool{false, true} {
					for _, prof := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256, colorprofile.ANSI, colorprofile.ASCII} {
						th, _ := theme.Get("hlp")
						s := Render(r, Opts{Width: w, Theme: th, Profile: prof, Compact: compact, Location: time.UTC})
						for i, line := range strings.Split(s, "\n") {
							if lw := lipgloss.Width(line); lw > w {
								t.Fatalf("%s %s w=%d compact=%v profile=%v line %d is %d wide: %q", label, name, w, compact, prof, i, lw, line)
							}
						}
					}
				}
			}
		}
	}
	t.Run("stub", func(t *testing.T) { stubCharts(t); check(t, "stub") })
	t.Run("real", func(t *testing.T) { check(t, "real") })
}

func TestHeightBudget(t *testing.T) {
	stubCharts(t)
	rs := fixtureResults(t)
	for _, name := range []string{"chart", "book", "trades", "markets", "fills"} {
		o := plainOpts(100)
		o.Height = 14
		s := Render(rs[name], o)
		if n := strings.Count(s, "\n") + 1; n > 16 {
			t.Errorf("%s with Height 14 rendered %d lines", name, n)
		}
	}
}

func TestFooterMeta(t *testing.T) {
	stubCharts(t)
	r := fixtureResults(t)["vaults"]
	// Full (CLI, MCP): NOTES with the whole warning, then the info row.
	s := Render(r, plainOpts(100))
	for _, want := range []string{"NOTES", "! hlp.xyz summary for HLP is stale", "SOURCE Hyperliquid + hlp.xyz", "AS OF Nov 15 14:02:11", "PARTIAL"} {
		if !strings.Contains(s, want) {
			t.Errorf("full card missing %q in\n%s", want, s)
		}
	}
	// Inline: no source or as-of, warnings collapse to one gold tag.
	o := plainOpts(100)
	o.Compact = true
	s = Render(r, o)
	if strings.Contains(s, "as of") || strings.Contains(s, "AS OF") || strings.Contains(s, "SOURCE") || strings.Contains(s, "NOTES") {
		t.Errorf("inline card keeps footer text:\n%s", s)
	}
	if !strings.Contains(s, "! 1 NOTE") || strings.Contains(s, "summary for HLP") {
		t.Errorf("inline warning tag:\n%s", s)
	}
	// Info brings NOTES and the info row back inline.
	o.Info = true
	if s := Render(r, o); !strings.Contains(s, "AS OF Nov 15 14:02:11") || !strings.Contains(s, "summary for HLP") {
		t.Errorf("inline Info card:\n%s", s)
	}
	old := nowFn
	t.Cleanup(func() { nowFn = old })
	nowFn = func() time.Time { return fixedAsOf.Add(3 * time.Minute) }
	stale := &core.Result{Card: core.CardKV, Data: [][2]string{{"a", "b"}}, Meta: domain.Meta{Source: "cache", Stale: true, AsOf: fixedAsOf, Network: domain.Testnet}}
	o = plainOpts(60)
	o.Compact = true
	if s := Render(stale, o); !strings.Contains(s, "STALE 3M") {
		t.Errorf("stale tag missing:\n%s", s)
	}
	if s := Render(stale, plainOpts(60)); !strings.Contains(s, "TESTNET") {
		t.Errorf("testnet missing from the info row:\n%s", s)
	}
}

func TestRenderNilAndWrongData(t *testing.T) {
	if Render(nil, Opts{}) != "" {
		t.Error("nil result should render empty")
	}
	// A card whose data has the wrong type falls back to a JSON dump.
	s := Render(&core.Result{Card: core.CardChart, Title: "oops", Data: 42}, plainOpts(60))
	if !strings.Contains(s, "OOPS") || !strings.Contains(s, "42") {
		t.Errorf("fallback render: %q", s)
	}
}

func TestChartCardUsesRealRenderer(t *testing.T) {
	r := fixtureResults(t)["chart"]
	s := Render(r, plainOpts(100))
	if n := strings.Count(s, "\n"); n < chartRowsInline {
		t.Errorf("chart card has %d lines, want at least %d", n, chartRowsInline)
	}
}

func TestSkeletonAndError(t *testing.T) {
	s := Skeleton("chart BTC 1h", "2S", 4, plainOpts(60))
	lines := strings.Split(s, "\n")
	if len(lines) != 5 || !strings.Contains(lines[0], "CHART BTC 1H") || !strings.Contains(lines[0], "2S") {
		t.Errorf("skeleton:\n%s", s)
	}
	// ■ and the message; the hint only as a runnable command; no code.
	ec := plainOpts(60)
	ec.Compact = true
	e := Error(&core.Error{Code: core.CodeUnknownCoin, Message: `unknown coin "FOOBARX"`, Hint: "list markets with `hlp markets --query FOOBARX`"}, ec)
	want := "  ■ Unknown coin FOOBARX\n    /markets --query FOOBARX"
	if e != want {
		t.Errorf("error card:\n%q\nwant\n%q", e, want)
	}
	if strings.Contains(e, "UNKNOWN_COIN") || strings.Contains(e, "list markets") {
		t.Errorf("error card keeps prose or code:\n%s", e)
	}
	if e := Error(&core.Error{Code: core.CodeUnknownCoin, Message: "unknown coin NOPE", Hint: "did you mean NEO?"}, ec); strings.Contains(e, "NEO") {
		t.Errorf("inline error keeps a prose hint:\n%s", e)
	}
	if Error(nil, Opts{}) != "" {
		t.Error("nil error should render empty")
	}
	if s := Error(errors.New("boom"), plainOpts(60)); !strings.Contains(s, "■ Boom") || strings.Contains(s, "ERROR") {
		t.Errorf("plain error: %q", s)
	}
}

func TestMarkdown(t *testing.T) {
	plain := Markdown("# Title\n\nsome **bold** text", plainOpts(40))
	if strings.Join(plain, "\n") != "# Title\n\nsome **bold** text" {
		t.Errorf("plain markdown should keep source: %q", plain)
	}
	th, _ := theme.Get("hlp")
	styled := Markdown("some **bold** text", Opts{Width: 40, Theme: th, Profile: colorprofile.TrueColor})
	if len(styled) == 0 || !strings.Contains(stripANSI(strings.Join(styled, "\n")), "bold") {
		t.Errorf("styled markdown: %q", styled)
	}
}

func TestOrderPreviewBadges(t *testing.T) {
	p := fixtureResults(t)["order"].Data.(*core.OrderPreview)
	s := OrderPreview(*p, plainOpts(80))
	for _, want := range []string{"BUY 2.43 HYPE", "$99.92", "[TESTNET]", "[DRY RUN]", "NOT SENT", "■ Notional under $1,000"} {
		if !strings.Contains(s, want) {
			t.Errorf("order preview missing %q:\n%s", want, s)
		}
	}
	p2 := *p
	p2.Network, p2.DryRun, p2.Envelope = domain.Mainnet, false, nil
	p2.Response = []byte(`{"status":"ok"}`)
	if s := OrderPreview(p2, plainOpts(80)); !strings.Contains(s, "[MAINNET]") || strings.Contains(s, "DRY RUN") || !strings.Contains(s, "SENT") {
		t.Errorf("mainnet sent preview:\n%s", s)
	}
	for _, a := range []string{"cancel", "vault_deposit", "vault_withdraw", "close_all", "leverage"} {
		if orderTitle(core.OrderPreview{Action: a, Coin: "BTC"}) == "" {
			t.Errorf("no title for %s", a)
		}
	}
}

func TestVaultTableSelectionAndSort(t *testing.T) {
	stubCharts(t)
	vs := fixtureVaults(t)
	SortVaults(vs, "tvl", true)
	if vs[0].Name != "Hyperliquidity Provider (HLP)" {
		t.Errorf("tvl desc first = %s", vs[0].Name)
	}
	SortVaults(vs, "maxdd", true)
	for i := 1; i < len(vs); i++ {
		if vs[i-1].MaxDD < vs[i].MaxDD {
			t.Fatalf("maxdd desc not shallowest-first: %v then %v", vs[i-1].MaxDD, vs[i].MaxDD)
		}
	}
	SortVaults(vs, "tvl", true)
	s := VaultTable(vs, plainOpts(120), VaultTableOpts{Selected: 1, Sort: "tvl", Desc: true, Rows: 4})
	lines := strings.Split(s, "\n")
	if len(lines) != 5 {
		t.Fatalf("want header + 4 rows, got %d:\n%s", len(lines), s)
	}
	if !strings.Contains(lines[0], "TVL ▾") {
		t.Errorf("sort marker missing: %q", lines[0])
	}
	if !strings.HasPrefix(strings.TrimLeft(lines[2], " "), ">") {
		t.Errorf("selected row not marked: %q", lines[2])
	}
	for _, k := range VaultSortKeys {
		_ = VaultSortValue(vs[0], k)
	}
}

func TestVaultCapacity(t *testing.T) {
	cases := []struct {
		v    domain.VaultSummary
		want float64
	}{
		{domain.VaultSummary{TVL: 1e6, LeaderFraction: 0.10}, 1e6},
		{domain.VaultSummary{TVL: 1e6, LeaderFraction: 0.04}, 0},
		{domain.VaultSummary{TVL: 1e6, LeaderFraction: 0.10, Rel: "parent"}, -1},
	}
	for _, c := range cases {
		if got := VaultCapacity(c.v); math.Abs(got-c.want) > 1e-6 {
			t.Errorf("capacity(%+v) = %v, want %v", c.v, got, c.want)
		}
	}
	if !math.IsNaN(VaultCapacity(domain.VaultSummary{TVL: 1e6})) {
		t.Error("unknown leader fraction should be NaN")
	}
	if capacityText(-1) != "NO CAP" || capacityText(0) != "FULL" || capacityText(math.NaN()) != dash {
		t.Error("capacity text")
	}
}

func TestFormat(t *testing.T) {
	cases := []struct{ got, want string }{
		{USD(950), "$950"}, {USD(12.5), "$12.50"},
		{USD(1234), "$1,234"},
		{USD(12_400), "$12.4K"},
		{USD(151_305_702), "$151.3M"},
		{USD(1.21e9), "$1.21B"},
		{USD(-3400), "−$3,400"},
		{USD(math.NaN()), dash},
		{SignedUSD(12), "+$12.00"},
		{Pct(0.1234, 1), "12.3%"},
		{SignedPct(0.1234, 1), "+12.3%"},
		{SignedPct(-0.04, 1), "−4.0%"},
		{Chg(0.24), "+24.0%"}, {Chg(-0.009), "−0.90%"}, {Chg(0.0004), "+0.04%"},
		{SignedPct(-0.00001, 1), "0.0%"},
		{Px(97412.3), "97,412"},
		{Px(3412.55), "3,412.6"},
		{Px(71.76), "71.76"}, {Px(65392), "65,392"},
		{Px(3.1483), "3.1483"},
		{Px(0.000012345), "0.00001234"},
		{Px(0), dash},
		{Size(11.45136), "11.45136"},
		{Size(1500.5), "1,500.5"},
		{Size(2), "2"},
		{Num(2.214, 2), "2.21"},
		{Thousands(-1234567.891, 2), "−1,234,567.89"},
		{ShortAddr("0x469f690213c467c39a23efacfd2816896009d7d8"), "0x469f…d7d8"},
		{ShortAddr("0xabc"), "0xabc"},
		{Age(12), "12D"},
		{Age(200), "6MO"},
		{Age(500), "1.4Y"},
		{Ago(45 * time.Second), "45S"}, {Ago(3 * time.Minute), "3M"}, {Ago(5 * time.Hour), "5H"}, {Ago(72 * time.Hour), "3D"},
		{Age(0), dash},
		{Clock(1794735351103, time.UTC), "09:35:51"},
		{Stamp(fixedAsOf, nil), "Nov 15 14:02:11"},
	}
	for i, c := range cases {
		if c.got != c.want {
			t.Errorf("case %d: got %q want %q", i, c.got, c.want)
		}
	}
	if PxDecimals(97412) != 0 || PxDecimals(3.1483) != 4 || PxDecimals(1e-12) != 8 {
		t.Error("PxDecimals")
	}
}

func TestSparkAndMeter(t *testing.T) {
	stubCharts(t)
	if s := spark([]float64{1, 2, 3, math.NaN()}, 6); lipgloss.Width(s) != 6 {
		t.Errorf("spark width %d", lipgloss.Width(s))
	}
	if s := spark(nil, 4); s != "    " {
		t.Errorf("empty spark %q", s)
	}
	if meter(1, 3) != "███" || meter(0, 3) != "" || meter(0.5, 1) != "▌" {
		t.Errorf("meter: %q %q %q", meter(1, 3), meter(0, 3), meter(0.5, 1))
	}
}

func TestTableDropsLowPriorityColumns(t *testing.T) {
	p := newPainter(plainOpts(100).normalized())
	cols := []column{{title: "name", flex: true, minW: 4, keep: 10}, {title: "keep", right: true, keep: 60}, {title: "dropme", keep: 1}}
	rows := [][]cell{{p.c("a long vault name"), p.c("1"), p.c("zzzzzz")}}
	out := p.table(cols, rows, 20, nil)
	if strings.Contains(out[0], "DROPME") || !strings.Contains(out[0], "KEEP") {
		t.Errorf("narrow table: %q", out)
	}
	for _, l := range out {
		if lipgloss.Width(l) > 20 {
			t.Errorf("table line too wide: %q", l)
		}
	}
	kv := p.kvLines([][2]string{{"a", "1"}, {"longer key", "2"}}, 40)
	if len(kv) != 2 || !strings.HasPrefix(kv[1], "LONGER KEY  2") {
		t.Errorf("kv: %q", kv)
	}
}

func TestSideBySideAndWrap(t *testing.T) {
	out := sideBySide([]string{"ab", "c"}, []string{"x", "y", "z"}, 4, 2)
	if out[0] != "ab    x" || out[2] != "      z" {
		t.Errorf("sideBySide: %q", out)
	}
	if w := wrapLines([]string{strings.Repeat("word ", 20)}, 20); len(w) < 4 {
		t.Errorf("wrap: %q", w)
	}
}

func TestFillMarkers(t *testing.T) {
	cs := fixtureCandles(t)
	ms := fillMarkers(cs, []domain.Fill{
		{Coin: "BTC", Side: domain.Buy, Time: cs[3].T + 1},
		{Coin: "ETH", Side: domain.Buy, Time: cs[3].T + 1},
		{Coin: "BTC", Side: domain.Sell, Time: cs[0].T - 1},
	}, "BTC")
	if len(ms) != 1 || ms[0].T != cs[3].T || !ms[0].Buy {
		t.Errorf("markers: %+v", ms)
	}
}
