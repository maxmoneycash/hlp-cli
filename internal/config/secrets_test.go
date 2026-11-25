package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/zalando/go-keyring"
)

const testKey = "0x0123456789012345678901234567890123456789012345678901234567890123"

func TestSecretRedacts(t *testing.T) {
	s := NewSecret(testKey)
	type holder struct {
		Key Secret
		N   int
	}
	h := holder{Key: s, N: 1}
	outs := []string{
		fmt.Sprintf("%v %+v %#v %s %q %x %d", s, s, s, s, s, s, s),
		fmt.Sprintf("%v %+v %#v", h, h, h),
		fmt.Sprintf("%v", &h),
		fmt.Sprint(s), fmt.Sprintln(s), s.String(), s.GoString(),
	}
	b, _ := json.Marshal(h)
	outs = append(outs, string(b))
	tb, _ := toml.Marshal(h)
	outs = append(outs, string(tb))
	for _, o := range outs {
		if strings.Contains(o, "0123456789") {
			t.Errorf("secret leaked: %s", o)
		}
		if !strings.Contains(o, "redacted") {
			t.Errorf("no redaction marker: %s", o)
		}
	}
	if s.Reveal() != testKey || s.Empty() {
		t.Error("Reveal/Empty")
	}
	if (Secret{}).String() != "<unset>" || !(Secret{}).Empty() {
		t.Error("zero Secret")
	}
}

type fakeKeychain struct {
	data  map[string]string
	calls int
	err   error // forced error for every call
}

func (f *fakeKeychain) Get(s, u string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	v, ok := f.data[s+"/"+u]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (f *fakeKeychain) Set(s, u, p string) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	f.data[s+"/"+u] = p
	return nil
}

func (f *fakeKeychain) Delete(s, u string) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	if _, ok := f.data[s+"/"+u]; !ok {
		return keyring.ErrNotFound
	}
	delete(f.data, s+"/"+u)
	return nil
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestSecretsEnvFirstWithoutKeychain(t *testing.T) {
	kc := &fakeKeychain{data: map[string]string{}}
	env := map[string]string{"HLP_AGENT_KEY": testKey, "ANTHROPIC_API_KEY": "sk-ant-x", "OPENAI_API_KEY": "sk-oai", "GROQ_API_KEY": "gsk"}
	s := newSecrets(envOf(env), kc)
	for name, want := range map[string]string{SecretAgentKey: testKey, SecretAnthropicKey: "sk-ant-x", SecretOpenAIKey: "sk-oai"} {
		got, err := s.Get(name)
		if err != nil || got.Reveal() != want {
			t.Errorf("Get(%s) = %v, %v", name, got, err)
		}
	}
	if kc.calls != 0 {
		t.Errorf("keychain touched %d times while env was set", kc.calls)
	}
}

func TestSecretsKeychainFallbackAndLifecycle(t *testing.T) {
	kc := &fakeKeychain{data: map[string]string{}}
	s := newSecrets(noEnv, kc)
	if _, err := s.Get(SecretAgentKey); !errors.Is(err, ErrSecretNotFound) || !strings.Contains(err.Error(), "HLP_AGENT_KEY") {
		t.Errorf("missing: %v", err)
	}
	if err := s.Set(SecretAgentKey, NewSecret(testKey)); err != nil {
		t.Fatal(err)
	}
	if kc.data["hlp/agent-key"] != testKey {
		t.Error("not stored under service hlp")
	}
	if got, err := s.Get(SecretAgentKey); err != nil || got.Reveal() != testKey {
		t.Errorf("Get after Set = %v, %v", got, err)
	}
	if err := s.Delete(SecretAgentKey); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(SecretAgentKey); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("double delete: %v", err)
	}
	if err := s.Set(SecretAgentKey, Secret{}); err == nil {
		t.Error("stored an empty secret")
	}
	for _, bad := range []string{"", " ", "a\nb"} {
		if _, err := s.Get(bad); err == nil {
			t.Errorf("Get(%q) accepted", bad)
		}
		if err := s.Set(bad, NewSecret("x")); err == nil {
			t.Errorf("Set(%q) accepted", bad)
		}
		if err := s.Delete(bad); err == nil {
			t.Errorf("Delete(%q) accepted", bad)
		}
	}
}

func TestSecretsKeychainUnavailable(t *testing.T) {
	kc := &fakeKeychain{data: map[string]string{}, err: errors.New("dbus: no Secret Service")}
	s := newSecrets(noEnv, kc)
	_, err := s.Get(SecretAgentKey)
	if !errors.Is(err, ErrSecretNotFound) || !errors.Is(err, ErrKeychainUnavailable) {
		t.Errorf("Get: %v", err)
	}
	err = s.Set(SecretAgentKey, NewSecret(testKey))
	if !errors.Is(err, ErrKeychainUnavailable) || !strings.Contains(err.Error(), "HLP_AGENT_KEY") {
		t.Errorf("Set: %v", err)
	}
	if strings.Contains(err.Error(), "0123456789") {
		t.Error("secret in error")
	}
	if err := s.Delete(SecretAgentKey); !errors.Is(err, ErrKeychainUnavailable) {
		t.Errorf("Delete: %v", err)
	}
	kc.err = keyring.ErrSetDataTooBig
	if err := s.Set(SecretAgentKey, NewSecret(testKey)); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("too big: %v", err)
	}
}

func TestEnvFor(t *testing.T) {
	for name, want := range map[string]string{
		SecretAgentKey: "HLP_AGENT_KEY", SecretAnthropicKey: "ANTHROPIC_API_KEY", SecretOpenAIKey: "OPENAI_API_KEY",
		"GROQ_API_KEY": "", "HLP_AGENT_KEY": "", "AWS_SECRET_ACCESS_KEY": "", "some-other": "", "lower_case": "",
	} {
		if got := EnvFor(name); got != want {
			t.Errorf("EnvFor(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestNewSecretsType(t *testing.T) {
	if _, ok := NewSecrets().(*secrets); !ok {
		t.Error("NewSecrets should return the env+keychain implementation")
	}
}
