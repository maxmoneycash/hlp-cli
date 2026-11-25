package hyperliquid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// Endpoints for one network.
type Endpoints struct {
	Info     string // https://api.hyperliquid.xyz/info
	Exchange string // https://api.hyperliquid.xyz/exchange
	WS       string // wss://api.hyperliquid.xyz/ws
	Stats    string // https://stats-data.hyperliquid.xyz/Mainnet
}

// EndpointsFor returns the public endpoints for a network.
func EndpointsFor(n domain.Network) Endpoints {
	if n == domain.Testnet {
		return Endpoints{
			Info:     "https://api.hyperliquid-testnet.xyz/info",
			Exchange: "https://api.hyperliquid-testnet.xyz/exchange",
			WS:       "wss://api.hyperliquid-testnet.xyz/ws",
			Stats:    "https://stats-data.hyperliquid.xyz/Testnet",
		}
	}
	return Endpoints{
		Info:     "https://api.hyperliquid.xyz/info",
		Exchange: "https://api.hyperliquid.xyz/exchange",
		WS:       "wss://api.hyperliquid.xyz/ws",
		Stats:    "https://stats-data.hyperliquid.xyz/Mainnet",
	}
}

// DefaultPenalty is how long an HTTP 429 pauses every request in the
// process (and in other hlp processes sharing a persistent limiter).
const DefaultPenalty = 5 * time.Second

// maxBody caps one /info response (the largest, spotMetaAndAssetCtxs, is
// ~0.4 MB): SECURITY S13.
const maxBody = 64 << 20

// DefaultUserAgent is sent when Options.UserAgent is empty.
const DefaultUserAgent = "hlp/dev (+https://github.com/maxmoneycash/hlp-cli)"

// Options configures a Client. Zero values pick safe defaults.
type Options struct {
	Network   domain.Network
	Endpoints *Endpoints   // override (tests point this at httptest servers)
	HTTP      *http.Client // default: 15s timeout
	CacheDir  string       // on-disk cache for meta, perpDexs, stats vaults ("" = memory only)
	UserAgent string       // default "hlp/<version> (+https://github.com/maxmoneycash/hlp-cli)"
	Limiter   *Limiter     // shared across clients in one process; default NewLimiter(1200, time.Minute)
	Now       func() time.Time
}

// Client talks to /info and stats-data. It is safe for concurrent use.
type Client struct {
	opts Options
	ep   Endpoints

	// retryBase is the first backoff step for 429/5xx retries (tests shrink it).
	retryBase time.Duration
	// maxRetries is the number of retries after the first attempt.
	maxRetries int
	// penalty is how long a 429 holds every request back (tests shrink it).
	penalty time.Duration

	// memo dedupes and briefly caches idempotent reads (allMids,
	// metaAndAssetCtxs, spotMetaAndAssetCtxs): PERFORMANCE #7.
	memo memoCache

	childMu  sync.Mutex
	children map[string]childMemo

	regOnce sync.Once
	reg     *registry

	stats statsCache

	mktMu  sync.Mutex
	mktAt  time.Time
	mkts   []domain.Market
	mktMet domain.Meta

	roleMu sync.Mutex
	roles  map[string]string
}

// New builds a Client.
func New(opts Options) *Client {
	if opts.Network == "" {
		opts.Network = domain.Mainnet
	}
	ep := EndpointsFor(opts.Network)
	if opts.Endpoints != nil {
		ep = *opts.Endpoints
	}
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: 15 * time.Second}
	}
	if opts.Limiter == nil {
		opts.Limiter = NewLimiter(1200, time.Minute)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.UserAgent == "" {
		opts.UserAgent = DefaultUserAgent
	}
	return &Client{opts: opts, ep: ep, retryBase: time.Second, maxRetries: 4, penalty: DefaultPenalty,
		roles: map[string]string{}, children: map[string]childMemo{}}
}

// Network returns the configured network.
func (c *Client) Network() domain.Network { return c.opts.Network }

// Endpoints returns the resolved endpoints (used by ws and exchange).
func (c *Client) Endpoints() Endpoints { return c.ep }

// Limiter returns the weight limiter shared by this client.
func (c *Client) Limiter() *Limiter { return c.opts.Limiter }

func (c *Client) now() time.Time { return c.opts.Now() }

// reqProbe extracts the fields of an info request that drive weight and
// error mapping.
type reqProbe struct {
	Type string `json:"type"`
	Coin string `json:"coin"`
	Req  *struct {
		Coin string `json:"coin"`
	} `json:"req"`
}

func (p reqProbe) coin() string {
	if p.Coin != "" {
		return p.Coin
	}
	if p.Req != nil {
		return p.Req.Coin
	}
	return ""
}

// itemSurcharge returns the post-response weight for n returned items
// (large responses cost extra weight under Hyperliquid's rate limits).
func itemSurcharge(infoType string, n int) int {
	switch infoType {
	case "candleSnapshot":
		return n / 60
	case "recentTrades", "historicalOrders", "userFills", "userFillsByTime", "fundingHistory",
		"userFunding", "nonUserFundingUpdates", "twapHistory", "userTwapSliceFills",
		"userTwapSliceFillsByTime", "delegatorHistory", "delegatorRewards", "validatorStats":
		return n / 20
	}
	return 0
}

