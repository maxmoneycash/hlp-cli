package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/core"
)

// snapshotRPC is a JSON-RPC server answering eth_call from
// fixtures/pvault/onchain_snapshot.json by calldata (unknown calls revert).
func snapshotRPC(t *testing.T) *httptest.Server {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "testdata", "fixtures", "pvault", "onchain_snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	type call struct {
		Calldata, Result string
	}
	var snap struct {
		Calls     map[string]call `json:"calls"`
		LensVault call            `json:"lens_vaultState"`
		LensUser  map[string]call `json:"lens_userState"`
	}
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatal(err)
	}
	results := map[string]string{}
	for _, c := range snap.Calls {
		results[strings.ToLower(c.Calldata)] = c.Result
	}
	results[snap.LensVault.Calldata] = snap.LensVault.Result
	for _, c := range snap.LensUser {
		results[c.Calldata] = c.Result
	}
	type req struct {
		ID     int               `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	answer := func(q req) map[string]any {
		out := map[string]any{"jsonrpc": "2.0", "id": q.ID}
		switch q.Method {
		case "eth_chainId":
			out["result"] = "0x3e6"
		case "eth_getCode":
			out["result"] = "0x6080"
		case "eth_call":
			var p struct{ Data string }
			_ = json.Unmarshal(q.Params[0], &p)
			if r, ok := results[strings.ToLower(p.Data)]; ok && r != "" {
				out["result"] = r
			} else {
				out["error"] = map[string]any{"code": 3, "message": "execution reverted"}
			}
		default:
			out["error"] = map[string]any{"code": -32601, "message": "method not allowed in test"}
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
				out[i] = answer(q)
			}
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		var q req
		_ = json.Unmarshal(body, &q)
		_ = json.NewEncoder(w).Encode(answer(q))
	}))
}

func pvaultApp(t *testing.T, jsonOut bool) (*app, *bytes.Buffer) {
	t.Helper()
	fixtures := isolate(t)
	var out, errb bytes.Buffer
	env := map[string]string{"HLP_FIXTURES": fixtures}
	pio := IO{Out: &out, Err: &errb, Width: 100, Getenv: func(k string) string { return env[k] }}
	return &app{io: pio, g: &globals{json: jsonOut, plain: !jsonOut, color: "never"}, bi: BuildInfo{Version: "1.2.3"}, reg: core.Default()}, &out
}

func TestPVaultCmdJSON(t *testing.T) {
	srv := snapshotRPC(t)
	defer srv.Close()
	a, out := pvaultApp(t, true)
	cmd := a.pvaultCmd()
	cmd.SetArgs([]string{"trend-pyramid", "--rpc", srv.URL, "--no-history", "--user", "0xF2129a3b4b3594b28DEF2C4B5Cefc2d1Be3356A4"})
	cmd.SetOut(io.Discard)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, meta := decodeEnvelope(t, out.String())
	d := data.(map[string]any)
	if d["source"] != "lens" || d["navUsd"].(float64) < 349 || d["symbol"] != "hlpTP" || d["chainId"].(float64) != 998 {
		t.Fatalf("%v", d)
	}
	if meta["network"] != "testnet" {
		t.Fatalf("meta %v", meta)
	}
	if u, ok := d["user"].(map[string]any); !ok || u["shares"].(float64) < 14.99 {
		t.Fatalf("user %v", d["user"])
	}
}

func TestPVaultCmdCardAndErrors(t *testing.T) {
	srv := snapshotRPC(t)
	defer srv.Close()
	a, out := pvaultApp(t, false)
	cmd := a.pvaultCmd()
	cmd.SetArgs([]string{"--rpc", srv.URL, "--no-history", "--chain", "998"})
	cmd.SetOut(io.Discard)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "TREND PYRAMID") || !strings.Contains(s, "$349") || strings.Contains(s, "\x1b[") {
		t.Fatalf("card:\n%s", s)
	}

	a, _ = pvaultApp(t, true)
	cmd = a.pvaultCmd()
	cmd.SetArgs([]string{"0x12", "--rpc", srv.URL})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.ExecuteContext(context.Background())
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != core.CodeUsage {
		t.Fatalf("want USAGE, got %v", err)
	}

	a, out = pvaultApp(t, true)
	cmd = a.pvaultCmd()
	cmd.SetArgs([]string{"user", "0x7ffb5ea882eec736200c51209d668a6972092000", "--rpc", srv.URL})
	cmd.SetOut(io.Discard)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, _ := decodeEnvelope(t, out.String())
	if data.(map[string]any)["mayDeposit"] != true {
		t.Fatalf("%v", data)
	}
}
