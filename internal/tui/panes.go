package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/maxmoneycash/hlp-cli/internal/chart"
	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/tui/cards"
	"github.com/maxmoneycash/hlp-cli/internal/tui/glyph"
)

type paneKind int

const (
	paneCard   paneKind = iota // any card, full size, scrollable (tearsheet, watch, arena, ...)
	paneChart                  // interactive candles
	paneVaults                 // sortable, filterable screener
	paneDash                   // a grid of live tiles (/dash)
)

// pane is a full-screen view over the transcript.
type pane struct {
	kind    paneKind
	seq     int // identity for live subscriptions
	entryID int // transcript entry this pane mirrors (0 = none)
	res     *core.Result
	tool    core.Tool
	call    core.Call
	scroll  int
	loading bool
	err     error

	// dashboard (paneDash): its layout and one pane per tile; a tile's
	// label is the command it runs
	dash  *dashSpec
	tiles []*pane
	label string

	// chart
	zoom, offset, cursor, style int // zoom 0 = auto (defaultZoom of the pane width)
	shownZoom                   int // the stride last rendered
	log, noMAs                  bool
	vp                          chart.Viewport
	chartX, chartY              int        // screen origin of the chart grid
	rng                         [2]float64 // last price range, kept while only prices change
	rngKey                      string     // the view rng was rendered for
	ck                          chartKey   // inputs of the memoized chart lines
	cl                          []string   // the chart lines rendered for ck
	foot, footKey               string     // memoized hint bar

	// memoized cards.Focal of res
	fk           focalKey
	fv           float64
	fbig, fsmall string
	fok          bool
	bandKey      string // memoized chart band
	band         []string

	// vaults
	all       []domain.VaultSummary
	view      []domain.VaultSummary
	sortIdx   int
	asc       bool
	sel, off  int
	filter    editor
	filtering bool

	showKeys bool // ? lists the pane's keys
	info     bool // i: source, as-of and NOTES (cards.Opts.Info)
}

var paneSeq int

// chartStyles is the pane's s-key cycle; it starts at the configured style.
var chartStyles = chart.Styles

// paneStyle is the style a chart pane shows: the shell's style (/candles,
// chart.style) advanced by the pane's own s-key presses.
func (m *Model) paneStyle(p *pane) chart.Style {
	base := 0
	for i, s := range chartStyles {
		if s == m.chartStyle {
			base = i
		}
	}
	return chartStyles[(base+p.style)%len(chartStyles)]
}

// chartIntervals are bound to the number keys 1-9 in the chart pane.
var chartIntervals = []domain.Interval{domain.I1m, domain.I5m, domain.I15m, domain.I30m, domain.I1h, domain.I4h, domain.I12h, domain.I1d, domain.I1w}

var zoomSteps = []int{1, 2, 4, 6}

func newPane(r *core.Result, entryID int, tool core.Tool, call core.Call) *pane {
	paneSeq++
	p := &pane{seq: paneSeq, entryID: entryID, tool: tool, call: call, cursor: -1}
	p.setResult(r)
	return p
}

// setResult installs a (new or live-updated) result, keeping view state.
func (p *pane) setResult(r *core.Result) {
	p.res = r
	if r == nil {
		return
	}
	switch r.Card {
	case core.CardChart:
		p.kind = paneChart
	case core.CardVaults:
		p.kind = paneVaults
		if vs, ok := asVaults(r.Data); ok {
			p.all = vs
			p.applyVaultView()
		}
	default:
		p.kind = paneCard
	}
}

func asVaults(v any) ([]domain.VaultSummary, bool) {
	switch x := v.(type) {
	case []domain.VaultSummary:
		return x, true
	case *[]domain.VaultSummary:
		if x != nil {
			return *x, true
		}
	}
	return nil, false
}

func asChart(v any) (*core.ChartData, bool) {
	switch x := v.(type) {
	case *core.ChartData:
		return x, x != nil
	case core.ChartData:
		return &x, true
	}
	return nil, false
}

// applyVaultView filters (fuzzy-ish substring on name/address) and sorts.
func (p *pane) applyVaultView() {
	q := strings.ToLower(strings.TrimSpace(p.filter.Value()))
	p.view = p.view[:0]
	for _, v := range p.all {
		if q == "" || strings.Contains(strings.ToLower(v.Name), q) || strings.HasPrefix(strings.ToLower(v.Address), q) {
			p.view = append(p.view, v)
		}
	}
	cards.SortVaults(p.view, cards.VaultSortKeys[p.sortIdx], !p.asc)
	p.sel = min(max(p.sel, 0), max(len(p.view)-1, 0))
}

