// Package safe reads contract accounts on HyperEVM (Safe multisigs and the
// Zodiac modules on them) and builds, never signs or sends, the Safe
// transactions a contract account needs to trade on HyperCore.
//
// Everything here is written from public specs: the Safe EIP-712 SafeTx
// type, the Zodiac Roles execTransactionWithRole ABI and HL's CoreWriter
// wire format. hlp holds no EVM key; the Safe's owners execute the output
// in Safe{Wallet} or Zodiac Pilot.
package safe

import (
	"errors"
	"math/big"
	"strconv"
	"strings"

	"golang.org/x/crypto/sha3"

	"github.com/maxmoneycash/hlp-cli/internal/evm"
)

// Operation is a Safe transaction's call type.
type Operation uint8

// Operations.
const (
	Call         Operation = 0
	DelegateCall Operation = 1
)

// Type hashes (Safe ≥ 1.0: SafeTx with baseGas).
var (
	SafeTxTypeHash = keccak([]byte("SafeTx(address to,uint256 value,bytes data,uint8 operation,uint256 safeTxGas,uint256 baseGas,uint256 gasPrice,address gasToken,address refundReceiver,uint256 nonce)"))
	// DomainTypeHash is the Safe ≥ 1.3.0 domain (chain-bound).
	DomainTypeHash = keccak([]byte("EIP712Domain(uint256 chainId,address verifyingContract)"))
	// LegacyDomainTypeHash is the Safe < 1.3.0 domain (no chainId).
	LegacyDomainTypeHash = keccak([]byte("EIP712Domain(address verifyingContract)"))
)

// Selectors used here (checked against keccak in the tests).
var (
	selVersion             = evm.MustSelector("0xffa1ad74") // VERSION()
	selGetOwners           = evm.MustSelector("0xa0e67e2b") // getOwners()
	selGetThreshold        = evm.MustSelector("0xe75235b8") // getThreshold()
	selNonce               = evm.MustSelector("0xaffed0e0") // nonce()
	selGetModulesPaginated = evm.MustSelector("0xcc2f8452") // getModulesPaginated(address,uint256)
	selDomainSeparator     = evm.MustSelector("0xf698da25") // domainSeparator()
	selGetTransactionHash  = evm.MustSelector("0xd8d11f78") // getTransactionHash(address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,uint256)
	selAvatar              = evm.MustSelector("0x5aef7de6") // avatar()
	selTarget              = evm.MustSelector("0xd4b83992") // target()
	selOwner               = evm.MustSelector("0x8da5cb5b") // owner()
	selTxCooldown          = evm.MustSelector("0xdcafac09") // txCooldown()
	selTxExpiration        = evm.MustSelector("0x605df59c") // txExpiration()
	selDefaultRoles        = evm.MustSelector("0x2d3c2547") // defaultRoles(address)
	// SelExecTransactionWithRole is Zodiac Roles
	// execTransactionWithRole(address,uint256,bytes,uint8,bytes32,bool).
	SelExecTransactionWithRole = evm.MustSelector("0xc6fe8747")
)

// selectorSigs is every selector above with its signature (tests).
var selectorSigs = map[evm.Selector]string{
	selVersion: "VERSION()", selGetOwners: "getOwners()", selGetThreshold: "getThreshold()", selNonce: "nonce()",
	selGetModulesPaginated: "getModulesPaginated(address,uint256)", selDomainSeparator: "domainSeparator()",
	selGetTransactionHash: "getTransactionHash(address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,uint256)",
	selAvatar:             "avatar()", selTarget: "target()", selOwner: "owner()", selTxCooldown: "txCooldown()",
	selTxExpiration: "txExpiration()", selDefaultRoles: "defaultRoles(address)",
	SelExecTransactionWithRole: "execTransactionWithRole(address,uint256,bytes,uint8,bytes32,bool)",
}

// Tx is a Safe transaction (SafeTx). Gas refund fields are zero for an
// owner-executed transaction, which is what hlp builds.
type Tx struct {
	To             evm.Address
	Value          *big.Int
	Data           []byte
	Operation      Operation
	SafeTxGas      *big.Int
	BaseGas        *big.Int
	GasPrice       *big.Int
	GasToken       evm.Address
	RefundReceiver evm.Address
	Nonce          uint64
}

func keccak(parts ...[]byte) [32]byte {
	h := sha3.NewLegacyKeccak256()
	for _, p := range parts {
		h.Write(p)
	}
	var out [32]byte
	h.Sum(out[:0])
	return out
}

func uintWord(v *big.Int) evm.Word {
	if v == nil {
		return evm.Word{}
	}
	w, err := evm.UintWord(v)
	if err != nil {
		return evm.Word{}
	}
	return w
}

// ChainBoundDomain reports whether a Safe version uses the chainId domain
// (1.3.0 and later). An unparsable version is treated as modern.
func ChainBoundDomain(version string) bool {
	maj, min, ok := parseVersion(version)
	if !ok {
		return true
	}
	return maj > 1 || (maj == 1 && min >= 3)
}

