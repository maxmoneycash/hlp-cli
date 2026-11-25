package core

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/hlpxyz"
)

// MonthlyObservation retains the actual UTC observation boundaries. A missing
// boundary is not a zero return. Complete concerns calendar coverage, independent
// of the upstream batch's completeness. Current/inception months are partial.
type MonthlyObservation struct {
	Year     int      `json:"year"`
	Month    int      `json:"month"`
	Return   *float64 `json:"return"`
	Start    int64    `json:"start"`
	End      int64    `json:"end"`
	Complete bool     `json:"complete"`
	Status   string   `json:"status"` // complete | current | inception | gap
}

type VaultHistoryRow struct {
	// Website Stats uses the summary equity for the pie and scales missing history
	// from summary share prices. These estimates stay distinct from observations.
	SummaryEquity   *float64             `json:"summaryEquity,omitempty"`
	EstimatedEquity domain.Series        `json:"estimatedEquity,omitempty"`
	Address         string               `json:"address"`
	Name            string               `json:"name"`
	Equity          domain.Series        `json:"equity"`
	NAV             domain.Series        `json:"nav"`
	Monthly         []MonthlyObservation `json:"monthly"`
	EquityAsOf      int64                `json:"equityAsOf"`
	EquityUpdatedAt int64                `json:"equityUpdatedAt"`
	NAVAsOf         int64                `json:"navAsOf"`
	NAVModel        string               `json:"navModel"`
	Complete        bool                 `json:"complete"`
	Sampled         bool                 `json:"sampled"`
}

type VaultHistoryData struct {
	Vaults     []VaultHistoryRow `json:"vaults"`
	Window     string            `json:"window"`
	ObservedAt int64             `json:"observedAt"`
	Complete   bool              `json:"complete"`
	Warnings   []string          `json:"warnings"`
}

// Optional extension preserves existing custom hlpxyz.API implementations.
type indexCurveSource interface {
	IndexCurves(context.Context) (map[string]domain.Series, bool, error)
}

func historyOptions(window string, points int, now int64) (string, int, int64, error) {
	if window == "" {
		window = "all"
	}
	start, err := WindowStart(window, now)
	if err != nil {
		return "", 0, 0, err
	}
	if points == 0 {
		points = 400
	}
	if points < 2 || points > 5000 {
		return "", 0, 0, Errorf(CodeUsage, "points must be between 2 and 5000")
	}
	return window, points, start, nil
}

func bulkIndexCurves(ctx context.Context, x hlpxyz.API) (map[string]domain.Series, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if source, ok := x.(indexCurveSource); ok {
		return source.IndexCurves(ctx)
	}
	return nil, false, fmt.Errorf("bulk v6 NAV history is unavailable from this source")
}

func runVaultHistory(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*VaultHistoryInput)
	now := env.now().UnixMilli()
	window, points, start, err := historyOptions(p.Window, p.Points, now)
	if err != nil {
		return nil, err
	}
	x, err := env.needHLPXYZ()
	if err != nil {
		return nil, err
	}
	// Sequential, bounded bulk reads. No per-vault HTTP calls, live polling or
	// full activity payloads. A failed NAV batch still leaves useful equity.
	summary, err := x.Summary(ctx, false)
	if err != nil {
		return nil, err
	}
	curves, complete, curveErr := bulkIndexCurves(ctx, x)
	out := &VaultHistoryData{Window: window, ObservedAt: now, Complete: complete, Vaults: []VaultHistoryRow{}, Warnings: []string{}}
	if curveErr != nil {
		out.Warnings = append(out.Warnings, "v6 NAV history unavailable: "+AsError(curveErr).Message)
	}
	if !complete {
		out.Warnings = append(out.Warnings, "Bulk v6 history is incomplete; missing months remain unavailable")
	}
	meta := env.meta("hlp.xyz summary + idxCurves/v6")
	for _, s := range summary.Vaults {
		row := VaultHistoryRow{Address: strings.ToLower(s.Address), Name: domain.CleanText(s.Name), EquityUpdatedAt: s.LastUpdated, NAVModel: string(domain.SPv6)}
		var equity, estimates domain.Series
		lastShare := 1.0
		if len(s.PerfSeries) > 0 && s.Equity > 0 {
			row.SummaryEquity = &s.Equity
			if last := s.PerfSeries[len(s.PerfSeries)-1].SharePrice; last != 0 {
				lastShare = last
			}
		}
		for _, p := range s.PerfSeries {
			t, err := time.Parse(time.RFC3339, p.Date)
			if err != nil {
				t, err = time.Parse("2006-01-02", p.Date)
			}
			if err != nil {
				continue
			}
			if p.Equity != nil {
				equity = append(equity, domain.Point{T: t.UnixMilli(), V: *p.Equity})
			} else if row.SummaryEquity != nil && lastShare > 0 && p.SharePrice > 0 {
				estimates = append(estimates, domain.Point{T: t.UnixMilli(), V: s.Equity * p.SharePrice / lastShare})
			}
		}
		equity = cleanHistory(equity, now, false)
		nav := cleanHistory(curves[row.Address], now, false)
		row.EquityAsOf, row.NAVAsOf = historyEnd(equity), historyEnd(nav)
		row.Complete = complete && len(nav) > 1 && len(equity) > 0
		row.Monthly = monthlyObservations(nav, now, start)
		equity, nav = observedWindow(equity, start), observedWindow(nav, start)
		estimates = observedWindow(cleanHistory(estimates, now, true), start)
		row.EstimatedEquity = sampleHistory(estimates, points)
		row.Sampled = len(equity) > points || len(nav) > points || len(estimates) > points
		row.Equity, row.NAV = sampleHistory(equity, points), sampleHistory(nav, points)
		out.Complete = out.Complete && row.Complete
		if row.EquityAsOf == 0 || now-row.EquityAsOf > historyTolerance || row.NAVAsOf == 0 || now-row.NAVAsOf > historyTolerance {
			meta.Stale = true
		}
		meta.Truncated = meta.Truncated || row.Sampled
		out.Vaults = append(out.Vaults, row)
	}
	if !out.Complete && complete {
		out.Warnings = append(out.Warnings, "Some curated vaults lack equity or v6 NAV history")
	}
	meta.Partial, meta.Warnings = !out.Complete, append(meta.Warnings, out.Warnings...)
	return &Result{Card: CardKV, Title: "Dated vault histories", Data: out, Meta: meta}, nil
}

