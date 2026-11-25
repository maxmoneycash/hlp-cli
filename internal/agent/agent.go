package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/llm"
)

// EventKind classifies agent progress events for the TUI.
type EventKind string

// Event kinds, in the order a turn usually produces them.
const (
	EvText       EventKind = "text"        // streamed assistant text
	EvToolStart  EventKind = "tool_start"  // about to run Tool with Args
	EvToolResult EventKind = "tool_result" // Result ready (render as a card); Err set when the tool failed
	EvConfirm    EventKind = "confirm"     // mutating tool waiting on Confirmer
	EvError      EventKind = "error"       // provider failure, malformed tool call, refused tool, step limit
	EvDone       EventKind = "done"        // turn finished; Text is the final answer
)

// Event is emitted during Run.
//
// Contract for front-ends: EvText chunks concatenate to the assistant text;
// every EvToolStart is followed by exactly one EvToolResult (Result on
// success, Err on failure) unless the call was refused before running, in
// which case an EvError with Tool set follows instead. EvDone is emitted once
// when the turn completes normally.
type Event struct {
	Kind   EventKind
	Text   string
	Tool   string
	Args   json.RawMessage
	Result *core.Result
	Err    error
}

// Agent runs the tool-calling loop over the core registry.
type Agent struct {
	Provider llm.Provider
	Registry *core.Registry
	Env      *core.Env
	// AllowMutating exposes place_order etc. to the model. Even then every
	// call goes through Env.Guard and Env.Confirm (a confirm card in the TUI).
	AllowMutating bool
	MaxSteps      int // tool round-trips per user turn, default 8
	System        string

	// Invoke runs one tool call; nil = Registry.Invoke. Tests and front-ends
	// that wrap invocation (tracing, caching) set it.
	Invoke func(ctx context.Context, env *core.Env, c core.Call) (*core.Result, error)
	// MaxResultBytes caps each tool result fed back to the model; default
	// DefaultMaxResultBytes.
	MaxResultBytes int
	// MaxTokens per model call; 0 = provider default.
	MaxTokens int
}

// DefaultMaxSteps is the tool round-trip budget per user turn.
const DefaultMaxSteps = 8

// DefaultMaxResultBytes is the per-result budget fed back to the model.
const DefaultMaxResultBytes = 8 << 10

// ErrMaxSteps is returned by Run when the model keeps calling tools after
// MaxSteps round-trips. The returned history is still consistent (every
// tool call has a result).
var ErrMaxSteps = errors.New("agent: tool step limit reached")

// ErrNoProvider is returned by Run without a model.
var ErrNoProvider = errors.New("agent: no model configured")

