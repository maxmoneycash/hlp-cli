package tui

import (
	"sort"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/tui/cards"
	"github.com/maxmoneycash/hlp-cli/internal/tui/glyph"
)

// View implements tea.Model.
func (m *Model) View() tea.View {
	if m.frame == nil {
		m.setFrame(m.render())
	}
	v := tea.NewView(strings.Join(m.frame, "\n")) // one element: no copy
	v.AltScreen = true
	v.ReportFocus = true
	v.WindowTitle = "hlp"
	// Rush paints its own ground (#131315) so the page is the landing's,
	// not the terminal's; any theme of the other polarity than the
	// terminal does the same so its gray text is not on the wrong ground.
	// Bubble Tea restores both colors on exit (OSC 11/10).
	if m.paintsBackground() {
		v.BackgroundColor = m.th.BG.Resolve(colorprofile.TrueColor)
		v.ForegroundColor = m.th.Text.Resolve(colorprofile.TrueColor)
	}
	v.MouseMode = tea.MouseModeAllMotion // hover moves the reticle, drives the crosshair
	return v
}

// themeMismatch reports whether the theme's polarity differs from the
// terminal background the terminal reported.
func (m *Model) themeMismatch() bool {
	return m.termBG && m.th.Dark != m.termDark
}

// paintsBackground reports whether View sets the terminal ground.
func (m *Model) paintsBackground() bool {
	if m.themeMismatch() {
		return true
	}
	// rush and paper paint their own ground (a theme paired with a chart
	// skin), so a paper chart is never a beige box on a dark page.
	return m.th.ChartSkin != "" && m.ui().PaintBackground && m.profile != colorprofile.NoTTY
}

// sideWidth is the watchlist column width (shown with ctrl+b on wide terminals).
const sideWidth = 30

// sideMinWidth: the side panel needs a wide terminal (DESIGN.md 6.3).
const sideMinWidth = 120

// gutter is the 2-cell inset every row of content keeps from the left
// edge (DESIGN.md 6.1). The reticle's left corners sit in it.
const gutter = "  "

// render draws the whole screen as exactly height lines of at most width cells.
//
// Transcript: top bar, the transcript (it opens and closes with a blank
// row), the hairline, the prompt row with its context keys on the right.
// Pane: top bar, the pane body, one key row.
func (m *Model) render() []string {
	W, H := m.width, m.height
	out := make([]string, 0, H)
	out = append(out, m.statusBar(W))

	if p := m.topPane(); p != nil {
		var body []string
		chartPane := p.kind == paneChart && !p.showKeys
		switch {
		case p.showKeys:
			body = m.paneKeyList(p, W)
		case chartPane:
			// one blank row between the time axis and the key row, so the
			// keys never read as axis labels
			body = append(m.renderPane(p, W, H-3), "")
		default:
			body = m.renderPane(p, W, H-2)
		}
		if p.kind == paneChart && p.err == nil && !p.showKeys {
			// Chart lines are drawn inside W: no need to measure them.
			out = append(out, fitH(body, H-2)...)
		} else {
			out = append(out, fit(body, W, H-2)...)
		}
		return append(out, m.paneKeyRow(p, W))
	}

	var bottom []string
	if m.confirm != nil {
		bottom = m.confirmView(W)
	} else {
		bottom = append(bottom, m.completionLines(W)...)
		bottom = append(bottom, m.st.faint.Render(strings.Repeat(glyph.Rule, W)))
		in := m.inputLines(W)
		if m.palette != nil {
			in = []string{m.withKeys(gutter+m.st.dim.Render(glyph.Prompt), W)} // the palette owns input
		}
		if len(in) > 6 {
			in = in[len(in)-6:]
		}
		bottom = append(bottom, in...)
	}
	mh := max(H-1-len(bottom), 1)

	tw := m.transcriptWidth()
	side := tw != W
	var middle []string
	switch {
	case m.showHelp:
		middle = fit(append([]string{""}, m.helpLines(tw, mh-1)...), tw, mh)
	case m.palette != nil:
		middle = fit(append([]string{""}, m.paletteLines(tw, mh-1)...), tw, mh)
	case m.welcomeOnly():
		middle = m.welcomeScreen(tw, mh)
	default:
		// Transcript lines are fitted once per entry (entry.rl).
		all := m.layoutTranscript(tw)
		m.scroll = min(m.scroll, max(len(all)-mh, 0))
		lo, hi := visibleRange(len(all), mh, m.scroll)
		m.viewLo, m.viewHi = lo, hi
		middle = fitH(all[lo:hi], mh)
		m.floorTranscript(middle, lo, tw)
	}
	if side {
		sl := fit(m.sideLines(sideWidth), sideWidth, mh)
		rule := m.st.faint.Render("│")
		for i := range middle {
			middle[i] = padRight(middle[i], tw) + rule + sl[i]
		}
	}
	out = append(out, middle...)
	out = append(out, fit(bottom, W, len(bottom))...)
	return out
}

