package main

// parse.go turns ANSI text (a tmux `capture-pane -p -e` dump, or any program
// output) into a cell grid. It understands SGR (16, 256 and truecolor, with
// ";" or ":" separators; bold, dim, italic, underline, reverse, strike,
// hidden), skips every other escape sequence, measures East-Asian wide
// characters as two cells and attaches zero-width runes to the previous cell.

import (
	"image/color"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// colorKind tells how a cell color is set: the terminal default, an indexed
// palette entry (0-255) or a direct RGB value.
type colorKind uint8

const (
	colDefault colorKind = iota
	colIndex
	colRGB
)

type tcolor struct {
	kind colorKind
	idx  uint8
	rgb  color.RGBA
}

type attrs struct {
	fg, bg                                      tcolor
	bold, dim, italic, under, rev, strike, hide bool
	ulColor                                     tcolor
}

type cell struct {
	text  string // grapheme (base rune plus any combining marks); "" = blank
	wide  bool   // first half of a 2-cell glyph
	cont  bool   // second half of a 2-cell glyph (draw nothing)
	attrs attrs
}

type grid struct {
	w, h  int
	cells []cell // row-major, len w*h
}

func (g *grid) at(x, y int) *cell { return &g.cells[y*g.w+x] }

// parseANSI lays the text out on a grid. cols <= 0 sizes the grid to the
// widest line; rows <= 0 to the number of lines. Lines wider than cols wrap
// (so `capture-pane -J` output still lands on the right rows); extra rows are
// dropped.
func parseANSI(src string, cols, rows int) *grid {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	// First pass: lay out into variable-length rows.
	var lines [][]cell
	var cur []cell
	var a attrs
	x := 0
	put := func(c cell) {
		for len(cur) < x {
			cur = append(cur, cell{attrs: attrs{bg: a.bg}})
		}
		if x < len(cur) {
			cur[x] = c
		} else {
			cur = append(cur, c)
		}
		x++
	}
	newline := func() {
		lines = append(lines, cur)
		cur, x = nil, 0
	}
	i := 0
	for i < len(src) {
		b := src[i]
		switch {
		case b == 0x1b:
			n, sgr, params := escape(src[i:])
			if sgr {
				applySGR(&a, params)
			}
			i += n
			continue
		case b == '\n':
			newline()
			i++
			continue
		case b == '\r':
			x = 0
			i++
			continue
		case b == '\t':
			next := (x/8 + 1) * 8
			for x < next {
				put(cell{text: " ", attrs: a})
			}
			i++
			continue
		case b < 0x20 || b == 0x7f:
			i++
			continue
		}
		r, sz := utf8.DecodeRuneInString(src[i:])
		i += sz
		if r == utf8.RuneError && sz <= 1 {
			continue
		}
		w := ansi.StringWidth(string(r))
		if w == 0 && !unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) && !(r >= 0xFE00 && r <= 0xFE0F) {
			w = 1 // private-use (powerline, nerd font) and other unassigned widths
		}
		if w == 0 {
			// Combining mark / ZWJ / variation selector: join to the previous cell.
			if x > 0 && x-1 < len(cur) {
				p := x - 1
				if cur[p].cont && p > 0 {
					p--
				}
				cur[p].text += string(r)
			}
			continue
		}
		if cols > 0 && x+w > cols {
			newline()
		}
		if w >= 2 {
			put(cell{text: string(r), wide: true, attrs: a})
			put(cell{cont: true, attrs: a})
		} else {
			put(cell{text: string(r), attrs: a})
		}
	}
	if len(cur) > 0 || (len(src) > 0 && src[len(src)-1] != '\n') {
		newline()
	}
	// Drop the trailing empty lines a capture ends with only when the caller
	// did not ask for a fixed height (rows pads them back as blank anyway).
	if rows <= 0 {
		for len(lines) > 0 && blankLine(lines[len(lines)-1]) {
			lines = lines[:len(lines)-1]
		}
	}
	w := cols
	if w <= 0 {
		for _, l := range lines {
			w = max(w, len(l))
		}
	}
	h := rows
	if h <= 0 {
		h = len(lines)
	}
	w, h = max(w, 1), max(h, 1)
	g := &grid{w: w, h: h, cells: make([]cell, w*h)}
	for y := 0; y < h && y < len(lines); y++ {
		for x, c := range lines[y] {
			if x >= w {
				break
			}
			g.cells[y*w+x] = c
		}
		// A capture that keeps trailing spaces carries their background; a
		// line that ends early fills with the terminal default, like a
		// terminal after an erase-to-end-of-line with default colors.
	}
	return g
}

