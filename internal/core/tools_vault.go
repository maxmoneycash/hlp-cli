package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/sahilm/fuzzy"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/hlpxyz"
	"github.com/maxmoneycash/hlp-cli/internal/metrics"
)

// ---- vault list ----

// loadVaults returns the merged, unfiltered universe: stats-data (every
// Hyperliquid vault) joined with the hlp.xyz curated list. Either source may
// fail; the other still answers with Meta.Partial set.
func loadVaults(ctx context.Context, env *Env) ([]domain.VaultSummary, domain.Meta, error) {
	var (
		hl, xyz         []domain.VaultSummary
		hlMeta, xyzMeta domain.Meta
		hlErr, xyzErr   error
	)
	parallel(2, func(i int) {
		if i == 0 {
			hl, hlMeta, hlErr = env.HL.VaultList(ctx)
			return
		}
		if env.HLPXYZ != nil {
			xyz, xyzMeta, xyzErr = env.HLPXYZ.VaultList(ctx)
		}
	})
	if hlErr != nil && (env.HLPXYZ == nil || xyzErr != nil) {
		return nil, domain.Meta{}, hlErr
	}
	meta := fillMeta(hlMeta, env, "stats-data")
	switch {
	case hlErr != nil:
		meta = fillMeta(xyzMeta, env, "hlp.xyz")
		meta.Partial = true
		meta.Warnings = append(meta.Warnings, "Hyperliquid vault list unavailable ("+AsError(hlErr).Message+"); showing hlp.xyz curated vaults only")
	case env.HLPXYZ != nil && xyzErr != nil:
		meta.Partial = true
		meta.Warnings = append(meta.Warnings, "hlp.xyz unavailable ("+AsError(xyzErr).Message+"); curated metrics missing")
	case env.HLPXYZ != nil:
		meta.Source = "stats-data+hlp.xyz"
		meta.Stale = meta.Stale || xyzMeta.Stale
		meta.Warnings = append(meta.Warnings, xyzMeta.Warnings...)
	}
	return MergeVaults(hl, xyz), meta, nil
}

// MergeVaults joins Hyperliquid rows with hlp.xyz curated rows by address.
// Curated rows set Curated and their v6 share-price metrics (returns, max
// DD, Sharpe, spark, model, accuracy) win; TVL, APR and the live flags stay
// from Hyperliquid unless missing. Curated-only rows are kept.
func MergeVaults(hl, xyz []domain.VaultSummary) []domain.VaultSummary {
	idx := make(map[string]int, len(hl))
	out := make([]domain.VaultSummary, 0, len(hl)+len(xyz))
	for _, v := range hl {
		v.Address = strings.ToLower(v.Address)
		if len(v.Sources) == 0 {
			v.Sources = []string{"stats-data"}
		}
		idx[v.Address] = len(out)
		out = append(out, v)
	}
	for _, x := range xyz {
		x.Address = strings.ToLower(x.Address)
		i, ok := idx[x.Address]
		if !ok {
			x.Curated = true
			if len(x.Sources) == 0 {
				x.Sources = []string{"hlp.xyz"}
			}
			idx[x.Address] = len(out)
			out = append(out, x)
			continue
		}
		v := &out[i]
		v.Curated = true
		if len(x.Returns) > 0 {
			v.Returns = x.Returns
			v.MetricsAsOf = x.MetricsAsOf
			if x.ReturnsModel != "" {
				v.ReturnsModel = x.ReturnsModel
			}
		}
		if x.CapacityUSD != 0 && v.CapacityUSD == 0 {
			v.CapacityUSD = x.CapacityUSD
		}
		mergeVaultRisk(v, x)
		if len(x.Spark) > 0 {
			v.Spark = x.Spark
			v.SparkModel = x.SparkModel
		}
		if x.SPModel != "" {
			v.SPModel = x.SPModel
			v.AccuracyPct = x.AccuracyPct
		}
		if v.TVL == 0 && !v.TVLKnown {
			v.TVL = x.TVL
			v.TVLKnown = x.TVLKnown
		}
		if v.Name == "" {
			v.Name = x.Name
		}
		if v.Leader == "" {
			v.Leader = x.Leader
		}
		if v.MaxGrossLev == 0 {
			v.MaxGrossLev = x.MaxGrossLev
		}
		if v.AgeDays == 0 {
			v.AgeDays = x.AgeDays
		}
		if !x.UpdatedAt.IsZero() {
			v.UpdatedAt = x.UpdatedAt
		}
		v.Stale = v.Stale || x.Stale
		v.Sources = appendUnique(v.Sources, x.Sources...)
		if len(x.Sources) == 0 {
			v.Sources = appendUnique(v.Sources, "hlp.xyz")
		}
	}
	for i := range out {
		if v := &out[i]; v.CapacityUSD == 0 && v.LeaderFraction > 0 {
			v.CapacityUSD = metrics.Capacity(v.TVL, v.LeaderFraction, false)
		}
	}
	return out
}

func appendUnique(s []string, add ...string) []string {
	for _, a := range add {
		dup := false
		for _, x := range s {
			if x == a {
				dup = true
			}
		}
		if !dup {
			s = append(s, a)
		}
	}
	return s
}

// FilterVaults applies screener filters, sort and limit. With a query, the
// default $10k TVL floor drops to $1 (empty and dust vaults never match a
// name search) and matches are ranked by name closeness, then live before
// dead, then TVL. A max-dd below 1 is read as a fraction (0.2 = 20%).
func FilterVaults(vs []domain.VaultSummary, p VaultsInput) ([]domain.VaultSummary, error) {
	normalizeMaxDD(&p)
	out, _, err := filterVaults(vs, p)
	return out, err
}

// normalizeMaxDD reads a max-dd in (0,1) as a fraction (every output is a
// fraction, so agents pass 0.2) and returns the warning saying so.
func normalizeMaxDD(p *VaultsInput) string {
	if p.MaxDD <= 0 || p.MaxDD >= 1 {
		return ""
	}
	in := p.MaxDD
	p.MaxDD *= 100
	return fmt.Sprintf("maxDd %g read as a fraction (%g%%); for %g%% pass %g", in, p.MaxDD, in, in/100)
}

// queryMinTVL is the TVL floor for name searches: it drops empty and dust
// vaults (copycats and dead namesakes) that would otherwise rank first.
const queryMinTVL = 1.0

func hasVaultDrawdown(v domain.VaultSummary) bool {
	return v.RiskByWindow[domain.WAll].MaxDD != nil || v.MaxDD != 0
}

func hasVaultSharpe(v domain.VaultSummary) bool {
	return v.RiskByWindow[domain.WAll].Sharpe != nil || v.Sharpe != 0
}

// HasRiskMetrics reports whether either all-time risk metric is available.
func HasRiskMetrics(v domain.VaultSummary) bool { return hasVaultDrawdown(v) || hasVaultSharpe(v) }

