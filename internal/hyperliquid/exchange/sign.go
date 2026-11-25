package exchange

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/vmihailenco/msgpack/v5"
	"golang.org/x/crypto/sha3"
)

// ErrBadKey is returned for malformed private keys.
var ErrBadKey = errors.New("invalid private key")

// ErrBadAddress is returned for malformed 20-byte hex addresses.
var ErrBadAddress = errors.New("invalid address")

func keccak(parts ...[]byte) [32]byte {
	h := sha3.NewLegacyKeccak256()
	for _, p := range parts {
		h.Write(p)
	}
	var out [32]byte
	h.Sum(out[:0])
	return out
}

// EncodeAction msgpack-encodes an action exactly as the Python SDK does:
// struct field order preserved, optional keys omitted, positive integers
// as the smallest msgpack uint.
func EncodeAction(action any) ([]byte, error) {
	var buf bytes.Buffer
	enc := msgpack.NewEncoder(&buf)
	enc.UseCompactInts(true)
	if err := enc.Encode(action); err != nil {
		return nil, fmt.Errorf("exchange: msgpack: %w", err)
	}
	return buf.Bytes(), nil
}

// parseAddress decodes a 0x + 40 hex address (case-insensitive).
func parseAddress(a string) ([20]byte, error) {
	var out [20]byte
	s := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(a)), "0x")
	if len(s) != 40 {
		return out, fmt.Errorf("%w: %q", ErrBadAddress, a)
	}
	if _, err := hex.Decode(out[:], []byte(s)); err != nil {
		return out, fmt.Errorf("%w: %q", ErrBadAddress, a)
	}
	return out, nil
}

// ConnectionID exposes the hash for golden tests (8.3 production vector:
// 0x0fcbeda5ae3c4950a548021552a4fea2226858c4453571bf3f24ba017eac2908).
//
// connectionId = keccak(msgpack(action) || nonce u64be ||
// (vault == nil ? 0x00 : 0x01 || vault20) || [0x00 || expiresAfter u64be]).
func ConnectionID(action any, nonce uint64, vault *string, expiresAfter *uint64) ([32]byte, error) {
	data, err := EncodeAction(action)
	if err != nil {
		return [32]byte{}, err
	}
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], nonce)
	data = append(data, n[:]...)
	if vault == nil {
		data = append(data, 0x00)
	} else {
		addr, err := parseAddress(*vault)
		if err != nil {
			return [32]byte{}, err
		}
		data = append(data, 0x01)
		data = append(data, addr[:]...)
	}
	if expiresAfter != nil {
		var e [8]byte
		binary.BigEndian.PutUint64(e[:], *expiresAfter)
		data = append(data, 0x00)
		data = append(data, e[:]...)
	}
	return keccak(data), nil
}

// --- EIP-712 ---

// TypedField is one EIP-712 struct member.
type TypedField struct {
	Name string
	Type string // string | address | bool | bytes | bytes32 | uint8..uint256
}

// typeString renders "Name(type1 name1,type2 name2)".
func typeString(primary string, fields []TypedField) string {
	var b strings.Builder
	b.WriteString(primary)
	b.WriteByte('(')
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(f.Type)
		b.WriteByte(' ')
		b.WriteString(f.Name)
	}
	b.WriteByte(')')
	return b.String()
}

// encodeValue returns the 32-byte EIP-712 encoding of an atomic value.
func encodeValue(typ string, v any) ([32]byte, error) {
	var out [32]byte
	switch {
	case typ == "string":
		s, ok := v.(string)
		if !ok {
			return out, fmt.Errorf("eip712: %s wants string, got %T", typ, v)
		}
		return keccak([]byte(s)), nil
	case typ == "bytes":
		var b []byte
		switch x := v.(type) {
		case []byte:
			b = x
		case string:
			d, err := hex.DecodeString(strings.TrimPrefix(x, "0x"))
			if err != nil {
				return out, fmt.Errorf("eip712: bytes: %w", err)
			}
			b = d
		default:
			return out, fmt.Errorf("eip712: bytes wants []byte or hex, got %T", v)
		}
		return keccak(b), nil
	case typ == "bytes32":
		switch x := v.(type) {
		case [32]byte:
			return x, nil
		case []byte:
			if len(x) != 32 {
				return out, fmt.Errorf("eip712: bytes32 length %d", len(x))
			}
			copy(out[:], x)
			return out, nil
		}
		return out, fmt.Errorf("eip712: bytes32 wants [32]byte, got %T", v)
	case typ == "address":
		s, ok := v.(string)
		if !ok {
			return out, fmt.Errorf("eip712: address wants string, got %T", v)
		}
		a, err := parseAddress(s)
		if err != nil {
			return out, err
		}
		copy(out[12:], a[:])
		return out, nil
	case typ == "bool":
		b, ok := v.(bool)
		if !ok {
			return out, fmt.Errorf("eip712: bool wants bool, got %T", v)
		}
		if b {
			out[31] = 1
		}
		return out, nil
	case strings.HasPrefix(typ, "uint"):
		var n big.Int
		switch x := v.(type) {
		case uint64:
			n.SetUint64(x)
		case uint32:
			n.SetUint64(uint64(x))
		case int:
			if x < 0 {
				return out, fmt.Errorf("eip712: negative %s", typ)
			}
			n.SetInt64(int64(x))
		case int64:
			if x < 0 {
				return out, fmt.Errorf("eip712: negative %s", typ)
			}
			n.SetInt64(x)
		case *big.Int:
			n.Set(x)
		default:
			return out, fmt.Errorf("eip712: %s wants an unsigned integer, got %T", typ, v)
		}
		if n.Sign() < 0 || n.BitLen() > 256 {
			return out, fmt.Errorf("eip712: %s out of range", typ)
		}
		n.FillBytes(out[:])
		return out, nil
	}
	return out, fmt.Errorf("eip712: unsupported type %q", typ)
}

