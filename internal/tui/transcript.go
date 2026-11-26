package tui

import (
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/tui/cards"
	"github.com/maxmoneycash/hlp-cli/internal/tui/glyph"
)

type entryKind int

const (
	entryWelcome   entryKind = iota
	entryUser                // the echoed prompt line
	entryPending             // skeleton while a tool runs
	entryCard                // a tool Result
	entryError               // a failed tool or a usage error
	entryInfo                // suggestions, notices
	entryAssistant           // streamed model text (markdown)
)

// entry is one transcript item. Rendered output is cached per width/theme.
type entry struct {
	id      int
	kind    entryKind
	text    string
	title   string
	result  *core.Result
	err     error
	tool    core.Tool
	call    core.Call
	start   time.Time
	rows    int    // skeleton rows
	state   string // entryInfo: a card that ended without a result ("□ STOPPED")
	pendKey string

	cache  string
	cacheW int
	lines  int
	top    int // first transcript line (set by layoutTranscript)

	// rl is cache split into lines, each already fitted to cacheW, so a
	// frame neither re-splits nor re-measures the transcript (PERF #5/#6).
	rl    []string
	rlSrc string
	rlW   int // widest line of rl (where the floor may start beside it)

	// Streaming assistant text (PERF #2): markdown is rendered at most
	// every mdEvery; the tail since then shows as wrapped plain text.
	streaming bool
	mdAt      time.Time
	mdLen     int
	mdW       int
	mdLines   []string
	tailFrom  int      // text[mdLen:tailFrom] is wrapped in tailLines
	tailLines []string // styled, complete lines of the plain tail

	// slim: the result's bulk (candles, series) was dropped to bound
	// memory; slimText is the card as last rendered, reused until the
	// card is opened again (which re-runs its call).
	slim     bool
	slimText string
}

// Transcript bounds (PERFORMANCE #6): at most maxEntries entries (about 7k
// lines), and cards more than slimAfter cards behind the newest keep only
// their rendered text.
const (
	maxEntries = 500
	slimAfter  = 20
)

// mdEvery throttles markdown renders of a streaming reply.
const mdEvery = 150 * time.Millisecond

func (m *Model) add(e *entry) *entry {
	if e.kind != entryWelcome {
		m.dropWelcome()
	}
	m.nextID++
	e.id = m.nextID
	m.entries = append(m.entries, e)
	m.scroll = 0 // new output pins the view to the bottom
	m.boundTranscript()
	return e
}

// boundTranscript drops the oldest entries past maxEntries (cancelling
// their live feeds) and slims cards far behind the newest.
func (m *Model) boundTranscript() {
	if n := len(m.entries); n > maxEntries {
		drop := n - maxEntries
		for _, old := range m.entries[:drop] {
			key := "entry:" + strconv.Itoa(old.id)
			if s := m.live[key]; s != nil {
				s.cancel()
				delete(m.live, key)
			}
		}
		m.entries = append(m.entries[:0:0], m.entries[drop:]...)
	}
	cardsSeen := 0
	for i := len(m.entries) - 1; i >= 0; i-- {
		e := m.entries[i]
		if e.kind != entryCard {
			continue
		}
		cardsSeen++
		if cardsSeen <= slimAfter {
			continue
		}
		if e.slim {
			break // everything older is already slim
		}
		m.slimEntry(e)
	}
}

// slimEntry keeps a card's rendered text and drops its result's bulk.
func (m *Model) slimEntry(e *entry) {
	if e.result == nil || e.kind != entryCard {
		return
	}
	if e.cache == "" {
		m.renderEntry(e, max(e.cacheW, m.transcriptWidth()))
	}
	e.slim, e.slimText = true, e.cache
	e.result = slimResult(e.result)
}

// slimResult copies r without its large slices (chart candles, series).
func slimResult(r *core.Result) *core.Result {
	if r == nil {
		return nil
	}
	nr := *r
	if d, ok := asChart(r.Data); ok {
		nd := *d
		nd.Candles, nd.Lines = nil, nil
		nr.Data = &nd
		return &nr
	}
	nr.Data = nil
	return &nr
}

