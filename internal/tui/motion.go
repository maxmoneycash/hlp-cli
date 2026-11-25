package tui

import (
	"math"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/maxmoneycash/hlp-cli/internal/tui/cards"
	"github.com/maxmoneycash/hlp-cli/internal/tui/glyph"
)

// Motion with a cause (DESIGN.md section 7). Only two things move: the
// split-flap ticker when a displayed character changes, and the prompt
// cursor's blink. Neither runs a free timer: a tick is scheduled only
// while a flap is in flight or the blink is due, and only while the
// terminal has focus and motion is not reduced.

// flapMsg advances the ticker's flaps.
type flapMsg struct{}

// blinkMsg toggles the prompt cursor.
type blinkMsg struct{}

// tickerMinWidth: under 60 columns there is no ticker (DESIGN.md 6.3).
const tickerMinWidth = 60

// tickerShown reports whether the top bar carries the ticker right now.
func (m *Model) tickerShown() bool {
	// Help and the palette cover the transcript: the ticker steps aside
	// with it, so only the overlay talks.
	return m.topPane() == nil && m.width >= tickerMinWidth && !m.showHelp && m.palette == nil
}

// tickerText is a coin's price and 24h change as the ticker shows it:
// "64,951 −0.9%", the change with its sign and one decimal (U+2212 for a
// loss). Empty until the first price arrives.
func (m *Model) tickerText(coin string) string {
	px, ok := m.mids[coin]
	if !ok {
		return ""
	}
	return priceText(px, m.prevDay[coin])
}

// priceText is "64,951 −0.9%" for a price and the price 24h ago (0 =
// unknown: the price alone).
func priceText(px, prevDay float64) string {
	if px == 0 {
		return ""
	}
	s := cards.PxFixed(px)
	if prevDay > 0 {
		s += " " + signedPct1(px/prevDay-1)
	}
	return s
}

// signedPct1 formats a fraction as a signed percent with one decimal and
// U+2212 for negatives (DESIGN.md 2 rule 1).
func signedPct1(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return ""
	}
	v := math.Round(f*1000) / 10
	if v == 0 {
		return "+0.0%"
	}
	s := strconv.FormatFloat(math.Abs(v), 'f', 1, 64) + "%"
	if v < 0 {
		return glyph.Minus + s
	}
	return "+" + s
}

// setFlaps feeds the latest prices to the ticker's flaps. Each flap
// throttles itself (one flip per 2.6 s, latest value wins).
func (m *Model) setFlaps() {
	now := m.now()
	for _, c := range m.welcomeCoins() {
		t := m.tickerText(c)
		if t == "" {
			continue
		}
		f := m.flaps[c]
		if f == nil {
			f = glyph.NewFlap()
			m.flaps[c] = f
		}
		f.Set(t, now)
	}
}

// flapsLive reports whether flaps may animate: the ticker is on screen,
// the terminal has focus and motion is on.
func (m *Model) flapsLive() bool {
	return m.focused && m.tickerShown() && !m.reducedMotion()
}

// blinkLive reports whether the cursor blinks: the prompt owns the
// keyboard, the terminal has focus, nothing is streaming, motion is on.
func (m *Model) blinkLive() bool {
	return m.focused && !m.reducedMotion() && m.topPane() == nil && m.confirm == nil &&
		m.palette == nil && !m.showHelp && len(m.jobs) == 0 && m.agentStop == nil
}

// cursorOn reports whether the prompt's block cursor is drawn this frame.
func (m *Model) cursorOn() bool {
	if !m.blinkLive() {
		return true // solid
	}
	return glyph.CursorOn(m.lastKey, m.now())
}

// motionCmd arms the next flap or blink tick when one is due and none is
// pending. It returns nil when nothing moves (idle, unfocused, reduced).
func (m *Model) motionCmd() tea.Cmd {
	var cmds []tea.Cmd
	now := m.now()
	if !m.flapArmed && m.flapsLive() {
		var next time.Duration
		for _, f := range m.flaps {
			if !f.Busy(now) {
				continue
			}
			if d := f.Next(now); d > 0 && (next == 0 || d < next) {
				next = d
			}
		}
		if next > 0 {
			m.flapArmed = true
			cmds = append(cmds, tea.Tick(next, func(time.Time) tea.Msg { return flapMsg{} }))
		}
	}
	if !m.blinkArmed && m.blinkLive() {
		if d := glyph.BlinkNext(m.lastKey, now); d > 0 {
			m.blinkArmed = true
			cmds = append(cmds, tea.Tick(d, func(time.Time) tea.Msg { return blinkMsg{} }))
		}
	}
	switch len(cmds) {
	case 0:
		return nil
	case 1:
		return cmds[0]
	}
	return tea.Batch(cmds...)
}

// tickerItem renders one coin's flap: the symbol as a muted label, then
// each character of price and change on a Surface cell (the flap face),
// the change in ink or red by its sign. A flipping cell shows ▀ then ▄
// in Muted.
func (m *Model) tickerItem(coin string, now time.Time) string {
	sym := m.st.label.Render(strings.ToUpper(strings.TrimPrefix(coin, "xyz:")))
	var cells []glyph.Cell
	f := m.flaps[coin]
	switch {
	case f == nil:
		t := m.tickerText(coin)
		if t == "" {
			return ""
		}
		for _, r := range t {
			cells = append(cells, glyph.Cell{R: r})
		}
	case m.reducedMotion() || !m.focused:
		for _, r := range f.Text() {
			cells = append(cells, glyph.Cell{R: r})
		}
	default:
		cells = f.Cells(now)
	}
	if len(cells) == 0 {
		return ""
	}
	// The change is everything after the last space; it is red when it
	// carries a minus.
	split := len(cells)
	for i := len(cells) - 1; i >= 0; i-- {
		if cells[i].R == ' ' && cells[i].Phase == glyph.Settled {
			split = i
			break
		}
	}
	neg := false
	if split < len(cells)-1 {
		for _, c := range cells[split+1:] {
			if string(c.R) == glyph.Minus {
				neg = true
			}
		}
	}
	var b strings.Builder
	b.WriteString(sym)
	b.WriteString("  ")
	ink, chg := m.st.text, m.st.text
	if neg {
		chg = m.st.down
	}
	onFace := func(st lipgloss.Style) lipgloss.Style {
		if bg := m.st.flapFace.GetBackground(); bg != nil {
			if _, none := bg.(lipgloss.NoColor); !none {
				return st.Background(bg)
			}
		}
		return st
	}
	inkF, chgF, flipF := onFace(ink), onFace(chg), onFace(m.st.muted)
	var seg strings.Builder
	cur := -1 // 0 price, 1 change, 2 flipping
	flush := func() {
		s := seg.String()
		seg.Reset()
		if s == "" {
			return
		}
		switch cur {
		case 0:
			b.WriteString(inkF.Render(s))
		case 1:
			b.WriteString(chgF.Render(s))
		case 2:
			b.WriteString(flipF.Render(s))
		}
	}
	for i, c := range cells {
		kind := 0
		switch {
		case c.Phase != glyph.Settled:
			kind = 2
		case i > split:
			kind = 1
		}
		if i == split && c.Phase == glyph.Settled {
			flush()
			cur = -1
			b.WriteString(" ") // the gap between price and change is not a flap cell
			continue
		}
		if kind != cur {
			flush()
			cur = kind
		}
		seg.WriteRune(c.R)
	}
	flush()
	return b.String()
}
