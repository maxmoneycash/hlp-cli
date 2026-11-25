package hyperliquid

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrUnknownCoin: coin is not in meta/spotMeta/perpDexs (validated locally).
	ErrUnknownCoin = errors.New("unknown coin")
	// ErrNotFound: e.g. vaultDetails returned null for an address that is not a vault.
	ErrNotFound = errors.New("not found")
	// ErrRateLimited: the local limiter or HTTP 429 refused the request.
	ErrRateLimited = errors.New("rate limited")
)

// APIError is a non-2xx or malformed response from Hyperliquid.
type APIError struct {
	Status int    // HTTP status
	Code   string // stable code: BAD_REQUEST | SERVER | DECODE
	Body   string // truncated to 512 bytes
	// RetryAfter is a suggested wait before retrying a 5xx (0 = do not retry).
	RetryAfter time.Duration
}

// Retryable reports whether the request may succeed if repeated (5xx).
func (e *APIError) Retryable() bool { return e != nil && e.Status >= 500 }

// RateLimitError is an HTTP 429 that survived every retry. It wraps
// ErrRateLimited, so errors.Is(err, ErrRateLimited) holds.
type RateLimitError struct {
	Type       string        // the /info request type
	RetryAfter time.Duration // how long the limiter now holds requests back
}

// Error implements error.
func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%v: HTTP 429 from %s (retry in %s)", ErrRateLimited, e.Type, e.RetryAfter.Round(time.Second))
}

// Unwrap makes errors.Is(err, ErrRateLimited) true.
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// RetryAfter reports whether err is worth retrying and how long to wait
// first: a 429 (RateLimitError) or a 5xx APIError. Core maps it to
// Error.Retryable and details.retryAfterMs (AGENT-UX #19).
func RetryAfter(err error) (time.Duration, bool) {
	var rl *RateLimitError
	if errors.As(err, &rl) {
		return rl.RetryAfter, true
	}
	var ae *APIError
	if errors.As(err, &ae) && ae.Retryable() {
		d := ae.RetryAfter
		if d <= 0 {
			d = time.Second
		}
		return d, true
	}
	if errors.Is(err, ErrRateLimited) {
		return DefaultPenalty, true
	}
	return 0, false
}

// Error implements error.
func (e *APIError) Error() string {
	return fmt.Sprintf("hyperliquid %s (HTTP %d): %s", e.Code, e.Status, e.Body)
}

// UnknownCoinError is returned by Registry.Resolve on a miss. It wraps
// ErrUnknownCoin (errors.Is works) and carries up to 5 suggestions.
type UnknownCoinError struct {
	Input       string
	Suggestions []string
}

// Error lists the suggestions, e.g. `unknown coin "BTX" (did you mean BTC?)`.
func (e *UnknownCoinError) Error() string {
	if len(e.Suggestions) == 0 {
		return fmt.Sprintf("unknown coin %q", e.Input)
	}
	return fmt.Sprintf("unknown coin %q (did you mean %s?)", e.Input, strings.Join(e.Suggestions, ", "))
}

// Unwrap makes errors.Is(err, ErrUnknownCoin) true.
func (e *UnknownCoinError) Unwrap() error { return ErrUnknownCoin }
