package exchange

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// testKey is the Python SDK's PUBLIC test key.
// It is a published fixture, not a wallet. No other key may appear here.
const testKey = "0x0123456789012345678901234567890123456789012345678901234567890123"

type dummyAction struct {
	Type string `msgpack:"type" json:"type"`
	Num  int    `msgpack:"num" json:"num"`
}

func mustSigner(t *testing.T) *KeySigner {
	t.Helper()
	s, err := NewKeySigner(testKey)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestProductionConnectionID(t *testing.T) {
	action := OrderAction{Type: "order", Grouping: "na", Orders: []OrderWire{{
		A: 4, B: true, P: "1670.1", S: "0.0147", R: false, T: OrderType{Limit: &LimitType{Tif: "Ioc"}},
	}}}
	mp, err := EncodeAction(action)
	if err != nil {
		t.Fatal(err)
	}
	wantMP := "83a474797065a56f72646572a66f72646572739186a16104a162c3a170a6313637302e31a173a6302e30313437a172c2a17481a56c696d697481a3746966a3496f63a867726f7570696e67a26e61"
	if hex.EncodeToString(mp) != wantMP {
		t.Fatalf("msgpack\n got %x\nwant %s", mp, wantMP)
	}
	cid, err := ConnectionID(action, 1677777606040, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := "0x" + hex.EncodeToString(cid[:]); got != "0x0fcbeda5ae3c4950a548021552a4fea2226858c4453571bf3f24ba017eac2908" {
		t.Fatalf("connectionId %s", got)
	}
}

func TestSigningVectors(t *testing.T) {
	s := mustSigner(t)
	vault := "0x1719884eb866cb12b2287399b15f7db5e7d775ea"
	order := OrderAction{Type: "order", Grouping: "na", Orders: []OrderWire{{
		A: 1, B: true, P: "100", S: "100", R: false, T: OrderType{Limit: &LimitType{Tif: "Gtc"}},
	}}}
	orderCloid := order
	orderCloid.Orders = []OrderWire{order.Orders[0]}
	orderCloid.Orders[0].C = "0x00000000000000000000000000000001"
	dummy := dummyAction{Type: "dummy", Num: 100000000000}
	cases := []struct {
		name    string
		action  any
		vault   *string
		mainnet bool
		r, s    string
		v       int
	}{
		{"dummy main", dummy, nil, true, "0x53749d5b30552aeb2fca34b530185976545bb22d0b3ce6f62e31be961a59298", "0x755c40ba9bf05223521753995abb2f73ab3229be8ec921f350cb447e384d8ed8", 27},
		{"dummy test", dummy, nil, false, "0x542af61ef1f429707e3c76c5293c80d01f74ef853e34b76efffcb57e574f9510", "0x17b8b32f086e8cdede991f1e2c529f5dd5297cbe8128500e00cbaf766204a613", 28},
		{"order", order, nil, true, "0xd65369825a9df5d80099e513cce430311d7d26ddf477f5b3a33d2806b100d78e", "0x2b54116ff64054968aa237c20ca9ff68000f977c93289157748a3162b6ea940e", 28},
		{"order cloid", orderCloid, nil, true, "0x41ae18e8239a56cacbc5dad94d45d0b747e5da11ad564077fcac71277a946e3", "0x3c61f667e747404fe7eea8f90ab0e76cc12ce60270438b2058324681a00116da", 27},
		{"dummy vault", dummy, &vault, true, "0x3c548db75e479f8012acf3000ca3a6b05606bc2ec0c29c50c515066a326239", "0x4d402be7396ce74fbba3795769cda45aec00dc3125a984f2a9f23177b190da2c", 28},
	}
	for _, c := range cases {
		sig, err := s.SignL1(c.action, 0, c.vault, nil, c.mainnet)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if sig.R != c.r || sig.S != c.s || sig.V != c.v {
			t.Errorf("%s:\n got r=%s s=%s v=%d\nwant r=%s s=%s v=%d", c.name, sig.R, sig.S, sig.V, c.r, c.s, c.v)
		}
		cid, _ := ConnectionID(c.action, 0, c.vault, nil)
		addr, err := RecoverAddress(L1Digest(cid, c.mainnet), sig)
		if err != nil || addr != s.Address() {
			t.Errorf("%s: recovered %s want %s (%v)", c.name, addr, s.Address(), err)
		}
	}
}

func TestSignerAddressAndKeyHygiene(t *testing.T) {
	s := mustSigner(t)
	if s.Address() != "0x14791697260e4c9a71f18484c9f997b308e59325" {
		t.Fatalf("address %s", s.Address())
	}
	if strings.Contains(s.String(), "0123456789") {
		t.Fatal("String leaks key")
	}
	for _, bad := range []string{"", "0x12", testKey + "00", "0x" + strings.Repeat("zz", 32), "0x" + strings.Repeat("00", 32)} {
		if _, err := NewSigner(bad); err == nil {
			t.Errorf("accepted bad key %q", bad)
		} else if strings.Contains(err.Error(), "0123") {
			t.Error("error echoes key")
		}
	}
	if _, err := NewSigner(testKey); err != nil {
		t.Fatal(err)
	}
}

func TestCancelNeverEncodesF(t *testing.T) {
	mp, err := EncodeAction(NewCancelAction(Cancel{A: 1, O: 605976652868}))
	if err != nil {
		t.Fatal(err)
	}
	h := hex.EncodeToString(mp)
	if strings.Contains(h, hex.EncodeToString([]byte{0xa1, 'f'})) {
		t.Fatalf("cancel encodes f: %s", h)
	}
	// oid > 2^32 must be uint64 (0xcf), never int64 (0xd3).
	if !strings.Contains(h, "cf") || strings.Contains(h, "d3") {
		t.Fatalf("oid encoding: %s", h)
	}
}

func TestOptionalFieldsOmitted(t *testing.T) {
	w, err := LimitOrder(4, true, decimal.RequireFromString("1670.10"), decimal.RequireFromString("0.01470"), TifIoc, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if w.P != "1670.1" || w.S != "0.0147" {
		t.Fatalf("wire %+v", w)
	}
	// Same as the production vector: no "c", no "builder".
	cid, _ := ConnectionID(NewOrderAction(w), 1677777606040, nil, nil)
	if hex.EncodeToString(cid[:]) != "0fcbeda5ae3c4950a548021552a4fea2226858c4453571bf3f24ba017eac2908" {
		t.Fatal("builder output differs from the production vector")
	}
	mp, _ := EncodeAction(NewScheduleCancel(0))
	if strings.Contains(string(mp), "time") {
		t.Fatal("scheduleCancel time must be omitted when unset")
	}
	mp, _ = EncodeAction(NewScheduleCancel(1794735600000))
	if !strings.Contains(string(mp), "time") {
		t.Fatal("scheduleCancel time missing")
	}
	if _, err := LimitOrder(1, true, decimal.NewFromInt(1), decimal.NewFromInt(1), "Fok", false, ""); err == nil {
		t.Fatal("FOK accepted")
	}
	if _, err := LimitOrder(1, true, decimal.NewFromInt(1), decimal.NewFromInt(1), TifGtc, false, "0x1"); err == nil {
		t.Fatal("bad cloid accepted")
	}
	tw, err := TriggerOrder(1, false, decimal.NewFromInt(90), decimal.NewFromInt(1), decimal.RequireFromString("95.0"), true, "sl", true, "")
	if err != nil || tw.T.Trigger == nil || tw.T.Trigger.TriggerPx != "95" || tw.T.Limit != nil {
		t.Fatalf("trigger %+v %v", tw, err)
	}
	if _, err := TriggerOrder(1, false, decimal.NewFromInt(90), decimal.NewFromInt(1), decimal.NewFromInt(95), true, "x", true, ""); err == nil {
		t.Fatal("bad tpsl accepted")
	}
	c := NewCloid()
	if !ValidCloid(c) || c == NewCloid() {
		t.Fatal("cloid")
	}
	if NewUpdateLeverage(3, true, 5).Type != "updateLeverage" {
		t.Fatal("leverage")
	}
}

func TestVaultTransferBuilder(t *testing.T) {
	a, err := NewVaultTransfer("0xDFC24B077BC1425AD1DEA75BCB6F8158E10DF303", true, decimal.RequireFromString("12.3456789"))
	if err != nil || a.USD != 12345678 || a.VaultAddress != "0xdfc24b077bc1425ad1dea75bcb6f8158e10df303" {
		t.Fatalf("%+v %v", a, err)
	}
	if _, err := NewVaultTransfer("0x12", true, decimal.NewFromInt(1)); err == nil {
		t.Fatal("bad vault accepted")
	}
	if _, err := NewVaultTransfer("0xdfc24b077bc1425ad1dea75bcb6f8158e10df303", true, decimal.Zero); err == nil {
		t.Fatal("zero accepted")
	}
}

func TestExpiresAfterInHash(t *testing.T) {
	d := dummyAction{Type: "dummy", Num: 1}
	a, _ := ConnectionID(d, 5, nil, nil)
	e := uint64(10)
	b, _ := ConnectionID(d, 5, nil, &e)
	if a == b {
		t.Fatal("expiresAfter not hashed")
	}
	bad := "0x12"
	if _, err := ConnectionID(d, 5, &bad, nil); err == nil {
		t.Fatal("bad vault accepted")
	}
}

func TestEIP712GenericMatchesL1(t *testing.T) {
	// The generic HashStruct path must agree with the vector-verified L1 path.
	var cid [32]byte
	cid[0] = 0xab
	dom, err := DomainSeparator("Exchange", "1", 1337)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := HashStruct("Agent", []TypedField{{"source", "string"}, {"connectionId", "bytes32"}}, map[string]any{"source": "a", "connectionId": cid[:]})
	if err != nil {
		t.Fatal(err)
	}
	if TypedDigest(dom, sh) != L1Digest(cid, true) {
		t.Fatal("generic EIP-712 disagrees with L1 digest")
	}
	if typeString("HyperliquidTransaction:ApproveAgent", UserSignedTypes["approveAgent"].Fields) !=
		"HyperliquidTransaction:ApproveAgent(string hyperliquidChain,address agentAddress,string agentName,uint64 nonce)" {
		t.Fatal("approveAgent type string")
	}
}

func TestUserSignedRecovers(t *testing.T) {
	s := mustSigner(t)
	vals := map[string]any{
		"hyperliquidChain": "Testnet",
		"agentAddress":     "0x5e9ee1089755c3435139848e47e6635505d5a13a",
		"agentName":        "",
		"nonce":            uint64(1794735535797),
	}
	typ := UserSignedTypes["approveAgent"]
	sig, err := s.SignUserAction(typ, vals, 0x66eee)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := UserSignedDigest(typ, vals, 0x66eee)
	if addr, err := RecoverAddress(d, sig); err != nil || addr != s.Address() {
		t.Fatalf("recover %s %v", addr, err)
	}
	// Chain id is part of the domain.
	d2, _ := UserSignedDigest(typ, vals, 0xa4b1)
	if d == d2 {
		t.Fatal("chain id ignored")
	}
	delete(vals, "nonce")
	if _, err := s.SignUserAction(typ, vals, 0x66eee); err == nil {
		t.Fatal("missing field accepted")
	}
	for _, c := range []struct {
		typ string
		v   any
	}{{"string", 1}, {"address", "0x1"}, {"bool", "x"}, {"uint64", -1}, {"bytes32", []byte{1}}, {"tuple", 1}} {
		if _, err := encodeValue(c.typ, c.v); err == nil {
			t.Errorf("%s %v accepted", c.typ, c.v)
		}
	}
	for _, c := range []struct {
		typ string
		v   any
	}{{"bytes", "0xdead"}, {"bytes", []byte{1}}, {"uint32", uint32(5)}, {"uint256", 7}, {"bool", true}} {
		if _, err := encodeValue(c.typ, c.v); err != nil {
			t.Errorf("%s %v: %v", c.typ, c.v, err)
		}
	}
	if _, err := RecoverAddress(d, Signature{R: "zz", S: "1", V: 27}); err == nil {
		t.Fatal("bad sig accepted")
	}
}

func TestNoncesMonotonic(t *testing.T) {
	fixed := time.UnixMilli(1000)
	n := NewNonces(func() time.Time { return fixed })
	var mu sync.Mutex
	seen := map[uint64]bool{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v := n.Next()
			mu.Lock()
			seen[v] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(seen) != 50 || !seen[1000] || !seen[1049] {
		t.Fatalf("nonces %d", len(seen))
	}
	fixed = time.UnixMilli(5000)
	if n.Next() != 5000 {
		t.Fatal("clock not followed")
	}
}

func TestPostIsDryRunAndNeverConnects(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":1}}]}}}`))
	}))
	defer srv.Close()
	if LiveSendCompiled() {
		t.Fatal("liveSend must be false in this build")
	}
	c := NewHTTPClient(srv.URL, mustSigner(t), nil)
	c.ExpiresAfter = time.Minute
	w, _ := LimitOrder(0, true, decimal.NewFromInt(60000), decimal.RequireFromString("0.001"), TifGtc, false, NewCloid())
	vault := "0x1719884EB866CB12B2287399B15F7DB5E7D775EA"
	for _, gate := range []Gate{{Live: false, Mainnet: true}, {Live: true, Mainnet: true}} {
		res, err := c.Post(context.Background(), NewOrderAction(w), &vault, gate)
		if err != nil {
			t.Fatal(err)
		}
		if !res.DryRun || res.Envelope.Signature.R == "" || res.Envelope.VaultAddress == nil ||
			*res.Envelope.VaultAddress != strings.ToLower(vault) || res.Envelope.ExpiresAfter == nil {
			t.Fatalf("result %+v", res)
		}
		// The envelope verifies against the signer.
		cid, _ := ConnectionID(res.Envelope.Action, res.Envelope.Nonce, res.Envelope.VaultAddress, res.Envelope.ExpiresAfter)
		if addr, _ := RecoverAddress(L1Digest(cid, true), res.Envelope.Signature); addr != mustSigner(t).Address() {
			t.Fatal("envelope signature does not verify")
		}
		// Dry runs are signed already expired, so they cannot be replayed.
		if *res.Envelope.ExpiresAfter >= res.Envelope.Nonce {
			t.Fatalf("dry-run expiresAfter %d not before nonce %d", *res.Envelope.ExpiresAfter, res.Envelope.Nonce)
		}
		b, _ := json.Marshal(res.Envelope)
		if !strings.Contains(string(b), `"vaultAddress":"0x1719`) {
			t.Fatalf("envelope json %s", b)
		}
	}
	// vaultTransfer is vault-agnostic: vaultAddress forced to null.
	vt, _ := NewVaultTransfer("0xdfc24b077bc1425ad1dea75bcb6f8158e10df303", true, decimal.NewFromInt(10))
	res, err := c.Post(context.Background(), vt, &vault, Gate{Live: true, Mainnet: true})
	if err != nil || res.Envelope.VaultAddress != nil {
		t.Fatalf("vaultTransfer vault %+v %v", res.Envelope.VaultAddress, err)
	}
	if hits.Load() != 0 {
		t.Fatalf("Post reached the server %d times", hits.Load())
	}
	// Network mismatch guard on real hosts.
	tc := NewHTTPClient("https://api.hyperliquid-testnet.xyz/exchange", mustSigner(t), nil)
	if _, err := tc.Post(context.Background(), NewOrderAction(w), nil, Gate{Mainnet: true}); err != ErrNetworkMismatch {
		t.Fatalf("mismatch: %v", err)
	}
	if _, err := NewHTTPClient(srv.URL, nil, nil).Post(context.Background(), NewOrderAction(w), nil, Gate{Mainnet: true}); err != ErrNoSigner {
		t.Fatal("nil signer")
	}
	var _ Client = NewClient(srv.URL, mustSigner(t))
}

func TestParseResponse(t *testing.T) {
	var r Result
	err := ParseResponse(200, []byte(`{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":77,"cloid":"0xab"}},{"filled":{"totalSz":"0.02","avgPx":"1891.4","oid":78}},{"error":"Order must have minimum value of $10."}]}}}`), &r)
	if err != nil || r.Status != "ok" || len(r.Statuses) != 3 {
		t.Fatalf("%v %+v", err, r)
	}
	if !r.Statuses[0].OK() || r.Statuses[0].Resting.OID != 77 || !r.Statuses[1].OK() || r.Statuses[1].Filled.AvgPx != "1891.4" || r.Statuses[2].OK() {
		t.Fatalf("statuses %+v", r.Statuses)
	}
	var cr Result
	if err := ParseResponse(200, []byte(`{"status":"ok","response":{"type":"cancel","data":{"statuses":["success",{"error":"Order was never placed, already canceled, or filled."}]}}}`), &cr); err != nil {
		t.Fatal(err)
	}
	if !cr.Statuses[0].Success || cr.Statuses[1].OK() {
		t.Fatalf("cancel %+v", cr.Statuses)
	}
	var er Result
	if err := ParseResponse(200, []byte(`{"status":"err","response":"User or API Wallet 0xabc does not exist."}`), &er); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err envelope %v", err)
	}
	var ur Result
	if err := ParseResponse(502, []byte("bad gateway"), &ur); err == nil || !ur.Unknown {
		t.Fatal("5xx must be unknown outcome")
	}
	var gr Result
	if err := ParseResponse(200, []byte("<html>"), &gr); err == nil || !gr.Unknown {
		t.Fatal("garbage must be unknown outcome")
	}
	var br Result
	if err := ParseResponse(422, []byte("Failed to deserialize"), &br); err == nil || br.Unknown {
		t.Fatal("4xx text")
	}
}
