package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/maxmoneycash/hlp-cli/internal/config"
)

// OpenAIOptions configure an OpenAI-compatible Chat Completions provider
// (OpenAI, Ollama, LM Studio, vLLM, llama.cpp server, ...).
type OpenAIOptions struct {
	Label      string        // name prefix: "ollama", "lmstudio"; default "openai"
	BaseURL    string        // ends in /v1, e.g. http://localhost:11434/v1
	Model      string        // "" = first model from GET /models (local servers)
	Key        config.Secret // optional for local servers
	HTTPClient *http.Client
	MaxTokens  int // default when Request.MaxTokens is 0; 0 = server default
	// Headers are extra request headers (OpenRouter's app attribution).
	// They must not carry credentials: Key is the only secret.
	Headers map[string]string
}

// OpenAI is the OpenAI-compatible provider.
type OpenAI struct {
	opts  OpenAIOptions
	retry retryPolicy

	mu    sync.Mutex
	model string // resolved model
}

// NewOpenAI returns an OpenAI-compatible provider.
func NewOpenAI(o OpenAIOptions) *OpenAI {
	o.BaseURL = strings.TrimRight(or(o.BaseURL, OpenAIBaseURL), "/")
	o.Label = or(o.Label, "openai")
	o.HTTPClient = safeClient(o.HTTPClient)
	return &OpenAI{opts: o, retry: defaultRetry, model: o.Model}
}

// Name implements Provider: "<label>:<model>".
func (p *OpenAI) Name() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.model == "" {
		return p.opts.Label
	}
	return p.opts.Label + ":" + p.model
}

// Local implements Provider.
func (p *OpenAI) Local() bool { return isLocalURL(p.opts.BaseURL) }

// Model returns the configured or resolved model ("" until resolved).
func (p *OpenAI) Model() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.model
}

// resolveModel picks the first served model when none is configured.
func (p *OpenAI) resolveModel(ctx context.Context) (string, error) {
	p.mu.Lock()
	m := p.model
	p.mu.Unlock()
	if m != "" {
		return m, nil
	}
	models, err := listModels(ctx, p.opts.HTTPClient, p.opts.BaseURL, p.opts.Key)
	if err != nil {
		return "", err
	}
	if len(models) == 0 {
		return "", &Error{Provider: p.opts.Label, Message: "the server has no models loaded (try `ollama pull qwen3:8b`)"}
	}
	p.mu.Lock()
	p.model = models[0]
	p.mu.Unlock()
	return models[0], nil
}

// listModels calls GET {base}/models and returns model ids in server order.
func listModels(ctx context.Context, hc *http.Client, base string, key config.Secret) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/models", nil)
	if err != nil {
		return nil, &Error{Provider: "openai", Message: "bad endpoint URL"}
	}
	if !key.Empty() {
		req.Header.Set("Authorization", "Bearer "+key.Reveal())
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Provider: "openai", Message: scrub(transportMessage(err), key)}
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, readHTTPError(resp, "openai", key)
	}
	var v struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&v); err != nil {
		return nil, &Error{Provider: "openai", Message: "decode /models: " + err.Error()}
	}
	out := make([]string, 0, len(v.Data))
	for _, d := range v.Data {
		if d.ID != "" {
			out = append(out, d.ID)
		}
	}
	return out, nil
}

type oaFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

type oaToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function oaFunctionCall `json:"function"`
}

type oaMessage struct {
	Role       string       `json:"role"`
	Content    *string      `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type oaRequest struct {
	Model         string      `json:"model"`
	Messages      []oaMessage `json:"messages"`
	Tools         []oaTool    `json:"tools,omitempty"`
	MaxTokens     int         `json:"max_tokens,omitempty"`
	Temperature   *float64    `json:"temperature,omitempty"`
	Stream        bool        `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
	ResponseFormat *struct {
		Type string `json:"type"`
	} `json:"response_format,omitempty"`
}

func strp(s string) *string { return &s }

