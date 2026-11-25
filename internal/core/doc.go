// Package core is the single tool registry that every surface shares:
// the cobra CLI, the TUI slash commands, the MCP server and the LLM agent
// all call the SAME Tool with the SAME typed input and get the SAME Result.
// One implementation, four front-ends. This is what makes hlp agent-native.
//
// The contract types (Env, Tool, Result, Card, Error, Call, Guard) are
// stable; tool implementations live in tools_*.go.
//
// Rules:
//   - Tools are read-only unless Mutating is true. Mutating tools must go
//     through Env.Guard (risk caps) and Env.Confirm, and default to dry-run.
//   - Every Result carries domain.Meta (source, asOf, stale).
//   - Errors returned by tools are *Error with a stable Code; ExitCode maps
//     them to 0/1/2/3 (ok / runtime / usage / safety-refused).
//   - Input structs use json tags (wire names) and `jsonschema:"..."`
//     descriptions; the JSON Schema for MCP, the agent and `hlp schema` is
//     generated from them, so keep descriptions agent-quality.
package core
