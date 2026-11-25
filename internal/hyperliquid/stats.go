package hyperliquid

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// StatsRefreshEvery is the minimum interval between stats-data downloads.
const StatsRefreshEvery = 5 * time.Minute

// statsCache holds the vault list in memory and mirrors it on disk.
type statsCache struct {
	mu        sync.Mutex
	loaded    bool // disk cache consulted
	vaults    []StatsVault
	etag      string
	fetchedAt time.Time // last successful 200 or 304
}

// statsDisk is the compact gzip cache format.
type statsDisk struct {
	ETag      string       `json:"etag"`
	FetchedAt time.Time    `json:"fetchedAt"`
	Vaults    []StatsVault `json:"vaults"`
}

func (c *Client) statsPath() string {
	if c.opts.CacheDir == "" {
		return ""
	}
	return filepath.Join(c.opts.CacheDir, fmt.Sprintf("stats-vaults-%s.json.gz", c.opts.Network))
}

// StatsVaults downloads stats-data/<Net>/vaults with If-None-Match against
// the on-disk ETag cache (304 -> cached copy), stream-decodes the 14 MB array,
// and keeps only what we use. Refresh at most every 5 minutes. meta.Stale is
// set when the cache is served because the network failed.
//
// Every vault is kept (open and closed, flagged by Summary.IsClosed); the
// compact cache is a few hundred KB gzipped.
func (c *Client) StatsVaults(ctx context.Context) ([]StatsVault, domain.Meta, error) {
	s := &c.stats
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.loaded {
		s.loaded = true
		c.loadStatsDisk()
	}
	now := c.now()
	meta := domain.Meta{Source: "stats-data", Network: c.opts.Network}
	if s.vaults != nil && now.Sub(s.fetchedAt) < StatsRefreshEvery {
		meta.AsOf = s.fetchedAt
		return s.vaults, meta, nil
	}

	vaults, etag, notModified, err := c.fetchStats(ctx, s.etag)
	if err != nil {
		if s.vaults != nil && ctx.Err() == nil {
			meta.AsOf = s.fetchedAt
			meta.Stale = true
			meta.Warnings = append(meta.Warnings, "stats-data unreachable, serving cached vault list: "+err.Error())
			return s.vaults, meta, nil
		}
		return nil, meta, err
	}
	s.fetchedAt = now
	if notModified {
		// Re-encoding ~14 MB of vaults just to bump fetchedAt is waste
		// (PERFORMANCE #12): touch the file and let loadStatsDisk read
		// the freshness from its mtime.
		if p := c.statsPath(); p != "" {
			_ = os.Chtimes(p, now, now)
		}
	} else {
		s.vaults = vaults
		s.etag = etag
		c.saveStatsDisk()
	}
	meta.AsOf = now
	return s.vaults, meta, nil
}

// fetchStats performs the conditional GET and stream-decodes the array.
func (c *Client) fetchStats(ctx context.Context, etag string) (vaults []StatsVault, newETag string, notModified bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ep.Stats+"/vaults", nil)
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("User-Agent", c.opts.UserAgent)
	req.Header.Set("Accept", "application/json")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	// stats-data is a 14 MB file; give it more time than the info timeout.
	hc := *c.opts.HTTP
	if hc.Timeout > 0 && hc.Timeout < 90*time.Second {
		hc.Timeout = 90 * time.Second
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return nil, etag, true, nil
	case resp.StatusCode != http.StatusOK:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, "", false, &APIError{Status: resp.StatusCode, Code: "SERVER", Body: string(b)}
	}
	vaults, err = DecodeStatsVaults(resp.Body)
	if err != nil {
		return nil, "", false, err
	}
	return vaults, resp.Header.Get("ETag"), false, nil
}

// DecodeStatsVaults stream-decodes a stats-data vaults array element by
// element, so the full 14 MB document is never held as one value.
func DecodeStatsVaults(r io.Reader) ([]StatsVault, error) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return nil, &APIError{Status: 200, Code: "DECODE", Body: "stats vaults: " + err.Error()}
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil, &APIError{Status: 200, Code: "DECODE", Body: "stats vaults: expected array"}
	}
	out := make([]StatsVault, 0, 1024)
	for dec.More() {
		var v StatsVault
		if err := dec.Decode(&v); err != nil {
			return nil, &APIError{Status: 200, Code: "DECODE", Body: "stats vault: " + err.Error()}
		}
		if v.Summary.VaultAddress == "" {
			continue
		}
		out = append(out, v)
	}
	if _, err := dec.Token(); err != nil {
		return nil, &APIError{Status: 200, Code: "DECODE", Body: "stats vaults: " + err.Error()}
	}
	return out, nil
}

// loadStatsDisk reads the compact cache; mu must be held.
func (c *Client) loadStatsDisk() {
	p := c.statsPath()
	if p == "" {
		return
	}
	f, err := os.Open(p)
	if err != nil {
		return
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return
	}
	defer gz.Close()
	var d statsDisk
	if err := json.NewDecoder(gz).Decode(&d); err != nil || d.Vaults == nil {
		return
	}
	// A 304 refresh only touches the file, so its mtime may be newer.
	if fi, err := f.Stat(); err == nil && fi.ModTime().After(d.FetchedAt) && !fi.ModTime().After(c.now()) {
		d.FetchedAt = fi.ModTime()
	}
	c.stats.vaults, c.stats.etag, c.stats.fetchedAt = d.Vaults, d.ETag, d.FetchedAt
}

// saveStatsDisk writes the compact cache atomically; mu must be held.
func (c *Client) saveStatsDisk() {
	p := c.statsPath()
	if p == "" || c.stats.vaults == nil {
		return
	}
	_ = writeGzipJSON(p, statsDisk{ETag: c.stats.etag, FetchedAt: c.stats.fetchedAt, Vaults: c.stats.vaults})
}

// writeGzipJSON writes v as gzip JSON via a temp file + rename (owner-only
// permissions, SECURITY S14).
func writeGzipJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(tmp)
	err = json.NewEncoder(gz).Encode(v)
	if cerr := gz.Close(); err == nil {
		err = cerr
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return replaceFile(tmp.Name(), path)
}

// statsLookup returns the cached stats row for an address without any
// network access (used to enrich Vault with createTime).
func (c *Client) statsLookup(addr string) (StatsVault, bool) {
	s := &c.stats
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.vaults {
		if v.Summary.VaultAddress == addr {
			return v, true
		}
	}
	return StatsVault{}, false
}