// filterVaults is FilterVaults that also counts the rows a risk filter
// excluded because their metric is unknown. Unknown metrics never pass a
// filter they were not checked against, and sort last on that key in
// either direction.
func filterVaults(vs []domain.VaultSummary, p VaultsInput) ([]domain.VaultSummary, int, error) {
	less, err := vaultSort(p.Sort)
	if err != nil {
		return nil, 0, err
	}
	if k := strings.ToLower(p.Sort); k == "sharpe" || k == "maxdd" || k == "dd" {
		known := hasVaultDrawdown
		if k == "sharpe" {
			known = hasVaultSharpe
		}
		inner := less
		less = func(a, b domain.VaultSummary) bool {
			ka, kb := known(a), known(b)
			if ka != kb {
				return ka
			}
			return inner(a, b)
		}
	}
	unknown := 0
	if p.MaxDD < 0 || p.MaxDD > 100 {
		return nil, 0, &Error{Code: CodeUsage, Message: fmt.Sprintf("max-dd must be a percent (20) or a fraction (0.2), got %g", p.MaxDD), Hint: "e.g. --max-dd 25 or --max-dd 0.25"}
	}
	q := strings.TrimSpace(p.Query)
	byAddr := strings.HasPrefix(strings.ToLower(q), "0x")
	minTVL := p.MinTVL
	switch {
	case minTVL != 0:
	case q == "":
		minTVL = 10_000
	case !byAddr:
		minTVL = queryMinTVL
	}
	if minTVL < 0 { // tag filters: no floor
		minTVL = math.Inf(-1)
	}
	out := make([]domain.VaultSummary, 0, len(vs))
	var closed []domain.VaultSummary // name search: used only when nothing open matches
	for _, v := range vs {
		// Closed vaults stay opt-in for the CLI. Directory clients can include
		// them explicitly; the open-only filter below still takes precedence.
		if v.Closed && q == "" && !p.IncludeClosed {
			continue
		}
		if v.TVL < minTVL {
			continue
		}
		if p.MaxDD > 0 && !hasVaultDrawdown(v) {
			unknown++
			continue
		}
		if p.MaxDD > 0 && v.MaxDD < -p.MaxDD/100 {
			continue
		}
		if p.MinAgeDays > 0 && v.AgeDays < p.MinAgeDays {
			continue
		}
		if p.OpenOnly && (!v.AllowDeposits || v.Closed) {
			continue
		}
		if v.Rel == "child" && !p.IncludeHLP && q == "" {
			continue
		}
		if p.CuratedOnly && !v.Curated {
			continue
		}
		if v.Closed && !byAddr && !p.IncludeClosed {
			if q != "" {
				closed = append(closed, v)
			}
			continue
		}
		out = append(out, v)
	}
	if q != "" {
		out = rankVaultMatches(fuzzyVaults(out, q), q)
		if len(out) == 0 && !p.OpenOnly {
			out = rankVaultMatches(fuzzyVaults(closed, q), q)
		}
		if p.Sort != "" {
			sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j]) })
		}
	} else {
		sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j]) })
	}
	out, _ = pageVaults(out, p.Offset, p.Limit)
	return out, unknown, nil
}

// pageVaults applies offset and limit (default 50) and returns the rows
// and the count before paging.
func pageVaults(vs []domain.VaultSummary, offset, limit int) ([]domain.VaultSummary, int) {
	total := len(vs)
	if limit <= 0 {
		limit = 50
	}
	offset = min(max(offset, 0), total)
	vs = vs[offset:]
	if len(vs) > limit {
		vs = vs[:limit]
	}
	return vs, total
}

func fuzzyVaults(vs []domain.VaultSummary, q string) []domain.VaultSummary {
	lq := strings.ToLower(q)
	if strings.HasPrefix(lq, "0x") {
		var out []domain.VaultSummary
		for _, v := range vs {
			if strings.HasPrefix(v.Address, lq) {
				out = append(out, v)
			}
		}
		return out
	}
	names := make([]string, len(vs))
	for i, v := range vs {
		names[i] = v.Name
	}
	var exact, rest []domain.VaultSummary
	seen := map[int]bool{}
	for i, v := range vs {
		if strings.EqualFold(v.Name, q) {
			exact = append(exact, v)
			seen[i] = true
		}
	}
	for _, m := range fuzzy.Find(q, names) {
		if !seen[m.Index] {
			rest = append(rest, vs[m.Index])
		}
	}
	return append(exact, rest...)
}

func vaultSort(key string) (func(a, b domain.VaultSummary) bool, error) {
	switch strings.ToLower(key) {
	case "", "tvl":
		return func(a, b domain.VaultSummary) bool { return a.TVL > b.TVL }, nil
	case "apr":
		return func(a, b domain.VaultSummary) bool { return a.APR > b.APR }, nil
	case "ret7d", "7d":
		return byReturn(domain.W1W), nil
	case "ret30d", "30d":
		return byReturn(domain.W1M), nil
	case "retall", "all":
		return byReturn(domain.WAll), nil
	case "sharpe":
		return func(a, b domain.VaultSummary) bool { return a.Sharpe > b.Sharpe }, nil
	case "maxdd", "dd":
		return func(a, b domain.VaultSummary) bool { return a.MaxDD > b.MaxDD }, nil
	case "age":
		return func(a, b domain.VaultSummary) bool { return a.AgeDays > b.AgeDays }, nil
	}
	return nil, &Error{Code: CodeUsage, Message: fmt.Sprintf("unknown sort %q", key), Hint: "sort by one of: tvl, apr, ret7d, ret30d, retall, sharpe, maxdd, age"}
}

func byReturn(w domain.Window) func(a, b domain.VaultSummary) bool {
	return func(a, b domain.VaultSummary) bool { return a.Returns[w] > b.Returns[w] }
}

func runVaults(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*VaultsInput)
	tagged := len(splitList(p.Tags)) > 0 || len(splitList(p.ExcludeTags)) > 0 || strings.TrimSpace(p.Kind) != ""
	if tagged {
		// validate tags before the (large) vault download
		if _, err := registryFilter(env.vaultRegistry(), p.Tags, p.ExcludeTags, p.Kind); err != nil {
			return nil, err
		}
	}
	vs, meta, err := loadVaults(ctx, env)
	if err != nil {
		return nil, err
	}
	meta.Warnings = append(meta.Warnings, env.VaultWarnings...)
	q := *p
	if tagged {
		if vs, err = tagVaults(env, vs, q); err != nil {
			return nil, err
		}
		// curated rows without live data have TVL 0: keep them unless the
		// user set a floor; closed and child rows were already chosen by tag.
		if q.MinTVL == 0 {
			q.MinTVL = -1
		}
		q.IncludeHLP = true
	}
	if w := normalizeMaxDD(&q); w != "" {
		meta.Warnings = append(meta.Warnings, w)
	}
	limit, offset := q.Limit, q.Offset
	q.Limit, q.Offset = math.MaxInt, 0
	all, unknown, err := filterVaults(vs, q)
	if err != nil {
		return nil, err
	}
	out, total := pageVaults(all, offset, limit)
	meta.Total = &total
	meta.Truncated = offset+len(out) < total
	if unknown > 0 {
		meta.Warnings = append(meta.Warnings, fmt.Sprintf("%d vaults without drawdown data excluded (risk metrics come from hlp.xyz curation)", unknown))
	}
	return &Result{Card: CardVaults, Title: "Vaults", Data: out, Meta: meta}, nil
}

