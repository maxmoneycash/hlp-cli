package cards

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/maxmoneycash/hlp/internal/chart"
	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/metrics"
	"github.com/maxmoneycash/hlp/internal/theme"
	"github.com/maxmoneycash/hlp/internal/tui/glyph"
)

// Opts control rendering.
type Opts struct {
	Width   int // total width including the inset on both sides
	Height  int // max height; 0 = natural (charts default to 16 rows inline, full in panes)
	Theme   theme.Theme
	Profile colorprofile.Profile
	Compact bool // inline-in-chat size (mini chart, top 10 rows)
	Plain   bool // no ANSI (golden tests, --plain)
	// AllRows prints every row of a table instead of a screen-sized budget
	// (piped output, or an explicit --limit the user asked for).
	AllRows bool

	// Location formats intraday time labels and footers. nil = UTC. Daily
	// and longer chart intervals always label in UTC.
	Location *time.Location
	// ZoneLabel names the zone after footer times ("as of Nov 15 04:41:08
	// PST", "… UTC"), so a CLI reader never has to guess. The TUI leaves it
	// off: its clock is the user's own.
	ZoneLabel bool

	// ChartSkin and ChartStyle are the candle charts' skin (setup-rush
	// theme, "" = terminal, "none" = theme colors) and candle style ("" =
	// the skin's classic look).
	ChartSkin  string
	ChartStyle chart.Style

	// Hero: the pane header draws the focal as big digits (Focal), so the
	// card omits its header focal.
	Hero bool
	// Info renders the info row and NOTES even inline.
	Info bool
}

// DefaultWidth is used when Opts.Width is zero (pipes, tests).
const DefaultWidth = 100

// Inline chart heights (rows of the chart grid itself).
const (
	chartRowsCompact = 12
	chartRowsInline  = 16
)

func (o Opts) normalized() Opts {
	if o.Width <= 0 {
		o.Width = DefaultWidth
	}
	if o.Width < 24 {
		o.Width = 24
	}
	if o.Theme.Name == "" {
		o.Theme = theme.Default(true)
	}
	if o.Profile == colorprofile.Unknown {
		o.Profile = colorprofile.TrueColor
	}
	if o.ChartSkin == "" {
		o.ChartSkin = o.Theme.ChartSkin
	}
	if o.ChartSkin == "" && !o.Theme.Dark {
		// The default setup-rush skin paints a near-black ground: on a
		// light UI theme that is a black slab, so light themes default to
		// the paper skin (chart.theme still wins when set).
		o.ChartSkin = "paper"
	}
	return o
}

// Inset is the left (and right) inset every card line gets; the TUI aligns
// echo lines to it (DESIGN.md section 6.1: a 2-cell gutter).
const Inset = 2

// nowFn is the clock for STALE ages (a test seam).
var nowFn = time.Now

// Render returns the card for r. Unknown cards fall back to an indented
// key/value dump of r.Data. Inline (Compact) cards end with at most one
// gold tag line (STALE, warnings); full cards (panes, the CLI, Opts.Info)
// add a NOTES section and the SOURCE / AS OF info row instead.
func Render(r *core.Result, o Opts) string {
	if r == nil {
		return ""
	}
	o = o.normalized()
	p := newPainter(o)
	c := p.build(r)
	return p.finish(p.frame(c, r.Meta))
}

// finish is the last pass over any rendered card: control filtering, and
// in plain mode the display minus folded back to ASCII.
func (p *painter) finish(s string) string {
	s = safeTerminal(s, p.o.Plain)
	if p.o.Plain {
		s = strings.ReplaceAll(s, minus, "-")
	}
	return s
}

// build runs the renderer for r's card kind.
func (p *painter) build(r *core.Result) card {
	var c card
	switch r.Card {
	case core.CardChart:
		c = p.chartCard(r)
	case core.CardBook:
		c = p.bookCard(r)
	case core.CardTrades:
		c = p.tradesCard(r)
	case core.CardMarkets:
		c = p.marketsCard(r)
	case core.CardFunding:
		c = p.fundingCard(r)
	case core.CardAccount:
		c = p.accountCard(r)
	case core.CardFills:
		c = p.fillsCard(r)
	case core.CardVaults:
		c = p.vaultsCard(r)
	case core.CardVault:
		c = p.vaultCard(r)
	case core.CardCompare:
		c = p.compareCard(r)
	case core.CardBacktest:
		c = p.backtestCard(r)
	case core.CardPaper:
		c = p.paperCard(r)
	case core.CardArena:
		c = p.arenaCard(r)
	case core.CardOrder:
		c = p.orderCard(r)
	case core.CardFund:
		c = p.fundCard(r)
	case core.CardPVault, core.CardPVaultEpochs, core.CardPVaultUser:
		var ok bool
		if c, ok = p.pvaultAny(r); !ok {
			c = p.fallbackCard(r)
		}
	case core.CardDiscover:
		c = p.discoverCard(r)
	case core.CardIndex:
		c = p.indexCard(r)
	case core.CardStrategy:
		c = p.strategyCard(r)
	case core.CardKV:
		c = p.kvCard(r)
	case core.CardPrice:
		c = p.priceCard(r)
	case core.CardStatus:
		c = p.statusCard(r)
	case core.CardSchema:
		c = p.schemaCard(r)
	case core.CardText:
		c = p.textCard(r)
	default:
		c = p.fallbackCard(r)
	}
	if c.title == "" {
		c.title = r.Title
	}
	return c
}

