package evm

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"unicode/utf8"
)

// WordSize is the ABI word size in bytes.
const WordSize = 32

// Address is a 20-byte EVM account address.
type Address [20]byte

// ParseAddress parses a 0x-prefixed 40-hex-digit address (any case; the
// EIP-55 checksum is not verified).
func ParseAddress(s string) (Address, error) {
	var a Address
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "0x") && !strings.HasPrefix(s, "0X") {
		return a, fmt.Errorf("address %q: missing 0x prefix", s)
	}
	if len(s) != 42 {
		return a, fmt.Errorf("address %q: want 40 hex digits", s)
	}
	if _, err := hex.Decode(a[:], []byte(s[2:])); err != nil {
		return a, fmt.Errorf("address %q: %v", s, err)
	}
	return a, nil
}

// MustAddress is ParseAddress for constants; it panics on bad input.
func MustAddress(s string) Address {
	a, err := ParseAddress(s)
	if err != nil {
		panic(err)
	}
	return a
}

// IsAddress reports whether s parses as an address.
func IsAddress(s string) bool {
	_, err := ParseAddress(s)
	return err == nil
}

// Hex is the lowercase 0x form.
func (a Address) Hex() string { return "0x" + hex.EncodeToString(a[:]) }

// String implements fmt.Stringer.
func (a Address) String() string { return a.Hex() }

// IsZero reports the zero address.
func (a Address) IsZero() bool { return a == Address{} }

// MarshalText encodes the lowercase 0x form (JSON string).
func (a Address) MarshalText() ([]byte, error) { return []byte(a.Hex()), nil }

// UnmarshalText parses a 0x address.
func (a *Address) UnmarshalText(b []byte) error {
	v, err := ParseAddress(string(b))
	if err != nil {
		return err
	}
	*a = v
	return nil
}

// Selector is a 4-byte function, error or (truncated) event selector.
type Selector [4]byte

// MustSelector parses "0x12345678"; it panics on bad input (constants only).
func MustSelector(s string) Selector {
	b, err := DecodeHex(s)
	if err != nil || len(b) != 4 {
		panic(fmt.Sprintf("evm: bad selector %q", s))
	}
	var sel Selector
	copy(sel[:], b)
	return sel
}

// Hex is the 0x form.
func (s Selector) Hex() string { return "0x" + hex.EncodeToString(s[:]) }

// String implements fmt.Stringer.
func (s Selector) String() string { return s.Hex() }

// DecodeHex decodes hex with an optional 0x prefix. "0x" and "" are empty.
func DecodeHex(s string) ([]byte, error) {
	s = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "0x"), "0X")
	if len(s)%2 == 1 {
		return nil, fmt.Errorf("odd-length hex %q", truncate(s, 16))
	}
	return hex.DecodeString(s)
}

// EncodeHex is the 0x-prefixed lowercase encoding.
func EncodeHex(b []byte) string { return "0x" + hex.EncodeToString(b) }

// Word is one 32-byte ABI word.
type Word [WordSize]byte

// AddressWord left-pads an address to a word.
func AddressWord(a Address) Word {
	var w Word
	copy(w[12:], a[:])
	return w
}

// Uint64Word is a big-endian, left-padded uint64.
func Uint64Word(v uint64) Word {
	var w Word
	for i := 0; i < 8; i++ {
		w[31-i] = byte(v >> (8 * i))
	}
	return w
}

// UintWord encodes a non-negative integer below 2^256.
func UintWord(v *big.Int) (Word, error) {
	var w Word
	if v == nil || v.Sign() < 0 || v.BitLen() > 256 {
		return w, errors.New("uint256 out of range")
	}
	v.FillBytes(w[:])
	return w, nil
}

// BoolWord is 0 or 1.
func BoolWord(b bool) Word {
	var w Word
	if b {
		w[31] = 1
	}
	return w
}

// Calldata is selector ‖ words. Only static arguments are supported; none
// of the strategy-vault reads takes a dynamic one.
func Calldata(sel Selector, args ...Word) []byte {
	out := make([]byte, 0, 4+WordSize*len(args))
	out = append(out, sel[:]...)
	for _, w := range args {
		out = append(out, w[:]...)
	}
	return out
}

// DecodeError reports malformed ABI return data.
type DecodeError struct{ Msg string }

func (e *DecodeError) Error() string { return "abi decode: " + e.Msg }

func decodeErr(format string, a ...any) error { return &DecodeError{Msg: fmt.Sprintf(format, a...)} }

// Words is ABI return data viewed as 32-byte words, with bounds-checked
// accessors. Indices are word indices.
type Words []byte

// NewWords checks that b is whole words.
func NewWords(b []byte) (Words, error) {
	if len(b)%WordSize != 0 {
		return nil, decodeErr("length %d is not a multiple of 32", len(b))
	}
	return Words(b), nil
}

// ExpectWords checks that b is exactly n words. Static structs use it so a
// layout change fails loudly instead of being misread.
func ExpectWords(b []byte, n int) (Words, error) {
	if len(b) != n*WordSize {
		return nil, decodeErr("got %d bytes, want exactly %d (%d words)", len(b), n*WordSize, n)
	}
	return Words(b), nil
}

// Len is the number of words.
func (w Words) Len() int { return len(w) / WordSize }

// Word returns word i.
func (w Words) Word(i int) ([]byte, error) {
	if i < 0 || (i+1)*WordSize > len(w) {
		return nil, decodeErr("word %d out of range (%d words)", i, w.Len())
	}
	return w[i*WordSize : (i+1)*WordSize], nil
}

