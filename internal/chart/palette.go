package chart

import (
	"math"
	"strings"

	"github.com/maxmoneycash/hlp/internal/theme"
)

// Palette is a chart skin ported from setup-rush (game/core.js THEMES and
// game/shop-catalog.js). A skin paints its own ground, so hollow bodies,
// washes and partial blocks resolve against a known background.
type Palette struct {
	Name  string // hlp name: "terminal" is setup-rush "night"
	Title string // setup-rush display name
	Light bool   // light ground

	BG, Text, Muted                     theme.Color
	Grid                                theme.Color // pre-blended over BG
	Up, Down, WickUp, WickDown, Outline theme.Color // Outline zero = none
	Bull, Bear, Gain, Loss              theme.Color // Gain/Loss: live tag and price line (always a green and a red)
	Ext                                 *PaletteExt // graphite only

	gridInk theme.RGB // the grid's source ink, blended at gridA over any wash
	gridA   float64
}

// PaletteExt holds graphite's extras (core.js THEMES.graphite.ext).
type PaletteExt struct {
	LastLine        theme.Color // solid price line instead of the dotted one
	AutoInk         bool        // tag text by the fill's luminance
	VolUp, VolDown  theme.Color // volume inks, fading to the floor
	Wash, NameAlpha float64     // zone alpha multiplier, pattern-name alpha
}

// DefaultPalette is the skin used when CandleOpts.Skin is empty.
const DefaultPalette = "terminal"

// SkinNone selects the pre-skin rendering that follows theme.Theme only.
const SkinNone = "none"

// rawPalette is one row of the port spec's table 4.1.
type rawPalette struct {
	name, title                         string
	light                               bool
	bg, text, muted                     string
	grid                                string
	gridA                               float64
	up, down, wickUp, wickDown, outline string
	bull, bear, gain, loss              string
}