// Focal returns the card's one focal number for a pane header (DESIGN.md
// section 3.1): value (for sign and colour), the big part drawn in big
// digits ("64953.5", "+367.9", "−5.2"), and the small suffix drawn after
// it on the baseline ("−0.9%  24H", "%  ALL", "M  +$54.8M"). ok is false
// when the card has no numeric focal (a count, a word, an empty state).
func Focal(r *core.Result, o Opts) (value float64, big, small string, ok bool) {
	if r == nil {
		return 0, "", "", false
	}
	o.Hero = false
	o = o.normalized()
	p := newPainter(o)
	c := p.build(r)
	if c.hero != "" {
		c.focal, c.numeric, c.delta, c.deltaLabel = c.hero, true, c.heroSmall, ""
	}
	if c.focal == "" || !c.numeric {
		return 0, "", "", false
	}
	big, unit, v, ok := splitNumber(c.focal)
	if !ok {
		return 0, "", "", false
	}
	if c.focalVal != nil {
		v = *c.focalVal
	}
	var tail []string
	if unit != "" {
		tail = append(tail, unit)
	}
	if c.delta != "" {
		tail = append(tail, c.delta)
	}
	if c.deltaLabel != "" {
		tail = append(tail, strings.ToUpper(c.deltaLabel))
	}
	small = strings.Join(tail, "  ")
	if o.Plain {
		big, small = strings.ReplaceAll(big, minus, "-"), strings.ReplaceAll(small, minus, "-")
	}
	return v, big, small, true
}

// splitNumber cuts a formatted focal ("$152.5M", "+367.9%", "64,953",
// "−$3.4K") into the part big digits can draw ("152.5", "+367.9",
// "64953", "−3.4"), its unit ("M", "%", ""), and its value as a float in
// display units. A currency sign is dropped from both: the header label
// already says what the number is.
func splitNumber(s string) (big, unit string, v float64, ok bool) {
	sign := ""
	switch {
	case strings.HasPrefix(s, "+"):
		sign, s = "+", s[1:]
	case strings.HasPrefix(s, minus):
		sign, s = minus, s[len(minus):]
	case strings.HasPrefix(s, "-"):
		sign, s = minus, s[1:]
	}
	s = strings.TrimPrefix(s, "$")
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.' || s[i] == ',') {
		i++
	}
	num, unit := strings.ReplaceAll(s[:i], ",", ""), strings.TrimSpace(s[i:])
	if num == "" || strings.ContainsAny(unit, "0123456789 ") || len(unit) > 2 {
		return "", "", 0, false
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return "", "", 0, false
	}
	if sign == minus {
		f = -f
	}
	return sign + num, unit, f, true
}

// Skeleton renders the placeholder shown while a tool is running: the
// card's own header and a few quiet rows at the content's geometry, so the
// transcript does not jump when the result lands. status sits in the focal
// slot (the TUI passes elapsed seconds, "2S").
func Skeleton(title, status string, rows int, o Opts) string {
	o = o.normalized()
	p := newPainter(o)
	rows = max(rows, 1)
	widths := []int{62, 45, 70, 52, 66, 40}
	body := make([]string, 0, rows)
	for i := 0; i < rows; i++ {
		w := max(4, p.w*widths[i%len(widths)]/100)
		body = append(body, p.st.faint.Render(strings.Repeat("─", w)))
	}
	c := card{title: title, focal: status, focalStyle: p.st.dim, plainFocal: true, body: body, noFooter: true}
	return p.finish(p.frame(c, domain.Meta{}))
}

