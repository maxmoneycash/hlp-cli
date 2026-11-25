package safe

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/evm"
	"github.com/maxmoneycash/hlp-cli/internal/hyperevm/corewriter"
)

func TestSelectorsMatchKeccak(t *testing.T) {
	for sel, sig := range selectorSigs {
		k := keccak([]byte(sig))
		if evm.EncodeHex(k[:4]) != sel.Hex() {
			t.Errorf("%s: %s, keccak %x", sig, sel.Hex(), k[:4])
		}
	}
	if evm.EncodeHex(SafeTxTypeHash[:]) != "0xbb8310d486368db6bd6f849402fdd73ad53d316b5a4b2644ad6efe0f941286d8" {
		t.Error("SafeTx type hash")
	}
}

// Vectors computed independently with foundry (cast keccak / abi-encode):
//
//	DOM=$(cast keccak $(cast abi-encode 'f(bytes32,uint256,address)' $DOMAIN_TYPEHASH 999 $SAFE))
//	SH=$(cast keccak $(cast abi-encode 'f(bytes32,address,uint256,bytes32,uint8,uint256,uint256,uint256,address,address,uint256)' \
//	     $SAFETX_TYPEHASH 0x3333…3333 0 $(cast keccak $DATA) 0 0 0 0 0x0 0x0 7))
//	cast keccak 0x1901${DOM:2}${SH:2}
const (
	vecSafe     = "0x1111111111111111111111111111111111111111"
	vecDom      = "0xf49f3a3bbdd743f7f6e3dc55dac6e8b616ce67515fd5455bfbda9e6c7574724c"
	vecDomOld   = "0xc6172c436a72ad7286cf5e21dcc39bf417e7f8a6404b5f0f1c25c6a30b44831c"
	vecStruct   = "0x174c5d80eaf72478efd322148a82d39ec2e69a79d452f151dabf40ed1ff08ff6"
	vecHash     = "0x4037b40e12e3b32722ce39652e92bccd7471beae92a3029555d4cd8a38d1e541"
	vecHashOld  = "0xab081e9b22d39ab95f1c43d2b5da5b9077c5d783024a6483b5e2276217bbece9"
	vecRolesPfx = "0xc6fe87470000000000000000000000003333333333333333333333333333333333333333000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000c000000000000000000000000000000000000000000000000000000000000000006b65657065720000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000100000000000000000000000000000000000000000000000000000000000000e4"
)

var vecAgent = evm.MustAddress("0x7e57a6e0000000000000000000000000000000c1")

func vecData() []byte {
	return corewriter.SendRawActionCalldata(corewriter.AddAPIWallet(vecAgent, corewriter.AgentName("hlp", time.UnixMilli(1_790_000_000_000))))
}

func TestSafeTxHashVectors(t *testing.T) {
	safe := evm.MustAddress(vecSafe)
	tx := Tx{To: corewriter.Address, Value: new(big.Int), Data: vecData(), Nonce: 7}
	h := func(b [32]byte) string { return evm.EncodeHex(b[:]) }
	if got := h(DomainSeparator(999, safe, "1.4.1")); got != vecDom {
		t.Errorf("domain %s", got)
	}
	if got := h(DomainSeparator(999, safe, "1.2.0")); got != vecDomOld {
		t.Errorf("legacy domain %s", got)
	}
	if got := h(tx.StructHash()); got != vecStruct {
		t.Errorf("struct %s", got)
	}
	if got := h(tx.Hash(999, safe, "1.4.1")); got != vecHash {
		t.Errorf("hash %s", got)
	}
	if got := h(tx.Hash(999, safe, "1.1.1")); got != vecHashOld {
		t.Errorf("legacy hash %s", got)
	}
	key, err := ParseRoleKey("keeper")
	if err != nil {
		t.Fatal(err)
	}
	got := evm.EncodeHex(ExecTransactionWithRoleCalldata(corewriter.Address, new(big.Int), vecData(), Call, key, true))
	if !strings.HasPrefix(got, vecRolesPfx) || !strings.HasSuffix(got, strings.TrimPrefix(evm.EncodeHex(vecData()), "0x")+strings.Repeat("0", 56)) {
		t.Errorf("roles calldata %s", got)
	}
}

