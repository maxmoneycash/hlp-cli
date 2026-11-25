package cards

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/maxmoneycash/hlp-cli/internal/chart"
	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
)

// rowLimit picks how many table rows fit: compact 5 (DESIGN.md section 5.1
// rule 9), natural def, or the height budget minus header, footer and
// table header.
func (p *painter) rowLimit(def int) int {
	if p.o.AllRows {
		return 1 << 30
	}
	if p.o.Height > 0 {
		return max(p.o.Height-3, 1)
	}
	if p.o.Compact {
		return min(def, inlineRows)
	}
	return def
}

// inlineRows is the most table rows an inline card shows (8 for positions).
const inlineRows = 5

// more appends a Dim "+N" line when rows were cut. The full pane (ctrl+o)
// has every row, so the line carries no hint.
func (p *painter) more(lines []string, total, shown int) []string {
	if total > shown {
		lines = append(lines, p.st.dim.Render(fmt.Sprintf("+%d", total-shown)))
	}
	return lines
}

// chartLocation picks the label zone: local for intraday, UTC for >= 1d.
func (p *painter) chartLocation(iv domain.Interval) *time.Location {
	if iv.Duration() >= 24*time.Hour {
		return time.UTC
	}
	return locOr(p.o.Location)
}

func marketTitle(m domain.Market, coin string) string {
	if m.Display != "" {
		return m.Display
	}
	return coin
}

// chartCard: header with the last price as the focal number, the candle
// chart at card width, then one line of funding / OI / volume context.
func (p *painter) chartCard(r *core.Result) card {
	d, ok := as[core.ChartData](r.Data)
	if !ok {
		return p.fallbackCard(r)
	}
	c := card{title: strings.TrimPrefix(marketTitle(d.Market, d.Coin), "xyz:"), sub: string(d.Interval), tagOwnLine: true}
	last := d.Summary.Last
	if last == 0 && len(d.Candles) > 0 {
		last = d.Candles[len(d.Candles)-1].C
	}
	if last != 0 {
		c.focal, c.focalStyle, c.numeric = Px(last), p.st.bold, true
	}
	chg, chgLabel := d.Summary.Change, ""
	if d.Market.PrevDay > 0 && d.Market.Mark > 0 {
		chg, chgLabel = d.Market.ComputeChange24h(), "24h"
	}
	if chg != 0 {
		c.delta, c.deltaStyle, c.deltaLabel = SignedPct(chg, 1), p.sign(chg), chgLabel
		if math.Abs(chg) < 0.0005 {
			c.deltaStyle = p.st.text // rounds to 0.0%: no direction to color
		}
	}

	ctx := p.marketContext(d.Market)
	h := chartRowsInline
	if p.o.Compact {
		h = chartRowsCompact
	}
	if p.o.Height > 0 {
		h = max(p.o.Height-2, 4)
		if ctx != "" {
			h -= 2
		}
	}
	// Explicit --height/--style/--zoom (ChartData render hints) win over the
	// inline defaults; a pane's height budget still caps the height.
	if d.Height > 0 {
		if p.o.Height > 0 {
			h = min(h, d.Height)
		} else {
			h = d.Height
		}
	}
	style := p.o.ChartStyle
	if st, ok := chart.ParseStyle(d.Style); ok && d.Style != "" {
		style = st
	}
	zoom := 2
	if chart.WideBodies(style, p.o.ChartSkin) {
		// an open body needs 3 columns to read as a box (a joined ┌┐└┘
		// outline), not a 1-column rule (setup-rush landing candles)
		zoom = 4
	}
	if d.Zoom > 0 {
		zoom = d.Zoom
	}
	if len(d.Candles) == 0 {
		c.focal, c.focalStyle, c.numeric = "NO CANDLES", p.st.muted, false
		return c
	}
	g, _ := candlesFn(d.Candles, chart.CandleOpts{
		Width: p.w, Height: h, Style: style, Skin: p.o.ChartSkin, Zoom: zoom, VolRows: -1, VolumeUSD: true,
		MAs: d.MAs, EMAs: d.EMAs, Lines: d.Lines, Cursor: -1, Header: !p.o.Compact, Coin: d.Coin, Interval: d.Interval,
		NoTitle: true, Location: p.chartLocation(d.Interval),
	}, p.th)
	c.body = p.gridLines(g)
	if ctx != "" {
		// one blank row between the time labels and the meta line
		c.body = append(c.body, "", ctx)
	}
	return c
}