func (p *OpenAI) buildRequest(model string, req Request) oaRequest {
	r := oaRequest{Model: model, Temperature: req.Temperature, Stream: true}
	r.MaxTokens = req.MaxTokens
	if r.MaxTokens <= 0 {
		r.MaxTokens = p.opts.MaxTokens
	}
	if s := strings.TrimSpace(req.System); s != "" {
		r.Messages = append(r.Messages, oaMessage{Role: "system", Content: strp(s)})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case System:
			r.Messages = append(r.Messages, oaMessage{Role: "system", Content: strp(m.Content)})
		case User:
			r.Messages = append(r.Messages, oaMessage{Role: "user", Content: strp(m.Content)})
		case Assistant:
			om := oaMessage{Role: "assistant"}
			if m.Content != "" || len(m.ToolCalls) == 0 {
				om.Content = strp(m.Content)
			}
			for _, tc := range m.ToolCalls {
				om.ToolCalls = append(om.ToolCalls, oaToolCall{ID: tc.ID, Type: "function", Function: oaFunctionCall{Name: tc.Name, Arguments: argsString(tc.Args)}})
			}
			r.Messages = append(r.Messages, om)
		case ToolRole:
			r.Messages = append(r.Messages, oaMessage{Role: "tool", ToolCallID: m.ToolCallID, Content: strp(m.Content)})
		}
	}
	for _, t := range req.Tools {
		var ot oaTool
		ot.Type = "function"
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.Schema
		if len(ot.Function.Parameters) == 0 || string(ot.Function.Parameters) == "null" {
			ot.Function.Parameters = json.RawMessage(`{"type":"object"}`)
		}
		r.Tools = append(r.Tools, ot)
	}
	if req.JSON {
		r.ResponseFormat = &struct {
			Type string `json:"type"`
		}{"json_object"}
	}
	if !p.Local() {
		// Hosted endpoints report usage only when asked; some local servers
		// reject unknown fields, so it is not sent to them.
		r.StreamOptions = &struct {
			IncludeUsage bool `json:"include_usage"`
		}{true}
	}
	return r
}

// Chat implements Provider.
func (p *OpenAI) Chat(ctx context.Context, req Request, onChunk func(Chunk)) (Message, error) {
	if onChunk == nil {
		onChunk = func(Chunk) {}
	}
	model := req.Model
	if model == "" {
		m, err := p.resolveModel(ctx)
		if err != nil {
			return Message{}, err
		}
		model = m
	}
	body, err := json.Marshal(p.buildRequest(model, req))
	if err != nil {
		return Message{}, &Error{Provider: p.opts.Label, Message: "encode request: " + err.Error()}
	}
	headers := map[string]string{}
	for k, v := range p.opts.Headers {
		headers[k] = v
	}
	if !p.opts.Key.Empty() {
		headers["Authorization"] = "Bearer " + p.opts.Key.Reveal()
	}
	resp, err := postStream(ctx, p.opts.HTTPClient, p.retry, p.opts.Label, p.opts.BaseURL+"/chat/completions", headers, body, p.opts.Key)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "application/json") {
		// Some servers ignore stream:true; accept a plain completion.
		return p.readJSON(resp.Body, onChunk)
	}
	return p.readStream(ctx, resp.Body, onChunk)
}

type oaPartial struct {
	id, name string
	args     strings.Builder
}

