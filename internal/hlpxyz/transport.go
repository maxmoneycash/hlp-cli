package hlpxyz

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Cache lifetimes per endpoint class.
const (
	ttlList   = 60 * time.Second // summary, v6status, v6spark, live, pvault status
	ttlDetail = 5 * time.Minute  // vault detail, NAV, composition, pvault history
	ttlStatic = time.Hour        // quant, indexes, benchmarks

	// staleLimit is how old a cached body may be and still be served when
	// the network fails (marked stale).
	staleLimit = 24 * time.Hour

	maxBody      = 64 << 20 // detail for HLP is ~2 MB; vaults-bulk (never used) is 25 MB
	maxRetries   = 2        // extra attempts on 5xx / network errors
	maxRateWait  = 60 * time.Second
	cacheSubdir  = "hlpxyz"
	pruneEntries = 256
)

// ErrRateLimited is returned when hlp.xyz keeps answering 429 or the reset
// time is beyond the context deadline.
var ErrRateLimited = errors.New("hlp.xyz rate limit reached")

// ErrForbiddenEndpoint guards the hard rule: this client never calls
// authenticated or mutating hlp.xyz endpoints.
var ErrForbiddenEndpoint = errors.New("hlpxyz: refusing to call an authenticated or mutating endpoint")

// APIError is a non-2xx response or a {"error": "..."} body.
type APIError struct {
	Status  int    // HTTP status (200 when the error came in a 200 body)
	Message string // server message, e.g. "Vault not found"
	Path    string // request path (no host)
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("hlp.xyz %s: HTTP %d", e.Path, e.Status)
	}
	return fmt.Sprintf("hlp.xyz %s: %s (HTTP %d)", e.Path, e.Message, e.Status)
}

type noCacheKey struct{}

// WithoutCache returns a context whose requests skip the cache read (the
// response still refreshes the cache). Use it for watch/poll loops that
// must see fresh /live numbers more often than the 60 s list TTL.
func WithoutCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, noCacheKey{}, true)
}

// fetchInfo describes where a body came from.
type fetchInfo struct {
	At     time.Time // when the body was fetched from the network
	Cached bool      // served from cache (fresh)
	Stale  bool      // served from an expired cache entry after a network failure
}

// request is one GET.
type request struct {
	path  string     // e.g. /api/vaults
	query url.Values // nil ok
	ttl   time.Duration
	// cacheable decides whether a 200 body may be cached (e.g. not
	// {"status":"loading"}). nil means always.
	cacheable func([]byte) bool
}

func (r request) url(base string) string {
	u := strings.TrimRight(base, "/") + r.path
	if len(r.query) > 0 {
		u += "?" + r.query.Encode() // Encode sorts keys: stable cache keys
	}
	return u
}

// forbidden reports whether a request targets an endpoint this client must
// never call (auth, bookkeeping writes, index investing, admin).
func forbidden(r request) bool {
	p := strings.ToLower(r.path)
	if strings.HasPrefix(p, "/api/auth") || strings.HasPrefix(p, "/api/casa") || strings.HasPrefix(p, "/api/contact") {
		return true
	}
	a := strings.ToLower(r.query.Get("action"))
	switch {
	case a == "record" || a == "recordclosure" || a == "idxmine" || a == "notify":
		return true
	case strings.HasPrefix(a, "idxinv"), strings.HasPrefix(a, "manage"), strings.HasPrefix(a, "real"):
		return true
	case strings.HasPrefix(p, "/api/user") && a != "v2vault" && a != "v3vault" && a != "v4vault" && a != "v5vault" && a != "v6vault" && a != "leaderboard":
		return true
	}
	return false
}

// limiter is a token bucket: RatePerMinute tokens per minute with a small
// burst, so that burst + one minute of refill stays under the server's 120
// requests per window.
type limiter struct {
	mu       sync.Mutex
	rate     float64 // tokens per second
	capacity float64
	tokens   float64
	last     time.Time
	blocked  time.Time // no requests before this (429 reset)
}

func newLimiter(perMinute int) *limiter {
	if perMinute <= 0 {
		perMinute = 90
	}
	capacity := float64(perMinute) / 6
	if capacity < 1 {
		capacity = 1
	}
	return &limiter{rate: float64(perMinute) / 60, capacity: capacity, tokens: capacity, last: time.Now()}
}