// rawPalettes is the order of the port spec (originals, then the catalog).
// terminal and rush follow the setup-rush landing's ChartWall, which draws
// up candles hollow (fill = the ground, ChartWall.tsx:27) and down candles
// solid red: their Up is their own ground, so look() takes the hollow path
// with an ink outline (owner direction, CRITIQUE.md follow-up). Their live
// tag is white up and red down, like the landing's chamfered tag (the
// chart draws it in Bull on a hollow-up skin; Gain stays for SVG exports).
var rawPalettes = []rawPalette{
	{"terminal", "Terminal", false, "#0a0a0b", "#f4f3ef", "#8a8a8f", "#ffffff", .05, "#0a0a0b", "#ff4b3e", "#f4f3ef", "#ff4b3e", "#f4f3ef", "#f4f3ef", "#ff4b3e", "#42c86c", ""},
	// rush is terminal on the hlp rush theme's ground (docs/DESIGN.md section 2
	// rule 5), so a TUI chart is not a darker rectangle on the page. Its live
	// tag is ink, not green (rule 1: no green in rush).
	{"rush", "Rush", false, "#131315", "#f4f3ef", "#8a8a8f", "#ffffff", .05, "#131315", "#ff4b3e", "#f4f3ef", "#ff4b3e", "#f4f3ef", "#f4f3ef", "#ff4b3e", "", ""},
	// paper's up ink is the ink, not green: its live tag and volume follow
	// it, so the paper app (theme paper) has no green, like rush.
	{"paper", "Paper", true, "#c6c4ba", "#191a17", "#6d6a62", "#46453f", .13, "#f7f6ef", "#242521", "#2d2e29", "#2d2e29", "#2d2e29", "#242521", "#c0362c", "", ""},
	{"pro", "Pro Dark", false, "#131722", "#d1d4dc", "#787b86", "#787b86", .1, "#26a69a", "#ef5350", "#26a69a", "#ef5350", "", "#26a69a", "#ef5350", "", ""},
	{"exchange", "Exchange", false, "#0c0e12", "#eaecef", "#848e9c", "#ffffff", .045, "#42c86c", "#f5474b", "#42c86c", "#f5474b", "", "#42c86c", "#f5474b", "", ""},
	{"candy", "Candy", true, "#ffe3ee", "#2b1640", "#9a6b8e", "#7a5cff", .1, "#ffffff", "#7a5cff", "#ff5fa2", "#7a5cff", "#ff5fa2", "#12b88f", "#ff3f78", "", ""},
	{"crt", "CRT", false, "#030603", "#b8ffcf", "#4f8a5f", "#50ff8c", .06, "#030603", "#2fe07a", "#2fe07a", "#2fe07a", "#2fe07a", "#2fe07a", "#ff4d6a", "", ""},
	{"neon", "Neon", false, "#0d0221", "#f5e9ff", "#9a86c7", "#ff3864", .08, "#0d0221", "#f222ff", "#2de2e6", "#f222ff", "#2de2e6", "#2de2e6", "#ff3864", "#3ee88a", ""},
	{"amber", "Amber", false, "#120a01", "#ffd27a", "#a07426", "#ffb000", .07, "#120a01", "#ffb000", "#ffb000", "#ffb000", "#ffb000", "#ffcc4d", "#ff5a36", "#7fd66a", ""},
	{"blueprint", "Blueprint", false, "#1b4f8a", "#e8f1ff", "#9fc0e6", "#e8f1ff", .12, "#1b4f8a", "#e8f1ff", "#e8f1ff", "#e8f1ff", "#e8f1ff", "#7cf0c0", "#ff9a9a", "", ""},
	{"ink", "Ink", false, "#000000", "#ffffff", "#7a7a7a", "#ffffff", .07, "#000000", "#ffffff", "#ffffff", "#ffffff", "#ffffff", "#ffffff", "#8a8a8a", "#42c86c", "#ff626c"},
	{"graphite", "Graphite", true, "#b0b0b0", "#1a1a1a", "#4a4a4a", "#1a1a1a", .05, "#e4e4e4", "#3a3a3a", "#2a2a2a", "#2a2a2a", "#2a2a2a", "#f2f2f2", "#4a4a4a", "#004b23", "#861528"},
	{"ocean", "Ocean", false, "#071a2b", "#dcefff", "#6f93ad", "#dcefff", .07, "#19d3a2", "#ff6b6b", "#19d3a2", "#ff6b6b", "", "#19d3a2", "#ff6b6b", "", ""},
	{"violet", "Violet", false, "#140b24", "#efe6ff", "#8c7aa8", "#c9a7ff", .08, "#140b24", "#ff5c8a", "#c9a7ff", "#ff5c8a", "#c9a7ff", "#c9a7ff", "#ff5c8a", "#5fe0a0", ""},
	{"mint", "Mint", true, "#e6f0e9", "#13241a", "#5d7a66", "#13241a", .1, "#ffffff", "#1f8a4c", "#1f8a4c", "#1f8a4c", "#1f8a4c", "#13a862", "#d9405a", "", ""},
	{"gold", "Gold", false, "#0a0a0a", "#f3e3b3", "#9c8a55", "#e6c068", .08, "#0a0a0a", "#e6c068", "#e6c068", "#e6c068", "#e6c068", "#e6c068", "#c0504d", "#4cc775", "#ff5f57"},

	catalog("glacier", "Glacier", false, "#101923", "#edf5ff", "#94a5b7", "#87cfff", "#ff9668", "#4fd88a", "#ff5a5f"),
	catalog("ember", "Ember", false, "#201410", "#fff0e2", "#bda493", "#ffe1a6", "#ff7357", "#7fd88f", ""),
	catalog("parchment", "Parchment", true, "#eee5d4", "#2e261c", "#786a56", "#376955", "#a34b34", "", ""),
	catalog("lagoon", "Lagoon", false, "#082522", "#d9fff4", "#8fb8ae", "#73e4b1", "#ff8e82", "", ""),
	catalog("cobalt", "Cobalt", false, "#101328", "#f0f2ff", "#929ab9", "#84b4ff", "#ffad66", "#45d483", "#ff5b5b"),
	catalog("rose", "Rose", true, "#f4e7e4", "#342728", "#856b6a", "#32685e", "#a64361", "", ""),
	catalog("phosphor", "Phosphor", false, "#171b14", "#f0f5dd", "#a3b18d", "#c4eb76", "#ffa77d", "#4fd18b", "#ff5a4f"),
	catalog("contrast", "Contrast", false, "#070b12", "#ffffff", "#b0bbcb", "#65bdff", "#ffb259", "", ""),
	catalog("midnight", "Midnight", false, "#0a1022", "#e6ecfa", "#8e9ab8", "#5aa9ff", "#ff4d5e", "#42d17a", ""),
	catalog("bordeaux", "Bordeaux", false, "#1a0b12", "#f8e9ee", "#a8868f", "#9fe0b0", "#ff4f6d", "", ""),
	catalog("sunset", "Sunset", false, "#1c1026", "#fbeaf2", "#a58aa6", "#ffbf78", "#ff4f8f", "#5ee39a", ""),
	catalog("forest", "Forest", false, "#0b1a13", "#e3f3e8", "#86a493", "#4fe08f", "#ff3f86", "", ""),
	catalog("gunmetal", "Gunmetal", false, "#1c2129", "#eef1f5", "#9aa4b2", "#e9eef5", "#ff5f6e", "#4fd18b", ""),
	catalog("acid", "Acid", false, "#050505", "#f5f7ee", "#8d9282", "#c8ff3a", "#ff2d55", "#4fd18b", ""),
	catalog("moonlight", "Moonlight", false, "#0c0f17", "#e9eef8", "#8790a6", "#dfe7f7", "#6f7a96", "#4fd18b", "#ff5d6c"),
	catalog("thermal", "Thermal", false, "#0e0709", "#fff1e4", "#a8908a", "#ffd877", "#8b6bff", "#4fd18b", "#ff4f5e"),
}

