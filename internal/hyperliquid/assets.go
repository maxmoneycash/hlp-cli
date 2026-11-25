package hyperliquid

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sahilm/fuzzy"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// Registry maps user input to markets and asset ids:
//   - native perp "BTC" -> asset id = universe index (HYPE = 159)
//   - HIP-3 "xyz:SP500" -> 100000 + perpDexIndex*10000 + index (= 110052)
//   - spot "@107" / "HYPE/USDC" -> 10000 + spot index
//   - outcome "#N" -> 100000000 + N
//
// Resolve is case-insensitive on input and returns the canonical
// case-sensitive wire coin. Aliases: "SPY"/"S&P" -> xyz:SP500 via
// perpAnnotation keywords; "btc" -> "BTC"; "HYPE/USDC" -> "@107".
// Delisted assets resolve but are flagged. Unknown input returns
// ErrUnknownCoin with up to 5 fuzzy suggestions in the error message.
type Registry interface {
	Resolve(input string) (domain.Market, error)
	Markets() []domain.Market // all live markets, all kinds
	Refresh(ctx context.Context) error
}

// RegistryTTL is how long registry metadata (memory and disk) stays fresh.
const RegistryTTL = time.Hour

// Asset id offsets: spot, HIP-3 dex and outcome ids are offset from perps.
const (
	SpotAssetOffset    = 10000
	HIP3AssetOffset    = 100000
	HIP3DexStride      = 10000
	OutcomeAssetOffset = 100000000
)

// RegistryData is the raw metadata a Registry is built from. It is also the
// on-disk cache format.
type RegistryData struct {
	FetchedAt  time.Time      `json:"fetchedAt"`
	Dexs       []*PerpDex     `json:"dexs"`  // perpDexs order; [0] = nil (native)
	Metas      []Meta         `json:"metas"` // aligned with Dexs
	Spot       SpotMeta       `json:"spot"`
	Categories []PerpCategory `json:"categories,omitempty"`
	Outcomes   OutcomeMeta    `json:"outcomes"`
}

// DexNames returns the dex names in perpDexs order ("" = native).
func (d RegistryData) DexNames() []string {
	if len(d.Dexs) == 0 {
		return []string{""}
	}
	out := make([]string, len(d.Dexs))
	for i, dx := range d.Dexs {
		if dx != nil && i > 0 {
			out[i] = dx.Name
		}
	}
	return out
}

// NewRegistry builds a Registry backed by c (meta, allPerpMetas, perpDexs,
// spotMeta, perpCategories), cached on disk under Options.CacheDir for 1h.
// Prefer Client.Registry(), which returns one shared instance per Client.
func NewRegistry(c *Client) Registry { return &registry{c: c} }

// NewRegistryFrom builds a static Registry from already-fetched metadata
// (tests, offline mode). Refresh is a no-op.
func NewRegistryFrom(d RegistryData) Registry {
	r := &registry{}
	r.install(d)
	return r
}

// CoinValidator adapts a Registry into a ws.Validator-compatible func: it
// accepts only exact wire coins that the registry knows (an unknown coin on
// the WebSocket kills the whole socket).
func CoinValidator(r Registry) func(coin string) error {
	return func(coin string) error {
		m, err := r.Resolve(coin)
		if err != nil {
			return err
		}
		if m.Coin != coin {
			return fmt.Errorf("%w: %q is not a wire coin (use %q)", ErrUnknownCoin, coin, m.Coin)
		}
		return nil
	}
}

type registry struct {
	c *Client // nil for static registries

	refreshMu sync.Mutex // serializes Refresh

	mu    sync.RWMutex
	data  RegistryData
	all   []domain.Market
	exact map[string]int
	keys  map[string]int // lowercase coin, display, bare HIP-3 names
	names []string       // suggestion corpus (live coins)
	// spotUSDC maps a base token index to its spot pair coin quoted in USDC.
	spotUSDC map[int]string
	// bare maps a lowercase bare HIP-3 name ("nvda") to every live HIP-3
	// coin that lists it, in perpDexs order.
	bare map[string][]string

	tryMu   sync.Mutex
	lastTry time.Time // last lazy load attempt (throttles retries when offline)
}

