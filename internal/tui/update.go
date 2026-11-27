package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/maxmoneycash/hlp-cli/internal/theme"
)

// Update implements tea.Model. After handling msg it re-lays the frame
// (which tells us what is visible) and reconciles live subscriptions so
// only visible, focused views stream. Messages that cannot change what is
// on screen (a price tick nobody shows, a repeated WS state, mouse motion
// outside the chart) skip the frame entirely (PERFORMANCE #5).
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	quiet := m.quiet(msg)
	cmd := m.update(msg)
	if !quiet || m.frame == nil {
		m.setFrame(m.render())
	}
	return m, tea.Batch(cmd, m.reconcileLive(), m.motionCmd())
}

// setFrame stores the frame as one joined string: View serves it as is.
func (m *Model) setFrame(lines []string) {
	if r := m.groundReplacer(); r != nil {
		// Most lines repeat from frame to frame: look them up instead of
		// rescanning (a map hit is far cheaper than a Replace).
		out := make([]string, len(lines))
		for i, l := range lines {
			v, ok := m.groundLines[l]
			if !ok {
				v = l
				if strings.Contains(l, m.groundKey) {
					v = r.Replace(l)
				}
				if len(m.groundLines) > 4096 {
					clear(m.groundLines)
				}
				m.groundLines[l] = v
			}
			out[i] = v
		}
		lines = out
	}
	m.frame = []string{strings.Join(lines, "\n")}
}

// groundReplacer turns cells painted with the theme ground into the
// terminal's default background while hlp sets that default to the same
// ground (OSC 11). Opaque terminals look the same; translucent ones (Warp,
// Ghostty, iTerm over a wallpaper) show one even glass instead of opaque
// blocks wherever a chart or pane painted its ground. nil when hlp does
// not paint the ground.
func (m *Model) groundReplacer() *strings.Replacer {
	if !m.paintsBackground() {
		return nil
	}
	bg := m.th.BG
	var k string
	switch {
	case m.profile == colorprofile.TrueColor && bg.True.Valid:
		k = fmt.Sprintf("48;2;%d;%d;%d", bg.True.R, bg.True.G, bg.True.B)
	case m.profile == colorprofile.ANSI256 && bg.C256 >= 0:
		k = fmt.Sprintf("48;5;%d", bg.C256)
	default:
		return nil
	}
	if m.groundRep == nil || m.groundKey != k {
		m.groundLines = map[string]string{}
		m.groundKey, m.groundRep = k, strings.NewReplacer("["+k+"m", "[49m", "["+k+";", "[49;", ";"+k+"m", ";49m", ";"+k+";", ";49;")
	}
	return m.groundRep
}

// quiet reports, before msg is applied, that it changes nothing visible.
func (m *Model) quiet(msg tea.Msg) bool {
	switch x := msg.(type) {
	case midsMsg:
		if !m.active(x.sub) {
			return true
		}
		if len(x.prevDay) > 0 {
			for k, v := range x.prevDay {
				if m.prevDay[k] != v {
					return false
				}
			}
		}
		// A price that moves below the shown precision changes nothing on
		// screen: compare what would be drawn, not the raw mids.
		for _, c := range m.shownMids() {
			if v, ok := x.mids[c]; ok && m.mids[c] != v && priceText(v, m.prevDay[c]) != priceText(m.mids[c], m.prevDay[c]) {
				return false
			}
		}
		return true
	case acctMsg:
		return !m.active(x.sub) || !m.sideShown()
	case wsStateMsg:
		return string(x) == m.wsState
	case tea.MouseMotionMsg:
		if p := m.topPane(); p != nil {
			return p.kind != paneChart
		}
		// In the transcript, motion matters only when it moves the reticle.
		return m.hoverTarget(x.Mouse().Y) == m.hoverEntry
	case blinkMsg:
		return !m.blinkLive()
	}
	return false
}

// sideShown reports whether the watchlist column is on screen.
func (m *Model) sideShown() bool {
	return m.showSide && m.width >= sideMinWidth && m.topPane() == nil && !m.showHelp && m.palette == nil
}

