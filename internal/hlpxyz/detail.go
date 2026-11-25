package hlpxyz

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// DetailParts are the hlp.xyz responses ToDetail merges. Every part is
// optional; nil parts are simply absent from the result.
type DetailParts struct {
	Address     string       // required (any case)
	Live        *VaultLive   // /api/vault/<a>/live: name, leader, TVL, live share price, lockup
	NAV         *NAVCurve    // /api/user?action=v6vault: the canonical v6 share-price curve
	Composition *Composition // /api/vault/<a>/composition: exposure by coin
	Detail      *VaultDetail // /api/vault/<a>?enc=t1: fills, flows, equity snapshots, leverage
	Status      *V6Status    // v6status row: window returns, MaxDD, Sharpe, accuracy
	Now         time.Time    // for staleness; zero = time.Now()
}

// ToDetail merges VaultLive + NAVCurve + Composition + VaultDetail (+ the
// v6status row) into a domain.VaultDetail.
//
// What it fills (core adds Hyperliquid data on top: followers, positions,
// leader fraction, capacity, tearsheet):
//   - Summary: name, leader, profit share, TVL (live, else last snapshot),
//     deposits/closed flags, returns/MaxDD/Sharpe (v6status, else the v6
//     curve's window returns), peak gross leverage, age, Curated when hlp.xyz
//     tracks the vault (detail or v6 present), Sources ["hlp.xyz"].
//   - NAV: the v6 share-price curve as served (inception = 1.0, so its first
//     sample can be above 1); SharePrice/SPModel = v6 navNow (SPv6), or the
//     /live price (SPLive) when there is no v6 curve.
//   - Equity/PnL: account value and cumulative PnL from the equity snapshots.
//   - Flows: the ledger (all depositors), with commission on withdrawals.
//   - Fills: DecodeT1 of the columnar trades.
//   - Composition: one domain.Exposure per bucket.
//   - CapacityUSD: -1 for HLP; otherwise 0 here because hlp.xyz does not
//     expose the leader fraction (core computes it with metrics.Capacity).
//   - Meta: Source "hlp.xyz", AsOf = newest part, Stale when the detail's
//     lastUpdated is older than StaleAfter, Warnings for missing parts.
func ToDetail(p DetailParts) (domain.VaultDetail, error) {
	addr, err := NormalizeAddress(p.Address)
	if err != nil {
		return domain.VaultDetail{}, err
	}
	now := p.Now
	if now.IsZero() {
		now = time.Now()
	}
	d := domain.VaultDetail{
		Summary: domain.VaultSummary{
			Address: addr,
			Kind:    domain.VaultHyperCore,
			Sources: []string{"hlp.xyz"},
		},
		Meta:      domain.Meta{Source: "hlp.xyz"},
		Analytics: &domain.VaultAnalytics{},
		Freshness: make(map[string]domain.VaultSourceFreshness),
	}
	stamp := func(key, source string, at int64, cached, stale bool) {
		d.Freshness[key] = domain.VaultSourceFreshness{Source: source, AsOf: at, ObservedAt: now.UnixMilli(), Cached: cached,
			Stale: stale || at > 0 && now.Sub(time.UnixMilli(at)) > StaleAfter}
	}
	s := &d.Summary
	var asOf int64
	var updated time.Time // detail meta.lastUpdated
	touch := func(ms int64) { asOf = max(asOf, ms) }
	if addr == HLPAddress {
		s.Rel = "parent"
		d.CapacityUSD = -1
	}

	if v := p.Detail; v != nil {
		stamp("history", "hlp.xyz history", v.Meta.LastUpdated, v.Meta.Cached, v.Meta.Stale)
		d.Analytics.History = &domain.VaultHistoryAnalytics{UpdatedAt: v.Meta.LastUpdated, DataStart: v.Meta.DataStart, DataEnd: v.Meta.DataEnd,
			Light: v.Light, Counts: map[string]int{"trades": v.Meta.TradesCount, "funding": v.Meta.FundingCount, "ledger": v.Meta.LedgerCount, "snapshots": v.Meta.SnapshotCount}}
		for _, f := range v.Funding {
			d.Analytics.Funding = append(d.Analytics.Funding, domain.VaultFunding{T: f.TS, Coin: domain.CleanText(f.Coin), Size: f.Sz,
				Side: domain.CleanText(f.Side), Payment: f.Payment, Rate: f.Rate})
		}
		sort.SliceStable(d.Analytics.Funding, func(i, j int) bool { return d.Analytics.Funding[i].T < d.Analytics.Funding[j].T })
		for _, l := range v.Leverage {
			d.Analytics.Leverage = append(d.Analytics.Leverage, domain.VaultLeverage{T: l.TS, Gross: l.Leverage, Net: l.NetLeverage,
				Equity: l.AccountValue, GrossNotional: l.TotalNtlPos, NetNotional: l.NetNtlPos, MarginUsed: l.TotalMarginUsed})
		}
		sort.SliceStable(d.Analytics.Leverage, func(i, j int) bool { return d.Analytics.Leverage[i].T < d.Analytics.Leverage[j].T })
		if n := len(d.Analytics.Leverage); n > 0 {
			stamp("leverage", "hlp.xyz leverage history", d.Analytics.Leverage[n-1].T, v.Meta.Cached, false)
		}
		s.Name = strings.TrimSpace(domain.CleanText(v.VaultInfo.Name))
		s.Leader = strings.ToLower(domain.CleanText(v.VaultInfo.Leader))
		s.ProfitShare = v.VaultInfo.ProfitShare
		d.Description = strings.TrimSpace(domain.CleanText(v.VaultInfo.Desc))
		s.Closed = v.Meta.IsClosed
		s.AllowDeposits = v.Meta.AllowDeposits
		s.Curated = true
		if v.Meta.DataStart > 0 {
			s.CreatedAt = time.UnixMilli(v.Meta.DataStart).UTC()
		}
		for _, sn := range v.Portfolio.Snapshots {
			d.Equity = append(d.Equity, domain.Point{T: sn.TS, V: sn.AV})
			d.PnL = append(d.PnL, domain.Point{T: sn.TS, V: sn.PnL})
		}
		sortSeries(d.Equity)
		sortSeries(d.PnL)
		if len(d.Equity) > 0 {
			s.TVL = d.Equity[len(d.Equity)-1].V
			s.TVLKnown = true
			touch(d.Equity[len(d.Equity)-1].T)
		}
		for _, r := range v.Deps {
			t := r.TS
			if t == 0 {
				continue
			}
			d.Flows = append(d.Flows, domain.Flow{
				Time: t, User: strings.ToLower(r.User), USD: r.AccountValueChange,
				Kind: r.Type, Commission: r.Commission,
			})
		}
		sort.SliceStable(d.Flows, func(i, j int) bool { return d.Flows[i].Time < d.Flows[j].Time })
		if v.Trades != nil {
			fills, err := DecodeT1(*v.Trades)
			if err != nil {
				return domain.VaultDetail{}, err
			}
			d.Fills = fills
		}
		for _, l := range v.Leverage {
			s.MaxGrossLev = max(s.MaxGrossLev, l.Leverage)
		}
		touch(v.Meta.LastUpdated)
		if v.Meta.LastUpdated > 0 {
			updated = time.UnixMilli(v.Meta.LastUpdated).UTC()
			if age := now.Sub(updated); age > StaleAfter {
				d.Meta.Stale = true
				d.Meta.Warnings = append(d.Meta.Warnings, StaleWarning(updated, age))
			}
		}
	} else {
		d.Meta.Warnings = append(d.Meta.Warnings, "no hlp.xyz vault history (fills, flows)")
	}

	if l := p.Live; l != nil {
		stamp("live", "hlp.xyz live ledger", l.CachedAt, l.Cached, false)
		d.Analytics.Live = &domain.VaultLiveAnalytics{SharePrice: l.SharePrice, TotalEquity: l.TotalEquity, CachedAt: l.CachedAt,
			Cached: l.Cached, DepositCapacity: l.DepositCapacity}
		for _, pt := range l.RecentAV {
			d.Analytics.Live.RecentAV = append(d.Analytics.Live.RecentAV, domain.VaultValuePoint{T: pt.TS, Equity: pt.AV, PnL: pt.PnL})
		}
		sort.SliceStable(d.Analytics.Live.RecentAV, func(i, j int) bool { return d.Analytics.Live.RecentAV[i].T < d.Analytics.Live.RecentAV[j].T })
		if name := strings.TrimSpace(domain.CleanText(l.Name)); name != "" {
			s.Name = name
		}
		if l.Leader != "" {
			s.Leader = strings.ToLower(domain.CleanText(l.Leader))
		}
		s.ProfitShare = l.ProfitShare
		if l.AllowDeposits != nil {
			s.AllowDeposits = *l.AllowDeposits
		}
		if l.hasTotalEquity() {
			s.TVL = l.TotalEquity
			s.TVLKnown = true
		}
		d.LockupDays = l.LockupDays
		d.SharePrice, d.SPModel = l.SharePrice, domain.SPLive
		s.SPModel = domain.SPLive
		touch(l.CachedAt)
	}

	if n := p.NAV; n != nil && len(n.Curve) > 0 {
		navAsOf := n.BuiltAt
		if navAsOf == 0 {
			for _, pt := range n.Curve {
				navAsOf = max(navAsOf, pt.TS)
			}
		}
		stamp("nav", "hlp.xyz v6 NAV", navAsOf, false, false)
		d.Analytics.NAV = &domain.VaultNAVAnalytics{NavNow: n.NavNow, TotalReturn: n.TotalReturnPct / 100, BuiltAt: n.BuiltAt,
			AnchorCount: n.AnchorCount, AccuracyPct: n.AccuracyPct, SampleSize: n.SampleSize, WindowAccuracy: n.WindowAccuracy,
			Source: domain.CleanText(n.Source), Pipeline: domain.CleanText(n.Pipeline), ForwardPoints: n.ForwardPoints, LastRosterTs: n.LastRosterTs}
		s.MetricsAsOf = n.BuiltAt
		touch(n.BuiltAt)
		d.NAV = make(domain.Series, 0, len(n.Curve))
		for _, pt := range n.Curve {
			if pt.NAV >= 0 {
				d.NAV = append(d.NAV, domain.Point{T: pt.TS, V: pt.NAV})
			}
		}
		sortSeries(d.NAV)
		if n.hasNavNow() && n.NavNow >= 0 {
			d.SharePrice, d.SPModel = n.NavNow, domain.SPv6
			s.SPModel = domain.SPv6
		} else if len(d.NAV) > 0 {
			d.SharePrice, d.SPModel = d.NAV[len(d.NAV)-1].V, domain.SPv6
			s.SPModel = domain.SPv6
		}
		s.AccuracyPct = n.AccuracyPct
		if len(n.WindowReturns) > 0 {
			s.Returns = map[domain.Window]float64{}
			for k, v := range n.WindowReturns {
				switch k {
				case "day":
					s.Returns[domain.W1D] = v
				case "week":
					s.Returns[domain.W1W] = v
				case "month":
					s.Returns[domain.W1M] = v
				case "allTime", "all":
					s.Returns[domain.WAll] = v
				}
			}
		}
		if len(d.NAV) > 0 {
			touch(d.NAV[len(d.NAV)-1].T)
			if s.CreatedAt.IsZero() {
				s.CreatedAt = time.UnixMilli(d.NAV[0].T).UTC()
			}
		}
		s.Curated = true
	} else {
		d.Meta.Warnings = append(d.Meta.Warnings, "no hlp.xyz v6 NAV curve")
	}

	if st := p.Status; st != nil {
		stamp("metrics", "hlp.xyz v6 metrics", st.ComputedAt, false, false)
		s.MetricsAsOf = st.ComputedAt
		if r := V6Returns(*st); len(r) > 0 {
			s.Returns = r
		}
		applyV6Risk(s, *st)
		if st.AccuracyPct > 0 {
			s.AccuracyPct = st.AccuracyPct
		}
		s.SPModel = domain.SPv6
		s.Curated = true
		touch(st.ComputedAt)
	}

	if c := p.Composition; c != nil {
		stamp("composition", "hlp.xyz composition", c.UpdatedAt, c.Cached, false)
		coins := make([]string, len(c.Coins))
		for i, coin := range c.Coins {
			coins[i] = domain.CleanText(coin)
		}
		d.Analytics.Composition = &domain.VaultCompositionAnalytics{UpdatedAt: c.UpdatedAt, Range: domain.CleanText(c.Range), BucketMs: c.BucketMs, Coins: coins}
		for _, pt := range c.Points {
			by := make(map[string]float64, len(pt.V))
			for k, v := range pt.V {
				by[domain.CleanText(k)] += v
			}
			d.Composition = append(d.Composition, domain.Exposure{T: pt.TS, Equity: pt.AV, ByCoin: by})
		}
		sort.SliceStable(d.Composition, func(i, j int) bool { return d.Composition[i].T < d.Composition[j].T })
		touch(c.UpdatedAt)
	}

	if !s.CreatedAt.IsZero() {
		s.AgeDays = int(now.Sub(s.CreatedAt) / (24 * time.Hour))
	}
	if asOf > 0 {
		d.Meta.AsOf = time.UnixMilli(asOf).UTC()
		s.UpdatedAt = d.Meta.AsOf
	}
	if !updated.IsZero() {
		// the vault history's own stamp, not the newest part: staleness
		// text must name the date the numbers stopped moving
		s.UpdatedAt = updated
	}
	s.Stale = d.Meta.Stale
	return d, nil
}