// catalog mirrors shop-catalog.js palette(): solid bodies, wicks in the body
// ink, bull = up, bear = down, a fixed faint grid and no outline.
func catalog(name, title string, light bool, bg, text, muted, up, down, gain, loss string) rawPalette {
	grid, a := "#f0f5fa", .07
	if light {
		grid, a = "#14191e", .09
	}
	return rawPalette{name, title, light, bg, text, muted, grid, a, up, down, up, down, "", up, down, gain, loss}
}

// Palettes lists every skin, the default first.
var Palettes = buildPalettes()

func buildPalettes() []Palette {
	out := make([]Palette, 0, len(rawPalettes))
	for _, r := range rawPalettes {
		bg := theme.Hex(r.bg)
		p := Palette{
			Name: r.name, Title: r.title, Light: r.light,
			BG:       bgColor(bg),
			Text:     inkColor(theme.Hex(r.text), r.light),
			Muted:    mutedColor(theme.Hex(r.muted)),
			Grid:     faintColor(theme.Mix(bg, theme.Hex(r.grid), r.gridA)),
			Up:       fgColor(theme.Hex(r.up)),
			Down:     fgColor(theme.Hex(r.down)),
			WickUp:   fgColor(theme.Hex(r.wickUp)),
			WickDown: fgColor(theme.Hex(r.wickDown)),
			Bull:     fgColor(theme.Hex(r.bull)),
			Bear:     fgColor(theme.Hex(r.bear)),
			gridInk:  theme.Hex(r.grid),
			gridA:    r.gridA,
		}
		if r.outline != "" {
			p.Outline = fgColor(theme.Hex(r.outline))
		}
		p.Gain, p.Loss = p.Bull, p.Bear
		if r.gain != "" {
			p.Gain = fgColor(theme.Hex(r.gain))
		}
		if r.loss != "" {
			p.Loss = fgColor(theme.Hex(r.loss))
		}
		if r.name == "graphite" {
			p.Ext = &PaletteExt{LastLine: fgColor(theme.Hex("#f2f2f2")), AutoInk: true,
				VolUp: fgColor(theme.Hex("#d4d4d4")), VolDown: fgColor(theme.Hex("#505050")), Wash: 2, NameAlpha: 1}
		}
		out = append(out, p)
	}
	return out
}

