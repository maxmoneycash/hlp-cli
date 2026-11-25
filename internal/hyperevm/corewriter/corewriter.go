// Package corewriter encodes HyperCore CoreWriter actions, written from
// Hyperliquid's public CoreWriter specification (a raw action is
// version 0x01 ‖ uint24 action id ‖ abi.encode(fields), passed as the one
// bytes argument of CoreWriter.sendRawAction at 0x3333…3333).
//
// It only builds bytes. Nothing here signs or sends anything: hlp has no
// EVM signer, and a contract account's owners execute the transaction in
// their own wallet.
package corewriter

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp/internal/evm"
)

// Address is the CoreWriter system contract on HyperEVM (both chains).
var Address = evm.MustAddress("0x3333333333333333333333333333333333333333")

// SystemAddress is the HyperCore↔HyperEVM bridge address of USDC/HYPE
// (token 0's system address): sendAsset to it moves Core spot to the
// sender's own EVM balance.
var SystemAddress = evm.MustAddress("0x2000000000000000000000000000000000000000")

// SelSendRawAction is sendRawAction(bytes).
var SelSendRawAction = evm.MustSelector("0x17938e13")

// RawActionTopic is RawAction(address indexed user, bytes data).
const RawActionTopic = "0x8c7f585fb295f7eb1e6aeb8fba61b23a4fe60beda405f0045073b185c74412e3"

// Version is the only raw-action encoding version.
const Version byte = 0x01

// Action ids (HL CoreWriter spec). Only the ones with a typed builder here
// are checked against applied transactions (pvault fixtures); verify the
// others against the current HL docs before building them.
const (
	ActionLimitOrder        uint32 = 1
	ActionVaultTransfer     uint32 = 2
	ActionTokenDelegate     uint32 = 3
	ActionStakingDeposit    uint32 = 4
	ActionStakingWithdraw   uint32 = 5
	ActionSpotSend          uint32 = 6
	ActionUSDClassTransfer  uint32 = 7
	ActionFinalizeEVM       uint32 = 8
	ActionAddAPIWallet      uint32 = 9
	ActionCancelByOID       uint32 = 10
	ActionCancelByCloid     uint32 = 11
	ActionApproveBuilderFee uint32 = 12
	ActionSendAsset         uint32 = 13
)

// DexSpot is the sendAsset dex sentinel for the spot balance (uint32 max).
// 0 is the main perp dex; n is perpDexs[n].
const DexSpot uint32 = math.MaxUint32

// ErrActionID is returned for an id that does not fit in uint24 or is 0.
var ErrActionID = errors.New("corewriter: action id must be 1..2^24-1")

// Encode frames abi-encoded fields as a raw action:
// 0x01 ‖ uint24 big-endian action id ‖ fields.
func Encode(actionID uint32, fields []byte) ([]byte, error) {
	if actionID == 0 || actionID > 0xFFFFFF {
		return nil, ErrActionID
	}
	out := make([]byte, 0, 4+len(fields))
	out = append(out, Version, byte(actionID>>16), byte(actionID>>8), byte(actionID))
	return append(out, fields...), nil
}

// Decoded is a raw action split into its header and field words.
type Decoded struct {
	Version  byte
	ActionID uint32
	Fields   evm.Words
}

// Decode splits a raw action. The field bytes must be whole words.
func Decode(raw []byte) (*Decoded, error) {
	if len(raw) < 4 {
		return nil, errors.New("corewriter: raw action shorter than its 4-byte header")
	}
	w, err := evm.NewWords(raw[4:])
	if err != nil {
		return nil, fmt.Errorf("corewriter: %w", err)
	}
	return &Decoded{Version: raw[0], ActionID: uint32(raw[1])<<16 | uint32(raw[2])<<8 | uint32(raw[3]), Fields: w}, nil
}