// Error renders a structured error (DESIGN.md section 5.2, Errors): a red
// ■ and the message, then the hint as a runnable command only, in Dim. The
// machine code stays in --json.
func Error(err error, o Opts) string {
	if err == nil {
		return ""
	}
	o = o.normalized()
	p := newPainter(o)
	msg, hint := err.Error(), ""
	if e, ok := err.(*core.Error); ok {
		msg, hint = e.Message, e.Hint
	}
	// Error text carries untrusted strings (paths from shared arena files,
	// engine stderr, upstream bodies): clean before styling (SECURITY S3).
	msg, hint = domain.CleanText(msg), domain.CleanText(hint)
	msg = strings.NewReplacer("\"", "", "`", "").Replace(msg)
	if o.Compact {
		// inside the shell, CLI hints read as slash commands
		hint = core.SlashHint(hint)
	}
	// Capitalize the first word, except names that are lower case on
	// purpose (hlp, hlp.xyz, 0x addresses).
	if r := []rune(msg); len(r) > 0 && !strings.HasPrefix(msg, "hlp") && !strings.HasPrefix(msg, "0x") {
		msg = strings.ToUpper(string(r[0])) + string(r[1:])
	}
	lines := []string{p.st.down.Render(glyph.On) + " " + p.st.text.Render(msg)}
	if cmd := hintCommand(hint); cmd != "" {
		lines = append(lines, "  "+p.st.dim.Render(cmd))
	} else if hint != "" && !o.Compact {
		lines = append(lines, "  "+p.st.muted.Render(hint))
	}
	return p.finish(p.indent(wrapLines(lines, p.w)))
}

// hintCommand pulls the runnable command out of a hint: the text inside
// backticks, or the whole hint when it already is a command.
func hintCommand(h string) string {
	h = strings.TrimSpace(h)
	if i := strings.IndexByte(h, '`'); i >= 0 {
		if j := strings.IndexByte(h[i+1:], '`'); j > 0 {
			return h[i+1 : i+1+j]
		}
	}
	if strings.HasPrefix(h, "/") || strings.HasPrefix(h, "hlp ") {
		return h
	}
	return ""
}

// card is the intermediate form every renderer returns.
type card struct {
	title      string
	name       bool   // title is a name (vault, strategy): keeps its case, cut at 40 cells
	mark       string // pre-styled mark before the title (■ curated)
	pill       string // pre-styled pill after the title (TESTNET)
	sub        string // label after the title (UPPERCASE unless it holds an address)
	legend     string // pre-styled, after sub (dropped first when narrow)
	focal      string // the one number that gets to be big (right side, bold)
	focalStyle lipgloss.Style
	plainFocal bool     // focal keeps its style without bold (pending seconds)
	numeric    bool     // focal is a number a pane may draw as big digits
	focalVal   *float64 // the focal's value when it is not the parsed text
	delta      string   // after the focal, coloured by sign
	deltaStyle lipgloss.Style
	deltaLabel string // muted label after the delta ("24H", "ALL")
	body       []string
	tag        string   // inline warning tag, 4 words or fewer, without "!"
	tagOwnLine bool     // the last body line is a chart: the tag gets its own line
	notes      []string // extra NOTES lines in a full card (legends, details)
	warns      []string // payload warnings, merged with meta.warnings
	hero       string   // what Focal parses when it is not the focal (arena)
	heroSmall  string   // Focal's small suffix with hero
	noFooter   bool
	noHeader   bool   // a pane's band already names it (Hero): body only
	noBig      bool   // the focal stays a header value inline (no big digits)
	said       string // inline, warnings containing this are already said by the header (TESTNET)
}

// styles is the resolved style set; all zero styles in plain mode.
type styles struct {
	title, text, muted, faint, accent, up, down, warn, err, ok, bold, dim, label lipgloss.Style
	pill, warnPill                                                               lipgloss.Style
}

type painter struct {
	o  Opts
	th theme.Theme
	st styles
	w  int // inner width (card width minus the inset on each side)
	// band, when set, names the table row drawn on the Surface band
	// across the full table width (the screener's selected row).
	band func(i int) bool
}

func newPainter(o Opts) *painter {
	p := &painter{o: o, th: o.Theme, w: o.Width - 2*Inset}
	if o.Plain || o.Profile == colorprofile.NoTTY || o.Profile == colorprofile.ASCII {
		n := lipgloss.NewStyle()
		p.st = styles{n, n, n, n, n, n, n, n, n, n, n, n, n, n, n}
		if !o.Plain {
			p.st.pill = n.Reverse(true)
			p.st.warnPill = n.Reverse(true)
		}
		return p
	}
	fg := func(c theme.Color) lipgloss.Style {
		s := lipgloss.NewStyle()
		if col := c.Resolve(o.Profile); col != nil {
			s = s.Foreground(col)
		}
		return s
	}
	t := o.Theme
	dim := t.Dim
	if !dim.True.Valid {
		dim = t.Muted
	}
	p.st = styles{
		title:  fg(t.Text).Bold(true),
		text:   fg(t.Text),
		muted:  fg(t.Muted),
		faint:  fg(t.Border),
		accent: fg(t.Accent),
		up:     fg(t.Up),
		down:   fg(t.Down),
		warn:   fg(t.Warn),
		err:    fg(t.Error).Bold(true),
		ok:     fg(t.Success),
		bold:   fg(t.Text).Bold(true),
		dim:    fg(dim),
		label:  fg(t.Muted),
		pill:   fg(t.Text).Reverse(true),
	}
	p.st.warnPill = fg(t.Warn).Reverse(true).Bold(true)
	return p
}

