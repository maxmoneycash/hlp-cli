package cards

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/maxmoneycash/hlp/internal/chart"
	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/metrics"
	"github.com/maxmoneycash/hlp/internal/theme"
	"github.com/maxmoneycash/hlp/internal/tui/glyph"
)

// VaultSortKeys are the screener sort keys, in the order the TUI cycles them
// (they match core.VaultsInput.Sort).
var VaultSortKeys = []string{"tvl", "apr", "ret7d", "ret30d", "retall", "sharpe", "maxdd", "age"}

// VaultSortValue returns the numeric value a sort key compares.
func VaultSortValue(v domain.VaultSummary, key string) float64 {
	switch key {
	case "apr":
		return v.APR
	case "ret7d":
		return v.Returns[domain.W1W]
	case "ret30d":
		return v.Returns[domain.W1M]
	case "retall":
		return v.Returns[domain.WAll]
	case "sharpe":
		return v.Sharpe
	case "maxdd":
		return v.MaxDD // negative: "desc" puts the shallowest drawdown first
	case "age":
		return float64(v.AgeDays)
	}
	return v.TVL
}

// SortVaults sorts in place by key (desc = biggest first); ties keep TVL order.
func SortVaults(vs []domain.VaultSummary, key string, desc bool) {
	sort.SliceStable(vs, func(i, j int) bool {
		a, b := VaultSortValue(vs[i], key), VaultSortValue(vs[j], key)
		if a == b {
			return vs[i].TVL > vs[j].TVL
		}
		if desc {
			return a > b
		}
		return a < b
	})
}

// VaultCapacity is the deposit room left under the leader 5% rule (the
// leader must keep >= 5% of the vault): leaderEquity/0.05 - TVL. -1 means
// unlimited (HLP family); NaN means unknown. A source-provided
// v.CapacityUSD wins; otherwise this mirrors metrics.Capacity.
func VaultCapacity(v domain.VaultSummary) float64 {
	if v.CapacityUSD != 0 {
		return v.CapacityUSD
	}
	if isHLP(v) {
		return -1
	}
	if v.LeaderFraction <= 0 || v.TVL <= 0 {
		return math.NaN()
	}
	return math.Max(0, v.LeaderFraction*v.TVL/0.05-v.TVL)
}

func isHLP(v domain.VaultSummary) bool {
	return v.Rel == "parent" || v.Rel == "child" || strings.HasPrefix(v.Name, "Hyperliquidity Provider")
}

func capacityText(c float64) string {
	switch {
	case math.IsNaN(c):
		return dash
	case c < 0:
		return "NO CAP"
	case c == 0:
		return "FULL"
	}
	return USD(c)
}

// VaultTableOpts drive the screener table in the vaults pane.
type VaultTableOpts struct {
	Selected int    // highlighted row (-1 = none)
	Sort     string // active sort key, marked in its header
	Desc     bool
	Offset   int // first row shown (scrolling)
	Rows     int // rows shown (0 = all)
}

// bySharpe reports whether a vault list is ranked by Sharpe (descending)
// rather than by TVL, the default: the card then shows the SHARPE column.
func bySharpe(vs []domain.VaultSummary) bool {
	if len(vs) < 3 {
		return false
	}
	tvlOrder, n := true, 0
	for i := 1; i < len(vs); i++ {
		if vs[i].Sharpe > vs[i-1].Sharpe {
			return false
		}
		if vs[i].TVL > vs[i-1].TVL {
			tvlOrder = false
		}
		if vs[i].Sharpe != 0 {
			n++
		}
	}
	return !tvlOrder && n > 0
}

// VaultTable renders the screener at o.Width: a gutter mark for curated
// vaults (or the selection bar), then name, TVL, returns, risk, age,
// capacity and a sparkline, dropping low-priority columns when narrow.
func VaultTable(vs []domain.VaultSummary, o Opts, t VaultTableOpts) string {
	o = o.normalized()
	p := newPainter(o)
	return p.indent(p.vaultRows(vs, t))
}