// SendRawActionCalldata is the calldata of CoreWriter.sendRawAction(raw):
// selector ‖ abi.encode(bytes raw).
func SendRawActionCalldata(raw []byte) []byte {
	return append(append([]byte(nil), SelSendRawAction[:]...), EncodeBytesArg(raw)...)
}

// EncodeBytesArg is abi.encode(bytes b) for a single dynamic argument:
// offset 0x20 ‖ length ‖ b right-padded to a word. It is also the data of
// the RawAction event.
func EncodeBytesArg(b []byte) []byte {
	out := make([]byte, 0, 64+pad32(len(b)))
	off := evm.Uint64Word(32)
	n := evm.Uint64Word(uint64(len(b)))
	out = append(out, off[:]...)
	out = append(out, n[:]...)
	return appendPadded(out, b)
}

// DecodeSendRawAction returns the raw action inside sendRawAction calldata
// (or inside RawAction event data when withSelector is false).
func DecodeSendRawAction(data []byte, withSelector bool) ([]byte, error) {
	if withSelector {
		if len(data) < 4 || [4]byte(data[:4]) != [4]byte(SelSendRawAction) {
			return nil, errors.New("corewriter: not sendRawAction calldata")
		}
		data = data[4:]
	}
	w, err := evm.NewWords(data)
	if err != nil {
		return nil, err
	}
	return w.Bytes(0)
}

func pad32(n int) int { return (n + 31) / 32 * 32 }

func appendPadded(out, b []byte) []byte {
	out = append(out, b...)
	if r := len(b) % 32; r != 0 {
		out = append(out, make([]byte, 32-r)...)
	}
	return out
}

// AddAPIWallet is action 9 addApiWallet(address wallet, string name). An
// empty name uses the account's single unnamed slot. Registering the zero
// address under an existing name is how HL documents removal; it is not
// proven on a contract account.
func AddAPIWallet(wallet evm.Address, name string) []byte {
	aw := evm.AddressWord(wallet)
	off := evm.Uint64Word(64)
	n := evm.Uint64Word(uint64(len(name)))
	f := make([]byte, 0, 96+pad32(len(name)))
	f = append(f, aw[:]...)
	f = append(f, off[:]...)
	f = append(f, n[:]...)
	f = appendPadded(f, []byte(name))
	out, _ := Encode(ActionAddAPIWallet, f)
	return out
}

// AgentName is HL's named-agent convention: "<name> valid_until <ms>". HL
// parses the expiry from the suffix; without it the agent gets HL's default
// validity.
func AgentName(name string, validUntil time.Time) string {
	name = strings.TrimSpace(name)
	if validUntil.IsZero() {
		return name
	}
	return name + " valid_until " + strconv.FormatInt(validUntil.UnixMilli(), 10)
}

// ParseAgentName splits "<name> valid_until <ms>" (ok=false without the
// suffix).
func ParseAgentName(s string) (name string, validUntilMs int64, ok bool) {
	i := strings.LastIndex(s, " valid_until ")
	if i < 0 {
		return s, 0, false
	}
	ms, err := strconv.ParseInt(s[i+len(" valid_until "):], 10, 64)
	if err != nil {
		return s, 0, false
	}
	return s[:i], ms, true
}

// USDClassTransfer is action 7 usdClassTransfer(uint64 ntl, bool toPerp):
// moves USDC between the sender's spot and main-perp balances. ntl is in
// 1e-6 USD.
func USDClassTransfer(ntl uint64, toPerp bool) []byte {
	a, b := evm.Uint64Word(ntl), evm.BoolWord(toPerp)
	out, _ := Encode(ActionUSDClassTransfer, append(a[:], b[:]...))
	return out
}