// overlayLegend names the chart's MA/EMA lines in their own colors, in the
// order the chart assigns them ("·MA20 ·MA50").
func (p *painter) overlayLegend(mas, emas []int) string {
	var parts []string
	add := func(label string) {
		if len(parts) == 3 {
			return
		}
		st := p.st.muted
		if !p.o.Plain {
			if col := p.th.MA[len(parts)].Resolve(p.o.Profile); col != nil {
				st = lipgloss.NewStyle().Foreground(col)
			}
		}
		parts = append(parts, st.Render("·"+label))
	}
	for _, n := range mas {
		add("MA" + strconv.Itoa(n))
	}
	for _, n := range emas {
		add("EMA" + strconv.Itoa(n))
	}
	return strings.Join(parts, " ")
}

// marketContext is the one meta line under a chart:
// FUNDING +11.0%   OI $2.34B   VOL $6.74B (funding as APR; the hourly
// rate is in the full card's NOTES).
func (p *painter) marketContext(m domain.Market) string {
	if m.Mark == 0 || m.Kind == domain.Spot {
		return ""
	}
	var pairs [][2]string
	if m.Funding != 0 {
		pairs = append(pairs, [2]string{"funding", p.sign(m.Funding).Render(SignedPct(m.ComputeFundingAPR(), 1))})
		if !p.o.Compact {
			pairs = append(pairs, [2]string{"1h", p.sign(m.Funding).Render(fundingHourly(m.Funding))})
		}
	}
	if m.OpenInterest > 0 {
		pairs = append(pairs, [2]string{"OI", p.st.text.Render(USD(m.OpenInterest * m.Mark))})
	}
	if m.DayNtlVlm > 0 {
		pairs = append(pairs, [2]string{"vol", p.st.text.Render(USD(m.DayNtlVlm))})
	}
	return p.meta(pairs...)
}

// fundingHourly prints an hourly rate as a signed percent with 4 decimals.
func fundingHourly(v float64) string { return SignedPct(v, 4) }

// bookCard: mid as the focal number, the depth ladder (with its spread row) below.
func (p *painter) bookCard(r *core.Result) card {
	b, ok := as[domain.Book](r.Data)
	if !ok {
		return p.fallbackCard(r)
	}
	c := card{title: strings.TrimPrefix(b.Coin, "xyz:"), sub: "book"}
	if mid := b.Mid(); mid > 0 && !p.o.Compact {
		// Inline, the ladder's spread row carries the mid in bold: the
		// header says it once in a pane only.
		c.focal, c.focalStyle, c.numeric = Px(mid), p.st.bold, true
	}
	levels := 10
	if p.o.Compact {
		levels = 5
	}
	if p.o.Height > 0 {
		levels = max((p.o.Height-2-2)/2, 1) // card header + footer, ladder header + spread row
	}
	levels = max(min(levels, max(len(b.Bids), len(b.Asks))), 1)
	h := levels*2 + 2 // ladder header, asks, spread row, bids
	if len(b.Bids) == 0 && len(b.Asks) == 0 {
		c.focal, c.focalStyle, c.numeric = "EMPTY", p.st.muted, false
		return c
	}
	// The book reads at 72 columns at most (DESIGN.md section 6.1).
	g := depthFn(b, chart.DepthOpts{Width: min(p.w, bookWidth), Height: h, Levels: levels, Ladder: true, Decimals: -1, SzDecimals: bookSzDecimals(b), Compact: p.o.Compact}, p.th)
	c.body = p.gridLines(g)
	return c
}

// bookWidth is the book card's widest ladder.
const bookWidth = 72 - 2*Inset

// bookSzDecimals infers the asset's size precision from the book itself:
// every level size is a multiple of 10^-szDecimals, so the most decimals any
// level uses is a lower bound that is exact on any book with a small level.
// (domain.Book carries no market; 0 = unknown lets the chart fall back.)
func bookSzDecimals(b domain.Book) int {
	dec := 0
	for _, side := range [][]domain.Level{b.Bids, b.Asks} {
		for _, l := range side {
			s := strconv.FormatFloat(l.Sz, 'f', -1, 64)
			if i := strings.IndexByte(s, '.'); i >= 0 {
				dec = max(dec, min(len(s)-i-1, 8))
			}
		}
	}
	return dec
}