// Run appends the user input to history, loops model -> tools -> model until
// the model answers without tool calls or MaxSteps is hit, emitting events.
// Tool results are fed back as compact JSON (Result.Data + Meta, truncated to
// ~8 KB; charts as ChartSummary + Plain text, never raw candles).
// Returns the updated history.
//
// The returned history always satisfies provider invariants: each assistant
// tool call is answered by a tool message, even on cancellation or errors
// mid-turn, so the next Run can continue the conversation.
func (a *Agent) Run(ctx context.Context, history []llm.Message, input string, emit func(Event)) ([]llm.Message, error) {
	if emit == nil {
		emit = func(Event) {}
	}
	msgs := append(append([]llm.Message(nil), history...), llm.Message{Role: llm.User, Content: input})
	if a.Provider == nil {
		emit(Event{Kind: EvError, Err: ErrNoProvider})
		return msgs, ErrNoProvider
	}
	if a.Registry == nil {
		err := errors.New("agent: no tool registry")
		emit(Event{Kind: EvError, Err: err})
		return msgs, err
	}
	specs, err := ToolSpecs(a.Registry, a.AllowMutating)
	if err != nil {
		emit(Event{Kind: EvError, Err: err})
		return msgs, err
	}
	system := a.System
	if strings.TrimSpace(system) == "" {
		system = SystemPrompt(a.Env)
	}
	maxSteps := a.MaxSteps
	if maxSteps <= 0 {
		maxSteps = DefaultMaxSteps
	}

	for rounds := 0; ; rounds++ {
		req := llm.Request{System: system, Messages: msgs, Tools: specs, MaxTokens: a.MaxTokens}
		reply, err := a.Provider.Chat(ctx, req, func(c llm.Chunk) {
			if c.Text != "" {
				emit(Event{Kind: EvText, Text: c.Text})
			}
		})
		if err != nil {
			emit(Event{Kind: EvError, Err: err})
			return msgs, err
		}
		reply.Role = llm.Assistant
		msgs = append(msgs, reply)
		if len(reply.ToolCalls) == 0 {
			emit(Event{Kind: EvDone, Text: reply.Content})
			return msgs, nil
		}
		if rounds >= maxSteps {
			for _, tc := range reply.ToolCalls {
				msgs = append(msgs, toolMessage(tc, errorPayload(tc.Name, core.CodeSafety, "not run: tool step limit reached for this turn; answer with what you have", "")))
			}
			emit(Event{Kind: EvError, Err: ErrMaxSteps})
			return msgs, ErrMaxSteps
		}
		for i, tc := range reply.ToolCalls {
			if ctx.Err() != nil {
				// Answer the remaining calls so the history stays valid.
				for _, rest := range reply.ToolCalls[i:] {
					msgs = append(msgs, toolMessage(rest, errorPayload(rest.Name, "CANCELLED", "cancelled by the user", "")))
				}
				emit(Event{Kind: EvError, Err: ctx.Err()})
				return msgs, ctx.Err()
			}
			msgs = append(msgs, toolMessage(tc, a.runTool(ctx, tc, emit)))
		}
	}
}

func toolMessage(tc llm.ToolCall, content string) llm.Message {
	return llm.Message{Role: llm.ToolRole, ToolCallID: tc.ID, Name: tc.Name, Content: content}
}

// runTool validates, sanitizes and invokes one call, returning the content
// fed back to the model.
func (a *Agent) runTool(ctx context.Context, tc llm.ToolCall, emit func(Event)) string {
	tool, ok := a.Registry.Get(tc.Name)
	if !ok {
		err := core.Errorf(core.CodeUsage, "unknown tool %q", tc.Name)
		emit(Event{Kind: EvError, Tool: tc.Name, Args: tc.Args, Err: err})
		return errorPayload(tc.Name, err.Code, err.Message, "use one of the advertised tools")
	}
	if tool.Mutating && !a.AllowMutating {
		err := core.Errorf(core.CodeSafety, "%s is not available to the assistant", tc.Name)
		err.Hint = "trading tools are disabled for the model; the user can run the command themselves"
		emit(Event{Kind: EvError, Tool: tc.Name, Args: tc.Args, Err: err})
		return errorPayload(tc.Name, err.Code, err.Message, err.Hint)
	}
	args, err := sanitizeArgs(tc.Args, tool.Mutating)
	if err != nil {
		e := core.Errorf(core.CodeUsage, "malformed arguments for %s: %v", tc.Name, err)
		emit(Event{Kind: EvError, Tool: tc.Name, Args: tc.Args, Err: e})
		return errorPayload(tc.Name, e.Code, e.Message, "send the arguments as one JSON object matching the tool schema")
	}

	emit(Event{Kind: EvToolStart, Tool: tc.Name, Args: args})
	env := a.Env
	if tool.Mutating && env != nil && env.Confirm != nil {
		wrapped := *env
		orig := env.Confirm
		wrapped.Confirm = func(cctx context.Context, p core.OrderPreview) (bool, error) {
			emit(Event{Kind: EvConfirm, Tool: tc.Name, Args: args})
			return orig(cctx, p)
		}
		env = &wrapped
	}
	res, err := a.invoke(ctx, env, core.Call{Tool: tc.Name, Args: args})
	if err != nil {
		emit(Event{Kind: EvToolResult, Tool: tc.Name, Args: args, Err: err})
		return errorFromErr(tc.Name, err)
	}
	emit(Event{Kind: EvToolResult, Tool: tc.Name, Args: args, Result: res})
	return CompactResult(tc.Name, res, a.maxResultBytes())
}