// ---- vault resolution ----

// vaultIndex resolves vault names against the merged universe, loading it
// at most once (compare and arena resolve several names concurrently).
type vaultIndex struct {
	env  *Env
	once sync.Once
	vs   []domain.VaultSummary
	err  error
	xmu  sync.Mutex
	xvs  []domain.VaultSummary // hlp.xyz list (cheap) for address lookups
	xok  bool
}

func newVaultIndex(env *Env) *vaultIndex { return &vaultIndex{env: env} }

func (ix *vaultIndex) all(ctx context.Context) ([]domain.VaultSummary, error) {
	ix.once.Do(func() { ix.vs, _, ix.err = loadVaults(ctx, ix.env) })
	return ix.vs, ix.err
}

func (ix *vaultIndex) curated(ctx context.Context) []domain.VaultSummary {
	ix.xmu.Lock()
	defer ix.xmu.Unlock()
	if !ix.xok && ix.env.HLPXYZ != nil {
		if xs, _, err := ix.env.HLPXYZ.VaultList(ctx); err == nil {
			ix.xvs, ix.xok = xs, true
		}
	}
	return ix.xvs
}

// resolve maps an address or (fuzzy) name to an address and, when known,
// the screener row. Addresses skip the large stats-data download; the cheap
// hlp.xyz list still supplies curated metrics.
func (ix *vaultIndex) resolve(ctx context.Context, s string) (string, *domain.VaultSummary, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil, Errorf(CodeUsage, "vault is required (an address or a name)")
	}
	if strings.EqualFold(s, "hlp") {
		s = HLPAddress
	}
	if IsAddress(s) {
		addr := strings.ToLower(s)
		xs := ix.curated(ctx)
		for i := range xs {
			if strings.EqualFold(xs[i].Address, addr) {
				row := MergeVaults(nil, xs[i:i+1])[0]
				return addr, &row, nil
			}
		}
		return addr, nil, nil
	}
	addr, err := resolveCurated(ix.env, s)
	if err != nil {
		return "", nil, err
	}
	vs, err := ix.all(ctx)
	if err != nil {
		return "", nil, AsError(err)
	}
	if addr != "" {
		// a curated name wins over namesakes, when the live list has it
		for i := range vs {
			if strings.EqualFold(vs[i].Address, addr) {
				row := vs[i]
				return addr, &row, nil
			}
		}
	}
	ms := rankVaultMatches(fuzzyVaults(vs, s), s)
	if len(ms) == 0 {
		return "", nil, &Error{Code: CodeUnknownVault, Message: "no vault matches " + s, Hint: "search with `hlp vaults <words>` or pass the 0x address"}
	}
	best := ms[0]
	return best.Address, &best, nil
}

// normName folds a vault name for matching: lowercase letters and digits
// only, so "Growi HF" and "GrowiHF" compare equal.
func normName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// rankVaultMatches orders name matches: closeness of the name first (exact,
// same letters, prefix, substring, fuzzy), then live vaults (open, with TVL)
// before closed or empty ones, then TVL. Copycat and dead namesakes are
// common, so "/vault growi" must land on the live $12M vault, not a closed
// one with the same letters.
func rankVaultMatches(ms []domain.VaultSummary, q string) []domain.VaultSummary {
	nq := normName(q)
	tier := func(v domain.VaultSummary) int {
		n := normName(v.Name)
		switch {
		case strings.EqualFold(v.Name, q):
			return 0
		case n == nq:
			return 1
		case nq != "" && strings.HasPrefix(n, nq):
			return 2
		case nq != "" && strings.Contains(n, nq):
			return 3
		}
		return 4
	}
	live := func(v domain.VaultSummary) bool { return !v.Closed && v.TVL > 0 }
	out := append([]domain.VaultSummary(nil), ms...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ta, tb := tier(a), tier(b); ta != tb {
			return ta < tb
		}
		if la, lb := live(a), live(b); la != lb {
			return la
		}
		return a.TVL > b.TVL
	})
	return out
}

// vaultAlternatives names other close matches of q besides chosen (at most
// two), for a "also: GrowiHF (closed)" footer.
func vaultAlternatives(ctx context.Context, ix *vaultIndex, q, chosen string) []string {
	if IsAddress(q) || strings.EqualFold(q, "hlp") {
		return nil
	}
	vs, err := ix.all(ctx)
	if err != nil {
		return nil
	}
	nq := normName(q)
	var out []string
	for _, v := range rankVaultMatches(fuzzyVaults(vs, q), q) {
		if strings.EqualFold(v.Address, chosen) || !strings.Contains(normName(v.Name), nq) {
			continue
		}
		name := v.Name
		if v.Closed {
			name += " (closed)"
		} else if v.TVL <= 0 {
			name += " (empty)"
		}
		out = append(out, name)
		if len(out) == 2 {
			break
		}
	}
	return out
}

// resolveVault resolves one vault argument.
func resolveVault(ctx context.Context, env *Env, s string) (string, *domain.VaultSummary, error) {
	return newVaultIndex(env).resolve(ctx, s)
}

// ---- benchmarks ----

// equityBenchmarks come from hlp.xyz (Yahoo prices); everything else is a
// Hyperliquid market or HLP itself.
var equityBenchmarks = map[string]bool{"SPY": true, "NQ": true, "DOW": true, "NVDA": true}

// benchmarkCurve returns a raw price/NAV series for a benchmark name over
// [start, end] (daily).
func benchmarkCurve(ctx context.Context, env *Env, name string, start, end time.Time) (domain.Series, error) {
	up := strings.ToUpper(strings.TrimSpace(name))
	switch {
	case up == "HLP":
		return vaultCurve(ctx, env, HLPAddress, nil)
	case equityBenchmarks[up]:
		x, err := env.needHLPXYZ()
		if err != nil {
			return nil, err
		}
		return x.Benchmark(ctx, up, start, end)
	}
	m, err := env.Resolve(ctx, name)
	if err != nil {
		return nil, err
	}
	days := int(end.Sub(start).Hours()/24) + 3
	days = min(max(days, 2), 5000)
	cs, err := env.HL.Candles(ctx, m.Coin, domain.I1d, days, end.UnixMilli())
	if err != nil {
		return nil, err
	}
	out := make(domain.Series, 0, len(cs))
	for _, c := range cs {
		out = append(out, domain.Point{T: c.T, V: c.C})
	}
	return out, nil
}

// vaultCurve returns the best share-price curve for a vault: hlp.xyz v6 NAV
// when available, else the flow-neutral curve from Hyperliquid's portfolio.
func vaultCurve(ctx context.Context, env *Env, addr string, d *domain.VaultDetail) (domain.Series, error) {
	if env.HLPXYZ != nil {
		if nav, err := env.HLPXYZ.NAV(ctx, addr); err == nil && len(nav.Curve) > 1 {
			return navSeries(nav), nil
		}
	}
	if d == nil {
		det, err := env.HL.Vault(ctx, addr)
		if err != nil {
			return nil, err
		}
		d = &det
	}
	if len(d.NAV) > 1 {
		return d.NAV, nil
	}
	return metrics.CurveFromPortfolio(d.Equity, d.PnL), nil
}

