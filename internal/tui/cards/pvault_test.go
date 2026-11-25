package cards

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/evm"
	"github.com/maxmoneycash/hlp-cli/internal/hlpxyz"
	"github.com/maxmoneycash/hlp-cli/internal/pvault"
)

func fixturePVaultTearsheet(t testing.TB) *pvault.Tearsheet {
	t.Helper()
	var snap struct {
		Vault     string `json:"vault"`
		Lens      string `json:"lens"`
		VaultCall struct {
			Result string `json:"result"`
		} `json:"lens_vaultState"`
		UserCall map[string]struct {
			Result string `json:"result"`
		} `json:"lens_userState"`
	}
	readFixture(t, "fixtures/pvault/onchain_snapshot.json", &snap)
	raw, _ := evm.DecodeHex(snap.VaultCall.Result)
	st, err := pvault.DecodeVaultState(raw)
	if err != nil {
		t.Fatal(err)
	}
	urw, _ := evm.DecodeHex(snap.UserCall["owner"].Result)
	u, err := pvault.DecodeUserState(urw)
	if err != nil {
		t.Fatal(err)
	}
	var ep struct {
		Epochs []pvault.EpochData `json:"epochs"`
	}
	readFixture(t, "fixtures/pvault/epochs.json", &ep)
	var status hlpxyz.PVaultStatus
	readFixture(t, "fixtures/pvault/api_status.json", &status)
	var hist struct {
		Samples []hlpxyz.PVaultSample `json:"samples"`
	}
	readFixture(t, "fixtures/pvault/api_history.json", &hist)
	owner := evm.MustAddress("0xF2129a3b4b3594b28DEF2C4B5Cefc2d1Be3356A4")
	d := &pvault.Details{Name: "Trend Pyramid (testnet)", Symbol: "hlpTP", EpochDuration: 600, SettleGrace: 600,
		MinDeposit: evm.BigInt64(1_000_000), DepositCap: evm.BigInt64(10_000_000_000), MaxDeploy: evm.BigInt64(250_000_000),
		PerformanceFeeBps: 1000, Owner: owner, Keeper: evm.MustAddress("0xbF3Cb224E1dFD20b6FC24cCFce168C173D81cdF1"),
		Guardian: st.Manager, Manager: st.Manager, FeeRecipient: evm.MustAddress("0x7fFB5EA882eEc736200c51209d668A6972092000"),
		APIWallet: st.APIWallet, NavDexes: []uint32{0, 1}}
	return pvault.BuildTearsheet(pvault.Inputs{
		ChainID: 998, Vault: evm.MustAddress(snap.Vault), Lens: evm.MustAddress(snap.Lens), Source: pvault.SourceLens,
		State: st, Details: d, Epochs: ep.Epochs[len(ep.Epochs)-12:], Status: &status, History: hist.Samples,
		User: u, UserAddr: owner, Now: time.Unix(1794744506, 0),
	})
}

func pvResult(card string, data any) *core.Result {
	return &core.Result{Card: core.Card(card), Data: data,
		Meta: domain.Meta{Source: "hyperevm+hlp.xyz", AsOf: fixedAsOf, Network: domain.Testnet}}
}

func TestPVaultTearsheetCard(t *testing.T) {
	ts := fixturePVaultTearsheet(t)
	for _, w := range []int{100, 72, 40} {
		for _, compact := range []bool{false, true} {
			out := RenderPVault(pvResult(pvault.CardTearsheet, ts), Opts{Width: w, Plain: true, Compact: compact})
			for i, l := range strings.Split(out, "\n") {
				if lw := lipgloss.Width(l); lw > w {
					t.Errorf("w=%d compact=%v line %d is %d wide: %q", w, compact, i, lw, l)
				}
			}
			if !strings.Contains(out, "TREND PYRAMID") || !strings.Contains(out, "1.0261") {
				t.Errorf("w=%d: missing title or share price:\n%s", w, out)
			}
		}
	}
	out := RenderPVault(pvResult(pvault.CardTearsheet, ts), Opts{Width: 100, Plain: true})
	for _, want := range []string{"1.0261", "HWM -3.3%", "EPOCH 444", "1.0612", "444 open", "DEPLOY ROOM", "$174", "perf 10%",
		"SHORT", "RECENT", "443", "YOU 0xf212", "TESTNET"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// plumbing never shows (DESIGN.md section 5.1 rule 5)
	if strings.Contains(out, "via lens") || strings.Contains(out, "@ http") {
		t.Errorf("read path shown:\n%s", out)
	}
	// inline: header, one meta line, the NAV sparkline; nothing else
	in := RenderPVault(pvResult(pvault.CardTearsheet, ts), Opts{Width: 100, Plain: true, Compact: true})
	if n := strings.Count(in, "\n") + 1; n > 4 || strings.Contains(in, "DEPLOY ROOM") {
		t.Errorf("inline pvault card is %d lines:\n%s", n, in)
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("ANSI in plain output")
	}
	// colored path renders too
	if s := RenderPVault(pvResult(pvault.CardTearsheet, ts), Opts{Width: 100}); !strings.Contains(s, "TREND PYRAMID") {
		t.Error("color render")
	}
}

func TestPVaultEpochsAndUserCards(t *testing.T) {
	var ep struct {
		Epochs []pvault.EpochData `json:"epochs"`
	}
	readFixture(t, "fixtures/pvault/epochs.json", &ep)
	v := evm.MustAddress("0x688dD654bDbDE2BF8b6154685437f3F9dB2eAABd")
	tab := pvault.BuildEpochTable(998, v, "Trend Pyramid (testnet)", 1, 443, ep.Epochs)
	out := RenderPVault(pvResult(pvault.CardEpochs, tab), Opts{Width: 90, Plain: true})
	if !strings.Contains(out, "EPOCHS 1-443") || !strings.Contains(out, "+423") || !strings.Contains(out, "GAP 11m") {
		t.Errorf("epochs card:\n%s", out)
	}
	all := RenderPVault(pvResult(pvault.CardEpochs, tab), Opts{Width: 90, Plain: true, AllRows: true})
	if strings.Count(all, "\n") < 443 {
		t.Error("AllRows should print every epoch")
	}
	ts := fixturePVaultTearsheet(t)
	uo := RenderPVault(pvResult(pvault.CardUser, ts.User), Opts{Width: 60, Plain: true})
	if !strings.Contains(uo, "15.0  SH") {
		t.Errorf("user card:\n%s", uo)
	}
	// non-pvault data falls back to Render
	if s := RenderPVault(&core.Result{Card: core.CardText, Data: "hi"}, Opts{Plain: true}); s == "" {
		t.Error("fallback")
	}
}
