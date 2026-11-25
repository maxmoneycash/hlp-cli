package hyperliquid

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// Reader is the domain-level read API every consumer (core, strategy,
// hlpxyz analytics) codes against. *Client implements it; tests use fakes
// built from fixtures. All addresses may be a user, sub-account, legacy
// vault, or HyperEVM vault contract (the contract IS its HyperCore account).
type Reader interface {
	// Markets returns every live market with context (perps on all dexes,
	// spot, optionally outcomes), from metaAndAssetCtxs per dex + spotMetaAndAssetCtxs.
	Markets(ctx context.Context) ([]domain.Market, domain.Meta, error)
	// Candles returns up to n most recent candles ending at end (0 = now),
	// oldest first. n is capped at 5000 by Hyperliquid.
	Candles(ctx context.Context, coin string, iv domain.Interval, n int, end int64) ([]domain.Candle, error)
	// Book returns an L2 snapshot (<= 20 levels per side).
	Book(ctx context.Context, coin string, o BookOpts) (domain.Book, error)
	Trades(ctx context.Context, coin string) ([]domain.Trade, error)
	Funding(ctx context.Context, coin string, start, end int64) ([]domain.FundingPoint, error)
	// Account aggregates every perp dex + spot + Earn supply. Equity is the
	// sum of all three (never just clearinghouseState.accountValue).
	Account(ctx context.Context, addr string) (domain.Account, error)
	Fills(ctx context.Context, addr string, start, end int64) ([]domain.Fill, error)
	// VaultList: stats-data vaults mapped to domain.VaultSummary (Sources ["stats-data"]).
	VaultList(ctx context.Context) ([]domain.VaultSummary, domain.Meta, error)
	// Vault: vaultDetails + Account + portfolio curves. Tearsheet left nil
	// (metrics package computes it).
	Vault(ctx context.Context, addr string) (domain.VaultDetail, error)
	Registry() Registry
}

var _ Reader = (*Client)(nil)

// MarketsCacheTTL dampens bursts of Markets calls (each costs ~240 weight).
const MarketsCacheTTL = 3 * time.Second

// MarketsDiskTTL is the cross-process micro-cache of Markets under
// CacheDir: agents run `markets` and `funding` back to back (PERFORMANCE #7).
const MarketsDiskTTL = 5 * time.Second

// DefaultCandles is used when Candles is called with n <= 0.
const DefaultCandles = 500

// MaxCandles is the deepest history Hyperliquid keeps per interval.
const MaxCandles = 5000

// wireCoin resolves friendly input ("btc", "SPY") to the wire coin when the
// registry is available. With no registry (offline), input passes through.
func (c *Client) wireCoin(coin string) (string, error) {
	r := c.registry()
	m, err := r.Resolve(coin)
	if err == nil {
		return m.Coin, nil
	}
	var uc *UnknownCoinError
	if errors.As(err, &uc) {
		return "", err
	}
	return coin, nil
}