func navSeries(n hlpxyz.NAVCurve) domain.Series {
	out := make(domain.Series, 0, len(n.Curve))
	for _, p := range n.Curve {
		out = append(out, domain.Point{T: p.TS, V: p.NAV})
	}
	return out
}

// WindowStart returns the first timestamp (ms) of window w ending at endMs;
// 0 means "all". Unknown windows are USAGE errors.
func WindowStart(w string, endMs int64) (int64, error) {
	end := time.UnixMilli(endMs).UTC()
	day := int64(24 * time.Hour / time.Millisecond)
	switch domain.Window(strings.ToLower(w)) {
	case "", domain.WAll:
		return 0, nil
	case domain.W1D:
		return endMs - day, nil
	case domain.W1W:
		return endMs - 7*day, nil
	case domain.W1M:
		return endMs - 30*day, nil
	case domain.W3M:
		return endMs - 91*day, nil
	case domain.W6M:
		return endMs - 182*day, nil
	case domain.W1Y:
		return endMs - 365*day, nil
	case domain.WYTD:
		return time.Date(end.Year(), 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), nil
	}
	return 0, Errorf(CodeUsage, "window must be 1d, 1w, 1m, 3m, 6m, ytd, 1y or all, got %q", w)
}

// ClipSeries keeps points at or after start (keeping the last point before
// start as the base so window returns are exact).
func ClipSeries(s domain.Series, start int64) domain.Series {
	if start <= 0 || len(s) == 0 {
		return s
	}
	i := sort.Search(len(s), func(i int) bool { return s[i].T >= start })
	if i > 0 && (i == len(s) || s[i].T > start) {
		i--
	}
	return s[i:]
}

// ---- vault detail ----

type vaultOpts struct {
	window     string
	benchmarks []string
	fills      bool
	depositors bool
	light      bool // compare: no benchmarks, live or composition
}

// buildVault assembles the tearsheet input: Hyperliquid vaultDetails, then
// hlp.xyz enrichment via hlpxyz.ToDetail (v6 NAV, lockup, composition,
// flows, leverage history), benchmarks, metrics.Tearsheet and the checklist.
func buildVault(ctx context.Context, env *Env, addr string, row *domain.VaultSummary, o vaultOpts) (domain.VaultDetail, domain.Meta, error) {
	meta := env.meta("hyperliquid")
	d, err := env.HL.Vault(ctx, addr)
	if err != nil {
		e := AsError(err)
		if e.Code == CodeNotFound {
			return d, meta, &Error{Code: CodeUnknownVault, Message: addr + " is not a Hyperliquid vault", Hint: "HyperEVM strategy vaults: try `hlp fund`; any account: `hlp account " + addr + "`"}
		}
		return d, meta, e
	}
	// Custom readers and older cached fixtures may not carry the internal
	// presence bit. A returned account or equity series still establishes TVL.
	if d.Account != nil || len(d.Equity) > 0 {
		d.Summary.TVLKnown = true
	}
	if row != nil {
		mergeSummary(&d.Summary, *row)
	}
	if d.Summary.Address == "" {
		d.Summary.Address = addr
	}
	var warns []string
	model := d.SPModel
	curve := d.NAV
	if env.HLPXYZ != nil {
		if x, ok := fetchXYZDetail(ctx, env, addr, o.light, o.fills, o.window); ok {
			// Date the v6 curve by its own build/sample time. Old activity
			// history must not discard a current NAV curve: those endpoints
			// update independently. Keep activity's age in its own source row.
			fresh := metrics.CurveFromPortfolio(d.Equity, d.PnL)
			navAsOf := int64(0)
			if x.Analytics != nil && x.Analytics.NAV != nil {
				navAsOf = x.Analytics.NAV.BuiltAt
			}
			if navAsOf == 0 && len(x.NAV) > 0 {
				navAsOf = x.NAV[len(x.NAV)-1].T
			}
			navStale := navAsOf > 0 && env.now().Sub(time.UnixMilli(navAsOf)) > hlpxyz.StaleAfter
			if h, ok := x.Freshness["history"]; ok && h.Stale {
				meta.Warnings = append(meta.Warnings, "hlp.xyz activity history is "+staleAgeText(x.Meta.Warnings))
			}
			x.Meta.Stale = navStale
			useFresh := navStale && len(fresh) > 1
			if len(x.NAV) > 1 && !useFresh {
				curve, model = x.NAV, domain.SPv6
				d.SharePrice = x.SharePrice
				d.Summary.AccuracyPct = nz(x.Summary.AccuracyPct, d.Summary.AccuracyPct)
				meta.Source = "hyperliquid+hlp.xyz"
			}
			if useFresh {
				x.Meta.Stale = false
				x.NAV = nil
				curve, model = fresh, domain.SPHL
				meta.Warnings = append(meta.Warnings, "curve from the Hyperliquid portfolio: hlp.xyz v6 NAV last updated "+hlpxyz.ShortDate(time.UnixMilli(navAsOf), env.now())+" (over 48h)")
			}
			mergeXYZ(&d, x)
			meta.Partial = meta.Partial || x.Meta.Partial
			for _, warning := range x.Meta.Warnings {
				if !strings.Contains(warning, "last updated") {
					meta.Warnings = append(meta.Warnings, warning)
				}
			}
			if x.Meta.Stale {
				// say why: AGENTS.md tells agents to respect meta.stale
				meta.Warnings = append(meta.Warnings, staleWarnings(x.Meta.Warnings)...)
			}
		}
	}
	if row != nil {
		// the screener row's curated v6 metrics win over the per-vault
		// curves, so list_vaults, get_vault and compare_vaults agree
		mergeSummary(&d.Summary, *row)
	}
	if d.Freshness == nil {
		d.Freshness = make(map[string]domain.VaultSourceFreshness)
	}
	if d.Account != nil {
		d.Freshness["account"] = domain.VaultSourceFreshness{Source: "Hyperliquid account", AsOf: d.Account.Time, ObservedAt: env.now().UnixMilli()}
	}
	if d.Summary.MetricsAsOf > 0 {
		d.Freshness["metrics"] = domain.VaultSourceFreshness{Source: "hlp.xyz metrics", AsOf: d.Summary.MetricsAsOf,
			ObservedAt: env.now().UnixMilli(), Stale: env.now().Sub(time.UnixMilli(d.Summary.MetricsAsOf)) > hlpxyz.StaleAfter}
	}
	if s := &d.Summary; s.CreatedAt.IsZero() {
		// never leak the zero time (AGENT-UX #13): derive it from the age,
		// else from the start of the curve
		switch {
		case s.AgeDays > 0:
			s.CreatedAt = env.now().UTC().AddDate(0, 0, -s.AgeDays).Truncate(24 * time.Hour)
		case len(curve) > 0:
			s.CreatedAt = time.UnixMilli(curve[0].T).UTC()
		}
	}
	if d.Summary.ReturnsModel == "" && len(d.Summary.Returns) > 0 {
		d.Summary.ReturnsModel = domain.ReturnsTWR // portfolio and v6 curves are both flow-neutral
	}
	if len(curve) < 2 {
		curve, model = metrics.CurveFromPortfolio(d.Equity, d.PnL), domain.SPHL
	}
	if len(curve) < 2 {
		warns = append(warns, "not enough history for a share-price curve")
	}
	// The summary describes the same curve as the top-level fields: one
	// share-price model per result (AGENT-UX #15).
	d.NAV, d.SPModel = curve, model
	d.Summary.SPModel = model
	recentFillsStart := int64(0)
	if o.fills && len(d.Fills) == 0 {
		now := env.now()
		if fs, ferr := env.HL.Fills(ctx, addr, now.Add(-7*24*time.Hour).UnixMilli(), now.UnixMilli()); ferr == nil {
			d.Fills = fs
			recentFillsStart = now.Add(-7 * 24 * time.Hour).UnixMilli()
		} else {
			warns = append(warns, "fills unavailable: "+AsError(ferr).Message)
		}
	}
	end := env.now().UnixMilli()
	if len(curve) > 0 {
		end = curve[len(curve)-1].T
	}
	start, err := WindowStart(o.window, end)
	if err != nil {
		return d, meta, err
	}
	sub := ClipSeries(curve, start)

	bench := map[string]domain.Series{}
	if !o.light && len(sub) > 1 {
		names := o.benchmarks
		if len(names) == 0 {
			names = []string{"HLP", "BTC", "HYPE"}
		}
		if addr == HLPAddress {
			names = removeName(names, "HLP")
		}
		t0, t1 := time.UnixMilli(sub[0].T), time.UnixMilli(sub[len(sub)-1].T)
		series := make([]domain.Series, len(names))
		errs := make([]error, len(names))
		parallel(len(names), func(i int) {
			series[i], errs[i] = benchmarkCurve(ctx, env, names[i], t0, t1)
		})
		for i, n := range names {
			if errs[i] != nil || len(series[i]) < 2 {
				msg := "no data"
				if errs[i] != nil {
					msg = AsError(errs[i]).Message
				}
				warns = append(warns, "benchmark "+strings.ToUpper(n)+": "+msg)
				continue
			}
			bench[strings.ToUpper(n)] = series[i]
		}
	}
	var closed []float64
	metricStart, metricEnd := start, end
	if len(sub) > 0 {
		metricStart, metricEnd = sub[0].T, sub[len(sub)-1].T
	}
	if recentFillsStart > metricStart {
		warns = append(warns, "Trade metrics use recent Hyperliquid fills since "+time.UnixMilli(recentFillsStart).UTC().Format(time.RFC3339)+"; earlier selected-period activity is unavailable")
	}
	for _, f := range d.Fills {
		if f.ClosedPnL != 0 && f.Time >= metricStart && f.Time <= metricEnd {
			closed = append(closed, f.ClosedPnL)
		}
	}
	name := d.Summary.Name
	if name == "" {
		name = shortAddr(addr)
	}
	ts := metrics.Tearsheet(sub, metrics.TearsheetOpts{Title: name, Benchmarks: bench, ClosedPnL: closed, Now: env.now()})
	meta.Warnings = append(meta.Warnings, ts.Meta.Warnings...)
	var hlpM *domain.Metrics
	for i := range ts.Benchmarks {
		if ts.Benchmarks[i].Name == "HLP" {
			hlpM = &ts.Benchmarks[i].Metrics
		}
	}
	ts.Checklist = metrics.Checklist(d, hlpM)
	if len(warns) > 0 {
		meta.Partial = true
		meta.Warnings = append(meta.Warnings, warns...)
	}
	meta.Stale = meta.Stale || d.Summary.Stale
	if meta.Stale && len(staleWarnings(meta.Warnings)) == 0 {
		meta.Warnings = append(meta.Warnings, staleReason(d.Summary, env.now()))
	}
	ts.Meta = meta
	d.Tearsheet = &ts
	d.Meta = meta
	if !o.depositors {
		d.Followers, d.Flows = nil, nil
	}
	if !o.fills {
		d.Fills = nil
	}
	return d, meta, nil
}

