package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hlpxyz"
	"github.com/maxmoneycash/hlp/internal/hyperliquid"
	"github.com/maxmoneycash/hlp/internal/registry"
)

// AsError maps any error to a structured *Error with a stable Code, so every
// front-end (CLI stderr JSON, MCP isError content, agent tool results) sees
// the same contract. A nil error returns nil.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	var api *hyperliquid.APIError
	var hx *hlpxyz.APIError
	var ue *url.Error
	var ne net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return &Error{Code: CodeNetwork, Message: "canceled"}
	case errors.Is(err, context.DeadlineExceeded):
		return transient(CodeNetwork, "timed out", "a timeout; wait retryAfterMs and retry once", networkRetry)
	case errors.Is(err, hyperliquid.ErrUnknownCoin):
		return &Error{Code: CodeUnknownCoin, Message: err.Error(), Hint: "list markets with `hlp markets <text>`"}
	case errors.Is(err, hyperliquid.ErrRateLimited):
		wait, _ := hyperliquid.RetryAfter(err)
		return rateLimited(err.Error(), wait)
	case errors.Is(err, hlpxyz.ErrRateLimited):
		return rateLimited(err.Error(), hlpxyzRetry)
	case errors.Is(err, hyperliquid.ErrNotFound):
		return &Error{Code: CodeNotFound, Message: err.Error()}
	case errors.Is(err, hlpxyz.ErrNotAVault):
		return &Error{Code: CodeUnknownVault, Message: err.Error(), Hint: "search with `hlp vaults <name>`"}
	case errors.Is(err, hlpxyz.ErrNoCache):
		return &Error{Code: CodeNotFound, Message: err.Error(), Hint: "hlp.xyz does not track this vault; Hyperliquid data is still available"}
	case errors.Is(err, hlpxyz.ErrLoading):
		return transient(CodeStale, err.Error(), "hlp.xyz is still loading this data; wait retryAfterMs and retry once", loadingRetry)
	case errors.Is(err, domain.ErrNotImplemented):
		return &Error{Code: CodeNotImplemented, Message: err.Error()}
	case errors.As(err, &api):
		code := CodeAPI
		if api.Code == "BAD_REQUEST" {
			code = CodeUsage
		}
		e := &Error{Code: code, Message: api.Error(), Details: map[string]any{"status": api.Status, "upstream": api.Code}}
		wait, ok := hyperliquid.RetryAfter(err)
		if !ok && api.Status >= 500 {
			wait, ok = networkRetry, true // every 5xx is transient
		}
		if ok {
			e.Retryable = true
			e.Details = map[string]any{"status": api.Status, "upstream": api.Code, "retryAfterMs": wait.Milliseconds()}
			e.Hint = "a temporary Hyperliquid error; wait retryAfterMs and retry once"
		}
		return e
	case errors.As(err, &hx):
		return hlpxyzError(hx)
	case errors.As(err, &ue), errors.As(err, &ne):
		return transient(CodeNetwork, err.Error(), "check your connection; Hyperliquid may be unreachable. Wait retryAfterMs and retry once", networkRetry)
	}
	return &Error{Code: CodeInternal, Message: err.Error()}
}

// Waits suggested for transient failures (exit 4, retryable:true).
const (
	networkRetry = 2 * time.Second  // timeouts, dial/reset errors, 5xx
	loadingRetry = 60 * time.Second // hlp.xyz still building a cache
)

// transient is a retryable error with details.retryAfterMs: the exit-4
// class the help and AGENTS.md promise (rate limit, network, 5xx).
func transient(code, msg, hint string, wait time.Duration) *Error {
	return &Error{Code: code, Message: msg, Hint: hint, Retryable: true,
		Details: map[string]any{"retryAfterMs": wait.Milliseconds()}}
}

// hlpxyzRetry is the suggested wait after an hlp.xyz 429 or 5xx (the
// client keeps hlp.xyz at 90 requests a minute, so a slot frees quickly).
const hlpxyzRetry = 5 * time.Second

