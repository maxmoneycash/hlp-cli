package cards

import (
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"

	"github.com/maxmoneycash/hlp/internal/core"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/theme"
)

// evilText is third-party text (a vault name, a model reply) carrying an
// OSC 52 clipboard write, a window-title OSC, a C1 CSI and a bidi override
// (audit S3). None of it may reach the terminal.
const evilText = "Nice vault\x1b]52;c;MHhkZWFk\x07 \x1b]0;pwned\x07\x9b2J‮gnp"

// rawControl reports the first terminal control that is not a styling
// SGR sequence (ESC [ ... m), which the styled renderer emits itself.
func rawControl(s string) (string, bool) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case 0x07:
			return "BEL", true
		case 0x1b:
			if i+1 < len(s) && s[i+1] == '[' {
				j := i + 2
				for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == ';' || s[j] == ':') {
					j++
				}
				if j < len(s) && s[j] == 'm' {
					i = j
					continue
				}
			}
			return "ESC " + strings.TrimSpace(s[i+1:min(i+4, len(s))]), true
		}
	}
	for _, r := range s {
		switch {
		case r >= 0x80 && r < 0xa0:
			return "C1", true
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return "bidi override", true
		}
	}
	return "", false
}

// TestThirdPartyTextIsSanitized is the S3 regression for the whole chain:
// hostile text in data reaches neither the styled nor the plain card, nor
// the markdown renderer. It goes green once the CleanText call sites in the
// mappers and renderers (WP-C cards, WP-D hlpxyz, WP-E hyperliquid and the
// agent stream, WP-F MCP text) are in place.
func TestThirdPartyTextIsSanitized(t *testing.T) {
	results := map[string]*core.Result{
		"vaults": {Card: core.CardVaults, Title: "Vaults", Data: []domain.VaultSummary{
			{Address: "0x1111111111111111111111111111111111111111", Name: evilText, Leader: evilText, TVL: 1e6, AgeDays: 100, AllowDeposits: true},
		}},
		"vault": {Card: core.CardVault, Title: "Vault", Data: domain.VaultDetail{
			Summary:     domain.VaultSummary{Address: "0x1111111111111111111111111111111111111111", Name: evilText, TVL: 1e6, AgeDays: 100},
			Description: evilText,
		}},
		"markets": {Card: core.CardMarkets, Title: "Markets", Data: []domain.Market{
			{Coin: "xyz:EVIL", Display: evilText, Kind: domain.HIP3, Mark: 1, Mid: 1, PrevDay: 1},
		}},
		"order": {Card: core.CardOrder, Title: "Confirm", Data: &core.OrderPreview{Action: "vault_deposit", NotionalUSD: 100,
			Vault: "0x1111111111111111111111111111111111111111", DryRun: true, Warnings: []string{evilText},
			Checks: []domain.Check{{ID: "vault_target", Label: "Vault", Status: domain.CheckPass, Detail: evilText}}}},
		"text": {Card: core.CardText, Data: evilText},
	}
	th := theme.Default(true)
	for name, r := range results {
		for _, plain := range []bool{false, true} {
			out := Render(r, Opts{Width: 100, Theme: th, Profile: colorprofile.TrueColor, Plain: plain})
			if what, bad := rawControl(out); bad {
				t.Errorf("%s (plain=%v): %s reached the terminal:\n%q", name, plain, what, out)
			}
		}
	}
	for _, plain := range []bool{false, true} {
		out := strings.Join(Markdown("# hi\n\n"+evilText+"\n", Opts{Width: 80, Theme: th, Profile: colorprofile.TrueColor, Plain: plain}), "\n")
		if what, bad := rawControl(out); bad {
			t.Errorf("markdown (plain=%v): %s reached the terminal:\n%q", plain, what, out)
		}
	}
}

// TestErrorCardIsSanitized: error text carries untrusted strings (an arena
// entrant path with an OSC 52 clipboard write) and must not reach the
// terminal raw (SECURITY S3).
func TestErrorCardIsSanitized(t *testing.T) {
	err := &core.Error{Code: core.CodeUsage, Message: "stat ex/x\x1b]52;c;MHhkZWFk\x07.pine: no such file", Hint: evilText}
	for _, plain := range []bool{false, true} {
		out := Error(err, Opts{Width: 100, Plain: plain, Theme: theme.Default(true), Profile: colorprofile.TrueColor})
		if what, bad := rawControl(out); bad {
			t.Fatalf("plain=%v: error card leaks %s: %q", plain, what, out)
		}
	}
}

func TestOrderPreviewIsSanitized(t *testing.T) {
	d := core.OrderPreview{Coin: "BTC", Side: domain.Buy, VaultName: evilText}
	out := OrderPreview(d, Opts{Width: 100, Theme: theme.Default(true), Profile: colorprofile.TrueColor})
	if what, bad := rawControl(out); bad {
		t.Fatalf("order preview leaks %s: %q", what, out)
	}
}