// StaleWarning explains a stale hlp.xyz figure: when it was last updated
// and how long ago, e.g. "hlp.xyz vault history last updated Nov 12
// (3d ago, over 48h)".
func StaleWarning(updated time.Time, age time.Duration) string {
	return fmt.Sprintf("hlp.xyz vault history last updated %s (%s ago, over %dh)",
		ShortDate(updated, updated.Add(age)), HumanAge(age), int(StaleAfter.Hours()))
}

// ShortDate is a UTC date for humans: "Apr 28", with the year when it is
// not now's year ("Apr 28 2025").
func ShortDate(t, now time.Time) string {
	t = t.UTC()
	if t.Year() != now.UTC().Year() {
		return t.Format("Jan 2 2006")
	}
	return t.Format("Jan 2")
}

// HumanAge renders a duration as "45m", "7h" or "3d".
func HumanAge(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(int(d.Minutes()), 0))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func sortSeries(s domain.Series) {
	sort.SliceStable(s, func(i, j int) bool { return s[i].T < s[j].T })
}

// FetchOpts controls FetchDetail.
type FetchOpts struct {
	// CompositionRange for the exposure chart ("" = "1m").
	CompositionRange string
	// Light skips fills and funding (much smaller response).
	Light bool
	// WithStatus also fetches v6status for window returns, MaxDD and
	// Sharpe (one shared, 60 s cached list call).
	WithStatus bool
}