// transcriptWidth is the width layoutTranscript uses.
func (m *Model) transcriptWidth() int {
	if m.showSide && m.width >= sideMinWidth {
		return m.width - sideWidth - 1
	}
	return m.width
}

// endStream finishes a streaming reply: one final full markdown render.
func (e *entry) endStream() {
	if e != nil && e.streaming {
		e.streaming, e.cache, e.mdLines = false, "", nil
	}
}

func (m *Model) entryByID(id int) *entry {
	for _, e := range m.entries {
		if e.id == id {
			return e
		}
	}
	return nil
}

func (m *Model) addWelcome() {
	m.add(&entry{kind: entryWelcome})
}

// dropWelcome removes the welcome entry: it is an empty state, not
// history, and leaves when the first real entry lands.
func (m *Model) dropWelcome() {
	if len(m.entries) > 0 && m.entries[0].kind == entryWelcome {
		m.entries = append(m.entries[:0:0], m.entries[1:]...)
	}
}

// maxCardWidth: transcript cards are at most 100 columns plus the 2-cell
// inset each side (DESIGN.md 6.1); wide terminals get more empty space on
// the right, not wider cards.
const maxCardWidth = 100 + 4

// wideCard reports whether a card is led by a chart (the arena race and a
// paper equity curve included) and so takes the full transcript width
// instead of maxCardWidth; tables keep their content width.
func wideCard(r *core.Result) bool {
	if r == nil {
		return false
	}
	switch r.Card {
	case core.CardChart, core.CardVault, core.CardBacktest, core.CardCompare, core.CardArena, core.CardPaper:
		return true
	}
	return false
}

// proseWidth bounds agent prose (72 columns of text plus the gutter).
const proseWidth = 72 + 4

// renderEntry renders one transcript item at width (cached).
func (m *Model) renderEntry(e *entry, width int) string {
	if e.cache != "" && e.cacheW == width && e.kind != entryPending {
		return e.cache
	}
	if e.kind == entryPending {
		// A skeleton changes once a second (its elapsed seconds).
		k := elapsed(m.now().Sub(e.start)) + "|" + itoa(width)
		if e.cache != "" && e.pendKey == k {
			return e.cache
		}
		e.pendKey = k
	}
	cw := min(width, maxCardWidth)
	var s string
	switch {
	case e.slim:
		lines := strings.Split(e.slimText, "\n")
		for i, l := range lines {
			lines[i] = truncate(l, width)
		}
		s = strings.Join(lines, "\n")
		e.cache, e.cacheW = s, width
		e.lines = len(lines)
		return s
	case e.kind == entryAssistant && e.streaming:
		lines := m.streamingLines(e, min(cw, proseWidth))
		s = strings.Join(lines, "\n")
		e.cache, e.cacheW = s, width
		e.lines = max(len(lines), 1)
		e.rl, e.rlSrc = lines, s // already fitted: layoutTranscript skips the split
		return s
	}
	switch e.kind {
	case entryWelcome:
		s = "" // drawn by welcomeScreen while it is the only entry
	case entryUser:
		s = gutter + m.st.dim.Render(glyph.Prompt) + " " + m.st.muted.Render(truncate(echoText(e.text), cw-4))
	case entryPending:
		s = cards.Skeleton(e.title, elapsed(m.now().Sub(e.start)), e.rows, m.cardOpts(cw, true))
	case entryCard:
		if wideCard(e.result) {
			// A chart card runs the full transcript width (the landing's
			// chart wall): the right third of the screen is not left empty.
			cw = width
		}
		o := m.cardOpts(cw, true)
		if e.result != nil && e.result.Card == core.CardChart && len(m.entries) > 0 && m.entries[len(m.entries)-1] == e && m.height > 0 {
			// The newest chart uses the viewport (like the CLI's rows-4,
			// capped at 30) so the hero chart shows without ctrl+o; it
			// keeps that size once rendered.
			o.Height = min(30, max(m.height-10, 14))
		}
		s = cards.Render(e.result, o)
	case entryError:
		s = cards.Error(e.err, m.cardOpts(cw, true))
	case entryInfo:
		if e.state != "" {
			s = m.stateLine(e.title, e.state, cw)
			break
		}
		s = m.infoBlock(e.text, cw)
	case entryAssistant:
		lines := cards.Markdown(e.text, m.cardOpts(min(cw, proseWidth), false))
		for i, l := range lines {
			lines[i] = gutter + l
		}
		s = strings.Join(lines, "\n")
	}
	e.cache, e.cacheW = s, width
	e.lines = strings.Count(s, "\n") + 1
	return s
}