// Registry returns the Client's shared registry (built once, refreshed lazily).
func (c *Client) Registry() Registry { return c.registry() }

func (c *Client) registry() *registry {
	c.regOnce.Do(func() { c.reg = &registry{c: c} })
	return c.reg
}

// aliases maps common names to wire coins; applied only if the target exists.
var aliases = map[string][]string{
	"spy":         {"xyz:SP500"},
	"s&p":         {"xyz:SP500"},
	"s&p500":      {"xyz:SP500"},
	"s&p 500":     {"xyz:SP500"},
	"sp500":       {"xyz:SP500"},
	"spx":         {"xyz:SP500"},
	"es":          {"xyz:SP500"},
	"bitcoin":     {"BTC"},
	"xbt":         {"BTC"},
	"ethereum":    {"ETH"},
	"ether":       {"ETH"},
	"solana":      {"SOL"},
	"hyperliquid": {"HYPE"},
	"gold":        {"xyz:GOLD", "flx:GOLD"},
	"silver":      {"xyz:SILVER", "flx:SILVER"},
	"nvidia":      {"xyz:NVDA"},
	"tesla":       {"xyz:TSLA"},
	"apple":       {"xyz:AAPL"},
}

// Refresh loads metadata: memory if fresh, else the disk cache if fresh,
// else the network. On network failure a stale disk/memory copy is kept
// and nil is returned; an error is returned only when nothing is loaded.
func (r *registry) Refresh(ctx context.Context) error {
	if r.c == nil {
		return nil
	}
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()

	now := r.c.now()
	r.mu.RLock()
	fresh := r.all != nil && now.Sub(r.data.FetchedAt) < RegistryTTL
	r.mu.RUnlock()
	if fresh {
		return nil
	}
	if d, ok := r.loadDisk(); ok && now.Sub(d.FetchedAt) < RegistryTTL {
		r.install(d)
		return nil
	}
	d, err := r.fetch(ctx)
	if err != nil {
		r.mu.RLock()
		have := r.all != nil
		r.mu.RUnlock()
		if have {
			return nil
		}
		if d, ok := r.loadDisk(); ok {
			r.install(d) // stale but better than nothing
			return nil
		}
		return fmt.Errorf("hyperliquid registry: %w", err)
	}
	r.install(d)
	r.saveDisk(d)
	return nil
}

func (r *registry) fetch(ctx context.Context) (RegistryData, error) {
	d := RegistryData{FetchedAt: r.c.now()}
	var (
		wg                       sync.WaitGroup
		errDex, errMeta, errSpot error
		cats                     []PerpCategory
		outcomes                 OutcomeMeta
		metas                    []Meta
		dexs                     []*PerpDex
		spot                     SpotMeta
	)
	wg.Add(5)
	go func() { defer wg.Done(); dexs, errDex = r.c.PerpDexs(ctx) }()
	go func() { defer wg.Done(); metas, errMeta = r.c.AllPerpMetas(ctx) }()
	go func() { defer wg.Done(); spot, errSpot = r.c.SpotMeta(ctx) }()
	go func() { defer wg.Done(); cats, _ = r.c.PerpCategories(ctx) }()  // best effort
	go func() { defer wg.Done(); outcomes, _ = r.c.OutcomeMeta(ctx) }() // best effort
	wg.Wait()
	if errDex != nil {
		return d, errDex
	}
	if errSpot != nil {
		return d, errSpot
	}
	if len(dexs) == 0 {
		dexs = []*PerpDex{nil}
	}
	if errMeta != nil || len(metas) < len(dexs) {
		// Fall back to per-dex meta for whatever allPerpMetas did not cover.
		full := make([]Meta, len(dexs))
		copy(full, metas)
		for i := len(metas); i < len(dexs); i++ {
			name := ""
			if dexs[i] != nil && i > 0 {
				name = dexs[i].Name
			}
			m, err := r.c.Meta(ctx, name)
			if err != nil {
				if i == 0 {
					return d, err
				}
				continue // a broken builder dex must not hide the others
			}
			full[i] = m
		}
		metas = full
	}
	d.Dexs, d.Metas, d.Spot, d.Categories, d.Outcomes = dexs, metas, spot, cats, outcomes
	return d, nil
}

