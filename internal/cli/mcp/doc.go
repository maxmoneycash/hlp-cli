// Package mcp serves the core tool registry over the Model Context Protocol
// (stdio) using the official Go SDK: `hlp mcp`.
//
// Ownership: agents engineer.
//
// Rules:
//   - Read-only tools only, unless BOTH config mcp.allow_trading and the
//     --allow-trading flag are set; then mutating tools are exposed and
//     bounded by the risk caps (no Confirmer exists over MCP, so caps + the
//     explicit "yes" argument are the gate). preview_order is read-only and
//     always listed.
//   - Tool results: structuredContent = {data, meta}, compacted for agents by
//     core.Compact (series ≤ 200 points, curves dropped unless series:true,
//     64 KB cap with meta.truncated); text content = the same envelope as
//     compact JSON bounded to 16 KB, plus the plain card for charts,
//     comparisons and backtests. Third-party text is cleaned of terminal
//     controls (domain.CleanText).
//   - The server adds select, limit and series to every input schema, and
//     maxEvents to streaming tools; it consumes them before core decoding.
//     Streams are never unbounded: one snapshot by default, at most
//     MaxEvents updates within the stream window.
//   - Every tool declares an outputSchema ({data, meta} with a relaxed data
//     schema, since shaping drops fields) and carries _meta.contract.
//   - Never write anything but protocol frames to stdout. Logs go to stderr.
package mcp
