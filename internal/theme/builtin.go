package theme

// Built-in themes.
//
// "hlp" and "light" carry measured per-profile palettes.
// The others are ports of well-known terminal palettes. Every token has a
// hand-picked ANSI256 and ANSI16 value: automatic downsampling turns slate
// grays teal and amber into red, so nothing here
// relies on it. C16 = -2 (omit at 16 colors) is used only for Grid and Cursor,
// the two tints that would otherwise paint solid blocks of black or gray.
//
// Palette discipline (Stageshow craft.md, "Color and looks"): one neutral
// family for chrome (BG, Grid, Cursor, Surface, Border, AxisLine, Dim,
// AxisText, Muted, Text), one accent, and status colors that mean status.
// Up and Down are the only saturated pair on a chart; the MAs are chosen to
// stay clear of both.
//
// "rush" is the default (docs/DESIGN.md section 2): the setuprush.com landing
// surfaces and app tokens, one Hyperliquid mint accent, white gains and red losses, no
// green or teal anywhere.

// Built-in theme names.
const (
	NameRush        = "rush"
	NameHLP         = "hlp"
	NameLight       = "light"
	NamePaper       = "paper"
	NameTokyoNight  = "tokyonight"
	NameCatppuccin  = "catppuccin"
	NameGruvbox     = "gruvbox"
	NameMono        = "mono"
	NameColorblind  = "colorblind"
	NameHyperliquid = "hyperliquid"
)

func init() {
	for _, t := range builtins() {
		Register(t)
	}
}

func builtins() []Theme {
	return []Theme{rushTheme(), hlpTheme(), lightTheme(), paperTheme(), hyperliquid(), tokyoNight(), catppuccin(), gruvbox(), mono(), colorblind()}
}

// rushTheme is the setup-rush look (docs/DESIGN.md section 2, the token
// table is binding). Ground and surfaces come from the landing
// (landing.css:10-16), inks from the app tokens (index.css:42-71). Gains are
// ink: Up == Text and Success == Text, and the sign carries direction. The
// mint is the only accent; at 16 colors it is bright cyan (14) so it never
// collides with Down (9), and Warn is bright yellow (11), always paired with
// its "!" glyph or a word.
func rushTheme() Theme {
	return Theme{
		Name:     NameRush,
		Dark:     true,
		BG:       C("#131315", 233, 0),
		Up:       C("#f4f3ef", 255, 15),
		Down:     C("#ff4b3e", 203, 9),
		Grid:     C("#1e1e21", 234, -2),
		AxisLine: C("#303034", 236, 8),
		AxisText: C("#97979c", 246, 7),
		Text:     C("#f4f3ef", 255, 15),
		Dim:      C("#6b6b70", 242, 8),
		Cursor:   C("#202023", 235, -2),
		MA:       [3]Color{C("#8ab4f8", 111, 12), C("#b4a2ff", 147, 13), C("#e9b949", 179, 11)},
		VolMix:   0.30,

		Accent:  C("#97fce4", 122, 14),
		Accent2: C("#8ab4f8", 111, 12),
		Muted:   C("#97979c", 246, 7),
		Border:  C("#303034", 236, 8),
		Surface: C("#1a1a1d", 234, -2),
		Warn:    C("#e9b949", 179, 11),
		Error:   C("#ff4b3e", 203, 9),
		Success: C("#f4f3ef", 255, 15),

		Raised:     C("#202023", 235, -2),
		Floor:      C("#2c2c30", 236, -2),
		AccentEdge: C("#2f7d6d", 30, -2),
		ChartSkin:  "rush",
	}
}

