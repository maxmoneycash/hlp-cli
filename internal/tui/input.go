package tui

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// editor is the prompt's line editor: a rune buffer with a cursor. It is
// deliberately small (no textarea) so the prompt row is exactly what we
// draw: "❯ text" with a block cursor, wrapping onto continuation rows.
type editor struct {
	buf []rune
	pos int
}

func (e *editor) Value() string { return string(e.buf) }

func (e *editor) Set(s string) {
	e.buf = []rune(s)
	e.pos = len(e.buf)
}

func (e *editor) Reset() { e.buf, e.pos = nil, 0 }

func (e *editor) Empty() bool { return len(e.buf) == 0 }

func (e *editor) Insert(s string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	rs := []rune(s)
	e.buf = append(e.buf[:e.pos], append(rs, e.buf[e.pos:]...)...)
	e.pos += len(rs)
}

// Multiline reports whether the buffer has more than one line.
func (e *editor) Multiline() bool { return strings.ContainsRune(string(e.buf), '\n') }

// HandleKey applies an editing key. It returns false for keys it does not
// own (enter, tab, up/down, ...), which the model handles.
func (e *editor) HandleKey(k tea.KeyPressMsg) bool {
	switch k.String() {
	case "left":
		e.pos = max(e.pos-1, 0)
	case "right":
		e.pos = min(e.pos+1, len(e.buf))
	case "home", "ctrl+a":
		e.pos = 0
	case "end", "ctrl+e":
		e.pos = len(e.buf)
	case "alt+left", "alt+b", "ctrl+left":
		e.pos = e.wordLeft()
	case "alt+right", "alt+f", "ctrl+right":
		e.pos = e.wordRight()
	case "backspace", "ctrl+h":
		if e.pos > 0 {
			e.buf = append(e.buf[:e.pos-1], e.buf[e.pos:]...)
			e.pos--
		}
	case "delete", "ctrl+d":
		if e.pos < len(e.buf) {
			e.buf = append(e.buf[:e.pos], e.buf[e.pos+1:]...)
		}
	case "ctrl+w", "alt+backspace":
		l := e.wordLeft()
		e.buf = append(e.buf[:l], e.buf[e.pos:]...)
		e.pos = l
	case "ctrl+u":
		e.buf = e.buf[e.pos:]
		e.pos = 0
	case "shift+enter", "alt+enter", "ctrl+j":
		e.Insert("\n")
	case "space":
		e.Insert(" ")
	default:
		if k.Text != "" && k.Mod&^tea.ModShift == 0 {
			e.Insert(k.Text)
			return true
		}
		return false
	}
	return true
}

func (e *editor) wordLeft() int {
	i := e.pos
	for i > 0 && unicode.IsSpace(e.buf[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(e.buf[i-1]) {
		i--
	}
	return i
}

func (e *editor) wordRight() int {
	i := e.pos
	for i < len(e.buf) && unicode.IsSpace(e.buf[i]) {
		i++
	}
	for i < len(e.buf) && !unicode.IsSpace(e.buf[i]) {
		i++
	}
	return i
}

// View renders the prompt rows at width: the prompt on the first row, a
// matching indent on continuation rows, a reverse-video block cursor, and
// the placeholder (muted) when empty.
func (e *editor) View(width int, prompt, placeholder string, promptSt, textSt, mutedSt, cursorSt lipgloss.Style, focused bool) []string {
	pw := lipgloss.Width(prompt) + 1
	avail := max(width-pw, 4)
	indent := strings.Repeat(" ", pw)
	head := promptSt.Render(prompt) + " "
	if len(e.buf) == 0 {
		cur := cursorSt.Render(" ")
		if !focused {
			cur = ""
		}
		ph := ""
		if placeholder != "" {
			ph = mutedSt.Render(ansi.Truncate(placeholder, avail-1, "…"))
		}
		return []string{head + cur + ph}
	}
	// Lay the runes out into visual rows, tracking the cursor cell.
	var rows [][]rune
	var row []rune
	curRow, curCol := 0, 0
	col := 0
	for i := 0; i <= len(e.buf); i++ {
		if i == e.pos {
			curRow, curCol = len(rows), col
			if col >= avail {
				curRow, curCol = len(rows)+1, 0
			}
		}
		if i == len(e.buf) {
			break
		}
		r := e.buf[i]
		if r == '\n' {
			rows = append(rows, row)
			row, col = nil, 0
			continue
		}
		w := max(ansi.StringWidth(string(r)), 1)
		if col+w > avail {
			rows = append(rows, row)
			row, col = nil, 0
			if i == e.pos {
				curRow, curCol = len(rows), 0
			}
		}
		row = append(row, r)
		col += w
	}
	rows = append(rows, row)
	if curRow >= len(rows) {
		rows = append(rows, nil)
	}
	out := make([]string, len(rows))
	for ri, r := range rows {
		lead := indent
		if ri == 0 {
			lead = head
		}
		if ri != curRow || !focused {
			out[ri] = lead + textSt.Render(string(r))
			continue
		}
		// Split at the cursor column (in cells).
		var before, at, after []rune
		c := 0
		for _, ch := range r {
			switch {
			case c < curCol:
				before = append(before, ch)
			case c == curCol && at == nil:
				at = []rune{ch}
			default:
				after = append(after, ch)
			}
			c += max(ansi.StringWidth(string(ch)), 1)
		}
		cur := " "
		if at != nil {
			cur = string(at)
		}
		out[ri] = lead + textSt.Render(string(before)) + cursorSt.Render(cur) + textSt.Render(string(after))
	}
	return out
}
