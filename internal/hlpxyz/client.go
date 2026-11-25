package hlpxyz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// DefaultBaseURL: call www directly (bare hlp.xyz 307-redirects).
const DefaultBaseURL = "https://www.hlp.xyz"

// DefaultUserAgent identifies CLI traffic to the hlp.xyz owner.
const DefaultUserAgent = "hlp-cli/dev (+https://github.com/maxmoneycash/hlp-cli)"

// HLPAddress is the Hyperliquidity Provider (HLP) parent vault.
const HLPAddress = "0xdfc24b077bc1425ad1dea75bcb6f8158e10df303"

// LiveChunk is the most addresses hlp.xyz accepts per bulk /live call.
const LiveChunk = 50

// BenchmarkSymbols are the symbols /api/vaults?action=benchmark accepts.
var BenchmarkSymbols = []string{"SPY", "NQ", "DOW", "NVDA", "BTC", "ETH", "SOL", "HYPE"}

// CompositionRanges are the ranges /api/vault/<addr>/composition accepts.
var CompositionRanges = []string{"1d", "1w", "1m", "3m", "6m", "1y", "ytd", "all"}

// ErrBadAddress is returned for inputs that are not 0x-prefixed 20-byte hex
// addresses (checked locally, before any request).
var ErrBadAddress = errors.New("not a 0x address")

// Options configures the client.
type Options struct {
	BaseURL   string       // default DefaultBaseURL
	HTTP      *http.Client // default 20s timeout
	UserAgent string       // "hlp-cli/<version> (+https://github.com/maxmoneycash/hlp-cli)" so the owner can identify CLI traffic
	// RatePerMinute keeps us well under the server's 120/window limit.
	// Default 90. 429 -> honor x-ratelimit-reset.
	RatePerMinute int
	CacheDir      string // disk cache: summary/v6status/v6spark 60 s, detail 5 min
	Now           func() time.Time

	// LoadingRetry is the wait between polls while a vault detail answers
	// {status:"loading"} (default 5 s, as the website does). Tests shorten it.
	LoadingRetry time.Duration
	// LoadingTries caps those polls (default 60).
	LoadingTries int
	// LiveBudget bounds how long VaultList waits for bulk /live TVL
	// (default 4 s). hlp.xyz computes /live per address and a cold chunk
	// of 50 can take 10-30 s; past the budget the list falls back to
	// summary equity (Meta.Partial) instead of blocking the screener.
	// HLP's own /live is always fetched separately (fast, ~150 ms).
	LiveBudget time.Duration
}

// Client is the hlp.xyz public API client (only endpoints that need no
// auth). Never call auth/record/idxInv*/manage*.
// It is safe for concurrent use.
type Client struct {
	opts      Options
	limit     *limiter
	cache     *cache
	retryBase time.Duration // first backoff on 5xx/network errors (tests shorten it)
}

