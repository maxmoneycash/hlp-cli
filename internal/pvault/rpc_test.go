package pvault

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/evm"
	"github.com/maxmoneycash/hlp-cli/internal/hlpxyz"
)

// rpcProxy is a JSON-RPC endpoint for chain 998 that answers eth_call and
// eth_getCode from the fixture caller (single requests and batches).
func rpcProxy(t *testing.T, fc *fakeCaller) *httptest.Server {
	t.Helper()
	type req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params []any           `json:"params"`
	}
	one := func(q req) map[string]any {
		out := map[string]any{"jsonrpc": "2.0", "id": q.ID}
		switch q.Method {
		case "eth_chainId":
			out["result"] = "0x3e6"
		case "eth_getCode":
			a, _ := evm.ParseAddress(q.Params[0].(string))
			b, _ := fc.GetCode(context.Background(), a)
			out["result"] = evm.EncodeHex(b)
		case "eth_call":
			m := q.Params[0].(map[string]any)
			to, _ := evm.ParseAddress(m["to"].(string))
			data, _ := evm.DecodeHex(m["data"].(string))
			b, err := fc.Call(context.Background(), to, data)
			switch {
			case errors.Is(err, evm.ErrEmptyResult):
				out["result"] = "0x"
			case err != nil:
				out["error"] = map[string]any{"code": 3, "message": "execution reverted", "data": "0x"}
			default:
				out["result"] = evm.EncodeHex(b)
			}
		default:
			out["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		return out
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if len(body) > 0 && body[0] == '[' {
			var qs []req
			_ = json.Unmarshal(body, &qs)
			out := make([]any, len(qs))
			for i, q := range qs {
				out[i] = one(q)
			}
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		var q req
		_ = json.Unmarshal(body, &q)
		_ = json.NewEncoder(w).Encode(one(q))
	}))
}

