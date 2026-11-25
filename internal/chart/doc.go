// Package chart renders terminal charts as pure functions:
// (data, size, options, theme) -> *Grid -> ANSI/plain/PNG.
//
// Design rules:
//   - Paint into a Grid of cells; serialize once with run-length SGR. Never
//     call lipgloss Style.Render per cell. Lip Gloss is for chrome only and
//     lives in internal/tui.
//   - Default style "blocks": eighth-block bodies + SGR-7 upper partials,
//     light box wicks, round body edges to the cell where a wick continues.
//   - Overlays (MA/EMA/Pine plots) are braille, drawn only into empty/grid
//     cells (they pass behind candles). Autoscale on candles only.
//   - Colors come from theme.Theme (hand-picked per profile). The Grid stores
//     theme.Color so ANSI(profile) resolves exactly once at serialization.
//   - Live charts stay calm: CandleOpts.Range keeps the price scale while
//     price stays inside it (no "breathing"), CandleOpts.Now drives the bar
//     countdown, and Viewport maps mouse columns back to candles.
//   - No I/O, no clocks (pass a *time.Location), no globals: everything must
//     be golden-testable with TZ=UTC and the BTC 15m candle sample.
package chart
