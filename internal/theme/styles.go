package theme

import (
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// Styles are the Lip Gloss styles the TUI and CLI tables use. Charts do NOT
// use these (they paint cells directly, see internal/chart).
//
// Hierarchy rules (Stageshow craft.md "Color and looks" and "Restraint"):
//   - The accent marks one thing at a time: the prompt, the focused field, the
//     current selection. Card titles and table headers do not use it.
//   - Depth comes from a lighter surface, not borders: the status bar, pills
//     and the selected row sit on Surface. Where Surface cannot be painted
//     (16 colors, no color) selection falls back to reverse video, which
//     survives every profile.
//   - Status colors mean status: Up/Down for signed values, Warn for testnet
//     and stale data, Error for failures and mainnet order previews.
type Styles struct {
	Title    lipgloss.Style
	Text     lipgloss.Style
	Muted    lipgloss.Style
	Accent   lipgloss.Style
	Up       lipgloss.Style
	Down     lipgloss.Style
	Warn     lipgloss.Style
	Error    lipgloss.Style
	Success  lipgloss.Style
	Card     lipgloss.Style // transcript card frame: no border, no box (DESIGN.md section 2 rule 4)
	CardHead lipgloss.Style
	Pill     lipgloss.Style // status-bar segments, tags
	Prompt   lipgloss.Style // the "❯" in the input box
	Input    lipgloss.Style // input row: no border; the hairline above it is Rule
	Selected lipgloss.Style
	TableHdr lipgloss.Style

	Dim          lipgloss.Style // meta footers, secondary hints
	Rule         lipgloss.Style // dividers and table rules (foreground only)
	Key          lipgloss.Style // a key in a hint line: "ctrl+k"
	KeyDesc      lipgloss.Style // its description: "palette"
	StatusBar    lipgloss.Style // the full-width status row
	InputFocused lipgloss.Style // input box border while it owns the keyboard in a modal
	Badge        lipgloss.Style // neutral tag: CURATED, DRY RUN, local
	BadgeWarn    lipgloss.Style // TESTNET, STALE
	BadgeError   lipgloss.Style // MAINNET on an order preview, LIVE

	// Rush marks (DESIGN.md sections 3 and 4). A style with no foreground
	// and no background at some profile means "do not draw it" there; see
	// FloorFunc and KeycapLip.
	Label      lipgloss.Style // UPPERCASE labels: column headers, TVL, MAX  DD (Muted, never bold)
	Reticle    lipgloss.Style // corner brackets on the focused card (Accent)
	Keycap     lipgloss.Style // the one primary action on a confirm: Accent face, BG text, bold
	KeycapEdge lipgloss.Style // the keycap's lip row of ▀ (AccentEdge); empty at 16 colors
	FlapFace   lipgloss.Style // ticker flap cell face (Surface); empty at 16 colors
	FloorDot   lipgloss.Style // floor-grid dot (Floor); empty at 16 colors and with no color
}

// Styles builds the style set for a color profile.
func (t Theme) Styles(p colorprofile.Profile) Styles {
	fg := func(c Color) lipgloss.Style {
		s := lipgloss.NewStyle()
		if col := c.Resolve(p); col != nil {
			s = s.Foreground(col)
		}
		return s
	}
	plain := lipgloss.NewStyle()
	border := func(c Color) lipgloss.Style {
		s := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
		if col := c.Resolve(p); col != nil {
			s = s.BorderForeground(col)
		}
		return s
	}
	surface := t.Surface.Resolve(p)
	onSurface := func(s lipgloss.Style) lipgloss.Style {
		if surface != nil {
			return s.Background(surface)
		}
		return s
	}
	badge := func(c Color) lipgloss.Style {
		s := lipgloss.NewStyle().Bold(true).Padding(0, 1)
		bg, ink := c.Resolve(p), t.BG.Resolve(p)
		if bg == nil || ink == nil {
			// No color: reverse video keeps the tag visible (it survives the
			// Ascii profile).
			return s.Reverse(true)
		}
		return s.Background(bg).Foreground(ink)
	}

	// The keycap face: Accent with BG ink. Without a hand-picked ground
	// (16 colors, where BG is the terminal's own black) the face is reverse
	// video of the accent; with no color at all, plain reverse video.
	keycap := lipgloss.NewStyle().Bold(true).Padding(0, 1)
	switch acc, ink := t.Accent.Resolve(p), t.BG.Resolve(p); {
	case p == colorprofile.ANSI && acc != nil:
		keycap = keycap.Foreground(acc).Reverse(true)
	case acc != nil && ink != nil:
		keycap = keycap.Background(acc).Foreground(ink)
	default:
		keycap = keycap.Reverse(true)
	}

	selected := fg(t.Text).Bold(true)
	if surface != nil {
		selected = selected.Background(surface)
	} else {
		selected = selected.Reverse(true)
	}

	return Styles{
		Title:    fg(t.Text).Bold(true),
		Text:     fg(t.Text),
		Muted:    fg(t.Muted),
		Accent:   fg(t.Accent).Bold(true),
		Up:       fg(t.Up),
		Down:     fg(t.Down),
		Warn:     fg(t.Warn),
		Error:    fg(t.Error).Bold(true),
		Success:  fg(t.Success),
		Card:     plain,
		CardHead: fg(t.Text).Bold(true),
		Pill:     onSurface(fg(t.Text)).Padding(0, 1),
		Prompt:   fg(t.Accent).Bold(true),
		Input:    plain,
		Selected: selected,
		TableHdr: fg(t.Muted),

		Dim:          fg(t.Dim),
		Rule:         fg(t.Border),
		Key:          fg(t.Text).Bold(true),
		KeyDesc:      fg(t.Muted),
		StatusBar:    onSurface(fg(t.Muted)),
		InputFocused: border(t.Accent),
		Badge:        badge(t.Muted),
		BadgeWarn:    badge(t.Warn),
		BadgeError:   badge(t.Error),

		Label:      fg(t.Muted),
		Reticle:    fg(t.Accent),
		Keycap:     keycap,
		KeycapEdge: fg(t.AccentEdge),
		FlapFace:   flapFace(t, plain, onSurface),
		FloorDot:   fg(t.Floor),
	}
}

// FloorFunc returns the painter for floor-grid dots, or nil when the floor
// is not drawn at this profile (16 colors, no color). A nil painter tells
// glyph.Floor to leave the lines unchanged.
func (s Styles) FloorFunc() func(string) string {
	if _, none := s.FloorDot.GetForeground().(lipgloss.NoColor); none {
		return nil
	}
	st := s.FloorDot
	return func(x string) string { return st.Render(x) }
}

// KeycapLip reports whether the keycap's ▀ lip row is drawn at this profile
// (DESIGN.md 4.2: at 16 colors the face is reverse video with no lip).
func (s Styles) KeycapLip() bool {
	_, none := s.KeycapEdge.GetForeground().(lipgloss.NoColor)
	return !none
}

// Sign returns the Up style for v >= 0 and the Down style otherwise. In
// rush, Up is ink: gains are not colored, the sign carries direction.
func (s Styles) Sign(v float64) lipgloss.Style {
	if v >= 0 {
		return s.Up
	}
	return s.Down
}

// flapFace is the ticker's flap cell face: the Surface step on a dark
// ground, where it reads as a flap, but no face on a light one, where a
// darker Surface cell reads as a gray chip behind every value.
func flapFace(t Theme, plain lipgloss.Style, onSurface func(lipgloss.Style) lipgloss.Style) lipgloss.Style {
	if !t.Dark {
		return plain
	}
	return onSurface(plain)
}