// Info POSTs req (a struct or map with a "type" field) to /info and decodes
// the response into out. It charges weight(req) against the limiter first,
// maps 422 -> *APIError{Code: "BAD_REQUEST"}, 500+"null" -> ErrUnknownCoin
// (for coin-bearing requests), retries 429/5xx up to 4 times with backoff.
// A 429 also penalizes the shared limiter (every caller waits about 5 s),
// and one that survives the retries is a *RateLimitError carrying RetryAfter.
// A 200 "null" for vaultDetails maps to ErrNotFound.
func (c *Client) Info(ctx context.Context, req any, out any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("hyperliquid: encode request: %w", err)
	}
	var probe reqProbe
	_ = json.Unmarshal(body, &probe)
	if probe.Type == "" {
		return &APIError{Code: "BAD_REQUEST", Body: "request has no \"type\""}
	}
	if err := c.opts.Limiter.Wait(ctx, Weight(probe.Type)); err != nil {
		return err
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, backoff(c.retryBase, attempt)); err != nil {
				return err
			}
			// Retries cost weight too.
			if err := c.opts.Limiter.Wait(ctx, Weight(probe.Type)); err != nil {
				return err
			}
		}
		status, raw, hdr, err := c.post(ctx, body)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = err
			continue // network errors are retried like 5xx
		}
		trimmed := bytes.TrimSpace(raw)
		switch {
		case status == http.StatusOK:
			if string(trimmed) == "null" {
				if probe.Type == "vaultDetails" {
					return ErrNotFound
				}
				return nil // e.g. subAccounts for a user without any
			}
			if out == nil {
				return nil
			}
			if err := json.Unmarshal(trimmed, out); err != nil {
				return &APIError{Status: status, Code: "DECODE", Body: truncate(err.Error() + ": " + string(trimmed))}
			}
			if n := lenOf(out); n > 0 {
				if w := itemSurcharge(probe.Type, n); w > 0 {
					c.opts.Limiter.Charge(w)
				}
			}
			return nil
		case status == http.StatusUnprocessableEntity || status == http.StatusBadRequest:
			return &APIError{Status: status, Code: "BAD_REQUEST", Body: truncate(string(trimmed))}
		case status == http.StatusTooManyRequests:
			d := max(c.penalty, retryAfterHeader(hdr))
			c.opts.Limiter.Penalize(d)
			lastErr = &RateLimitError{Type: probe.Type, RetryAfter: d}
			continue
		case status >= 500:
			if string(trimmed) == "null" && probe.coin() != "" {
				return fmt.Errorf("%w: %q", ErrUnknownCoin, probe.coin())
			}
			lastErr = &APIError{Status: status, Code: "SERVER", Body: truncate(string(trimmed)), RetryAfter: c.retryBase}
			continue
		default:
			return &APIError{Status: status, Code: "SERVER", Body: truncate(string(trimmed))}
		}
	}
	return lastErr
}

func (c *Client) post(ctx context.Context, body []byte) (int, []byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ep.Info, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.opts.UserAgent)
	resp, err := c.opts.HTTP.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp.StatusCode, nil, resp.Header, err
	}
	return resp.StatusCode, raw, resp.Header, nil
}

// retryAfterHeader parses a Retry-After header in seconds (0 when absent),
// capped at one minute.
func retryAfterHeader(h http.Header) time.Duration {
	if h == nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After")))
	if err != nil || n <= 0 {
		return 0
	}
	return min(time.Duration(n)*time.Second, time.Minute)
}

// backoff returns base*2^(attempt-1) with +-50% jitter.
func backoff(base time.Duration, attempt int) time.Duration {
	d := base << (attempt - 1)
	if d <= 0 {
		return 0
	}
	j := time.Duration(rand.Int64N(int64(d))) - d/2
	return d + j
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func truncate(s string) string {
	if len(s) > 512 {
		return s[:512] + "…"
	}
	return s
}

// lenOf returns the length of *[]T or *map, else 0.
func lenOf(out any) int {
	v := reflect.ValueOf(out)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return 0
	}
	v = v.Elem()
	switch v.Kind() {
	case reflect.Slice, reflect.Map:
		return v.Len()
	}
	return 0
}

// ErrInvalidAddress is returned for strings that are not 0x + 40 hex chars.
var ErrInvalidAddress = errors.New("invalid address")

// NormalizeAddress validates a 0x-prefixed 20-byte hex address and returns
// it lowercased.
func NormalizeAddress(addr string) (string, error) {
	a := strings.ToLower(strings.TrimSpace(addr))
	if len(a) != 42 || !strings.HasPrefix(a, "0x") {
		return "", fmt.Errorf("%w: %q", ErrInvalidAddress, addr)
	}
	for _, r := range a[2:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return "", fmt.Errorf("%w: %q", ErrInvalidAddress, addr)
		}
	}
	return a, nil
}

// nowMs returns the client clock in ms.
func (c *Client) nowMs() int64 { return c.now().UnixMilli() }

// parseHistory converts [[ms, "value"], ...] pairs into a Series.
func parseHistory(h [][2]any) domain.Series {
	out := make(domain.Series, 0, len(h))
	for _, p := range h {
		t, ok1 := toInt64(p[0])
		v, ok2 := toFloat(p[1])
		if ok1 && ok2 {
			out = append(out, domain.Point{T: t, V: v})
		}
	}
	return out
}

func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case json.Number:
		n, err := x.Int64()
		return n, err == nil
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		return n, err == nil
	}
	return 0, false
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}
