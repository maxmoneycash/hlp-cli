// Package tui is the interactive chat-style trading terminal (`hlp` with no
// arguments), in the style of Charm's Crush / Claude Code / OpenCode.
//
// The subpackage cards renders core.Result values as rich cards for both
// the TUI scrollback and the CLI's human (TTY) output.
//
// Stack: charm.land/bubbletea/v2, lipgloss/v2, bubbles/v2 (textarea,
// viewport, list, spinner, help, key), huh/v2 (confirm card, order ticket,
// first-run setup), glamour/v2 (markdown in assistant replies).
//
// Behavior contract:
//   - Input "/x args" -> core.Registry.BySlash + core.ParseArgs -> Invoke.
//   - Other input -> agent.ParseIntent; if not understood and an Agent is
//     configured -> agent.Run (streamed text + tool cards); else suggestions.
//   - Every tool Result is appended as a card. Streaming tools (live chart,
//     book, watch, paper, arena) update their card in place while visible.
//   - Polling and streams pause when the terminal reports focus lost
//     (tea.BlurMsg) and when a card scrolls out of view (owner rule:
//     hidden views never poll; hlp.xyz polling >= 5 s).
//   - Mutating tools show an order-preview confirm card (huh) before
//     anything is signed; Esc cancels. Testnet shows a TESTNET badge.
//   - Keys: enter send (empty prompt: open the latest card), shift+enter
//     newline, tab autocomplete, up/down history, pgup/pgdn scroll,
//     ctrl+o open the latest card full screen (or click a card), ctrl+k
//     command palette, ctrl+b side panel, ctrl+t theme, ? help, esc
//     interrupt/close pane, ctrl+c twice quit. In chart panes: h/l pan,
//     +/- zoom, 0 reset, 1-9 intervals, c crosshair (left/right move it,
//     mouse hover drives it), i indicators, L log, s style. In the vaults
//     screener: up/down select, enter tearsheet, s/S sort, r reverse,
//     / filter.
//   - Charts render via internal/chart into the card width; never wrap
//     chart strings in width-setting styles.
//
// Design (docs/DESIGN.md, the "rush" look; Stageshow craft rules):
//   - Chrome is one line at the top (hlp, the split-flap ticker, the live
//     mark) and one at the bottom (a hairline and the prompt, with the
//     context keys on its right). Panes trade the prompt for one key row.
//   - One focal point per screen: the wordmark on the welcome, the newest
//     card (marked by the accent corner reticle) in the transcript, big
//     digits in a pane, the keycap on a confirm.
//   - The accent marks one thing at a time: the prompt, the reticle, the live
//     mark, the selection bar, the keycap. Gains are ink, losses red.
//   - Restraint on words: labels are UPPERCASE and muted, no sentences in
//     chrome, tips are shown once ever (<data dir>/seen.json).
//   - Motion with a cause: only the ticker flaps (when a shown character
//     changes) and the cursor blink move, each on a tick scheduled only
//     while it is due; HLP_REDUCED_MOTION=1 or ui.motion = "off" stops both.
//   - The first frame renders before any network call (skeleton cards at
//     the result's geometry, so the transcript barely moves on arrival).
package tui
