package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/tui/cards"
)

// Dashboards: `/dash NAME [COIN]` opens a full-screen grid of tiles. Each
// tile is an ordinary tool call rendered as its card at the tile's size,
// and streams like a pane does. 1-9 opens a tile full screen, tab moves to
// the next dashboard.

// dashTile is one cell of a dashboard row: a slash command (without the
// slash; {coin} is the dashboard's coin) and its share of the row width.
type dashTile struct {
	cmd string
	w   int
}

// dashRow is one band of tiles and its share of the height.
type dashRow struct {
	h     int
	tiles []dashTile
}

type dashSpec struct {
	name, title, coin string
	rows              []dashRow
}

// dashboards are the built-in layouts, in tab order.
var dashboards = []dashSpec{
	{name: "markets", title: "markets", coin: "BTC", rows: []dashRow{
		{h: 3, tiles: []dashTile{{"chart {coin} 15m", 5}, {"book {coin} --depth 20", 2}}},
		{h: 2, tiles: []dashTile{{"trades {coin}", 2}, {"markets --sort volume", 3}, {"funding", 2}}},
	}},
	{name: "vaults", title: "vaults", rows: []dashRow{
		{h: 3, tiles: []dashTile{{"vaults", 4}, {"vault hlp", 3}}},
		{h: 2, tiles: []dashTile{{"compare hlp growi 22cap", 4}, {"discover", 3}}},
	}},
	{name: "hlp", title: "hlp", rows: []dashRow{
		{h: 3, tiles: []dashTile{{"hlp", 3}, {"arena --vaults hlp,growi,22cap,citadel", 4}}},
		{h: 2, tiles: []dashTile{{"index mavericks", 3}, {"fund", 4}}},
	}},
	{name: "strategies", title: "strategies", rows: []dashRow{
		{h: 3, tiles: []dashTile{{"backtest examples/pine/ema-cross.pine --coin BTC --days 90", 1}, {`backtest --thesis "long BTC, short DOGE FARTCOIN, rebalance 1w" --days 90`, 1}}},
		{h: 2, tiles: []dashTile{{"strat trend", 3}, {"preview buy {coin} --usd 100", 4}}},
	}, coin: "BTC"},
	{name: "research", title: "research", coin: "HYPE", rows: []dashRow{
		{h: 3, tiles: []dashTile{{"chart {coin} 1h", 5}, {"macro", 4}}},
		{h: 2, tiles: []dashTile{{"book {coin} --depth 20", 2}, {"trades {coin}", 2}, {"funding", 2}, {"markets xyz", 3}}},
	}},
}

func dashByName(name string) (dashSpec, int, bool) {
	for i, d := range dashboards {
		if strings.EqualFold(d.name, name) {
			return d, i, true
		}
	}
	return dashSpec{}, 0, false
}

func dashNames() []string {
	out := make([]string, len(dashboards))
	for i, d := range dashboards {
		out[i] = d.name
	}
	return out
}