func (m *Model) topPane() *pane {
	if len(m.panes) == 0 {
		return nil
	}
	return m.panes[len(m.panes)-1]
}

func (m *Model) pushPane(p *pane) {
	m.panes = append(m.panes, p)
	m.compOff = true
}

// popPane closes the top pane. A pane opened from a transcript card hands
// its latest (possibly live-updated) result back to that card.
func (m *Model) popPane() {
	if len(m.panes) == 0 {
		return
	}
	p := m.panes[len(m.panes)-1]
	m.panes = m.panes[:len(m.panes)-1]
	if e := m.entryByID(p.entryID); e != nil && e.kind == entryCard && p.res != nil {
		e.result, e.cache = p.res, ""
	}
}

// openEntry opens a transcript card full screen. A slimmed card (its data
// dropped to bound memory) re-runs its call into a new pane.
func (m *Model) openEntry(e *entry) tea.Cmd {
	if e == nil || e.kind != entryCard || e.result == nil {
		return nil
	}
	t := e.tool
	if t.Name == "" {
		t = m.toolFor(e.result, e.call.Tool)
	}
	if e.slim {
		if t.Name != "" && len(e.call.Args) > 0 && !t.Mutating {
			return m.runJob(t, e.call, target{push: true})
		}
		return nil
	}
	m.pushPane(newPane(e.result, e.id, t, e.call))
	return nil
}

// openLatest opens the newest card (Enter on an empty prompt, ctrl+o).
func (m *Model) openLatest() tea.Cmd {
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].kind == entryCard {
			return m.openEntry(m.entries[i])
		}
	}
	return nil
}

// paneKey handles keys while a pane is open.
func (m *Model) paneKey(p *pane, k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if p.kind == paneVaults && p.filtering {
		switch key {
		case "esc":
			p.filtering = false
			p.filter.Reset()
			p.applyVaultView()
		case "enter":
			p.filtering = false
		default:
			if p.filter.HandleKey(k) {
				p.sel, p.off = 0, 0
				p.applyVaultView()
			}
		}
		return nil
	}
	if p.showKeys {
		p.showKeys = false // any key closes the key list
		return nil
	}
	switch key {
	case "esc", "q":
		m.popPane()
		return nil
	case "?":
		p.showKeys = true
		return nil
	}
	switch p.kind {
	case paneChart:
		return m.chartKey(p, key)
	case paneVaults:
		return m.vaultsKey(p, key)
	case paneDash:
		return m.dashKey(p, key)
	}
	if key == "i" {
		p.info = !p.info
		return nil
	}
	return m.scrollKey(p, key)
}

func (m *Model) scrollKey(p *pane, key string) tea.Cmd {
	page := max(m.height-4, 1)
	switch key {
	case "j", "down":
		p.scroll++
	case "k", "up":
		p.scroll--
	case "pgdown", "space", "f":
		p.scroll += page
	case "pgup", "b":
		p.scroll -= page
	case "g", "home":
		p.scroll = 0
	case "G", "end":
		p.scroll = 1 << 20
	}
	p.scroll = max(p.scroll, 0)
	return nil
}

func (m *Model) chartKey(p *pane, key string) tea.Cmd {
	d, _ := asChart(p.res.Data)
	n := 0
	if d != nil {
		n = len(d.Candles)
	}
	step := max(p.vp.Count/8, 1)
	switch key {
	case "h", "left":
		if p.cursor >= 0 && key == "left" {
			p.cursor = min(p.cursor+1, max(p.vp.Count-1, 0))
		} else {
			// Pan no further than the chart can show: past MaxOffset the
			// window would stop moving while the pan state kept growing.
			lim := max(n-4, 0)
			if p.vp.Count > 0 {
				lim = p.vp.MaxOffset
			}
			p.offset = min(p.offset+step, lim)
		}
	case "l", "right":
		if p.cursor >= 0 && key == "right" {
			p.cursor = max(p.cursor-1, 0)
		} else {
			p.offset = max(p.offset-step, 0)
		}
	case "+", "=":
		p.zoom = nextZoom(p.effZoom(), +1)
	case "-", "_":
		p.zoom = nextZoom(p.effZoom(), -1)
	case "0":
		p.offset, p.cursor, p.zoom = 0, -1, 0
	case "c":
		if p.cursor >= 0 {
			p.cursor = -1
		} else {
			p.cursor = 0
		}
	case "i":
		p.noMAs = !p.noMAs
	case "L":
		p.log = !p.log
	case "s":
		p.style = (p.style + 1) % len(chartStyles)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		iv := chartIntervals[key[0]-'1']
		if d == nil || d.Interval == iv || p.tool.Name == "" {
			return nil
		}
		args := map[string]any{}
		_ = json.Unmarshal(p.call.Args, &args)
		args["coin"] = d.Coin
		args["interval"] = string(iv)
		raw, _ := json.Marshal(args)
		p.call = core.Call{Tool: p.tool.Name, Args: raw}
		p.loading, p.offset, p.cursor = true, 0, -1
		return m.runJob(p.tool, p.call, target{pane: p})
	}
	return nil
}