// Markets returns every live perp (all dexes) and spot market with context.
// A failing HIP-3 dex marks Meta.Partial instead of failing the call.
func (c *Client) Markets(ctx context.Context) ([]domain.Market, domain.Meta, error) {
	c.mktMu.Lock()
	defer c.mktMu.Unlock()
	if c.mkts != nil && c.now().Sub(c.mktAt) < MarketsCacheTTL {
		return cloneMarkets(c.mkts), c.mktMet, nil
	}
	if out, meta, at, ok := c.loadMarketsDisk(); ok {
		c.mkts, c.mktAt, c.mktMet = out, at, meta
		return cloneMarkets(out), meta, nil
	}
	r := c.registry()
	_ = r.Refresh(ctx)
	data, _ := r.snapshot()
	dexs := data.DexNames()

	type perpRes struct {
		meta Meta
		ctxs []AssetCtx
		err  error
	}
	perps := make([]perpRes, len(dexs))
	var spot SpotMeta
	var spotCtxs []SpotAssetCtx
	var spotErr error
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, d := range dexs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			m, cx, err := c.MetaAndAssetCtxs(ctx, d)
			perps[i] = perpRes{m, cx, err}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		spot, spotCtxs, spotErr = c.SpotMetaAndAssetCtxs(ctx)
	}()
	wg.Wait()

	meta := domain.Meta{Source: "hyperliquid", AsOf: c.now(), Network: c.opts.Network}
	if perps[0].err != nil {
		return nil, meta, perps[0].err
	}
	var out []domain.Market
	for di, pr := range perps {
		if pr.err != nil {
			meta.Partial = true
			meta.Warnings = append(meta.Warnings, fmt.Sprintf("dex %q: %v", dexs[di], pr.err))
			continue
		}
		reg := BuildMarkets(RegistryData{Dexs: dexsFor(data, di), Metas: metasFor(di, pr.meta), Categories: data.Categories})
		for i, m := range reg {
			if m.Delisted || i >= len(pr.ctxs) {
				continue
			}
			x := pr.ctxs[i]
			m.Mark = x.MarkPx.Float()
			m.Mid = x.MidPx.Float()
			m.Oracle = x.OraclePx.Float()
			m.PrevDay = x.PrevDayPx.Float()
			m.Funding = x.Funding.Float()
			m.OpenInterest = x.OpenInterest.Float()
			m.DayNtlVlm = x.DayNtlVlm.Float()
			m.Derive()
			out = append(out, m)
		}
	}
	if spotErr != nil {
		meta.Partial = true
		meta.Warnings = append(meta.Warnings, "spot: "+spotErr.Error())
	} else {
		sm := BuildMarkets(RegistryData{Spot: spot})
		byCoin := map[string]SpotAssetCtx{}
		for _, x := range spotCtxs {
			byCoin[x.Coin] = x
		}
		for _, m := range sm {
			x, ok := byCoin[m.Coin]
			if !ok {
				continue
			}
			m.Mark = x.MarkPx.Float()
			m.Mid = x.MidPx.Float()
			m.PrevDay = x.PrevDayPx.Float()
			m.DayNtlVlm = x.DayNtlVlm.Float()
			m.Derive()
			out = append(out, m)
		}
	}
	c.mkts, c.mktAt, c.mktMet = out, c.now(), meta
	c.saveMarketsDisk(out, meta) // partial too: its warnings travel with it
	return cloneMarkets(out), meta, nil
}

// marketsDisk is the Markets micro-cache file.
type marketsDisk struct {
	At      time.Time       `json:"at"`
	Meta    domain.Meta     `json:"meta"`
	Markets []domain.Market `json:"markets"`
}

func (c *Client) marketsPath() string {
	if c.opts.CacheDir == "" {
		return ""
	}
	return filepath.Join(c.opts.CacheDir, fmt.Sprintf("markets-%s.json.gz", c.opts.Network))
}

// loadMarketsDisk returns the cached Markets when younger than MarketsDiskTTL.
func (c *Client) loadMarketsDisk() ([]domain.Market, domain.Meta, time.Time, bool) {
	p := c.marketsPath()
	if p == "" {
		return nil, domain.Meta{}, time.Time{}, false
	}
	fi, err := os.Stat(p)
	if err != nil || c.now().Sub(fi.ModTime()) > MarketsDiskTTL+time.Second {
		return nil, domain.Meta{}, time.Time{}, false
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, domain.Meta{}, time.Time{}, false
	}
	defer f.Close()
	gz, err := gzip.NewReader(io.LimitReader(f, maxBody))
	if err != nil {
		return nil, domain.Meta{}, time.Time{}, false
	}
	defer gz.Close()
	var d marketsDisk
	if json.NewDecoder(gz).Decode(&d) != nil || len(d.Markets) == 0 {
		return nil, domain.Meta{}, time.Time{}, false
	}
	age := c.now().Sub(d.At)
	if age < 0 || age >= MarketsDiskTTL {
		return nil, domain.Meta{}, time.Time{}, false
	}
	return d.Markets, d.Meta, d.At, true
}

func (c *Client) saveMarketsDisk(ms []domain.Market, meta domain.Meta) {
	if p := c.marketsPath(); p != "" {
		_ = writeGzipJSON(p, marketsDisk{At: c.now(), Meta: meta, Markets: ms})
	}
}

// dexsFor returns a Dexs slice where index di holds data.Dexs[di], so
// BuildMarkets computes the right HIP-3 asset ids for one dex.
func dexsFor(data RegistryData, di int) []*PerpDex {
	out := make([]*PerpDex, di+1)
	if di < len(data.Dexs) {
		out[di] = data.Dexs[di]
	}
	return out
}

