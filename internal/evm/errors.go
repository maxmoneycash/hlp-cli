package evm

import (
	"errors"
	"fmt"
	"strings"
)

// Standard Solidity revert payload selectors.
var (
	SelErrorString = MustSelector("0x08c379a0") // Error(string)
	SelPanic       = MustSelector("0x4e487b71") // Panic(uint256)
)

// ErrEmptyResult is an eth_call that returned "0x": usually no contract at
// the address (or a call to a function it does not have that does not
// revert). Callers check eth_getCode once to tell which.
var ErrEmptyResult = errors.New("eth_call returned no data (no contract at this address?)")

// ErrNoEndpoints means the client was built without RPC URLs.
var ErrNoEndpoints = errors.New("no HyperEVM RPC endpoints configured")

// ChainMismatchError means an endpoint answers for a different chain.
type ChainMismatchError struct {
	Endpoint  string
	Want, Got uint64
}

func (e *ChainMismatchError) Error() string {
	return fmt.Sprintf("%s serves chain %d, want %d", e.Endpoint, e.Got, e.Want)
}

// RPCError is a JSON-RPC error object other than a revert.
type RPCError struct {
	Code    int
	Message string
	Data    []byte
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// RevertError is an eth_call that reverted. Name is the custom error
// signature when the selector is known ("NotKeeper()"), Reason the
// Error(string) text, Panic the Panic(uint256) code.
type RevertError struct {
	Data     []byte
	Selector Selector
	Name     string
	Reason   string
	Panic    uint64
	HasPanic bool
}

func (e *RevertError) Error() string {
	switch {
	case e.Reason != "":
		return "execution reverted: " + e.Reason
	case e.HasPanic:
		return fmt.Sprintf("execution reverted: panic 0x%02x", e.Panic)
	case e.Name != "":
		return "execution reverted: " + e.Name
	case len(e.Data) >= 4:
		return "execution reverted: " + e.Selector.Hex()
	}
	return "execution reverted"
}

// IsPrecompileFailure reports a PrecompileLib__* revert: the RPC cannot
// serve HyperCore state (wrong block tag or a node without L1 state), so
// another endpoint may succeed.
func (e *RevertError) IsPrecompileFailure() bool {
	return strings.HasPrefix(e.Name, "PrecompileLib__")
}

// DecodeRevert builds a RevertError from revert data, naming custom errors
// through names (may be nil).
func DecodeRevert(data []byte, names map[Selector]string) *RevertError {
	e := &RevertError{Data: data}
	if len(data) < 4 {
		return e
	}
	copy(e.Selector[:], data[:4])
	body := Words(data[4:])
	switch e.Selector {
	case SelErrorString:
		e.Name = "Error(string)"
		if s, err := body.String(0); err == nil {
			e.Reason = s
		}
	case SelPanic:
		e.Name = "Panic(uint256)"
		if v, err := body.Uint64(0); err == nil {
			e.Panic, e.HasPanic = v, true
		}
	default:
		e.Name = names[e.Selector]
	}
	return e
}

// AsRevert unwraps a *RevertError.
func AsRevert(err error) (*RevertError, bool) {
	var r *RevertError
	ok := errors.As(err, &r)
	return r, ok
}