// defaultZoom picks the full-screen chart stride for a pane width: stride 4
// (wide bodies) only when it still shows about 60 bars, else stride 2, so
// opening a chart full screen never shows less history than the inline
// card did. A skin with open up bodies (terminal, rush) always uses stride
// 4, like its inline card.
func defaultZoom(width int) int {
	plot := width - 2 - 12 // margin, price axis and right pad
	if plot/4 >= 60 {
		return 4
	}
	return 2
}

// effZoom is the stride in use: the explicit zoom, or what auto rendered.
func (p *pane) effZoom() int {
	switch {
	case p.zoom > 0:
		return p.zoom
	case p.shownZoom > 0:
		return p.shownZoom
	}
	return 2
}

func nextZoom(z, dir int) int {
	for i, s := range zoomSteps {
		if s == z {
			return zoomSteps[min(max(i+dir, 0), len(zoomSteps)-1)]
		}
	}
	return 2
}

func (m *Model) vaultsKey(p *pane, key string) tea.Cmd {
	rows := m.vaultRows()
	switch key {
	case "j", "down":
		p.sel++
	case "k", "up":
		p.sel--
	case "pgdown", "space":
		p.sel += rows
	case "pgup":
		p.sel -= rows
	case "g", "home":
		p.sel = 0
	case "G", "end":
		p.sel = len(p.view) - 1
	case "s", "tab":
		p.sortIdx = (p.sortIdx + 1) % len(cards.VaultSortKeys)
		p.applyVaultView()
	case "S", "shift+tab":
		p.sortIdx = (p.sortIdx + len(cards.VaultSortKeys) - 1) % len(cards.VaultSortKeys)
		p.applyVaultView()
	case "r":
		p.asc = !p.asc
		p.applyVaultView()
	case "/":
		p.filtering = true
	case "enter":
		if p.sel < 0 || p.sel >= len(p.view) || m.reg == nil {
			return nil
		}
		t, ok := m.reg.Get("get_vault")
		if !ok {
			return nil
		}
		raw, _ := json.Marshal(map[string]any{"vault": p.view[p.sel].Address})
		p.loading = true
		return m.runJob(t, core.Call{Tool: t.Name, Args: raw}, target{push: true})
	}
	p.sel = min(max(p.sel, 0), max(len(p.view)-1, 0))
	if p.sel < p.off {
		p.off = p.sel
	}
	if p.sel >= p.off+rows {
		p.off = p.sel - rows + 1
	}
	return nil
}

// vaultRows is how many screener rows fit in the pane.
func (m *Model) vaultRows() int { return max(m.height-8, 3) }

// paneMouse maps mouse motion in the chart pane to the crosshair, and the
// wheel to panning or scrolling.
func (m *Model) paneMouse(p *pane, msg tea.MouseMsg) {
	mo := msg.Mouse()
	switch msg.(type) {
	case tea.MouseWheelMsg:
		up := mo.Button == tea.MouseWheelUp
		switch p.kind {
		case paneChart:
			if up {
				m.chartKey(p, "h")
			} else {
				m.chartKey(p, "l")
			}
		case paneVaults:
			if up {
				m.vaultsKey(p, "k")
			} else {
				m.vaultsKey(p, "j")
			}
		default:
			if up {
				p.scroll = max(p.scroll-3, 0)
			} else {
				p.scroll += 3
			}
		}
	case tea.MouseMotionMsg, tea.MouseClickMsg:
		if p.kind != paneChart || p.vp.Count == 0 {
			return
		}
		x, y := mo.X-p.chartX, mo.Y-p.chartY
		if y < 0 || y >= p.vp.PlotY+p.vp.PlotH+8 {
			p.cursor = -1
			return
		}
		idx := p.vp.IndexAt(x)
		if idx < 0 {
			return
		}
		newest := p.vp.First + p.vp.Count - 1
		p.cursor = max(newest-idx, 0)
	}
}