// metasFor places meta at index di (others empty).
func metasFor(di int, m Meta) []Meta {
	out := make([]Meta, di+1)
	out[di] = m
	return out
}

func cloneMarkets(in []domain.Market) []domain.Market {
	out := make([]domain.Market, len(in))
	copy(out, in)
	return out
}

// Candles returns up to n most recent candles ending at end (0 = now),
// oldest first, deduplicated by open time.
func (c *Client) Candles(ctx context.Context, coin string, iv domain.Interval, n int, end int64) ([]domain.Candle, error) {
	if !iv.Valid() {
		return nil, &APIError{Code: "BAD_REQUEST", Body: fmt.Sprintf("invalid interval %q", iv)}
	}
	coin, err := c.wireCoin(coin)
	if err != nil {
		return nil, err
	}
	if n <= 0 {
		n = DefaultCandles
	}
	if n > MaxCandles {
		n = MaxCandles
	}
	endT := end
	if endT <= 0 {
		endT = c.nowMs()
	}
	step := iv.Duration().Milliseconds()
	start := endT - int64(n+1)*step
	if start < 0 {
		start = 0
	}
	wc, err := c.CandleSnapshot(ctx, coin, iv, start, end)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Candle, 0, len(wc))
	for _, w := range wc {
		out = append(out, ToCandle(w))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].T < out[j].T })
	dedup := out[:0]
	for i, k := range out {
		if i > 0 && k.T == dedup[len(dedup)-1].T {
			dedup[len(dedup)-1] = k
			continue
		}
		dedup = append(dedup, k)
	}
	if len(dedup) > n {
		dedup = dedup[len(dedup)-n:]
	}
	return dedup, nil
}

// Book returns an L2 snapshot.
func (c *Client) Book(ctx context.Context, coin string, o BookOpts) (domain.Book, error) {
	coin, err := c.wireCoin(coin)
	if err != nil {
		return domain.Book{}, err
	}
	b, err := c.L2Book(ctx, coin, o)
	if err != nil {
		return domain.Book{}, err
	}
	return ToBook(b), nil
}

// Trades returns the 10 most recent public trades.
func (c *Client) Trades(ctx context.Context, coin string) ([]domain.Trade, error) {
	coin, err := c.wireCoin(coin)
	if err != nil {
		return nil, err
	}
	wt, err := c.RecentTrades(ctx, coin)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Trade, 0, len(wt))
	for _, t := range wt {
		out = append(out, ToTrade(t))
	}
	return out, nil
}

// DefaultFundingLookback applies when Funding is called with start 0.
const DefaultFundingLookback = 7 * 24 * time.Hour

// Funding returns hourly funding prints in [start, end] (end 0 = now,
// start 0 = end minus 7 days), oldest first.
func (c *Client) Funding(ctx context.Context, coin string, start, end int64) ([]domain.FundingPoint, error) {
	coin, err := c.wireCoin(coin)
	if err != nil {
		return nil, err
	}
	if start <= 0 {
		e := end
		if e <= 0 {
			e = c.nowMs()
		}
		start = e - DefaultFundingLookback.Milliseconds()
	}
	wf, err := c.FundingHistory(ctx, coin, start, end)
	if err != nil {
		return nil, err
	}
	out := make([]domain.FundingPoint, 0, len(wf))
	for _, f := range wf {
		out = append(out, ToFunding(f))
	}
	return out, nil
}