// HashStruct computes keccak(typeHash || enc(v1) || ...) for a flat struct.
func HashStruct(primary string, fields []TypedField, values map[string]any) ([32]byte, error) {
	th := keccak([]byte(typeString(primary, fields)))
	buf := make([]byte, 0, 32*(len(fields)+1))
	buf = append(buf, th[:]...)
	for _, f := range fields {
		v, ok := values[f.Name]
		if !ok {
			return [32]byte{}, fmt.Errorf("eip712: missing field %q", f.Name)
		}
		e, err := encodeValue(f.Type, v)
		if err != nil {
			return [32]byte{}, fmt.Errorf("eip712: %s: %w", f.Name, err)
		}
		buf = append(buf, e[:]...)
	}
	return keccak(buf), nil
}

var domainFields = []TypedField{
	{"name", "string"}, {"version", "string"}, {"chainId", "uint256"}, {"verifyingContract", "address"},
}

const zeroAddress = "0x0000000000000000000000000000000000000000"

// DomainSeparator hashes an EIP712Domain{name, version, chainId, 0x0}.
func DomainSeparator(name, version string, chainID uint64) ([32]byte, error) {
	return HashStruct("EIP712Domain", domainFields, map[string]any{
		"name": name, "version": version, "chainId": chainID, "verifyingContract": zeroAddress,
	})
}

// TypedDigest is keccak(0x19 0x01 || domainSeparator || structHash).
func TypedDigest(domain, structHash [32]byte) [32]byte {
	return keccak([]byte{0x19, 0x01}, domain[:], structHash[:])
}

var agentFields = []TypedField{{"source", "string"}, {"connectionId", "bytes32"}}

// L1Digest is the EIP-712 digest of Agent{source, connectionId} on domain
// Exchange/1/1337/0x0. source is "a" on mainnet and "b" on testnet.
func L1Digest(connectionID [32]byte, mainnet bool) [32]byte {
	src := "b"
	if mainnet {
		src = "a"
	}
	dom, _ := DomainSeparator("Exchange", "1", 1337)
	sh, _ := HashStruct("Agent", agentFields, map[string]any{"source": src, "connectionId": connectionID})
	return TypedDigest(dom, sh)
}

// UserSignedType describes one user-signed action (Python *_SIGN_TYPES).
type UserSignedType struct {
	PrimaryType string
	Fields      []TypedField
}

// UserSignedTypes lists the user-signed actions by action "type"
// (EIP-712 typed data). Only a master wallet may sign these.
var UserSignedTypes = map[string]UserSignedType{
	"approveAgent": {"HyperliquidTransaction:ApproveAgent", []TypedField{
		{"hyperliquidChain", "string"}, {"agentAddress", "address"}, {"agentName", "string"}, {"nonce", "uint64"}}},
	"approveBuilderFee": {"HyperliquidTransaction:ApproveBuilderFee", []TypedField{
		{"hyperliquidChain", "string"}, {"maxFeeRate", "string"}, {"builder", "address"}, {"nonce", "uint64"}}},
	"usdSend": {"HyperliquidTransaction:UsdSend", []TypedField{
		{"hyperliquidChain", "string"}, {"destination", "string"}, {"amount", "string"}, {"time", "uint64"}}},
	"spotSend": {"HyperliquidTransaction:SpotSend", []TypedField{
		{"hyperliquidChain", "string"}, {"destination", "string"}, {"token", "string"}, {"amount", "string"}, {"time", "uint64"}}},
	"withdraw3": {"HyperliquidTransaction:Withdraw", []TypedField{
		{"hyperliquidChain", "string"}, {"destination", "string"}, {"amount", "string"}, {"time", "uint64"}}},
	"usdClassTransfer": {"HyperliquidTransaction:UsdClassTransfer", []TypedField{
		{"hyperliquidChain", "string"}, {"amount", "string"}, {"toPerp", "bool"}, {"nonce", "uint64"}}},
	"tokenDelegate": {"HyperliquidTransaction:TokenDelegate", []TypedField{
		{"hyperliquidChain", "string"}, {"validator", "address"}, {"wei", "uint64"}, {"isUndelegate", "bool"}, {"nonce", "uint64"}}},
}