// renderPane draws the top pane into width x height (without the status bar).
func (m *Model) renderPane(p *pane, width, height int) []string {
	switch p.kind {
	case paneChart:
		return m.renderChartPane(p, width, height)
	case paneVaults:
		return m.renderVaultsPane(p, width, height)
	case paneDash:
		return m.renderDashPane(p, width, height)
	}
	o := m.cardOpts(width, false)
	o.Info = p.info
	var band []string
	if m.bigOK(width, height) {
		if v, big, small, ok := m.paneFocal(p, o); ok {
			if b := m.paneBand(v, big, small); b != nil {
				band, o.Hero = b, true
			}
		}
	}
	if band == nil {
		band = []string{""}
	}
	if fillsHeight(p.res) {
		o.Height = height - len(band)
	}
	body := strings.Split(cards.Render(p.res, o), "\n")
	if p.err != nil {
		body = append(strings.Split(cards.Error(p.err, o), "\n"), body...)
	}
	h := max(height-len(band), 1)
	maxScroll := max(len(body)-h, 0)
	p.scroll = min(p.scroll, maxScroll)
	return append(band, body[p.scroll:min(len(body), p.scroll+h)]...)
}

// bigOK reports whether a pane may draw big digits: at least 60 x 24 and
// a color terminal (DESIGN.md 3.1 fallback).
func (m *Model) bigOK(width, height int) bool {
	return width >= 60 && height+2 >= 24 && m.profile != colorprofile.NoTTY && m.profile != colorprofile.Ascii
}

// paneFocal is cards.Focal for the pane's result, memoized on the result
// and the look (a card build per frame would be wasted work).
func (m *Model) paneFocal(p *pane, o cards.Opts) (float64, string, string, bool) {
	k := focalKey{res: p.res, w: o.Width, th: m.th.Name, prof: m.profile}
	if p.fk != k {
		p.fk = k
		p.fv, p.fbig, p.fsmall, p.fok = cards.Focal(p.res, o)
	}
	return p.fv, p.fbig, p.fsmall, p.fok
}

type focalKey struct {
	res  *core.Result
	w    int
	th   string
	prof colorprofile.Profile
}

// paneBand is a pane's header band (DESIGN.md 3.1 and 6.1): a blank row,
// the focal in 3-row big digits with its unit and delta small and bold on
// the baseline, then a blank row. Ink, or red when the value is negative.
// nil when the number has a rune big digits cannot draw.
func (m *Model) paneBand(v float64, big, small string) []string {
	rows, ok := glyph.Big(big)
	if !ok {
		return nil
	}
	st := m.st.bold
	if v < 0 {
		st = m.st.down.Bold(true)
	}
	out := []string{"", gutter + st.Render(rows[0]), gutter + st.Render(rows[1]), gutter + st.Render(rows[2])}
	if small != "" {
		sm := m.st.bold
		if strings.HasPrefix(small, glyph.Minus) {
			sm = m.st.down.Bold(true)
		}
		parts := strings.SplitN(small, "  ", 2)
		out[3] += "  " + sm.Render(parts[0])
		if len(parts) > 1 {
			out[3] += "  " + m.st.label.Render(parts[1])
		}
	}
	return append(out, "")
}

// chartKey is everything the chart pane's lines depend on. When it is
// unchanged (a WS state change, a spinner tick, mouse motion on the same
// bar) the pane reuses its lines instead of redrawing and re-serializing
// the grid (PERFORMANCE #5).
type chartKey struct {
	res                  *core.Result
	w, h, zoom, off, cur int
	style                chart.Style
	log, noMAs           bool
	th, skin             string
	prof                 colorprofile.Profile
	nowSec               int64
	band                 int
}

