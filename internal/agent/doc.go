// Package agent is the optional LLM layer of the chat shell: a provider-
// agnostic tool-calling loop over the core registry (tools map 1:1 to CLI
// commands), plus the deterministic intent parser that works with no LLM.
//
// Guardrails:
//   - The shell is fully useful without any model (ParseIntent).
//   - Mutating tools are hidden from the model unless AllowMutating, and
//     still pass Env.Guard + a confirm card for every call; the model can
//     never set "yes": true on its own (the loop strips it).
//   - Tool output fed back to the model is data, never instructions.
//   - No paid API is ever called by tests or by default (local-first).
package agent
