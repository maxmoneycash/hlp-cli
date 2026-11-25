package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Audit S14: a group- or world-writable config warns, and may not turn on
// trading or MCP trading.
func TestLoadWritableConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix permission bits")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	write := func(body string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	write("theme = \"hlp\"\n", 0o600)
	cfg, err := Load(path)
	if err != nil || len(cfg.Warnings) != 0 {
		t.Fatalf("0600: %v %v", cfg.Warnings, err)
	}
	write("theme = \"hlp\"\n", 0o664)
	cfg, err = Load(path)
	if err != nil || len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "group-writable") {
		t.Fatalf("0664 warning: %v %v", cfg.Warnings, err)
	}
	write("[mcp]\nallow_trading = true\n", 0o666)
	cfg, err = Load(path)
	if err == nil || !strings.Contains(err.Error(), "mcp.allow_trading") || cfg.MCP.AllowTrading {
		t.Fatalf("world-writable allow_trading: %v %+v", err, cfg.MCP)
	}
	write("[trading]\nenabled = true\nagent_address = \"0x1111111111111111111111111111111111111111\"\n", 0o620)
	cfg, err = Load(path)
	if err == nil || !strings.Contains(err.Error(), "trading.enabled") || cfg.Trading.Enabled {
		t.Fatalf("group-writable trading: %v", err)
	}
	for _, body := range []string{
		"[trading]\nallow_unattended = true\n",
		"[mcp]\nallow_unattended = true\n",
		"[strategy]\nallow_external_engines = true\n",
		"[pineforge]\naccept_license = true\n",
		"[llm]\nallow_remote_bots = true\n",
	} {
		write(body, 0o664)
		cfg, err = Load(path)
		if err == nil || cfg.Trading.AllowUnattended || cfg.MCP.AllowUnattended || cfg.Strategy.AllowExternalEngines || cfg.PineForge.AcceptLicense || cfg.LLM.AllowRemoteBots {
			t.Fatalf("group-writable %q: %v", body, err)
		}
	}
	write("[trading]\nenabled = true\nagent_address = \"0x1111111111111111111111111111111111111111\"\n", 0o600)
	if cfg, err = Load(path); err != nil || !cfg.Trading.Enabled {
		t.Fatalf("0600 trading: %v", err)
	}
}

// Audit S7 follow-up: http only for loopback on every configurable endpoint.
func TestRemoteEndpointsNeedHTTPS(t *testing.T) {
	for _, body := range []string{
		"[hlpxyz]\nbase_url = \"http://hlp.example\"\n",
		"[hyperevm.999]\nrpc = [\"http://rpc.example\"]\n",
		"[hyperevm.999]\nrpc = [\"https://rpc.example\"]\ninfo = \"http://info.example/info\"\n",
		"[llm]\nprovider = \"ollama\"\nbase_url = \"http://10.0.0.5:11434/v1\"\n",
	} {
		if _, err := Parse("t", []byte(body)); err == nil || !strings.Contains(err.Error(), "https") {
			t.Errorf("%s: %v", body, err)
		}
	}
	for _, body := range []string{
		"[hlpxyz]\nbase_url = \"http://localhost:3000\"\n",
		"[hyperevm.999]\nrpc = [\"http://127.0.0.1:8545\", \"https://rpc.hyperliquid.xyz/evm\"]\n",
		"[llm]\nprovider = \"ollama\"\nbase_url = \"http://localhost:11434/v1\"\n",
	} {
		if _, err := Parse("t", []byte(body)); err != nil {
			t.Errorf("%s: %v", body, err)
		}
	}
}