// tradesCard: the tape, newest first, aggressor side colored.
func (p *painter) tradesCard(r *core.Result) card {
	ts, ok := as[[]domain.Trade](r.Data)
	if !ok {
		return p.fallbackCard(r)
	}
	c := card{title: strings.TrimPrefix(strings.TrimSuffix(r.Title, " trades"), "xyz:"), sub: "trades"}
	if len(ts) == 0 {
		c.focal, c.focalStyle = "NO TRADES", p.st.muted
		return c
	}
	sorted := append([]domain.Trade(nil), ts...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Time > sorted[j].Time })
	if c.title == "" {
		c.title = sorted[0].Coin
	}
	c.focal, c.focalStyle, c.numeric = Px(sorted[0].Px), p.st.bold, true
	var buy, total float64
	for _, t := range ts {
		n := t.Px * t.Sz
		total += n
		if t.Side == domain.Buy {
			buy += n
		}
	}
	if total > 0 {
		c.delta, c.deltaStyle = "BUY "+Pct(buy/total, 0), p.st.muted
	}
	n := min(len(sorted), p.rowLimit(20))
	rows := make([][]cell, 0, n)
	for _, t := range sorted[:n] {
		// size is signed by the aggressor side: the sign says buy or sell
		sz, st := Size(t.Sz), p.st.up
		if t.Side == domain.Sell {
			sz, st = minus+sz, p.st.down
		} else {
			sz = "+" + sz
		}
		rows = append(rows, []cell{
			p.cm(Clock(t.Time, p.o.Location)),
			p.c(Px(t.Px)),
			p.cst(sz, st),
			p.cm(USD(t.Px * t.Sz)),
		})
	}
	cols := []column{
		{title: "time", keep: 2}, {title: "px", right: true, keep: 9},
		{title: "size", right: true, keep: 8}, {title: "value", right: true, keep: 4},
	}
	c.body = p.more(p.table(cols, rows, p.w, nil), len(sorted), n)
	return c
}

// marketsCard: one row per instrument; price, 24h change, funding APR, OI, volume.
func (p *painter) marketsCard(r *core.Result) card {
	ms, ok := as[[]domain.Market](r.Data)
	if !ok {
		return p.fallbackCard(r)
	}
	c := card{title: r.Title}
	if c.title == "" {
		c.title = "Markets"
	}
	c.focal, c.focalStyle = strconv.Itoa(len(ms)), p.st.bold
	if len(ms) == 0 {
		c.focal, c.focalStyle = "NO MATCH", p.st.muted
		return c
	}
	n := min(len(ms), p.rowLimit(50))
	rows := make([][]cell, 0, n)
	for _, m := range ms[:n] {
		chg := m.ComputeChange24h()
		fund := p.cm(dash)
		if m.Kind != domain.Spot {
			fund = p.cs(SignedPct(m.ComputeFundingAPR(), 1), m.Funding)
		}
		oi := dash
		if m.OpenInterest > 0 {
			oi = USD(m.OpenInterest * m.Mark)
		}
		rows = append(rows, []cell{
			p.c(strings.TrimPrefix(marketTitle(m, m.Coin), "xyz:")),
			p.c(Px(m.Mark)),
			p.cs(Chg(chg), chg),
			fund,
			p.cm(oi),
			p.cm(USD(m.DayNtlVlm)),
		})
	}
	cols := []column{
		{title: "market", flex: true, minW: 8, keep: 10}, {title: "px", right: true, keep: 9},
		{title: "24h", right: true, keep: 8}, {title: "funding", right: true, keep: 6},
		{title: "OI", right: true, keep: 4}, {title: "vol", right: true, keep: 5},
	}
	c.body = p.more(p.table(cols, rows, p.w, nil), len(ms), n)
	return c
}

