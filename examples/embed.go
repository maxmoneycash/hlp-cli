// Package examples embeds the shipped example strategies so agents and MCP
// clients can run them by name (example:ema-cross) from any working
// directory, without file-system access.
package examples

import "embed"

// FS holds pine/, thesis/, llm/ and arena/.
//
//go:embed pine thesis llm arena
var FS embed.FS
