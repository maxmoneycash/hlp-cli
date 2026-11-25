// Package theme owns every color in hlp: the chart palette, the TUI chrome
// palette, and the Lip Gloss styles built from them.
//
// Rule: never rely on automatic color
// downsampling. Every Color carries hand-picked ANSI256 and ANSI16 values and
// Resolve picks the right one for the detected colorprofile.Profile.
// Theme.Validate enforces it, plus Up/Down contrast >= 3:1 against BG.
//
// Built-ins: rush (the default on dark and light terminals; docs/DESIGN.md
// section 2), hlp (the previous teal dark theme), light, hyperliquid, tokyonight, catppuccin (mocha),
// gruvbox (dark), mono (grays only, pairs with chart style "mono") and
// colorblind (blue up, orange down).
//
// Rush adds the tokens Raised, Floor and AccentEdge (Register derives them for
// older themes) and ChartSkin, the chart.Palette the TUI pairs with the theme.
// Its Styles add Label, Reticle, Keycap, KeycapEdge, FlapFace and FloorDot;
// Card and Input draw no box.
//
// Choosing a theme: Select(cfg.Theme, darkBackground) handles "" and "auto";
// Next cycles for the TUI's ctrl+t; Preview renders a three-line swatch for a
// theme picker or gallery.
//
// Files:
//
//	theme.go    Color, RGB, Theme, Register/Get/Names/Default, derived tokens
//	builtin.go  the built-in palettes
//	select.go   Select, Next, Contrast/Luminance, Tokens, Validate
//	styles.go   Styles (Lip Gloss) built per color profile, FloorFunc, KeycapLip
//	preview.go  Preview swatch
package theme
