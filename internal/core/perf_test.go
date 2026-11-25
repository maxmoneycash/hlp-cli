package core

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/config"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/evm"
	"github.com/maxmoneycash/hlp-cli/internal/hyperliquid"
	"github.com/maxmoneycash/hlp-cli/internal/theme"
)

const hlFixtures = "../../testdata/fixtures/hyperliquid/info"

// weighed is a logging /info server over the recorded fixtures: it counts
// requests and their documented weight (the PERFORMANCE audit's
// logging transport).
type weighed struct {
	t      testing.TB
	srv    *httptest.Server
	mu     sync.Mutex
	weight int
	n      int
	types  map[string]int
	status map[string]int // type -> forced HTTP status
}

func newWeighed(t testing.TB) *weighed {
	w := &weighed{t: t, types: map[string]int{}, status: map[string]int{}}
	w.srv = httptest.NewServer(http.HandlerFunc(w.serve))
	t.Cleanup(w.srv.Close)
	return w
}

func (w *weighed) counts() (n, weight int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n, w.weight
}

func (w *weighed) fx(name string) []byte {
	b, err := os.ReadFile(filepath.Join(hlFixtures, name))
	if err != nil {
		w.t.Fatalf("fixture %s: %v", name, err)
	}
	return b
}

func (w *weighed) serve(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/info" {
		w.t.Errorf("request to %s", r.URL.Path)
		rw.WriteHeader(500)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	typ, _ := body["type"].(string)
	str := func(k string) string { s, _ := body[k].(string); return s }
	w.mu.Lock()
	w.n++
	w.weight += hyperliquid.Weight(typ)
	w.types[typ]++
	code := w.status[typ]
	w.mu.Unlock()
	if code != 0 {
		rw.WriteHeader(code)
		return
	}
	send := func(b []byte) { rw.Header().Set("Content-Type", "application/json"); _, _ = rw.Write(b) }
	switch typ {
	case "perpDexs":
		send([]byte(`[null]`)) // native dex only keeps the arithmetic obvious
	case "allPerpMetas":
		send([]byte("[" + string(w.fx("meta.json")) + "]"))
	case "meta":
		send(w.fx("meta.json"))
	case "spotMeta":
		send(w.fx("spotMeta.json"))
	case "perpCategories":
		send(w.fx("perpCategories.json"))
	case "outcomeMeta":
		send(w.fx("outcomeMeta.json"))
	case "allMids":
		send(w.fx("allMids.json"))
	case "exchangeStatus":
		send(w.fx("exchangeStatus.json"))
	case "userRateLimit":
		send(w.fx("userRateLimit.json"))
	case "vaultDetails":
		if str("vaultAddress") == HLPAddress {
			send(w.fx("vaultDetails_HLP.json"))
			return
		}
		send([]byte("null"))
	case "clearinghouseState":
		if str("user") == HLPAddress {
			send(w.fx("clearinghouseState_HLP_parent.json"))
			return
		}
		send(w.fx("clearinghouseState_dex_xyz_empty.json"))
	case "spotClearinghouseState":
		send([]byte(`{"balances":[]}`))
	case "borrowLendUserState":
		send([]byte(`{"tokenToState":[],"health":"healthy","healthFactor":null}`))
	case "frontendOpenOrders":
		send([]byte(`[]`))
	default:
		rw.WriteHeader(422)
	}
}

func (w *weighed) env(t testing.TB) *Env {
	ep := hyperliquid.Endpoints{Info: w.srv.URL + "/info", Exchange: w.srv.URL + "/exchange", WS: "ws://unused", Stats: w.srv.URL + "/stats"}
	cache := t.TempDir()
	hl := hyperliquid.New(hyperliquid.Options{Endpoints: &ep, CacheDir: cache,
		Limiter: hyperliquid.NewPersistentLimiter(filepath.Join(cache, "ratelimit-mainnet.json"), 1200, time.Minute)})
	return &Env{Config: config.Defaults(), Paths: config.Paths{CacheDir: cache, ConfigFile: filepath.Join(cache, "config.toml")},
		HL: hl, Guard: DenyAll{}, Theme: theme.Default(true), Now: time.Now}
}

