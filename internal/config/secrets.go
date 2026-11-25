package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/zalando/go-keyring"
)

// Secret is a string that never prints. Use Reveal() at the single point of use.
type Secret struct{ v string }

// NewSecret wraps a value.
func NewSecret(v string) Secret { return Secret{v} }

// Reveal returns the raw value. Call it only where the value is consumed
// (signing, an Authorization header). Never pass the result to a logger.
func (s Secret) Reveal() string { return s.v }

// Empty reports whether no value is set.
func (s Secret) Empty() bool { return s.v == "" }

// String implements fmt.Stringer with redaction.
func (s Secret) String() string {
	if s.v == "" {
		return "<unset>"
	}
	return "<redacted>"
}

// GoString implements fmt.GoStringer so %#v redacts too.
func (s Secret) GoString() string { return s.String() }

// Format implements fmt.Formatter so %v, %+v, %#v, %s, %q all redact.
func (s Secret) Format(f fmt.State, _ rune) { _, _ = fmt.Fprint(f, s.String()) }

// MarshalText redacts in JSON/TOML/YAML output.
func (s Secret) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// Well-known secret names.
const (
	SecretAgentKey     = "agent-key"     // API (agent) wallet private key; can trade, cannot withdraw
	SecretAnthropicKey = "anthropic-key" // only if the user opts in; env ANTHROPIC_API_KEY wins
	SecretOpenAIKey    = "openai-key"    // env OPENAI_API_KEY wins
)

// KeychainService is the OS keychain service name hlp stores secrets under.
const KeychainService = "hlp"

// secretEnv maps well-known secret names to the env var that overrides them.
var secretEnv = map[string]string{
	SecretAgentKey:     "HLP_AGENT_KEY",
	SecretAnthropicKey: "ANTHROPIC_API_KEY",
	SecretOpenAIKey:    "OPENAI_API_KEY",
}

// EnvFor returns the environment variable consulted for a well-known secret
// name (agent-key -> HLP_AGENT_KEY). Any other name has no env var (""):
// Secrets.Get never resolves an arbitrary environment variable, so a name
// that arrives from a file cannot be turned into a read of HLP_AGENT_KEY,
// AWS_SECRET_ACCESS_KEY or similar (audit S1). The LLM api_key_env path reads
// the environment itself, after CheckLLMKeyEnv.
func EnvFor(name string) string {
	return secretEnv[name]
}