func parseVersion(v string) (int, int, bool) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	maj, e1 := strconv.Atoi(parts[0])
	min, e2 := strconv.Atoi(parts[1])
	return maj, min, e1 == nil && e2 == nil
}

// DomainSeparator is the Safe's EIP-712 domain separator.
func DomainSeparator(chainID uint64, safe evm.Address, version string) [32]byte {
	if !ChainBoundDomain(version) {
		a := evm.AddressWord(safe)
		return keccak(LegacyDomainTypeHash[:], a[:])
	}
	c, a := evm.Uint64Word(chainID), evm.AddressWord(safe)
	return keccak(DomainTypeHash[:], c[:], a[:])
}

// StructHash is hashStruct(SafeTx).
func (t Tx) StructHash() [32]byte {
	dh := keccak(t.Data)
	words := []evm.Word{evm.AddressWord(t.To), uintWord(t.Value), dh, evm.Uint64Word(uint64(t.Operation)),
		uintWord(t.SafeTxGas), uintWord(t.BaseGas), uintWord(t.GasPrice), evm.AddressWord(t.GasToken),
		evm.AddressWord(t.RefundReceiver), evm.Uint64Word(t.Nonce)}
	buf := append([]byte(nil), SafeTxTypeHash[:]...)
	for _, w := range words {
		buf = append(buf, w[:]...)
	}
	return keccak(buf)
}

// Hash is the safeTxHash owners sign (and compare on hardware wallets):
// keccak(0x19 0x01 ‖ domainSeparator ‖ hashStruct(SafeTx)).
func (t Tx) Hash(chainID uint64, safe evm.Address, version string) [32]byte {
	d, s := DomainSeparator(chainID, safe, version), t.StructHash()
	return keccak([]byte{0x19, 0x01}, d[:], s[:])
}

// arg is one ABI argument: a static word or dynamic bytes.
type arg struct {
	w   evm.Word
	dyn []byte
	isD bool
}

func static(w evm.Word) arg  { return arg{w: w} }
func dynamic(b []byte) arg   { return arg{dyn: b, isD: true} }
func pad32(n int) int        { return (n + 31) / 32 * 32 }
func word(v uint64) evm.Word { return evm.Uint64Word(v) }

// encodeCall is selector ‖ abi.encode(args) with dynamic bytes support.
func encodeCall(sel evm.Selector, args ...arg) []byte {
	head := make([]byte, 0, 32*len(args))
	var tail []byte
	for _, a := range args {
		if !a.isD {
			head = append(head, a.w[:]...)
			continue
		}
		off := word(uint64(32*len(args) + len(tail)))
		head = append(head, off[:]...)
		n := word(uint64(len(a.dyn)))
		tail = append(tail, n[:]...)
		tail = append(tail, a.dyn...)
		tail = append(tail, make([]byte, pad32(len(a.dyn))-len(a.dyn))...)
	}
	out := append([]byte(nil), sel[:]...)
	return append(append(out, head...), tail...)
}

// GetTransactionHashCalldata is Safe.getTransactionHash(...) for t: an
// eth_call of it returns the Safe's own safeTxHash, a cross-check of Hash.
func (t Tx) GetTransactionHashCalldata() []byte {
	return encodeCall(selGetTransactionHash, static(evm.AddressWord(t.To)), static(uintWord(t.Value)), dynamic(t.Data),
		static(word(uint64(t.Operation))), static(uintWord(t.SafeTxGas)), static(uintWord(t.BaseGas)), static(uintWord(t.GasPrice)),
		static(evm.AddressWord(t.GasToken)), static(evm.AddressWord(t.RefundReceiver)), static(word(t.Nonce)))
}

// ExecTransactionWithRoleCalldata is the Zodiac Roles call a role member
// sends to the Roles modifier instead of collecting owner signatures.
// shouldRevert makes a failed inner call revert the whole transaction.
func ExecTransactionWithRoleCalldata(to evm.Address, value *big.Int, data []byte, op Operation, roleKey [32]byte, shouldRevert bool) []byte {
	return encodeCall(SelExecTransactionWithRole, static(evm.AddressWord(to)), static(uintWord(value)), dynamic(data),
		static(word(uint64(op))), static(roleKey), static(evm.BoolWord(shouldRevert)))
}

// ParseRoleKey accepts a 0x-prefixed 32-byte hex key, or a label of at most
// 31 bytes encoded as the Roles SDK does (bytes32 string, left-aligned).
func ParseRoleKey(s string) ([32]byte, error) {
	var k [32]byte
	s = strings.TrimSpace(s)
	if s == "" {
		return k, errors.New("role key is empty")
	}
	if strings.HasPrefix(strings.ToLower(s), "0x") && len(s) == 66 {
		b, err := evm.DecodeHex(s)
		if err != nil {
			return k, err
		}
		copy(k[:], b)
		return k, nil
	}
	if len(s) > 31 {
		return k, errors.New("role key label must be at most 31 bytes, or 0x + 64 hex")
	}
	copy(k[:], s)
	return k, nil
}