// E-T6: `hlp hlp` cold stays under 900 weight, and the live card resumed
// with a fresh seed costs nothing.
func TestHLPWeightBudget(t *testing.T) {
	w := newWeighed(t)
	env := w.env(t)
	ctx := context.Background()
	res, err := Default().Invoke(ctx, env, Call{Tool: "hlp_positioning", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	n, weight := w.counts()
	if weight > 900 {
		t.Fatalf("cold hlp = %d requests, %d weight (> 900): %v", n, weight, w.types)
	}
	if w.types["allMids"] > 1 || w.types["vaultDetails"] != 1 {
		t.Fatalf("fan-out not deduped: %v", w.types)
	}
	a := res.Data.(domain.Account)
	for _, p := range a.Positions {
		if p.Vault == "" {
			t.Fatalf("position %s has no vault", p.Coin)
		}
	}
	// The TUI seeds the live card with the result it just showed.
	var emitted int
	err = Default().RunStream(WithSeed(ctx, res), env, Call{Tool: "hlp_positioning", Args: json.RawMessage(`{}`)}, func(*Result) { emitted++ })
	if err != nil {
		t.Fatal(err)
	}
	if n2, w2 := w.counts(); n2 != n {
		t.Fatalf("seeded live card made %d requests (%d weight)", n2-n, w2-weight)
	}
}

func TestStatusChecks(t *testing.T) {
	w := newWeighed(t)
	env := w.env(t)
	env.Config.HLPXYZ.Disabled = true
	env.Config.Network = domain.Mainnet
	old := statusTimeout
	statusTimeout = 2 * time.Second
	defer func() { statusTimeout = old }()
	// HyperEVM: a local JSON-RPC that answers chain 999.
	rpc := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x3e7"}`))
	}))
	defer rpc.Close()
	env.Config.HyperEVM = map[string]evm.Chain{"999": {RPC: []string{rpc.URL}, RPCOnly: true}}
	res, err := Default().Invoke(context.Background(), env, Call{Tool: "status", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	d := res.Data.(StatusData)
	names := map[string]StatusCheck{}
	for _, c := range d.Checks {
		names[c.Name] = c
	}
	for _, want := range []string{"info", "ws", "limiter", "hlpxyz", "hyperevm:999", "config"} {
		c, ok := names[want]
		if !ok || !c.OK {
			t.Fatalf("check %s: %+v (all %+v)", want, c, d.Checks)
		}
	}
	if !d.OK || res.Card != CardStatus {
		t.Fatalf("status: %+v", d)
	}
	if home, _ := os.UserHomeDir(); home != "" && strings.Contains(names["config"].Detail, home) && !strings.HasPrefix(env.Paths.ConfigFile, os.TempDir()) {
		t.Fatalf("config path not redacted: %s", names["config"].Detail)
	}

	// A failing /info makes the call an error (exit 1) carrying every check.
	w.status["exchangeStatus"] = 503
	_, err = Default().Invoke(context.Background(), env, Call{Tool: "status", Args: json.RawMessage(`{}`)})
	e := AsError(err)
	if e == nil || e.Code != CodeAPI || ExitCode(err) == 0 {
		t.Fatalf("failing status: %v", err)
	}
	if sd, ok := e.Details.(StatusData); !ok || sd.OK || len(sd.Checks) == 0 {
		t.Fatalf("details: %+v", e.Details)
	}
}

func TestAgentAddressMustMatchKey(t *testing.T) {
	if err := checkAgentAddress("0xabc", ""); err != nil {
		t.Fatal(err)
	}
	if err := checkAgentAddress("0xAbC", "0xabc"); err != nil {
		t.Fatal(err)
	}
	err := checkAgentAddress("0xabc", "0xdef")
	if e := AsError(err); e == nil || e.Code != CodeUsage || !strings.Contains(e.Message, "0xdef") {
		t.Fatalf("mismatch: %v", err)
	}
}

func TestHLPXYZOffIsNotFound(t *testing.T) {
	env := &Env{}
	_, err := env.needHLPXYZ()
	if e := AsError(err); e == nil || e.Code != CodeNotFound || ExitCode(err) != 1 {
		t.Fatalf("needHLPXYZ: %v", err)
	}
}

func TestParallelNBound(t *testing.T) {
	var mu sync.Mutex
	cur, peak := 0, 0
	parallelN(10, 2, func(int) {
		mu.Lock()
		cur++
		peak = max(peak, cur)
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		cur--
		mu.Unlock()
	})
	if peak > 2 {
		t.Fatalf("peak concurrency %d", peak)
	}
}
