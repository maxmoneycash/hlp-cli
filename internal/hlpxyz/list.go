package hlpxyz

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// StaleAfter is how old a vault's freshest figures may be before the row is
// flagged Stale.
const StaleAfter = 48 * time.Hour

// v6Windows maps v6status window keys to domain windows.
var v6Windows = map[string]domain.Window{
	"day": domain.W1D, "week": domain.W1W, "month": domain.W1M, "m3": domain.W3M,
	"m6": domain.W6M, "ytd": domain.WYTD, "year": domain.W1Y, "all": domain.WAll,
}

// SummaryReturns reproduces the website's window returns for a summary row:
// perfSeries[-1].sharePrice / baseSP<Window> - 1, as FRACTIONS. Windows
// whose base is missing (0) are omitted.
func SummaryReturns(v SummaryVault) map[domain.Window]float64 {
	if len(v.PerfSeries) == 0 {
		return nil
	}
	last := v.PerfSeries[len(v.PerfSeries)-1].SharePrice
	bases := map[domain.Window]float64{
		domain.W1D: v.BaseSPDay, domain.W1W: v.BaseSPWeek, domain.W1M: v.BaseSPMonth,
		domain.W3M: v.BaseSP3Month, domain.W6M: v.BaseSP6Month, domain.WYTD: v.BaseSPYtd,
		domain.W1Y: v.BaseSPYear, domain.WAll: v.BaseSPAll,
	}
	out := make(map[domain.Window]float64, len(bases))
	for w, b := range bases {
		if b > 0 && last > 0 {
			out[w] = last/b - 1
		}
	}
	return out
}

// V6Returns converts v6status.ret (PERCENT) to fractions per domain window.
func V6Returns(s V6Status) map[domain.Window]float64 {
	if len(s.Ret) == 0 {
		return nil
	}
	out := make(map[domain.Window]float64, len(s.Ret))
	for k, v := range s.Ret {
		if w, ok := v6Windows[k]; ok {
			out[w] = v / 100
		}
	}
	return out
}

func applyV6Risk(row *domain.VaultSummary, s V6Status) {
	for key, window := range v6Windows {
		dd, sharpe := s.MaxDD[key], s.Sharpe[key]
		if dd == nil && sharpe == nil {
			continue
		}
		risk := domain.WindowRisk{Sharpe: sharpe}
		if dd != nil {
			fraction := *dd / 100
			risk.MaxDD = &fraction
		}
		if row.RiskByWindow == nil {
			row.RiskByWindow = make(map[domain.Window]domain.WindowRisk)
		}
		row.RiskByWindow[window] = risk
	}
	all := row.RiskByWindow[domain.WAll]
	if all.MaxDD != nil {
		row.MaxDD = *all.MaxDD
	}
	if all.Sharpe != nil {
		row.Sharpe = *all.Sharpe
	}
}

// MapVaultList joins the three list endpoints (and optional bulk /live) into
// screener rows, sorted by TVL descending.
//
//   - Returns, MaxDD and Sharpe come from v6status (percent → fraction),
//     falling back to the summary row when a vault has no v6 status.
//   - TVL comes from /live when present (the summary row can be months
//     old: HLP shows $358M there vs $151M live), else summary equity.
//   - Spark is the v6 sparkline, else the summary perfSeries share price.
//   - SPModel is SPv6 when v6 data backs the row; rows without it keep
//     the legacy summary numbers, labelled SPSummary.
//   - Stale: the freshest source for the row (summary lastUpdated or
//     v6status computedAt) is older than StaleAfter. HLP's summary row is
//     stale but its v6 metrics and /live TVL are not, so it is not flagged.
//   - AgeDays/CreatedAt come from dataStart, so they do not freeze when the
//     summary row stops updating.
func MapVaultList(sum SummaryResponse, st map[string]V6Status, sp map[string]domain.Series, live []LiveVault, now time.Time) []domain.VaultSummary {
	liveBy := make(map[string]LiveVault, len(live))
	for _, l := range live {
		liveBy[strings.ToLower(l.Address)] = l
	}
	out := make([]domain.VaultSummary, 0, len(sum.Vaults))
	for _, v := range sum.Vaults {
		addr := strings.ToLower(v.Address)
		row := domain.VaultSummary{
			Address:       addr,
			Name:          strings.TrimSpace(domain.CleanText(v.Name)),
			Kind:          domain.VaultHyperCore,
			TVL:           v.Equity,
			TVLKnown:      true,
			Returns:       SummaryReturns(v),
			MaxDD:         v.MaxDD / 100,
			Sharpe:        v.Sharpe,
			AgeDays:       v.AgeDays,
			AllowDeposits: v.AllowDeposits,
			Closed:        v.IsClosed,
			MaxGrossLev:   v.MaxGrossLev,
			Curated:       true,
			Sources:       []string{"hlp.xyz"},
			SPModel:       domain.SPSummary,
			ReturnsModel:  domain.ReturnsTWR,
		}
		if addr == HLPAddress {
			row.Rel = "parent"
			row.CapacityUSD = -1
		}
		updated := v.LastUpdated
		row.MetricsAsOf = v.LastUpdated
		if v.DataStart > 0 {
			row.CreatedAt = time.UnixMilli(v.DataStart).UTC()
			if d := int(now.Sub(row.CreatedAt) / (24 * time.Hour)); d > row.AgeDays {
				row.AgeDays = d
			}
		}
		if s, ok := st[addr]; ok {
			row.MetricsAsOf = s.ComputedAt
			if r := V6Returns(s); len(r) > 0 {
				row.Returns = r
			}
			applyV6Risk(&row, s)
			row.AccuracyPct = s.AccuracyPct
			row.SPModel = domain.SPv6
			updated = max(updated, s.ComputedAt)
		}
		v6Spark := sp[addr]
		if len(v6Spark) > 0 {
			row.Spark = v6Spark
			row.SparkModel = domain.SPv6
		}
		if len(v6Spark) <= 1 && len(v.PerfSeries) > 0 {
			history := perfSpark(v.PerfSeries)
			// One v6 observation cannot describe a return curve. Prefer
			// real summary history, but keep that observation if the
			// summary also lacks distinct usable dates. Never splice them.
			usable := len(v6Spark) == 0
			for _, point := range history {
				if point.T != history[0].T {
					usable = true
					break
				}
			}
			if usable && len(history) > 0 {
				row.Spark = history
				row.SparkModel = domain.SPSummary
			}
		}
		if l, ok := liveBy[addr]; ok && l.hasTotalEquity() {
			row.TVL = l.TotalEquity
			// Live TVL is fresh but does not refresh returns: keep the
			// staleness decision on the metric sources above.
		}
		if updated > 0 {
			row.UpdatedAt = time.UnixMilli(updated).UTC()
			row.Stale = now.Sub(row.UpdatedAt) > StaleAfter
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TVL > out[j].TVL })
	return out
}