func (p *painter) vaultRows(vs []domain.VaultSummary, t VaultTableOpts) []string {
	end := len(vs)
	if t.Rows > 0 {
		end = min(len(vs), t.Offset+t.Rows)
	}
	start := min(max(t.Offset, 0), end)
	view := vs[start:end]
	sparkW := 10
	rows := make([][]cell, 0, len(view))
	for i, v := range view {
		vn := screenerName(v.Name)
		// Curated vaults read in ink, the rest muted (no • marks).
		name := p.cm(vn)
		if v.Curated {
			name = p.c(vn)
		}
		if t.Selected >= 0 && start+i == t.Selected {
			name = p.cst(vn, p.st.bold)
		}
		if v.Closed {
			name = p.cst(vn+" "+glyph.Off, p.st.dim)
		} else if v.Stale {
			name = p.cst(vn+" "+glyph.Note, p.st.warn)
		}
		// Red marks a loss only in the column the table is sorted by
		// (DESIGN.md 2 rule 3): elsewhere a negative is ink with its −
		// sign, so 8 columns of losses do not turn the pane into a wall.
		signed := func(x float64, key string) cell {
			if key == t.Sort {
				return p.cs(SignedPct(x, 1), x)
			}
			return p.c(SignedPct(x, 1))
		}
		ret := func(w domain.Window) cell {
			x, ok := v.Returns[w]
			if !ok {
				return p.cm(dash)
			}
			return signed(x, retKey[w])
		}
		dd := p.cm(dash)
		if v.MaxDD != 0 {
			dd = p.cst(SignedPct(v.MaxDD, 1), p.ddStyle(v.MaxDD))
		}
		sh := p.cm(dash)
		if v.Sharpe != 0 {
			sh = p.c(Num(v.Sharpe, 2))
		}
		apr := p.cm(dash)
		if v.APR != 0 {
			apr = signed(v.APR, "apr")
		}
		sp := p.cm(strings.Repeat(" ", sparkW))
		if vals := v.Spark.Values(); len(vals) > 1 {
			// A 1-row braille line in Muted (ink on the selected row): the
			// trend's shape without a block slab heavier than the numbers.
			st := p.st.muted
			if t.Selected >= 0 && start+i == t.Selected {
				st = p.st.text
			}
			sp = p.cst(lineSpark(vals, sparkW), st)
		}
		if p.o.Compact {
			// inline: VAULT TVL APR 30D MAXDD TREND; the pane has the rest.
			// Ranked by Sharpe, SHARPE takes the 30D slot: the answer
			// to the question is in the card.
			mid := ret(domain.W1M)
			if t.Sort == "sharpe" {
				mid = sh
			}
			rows = append(rows, []cell{name, p.c(USD(v.TVL)), apr, mid, dd, sp})
			continue
		}
		rows = append(rows, []cell{
			name, p.c(USD(v.TVL)), apr, ret(domain.W1W), ret(domain.W1M), ret(domain.WAll),
			dd, sh, p.cm(Age(v.AgeDays)), p.cm(capacityText(VaultCapacity(v))), sp,
		})
	}
	title := func(label, key string, right bool) column {
		if t.Sort == key {
			if t.Desc {
				label += " ▾"
			} else {
				label += " ▴"
			}
		}
		return column{title: label, right: right}
	}
	cols := []column{
		{title: "vault", flex: true, minW: 12, keep: 100},
		title("TVL", "tvl", true), title("APR", "apr", true), title("7d", "ret7d", true),
		title("30d", "ret30d", true), title("all", "retall", true), title("maxdd", "maxdd", true),
		title("sharpe", "sharpe", true), title("age", "age", true), {title: "room", right: true}, {title: "trend"},
	}
	keeps := []int{100, 90, 40, 50, 70, 80, 75, 60, 30, 20, 65}
	for i := range cols {
		cols[i].keep = keeps[i]
		if i == 0 {
			// Names stay readable (about 22 cells) before they shrink:
			// at 80 columns the 7d return or the trend goes first.
			cols[i].flex, cols[i].minW, cols[i].mid = true, 22, true
		}
	}
	if p.o.Compact {
		mid := cols[4]
		if t.Sort == "sharpe" {
			mid = cols[7]
		}
		cols = []column{cols[0], cols[1], cols[2], mid, cols[6], cols[10]}
		cols[3].keep, cols[5].keep = 85, 80
		return p.table(cols, rows, p.w, nil)
	}
	// The room column is noise unless at least half the rows have a finite
	// value (HLP's NO CAP alone would keep a column of dashes).
	known := 0
	for _, v := range view {
		if c := VaultCapacity(v); !math.IsNaN(c) && c >= 0 {
			known++
		}
	}
	if known*2 < len(view) {
		const room = 9
		cols = append(cols[:room:room], cols[room+1:]...)
		for i := range rows {
			rows[i] = append(rows[i][:room:room], rows[i][room+1:]...)
		}
	}
	var mark func(i int) string
	if t.Selected >= 0 {
		p.band = func(i int) bool { return start+i == t.Selected }
		defer func() { p.band = nil }()
		mark = func(i int) string {
			if start+i == t.Selected {
				if p.o.Plain {
					return ">"
				}
				return p.st.accent.Render(glyph.Sel)
			}
			return " "
		}
	}
	return p.table(cols, rows, p.w, mark)
}

// retKey is the sort key of each return window's column.
var retKey = map[domain.Window]string{domain.W1W: "ret7d", domain.W1M: "ret30d", domain.WAll: "retall"}

// ddCap is the drawdown past which max DD turns red (DESIGN.md section 2
// rule 3: a drawdown is negative by definition, so it is ink until it is
// worse than the cap).
const ddCap = -0.30

// ddStyle colours a drawdown: ink, red only past the cap.
func (p *painter) ddStyle(dd float64) lipgloss.Style {
	if dd < ddCap {
		return p.st.down
	}
	return p.st.text
}

// vaultsCard: the screener. Focal: how many vaults and their combined TVL.
func (p *painter) vaultsCard(r *core.Result) card {
	vs, ok := as[[]domain.VaultSummary](r.Data)
	if !ok {
		return p.fallbackCard(r)
	}
	c := card{title: "Vaults"}
	tvl := 0.0
	for _, v := range vs {
		tvl += v.TVL
	}
	if len(vs) == 0 {
		c.focal, c.focalStyle = "NO MATCH", p.st.muted
		return c
	}
	c.focal, c.focalStyle, c.numeric = USD(tvl), p.st.bold, true
	if p.o.Compact && !p.o.Plain {
		// inline the total leads as big digits labelled TVL; the count
		// moves up beside the title
		c.sub, c.deltaLabel = strconv.Itoa(len(vs)), "TVL"
	} else {
		c.delta, c.deltaStyle = strconv.Itoa(len(vs)), p.st.muted
	}
	n := min(len(vs), p.rowLimit(25))
	t := VaultTableOpts{Selected: -1, Rows: n}
	if bySharpe(vs) {
		t.Sort, t.Desc = "sharpe", true
	}
	c.body = p.vaultRows(vs, t)
	c.body = p.more(c.body, len(vs), n)
	return c
}

