package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"

	"github.com/maxmoneycash/hlp/internal/hlpxyz"
	"github.com/maxmoneycash/hlp/internal/hyperliquid"
)

// TestAsErrorExitCodes locks the exit-code contract: 4 (retryable, with
// details.retryAfterMs) for rate limits, timeouts, network errors, 5xx and
// hlp.xyz still loading; 2 for usage; 1 otherwise.
func TestAsErrorExitCodes(t *testing.T) {
	dial := &url.Error{Op: "Get", URL: "https://api.hyperliquid.xyz/info", Err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}
	for _, tc := range []struct {
		name string
		err  error
		exit int
	}{
		{"timeout", fmt.Errorf("info: %w", context.DeadlineExceeded), 4},
		{"dial", dial, 4},
		{"hl 5xx", &hyperliquid.APIError{Status: 502, Code: "UPSTREAM"}, 4},
		{"hl 4xx", &hyperliquid.APIError{Status: 400, Code: "BAD_REQUEST"}, 2},
		{"hlpxyz loading", fmt.Errorf("vault: %w", hlpxyz.ErrLoading), 4},
		{"hlpxyz 5xx", &hlpxyz.APIError{Status: 503}, 4},
		{"hlpxyz 429", &hlpxyz.APIError{Status: 429}, 4},
		{"unknown coin", fmt.Errorf("%w: ZZZ", hyperliquid.ErrUnknownCoin), 2},
		{"canceled", context.Canceled, 1},
		{"internal", errors.New("boom"), 1},
	} {
		e := AsError(tc.err)
		if got := ExitCode(e); got != tc.exit {
			t.Errorf("%s: exit %d, want %d (%+v)", tc.name, got, tc.exit, e)
		}
		if tc.exit == 4 {
			d, _ := e.Details.(map[string]any)
			if !e.Retryable || d["retryAfterMs"] == nil {
				t.Errorf("%s: retryable %v details %v", tc.name, e.Retryable, e.Details)
			}
		}
	}
}