// lbl renders an UPPERCASE muted label (column headers, TVL, MAX DD).
func (p *painter) lbl(s string) string { return p.st.label.Render(upperKeepAddr(s)) }

// meta lays out label/value pairs on one line, three cells apart:
// "TVL $151.6M   3.5Y   4D LOCK". A pair with an empty label is a value
// alone; values arrive pre-styled. Pairs that do not fit are dropped from
// the right, never cut.
func (p *painter) meta(pairs ...[2]string) string {
	out, w := "", 0
	for _, kv := range pairs {
		if strings.TrimSpace(ansi.Strip(kv[1])) == "" {
			continue // a label with no value (a styled empty string)
		}
		item := kv[1]
		if kv[0] != "" {
			item = p.lbl(kv[0]) + " " + kv[1]
		}
		iw := lipgloss.Width(item)
		if w > 0 && w+3+iw > p.w {
			break
		}
		if w > 0 {
			out += "   "
			w += 3
		}
		out += item
		w += iw
	}
	return out
}

// strong makes a style bold, except in plain mode (no escape codes at all).
func (p *painter) strong(st lipgloss.Style) lipgloss.Style {
	if p.o.Plain || p.o.Profile == colorprofile.NoTTY || p.o.Profile == colorprofile.ASCII {
		return st
	}
	return st.Bold(true)
}

// quietSign is sign at a muted tint (55% toward the background), for
// filler like table sparklines that must not outshout the numbers.
func (p *painter) quietSign(v float64) lipgloss.Style {
	if p.o.Plain || p.o.Profile == colorprofile.NoTTY || p.o.Profile == colorprofile.ASCII {
		return p.st.muted
	}
	c := p.th.Text
	switch {
	case v > 0:
		c = p.th.Up
	case v < 0:
		c = p.th.Down
	}
	if c.True.Valid && p.th.BG.True.Valid {
		mixed := theme.Mix(p.th.BG.True, c.True, 0.55)
		c = theme.Color{True: mixed, C256: theme.Hue256(mixed), C16: c.C16}
	}
	if col := c.Resolve(p.o.Profile); col != nil {
		return lipgloss.NewStyle().Foreground(col)
	}
	return lipgloss.NewStyle()
}

// sign picks the up/down style for v.
func (p *painter) sign(v float64) lipgloss.Style {
	switch {
	case v > 0:
		return p.st.up
	case v < 0:
		return p.st.down
	}
	return p.st.text
}

// badge renders a short uppercase tag. In plain mode it becomes [TAG].
func (p *painter) badge(s string, warn bool) string {
	if p.o.Plain {
		return "[" + s + "]"
	}
	if warn {
		return p.st.warnPill.Render(" " + s + " ")
	}
	return p.st.pill.Render(" " + s + " ")
}

// frame lays out header, body and the closing line at the card width.
//
//	TITLE  SUB                                   FOCAL  delta  LABEL
//	body…
//	                                              ! TAG +N   STALE 3M
//
// A full card (pane, CLI, Opts.Info) replaces the tag with a NOTES section
// and the SOURCE / AS OF info row (DESIGN.md section 5.1 rules 4 and 5).
func (p *painter) frame(c card, m domain.Meta) string {
	if p.fitsContent() {
		// An inline card is as wide as its content (DESIGN.md 6.1: the
		// focal sits over the last table column), not the 100-column
		// cap: the header's focal and the reticle's right corners move in
		// to the table's edge.
		full := p.w
		cw := 0
		for _, l := range c.body {
			for _, sub := range strings.Split(l, "\n") {
				// trailing blanks (a grid padded to its width) are not content
				cw = max(cw, lipgloss.Width(strings.TrimRight(ansi.Strip(sub), " ")))
			}
		}
		cw = max(cw, p.headerWidth(c), p.tagWidth(c, m), minFitWidth)
		p.w = min(cw, full)
		defer func() { p.w = full }()
	}
	var out []string
	if !c.noHeader {
		if big, ok := p.bigFocal(c); ok {
			// The focal leads the card as compact big digits under the
			// title (the landing's tile number), not a bold value at the
			// far right of the header.
			hc := c
			hc.focal, hc.delta, hc.deltaLabel = "", "", ""
			out = append(out, p.header(hc))
			out = append(out, big...)
		} else {
			out = append(out, p.header(c))
		}
	}
	out = append(out, c.body...)
	if !c.noFooter {
		out = p.close(out, c, m)
	}
	return p.indent(out)
}

