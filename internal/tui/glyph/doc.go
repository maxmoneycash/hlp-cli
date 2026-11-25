// Package glyph holds the pure drawing primitives of the "rush" look
// (docs/DESIGN.md §3.1, §4, §7): big display digits and the HLP wordmark,
// the split-flap ticker state machine, the cursor blink clock, the corner
// reticle, the dotted floor grid, the mark constants and the reduced-motion
// switch.
//
// Nothing here imports Bubble Tea or reads a clock: every time-dependent
// function takes `now` from the caller, so the shell can schedule ticks only
// while something is in flight and tests can drive a fake clock. Styles are
// passed in as func(string) string, so the package never decides a colour.
package glyph