// New builds a client.
func New(opts Options) *Client {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	if opts.UserAgent == "" {
		opts.UserAgent = DefaultUserAgent
	}
	if opts.RatePerMinute == 0 {
		opts.RatePerMinute = 90
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.LoadingRetry <= 0 {
		opts.LoadingRetry = 5 * time.Second
	}
	if opts.LoadingTries <= 0 {
		opts.LoadingTries = 60
	}
	if opts.LiveBudget <= 0 {
		opts.LiveBudget = 4 * time.Second
	}
	dir := ""
	if opts.CacheDir != "" {
		dir = filepath.Join(opts.CacheDir, cacheSubdir)
	}
	return &Client{
		opts:      opts,
		limit:     newLimiter(opts.RatePerMinute),
		cache:     &cache{mem: map[string]cacheEntry{}, dir: dir},
		retryBase: 250 * time.Millisecond,
	}
}

// API is the surface core and the TUI code against (fakes in tests).
type API interface {
	// VaultList joins summary + v6status + v6spark on address (3 parallel
	// GETs). Returns use v6status.ret (fractions), share price model SPv6.
	// Rows whose freshest metric source (summary lastUpdated or v6status
	// computedAt) is older than 48 h are Stale. TVL comes from bulk /live,
	// so HLP headline numbers never come from the stale summary row.
	VaultList(ctx context.Context) ([]domain.VaultSummary, domain.Meta, error)
	Summary(ctx context.Context, traders bool) (SummaryResponse, error)
	V6Status(ctx context.Context) (map[string]V6Status, error)
	V6Spark(ctx context.Context) (map[string]domain.Series, error)
	// Live is the bulk /live (<= 50 addresses per call; chunk internally).
	Live(ctx context.Context, addrs []string) ([]LiveVault, error)
	VaultLive(ctx context.Context, addr string) (VaultLive, error)
	// VaultDetail handles {status:"loading"} (retry every 5 s, <= 60 tries,
	// respecting ctx) and {status:"no-cache"} (returns ErrNoCache).
	VaultDetail(ctx context.Context, addr string, o DetailOpts) (VaultDetail, error)
	Composition(ctx context.Context, addr string, rng string) (Composition, error)
	NAV(ctx context.Context, addr string) (NAVCurve, error) // /api/user?action=v6vault
	Benchmark(ctx context.Context, symbol string, start, end time.Time) (domain.Series, error)

	Indexes(ctx context.Context, all bool) ([]Index, error)
	Index(ctx context.Context, slug string) (Index, error)

	PVaultStatus(ctx context.Context) (PVaultStatus, error)
	PVaultHistory(ctx context.Context, chainID int, vault string) ([]PVaultSample, error)
	Quant(ctx context.Context, id string) (QuantLive, error)           // /api/quant?id=
	QuantBacktest(ctx context.Context, id string) (QuantStatic, error) // /assets/quant/<id>.json
}

var _ API = (*Client)(nil)

var (
	addrRe = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	slugRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
)

// NormalizeAddress trims and lowercases a vault/user address and checks
// its shape. hlp.xyz keys every map by lowercase address.
func NormalizeAddress(a string) (string, error) {
	a = strings.TrimSpace(a)
	if !addrRe.MatchString(a) {
		return "", fmt.Errorf("%w: %q", ErrBadAddress, a)
	}
	return strings.ToLower(a), nil
}

func vaultsReq(action string, ttl time.Duration, kv ...string) request {
	q := url.Values{"action": {action}}
	for i := 0; i+1 < len(kv); i += 2 {
		q.Set(kv[i], kv[i+1])
	}
	return request{path: "/api/vaults", query: q, ttl: ttl}
}

// Summary returns the curated vault list (/api/vaults?action=summary, or
// tracked trader accounts with traders=true). Wire values are percents.
func (c *Client) Summary(ctx context.Context, traders bool) (SummaryResponse, error) {
	r, _, err := c.summary(ctx, traders)
	return r, err
}

func (c *Client) summary(ctx context.Context, traders bool) (SummaryResponse, fetchInfo, error) {
	req := vaultsReq("summary", ttlList)
	if traders {
		req.query.Set("kind", "trader")
	}
	var r SummaryResponse
	info, err := c.getJSON(ctx, req, &r)
	for i := range r.Vaults {
		r.Vaults[i].Address = strings.ToLower(r.Vaults[i].Address)
	}
	return r, info, err
}

// V6Status returns v6 NAV-pipeline metrics keyed by lowercase address.
// ret/maxDD are PERCENT on the wire (see V6Status).
func (c *Client) V6Status(ctx context.Context) (map[string]V6Status, error) {
	m, _, err := c.v6status(ctx)
	return m, err
}

func (c *Client) v6status(ctx context.Context) (map[string]V6Status, fetchInfo, error) {
	var r struct {
		Statuses map[string]V6Status `json:"statuses"`
	}
	info, err := c.getJSON(ctx, vaultsReq("v6status", ttlList), &r)
	if err != nil {
		return nil, info, err
	}
	out := make(map[string]V6Status, len(r.Statuses))
	for k, v := range r.Statuses {
		out[strings.ToLower(k)] = v
	}
	return out, info, nil
}

// V6Spark returns ~100-point v6 share-price sparklines keyed by lowercase
// address.
func (c *Client) V6Spark(ctx context.Context) (map[string]domain.Series, error) {
	m, _, err := c.v6spark(ctx)
	return m, err
}

func (c *Client) v6spark(ctx context.Context) (map[string]domain.Series, fetchInfo, error) {
	var r struct {
		Sparks map[string][][2]float64 `json:"sparks"`
	}
	info, err := c.getJSON(ctx, vaultsReq("v6spark", ttlList), &r)
	if err != nil {
		return nil, info, err
	}
	out := make(map[string]domain.Series, len(r.Sparks))
	for k, pts := range r.Sparks {
		out[strings.ToLower(k)] = pairsToSeries(pts)
	}
	return out, info, nil
}

func pairsToSeries(pts [][2]float64) domain.Series {
	s := make(domain.Series, 0, len(pts))
	for _, p := range pts {
		s = append(s, domain.Point{T: int64(p[0]), V: p[1]})
	}
	return s
}

// Live returns live share price and equity for many vaults (bulk /live),
// chunked by LiveChunk addresses (chunks run concurrently). Invalid
// addresses are an error; duplicates are fetched once. Order follows the
// chunks, then the server's answer within each chunk. hlp.xyz computes
// /live per address, so a cold chunk can take many seconds: bound ctx.
func (c *Client) Live(ctx context.Context, addrs []string) ([]LiveVault, error) {
	out, _, err := c.live(ctx, addrs)
	return out, err
}

func (c *Client) live(ctx context.Context, addrs []string) ([]LiveVault, fetchInfo, error) {
	seen := map[string]bool{}
	norm := make([]string, 0, len(addrs))
	for _, a := range addrs {
		n, err := NormalizeAddress(a)
		if err != nil {
			return nil, fetchInfo{}, err
		}
		if !seen[n] {
			seen[n] = true
			norm = append(norm, n)
		}
	}
	var chunks [][]string
	for i := 0; i < len(norm); i += LiveChunk {
		chunks = append(chunks, norm[i:min(i+LiveChunk, len(norm))])
	}
	type res struct {
		rows []LiveVault
		info fetchInfo
		err  error
	}
	results := make([]res, len(chunks))
	var wg sync.WaitGroup
	for i, chunk := range chunks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var r struct {
				Vaults []LiveVault `json:"vaults"`
			}
			fi, err := c.getJSON(ctx, vaultsReq("live", ttlList, "addresses", strings.Join(chunk, ",")), &r)
			results[i] = res{r.Vaults, fi, err}
		}()
	}
	wg.Wait()
	var out []LiveVault
	var info fetchInfo
	for _, r := range results {
		if r.err != nil {
			return nil, info, r.err
		}
		if info.At.IsZero() || r.info.At.Before(info.At) {
			info.At = r.info.At
		}
		info.Stale = info.Stale || r.info.Stale
		for _, v := range r.rows {
			v.Address = strings.ToLower(v.Address)
			out = append(out, v)
		}
	}
	return out, info, nil
}