func removeName(ns []string, drop string) []string {
	out := ns[:0:0]
	for _, n := range ns {
		if !strings.EqualFold(n, drop) {
			out = append(out, n)
		}
	}
	return out
}

// xyzDetailTimeout bounds the hlp.xyz detail fetch: a vault whose cache is
// still "loading" must not stall a tearsheet that Hyperliquid can serve.
const xyzDetailTimeout = 15 * time.Second

// fetchXYZDetail loads the hlp.xyz parts for a vault in parallel and merges
// them with hlpxyz.ToDetail. ok is false when hlp.xyz knows nothing about it.
func fetchXYZDetail(ctx context.Context, env *Env, addr string, light, fills bool, window string) (domain.VaultDetail, bool) {
	compositionRange := strings.ToLower(window)
	if compositionRange == "" {
		compositionRange = "all"
	}
	var (
		parts    hlpxyz.DetailParts
		mu       sync.Mutex
		warnings []string
	)
	failed := func(part string, err error) {
		if err == nil || errors.Is(err, hlpxyz.ErrNoCache) || errors.Is(err, hlpxyz.ErrNotAVault) {
			return
		}
		mu.Lock()
		warnings = append(warnings, "hlp.xyz "+part+" unavailable: "+AsError(err).Message)
		mu.Unlock()
	}
	parts.Address, parts.Now = addr, env.now()
	parallel(4, func(i int) {
		switch i {
		case 0:
			nav, err := env.HLPXYZ.NAV(ctx, addr)
			failed("NAV", err)
			if err == nil && len(nav.Curve) > 1 {
				mu.Lock()
				parts.NAV = &nav
				mu.Unlock()
			}
		case 1:
			if light {
				return
			}
			live, err := env.HLPXYZ.VaultLive(ctx, addr)
			failed("live ledger", err)
			if err == nil && live.Error == "" {
				mu.Lock()
				parts.Live = &live
				mu.Unlock()
			}
		case 2:
			if light {
				return
			}
			comp, err := env.HLPXYZ.Composition(ctx, addr, compositionRange)
			failed("composition", err)
			if err == nil {
				mu.Lock()
				parts.Composition = &comp
				mu.Unlock()
			}
		case 3:
			if light {
				return
			}
			dctx, cancel := context.WithTimeout(ctx, xyzDetailTimeout)
			defer cancel()
			det, err := env.HLPXYZ.VaultDetail(dctx, addr, hlpxyz.DetailOpts{Light: !fills, Lev: !fills})
			failed("history", err)
			if err == nil && det.Status == "" {
				det.Light = !fills
				mu.Lock()
				parts.Detail = &det
				mu.Unlock()
			}
		}
	})
	if parts.NAV == nil && parts.Live == nil && parts.Composition == nil && parts.Detail == nil {
		return domain.VaultDetail{}, false
	}
	x, err := hlpxyz.ToDetail(parts)
	if err != nil {
		return domain.VaultDetail{}, false
	}
	x.Meta.Warnings = append(x.Meta.Warnings, warnings...)
	x.Meta.Partial = len(warnings) > 0
	return x, true
}