func (a *Agent) maxResultBytes() int {
	if a.MaxResultBytes > 0 {
		return a.MaxResultBytes
	}
	return DefaultMaxResultBytes
}

// invoke runs the call with panic protection: a buggy tool must not take
// down the chat shell.
func (a *Agent) invoke(ctx context.Context, env *core.Env, c core.Call) (res *core.Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, err = nil, core.Errorf(core.CodeInternal, "tool %s panicked: %v", c.Tool, r)
		}
	}()
	ctx = core.WithSource(ctx, "agent")
	if a.Invoke != nil {
		return a.Invoke(ctx, env, c)
	}
	return a.Registry.Invoke(ctx, env, c)
}

// confirmKeys are stripped (compared case-insensitively, since
// encoding/json matches field names that way) from model-produced arguments
// of mutating tools: the model can never pre-approve an action on the
// user's behalf. Core enforces the same rule for Source "agent".
var confirmKeys = []string{"yes", "confirm", "confirmed", "y"}

func isConfirmKey(k string) bool {
	for _, c := range confirmKeys {
		if strings.EqualFold(k, c) {
			return true
		}
	}
	return false
}

// sanitizeArgs requires a JSON object (null/empty = {}) and, for mutating
// tools, removes any confirmation flag so Env.Guard and Env.Confirm always
// decide (with no Confirmer, core refuses mutating calls without "yes").
func sanitizeArgs(raw json.RawMessage, mutating bool) (json.RawMessage, error) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		t = []byte("{}")
	}
	if t[0] != '{' {
		var s string
		if json.Unmarshal(t, &s) == nil {
			return nil, fmt.Errorf("not valid JSON: %s", truncateString(s, 120))
		}
		return nil, errors.New("arguments must be a JSON object")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(t, &obj); err != nil {
		return nil, fmt.Errorf("not valid JSON: %v", err)
	}
	if !mutating {
		return json.RawMessage(t), nil
	}
	for k := range obj {
		if isConfirmKey(k) {
			delete(obj, k)
		}
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// envelope is the compact JSON fed back to the model.
type envelope struct {
	OK        bool        `json:"ok"`
	Tool      string      `json:"tool"`
	Card      core.Card   `json:"card,omitempty"`
	Title     string      `json:"title,omitempty"`
	Data      any         `json:"data,omitempty"`
	Meta      any         `json:"meta,omitempty"`
	Error     *core.Error `json:"error,omitempty"`
	Truncated bool        `json:"truncated,omitempty"`
	Total     int         `json:"total,omitempty"` // items before truncation
	Note      string      `json:"note,omitempty"`
}

// chartDigest is what the model sees for a chart: numbers and the plain
// text rendering, never raw candles.
type chartDigest struct {
	Coin     string            `json:"coin"`
	Interval string            `json:"interval"`
	Market   any               `json:"market,omitempty"`
	Bars     int               `json:"bars"`
	First    *time.Time        `json:"firstBar,omitempty"`
	Last     *time.Time        `json:"lastBar,omitempty"`
	MAs      []int             `json:"mas,omitempty"`
	Summary  core.ChartSummary `json:"summary"`
	Plain    string            `json:"plain,omitempty"`
}

// CompactResult renders a tool result as compact JSON for the model, within
// maxBytes: {"ok":true,"tool":…,"card":…,"data":…,"meta":…}. Charts become
// a digest (summary + plain rendering). Oversized list data keeps as many
// leading items as fit and sets truncated/total; anything else falls back to
// a truncated text form. maxBytes <= 0 means DefaultMaxResultBytes.
func CompactResult(tool string, r *core.Result, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxResultBytes
	}
	if r == nil {
		return mustJSON(envelope{OK: true, Tool: tool, Note: "no result"})
	}
	env := envelope{OK: true, Tool: tool, Card: r.Card, Title: r.Title, Data: digest(r.Data), Meta: r.Meta}
	out := mustJSON(env)
	if len(out) <= maxBytes {
		return out
	}
	// Charts: drop the text rendering first, keep the numbers.
	if cd, ok := env.Data.(chartDigest); ok && cd.Plain != "" {
		cd.Plain = ""
		env.Data = cd
		env.Note = "plain chart omitted to fit the budget"
		if out = mustJSON(env); len(out) <= maxBytes {
			return out
		}
	}
	// Lists: keep the longest prefix that fits.
	raw, err := json.Marshal(env.Data)
	if err == nil {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) == nil && len(items) > 0 {
			// Per-row series (vault sparklines: ~100 points each) cost
			// more than every other field together. Drop them from each
			// row before dropping rows, so the model sees the whole
			// table, not "1 of 8 items".
			if slim, dropped := slimItems(items); len(dropped) > 0 {
				items = slim
				env.Data = items
				env.Note = "per-row series omitted to fit the budget: " + strings.Join(dropped, ", ")
				if out = mustJSON(env); len(out) <= maxBytes {
					return out
				}
			}
			slimNote := env.Note
			env.Truncated, env.Total = true, len(items)
			// The note is sized for the worst case before searching.
			env.Note = fmt.Sprintf("showing %d of %d items; narrow the query (limit, filters) for more", len(items), len(items))
			if slimNote != "" {
				env.Note += "; " + slimNote
			}
			lo, hi := 0, len(items)
			for lo < hi { // largest n with fitting output
				mid := (lo + hi + 1) / 2
				env.Data = items[:mid]
				if len(mustJSON(env)) <= maxBytes {
					lo = mid
				} else {
					hi = mid - 1
				}
			}
			env.Data = items[:lo]
			env.Note = fmt.Sprintf("showing %d of %d items; narrow the query (limit, filters) for more", lo, len(items))
			if slimNote != "" {
				env.Note += "; " + slimNote
			}
			if out = mustJSON(env); len(out) <= maxBytes {
				return out
			}
		}
	}
	// Anything else: a truncated JSON text of the data.
	env.Truncated = true
	env.Total = 0
	env.Note = "data truncated to fit the budget"
	env.Data = nil
	head := mustJSON(env)
	budget := maxBytes - len(head) - 32
	if budget < 0 {
		budget = 0
	}
	for {
		env.Data = truncateString(string(raw), budget)
		out = mustJSON(env)
		if len(out) <= maxBytes || budget == 0 {
			return out
		}
		// JSON escaping grew the text; shrink by the overshoot.
		budget -= len(out) - maxBytes
		if budget < 0 {
			budget = 0
		}
	}
}