func (p *OpenAI) readStream(ctx context.Context, r io.Reader, onChunk func(Chunk)) (Message, error) {
	var text strings.Builder
	calls := map[int]*oaPartial{}
	var order []int
	var usage *Usage
	var streamErr error
	done := false
	extra, last := 0, -1

	err := readSSE(r, func(ev sseEvent) bool {
		if ctx.Err() != nil {
			return false
		}
		data := strings.TrimSpace(string(ev.Data))
		if data == "" {
			return true
		}
		if data == "[DONE]" {
			done = true
			return false
		}
		var c struct {
			Choices []struct {
				Delta struct {
					Content   *string `json:"content"`
					ToolCalls []struct {
						Index    *int   `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(ev.Data, &c) != nil {
			return true
		}
		if len(c.Error) > 0 && string(c.Error) != "null" {
			typ, msg := decodeErrorBody([]byte(`{"error":` + string(c.Error) + `}`))
			streamErr = &Error{Provider: p.opts.Label, Type: scrub(typ, p.opts.Key), Message: scrub(msg, p.opts.Key)}
			return false
		}
		if c.Usage != nil {
			usage = &Usage{InputTokens: c.Usage.PromptTokens, OutputTokens: c.Usage.CompletionTokens}
		}
		for _, ch := range c.Choices {
			if ch.Delta.Content != nil && *ch.Delta.Content != "" {
				text.WriteString(*ch.Delta.Content)
				onChunk(Chunk{Text: *ch.Delta.Content})
			}
			for _, tc := range ch.Delta.ToolCalls {
				var idx int
				switch {
				case tc.Index != nil:
					idx = *tc.Index
					// A new id at an occupied slot starts a new call
					// (servers that reuse index 0 for distinct calls).
					if tc.ID != "" && calls[idx] != nil && calls[idx].id != "" && calls[idx].id != tc.ID {
						idx = 1000 + extra
						extra++
					}
				case last < 0 || (tc.ID != "" && calls[last].id != tc.ID):
					idx = 1000 + extra
					extra++
				default:
					idx = last // continuation of the previous call
				}
				last = idx
				pc := calls[idx]
				if pc == nil {
					pc = &oaPartial{}
					calls[idx] = pc
					order = append(order, idx)
				}
				if tc.ID != "" {
					pc.id = tc.ID
				}
				if tc.Function.Name != "" {
					pc.name = tc.Function.Name
				}
				pc.args.WriteString(tc.Function.Arguments)
			}
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
		return Message{}, ctxErr(ctx, &Error{Provider: p.opts.Label, Message: "stream: " + err.Error()})
	}
	_ = done // some servers close without [DONE]; the content read so far is complete
	msg := Message{Role: Assistant, Content: text.String()}
	sort.SliceStable(order, func(i, j int) bool { return order[i] < order[j] })
	for n, idx := range order {
		pc := calls[idx]
		id := pc.id
		if id == "" {
			id = "call_" + itoa(n)
		}
		tc := ToolCall{ID: id, Name: pc.name, Args: argsJSON(pc.args.String())}
		msg.ToolCalls = append(msg.ToolCalls, tc)
		onChunk(Chunk{ToolCall: &tc})
	}
	if usage == nil {
		usage = &Usage{}
	}
	onChunk(Chunk{Done: true, Usage: usage})
	return msg, nil
}

// readJSON handles a non-streamed chat completion.
func (p *OpenAI) readJSON(r io.Reader, onChunk func(Chunk)) (Message, error) {
	var v struct {
		Choices []struct {
			Message struct {
				Content   *string      `json:"content"`
				ToolCalls []oaToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(io.LimitReader(r, 16<<20)).Decode(&v); err != nil {
		return Message{}, &Error{Provider: p.opts.Label, Message: "decode completion: " + err.Error()}
	}
	msg := Message{Role: Assistant}
	if len(v.Choices) > 0 {
		c := v.Choices[0].Message
		if c.Content != nil {
			msg.Content = *c.Content
			if msg.Content != "" {
				onChunk(Chunk{Text: msg.Content})
			}
		}
		for n, tc := range c.ToolCalls {
			id := tc.ID
			if id == "" {
				id = "call_" + itoa(n)
			}
			call := ToolCall{ID: id, Name: tc.Function.Name, Args: argsJSON(tc.Function.Arguments)}
			msg.ToolCalls = append(msg.ToolCalls, call)
			onChunk(Chunk{ToolCall: &call})
		}
	}
	onChunk(Chunk{Done: true, Usage: &Usage{InputTokens: v.Usage.PromptTokens, OutputTokens: v.Usage.CompletionTokens}})
	return msg, nil
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
