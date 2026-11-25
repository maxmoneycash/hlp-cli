package glyph

import "strings"

// Big digits (DESIGN §3.1): a 3-row display face for the one hero number on
// a pane header. Digits, `+` and `−` are 3 cells wide, the decimal point is
// 1 cell, glyphs are separated by a 1-cell gap, and thousands groups by one
// more blank cell instead of a comma.

type bigGlyph [3]string

var bigFace = map[rune]bigGlyph{
	'0': {"█▀█", "█ █", "▀▀▀"},
	'1': {"▀█ ", " █ ", "▀▀▀"},
	'2': {"▀▀█", "█▀▀", "▀▀▀"},
	'3': {"▀▀█", " ▀█", "▀▀▀"},
	'4': {"█ █", "▀▀█", "  ▀"},
	'5': {"█▀▀", "▀▀█", "▀▀▀"},
	'6': {"█▀▀", "█▀█", "▀▀▀"},
	'7': {"▀▀█", "  █", "  ▀"},
	'8': {"█▀█", "█▀█", "▀▀▀"},
	'9': {"█▀█", "▀▀█", "▀▀▀"},
	'.': {" ", " ", "▀"},
	'+': {" ▄ ", "▀█▀", "   "},
	'−': {"   ", "▀▀▀", "   "},
	' ': {"   ", "   ", "   "},
	'$': {"▄█▄", "█▄▄", "▄▄█"}, // inline card focals only: an S with its stem
}

var wordFace = map[rune]bigGlyph{
	'H': {"█ █", "█▀█", "▀ ▀"},
	'L': {"█  ", "█  ", "▀▀▀"},
	'P': {"█▀█", "█▀▀", "▀  "},
}

// token is one glyph plus the blank cells drawn before it.
type token struct {
	r   rune
	gap int
}

// layout groups the integer part into thousands and returns the glyph run.
// ok is false on any rune the face does not have (including ASCII '-' and
// ','; the caller passes "64953.5", "−0.9" or "+367.9").
func layout(s string) ([]token, bool) {
	rs := []rune(s)
	if len(rs) == 0 {
		return nil, false
	}
	for _, r := range rs {
		if _, ok := bigFace[r]; !ok {
			return nil, false
		}
	}
	// The integer part is the first run of digits; group it from the right.
	start := -1
	end := len(rs)
	for i, r := range rs {
		isDigit := r >= '0' && r <= '9'
		if start < 0 && isDigit {
			start = i
		} else if start >= 0 && !isDigit {
			end = i
			break
		}
	}
	out := make([]token, 0, len(rs))
	for i, r := range rs {
		gap := 1
		if i == 0 {
			gap = 0
		} else if start >= 0 && i > start && i < end && (end-i)%3 == 0 {
			gap = 2 // one extra blank cell between thousands groups
		}
		out = append(out, token{r: r, gap: gap})
	}
	return out, true
}

func draw(ts []token, face map[rune]bigGlyph) [3]string {
	var b [3]strings.Builder
	for _, t := range ts {
		g := face[t.r]
		for row := range 3 {
			b[row].WriteString(strings.Repeat(" ", t.gap))
			b[row].WriteString(g[row])
		}
	}
	return [3]string{b[0].String(), b[1].String(), b[2].String()}
}

// Big draws s in the big-digit face. Every row has the same width,
// BigWidth(s). ok is false (and rows empty) when s holds a rune outside the
// face: the caller then falls back to one bold line.
func Big(s string) (rows [3]string, ok bool) {
	ts, ok := layout(s)
	if !ok {
		return rows, false
	}
	return draw(ts, bigFace), true
}

// BigWidth is the cell width of Big(s), or 0 when s cannot be drawn big.
func BigWidth(s string) int {
	ts, ok := layout(s)
	if !ok {
		return 0
	}
	w := 0
	for _, t := range ts {
		w += t.gap + len([]rune(bigFace[t.r][0]))
	}
	return w
}

// Wordmark returns `HLP` in the big face, for the welcome screen only.
func Wordmark() [3]string {
	return draw([]token{{'H', 0}, {'L', 1}, {'P', 1}}, wordFace)
}