func (r *registry) diskPath() string {
	if r.c == nil || r.c.opts.CacheDir == "" {
		return ""
	}
	return filepath.Join(r.c.opts.CacheDir, fmt.Sprintf("registry-%s.json", r.c.opts.Network))
}

func (r *registry) loadDisk() (RegistryData, bool) {
	p := r.diskPath()
	if p == "" {
		return RegistryData{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return RegistryData{}, false
	}
	var d RegistryData
	if err := json.Unmarshal(b, &d); err != nil || len(d.Metas) == 0 {
		return RegistryData{}, false
	}
	return d, true
}

func (r *registry) saveDisk(d RegistryData) {
	p := r.diskPath()
	if p == "" {
		return
	}
	b, err := json.Marshal(d)
	if err != nil {
		return
	}
	_ = writeFileAtomic(p, b)
}

// install rebuilds the indexes from d.
func (r *registry) install(d RegistryData) {
	all := BuildMarkets(d)
	exact := make(map[string]int, len(all))
	keys := make(map[string]int, len(all)*3)
	spotUSDC := map[int]string{}
	var names []string

	put := func(m map[string]int, k string, i int) {
		if k == "" {
			return
		}
		if j, ok := m[k]; ok {
			// Prefer live over delisted; otherwise first wins.
			if !(all[j].Delisted && !all[i].Delisted) {
				return
			}
		}
		m[k] = i
	}
	for i, m := range all {
		put(exact, m.Coin, i)
		put(keys, strings.ToLower(m.Coin), i)
		put(keys, strings.ToLower(m.Display), i)
		if !m.Delisted {
			names = append(names, m.Coin)
		}
	}
	// Lower-priority keys: bare HIP-3 names ("nvda" -> xyz:NVDA) after
	// every native/display key is placed.
	bareNames := map[string][]string{}
	for i, m := range all {
		if m.Kind == domain.HIP3 {
			if _, bare, ok := strings.Cut(m.Coin, ":"); ok {
				put(keys, strings.ToLower(bare), i)
				if !m.Delisted {
					bareNames[strings.ToLower(bare)] = append(bareNames[strings.ToLower(bare)], m.Coin)
				}
			}
		}
	}
	for _, p := range d.Spot.Universe {
		if p.Tokens[1] == 0 { // quoted in USDC
			if _, ok := spotUSDC[p.Tokens[0]]; !ok {
				spotUSDC[p.Tokens[0]] = p.Name
			}
		}
	}
	r.mu.Lock()
	r.data, r.all, r.exact, r.keys, r.names, r.spotUSDC, r.bare = d, all, exact, keys, names, spotUSDC, bareNames
	r.mu.Unlock()
}

// BuildMarkets turns registry metadata into markets (no live context).
// Order: native perps, HIP-3 perps (perpDexs order), spot, outcomes.
func BuildMarkets(d RegistryData) []domain.Market {
	cats := map[string]string{}
	for _, c := range d.Categories {
		cats[c.Coin] = c.Category
	}
	var out []domain.Market
	for di, meta := range d.Metas {
		dex := ""
		if di > 0 {
			if di >= len(d.Dexs) || d.Dexs[di] == nil {
				continue
			}
			dex = d.Dexs[di].Name
		}
		for i, a := range meta.Universe {
			m := domain.Market{
				Coin:         a.Name,
				Display:      a.Name,
				Kind:         domain.Perp,
				Dex:          dex,
				AssetID:      i,
				SzDecimals:   a.SzDecimals,
				MaxLeverage:  a.MaxLeverage,
				OnlyIsolated: a.OnlyIsolated || a.MarginMode == "strictIsolated" || a.MarginMode == "noCross",
				Delisted:     a.IsDelisted,
				Category:     cats[a.Name],
			}
			if di > 0 {
				m.Kind = domain.HIP3
				m.AssetID = HIP3AssetOffset + di*HIP3DexStride + i
				if !strings.Contains(a.Name, ":") {
					m.Coin = dex + ":" + a.Name
				}
				_, bare, _ := strings.Cut(m.Coin, ":")
				m.Display = bare + " (" + dex + ")"
			} else if m.Category == "" {
				m.Category = "crypto"
			}
			out = append(out, m)
		}
	}
	tokens := map[int]SpotToken{}
	for _, t := range d.Spot.Tokens {
		tokens[t.Index] = t
	}
	for _, p := range d.Spot.Universe {
		base, quote := tokens[p.Tokens[0]], tokens[p.Tokens[1]]
		disp := p.Name
		if base.Name != "" && quote.Name != "" {
			disp = base.Name + "/" + quote.Name
		}
		out = append(out, domain.Market{
			Coin:       p.Name,
			Display:    disp,
			Kind:       domain.Spot,
			AssetID:    SpotAssetOffset + p.Index,
			SzDecimals: base.SzDecimals,
			Category:   "spot",
		})
	}
	for _, o := range d.Outcomes.Outcomes {
		kv := parseKV(o.Description)
		q := outcomeQuestion(o, kv)
		for side, s := range o.SideSpecs {
			n := 10*o.Outcome + side
			out = append(out, domain.Market{
				Coin:     "#" + strconv.Itoa(n),
				Display:  q + ": " + renderTemplate(s.Name, kv),
				Kind:     domain.Outcome,
				AssetID:  OutcomeAssetOffset + n,
				Category: "outcome",
			})
		}
	}
	// Builder dexes, spot tokens and outcome questions are third-party
	// names: strip terminal controls before they reach any card (S3).
	for i := range out {
		out[i].Display = domain.CleanText(out[i].Display)
	}
	return out
}

// parseKV parses "k:v|k:v" outcome descriptions (values may contain ':').
func parseKV(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, "|") {
		if k, v, ok := strings.Cut(part, ":"); ok {
			out[k] = v
		}
	}
	return out
}