// streamingLines renders a streaming reply: the markdown of the text as of
// the last render (at most every mdEvery), plus the newer tail wrapped as
// plain text. Complete tail lines are wrapped once, so a token costs a wrap
// of the current line, not a glamour pass over the reply (PERF #2). Every
// line fits cw.
func (m *Model) streamingLines(e *entry, cw int) []string {
	if e.mdLines == nil || e.mdW != cw || m.now().Sub(e.mdAt) >= mdEvery || e.mdLen > len(e.text) {
		md := cards.Markdown(e.text, m.cardOpts(cw, false))
		e.mdLines = make([]string, len(md))
		for i, l := range md {
			e.mdLines[i] = truncate(gutter+l, cw)
		}
		e.mdLen, e.mdW, e.mdAt = len(e.text), cw, m.now()
		e.tailFrom, e.tailLines = e.mdLen, nil
	}
	tailW := max(min(cw, proseWidth)-len(gutter)-2, 8)
	if cut := strings.LastIndexByte(e.text[e.tailFrom:], '\n'); cut >= 0 {
		for _, l := range cards.WrapPlain(e.text[e.tailFrom:e.tailFrom+cut], tailW) {
			e.tailLines = append(e.tailLines, gutter+m.st.muted.Render(l))
		}
		e.tailFrom += cut + 1
	}
	lines := make([]string, 0, len(e.mdLines)+len(e.tailLines)+2)
	lines = append(append(lines, e.mdLines...), e.tailLines...)
	for _, l := range cards.WrapPlain(e.text[e.tailFrom:], tailW) {
		lines = append(lines, gutter+m.st.muted.Render(l))
	}
	return lines
}

// welcomeCoins are the ticker's coins.
func (m *Model) welcomeCoins() []string {
	if m.env != nil && len(m.env.Config.Pinned) > 0 {
		return m.env.Config.Pinned
	}
	return []string{"BTC", "ETH", "HYPE", "xyz:SP500"}
}

// welcomeOnly reports whether the shell is still on its first screen: the
// welcome is an empty state, not history, so it leaves with the first
// entry (DESIGN.md 5.2 Welcome).
func (m *Model) welcomeOnly() bool {
	return len(m.entries) == 1 && m.entries[0].kind == entryWelcome
}