// FetchDetail fetches every hlp.xyz part of a vault concurrently through
// any API (the real Client or a fake) and merges them with ToDetail.
//
// /live is required (it also answers for vaults hlp.xyz does not track and
// is how a non-vault address is detected: ErrNotAVault). The other parts
// are best-effort: an untracked vault (ErrNoCache) leaves its history
// empty, and a failing endpoint also sets Meta.Partial with a warning.
func FetchDetail(ctx context.Context, api API, addr string, o FetchOpts) (domain.VaultDetail, error) {
	a, err := NormalizeAddress(addr)
	if err != nil {
		return domain.VaultDetail{}, err
	}
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		parts = DetailParts{Address: a}
		warns []string
		live  VaultLive
		liveE error
	)
	soft := func(what string, err error) {
		mu.Lock()
		defer mu.Unlock()
		if errors.Is(err, ErrNoCache) {
			return // untracked vault: ToDetail already notes the missing history
		}
		warns = append(warns, fmt.Sprintf("hlp.xyz %s unavailable: %v", what, err))
	}
	wg.Add(4)
	go func() { defer wg.Done(); live, liveE = api.VaultLive(ctx, a) }()
	go func() {
		defer wg.Done()
		n, err := api.NAV(ctx, a)
		if err != nil {
			soft("NAV curve", err)
			return
		}
		if len(n.Curve) > 0 {
			mu.Lock()
			parts.NAV = &n
			mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		c, err := api.Composition(ctx, a, o.CompositionRange)
		if err != nil {
			soft("composition", err)
			return
		}
		mu.Lock()
		parts.Composition = &c
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		d, err := api.VaultDetail(ctx, a, DetailOpts{Light: o.Light, Lev: o.Light})
		if err != nil {
			soft("vault history", err)
			return
		}
		mu.Lock()
		parts.Detail = &d
		mu.Unlock()
	}()
	if o.WithStatus {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := api.V6Status(ctx)
			if err != nil {
				soft("v6status", err)
				return
			}
			if s, ok := m[a]; ok {
				mu.Lock()
				parts.Status = &s
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if liveE != nil {
		return domain.VaultDetail{}, liveE
	}
	if err := ctx.Err(); err != nil {
		return domain.VaultDetail{}, err
	}
	parts.Live = &live
	d, err := ToDetail(parts)
	if err != nil {
		return domain.VaultDetail{}, err
	}
	if len(warns) > 0 {
		sort.Strings(warns)
		d.Meta.Partial = true
		d.Meta.Warnings = append(warns, d.Meta.Warnings...)
	}
	return d, nil
}