// slimSeriesLen is the array length above which a per-row field counts as
// a series and is dropped by slimItems.
const slimSeriesLen = 16

// slimItems drops long array fields (series) from object items, returning
// the slimmed items and the sorted names of the dropped fields. Items that
// are not objects are kept as they are.
func slimItems(items []json.RawMessage) ([]json.RawMessage, []string) {
	out := make([]json.RawMessage, len(items))
	dropped := map[string]bool{}
	for i, it := range items {
		out[i] = it
		var obj map[string]json.RawMessage
		if json.Unmarshal(it, &obj) != nil {
			continue
		}
		changed := false
		for k, v := range obj {
			var arr []json.RawMessage
			if json.Unmarshal(v, &arr) == nil && len(arr) > slimSeriesLen {
				delete(obj, k)
				dropped[k] = true
				changed = true
			}
		}
		if changed {
			if b, err := json.Marshal(obj); err == nil {
				out[i] = b
			}
		}
	}
	names := make([]string, 0, len(dropped))
	for k := range dropped {
		names = append(names, k)
	}
	sort.Strings(names)
	return out, names
}

// digest swaps chart payloads (and anything carrying raw candles) for a
// model-friendly digest.
func digest(d any) any {
	var cd *core.ChartData
	switch v := d.(type) {
	case *core.ChartData:
		cd = v
	case core.ChartData:
		cd = &v
	}
	if cd == nil {
		return d
	}
	out := chartDigest{Coin: cd.Coin, Interval: string(cd.Interval), Bars: len(cd.Candles), MAs: cd.MAs, Summary: cd.Summary, Plain: cd.Plain}
	if cd.Market.Coin != "" {
		out.Market = cd.Market
	}
	if n := len(cd.Candles); n > 0 {
		f := time.UnixMilli(cd.Candles[0].T).UTC()
		l := time.UnixMilli(cd.Candles[n-1].T).UTC()
		out.First, out.Last = &f, &l
	}
	return out
}