// hoverTarget is the entry that should carry the reticle for a pointer on
// screen row y: a card, or 0 to fall back to the newest card.
func (m *Model) hoverTarget(y int) int {
	if m.welcomeOnly() || m.showHelp || m.palette != nil {
		return 0
	}
	id := m.entryAt(y)
	if e := m.entryByID(id); e != nil {
		if focusable(e) {
			return id
		}
		// the echo line belongs to the card under it
		for i, x := range m.entries {
			if x == e && x.kind == entryUser && i+1 < len(m.entries) && focusable(m.entries[i+1]) {
				return m.entries[i+1].id
			}
		}
	}
	return m.hoverEntry // between cards the reticle stays where it was
}

// shownMids lists the coins whose price is on screen right now: the
// watchlist column and the top bar's ticker.
func (m *Model) shownMids() []string {
	var out []string
	if m.sideShown() {
		out = append(out, m.sideCoins()...)
	}
	if m.tickerShown() {
		out = append(out, m.welcomeCoins()...)
	}
	return out
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(msg.Width, 20), max(msg.Height, 6)
		return nil
	case tea.BackgroundColorMsg:
		m.termBG, m.termDark = true, msg.IsDark()
		if !m.themeExplicit {
			m.th = theme.Default(msg.IsDark())
			m.restyle()
		}
		return nil
	case flapMsg:
		m.flapArmed = false
		return nil
	case blinkMsg:
		m.blinkArmed = false
		return nil
	case tea.ColorProfileMsg:
		if m.env == nil || m.env.Profile == 0 {
			m.profile = msg.Profile
			m.restyle()
		}
		return nil
	case tea.FocusMsg:
		m.focused = true
		m.lastKey = m.now() // the cursor comes back solid
		return nil
	case tea.BlurMsg:
		m.focused = false
		return nil
	case tea.PasteMsg:
		if m.confirm != nil {
			return nil
		}
		if p := m.topPane(); p != nil {
			if p.kind == paneVaults && p.filtering {
				p.filter.Insert(msg.Content)
				p.applyVaultView()
			}
			return nil
		}
		if m.palette != nil {
			m.palette.ed.Insert(msg.Content)
			return nil
		}
		m.in.Insert(msg.Content)
		m.compOff, m.compSel = false, 0
		return nil
	case tea.KeyPressMsg:
		return m.key(msg)
	case tea.MouseMsg:
		return m.mouse(msg)
	case resultMsg:
		return m.onResult(msg)
	case spinMsg:
		return m.onSpin()
	case agentEventMsg:
		return m.onAgentEvent(msg)
	case agentDoneMsg:
		return m.onAgentDone(msg)
	case liveMsg, candleMsg, midsMsg, acctMsg:
		return m.onLive(msg)
	case wsStateMsg:
		m.wsState = string(msg)
		if m.opts.WSFeed != nil {
			return waitWSState(m.opts.WSFeed.C())
		}
		return nil
	case confirmReqMsg:
		return m.openConfirm(confirmReq(msg))
	}
	// Anything else (cursor blinks and the like) belongs to the confirm form.
	if m.confirm != nil && m.confirm.form != nil {
		return m.formUpdate(msg)
	}
	return nil
}

