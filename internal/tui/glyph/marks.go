package glyph

// The kit's marks (DESIGN §4). Any other glyph used as an icon is removed.
const (
	Prompt = "❯" // prompt; echo of a typed line
	On     = "■" // live / pass / curated / error lead
	Off    = "□" // not live / n/a
	Sel    = "▌" // selected row in a list
	Note   = "!" // warning or note
	Minus  = "−" // U+2212, the sign of a negative number
	Crumb  = "›" // breadcrumb in the top bar
	Rule   = "─" // the one hairline above the prompt
	Dot    = "·" // floor dot; sparing meta separator
)