// screenerName moves a bracketed operator prefix to the end as a short tag
// ("[ Systemic Strategies ] HyperGrowth" -> "HyperGrowth [Systemic]"), so
// the part that tells rows apart leads.
func screenerName(s string) string {
	// Decorative marks in names (♾️ HyperGrowth ♾️) are noise in a column
	// of names: drop them and the spaces they leave.
	if strings.ContainsAny(s, "∞♾") {
		s = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
			switch r {
			case '∞', '♾', '\ufe0f', '\ufe0e', '\u200d':
				return -1
			}
			return r
		}, s)), " ")
	}
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "[") {
		return s
	}
	end := strings.Index(t, "]")
	if end < 0 {
		return s
	}
	tag := strings.Fields(t[1:end])
	rest := strings.TrimLeft(strings.TrimSpace(t[end+1:]), "-–|: ")
	if len(tag) == 0 || rest == "" {
		return s
	}
	return rest + " [" + tag[0] + "]"
}

// seriesColor maps a line-chart color index to its theme token
// (0 = ink, 1..3 = MA[0..2]): the subject is never orange (DESIGN.md
// section 2 rule 2).
func seriesColor(th theme.Theme, i int) theme.Color {
	if i <= 0 {
		return th.Text
	}
	return th.MA[(i-1)%3]
}

// identityColor gives each well-known series one color on every card
// (tearsheet, compare, backtest): HLP, BTC and HYPE keep their hue
// wherever they appear, the subject takes the accent, anything else is a
// quiet neutral. The subject is ink: orange marks one thing at a time and
// a curve is never it.
func identityColor(th theme.Theme, name string, subject bool) theme.Color {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "HLP":
		return th.MA[1]
	case "BTC":
		return th.MA[0]
	case "HYPE":
		return th.MA[2]
	}
	if subject {
		return th.Text
	}
	return th.AxisText
}

// legendName is a series' name in a legend or column head: the ticker in
// parentheses when the name has one (Hyperliquidity Provider (HLP) -> HLP),
// else the name cut to 16 cells.
func legendName(s string) string {
	if i, j := strings.LastIndex(s, "("), strings.LastIndex(s, ")"); i >= 0 && j > i+1 && j-i <= 9 {
		if t := strings.TrimSpace(s[i+1 : j]); t != "" && !strings.Contains(t, " ") {
			return t
		}
	}
	return shortName(s)
}

// shortName trims a series name for a chart legend.
func shortName(s string) string {
	if r := []rune(s); len(r) > 16 {
		return string(r[:15]) + "…"
	}
	return s
}

// curveBlock draws the subject curve against benchmarks as % change, with
// the chart's own legend. The area fills between the curve and 0%.
func (p *painter) curveBlock(subject string, curve domain.Series, benches []domain.Benchmark, h int) []string {
	return p.curve(subject, curve, benches, h, false)
}

// curve is curveBlock; others names only the benchmarks in the legend.
func (p *painter) curve(subject string, curve domain.Series, benches []domain.Benchmark, h int, others bool) []string {
	if len(curve) < 2 {
		return []string{p.lbl("no history")}
	}
	series := []chart.Named{{Name: legendName(subject), Series: curve, Col: identityColor(p.th, subject, true)}}
	// The y-range belongs to the vault and HLP: a benchmark that moved more
	// than 2x as far (HYPE +436% beside a +78% vault) would flatten the
	// subject into the bottom of the chart, so it stays in the metrics
	// table and leaves the overlay with a note.
	scale := swing(curve)
	for _, b := range benches {
		if strings.EqualFold(b.Name, "HLP") {
			scale = math.Max(scale, swing(b.Curve))
		}
	}
	var off []string
	for i, b := range benches {
		if i == 3 || len(b.Curve) < 2 {
			continue
		}
		if sw := swing(b.Curve); !strings.EqualFold(b.Name, "HLP") && sw > 2*math.Max(scale, 0.05) {
			// The end-of-window return, the same number the metrics table
			// shows (the peak excursion would contradict it).
			ret := b.Metrics.TotalReturn
			if ret == 0 {
				ret = b.Curve[len(b.Curve)-1].V - 1
			}
			off = append(off, b.Name+" "+SignedPct(ret, 0))
			continue
		}
		series = append(series, chart.Named{Name: b.Name, Series: b.Curve, Col: identityColor(p.th, b.Name, false)})
	}
	// A line only: a cell-quantized area wash reads as stepped gray slabs,
	// not a curve (CRITIQUE.md, equity charts).
	g := lineFn(chart.LineOpts{Width: p.w, Height: h, Series: series, Percent: true,
		Baseline: 1, Location: p.o.Location, LegendOthers: others}, p.th)
	out := p.gridLines(g) // the line chart draws its own legend
	if len(off) > 0 && !p.o.Compact {
		note := p.lbl("off scale") + " " + p.st.muted.Render(strings.Join(off, "   "))
		out = append(out, ansi.Truncate(note, p.w, "…"))
	}
	return out
}

