package config

import (
	"strings"
	"testing"
)

// Audit S1: llm.api_key_env may only name an LLM provider key.
func TestCheckLLMKeyEnv(t *testing.T) {
	for _, ok := range []string{"", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GROQ_API_KEY", "OPENROUTER_API_KEY", "MY_API_KEY"} {
		if err := CheckLLMKeyEnv(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"HLP_AGENT_KEY", "hlp_agent_key", " HLP_AGENT_KEY ", "HLP_ANYTHING_API_KEY",
		"AGENT_API_KEY", "WALLET_PRIVATE_KEY", "PRIVATE_API_KEY", "SEED_API_KEY", "MNEMONIC",
		"AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN", "GH_TOKEN", "HOME", "PATH", "anthropic_api_key",
	} {
		if err := CheckLLMKeyEnv(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestValidateRejectsTradingKeyAsLLMKey(t *testing.T) {
	for _, name := range []string{"HLP_AGENT_KEY", "AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN"} {
		cfg := Defaults()
		cfg.LLM.Provider = "openai-compatible"
		cfg.LLM.APIKeyEnv = name
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "llm.api_key_env:") {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

// Audit S7: remote LLM endpoints must use https; http only for localhost.
func TestCheckLLMBaseURL(t *testing.T) {
	for _, ok := range []string{
		"https://api.openai.com/v1", "http://localhost:11434/v1", "http://127.0.0.1:1234/v1",
		"http://[::1]:8080/v1", "http://ollama.localhost/v1",
	} {
		if err := CheckLLMBaseURL(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://evil.example/v1", "http://10.0.0.5:11434/v1", "http://localhost.evil.example/v1", "ftp://x", "localhost"} {
		if err := CheckLLMBaseURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	cfg := Defaults()
	cfg.LLM.Provider = "openai-compatible"
	cfg.LLM.BaseURL = "http://evil.example/v1"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "https") {
		t.Errorf("remote http base_url: %v", err)
	}
}

// Audit S1: Secrets.Get resolves only well-known names from the environment.
func TestSecretsGetIgnoresArbitraryEnv(t *testing.T) {
	env := map[string]string{"AWS_SECRET_ACCESS_KEY": "aws-secret", "GROQ_API_KEY": "groq"}
	s := newSecrets(func(k string) string { return env[k] }, &fakeKeychain{data: map[string]string{}})
	for name := range env {
		if v, err := s.Get(name); err == nil || !v.Empty() {
			t.Errorf("Get(%q) resolved an arbitrary env var", name)
		}
	}
}
