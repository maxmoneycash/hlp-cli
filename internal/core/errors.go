package core

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Stable error codes. Agents switch on these, so they are never renamed.
const (
	CodeUsage           = "USAGE"            // bad flags/args            -> exit 2
	CodeUnknownCoin     = "UNKNOWN_COIN"     //                           -> exit 2
	CodeUnknownVault    = "UNKNOWN_VAULT"    //                           -> exit 2
	CodeInvalidTick     = "INVALID_TICK"     // off-grid user price       -> exit 2
	CodeNotFound        = "NOT_FOUND"        //                           -> exit 1
	CodeAPI             = "API_ERROR"        // upstream non-2xx          -> exit 1
	CodeRateLimited     = "RATE_LIMITED"     //                           -> exit 1
	CodeNetwork         = "NETWORK"          // dial/timeout              -> exit 1
	CodeStale           = "STALE_DATA"       //                           -> exit 1
	CodeTradingDisabled = "TRADING_DISABLED" // trading off / no agent key -> exit 3
	CodeConfirmRequired = "CONFIRM_REQUIRED" // non-TTY without --yes     -> exit 3
	CodeRiskLimit       = "RISK_LIMIT"       // risk cap exceeded         -> exit 3
	CodeSafety          = "SAFETY_REFUSED"   // any other safety refusal  -> exit 3
	CodeOrderPending    = "ORDER_PENDING"    // unknown outcome; do NOT resend -> exit 1
	CodeExchange        = "EXCHANGE_REJECTED"
	CodeNotImplemented  = "NOT_IMPLEMENTED" // -> exit 1
	CodeInternal        = "INTERNAL"        // -> exit 1
)

// Error is the structured error printed to stderr as
// {"error":{"code","message","details","hint"}}.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
	Hint    string `json:"hint,omitempty"`
	// Retryable marks transient failures (rate limits, 5xx) that the same
	// call may succeed on later; Details may carry retryAfterMs. Exit 4.
	Retryable bool `json:"retryable,omitempty"`
}

// Error implements the error interface as "CODE: message".
func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errorf builds an *Error.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ExitCode maps an error to the process exit code:
// 0 ok, 1 runtime/API, 2 usage/validation, 3 safety-refused,
// 4 retryable (rate limited or transient upstream failure; try again later).
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var e *Error
	if errors.As(err, &e) {
		if e.Retryable {
			return 4
		}
		switch e.Code {
		case CodeUsage, CodeUnknownCoin, CodeUnknownVault, CodeInvalidTick:
			return 2
		case CodeTradingDisabled, CodeConfirmRequired, CodeRiskLimit, CodeSafety:
			return 3
		}
	}
	return 1
}

var (
	slashOnce sync.Once
	slashRepl *strings.Replacer
)

// SlashHint rewrites CLI command references in a hint ("`hlp vaults
// discover`", "`hlp fund user 0x…`") to the shell's slash commands
// ("`/discover`", "`/fund-user 0x…`"), using each tool's Slash name. A
// command with no slash form keeps its words after the slash.
func SlashHint(hint string) string {
	slashOnce.Do(func() {
		tools := catalog()
		sort.SliceStable(tools, func(i, j int) bool { return len(tools[i].Command) > len(tools[j].Command) })
		var pairs []string
		for _, t := range tools {
			if t.Command == "" || t.Slash == "" {
				continue
			}
			for _, end := range []string{"`", " "} {
				pairs = append(pairs, "`hlp "+t.Command+end, "`/"+t.Slash+end)
			}
		}
		pairs = append(pairs, "`hlp ", "`/")
		slashRepl = strings.NewReplacer(pairs...)
	})
	return slashRepl.Replace(hint)
}