// Account aggregates every perp dex + spot + Earn supply for any address.
// The native dex must answer; a failing HIP-3 dex or Earn read is skipped
// and reported through Account.Partial and Account.Warnings.
func (c *Client) Account(ctx context.Context, addr string) (domain.Account, error) {
	addr, err := NormalizeAddress(addr)
	if err != nil {
		return domain.Account{}, err
	}
	r := c.registry()
	_ = r.Refresh(ctx)
	data, spotUSDC := r.snapshot()
	dexs := data.DexNames()

	states := make([]DexState, len(dexs))
	stateErr := make([]error, len(dexs))
	var (
		balances []SpotBalance
		earn     BorrowLendUserState
		mids     map[string]Dec
		spotErr  error
		earnErr  error
		wg       sync.WaitGroup
	)
	sem := make(chan struct{}, 6)
	for i, d := range dexs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s, err := c.ClearinghouseState(ctx, addr, d)
			states[i], stateErr[i] = DexState{Dex: d, State: s}, err
		}()
	}
	wg.Add(3)
	go func() { defer wg.Done(); balances, spotErr = c.SpotClearinghouseState(ctx, addr) }()
	go func() { defer wg.Done(); earn, earnErr = c.BorrowLendUserState(ctx, addr) }()
	go func() { defer wg.Done(); mids, _ = c.AllMids(ctx, "") }()
	wg.Wait()
	if stateErr[0] != nil {
		return domain.Account{}, stateErr[0]
	}
	if spotErr != nil {
		return domain.Account{}, spotErr
	}
	var ok []DexState
	for i, s := range states {
		if stateErr[i] == nil {
			ok = append(ok, s)
		}
	}
	acct := PerpAccount(addr, ok)
	for i, e := range stateErr {
		if e != nil {
			acct.Partial = true
			acct.Warnings = append(acct.Warnings, fmt.Sprintf("dex %s unavailable: equity excludes it", dexs[i]))
		}
	}

	price := func(token int) float64 {
		if token == 0 {
			return 1
		}
		if coin, ok := spotUSDC[token]; ok {
			return mids[coin].Float()
		}
		return 0
	}
	for _, b := range balances {
		total := b.Total.Float()
		if total == 0 {
			continue
		}
		px := price(b.Token)
		usd := total * px
		if px == 0 {
			usd = b.EntryNtl.Float() // best effort for tokens without a USDC pair
		}
		acct.SpotUSD += usd
		acct.Balances = append(acct.Balances, domain.Balance{Coin: b.Coin, Total: total, Hold: b.Hold.Float(), USD: usd})
	}
	if earnErr == nil {
		acct.EarnUSD = EarnUSD(earn, price)
	} else if ctx.Err() == nil {
		acct.Partial = true
		acct.Warnings = append(acct.Warnings, "Earn balance unavailable: equity excludes it")
	}
	acct.Equity = acct.PerpEquity + acct.SpotUSD + acct.EarnUSD

	// Open orders: native dex plus every dex where the account has value.
	var orderDexs []string
	for _, s := range ok {
		if s.Dex == "" || s.State.MarginSummary.AccountValue.Float() != 0 || len(s.State.AssetPositions) > 0 {
			orderDexs = append(orderDexs, s.Dex)
		}
	}
	orders := make([][]FrontendOrder, len(orderDexs))
	for i, d := range orderDexs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			orders[i], _ = c.FrontendOpenOrders(ctx, addr, d)
		}()
	}
	wg.Wait()
	for _, os := range orders {
		for _, o := range os {
			acct.OpenOrders = append(acct.OpenOrders, ToOrder(o))
		}
	}
	if acct.Time == 0 {
		acct.Time = c.nowMs()
	}
	return acct, nil
}

// EarnUSD values net Earn supply (supply - borrow) per token with price.
func EarnUSD(s BorrowLendUserState, price func(token int) float64) float64 {
	var total float64
	for _, t := range s.TokenToState {
		net := t.State.Supply.Value.Float() - t.State.Borrow.Value.Float()
		if net == 0 {
			continue
		}
		total += net * price(t.Token)
	}
	return total
}