// wait blocks until a token is available or ctx ends.
func (l *limiter) wait(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := time.Now()
		l.tokens += now.Sub(l.last).Seconds() * l.rate
		if l.tokens > l.capacity {
			l.tokens = l.capacity
		}
		l.last = now
		var d time.Duration
		switch {
		case now.Before(l.blocked):
			d = l.blocked.Sub(now)
		case l.tokens >= 1:
			l.tokens--
			l.mu.Unlock()
			return nil
		default:
			d = time.Duration((1 - l.tokens) / l.rate * float64(time.Second))
		}
		l.mu.Unlock()
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) < d {
			return fmt.Errorf("%w: next slot in %s", ErrRateLimited, d.Round(time.Millisecond))
		}
		if err := sleep(ctx, d); err != nil {
			return err
		}
	}
}

// block stops all requests until t (a 429's reset time).
func (l *limiter) block(t time.Time) {
	l.mu.Lock()
	if t.After(l.blocked) {
		l.blocked = t
	}
	l.tokens = 0
	l.mu.Unlock()
}

func sleep(ctx context.Context, d time.Duration) error {
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

// resetDelay parses x-ratelimit-reset (seconds until reset, or a Unix
// timestamp in seconds) and Retry-After.
func resetDelay(h http.Header, now time.Time) time.Duration {
	for _, k := range []string{"X-Ratelimit-Reset", "Retry-After"} {
		v := strings.TrimSpace(h.Get(k))
		if v == "" {
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			continue
		}
		if f > 1e9 { // absolute Unix seconds
			return time.Unix(int64(f), 0).Sub(now)
		}
		return time.Duration(f * float64(time.Second))
	}
	return 0
}

// memEntry is an in-memory cache entry; disk entries use the same shape.
type cacheEntry struct {
	FetchedAt int64           `json:"fetchedAt"` // ms
	Body      json.RawMessage `json:"body"`
}

type cache struct {
	mu     sync.Mutex
	mem    map[string]cacheEntry
	dir    string // "" = memory only
	pruned bool
}

func cacheKey(u string) string {
	h := sha256.Sum256([]byte(u))
	return hex.EncodeToString(h[:16])
}

func (c *cache) get(key string) (cacheEntry, bool) {
	c.mu.Lock()
	e, ok := c.mem[key]
	c.mu.Unlock()
	if ok || c.dir == "" {
		return e, ok
	}
	b, err := os.ReadFile(filepath.Join(c.dir, key+".json"))
	if err != nil {
		return cacheEntry{}, false
	}
	if json.Unmarshal(b, &e) != nil || len(e.Body) == 0 {
		return cacheEntry{}, false
	}
	c.mu.Lock()
	c.mem[key] = e
	c.mu.Unlock()
	return e, true
}

func (c *cache) put(key string, e cacheEntry, now time.Time) {
	c.mu.Lock()
	c.mem[key] = e
	doPrune := !c.pruned
	c.pruned = true
	c.mu.Unlock()
	if c.dir == "" {
		return
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return
	}
	if doPrune {
		c.prune(now)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(c.dir, key+".*.tmp")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return
	}
	if os.Rename(tmp.Name(), filepath.Join(c.dir, key+".json")) != nil {
		os.Remove(tmp.Name())
	}
}

// prune deletes entries older than staleLimit, and the oldest entries when
// there are more than pruneEntries (disk is tight; details can be MBs).
func (c *cache) prune(now time.Time) {
	ents, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	type f struct {
		name string
		mod  time.Time
	}
	var files []f
	for _, e := range ents {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		if now.Sub(info.ModTime()) > staleLimit || strings.HasSuffix(e.Name(), ".tmp") {
			os.Remove(filepath.Join(c.dir, e.Name()))
			continue
		}
		files = append(files, f{e.Name(), info.ModTime()})
	}
	if len(files) <= pruneEntries {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, x := range files[:len(files)-pruneEntries] {
		os.Remove(filepath.Join(c.dir, x.name))
	}
}

// fetch performs a cached, rate-limited GET and returns the raw JSON body.
func (c *Client) fetch(ctx context.Context, r request) ([]byte, fetchInfo, error) {
	if forbidden(r) {
		return nil, fetchInfo{}, fmt.Errorf("%w: %s", ErrForbiddenEndpoint, r.path)
	}
	u := r.url(c.opts.BaseURL)
	key := cacheKey(u)
	now := c.opts.Now()
	skipRead, _ := ctx.Value(noCacheKey{}).(bool)
	cached, haveCached := c.cache.get(key)
	if haveCached && !skipRead && r.ttl > 0 {
		at := time.UnixMilli(cached.FetchedAt)
		if age := now.Sub(at); age >= 0 && age < r.ttl {
			return cached.Body, fetchInfo{At: at, Cached: true}, nil
		}
	}
	body, err := c.do(ctx, r, u)
	if err != nil {
		// Serve a stale copy on transport failures (not on 4xx answers, and
		// not when the caller gave up).
		var ae *APIError
		isClient := errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 && ae.Status != http.StatusTooManyRequests
		if haveCached && !isClient && ctx.Err() == nil {
			at := time.UnixMilli(cached.FetchedAt)
			if now.Sub(at) < staleLimit {
				return cached.Body, fetchInfo{At: at, Cached: true, Stale: true}, nil
			}
		}
		return nil, fetchInfo{}, err
	}
	if err := bodyError(body, r.path); err != nil {
		return nil, fetchInfo{}, err
	}
	if r.ttl > 0 && (r.cacheable == nil || r.cacheable(body)) {
		c.cache.put(key, cacheEntry{FetchedAt: now.UnixMilli(), Body: body}, now)
	}
	return body, fetchInfo{At: now}, nil
}

// bodyError detects the {"error": "..."} shape hlp.xyz uses for failures
// (sometimes with HTTP 200).
func bodyError(b []byte, path string) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '{' || !bytes.Contains(b[:min(len(b), 512)], []byte(`"error"`)) {
		return nil
	}
	var e struct {
		Error any `json:"error"`
	}
	if json.Unmarshal(b, &e) != nil || e.Error == nil {
		return nil
	}
	msg, ok := e.Error.(string)
	if !ok || msg == "" {
		return nil
	}
	return &APIError{Status: http.StatusOK, Message: msg, Path: path}
}

// do runs the HTTP request with the limiter, 429 handling and retries.
func (c *Client) do(ctx context.Context, r request, u string) ([]byte, error) {
	var lastErr error
	rateTries := 0
	for attempt := 0; attempt <= maxRetries; {
		if err := c.limit.wait(ctx); err != nil {
			return nil, err
		}
		body, status, hdr, err := c.once(ctx, u)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("hlp.xyz %s: %w", r.path, err)
		case status == http.StatusTooManyRequests:
			rateTries++
			d := resetDelay(hdr, time.Now())
			if d <= 0 {
				d = time.Second
			}
			if d > maxRateWait || rateTries > 3 {
				return nil, fmt.Errorf("%w (retry in %s)", ErrRateLimited, d.Round(time.Second))
			}
			if dl, ok := ctx.Deadline(); ok && time.Until(dl) < d {
				return nil, fmt.Errorf("%w (retry in %s)", ErrRateLimited, d.Round(time.Second))
			}
			c.limit.block(time.Now().Add(d))
			continue // does not consume a retry
		case status >= 200 && status < 300:
			return body, nil
		case status >= 500:
			lastErr = apiError(status, body, r.path)
		default:
			return nil, apiError(status, body, r.path)
		}
		attempt++
		if attempt > maxRetries {
			break
		}
		backoff := c.retryBase << attempt
		if backoff > 1 {
			backoff += time.Duration(rand.Int64N(int64(backoff / 2)))
		}
		if err := sleep(ctx, backoff); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func apiError(status int, body []byte, path string) error {
	if err := bodyError(body, path); err != nil {
		err.(*APIError).Status = status
		return err
	}
	return &APIError{Status: status, Path: path}
}

// once performs a single GET and returns the decoded body.
func (c *Client) once(ctx context.Context, u string) ([]byte, int, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, nil, err
	}
	req.Header.Set("User-Agent", c.opts.UserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := c.opts.HTTP.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	var rd io.Reader = resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, resp.StatusCode, resp.Header, fmt.Errorf("gzip: %w", err)
		}
		defer gz.Close()
		rd = gz
	}
	body, err := io.ReadAll(io.LimitReader(rd, maxBody+1))
	if err != nil {
		return nil, resp.StatusCode, resp.Header, err
	}
	if len(body) > maxBody {
		return nil, resp.StatusCode, resp.Header, fmt.Errorf("response larger than %d MB", maxBody>>20)
	}
	return body, resp.StatusCode, resp.Header, nil
}

// getJSON fetches and decodes into out.
func (c *Client) getJSON(ctx context.Context, r request, out any) (fetchInfo, error) {
	body, info, err := c.fetch(ctx, r)
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return info, fmt.Errorf("hlp.xyz %s: decode: %w", r.path, err)
	}
	return info, nil
}
