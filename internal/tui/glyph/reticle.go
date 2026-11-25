package glyph

import "strings"

// Reticle returns one row of the corner reticle (DESIGN §4.1), exactly width
// cells wide: `┌─` … `─┐` when top, `└─` … `─┘` otherwise, with plain
// spaces between (never a `─` run: the reticle is corners, not a box). st
// styles each corner; nil leaves them plain. Below 4 cells there is no room
// for two corners and the row is blank.
func Reticle(width int, top bool, st func(string) string) string {
	if width <= 0 {
		return ""
	}
	if width < 4 {
		return strings.Repeat(" ", width)
	}
	l, r := "└─", "─┘"
	if top {
		l, r = "┌─", "─┐"
	}
	if st != nil {
		l, r = st(l), st(r)
	}
	return l + strings.Repeat(" ", width-4) + r
}
