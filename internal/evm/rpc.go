package evm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Caller is the read surface the vault code needs. *Client implements it;
// tests substitute fixtures.
type Caller interface {
	// Call runs eth_call against "latest" and returns the raw result.
	Call(ctx context.Context, to Address, data []byte) ([]byte, error)
	// BatchCall runs several eth_calls in one JSON-RPC batch. The result has
	// one entry per message, in order; per-call failures (reverts) are in
	// CallResult.Err and do not fail the batch.
	BatchCall(ctx context.Context, msgs []CallMsg) ([]CallResult, error)
	// GetCode returns the runtime bytecode at addr ("latest").
	GetCode(ctx context.Context, addr Address) ([]byte, error)
}

// CallMsg is one eth_call.
type CallMsg struct {
	To   Address
	Data []byte
}

// CallResult is one eth_call outcome.
type CallResult struct {
	Data []byte
	Err  error
}

// Options configure a Client.
type Options struct {
	// Endpoints are tried in order; the last one that worked is tried first
	// next time.
	Endpoints []string
	// ChainID, when non-zero, is checked once per endpoint with eth_chainId;
	// an endpoint on another chain is never used.
	ChainID uint64
	// HTTP defaults to a client with Timeout.
	HTTP *http.Client
	// Timeout per request (default 10 s).
	Timeout time.Duration
	// MinInterval spaces requests from this client (default 150 ms). Public
	// RPCs rate-limit hard; batches keep the request count low.
	MinInterval time.Duration
	// MaxBatch caps calls per JSON-RPC batch (default 20). It shrinks on
	// its own when a provider refuses a batch for size.
	MaxBatch int
	// Multicall is the Multicall3 address BatchCall aggregates through
	// (default evm.Multicall3). NoMulticall disables it.
	Multicall   Address
	NoMulticall bool
	// MaxMulticall caps calls per aggregate3 (default 64).
	MaxMulticall int
	// UserAgent is sent on every request.
	UserAgent string
	// Errors names custom-error selectors in RevertError.
	Errors map[Selector]string
	// StateFile, when set, remembers the endpoint that last answered across
	// processes (<CacheDir>/evm-rpc-<chain>.json). A remembered endpoint
	// that is still configured is tried first for StateTTL (default 24 h),
	// so a cold run does not pay for a broken endpoint listed before it.
	StateFile string
	StateTTL  time.Duration
	// Now defaults to time.Now (state-file age).
	Now func() time.Time
}

// Failover is one endpoint that failed during a call that another endpoint
// then answered.
type Failover struct {
	Endpoint string `json:"endpoint"`
	Reason   string `json:"reason"`
	Used     string `json:"used"`
}

// Client is a read-only JSON-RPC client with endpoint failover. It is safe
// for concurrent use.
type Client struct {
	o    Options
	http *http.Client

	mu       sync.Mutex
	good     int             // index of the endpoint that last worked
	verified map[string]bool // chain id checked
	last     time.Time       // last request start (rate spacing)
	codes    map[Address][]byte
	nextID   int
	mcBroken bool       // multicall failed once: use JSON-RPC batches
	saved    string     // endpoint recorded in the state file
	savedAt  time.Time  // when it was recorded
	fails    []Failover // since the last TakeFailovers
}

var _ Caller = (*Client)(nil)

