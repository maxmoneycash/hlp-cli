package llm

// Regression tests for the transport's redirect and credential checks. Each
// one fails if the check it pins is deleted. They call the redirect policy
// directly: no network, no key.

import (
	"net/http"
	"testing"
	"time"
)

func upReq(t *testing.T, u string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A same-host https -> http redirect is refused, so x-api-key and the
// prompt are never replayed in cleartext.
func TestRegressRedirectNoSchemeDowngrade(t *testing.T) {
	orig := upReq(t, "https://api.example.com/v1/messages")
	for _, to := range []string{
		"http://api.example.com/v1/messages",
		"HTTP://api.example.com/v1/messages",
		"http://api.example.com:443/v1/messages",
	} {
		if err := checkRedirect(upReq(t, to), []*http.Request{orig}); err == nil {
			t.Errorf("redirect https -> %s allowed", to)
		}
	}
	// the same origin is still fine
	if err := checkRedirect(upReq(t, "https://api.example.com/v1/other"), []*http.Request{orig}); err != nil {
		t.Fatalf("same-origin redirect refused: %v", err)
	}
}

// Redirects stop after maxRedirects, like net/http's default.
func TestRegressRedirectLimit(t *testing.T) {
	orig := upReq(t, "https://api.example.com/v1/messages")
	via := make([]*http.Request, maxRedirects)
	for i := range via {
		via[i] = orig
	}
	if err := checkRedirect(upReq(t, "https://api.example.com/v1/again"), via); err == nil {
		t.Fatalf("redirect %d allowed", maxRedirects+1)
	}
	if err := checkRedirect(upReq(t, "https://api.example.com/v1/again"), via[:maxRedirects-1]); err != nil {
		t.Fatalf("redirect %d refused: %v", maxRedirects, err)
	}
}

// A caller-supplied client without a redirect policy gets ours (on a
// copy, so the caller's client is not mutated).
func TestRegressSafeClientInstallsPolicy(t *testing.T) {
	hc := &http.Client{Timeout: 5 * time.Second}
	c := safeClient(hc)
	if c.CheckRedirect == nil {
		t.Fatal("caller client has no redirect policy")
	}
	if c.Timeout != hc.Timeout {
		t.Errorf("timeout lost: %v", c.Timeout)
	}
	if hc.CheckRedirect != nil {
		t.Error("caller client mutated")
	}
	orig := upReq(t, "https://api.example.com/v1")
	if err := c.CheckRedirect(upReq(t, "https://evil.example/v1"), []*http.Request{orig}); err == nil {
		t.Error("policy allows a cross-host redirect")
	}
	if c := safeClient(nil); c.CheckRedirect == nil {
		t.Error("nil client has no redirect policy")
	}
}

// Only loopback IP literals (and localhost) count as local, so a keyless
// remote endpoint never gets the "local" treatment.
func TestRegressIsLocalURLLoopbackOnly(t *testing.T) {
	for u, want := range map[string]bool{
		"http://localhost:11434":     true,
		"http://ollama.localhost/v1": true,
		"http://127.0.0.1:8080":      true,
		"http://127.1.2.3:8080":      true,
		"http://[::1]:8080":          true,
		"https://203.0.113.5/v1":     false,
		"http://10.0.0.5:11434":      false,
		"http://0.0.0.0:11434":       false,
		"http://[2001:db8::1]/v1":    false,
		"https://api.example.com":    false,
	} {
		if got := isLocalURL(u); got != want {
			t.Errorf("isLocalURL(%s) = %v, want %v", u, got, want)
		}
	}
}