// Fills returns an address's fills. start=end=0 returns the 2000 most
// recent; otherwise [start, end] is paged (10k most recent reachable).
// Oldest first.
func (c *Client) Fills(ctx context.Context, addr string, start, end int64) ([]domain.Fill, error) {
	addr, err := NormalizeAddress(addr)
	if err != nil {
		return nil, err
	}
	var wf []WireFill
	if start == 0 && end == 0 {
		wf, err = c.UserFills(ctx, addr)
	} else {
		wf, err = c.UserFillsByTime(ctx, addr, start, end)
	}
	if err != nil {
		return nil, err
	}
	out := make([]domain.Fill, 0, len(wf))
	for _, f := range wf {
		out = append(out, ToFill(f))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out, nil
}

// statsWindows maps stats-data pnl windows to domain windows.
var statsWindows = map[string]domain.Window{"day": domain.W1D, "week": domain.W1W, "month": domain.W1M}

// VaultList maps stats-data vaults to screener rows (open and closed; the
// Closed flag lets callers filter). Returns per window are approximations
// from the cumulative PnL series: dPnL / (TVL - dPnL), which ignores
// deposits and withdrawals inside the window; curated hlp.xyz v6 numbers
// should win when present. AllowDeposits is assumed true for open vaults.
func (c *Client) VaultList(ctx context.Context) ([]domain.VaultSummary, domain.Meta, error) {
	sv, meta, err := c.StatsVaults(ctx)
	if err != nil {
		return nil, meta, err
	}
	out := make([]domain.VaultSummary, 0, len(sv))
	for _, v := range sv {
		out = append(out, StatsToSummary(v, meta.AsOf, meta.Stale))
	}
	return out, meta, nil
}

// StatsToSummary maps one stats-data row.
func StatsToSummary(v StatsVault, asOf time.Time, stale bool) domain.VaultSummary {
	s := v.Summary
	tvl := s.TVL.Float()
	created := time.UnixMilli(s.CreateTimeMillis).UTC()
	sum := domain.VaultSummary{
		Address:       strings.ToLower(s.VaultAddress),
		Name:          domain.CleanText(s.Name),
		Leader:        strings.ToLower(s.Leader),
		Kind:          domain.VaultHyperCore,
		Rel:           s.Relationship.Type,
		TVL:           tvl,
		TVLKnown:      true,
		APR:           v.APR,
		Returns:       map[domain.Window]float64{},
		Closed:        s.IsClosed,
		AllowDeposits: !s.IsClosed,
		Sources:       []string{"stats-data"},
		UpdatedAt:     asOf,
		Stale:         stale,
	}
	if sum.Rel == "" {
		sum.Rel = "normal"
	}
	sum.ReturnsModel = domain.ReturnsPnLOverTVL
	if sum.Rel == "parent" || sum.Rel == "child" {
		sum.CapacityUSD = Capacity(tvl, 0, true)
	}
	if s.CreateTimeMillis > 0 {
		sum.CreatedAt = created
		if !asOf.IsZero() {
			sum.AgeDays = int(asOf.Sub(created).Hours() / 24)
		}
	}
	for _, p := range v.PNLs {
		name, _ := p[0].(string)
		vals := anyFloats(p[1])
		if len(vals) == 0 {
			continue
		}
		if w, ok := statsWindows[name]; ok {
			d := vals[len(vals)-1] - vals[0]
			if base := tvl - d; base > 0 {
				sum.Returns[w] = d / base
			}
		}
		if name == "allTime" {
			sum.Spark = spreadSeries(vals, s.CreateTimeMillis, asOf.UnixMilli())
		}
	}
	if len(sum.Returns) == 0 {
		sum.Returns = nil
	}
	return sum
}

func anyFloats(v any) []float64 {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]float64, 0, len(arr))
	for _, x := range arr {
		if f, ok := toFloat(x); ok {
			out = append(out, f)
		}
	}
	return out
}

// spreadSeries assigns evenly spaced timestamps in [from, to] to vals
// (stats-data pnls carry no timestamps).
func spreadSeries(vals []float64, from, to int64) domain.Series {
	out := make(domain.Series, len(vals))
	if to <= from || from <= 0 {
		for i, v := range vals {
			out[i] = domain.Point{T: int64(i), V: v}
		}
		return out
	}
	step := float64(to-from) / float64(max(1, len(vals)-1))
	for i, v := range vals {
		out[i] = domain.Point{T: from + int64(float64(i)*step), V: v}
	}
	return out
}

