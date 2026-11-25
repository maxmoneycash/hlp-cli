package apps

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// ConfirmTTL is how long a confirm token lives.
const ConfirmTTL = 60 * time.Second

// maxTokens bounds the store; the oldest entries go first.
const maxTokens = 256

// Token errors. The confirm tool maps every one to the same refusal: the
// view must preview again.
var (
	ErrTokenUnknown = errors.New("confirm token unknown or already used")
	ErrTokenExpired = errors.New("confirm token expired")
	ErrTokenSession = errors.New("confirm token belongs to another session")
)

// Pending is what a token stands for: the preview_order arguments it was
// issued for, the intent hash of that preview, and its notional.
type Pending struct {
	Args        json.RawMessage
	Intent      string
	NotionalUSD float64
	Expires     time.Time

	session any
}

// Tokens is the single-use confirm token store:
// random 128-bit tokens, 60 s TTL, bound to the issuing session and to the
// intent of the preview they were issued for.
type Tokens struct {
	mu  sync.Mutex
	m   map[string]*Pending
	Now func() time.Time
}

// NewTokens returns an empty store on the wall clock.
func NewTokens() *Tokens { return &Tokens{m: map[string]*Pending{}, Now: time.Now} }

// Issue mints a token for a preview. session is any comparable identity of
// the MCP session (the *sdk.ServerSession); a token redeems only there.
func (t *Tokens) Issue(session any, p Pending) (string, time.Time, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", time.Time{}, err
	}
	tok := "hlpc_" + hex.EncodeToString(b[:])
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.Now()
	for k, v := range t.m {
		if !now.Before(v.Expires) {
			delete(t.m, k)
		}
	}
	for len(t.m) >= maxTokens {
		var oldest string
		for k, v := range t.m {
			if oldest == "" || v.Expires.Before(t.m[oldest].Expires) {
				oldest = k
			}
		}
		delete(t.m, oldest)
	}
	p.Expires = now.Add(ConfirmTTL)
	p.session = session
	t.m[tok] = &p
	return tok, p.Expires, nil
}

// Redeem consumes a token: any lookup, right or wrong, burns it.
func (t *Tokens) Redeem(session any, tok string) (Pending, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := strings.TrimSpace(tok)
	p, ok := t.m[key]
	if !ok {
		return Pending{}, ErrTokenUnknown
	}
	delete(t.m, key)
	if p.session != session {
		return Pending{}, ErrTokenSession
	}
	if !t.Now().Before(p.Expires) {
		return Pending{}, ErrTokenExpired
	}
	return *p, nil
}

// Len reports live entries (tests).
func (t *Tokens) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.m)
}

// IntentHash is the hash a confirm compares: the normalized preview_order
// arguments plus the fields of the derived order that must not change
// between preview and confirm (action, coin, side, vault, network).
func IntentHash(args json.RawMessage, action, coin, side, vault, network string) string {
	var norm any
	if err := json.Unmarshal(args, &norm); err != nil || norm == nil {
		norm = map[string]any{}
	}
	b, _ := json.Marshal(norm) // map keys sort: a canonical form
	h := sha256.New()
	for _, s := range []string{string(b), action, strings.ToUpper(coin), side, strings.ToLower(vault), network} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
