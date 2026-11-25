package tui

import (
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/maxmoneycash/hlp-cli/internal/config"
	"github.com/maxmoneycash/hlp-cli/internal/theme"
	"github.com/maxmoneycash/hlp-cli/internal/tui/cards"
	"github.com/maxmoneycash/hlp-cli/internal/tui/glyph"
)

// chrome is the shell's resolved style set. Everything in the shell takes
// its color from the active theme; nothing is hard-coded. The rush marks
// (label, reticle, keycap, flap face, floor dot) come from theme.Styles so
// the shell and the cards share one definition (DESIGN.md sections 2 to 4).
type chrome struct {
	text, muted, faint, dim, accent, brand, up, down, warn, err, ok, bold lipgloss.Style
	cursor, pill, warnPill, sel                                           lipgloss.Style

	label, reticle, keycap, keycapEdge, flapFace lipgloss.Style
	keycapLip                                    bool
	floor                                        func(string) string // nil = no floor at this profile
	reticleFn                                    func(string) string
}

func (m *Model) restyle() {
	fg := func(c theme.Color) lipgloss.Style {
		s := lipgloss.NewStyle()
		if col := c.Resolve(m.profile); col != nil {
			s = s.Foreground(col)
		}
		return s
	}
	t := m.th
	ts := t.Styles(m.profile)
	m.st = chrome{
		text:     fg(t.Text),
		muted:    fg(t.Muted),
		faint:    fg(t.Border),
		dim:      fg(t.Dim),
		accent:   fg(t.Accent),
		brand:    fg(t.Text).Bold(true),
		up:       fg(t.Up),
		down:     fg(t.Down),
		warn:     fg(t.Warn),
		err:      fg(t.Error).Bold(true),
		ok:       fg(t.Success),
		bold:     fg(t.Text).Bold(true),
		cursor:   fg(t.Text).Reverse(true),
		pill:     fg(t.Muted).Reverse(true),
		warnPill: ts.BadgeWarn,
		sel:      fg(t.Text).Bold(true),

		label:      ts.Label,
		reticle:    ts.Reticle,
		keycap:     ts.Keycap,
		keycapEdge: ts.KeycapEdge,
		flapFace:   ts.FlapFace,
		keycapLip:  ts.KeycapLip(),
		floor:      ts.FloorFunc(),
	}
	if !m.uiFloor() {
		m.st.floor = nil
	}
	ret := m.st.reticle
	m.st.reticleFn = func(s string) string { return ret.Render(s) }
	for _, e := range m.entries {
		e.cache = ""
	}
	m.tickerKey = ""
}

// cardOpts returns card rendering options at width.
func (m *Model) cardOpts(width int, compact bool) cards.Opts {
	skin := m.effSkin()
	return cards.Opts{
		Width:    width,
		Theme:    m.th,
		Profile:  m.profile,
		Compact:  compact,
		Plain:    m.profile == colorprofile.NoTTY,
		Location: time.Local,

		ChartSkin:  skin,
		ChartStyle: m.chartStyle,
	}
}

// effSkin is the chart skin in use: the configured or /theme-picked one,
// else the theme's own pairing (rush draws the rush skin, DESIGN 2 rule 5).
// "" lets cards fall back (paper on light themes).
func (m *Model) effSkin() string {
	if m.chartSkin != "" {
		return m.chartSkin
	}
	return m.th.ChartSkin
}

// ui is the [ui] config. A zero UIConfig (tests, an Env built by hand)
// reads as the defaults: paint on, motion on, floor on.
func (m *Model) ui() config.UIConfig {
	if m.env == nil || m.env.Config.UI == (config.UIConfig{}) {
		return config.UIConfig{PaintBackground: true, Motion: config.MotionOn, Floor: true}
	}
	return m.env.Config.UI
}

func (m *Model) uiFloor() bool { return m.ui().Floor }

// reduced reports reduced motion (config, HLP_REDUCED_MOTION, NO_MOTION,
// TERM=dumb). It is read once: the environment does not change under us.
func (m *Model) reducedMotion() bool {
	if m.reducedSet {
		return m.reduced
	}
	m.reduced, m.reducedSet = glyph.Reduced(m.ui().Motion), true
	return m.reduced
}
