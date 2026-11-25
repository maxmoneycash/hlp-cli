package corewriter

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/sha3"

	"github.com/maxmoneycash/hlp/internal/evm"
)

func hexb(t *testing.T, s string) []byte {
	t.Helper()
	b, err := evm.DecodeHex(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSelectorIsKeccak(t *testing.T) {
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte("sendRawAction(bytes)"))
	if got := evm.EncodeHex(h.Sum(nil)[:4]); got != SelSendRawAction.Hex() {
		t.Fatalf("selector %s, keccak %s", SelSendRawAction.Hex(), got)
	}
	h = sha3.NewLegacyKeccak256()
	h.Write([]byte("RawAction(address,bytes)"))
	if got := evm.EncodeHex(h.Sum(nil)); got != RawActionTopic {
		t.Fatalf("topic %s, keccak %s", RawActionTopic, got)
	}
}

// The pvault deployment's applied CoreWriter actions (decoded receipts from
// testnet) are the golden vectors: the RawAction event data is
// abi.encode(bytes raw), the same bytes sendRawAction takes after its
// selector.
func TestPVaultFixtures(t *testing.T) {
	b, err := os.ReadFile("../../../testdata/fixtures/pvault/corewriter_actions.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Txs []struct {
			Label string `json:"label"`
			Logs  []struct {
				Address string   `json:"address"`
				Topics  []string `json:"topics"`
				Data    string   `json:"data"`
				Decoded *struct {
					ActionID     uint32 `json:"actionId"`
					AddAPIWallet *struct {
						Agent string `json:"agent"`
						Name  string `json:"name"`
					} `json:"addApiWallet"`
					SendAsset *struct {
						Destination    string `json:"destination"`
						SubAccount     string `json:"subAccount"`
						SourceDex      uint32 `json:"sourceDex"`
						DestinationDex uint32 `json:"destinationDex"`
						Token          uint64 `json:"token"`
						AmountWei8dp   uint64 `json:"amountWei8dp"`
					} `json:"sendAsset"`
				} `json:"decoded"`
			} `json:"logs"`
		} `json:"txs"`
	}
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	vault := evm.MustAddress("0x688dd654bdbde2bf8b6154685437f3f9db2eaabd")
	checked := 0
	for _, tx := range fx.Txs {
		for _, l := range tx.Logs {
			if l.Decoded == nil {
				continue
			}
			if !strings.EqualFold(l.Address, Address.Hex()) || l.Topics[0] != RawActionTopic {
				t.Fatalf("%s: not a CoreWriter RawAction log", tx.Label)
			}
			want := hexb(t, l.Data)
			var raw []byte
			switch {
			case l.Decoded.AddAPIWallet != nil:
				a := l.Decoded.AddAPIWallet
				raw = AddAPIWallet(evm.MustAddress(a.Agent), a.Name)
			case l.Decoded.SendAsset != nil:
				s := l.Decoded.SendAsset
				dest := evm.MustAddress(s.Destination)
				switch {
				case dest == SystemAddress:
					raw = SpotUSDCToOwnEVM(s.AmountWei8dp)
				case dest == vault:
					raw = MoveUSDC(vault, s.SourceDex, s.DestinationDex, s.AmountWei8dp)
				default:
					raw = SendAsset(dest, evm.MustAddress(s.SubAccount), s.SourceDex, s.DestinationDex, s.Token, s.AmountWei8dp)
				}
			default:
				t.Fatalf("%s: unhandled action %d", tx.Label, l.Decoded.ActionID)
			}
			if got := EncodeBytesArg(raw); !bytes.Equal(got, want) {
				t.Errorf("%s:\n got %x\nwant %x", tx.Label, got, want)
			}
			// The calldata round-trips back to the same raw action.
			back, err := DecodeSendRawAction(SendRawActionCalldata(raw), true)
			if err != nil || !bytes.Equal(back, raw) {
				t.Errorf("%s: calldata round trip: %v", tx.Label, err)
			}
			ev, err := DecodeSendRawAction(want, false)
			if err != nil || !bytes.Equal(ev, raw) {
				t.Errorf("%s: event decode: %v", tx.Label, err)
			}
			d, _ := Decode(raw)
			if d.Version != Version || d.ActionID != l.Decoded.ActionID {
				t.Errorf("%s: header %d/%d", tx.Label, d.Version, d.ActionID)
			}
			checked++
		}
	}
	if checked != 4 {
		t.Fatalf("checked %d fixture actions, want 4", checked)
	}
}