// swing is the largest distance of a normalized curve from 1.0.
func swing(s domain.Series) float64 {
	return math.Abs(farthest(s) - 1)
}

// farthest is the curve value farthest from 1.0.
func farthest(s domain.Series) float64 {
	best := 1.0
	for _, pt := range s {
		if math.Abs(pt.V-1) > math.Abs(best-1) {
			best = pt.V
		}
	}
	return best
}

// drawdownBlock renders the underwater curve as a filled % chart.
func (p *painter) drawdownBlock(dd domain.Series, h int) []string {
	if len(dd) < 2 {
		return nil
	}
	// Red only underwater: points at (or within a hair of) 0% break the
	// line, so the Dim baseline shows along the flat stretches instead of
	// a red dotted run. A hair is 3% of the deepest drawdown, under one
	// dot row. The last point stays, so the end tag always has a value.
	deep := 0.0
	for _, pt := range dd {
		deep = math.Min(deep, pt.V)
	}
	flat := func(v float64) bool { return v > deep*0.03 }
	s := make(domain.Series, len(dd))
	for i, pt := range dd {
		v := 1 + pt.V
		if flat(pt.V) && i < len(dd)-1 {
			prevUnder := i > 0 && !flat(dd[i-1].V)
			nextUnder := !flat(dd[i+1].V)
			if !prevUnder && !nextUnder {
				v = math.NaN() // keep the points that close an underwater dip
			} else {
				v = 1
			}
		}
		s[i] = domain.Point{T: pt.T, V: v}
	}
	// Underwater: fill from 0% down to the line, in the loss color.
	// The wash fills only below 0% (a column on the baseline gets none),
	// and the current-value tag is ink unless the drawdown is past the cap:
	// a 0.0% drawdown is not an alarm.
	tag := p.th.Text
	if last := dd[len(dd)-1].V; last < ddCap {
		tag = p.th.Down
	}
	g := lineFn(chart.LineOpts{Width: p.w, Height: h, Series: []chart.Named{{Name: "drawdown", Series: s, Col: p.th.Down}},
		Area: true, AreaToBaseline: true, AreaMix: 0.30, Percent: true, Baseline: 1, Location: p.o.Location, TagCol: tag}, p.th)
	return append([]string{p.section("drawdown")}, p.gridLines(g)...)
}

// metricsTable compares the subject's metrics with each benchmark.
func (p *painter) metricsTable(subject string, m domain.Metrics, benches []domain.Benchmark, width int, corr bool) []string {
	type row struct {
		label string
		get   func(domain.Metrics) float64
		fmt   func(float64) cell
	}
	pct := func(v float64) cell { return p.cs(SignedPct(v, 1), v) }
	ratio := func(v float64) cell { return p.c(Num(v, 2)) }
	ddc := func(v float64) cell { return p.cst(SignedPct(v, 1), p.ddStyle(v)) }
	vol := func(v float64) cell { return p.c(Pct(v, 1)) }
	rowsDef := []row{
		{"Return", func(x domain.Metrics) float64 { return x.TotalReturn }, pct},
		{"CAGR", func(x domain.Metrics) float64 { return x.CAGR }, pct},
		{"Sharpe", func(x domain.Metrics) float64 { return x.Sharpe }, ratio},
		{"Sortino", func(x domain.Metrics) float64 { return x.Sortino }, ratio},
		{"Max DD", func(x domain.Metrics) float64 { return x.MaxDD }, ddc},
		{"Calmar", func(x domain.Metrics) float64 { return x.Calmar }, ratio},
		{"Volatility", func(x domain.Metrics) float64 { return x.Vol }, vol},
	}
	cols := []column{{title: "", flex: true, minW: 7, keep: 100}, {title: subject, right: true, keep: 99, name: true}}
	for i, b := range benches {
		cols = append(cols, column{title: b.Name, right: true, keep: 90 - i, name: true})
	}
	if p.o.Compact {
		// inline: 5 rows (return, CAGR, Sharpe, max DD, volatility)
		rowsDef = []row{rowsDef[0], rowsDef[1], rowsDef[2], rowsDef[4], rowsDef[6]}
	}
	var rows [][]cell
	for _, rd := range rowsDef {
		r := []cell{p.cst(strings.ToUpper(rd.label), p.st.label), rd.fmt(rd.get(m))}
		for _, b := range benches {
			r = append(r, rd.fmt(rd.get(b.Metrics)))
		}
		rows = append(rows, r)
	}
	if len(benches) > 0 && corr && measured(benches) {
		corr := []cell{p.cst("CORR", p.st.label), p.cm(dash)}
		beta := []cell{p.cst("BETA", p.st.label), p.cm(dash)}
		for _, b := range benches {
			corr = append(corr, p.c(Num(b.Corr, 2)))
			beta = append(beta, p.c(Num(b.Beta, 2)))
		}
		rows = append(rows, corr, beta)
	}
	if subject == "" {
		cols[1].title = "this"
	}
	return p.table(cols, rows, width, nil)
}

// measured reports whether any benchmark has a correlation or beta: rows
// of 0.00 where the figure could not be computed read as broken data.
func measured(bs []domain.Benchmark) bool {
	for _, b := range bs {
		if math.Abs(b.Corr) > 1e-9 || math.Abs(b.Beta) > 1e-9 {
			return true
		}
	}
	return false
}

