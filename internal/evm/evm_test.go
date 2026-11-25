package evm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAddressAndCalldata(t *testing.T) {
	a := MustAddress("0x688dD654bDbDE2BF8b6154685437f3F9dB2eAABd")
	if a.Hex() != "0x688dd654bdbde2bf8b6154685437f3f9db2eaabd" {
		t.Fatal(a.Hex())
	}
	got := EncodeHex(Calldata(MustSelector("0x54e98549"), AddressWord(a)))
	want := "0x54e98549000000000000000000000000688dd654bdbde2bf8b6154685437f3f9db2eaabd"
	if got != want {
		t.Fatalf("calldata %s", got)
	}
	for _, bad := range []string{"", "0x12", "688dD654bDbDE2BF8b6154685437f3F9dB2eAABd", "0xZZ8dD654bDbDE2BF8b6154685437f3F9dB2eAABd"} {
		if IsAddress(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	b, _ := json.Marshal(struct{ A Address }{a})
	if !strings.Contains(string(b), "0x688dd654") {
		t.Fatal(string(b))
	}
}

func TestWords(t *testing.T) {
	w := make([]byte, 32*4)
	w[31] = 1                     // bool true
	copy(w[32:64], bytesOf(0xff)) // -1
	w[64+23] = 1                  // > uint64
	w[96+5] = 1                   // not an address
	ws, err := NewWords(w)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := ws.Bool(0); err != nil || !b {
		t.Fatal("bool", b, err)
	}
	if v, err := ws.Int(1); err != nil || v.Int64() != -1 {
		t.Fatal("int", v, err)
	}
	if _, err := ws.Uint64(2); err == nil {
		t.Fatal("uint64 overflow accepted")
	}
	if _, err := ws.Address(3); err == nil {
		t.Fatal("dirty address accepted")
	}
	if _, err := ws.Word(4); err == nil {
		t.Fatal("out of range accepted")
	}
	if _, err := NewWords(make([]byte, 33)); err == nil {
		t.Fatal("ragged accepted")
	}
	if _, err := ExpectWords(make([]byte, 64), 3); err == nil {
		t.Fatal("length mismatch accepted")
	}
	two := make([]byte, 32)
	two[31] = 2
	if _, err := Words(two).Bool(0); err == nil {
		t.Fatal("bool 2 accepted")
	}
}

func bytesOf(b byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestStringAndArray(t *testing.T) {
	raw, _ := DecodeHex("0x000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000000175472656e6420507972616d69642028746573746e657429000000000000000000")
	s, err := Words(raw).String(0)
	if err != nil || s != "Trend Pyramid (testnet)" {
		t.Fatal(s, err)
	}
	// [offset=0x20][n=2][a0 a1][b0 b1]
	arr := make([]byte, 32*6)
	arr[31] = 0x20
	arr[63] = 2
	arr[95], arr[127], arr[159], arr[191] = 1, 2, 3, 4
	el, err := Words(arr).TupleArray(0, 2)
	if err != nil || len(el) != 2 {
		t.Fatal(err)
	}
	if v, _ := el[1].Uint64(1); v != 4 {
		t.Fatal(v)
	}
	if _, err := Words(arr[:32*5]).TupleArray(0, 2); err == nil {
		t.Fatal("short array accepted")
	}
}

func TestBigJSON(t *testing.T) {
	v, _ := new(big.Int).SetString("340232240130696325006", 10)
	b, _ := json.Marshal(NewBig(v))
	if string(b) != `"340232240130696325006"` {
		t.Fatal(string(b))
	}
	var x Big
	if err := json.Unmarshal([]byte(`123`), &x); err != nil || x.Int64() != 123 {
		t.Fatal(err)
	}
	if f := Float(v, 18); f < 340.23 || f > 340.24 {
		t.Fatal(f)
	}
}

func TestDecodeRevert(t *testing.T) {
	names := map[Selector]string{MustSelector("0xf512b278"): "NotKeeper()"}
	e := DecodeRevert(MustSelector("0xf512b278").bytes(), names)
	if e.Name != "NotKeeper()" || !strings.Contains(e.Error(), "NotKeeper") {
		t.Fatal(e)
	}
	// Error("nope")
	d, _ := DecodeHex("0x08c379a0" +
		"0000000000000000000000000000000000000000000000000000000000000020" +
		"0000000000000000000000000000000000000000000000000000000000000004" +
		"6e6f706500000000000000000000000000000000000000000000000000000000")
	if e := DecodeRevert(d, nil); e.Reason != "nope" {
		t.Fatal(e)
	}
	p, _ := DecodeHex("0x4e487b710000000000000000000000000000000000000000000000000000000000000011")
	if e := DecodeRevert(p, nil); !e.HasPanic || e.Panic != 0x11 {
		t.Fatal(e)
	}
	pre := DecodeRevert(MustSelector("0xafa95720").bytes(), map[Selector]string{MustSelector("0xafa95720"): "PrecompileLib__CoreUserExistsPrecompileFailed()"})
	if !pre.IsPrecompileFailure() {
		t.Fatal("precompile")
	}
}

func (s Selector) bytes() []byte { return append([]byte(nil), s[:]...) }

// rpcServer answers eth_chainId with chain and eth_call with result.
func rpcServer(t *testing.T, chain string, handle func(method string, params []any) (any, *rpcErrObj)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		one := func(q rpcReq) map[string]any {
			if q.Method == "eth_chainId" {
				return map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": chain}
			}
			res, e := handle(q.Method, q.Params)
			if e != nil {
				return map[string]any{"jsonrpc": "2.0", "id": q.ID, "error": e}
			}
			return map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": res}
		}
		if body[0] == '[' {
			var qs []rpcReq
			_ = json.Unmarshal(body, &qs)
			out := make([]any, 0, len(qs))
			for i := len(qs) - 1; i >= 0; i-- { // reversed: match by id
				out = append(out, one(qs[i]))
			}
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		var q rpcReq
		_ = json.Unmarshal(body, &q)
		_ = json.NewEncoder(w).Encode(one(q))
	}))
}

func TestFailoverGarbageAnd429(t *testing.T) {
	var garbageHits, limitHits atomic.Int32
	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		garbageHits.Add(1)
		_, _ = w.Write(bytesOf(0xff)) // like the testnet TLS failure
	}))
	defer garbage.Close()
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limitHits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer limited.Close()
	good := rpcServer(t, "0x3e6", func(method string, params []any) (any, *rpcErrObj) {
		return "0x" + strings.Repeat("00", 31) + "2a", nil
	})
	defer good.Close()

	c, err := NewClient(Options{Endpoints: []string{garbage.URL, limited.URL, good.URL}, ChainID: 998, MinInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Call(context.Background(), Address{1}, []byte{1, 2, 3, 4})
	if err != nil || b[31] != 0x2a {
		t.Fatal(b, err)
	}
	if c.Endpoint() != good.URL {
		t.Fatal("did not remember the good endpoint")
	}
	before := garbageHits.Load()
	if _, err := c.Call(context.Background(), Address{1}, []byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	if garbageHits.Load() != before {
		t.Fatal("retried a bad endpoint after a good one answered")
	}
}

func TestChainMismatch(t *testing.T) {
	wrong := rpcServer(t, "0x3e7", func(string, []any) (any, *rpcErrObj) { return "0x01", nil })
	defer wrong.Close()
	c, _ := NewClient(Options{Endpoints: []string{wrong.URL}, ChainID: 998, MinInterval: -1})
	_, err := c.Call(context.Background(), Address{1}, []byte{1, 2, 3, 4})
	var cm *ChainMismatchError
	if !errors.As(err, &cm) || cm.Got != 999 {
		t.Fatal(err)
	}
}

func TestBatchRevertAndEmpty(t *testing.T) {
	names := map[Selector]string{MustSelector("0xda748b10"): "x"}
	srv := rpcServer(t, "0x3e6", func(method string, params []any) (any, *rpcErrObj) {
		data := params[0].(map[string]any)["data"].(string)
		switch data {
		case "0xda748b10":
			return nil, &rpcErrObj{Code: 3, Message: "execution reverted"}
		case "0x00000001":
			return "0x", nil
		case "0xf512b278":
			return nil, &rpcErrObj{Code: 3, Message: "execution reverted", Data: json.RawMessage(`"0xf512b278"`)}
		}
		return "0x" + strings.Repeat("00", 32), nil
	})
	defer srv.Close()
	c, _ := NewClient(Options{Endpoints: []string{srv.URL}, ChainID: 998, MinInterval: -1, Errors: names, MaxBatch: 2})
	msgs := []CallMsg{{Data: []byte{0xda, 0x74, 0x8b, 0x10}}, {Data: []byte{0, 0, 0, 1}}, {Data: []byte{9, 9, 9, 9}}, {Data: []byte{0xf5, 0x12, 0xb2, 0x78}}}
	res, err := c.BatchCall(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := AsRevert(res[0].Err); !ok {
		t.Fatal("want revert", res[0].Err)
	}
	if !errors.Is(res[1].Err, ErrEmptyResult) {
		t.Fatal("want empty", res[1].Err)
	}
	if res[2].Err != nil || len(res[2].Data) != 32 {
		t.Fatal(res[2])
	}
	if r, ok := AsRevert(res[3].Err); !ok || r.Selector != MustSelector("0xf512b278") {
		t.Fatal(res[3].Err)
	}
}

func TestPrecompileFailover(t *testing.T) {
	stale := rpcServer(t, "0x3e6", func(string, []any) (any, *rpcErrObj) {
		return nil, &rpcErrObj{Code: 3, Message: "execution reverted", Data: json.RawMessage(`"0xafa95720"`)}
	})
	defer stale.Close()
	good := rpcServer(t, "0x3e6", func(string, []any) (any, *rpcErrObj) { return "0x" + strings.Repeat("00", 32), nil })
	defer good.Close()
	c, _ := NewClient(Options{Endpoints: []string{stale.URL, good.URL}, ChainID: 998, MinInterval: -1,
		Errors: map[Selector]string{MustSelector("0xafa95720"): "PrecompileLib__CoreUserExistsPrecompileFailed()"}})
	if _, err := c.Call(context.Background(), Address{1}, []byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
}

func TestPacing(t *testing.T) {
	srv := rpcServer(t, "0x3e6", func(string, []any) (any, *rpcErrObj) { return "0x01", nil })
	defer srv.Close()
	c, _ := NewClient(Options{Endpoints: []string{srv.URL}, MinInterval: 40 * time.Millisecond})
	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := c.BlockNumber(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if time.Since(start) < 80*time.Millisecond {
		t.Fatal("requests not spaced")
	}
}

// decodeAggregate3Call parses aggregate3 calldata (test-side mirror).
func decodeAggregate3Call(t *testing.T, data []byte) []CallMsg {
	t.Helper()
	w := Words(data[4:])
	off, _ := w.Uint64(0)
	arr := Words(w[off:])
	n, _ := arr.Uint64(0)
	body := Words(arr[32:])
	out := make([]CallMsg, n)
	for i := range out {
		eo, _ := body.Uint64(i)
		el := Words(body[eo:])
		out[i].To, _ = el.Address(0)
		out[i].Data, _ = el.Bytes(2)
	}
	return out
}

// encodeAggregate3Result encodes (bool,bytes)[].
func encodeAggregate3Result(res []CallResult) []byte {
	var elems [][]byte
	for _, r := range res {
		ok := BoolWord(r.Err == nil)
		data := r.Data
		if re, isRe := AsRevert(r.Err); isRe {
			data = re.Data
		}
		off, l := Uint64Word(64), Uint64Word(uint64(len(data)))
		e := append(append(append(ok[:], off[:]...), l[:]...), data...)
		e = append(e, make([]byte, pad32(len(data))-len(data))...)
		elems = append(elems, e)
	}
	h, n := Uint64Word(32), Uint64Word(uint64(len(res)))
	out := append(h[:], n[:]...)
	pos := 32 * len(res)
	for _, e := range elems {
		w := Uint64Word(uint64(pos))
		out = append(out, w[:]...)
		pos += len(e)
	}
	for _, e := range elems {
		out = append(out, e...)
	}
	return out
}

func TestMulticall(t *testing.T) {
	var requests atomic.Int32
	notKeeper := MustSelector("0xf512b278")
	srv := rpcServer(t, "0x3e6", func(method string, params []any) (any, *rpcErrObj) {
		requests.Add(1)
		p := params[0].(map[string]any)
		data, _ := DecodeHex(p["data"].(string))
		if MustAddress(p["to"].(string)) != Multicall3 {
			return nil, &rpcErrObj{Code: -32000, Message: "direct call not expected"}
		}
		var res []CallResult
		for _, m := range decodeAggregate3Call(t, data) {
			switch {
			case len(m.Data) == 4 && Selector(m.Data[:4]) == notKeeper:
				res = append(res, CallResult{Err: &RevertError{Data: notKeeper.bytes()}})
			case len(m.Data) == 4 && m.Data[0] == 0xee:
				res = append(res, CallResult{})
			default:
				w := Uint64Word(uint64(m.Data[3]))
				res = append(res, CallResult{Data: w[:]})
			}
		}
		return EncodeHex(encodeAggregate3Result(res)), nil
	})
	defer srv.Close()
	c, _ := NewClient(Options{Endpoints: []string{srv.URL}, MinInterval: -1, MaxMulticall: 3,
		Errors: map[Selector]string{notKeeper: "NotKeeper()"}})
	msgs := []CallMsg{{Data: []byte{0, 0, 0, 7}}, {Data: notKeeper.bytes()}, {Data: []byte{0xee, 0, 0, 0}}, {Data: []byte{1, 2, 3, 9}}}
	res, err := c.BatchCall(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := Words(res[0].Data).Uint64(0); v != 7 {
		t.Fatal(res[0])
	}
	if r, ok := AsRevert(res[1].Err); !ok || r.Name != "NotKeeper()" {
		t.Fatal(res[1].Err)
	}
	if !errors.Is(res[2].Err, ErrEmptyResult) {
		t.Fatal(res[2].Err)
	}
	if v, _ := Words(res[3].Data).Uint64(0); v != 9 {
		t.Fatal(res[3])
	}
	if requests.Load() != 2 { // 4 calls / 3 per aggregate
		t.Fatalf("%d requests", requests.Load())
	}
}

func TestBatchLimitAdapts(t *testing.T) {
	var batches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if body[0] != '[' {
			var q rpcReq
			_ = json.Unmarshal(body, &q)
			if strings.Contains(string(body), strings.ToLower(Multicall3.Hex())) { // no multicall here
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": q.ID, "error": map[string]any{"code": 3, "message": "execution reverted"}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": "0x" + strings.Repeat("00", 31) + "01"})
			return
		}
		batches.Add(1)
		var qs []rpcReq
		_ = json.Unmarshal(body, &qs)
		out := make([]any, len(qs))
		for i, q := range qs {
			if len(qs) > 3 {
				out[i] = map[string]any{"jsonrpc": "2.0", "id": q.ID, "error": map[string]any{"code": 31, "message": "Batch of more than 3 requests are not allowed on free plan"}}
				continue
			}
			out[i] = map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": "0x" + strings.Repeat("00", 31) + "01"}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	c, _ := NewClient(Options{Endpoints: []string{srv.URL}, MinInterval: -1})
	msgs := make([]CallMsg, 7)
	for i := range msgs {
		msgs[i].Data = []byte{1, 2, 3, byte(i)}
	}
	res, err := c.BatchCall(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range res {
		if r.Err != nil || len(r.Data) != 32 {
			t.Fatalf("%d: %+v", i, r)
		}
	}
	if b := batches.Load(); b != 3 { // 1 refused + 3 + 3 (the last call goes alone)
		t.Fatalf("%d batches", b)
	}
}
