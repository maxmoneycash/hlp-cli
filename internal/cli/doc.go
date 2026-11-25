// Package cli is the non-interactive command surface (cobra + Charm fang)
// and process wiring: config -> core.Env -> commands / TUI / MCP.
//
// Agent contract:
//   - stdout: data only. JSON {"data","meta"} when --json, HLP_JSON=1, or
//     stdout is not a TTY; NDJSON for streams; cards on a TTY.
//   - stderr: {"error":{"code","message","details","hint"}} with stable codes.
//   - exit codes: 0 ok, 1 runtime/API, 2 usage/validation, 3 safety-refused.
//   - --select a,b.c and --limit on every list command; `hlp schema [tool]`.
//   - never prompt when stdin/stdout is not a TTY; keys never on argv.
package cli