func TestParseRoleKey(t *testing.T) {
	if _, err := ParseRoleKey(strings.Repeat("x", 32)); err == nil {
		t.Error("32-byte label accepted")
	}
	k, err := ParseRoleKey("0x" + strings.Repeat("ab", 32))
	if err != nil || k[0] != 0xab || k[31] != 0xab {
		t.Error(k, err)
	}
	if ChainBoundDomain("1.2.0") || !ChainBoundDomain("1.3.0") || !ChainBoundDomain("1.4.1+L2") || !ChainBoundDomain("") {
		t.Error("version gate")
	}
}

// fakeRPC answers eth_call by (to, selector); unknown calls revert.
type fakeRPC struct {
	code  map[evm.Address][]byte
	calls map[string][]byte // to.Hex()+selector hex → result
	full  map[string][]byte // to.Hex()+full calldata hex → result
}

func (f *fakeRPC) key(to evm.Address, data []byte) string { return to.Hex() + evm.EncodeHex(data[:4]) }

func (f *fakeRPC) Call(_ context.Context, to evm.Address, data []byte) ([]byte, error) {
	if r, ok := f.full[to.Hex()+evm.EncodeHex(data)]; ok {
		return r, nil
	}
	if r, ok := f.calls[f.key(to, data)]; ok {
		return r, nil
	}
	return nil, &evm.RevertError{}
}

func (f *fakeRPC) BatchCall(ctx context.Context, msgs []evm.CallMsg) ([]evm.CallResult, error) {
	out := make([]evm.CallResult, len(msgs))
	for i, m := range msgs {
		out[i].Data, out[i].Err = f.Call(ctx, m.To, m.Data)
	}
	return out, nil
}

func (f *fakeRPC) GetCode(_ context.Context, a evm.Address) ([]byte, error) { return f.code[a], nil }

func words(ws ...evm.Word) []byte {
	var b []byte
	for _, w := range ws {
		b = append(b, w[:]...)
	}
	return b
}

func addrArray(head []evm.Word, as ...evm.Address) []byte {
	ws := append([]evm.Word(nil), head...)
	ws = append(ws, evm.Uint64Word(uint64(len(as))))
	for _, a := range as {
		ws = append(ws, evm.AddressWord(a))
	}
	return words(ws...)
}

func strRet(s string) []byte {
	b := words(evm.Uint64Word(32), evm.Uint64Word(uint64(len(s))))
	p := make([]byte, 32)
	copy(p, s)
	return append(b, p...)
}

var (
	owner1 = evm.MustAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	owner2 = evm.MustAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	owner3 = evm.MustAddress("0xcccccccccccccccccccccccccccccccccccccccc")
	rolesM = evm.MustAddress("0x9999999999999999999999999999999999999999")
	delayM = evm.MustAddress("0x8888888888888888888888888888888888888888")
)

func fakeSafe(t *testing.T, safe evm.Address) *fakeRPC {
	t.Helper()
	f := &fakeRPC{code: map[evm.Address][]byte{safe: {0x60, 0x80}, owner1: nil}, calls: map[string][]byte{}, full: map[string][]byte{}}
	set := func(to evm.Address, sel evm.Selector, b []byte) { f.calls[to.Hex()+sel.Hex()] = b }
	set(safe, selVersion, strRet("1.4.1"))
	set(safe, selGetOwners, addrArray([]evm.Word{evm.Uint64Word(32)}, owner1, owner2, owner3))
	set(safe, selGetThreshold, words(evm.Uint64Word(2)))
	set(safe, selNonce, words(evm.Uint64Word(7)))
	set(safe, selGetModulesPaginated, addrArray([]evm.Word{evm.Uint64Word(64), evm.AddressWord(sentinel)}, rolesM, delayM))
	dom := DomainSeparator(999, safe, "1.4.1")
	set(safe, selDomainSeparator, dom[:])
	for _, m := range []evm.Address{rolesM, delayM} {
		set(m, selAvatar, words(evm.AddressWord(safe)))
		set(m, selTarget, words(evm.AddressWord(safe)))
		set(m, selOwner, words(evm.AddressWord(safe)))
	}
	set(rolesM, selDefaultRoles, words(evm.Word{}))
	set(delayM, selTxCooldown, words(evm.Uint64Word(86400)))
	set(delayM, selTxExpiration, words(evm.Uint64Word(0)))
	return f
}