// checklist renders due-diligence checks with the kit's marks: ■ ink
// (pass), ! gold (warn), ■ red (fail), □ dim (n/a). Details stay on one
// line.
func (p *painter) checklist(cs []domain.Check, width int) []string {
	return p.checks(cs, width, true)
}

// checks renders check rows; vault tearsheet checks (values) show their
// label-style names and values, other checks (order previews) their own
// label and detail.
func (p *painter) checks(cs []domain.Check, width int, vault bool) []string {
	if len(cs) == 0 {
		return nil
	}
	lw := 0
	for _, c := range cs {
		lw = max(lw, lipgloss.Width(checkName(c, vault)))
	}
	lw = min(lw, max(width/2, 12))
	dw := max(width-lw-4, 8)
	out := []string{p.section("checklist")}
	for _, c := range cs {
		mark, st := glyph.Off, p.st.dim
		switch c.Status {
		case domain.CheckPass:
			mark, st = glyph.On, p.st.text
		case domain.CheckWarn:
			mark, st = glyph.Note, p.st.warn
		case domain.CheckFail:
			mark, st = glyph.On, p.st.down
		}
		label := checkName(c, vault)
		if lipgloss.Width(label) > lw {
			label = ansi.Truncate(label, lw, "…")
		}
		if !vault {
			line := st.Render(mark) + " " + p.st.text.Render(label)
			if c.Detail == "" {
				out = append(out, line)
				continue
			}
			pad := strings.Repeat(" ", max(lw-lipgloss.Width(label), 0)+2)
			out = append(out, line+pad+p.st.muted.Render(ansi.Truncate(c.Detail, dw, "…")))
			continue
		}
		line := st.Render(mark) + " " + p.st.label.Render(label)
		val := checkValue(c, p.o.Plain)
		if val == "" {
			out = append(out, line)
			continue
		}
		vs := p.st.text
		if val == "n/a" || val == dash {
			// no value: a dim dot, not a lower-case word among UPPERCASE
			// labels
			vs, val = p.st.dim, "·"
		}
		pad := strings.Repeat(" ", max(lw-lipgloss.Width(label), 0)+2)
		out = append(out, line+pad+vs.Render(ansi.Truncate(val, dw, "…")))
	}
	return out
}

func checkName(c domain.Check, vault bool) string {
	if vault {
		return checkLabel(c)
	}
	return c.Label
}

// checkLabel is a check's name in label style: MAX DD, TRACK, TVL.
func checkLabel(c domain.Check) string {
	switch c.ID {
	case metrics.CheckMaxDD:
		return "MAX DD"
	case metrics.CheckAge:
		return "TRACK"
	case metrics.CheckTVL:
		return "TVL"
	case metrics.CheckLeaderSkin:
		return "LEADER"
	case metrics.CheckCapacity:
		return "ROOM"
	case metrics.CheckConcentration:
		return "HOLDERS"
	case metrics.CheckLeverage:
		return "LEVERAGE"
	case metrics.CheckDepositsOpen:
		return "DEPOSITS"
	case metrics.CheckDataFresh:
		return "DATA"
	case metrics.CheckSharpeVsHLP:
		return "VS HLP"
	}
	return strings.ToUpper(c.Label)
}

var (
	reDays  = regexp.MustCompile(`^(\d+) days`)
	reTop   = regexp.MustCompile(`top (\d+) depositors hold ([0-9.]+%)`)
	reLev   = regexp.MustCompile(`^([0-9.]+)x gross`)
	reVsHLP = regexp.MustCompile(`^([0-9.-]+) vs ([0-9.-]+) Sharpe`)
	reAgo   = regexp.MustCompile(`last updated (\w+) ago`)
)

// checkValue turns a check's detail sentence into a value (DESIGN.md 5.2
// Tearsheet): 17.5% TOP10, OPEN, 3.5Y, NO CAP, n/a. The thresholds in
// parentheses are the check's rule, not its value: the mark says the
// status, the full sentence stays in the JSON.
func checkValue(c domain.Check, plain bool) string {
	d := strings.TrimSpace(c.Detail)
	if i := strings.Index(d, " ("); i > 0 && !strings.Contains(d, "last updated") {
		d = d[:i]
	}
	sign := func(v string) string {
		if !plain && strings.HasPrefix(v, "-") {
			return minus + v[1:]
		}
		return v
	}
	switch {
	case d == "":
		return ""
	case c.Status == domain.CheckNA && c.ID != metrics.CheckLeaderSkin && c.ID != metrics.CheckSharpeVsHLP && c.ID != metrics.CheckCapacity:
		return "n/a"
	}
	switch c.ID {
	case metrics.CheckAge:
		if m := reDays.FindStringSubmatch(d); m != nil {
			n, _ := strconv.Atoi(m[1])
			return Age(n)
		}
	case metrics.CheckLeaderSkin:
		switch {
		case d == "protocol vault":
			return "PROTOCOL"
		case c.Status == domain.CheckNA:
			return "n/a"
		}
		return strings.TrimSuffix(d, " of the vault")
	case metrics.CheckCapacity:
		switch {
		case d == "unlimited":
			return "NO CAP"
		case strings.Contains(d, "closed"):
			return "CLOSED"
		case strings.HasPrefix(d, "no deposit room"):
			return "NONE"
		case c.Status == domain.CheckNA:
			return "n/a"
		}
		return strings.TrimSuffix(d, " before the leader hits 5%")
	case metrics.CheckConcentration:
		if m := reTop.FindStringSubmatch(d); m != nil {
			return m[2] + " TOP" + m[1]
		}
	case metrics.CheckLeverage:
		if m := reLev.FindStringSubmatch(d); m != nil {
			return m[1] + "X"
		}
	case metrics.CheckDepositsOpen:
		if c.Status == domain.CheckPass {
			return "OPEN"
		}
		return "CLOSED"
	case metrics.CheckDataFresh:
		if c.Status == domain.CheckPass {
			return "FRESH"
		}
		if m := reAgo.FindStringSubmatch(d); m != nil {
			return "STALE " + strings.ToUpper(m[1])
		}
		return "STALE"
	case metrics.CheckSharpeVsHLP:
		if m := reVsHLP.FindStringSubmatch(d); m != nil {
			return sign(m[1]) + " / " + sign(m[2])
		}
		return dash
	case metrics.CheckMaxDD, metrics.CheckTVL:
		return sign(d)
	}
	if len(strings.Fields(d)) > 2 {
		return "n/a"
	}
	return strings.ToUpper(d)
}