// bigFocal draws an inline card's numeric focal in the big-digit face
// under the title (the landing's tile number, "−0.9%"): the number in ink
// (red when negative), a currency sign and unit small and bold on the
// baseline, then the delta and its label. ok is false where the focal
// stays a bold header value: panes (the band draws it), plain and ASCII
// output, narrow cards, and cards that opt out.
func (p *painter) bigFocal(c card) ([]string, bool) {
	if !p.o.Compact || p.o.Hero || p.o.Plain || c.noBig || !c.numeric || c.focal == "" || p.o.Width < 60 {
		return nil, false
	}
	if p.o.Profile == colorprofile.NoTTY || p.o.Profile == colorprofile.ASCII {
		return nil, false
	}
	num, unit, v, ok := splitNumber(c.focal)
	if !ok {
		return nil, false
	}
	if c.focalVal != nil {
		v = *c.focalVal
	}
	if strings.Contains(c.focal, "$") {
		// money leads with its sign, drawn big like the landing's +$915
		if strings.HasPrefix(num, "+") || strings.HasPrefix(num, minus) {
			r := []rune(num)
			num = string(r[0]) + "$" + string(r[1:])
		} else {
			num = "$" + num
		}
	}
	rows, ok := glyph.Big(num)
	if !ok {
		return nil, false
	}
	st := p.st.text
	if v < 0 {
		st = p.st.down
	}
	small := ""
	if unit != "" {
		small = p.strong(st).Render(unit)
	}
	if c.delta != "" {
		if small != "" {
			small += "  "
		}
		small += c.deltaStyle.Render(c.delta)
	}
	if c.deltaLabel != "" {
		if small != "" {
			small += "  "
		}
		small += p.lbl(c.deltaLabel)
	}
	out := []string{st.Render(rows[0]), st.Render(rows[1]), st.Render(rows[2])}
	if small != "" {
		gap := " "
		if unit == "" {
			gap = "  "
		}
		out[2] += gap + small
	}
	return out, true
}

// minFitWidth is the narrowest a content-sized card gets, so a one-line
// card still reads as a card under its echo.
const minFitWidth = 32

// fitsContent reports whether the card is sized to its content: inline
// transcript cards only. Panes, the CLI and Info cards keep the full width.
func (p *painter) fitsContent() bool {
	return p.o.Compact && !p.o.Info && !p.o.Plain
}

// headerWidth is the header's natural width: title, sub and legend, three
// cells, then focal, delta and label.
func (p *painter) headerWidth(c card) int {
	if big, ok := p.bigFocal(c); ok {
		hc := c
		hc.focal, hc.delta, hc.deltaLabel = "", "", ""
		left, _ := p.headerParts(hc)
		return max(lipgloss.Width(left), lipgloss.Width(big[2]))
	}
	left, right := p.headerParts(c)
	if right == "" {
		return lipgloss.Width(left)
	}
	return lipgloss.Width(left) + 3 + lipgloss.Width(right)
}

// tagWidth is the width the inline tag line needs (0 without one).
func (p *painter) tagWidth(c card, m domain.Meta) int {
	saved := p.w
	p.w = 512
	out := p.close([]string{""}, card{tag: c.tag, warns: c.warns, tagOwnLine: true}, m)
	p.w = saved
	w := 0
	for _, l := range out[1:] {
		w = max(w, lipgloss.Width(strings.TrimLeft(ansi.Strip(l), " ")))
	}
	return w
}

// full reports whether the card shows the NOTES section instead of the
// inline tag: Opts.Info, or a full card with no height budget (a pane, the
// CLI, MCP text).
func (p *painter) full() bool { return p.o.Info || !p.o.Compact && p.o.Height == 0 }

// infoRow reports whether the SOURCE / AS OF row is drawn: Opts.Info (the
// pane's i key), or a full card outside the TUI (the CLI names its zone,
// plain text is for pipes and agents).
func (p *painter) infoRow() bool {
	return p.o.Info || p.full() && (p.o.ZoneLabel || p.o.Plain)
}