// Vectors generated independently with foundry:
//
//	cast calldata 'sendRawAction(bytes)' 0x01000007$(cast abi-encode 'f(uint64,bool)' 1500000 true | cut -c3-)
//	cast calldata 'sendRawAction(bytes)' 0x01000009$(cast abi-encode 'f(address,string)' 0x7e57…00c1 'hlp valid_until 1790000000000' | cut -c3-)
func TestCastVectors(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		want string
	}{
		{"usdClassTransfer", USDClassTransfer(1_500_000, true),
			"0x17938e130000000000000000000000000000000000000000000000000000000000000020000000000000000000000000000000000000000000000000000000000000004401000007000000000000000000000000000000000000000000000000000000000016e360000000000000000000000000000000000000000000000000000000000000000100000000000000000000000000000000000000000000000000000000"},
		{"addApiWallet+valid_until", AddAPIWallet(evm.MustAddress("0x7e57a6e0000000000000000000000000000000c1"), AgentName("hlp", time.UnixMilli(1_790_000_000_000))),
			"0x17938e1300000000000000000000000000000000000000000000000000000000000000200000000000000000000000000000000000000000000000000000000000000084010000090000000000000000000000007e57a6e0000000000000000000000000000000c10000000000000000000000000000000000000000000000000000000000000040000000000000000000000000000000000000000000000000000000000000001d686c702076616c69645f756e74696c203137393030303030303030303000000000000000000000000000000000000000000000000000000000000000"},
	}
	for _, c := range cases {
		if got := evm.EncodeHex(SendRawActionCalldata(c.raw)); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

func TestEncodeBounds(t *testing.T) {
	if _, err := Encode(0, nil); err == nil {
		t.Error("action 0 accepted")
	}
	if _, err := Encode(1<<24, nil); err == nil {
		t.Error("action 2^24 accepted")
	}
	r, err := Encode(0xABCDEF, []byte{})
	if err != nil || !bytes.Equal(r, []byte{1, 0xAB, 0xCD, 0xEF}) {
		t.Errorf("header %x %v", r, err)
	}
	// empty name: one word of length 0, no data words
	if n := len(AddAPIWallet(evm.Address{}, "")); n != 4+96 {
		t.Errorf("empty-name action is %d bytes", n)
	}
}

func TestAgentName(t *testing.T) {
	n := AgentName(" hlp ", time.UnixMilli(1_790_000_000_000))
	if n != "hlp valid_until 1790000000000" {
		t.Fatal(n)
	}
	base, ms, ok := ParseAgentName(n)
	if !ok || base != "hlp" || ms != 1_790_000_000_000 {
		t.Fatal(base, ms, ok)
	}
	if _, _, ok := ParseAgentName("strategy"); ok {
		t.Fatal("plain name parsed as having an expiry")
	}
	if AgentName("x", time.Time{}) != "x" {
		t.Fatal("zero time added a suffix")
	}
}

func TestParseUSD(t *testing.T) {
	ok := map[string]uint64{"200": 20_000_000_000, "187.562653": 18_756_265_300, "0.00000001": 1, "0": 0}
	for s, want := range ok {
		got, err := ParseUSD(s, 8)
		if err != nil || got != want {
			t.Errorf("%s: %d %v", s, got, err)
		}
	}
	for _, s := range []string{"1.000000001", "-1", "1e3", "abc", "1/3", "184467440737.09551616"} {
		if _, err := ParseUSD(s, 8); err == nil {
			t.Errorf("%s accepted", s)
		}
	}
}

func TestDescribe(t *testing.T) {
	cases := map[string][]byte{
		`addApiWallet(0x6d2b4d11a1b7cecdddb7a897b1874f3ac2739a92, "strategy")`: AddAPIWallet(evm.MustAddress("0x6d2b4d11a1b7cecdddb7a897b1874f3ac2739a92"), "strategy"),
		"usdClassTransfer(1.5 USDC, toPerp=true)":                              USDClassTransfer(1_500_000, true),
		"sendAsset(dest=0x2000000000000000000000000000000000000000, sub=0x0000000000000000000000000000000000000000, srcDex=spot, dstDex=spot, token=0, wei=20000000000)": SpotUSDCToOwnEVM(20_000_000_000),
	}
	for want, raw := range cases {
		if got := Describe(raw); got != want {
			t.Errorf("got %s want %s", got, want)
		}
	}
}