// llmKeyEnvRe is the shape of an env var that may hold an LLM provider key:
// upper case, ending in _API_KEY (ANTHROPIC_API_KEY, OPENAI_API_KEY,
// GROQ_API_KEY, OPENROUTER_API_KEY, ...).
var llmKeyEnvRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*_API_KEY$`)

// tradingSecretMarkers are substrings of env var names that hold wallet or
// signing material; such a variable is never sent to an LLM endpoint.
var tradingSecretMarkers = []string{"AGENT", "PRIVATE", "WALLET", "MNEMONIC", "SEED", "SIGNER", "SECRET"}

// CheckLLMKeyEnv reports whether name may be used as llm.api_key_env, the
// env var whose value is sent to the model endpoint as a bearer token or
// x-api-key. It allows only provider-key shaped names (*_API_KEY) and
// hard-blocks HLP_AGENT_KEY, every HLP_* variable and anything that names a
// wallet or trading secret (audit S1). An empty name is allowed (unset).
func CheckLLMKeyEnv(name string) error {
	n := strings.TrimSpace(name)
	if n == "" {
		return nil
	}
	u := strings.ToUpper(n)
	if u == secretEnv[SecretAgentKey] || strings.HasPrefix(u, "HLP_") {
		return fmt.Errorf("%q must not name the trading key or another hlp secret", n)
	}
	for _, m := range tradingSecretMarkers {
		if strings.Contains(u, m) {
			return fmt.Errorf("%q looks like a wallet or trading secret; only LLM provider keys may be sent to a model endpoint", n)
		}
	}
	if !llmKeyEnvRe.MatchString(n) {
		return fmt.Errorf("%q must be an LLM provider key variable ending in _API_KEY (e.g. ANTHROPIC_API_KEY, GROQ_API_KEY)", n)
	}
	return nil
}

// Secrets resolves secrets. Lookup order: environment variable
// (HLP_AGENT_KEY, ANTHROPIC_API_KEY, OPENAI_API_KEY; only well-known names),
// then the OS keychain (service "hlp"). The master wallet key is NEVER stored.
type Secrets interface {
	Get(name string) (Secret, error) // returns ErrSecretNotFound when absent
	Set(name string, value Secret) error
	Delete(name string) error
}

// ErrSecretNotFound is returned when no env var or keychain entry exists.
var ErrSecretNotFound = fmt.Errorf("secret not found")

// ErrKeychainUnavailable wraps keychain failures other than "not found" (no
// Secret Service on a headless Linux box, a locked keychain).
var ErrKeychainUnavailable = errors.New("OS keychain unavailable")

// keychain is the subset of go-keyring hlp uses; tests swap in a fake.
type keychain interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

type systemKeychain struct{}

func (systemKeychain) Get(s, u string) (string, error) { return keyring.Get(s, u) }
func (systemKeychain) Set(s, u, p string) error        { return keyring.Set(s, u, p) }
func (systemKeychain) Delete(s, u string) error        { return keyring.Delete(s, u) }

// NewSecrets returns the env+keychain implementation (zalando/go-keyring).
// On systems without a keychain, Set returns an error explaining how to use
// the env var instead; it never falls back to a plaintext file.
func NewSecrets() Secrets { return newSecrets(os.Getenv, systemKeychain{}) }

func newSecrets(getenv func(string) string, kc keychain) *secrets {
	return &secrets{getenv: getenv, kc: kc}
}

type secrets struct {
	getenv func(string) string
	kc     keychain
}

func checkName(name string) error {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\x00\n\r") {
		return fmt.Errorf("invalid secret name %q", name)
	}
	return nil
}

// Get returns the env value if set, else the keychain entry.
func (s *secrets) Get(name string) (Secret, error) {
	if err := checkName(name); err != nil {
		return Secret{}, err
	}
	if env := EnvFor(name); env != "" {
		if v := strings.TrimSpace(s.getenv(env)); v != "" {
			return NewSecret(v), nil
		}
	}
	v, err := s.kc.Get(KeychainService, name)
	switch {
	case err == nil && v != "":
		return NewSecret(v), nil
	case err == nil, errors.Is(err, keyring.ErrNotFound):
		return Secret{}, fmt.Errorf("%w: %s%s", ErrSecretNotFound, name, envHint(name))
	default:
		// The keychain's error text never contains the secret; keep it for
		// diagnosis but classify it as not found for callers.
		return Secret{}, fmt.Errorf("%w: %s (%w: %v)%s", ErrSecretNotFound, name, ErrKeychainUnavailable, err, envHint(name))
	}
}

// Set stores value in the OS keychain. It refuses empty values and never
// writes to disk itself.
func (s *secrets) Set(name string, value Secret) error {
	if err := checkName(name); err != nil {
		return err
	}
	if value.Empty() {
		return fmt.Errorf("refusing to store an empty %s", name)
	}
	if err := s.kc.Set(KeychainService, name, value.Reveal()); err != nil {
		if errors.Is(err, keyring.ErrSetDataTooBig) {
			return fmt.Errorf("%s is too large for the OS keychain", name)
		}
		return fmt.Errorf("%w: cannot store %s (%v)%s", ErrKeychainUnavailable, name, err, envHint(name))
	}
	return nil
}

// Delete removes the keychain entry. Env vars are left alone (hlp does not own
// the environment).
func (s *secrets) Delete(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	err := s.kc.Delete(KeychainService, name)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, keyring.ErrNotFound):
		return fmt.Errorf("%w: %s", ErrSecretNotFound, name)
	default:
		return fmt.Errorf("%w: cannot delete %s (%v)", ErrKeychainUnavailable, name, err)
	}
}

func envHint(name string) string {
	if env := EnvFor(name); env != "" {
		return "; set " + env + " instead"
	}
	return ""
}