// exposureBlock shows signed notional per coin (latest composition, or
// the account's positions when no composition is available).
func (p *painter) exposureBlock(d domain.VaultDetail, width int) []string {
	by := map[string]float64{}
	equity := 0.0
	if n := len(d.Composition); n > 0 {
		last := d.Composition[n-1]
		for k, v := range last.ByCoin {
			by[k] = v
		}
		equity = last.Equity
	} else if d.Account != nil {
		for _, pos := range d.Account.Positions {
			v := math.Abs(pos.Value)
			if pos.Size < 0 {
				v = -v
			}
			by[pos.Coin] += v
		}
		equity = d.Account.Equity
	}
	if len(by) == 0 {
		return nil
	}
	return p.exposureBars(by, equity, width, 8)
}

func (p *painter) exposureBars(by map[string]float64, equity float64, width, top int) []string {
	type kv struct {
		k string
		v float64
	}
	list := make([]kv, 0, len(by))
	gross, net := 0.0, 0.0
	for k, v := range by {
		list = append(list, kv{k, v})
		gross += math.Abs(v)
		net += v
	}
	other := func(k string) bool { return strings.EqualFold(k, "other") }
	// "Other" is a remainder, not a position: it sorts last and is always
	// kept, so the widest bar is a real coin.
	sort.Slice(list, func(i, j int) bool {
		if oi, oj := other(list[i].k), other(list[j].k); oi != oj {
			return oj
		}
		if math.Abs(list[i].v) == math.Abs(list[j].v) {
			return list[i].k < list[j].k
		}
		return math.Abs(list[i].v) > math.Abs(list[j].v)
	})
	if len(list) > top {
		if last := list[len(list)-1]; other(last.k) {
			list = append(list[:top-1:top-1], last)
		} else {
			list = list[:top]
		}
	}
	labels := make([]string, len(list))
	values := make([]float64, len(list))
	for i, e := range list {
		labels[i], values[i] = e.k, e.v
	}
	pairs := [][2]string{{"", p.lbl("exposure")}, {"gross", p.st.text.Render(USD(gross))}, {"net", p.sign(net).Render(SignedUSD(net))}}
	if equity > 0 {
		pairs = append(pairs, [2]string{"", p.st.text.Render(Num(gross/equity, 2) + "X")})
	}
	g := barsFn(labels, values, width, p.th)
	return append([]string{p.meta(pairs...)}, p.gridLines(g)...)
}

