package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/config"
)

// Price is a model's list price in USD per million tokens. A zero Price
// with Known false means the price is not published (variable or hidden).
type Price struct {
	InPerM  float64 `json:"inPerM"`  // USD per 1M input (prompt) tokens
	OutPerM float64 `json:"outPerM"` // USD per 1M output (completion) tokens
	Known   bool    `json:"known"`
}

// Cost returns the USD cost of the given token counts at p.
func (p Price) Cost(in, out int) float64 {
	return (float64(in)*p.InPerM + float64(out)*p.OutPerM) / 1e6
}

// ModelInfo is one entry of OpenRouter's public model catalog.
type ModelInfo struct {
	ID            string `json:"id"` // "anthropic/claude-sonnet-5"
	Name          string `json:"name"`
	ContextLength int    `json:"contextLength,omitempty"`
	Price         Price  `json:"price"`
	Created       int64  `json:"created,omitempty"` // unix seconds
}

// Ref is the arena / --entrants spelling of the model:
// "llm:openrouter/<id>".
func (m ModelInfo) Ref() string { return "llm:openrouter/" + m.ID }

// CatalogTTL is how long a fetched catalog is reused (memory and disk).
const CatalogTTL = time.Hour

// maxCatalogBody caps the /models response (the live catalog is ~1 MB).
const maxCatalogBody = 16 << 20

// CatalogOptions configure a Catalog.
type CatalogOptions struct {
	BaseURL    string // default OpenRouterBaseURL
	HTTPClient *http.Client
	// CacheFile, when set, persists the catalog across processes for
	// CatalogTTL (e.g. ~/.cache/hlp/openrouter-models.json).
	CacheFile string
	UserAgent string
	Now       func() time.Time
}

// Catalog reads OpenRouter's public model list (GET /api/v1/models). It
// needs no API key and sends none: the request carries no credentials or
// user data. Results are cached in memory and, with CacheFile, on disk.
type Catalog struct {
	o  CatalogOptions
	mu sync.Mutex

	models  []ModelInfo
	fetched time.Time
}

// NewCatalog returns a catalog client. It does not touch the network.
func NewCatalog(o CatalogOptions) *Catalog {
	o.BaseURL = strings.TrimRight(or(o.BaseURL, OpenRouterBaseURL), "/")
	o.HTTPClient = safeClient(o.HTTPClient)
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Catalog{o: o}
}

type catalogDisk struct {
	Fetched int64       `json:"fetched"`
	Models  []ModelInfo `json:"models"`
}

// Models returns the catalog sorted by id, and when it was fetched. A
// fresh cache (memory, then disk) is reused; otherwise the list is
// fetched. When the fetch fails a stale cache is returned with the error,
// so callers can show it with a warning.
func (c *Catalog) Models(ctx context.Context) ([]ModelInfo, time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.o.Now()
	if c.models != nil && now.Sub(c.fetched) < CatalogTTL {
		return clone(c.models), c.fetched, nil
	}
	if c.models == nil {
		if d, ok := c.loadDisk(); ok {
			c.models, c.fetched = d.Models, time.UnixMilli(d.Fetched)
			if now.Sub(c.fetched) < CatalogTTL {
				return clone(c.models), c.fetched, nil
			}
		}
	}
	ms, err := c.fetch(ctx)
	if err != nil {
		if c.models != nil {
			return clone(c.models), c.fetched, err
		}
		return nil, time.Time{}, err
	}
	c.models, c.fetched = ms, now
	c.saveDisk()
	return clone(ms), now, nil
}

// Lookup returns the catalog entry for id ("anthropic/claude-sonnet-5").
func (c *Catalog) Lookup(ctx context.Context, id string) (ModelInfo, bool) {
	ms, _, err := c.Models(ctx)
	if err != nil && len(ms) == 0 {
		return ModelInfo{}, false
	}
	id = strings.ToLower(strings.TrimSpace(id))
	i := sort.Search(len(ms), func(i int) bool { return strings.ToLower(ms[i].ID) >= id })
	if i < len(ms) && strings.ToLower(ms[i].ID) == id {
		return ms[i], true
	}
	return ModelInfo{}, false
}

// PriceFor estimates the list price of a provider name as Provider.Name()
// prints it ("openrouter:anthropic/claude-sonnet-5", "anthropic:claude-sonnet-5",
// "openai:gpt-5"). Direct Anthropic and OpenAI models are priced from
// their OpenRouter listing ("anthropic/<model>"), which tracks the vendor's
// list price. Local models are free.
func (c *Catalog) PriceFor(ctx context.Context, name string) (Price, bool) {
	label, model, _ := strings.Cut(name, ":")
	var id string
	switch label {
	case ProviderOllama, ProviderLMStudio:
		return Price{Known: true}, true
	case ProviderOpenRouter:
		id = model
	case ProviderAnthropic:
		id = "anthropic/" + model
	case "openai":
		id = "openai/" + model
	default:
		return Price{}, false
	}
	if c == nil || id == "" {
		return Price{}, false
	}
	m, ok := c.Lookup(ctx, id)
	if !ok || !m.Price.Known {
		return Price{}, false
	}
	return m.Price, true
}