// welcomeScreen is the first frame (DESIGN.md 4.3 and 6.2): the dotted
// floor over the whole transcript area, the HLP wordmark at column 4 about
// 40% down inside the reticle, and on a first run only, one Dim line of
// commands under it. The ticker in the top bar already shows the markets.
func (m *Model) welcomeScreen(w, h int) []string {
	key := itoa(w) + "x" + itoa(h) + m.th.Name + itoa(int(m.profile))
	// The first-run line steps aside once typing starts: the prompt has
	// the person's attention, and the autocomplete takes over.
	firstRun := !m.seen.Seen(tipWelcome) && m.in.Empty()
	if firstRun {
		key += "first"
	}
	// While the input has text (completions open) focus is on the prompt:
	// the wordmark loses its reticle, so the accent marks the prompt and the
	// selected completion only (DESIGN.md 4.1).
	typing := !m.in.Empty() || m.palette != nil || m.showHelp
	if typing {
		key += "typing"
	}
	if e := m.entries[0]; e.cache != "" && e.title == key {
		return e.rl
	}
	lines := make([]string, h)
	// The wordmark grows with the window so it stays the focal point of an
	// empty screen (DESIGN.md 6.2): 1x on a laptop split, up to 4x on a
	// full-size terminal.
	m3 := glyph.Wordmark()
	mark := glyph.Scale(m3[:], max(min(w/36, h/10, 4), 1))
	const col = 4
	markW := lipgloss.Width(mark[0])
	// reticle: one blank cell each side of the mark, corners in the rows
	// above and below it
	retW := markW + 4
	retTop, retBot := strings.Repeat(" ", col-2)+glyph.Reticle(retW, true, m.st.reticleFn),
		strings.Repeat(" ", col-2)+glyph.Reticle(retW, false, m.st.reticleFn)
	if typing {
		retTop, retBot = "", ""
	}
	block := []string{retTop}
	markSt := m.st.bold
	if typing {
		markSt = m.st.dim // the completion list has the focus now
	}
	for _, r := range mark {
		block = append(block, strings.Repeat(" ", col)+markSt.Render(r))
	}
	block = append(block, retBot)
	if firstRun {
		block = append(block, "", strings.Repeat(" ", col)+m.st.dim.Render("/dash markets   /chart BTC   /vaults   /vault hlp"))
	}
	top := max((h*2)/5-2, 1)
	if top+len(block) > h {
		top = max(h-len(block), 0)
	}
	for i, l := range block {
		if top+i < h {
			lines[top+i] = l
		}
	}
	if m.st.floor != nil && m.uiFloor() {
		// y0 = 1: the transcript starts under the top bar, so the dots sit
		// on the screen's even rows.
		lines = glyph.Floor(lines, w, 1, m.st.floor)
	}
	e := m.entries[0]
	e.cache, e.title, e.rl = "welcome", key, lines
	return lines
}

func (m *Model) infoBlock(text string, width int) string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		// wrap at word boundaries; never cut a command mid-word
		for _, wl := range strings.Split(ansi.Wrap(l, max(width-len(gutter), 8), " "), "\n") {
			out = append(out, gutter+truncate(wl, width-len(gutter)))
		}
	}
	return strings.Join(out, "\n")
}

// layoutTranscript renders every entry at width, records each entry's
// first line, and returns all lines. The transcript opens and closes with
// one blank row (under the top bar, above the hairline); groups (an echo
// and its answer) are separated by one blank row. The reticle is drawn
// into the blank rows around the focused group, so focusing never shifts
// the layout (DESIGN.md 4.1).
//
// Lines are fitted to width once per rendered entry (entry.rl), so the
// caller can place them without truncating again.
func (m *Model) layoutTranscript(width int) []string {
	all := make([]string, 0, 256)
	all = append(all, "")
	for i, e := range m.entries {
		if e.kind == entryWelcome {
			e.top, e.lines = len(all), 0
			continue
		}
		if i > 0 && m.entries[i-1].kind != entryUser && m.entries[i-1].kind != entryWelcome {
			all = append(all, "")
		}
		e.top = len(all)
		s := m.renderEntry(e, width)
		if e.rl == nil || e.rlSrc != s || e.rlW < 0 {
			rl := strings.Split(s, "\n")
			w := 0
			for j, l := range rl {
				rl[j] = truncate(l, width)
				w = max(w, lipgloss.Width(rl[j]))
			}
			e.rl, e.rlSrc, e.rlW = rl, s, w
		}
		all = append(all, e.rl...)
	}
	all = append(all, "")
	m.drawReticle(all, width)
	m.floorEdges(all)
	return all
}

