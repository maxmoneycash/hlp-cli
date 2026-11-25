package hyperliquid

import (
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

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

const fxDir = "../../testdata/fixtures/hyperliquid"

const (
	addrTrader    = "0xf5d81a135f756ca16544e53c20fc20643ec3ad53"
	addrHLP       = "0xdfc24b077bc1425ad1dea75bcb6f8158e10df303"
	addrStrategyX = "0x469f690213c467c39a23efacfd2816896009d7d8"
)

func readFx(t testing.TB, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fxDir, rel))
	if err != nil {
		t.Fatalf("fixture %s: %v", rel, err)
	}
	return b
}

// fakeHL is an httptest /info server that answers from fixtures.
type fakeHL struct {
	t    testing.TB
	srv  *httptest.Server
	mu   sync.Mutex
	hits map[string]int
	// override lets a test intercept a request type.
	override func(typ string, body map[string]any) (int, []byte, bool)
}

func newFakeHL(t testing.TB) *fakeHL {
	f := &fakeHL{t: t, hits: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHL) count(typ string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[typ]
}

func (f *fakeHL) client(t testing.TB) *Client {
	ep := Endpoints{Info: f.srv.URL + "/info", Stats: f.srv.URL + "/stats", WS: "ws://unused", Exchange: f.srv.URL + "/exchange"}
	c := New(Options{Endpoints: &ep, Limiter: NewLimiter(100000, time.Minute)})
	c.retryBase = time.Millisecond
	c.penalty = time.Millisecond
	return c
}

func emptyCH() []byte {
	return []byte(`{"marginSummary":{"accountValue":"0.0","totalNtlPos":"0.0","totalRawUsd":"0.0","totalMarginUsed":"0.0"},"crossMarginSummary":{"accountValue":"0.0","totalNtlPos":"0.0","totalRawUsd":"0.0","totalMarginUsed":"0.0"},"crossMaintenanceMarginUsed":"0.0","withdrawable":"0.0","assetPositions":[],"time":1794735423948}`)
}

func (f *fakeHL) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/exchange" {
		f.t.Errorf("request to /exchange: must never happen")
		w.WriteHeader(500)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	typ, _ := body["type"].(string)
	f.mu.Lock()
	f.hits[typ]++
	f.mu.Unlock()
	if f.override != nil {
		if code, b, ok := f.override(typ, body); ok {
			w.WriteHeader(code)
			w.Write(b)
			return
		}
	}
	str := func(k string) string { s, _ := body[k].(string); return s }
	send := func(b []byte) { w.Header().Set("Content-Type", "application/json"); w.Write(b) }
	switch typ {
	case "perpDexs":
		send(readFx(f.t, "info/perpDexs.json"))
	case "allPerpMetas":
		var third []json.RawMessage
		_ = json.Unmarshal(readFx(f.t, "info/allPerpMetas.json"), &third)
		out, _ := json.Marshal([]json.RawMessage{readFx(f.t, "info/meta.json"), readFx(f.t, "info/meta_dex_xyz.json"), third[2]})
		send(out)
	case "meta":
		switch str("dex") {
		case "":
			send(readFx(f.t, "info/meta.json"))
		case "xyz":
			send(readFx(f.t, "info/meta_dex_xyz.json"))
		default:
			w.WriteHeader(422)
			w.Write([]byte("Failed to deserialize the JSON body into the target type"))
		}
	case "spotMeta":
		send(readFx(f.t, "info/spotMeta.json"))
	case "perpCategories":
		send(readFx(f.t, "info/perpCategories.json"))
	case "outcomeMeta":
		send(readFx(f.t, "info/outcomeMeta.json"))
	case "allMids":
		send(readFx(f.t, "info/allMids.json"))
	case "metaAndAssetCtxs":
		if str("dex") == "" {
			send(readFx(f.t, "info/metaAndAssetCtxs.json"))
			return
		}
		if str("dex") == "xyz" {
			var native [2]json.RawMessage
			_ = json.Unmarshal(readFx(f.t, "info/metaAndAssetCtxs.json"), &native)
			var ctxs []json.RawMessage
			_ = json.Unmarshal(native[1], &ctxs)
			out, _ := json.Marshal([]any{json.RawMessage(readFx(f.t, "info/meta_dex_xyz.json")), ctxs[:3]})
			send(out)
			return
		}
		w.WriteHeader(500)
		w.Write([]byte("null"))
	case "spotMetaAndAssetCtxs":
		send(readFx(f.t, "info/spotMetaAndAssetCtxs.json"))
	case "clearinghouseState":
		switch {
		case str("user") == addrTrader && str("dex") == "":
			send(readFx(f.t, "info/clearinghouseState.json"))
		case str("user") == addrTrader && str("dex") == "xyz":
			send(readFx(f.t, "info/clearinghouseState_dex_xyz_empty.json"))
		case str("user") == addrHLP && str("dex") == "":
			send(readFx(f.t, "info/clearinghouseState_HLP_parent.json"))
		case str("user") == addrStrategyX && str("dex") == "":
			send([]byte(strings.ReplaceAll(string(emptyCH()), `"accountValue":"0.0"`, `"accountValue":"1000.0"`)))
		default:
			send(emptyCH())
		}
	case "spotClearinghouseState":
		if str("user") == addrTrader {
			send(readFx(f.t, "info/spotClearinghouseState.json"))
			return
		}
		send([]byte(`{"balances":[]}`))
	case "borrowLendUserState":
		if str("user") == addrStrategyX {
			send(readFx(f.t, "info/borrowLendUserState_HLP_StrategyX.json"))
			return
		}
		send([]byte(`{"tokenToState":[],"health":"healthy","healthFactor":null}`))
	case "frontendOpenOrders":
		if str("user") == addrTrader && str("dex") == "" {
			send(readFx(f.t, "info/frontendOpenOrders.json"))
			return
		}
		send([]byte(`[]`))
	case "userFills", "userFillsByTime":
		send(readFx(f.t, "info/userFills.json"))
	case "vaultDetails":
		switch str("vaultAddress") {
		case addrHLP:
			send(readFx(f.t, "info/vaultDetails_HLP.json"))
		case addrStrategyX:
			send(readFx(f.t, "info/vaultDetails_HLP_child_StrategyX.json"))
		default:
			send([]byte("null"))
		}
	case "candleSnapshot":
		req, _ := body["req"].(map[string]any)
		if req["coin"] == "BTC" {
			send(readFx(f.t, "info/candleSnapshot_BTC_1h.json"))
			return
		}
		w.WriteHeader(500)
		w.Write([]byte("null"))
	case "l2Book":
		switch str("coin") {
		case "BTC":
			send(readFx(f.t, "info/l2Book_BTC.json"))
		case "xyz:NVDA":
			send(readFx(f.t, "info/l2Book_xyz_NVDA.json"))
		default:
			w.WriteHeader(500)
			w.Write([]byte("null"))
		}
	case "recentTrades":
		send(readFx(f.t, "info/recentTrades_BTC.json"))
	case "fundingHistory":
		send(readFx(f.t, "info/fundingHistory_BTC.json"))
	case "userRole":
		send(readFx(f.t, "info/userRole_vault.json"))
	case "userVaultEquities":
		send(readFx(f.t, "info/userVaultEquities.json"))
	case "portfolio":
		send(readFx(f.t, "info/portfolio.json"))
	default:
		w.WriteHeader(422)
		w.Write([]byte("Failed to deserialize the JSON body into the target type"))
	}
}

// fixtureRegistryData builds RegistryData straight from fixture files.
func fixtureRegistryData(t testing.TB) RegistryData {
	var d RegistryData
	must := func(rel string, v any) {
		if err := json.Unmarshal(readFx(t, rel), v); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
	}
	must("info/perpDexs.json", &d.Dexs)
	var native, xyz Meta
	must("info/meta.json", &native)
	must("info/meta_dex_xyz.json", &xyz)
	d.Metas = []Meta{native, xyz}
	must("info/spotMeta.json", &d.Spot)
	must("info/perpCategories.json", &d.Categories)
	must("info/outcomeMeta.json", &d.Outcomes)
	d.FetchedAt = time.Now()
	return d
}

var _ = domain.Mainnet