// TWRCurve builds a flow-neutral share-price curve (starts at 1.0) from an
// account-value series and the cumulative-PnL series at the same
// timestamps. Each interval's return is modified Dietz
// (domain.ModifiedDietz): the net flow F = ΔA - ΔP is assumed to arrive
// mid-period, r_i = ΔP / (A_{i-1} + F/2), and an interval whose flow is at
// least half its starting equity is unmeasurable and counts as zero. This
// replaces simple Dietz (ΔP / A_{i-1}), which
// over-credited intervals with large deposits.
func TWRCurve(av, pnl domain.Series) domain.Series {
	n := min(len(av), len(pnl))
	if n == 0 {
		return nil
	}
	out := make(domain.Series, 0, n)
	nav := 1.0
	out = append(out, domain.Point{T: av[0].T, V: nav})
	for i := 1; i < n; i++ {
		if r, ok := domain.ModifiedDietz(av[i-1].V, av[i].V, pnl[i].V-pnl[i-1].V); ok {
			nav *= 1 + r
		}
		out = append(out, domain.Point{T: av[i].T, V: nav})
	}
	return out
}

// maxDrawdown returns the most negative peak-to-trough fraction (<= 0).
func maxDrawdown(s domain.Series) float64 {
	peak, dd := math.Inf(-1), 0.0
	for _, p := range s {
		peak = math.Max(peak, p.V)
		if peak > 0 {
			dd = math.Min(dd, p.V/peak-1)
		}
	}
	return dd
}

// portfolioWindows maps vaultDetails portfolio windows to domain windows.
var portfolioWindows = map[string]domain.Window{"day": domain.W1D, "week": domain.W1W, "month": domain.W1M, "allTime": domain.WAll}

// Lockups (DOCS): HLP 4 days, user vaults 1 day.
const (
	HLPLockupDays  = 4
	UserLockupDays = 1
)

// Vault returns vaultDetails + Account + portfolio curves for a vault
// address. NAV is our flow-neutral TWR from portfolio.allTime
// (SPModel hl-portfolio). ErrNotFound if the address is not a vault.
func (c *Client) Vault(ctx context.Context, addr string) (domain.VaultDetail, error) {
	addr, err := NormalizeAddress(addr)
	if err != nil {
		return domain.VaultDetail{}, err
	}
	var (
		vd      VaultDetails
		vdErr   error
		acct    domain.Account
		acctErr error
		wg      sync.WaitGroup
	)
	wg.Add(2)
	go func() { defer wg.Done(); vd, vdErr = c.VaultDetails(ctx, addr, "") }()
	go func() { defer wg.Done(); acct, acctErr = c.Account(ctx, addr) }()
	wg.Wait()
	if vdErr != nil {
		return domain.VaultDetail{}, vdErr
	}
	det := VaultDetailFrom(vd, c.now())
	det.Meta.Network = c.opts.Network
	if acctErr == nil {
		det.Account = &acct
		if !det.Summary.TVLKnown {
			det.Summary.TVL = acct.Equity
			det.Summary.TVLKnown = true
		}
		exp := domain.Exposure{T: acct.Time, Equity: acct.Equity, ByCoin: map[string]float64{}}
		for _, p := range acct.Positions {
			v := p.Value
			if p.Size < 0 {
				v = -v
			}
			exp.ByCoin[p.Coin] += v
		}
		det.Composition = []domain.Exposure{exp}
	} else {
		det.Meta.Partial = true
		det.Meta.Warnings = append(det.Meta.Warnings, "account: "+acctErr.Error())
	}
	det.CapacityUSD = Capacity(det.Summary.TVL, det.Summary.LeaderFraction, det.Summary.Rel == "parent" || det.Summary.Rel == "child")
	if sv, ok := c.statsLookup(addr); ok && sv.Summary.CreateTimeMillis > 0 {
		det.Summary.CreatedAt = time.UnixMilli(sv.Summary.CreateTimeMillis).UTC()
		det.Summary.AgeDays = int(c.now().Sub(det.Summary.CreatedAt).Hours() / 24)
		det.Summary.Sources = append(det.Summary.Sources, "stats-data")
	}
	return det, nil
}

