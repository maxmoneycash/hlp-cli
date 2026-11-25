package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp/internal/config"
)

// Error is an upstream provider failure. It never contains the API key or
// the request body: Message is the provider's error text, scrubbed.
type Error struct {
	Provider string // "anthropic", "ollama", ...
	Status   int    // HTTP status, 0 for stream-level errors
	Type     string // provider error type, e.g. "rate_limit_error"
	Message  string
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("llm ")
	b.WriteString(e.Provider)
	if e.Status != 0 {
		fmt.Fprintf(&b, " %d", e.Status)
	}
	if e.Type != "" {
		b.WriteString(" ")
		b.WriteString(e.Type)
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	return b.String()
}

// Retryable reports whether the request may be retried (429, 5xx, 529).
func (e *Error) Retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// maxErrorBody caps how much of an error body is read and kept.
const maxErrorBody = 4 << 10

// retryPolicy controls retries before a stream starts.
type retryPolicy struct {
	attempts int           // total attempts, >= 1
	base     time.Duration // first backoff
	max      time.Duration // cap (also caps retry-after)
}

var defaultRetry = retryPolicy{attempts: 3, base: 500 * time.Millisecond, max: 8 * time.Second}

// postStream POSTs body as JSON and returns the response once it is 2xx.
// It retries 429/5xx/connection errors before any byte is streamed.
func postStream(ctx context.Context, hc *http.Client, rp retryPolicy, provider, endpoint string, headers map[string]string, body []byte, key config.Secret) (*http.Response, error) {
	if rp.attempts < 1 {
		rp.attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < rp.attempts; attempt++ {
		if attempt > 0 {
			wait := rp.base << (attempt - 1)
			if ra, ok := lastErr.(retryAfterError); ok && ra.after > 0 {
				wait = ra.after
			}
			if wait > rp.max {
				wait = rp.max
			}
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			case <-t.C:
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, &Error{Provider: provider, Message: "bad endpoint URL"}
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := hc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = &Error{Provider: provider, Message: scrub(transportMessage(err), key)}
			continue
		}
		if resp.StatusCode/100 == 2 {
			return resp, nil
		}
		e := readHTTPError(resp, provider, key)
		_ = resp.Body.Close()
		if !e.Retryable() {
			return nil, e
		}
		lastErr = retryAfterError{err: e, after: parseRetryAfter(resp.Header.Get("Retry-After"))}
	}
	if ra, ok := lastErr.(retryAfterError); ok {
		return nil, ra.err
	}
	return nil, lastErr
}

// retryAfterError carries a Retry-After hint between attempts.
type retryAfterError struct {
	err   *Error
	after time.Duration
}

func (r retryAfterError) Error() string { return r.err.Error() }

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if s, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && s >= 0 {
		return time.Duration(s * float64(time.Second))
	}
	return 0
}

// transportMessage reduces a net error to a short, key-free description.
func transportMessage(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout connecting to the model server"
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		return "cannot reach the model server (" + oe.Op + ")"
	}
	return err.Error()
}

// readHTTPError decodes {"error":{"type","message"}} (Anthropic and OpenAI
// shapes) or falls back to the status text.
func readHTTPError(resp *http.Response, provider string, key config.Secret) *Error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	e := &Error{Provider: provider, Status: resp.StatusCode}
	e.Type, e.Message = decodeErrorBody(raw)
	if e.Message == "" {
		e.Message = http.StatusText(resp.StatusCode)
	}
	e.Type = scrub(e.Type, key)
	e.Message = scrub(e.Message, key)
	return e
}

