package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

func noEnv(string) string { return "" }

func TestDefaultsValidate(t *testing.T) {
	if err := Defaults().Validate(); err != nil {
		t.Fatalf("Defaults() invalid: %v", err)
	}
}

func TestLoadMissingFileIsDefaults(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, Defaults()) {
		t.Fatalf("got %+v", cfg)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "sub", "config.toml")
	cfg := Defaults()
	cfg.Network = domain.Testnet
	cfg.Theme = "gruvbox"
	cfg.Account = "0xdfc24b077bc1425ad1dea75bcb6f8158e10df303"
	cfg.Watch = []string{"0x010461c14e146ac35fe42271bdc1134ee31c703a"}
	cfg.Pinned = []string{"BTC", "xyz:SP500"}
	cfg.Chart = ChartConfig{Style: "lines", Zoom: 4, Interval: domain.I4h, MAs: []int{9, 21, 200}, UTC: true}
	cfg.LLM = LLMConfig{Provider: "ollama", BaseURL: "http://localhost:11434/v1", Model: "qwen3", APIKeyEnv: "OLLAMA_API_KEY"}
	cfg.Risk.AllowedCoins = []string{"BTC", "ETH"}
	cfg.Risk.DeadManSeconds = 60
	cfg.MCP.AllowTrading = true
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, cfg) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, cfg)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("mode %v, want 0600", st.Mode().Perm())
		}
	}
	body, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(body), "# hlp configuration") {
		t.Errorf("missing header:\n%s", body)
	}
	// No temp files left behind.
	ents, _ := os.ReadDir(filepath.Dir(path))
	if len(ents) != 1 {
		t.Errorf("dir has %d entries, want 1", len(ents))
	}
}

func TestSaveRefusesInvalid(t *testing.T) {
	cfg := Defaults()
	cfg.Risk.MaxLeverage = 0
	path := filepath.Join(t.TempDir(), "c.toml")
	if err := Save(path, cfg); err == nil {
		t.Fatal("Save accepted an invalid config")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("invalid config was written")
	}
}

