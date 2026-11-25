// Command apphost is a local dev host for hlp's MCP Apps views. It serves one page that plays the host
// side of the io.modelcontextprotocol/ui bridge: it loads a view from
// internal/cli/mcp/apps into a sandboxed iframe, replays a recorded tool
// result into it, answers the view's tools/call from the fixtures, logs
// ui/update-model-context, and lets you toggle the theme and the host's
// serverTools capability.
//
//	go run ./tools/apphost              # http://127.0.0.1:8765
//	HLP_APPHOST_UPDATE=1 go test ./tools/apphost   # re-record fixtures (offline)
//
// It binds to loopback only, makes no network calls and never runs the MCP
// server: fixtures are recorded results.
package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/maxmoneycash/hlp-cli/internal/cli/mcp/apps"
)

//go:embed index.html fixtures/*.json
var static embed.FS

// fixture is one recorded call.
type fixture struct {
	Tool      string              `json:"tool"`
	URI       string              `json:"uri,omitempty"`
	Arguments map[string]any      `json:"arguments"`
	Result    *sdk.CallToolResult `json:"result"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8765", "listen address (loopback only)")
	flag.Parse()
	host, _, err := net.SplitHostPort(*addr)
	if err != nil || !(host == "127.0.0.1" || host == "localhost" || host == "::1") {
		log.Fatalf("apphost binds to loopback only, got %q", *addr)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("hlp apphost: http://%s/\n", ln.Addr())
	log.Fatal(http.Serve(ln, handler()))
}

func handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		b, _ := static.ReadFile("index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; frame-src 'self'")
		_, _ = w.Write(b)
	})
	// /view?uri=ui://hlp/chart.html serves the exact resource text, with
	// the empty CSP the resource declares (no connect, no external loads).
	mux.HandleFunc("GET /view", func(w http.ResponseWriter, r *http.Request) {
		doc, err := apps.HTML(r.URL.Query().Get("uri"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:")
		_, _ = w.Write([]byte(doc))
	})
	mux.HandleFunc("GET /fixtures", func(w http.ResponseWriter, _ *http.Request) {
		all, err := loadFixtures()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(all)
	})
	return mux
}

// loadFixtures returns every fixture by file name (without .json).
func loadFixtures() (map[string]fixture, error) {
	names, err := fs.Glob(static, "fixtures/*.json")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	out := map[string]fixture{}
	for _, n := range names {
		b, err := static.ReadFile(n)
		if err != nil {
			return nil, err
		}
		var f fixture
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		out[strings.TrimSuffix(strings.TrimPrefix(n, "fixtures/"), ".json")] = f
	}
	return out, nil
}