func decodeErrorBody(raw []byte) (typ, msg string) {
	var v struct {
		Type  string          `json:"type"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return "", strings.TrimSpace(truncate(string(raw), 300))
	}
	var inner struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Code    any    `json:"code"`
	}
	if json.Unmarshal(v.Error, &inner) == nil && (inner.Message != "" || inner.Type != "") {
		return inner.Type, truncate(inner.Message, 500)
	}
	var s string
	if json.Unmarshal(v.Error, &s) == nil {
		return "", truncate(s, 500)
	}
	return v.Type, ""
}

// scrub removes any occurrence of the key (and its common prefixes) from s.
func scrub(s string, key config.Secret) string {
	k := key.Reveal()
	if k == "" || s == "" {
		return s
	}
	s = strings.ReplaceAll(s, k, "<redacted>")
	// Providers sometimes echo a key prefix/suffix ("sk-ant-...abcd").
	if len(k) >= 12 {
		s = strings.ReplaceAll(s, k[:12], "<redacted>")
		s = strings.ReplaceAll(s, k[len(k)-8:], "<redacted>")
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// sseEvent is one Server-Sent Event.
type sseEvent struct {
	Event string
	Data  []byte
}

// readSSE calls fn for each event in r until EOF, an error, or fn returns
// false. Lines longer than 4 MB are an error.
func readSSE(r io.Reader, fn func(sseEvent) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	var ev sseEvent
	var data bytes.Buffer
	flush := func() bool {
		if data.Len() == 0 && ev.Event == "" {
			return true
		}
		ev.Data = append([]byte(nil), data.Bytes()...)
		data.Reset()
		cont := fn(ev)
		ev = sseEvent{}
		return cont
	}
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			if !flush() {
				return nil
			}
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		switch string(field) {
		case "event":
			ev.Event = string(value)
		case "data":
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.Write(value)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	flush()
	return nil
}

// maxRedirects matches net/http's default limit.
const maxRedirects = 10

// safeClient returns hc (or a fresh client) with a redirect policy that
// never carries the API key off the original host. Go strips Authorization
// on a cross-host redirect but forwards custom headers such as x-api-key,
// and replays the body on 307/308 (audit S7). A caller-supplied
// CheckRedirect is kept; otherwise a shallow copy gets checkRedirect so the
// caller's client is not mutated.
func safeClient(hc *http.Client) *http.Client {
	if hc == nil {
		return &http.Client{CheckRedirect: checkRedirect}
	}
	if hc.CheckRedirect != nil {
		return hc
	}
	c := *hc
	c.CheckRedirect = checkRedirect
	return &c
}

// checkRedirect allows redirects only within the original scheme and host
// (same origin, no https -> http downgrade) and refuses everything else, so
// credentials and prompts never reach a third party.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	orig := via[0].URL
	if !strings.EqualFold(req.URL.Scheme, orig.Scheme) || !strings.EqualFold(req.URL.Host, orig.Host) {
		return fmt.Errorf("refusing redirect from %s to another host (%s)", orig.Host, req.URL.Host)
	}
	return nil
}

// isLocalURL reports whether u points at this machine.
func isLocalURL(u string) bool {
	p, err := url.Parse(u)
	if err != nil {
		return false
	}
	h := p.Hostname()
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// argsJSON returns raw if it is valid JSON (empty = "{}"), otherwise the
// raw text encoded as a JSON string literal (see ToolCall).
func argsJSON(raw string) json.RawMessage {
	t := strings.TrimSpace(raw)
	if t == "" {
		return json.RawMessage("{}")
	}
	if json.Valid([]byte(t)) {
		return json.RawMessage(t)
	}
	b, _ := json.Marshal(raw)
	return b
}

// argsObject returns args as a JSON object for providers that require one
// (malformed args from an earlier turn are replaced by {}).
func argsObject(args json.RawMessage) json.RawMessage {
	t := bytes.TrimSpace(args)
	if len(t) > 0 && t[0] == '{' && json.Valid(t) {
		return t
	}
	return json.RawMessage("{}")
}

// argsString returns args as the string form OpenAI expects in
// function.arguments (unquoting a string literal from a malformed call).
func argsString(args json.RawMessage) string {
	t := bytes.TrimSpace(args)
	if len(t) == 0 {
		return "{}"
	}
	if t[0] == '"' {
		var s string
		if json.Unmarshal(t, &s) == nil {
			return s
		}
	}
	return string(t)
}

// ctxErr prefers the context error over a read error caused by cancellation.
func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// String implements fmt.Stringer; providers print as their name so the key
// in their options can never be formatted by accident.
func (a *Anthropic) String() string { return a.Name() }

// Format implements fmt.Formatter (all verbs print the name).
func (a *Anthropic) Format(f fmt.State, _ rune) { _, _ = fmt.Fprint(f, a.Name()) }

// String implements fmt.Stringer (see Anthropic.String).
func (p *OpenAI) String() string { return p.Name() }

// Format implements fmt.Formatter (all verbs print the name).
func (p *OpenAI) Format(f fmt.State, _ rune) { _, _ = fmt.Fprint(f, p.Name()) }