// PaletteNames returns the skin names in Palettes order.
func PaletteNames() []string {
	out := make([]string, len(Palettes))
	for i, p := range Palettes {
		out[i] = p.Name
	}
	return out
}

// PaletteByName finds a skin (case-insensitive). setup-rush's own keys are
// accepted too: "night" is terminal and setup-rush "terminal" (CRT) is crt
// only when spelled "crt"; "highcontrast" is contrast.
func PaletteByName(name string) (Palette, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "", "default":
		n = DefaultPalette
	case "night":
		n = "terminal"
	case "highcontrast":
		n = "contrast"
	}
	for _, p := range Palettes {
		if p.Name == n {
			return p, true
		}
	}
	return Palette{}, false
}

// Theme returns base with its chart tokens taken from the skin, so other
// chart kinds (LineChart, Depth, Bars) and the TUI can match it.
func (p Palette) Theme(base theme.Theme) theme.Theme {
	t := base
	t.Dark = !p.Light
	t.BG = p.BG
	t.Up, t.Down = p.Bull, p.Bear
	t.Grid = p.Grid
	t.AxisLine = faintColor(theme.Mix(p.BG.True, p.Muted.True, 0.45))
	t.AxisText = p.Muted
	t.Text = p.Text
	t.Dim = mutedColor(theme.Mix(p.BG.True, p.Muted.True, 0.75))
	t.Cursor = washColor(theme.Mix(p.BG.True, p.Text.True, 0.07))
	t.VolMix = 0.12
	// MA overlays keep the UI theme's hues, so the card legends (drawn by
	// the TUI in theme colors) match the lines; light grounds darken them.
	if p.Name == "rush" || p.Name == "terminal" {
		// The landing's ChartWall pairs a cyan average with a light gray
		// one: cyan beside violet read as two near-identical blues.
		c16 := t.MA[1].C16
		t.MA[1] = fgColor(theme.Mix(p.BG.True, p.Text.True, 0.7))
		t.MA[1].C16 = c16
	}
	if p.Light {
		for i := range t.MA {
			c16 := t.MA[i].C16
			t.MA[i] = fgColor(theme.Mix(base.MA[i].True, p.Text.True, 0.35))
			t.MA[i].C16 = c16
		}
	}
	return t
}

// gridOver is the grid ink over a cell ground: setup-rush strokes its grid
// translucent, so over a zone or the line's wash it stays one step lighter
// (or darker) than what it crosses instead of cutting through it.
func (p Palette) gridOver(ground theme.Color) theme.Color {
	if !ground.True.Valid || ground.True == p.BG.True {
		return p.Grid
	}
	return faintColor(theme.Mix(ground.True, p.gridInk, p.gridA))
}

// fgColor is a foreground ink: exact at TrueColor, hue-keeping at 256 (a
// neutral on the gray ramp), the nearest of the 16 by hue.
func fgColor(c theme.RGB) theme.Color {
	return theme.Color{True: c, C256: skin256(c), C16: hue16(c)}
}

// skin256 is the 256-color pick for a skin token: theme.Hue256 for a
// colored ink, the nearest gray by luminance for a neutral one (paper's
// #c6c4ba ground is a warm gray, not the cube's yellow-green #d7d7af).
func skin256(c theme.RGB) int16 {
	if !c.Valid {
		return -1
	}
	if neutral(c) {
		return gray256(c)
	}
	return theme.Hue256(c)
}

// neutral reports a low-saturation color (HSV saturation at most 0.2).
func neutral(c theme.RGB) bool {
	hi, lo := max(c.R, c.G, c.B), min(c.R, c.G, c.B)
	return hi == 0 || float64(hi-lo)/float64(hi) <= 0.2
}

// gray256 returns the xterm gray (ramp 232-255 or a cube gray) nearest to
// c's luminance.
func gray256(c theme.RGB) int16 {
	y := 0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)
	best, bestD := 16, math.Inf(1)
	try := func(idx int, v float64) {
		if d := math.Abs(v - y); d < bestD {
			best, bestD = idx, d
		}
	}
	for i, v := range []float64{0, 95, 135, 175, 215, 255} {
		try(16+43*i, v) // 16, 59, 102, 145, 188, 231
	}
	for i := 0; i < 24; i++ {
		try(232+i, float64(8+10*i))
	}
	return int16(best)
}