// key handles a key press, most specific owner first: confirm card, quit
// chord, help, palette, global toggles, the open pane, then the prompt.
func (m *Model) key(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	m.lastKey = m.now() // any key shows the cursor (the blink restarts)
	if m.confirm != nil {
		return m.confirmKey(msg)
	}
	if k == "ctrl+c" {
		if m.ctrlC {
			m.quitting = true
			m.interrupt()
			return tea.Quit
		}
		m.ctrlC = true
		m.interrupt()
		m.in.Reset()
		return nil
	}
	m.ctrlC = false
	if m.showHelp {
		m.showHelp = false
		return nil
	}
	if m.palette != nil {
		return m.paletteKey(msg)
	}
	switch k {
	case "ctrl+t":
		m.cycleTheme()
		return nil
	case "ctrl+b":
		m.showSide = !m.showSide
		return nil
	case "ctrl+k", "f1": // f1: VS Code's terminal keeps ctrl+k (RELEASE R11)
		m.palette = &palette{}
		return nil
	case "ctrl+o":
		if m.topPane() == nil {
			return m.openLatest()
		}
	}
	if p := m.topPane(); p != nil {
		return m.paneKey(p, msg)
	}
	m.hoverEntry = 0 // typing puts focus back on the prompt and the newest card
	comp := m.completions()
	switch k {
	case "esc":
		switch {
		case len(comp) > 0:
			m.compOff = true
		case m.interrupt():
		default:
			m.in.Reset()
		}
		return nil
	case "tab":
		if len(comp) > 0 {
			m.acceptCompletion(comp)
		} else if full, _, ok := tagCompletion(m.env, m.in.Value()); ok {
			m.in.Set(full)
		}
		return nil
	case "enter":
		if len(comp) > 0 {
			typed := strings.TrimPrefix(m.in.Value(), "/")
			c := comp[min(m.compSel, len(comp)-1)]
			if c.slash != strings.ToLower(typed) && isPrefixMatch(c, typed) {
				m.acceptCompletion(comp)
				if c.needsArgs {
					return nil
				}
			}
		}
		if m.in.Empty() {
			return m.openLatest()
		}
		return m.submit(m.in.Value())
	case "up":
		if len(comp) > 0 {
			m.compSel = (m.compSel + len(comp) - 1) % len(comp)
			return nil
		}
		if !m.in.Multiline() {
			if s, ok := m.hist.Prev(m.in.Value()); ok {
				m.in.Set(s)
				m.compOff = true
			}
		}
		return nil
	case "down":
		if len(comp) > 0 {
			m.compSel = (m.compSel + 1) % len(comp)
			return nil
		}
		if !m.in.Multiline() {
			if s, ok := m.hist.Next(); ok {
				m.in.Set(s)
				m.compOff = true
			}
		}
		return nil
	case "pgup", "shift+up":
		m.scroll += m.pageSize(k)
		return nil
	case "pgdown", "shift+down":
		m.scroll = max(m.scroll-m.pageSize(k), 0)
		return nil
	case "?":
		if m.in.Empty() {
			m.showHelp = true
			return nil
		}
	}
	before := m.in.Value()
	if m.in.HandleKey(msg) && m.in.Value() != before {
		m.compOff, m.compSel = false, 0
	}
	return nil
}

func (m *Model) pageSize(k string) int {
	if strings.HasPrefix(k, "shift") {
		return 1
	}
	return max(m.height/2, 1)
}

// completions returns the autocomplete list while a slash name is typed.
func (m *Model) completions() []command {
	v := m.in.Value()
	if m.compOff || !strings.HasPrefix(v, "/") || strings.ContainsAny(v, " \n") || m.topPane() != nil {
		return nil
	}
	return matchCommands(m.cmds, v)
}

func (m *Model) acceptCompletion(comp []command) {
	c := comp[min(m.compSel, len(comp)-1)]
	m.in.Set("/" + c.slash + " ")
	m.compSel = 0
}

// mouse scrolls the transcript, opens a clicked card, or drives the pane.
func (m *Model) mouse(msg tea.MouseMsg) tea.Cmd {
	if m.confirm != nil {
		return nil
	}
	if p := m.topPane(); p != nil {
		m.paneMouse(p, msg)
		return nil
	}
	mo := msg.Mouse()
	switch msg.(type) {
	case tea.MouseWheelMsg:
		if mo.Button == tea.MouseWheelUp {
			m.scroll += 3
		} else {
			m.scroll = max(m.scroll-3, 0)
		}
	case tea.MouseMotionMsg:
		m.hoverEntry = m.hoverTarget(mo.Y)
	case tea.MouseClickMsg:
		if mo.Button != tea.MouseLeft || mo.Y < 1 {
			return nil
		}
		if id := m.hoverTarget(mo.Y); id != 0 {
			return m.openEntry(m.entryByID(id))
		}
	}
	return nil
}

// palette is the ctrl+k command palette.
type palette struct {
	ed  editor
	sel int
}

func (m *Model) paletteKey(msg tea.KeyPressMsg) tea.Cmd {
	p := m.palette
	list := matchCommands(m.cmds, p.ed.Value())
	switch msg.String() {
	case "esc", "ctrl+k":
		m.palette = nil
	case "up":
		if len(list) > 0 {
			p.sel = (p.sel + len(list) - 1) % len(list)
		}
	case "down", "tab":
		if len(list) > 0 {
			p.sel = (p.sel + 1) % len(list)
		}
	case "enter":
		m.palette = nil
		if len(list) == 0 {
			return nil
		}
		c := list[min(p.sel, len(list)-1)]
		if !c.needsArgs {
			return m.submit("/" + c.slash)
		}
		m.in.Set("/" + c.slash + " ")
		m.compOff = true
	default:
		if p.ed.HandleKey(msg) {
			p.sel = 0
		}
	}
	return nil
}