// mergeXYZ lays hlp.xyz facts over the Hyperliquid detail without replacing
// what Hyperliquid knows better (account, followers, capacity, live TVL).
func mergeXYZ(d *domain.VaultDetail, x domain.VaultDetail) {
	d.Analytics, d.Freshness = x.Analytics, x.Freshness
	if len(d.Fills) == 0 {
		d.Fills = x.Fills
	}
	if d.LockupDays == 0 {
		d.LockupDays = x.LockupDays
	}
	if d.Description == "" {
		d.Description = x.Description
	}
	if len(x.Composition) > 0 {
		d.Composition = x.Composition
	}
	if len(d.Flows) == 0 {
		d.Flows = x.Flows
	}
	s, xs := &d.Summary, x.Summary
	if s.ProfitShare == 0 {
		s.ProfitShare = xs.ProfitShare
	}
	if s.MaxGrossLev == 0 {
		s.MaxGrossLev = xs.MaxGrossLev
	}
	if xs.Curated {
		if xs.MetricsAsOf > 0 {
			s.MetricsAsOf = xs.MetricsAsOf
		}
		s.Curated = true
		if len(xs.Returns) > 0 {
			s.Returns = xs.Returns
			s.ReturnsModel = domain.ReturnsTWR
		}
		mergeVaultRisk(s, xs)
	}
	s.Sources = appendUnique(s.Sources, "hlp.xyz")
	if x.Meta.Stale {
		d.Meta.Stale = true
		s.Stale = true
		if !x.Summary.UpdatedAt.IsZero() {
			s.UpdatedAt = x.Summary.UpdatedAt
		}
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = xs.CreatedAt
	}
}

// A reported all-time zero supersedes the older scalar value just as a
// nonzero measurement does. Legacy rows carry only their all-time scalars.
func mergeVaultRisk(dst *domain.VaultSummary, src domain.VaultSummary) {
	if len(src.RiskByWindow) > 0 {
		dst.RiskByWindow = src.RiskByWindow
		dst.MetricsAsOf = src.MetricsAsOf
	}
	all := src.RiskByWindow[domain.WAll]
	if all.MaxDD != nil {
		dst.MaxDD = *all.MaxDD
	} else if src.MaxDD != 0 {
		dst.MaxDD = src.MaxDD
	}
	if all.Sharpe != nil {
		dst.Sharpe = *all.Sharpe
	} else if src.Sharpe != 0 {
		dst.Sharpe = src.Sharpe
	}
}

// staleReason explains a stale flag that no source warning explained.
func staleReason(s domain.VaultSummary, now time.Time) string {
	if s.UpdatedAt.IsZero() {
		return "some figures are stale (source did not say when they last updated)"
	}
	return fmt.Sprintf("%s figures last updated %s (%s ago)", metrics.SourceLabel(strings.Join(s.Sources, "+")),
		hlpxyz.ShortDate(s.UpdatedAt, now), hlpxyz.HumanAge(now.Sub(s.UpdatedAt)))
}

// staleAgeText shortens hlp.xyz's stale warning to "out of date: last
// updated Apr 28 (201d ago, …)" for a note that is not a stale flag.
func staleAgeText(ws []string) string {
	for _, w := range staleWarnings(ws) {
		if i := strings.Index(w, "last updated"); i >= 0 {
			return "out of date: " + w[i:]
		}
	}
	return "out of date"
}

// staleWarnings keeps the hlp.xyz warnings that explain a stale flag.
func staleWarnings(ws []string) []string {
	var out []string
	for _, w := range ws {
		if strings.Contains(w, "last updated") {
			out = append(out, w)
		}
	}
	return out
}

// mergeSummary fills fields the Hyperliquid detail lacks from the screener
// row (curation, v6 metrics, spark).
// dateOnly reports a timestamp with no time of day (a registry date).
func dateOnly(t time.Time) bool {
	return !t.IsZero() && t.UTC().Equal(t.UTC().Truncate(24*time.Hour))
}

func mergeSummary(dst *domain.VaultSummary, row domain.VaultSummary) {
	if dst.Name == "" {
		dst.Name = row.Name
	}
	if dst.Leader == "" {
		dst.Leader = row.Leader
	}
	dst.Curated = dst.Curated || row.Curated
	if len(row.Returns) > 0 && (row.Curated || len(dst.Returns) == 0) {
		dst.Returns = row.Returns
		dst.MetricsAsOf = row.MetricsAsOf
		if row.ReturnsModel != "" {
			dst.ReturnsModel = row.ReturnsModel
		}
	}
	// Prefer a real timestamp over a registry date-only one (midnight UTC)
	// so the same vault reads the same by address and by name.
	if dst.CreatedAt.IsZero() || (dateOnly(dst.CreatedAt) && !row.CreatedAt.IsZero() && !dateOnly(row.CreatedAt)) {
		dst.CreatedAt = row.CreatedAt
	}
	if row.Curated || dst.MaxDD == 0 {
		mergeVaultRisk(dst, row)
	}
	if len(dst.Spark) == 0 {
		dst.Spark = row.Spark
		dst.SparkModel = row.SparkModel
	}
	if dst.TVL == 0 && !dst.TVLKnown {
		dst.TVL = row.TVL
		dst.TVLKnown = row.TVLKnown
	}
	if dst.APR == 0 {
		dst.APR = row.APR
	}
	if dst.AgeDays == 0 {
		dst.AgeDays = row.AgeDays
	}
	if dst.SPModel == "" {
		dst.SPModel = row.SPModel
	}
	if dst.AccuracyPct == 0 {
		dst.AccuracyPct = row.AccuracyPct
	}
	dst.Sources = appendUnique(dst.Sources, row.Sources...)
	dst.Stale = dst.Stale || row.Stale
}

func runVault(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*VaultInput)
	ix := newVaultIndex(env)
	addr, row, err := ix.resolve(ctx, p.Vault)
	if err != nil {
		return nil, err
	}
	if row == nil {
		// By address too: merge the screener row, so a vault reads the
		// same whichever way it was named (AGENT-UX #15).
		row = ix.rowFor(ctx, addr)
	}
	d, meta, err := buildVault(ctx, env, addr, row, vaultOpts{window: p.Window, benchmarks: p.Benchmarks, fills: p.Fills, depositors: p.Depositors})
	if err != nil {
		return nil, err
	}
	if alts := vaultAlternatives(ctx, ix, p.Vault, addr); len(alts) > 0 {
		meta.Warnings = append(meta.Warnings, "also: "+strings.Join(alts, ", "))
	}
	d.Description = UntrustedText(d.Description)
	title := d.Summary.Name
	if title == "" {
		title = shortAddr(addr)
	}
	return &Result{Card: CardVault, Title: title, Data: d, Meta: meta}, nil
}

// UntrustedPrefix marks text written by a third party (a vault leader) in
// tool output, so agents treat it as data, never as instructions
// (SECURITY S11).
const UntrustedPrefix = "[vault leader text] "

// MaxUntrustedText caps third-party text in tool output (runes).
const MaxUntrustedText = 500