// VaultDetailFrom maps a vaultDetails response (no Account, no capacity
// beyond the leader rule) into a domain.VaultDetail.
func VaultDetailFrom(vd VaultDetails, now time.Time) domain.VaultDetail {
	rel := vd.Relationship.Type
	if rel == "" {
		rel = "normal"
	}
	sum := domain.VaultSummary{
		Address:        strings.ToLower(vd.VaultAddress),
		Name:           domain.CleanText(vd.Name),
		Leader:         strings.ToLower(vd.Leader),
		Kind:           domain.VaultHyperCore,
		Rel:            rel,
		APR:            vd.APR,
		LeaderFraction: vd.LeaderFraction,
		ProfitShare:    vd.LeaderCommission,
		Followers:      len(vd.Followers),
		AllowDeposits:  vd.AllowDeposits && !vd.IsClosed,
		Closed:         vd.IsClosed,
		SPModel:        domain.SPHL,
		Sources:        []string{"hyperliquid"},
		UpdatedAt:      now.UTC(), // AGENT-UX #13: never a local offset
		Returns:        map[domain.Window]float64{},
	}
	det := domain.VaultDetail{
		Description: domain.CleanText(vd.Description),
		LockupDays:  UserLockupDays,
		SPModel:     domain.SPHL,
		MaxWithdraw: vd.MaxWithdrawable,
		Meta:        domain.Meta{Source: "hyperliquid", AsOf: now.UTC()},
	}
	if rel == "parent" {
		det.LockupDays = HLPLockupDays
	}
	if vd.Relationship.Data != nil {
		for _, a := range vd.Relationship.Data.ChildAddresses {
			det.Children = append(det.Children, strings.ToLower(a))
		}
	}
	for k, w := range vd.Portfolio {
		dw, ok := portfolioWindows[k]
		if !ok {
			continue
		}
		curve := TWRCurve(w.AccountValues(), w.PnLs())
		if len(curve) > 1 {
			sum.Returns[dw] = curve.Last() - 1
		}
	}
	if all, ok := vd.Portfolio["allTime"]; ok {
		det.Equity = all.AccountValues()
		det.PnL = all.PnLs()
		det.NAV = TWRCurve(det.Equity, det.PnL)
		sum.TVL = det.Equity.Last()
		sum.TVLKnown = len(det.Equity) > 0
		sum.MaxDD = maxDrawdown(det.NAV)
		sum.Spark = det.NAV
		det.SharePrice = det.NAV.Last()
	}
	if len(sum.Returns) == 0 {
		sum.Returns = nil
	}
	for _, f := range vd.Followers {
		det.Followers = append(det.Followers, domain.Follower{
			User:          f.User,
			Equity:        f.VaultEquity.Float(),
			PnL:           f.Pnl.Float(),
			AllTimePnL:    f.AllTimePnl.Float(),
			DaysFollowing: f.DaysFollowing,
			EntryTime:     f.VaultEntryTime,
			LockupUntil:   f.LockupUntil,
		})
	}
	det.Summary = sum
	det.CapacityUSD = Capacity(sum.TVL, sum.LeaderFraction, rel == "parent" || rel == "child")
	return det
}

// MarketCtx returns one market with its live context (mark, prev-day,
// funding, open interest, volume). It reads only that market's dex
// (metaAndAssetCtxs, weight 20) or spot context, so it is much cheaper than
// Markets for a single chart header.
func (c *Client) MarketCtx(ctx context.Context, coin string) (domain.Market, error) {
	m, err := c.registry().Resolve(coin)
	if err != nil {
		return domain.Market{}, err
	}
	if m.Kind == domain.Spot {
		_, cx, err := c.SpotMetaAndAssetCtxs(ctx)
		if err != nil {
			return m, err
		}
		for _, x := range cx {
			if x.Coin == m.Coin {
				m.Mark, m.Mid = x.MarkPx.Float(), x.MidPx.Float()
				m.PrevDay, m.DayNtlVlm = x.PrevDayPx.Float(), x.DayNtlVlm.Float()
				m.Derive()
				return m, nil
			}
		}
		return m, nil
	}
	meta, cx, err := c.MetaAndAssetCtxs(ctx, m.Dex)
	if err != nil {
		return m, err
	}
	for i, a := range meta.Universe {
		if a.Name != m.Coin || i >= len(cx) {
			continue
		}
		x := cx[i]
		m.Mark, m.Mid, m.Oracle = x.MarkPx.Float(), x.MidPx.Float(), x.OraclePx.Float()
		m.PrevDay, m.Funding = x.PrevDayPx.Float(), x.Funding.Float()
		m.OpenInterest, m.DayNtlVlm = x.OpenInterest.Float(), x.DayNtlVlm.Float()
		m.Derive()
		break
	}
	return m, nil
}
