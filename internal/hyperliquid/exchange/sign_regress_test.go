package exchange

// Regression tests for the signing input checks. Core
// validates these inputs upstream, but the strategy-vault bots import this
// package directly. No network: signing only, with the public SDK test key.

import (
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// upKey is the Hyperliquid Python SDK's PUBLIC test key.
const upKey = "0x0123456789012345678901234567890123456789012345678901234567890123"

// upNoPanic runs f and turns a panic into a test failure.
func upNoPanic(t *testing.T, what string, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s panicked: %v", what, r)
		}
	}()
	f()
}

const upNonHex = "0xzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz" // 40 chars, not hex

// parseAddress refuses a 40-char address that is not hex.
func TestRegressParseAddressRejectsNonHex(t *testing.T) {
	for _, a := range []string{upNonHex, "0x12345678901234567890123456789012345678zz"} {
		if _, err := parseAddress(a); !errors.Is(err, ErrBadAddress) {
			t.Errorf("parseAddress(%q): %v", a, err)
		}
	}
	if _, err := parseAddress("0x" + strings.Repeat("Ab", 20)); err != nil {
		t.Fatalf("mixed-case hex: %v", err)
	}
}

// Signing for a malformed vault fails instead of signing for a partly
// zeroed address.
func TestRegressSignRejectsBadVault(t *testing.T) {
	s, err := NewKeySigner(upKey)
	if err != nil {
		t.Fatal(err)
	}
	c := NewHTTPClient("http://127.0.0.1:1/exchange", s, nil)
	act := CancelAction{Type: "cancel", Cancels: []Cancel{{A: 0, O: 1}}}
	for _, v := range []string{upNonHex, "0x12345678901234567890123456789012345678zz"} {
		v := v
		if e, err := c.Sign(act, &v, false); err == nil {
			t.Errorf("signed for vault %q as %v", v, *e.VaultAddress)
		}
		if _, err := c.SignExpired(act, &v, false); err == nil {
			t.Errorf("signed expired for vault %q", v)
		}
	}
}

// EIP-712 uints: negative ints are refused (and say so); out-of-range
// big.Ints are refused without panicking.
func TestRegressEncodeUintRange(t *testing.T) {
	for _, v := range []any{-1, int64(-1)} {
		_, err := encodeValue("uint64", v)
		if err == nil || !strings.Contains(err.Error(), "negative") {
			t.Errorf("encodeValue(uint64, %T -1): %v, want a negative error", v, err)
		}
	}
	tooBig := new(big.Int).Lsh(big.NewInt(1), 256) // 2^256: 257 bits
	for name, v := range map[string]*big.Int{"2^256": tooBig, "-1": big.NewInt(-1)} {
		upNoPanic(t, "encodeValue "+name, func() {
			if _, err := encodeValue("uint256", v); err == nil {
				t.Errorf("encodeValue(uint256, %s) accepted", name)
			}
		})
	}
	max := new(big.Int).Sub(tooBig, big.NewInt(1))
	if _, err := encodeValue("uint256", max); err != nil {
		t.Fatalf("2^256-1: %v", err)
	}
}

// HashStruct names a missing field rather than hashing a nil.
func TestRegressHashStructMissingField(t *testing.T) {
	fields := []TypedField{{"destination", "string"}, {"amount", "string"}, {"time", "uint64"}}
	_, err := HashStruct("UsdSend", fields, map[string]any{"destination": "0x0", "time": uint64(1)})
	if err == nil || !strings.Contains(err.Error(), `missing field "amount"`) {
		t.Fatalf("missing field: %v", err)
	}
}

// RecoverAddress accepts only v = 27 or 28.
func TestRegressRecoverRejectsBadV(t *testing.T) {
	s, err := NewKeySigner(upKey)
	if err != nil {
		t.Fatal(err)
	}
	var d [32]byte
	d[0] = 1
	sig := s.SignDigest(d)
	if a, err := RecoverAddress(d, sig); err != nil || a != s.Address() {
		t.Fatalf("recover good sig: %s %v", a, err)
	}
	for _, v := range []int{0, 1, 26, 29, 31, 32, 35} {
		bad := sig
		bad.V = v
		upNoPanic(t, "RecoverAddress", func() {
			if a, err := RecoverAddress(d, bad); err == nil {
				t.Errorf("v=%d accepted (recovered %s)", v, a)
			}
		})
	}
}

// LimitOrder refuses a non-positive price or size.
func TestRegressLimitOrderPositive(t *testing.T) {
	one := decimal.NewFromInt(1)
	for _, tc := range []struct{ px, sz decimal.Decimal }{
		{decimal.Zero, one}, {one, decimal.Zero}, {decimal.NewFromInt(-1), one}, {one, decimal.NewFromInt(-1)},
	} {
		if _, err := LimitOrder(0, true, tc.px, tc.sz, TifGtc, false, ""); err == nil {
			t.Errorf("LimitOrder px %s sz %s accepted", tc.px, tc.sz)
		}
		if _, err := TriggerOrder(0, true, tc.px, tc.sz, one, true, "tp", true, ""); err == nil {
			t.Errorf("TriggerOrder px %s sz %s accepted", tc.px, tc.sz)
		}
	}
	if _, err := LimitOrder(0, true, one, one, TifGtc, false, ""); err != nil {
		t.Fatal(err)
	}
}