func (m *Model) renderChartPane(p *pane, width, height int) []string {
	d, ok := asChart(p.res.Data)
	if !ok {
		return []string{m.st.muted.Render(" no chart data")}
	}
	mas := d.MAs
	if p.noMAs {
		mas = nil
	} else if len(mas) == 0 && m.env != nil {
		mas = m.env.Config.Chart.MAs
	}
	h := height
	lines := []string{""}
	banded := false
	if m.bigOK(width, height) && len(d.Candles) > 0 {
		if b := m.chartBand(p, d); b != nil {
			// The band says the price; the readout row gives way to the
			// MA legend alone (labels in their line colors).
			lines, banded = append(b[:len(b)-1:len(b)-1], m.maLegend(mas, d.EMAs)), true
		}
	}
	h -= len(lines)
	if p.err != nil {
		el := strings.Split(cards.Error(p.err, m.cardOpts(width, true)), "\n")
		lines = append(lines, el...)
		h -= len(el)
	}
	loc := m.chartLoc(d.Interval)
	zoom := p.zoom
	if zoom <= 0 {
		zoom = defaultZoom(width)
		if chart.WideBodies(m.paneStyle(p), m.paneSkin()) {
			// open (hollow) up bodies need 3 columns to read as a box, as
			// on the inline card: a 1-column body is only a heavy rule
			zoom = 4
		}
	}
	p.shownZoom = zoom
	// Live ticks keep the scale (the chart's hysteresis) until the view
	// itself changes: pan, zoom, size, coin, interval, scale or look.
	key := fmt.Sprint(d.Coin, d.Interval, width, h, zoom, p.offset, p.log, m.paneStyle(p), m.paneSkin())
	if key != p.rngKey {
		p.rng, p.rngKey = [2]float64{}, key
	}
	var now time.Time
	if p.offset == 0 && m.now != nil {
		now = m.now() // the bar countdown while the newest bar is open
	}
	ck := chartKey{res: p.res, w: width, h: h, zoom: zoom, off: p.offset, cur: p.cursor, style: m.paneStyle(p),
		log: p.log, noMAs: p.noMAs, th: m.th.Name, skin: m.paneSkin(), prof: m.profile}
	if !now.IsZero() {
		ck.nowSec = now.Unix()
	}
	ck.band = len(lines)
	if p.cl != nil && ck == p.ck {
		return append(m.panTag(lines, p, width), p.cl...)
	}
	g, vp := chart.Candles(d.Candles, chart.CandleOpts{
		Width: width - 4, Height: max(h, 4), Style: m.paneStyle(p), Skin: m.paneSkin(), Zoom: zoom, VolRows: -1,
		VolumeUSD: true, MAs: mas, EMAs: d.EMAs, Lines: d.Lines, Cursor: p.cursor, Offset: p.offset, Log: p.log, Header: !banded,
		Coin: d.Coin, Interval: d.Interval, Location: loc, Range: p.rng, Now: now, NoTitle: len(lines) > 1,
	}, m.th)
	p.vp = vp
	p.rng = [2]float64{vp.Lo, vp.Hi}
	p.chartX, p.chartY = len(gutter), 1+len(lines) // status bar row + rows above the grid
	var s string
	if m.profile == colorprofile.Unknown || m.profile == colorprofile.NoTTY {
		s = g.Plain()
	} else {
		s = g.ANSI(m.profile)
	}
	cl := strings.Split(s, "\n")
	for i, l := range cl {
		cl[i] = gutter + l
	}
	p.ck, p.cl = ck, cl
	return append(m.panTag(lines, p, width), cl...)
}

// panTag right-aligns the applied pan ("−12 BARS", Dim) on the row above
// the chart while the view is panned off the newest bar.
func (m *Model) panTag(lines []string, p *pane, width int) []string {
	if p.vp.Offset <= 0 || len(lines) == 0 {
		return lines
	}
	tag := m.st.dim.Render("−" + itoa(p.vp.Offset) + " BARS")
	last := len(lines) - 1
	lw, tw := lipgloss.Width(lines[last]), lipgloss.Width(tag)
	if lw+tw+3 > width-len(gutter) {
		return lines
	}
	out := append(lines[:last:last], lines[last]+strings.Repeat(" ", width-len(gutter)-lw-tw)+tag)
	return out
}