// UntrustedText cleans s of terminal controls, caps it at MaxUntrustedText
// runes and marks it with UntrustedPrefix. Empty text stays empty.
func UntrustedText(s string) string {
	s = strings.TrimSpace(domain.CleanText(strings.TrimPrefix(s, UntrustedPrefix)))
	if s == "" {
		return ""
	}
	if r := []rune(s); len(r) > MaxUntrustedText {
		s = strings.TrimSpace(string(r[:MaxUntrustedText-1])) + "…"
	}
	return UntrustedPrefix + s
}

// ---- compare ----

func runCompare(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*CompareInput)
	if len(p.Vaults) < 2 || len(p.Vaults) > 6 {
		return nil, Errorf(CodeUsage, "compare takes 2-6 vaults, got %d", len(p.Vaults))
	}
	if _, err := WindowStart(p.Window, 0); err != nil {
		return nil, err
	}
	n := len(p.Vaults)
	details := make([]domain.VaultDetail, n)
	errs := make([]error, n)
	ix := newVaultIndex(env)
	parallel(n, func(i int) {
		addr, row, err := ix.resolve(ctx, p.Vaults[i])
		if err != nil {
			errs[i] = err
			return
		}
		details[i], _, errs[i] = buildVault(ctx, env, addr, row, vaultOpts{window: p.Window, light: true})
	})
	for i, err := range errs {
		if err != nil {
			e := AsError(err)
			e.Message = p.Vaults[i] + ": " + e.Message
			return nil, e
		}
		details[i].Description = UntrustedText(details[i].Description)
	}
	corr := make([][]float64, n)
	for i := range corr {
		corr[i] = make([]float64, n)
		for j := range corr[i] {
			switch {
			case i == j:
				corr[i][j] = 1
			case j < i:
				corr[i][j] = corr[j][i]
			default:
				corr[i][j] = finite(metrics.Correlation(curveOf(details[i]), curveOf(details[j])))
			}
		}
	}
	meta := env.meta("hyperliquid")
	for _, d := range details {
		meta.Partial = meta.Partial || d.Meta.Partial
		meta.Stale = meta.Stale || d.Meta.Stale
		if strings.Contains(d.Meta.Source, "hlp.xyz") {
			meta.Source = "hyperliquid+hlp.xyz"
		}
	}
	return &Result{Card: CardCompare, Title: "Compare", Data: &CompareData{Vaults: details, Corr: corr}, Meta: meta}, nil
}

func curveOf(d domain.VaultDetail) domain.Series {
	if d.Tearsheet != nil && len(d.Tearsheet.Curve) > 1 {
		return d.Tearsheet.Curve
	}
	return d.NAV
}

func finite(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return x
}

// ---- hlp.xyz: indexes, fund, quant strategies ----

func runIndex(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*IndexInput)
	if p.HistoryMethod != "" && p.HistoryMethod != "observed" && p.HistoryMethod != "website" {
		return nil, Errorf(CodeUsage, "history-method must be observed or website")
	}
	if p.HistoryMethod != "" && !p.History {
		return nil, Errorf(CodeUsage, "history-method requires --history")
	}
	if _, _, _, err := historyOptions(p.Window, p.Points, env.now().UnixMilli()); err != nil {
		return nil, err
	}
	if (p.History || p.Constraints || p.Exposure) && strings.TrimSpace(p.Slug) == "" {
		return nil, Errorf(CodeUsage, "a slug is required for index history, constraints or exposure; use hlp index to list indexes")
	}
	x, err := env.needHLPXYZ()
	if err != nil {
		return nil, err
	}
	if p.Slug == "" {
		ix, err := x.Indexes(ctx, false)
		if err != nil {
			return nil, err
		}
		return &Result{Card: CardIndex, Title: "Vault indexes", Data: &IndexData{Indexes: ix}, Meta: env.meta("hlp.xyz")}, nil
	}
	slug := strings.ToLower(strings.TrimSpace(p.Slug))
	ix, err := x.Index(ctx, slug)
	if err != nil {
		var ae *hlpxyz.APIError
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound || strings.Contains(err.Error(), "bad index slug") {
			// a slug the user typed: a usage error (exit 2), with the slugs
			e := &Error{Code: CodeUsage, Message: fmt.Sprintf("unknown index %q", strings.TrimSpace(p.Slug)), Hint: "`hlp index` lists them"}
			if list, lerr := x.Indexes(ctx, false); lerr == nil && len(list) > 0 {
				slugs := make([]string, 0, len(list))
				for _, l := range list {
					slugs = append(slugs, l.Def.Slug)
				}
				e.Hint = "one of: " + strings.Join(slugs, ", ")
			}
			return nil, e
		}
		return nil, err
	}
	title := ix.Def.Name
	if title == "" {
		title = p.Slug
	}
	view := indexView(ctx, env, ix)
	meta := env.meta("hlp.xyz")
	if p.History {
		view.History = loadIndexHistory(ctx, env, x, view, p)
		meta.Partial = !view.History.Complete
		meta.Stale = view.History.AsOf == 0 || env.now().UnixMilli()-view.History.AsOf > historyTolerance
		meta.Warnings = append(meta.Warnings, view.History.Warnings...)
		meta.Truncated = view.History.Sampled
	}
	if p.Constraints {
		view.Constraints = loadIndexConstraints(ctx, env, x, view.Index, p)
		meta.Partial = meta.Partial || !view.Constraints.Complete
		meta.Truncated = meta.Truncated || view.Constraints.Sampled
		meta.Warnings = append(meta.Warnings, view.Constraints.Warnings...)
		for _, s := range view.Constraints.Sources {
			meta.Stale = meta.Stale || s.Stale
		}
		for _, m := range view.Constraints.Members {
			meta.Stale = meta.Stale || m.CapacityStale
		}
	}
	if p.Exposure {
		view.Exposure = loadIndexExposure(ctx, env, x, view.Index, p)
		meta.Partial = meta.Partial || !view.Exposure.Complete
		meta.Truncated = meta.Truncated || view.Exposure.Sampled
		meta.Stale = meta.Stale || view.Exposure.Source.Stale
		meta.Warnings = append(meta.Warnings, view.Exposure.Warnings...)
		for _, m := range view.Exposure.Members {
			meta.Stale = meta.Stale || m.Stale
		}
	}
	return &Result{Card: CardIndex, Title: title, Data: &IndexData{Index: view}, Meta: meta}, nil
}

// IndexView is one hlp.xyz index plus its latest allocation resolved to
// vault names and live numbers. It embeds hlpxyz.Index, so the JSON keeps
// every field of the raw index and adds "members".
type IndexView struct {
	hlpxyz.Index
	Members     []IndexMember            `json:"members"`
	History     *IndexHistory            `json:"history,omitempty"`
	Constraints *hlpxyz.IndexConstraints `json:"constraints,omitempty"`
	Exposure    *hlpxyz.IndexExposure    `json:"exposure,omitempty"`
}

// IndexMember is one vault of the latest allocation.
type IndexMember struct {
	Address              string   `json:"address"`
	Name                 string   `json:"name,omitempty"`
	Weight               float64  `json:"weight"` // fraction
	TVL                  float64  `json:"tvl,omitempty"`
	Return30d            *float64 `json:"return30d,omitempty"`     // fraction; nil = unknown
	CurrentWeight        *float64 `json:"currentWeight,omitempty"` // modeled drifted weight, fraction
	ReturnRange          *float64 `json:"returnRange,omitempty"`
	ReturnSinceInception *float64 `json:"returnSinceInception,omitempty"`
	AsOf                 int64    `json:"asOf,omitempty"`
}

