package exchange

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// liveSend is the Phase-2 compile-time gate. While it is
// false, Post NEVER opens a connection: it signs and returns the envelope
// with DryRun:true, whatever Gate says. The lead flips it only after the
// owner's testnet run of order, cancel, modify, updateLeverage and
// vaultTransfer.
const liveSend = false

// LiveSendCompiled reports whether this build can ever send to /exchange.
func LiveSendCompiled() bool { return liveSend }

// ErrNetworkMismatch: Gate.Mainnet disagrees with the exchange URL.
var ErrNetworkMismatch = errors.New("exchange: gate network does not match the exchange URL")

// ErrNoSigner: the client has no signer.
var ErrNoSigner = errors.New("exchange: no signer configured")

// Nonces issues strictly increasing ms nonces: max(now_ms, last+1). Nonces
// are per signer on Hyperliquid; use one Nonces (one Client) per agent key.
type Nonces struct {
	last atomic.Uint64
	now  func() time.Time
}

// NewNonces returns a nonce source on the given clock (nil = time.Now).
func NewNonces(now func() time.Time) *Nonces {
	if now == nil {
		now = time.Now
	}
	return &Nonces{now: now}
}

// Next returns the next nonce; safe for concurrent use.
func (n *Nonces) Next() uint64 {
	for {
		last := n.last.Load()
		next := uint64(n.now().UnixMilli())
		if next <= last {
			next = last + 1
		}
		if n.last.CompareAndSwap(last, next) {
			return next
		}
	}
}

// ItemStatus is one entry of response.data.statuses.
type ItemStatus struct {
	Resting *struct {
		OID   uint64 `json:"oid"`
		Cloid string `json:"cloid,omitempty"`
	} `json:"resting,omitempty"`
	Filled *struct {
		TotalSz string `json:"totalSz"`
		AvgPx   string `json:"avgPx"`
		OID     uint64 `json:"oid"`
	} `json:"filled,omitempty"`
	Error   string `json:"error,omitempty"`
	Success bool   `json:"success,omitempty"` // cancel: the bare string "success"
	Raw     string `json:"raw,omitempty"`     // any other bare string status
}

// UnmarshalJSON accepts both object statuses and bare strings ("success").
func (s *ItemStatus) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		if str == "success" {
			s.Success = true
		} else {
			s.Raw = str
		}
		return nil
	}
	type plain ItemStatus
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*s = ItemStatus(p)
	return nil
}

// OK reports whether the item succeeded (resting, filled or success).
func (s ItemStatus) OK() bool {
	return s.Error == "" && (s.Resting != nil || s.Filled != nil || s.Success)
}

// ParseResponse decodes an /exchange reply into r.Status, r.Response and
// r.Statuses. 5xx is an unknown outcome: the caller must check orderStatus
// (by cloid) instead of resending.
func ParseResponse(httpStatus int, body []byte, r *Result) error {
	if httpStatus >= 500 {
		r.Unknown = true
		return fmt.Errorf("exchange: HTTP %d after send: outcome unknown, do not resend", httpStatus)
	}
	var env struct {
		Status   string          `json:"status"`
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		if httpStatus != http.StatusOK {
			return fmt.Errorf("exchange: HTTP %d: %s", httpStatus, truncate(string(body)))
		}
		r.Unknown = true
		return fmt.Errorf("exchange: undecodable reply: %w", err)
	}
	r.Status = env.Status
	var resp any
	_ = json.Unmarshal(env.Response, &resp)
	r.Response = resp
	if env.Status != "ok" {
		msg, _ := resp.(string)
		if msg == "" {
			msg = truncate(string(env.Response))
		}
		return fmt.Errorf("exchange: %s", msg)
	}
	var data struct {
		Data struct {
			Statuses []ItemStatus `json:"statuses"`
		} `json:"data"`
	}
	if json.Unmarshal(env.Response, &data) == nil {
		r.Statuses = data.Data.Statuses
	}
	return nil
}

func truncate(s string) string {
	if len(s) > 512 {
		return s[:512] + "…"
	}
	return s
}

// DefaultExpiresAfter bounds how long a signed live envelope stays valid
// (audit S9): if the outcome of a send is unknown, the envelope cannot be
// replayed for the rest of the nonce window (about a day).
const DefaultExpiresAfter = 60 * time.Second

