package evm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// D-T1: an endpoint answering 0xff garbage is listed first and a good one
// second. The first run fails over and records the good endpoint; a second
// client (a new process) starts on it and never touches the broken one.
func TestStateFileSkipsBrokenEndpoint(t *testing.T) {
	var garbageHits atomic.Int32
	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		garbageHits.Add(1)
		_, _ = w.Write(bytesOf(0xff))
	}))
	defer garbage.Close()
	good := rpcServer(t, "0x3e6", func(string, []any) (any, *rpcErrObj) {
		return "0x" + strings.Repeat("00", 31) + "2a", nil
	})
	defer good.Close()
	state := filepath.Join(t.TempDir(), "evm-rpc-998.json")
	opts := Options{Endpoints: []string{garbage.URL, good.URL}, ChainID: 998, MinInterval: -1, StateFile: state}

	c1, err := NewClient(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c1.Call(context.Background(), Address{1}, []byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	fo := c1.TakeFailovers()
	if len(fo) != 1 || fo[0].Endpoint != garbage.URL || fo[0].Used != good.URL || !strings.Contains(fo[0].Reason, "non-JSON") {
		t.Fatalf("failovers %+v", fo)
	}
	if again := c1.TakeFailovers(); len(again) != 0 {
		t.Fatalf("failovers not cleared: %+v", again)
	}
	if fi, err := os.Stat(state); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("state file %v %v", fi, err)
	}

	garbageHits.Store(0)
	c2, err := NewClient(opts)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Endpoint() != good.URL {
		t.Fatalf("second run starts at %s", c2.Endpoint())
	}
	if _, err := c2.Call(context.Background(), Address{1}, []byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	if n := garbageHits.Load(); n != 0 {
		t.Fatalf("second run sent %d requests to the broken endpoint", n)
	}
	if fo := c2.TakeFailovers(); len(fo) != 0 {
		t.Fatalf("unexpected failovers %+v", fo)
	}

	// Expired or foreign-chain state is ignored.
	old := opts
	old.Now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	if c3, _ := NewClient(old); c3.Endpoint() != garbage.URL {
		t.Fatal("expired state still used")
	}
	other := opts
	other.ChainID = 999
	if c4, _ := NewClient(other); c4.Endpoint() != garbage.URL {
		t.Fatal("state from another chain used")
	}
	// An endpoint no longer configured is ignored.
	gone := opts
	gone.Endpoints = []string{garbage.URL}
	if c5, _ := NewClient(gone); c5.Endpoint() != garbage.URL {
		t.Fatal("unconfigured endpoint used")
	}
}

// D-T2: config rpc is tried before the built-ins; rpc_only replaces them.
func TestMergeChainsPrependsRPC(t *testing.T) {
	const x = "https://x.example/rpc"
	def := DefaultChains()
	official, drpc := def[ChainTestnet].RPC[0], def[ChainTestnet].RPC[1]

	got := MergeChains(def, map[uint64]Chain{ChainTestnet: {RPC: []string{x}}})[ChainTestnet].RPC
	if want := []string{x, official, drpc}; !reflect.DeepEqual(got, want) {
		t.Fatalf("prepend: %v", got)
	}
	// duplicates collapse to the first position
	got = MergeChains(def, map[uint64]Chain{ChainTestnet: {RPC: []string{drpc + "/", x}}})[ChainTestnet].RPC
	if want := []string{drpc + "/", x, official}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dedupe: %v", got)
	}
	only := MergeChains(def, map[uint64]Chain{ChainTestnet: {RPC: []string{x}, RPCOnly: true}})[ChainTestnet]
	if !reflect.DeepEqual(only.RPC, []string{x}) || !only.RPCOnly {
		t.Fatalf("rpc_only: %+v", only)
	}
	// a later plain overlay still prepends to an rpc_only list
	got = MergeChains(MergeChains(def, map[uint64]Chain{ChainTestnet: {RPC: []string{x}, RPCOnly: true}}),
		map[uint64]Chain{ChainTestnet: {RPC: []string{"https://y.example"}}})[ChainTestnet].RPC
	if want := []string{"https://y.example", x}; !reflect.DeepEqual(got, want) {
		t.Fatalf("overlay: %v", got)
	}
	if got := SplitRPCList("https://a, https://b", " https://a ,", ""); !reflect.DeepEqual(got, []string{"https://a", "https://b"}) {
		t.Fatalf("split: %v", got)
	}
}