func (m *Model) renderVaultsPane(p *pane, width, height int) []string {
	key := cards.VaultSortKeys[p.sortIdx]
	// The count is in the top bar's crumb; a header row appears only for
	// the filter.
	lines := []string{""}
	switch {
	case p.filtering:
		lines = append(lines, gutter+m.st.accent.Render(glyph.Crumb)+" "+m.st.text.Render(p.filter.Value())+m.st.cursor.Render(" "), "")
	case p.filter.Value() != "":
		lines = append(lines, gutter+m.st.dim.Render(glyph.Crumb)+" "+m.st.text.Render(p.filter.Value()), "")
	}
	if p.err != nil {
		lines = append(lines, strings.Split(cards.Error(p.err, m.cardOpts(width, true)), "\n")...)
	}
	// the table header, then one blank row above the key row
	rows := max(height-len(lines)-2, 1)
	if p.sel >= p.off+rows {
		p.off = p.sel - rows + 1
	}
	table := cards.VaultTable(p.view, m.cardOpts(width, false), cards.VaultTableOpts{
		Selected: p.sel, Sort: key, Desc: !p.asc, Offset: p.off, Rows: rows,
	})
	lines = append(lines, strings.Split(table, "\n")...)
	if len(p.view) == 0 {
		lines = append(lines, gutter+m.st.bold.Render("NO MATCH"))
		if m.st.floor != nil && m.uiFloor() {
			void := make([]string, max(height-len(lines), 0))
			lines = append(lines, glyph.Floor(void, width, 1+len(lines), m.st.floor)...)
		}
	}
	return lines
}

// chartLoc picks the label zone: local for intraday, UTC for >= 1d, or UTC
// throughout when config says so.
func (m *Model) chartLoc(iv domain.Interval) *time.Location {
	if (m.env != nil && m.env.Config.Chart.UTC) || iv.Duration() >= 24*time.Hour {
		return time.UTC
	}
	return time.Local
}

func padBetween(left, right string, width int) string {
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	if right == "" {
		return truncate(left, width)
	}
	if lw+rw+2 > width {
		return truncate(left, width)
	}
	return left + strings.Repeat(" ", width-lw-rw-1) + right
}

// fillsHeight reports whether a card should stretch to the pane height
// (ladders and tables) rather than render at its natural height.
func fillsHeight(r *core.Result) bool {
	if r == nil {
		return false
	}
	switch r.Card {
	case core.CardBook, core.CardTrades, core.CardMarkets, core.CardFills, core.CardFunding, core.CardArena:
		return true
	}
	return false
}

// paneSkin is the chart skin for full-screen panes: the configured one, or
// paper on a light UI theme (the default setup-rush ground is near black).
func (m *Model) paneSkin() string {
	if m.chartSkin == "" && !m.th.Dark {
		return "paper"
	}
	return m.effSkin()
}

// chartBand is the chart pane's header band: the last price (or the
// crosshair candle's close) in big digits with the 24h change small on
// its baseline.
func (m *Model) chartBand(p *pane, d *core.ChartData) []string {
	px := d.Summary.Last
	if px == 0 {
		px = d.Candles[len(d.Candles)-1].C
	}
	small := ""
	if p.cursor >= 0 && p.vp.Count > 0 {
		// the crosshair candle's close and its change on the bar before
		i := p.vp.First + p.vp.Count - 1 - p.cursor
		if i >= 0 && i < len(d.Candles) {
			px = d.Candles[i].C
			if i > 0 && d.Candles[i-1].C != 0 {
				// labelled: the change over that one bar (15M), not 24H
				small = signedPct1(px/d.Candles[i-1].C-1) + "  " + strings.ToUpper(string(d.Interval))
			}
		}
	} else if d.Summary.Change24h != 0 {
		small = signedPct1(d.Summary.Change24h) + "  24H"
	}
	big := strings.ReplaceAll(cards.Px(px), ",", "")
	k := big + "|" + small + "|" + m.th.Name + itoa(int(m.profile))
	if p.bandKey != k {
		p.bandKey, p.band = k, m.paneBand(px, big, small)
	}
	return p.band
}

// maLegend is the chart pane's legend row: each overlay's label in its
// line color (MA20 MA50). Empty without overlays.
func (m *Model) maLegend(mas, emas []int) string {
	// the chart skin's overlay colors, so each label matches its line
	ma := m.th.MA
	if pal, ok := chart.PaletteByName(m.paneSkin()); ok {
		ma = pal.Theme(m.th).MA
	}
	var parts []string
	add := func(prefix string, ps []int) {
		for _, p := range ps {
			if len(parts) >= 3 {
				return
			}
			st := lipgloss.NewStyle()
			if c := ma[len(parts)%3].Resolve(m.profile); c != nil {
				st = st.Foreground(c)
			}
			parts = append(parts, st.Render(prefix+itoa(p)))
		}
	}
	add("MA", mas)
	add("EMA", emas)
	if len(parts) == 0 {
		return ""
	}
	return gutter + strings.Join(parts, "   ")
}