// inkColor is the text ink: bright white on dark grounds, the terminal
// default on light ones at 16 colors (legible on either terminal ground).
func inkColor(c theme.RGB, light bool) theme.Color {
	col := fgColor(c)
	col.C16 = 15
	if light {
		col.C16 = -1
	}
	return col
}

// mutedColor is secondary text: bright black at 16 colors.
func mutedColor(c theme.RGB) theme.Color {
	col := fgColor(c)
	col.C16 = 8
	return col
}

// faintColor is a foreground that disappears at 16 colors (grid, volume).
func faintColor(c theme.RGB) theme.Color {
	return theme.Color{True: c, C256: dark256(c), C16: -2}
}

// washColor is a background tint, never painted at 16 colors.
func washColor(c theme.RGB) theme.Color {
	return theme.Color{True: c, C256: dark256(c), C16: -2}
}

// dark256 keeps the hue at 256 colors only where the cube has a near
// entry: the cube's darkest hues (#00005f...) are far brighter than a
// near-black ground or a faint wash, which belong on the gray ramp.
func dark256(c theme.RGB) int16 {
	switch {
	case !c.Valid:
		return -1
	case neutral(c):
		return gray256(c)
	case max(c.R, c.G, c.B) < 72:
		return -1
	}
	return theme.Hue256(c)
}

// bgColor is the skin ground: painted at TrueColor and 256, never at 16.
func bgColor(c theme.RGB) theme.Color { return washColor(c) }

// hue16 maps a color to the 16-color palette by hue (port spec 4.3).
func hue16(c theme.RGB) int16 {
	if !c.Valid {
		return -1
	}
	r, g, b := float64(c.R)/255, float64(c.G)/255, float64(c.B)/255
	hi, lo := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	if hi-lo < 0.18 || (hi > 0 && (hi-lo)/hi < 0.22) {
		switch {
		case hi > 0.62:
			return 15
		case hi > 0.3:
			return 7
		}
		return 8
	}
	var h float64
	switch hi {
	case r:
		h = math.Mod((g-b)/(hi-lo), 6)
	case g:
		h = (b-r)/(hi-lo) + 2
	default:
		h = (r-g)/(hi-lo) + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	switch {
	case h < 18 || h >= 330:
		return 9
	case h < 70:
		return 11
	case h < 160:
		return 10
	case h < 200:
		return 14
	case h < 260:
		return 12
	}
	return 13
}

// HollowUp reports whether the skin draws classic up candles hollow (its
// up body is its own ground), as terminal and rush do.
func (p Palette) HollowUp() bool { return sameRGB(p.Up, p.BG) }

// HollowUp reports whether a style on a skin draws up bodies as outlines,
// which need a body at least 3 columns wide to read as a box.
func HollowUp(style Style, skin string) bool {
	switch style {
	case "", Classic, Hollow, Heikin:
	default:
		return false
	}
	if strings.EqualFold(strings.TrimSpace(skin), SkinNone) {
		return style == Hollow
	}
	p, ok := PaletteByName(skin)
	if !ok {
		p, _ = PaletteByName(DefaultPalette)
	}
	return style != Classic && style != "" || p.HollowUp()
}

// WideBodies reports whether a style on a skin draws outlined up bodies
// (hollow, or filled with an edge like paper's), which need a body at
// least 3 columns wide: in one column they collapse to a ║ or a tint.
func WideBodies(style Style, skin string) bool {
	if HollowUp(style, skin) {
		return true
	}
	if style != "" && style != Classic {
		return false
	}
	p, ok := PaletteByName(skin)
	if !ok || strings.EqualFold(strings.TrimSpace(skin), SkinNone) {
		return false
	}
	return p.Outline.True.Valid && !sameRGB(p.Outline, p.Up)
}