const historyTolerance = int64(48 * time.Hour / time.Millisecond)

func cleanHistory(s domain.Series, now int64, positive bool) domain.Series {
	out := make(domain.Series, 0, len(s))
	for _, p := range s {
		if p.T > 0 && p.T <= now && !math.IsNaN(p.V) && !math.IsInf(p.V, 0) && p.V >= 0 && (!positive || p.V > 0) {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].T < out[j].T })
	n := 0
	for _, p := range out {
		if n > 0 && out[n-1].T == p.T {
			out[n-1] = p
		} else {
			out[n] = p
			n++
		}
	}
	return out[:n]
}

func historyEnd(s domain.Series) int64 {
	if len(s) == 0 {
		return 0
	}
	return s[len(s)-1].T
}

// Preserve actual observation timestamps; metrics.Clip would insert a carried
// value at the requested start, making old equity look newly observed.
func observedWindow(s domain.Series, start int64) domain.Series {
	i := sort.Search(len(s), func(i int) bool { return s[i].T >= start })
	return s[i:]
}

func sampleHistory(s domain.Series, n int) domain.Series {
	if len(s) <= n {
		return s
	}
	out := make(domain.Series, n)
	for i := range out {
		out[i] = s[i*(len(s)-1)/(n-1)]
	}
	return out
}

func monthlyObservations(s domain.Series, now, start int64) []MonthlyObservation {
	out := []MonthlyObservation{}
	for i := 0; i < len(s); {
		t := time.UnixMilli(s[i].T).UTC()
		begin := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
		end := begin.AddDate(0, 1, 0).UnixMilli()
		j := i + 1
		for j < len(s) && s[j].T < end {
			j++
		}
		base := s[i]
		if i > 0 && abs64(s[i-1].T-begin.UnixMilli()) <= historyTolerance {
			base = s[i-1]
		}
		last := s[j-1]
		m := MonthlyObservation{Year: t.Year(), Month: int(t.Month()), Start: base.T, End: last.T, Status: "complete"}
		startOK := abs64(base.T-begin.UnixMilli()) <= historyTolerance
		endOK := end-last.T <= historyTolerance
		current := now >= begin.UnixMilli() && now < end
		inception := i == 0 && !startOK
		switch {
		case !startOK && !inception || !endOK && !current:
			m.Status = "gap"
		case current:
			m.Status = "current"
		case inception:
			m.Status = "inception"
		}
		if m.Status == "complete" && last.T <= base.T {
			m.Status = "gap"
		}
		m.Complete = m.Status == "complete"
		if m.Status != "gap" && base.V > 0 && last.T > base.T {
			r := last.V/base.V - 1
			m.Return = &r
		}
		if last.T >= start {
			out = append(out, m)
		}
		i = j
	}
	return out
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
