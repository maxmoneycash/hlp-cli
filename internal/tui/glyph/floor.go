package glyph

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Floor grid geometry (DESIGN §4.3): a dot every 4 columns from column 2, on
// every other row.
const (
	FloorCol0  = 2
	FloorStepX = 4
	FloorStepY = 2
)

// Floor draws the dotted floor behind lines, the empty-state backdrop. Row i
// is screen row y0+i; dots go on even screen rows at columns 2, 6, 10 … below
// width, and only in blank cells after the line's last visible cell, so text
// always wins. Trailing plain spaces count as blank; trailing cells that
// carry any styling (a background, say) are kept as they are.
//
// dot styles the `·` (Floor colour). A nil dot returns lines unchanged: that
// is how 16 colours, ASCII and `ui.floor = false` turn the floor off.
func Floor(lines []string, width, y0 int, dot func(string) string) []string {
	if dot == nil {
		return lines
	}
	d := dot(Dot)
	out := make([]string, len(lines))
	for i, line := range lines {
		y := y0 + i
		if y%FloorStepY != 0 {
			out[i] = line
			continue
		}
		head, used := contentPrefix(line)
		var b strings.Builder
		col := used
		for c := FloorCol0; c < width; c += FloorStepX {
			if c < used {
				continue
			}
			if b.Len() == 0 {
				b.WriteString(head)
				if strings.ContainsRune(head, '\x1b') {
					b.WriteString("\x1b[m")
				}
			}
			b.WriteString(strings.Repeat(" ", c-col))
			b.WriteString(d)
			col = c + 1
		}
		if b.Len() == 0 {
			out[i] = line
		} else {
			out[i] = b.String()
		}
	}
	return out
}

// contentPrefix returns the part of line up to its last visible non-blank
// cell and that width. Trailing spaces are dropped only when nothing styles
// them: if any escape sequence other than a reset follows the last glyph, the
// whole line counts as content.
func contentPrefix(line string) (string, int) {
	full := lipgloss.Width(line)
	used := lipgloss.Width(strings.TrimRight(ansi.Strip(line), " "))
	if used == full {
		return line, full
	}
	tail := ansi.TruncateLeft(line, used, "")
	if hasStyling(tail) {
		return line, full
	}
	return ansi.Truncate(line, used, ""), used
}

// hasStyling reports whether s holds an escape sequence that is not a plain
// SGR reset.
func hasStyling(s string) bool {
	for {
		i := strings.IndexByte(s, '\x1b')
		if i < 0 {
			return false
		}
		s = s[i:]
		switch {
		case strings.HasPrefix(s, "\x1b[m"):
			s = s[3:]
		case strings.HasPrefix(s, "\x1b[0m"):
			s = s[4:]
		default:
			return true
		}
	}
}