// NewClient validates o and returns a client. It does not touch the network.
func NewClient(o Options) (*Client, error) {
	var eps []string
	for _, e := range o.Endpoints {
		if e = strings.TrimSpace(e); e != "" {
			eps = append(eps, e)
		}
	}
	if len(eps) == 0 {
		return nil, ErrNoEndpoints
	}
	o.Endpoints = eps
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.MinInterval == 0 {
		o.MinInterval = 150 * time.Millisecond
	}
	if o.MaxBatch <= 0 {
		o.MaxBatch = 20
	}
	if o.Multicall.IsZero() {
		o.Multicall = Multicall3
	}
	if o.MaxMulticall <= 0 {
		o.MaxMulticall = 64
	}
	if o.StateTTL <= 0 {
		o.StateTTL = 24 * time.Hour
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	h := o.HTTP
	if h == nil {
		h = &http.Client{Timeout: o.Timeout}
	}
	c := &Client{o: o, http: h, verified: map[string]bool{}, codes: map[Address][]byte{}}
	c.loadState()
	return c, nil
}

// rpcState is the StateFile body.
type rpcState struct {
	ChainID  uint64 `json:"chainId,omitempty"`
	Endpoint string `json:"endpoint"`
	At       int64  `json:"at"` // ms
}

// loadState starts at the remembered endpoint when it is fresh and still
// configured. A missing or unreadable file is ignored.
func (c *Client) loadState() {
	if c.o.StateFile == "" {
		return
	}
	b, err := os.ReadFile(c.o.StateFile)
	if err != nil {
		return
	}
	var st rpcState
	if json.Unmarshal(b, &st) != nil || st.Endpoint == "" || (c.o.ChainID != 0 && st.ChainID != 0 && st.ChainID != c.o.ChainID) {
		return
	}
	if c.o.Now().Sub(time.UnixMilli(st.At)) > c.o.StateTTL {
		return
	}
	for i, ep := range c.o.Endpoints {
		if sameEndpoint(ep, st.Endpoint) {
			c.good, c.saved, c.savedAt = i, ep, time.UnixMilli(st.At)
			return
		}
	}
}

// saveState records ep when it differs from the remembered endpoint or the
// record is older than half the TTL. Errors are ignored: the state file is
// an optimization.
func (c *Client) saveState(ep string) {
	if c.o.StateFile == "" {
		return
	}
	now := c.o.Now()
	c.mu.Lock()
	if c.saved == ep && now.Sub(c.savedAt) < c.o.StateTTL/2 {
		c.mu.Unlock()
		return
	}
	c.saved, c.savedAt = ep, now
	c.mu.Unlock()
	b, err := json.Marshal(rpcState{ChainID: c.o.ChainID, Endpoint: ep, At: now.UnixMilli()})
	if err != nil {
		return
	}
	dir := filepath.Dir(c.o.StateFile)
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".evm-rpc-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), c.o.StateFile) != nil {
		_ = os.Remove(tmp.Name())
	}
}

func sameEndpoint(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(strings.TrimSpace(a), "/"), strings.TrimRight(strings.TrimSpace(b), "/"))
}

// TakeFailovers returns the endpoints that failed before another answered
// since the last call, and clears the list.
func (c *Client) TakeFailovers() []Failover {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.fails
	c.fails = nil
	return out
}

// failReason is a short reason for a failover warning.
func failReason(err error) string {
	var te *transportError
	if errors.As(err, &te) {
		err = te.err
	}
	var cm *ChainMismatchError
	if errors.As(err, &cm) {
		return fmt.Sprintf("chain %d, want %d", cm.Got, cm.Want)
	}
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "tls") || strings.Contains(low, "x509") || strings.Contains(low, "handshake"):
		return "TLS error"
	case strings.Contains(low, "timeout") || strings.Contains(low, "deadline exceeded"):
		return "timeout"
	case strings.Contains(low, "connection refused"):
		return "connection refused"
	case strings.Contains(low, "no such host"):
		return "DNS lookup failed"
	}
	return truncate(msg, 80)
}

// Endpoint is the endpoint that last answered (or the first configured).
func (c *Client) Endpoint() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.o.Endpoints[c.good]
}

type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcErrObj struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type rpcResp struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcErrObj      `json:"error"`
}

// transportError marks failures that should move on to the next endpoint
// (network, TLS, non-JSON body, HTTP 429/5xx).
type transportError struct {
	endpoint string
	err      error
}