// VaultLive returns the live header for any Hyperliquid vault (tracked or
// not). A non-vault address returns ErrNotAVault.
func (c *Client) VaultLive(ctx context.Context, addr string) (VaultLive, error) {
	a, err := NormalizeAddress(addr)
	if err != nil {
		return VaultLive{}, err
	}
	var v VaultLive
	_, err = c.getJSON(ctx, request{path: "/api/vault/" + a + "/live", ttl: ttlList}, &v)
	if err != nil {
		if isNotFound(err) {
			return VaultLive{}, fmt.Errorf("%w: %s", ErrNotAVault, a)
		}
		return VaultLive{}, err
	}
	if v.Error != "" {
		return VaultLive{}, fmt.Errorf("%w: %s (%s)", ErrNotAVault, a, v.Error)
	}
	return v, nil
}

func isNotFound(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	return ae.Status == http.StatusNotFound || strings.Contains(strings.ToLower(ae.Message), "not found")
}

// detailStatus peeks at {"status": ...} without decoding the whole body.
func detailStatus(b []byte) string {
	var s struct {
		Status string `json:"status"`
	}
	// A full detail is large; status-only answers are tiny.
	if len(b) > 256 {
		return ""
	}
	if json.Unmarshal(b, &s) != nil {
		return ""
	}
	return s.Status
}

