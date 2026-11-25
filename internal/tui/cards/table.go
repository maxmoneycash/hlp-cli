package cards

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// column is one table track. Every row shares the same fixed tracks so
// columns line up (Stageshow craft: "Columns line up").
type column struct {
	title string
	right bool // numbers align right
	keep  int  // priority when narrowing: lower is dropped first
	flex  bool // the one column that truncates to fit (names)
	minW  int  // flex minimum width
	mid   bool // truncate in the middle, keeping the distinguishing end
	name  bool // the header is a name (a vault, a benchmark): keeps its case
}

// cell is one styled table value. The style colors the text only.
type cell struct {
	s  string
	st lipgloss.Style
}

func (p *painter) c(s string) cell                      { return cell{s, p.st.text} }
func (p *painter) cm(s string) cell                     { return cell{s, p.st.muted} }
func (p *painter) cs(s string, v float64) cell          { return cell{s, p.sign(v)} }
func (p *painter) cst(s string, st lipgloss.Style) cell { return cell{s, st} }

const colGap = 2

// maxInlineCols is the most columns an inline (Compact) table shows.
const maxInlineCols = 6

func visible(vis []bool) int {
	n := 0
	for _, v := range vis {
		if v {
			n++
		}
	}
	return n
}

// table lays rows out under cols within width. mark, when non-nil, returns
// a one-cell gutter mark for row i (curated dot, selection bar); a header
// row is drawn in muted text. Columns are dropped lowest-keep first until
// the table fits; the flex column then truncates with an ellipsis.
func (p *painter) table(cols []column, rows [][]cell, width int, mark func(i int) string) []string {
	if len(cols) == 0 {
		return nil
	}
	gutter := 0
	if mark != nil {
		gutter = 2
	}
	w := make([]int, len(cols))
	for i, c := range cols {
		w[i] = lipgloss.Width(c.title)
		for _, r := range rows {
			if i < len(r) {
				w[i] = max(w[i], lipgloss.Width(r[i].s))
			}
		}
	}
	vis := make([]bool, len(cols))
	for i := range vis {
		vis[i] = true
	}
	total := func() int {
		t, n := gutter, 0
		for i := range cols {
			if vis[i] {
				t += w[i]
				n++
			}
		}
		return t + colGap*max(n-1, 0)
	}
	flex := -1
	for i, c := range cols {
		if c.flex {
			flex = i
		}
	}
	natural := 0
	if flex >= 0 {
		natural = w[flex]
	}
	floor := func() int { return max(cols[flex].minW, lipgloss.Width(cols[flex].title)) }
	// comfy is the name width we defend by dropping minor columns first.
	comfy := 0
	if flex >= 0 {
		comfy = min(natural, max(floor(), 20))
	}
	shrinkTo := func(limit int) {
		if flex < 0 {
			return
		}
		if over := total() - width; over > 0 && w[flex] > limit {
			w[flex] = max(limit, w[flex]-over)
		}
	}
	drop := func(maxKeep int) bool {
		idx, lowest := -1, 1<<30
		for i, c := range cols {
			if vis[i] && !c.flex && c.keep < lowest && c.keep < maxKeep {
				idx, lowest = i, c.keep
			}
		}
		if idx < 0 {
			return false
		}
		vis[idx] = false
		return true
	}
	// 1. Trim long names to a comfortable width.
	shrinkTo(comfy)
	// 2. Drop minor columns (keep < 50) rather than cut names further.
	for total() > width && drop(50) {
	}
	// 3. Cut names down to their minimum.
	if flex >= 0 {
		shrinkTo(floor())
	}
	// 4. Drop whatever else it takes.
	for total() > width && drop(1<<30) {
	}
	// 5. An inline card shows at most 6 columns (DESIGN.md section 5.1
	// rule 9); the pane shows every one.
	if p.o.Compact {
		for visible(vis) > maxInlineCols && drop(1<<30) {
		}
	}
	// A flex column that was shrunk early may regain room after drops.
	if flex >= 0 {
		if slack := width - total(); slack > 0 && w[flex] < natural {
			w[flex] = min(natural, w[flex]+slack)
		}
	}

	for i := range w {
		w[i] = max(w[i], 0)
	}

	var bandBG color.Color
	if p.band != nil && !p.o.Plain {
		bandBG = p.th.Surface.Resolve(p.o.Profile)
	}
	tw := total()
	render := func(cs []cell, header bool, i int) string {
		if !header && bandBG != nil && p.band(i) {
			return p.bandRow(cs, cols, vis, w, mark, i, tw, bandBG)
		}
		var b strings.Builder
		if mark != nil {
			if header {
				b.WriteString("  ")
			} else {
				m := mark(i)
				if m == "" {
					m = " "
				}
				b.WriteString(m)
				b.WriteByte(' ')
			}
		}
		first := true
		for ci, col := range cols {
			if !vis[ci] || w[ci] <= 0 {
				continue
			}
			if !first {
				b.WriteString(strings.Repeat(" ", colGap))
			}
			first = false
			var cl cell
			if ci < len(cs) {
				cl = cs[ci]
			}
			txt := termSafe(cl.s)
			if lipgloss.Width(txt) > w[ci] {
				if col.mid && w[ci] >= 9 {
					txt = midTruncate(txt, w[ci])
				} else {
					txt = ansi.Truncate(txt, w[ci], "…")
				}
			}
			pad := strings.Repeat(" ", max(w[ci]-lipgloss.Width(txt), 0))
			styled := cl.st.Render(txt)
			if txt == "" {
				styled = ""
			}
			if col.right {
				b.WriteString(pad + styled)
			} else {
				b.WriteString(styled + pad)
			}
		}
		return strings.TrimRight(b.String(), " ")
	}
	hdr := make([]cell, len(cols))
	for i, c := range cols {
		t := c.title
		if !c.name {
			t = strings.ToUpper(t)
		}
		hdr[i] = cell{t, p.st.label}
	}
	out := []string{render(hdr, true, -1)}
	for i, r := range rows {
		out = append(out, render(r, false, i))
	}
	return out
}