// vaultCard is the tearsheet: return as the focal number, facts line,
// curve vs benchmarks, drawdown, metrics and checklist side by side when
// there is room, monthly heatmap, exposure.
func (p *painter) vaultCard(r *core.Result) card {
	d, ok := as[domain.VaultDetail](r.Data)
	if !ok {
		return p.fallbackCard(r)
	}
	s := d.Summary
	c := card{title: s.Name, name: true, tagOwnLine: true}
	if !p.o.Compact {
		c.sub = ShortAddr(s.Address)
	}
	if c.title == "" {
		c.title = r.Title
	}
	var ts domain.Tearsheet
	if d.Tearsheet != nil {
		ts = *d.Tearsheet
	}
	ret, has := ts.Metrics.TotalReturn, d.Tearsheet != nil && len(ts.Curve) > 1
	if !has {
		ret, has = s.Returns[domain.WAll]
	}
	switch {
	case has && ret == 0 && len(ts.Curve) < 2 && (s.TVL <= 0 || s.Closed):
		// no history on an empty or closed vault: never a fake 0.0%
		c.focal, c.focalStyle = "NO HISTORY", p.st.muted
	case has:
		c.focal, c.focalStyle, c.numeric = SignedPct(ret, 1), p.sign(ret), true
		c.deltaLabel = "all"
	}
	facts := p.vaultFacts(d)
	if p.o.Hero {
		// The pane's breadcrumb names the vault and the big digits carry
		// the return: no second name line, the address leads the facts.
		c.noHeader = true
		if a := ShortAddr(s.Address); a != "" {
			facts = append([][2]string{{"", p.st.muted.Render(a)}}, facts...)
		}
	}
	c.body = append(c.body, p.meta(facts...))
	if !p.o.Compact {
		c.body = append(c.body, "") // the facts and the chart legend do not touch
	}

	curve := ts.Curve
	if len(curve) < 2 {
		curve = d.NAV
	}
	subject := s.Name
	if subject == "" {
		subject = "vault"
	}
	if p.o.Compact {
		// at least 10 plot rows: a chart, not a strip
		c.body = append(c.body, p.curveBlock(subject, curve, ts.Benchmarks, 13)...)
		c.body = append(c.body, p.compactMetrics(ts.Metrics, s, ts.Benchmarks, true))
		c.tagOwnLine = false
		return c
	}
	c.body = append(c.body, p.curveBlock(subject, curve, ts.Benchmarks, 12)...)
	if dd := p.drawdownBlock(ts.Drawdown, 5); dd != nil {
		c.body = append(c.body, "")
		c.body = append(c.body, dd...)
	}
	if d.Tearsheet != nil {
		col := strings.ToUpper(legendName(subject))
		if r := []rune(col); len(r) > 12 {
			col = string(r[:11]) + "…"
		}
		// No CORR / BETA rows here: on a daily curve against these
		// benchmarks they print 0.00, which reads as missing data, and the
		// rows push MONTHLY % off the first screen (compare keeps them).
		mt := p.metricsTable(col, ts.Metrics, ts.Benchmarks, p.w, false)
		ck := p.checklist(ts.Checklist, p.w)
		mw := 0
		for _, l := range mt {
			mw = max(mw, lipgloss.Width(l))
		}
		// The table's Sharpe comes from this card's daily curve; the
		// screener's all-time figure is a labelled NOTES line, never a
		// second unlabelled number beside it.
		var foot []string
		if s.Sharpe != 0 && math.Abs(s.Sharpe-ts.Metrics.Sharpe) >= 0.05 {
			c.notes = append(c.notes, p.lbl("screener sharpe")+" "+p.st.text.Render(Num(s.Sharpe, 2)))
		}
		c.body = append(c.body, "", p.section("metrics"))
		if len(ck) > 0 && p.w-mw-4 >= 44 {
			ck = p.checklist(ts.Checklist, p.w-mw-4)
			c.body = append(c.body, sideBySide(mt, ck, mw, 4)...)
			c.body = append(c.body, foot...)
		} else {
			c.body = append(c.body, mt...)
			c.body = append(c.body, foot...)
			if len(ck) > 0 {
				c.body = append(c.body, "")
				c.body = append(c.body, ck...)
			}
		}
		if len(ts.Monthly) > 0 {
			c.body = append(c.body, "", p.section("monthly %"))
			c.body = append(c.body, p.gridLines(heatmapFn(ts.Monthly, p.w, p.th))...)
		}
	}
	if ex := p.exposureBlock(d, p.w); ex != nil {
		c.body = append(c.body, "")
		c.body = append(c.body, ex...)
	}
	if d.Description != "" {
		// the tool marks leader text for agents; the card shows it muted
		desc := strings.Join(strings.Fields(strings.TrimPrefix(d.Description, core.UntrustedPrefix)), " ")
		if len([]rune(desc)) > 240 {
			desc = string([]rune(desc)[:239]) + "…"
		}
		c.notes = append(c.notes, p.st.muted.Render(desc))
	}
	model := d.SPModel
	if model == "" {
		model = s.SPModel
	}
	if model != "" {
		note := metrics.SourceLabel(string(model))
		if s.AccuracyPct > 0 {
			note += " ±" + trimFloat(s.AccuracyPct, 2) + "%"
		}
		c.notes = append(c.notes, p.lbl("share px")+" "+p.st.muted.Render(note))
	}
	if !s.AllowDeposits && !s.Closed && s.TVL > 0 && !isHLP(s) {
		c.tag = "DEPOSITS OFF"
	}
	return c
}

// vaultFacts is the tearsheet's meta line: TVL $151.6M   3.5Y   4D LOCK.
// The full card adds leader share, profit share, room and depositors.
func (p *painter) vaultFacts(d domain.VaultDetail) [][2]string {
	s := d.Summary
	var pairs [][2]string
	add := func(label, val string) {
		if val == "" || val == dash {
			return
		}
		pairs = append(pairs, [2]string{label, p.st.text.Render(val)})
	}
	unit := func(val, label string) {
		if val == "" || val == dash {
			return
		}
		pairs = append(pairs, [2]string{"", p.st.text.Render(val) + p.lbl(label)})
	}
	add("TVL", USD(s.TVL))
	unit(Age(s.AgeDays), "")
	if d.LockupDays > 0 {
		unit(strings.ToUpper(trimFloat(d.LockupDays, 1)+"D"), " lock")
	}
	if s.Closed {
		pairs = append(pairs, [2]string{"", p.st.dim.Render(glyph.Off + " CLOSED")})
	}
	if s.Kind == domain.VaultHyperEVM {
		pairs = append(pairs, [2]string{"", p.lbl("HyperEVM")})
	}
	if p.o.Compact {
		return pairs
	}
	if s.LeaderFraction > 0 {
		add("leader", Pct(s.LeaderFraction, 1))
	}
	if s.ProfitShare > 0 {
		add("fee", Pct(s.ProfitShare, 0))
	}
	capUSD := d.CapacityUSD
	if capUSD == 0 {
		capUSD = VaultCapacity(s)
	}
	add("room", capacityText(capUSD))
	if s.Followers > 0 {
		f := strconv.Itoa(s.Followers)
		if s.Followers >= 100 {
			f = "100+"
		}
		unit(f, " dep")
	}
	return pairs
}