// VaultDetail returns the full (enc=t1) or light vault history. While
// hlp.xyz builds its cache it answers {status:"loading"}: this polls every
// Options.LoadingRetry up to Options.LoadingTries times (respecting ctx),
// then returns ErrLoading. An untracked vault returns ErrNoCache.
func (c *Client) VaultDetail(ctx context.Context, addr string, o DetailOpts) (VaultDetail, error) {
	a, err := NormalizeAddress(addr)
	if err != nil {
		return VaultDetail{}, err
	}
	q := url.Values{}
	if o.Light {
		q.Set("light", "1")
		if o.Lev {
			q.Set("lev", "1")
		}
	} else {
		q.Set("enc", "t1")
	}
	if o.Trader {
		q.Set("kind", "trader")
	}
	req := request{
		path: "/api/vault/" + a, query: q, ttl: ttlDetail,
		cacheable: func(b []byte) bool { return detailStatus(b) == "" },
	}
	for try := 0; ; try++ {
		body, _, err := c.fetch(ctx, req)
		if err != nil {
			return VaultDetail{}, err
		}
		switch st := detailStatus(body); st {
		case "":
		case "no-cache":
			return VaultDetail{}, fmt.Errorf("%w: %s", ErrNoCache, a)
		case "loading":
			if try+1 >= c.opts.LoadingTries {
				return VaultDetail{}, fmt.Errorf("%w: %s", ErrLoading, a)
			}
			if err := sleep(ctx, c.opts.LoadingRetry); err != nil {
				return VaultDetail{}, err
			}
			continue
		default:
			return VaultDetail{}, &APIError{Status: http.StatusOK, Message: "unexpected status " + strconv.Quote(st), Path: req.path}
		}
		var d VaultDetail
		if err := json.Unmarshal(body, &d); err != nil {
			return VaultDetail{}, fmt.Errorf("hlp.xyz %s: decode: %w", req.path, err)
		}
		return d, nil
	}
}

// Composition returns per-coin signed notional over time. rng is one of
// CompositionRanges ("" = "1m").
func (c *Client) Composition(ctx context.Context, addr string, rng string) (Composition, error) {
	a, err := NormalizeAddress(addr)
	if err != nil {
		return Composition{}, err
	}
	if rng == "" {
		rng = "1m"
	}
	rng = strings.ToLower(rng)
	if !contains(CompositionRanges, rng) {
		return Composition{}, fmt.Errorf("hlpxyz: composition range %q not in %s", rng, strings.Join(CompositionRanges, "|"))
	}
	var r Composition
	_, err = c.getJSON(ctx, request{path: "/api/vault/" + a + "/composition", query: url.Values{"range": {rng}}, ttl: ttlDetail}, &r)
	return r, err
}

// NAV returns the full v6 NAV curve (/api/user?action=v6vault, public).
func (c *Client) NAV(ctx context.Context, addr string) (NAVCurve, error) {
	a, err := NormalizeAddress(addr)
	if err != nil {
		return NAVCurve{}, err
	}
	var r NAVCurve
	_, err = c.getJSON(ctx, request{path: "/api/user", query: url.Values{"action": {"v6vault"}, "address": {a}}, ttl: ttlDetail}, &r)
	return r, err
}

// Benchmark returns daily closes for one of BenchmarkSymbols between start
// and end (UTC dates; hlp.xyz rejects ms timestamps). Points are stamped at
// UTC midnight of each date.
func (c *Client) Benchmark(ctx context.Context, symbol string, start, end time.Time) (domain.Series, error) {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	if !contains(BenchmarkSymbols, sym) {
		return nil, fmt.Errorf("hlpxyz: benchmark %q not in %s", symbol, strings.Join(BenchmarkSymbols, ", "))
	}
	if end.IsZero() {
		end = c.opts.Now()
	}
	if !start.Before(end) {
		return nil, fmt.Errorf("hlpxyz: benchmark start %s is not before end %s", start.Format(time.DateOnly), end.Format(time.DateOnly))
	}
	var r struct {
		Symbol string `json:"symbol"`
		Prices []struct {
			Date  string  `json:"date"`
			Price float64 `json:"price"`
		} `json:"prices"`
	}
	_, err := c.getJSON(ctx, vaultsReq("benchmark", ttlStatic,
		"symbol", sym,
		"start", start.UTC().Format(time.DateOnly),
		"end", end.UTC().Format(time.DateOnly),
		"interval", "day"), &r)
	if err != nil {
		return nil, err
	}
	s := make(domain.Series, 0, len(r.Prices))
	for _, p := range r.Prices {
		t, err := time.Parse(time.DateOnly, p.Date)
		if err != nil {
			if t, err = time.Parse(time.RFC3339, p.Date); err != nil {
				return nil, fmt.Errorf("hlpxyz: benchmark date %q: %w", p.Date, err)
			}
		}
		if p.Price > 0 {
			s = append(s, domain.Point{T: t.UTC().UnixMilli(), V: p.Price})
		}
	}
	return s, nil
}