func (e *transportError) Error() string { return e.endpoint + ": " + e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

// pace enforces MinInterval between requests.
func (c *Client) pace(ctx context.Context) error {
	if c.o.MinInterval < 0 {
		return nil
	}
	c.mu.Lock()
	wait := time.Until(c.last.Add(c.o.MinInterval))
	if wait < 0 {
		wait = 0
	}
	c.last = time.Now().Add(wait)
	c.mu.Unlock()
	if wait == 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) ids(n int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID + 1
	c.nextID += n
	return id
}

// post sends body to one endpoint and returns the raw JSON response.
func (c *Client) post(ctx context.Context, ep string, body []byte) (json.RawMessage, error) {
	if err := c.pace(ctx); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.o.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, bytes.NewReader(body))
	if err != nil {
		return nil, &transportError{ep, err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.o.UserAgent != "" {
		req.Header.Set("User-Agent", c.o.UserAgent)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &transportError{ep, err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, &transportError{ep, err}
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, &transportError{ep, fmt.Errorf("HTTP %d", resp.StatusCode)}
	}
	trim := bytes.TrimSpace(raw)
	if len(trim) == 0 || (trim[0] != '{' && trim[0] != '[') || !json.Valid(trim) {
		return nil, &transportError{ep, fmt.Errorf("HTTP %d: non-JSON response (%d bytes)", resp.StatusCode, len(raw))}
	}
	return trim, nil
}

// verifyChain checks eth_chainId once per endpoint.
func (c *Client) verifyChain(ctx context.Context, ep string) error {
	if c.o.ChainID == 0 {
		return nil
	}
	c.mu.Lock()
	ok := c.verified[ep]
	c.mu.Unlock()
	if ok {
		return nil
	}
	body, _ := json.Marshal(rpcReq{JSONRPC: "2.0", ID: c.ids(1), Method: "eth_chainId", Params: []any{}})
	raw, err := c.post(ctx, ep, body)
	if err != nil {
		return err
	}
	var r rpcResp
	if err := json.Unmarshal(raw, &r); err != nil || r.Error != nil {
		return &transportError{ep, errors.New("eth_chainId failed")}
	}
	got, err := hexQuantity(r.Result)
	if err != nil {
		return &transportError{ep, err}
	}
	if got != c.o.ChainID {
		return &ChainMismatchError{Endpoint: ep, Want: c.o.ChainID, Got: got}
	}
	c.mu.Lock()
	c.verified[ep] = true
	c.mu.Unlock()
	return nil
}

// do sends one request or batch with failover. retryable, when set, may ask
// to try the next endpoint after a successful round trip (precompile
// failures).
func (c *Client) do(ctx context.Context, body []byte, retryable func(json.RawMessage) bool) (json.RawMessage, error) {
	c.mu.Lock()
	start := c.good
	n := len(c.o.Endpoints)
	c.mu.Unlock()
	var errs []error
	var failed []Failover
	var lastRaw json.RawMessage
	answered := func(ep string) {
		c.mu.Lock()
		for _, f := range failed {
			f.Used = ep
			c.fails = append(c.fails, f)
		}
		if len(c.fails) > 32 {
			c.fails = c.fails[len(c.fails)-32:]
		}
		c.mu.Unlock()
		failed = nil
		c.saveState(ep)
	}
	for k := 0; k < n; k++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		i := (start + k) % n
		ep := c.o.Endpoints[i]
		if err := c.verifyChain(ctx, ep); err != nil {
			errs = append(errs, err)
			if ctx.Err() == nil {
				failed = append(failed, Failover{Endpoint: ep, Reason: failReason(err)})
			}
			continue
		}
		raw, err := c.post(ctx, ep, body)
		if err != nil {
			errs = append(errs, err)
			if ctx.Err() == nil {
				failed = append(failed, Failover{Endpoint: ep, Reason: failReason(err)})
			}
			continue
		}
		c.mu.Lock()
		c.good = i
		c.mu.Unlock()
		answered(ep)
		if retryable != nil && retryable(raw) && k < n-1 {
			lastRaw = raw
			continue
		}
		return raw, nil
	}
	if lastRaw != nil {
		return lastRaw, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, fmt.Errorf("all HyperEVM RPC endpoints failed: %w", errors.Join(errs...))
}

func (c *Client) single(ctx context.Context, method string, params []any, retry func(*rpcResp) bool) (*rpcResp, error) {
	body, err := json.Marshal(rpcReq{JSONRPC: "2.0", ID: c.ids(1), Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	var rf func(json.RawMessage) bool
	if retry != nil {
		rf = func(raw json.RawMessage) bool {
			var r rpcResp
			return json.Unmarshal(raw, &r) == nil && retry(&r)
		}
	}
	raw, err := c.do(ctx, body, rf)
	if err != nil {
		return nil, err
	}
	var r rpcResp
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("%s: bad response: %w", method, err)
	}
	return &r, nil
}

// ChainID returns eth_chainId.
func (c *Client) ChainID(ctx context.Context) (uint64, error) {
	r, err := c.single(ctx, "eth_chainId", []any{}, nil)
	if err != nil {
		return 0, err
	}
	if r.Error != nil {
		return 0, c.rpcErr(r.Error)
	}
	return hexQuantity(r.Result)
}

// BlockNumber returns eth_blockNumber.
func (c *Client) BlockNumber(ctx context.Context) (uint64, error) {
	r, err := c.single(ctx, "eth_blockNumber", []any{}, nil)
	if err != nil {
		return 0, err
	}
	if r.Error != nil {
		return 0, c.rpcErr(r.Error)
	}
	return hexQuantity(r.Result)
}

// GetCode returns the runtime code at addr; results are cached for the
// client's lifetime (code does not change for these contracts).
func (c *Client) GetCode(ctx context.Context, addr Address) ([]byte, error) {
	c.mu.Lock()
	if b, ok := c.codes[addr]; ok {
		c.mu.Unlock()
		return b, nil
	}
	c.mu.Unlock()
	r, err := c.single(ctx, "eth_getCode", []any{addr.Hex(), "latest"}, nil)
	if err != nil {
		return nil, err
	}
	if r.Error != nil {
		return nil, c.rpcErr(r.Error)
	}
	b, err := hexData(r.Result)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.codes[addr] = b
	c.mu.Unlock()
	return b, nil
}

func callParams(m CallMsg) []any {
	return []any{map[string]string{"to": m.To.Hex(), "data": EncodeHex(m.Data)}, "latest"}
}

// Call runs eth_call at "latest" (precompile-backed views revert at pinned
// blocks). A revert returns *RevertError; a precompile revert is retried on
// the next endpoint first.
func (c *Client) Call(ctx context.Context, to Address, data []byte) ([]byte, error) {
	r, err := c.single(ctx, "eth_call", callParams(CallMsg{To: to, Data: data}), func(r *rpcResp) bool {
		return c.isPrecompileFailure(r)
	})
	if err != nil {
		return nil, err
	}
	return c.callResult(r)
}

func (c *Client) isPrecompileFailure(r *rpcResp) bool {
	if r.Error == nil {
		return false
	}
	re, ok := c.rpcErr(r.Error).(*RevertError)
	return ok && re.IsPrecompileFailure()
}

func (c *Client) callResult(r *rpcResp) ([]byte, error) {
	if r.Error != nil {
		return nil, c.rpcErr(r.Error)
	}
	b, err := hexData(r.Result)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, ErrEmptyResult
	}
	return b, nil
}

// BatchCall runs calls through Multicall3.aggregate3 (one eth_call per
// MaxMulticall calls). If multicall is unavailable it falls back to JSON-RPC
// batches of at most MaxBatch (shrinking when the provider refuses a size),
// and to one request per call when batches are not supported at all.
func (c *Client) BatchCall(ctx context.Context, msgs []CallMsg) ([]CallResult, error) {
	out := make([]CallResult, len(msgs))
	if len(msgs) == 0 {
		return out, nil
	}
	if len(msgs) > 1 && !c.o.NoMulticall && !c.multicallBroken() {
		ok := true
		for lo := 0; lo < len(msgs) && ok; lo += c.o.MaxMulticall {
			hi := min(lo+c.o.MaxMulticall, len(msgs))
			err := c.multicall(ctx, msgs[lo:hi], out[lo:hi])
			switch {
			case err == nil:
			case errors.Is(err, errMulticallUnavailable):
				ok = false
			default:
				return nil, err
			}
		}
		if ok {
			return out, nil
		}
	}
	for lo := 0; lo < len(msgs); {
		c.mu.Lock()
		size := c.o.MaxBatch
		c.mu.Unlock()
		hi := min(lo+size, len(msgs))
		err := c.batchChunk(ctx, msgs[lo:hi], out[lo:hi])
		var bl *errBatchLimited
		if errors.As(err, &bl) && bl.max < size {
			c.mu.Lock()
			c.o.MaxBatch = bl.max
			c.mu.Unlock()
			continue // retry this chunk smaller
		}
		if err != nil {
			return nil, err
		}
		lo = hi
	}
	return out, nil
}

func (c *Client) multicallBroken() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mcBroken
}

// multicall runs one aggregate3. A revert, empty result or undecodable
// answer marks multicall unavailable for this client.
func (c *Client) multicall(ctx context.Context, msgs []CallMsg, out []CallResult) error {
	b, err := c.Call(ctx, c.o.Multicall, EncodeAggregate3(msgs))
	if err == nil {
		var res []CallResult
		if res, err = DecodeAggregate3(b, len(msgs), c.o.Errors); err == nil {
			copy(out, res)
			return nil
		}
	}
	if !isCallLevel(err) {
		var de *DecodeError
		if !errors.As(err, &de) {
			return err
		}
	}
	c.mu.Lock()
	c.mcBroken = true
	c.mu.Unlock()
	return errMulticallUnavailable
}

func (c *Client) batchChunk(ctx context.Context, msgs []CallMsg, out []CallResult) error {
	if len(msgs) == 1 {
		b, err := c.Call(ctx, msgs[0].To, msgs[0].Data)
		if err != nil && !isCallLevel(err) {
			return err
		}
		out[0] = CallResult{Data: b, Err: err}
		return nil
	}
	base := c.ids(len(msgs))
	reqs := make([]rpcReq, len(msgs))
	for i, m := range msgs {
		reqs[i] = rpcReq{JSONRPC: "2.0", ID: base + i, Method: "eth_call", Params: callParams(m)}
	}
	body, err := json.Marshal(reqs)
	if err != nil {
		return err
	}
	raw, err := c.do(ctx, body, func(raw json.RawMessage) bool {
		var rs []rpcResp
		if json.Unmarshal(raw, &rs) != nil {
			return false
		}
		for i := range rs {
			if c.isPrecompileFailure(&rs[i]) {
				return true
			}
		}
		return false
	})
	if err != nil {
		return err
	}
	var rs []rpcResp
	if raw[0] != '[' || json.Unmarshal(raw, &rs) != nil {
		// batch not supported here: one by one
		for i, m := range msgs {
			b, err := c.Call(ctx, m.To, m.Data)
			if err != nil && !isCallLevel(err) {
				return err
			}
			out[i] = CallResult{Data: b, Err: err}
		}
		return nil
	}
	if lim := batchLimit(rs); lim > 0 {
		return &errBatchLimited{max: lim}
	}
	byID := make(map[int]*rpcResp, len(rs))
	for i := range rs {
		id, err := strconv.Atoi(strings.Trim(string(rs[i].ID), `"`))
		if err == nil {
			byID[id] = &rs[i]
		}
	}
	for i := range msgs {
		r, ok := byID[base+i]
		if !ok {
			out[i] = CallResult{Err: errors.New("missing response in batch")}
			continue
		}
		b, err := c.callResult(r)
		out[i] = CallResult{Data: b, Err: err}
	}
	return nil
}

// batchLimit detects a provider refusing the batch for its size (every
// entry a non-revert error mentioning "batch") and returns the size to use.
func batchLimit(rs []rpcResp) int {
	if len(rs) == 0 {
		return 0
	}
	for _, r := range rs {
		if r.Error == nil || r.Error.Code == 3 || !strings.Contains(strings.ToLower(r.Error.Message), "batch") {
			return 0
		}
	}
	msg := strings.ToLower(rs[0].Error.Message)
	if i := strings.Index(msg, "more than "); i >= 0 {
		f := strings.Fields(msg[i+len("more than "):])
		if len(f) > 0 {
			if n, err := strconv.Atoi(f[0]); err == nil && n >= 1 {
				return n
			}
		}
	}
	return 1
}

// isCallLevel reports errors that belong to one call rather than the
// transport (reverts, empty results, other JSON-RPC errors).
func isCallLevel(err error) bool {
	var re *RevertError
	var rpc *RPCError
	return errors.As(err, &re) || errors.As(err, &rpc) || errors.Is(err, ErrEmptyResult)
}

// rpcErr maps a JSON-RPC error object to *RevertError or *RPCError.
func (c *Client) rpcErr(e *rpcErrObj) error {
	var data []byte
	if len(e.Data) > 0 {
		var s string
		if json.Unmarshal(e.Data, &s) == nil {
			data, _ = DecodeHex(s)
		}
	}
	msg := strings.ToLower(e.Message)
	if e.Code == 3 || strings.Contains(msg, "revert") {
		return DecodeRevert(data, c.o.Errors)
	}
	return &RPCError{Code: e.Code, Message: e.Message, Data: data}
}

func hexQuantity(raw json.RawMessage) (uint64, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, fmt.Errorf("bad quantity %s", truncate(string(raw), 40))
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if s == "" {
		return 0, errors.New("empty quantity")
	}
	return strconv.ParseUint(s, 16, 64)
}

func hexData(raw json.RawMessage) ([]byte, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("bad data %s", truncate(string(raw), 40))
	}
	return DecodeHex(s)
}