// UserSignedDigest computes the EIP-712 digest of a user-signed action on
// domain HyperliquidSignTransaction/1/<chainID>/0x0.
func UserSignedDigest(t UserSignedType, values map[string]any, chainID uint64) ([32]byte, error) {
	dom, err := DomainSeparator("HyperliquidSignTransaction", "1", chainID)
	if err != nil {
		return [32]byte{}, err
	}
	sh, err := HashStruct(t.PrimaryType, t.Fields, values)
	if err != nil {
		return [32]byte{}, err
	}
	return TypedDigest(dom, sh), nil
}

// --- keys ---

// KeySigner signs with an in-memory secp256k1 key. The key is never
// exported, printed or logged.
type KeySigner struct {
	key  *secp256k1.PrivateKey
	addr string
}

var _ Signer = (*KeySigner)(nil)

// NewSigner parses a hex private key. Test code must use the public SDK test key.
func NewSigner(hexKey string) (Signer, error) { return NewKeySigner(hexKey) }

// NewKeySigner is NewSigner returning the concrete type (for user-signed
// actions). Errors never echo the key.
func NewKeySigner(hexKey string) (*KeySigner, error) {
	s := strings.TrimPrefix(strings.TrimSpace(hexKey), "0x")
	if len(s) != 64 {
		return nil, ErrBadKey
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, ErrBadKey
	}
	k := secp256k1.PrivKeyFromBytes(b)
	for i := range b {
		b[i] = 0
	}
	if k.Key.IsZero() {
		return nil, ErrBadKey
	}
	return &KeySigner{key: k, addr: PubkeyAddress(k.PubKey())}, nil
}

// PubkeyAddress returns the lowercase 0x Ethereum address of a public key.
func PubkeyAddress(pub *secp256k1.PublicKey) string {
	u := pub.SerializeUncompressed()
	h := keccak(u[1:])
	return "0x" + hex.EncodeToString(h[12:])
}

// Address returns the lowercase 0x address of the key.
func (k *KeySigner) Address() string { return k.addr }

// String never reveals the key.
func (k *KeySigner) String() string { return "KeySigner(" + k.addr + ")" }

// SignDigest signs a 32-byte digest; v is 27 or 28.
func (k *KeySigner) SignDigest(digest [32]byte) Signature {
	sig := ecdsa.SignCompact(k.key, digest[:], false) // [27+recid, r(32), s(32)]
	r := new(big.Int).SetBytes(sig[1:33])
	s := new(big.Int).SetBytes(sig[33:65])
	// r/s rendered like the Python SDK's to_hex(int): no leading zeros.
	return Signature{R: "0x" + r.Text(16), S: "0x" + s.Text(16), V: int(sig[0])}
}

// SignL1 signs an L1 action (see Signer).
func (k *KeySigner) SignL1(action any, nonce uint64, vault *string, expiresAfter *uint64, mainnet bool) (Signature, error) {
	cid, err := ConnectionID(action, nonce, vault, expiresAfter)
	if err != nil {
		return Signature{}, err
	}
	return k.SignDigest(L1Digest(cid, mainnet)), nil
}

// SignUserAction signs a user-signed action (master wallet only; agents
// cannot). values must include hyperliquidChain and the nonce/time field.
func (k *KeySigner) SignUserAction(t UserSignedType, values map[string]any, chainID uint64) (Signature, error) {
	d, err := UserSignedDigest(t, values, chainID)
	if err != nil {
		return Signature{}, err
	}
	return k.SignDigest(d), nil
}

// RecoverAddress returns the signer address of sig over digest.
func RecoverAddress(digest [32]byte, sig Signature) (string, error) {
	r, ok1 := new(big.Int).SetString(strings.TrimPrefix(sig.R, "0x"), 16)
	s, ok2 := new(big.Int).SetString(strings.TrimPrefix(sig.S, "0x"), 16)
	if !ok1 || !ok2 || (sig.V != 27 && sig.V != 28) {
		return "", errors.New("exchange: malformed signature")
	}
	compact := make([]byte, 65)
	compact[0] = byte(sig.V)
	r.FillBytes(compact[1:33])
	s.FillBytes(compact[33:65])
	pub, _, err := ecdsa.RecoverCompact(compact, digest[:])
	if err != nil {
		return "", err
	}
	return PubkeyAddress(pub), nil
}