func errorPayload(tool, code, msg, hint string) string {
	return mustJSON(envelope{OK: false, Tool: tool, Error: &core.Error{Code: code, Message: msg, Hint: hint}})
}

func errorFromErr(tool string, err error) string {
	var ce *core.Error
	if errors.As(err, &ce) {
		e := *ce
		if d, err := json.Marshal(e.Details); err != nil || len(d) > 2048 {
			e.Details = nil
		}
		return mustJSON(envelope{OK: false, Tool: tool, Error: &e})
	}
	if errors.Is(err, context.Canceled) {
		return errorPayload(tool, "CANCELLED", "cancelled by the user", "")
	}
	return errorPayload(tool, core.CodeInternal, truncateString(err.Error(), 500), "")
}

func mustJSON(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprintf(`{"ok":false,"error":{"code":"INTERNAL","message":%q}}`, "encode result: "+err.Error())
	}
	return strings.TrimRight(b.String(), "\n")
}

func truncateString(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	// cut on a rune boundary
	for n > 0 && n < len(s) && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n] + "…"
}

// ToolSpecs converts the registry into model tool specs (mutating tools only
// when includeMutating). Each schema is the tool's input JSON Schema; for
// mutating tools the confirmation flag ("yes") is removed so the model is
// never offered it.
func ToolSpecs(r *core.Registry, includeMutating bool) ([]llm.ToolSpec, error) {
	if r == nil {
		return nil, errors.New("agent: nil registry")
	}
	var out []llm.ToolSpec
	for _, t := range r.All() {
		if t.Mutating && !includeMutating {
			continue
		}
		if t.NewInput == nil {
			continue
		}
		s, err := t.InputSchema()
		if err != nil {
			return nil, fmt.Errorf("agent: schema for %s: %w", t.Name, err)
		}
		if t.Mutating && s != nil {
			delete(s.Properties, "yes")
			s.Required = without(s.Required, "yes")
			if p, ok := s.Properties["dryRun"]; ok && p != nil {
				p.Description = "always true for the assistant: previews are signed and shown, never sent"
			}
		}
		schema, err := schemaJSON(s)
		if err != nil {
			return nil, fmt.Errorf("agent: schema for %s: %w", t.Name, err)
		}
		out = append(out, llm.ToolSpec{Name: t.Name, Description: toolDescription(t), Schema: schema})
	}
	return out, nil
}

func without(xs []string, drop string) []string {
	out := xs[:0:0]
	for _, x := range xs {
		if x != drop {
			out = append(out, x)
		}
	}
	return out
}

// schemaJSON marshals a schema as an object schema without the $schema
// dialect key (some providers reject unknown top-level keywords).
func schemaJSON(s *jsonschema.Schema) (json.RawMessage, error) {
	if s == nil {
		return json.RawMessage(`{"type":"object","properties":{}}`), nil
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	delete(m, "$schema")
	delete(m, "$id")
	if _, ok := m["type"]; !ok {
		m["type"] = "object"
	}
	if _, ok := m["properties"]; !ok {
		m["properties"] = map[string]any{}
	}
	return json.Marshal(m)
}

func toolDescription(t core.Tool) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(t.Summary))
	if d := strings.TrimSpace(t.Description); d != "" {
		if b.Len() > 0 {
			b.WriteString(". ")
		}
		b.WriteString(d)
	}
	if t.Command != "" {
		fmt.Fprintf(&b, " (CLI: hlp %s)", t.Command)
	}
	if t.Mutating {
		b.WriteString(" MUTATING: produces a signed dry-run preview; the user must confirm it in the app. Never claim an order was placed.")
	}
	return b.String()
}