// D-T3: the tearsheet names the endpoint that answered and warns about the
// one that failed first; the state file makes the next service skip it.
func TestTearsheetReportsRPCFailover(t *testing.T) {
	var garbageHits atomic.Int32
	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		garbageHits.Add(1)
		_, _ = w.Write([]byte(strings.Repeat("\xff", 256))) // the testnet TLS failure
	}))
	defer garbage.Close()
	good := rpcProxy(t, newFakeCaller(t))
	defer good.Close()

	chains := evm.DefaultChains()
	c := chains[evm.ChainTestnet]
	c.RPC = []string{garbage.URL, good.URL}
	chains[evm.ChainTestnet] = c
	dir := t.TempDir()
	newSvc := func() *Service {
		return NewService(Options{Chains: chains, Telemetry: newFakeTelemetry(t), RPCStateDir: dir,
			Now:     func() time.Time { return time.Unix(1794744506, 0) },
			NewCore: func(evm.Chain) CoreInfo { return newFakeCore(t) }})
	}
	ts, m, err := newSvc().Tearsheet(context.Background(), TearsheetInput{Vault: "trend-pyramid", Epochs: 3})
	if err != nil {
		t.Fatal(err)
	}
	goodHost, badHost := EndpointHost(good.URL), EndpointHost(garbage.URL)
	if ts.RPC != goodHost {
		t.Fatalf("rpc %q, want %q", ts.RPC, goodHost)
	}
	want := "rpc " + badHost + " failed ("
	n := 0
	for _, w := range m.Warnings {
		if strings.HasPrefix(w, want) && strings.HasSuffix(w, "; used "+goodHost) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want one failover warning, got %v", m.Warnings)
	}
	if m.Partial {
		t.Fatal("a failover is not a partial result")
	}
	if ts.NavUSD <= 0 || ts.Source != SourceLens {
		t.Fatalf("tearsheet %+v", ts)
	}
	if _, err := filepath.Glob(filepath.Join(dir, "evm-rpc-998.json")); err != nil {
		t.Fatal(err)
	}

	garbageHits.Store(0)
	ts, m, err = newSvc().Tearsheet(context.Background(), TearsheetInput{Vault: "trend-pyramid", Epochs: 3, NoHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	if garbageHits.Load() != 0 || ts.RPC != goodHost {
		t.Fatalf("second run: %d requests to the broken endpoint, rpc %s", garbageHits.Load(), ts.RPC)
	}
	for _, w := range m.Warnings {
		if strings.HasPrefix(w, "rpc ") {
			t.Fatalf("unexpected failover warning %q", w)
		}
	}
}

// HLP_HYPEREVM_RPC (Options.ExtraRPC) is tried before the chain's list.
func TestExtraRPCFirst(t *testing.T) {
	var got []string
	s := NewService(Options{ExtraRPC: []string{"https://y.example"}, NewCaller: func(ch evm.Chain) (evm.Caller, error) {
		got = ch.RPC
		return newFakeCaller(t), nil
	}})
	if _, _, err := s.Reader(evm.ChainTestnet); err != nil {
		t.Fatal(err)
	}
	def := evm.DefaultChains()[evm.ChainTestnet].RPC
	if len(got) != 3 || got[0] != "https://y.example" || got[1] != def[0] || got[2] != def[1] {
		t.Fatalf("endpoints %v", got)
	}
}

// D-T4: the history columns decode by name, and times come out in ms.
func TestHistoryColumnsAndMillis(t *testing.T) {
	tel := newFakeTelemetry(t)
	last := tel.hist[len(tel.hist)-1]
	if math.Abs(last.SupplyShares-340.2322) > 1e-9 || math.Abs(last.KeeperGasHYPE-0.05106) > 1e-4 {
		t.Fatalf("last sample %+v", last)
	}
	if last.TSec != 1794743716 || last.SharePrice != 1.025848 || last.NavUSD != 349.03 {
		t.Fatalf("last sample %+v", last)
	}
	b, err := json.Marshal(last)
	if err != nil || string(b) != "[1794743716,1.025848,349.03,340.2322,0.0510635]" {
		t.Fatalf("round trip %s %v", b, err)
	}
	var short hlpxyz.PVaultSample
	if err := json.Unmarshal([]byte("[1,2,3]"), &short); err != nil || short.NavUSD != 3 || short.SupplyShares != 0 {
		t.Fatal(short, err)
	}
	if err := json.Unmarshal([]byte(`{"t":1}`), &short); err == nil {
		t.Fatal("object accepted as a sample")
	}

	s := testService(t, newFakeCaller(t), nil)
	ts, _, err := s.Tearsheet(context.Background(), TearsheetInput{Vault: "trend-pyramid"})
	if err != nil {
		t.Fatal(err)
	}
	h := ts.History[len(ts.History)-1]
	if h.T != 1794743716000 || math.Abs(h.SupplyShares-340.2322) > 1e-9 || math.Abs(h.KeeperGasHYPE-0.0510635) > 1e-9 {
		t.Fatalf("history point %+v", h)
	}
	if ts.Keeper == nil || ts.Keeper.At < 1e12 || ts.Bot == nil || ts.Bot.At < 1e12 {
		t.Fatalf("keeper/bot times are not ms: %+v %+v", ts.Keeper, ts.Bot)
	}
	if ts.Keeper.At != tel.status.LastKeeperRun.At*1000 {
		t.Fatal(ts.Keeper.At)
	}
}

// D-T4: no c3/c4 column names remain in the vault packages.
func TestNoUnnamedHistoryColumns(t *testing.T) {
	if _, err := exec.LookPath("grep"); err != nil {
		t.Skip("no grep")
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..")
	out, _ := exec.Command("grep", "-rnwE", "c3|c4", filepath.Join(root, "hlpxyz"), filepath.Join(root, "pvault")).CombinedOutput()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" && !strings.Contains(line, "rpc_test.go") {
			t.Errorf("unnamed column: %s", line)
		}
	}
}
