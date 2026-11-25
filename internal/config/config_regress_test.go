package config

// Regression tests for config validation checks; each fails if the check it
// pins is deleted.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A pasted key that happens to be a valid identifier is called out as a
// key, not as a badly named variable.
func TestRegressAPIKeyEnvLooksLikeKey(t *testing.T) {
	for _, v := range []string{
		"sk_ant_api03_abcdefghijklmnopqrstuvwxyz",
		"AbCdEfGhIjKlMnOpQrStUvWxYz0123456789",
	} {
		cfg := Defaults()
		cfg.LLM.APIKeyEnv = v
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "looks like a key") {
			t.Errorf("api_key_env %q: %v", v, err)
		}
	}
}

// Save creates the config directory private to the user (0700).
func TestRegressSaveCreatesPrivateDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	dir := filepath.Join(t.TempDir(), "hlp")
	if err := Save(filepath.Join(dir, "config.toml"), Defaults()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Fatalf("config dir mode %o, want 700", perm)
	}
}
