package core

import (
	"math"
	"sort"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// IndexWebsiteStatistics identifies the public client's ne/wt formula, applied
// to the complete valuation grid before display sampling. Fractions, not percent.
// Null risk values distinguish insufficient observations from a measured zero;
// the website's display wrapper substitutes zero for those unavailable values.
type IndexWebsiteStatistics struct {
	Method           string   `json:"method"`
	Start            int64    `json:"start"`
	End              int64    `json:"end"`
	Days             float64  `json:"days"`
	ValuationSamples int      `json:"valuationSamples"`
	RiskSamples      int      `json:"riskSamples"` // valid log-return intervals after UTC-day thinning
	TotalReturn      float64  `json:"totalReturn"`
	CAGR             *float64 `json:"cagr"` // unavailable before half a 365-day year
	Vol              *float64 `json:"vol"`
	Sharpe           *float64 `json:"sharpe"`
	Sortino          *float64 `json:"sortino"`
	MaxDD            float64  `json:"maxDD"`
}

// Public data-BmYXk3Ng.js ne/wt: daily retained log returns, elapsed-time mean,
// centered residual variance with sample correction, annualized over 365 days.
// This is intentionally separate from metrics.Compute and its observed-sample
// conventions. Source provenance and clamped coverage remain on IndexHistory.
func websiteIndexStatistics(curve domain.Series) *IndexWebsiteStatistics {
	if len(curve) < 2 {
		return nil
	}
	for i, p := range curve {
		if p.V <= 0 || math.IsNaN(p.V) || math.IsInf(p.V, 0) || (i > 0 && p.T <= curve[i-1].T) {
			return nil
		}
	}
	first, last := curve[0], curve[len(curve)-1]
	m := &IndexWebsiteStatistics{Method: "website-client-log-time-v1", Start: first.T, End: last.T,
		Days: float64(last.T-first.T) / float64(websiteDay), ValuationSamples: len(curve), TotalReturn: last.V/first.V - 1}
	if math.IsNaN(m.TotalReturn) || math.IsInf(m.TotalReturn, 0) {
		return nil
	}
	peak := first.V
	for _, p := range curve {
		peak = math.Max(peak, p.V)
		m.MaxDD = math.Min(m.MaxDD, p.V/peak-1)
	}
	finite := func(value float64) *float64 {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil
		}
		return &value
	}
	if m.Days >= 365*.5 {
		m.CAGR = finite(math.Pow(math.Max(1e-9, 1+m.TotalReturn), 365/m.Days) - 1)
	}
	if last.T-first.T < 2*websiteDay {
		return m
	}
	// The client retains the last interior observation in each UTC day, then
	// discards observations less than half a day from the last retained sample.
	byDay := map[int64]domain.Point{}
	for _, p := range curve[1 : len(curve)-1] {
		byDay[p.T/websiteDay] = p
	}
	days := make([]int64, 0, len(byDay))
	for day := range byDay {
		days = append(days, day)
	}
	sort.Slice(days, func(i, j int) bool { return days[i] < days[j] })
	samples := domain.Series{first}
	for _, day := range days {
		p := byDay[day]
		if p.T-samples[len(samples)-1].T >= websiteDay/2 {
			samples = append(samples, p)
		}
	}
	if last.T-samples[len(samples)-1].T < websiteDay/2 && len(samples) > 1 {
		samples[len(samples)-1] = last
	} else {
		samples = append(samples, last)
	}
	if len(samples) < 3 {
		return m
	}
	returns, spans := make([]float64, 0, len(samples)-1), make([]float64, 0, len(samples)-1)
	var elapsed, totalLog float64
	for i := 1; i < len(samples); i++ {
		days := float64(samples[i].T-samples[i-1].T) / float64(websiteDay)
		r := math.Log(samples[i].V / samples[i-1].V)
		returns, spans = append(returns, r), append(spans, days)
		elapsed, totalLog = elapsed+days, totalLog+r
	}
	m.RiskSamples = len(returns)
	mean := totalLog / elapsed
	var variance, downside float64
	for i, r := range returns {
		residual := r - mean*spans[i]
		variance += residual * residual
		downside += math.Pow(math.Min(0, residual), 2)
	}
	correction := float64(max(1, len(returns)-1)) / float64(len(returns))
	sd, down := math.Sqrt(variance/elapsed/correction), math.Sqrt(downside/elapsed/correction)
	m.Vol = finite(sd * math.Sqrt(365))
	sharpe, sortino := 0.0, 0.0
	if sd > 0 {
		sharpe = mean / sd * math.Sqrt(365)
	}
	if down > 0 {
		sortino = mean / down * math.Sqrt(365)
	} else if mean > 0 {
		sortino = 10 // explicit public-client convention for zero downside deviation
	}
	m.Sharpe, m.Sortino = finite(sharpe), finite(sortino)
	return m
}