func perfSpark(ps []PerfPoint) domain.Series {
	s := make(domain.Series, 0, len(ps))
	for _, p := range ps {
		t, err := time.Parse(time.RFC3339Nano, p.Date)
		if err != nil {
			t, err = time.Parse("2006-01-02", p.Date)
		}
		if err != nil || p.SharePrice <= 0 {
			continue
		}
		s = append(s, domain.Point{T: t.UnixMilli(), V: p.SharePrice})
	}
	return s
}

// VaultList joins summary + v6status + v6spark (3 parallel GETs) with live
// TVL, mapped by MapVaultList. HLP's /live is fetched alongside the lists
// (its summary row is months old); every other open vault's TVL comes from
// bulk /live within Options.LiveBudget. Only the summary is required: a
// failing v6status or v6spark, or bulk /live missing the budget, degrades
// to summary numbers with Meta.Partial and a warning.
func (c *Client) VaultList(ctx context.Context) ([]domain.VaultSummary, domain.Meta, error) {
	var (
		wg                     sync.WaitGroup
		sum                    SummaryResponse
		st                     map[string]V6Status
		sp                     map[string]domain.Series
		hlp                    VaultLive
		sumI, stI, spI         fetchInfo
		sumE, stE, spErr, hlpE error
	)
	wg.Add(4)
	go func() { defer wg.Done(); sum, sumI, sumE = c.summary(ctx, false) }()
	go func() { defer wg.Done(); st, stI, stE = c.v6status(ctx) }()
	go func() { defer wg.Done(); sp, spI, spErr = c.v6spark(ctx) }()
	go func() { defer wg.Done(); hlp, hlpE = c.VaultLive(ctx, HLPAddress) }()
	wg.Wait()
	if sumE != nil {
		return nil, domain.Meta{}, fmt.Errorf("hlp.xyz vault list: %w", sumE)
	}
	meta := domain.Meta{Source: "hlp.xyz", AsOf: sumI.At, Stale: sumI.Stale}
	note := func(what string, err error) {
		meta.Partial = true
		meta.Warnings = append(meta.Warnings, fmt.Sprintf("hlp.xyz %s unavailable: %v", what, err))
	}
	if stE != nil {
		note("v6status", stE)
	}
	if spErr != nil {
		note("v6spark", spErr)
	}
	var live []LiveVault
	if hlpE == nil && hlp.hasTotalEquity() {
		live = append(live, LiveVault{Address: HLPAddress, SharePrice: hlp.SharePrice, TotalEquity: hlp.TotalEquity, totalEquityPresent: true})
	} else if hlpE != nil {
		note("HLP live TVL", hlpE)
	}
	addrs := make([]string, 0, len(sum.Vaults))
	for _, v := range sum.Vaults {
		if !v.IsClosed && addrRe.MatchString(v.Address) && v.Address != HLPAddress {
			addrs = append(addrs, v.Address)
		}
	}
	lctx, cancel := context.WithTimeout(ctx, c.opts.LiveBudget)
	bulk, liveI, liveE := c.live(lctx, addrs)
	cancel()
	switch {
	case liveE != nil && ctx.Err() != nil:
		return nil, meta, ctx.Err()
	case liveE != nil:
		meta.Partial = true
		meta.Warnings = append(meta.Warnings, "hlp.xyz live TVL is slow or unavailable; TVL shown from the hlp.xyz summary, which can lag")
	default:
		live = append(live, bulk...)
	}
	for _, fi := range []fetchInfo{stI, spI, liveI} {
		if !fi.At.IsZero() && fi.At.Before(meta.AsOf) {
			meta.AsOf = fi.At // the oldest piece bounds the freshness
		}
		meta.Stale = meta.Stale || fi.Stale
	}
	return MapVaultList(sum, st, sp, live, c.opts.Now()), meta, nil
}
