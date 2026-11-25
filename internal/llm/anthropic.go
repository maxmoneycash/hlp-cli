package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/maxmoneycash/hlp-cli/internal/config"
)

// AnthropicOptions configure the Anthropic Messages API provider.
type AnthropicOptions struct {
	BaseURL    string        // default https://api.anthropic.com
	Model      string        // default DefaultAnthropicModel
	Key        config.Secret // required for api.anthropic.com
	HTTPClient *http.Client  // nil = a default client (no overall timeout; ctx governs)
	MaxTokens  int           // default when Request.MaxTokens is 0; default 16000
	Version    string        // anthropic-version header, default 2023-06-01
}

// Anthropic is the Messages API provider (streaming SSE, tool_use blocks).
// It is implemented over raw HTTP because the official Go SDK is not a
// dependency of this module.
type Anthropic struct {
	opts  AnthropicOptions
	retry retryPolicy
}

// NewAnthropic returns an Anthropic provider.
func NewAnthropic(o AnthropicOptions) *Anthropic {
	o.BaseURL = strings.TrimRight(or(o.BaseURL, AnthropicBaseURL), "/")
	o.Model = or(o.Model, DefaultAnthropicModel)
	o.HTTPClient = safeClient(o.HTTPClient)
	if o.MaxTokens <= 0 {
		o.MaxTokens = 16000
	}
	o.Version = or(o.Version, "2023-06-01")
	return &Anthropic{opts: o, retry: defaultRetry}
}

// Name implements Provider.
func (a *Anthropic) Name() string { return "anthropic:" + a.opts.Model }

// Local implements Provider.
func (a *Anthropic) Local() bool { return isLocalURL(a.opts.BaseURL) }

type antBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type antMessage struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

type antTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type antRequest struct {
	Model       string       `json:"model"`
	MaxTokens   int          `json:"max_tokens"`
	System      string       `json:"system,omitempty"`
	Messages    []antMessage `json:"messages"`
	Tools       []antTool    `json:"tools,omitempty"`
	Temperature *float64     `json:"temperature,omitempty"`
	Stream      bool         `json:"stream"`
}

// jsonInstruction is appended to the system prompt for Request.JSON
// (the Messages API has no plain JSON mode).
const jsonInstruction = "Reply with exactly one JSON object and nothing else: no prose, no code fences."

