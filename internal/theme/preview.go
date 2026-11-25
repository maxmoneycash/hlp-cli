package theme

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// previewCloses is a fixed price walk for Preview: a dip and a recovery, so
// both Up and Down appear and the strip ends higher than it starts.
var previewCloses = []float64{
	60, 62, 61, 64, 67, 66, 69, 72, 70, 66, 61, 57, 54, 56, 53, 55, 59, 63, 62, 66,
	70, 69, 73, 76, 74, 78, 81, 79, 76, 78, 82, 85, 84, 88, 86, 90, 93, 91, 95, 98,
}

// PreviewMinWidth is the narrowest width Preview lays out without clipping.
const PreviewMinWidth = 28

// Preview renders a three-line swatch of t for a theme picker or gallery
// (`hlp config themes`, the ctrl+t toast):
//
//	rush                         +2.41%  −1.07%
//	▃▄▃▅▆▅▄▂▁▂▁▂▄▅▄▆▇▆▇█▇█   ━ ━ ━
//	❯ chart BTC 4h                 ███████ ■
//
// The candle strip is the focal point; names, signed values and the prompt
// show the chrome tokens once each. The swatch after the prompt is the
// neutral ladder (Surface, Raised, Floor, Border, Dim, Muted, Text) and the
// one accent. A loss carries U+2212 (DESIGN.md section 2 rule 1), except with
// no color, where machine-readable ASCII stays. Every line is exactly width cells wide
// (minimum PreviewMinWidth). Colors resolve for profile p; at Ascii/NoTTY the
// result carries no color.
func Preview(t Theme, p colorprofile.Profile, width int) string {
	if width < PreviewMinWidth {
		width = PreviewMinWidth
	}
	s := t.Styles(p)

	// Line 1: the name on the left, one up and one down value on the right.
	name := s.Title.Render(t.Name)
	minus := "−"
	if p == colorprofile.Ascii || p == colorprofile.NoTTY {
		minus = "-"
	}
	values := s.Up.Render("+2.41%") + "  " + s.Down.Render(minus+"1.07%")
	line1 := spread(name, values, width)

	// Line 2: the candle strip, then the three MA colors.
	mas := ""
	for i, c := range t.MA {
		if i > 0 {
			mas += " "
		}
		mas += fgStyle(c, p).Render("━")
	}
	stripW := width - lipgloss.Width(mas) - 3
	line2 := strip(t, p, stripW) + "   " + mas
	line2 = pad(line2, width)

	// Line 3: the prompt, as the input shows it, and the surface swatch.
	line3 := spread(s.Prompt.Render("❯")+" "+s.Text.Render("chart BTC 4h"), swatch(t, p), width)

	return line1 + "\n" + line2 + "\n" + line3
}

// strip draws previewCloses resampled to w cells as eighth-block bars, each
// colored by its direction against the previous close.
func strip(t Theme, p colorprofile.Profile, w int) string {
	if w <= 0 {
		return ""
	}
	const bars = "▁▂▃▄▅▆▇█"
	lo, hi := previewCloses[0], previewCloses[0]
	for _, v := range previewCloses {
		lo, hi = min(lo, v), max(hi, v)
	}
	up, down := fgStyle(t.Up, p), fgStyle(t.Down, p)
	var b strings.Builder
	prev := previewCloses[0]
	for i := range w {
		v := previewCloses[i*len(previewCloses)/w]
		level := int((v - lo) / (hi - lo) * 7.999)
		st := up
		if v < prev {
			st = down
		}
		b.WriteString(st.Render(string([]rune(bars)[level])))
		prev = v
	}
	return b.String()
}

// swatch draws one cell per neutral step, then the accent mark. A step
// that is omitted at p (a tint at 16 colors, anything with no color) is a
// blank cell, so the width never changes.
func swatch(t Theme, p colorprofile.Profile) string {
	var b strings.Builder
	for _, c := range []Color{t.Surface, t.Raised, t.Floor, t.Border, t.Dim, t.Muted, t.Text} {
		if c.Resolve(p) == nil {
			b.WriteByte(' ')
			continue
		}
		b.WriteString(fgStyle(c, p).Render("█"))
	}
	b.WriteString(" " + fgStyle(t.Accent, p).Render("■"))
	return b.String()
}

func fgStyle(c Color, p colorprofile.Profile) lipgloss.Style {
	s := lipgloss.NewStyle()
	if col := c.Resolve(p); col != nil {
		s = s.Foreground(col)
	}
	return s
}

// spread places left and right at the two ends of a width-cell line.
func spread(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return pad(left, width)
	}
	return left + strings.Repeat(" ", gap) + right
}

// pad right-pads s with spaces to width cells.
func pad(s string, width int) string {
	if w := lipgloss.Width(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}