// paperTheme is rush on the setup-rush "paper" chart skin's ground: the
// whole app recolors with /theme paper, not just the chart, so the chart is
// not a beige sticker on a black page (CRITIQUE.md, theme rows). Same rules
// as rush: ink gains, red losses, one mint accent (darkened to teal for the light
// ground), no green. The paper skin pairs with it and paints the same
// ground, which the shell sets with OSC 11.
func paperTheme() Theme {
	return Theme{
		Name:     NamePaper,
		Dark:     false,
		BG:       C("#c6c4ba", 251, 15),
		Up:       C("#191a17", 234, 0),
		Down:     C("#9c1f18", 124, 1),
		Grid:     C("#b6b4aa", 249, -2),
		AxisLine: C("#a3a197", 247, 7),
		AxisText: C("#4f4c46", 239, 8),
		Text:     C("#191a17", 234, 0),
		Dim:      C("#6d6a62", 242, 8),
		Cursor:   C("#b8b6ac", 250, -2),
		MA:       [3]Color{C("#2a55a0", 25, 4), C("#5a40b0", 61, 5), C("#6b5000", 94, 3)},
		VolMix:   0.30,

		Accent:  C("#064d42", 23, 6),
		Accent2: C("#2a55a0", 25, 4),
		Muted:   C("#4f4c46", 239, 8),
		Border:  C("#a3a197", 247, 7),
		Surface: C("#bcbab0", 250, -2),
		Warn:    C("#6b5000", 94, 3),
		Error:   C("#9c1f18", 124, 1),
		Success: C("#191a17", 234, 0),

		Raised:     C("#b3b1a7", 249, -2),
		Floor:      C("#b0aea4", 248, -2),
		AccentEdge: C("#03302a", 22, -2),
		ChartSkin:  "paper",
	}
}

// hyperliquid follows the Hyperliquid app's look: a deep teal-black ground, mint accent, green and pink for up and
// down. Colors only; no marks or logos.
func hyperliquid() Theme {
	return Theme{
		Name:     NameHyperliquid,
		Dark:     true,
		BG:       C("#0F1A1F", 234, 0),
		Up:       C("#1FA67D", 36, 2),
		Down:     C("#ED7088", 204, 1),
		Grid:     C("#18252B", 235, -2),
		AxisLine: C("#273035", 237, 8),
		AxisText: C("#949E9C", 247, 7),
		Text:     C("#F6FEFD", 255, 15),
		Dim:      C("#5D6B6E", 242, 8),
		Cursor:   C("#172328", 235, -2),
		MA:       [3]Color{C("#F0B90B", 214, 3), C("#A78BFA", 141, 5), C("#38BDF8", 75, 4)},
		VolMix:   0.42,

		Accent:  C("#97FCE4", 122, 14),
		Accent2: C("#50D2C1", 80, 6),
		Muted:   C("#949E9C", 247, 7),
		Border:  C("#273035", 237, 8),
		Surface: C("#132026", 234, -2),
		Warn:    C("#F0B90B", 214, 3),
		Error:   C("#ED7088", 204, 9),
		Success: C("#1FA67D", 36, 10),
	}
}

// hlpTheme is the previous default dark theme: TradingView teal/red on a near-black
// slate, with measured per-profile picks.
func hlpTheme() Theme {
	return Theme{
		Name:     NameHLP,
		Dark:     true,
		BG:       C("#0B0E11", 233, 0),
		Up:       C("#26A69A", 36, 2),
		Down:     C("#EF5350", 203, 1),
		Grid:     C("#1A1F26", 235, -2),
		AxisLine: C("#2B3139", 238, 8),
		AxisText: C("#848E9C", 245, 7),
		Text:     C("#D1D4DC", 252, 15),
		Dim:      C("#5E6673", 242, 8),
		Cursor:   C("#1C222B", 236, -2),
		MA:       [3]Color{C("#F0B90B", 214, 3), C("#A78BFA", 141, 5), C("#38BDF8", 75, 4)},
		VolMix:   0.42,

		Accent:  C("#50E3C2", 79, 6),
		Accent2: C("#38BDF8", 75, 4),
		Muted:   C("#848E9C", 245, 7),
		Border:  C("#2B3139", 238, 8),
		Surface: C("#161B22", 234, -2),
		Warn:    C("#F0B90B", 214, 3),
		Error:   C("#EF5350", 203, 9),
		Success: C("#26A69A", 36, 10),
	}
}