// buildAnthropicRequest converts the provider-neutral request.
func (a *Anthropic) buildRequest(req Request) antRequest {
	model := or(req.Model, a.opts.Model)
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = a.opts.MaxTokens
	}
	var sys []string
	if s := strings.TrimSpace(req.System); s != "" {
		sys = append(sys, s)
	}
	var msgs []antMessage
	push := func(role string, blocks ...json.RawMessage) {
		if len(blocks) == 0 {
			return
		}
		// The API requires alternating roles: merge consecutive same-role
		// turns (parallel tool results must share one user message).
		if n := len(msgs); n > 0 && msgs[n-1].Role == role {
			msgs[n-1].Content = append(msgs[n-1].Content, blocks...)
			return
		}
		msgs = append(msgs, antMessage{Role: role, Content: blocks})
	}
	mk := func(b antBlock) json.RawMessage {
		raw, _ := json.Marshal(b)
		return raw
	}
	for _, m := range req.Messages {
		switch m.Role {
		case System:
			if s := strings.TrimSpace(m.Content); s != "" {
				sys = append(sys, s)
			}
		case User:
			if m.Content != "" {
				push("user", mk(antBlock{Type: "text", Text: m.Content}))
			}
		case Assistant:
			var blocks []json.RawMessage
			blocks = append(blocks, m.Reasoning...)
			if m.Content != "" {
				blocks = append(blocks, mk(antBlock{Type: "text", Text: m.Content}))
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, mk(antBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: argsObject(tc.Args)}))
			}
			push("assistant", blocks...)
		case ToolRole:
			content := m.Content
			if content == "" {
				content = "(empty)"
			}
			push("user", mk(antBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: content, IsError: isErrorResult(content)}))
		}
	}
	if req.JSON {
		sys = append(sys, jsonInstruction)
	}
	ar := antRequest{Model: model, MaxTokens: maxTok, System: strings.Join(sys, "\n\n"), Messages: msgs, Temperature: req.Temperature, Stream: true}
	for _, t := range req.Tools {
		schema := t.Schema
		if len(schema) == 0 || string(schema) == "null" {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		ar.Tools = append(ar.Tools, antTool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	return ar
}

// isErrorResult recognises the agent's error envelope ({"ok":false,...}).
func isErrorResult(content string) bool {
	return strings.HasPrefix(strings.TrimSpace(content), `{"ok":false`)
}

// Chat implements Provider.
func (a *Anthropic) Chat(ctx context.Context, req Request, onChunk func(Chunk)) (Message, error) {
	if onChunk == nil {
		onChunk = func(Chunk) {}
	}
	body, err := json.Marshal(a.buildRequest(req))
	if err != nil {
		return Message{}, &Error{Provider: "anthropic", Message: "encode request: " + err.Error()}
	}
	headers := map[string]string{"anthropic-version": a.opts.Version}
	if !a.opts.Key.Empty() {
		headers["x-api-key"] = a.opts.Key.Reveal()
	}
	resp, err := postStream(ctx, a.opts.HTTPClient, a.retry, "anthropic", a.opts.BaseURL+"/v1/messages", headers, body, a.opts.Key)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()
	return a.readStream(ctx, resp.Body, onChunk)
}

type antBlockState struct {
	typ       string
	id, name  string
	text      strings.Builder
	json      strings.Builder
	thinking  strings.Builder
	signature string
	raw       json.RawMessage // redacted_thinking and other opaque blocks
}

func (a *Anthropic) readStream(ctx context.Context, r io.Reader, onChunk func(Chunk)) (Message, error) {
	msg := Message{Role: Assistant}
	usage := Usage{}
	blocks := map[int]*antBlockState{}
	var order []int
	var streamErr error
	stopped := false
	stopReason := ""
	var text strings.Builder

	err := readSSE(r, func(ev sseEvent) bool {
		if ctx.Err() != nil {
			return false
		}
		var env struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
			ContentBlock json.RawMessage `json:"content_block"`
			Delta        struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(ev.Data, &env) != nil {
			return true // ignore unparseable keep-alives
		}
		switch env.Type {
		case "message_start":
			usage.InputTokens = env.Message.Usage.InputTokens
			usage.OutputTokens = env.Message.Usage.OutputTokens
		case "content_block_start":
			var cb struct {
				Type string          `json:"type"`
				ID   string          `json:"id"`
				Name string          `json:"name"`
				Text string          `json:"text"`
				In   json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal(env.ContentBlock, &cb)
			st := &antBlockState{typ: cb.Type, id: cb.ID, name: cb.Name}
			switch cb.Type {
			case "text":
				if cb.Text != "" {
					st.text.WriteString(cb.Text)
					text.WriteString(cb.Text)
					onChunk(Chunk{Text: cb.Text})
				}
			case "tool_use", "thinking":
			default:
				st.raw = append(json.RawMessage(nil), env.ContentBlock...)
			}
			blocks[env.Index] = st
			order = append(order, env.Index)
		case "content_block_delta":
			st := blocks[env.Index]
			if st == nil {
				return true
			}
			switch env.Delta.Type {
			case "text_delta":
				st.text.WriteString(env.Delta.Text)
				text.WriteString(env.Delta.Text)
				if env.Delta.Text != "" {
					onChunk(Chunk{Text: env.Delta.Text})
				}
			case "input_json_delta":
				st.json.WriteString(env.Delta.PartialJSON)
			case "thinking_delta":
				st.thinking.WriteString(env.Delta.Thinking)
			case "signature_delta":
				st.signature += env.Delta.Signature
			}
		case "content_block_stop":
			st := blocks[env.Index]
			if st != nil && st.typ == "tool_use" {
				tc := ToolCall{ID: st.id, Name: st.name, Args: argsJSON(st.json.String())}
				onChunk(Chunk{ToolCall: &tc})
			}
		case "message_delta":
			if env.Delta.StopReason != "" {
				stopReason = env.Delta.StopReason
			}
			if env.Usage.OutputTokens > 0 {
				usage.OutputTokens = env.Usage.OutputTokens
			}
			if env.Usage.InputTokens > 0 {
				usage.InputTokens = env.Usage.InputTokens
			}
		case "message_stop":
			stopped = true
			return false
		case "error":
			streamErr = &Error{Provider: "anthropic", Type: scrub(env.Error.Type, a.opts.Key), Message: scrub(env.Error.Message, a.opts.Key)}
			return false
		}
		return true
	})
	if ctx.Err() != nil {
		return Message{}, ctx.Err()
	}
	if streamErr != nil {
		return Message{}, streamErr
	}
	if err != nil {
		return Message{}, ctxErr(ctx, &Error{Provider: "anthropic", Message: "stream: " + err.Error()})
	}
	if !stopped {
		return Message{}, &Error{Provider: "anthropic", Message: errStreamIncomplete.Error()}
	}
	msg.Content = text.String()
	if stopReason == "refusal" && strings.TrimSpace(msg.Content) == "" {
		msg.Content = RefusalText
		onChunk(Chunk{Text: RefusalText})
	}
	for _, idx := range order {
		st := blocks[idx]
		switch st.typ {
		case "tool_use":
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: st.id, Name: st.name, Args: argsJSON(st.json.String())})
		case "thinking":
			raw, _ := json.Marshal(struct {
				Type      string `json:"type"`
				Thinking  string `json:"thinking"`
				Signature string `json:"signature"`
			}{"thinking", st.thinking.String(), st.signature})
			msg.Reasoning = append(msg.Reasoning, raw)
		case "text":
		default:
			if st.typ == "redacted_thinking" && len(st.raw) > 0 {
				msg.Reasoning = append(msg.Reasoning, st.raw)
			}
		}
	}
	u := usage
	onChunk(Chunk{Done: true, Usage: &u})
	return msg, nil
}

// RefusalText replaces an empty reply the model's safety system declined
// (stop_reason "refusal"), so the chat never shows a blank answer.
const RefusalText = "The model declined to answer this request. Try rephrasing it, or use a slash command such as /chart or /vaults."

// errStreamIncomplete is returned when a stream breaks mid-message.
var errStreamIncomplete = errors.New("stream ended early")