// plumbing is internal detail (transport fallbacks, retries, data paths)
// that agents read in meta.warnings and people never need to see.
func plumbing(w string) bool {
	l := strings.ToLower(w)
	for _, k := range []string{"rpc", "tls", "via ", "fallback", "retry", "retried", "timed out", "x509"} {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

func (p *painter) close(out []string, c card, m domain.Meta) []string {
	var warns, also []string
	seen := map[string]bool{}
	for _, w := range append(append([]string(nil), m.Warnings...), c.warns...) {
		if seen[w] {
			continue
		}
		seen[w] = true
		switch {
		case strings.TrimSpace(w) == "" || plumbing(w):
		case resolved(w) && !p.full():
		case c.said != "" && !p.full() && strings.Contains(strings.ToLower(w), c.said):
		case strings.HasPrefix(w, "also: "): // other matches, not a problem
			also = append(also, w)
		default:
			warns = append(warns, w)
		}
	}
	var tags []string
	if m.Stale {
		s := "STALE"
		if !m.AsOf.IsZero() {
			s += " " + Ago(nowFn().Sub(m.AsOf))
		}
		tags = append(tags, s)
	}
	if m.Partial {
		tags = append(tags, "PARTIAL")
	}
	if !p.full() && len(warns) > 0 {
		t := c.tag
		if t == "" {
			t = autoTag(warns)
		}
		switch {
		case t == "" && len(warns) == 1:
			t = "1 NOTE"
		case t == "":
			t = strconv.Itoa(len(warns)) + " NOTES"
		case len(warns) > 1:
			t += " +" + strconv.Itoa(len(warns)-1)
		}
		tags = append([]string{glyph.Note + " " + strings.ToUpper(t)}, tags...)
	}
	if len(tags) > 0 && !p.full() {
		tag := p.st.warn.Render(strings.Join(tags, "   "))
		tw := lipgloss.Width(tag)
		last := len(out) - 1
		if lw := lipgloss.Width(out[last]); last > 0 && lw+3+tw <= p.w && lw <= p.w*2/3 && !c.tagOwnLine {
			out[last] += strings.Repeat(" ", p.w-lw-tw) + tag
		} else {
			out = append(out, strings.Repeat(" ", max(p.w-tw, 0))+tag)
		}
	}
	if !p.full() {
		return out
	}
	notes := append([]string(nil), c.notes...)
	for _, w := range warns {
		notes = append(notes, p.st.warn.Render(glyph.Note)+" "+p.st.text.Render(w))
	}
	for _, w := range also {
		notes = append(notes, p.st.muted.Render(w))
	}
	if len(notes) > 0 {
		out = append(out, "", p.lbl("NOTES"))
		out = append(out, wrapLines(notes, p.w)...)
	}
	info := ""
	if p.infoRow() {
		info = p.info(m)
	}
	if len(tags) > 0 {
		t := p.st.warn.Render(strings.Join(tags, "   "))
		if info == "" {
			info = t
		} else if lipgloss.Width(info)+3+lipgloss.Width(t) <= p.w {
			info += "   " + t
		} else {
			out = append(out, t)
		}
	}
	if info != "" {
		if len(notes) == 0 {
			out = append(out, "")
		}
		out = append(out, info)
	}
	return out
}

// info is the SOURCE / AS OF row of a full card.
func (p *painter) info(m domain.Meta) string {
	stamp := Stamp
	if p.o.ZoneLabel {
		stamp = StampZone
	}
	var pairs [][2]string
	if m.Source != "" {
		pairs = append(pairs, [2]string{"source", p.st.muted.Render(metrics.SourceLabel(m.Source))})
	}
	if s := stamp(m.AsOf, p.o.Location); s != "" {
		pairs = append(pairs, [2]string{"as of", p.st.muted.Render(s)})
	}
	if m.Network == domain.Testnet {
		pairs = append(pairs, [2]string{"", p.st.warn.Render("TESTNET")})
	}
	return p.meta(pairs...)
}

// titleText applies the title rule: UPPERCASE for 3 words or fewer, names
// keep their case and are cut at 40 cells.
func (p *painter) titleText(c card) string {
	t := termSafe(c.title)
	if c.name {
		if lipgloss.Width(t) > 40 {
			t = ansi.Truncate(t, 40, "…")
		}
		return t
	}
	if len(strings.Fields(t)) <= 3 {
		return upperKeepAddr(t)
	}
	return t
}

// upperKeepAddr upper-cases s except 0x addresses, which keep their case.
func upperKeepAddr(s string) string {
	f := strings.Split(s, " ")
	for i, w := range f {
		if !strings.HasPrefix(w, "0x") {
			f[i] = strings.ToUpper(w)
		}
	}
	return strings.Join(f, " ")
}

// headerParts is the header's left (title, sub, legend) and right (focal,
// delta, label) before fitting.
func (p *painter) headerParts(c card) (left, right string) {
	right = ""
	if c.focal != "" && !p.o.Hero {
		fs := c.focalStyle
		if !c.plainFocal {
			fs = p.strong(fs)
		}
		right = fs.Render(c.focal)
		if c.delta != "" {
			right += "  " + c.deltaStyle.Render(c.delta)
		}
		if c.deltaLabel != "" {
			right += "  " + p.lbl(c.deltaLabel)
		}
	}
	title := p.st.title.Render(p.titleText(c))
	if c.mark != "" {
		title = c.mark + " " + title
	}
	if c.pill != "" {
		title += "  " + c.pill
	}
	left = title
	if c.sub != "" {
		left += "  " + p.st.label.Render(upperKeepAddr(termSafe(c.sub)))
	}
	if c.legend != "" {
		left += "  " + c.legend
	}
	return left, right
}

func (p *painter) header(c card) string {
	legend := c.legend
	c.legend = ""
	left, right := p.headerParts(c)
	title := p.st.title.Render(p.titleText(c))
	if c.mark != "" {
		title = c.mark + " " + title
	}
	if c.pill != "" {
		title += "  " + c.pill
	}
	c.legend = legend
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	if c.legend != "" && lw+2+lipgloss.Width(c.legend)+rw+2 <= p.w {
		left += "  " + c.legend
		lw = lipgloss.Width(left)
	}
	if lw+rw+2 > p.w && c.sub != "" {
		left = title
		lw = lipgloss.Width(left)
	}
	if lw+rw+2 > p.w && c.deltaLabel != "" {
		right = strings.TrimSuffix(right, "  "+p.lbl(c.deltaLabel))
		rw = lipgloss.Width(right)
	}
	if lw+rw+2 > p.w && c.delta != "" {
		right = p.strong(c.focalStyle).Render(c.focal)
		rw = lipgloss.Width(right)
	}
	if lw+rw+2 > p.w {
		left = ansi.Truncate(left, max(p.w-rw-2, 4), "…")
		lw = lipgloss.Width(left)
	}
	if right == "" {
		return left
	}
	return left + strings.Repeat(" ", max(p.w-lw-rw, 2)) + right
}

// indent adds the inset and guarantees no line exceeds the width.
func (p *painter) indent(lines []string) string {
	pad := strings.Repeat(" ", Inset)
	var b strings.Builder
	for i, l := range lines {
		for _, sub := range strings.Split(l, "\n") {
			if b.Len() > 0 || i > 0 {
				b.WriteByte('\n')
			}
			if lipgloss.Width(sub) > p.w {
				sub = ansi.Truncate(sub, p.w, "")
			}
			if sub == "" {
				continue
			}
			b.WriteString(pad)
			b.WriteString(sub)
		}
	}
	return b.String()
}

// section is an UPPERCASE label above a block ("MONTHLY %").
func (p *painter) section(s string) string { return p.lbl(s) }

// gridLines serializes a chart grid for the active mode.
func (p *painter) gridLines(g interface {
	Plain() string
	ANSI(colorprofile.Profile) string
}) []string {
	var s string
	if p.o.Plain {
		s = g.Plain()
	} else {
		s = g.ANSI(p.o.Profile)
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

// sideBySide joins two blocks with a gap; left is padded to lw columns.
func sideBySide(left, right []string, lw, gap int) []string {
	n := max(len(left), len(right))
	out := make([]string, n)
	for i := 0; i < n; i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		if r == "" {
			out[i] = l
			continue
		}
		out[i] = l + strings.Repeat(" ", max(lw-lipgloss.Width(l), 0)+gap) + r
	}
	return out
}

// wrapLines soft-wraps plain-ish lines to width (ANSI aware).
func wrapLines(lines []string, width int) []string {
	var out []string
	for _, l := range lines {
		if lipgloss.Width(l) <= width {
			out = append(out, l)
			continue
		}
		out = append(out, strings.Split(ansi.Wrap(l, width, " "), "\n")...)
	}
	return out
}

// wrapItems lays out atomic items separated by sep, breaking lines only
// between items (never inside a badge like "• curated"). An item wider than
// width gets a line of its own and is wrapped there.
func wrapItems(items []string, sep string, width int) []string {
	var out []string
	cur, cw := "", 0
	sw := lipgloss.Width(sep)
	for _, it := range items {
		w := lipgloss.Width(it)
		switch {
		case cw == 0:
			cur, cw = it, w
		case cw+sw+w <= width:
			cur, cw = cur+sep+it, cw+sw+w
		default:
			out = append(out, cur)
			cur, cw = it, w
		}
	}
	if cw > 0 {
		out = append(out, cur)
	}
	return wrapLines(out, width)
}

func stripANSI(s string) string { return ansi.Strip(s) }

// termSafe drops emoji variation selectors and zero-width joiners from
// user-supplied names (vault names like "♾️ HyperGrowth ♾️"). Terminals
// disagree on how wide those sequences are, which breaks column alignment;
// without them every glyph has one agreed width.
func termSafe(s string) string {
	s = domain.CleanText(s)
	if !strings.ContainsAny(s, "\ufe0f\ufe0e\u200d") {
		return s
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '\ufe0f', '\ufe0e', '\u200d':
			return -1
		}
		return r
	}, s)
}

// as extracts T from v when v is a T or a *T.
func as[T any](v any) (T, bool) {
	switch x := v.(type) {
	case T:
		return x, true
	case *T:
		if x != nil {
			return *x, true
		}
	}
	var zero T
	return zero, false
}

// fallbackCard dumps unknown data as indented JSON (still readable by humans
// and agents) instead of failing.
func (p *painter) fallbackCard(r *core.Result) card {
	c := card{title: r.Title}
	if c.title == "" {
		c.title = string(r.Card)
	}
	b, err := json.MarshalIndent(r.Data, "", "  ")
	if err != nil {
		c.body = []string{p.st.muted.Render(fmt.Sprintf("%v", r.Data))}
		return c
	}
	lines := strings.Split(string(b), "\n")
	limit := 40
	if p.o.Compact {
		limit = 12
	}
	if p.o.Height > 0 {
		limit = max(p.o.Height-2, 3)
	}
	if len(lines) > limit {
		more := len(lines) - limit
		lines = append(lines[:limit], p.st.dim.Render(fmt.Sprintf("+%d", more)))
	}
	c.body = lines
	return c
}

// safeTerminal is the last line of defence against third-party text driving
// the terminal (SECURITY S3): whatever a renderer let through, the card that
// leaves Render keeps only SGR styling (ESC [ … m) and drops every other
// escape sequence whole (OSC 52 clipboard writes and titles, cursor moves,
// DCS/APC strings), every other C0/C1 control except newline and tab, DEL,
// bidi overrides and zero-width characters. Plain cards keep no escapes at
// all. Mappers clean text at ingestion too (domain.CleanText); this pass
// covers data that reaches a card any other way.
func safeTerminal(s string, plain bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c == 0x1b {
			i = skipEscape(s, i, plain, &b)
			continue
		}
		if c < 0x20 && c != '\n' && c != '\t' || c == 0x7f {
			i++
			continue
		}
		if c < 0x80 {
			b.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size <= 1:
		case r >= 0x80 && r < 0xa0:
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r >= 0x200b && r <= 0x200c, r >= 0x200e && r <= 0x200f, r == 0xfeff:
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// skipEscape consumes the escape sequence at s[i] (s[i] == ESC) and returns
// the index after it, writing it to b only when it is an SGR sequence and
// plain is false.
func skipEscape(s string, i int, plain bool, b *strings.Builder) int {
	if i+1 >= len(s) {
		return i + 1
	}
	switch s[i+1] {
	case '[': // CSI: params 0x30-0x3f, intermediates 0x20-0x2f, final 0x40-0x7e
		j := i + 2
		for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
			j++
		}
		params := s[i+2 : j]
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
			j++
		}
		if j >= len(s) {
			return j
		}
		final := s[j]
		if final < 0x40 || final > 0x7e {
			return j // malformed: drop the introducer and params
		}
		if final == 'm' && !plain && j == i+2+len(params) && strings.Trim(params, "0123456789;:") == "" {
			b.WriteString(s[i : j+1])
		}
		return j + 1
	case ']', 'P', 'X', '^', '_': // string sequences end at BEL or ST (ESC \)
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
		}
		return len(s)
	}
	return i + 2 // two-byte escape (ESC c, ESC 7, …)
}