// lightTheme is TradingView's light palette, used when the terminal reports a
// light background.
func lightTheme() Theme {
	return Theme{
		Name:     NameLight,
		Dark:     false,
		BG:       C("#FFFFFF", 231, 15),
		Up:       C("#089981", 30, 2),
		Down:     C("#F23645", 197, 1),
		Grid:     C("#F0F3FA", 255, -2),
		AxisLine: C("#D1D4DC", 252, 7),
		AxisText: C("#6A6D78", 242, 8),
		Text:     C("#131722", 234, 0),
		Dim:      C("#9598A1", 246, 8),
		Cursor:   C("#E9ECF2", 254, -2),
		MA:       [3]Color{C("#C77C02", 172, 3), C("#7E57C2", 98, 5), C("#0288D1", 32, 4)},
		VolMix:   0.35,

		Accent:  C("#00897B", 30, 6),
		Accent2: C("#0288D1", 32, 4),
		Muted:   C("#6A6D78", 242, 8),
		Border:  C("#D1D4DC", 252, 7),
		Surface: C("#F0F3FA", 255, -2),
		Warn:    C("#B26A00", 130, 3),
		Error:   C("#F23645", 197, 1),
		Success: C("#089981", 30, 2),
	}
}

// tokyoNight is folke's Tokyo Night ("night" variant).
func tokyoNight() Theme {
	return Theme{
		Name:     NameTokyoNight,
		Dark:     true,
		BG:       C("#1A1B26", 234, 0),
		Up:       C("#9ECE6A", 149, 2),
		Down:     C("#F7768E", 210, 1),
		Grid:     C("#1F2335", 235, -2),
		AxisLine: C("#3B4261", 238, 8),
		AxisText: C("#737AA2", 103, 7),
		Text:     C("#C0CAF5", 189, 15),
		Dim:      C("#565F89", 60, 8),
		Cursor:   C("#292E42", 236, -2),
		MA:       [3]Color{C("#E0AF68", 179, 3), C("#BB9AF7", 141, 5), C("#7DCFFF", 117, 6)},
		VolMix:   0.42,

		Accent:  C("#7AA2F7", 111, 4),
		Accent2: C("#7DCFFF", 117, 6),
		Muted:   C("#737AA2", 103, 7),
		Border:  C("#3B4261", 238, 8),
		Surface: C("#24283B", 235, -2),
		Warn:    C("#E0AF68", 179, 3),
		Error:   C("#F7768E", 210, 9),
		Success: C("#9ECE6A", 149, 10),
	}
}

// catppuccin is Catppuccin Mocha.
func catppuccin() Theme {
	return Theme{
		Name:     NameCatppuccin,
		Dark:     true,
		BG:       C("#1E1E2E", 234, 0),
		Up:       C("#A6E3A1", 151, 2),
		Down:     C("#F38BA8", 211, 1),
		Grid:     C("#24273A", 235, -2),
		AxisLine: C("#45475A", 238, 8),
		AxisText: C("#7F849C", 103, 7),
		Text:     C("#CDD6F4", 189, 15),
		Dim:      C("#6C7086", 60, 8),
		Cursor:   C("#313244", 236, -2),
		MA:       [3]Color{C("#FAB387", 216, 3), C("#CBA6F7", 183, 5), C("#89DCEB", 116, 6)},
		VolMix:   0.42,

		Accent:  C("#89B4FA", 111, 4),
		Accent2: C("#94E2D5", 116, 6),
		Muted:   C("#7F849C", 103, 7),
		Border:  C("#45475A", 238, 8),
		Surface: C("#313244", 236, -2),
		Warn:    C("#F9E2AF", 223, 3),
		Error:   C("#F38BA8", 211, 9),
		Success: C("#A6E3A1", 151, 10),
	}
}

