// Package cards renders core.Result values as styled terminal blocks. It is
// shared by the TUI transcript and by the CLI's human output on a TTY, so
// `hlp vaults` in a shell and `/vaults` in the shell look identical.
//
// Every card has the same anatomy: a header (title, muted subtitle, and on
// the right the single focal value with a small colored delta), a body
// (tables share fixed column tracks; charts come from internal/chart at the
// card's inner width), and a dim footer with source, as-of time and
// STALE/PARTIAL badges. Opts.Plain emits no escape codes at all, which is
// what --plain, MCP text content and golden tests use. Narrow widths drop
// low-priority columns before truncating names.
package cards