// floorEdges records, for every transcript line, the column where the
// floor grid may start (m.edges): past the right edge of the group (echo
// plus card) the line belongs to, plus a 2-cell margin, so the dots fill
// the ground around a card but never its inside. Rows below the last group
// get the floor across the whole width.
func (m *Model) floorEdges(all []string) {
	if cap(m.edges) < len(all) {
		m.edges = make([]int, len(all))
	}
	m.edges = m.edges[:len(all)]
	clear(m.edges)
	for i := 0; i < len(m.entries); i++ {
		e := m.entries[i]
		if e.kind == entryWelcome {
			continue
		}
		first, last := e, e
		if e.kind == entryUser && i+1 < len(m.entries) && m.entries[i+1].kind != entryUser {
			last = m.entries[i+1]
			i++
		}
		edge := max(first.rlW, last.rlW)
		if first == last && first.kind == entryUser {
			edge = first.rlW
		}
		lo, hi := first.top-1, last.top+max(last.lines, len(last.rl))
		edge = max(edge, lipgloss.Width(all[max(lo, 0)]))
		if hi < len(all) {
			edge = max(edge, lipgloss.Width(all[hi]))
		}
		for y := max(lo, 0); y <= hi && y < len(all); y++ {
			m.edges[y] = max(m.edges[y], edge+2)
		}
	}
}

// focusable reports whether an entry can carry the reticle: a card, a
// pending skeleton or an error card.
func focusable(e *entry) bool {
	return e.kind == entryCard || e.kind == entryPending || e.kind == entryError
}

// focusEntry is the index of the card the reticle marks: the one under the
// mouse, else the newest card. -1 when there is none, or while focus is on
// the prompt (input has text, an overlay or confirm is open).
func (m *Model) focusEntry() int {
	if !m.in.Empty() || m.palette != nil || m.showHelp || m.confirm != nil {
		return -1
	}
	if m.hoverEntry != 0 {
		for i, e := range m.entries {
			if e.id == m.hoverEntry && focusable(e) {
				return i
			}
		}
	}
	for i := len(m.entries) - 1; i >= 0; i-- {
		if focusable(m.entries[i]) {
			return i
		}
	}
	return -1
}

// drawReticle puts the corner reticle on the focused group (its echo line
// and card): ┌─ ─┐ on the blank row above, └─ ─┘ on the blank row below,
// the right corners at the card's right edge.
func (m *Model) drawReticle(all []string, width int) {
	i := m.focusEntry()
	if i < 0 {
		return
	}
	e := m.entries[i]
	first := e
	if i > 0 && m.entries[i-1].kind == entryUser {
		first = m.entries[i-1]
	}
	top, bot := first.top-1, e.top+max(e.lines, 1)
	if top < 0 || bot >= len(all) || all[top] != "" || all[bot] != "" {
		return
	}
	// The right corners sit at the group's own right edge (the card is
	// as wide as its content), plus the 2-cell gutter.
	cw := 0
	for _, l := range all[top+1 : bot] {
		cw = max(cw, lipgloss.Width(l))
	}
	limit := maxCardWidth
	if e.kind == entryCard && wideCard(e.result) {
		limit = width
	}
	cw = min(max(cw+len(gutter), 12), width, limit)
	all[top] = glyph.Reticle(cw, true, m.st.reticleFn)
	all[bot] = glyph.Reticle(cw, false, m.st.reticleFn)
}

// entryAt is the transcript entry drawn on screen row y (0 = none).
func (m *Model) entryAt(y int) int {
	line := m.viewLo + y - 1 // row 0 is the top bar
	for _, e := range m.entries {
		if e.kind != entryWelcome && line >= e.top && line < e.top+e.lines {
			return e.id
		}
	}
	return 0
}

// stateLine is a card that ended without a result: its title on the left
// and the state (□ STOPPED, □ NO RESULT) in the focal slot, Dim.
func (m *Model) stateLine(title, state string, width int) string {
	cw := min(width, maxCardWidth)
	return padBetween(gutter+m.st.muted.Render(title), m.st.dim.Render(glyph.Off+" "+state)+gutter, cw+1)
}