// Uint is word i as an unsigned integer.
func (w Words) Uint(i int) (*big.Int, error) {
	b, err := w.Word(i)
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(b), nil
}

// Uint64 is word i; it errors if the value does not fit in 64 bits.
func (w Words) Uint64(i int) (uint64, error) {
	b, err := w.Word(i)
	if err != nil {
		return 0, err
	}
	for _, x := range b[:24] {
		if x != 0 {
			return 0, decodeErr("word %d does not fit in uint64", i)
		}
	}
	var v uint64
	for _, x := range b[24:] {
		v = v<<8 | uint64(x)
	}
	return v, nil
}

// Uint8 is word i; it errors above 255.
func (w Words) Uint8(i int) (uint8, error) {
	v, err := w.Uint64(i)
	if err != nil {
		return 0, err
	}
	if v > 255 {
		return 0, decodeErr("word %d does not fit in uint8", i)
	}
	return uint8(v), nil
}

var two256 = new(big.Int).Lsh(big.NewInt(1), 256)

// Int is word i as a two's-complement int256.
func (w Words) Int(i int) (*big.Int, error) {
	b, err := w.Word(i)
	if err != nil {
		return nil, err
	}
	v := new(big.Int).SetBytes(b)
	if b[0]&0x80 != 0 {
		v.Sub(v, two256)
	}
	return v, nil
}

// Bool is word i; it must be exactly 0 or 1.
func (w Words) Bool(i int) (bool, error) {
	v, err := w.Uint64(i)
	if err != nil {
		return false, err
	}
	switch v {
	case 0:
		return false, nil
	case 1:
		return true, nil
	}
	return false, decodeErr("word %d is not a bool (%d)", i, v)
}

// Address is word i; its first 12 bytes must be zero.
func (w Words) Address(i int) (Address, error) {
	var a Address
	b, err := w.Word(i)
	if err != nil {
		return a, err
	}
	for _, x := range b[:12] {
		if x != 0 {
			return a, decodeErr("word %d is not an address", i)
		}
	}
	copy(a[:], b[12:])
	return a, nil
}

// offset reads word i as a byte offset into w and checks alignment/range.
func (w Words) offset(i int) (int, error) {
	off, err := w.Uint64(i)
	if err != nil {
		return 0, err
	}
	if off%WordSize != 0 || off >= uint64(len(w)) {
		return 0, decodeErr("bad offset %d at word %d", off, i)
	}
	return int(off), nil
}

// Bytes reads a dynamic bytes value whose head offset is word i.
func (w Words) Bytes(i int) ([]byte, error) {
	off, err := w.offset(i)
	if err != nil {
		return nil, err
	}
	n, err := Words(w[off:]).Uint64(0)
	if err != nil {
		return nil, err
	}
	start := off + WordSize
	if n > uint64(len(w)-start) {
		return nil, decodeErr("bytes length %d overruns data", n)
	}
	return append([]byte(nil), w[start:start+int(n)]...), nil
}

// String reads a dynamic string whose head offset is word i. Invalid UTF-8
// is rejected.
func (w Words) String(i int) (string, error) {
	b, err := w.Bytes(i)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) {
		return "", decodeErr("string is not valid UTF-8")
	}
	return string(b), nil
}

// TupleArray reads a dynamic array of static tuples (tupleWords words each)
// whose head offset is word i. It returns one Words per element and checks
// that the data ends exactly after the last element.
func (w Words) TupleArray(i, tupleWords int) ([]Words, error) {
	if tupleWords <= 0 {
		return nil, decodeErr("tuple size %d", tupleWords)
	}
	off, err := w.offset(i)
	if err != nil {
		return nil, err
	}
	n, err := Words(w[off:]).Uint64(0)
	if err != nil {
		return nil, err
	}
	start := off + WordSize
	size := tupleWords * WordSize
	if n > uint64((len(w)-start)/size) {
		return nil, decodeErr("array length %d overruns data", n)
	}
	if want := start + int(n)*size; want != len(w) {
		return nil, decodeErr("array of %d×%d words: got %d bytes, want %d", n, tupleWords, len(w), want)
	}
	out := make([]Words, n)
	for k := range out {
		out[k] = w[start+k*size : start+(k+1)*size]
	}
	return out, nil
}

// Big is a *big.Int that marshals to JSON as a decimal string, so 1e18
// fixed-point values survive JavaScript and jq. The zero value reads as 0.
type Big struct{ *big.Int }

// NewBig wraps v (nil becomes 0).
func NewBig(v *big.Int) Big {
	if v == nil {
		v = new(big.Int)
	}
	return Big{v}
}

// BigInt64 is a convenience constructor.
func BigInt64(v int64) Big { return Big{big.NewInt(v)} }

// V returns the value, never nil.
func (b Big) V() *big.Int {
	if b.Int == nil {
		return new(big.Int)
	}
	return b.Int
}

// String is the decimal form.
func (b Big) String() string { return b.V().String() }

// MarshalJSON writes a quoted decimal.
func (b Big) MarshalJSON() ([]byte, error) { return json.Marshal(b.V().String()) }

// UnmarshalJSON accepts a quoted or bare decimal.
func (b *Big) UnmarshalJSON(p []byte) error {
	s := strings.Trim(strings.TrimSpace(string(p)), `"`)
	if s == "" || s == "null" {
		b.Int = new(big.Int)
		return nil
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return fmt.Errorf("bad integer %q", truncate(s, 40))
	}
	b.Int = v
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
