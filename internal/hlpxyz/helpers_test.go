package hlpxyz

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const fixtures = "../../testdata/fixtures/hlpxyz"

// fixtureNow is just after the fixtures were captured (2026-11-15 ~09:46 UTC).
var fixtureNow = time.UnixMilli(1794736000000).UTC()

const (
	addrGarbage = "0xac26cf5f3c46b5e102048c65b977d2551b72a9c7" // Long HYPE & BTC | Short Garbage
	addrBredo   = "0x654016a8c9fcf0c4cb7ed6078aba21f7f399f7b7"
	addrPVault  = "0x688dD654bDbDE2BF8b6154685437f3F9dB2eAABd"
)

func readFixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeServer replays hlp.xyz fixtures by route and counts hits.
type fakeServer struct {
	t        testing.TB
	srv      *httptest.Server
	mu       sync.Mutex
	hits     map[string]int
	requests []*http.Request
	override map[string]http.HandlerFunc // route -> handler
}

// route names a request: path plus the identifying query parameter.
func route(r *http.Request) string {
	q := r.URL.Query()
	switch {
	case r.URL.Path == "/api/vaults" || r.URL.Path == "/api/pvault" || r.URL.Path == "/api/user":
		return r.URL.Path + "?" + q.Get("action")
	case r.URL.Path == "/api/quant":
		return "/api/quant?" + q.Get("id")
	}
	return r.URL.Path
}

func newFakeServer(t testing.TB) *fakeServer {
	f := &fakeServer{t: t, hits: map[string]int{}, override: map[string]http.HandlerFunc{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) serve(w http.ResponseWriter, r *http.Request) {
	rt := route(r)
	f.mu.Lock()
	f.hits[rt]++
	f.requests = append(f.requests, r.Clone(r.Context()))
	h := f.override[rt]
	f.mu.Unlock()
	if h != nil {
		h(w, r)
		return
	}
	file := ""
	q := r.URL.Query()
	switch rt {
	case "/api/vaults?summary":
		file = "vaults_summary.json"
		if q.Get("kind") == "trader" {
			file = "vaults_summary_trader.json"
		}
	case "/api/vaults?v6status":
		file = "vaults_v6status.json"
	case "/api/vaults?v6spark":
		file = "vaults_v6spark.json"
	case "/api/vaults?live":
		file = "vaults_live_bulk.json"
	case "/api/vaults?benchmark":
		file = "benchmark_spy.json"
	case "/api/vaults?idxList":
		file = "idx_list.json"
	case "/api/vaults?idxGet":
		file = "idx_get_mavericks.json"
	case "/api/vaults?idxCurves":
		file = "idx_curves.json"
	case "/api/user?v6vault":
		file = "user_v6vault.json"
	case "/api/pvault?status":
		file = "pvault_status.json"
	case "/api/pvault?history":
		file = "pvault_history.json"
	case "/api/quant?spxmeme":
		file = "quant_live_spxmeme.json"
	case "/api/quant?trend":
		file = "quant_live_trend.json"
	case "/assets/quant/spxmeme.json":
		file = "quant_spxmeme_static.json"
	case "/assets/quant/trend.json":
		file = "quant_trend_static.json"
	default:
		p := r.URL.Path
		switch {
		case p == "/api/vault/"+HLPAddress+"/live":
			file = "vault_live_hlp.json"
		case strings.HasPrefix(p, "/api/vault/") && strings.HasSuffix(p, "/live"):
			file = "vault_live.json"
		case strings.HasSuffix(p, "/composition"):
			file = "vault_composition_1m.json"
		case strings.HasPrefix(p, "/api/vault/"):
			file = "vault_detail_enc_t1.json"
			if q.Get("light") == "1" {
				file = "vault_detail_light_lev.json"
			}
		}
	}
	if file == "" {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(readFixture(f.t, file))
}

func (f *fakeServer) set(route string, h http.HandlerFunc) {
	f.mu.Lock()
	f.override[route] = h
	f.mu.Unlock()
}

func (f *fakeServer) count(route string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[route]
}

func (f *fakeServer) last() *http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

// client returns a fast client against the fake server.
func (f *fakeServer) client(mod ...func(*Options)) *Client {
	o := Options{
		BaseURL:       f.srv.URL,
		RatePerMinute: 60000,
		Now:           func() time.Time { return fixtureNow },
		LoadingRetry:  time.Millisecond,
		UserAgent:     "hlp-cli/test (+https://github.com/maxmoneycash/hlp-cli)",
	}
	for _, m := range mod {
		m(&o)
	}
	c := New(o)
	c.retryBase = time.Millisecond
	return c
}