// echoText cuts an echoed prompt line to one line (DESIGN.md 5.1 rule
// 10): addresses middle-truncated, paths shown as their basename, and a
// comma list of more than two items as its first item plus +N.
func echoText(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	f := strings.Fields(s)
	for i, w := range f {
		f[i] = echoWord(w)
	}
	return strings.Join(f, " ")
}

func echoWord(w string) string {
	if strings.HasPrefix(w, "-") {
		return w
	}
	if items := strings.Split(w, ","); len(items) > 1 {
		for i, it := range items {
			items[i] = echoItem(it)
		}
		if len(items) > 2 {
			return items[0] + " +" + strconv.Itoa(len(items)-1)
		}
		return strings.Join(items, ",")
	}
	return echoItem(w)
}

func echoItem(w string) string {
	if len(w) == 42 && strings.HasPrefix(strings.ToLower(w), "0x") {
		return cards.ShortAddr(w)
	}
	// A file path (a slash past the first rune and a dot or a ./ ~/ lead in
	// it) shows its basename; a spot pair such as PURR/USDC is kept.
	if k := strings.LastIndexByte(w, '/'); k > 0 && k < len(w)-1 {
		base := w[k+1:]
		if strings.Contains(base, ".") || strings.HasPrefix(w, "~") || strings.HasPrefix(w, ".") {
			return base
		}
	}
	return w
}

// visibleRange returns the transcript line window [lo, hi) for a viewport
// of h rows. Short transcripts sit at the top (no void above them).
func visibleRange(total, h, scroll int) (lo, hi int) {
	if total <= h {
		return 0, total
	}
	hi = total - scroll
	hi = max(min(hi, total), h)
	return hi - h, hi
}

// elapsed is a pending card's focal: whole seconds, "1S", "2S"... (Dim, a
// state rather than a spinner); nothing in the first second.
func elapsed(d time.Duration) string {
	if d < time.Second {
		return ""
	}
	return strconv.Itoa(int(d/time.Second)) + "S"
}

func oneLine(s string, width int) string {
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	return truncate(s, width)
}

func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return ansiTruncate(s, width)
}

// floorKey memoizes one floored transcript row.
type floorKey struct {
	line    string
	from, w int
}

// floorRow draws the floor dots on one transcript row from column from on
// (DESIGN.md 4.3: a dot every 4 columns from column 2, on even screen
// rows), keeping the line's own cells. Rows are memoized: a frame of an
// unchanged transcript costs a map lookup per row.
func (m *Model) floorRow(line string, from, w int) string {
	k := floorKey{line, from, w}
	if s, ok := m.floorMemo[k]; ok {
		return s
	}
	if m.floorMemo == nil || len(m.floorMemo) > 4096 {
		m.floorMemo = make(map[floorKey]string, 64)
	}
	lw := lipgloss.Width(line)
	from = max(from, lw)
	var b strings.Builder
	b.WriteString(line)
	col := lw
	dot := m.st.floor(glyph.Dot)
	for c := glyph.FloorCol0; c < w; c += glyph.FloorStepX {
		if c < from {
			continue
		}
		b.WriteString(strings.Repeat(" ", c-col))
		b.WriteString(dot)
		col = c + 1
	}
	s := b.String()
	m.floorMemo[k] = s
	return s
}

// floorTranscript paints the floor into the visible transcript rows
// (middle, the rows lo.. of the layout): the void below the last card and
// the ground beside each card, so the empty screen reads as the landing's
// dotted floor rather than dead black.
func (m *Model) floorTranscript(middle []string, lo, w int) {
	if m.st.floor == nil || !m.uiFloor() {
		return
	}
	for i := range middle {
		if (i+1)%glyph.FloorStepY != 0 { // screen row i+1: dots on even rows
			continue
		}
		from := 0
		if j := lo + i; j < len(m.edges) {
			from = m.edges[j]
		}
		middle[i] = m.floorRow(middle[i], from, w)
	}
}