func TestPartialFileKeepsDefaults(t *testing.T) {
	cfg, err := Parse("c.toml", []byte("[risk]\nmax_leverage = 3\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := Defaults()
	want.Risk.MaxLeverage = 3
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("got %+v", cfg)
	}
}

func TestUnknownKeyNamesKeyAndLine(t *testing.T) {
	src := "network = \"mainnet\"\n\n[risk]\nmax_levrage = 3\n"
	_, err := Parse("/home/u/.config/hlp/config.toml", []byte(src))
	if err == nil {
		t.Fatal("unknown key accepted")
	}
	msg := err.Error()
	for _, want := range []string{"risk.max_levrage", "config.toml:4", "unknown key", `did you mean "risk.max_leverage"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
	var ce *Error
	if !errors.As(err, &ce) || ce.Key != "risk.max_levrage" || ce.Line != 4 {
		t.Errorf("not a typed *Error: %#v", err)
	}
	// Several unknown keys are all reported.
	_, err = Parse("c.toml", []byte("colour = 1\n[chart]\nstlye = \"x\"\n"))
	if err == nil || !strings.Contains(err.Error(), "colour") || !strings.Contains(err.Error(), "chart.stlye") {
		t.Errorf("want both keys, got %v", err)
	}
}

func TestTypeAndSyntaxErrors(t *testing.T) {
	_, err := Parse("c.toml", []byte("[risk]\nmax_leverage = \"lots\"\n"))
	if err == nil || !strings.Contains(err.Error(), "c.toml:2: risk.max_leverage: want a whole number, got a string") {
		t.Errorf("type error: %v", err)
	}
	if got := friendlyDecodeMsg("something else"); got != "something else" {
		t.Errorf("passthrough: %q", got)
	}
	_, err = Parse("c.toml", []byte("network = \n"))
	if err == nil || !strings.Contains(err.Error(), "c.toml") {
		t.Errorf("syntax error: %v", err)
	}
	// Load surfaces the same errors from a file.
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(path, []byte("bogus = true\n"), 0o600)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("Load: %v", err)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		key string
		mut func(*Config)
	}{
		{"network", func(c *Config) { c.Network = "devnet" }},
		{"account", func(c *Config) { c.Account = "0x123" }},
		{"watch[0]", func(c *Config) { c.Watch = []string{"vitalik.eth"} }},
		{"pinned_markets[0]", func(c *Config) { c.Pinned = []string{" "} }},
		{"chart.style", func(c *Config) { c.Chart.Style = "renko" }},
		{"chart.zoom", func(c *Config) { c.Chart.Zoom = 3 }},
		{"chart.interval", func(c *Config) { c.Chart.Interval = "7m" }},
		{"chart.mas", func(c *Config) { c.Chart.MAs = []int{1, 2, 3, 4} }},
		{"chart.mas[0]", func(c *Config) { c.Chart.MAs = []int{1} }},
		{"hlpxyz.base_url", func(c *Config) { c.HLPXYZ.BaseURL = "ftp://x" }},
		{"llm.provider", func(c *Config) { c.LLM.Provider = "gpt" }},
		{"llm.base_url", func(c *Config) { c.LLM.BaseURL = "localhost" }},
		{"llm.api_key_env", func(c *Config) { c.LLM.APIKeyEnv = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz" }},
		{"llm.api_key_env", func(c *Config) { c.LLM.APIKeyEnv = "has space" }},
		{"trading.agent_address", func(c *Config) { c.Trading.Enabled = true }},
		{"trading.vault", func(c *Config) { c.Trading.VaultAddress = "nope" }},
		{"trading.slippage_bps", func(c *Config) { c.Trading.SlippageBps = -1 }},
		{"risk.max_notional_usd", func(c *Config) { c.Risk.MaxNotionalUSD = 0 }},
		{"risk.max_leverage", func(c *Config) { c.Risk.MaxLeverage = 51 }},
		{"risk.daily_loss_usd", func(c *Config) { c.Risk.DailyLossUSD = -5 }},
		{"risk.dead_man_seconds", func(c *Config) { c.Risk.DeadManSeconds = 2 }},
		{"risk.dead_man_seconds", func(c *Config) { c.Risk.DeadManSeconds = -1 }},
		{"risk.allowed_coins[1]", func(c *Config) { c.Risk.AllowedCoins = []string{"BTC", ""} }},
	}
	for _, tc := range cases {
		cfg := Defaults()
		tc.mut(&cfg)
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.key+":") {
			t.Errorf("%s: got %v", tc.key, err)
		}
	}
	// A private key pasted into an address field is called out.
	cfg := Defaults()
	cfg.Trading.AgentAddress = "0x" + strings.Repeat("ab", 32)
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "private key") {
		t.Errorf("private key in address: %v", err)
	} else if strings.Contains(err.Error(), "abab") {
		t.Error("the pasted key is echoed in the error")
	}
	// Valid edge values.
	cfg = Defaults()
	cfg.Chart.Interval = domain.I1M
	cfg.LLM.Provider = "anthropic"
	cfg.LLM.APIKeyEnv = "ANTHROPIC_API_KEY"
	cfg.Trading.Enabled = true
	cfg.Trading.AgentAddress = "0x0000000000000000000000000000000000000001"
	cfg.Risk.DeadManSeconds = 5
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

func TestApplyEnv(t *testing.T) {
	env := map[string]string{EnvNetwork: "TESTNET", EnvTheme: "tokyonight", EnvAccount: "0x010461c14e146ac35fe42271bdc1134ee31c703a"}
	cfg := Defaults()
	if err := ApplyEnv(&cfg, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	if cfg.Network != domain.Testnet || cfg.Theme != "tokyonight" || cfg.Account != env[EnvAccount] {
		t.Errorf("got %+v", cfg)
	}
	cfg = Defaults()
	if err := ApplyEnv(&cfg, noEnv); err != nil || !reflect.DeepEqual(cfg, Defaults()) {
		t.Errorf("empty env changed config: %v", err)
	}
	err := ApplyEnv(&cfg, func(k string) string {
		if k == EnvNetwork {
			return "mainet"
		}
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "HLP_NETWORK") {
		t.Errorf("bad network: %v", err)
	}
}

func TestLoadAppliesEnvOverFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(path, []byte("network = \"mainnet\"\ntheme = \"light\"\n"), 0o600)
	clearEnv(t)
	t.Setenv(EnvNetwork, "testnet")
	t.Setenv(EnvTheme, "mono")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Network != domain.Testnet || cfg.Theme != "mono" {
		t.Errorf("env did not win: %+v", cfg)
	}
	t.Setenv(EnvAccount, "not-an-address")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "account") {
		t.Errorf("invalid HLP_ACCOUNT accepted: %v", err)
	}
}

func TestDefaultPaths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/cfg")
	t.Setenv("XDG_CACHE_HOME", "/x/cache")
	t.Setenv("XDG_DATA_HOME", "/x/data")
	t.Setenv("HLP_CONFIG", "")
	p := DefaultPaths()
	want := Paths{ConfigFile: filepath.Join("/x/cfg", "hlp", "config.toml"), CacheDir: filepath.Join("/x/cache", "hlp"), DataDir: filepath.Join("/x/data", "hlp")}
	if p != want {
		t.Errorf("got %+v", p)
	}
	t.Setenv("HLP_CONFIG", "/elsewhere.toml")
	if DefaultPaths().ConfigFile != "/elsewhere.toml" {
		t.Error("HLP_CONFIG ignored")
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	if !strings.HasSuffix(filepath.ToSlash(DefaultPaths().CacheDir), "/x/cache/hlp") {
		t.Error("cache dir")
	}
}

func TestIsAddressAndSuggest(t *testing.T) {
	if !IsAddress("0xdfc24b077bc1425ad1dea75bcb6f8158e10df303") || IsAddress("dfc24b077bc1425ad1dea75bcb6f8158e10df303") {
		t.Error("IsAddress")
	}
	if suggestKey("risk.max_notional") != "risk.max_notional_usd" {
		t.Errorf("suggest = %q", suggestKey("risk.max_notional"))
	}
	if suggestKey("completely.unrelated.thing") != "" {
		t.Error("suggested for garbage")
	}
	if editDistance("kitten", "sitting") != 3 || editDistance("", "abc") != 3 {
		t.Error("editDistance")
	}
}

func TestErrorString(t *testing.T) {
	cases := map[string]*Error{
		"config f.toml:3: a.b: unknown key": {Path: "f.toml", Line: 3, Key: "a.b", Msg: "unknown key"},
		"config f.toml: boom":               {Path: "f.toml", Msg: "boom"},
		"config: k: bad":                    {Key: "k", Msg: "bad"},
	}
	for want, e := range cases {
		if e.Error() != want {
			t.Errorf("got %q want %q", e.Error(), want)
		}
	}
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{EnvNetwork, EnvTheme, EnvAccount} {
		t.Setenv(k, "")
	}
}