func renderTemplate(s string, kv map[string]string) string {
	s = strings.TrimPrefix(s, "template:")
	for k, v := range kv {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}

func outcomeQuestion(o OutcomeSpec, kv map[string]string) string {
	switch {
	case kv["participantA"] != "" && kv["participantB"] != "":
		a, b := kv["shortNameA"], kv["shortNameB"]
		if a == "" || b == "" {
			a, b = kv["participantA"], kv["participantB"]
		}
		return a + " vs " + b
	case kv["perp"] != "" && kv["threshold"] != "":
		return kv["perp"] + " vs " + kv["threshold"]
	}
	return renderTemplate(o.Name, kv)
}

// ensure lazily loads the registry for Resolve/Markets.
func (r *registry) ensure() {
	r.mu.RLock()
	loaded := r.all != nil
	r.mu.RUnlock()
	if loaded || r.c == nil {
		return
	}
	r.tryMu.Lock()
	now := r.c.now()
	if !r.lastTry.IsZero() && now.Sub(r.lastTry) < 30*time.Second {
		r.tryMu.Unlock()
		return
	}
	r.lastTry = now
	r.tryMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = r.Refresh(ctx)
}

// Markets returns every live (non-delisted) market, all kinds.
func (r *registry) Markets() []domain.Market {
	r.ensure()
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]domain.Market, 0, len(r.all))
	for _, m := range r.all {
		if !m.Delisted {
			out = append(out, m)
		}
	}
	return out
}

