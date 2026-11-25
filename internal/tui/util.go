package tui

import (
	"strconv"

	"github.com/charmbracelet/x/ansi"
)

func itoa(i int) string { return strconv.Itoa(i) }

// ansiTruncate cuts an ANSI-styled string to width cells.
func ansiTruncate(s string, width int) string { return ansi.Truncate(s, width, "…") }
