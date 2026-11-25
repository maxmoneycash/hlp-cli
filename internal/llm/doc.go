// Package llm is the provider-agnostic chat + tool-calling client used by
// internal/agent (chat) and internal/strategy (LLM strategies).
//
// Providers: "anthropic" (Messages API, tool_use blocks) and
// "openai-compatible" (Chat Completions with tools), which also covers
// Ollama (http://localhost:11434/v1) and LM Studio (http://localhost:1234/v1),
// plus the "openrouter" preset (https://openrouter.ai/api/v1, key in
// OPENROUTER_API_KEY) whose public model catalog Models reads without a key.
// Budget wraps providers with a shared call cap and a cost tally. Local first: with no config, Detect probes Ollama then LM Studio.
//
// Both providers stream (SSE) over plain net/http: the official Anthropic
// Go SDK is not a module dependency. Anthropic "thinking" blocks are kept
// in Message.Reasoning and replayed unchanged within a tool-use loop.
// Retries (429/5xx/529, Retry-After honoured, 3 attempts) happen only
// before a stream starts. Errors are *Error values scrubbed of the key.
//
// HARD RULES: keys come from env vars only (config.LLMConfig.APIKeyEnv,
// ANTHROPIC_API_KEY, OPENAI_API_KEY, OPENROUTER_API_KEY) or the keychain via config.Secrets;
// never logged, never in errors. Tests use httptest fakes; nothing in CI or
// development calls a paid API.
package llm
