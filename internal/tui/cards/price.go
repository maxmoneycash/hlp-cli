package cards

import (
	"strings"

	"github.com/maxmoneycash/hlp-cli/internal/core"
)

// priceCard is the quote card of get_price. One coin: the mid is the one
// big number with its 24h change, and a single quiet line carries the
// spread, mark and funding. Several coins: one row per coin, in the order
// asked, with the same few columns and nothing else.
func (p *painter) priceCard(r *core.Result) card {
	rows, ok := as[[]core.PriceRow](r.Data)
	if !ok {
		return p.fallbackCard(r)
	}
	c := card{title: r.Title}
	if len(rows) == 0 {
		c.focal, c.focalStyle = "NO MATCH", p.st.muted
		return c
	}
	if len(rows) == 1 {
		// Inline the quote is its header alone: BTC / 64,951  −0.9%  24H.
		// The full card adds bid, ask, mark and funding on one meta line.
		q := rows[0]
		c.title = strings.TrimPrefix(q.Coin, "xyz:")
		c.focal, c.focalStyle, c.numeric = Px(q.Mid), p.st.bold, true
		if q.Change24h != 0 {
			c.delta, c.deltaStyle, c.deltaLabel = SignedPct(q.Change24h, 1), p.sign(q.Change24h), "24h"
		}
		if p.o.Compact {
			return c
		}
		var pairs [][2]string
		if q.Bid > 0 && q.Ask > 0 {
			pairs = append(pairs, [2]string{"bid", p.st.text.Render(Px(q.Bid))}, [2]string{"ask", p.st.text.Render(Px(q.Ask))})
		}
		if q.Mark > 0 && q.Mark != q.Mid {
			pairs = append(pairs, [2]string{"mark", p.st.text.Render(Px(q.Mark))})
		}
		if q.Funding != 0 {
			pairs = append(pairs, [2]string{"funding", p.sign(q.Funding).Render(SignedPct(q.Funding*24*365, 1))},
				[2]string{"1h", p.sign(q.Funding).Render(fundingHourly(q.Funding))})
		}
		if len(pairs) > 0 {
			c.body = []string{p.meta(pairs...)}
		}
		return c
	}
	if c.title == "" || len(strings.Fields(c.title)) > 3 {
		c.title = "Prices"
	}
	cells := make([][]cell, 0, len(rows))
	for _, q := range rows {
		spread := dash
		if q.Bid > 0 && q.Ask > 0 {
			spread = Px(q.Bid) + " / " + Px(q.Ask)
		}
		fund := p.cm(dash)
		if q.Funding != 0 {
			fund = p.cs(SignedPct(q.Funding*24*365, 1), q.Funding)
		}
		cells = append(cells, []cell{
			p.c(strings.TrimPrefix(q.Coin, "xyz:")),
			p.c(Px(q.Mid)),
			p.cs(Chg(q.Change24h), q.Change24h),
			p.cm(spread),
			fund,
		})
	}
	cols := []column{
		{title: "coin", flex: true, minW: 6, keep: 10}, {title: "mid", right: true, keep: 9},
		{title: "24h", right: true, keep: 8}, {title: "bid / ask", right: true, keep: 4},
		{title: "funding", right: true, keep: 5},
	}
	c.body = p.table(cols, cells, p.w, nil)
	return c
}
