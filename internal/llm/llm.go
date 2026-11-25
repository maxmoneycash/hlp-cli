package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/config"
)

// Role of a message.
type Role string

// Message roles.
const (
	System    Role = "system"
	User      Role = "user"
	Assistant Role = "assistant"
	ToolRole  Role = "tool"
)

// ToolCall is a model request to run a tool.
//
// Args is always valid JSON. When a model streams arguments that are not
// valid JSON, the provider stores the raw text as a JSON string literal so
// the caller can report it (the agent loop feeds an error back to the model).
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// Message is one turn. Tool results use Role=ToolRole with ToolCallID set.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`
	ToolCallID string     `json:"toolCallId,omitempty"`
	Name       string     `json:"name,omitempty"`
	// Reasoning holds opaque provider reasoning blocks (Anthropic
	// "thinking"/"redacted_thinking" with signatures) that must be replayed
	// unchanged on the next request of a tool-use loop. Other providers
	// ignore it.
	Reasoning []json.RawMessage `json:"reasoning,omitempty"`
}

// ToolSpec advertises a tool to the model (schema from core.Tool.InputSchema).
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

// Request is one model call.
type Request struct {
	Model       string
	System      string
	Messages    []Message
	Tools       []ToolSpec
	MaxTokens   int
	Temperature *float64
	JSON        bool // ask for a JSON object reply (strategies)
}

// Chunk is a streamed delta. Exactly one of Text/ToolCall is set, or Done.
type Chunk struct {
	Text     string
	ToolCall *ToolCall
	Done     bool
	Usage    *Usage
}

// Usage reports tokens (shown in the status bar).
type Usage struct{ InputTokens, OutputTokens int }

// Provider is a chat model endpoint.
type Provider interface {
	Name() string // "ollama:qwen3:8b", "anthropic:claude-...", ...
	Local() bool  // true for localhost endpoints (shown as a badge)
	// Chat streams deltas to onChunk (may be nil) and returns the final
	// assistant message including any tool calls.
	Chat(ctx context.Context, req Request, onChunk func(Chunk)) (Message, error)
}

// Provider names accepted in config.LLMConfig.Provider.
const (
	ProviderOllama    = "ollama"
	ProviderLMStudio  = "lmstudio"
	ProviderOpenAI    = "openai-compatible"
	ProviderAnthropic = "anthropic"
	// ProviderOpenRouter is OpenRouter's OpenAI-compatible gateway: one
	// OPENROUTER_API_KEY reaches every model it lists, so an arena can pit
	// several vendors against each other ("openrouter/anthropic/claude-sonnet-5").
	ProviderOpenRouter = "openrouter"
)

// Default endpoints.
const (
	OllamaBaseURL    = "http://localhost:11434/v1"
	LMStudioBaseURL  = "http://localhost:1234/v1"
	OpenAIBaseURL    = "https://api.openai.com/v1"
	AnthropicBaseURL = "https://api.anthropic.com"
	// DefaultAnthropicModel is used when provider = anthropic and no model
	// is configured.
	DefaultAnthropicModel = "claude-sonnet-5"
	// OpenRouterBaseURL is OpenRouter's OpenAI-compatible API.
	OpenRouterBaseURL = "https://openrouter.ai/api/v1"
	// OpenRouterKeyEnv holds the OpenRouter key (passes config.CheckLLMKeyEnv).
	OpenRouterKeyEnv = "OPENROUTER_API_KEY"
	// DefaultOpenRouterModel is used when provider = openrouter and no
	// model is configured (the default model, via OpenRouter).
	DefaultOpenRouterModel = "anthropic/claude-sonnet-5"
)

// openRouterHeaders are OpenRouter's optional app-attribution headers. They
// identify the app, never the user.
var openRouterHeaders = map[string]string{
	"HTTP-Referer": "https://github.com/maxmoneycash/hlp-cli",
	"X-Title":      "hlp",
}