// fitH pads or cuts lines already known to fit the width to exactly h.
func fitH(lines []string, h int) []string {
	out := make([]string, h)
	copy(out, lines)
	return out
}

// fit truncates every line to w and pads/cuts to exactly h lines.
func fit(lines []string, w, h int) []string {
	out := make([]string, h)
	for i := 0; i < h; i++ {
		if i < len(lines) {
			out[i] = truncate(lines[i], w)
		}
	}
	return out
}

func padRight(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// statusBar is the one line of chrome at the top (DESIGN.md 5.2 Top bar):
// `hlp` in bold ink, a gold TESTNET pill only on testnet, the pane name in
// a pane; on the right the flap ticker (transcript only) and the live mark.
// Nothing else talks here: no network word, account, spinner or model.
func (m *Model) statusBar(W int) string {
	left := gutter + m.st.brand.Render("hlp")
	if m.env != nil && m.env.Config.Network == domain.Testnet {
		left += "  " + m.st.warnPill.Render("TESTNET")
	}
	if p := m.topPane(); p != nil {
		if name := paneName(p); name != "" {
			left += "  " + m.st.dim.Render(glyph.Crumb) + " " + m.st.text.Render(strings.ToUpper(name))
		}
	}
	mark := m.liveMark()
	right := mark + gutter
	if m.tickerShown() {
		avail := W - lipgloss.Width(left) - lipgloss.Width(right) - 4
		if t := m.ticker(avail); t != "" {
			right = t + "    " + right
		}
	}
	return padBetween(left, right, W+1)
}

// ticker is the split-flap market strip, items dropped from the right when
// they do not fit (never cut). Memoized on what it shows.
func (m *Model) ticker(avail int) string {
	now := m.now()
	busy := false
	for _, f := range m.flaps {
		if f.Busy(now) {
			busy = true
			break
		}
	}
	var key string
	if !busy {
		var b strings.Builder
		b.WriteString(itoa(avail))
		b.WriteString(m.th.Name)
		b.WriteString(itoa(int(m.profile)))
		for _, c := range m.welcomeCoins() {
			b.WriteString("|")
			if f := m.flaps[c]; f != nil {
				b.WriteString(f.Text())
			} else {
				b.WriteString(m.tickerText(c))
			}
		}
		key = b.String()
		if key == m.tickerKey {
			return m.tickerLine
		}
	}
	out, used := "", 0
	for _, c := range m.welcomeCoins() {
		it := m.tickerItem(c, now)
		if it == "" {
			continue
		}
		w := lipgloss.Width(it)
		if used > 0 {
			w += 4
		}
		if used+w > avail {
			break
		}
		if used > 0 {
			out += "    "
		}
		out += it
		used += w
	}
	if !busy {
		m.tickerKey, m.tickerLine = key, out
	}
	return out
}

// liveMark is the stream state as one mark: ■ Accent live, ■ Dim
// connecting, □ Dim idle, paused or offline.
func (m *Model) liveMark() string {
	off := m.st.dim.Render(glyph.Off)
	if !m.focused || m.env == nil || m.env.Stream == nil {
		return off
	}
	switch m.wsState {
	case "live":
		return m.st.accent.Render(glyph.On)
	case "connecting", "reconnecting":
		return m.st.dim.Render(glyph.On)
	case "":
		if len(m.live) > 0 {
			return m.st.dim.Render(glyph.On)
		}
	}
	return off
}

func paneName(p *pane) string {
	switch p.kind {
	case paneChart:
		if d, ok := asChart(p.res.Data); ok {
			return d.Coin + " " + string(d.Interval)
		}
		return "chart"
	case paneVaults:
		return "vaults " + itoa(len(p.view)) // the count lives in the crumb: said once
	case paneDash:
		if p.dash.coin != "" && strings.Contains(fmtRows(p.dash), "{coin}") {
			return p.dash.title + " " + p.dash.coin
		}
		return p.dash.title
	}
	if p.res != nil && p.res.Title != "" {
		return p.res.Title
	}
	if p.res != nil {
		return string(p.res.Card)
	}
	return ""
}

// contextKeys are the few keys that matter now, shown Dim at the right end
// of the prompt row: `? keys` idle, `esc stop` while a job runs, `tab esc`
// with completions open.
func (m *Model) contextKeys() string {
	switch {
	case m.ctrlC:
		return m.st.warn.Render("ctrl+c again to quit")
	case m.confirm != nil:
		return m.st.dim.Render("←→  enter  esc")
	case m.palette != nil:
		return m.st.dim.Render("↑↓  enter  esc")
	case m.showHelp:
		return m.st.dim.Render("esc")
	case len(m.jobs) > 0 || m.agentStop != nil:
		return m.st.dim.Render("esc stop")
	case len(m.completions()) > 0:
		return m.st.dim.Render("tab  esc")
	}
	return m.st.dim.Render("? keys")
}

// withKeys right-aligns the context keys on a prompt row.
func (m *Model) withKeys(row string, W int) string {
	return padBetween(row, m.contextKeys()+gutter, W+1)
}

// paneHints are a pane's keys in priority order; the key row shows the
// first three, `?` lists them all.
func (m *Model) paneHints(p *pane) [][2]string {
	switch p.kind {
	case paneChart:
		return [][2]string{{"esc", "back"}, {"+/-", "zoom"}, {"1-9", "interval"}, {"h/l", "pan"}, {"c", "crosshair"}, {"s", "style"}, {"i", "MAs"}, {"L", "log"}, {"0", "reset"}}
	case paneVaults:
		if p.filtering {
			return [][2]string{{"type", "filter"}, {"enter", "keep"}, {"esc", "clear"}}
		}
		return [][2]string{{"esc", "back"}, {"/", "filter"}, {"s", "sort"}, {"↑↓", "select"}, {"enter", "tearsheet"}, {"r", "reverse"}}
	case paneDash:
		return [][2]string{{"esc", "back"}, {"1-9", "open"}, {"tab", "next"}}
	}
	return [][2]string{{"esc", "back"}, {"i", "info"}, {"↑↓", "scroll"}, {"g/G", "top/bottom"}}
}

// paneKeyRow is a pane's one row of keys: three hints and `? keys`.
func (m *Model) paneKeyRow(p *pane, W int) string {
	if m.ctrlC {
		return gutter + m.st.warn.Render("ctrl+c again to quit")
	}
	hints := m.paneHints(p)
	if p.showKeys {
		hints = [][2]string{{"any key", "close"}}
	} else if !p.filtering {
		hints = append(hints[:min(3, len(hints)):min(3, len(hints))], [2]string{"?", "keys"})
	}
	key := strings.Join(flatHints(hints), "\x00") + "|" + itoa(W) + m.th.Name + itoa(int(m.profile))
	if p.footKey == key {
		return p.foot
	}
	p.foot, p.footKey = m.hintBar(hints, W), key
	return p.foot
}

func flatHints(h [][2]string) []string {
	out := make([]string, 0, 2*len(h))
	for _, x := range h {
		out = append(out, x[0], x[1])
	}
	return out
}

// paneKeyList is `?` in a pane: the pane's full key list.
func (m *Model) paneKeyList(p *pane, W int) []string {
	out := []string{"", gutter + m.st.label.Render("KEYS")}
	for _, h := range m.paneHints(p) {
		out = append(out, gutter+padRight(m.st.text.Render(h[0]), 10)+m.st.muted.Render(h[1]))
	}
	return out
}

// hintBar lays out key hints given in priority order: whole items are
// dropped from the end until the bar fits, never cut inside one.
func (m *Model) hintBar(hints [][2]string, W int) string {
	key := strings.Join(flatHints(hints), "\x00") + "|" + itoa(W) + m.th.Name + itoa(int(m.profile))
	hintMu.Lock()
	if s, ok := hintMemo[key]; ok {
		hintMu.Unlock()
		return s
	}
	hintMu.Unlock()
	s := m.buildHintBar(hints, W)
	hintMu.Lock()
	if len(hintMemo) > 128 {
		clear(hintMemo)
	}
	hintMemo[key] = s
	hintMu.Unlock()
	return s
}

// hintMemo caches hint bars: they depend only on the hints, the width and
// the look, and are drawn on every frame (PERF #5).
var (
	hintMu   sync.Mutex
	hintMemo = map[string]string{}
)

func (m *Model) buildHintBar(hints [][2]string, W int) string {
	sep := "   "
	out, used := gutter, len(gutter)
	for i, h := range hints {
		part := m.st.muted.Render(h[0]) + " " + m.st.dim.Render(h[1])
		w := lipgloss.Width(part)
		if i > 0 {
			w += len(sep)
		}
		if used+w > W {
			break
		}
		if i > 0 {
			out += sep
		}
		out += part
		used += w
	}
	return out
}

// inputLines renders the prompt with its ghost argument hint and, on the
// first row, the context keys.
func (m *Model) inputLines(W int) []string {
	placeholder := ""
	if m.welcomeOnly() && !m.showHelp && m.palette == nil {
		placeholder = "ask or /"
	}
	focused := m.confirm == nil && m.palette == nil
	cur := m.st.cursor
	if !m.cursorOn() {
		cur = m.st.text // the blink's off phase: the cell shows its own rune
	}
	keysW := lipgloss.Width(m.contextKeys()) + len(gutter) + 2
	lines := m.in.View(max(W-len(gutter)-keysW, 8), glyph.Prompt, placeholder, m.st.accent, m.st.text, m.st.dim, cur, focused)
	for i := range lines {
		lines[i] = gutter + lines[i]
	}
	// Ghost hint for the remaining positional args ("/chart BTC " -> <interval>).
	v := m.in.Value()
	if strings.HasPrefix(v, "/") && !m.in.Multiline() && len(lines) == 1 {
		f := splitArgs(strings.TrimPrefix(v, "/"))
		if len(f) > 0 {
			if c, ok := findCommand(m.cmds, f[0]); ok {
				if h := argHint(c, len(f)-1, strings.HasSuffix(v, " ")); h != "" {
					lines[0] += m.st.dim.Render(h)
				} else if _, rest, ok := tagCompletion(m.env, v); ok {
					lines[0] += m.st.dim.Render(rest)
				}
			}
		}
	}
	lines[0] = m.withKeys(lines[0], W)
	return lines
}

// completionRows is how many commands the autocomplete shows at once.
const completionRows = 5

// completionLines lists matching commands above the hairline: up to five
// rows of `/name` and a caption of four words or fewer; the selected row
// carries the ▌ bar, bold name and its argument hint (DESIGN.md 5.2).
func (m *Model) completionLines(W int) []string {
	comp := m.completions()
	if len(comp) == 0 {
		return nil
	}
	sel := min(m.compSel, len(comp)-1)
	start := 0
	if sel >= completionRows {
		start = sel - completionRows + 1
	}
	end := min(len(comp), start+completionRows)
	nameW := 0
	for i, c := range comp[start:end] {
		w := lipgloss.Width("/" + c.slash)
		if start+i == sel && c.args != "" {
			w += 1 + lipgloss.Width(c.args)
		}
		nameW = max(nameW, w)
	}
	nameW = min(nameW+3, W/2)
	out := make([]string, 0, end-start+1)
	for i := start; i < end; i++ {
		c := comp[i]
		mark, cell := "  ", m.st.text.Render("/"+c.slash)
		if i == sel {
			mark = m.st.accent.Render(glyph.Sel) + " "
			cell = m.st.sel.Render("/" + c.slash)
			if c.args != "" {
				cell += " " + m.st.dim.Render(c.args)
			}
		}
		lead := mark + padRight(truncate(cell, nameW), nameW)
		out = append(out, truncate(lead+m.st.muted.Render(caption(c)), W))
	}
	if len(comp) > end {
		out = append(out, gutter+m.st.dim.Render("+"+itoa(len(comp)-end)))
	}
	return out
}

// helpKeys is the KEYS column of the help overlay.
var helpKeys = [][2]string{
	{"enter", "send/open"},
	{"tab", "complete"},
	{"↑↓", "history"},
	{"pgup", "scroll"},
	{"ctrl+o", "open"},
	{"ctrl+k", "palette"},
	{"ctrl+b", "side"},
	{"ctrl+t", "theme"},
	{"esc", "back"},
}

// helpLines is the ? overlay in two columns: KEYS on the left, COMMANDS by
// group on the right, then the model and the tips already shown.
func (m *Model) helpLines(W, H int) []string {
	const keyW = 9
	left := []string{gutter + m.st.label.Render("KEYS")}
	for _, k := range helpKeys {
		left = append(left, gutter+padRight(m.st.text.Render(k[0]), keyW)+m.st.muted.Render(k[1]))
	}
	leftW := len(gutter) + keyW + 12
	if W < 64 {
		leftW = 0 // one column: commands under keys
	}
	rightW := max(W-leftW-2, 16)

	groups := map[string][]command{}
	var order []string
	for _, c := range m.cmds {
		if _, ok := groups[c.group]; !ok {
			order = append(order, c.group)
		}
		groups[c.group] = append(groups[c.group], c)
	}
	rank := map[string]int{"market": 0, "vault": 1, "account": 2, "strategy": 3, "trade": 4, "meta": 5, "shell": 6}
	sort.SliceStable(order, func(i, j int) bool { return rank[order[i]] < rank[order[j]] })
	const groupW = 10
	right := []string{m.st.label.Render("COMMANDS")}
	for _, g := range order {
		lead := padRight(m.st.label.Render(strings.ToUpper(g)), groupW)
		cur, curW := "", 0
		for _, c := range groups[g] {
			n := "/" + c.slash
			if curW > 0 && groupW+curW+2+len(n) > rightW {
				right = append(right, lead+cur)
				lead, cur, curW = strings.Repeat(" ", groupW), "", 0
			}
			if curW > 0 {
				cur += "  "
				curW += 2
			}
			cur += m.st.text.Render(n)
			curW += lipgloss.Width(n)
		}
		right = append(right, lead+cur)
	}

	var out []string
	if leftW == 0 {
		out = append(append(left, ""), func() []string {
			r := make([]string, len(right))
			for i, l := range right {
				r[i] = gutter + l
			}
			return r
		}()...)
	} else {
		n := max(len(left), len(right))
		out = make([]string, n)
		for i := 0; i < n; i++ {
			l, r := "", ""
			if i < len(left) {
				l = left[i]
			}
			if i < len(right) {
				r = right[i]
			}
			out[i] = padRight(l, leftW) + r
		}
	}
	model := m.st.text.Render("none")
	if a := m.opts.Agent; a != nil && a.Provider != nil {
		model = m.st.text.Render(a.Provider.Name())
	}
	foot := gutter + m.st.label.Render("MODEL") + "  " + model
	if tips := m.seen.List(); len(tips) > 0 {
		var t []string
		for _, k := range tips {
			if s, ok := tipText[k]; ok {
				t = append(t, s)
			}
		}
		if len(t) > 0 {
			foot += "    " + m.st.label.Render("TIPS") + "  " + m.st.muted.Render(strings.Join(t, "   "))
		}
	}
	out = append(out, "", foot)
	if H > 0 && len(out) > H {
		out = out[:H]
	}
	return out
}

// paletteLines is the ctrl+k overlay: a filter row and the ranked list.
func (m *Model) paletteLines(W, H int) []string {
	p := m.palette
	list := matchCommands(m.cmds, p.ed.Value())
	out := []string{}
	for _, l := range p.ed.View(W-4, glyph.Crumb, "", m.st.accent, m.st.text, m.st.dim, m.st.cursor, true) {
		out = append(out, gutter+l)
	}
	out = append(out, "")
	rows := max(H-len(out), 1)
	sel := min(p.sel, max(len(list)-1, 0))
	start := 0
	if sel >= rows {
		start = sel - rows + 1
	}
	for i := start; i < min(len(list), start+rows); i++ {
		c := list[i]
		mark, st := "  ", m.st.text
		if i == sel {
			mark, st = m.st.accent.Render(glyph.Sel)+" ", m.st.sel
		}
		out = append(out, mark+padRight(st.Render("/"+c.slash), 16)+m.st.muted.Render(caption(c)))
	}
	if len(list) == 0 {
		out = append(out, gutter+m.st.dim.Render("NO MATCH"))
	}
	return out
}

// sideCoins are the watchlist column's coins.
func (m *Model) sideCoins() []string {
	if m.env != nil && len(m.env.Config.Pinned) > 0 {
		return m.env.Config.Pinned
	}
	return []string{"BTC", "ETH", "HYPE", "SOL"}
}

// sideLines is the ctrl+b column: WATCH rows, the configured account's
// POSITIONS and EQUITY. No hint lines; the top bar's mark says offline.
func (m *Model) sideLines(w int) []string {
	out := []string{"", gutter + m.st.label.Render("WATCH")}
	for _, c := range m.sideCoins() {
		px, ok := m.mids[c]
		val := m.st.dim.Render(glyph.Off)
		if ok {
			val = m.st.text.Render(cards.PxFixed(px))
			if pd := m.prevDay[c]; pd > 0 {
				d := px/pd - 1
				st := m.st.text
				if d < 0 {
					st = m.st.down
				}
				val += " " + st.Render(padLeft(signedPct1(d), 6))
			}
		}
		out = append(out, padBetween(gutter+m.st.muted.Render(strings.TrimPrefix(c, "xyz:")), val+gutter, w+1))
	}
	if m.env != nil && m.env.Config.Account != "" && m.acct != nil {
		if len(m.acct.Positions) > 0 {
			out = append(out, "", gutter+m.st.label.Render("POSITIONS"))
			for _, p := range m.acct.Positions {
				st := m.st.text
				if p.UPnL < 0 {
					st = m.st.down
				}
				out = append(out, padBetween(gutter+m.st.muted.Render(p.Coin), st.Render(signedUSD(p.UPnL))+gutter, w+1))
			}
		}
		out = append(out, "", padBetween(gutter+m.st.label.Render("EQUITY"), m.st.text.Render(cards.USD(m.acct.Equity))+gutter, w+1))
	}
	return out
}

// signedUSD is cards.SignedUSD with U+2212 for a loss.
func signedUSD(v float64) string {
	s := cards.SignedUSD(v)
	if strings.HasPrefix(s, "-") {
		return glyph.Minus + s[1:]
	}
	return s
}

func padLeft(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return strings.Repeat(" ", w-n) + s
	}
	return s
}