// SendAsset is action 13 sendAsset(address destination, address
// subAccount, uint32 sourceDex, uint32 destinationDex, uint64 token,
// uint64 wei). For USDC, token is 0 and wei has 8 decimals.
func SendAsset(dest, subAccount evm.Address, srcDex, dstDex uint32, token, wei uint64) []byte {
	words := []evm.Word{evm.AddressWord(dest), evm.AddressWord(subAccount), evm.Uint64Word(uint64(srcDex)),
		evm.Uint64Word(uint64(dstDex)), evm.Uint64Word(token), evm.Uint64Word(wei)}
	f := make([]byte, 0, 32*len(words))
	for _, w := range words {
		f = append(f, w[:]...)
	}
	out, _ := Encode(ActionSendAsset, f)
	return out
}

// SpotUSDCToOwnEVM is sendAsset(0x2000…, 0, spot, spot, USDC, wei8dp):
// Core spot USDC back to the sender's own HyperEVM address (pvault's
// returnToEvm). Every field but the amount is fixed.
func SpotUSDCToOwnEVM(wei8dp uint64) []byte {
	return SendAsset(SystemAddress, evm.Address{}, DexSpot, DexSpot, 0, wei8dp)
}

// MoveUSDC is sendAsset to the sender itself between two of its dexes
// (pvault's moveCore): DexSpot, 0 (main perp) or a HIP-3 dex index.
func MoveUSDC(self evm.Address, srcDex, dstDex uint32, wei8dp uint64) []byte {
	return SendAsset(self, evm.Address{}, srcDex, dstDex, 0, wei8dp)
}

// ParseUSD parses a decimal USD amount exactly into units of 10^-dec,
// refusing anything that would need rounding (more than dec decimals),
// negative values and overflow.
func ParseUSD(s string, dec int) (uint64, error) {
	s = strings.TrimSpace(s)
	r, ok := new(big.Rat).SetString(s)
	if !ok || strings.ContainsAny(s, "eE/") {
		return 0, fmt.Errorf("corewriter: %q is not a decimal amount", s)
	}
	if r.Sign() < 0 {
		return 0, fmt.Errorf("corewriter: amount %q is negative", s)
	}
	r.Mul(r, new(big.Rat).SetInt(evm.Pow10(dec)))
	if !r.IsInt() {
		return 0, fmt.Errorf("corewriter: %q has more than %d decimals", s, dec)
	}
	n := r.Num()
	if !n.IsUint64() {
		return 0, fmt.Errorf("corewriter: %q overflows uint64", s)
	}
	return n.Uint64(), nil
}

// Describe names a raw action for display ("addApiWallet(0x…, \"x\")").
// Unknown ids are shown by number.
func Describe(raw []byte) string {
	d, err := Decode(raw)
	if err != nil {
		return "invalid raw action"
	}
	w := d.Fields
	switch d.ActionID {
	case ActionAddAPIWallet:
		a, _ := w.Address(0)
		name, _ := w.String(1)
		return fmt.Sprintf("addApiWallet(%s, %q)", a.Hex(), name)
	case ActionUSDClassTransfer:
		n, _ := w.Uint64(0)
		p, _ := w.Bool(1)
		return fmt.Sprintf("usdClassTransfer(%s USDC, toPerp=%v)", decStr(n, 6), p)
	case ActionSendAsset:
		dst, _ := w.Address(0)
		sub, _ := w.Address(1)
		sd, _ := w.Uint64(2)
		dd, _ := w.Uint64(3)
		tok, _ := w.Uint64(4)
		wei, _ := w.Uint64(5)
		return fmt.Sprintf("sendAsset(dest=%s, sub=%s, srcDex=%s, dstDex=%s, token=%d, wei=%d)", dst.Hex(), sub.Hex(), dexName(sd), dexName(dd), tok, wei)
	}
	return fmt.Sprintf("action %d (%d field words)", d.ActionID, w.Len())
}

func dexName(d uint64) string {
	if d == uint64(DexSpot) {
		return "spot"
	}
	return strconv.FormatUint(d, 10)
}

func decStr(v uint64, dec int) string {
	r := new(big.Rat).SetFrac(new(big.Int).SetUint64(v), evm.Pow10(dec))
	return strings.TrimRight(strings.TrimRight(r.FloatString(dec), "0"), ".")
}
