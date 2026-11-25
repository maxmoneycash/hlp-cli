package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/config"
)

const agentKeyValue = "0xdeadbeefTRADINGKEY0000000000000000000000000000000000000000000000"

// Audit S1: no config can make New send HLP_AGENT_KEY (or another secret
// env var) as the API key.
func TestNewRefusesTradingKeyEnv(t *testing.T) {
	t.Setenv("HLP_AGENT_KEY", agentKeyValue)
	t.Setenv("AWS_SECRET_ACCESS_KEY", agentKeyValue)
	t.Setenv("GITHUB_TOKEN", agentKeyValue)
	for _, env := range []string{"HLP_AGENT_KEY", "hlp_agent_key", "AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN"} {
		for _, prov := range []string{"openai-compatible", "anthropic", "ollama", "lmstudio"} {
			p, err := New(config.LLMConfig{Provider: prov, BaseURL: "https://evil.example/v1", Model: "m", APIKeyEnv: env}, nil)
			if err == nil || p != nil {
				t.Errorf("%s/%s: provider built: %v", prov, env, p)
			}
			if err != nil && strings.Contains(err.Error(), agentKeyValue) {
				t.Errorf("%s/%s: key value in error", prov, env)
			}
		}
	}
}

func TestLookupKeyHardBlocksTradingKey(t *testing.T) {
	t.Setenv("HLP_AGENT_KEY", agentKeyValue)
	t.Setenv("WALLET_PRIVATE_KEY", agentKeyValue)
	t.Setenv("GROQ_API_KEY", "gsk-ok")
	for _, env := range []string{"HLP_AGENT_KEY", "WALLET_PRIVATE_KEY"} {
		if k := lookupKey(env, "", "", nil); !k.Empty() {
			t.Errorf("lookupKey(%s) resolved the trading key", env)
		}
	}
	if k := lookupKey("GROQ_API_KEY", "", "", nil); k.Reveal() != "gsk-ok" {
		t.Errorf("provider key not resolved: %v", k)
	}
}

// Audit S7: remote plain-http endpoints are refused; localhost http is fine.
func TestNewRefusesRemoteHTTP(t *testing.T) {
	t.Setenv("MY_API_KEY", testKey)
	for _, prov := range []string{"openai-compatible", "anthropic", "ollama"} {
		if _, err := New(config.LLMConfig{Provider: prov, BaseURL: "http://evil.example/v1", Model: "m", APIKeyEnv: "MY_API_KEY"}, nil); err == nil || !strings.Contains(err.Error(), "https") {
			t.Errorf("%s remote http: %v", prov, err)
		}
	}
	if _, err := New(config.LLMConfig{Provider: "ollama", BaseURL: "http://localhost:11434/v1", Model: "m"}, nil); err != nil {
		t.Errorf("local ollama http: %v", err)
	}
}

// Audit S7: a cross-host redirect never carries x-api-key / Authorization
// (or the request body) to the other host.
func TestNoCredentialsOnCrossHostRedirect(t *testing.T) {
	var leaked atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Add(1)
		if r.Header.Get("x-api-key") != "" || r.Header.Get("Authorization") != "" {
			t.Error("credential forwarded to redirect target")
		}
		http.Error(w, "nope", 500)
	}))
	defer evil.Close()
	// 127.0.0.1 vs localhost are different hosts to net/http.
	evilURL := strings.Replace(evil.URL, "127.0.0.1", "localhost", 1)
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evilURL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redir.Close()

	a := NewAnthropic(AnthropicOptions{BaseURL: redir.URL, Model: "m", Key: config.NewSecret(testKey)})
	a.retry = fastRetry
	if _, err := a.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "hi"}}}, nil); err == nil {
		t.Error("anthropic: redirect followed without error")
	} else if strings.Contains(err.Error(), testKey) {
		t.Error("anthropic: key in error")
	}
	o := NewOpenAI(OpenAIOptions{BaseURL: redir.URL + "/v1", Model: "m", Key: config.NewSecret(testKey)})
	o.retry = fastRetry
	if _, err := o.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "hi"}}}, nil); err == nil {
		t.Error("openai: redirect followed without error")
	}
	if n := leaked.Load(); n != 0 {
		t.Errorf("redirect target reached %d times", n)
	}
	// A caller-supplied client is not mutated.
	hc := &http.Client{}
	_ = NewOpenAI(OpenAIOptions{HTTPClient: hc})
	if hc.CheckRedirect != nil {
		t.Error("caller's client mutated")
	}
}

func TestSameHostRedirectAllowed(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old/models" {
			http.Redirect(w, r, "/v1/models", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer srv.Close()
	models, err := listModels(context.Background(), safeClient(nil), srv.URL+"/old", config.Secret{})
	if err != nil || len(models) != 1 {
		t.Fatalf("same-host redirect: %v %v", models, err)
	}
}
