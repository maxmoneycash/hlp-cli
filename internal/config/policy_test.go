package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
)

const agent = "0x1111111111111111111111111111111111111111"

func TestTradingExpiry(t *testing.T) {
	tc := TradingConfig{Expires: "2026-12-31"}
	at, err := tc.Expiry()
	if err != nil || !at.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("date expiry %v %v", at, err)
	}
	if tc.Expired(time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC)) || !tc.Expired(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("a date runs through the end of that UTC day")
	}
	tc.Expires = "2026-12-31T18:00:00+02:00"
	if at, _ := tc.Expiry(); !at.Equal(time.Date(2026, 12, 31, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("rfc3339 %v", at)
	}
	if (TradingConfig{}).Expired(time.Now()) {
		t.Fatal("no expiry never expires")
	}
	bad := TradingConfig{Expires: "end of year"}
	if _, err := bad.Expiry(); err == nil || !bad.Expired(time.Unix(0, 0)) {
		t.Fatal("unparsable expiry must refuse, never mean forever")
	}
}

func TestAllowsVaultAndNetwork(t *testing.T) {
	v := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	var r RiskConfig
	if !r.AllowsVault(v, false, false) || r.AllowsVault(v, false, true) || !r.AllowsVault(v, true, true) {
		t.Fatal("empty list: any vault for a person, curated only for automated callers")
	}
	r.AllowedVaults = []string{"0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"}
	if !r.AllowsVault(v, false, true) || r.AllowsVault("0xcccccccccccccccccccccccccccccccccccccccc", true, false) {
		t.Fatal("listed address, case-insensitive; unlisted refused even for a person and even if curated")
	}
	r.AllowedVaults = append(r.AllowedVaults, "curated")
	if !r.AllowsVault("0xcccccccccccccccccccccccccccccccccccccccc", true, true) {
		t.Fatal("curated entry")
	}
	if !(RiskConfig{}).AllowsNetwork(domain.Mainnet) {
		t.Fatal("empty networks allows both")
	}
	r.Networks = []string{"Testnet"}
	if r.AllowsNetwork(domain.Mainnet) || !r.AllowsNetwork(domain.Testnet) {
		t.Fatal("testnet-only")
	}
}

func TestPolicyValidate(t *testing.T) {
	r := Defaults().Risk
	if r.MaxOrdersPerMin != DefaultMaxOrdersPerMin {
		t.Fatalf("default rate %d", r.MaxOrdersPerMin)
	}
	r.AllowedVaults = []string{"growi", "curated", "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	r.Networks = []string{"devnet"}
	r.MaxOrdersPerMin = -1
	err := r.Validate()
	for _, k := range []string{"risk.allowed_vaults[0]", "risk.networks[0]", "risk.max_orders_per_min"} {
		if err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("want error on %s, got %v", k, err)
		}
	}
	if strings.Contains(err.Error(), "allowed_vaults[1]") || strings.Contains(err.Error(), "allowed_vaults[2]") {
		t.Errorf("valid entries flagged: %v", err)
	}
	r = Defaults().Risk
	r.MaxOrdersPerMin = 0 // off is allowed
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
}

// Three-state policy loading: absent permits defaults, corrupt
// refuses, valid is enforced. A config returned with an error is disarmed
// so a caller that ignores the error still cannot trade.
func TestCorruptConfigIsDisarmed(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	armed := "[trading]\nenabled = true\nallow_unattended = true\nagent_address = \"" + agent + "\"\n[mcp]\nallow_trading = true\nallow_unattended = true\n"
	cases := map[string]string{
		"syntax":     armed + "[risk\n",
		"bad value":  armed + "[risk]\nmax_leverage = 0\n",
		"bad expiry": strings.Replace(armed, "enabled = true\n", "enabled = true\nexpires = \"someday\"\n", 1),
		"typo":       armed + "[risk]\nmax_orders_per_minute = 5\n",
		"wrong type": armed + "[risk]\nnetworks = \"testnet\"\n",
	}
	for name, body := range cases {
		cfg, err := Load(write(strings.ReplaceAll(name, " ", "_")+".toml", body))
		if err == nil {
			t.Errorf("%s: loaded", name)
			continue
		}
		if cfg.Trading.Enabled || cfg.Trading.AllowUnattended || cfg.MCP.AllowTrading || cfg.MCP.AllowUnattended {
			t.Errorf("%s: config returned with an error is still armed: %+v %+v", name, cfg.Trading, cfg.MCP)
		}
		if _, err := Parse(name, []byte(body)); err == nil {
			t.Errorf("%s: Parse accepted", name)
		}
	}
	// Absent: defaults, trading off, no error.
	cfg, err := Load(filepath.Join(dir, "missing.toml"))
	if err != nil || cfg.Trading.Enabled {
		t.Fatalf("absent: %v %+v", err, cfg.Trading)
	}
	// Valid: enforced as written.
	cfg, err = Load(write("ok.toml", armed+"[risk]\nnetworks = [\"testnet\"]\nallowed_vaults = [\"curated\"]\nmax_orders_per_min = 10\n"))
	if err != nil || !cfg.Trading.Enabled || cfg.Risk.MaxOrdersPerMin != 10 || len(cfg.Risk.Networks) != 1 {
		t.Fatalf("valid: %v %+v", err, cfg.Risk)
	}
	// A typo gets a suggestion.
	_, err = Load(write("typo2.toml", "[risk]\nmax_order_per_min = 5\n"))
	if err == nil || !strings.Contains(err.Error(), "risk.max_orders_per_min") {
		t.Fatalf("typo suggestion: %v", err)
	}
}