// HTTPClient signs actions and (only when compiled live and gated) posts
// them to /exchange.
type HTTPClient struct {
	url    string
	signer Signer
	nonces *Nonces
	http   *http.Client
	// ExpiresAfter, when > 0, adds expiresAfter = nonce + ExpiresAfter ms.
	// NewHTTPClient sets DefaultExpiresAfter; 0 signs without an expiry.
	ExpiresAfter time.Duration
}

var _ Client = (*HTTPClient)(nil)

// NewClient returns a Client for the exchange URL with the given signer.
func NewClient(url string, s Signer) Client { return NewHTTPClient(url, s, nil) }

// NewHTTPClient is NewClient with an explicit nonce source and the
// concrete type.
func NewHTTPClient(url string, s Signer, n *Nonces) *HTTPClient {
	if n == nil {
		n = NewNonces(nil)
	}
	return &HTTPClient{url: url, signer: s, nonces: n, http: &http.Client{Timeout: 10 * time.Second}, ExpiresAfter: DefaultExpiresAfter}
}

// vaultAgnostic actions are signed and posted with vaultAddress = nil
// (they concern the signer's own account, not a vault).
func vaultAgnostic(action any) bool {
	switch action.(type) {
	case VaultTransferAction, *VaultTransferAction:
		return true
	}
	return false
}

// Sign builds the signed envelope for action without sending it.
func (c *HTTPClient) Sign(action any, vault *string, mainnet bool) (Envelope, error) {
	return c.sign(action, vault, mainnet, false)
}

// SignExpired builds an envelope whose expiresAfter is already in the past
// (nonce - 1 ms). The exchange rejects it, so the envelope can be shown or
// logged without becoming a replayable order. Every dry run uses it.
func (c *HTTPClient) SignExpired(action any, vault *string, mainnet bool) (Envelope, error) {
	return c.sign(action, vault, mainnet, true)
}

func (c *HTTPClient) sign(action any, vault *string, mainnet, expired bool) (Envelope, error) {
	if c.signer == nil {
		return Envelope{}, ErrNoSigner
	}
	if vaultAgnostic(action) {
		vault = nil
	}
	if vault != nil {
		a, err := parseAddress(*vault)
		if err != nil {
			return Envelope{}, err
		}
		lower := fmt.Sprintf("0x%x", a)
		vault = &lower
	}
	nonce := c.nonces.Next()
	var exp *uint64
	switch {
	case expired:
		e := nonce - 1
		exp = &e
	case c.ExpiresAfter > 0:
		e := nonce + uint64(c.ExpiresAfter.Milliseconds())
		exp = &e
	}
	sig, err := c.signer.SignL1(action, nonce, vault, exp, mainnet)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{Action: action, Nonce: nonce, Signature: sig, VaultAddress: vault, ExpiresAfter: exp}, nil
}

// Post signs action and returns the envelope. In this build it NEVER
// sends: liveSend is false, so the result always has DryRun:true. When
// liveSend is enabled, it sends only if gate.Live is also true. Dry-run
// envelopes are signed already expired (see SignExpired).
func (c *HTTPClient) Post(ctx context.Context, action any, vault *string, gate Gate) (Result, error) {
	if strings.Contains(c.url, "hyperliquid") && testnetURL(c.url) == gate.Mainnet {
		return Result{}, ErrNetworkMismatch
	}
	if !liveSend || !gate.Live {
		// A dry run must never hand out a replayable signature: sign
		// with expiresAfter in the past so /exchange refuses it.
		env, err := c.SignExpired(action, vault, gate.Mainnet)
		if err != nil {
			return Result{}, err
		}
		return Result{DryRun: true, Envelope: env}, nil
	}
	env, err := c.Sign(action, vault, gate.Mainnet)
	if err != nil {
		return Result{}, err
	}
	return c.send(ctx, Result{DryRun: true, Envelope: env}) // reachable only when liveSend is flipped
}

// send posts the envelope. Unreachable while liveSend is false.
func (c *HTTPClient) send(ctx context.Context, res Result) (Result, error) {
	res.DryRun = false
	body, err := json.Marshal(res.Envelope)
	if err != nil {
		return res, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return res, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		res.Unknown = true // the request may have reached the server
		return res, fmt.Errorf("exchange: %w (outcome unknown, do not resend)", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		res.Unknown = true
		return res, err
	}
	return res, ParseResponse(resp.StatusCode, raw, &res)
}

func testnetURL(u string) bool { return strings.Contains(u, "testnet") }