// gruvbox is morhetz's Gruvbox dark (medium contrast). Its 256-color values
// are the palette's own canonical xterm indexes.
func gruvbox() Theme {
	return Theme{
		Name:     NameGruvbox,
		Dark:     true,
		BG:       C("#282828", 235, 0),
		Up:       C("#B8BB26", 142, 2),
		Down:     C("#FB4934", 167, 1),
		Grid:     C("#32302F", 236, -2),
		AxisLine: C("#504945", 239, 8),
		AxisText: C("#A89984", 246, 7),
		Text:     C("#EBDBB2", 223, 15),
		Dim:      C("#7C6F64", 243, 8),
		Cursor:   C("#3C3836", 237, -2),
		MA:       [3]Color{C("#FABD2F", 214, 3), C("#D3869B", 175, 5), C("#83A598", 109, 4)},
		VolMix:   0.45,

		Accent:  C("#8EC07C", 108, 6),
		Accent2: C("#83A598", 109, 4),
		Muted:   C("#A89984", 246, 7),
		Border:  C("#504945", 239, 8),
		Surface: C("#3C3836", 237, -2),
		Warn:    C("#FABD2F", 214, 3),
		Error:   C("#FB4934", 167, 9),
		Success: C("#B8BB26", 142, 10),
	}
}

// mono has no hue at all: grays on a dark ground. Direction is carried by
// brightness and by the chart's mono glyphs (hollow up, solid down), so it
// pairs with chart style "mono". At 16 colors only four grays exist, so the
// MAs share values with the candles there; the braille overlay still reads.
func mono() Theme {
	return Theme{
		Name:     NameMono,
		Dark:     true,
		BG:       C("#101010", 233, 0),
		Up:       C("#E4E4E4", 254, 15),
		Down:     C("#808080", 244, 7),
		Grid:     C("#1C1C1C", 234, -2),
		AxisLine: C("#3A3A3A", 237, 8),
		AxisText: C("#8A8A8A", 245, 7),
		Text:     C("#D0D0D0", 252, 15),
		Dim:      C("#626262", 241, 8),
		Cursor:   C("#262626", 235, -2),
		MA:       [3]Color{C("#BCBCBC", 250, 15), C("#9E9E9E", 247, 7), C("#6C6C6C", 242, 8)},
		VolMix:   0.40,

		Accent:  C("#FFFFFF", 231, 15),
		Accent2: C("#BCBCBC", 250, 7),
		Muted:   C("#8A8A8A", 245, 7),
		Border:  C("#3A3A3A", 237, 8),
		Surface: C("#262626", 235, -2),
		Warn:    C("#E4E4E4", 254, 15),
		Error:   C("#FFFFFF", 231, 15),
		Success: C("#BCBCBC", 250, 7),
	}
}

// colorblind swaps the red/green pair for blue/orange, which stays distinct
// under protanopia, deuteranopia and tritanopia. Success and Error follow the
// pair; Warn moves to a light yellow so it never reads as "down"; the MAs avoid
// blue and orange entirely.
func colorblind() Theme {
	return Theme{
		Name:     NameColorblind,
		Dark:     true,
		BG:       C("#0B0E11", 233, 0),
		Up:       C("#2196F3", 33, 12),
		Down:     C("#FF9800", 208, 3),
		Grid:     C("#1A1F26", 235, -2),
		AxisLine: C("#2B3139", 238, 8),
		AxisText: C("#848E9C", 245, 7),
		Text:     C("#D1D4DC", 252, 15),
		Dim:      C("#5E6673", 242, 8),
		Cursor:   C("#1C222B", 236, -2),
		MA:       [3]Color{C("#E6E6E6", 254, 15), C("#C792EA", 176, 5), C("#5EEAD4", 86, 6)},
		VolMix:   0.42,

		Accent:  C("#64B5F6", 75, 12),
		Accent2: C("#5EEAD4", 86, 6),
		Muted:   C("#848E9C", 245, 7),
		Border:  C("#2B3139", 238, 8),
		Surface: C("#161B22", 234, -2),
		Warn:    C("#FFE082", 222, 11),
		Error:   C("#FF9800", 208, 3),
		Success: C("#2196F3", 33, 12),
	}
}