// fundingCard: a ranking (who pays whom) or one market's history.
func (p *painter) fundingCard(r *core.Result) card {
	if pts, ok := as[[]domain.FundingPoint](r.Data); ok {
		return p.fundingHistory(r, pts)
	}
	ms, ok := as[[]domain.Market](r.Data)
	if !ok {
		return p.fallbackCard(r)
	}
	c := card{title: r.Title}
	if c.title == "" {
		c.title = "Funding"
	}
	var pos, neg []domain.Market
	for _, m := range ms {
		switch {
		case m.Funding > 0:
			pos = append(pos, m)
		case m.Funding < 0:
			neg = append(neg, m)
		}
	}
	sort.SliceStable(pos, func(i, j int) bool { return pos[i].Funding > pos[j].Funding })
	sort.SliceStable(neg, func(i, j int) bool { return neg[i].Funding < neg[j].Funding })
	top := 10
	if p.o.Compact {
		top = 5
	}
	if p.o.Height > 0 {
		top = max((p.o.Height-6)/2, 1)
	}
	pos, neg = pos[:min(len(pos), top)], neg[:min(len(neg), top)]
	peak := 0.0
	for _, m := range append(append([]domain.Market{}, pos...), neg...) {
		peak = math.Max(peak, math.Abs(m.Funding))
	}
	barW := max(min(p.w-44, 24), 0)
	withBar := barW > 0 && peak > 0
	row := func(m domain.Market) []cell {
		r := []cell{p.c(strings.TrimPrefix(marketTitle(m, m.Coin), "xyz:")), p.cs(fundingHourly(m.Funding), m.Funding), p.cs(SignedPct(m.ComputeFundingAPR(), 1), m.Funding)}
		if withBar {
			r = append(r, p.cs(meter(math.Abs(m.Funding)/peak, barW), m.Funding))
		}
		return r
	}
	// One table for both sides so the columns share tracks; the second
	// side's label is a muted divider row.
	var rows [][]cell
	first := "longs pay"
	for _, m := range pos {
		rows = append(rows, row(m))
	}
	if len(pos) == 0 {
		first = "shorts pay"
	} else if len(neg) > 0 {
		rows = append(rows, []cell{}, []cell{p.cst("SHORTS PAY", p.st.label)})
	}
	for _, m := range neg {
		rows = append(rows, row(m))
	}
	cols := []column{{title: first, flex: true, minW: 8, keep: 9}, {title: "1h", right: true, keep: 6}, {title: "APR", right: true, keep: 8}}
	if withBar {
		cols = append(cols, column{title: "", keep: 1})
	}
	if len(rows) > 0 {
		c.body = p.table(cols, rows, p.w, nil)
	}
	if len(c.body) == 0 {
		c.focal, c.focalStyle = "NO DATA", p.st.muted
	}
	return c
}

// pctLess compares two formatted signed percents ("+10.9%" < "+11.0%").
func pctLess(a, b string) bool {
	num := func(s string) (float64, error) {
		s = strings.Replace(strings.TrimPrefix(s, "+"), minus, "-", 1)
		return strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
	}
	pa, ea := num(a)
	pb, eb := num(b)
	return ea == nil && eb == nil && pa < pb
}

func (p *painter) fundingHistory(r *core.Result, pts []domain.FundingPoint) card {
	c := card{title: r.Title}
	if c.title == "" && len(pts) > 0 {
		c.title = pts[0].Coin + " funding"
	}
	if len(pts) == 0 {
		c.focal, c.focalStyle = "NO DATA", p.st.muted
		return c
	}
	c.tagOwnLine = true
	last := pts[len(pts)-1].Rate
	c.focal, c.focalStyle, c.numeric = SignedPct(last*24*365, 1), p.sign(last), true
	c.deltaLabel = "APR"
	s := make(domain.Series, len(pts))
	sum, lo, hi := 0.0, math.Inf(1), math.Inf(-1)
	for i, f := range pts {
		apr := f.Rate * 24 * 365
		s[i] = domain.Point{T: f.Time, V: apr}
		sum += apr
		lo, hi = math.Min(lo, apr), math.Max(hi, apr)
	}
	// The plot needs room for its time axis below the line (two rows of
	// axis), so the compact card keeps at least 7 rows.
	h := 10
	if p.o.Compact {
		h = 7
	}
	g := lineFn(chart.LineOpts{Width: p.w, Height: h, Series: []chart.Named{{Name: "APR", Series: s}},
		Baseline: 0, PercentAxis: true, Location: p.o.Location}, p.th)
	c.body = p.gridLines(g)
	if p.o.Plain {
		for i, l := range c.body {
			c.body[i] = strings.TrimRight(l, " ")
		}
	}
	hours := float64(pts[len(pts)-1].Time-pts[0].Time) / 3.6e6
	// The average sits between the low and the high once all three are
	// rounded the same way (10.95 must not print below a low of 11.0).
	avg := math.Min(math.Max(sum/float64(len(pts)), lo), hi)
	avgS, loS, hiS := SignedPct(avg, 1), SignedPct(lo, 1), SignedPct(hi, 1)
	if pctLess(avgS, loS) {
		avgS = loS
	} else if pctLess(hiS, avgS) {
		avgS = hiS
	}
	c.body = append(c.body, p.meta(
		[2]string{"avg", p.sign(avg).Render(avgS)},
		[2]string{"low", p.sign(lo).Render(loS)},
		[2]string{"high", p.sign(hi).Render(hiS)},
		[2]string{"", p.lbl(fmt.Sprintf("%.0fH", hours))},
	))
	if !p.o.Compact {
		c.notes = append(c.notes, p.lbl("funding 1h")+" "+p.sign(last).Render(fundingHourly(last)))
	}
	return c
}