// compactMetrics is the one-line metric digest of an inline tearsheet.
func (p *painter) compactMetrics(m domain.Metrics, s domain.VaultSummary, benches []domain.Benchmark, withCAGR bool) string {
	// Two Sharpe numbers exist (owner decision 13): the one computed from
	// the daily curve, and the screener's (hlp.xyz v6). Each keeps its own
	// label; both show when both are known.
	dd := m.MaxDD
	if dd == 0 {
		dd = s.MaxDD
	}
	// No history (or an empty vault): a dash, never a fake 0.00 / 0.0%.
	ddTxt := Pct(dd, 1)
	if m.Sharpe == 0 && s.Sharpe == 0 && dd == 0 {
		ddTxt = dash
	}
	// One Sharpe, labelled: the daily curve's when there is one, else the
	// screener's (DESIGN.md section 5.1 rule 11, no fake precision).
	sh := m.Sharpe
	if sh == 0 {
		sh = s.Sharpe
	}
	shTxt := dash
	if sh != 0 {
		shTxt = Num(sh, 2)
	}
	if ddTxt != dash {
		ddTxt = SignedPct(dd, 1)
	}
	pairs := [][2]string{
		{"sharpe", p.st.text.Render(shTxt)},
		{"max dd", p.ddStyle(dd).Render(ddTxt)},
	}
	if m.CAGR != 0 && withCAGR {
		pairs = append(pairs, [2]string{"CAGR", p.sign(m.CAGR).Render(SignedPct(m.CAGR, 1))})
	}
	for _, b := range benches {
		if b.Name == "HLP" && m.TotalReturn != 0 {
			diff := m.TotalReturn - b.Metrics.TotalReturn
			pairs = append(pairs, [2]string{"vs HLP", p.sign(diff).Render(SignedPct(diff, 1))})
		}
	}
	return p.meta(pairs...)
}

// compareCard overlays several vaults' curves with a metrics table and the
// correlation matrix.
func (p *painter) compareCard(r *core.Result) card {
	d, ok := as[core.CompareData](r.Data)
	if !ok {
		return p.fallbackCard(r)
	}
	c := card{title: r.Title}
	if c.title == "" {
		c.title = "Compare"
	}
	c.focal, c.focalStyle = strconv.Itoa(len(d.Vaults)), p.st.bold
	if len(d.Vaults) == 0 {
		c.focal, c.focalStyle = "NO MATCH", p.st.muted
		return c
	}
	short := func(s string) string {
		if r := []rune(s); len(r) > 14 {
			return string(r[:13]) + "…"
		}
		return s
	}
	var series []chart.Named
	benches := make([]domain.Benchmark, 0, len(d.Vaults))
	for i, v := range d.Vaults {
		curve := v.NAV
		var m domain.Metrics
		if v.Tearsheet != nil {
			if len(v.Tearsheet.Curve) > 1 {
				curve = v.Tearsheet.Curve
			}
			m = v.Tearsheet.Metrics
		}
		name := short(v.Summary.Name)
		if name == "" {
			name = ShortAddr(v.Summary.Address)
		}
		if len(curve) > 1 && i < 4 {
			series = append(series, chart.Named{Name: name, Series: curve, Color: i})
		}
		benches = append(benches, domain.Benchmark{Name: name, Metrics: m})
	}
	h := 12
	if p.o.Compact {
		h = 8
	}
	if len(series) > 0 {
		g := lineFn(chart.LineOpts{Width: p.w, Height: h, Series: series, Percent: true, Baseline: math.NaN(), Location: p.o.Location}, p.th)
		c.body = append(c.body, p.gridLines(g)...)
	}
	// One column per vault: reuse the metrics table with the first vault as subject.
	c.body = append(c.body, "")
	c.body = append(c.body, p.metricsTable(benches[0].Name, benches[0].Metrics, benches[1:], p.w, false)...)
	if len(d.Corr) == len(d.Vaults) && len(d.Vaults) > 1 && !p.o.Compact {
		c.body = append(c.body, "", p.section("correlation"))
		cols := []column{{title: "", flex: true, minW: 6, keep: 100}}
		for i, b := range benches {
			cols = append(cols, column{title: b.Name, right: true, keep: 50 - i, name: true})
		}
		var rows [][]cell
		for i, row := range d.Corr {
			cr := []cell{p.cst(benches[i].Name, p.st.label)}
			for j, v := range row {
				if i == j {
					cr = append(cr, p.cm(dash))
					continue
				}
				// tinted by value: strong correlation reads in ink, weak
				// muted (no highlight colour)
				st := p.st.muted
				if math.Abs(v) >= 0.7 {
					st = p.st.bold
				}
				cr = append(cr, p.cst(Num(v, 2), st))
			}
			rows = append(rows, cr)
		}
		c.body = append(c.body, p.table(cols, rows, p.w, nil)...)
	}
	return c
}

// fmtWeight renders a signed weight as a percent of NAV.
func fmtWeight(w float64) string { return SignedPct(w, 1) }