// schemaCard: the tool list as a table, or one tool's schema as compact
// JSON (an agent or a person reads it whole; indented JSON clipped at 40
// lines cut descriptions mid-word).
func (p *painter) schemaCard(r *core.Result) card {
	ts, ok := as[[]core.ToolSchema](r.Data)
	if !ok {
		return p.fallbackCard(r)
	}
	c := card{title: r.Title}
	if c.title == "" {
		c.title = "Tools"
	}
	detail := len(ts) == 1 && (ts[0].Input != nil || ts[0].Output != nil)
	if !detail {
		c.focal, c.focalStyle = strconv.Itoa(len(ts)), p.st.bold
		n := min(len(ts), p.rowLimit(60))
		rows := make([][]cell, 0, n)
		for _, t := range ts[:n] {
			rows = append(rows, []cell{p.c(t.Name), p.cm(t.Command), p.c(t.Summary)})
		}
		cols := []column{{title: "tool", keep: 9}, {title: "command", keep: 5}, {title: "summary", flex: true, minW: 10, keep: 3}}
		c.body = p.more(p.table(cols, rows, p.w, nil), len(ts), n)
		return c
	}
	t := ts[0]
	c.title, c.sub = t.Name, t.Command
	var lines []string
	if t.Summary != "" {
		lines = append(lines, wrapLines([]string{p.st.text.Render(t.Summary)}, p.w)...)
	}
	block := func(label string, v any) {
		bs, err := json.Marshal(v)
		if err != nil || string(bs) == "null" {
			return
		}
		lines = append(lines, p.section(label))
		lines = append(lines, hardWrap(string(bs), p.w)...)
	}
	block("input", t.Input)
	block("output", t.Output)
	limit := 40
	if p.o.Compact {
		limit = 12
	}
	if p.o.Height > 0 {
		limit = max(p.o.Height-2, 3)
	}
	if p.o.AllRows {
		limit = 1 << 30
	}
	if len(lines) > limit {
		more := len(lines) - limit
		lines = append(lines[:limit], p.st.dim.Render(fmt.Sprintf("+%d", more)))
	}
	c.body = lines
	return c
}

// hardWrap cuts s into lines of at most w columns (for unbroken JSON).
func hardWrap(s string, w int) []string {
	w = max(w, 8)
	var out []string
	r := []rune(s)
	for len(r) > w {
		out = append(out, string(r[:w]))
		r = r[w:]
	}
	if len(r) > 0 {
		out = append(out, string(r))
	}
	return out
}