// New builds a Provider from config. Provider "" returns (nil, nil): the
// app runs without an LLM.
//
// Keys: the env var named in cfg.APIKeyEnv wins, then the provider's well
// known secret (config.SecretAnthropicKey / config.SecretOpenAIKey via
// secrets, which itself checks ANTHROPIC_API_KEY / OPENAI_API_KEY), then
// those env vars directly. Local providers need no key. Remote providers
// without a key return an error naming the env var to set (never the key).
func New(cfg config.LLMConfig, secrets config.Secrets) (Provider, error) {
	p := strings.ToLower(strings.TrimSpace(cfg.Provider))
	switch p {
	case "", "none", "off":
		return nil, nil
	}
	// Audit S1/S7: the key env var must be a provider key (never
	// HLP_AGENT_KEY or another secret) and a remote endpoint must be https.
	if err := config.CheckLLMKeyEnv(cfg.APIKeyEnv); err != nil {
		return nil, fmt.Errorf("llm: api_key_env %w", err)
	}
	if strings.TrimSpace(cfg.BaseURL) != "" {
		if err := config.CheckLLMBaseURL(cfg.BaseURL); err != nil {
			return nil, fmt.Errorf("llm: base_url %w", err)
		}
	}
	switch p {
	case ProviderOllama:
		return NewOpenAI(OpenAIOptions{Label: "ollama", BaseURL: or(cfg.BaseURL, OllamaBaseURL), Model: cfg.Model,
			Key: lookupKey(cfg.APIKeyEnv, "", "", secrets)}), nil
	case ProviderLMStudio:
		return NewOpenAI(OpenAIOptions{Label: "lmstudio", BaseURL: or(cfg.BaseURL, LMStudioBaseURL), Model: cfg.Model,
			Key: lookupKey(cfg.APIKeyEnv, "", "", secrets)}), nil
	case ProviderOpenAI, "openai":
		base := or(cfg.BaseURL, OpenAIBaseURL)
		key := lookupKey(cfg.APIKeyEnv, config.SecretOpenAIKey, "OPENAI_API_KEY", secrets)
		if key.Empty() && !isLocalURL(base) {
			return nil, fmt.Errorf("llm: provider %q at %s needs an API key: set %s", p, base, or(cfg.APIKeyEnv, "OPENAI_API_KEY"))
		}
		if cfg.Model == "" && !isLocalURL(base) {
			return nil, fmt.Errorf("llm: provider %q needs llm.model in config", p)
		}
		return NewOpenAI(OpenAIOptions{BaseURL: base, Model: cfg.Model, Key: key}), nil
	case ProviderOpenRouter:
		base := or(cfg.BaseURL, OpenRouterBaseURL)
		key := lookupKey(cfg.APIKeyEnv, "", OpenRouterKeyEnv, secrets)
		if key.Empty() && !isLocalURL(base) {
			return nil, fmt.Errorf("llm: provider openrouter needs an API key: set %s (one key runs every model OpenRouter lists)", or(cfg.APIKeyEnv, OpenRouterKeyEnv))
		}
		return NewOpenAI(OpenAIOptions{Label: ProviderOpenRouter, BaseURL: base, Model: or(cfg.Model, DefaultOpenRouterModel),
			Key: key, Headers: openRouterHeaders}), nil
	case ProviderAnthropic, "claude":
		key := lookupKey(cfg.APIKeyEnv, config.SecretAnthropicKey, "ANTHROPIC_API_KEY", secrets)
		if key.Empty() {
			return nil, fmt.Errorf("llm: provider anthropic needs an API key: set %s (or use a local model: provider = \"ollama\")", or(cfg.APIKeyEnv, "ANTHROPIC_API_KEY"))
		}
		return NewAnthropic(AnthropicOptions{BaseURL: or(cfg.BaseURL, AnthropicBaseURL), Model: or(cfg.Model, DefaultAnthropicModel), Key: key}), nil
	default:
		return nil, fmt.Errorf("llm: unknown provider %q (use ollama, lmstudio, openai-compatible, anthropic or openrouter)", cfg.Provider)
	}
}

// lookupKey resolves an API key without ever exposing it in errors. envName
// is read only when config.CheckLLMKeyEnv allows it: HLP_AGENT_KEY and other
// wallet/trading secrets are never resolved here, whatever the caller passes
// (audit S1).
func lookupKey(envName, secretName, fallbackEnv string, secrets config.Secrets) config.Secret {
	if envName = strings.TrimSpace(envName); envName != "" && config.CheckLLMKeyEnv(envName) == nil {
		if v := strings.TrimSpace(os.Getenv(envName)); v != "" {
			return config.NewSecret(v)
		}
	}
	if secretName != "" && secrets != nil {
		if s, err := secrets.Get(secretName); err == nil && !s.Empty() {
			return s
		}
	}
	if fallbackEnv != "" {
		if v := strings.TrimSpace(os.Getenv(fallbackEnv)); v != "" {
			return config.NewSecret(v)
		}
	}
	return config.Secret{}
}

// LocalEndpoint is one local server Detect probes.
type LocalEndpoint struct {
	Label   string // "ollama", "lmstudio"
	BaseURL string // OpenAI-compatible base, ending in /v1
}

// DefaultLocalEndpoints are probed by Detect in priority order.
var DefaultLocalEndpoints = []LocalEndpoint{
	{Label: "ollama", BaseURL: OllamaBaseURL},
	{Label: "lmstudio", BaseURL: LMStudioBaseURL},
}

// DetectTimeout bounds each local probe.
const DetectTimeout = 300 * time.Millisecond

// Detect probes local servers (Ollama, LM Studio) with a 300 ms timeout and
// returns the first reachable one with its first available model, or nil.
func Detect(ctx context.Context) Provider {
	return DetectEndpoints(ctx, nil, DefaultLocalEndpoints)
}

// DetectEndpoints is Detect over an explicit endpoint list and HTTP client
// (nil = a default client). Probes run concurrently; the earliest endpoint
// in the list that answers with at least one model wins.
func DetectEndpoints(ctx context.Context, hc *http.Client, eps []LocalEndpoint) Provider {
	hc = safeClient(hc)
	type res struct {
		model string
		ok    bool
	}
	results := make([]chan res, len(eps))
	for i, ep := range eps {
		ch := make(chan res, 1)
		results[i] = ch
		go func(ep LocalEndpoint) {
			pctx, cancel := context.WithTimeout(ctx, DetectTimeout)
			defer cancel()
			models, err := listModels(pctx, hc, ep.BaseURL, config.Secret{})
			if err != nil || len(models) == 0 {
				ch <- res{}
				return
			}
			ch <- res{model: models[0], ok: true}
		}(ep)
	}
	for i, ch := range results {
		r := <-ch
		if r.ok {
			return NewOpenAI(OpenAIOptions{Label: eps[i].Label, BaseURL: eps[i].BaseURL, Model: r.model, HTTPClient: hc})
		}
	}
	return nil
}

func or(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