// Indexes lists vault indexes (official public ones; all=true adds hidden).
func (c *Client) Indexes(ctx context.Context, all bool) ([]Index, error) {
	req := vaultsReq("idxList", ttlStatic)
	if all {
		req.query.Set("all", "1")
	}
	var r struct {
		Indexes []Index `json:"indexes"`
	}
	_, err := c.getJSON(ctx, req, &r)
	for i := range r.Indexes {
		r.Indexes[i].clean()
	}
	return r.Indexes, err
}

// Index returns one index by slug.
func (c *Client) Index(ctx context.Context, slug string) (Index, error) {
	if !slugRe.MatchString(slug) {
		return Index{}, fmt.Errorf("hlpxyz: bad index slug %q", slug)
	}
	var r Index
	_, err := c.getJSON(ctx, vaultsReq("idxGet", ttlStatic, "slug", strings.ToLower(slug)), &r)
	if err == nil && r.Def.Slug == "" {
		err = &APIError{Status: http.StatusNotFound, Message: "index not found", Path: "/api/vaults?action=idxGet&slug=" + slug}
	}
	r.clean()
	return r, err
}

// PVaultStatus returns keeper and bot telemetry for the hlp.xyz on-chain
// strategy-vault prototype (HyperEVM).
func (c *Client) PVaultStatus(ctx context.Context) (PVaultStatus, error) {
	var r PVaultStatus
	_, err := c.getJSON(ctx, request{path: "/api/pvault", query: url.Values{"action": {"status"}}, ttl: ttlList}, &r)
	return r, err
}

// PVaultHistory returns the share-price, NAV, supply and keeper-gas samples
// of a pvault on chainID (998 testnet, 999 mainnet), oldest first.
func (c *Client) PVaultHistory(ctx context.Context, chainID int, vault string) ([]PVaultSample, error) {
	if !addrRe.MatchString(strings.TrimSpace(vault)) {
		return nil, fmt.Errorf("%w: %q", ErrBadAddress, vault)
	}
	if chainID <= 0 {
		return nil, fmt.Errorf("hlpxyz: bad chain id %d", chainID)
	}
	var r struct {
		Samples []PVaultSample `json:"samples"`
	}
	// Keep the vault's checksum case: the server echoes it as given.
	_, err := c.getJSON(ctx, request{path: "/api/pvault", query: url.Values{
		"action": {"history"}, "chain": {strconv.Itoa(chainID)}, "vault": {strings.TrimSpace(vault)},
	}, ttl: ttlDetail}, &r)
	return r.Samples, err
}

// Quant returns a bot strategy's live target book (e.g. "trend", "spxmeme").
func (c *Client) Quant(ctx context.Context, id string) (QuantLive, error) {
	if !slugRe.MatchString(id) {
		return QuantLive{}, fmt.Errorf("hlpxyz: bad strategy id %q", id)
	}
	var r QuantLive
	_, err := c.getJSON(ctx, request{path: "/api/quant", query: url.Values{"id": {id}}, ttl: ttlStatic}, &r)
	for i := range r.Positions {
		r.Positions[i].Coin = domain.CleanText(r.Positions[i].Coin)
		r.Positions[i].Label = domain.CleanText(r.Positions[i].Label)
	}
	return r, err
}

// QuantBacktest returns a strategy's static backtest (/assets/quant/<id>.json).
func (c *Client) QuantBacktest(ctx context.Context, id string) (QuantStatic, error) {
	if !slugRe.MatchString(id) {
		return QuantStatic{}, fmt.Errorf("hlpxyz: bad strategy id %q", id)
	}
	var r QuantStatic
	_, err := c.getJSON(ctx, request{path: "/assets/quant/" + id + ".json", ttl: ttlStatic}, &r)
	r.Name, r.Blurb, r.Status = domain.CleanText(r.Name), domain.CleanText(r.Blurb), domain.CleanText(r.Status)
	return r, err
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