// openDash pushes a dashboard pane and starts every tile's call.
func (m *Model) openDash(name, coin string) tea.Cmd {
	spec, _, ok := dashByName(name)
	if !ok {
		m.add(&entry{kind: entryInfo, text: m.choiceList("DASH", dashNames(), "")})
		return nil
	}
	if coin != "" {
		spec.coin = strings.ToUpper(coin)
	}
	if spec.coin == "" {
		spec.coin = "BTC"
	}
	paneSeq++
	p := &pane{kind: paneDash, seq: paneSeq, cursor: -1, dash: &spec}
	var cmds []tea.Cmd
	for _, row := range spec.rows {
		for _, tl := range row.tiles {
			t, cmd := m.newTile(strings.ReplaceAll(tl.cmd, "{coin}", spec.coin))
			p.tiles = append(p.tiles, t)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}
	if top := m.topPane(); top != nil && top.kind == paneDash {
		m.popPane() // tab replaces the dashboard rather than stacking them
	}
	m.pushPane(p)
	return tea.Batch(cmds...)
}

// newTile builds one tile and the job that fills it.
func (m *Model) newTile(cmd string) (*pane, tea.Cmd) {
	paneSeq++
	t := &pane{kind: paneCard, seq: paneSeq, cursor: -1, label: cmd}
	fields := splitArgs(cmd)
	if len(fields) == 0 || m.reg == nil {
		t.err = core.Errorf(core.CodeInternal, "no tool registry")
		return t, nil
	}
	tool, ok := m.reg.BySlash(strings.ToLower(fields[0]))
	if !ok {
		t.err = core.Errorf(core.CodeUsage, "unknown command %s", fields[0])
		return t, nil
	}
	raw, err := m.parse(tool, fields[1:])
	if err != nil {
		t.err = err
		return t, nil
	}
	t.tool, t.call, t.loading = tool, core.Call{Tool: tool.Name, Args: raw}, true
	return t, m.runJob(tool, t.call, target{pane: t})
}

// dashKey handles a dashboard's own keys: 1-9 open a tile, tab cycles.
func (m *Model) dashKey(p *pane, key string) tea.Cmd {
	switch key {
	case "tab", "shift+tab":
		_, i, _ := dashByName(p.dash.name)
		if key == "tab" {
			i = (i + 1) % len(dashboards)
		} else {
			i = (i + len(dashboards) - 1) % len(dashboards)
		}
		return m.openDash(dashboards[i].name, "")
	}
	if n, err := strconv.Atoi(key); err == nil && n >= 1 && n <= len(p.tiles) {
		if t := p.tiles[n-1]; t.res != nil {
			m.pushPane(newPane(t.res, 0, t.tool, t.call))
		}
	}
	return nil
}

// dashGap is the space between tiles: columns and rows.
const (
	dashColGap = 3
	dashRowGap = 1
)

// split divides total into parts proportional to weights, every part at
// least 1, the remainder going to the first parts.
func split(total int, weights []int) []int {
	sum := 0
	for _, w := range weights {
		sum += max(w, 1)
	}
	out := make([]int, len(weights))
	used := 0
	for i, w := range weights {
		out[i] = max(total*max(w, 1)/sum, 1)
		used += out[i]
	}
	for i := 0; used < total && len(out) > 0; i = (i + 1) % len(out) {
		out[i]++
		used++
	}
	// The 1-cell floor can overshoot a tiny total: take it back from the
	// largest parts.
	for used > total {
		big := 0
		for i := range out {
			if out[i] > out[big] {
				big = i
			}
		}
		if out[big] <= 1 {
			break
		}
		out[big]--
		used--
	}
	return out
}

// renderDashPane lays the tiles out in their rows: the grid fills the pane
// edge to edge (inside the 2-cell gutter), so nothing floats in space.
func (m *Model) renderDashPane(p *pane, width, height int) []string {
	inner := max(width-len(gutter)*2, 20)
	rows := p.dash.rows
	hw := make([]int, len(rows))
	for i, r := range rows {
		hw[i] = r.h
	}
	// one blank row above the grid and one below it, before the key row
	hs := split(max(height-dashRowGap*(len(rows)-1)-2, len(rows)), hw)
	out := []string{""}
	k := 0
	for ri, r := range rows {
		ww := make([]int, len(r.tiles))
		for i, t := range r.tiles {
			ww[i] = t.w
		}
		ws := split(inner-dashColGap*(len(r.tiles)-1), ww)
		blocks := make([][]string, len(r.tiles))
		for i := range r.tiles {
			blocks[i] = m.renderTile(p.tiles[k], ws[i], hs[ri])
			k++
		}
		for y := 0; y < hs[ri]; y++ {
			var b strings.Builder
			b.WriteString(gutter)
			for i := range blocks {
				if i > 0 {
					b.WriteString(strings.Repeat(" ", dashColGap))
				}
				b.WriteString(blocks[i][y])
			}
			out = append(out, b.String())
		}
		if ri < len(rows)-1 {
			for range dashRowGap {
				out = append(out, "")
			}
		}
	}
	return out
}

// renderTile draws one tile as exactly h lines of exactly w cells: its
// card at the tile's size, a skeleton while it loads, its error if it
// failed. Tiles carry no numbers: 1-9 open them in reading order.
func (m *Model) renderTile(t *pane, w, h int) []string {
	var lines []string
	// Cards inset themselves by cards.Inset on each side; the dashboard
	// already has its gutter and gaps, so render wider and drop the left
	// inset (the right one is cut with the tile's width).
	o := m.cardOpts(w+2*cards.Inset, false)
	o.Height = h
	uninset := func(s string) []string {
		ls := strings.Split(s, "\n")
		for i, l := range ls {
			ls[i] = ansi.TruncateLeft(l, cards.Inset, "")
		}
		return ls
	}
	switch {
	case t.res != nil:
		lines = uninset(cards.Render(t.res, o))
	case t.err != nil:
		lines = append([]string{m.st.dim.Render(strings.ToUpper(t.label))}, uninset(cards.Error(t.err, o))...)
	default:
		lines = []string{m.st.dim.Render(strings.ToUpper(t.label))}
		for i := 0; i < min(h-1, 6); i++ {
			lines = append(lines, m.st.dim.Render(strings.Repeat("─", max(w*(6-i%3)/8, 4))))
		}
	}
	// Cards open with a blank row in the transcript; a tile starts at its
	// title so every tile's first line aligns across the row.
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[0])) == "" {
		lines = lines[1:]
	}
	out := make([]string, h)
	for y := range out {
		s := ""
		if y < len(lines) {
			s = ansi.Truncate(lines[y], w, "")
		}
		out[y] = padRight(s, w)
		if lipgloss.Width(out[y]) > w {
			out[y] = ansi.Truncate(out[y], w, "")
		}
	}
	return out
}

// fmtRows is every tile command of a layout, for checks like "does it use
// the coin".
func fmtRows(d *dashSpec) string {
	var b strings.Builder
	for _, r := range d.rows {
		for _, t := range r.tiles {
			b.WriteString(t.cmd)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