// rateLimited is RATE_LIMITED with the retry timing agents act on
// (AGENT-UX #19): wait details.retryAfterMs, then retry once.
func rateLimited(msg string, wait time.Duration) *Error {
	if wait <= 0 {
		wait = hyperliquid.DefaultPenalty
	}
	return &Error{Code: CodeRateLimited, Message: msg, Retryable: true,
		Details: map[string]any{"retryAfterMs": wait.Milliseconds()},
		Hint:    "wait retryAfterMs, then retry once; do not run hlp calls in parallel"}
}

// hlpxyzError maps an hlp.xyz HTTP error (AGENT-UX #18): 404 is NOT_FOUND
// (an unknown index slug is USAGE with the valid slugs), 429 is
// RATE_LIMITED and 5xx is a retryable API_ERROR. The request path is a
// detail, not part of the message.
func hlpxyzError(hx *hlpxyz.APIError) *Error {
	msg := "hlp.xyz: " + hx.Message
	if hx.Message == "" {
		msg = fmt.Sprintf("hlp.xyz: HTTP %d", hx.Status)
	}
	details := map[string]any{"status": hx.Status, "path": hx.Path}
	switch {
	case hx.Status == http.StatusTooManyRequests:
		e := rateLimited(msg, hlpxyzRetry)
		e.Details.(map[string]any)["status"] = hx.Status
		return e
	case hx.Status == http.StatusNotFound && indexRequest(hx):
		slugs := indexSlugs()
		e := &Error{Code: CodeUsage, Message: msg, Details: details, Hint: "run `hlp index` to list indexes"}
		if len(slugs) > 0 {
			e.Hint = "one of: " + strings.Join(slugs, ", ")
			details["slugs"] = slugs
		}
		return e
	case hx.Status == http.StatusNotFound:
		return &Error{Code: CodeNotFound, Message: msg, Details: details}
	case hx.Status >= 500:
		details["retryAfterMs"] = hlpxyzRetry.Milliseconds()
		return &Error{Code: CodeAPI, Message: msg, Details: details, Retryable: true, Hint: "hlp.xyz is having trouble; wait retryAfterMs and retry once"}
	}
	return &Error{Code: CodeAPI, Message: msg, Details: details}
}

// indexRequest reports whether an hlp.xyz error came from an index lookup
// (idxGet), which says "Index not found" for an unknown slug.
func indexRequest(hx *hlpxyz.APIError) bool {
	return strings.Contains(hx.Path, "idxGet") || strings.Contains(strings.ToLower(hx.Message), "index")
}

// indexSlugs lists the curated index slugs (registry kind index).
func indexSlugs() []string {
	reg, err := registry.Default()
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range reg.Filter(registry.Filter{Kinds: []registry.Kind{registry.KindIndex}}) {
		if e.Slug != "" {
			out = append(out, e.Slug)
		}
	}
	return out
}

// Source labels where a call came from (cli | tui | agent | mcp | bot). It
// travels on the context so mutating tools can fill Intent.Source and the
// guard can apply surface-specific rules (MCP trading flag).
type sourceKey struct{}

// WithSource returns ctx tagged with a call source.
func WithSource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, sourceKey{}, source)
}

// SourceUnknown is the source of a ctx nobody tagged. It fails closed: the
// trading gates treat it like "agent" (yes is ignored and a human
// confirmer is required), so a new code path that forgets WithSource never
// gets the most trusted policy (audit S6).
const SourceUnknown = "unknown"

// SourceFrom returns the call source on ctx (SourceUnknown when unset).
func SourceFrom(ctx context.Context) string {
	if s, ok := ctx.Value(sourceKey{}).(string); ok && s != "" {
		return s
	}
	return SourceUnknown
}

// yesTrustedSource reports whether yes may pre-approve a call from source: a
// person at the CLI or TUI, or an MCP client (which also needs
// --allow-trading and mcp.allow_trading). Everything else (agent, bot,
// unknown) needs a human confirmer.
func yesTrustedSource(source string) bool {
	switch source {
	case "cli", "tui", "mcp":
		return true
	}
	return false
}