// bandRow renders one table row on a background band that runs the full
// table width (pads and gaps included), so a selection reads as a row,
// not as colored words.
func (p *painter) bandRow(cs []cell, cols []column, vis []bool, w []int, mark func(int) string, i, width int, bg color.Color) string {
	on := func(st lipgloss.Style) lipgloss.Style { return st.Background(bg) }
	fill := lipgloss.NewStyle().Background(bg)
	var b strings.Builder
	used := 0
	if mark != nil {
		m := mark(i)
		if m == "" {
			m = " "
		}
		b.WriteString(m + fill.Render(" "))
		used += 2
	}
	first := true
	for ci, col := range cols {
		if !vis[ci] || w[ci] <= 0 {
			continue
		}
		if !first {
			b.WriteString(fill.Render(strings.Repeat(" ", colGap)))
			used += colGap
		}
		first = false
		var cl cell
		if ci < len(cs) {
			cl = cs[ci]
		}
		txt := termSafe(cl.s)
		if lipgloss.Width(txt) > w[ci] {
			if col.mid && w[ci] >= 9 {
				txt = midTruncate(txt, w[ci])
			} else {
				txt = ansi.Truncate(txt, w[ci], "…")
			}
		}
		pad := fill.Render(strings.Repeat(" ", max(w[ci]-lipgloss.Width(txt), 0)))
		styled := on(cl.st).Render(txt)
		if col.right {
			b.WriteString(pad + styled)
		} else {
			b.WriteString(styled + pad)
		}
		used += w[ci]
	}
	if used < width {
		b.WriteString(fill.Render(strings.Repeat(" ", width-used)))
	}
	return b.String()
}

// midTruncate cuts s to w cells with the ellipsis in the middle, so names
// that share a prefix ("Systemic Strategies HyperGrowth" and "… Alpha")
// stay apart.
func midTruncate(s string, w int) string {
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	head := (w - 1) * 3 / 5
	tail := w - 1 - head
	h, t := string(r[:head]), string(r[len(r)-tail:])
	// Cut on word boundaries when that costs only a few cells.
	if i := strings.LastIndexByte(h, ' '); i > 0 && len([]rune(h[i:])) <= 5 {
		h = h[:i]
	}
	if i := strings.IndexByte(t, ' '); i >= 0 && len([]rune(t[:i])) <= 5 {
		t = t[i+1:]
	}
	return strings.TrimRight(h, " &|-") + " … " + strings.TrimLeft(t, " ")
}

// kvLines renders aligned "label  value" pairs (labels muted).
func (p *painter) kvLines(pairs [][2]string, width int) []string {
	lw := 0
	for _, kv := range pairs {
		lw = max(lw, lipgloss.Width(kv[0]))
	}
	lw = min(lw, max(width/3, 8))
	out := make([]string, 0, len(pairs))
	for _, kv := range pairs {
		k := kv[0]
		if lipgloss.Width(k) > lw {
			k = ansi.Truncate(k, lw, "…")
		}
		line := p.lbl(k) + strings.Repeat(" ", max(lw-lipgloss.Width(k), 0)+2) + p.st.text.Render(kv[1])
		out = append(out, line)
	}
	return wrapLines(out, width)
}