func blankLine(l []cell) bool {
	for _, c := range l {
		if (c.text != "" && c.text != " ") || c.attrs.bg.kind != colDefault {
			return false
		}
	}
	return true
}

// escape measures the escape sequence at s[0] (ESC). It reports whether it
// is an SGR (CSI ... m) and, if so, its raw parameter string.
func escape(s string) (n int, sgr bool, params string) {
	if len(s) < 2 {
		return len(s), false, ""
	}
	switch s[1] {
	case '[': // CSI: params 0x30-0x3F, intermediates 0x20-0x2F, final 0x40-0x7E
		j := 2
		for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
			j++
		}
		p := s[2:j]
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
			j++
		}
		if j >= len(s) {
			return len(s), false, ""
		}
		return j + 1, s[j] == 'm', p
	case ']', 'P', '_', '^', 'X': // OSC, DCS, APC, PM, SOS: up to BEL or ST
		for j := 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1, false, ""
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2, false, ""
			}
		}
		return len(s), false, ""
	case '(', ')', '*', '+', '#', '%', ' ':
		return min(3, len(s)), false, ""
	default:
		return 2, false, ""
	}
}

// applySGR updates attributes from an SGR parameter string. Both the
// "38;2;r;g;b" and the ITU "38:2::r:g:b" forms are accepted.
func applySGR(a *attrs, params string) {
	if params == "" {
		*a = attrs{}
		return
	}
	groups := strings.Split(params, ";")
	num := func(s string) int {
		v, err := strconv.Atoi(s)
		if err != nil {
			return 0
		}
		return v
	}
	for i := 0; i < len(groups); i++ {
		g := groups[i]
		sub := strings.Split(g, ":")
		code := num(sub[0])
		// extColor reads 38/48/58 arguments from colon sub-params or the
		// following semicolon groups.
		extColor := func() (tcolor, bool) {
			var args []int
			if len(sub) > 1 {
				for _, s := range sub[1:] {
					args = append(args, num(s))
				}
				if len(args) >= 1 && args[0] == 2 && len(args) == 5 { // 38:2:cs:r:g:b
					args = append(args[:1], args[2:]...)
				}
				if len(args) >= 1 && args[0] == 2 && len(args) >= 5 {
					args = append(args[:1], args[len(args)-3:]...)
				}
			} else {
				if i+1 >= len(groups) {
					return tcolor{}, false
				}
				switch num(groups[i+1]) {
				case 5:
					if i+2 < len(groups) {
						args = []int{5, num(groups[i+2])}
						i += 2
					}
				case 2:
					if i+4 < len(groups) {
						args = []int{2, num(groups[i+2]), num(groups[i+3]), num(groups[i+4])}
						i += 4
					}
				}
			}
			if len(args) >= 2 && args[0] == 5 {
				return tcolor{kind: colIndex, idx: uint8(args[1])}, true
			}
			if len(args) >= 4 && args[0] == 2 {
				return tcolor{kind: colRGB, rgb: color.RGBA{uint8(args[1]), uint8(args[2]), uint8(args[3]), 255}}, true
			}
			return tcolor{}, false
		}
		switch {
		case code == 0:
			*a = attrs{}
		case code == 1:
			a.bold = true
		case code == 2:
			a.dim = true
		case code == 3:
			a.italic = true
		case code == 4:
			a.under = !(len(sub) > 1 && sub[1] == "0")
		case code == 7:
			a.rev = true
		case code == 8:
			a.hide = true
		case code == 9:
			a.strike = true
		case code == 21:
			a.under = true
		case code == 22:
			a.bold, a.dim = false, false
		case code == 23:
			a.italic = false
		case code == 24:
			a.under = false
		case code == 27:
			a.rev = false
		case code == 28:
			a.hide = false
		case code == 29:
			a.strike = false
		case code >= 30 && code <= 37:
			a.fg = tcolor{kind: colIndex, idx: uint8(code - 30)}
		case code == 38:
			if c, ok := extColor(); ok {
				a.fg = c
			}
		case code == 39:
			a.fg = tcolor{}
		case code >= 40 && code <= 47:
			a.bg = tcolor{kind: colIndex, idx: uint8(code - 40)}
		case code == 48:
			if c, ok := extColor(); ok {
				a.bg = c
			}
		case code == 49:
			a.bg = tcolor{}
		case code == 58:
			if c, ok := extColor(); ok {
				a.ulColor = c
			}
		case code == 59:
			a.ulColor = tcolor{}
		case code >= 90 && code <= 97:
			a.fg = tcolor{kind: colIndex, idx: uint8(code - 90 + 8)}
		case code >= 100 && code <= 107:
			a.bg = tcolor{kind: colIndex, idx: uint8(code - 100 + 8)}
		}
	}
}