// fakeInfo answers userRole and extraAgents.
func fakeInfo(roles map[string]string, agents []AgentSlotRaw) InfoFunc {
	return func(_ context.Context, req any, out any) error {
		m := req.(map[string]string)
		var v any
		switch m["type"] {
		case "userRole":
			r := roles[strings.ToLower(m["user"])]
			if r == "" {
				r = `{"role":"missing"}`
			}
			return json.Unmarshal([]byte(r), out)
		case "extraAgents":
			v = agents
		default:
			return errors.New("unexpected " + m["type"])
		}
		b, _ := json.Marshal(v)
		return json.Unmarshal(b, out)
	}
}

var now = time.UnixMilli(1_794_744_300_000)

func TestDetectSafe(t *testing.T) {
	safe := evm.MustAddress(vecSafe)
	f := fakeSafe(t, safe)
	info := fakeInfo(map[string]string{strings.ToLower(safe.Hex()): `{"role":"user"}`},
		[]AgentSlotRaw{{Name: "bot valid_until 1795000000000", Address: "0x1234567890123456789012345678901234567890", ValidUntil: now.UnixMilli() + 3*86_400_000}})
	a, err := Detect(context.Background(), Deps{RPC: f, Info: info, Now: func() time.Time { return now }}, 999, safe, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != KindSafe || a.Safe.Threshold != 2 || len(a.Safe.Owners) != 3 || a.Safe.Nonce != 7 || a.Safe.Version != "1.4.1" {
		t.Fatalf("%+v %+v", a, a.Safe)
	}
	if a.Safe.DomainMatches == nil || !*a.Safe.DomainMatches {
		t.Error("domain separator should match")
	}
	if len(a.Safe.Modules) != 2 || a.Safe.Modules[0].Kind != ModuleRoles || a.Safe.Modules[1].Kind != ModuleDelay || *a.Safe.Modules[1].CooldownSec != 86400 {
		t.Errorf("modules %+v", a.Safe.Modules)
	}
	if a.Summary != "Contract account (Safe 1.4.1 2/3, modules: Roles, Delay)" {
		t.Error(a.Summary)
	}
	if a.Core == nil || !a.Core.AccountExists || a.Core.NamedSlotsUsed != 1 || !a.Core.Agents[0].ExpiresSoon || a.Core.Agents[0].DaysLeft != 3 {
		t.Errorf("core %+v", a.Core)
	}
	if !strings.Contains(strings.Join(a.Warnings, "|"), "expires in 3.0 days") {
		t.Errorf("warnings %v", a.Warnings)
	}
}

func TestDetectKinds(t *testing.T) {
	eoa := evm.MustAddress("0x00000000000000000000000000000000000000e0")
	del := evm.MustAddress("0x00000000000000000000000000000000000000e1")
	other := evm.MustAddress("0x00000000000000000000000000000000000000e2")
	target := evm.MustAddress("0x63c0c19a282a1b52b07dd5a65b58948a07dae32b")
	f := &fakeRPC{code: map[evm.Address][]byte{del: append([]byte{0xef, 0x01, 0x00}, target[:]...), other: {0x60}}, calls: map[string][]byte{}}
	d := Deps{RPC: f}
	for addr, want := range map[evm.Address]string{eoa: KindEOA, del: KindEIP7702, other: KindContract} {
		a, err := Detect(context.Background(), d, 998, addr, nil)
		if err != nil || a.Kind != want {
			t.Errorf("%s: %v %v", addr.Hex(), a, err)
		}
		if want == KindEIP7702 && a.Delegate != target.Hex() {
			t.Error("delegate", a.Delegate)
		}
	}
}

func TestPlanRegister(t *testing.T) {
	safe := evm.MustAddress(vecSafe)
	f := fakeSafe(t, safe)
	info := fakeInfo(map[string]string{strings.ToLower(safe.Hex()): `{"role":"user"}`}, nil)
	in := RegisterInput{ChainID: 999, Safe: safe, Agent: vecAgent, Name: "hlp", Days: 90, RoleKey: "keeper"}
	// the Safe's own getTransactionHash agrees with ours
	tx := Tx{To: corewriter.Address, Value: new(big.Int), Data: corewriter.SendRawActionCalldata(corewriter.AddAPIWallet(vecAgent,
		corewriter.AgentName("hlp", now.Add(90*24*time.Hour)))), Nonce: 7}
	h := tx.Hash(999, safe, "1.4.1")
	f.full[safe.Hex()+evm.EncodeHex(tx.GetTransactionHashCalldata())] = h[:]
	p, err := PlanRegister(context.Background(), Deps{RPC: f, Info: info, Now: func() time.Time { return now }}, in)
	if err != nil {
		t.Fatal(err)
	}
	if !p.DryRun || p.SafeTx.Nonce != 7 || p.SafeTx.SafeTxHash != evm.EncodeHex(h[:]) || p.Inner.To != corewriter.Address.Hex() {
		t.Fatalf("%+v", p)
	}
	if p.SafeTx.HashCheckedOnChain == nil || !*p.SafeTx.HashCheckedOnChain {
		t.Error("on-chain hash check missing")
	}
	if p.Roles == nil || p.Roles.Module != rolesM.Hex() || !strings.HasPrefix(p.Roles.Calldata, "0xc6fe8747") {
		t.Errorf("roles %+v", p.Roles)
	}
	if p.Action.ValidUntil != now.Add(90*24*time.Hour).UnixMilli() || !strings.HasSuffix(p.Action.AgentName, " valid_until "+itoa(p.Action.ValidUntil)) {
		t.Errorf("action %+v", p.Action)
	}
	if p.Slots == nil || p.Slots.AgentRole != "missing" {
		t.Errorf("slots %+v", p.Slots)
	}
}

func itoa(v int64) string { return big.NewInt(v).String() }

func TestBuildRefusals(t *testing.T) {
	safe := evm.MustAddress(vecSafe)
	base := func() *Account {
		return &Account{Kind: KindSafe, Safe: &SafeInfo{Version: "1.4.1", Owners: []string{owner1.Hex()}, Threshold: 1, Nonce: 3},
			Core: &CoreState{Role: "user", AccountExists: true, NamedSlotsMax: 3}}
	}
	in := RegisterInput{ChainID: 998, Safe: safe, Agent: vecAgent, Name: "new"}
	full := base()
	full.Core.NamedSlotsUsed = 3
	full.Core.Agents = []AgentSlot{{Name: "a"}, {Name: "b"}, {Name: "old valid_until 1"}}
	noCore := base()
	noCore.Core.AccountExists = false
	reused := base()
	reused.Core.Local = &AgentStatus{Address: vecAgent.Hex(), Role: "agent", User: owner2.Hex()}
	cases := map[string]*Account{"slots": full, "no HyperCore account": noCore, "already has HL role": reused,
		"not a Safe": {Kind: KindEOA}}
	for want, acct := range cases {
		_, err := Build(in, acct, now)
		if !IsRefused(err) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
	}
	// replacing an existing name is allowed with full slots
	in.Name = "old"
	p, err := Build(in, full, now)
	if err != nil || !p.Slots.ReplacesOld {
		t.Errorf("replace: %v", err)
	}
	// usage errors
	for _, bad := range []RegisterInput{{Safe: safe}, {Safe: safe, Agent: vecAgent, Days: 999}, {Safe: safe, Agent: vecAgent, Name: "x valid_until 5"}} {
		if _, err := Build(bad, base(), now); err == nil || IsRefused(err) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	low := uint64(1)
	if _, err := Build(RegisterInput{Safe: safe, Agent: vecAgent, Nonce: &low}, base(), now); err == nil {
		t.Error("nonce below current accepted")
	}
	if _, err := Build(RegisterInput{Safe: safe, Agent: vecAgent, RoleKey: "k"}, base(), now); err == nil {
		t.Error("role key without a Roles module accepted")
	}
}

func TestPollSchedule(t *testing.T) {
	if PollSchedule(0) != 5*time.Second || PollSchedule(5*time.Minute) != 20*time.Second || PollSchedule(time.Hour) != time.Minute {
		t.Error("schedule")
	}
}
