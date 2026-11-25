package hyperliquid

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TTLs of the per-Client memo for idempotent reads (PERFORMANCE #7). They
// are shorter than any UI refresh, so they only merge bursts: `hlp hlp`
// valuing nine accounts makes one allMids call instead of nine, and the
// TUI and MCP share one metaAndAssetCtxs per dex.
const (
	AllMidsTTL  = 2 * time.Second
	AssetCtxTTL = 3 * time.Second
)

// memoCache is a tiny singleflight plus TTL cache keyed by request.
type memoCache struct {
	mu sync.Mutex
	m  map[string]*memoEntry
}

type memoEntry struct {
	done chan struct{} // closed when val/err are set
	at   time.Time
	val  any
	err  error
}

// do returns the cached value for key when younger than ttl, joins an
// in-flight call for key, or runs f. Errors are never cached. A caller
// whose ctx ends while waiting on another caller's flight gives up alone.
func (c *Client) memoDo(ctx context.Context, key string, ttl time.Duration, f func() (any, error)) (any, error) {
	mc := &c.memo
	mc.mu.Lock()
	if mc.m == nil {
		mc.m = map[string]*memoEntry{}
	}
	if e, ok := mc.m[key]; ok {
		select {
		case <-e.done:
			if e.err == nil && c.now().Sub(e.at) < ttl {
				mc.mu.Unlock()
				return e.val, nil
			}
		default:
			mc.mu.Unlock()
			select {
			case <-e.done:
				if e.err != nil {
					return nil, e.err
				}
				return e.val, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	e := &memoEntry{done: make(chan struct{})}
	mc.m[key] = e
	mc.mu.Unlock()

	e.val, e.err = f()
	e.at = c.now()
	close(e.done)
	if e.err != nil {
		mc.mu.Lock()
		if mc.m[key] == e {
			delete(mc.m, key)
		}
		mc.mu.Unlock()
	}
	return e.val, e.err
}

// ---- vault children (hlpMembers), memoized for an hour ----

// ChildrenTTL is how long a vault's child list is reused. HLP's children
// change a few times a year; a stale list only costs a missing child.
const ChildrenTTL = time.Hour

type childMemo struct {
	At       time.Time `json:"at"`
	Children []string  `json:"children"`
}

// VaultChildren returns a parent vault's child addresses from vaultDetails
// (weight 20, no Account fan-out), memoized for ChildrenTTL in memory and
// under CacheDir so short-lived CLI processes share it. A vault without
// children returns an empty list.
func (c *Client) VaultChildren(ctx context.Context, vault string) ([]string, error) {
	addr, err := NormalizeAddress(vault)
	if err != nil {
		return nil, err
	}
	now := c.now()
	c.childMu.Lock()
	if m, ok := c.children[addr]; ok && now.Sub(m.At) < ChildrenTTL {
		c.childMu.Unlock()
		return append([]string(nil), m.Children...), nil
	}
	c.childMu.Unlock()
	path := c.childrenPath(addr)
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			var m childMemo
			if json.Unmarshal(b, &m) == nil && now.Sub(m.At) < ChildrenTTL && now.Sub(m.At) >= 0 {
				c.childMu.Lock()
				c.children[addr] = m
				c.childMu.Unlock()
				return append([]string(nil), m.Children...), nil
			}
		}
	}
	vd, err := c.VaultDetails(ctx, addr, "")
	if err != nil {
		return nil, err
	}
	m := childMemo{At: now, Children: []string{}}
	if vd.Relationship.Data != nil {
		for _, a := range vd.Relationship.Data.ChildAddresses {
			m.Children = append(m.Children, strings.ToLower(a))
		}
	}
	c.childMu.Lock()
	c.children[addr] = m
	c.childMu.Unlock()
	if path != "" {
		if b, err := json.Marshal(m); err == nil {
			_ = writeFileAtomic(path, b)
		}
	}
	return append([]string(nil), m.Children...), nil
}

func (c *Client) childrenPath(addr string) string {
	if c.opts.CacheDir == "" {
		return ""
	}
	return filepath.Join(c.opts.CacheDir, "vault-children", addr+".json")
}

// ---- cache files ----

// writeFileAtomic writes b to path through a temp file and a rename, with
// owner-only permissions (SECURITY S14: the cache shows which markets and
// accounts a user watches).
func writeFileAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return replaceFile(tmp.Name(), path)
}

// replaceFile renames tmp onto path. On Windows a rename onto a file that
// another process holds open fails, so it removes the target and retries
// once (RELEASE R7); a reader then misses the cache for one call at worst.
func replaceFile(tmp, path string) error {
	err := os.Rename(tmp, path)
	if err == nil {
		return nil
	}
	_ = os.Remove(path)
	if err = os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
	}
	return err
}