// Resolve maps user input to a market (see Registry).
func (r *registry) Resolve(input string) (domain.Market, error) {
	r.ensure()
	in := strings.TrimSpace(input)
	in = strings.TrimPrefix(in, "$")
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.all == nil {
		return domain.Market{}, fmt.Errorf("%w: %q (market registry not loaded)", ErrUnknownCoin, input)
	}
	if in == "" {
		return domain.Market{}, &UnknownCoinError{Input: input}
	}
	if i, ok := r.exact[in]; ok {
		return r.all[i], nil
	}
	low := strings.ToLower(in)
	for _, cand := range candidates(low) {
		if i, ok := r.keys[cand]; ok {
			return r.all[i], nil
		}
		for _, target := range aliases[cand] {
			if i, ok := r.exact[target]; ok {
				return r.all[i], nil
			}
		}
	}
	return domain.Market{}, &UnknownCoinError{Input: input, Suggestions: r.suggest(in)}
}

// Alternatives reports every live HIP-3 market a bare name like "nvda"
// could mean when more than one builder dex lists it ("xyz:NVDA",
// "abc:NVDA"). It returns nil for qualified input ("xyz:nvda"), native
// coins, unambiguous names and registries it does not know. Mutating tools
// refuse ambiguous names: a thin builder dex must never capture a ticker.
func Alternatives(r Registry, input string) []string {
	reg, ok := r.(*registry)
	if !ok {
		return nil
	}
	low := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(input), "$"))
	if low == "" || strings.Contains(low, ":") {
		return nil
	}
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	if _, native := reg.exact[strings.ToUpper(low)]; native {
		return nil
	}
	if alts := reg.bare[low]; len(alts) > 1 {
		return append([]string(nil), alts...)
	}
	return nil
}

// candidates returns normalized spellings to try, most specific first:
// "btc-perp" -> "btc", "eth/usd" -> "eth", "solusdt" -> "sol".
func candidates(low string) []string {
	out := []string{low}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && s != low {
			out = append(out, s)
		}
	}
	// "hype-usdc" / "hype usdc" mean the HYPE/USDC spot pair when it
	// exists; the perp is only the fallback.
	for _, suf := range []string{"-usdc", " usdc"} {
		if strings.HasSuffix(low, suf) {
			add(strings.TrimSuffix(low, suf) + "/usdc")
		}
	}
	for _, suf := range []string{"-perp", " perp", "perp", "-usd", "/usd", "-usdc", "usdt", "-usdt", "/usdt"} {
		if strings.HasSuffix(low, suf) {
			add(strings.TrimSuffix(low, suf))
		}
	}
	if strings.HasPrefix(low, "@") || strings.HasPrefix(low, "#") {
		return out
	}
	// "xyz-nvda" / "nvda-xyz" / "nvda (xyz)" spellings of HIP-3 coins.
	if a, b, ok := strings.Cut(low, "-"); ok {
		add(a + ":" + b)
		add(b + ":" + a)
	}
	return out
}

// suggest returns up to 5 close coins: fuzzy subsequence matches first,
// then edit-distance neighbours.
func (r *registry) suggest(in string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] && len(out) < 5 {
			seen[s] = true
			out = append(out, s)
		}
	}
	upper := strings.ToUpper(in)
	for _, m := range fuzzy.Find(upper, upperAll(r.names)) {
		add(r.names[m.Index])
	}
	if len(out) < 5 {
		type cand struct {
			name string
			d    int
		}
		var cs []cand
		for _, n := range r.names {
			bare := n
			if _, b, ok := strings.Cut(n, ":"); ok {
				bare = b
			}
			d := levenshtein(upper, strings.ToUpper(bare))
			if d <= 2 && d < len(upper) {
				cs = append(cs, cand{n, d})
			}
		}
		sort.SliceStable(cs, func(i, j int) bool { return cs[i].d < cs[j].d })
		for _, c := range cs {
			add(c.name)
		}
	}
	return out
}

func upperAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToUpper(s)
	}
	return out
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// snapshot returns the installed data and the USDC spot map (for Account).
func (r *registry) snapshot() (RegistryData, map[int]string) {
	r.ensure()
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.data, r.spotUSDC
}

// lookup returns the market for an exact wire coin.
func (r *registry) lookup(coin string) (domain.Market, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	i, ok := r.exact[coin]
	if !ok {
		return domain.Market{}, false
	}
	return r.all[i], true
}