func indexView(ctx context.Context, env *Env, ix hlpxyz.Index) *IndexView {
	v := &IndexView{Index: ix}
	if len(ix.Allocs) == 0 {
		return v
	}
	byAddr := map[string]domain.VaultSummary{}
	if vs, _, err := loadVaults(ctx, env); err == nil {
		for _, s := range vs {
			byAddr[strings.ToLower(s.Address)] = s
		}
	}
	reg := env.vaultRegistry()
	latest, ok := latestIndexAlloc(ix.Allocs, env.now().UnixMilli())
	if !ok {
		return v
	}
	for a, w := range latest.Weights {
		if w <= 0 {
			continue
		}
		m := IndexMember{Address: a, Weight: w}
		if e, ok := reg.ByAddress(a); ok {
			m.Name = e.Name
		}
		if s, ok := byAddr[strings.ToLower(a)]; ok {
			if m.Name == "" {
				m.Name = s.Name
			}
			m.TVL = s.TVL
			if r, ok := s.Returns[domain.W1M]; ok {
				m.Return30d = &r
			}
		}
		v.Members = append(v.Members, m)
	}
	sort.Slice(v.Members, func(i, j int) bool {
		if v.Members[i].Weight != v.Members[j].Weight {
			return v.Members[i].Weight > v.Members[j].Weight
		}
		return v.Members[i].Address < v.Members[j].Address
	})
	return v
}

// FundData is the payload of CardFund: the hlp.xyz strategy-vault protocol
// status plus its share-price and NAV history. No tool returns it since
// `hlp fund` became the on-chain strategy-vault tearsheet (CardPVault); the
// card stays for saved transcripts.
type FundData struct {
	Status     hlpxyz.PVaultStatus `json:"status"`
	SharePrice domain.Series       `json:"sharePrice"`
	NAV        domain.Series       `json:"nav"`
}

// StrategyData is the payload of CardStrategy: an hlp.xyz quant strategy's
// live target book plus its static backtest. With no id it carries the
// list of strategies instead (Strategies set, Live empty).
type StrategyData struct {
	Live       hlpxyz.QuantLive    `json:"live"`
	Static     *hlpxyz.QuantStatic `json:"static,omitempty"`
	Strategies []StrategySummary   `json:"strategies,omitempty"`
}

// StrategySummary is one row of `hlp strat` with no id: the strategy's
// name and its full-period static backtest (fractions, not percents).
type StrategySummary struct {
	ID     string  `json:"id"`
	Name   string  `json:"name,omitempty"`
	Status string  `json:"status,omitempty"`
	CAGR   float64 `json:"cagr"`
	Sharpe float64 `json:"sharpe"`
	MaxDD  float64 `json:"maxDD"`
	Start  string  `json:"start,omitempty"`
	OK     bool    `json:"ok"` // false when the backtest could not be read
}

// QuantPeriod is one period ("all", "is", "oos") of a static backtest.
type QuantPeriod struct {
	Start  string  `json:"start"`
	End    string  `json:"end"`
	CAGR   float64 `json:"cagr"`
	Vol    float64 `json:"vol"`
	Sharpe float64 `json:"sharpe"`
	MaxDD  float64 `json:"maxDD"`
	Calmar float64 `json:"calmar"`
}

// QuantPeriodOf decodes period name ("all", "oos", ...) of a static
// backtest; ok is false when it is missing or malformed.
func QuantPeriodOf(s *hlpxyz.QuantStatic, name string) (QuantPeriod, bool) {
	var q QuantPeriod
	if s == nil || len(s.Periods[name]) == 0 {
		return q, false
	}
	if err := json.Unmarshal(s.Periods[name], &q); err != nil {
		return q, false
	}
	return q, true
}

func listStrats(ctx context.Context, env *Env) (*Result, error) {
	x, err := env.needHLPXYZ()
	if err != nil {
		return nil, err
	}
	rows := make([]StrategySummary, len(QuantIDs))
	errs := make([]error, len(QuantIDs))
	parallel(len(QuantIDs), func(i int) {
		id := QuantIDs[i]
		rows[i] = StrategySummary{ID: id}
		st, err := x.QuantBacktest(ctx, id)
		if err != nil {
			errs[i] = err
			return
		}
		rows[i].Name, rows[i].Status = st.Name, st.Status
		if q, ok := QuantPeriodOf(&st, "all"); ok {
			rows[i].CAGR, rows[i].Sharpe, rows[i].MaxDD, rows[i].Start, rows[i].OK = q.CAGR, q.Sharpe, q.MaxDD, q.Start, true
		}
	})
	meta := env.meta("hlp.xyz")
	failed := 0
	for _, e := range errs {
		if e != nil {
			failed++
		}
	}
	if failed == len(QuantIDs) {
		return nil, errs[0]
	}
	if failed > 0 {
		meta.Partial = true
		meta.Warnings = append(meta.Warnings, fmt.Sprintf("%d of %d backtests unavailable", failed, len(QuantIDs)))
	}
	return &Result{Card: CardStrategy, Title: "Quant strategies", Data: &StrategyData{Strategies: rows}, Meta: meta}, nil
}

// QuantIDs are the hlp.xyz quant strategies with a live book.
var QuantIDs = []string{"trend", "spxmeme", "bluechip", "dilution", "lotto", "memeshort"}

func runStrat(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*StrategyInput)
	id := strings.ToLower(strings.TrimSpace(p.ID))
	if id == "" {
		return listStrats(ctx, env)
	}
	// discover_vaults names these strategies by registry id (quant-<id>)
	id = strings.TrimPrefix(id, "quant-")
	known := false
	for _, q := range QuantIDs {
		if q == id {
			known = true
		}
	}
	if !known {
		return nil, &Error{Code: CodeUsage, Message: fmt.Sprintf("unknown strategy %q", strings.TrimSpace(p.ID)), Hint: "one of: " + strings.Join(QuantIDs, ", ") + " · `hlp strat` lists them"}
	}
	x, err := env.needHLPXYZ()
	if err != nil {
		return nil, err
	}
	var (
		live       hlpxyz.QuantLive
		static     hlpxyz.QuantStatic
		lerr, sErr error
		meta       = env.meta("hlp.xyz")
		data       = &StrategyData{}
		title      = id
	)
	parallel(2, func(i int) {
		if i == 0 {
			live, lerr = x.Quant(ctx, id)
		} else {
			static, sErr = x.QuantBacktest(ctx, id)
		}
	})
	if lerr != nil {
		return nil, lerr
	}
	data.Live = live
	if sErr == nil {
		data.Static = &static
		if static.Name != "" {
			title = static.Name
		}
	} else {
		meta.Partial = true
		meta.Warnings = append(meta.Warnings, "backtest unavailable: "+AsError(sErr).Message)
	}
	return &Result{Card: CardStrategy, Title: title, Data: data, Meta: meta}, nil
}