func (c *Catalog) fetch(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.o.BaseURL+"/models", nil)
	if err != nil {
		return nil, &Error{Provider: ProviderOpenRouter, Message: "bad catalog URL"}
	}
	req.Header.Set("Accept", "application/json")
	if c.o.UserAgent != "" {
		req.Header.Set("User-Agent", c.o.UserAgent)
	}
	resp, err := c.o.HTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Provider: ProviderOpenRouter, Message: transportMessage(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, readHTTPError(resp, ProviderOpenRouter, config.Secret{})
	}
	return decodeCatalog(io.LimitReader(resp.Body, maxCatalogBody))
}

// decodeCatalog parses {"data":[{id,name,context_length,created,pricing:{prompt,completion}}]}.
// Prices are USD per token as decimal strings; listPrice decides whether
// they are usable.
func decodeCatalog(r io.Reader) ([]ModelInfo, error) {
	var v struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength *int   `json:"context_length"`
			Created       int64  `json:"created"`
			Pricing       struct {
				Prompt     json.RawMessage `json:"prompt"`
				Completion json.RawMessage `json:"completion"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r).Decode(&v); err != nil {
		return nil, &Error{Provider: ProviderOpenRouter, Message: "decode /models: " + err.Error()}
	}
	out := make([]ModelInfo, 0, len(v.Data))
	for _, d := range v.Data {
		id := strings.TrimSpace(d.ID)
		if id == "" || len(id) > 200 || strings.ContainsAny(id, " \t\r\n\x1b") {
			continue
		}
		m := ModelInfo{ID: id, Name: cleanName(or(d.Name, id)), Created: d.Created}
		if d.ContextLength != nil {
			m.ContextLength = *d.ContextLength
		}
		m.Price = listPrice(d.Pricing.Prompt, d.Pricing.Completion)
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].ID) < strings.ToLower(out[j].ID) })
	return out, nil
}

// maxPerToken is the largest per-token rate taken at face value ($10,000
// per million tokens, orders of magnitude above any listed model).
const maxPerToken = 0.01

// listPrice builds a Price from a catalog entry's two per-token rates. A
// model counts as priced only when both rates read as ordinary amounts:
// a decimal (string or number) from 0 up to maxPerToken. Anything else,
// whether a missing field, a placeholder for routed or auctioned models,
// or an absurd value, leaves Known false, so the budget reports the model
// as unpriced instead of charging it an invented figure. Zero on both
// sides is a free model and is known.
func listPrice(prompt, completion json.RawMessage) Price {
	in, okIn := rate(prompt)
	out, okOut := rate(completion)
	if !okIn || !okOut {
		return Price{}
	}
	return Price{InPerM: in * 1e6, OutPerM: out * 1e6, Known: true}
}

// rate reads one per-token amount.
func rate(raw json.RawMessage) (float64, bool) {
	s := strings.TrimSpace(string(raw))
	if uq, err := strconv.Unquote(s); err == nil {
		s = strings.TrimSpace(uq)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || !(f >= 0 && f <= maxPerToken) {
		return 0, false
	}
	return f, true
}

// cleanName strips control characters from a third-party display name.
func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
	return truncate(strings.TrimSpace(s), 120)
}

func (c *Catalog) loadDisk() (catalogDisk, bool) {
	if c.o.CacheFile == "" {
		return catalogDisk{}, false
	}
	b, err := os.ReadFile(c.o.CacheFile)
	if err != nil || len(b) > maxCatalogBody {
		return catalogDisk{}, false
	}
	var d catalogDisk
	if json.Unmarshal(b, &d) != nil || len(d.Models) == 0 {
		return catalogDisk{}, false
	}
	return d, true
}

func (c *Catalog) saveDisk() {
	if c.o.CacheFile == "" {
		return
	}
	b, err := json.Marshal(catalogDisk{Fetched: c.fetched.UnixMilli(), Models: c.models})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.o.CacheFile), 0o700); err != nil {
		return
	}
	tmp := fmt.Sprintf("%s.%d.tmp", c.o.CacheFile, os.Getpid())
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, c.o.CacheFile)
	}
}

func clone(ms []ModelInfo) []ModelInfo { return append([]ModelInfo(nil), ms...) }
