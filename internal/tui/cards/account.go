package cards

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/tui/glyph"
)

// accountCard: equity is the focal number; one line of balances; the
// positions table with PnL color and distance to liquidation; open orders.
func (p *painter) accountCard(r *core.Result) card {
	a, ok := as[domain.Account](r.Data)
	if !ok {
		return p.fallbackCard(r)
	}
	c := card{title: "Account", sub: ShortAddr(a.Address)}
	if a.Address == "" {
		c.title, c.sub = r.Title, ""
	}
	if a.Role != "" && a.Role != "user" && !p.o.Compact {
		c.notes = append(c.notes, p.lbl("role")+" "+p.st.text.Render(a.Role))
	}
	c.focal, c.focalStyle, c.numeric = USD(a.Equity), p.st.bold, true
	upnl := 0.0
	for _, pos := range a.Positions {
		upnl += pos.UPnL
	}
	if len(a.Positions) > 0 {
		c.delta, c.deltaStyle = SignedUSD(upnl), p.sign(upnl)
	}
	if b := p.balances(a); b != "" {
		c.body = append(c.body, b)
	}
	limit := p.rowLimit(30)
	if p.o.Compact && p.o.Height <= 0 && !p.o.AllRows {
		limit = 8 // positions get 8 inline rows (DESIGN.md section 5.1 rule 9)
	}
	if len(a.Positions) == 0 {
		c.focalStyle = p.st.bold
		c.delta, c.deltaStyle = "FLAT", p.st.muted
	} else {
		c.body = append(c.body, p.positions(a.Positions, p.w, limit)...)
	}
	if n := len(a.OpenOrders); n > 0 && !p.o.Compact {
		c.body = append(c.body, "")
		c.body = append(c.body, p.orders(a.OpenOrders)...)
	}
	return c
}

// balances is the account's one meta line: LEV 1.26X   FREE $333
// 2 ORDERS. The full card adds the perp / spot / earn split.
func (p *painter) balances(a domain.Account) string {
	var pairs [][2]string
	// A vault aggregate (hlp_positioning) has no account leverage: never
	// print 0.00x.
	if a.Equity > 0 && a.GrossNtl/a.Equity >= 0.005 {
		pairs = append(pairs, [2]string{"lev", p.st.text.Render(Num(a.GrossNtl/a.Equity, 2) + "X")})
	}
	pairs = append(pairs, [2]string{"free", p.st.text.Render(USD(a.Withdrawable))})
	if n := len(a.OpenOrders); n > 0 {
		pairs = append(pairs, [2]string{"", p.st.text.Render(strconv.Itoa(n)) + " " + p.lbl(plural(n, "order", "orders"))})
	}
	if !p.o.Compact {
		pairs = append(pairs, [2]string{"perp", p.st.text.Render(USD(a.PerpEquity))})
		if a.SpotUSD != 0 {
			pairs = append(pairs, [2]string{"spot", p.st.text.Render(USD(a.SpotUSD))})
		}
		if a.EarnUSD != 0 {
			pairs = append(pairs, [2]string{"earn", p.st.text.Render(USD(a.EarnUSD))})
		}
	}
	return p.meta(pairs...)
}

// positions renders the positions table, largest notional first.
func (p *painter) positions(ps []domain.Position, width, limit int) []string {
	if len(ps) == 0 {
		return nil
	}
	sorted := append([]domain.Position(nil), ps...)
	sort.SliceStable(sorted, func(i, j int) bool { return math.Abs(sorted[i].Value) > math.Abs(sorted[j].Value) })
	n := min(len(sorted), limit)
	rows := make([][]cell, 0, n)
	for _, pos := range sorted[:n] {
		mark := pos.Mark
		if mark == 0 && pos.Size != 0 {
			mark = math.Abs(pos.Value / pos.Size)
		}
		lev := dash
		if pos.Leverage > 0 {
			lev = trimFloat(pos.Leverage, 1) + "x"
			if !pos.Cross {
				lev += " iso"
			}
		}
		liq, toLiq := p.cm(dash), p.cm(dash)
		if pos.LiqPx > 0 && mark > 0 {
			liq = p.c(Px(pos.LiqPx))
			d := math.Abs(mark-pos.LiqPx) / mark
			switch {
			case d < 0.10:
				toLiq = p.cst(glyph.Note+" "+Pct(d, 1), p.st.warn)
			case d >= 10:
				// one unit for the whole column (distance in percent); a
				// short liquidated only far above the mark is >999%, not
				// a fake-precise 28,433%
				toLiq = p.cm(">999%")
			case d >= 1:
				toLiq = p.cm(Pct(d, 0))
			default:
				toLiq = p.cm(Pct(d, 1))
			}
		}
		// size is signed: the sign says long or short (no side column)
		rows = append(rows, []cell{
			p.c(strings.TrimPrefix(pos.Coin, "xyz:")),
			// a short is not a loss (DESIGN 2 rule 3): ink, the sign
			// carries the direction
			p.c(signedSize(pos.Size)),
			p.c(USD(math.Abs(pos.Value))),
			p.cs(SignedUSD(pos.UPnL), pos.UPnL),
			p.cs(SignedPct(pos.ROE, 1), pos.ROE),
			toLiq,
			p.cm(Px(pos.Entry)),
			p.cm(Px(mark)),
			p.cm(lev),
			liq,
		})
	}
	// Inline: COIN SIZE VALUE UPNL ROE LIQ (LIQ = distance to liquidation).
	// The pane adds entry, mark, leverage and the liquidation price.
	cols := []column{
		{title: "coin", flex: true, minW: 5, keep: 20}, {title: "size", right: true, keep: 19},
		{title: "value", right: true, keep: 18}, {title: "upnl", right: true, keep: 17},
		{title: "ROE", right: true, keep: 15}, {title: "liq", right: true, keep: 14},
		{title: "entry", right: true, keep: 10}, {title: "mark", right: true, keep: 9},
		{title: "lev", right: true, keep: 8}, {title: "liq px", right: true, keep: 7},
	}
	if p.o.Compact {
		cols, rows = cols[:6], cutCells(rows, 6)
	}
	// Drop columns where every row is a dash (aggregated vault positions
	// carry no leverage or liquidation): columns of nothing.
	for ci := len(cols) - 1; ci >= 1; ci-- {
		empty := true
		for _, r := range rows {
			if r[ci].s != dash {
				empty = false
				break
			}
		}
		if empty {
			cols = append(cols[:ci:ci], cols[ci+1:]...)
			for i := range rows {
				rows[i] = append(rows[i][:ci:ci], rows[i][ci+1:]...)
			}
		}
	}
	return p.more(p.table(cols, rows, width, nil), len(sorted), n)
}

// signedSize is a size with its direction as the sign: +1.5, −717,362.
func signedSize(v float64) string {
	switch {
	case v > 0:
		return "+" + Size(v)
	case v < 0:
		return minus + Size(-v)
	}
	return Size(v)
}

// cutCells keeps the first n cells of every row.
func cutCells(rows [][]cell, n int) [][]cell {
	for i, r := range rows {
		if len(r) > n {
			rows[i] = r[:n]
		}
	}
	return rows
}

func (p *painter) orders(os []domain.Order) []string {
	n := min(len(os), p.rowLimit(10))
	rows := make([][]cell, 0, n)
	for _, o := range os[:n] {
		sz, st := "+"+Size(o.Sz), p.st.up
		if o.Side == domain.Sell {
			sz, st = minus+Size(o.Sz), p.st.down
		}
		typ := strings.ToUpper(o.Type)
		if typ == "" {
			typ = "LIMIT"
		}
		if o.ReduceOnly {
			typ += " RO"
		}
		px := Px(o.Px)
		if o.TriggerPx > 0 {
			px = Px(o.TriggerPx)
			typ += " TRIG"
		}
		rows = append(rows, []cell{p.c(strings.TrimPrefix(o.Coin, "xyz:")), p.cst(sz, st), p.c(px), p.cst(typ, p.st.label)})
	}
	cols := []column{
		{title: "orders", flex: true, minW: 6, keep: 9}, {title: "size", right: true, keep: 8}, {title: "px", right: true, keep: 7},
		{title: "type", keep: 3},
	}
	return p.more(p.table(cols, rows, p.w, nil), len(os), n)
}

// fillsCard: recent executions, realized PnL colored, fees muted.
func (p *painter) fillsCard(r *core.Result) card {
	fs, ok := as[[]domain.Fill](r.Data)
	if !ok {
		return p.fallbackCard(r)
	}
	c := card{title: "Fills"}
	if len(fs) == 0 {
		c.focal, c.focalStyle = "NO FILLS", p.st.muted
		return c
	}
	sorted := append([]domain.Fill(nil), fs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Time > sorted[j].Time })
	var pnl, fees float64
	for _, f := range fs {
		pnl += f.ClosedPnL
		fees += f.Fee
	}
	c.focal, c.focalStyle, c.numeric = SignedUSD(pnl), p.sign(pnl), true
	c.deltaLabel = "realized"
	n := min(len(sorted), p.rowLimit(20))
	rows := make([][]cell, 0, n)
	for _, f := range sorted[:n] {
		sz, st := "+"+Size(f.Sz), p.st.up
		if f.Side == domain.Sell {
			sz, st = minus+Size(f.Sz), p.st.down
		}
		if f.Liq {
			sz, st = glyph.Note+" "+sz, p.st.warn
		}
		pnlCell := p.cm(dash)
		if f.ClosedPnL != 0 {
			pnlCell = p.cs(SignedUSD(f.ClosedPnL), f.ClosedPnL)
		}
		dir := f.Dir
		if dir == "" {
			dir = "Buy"
			if f.Side == domain.Sell {
				dir = "Sell"
			}
		}
		rows = append(rows, []cell{
			p.cm(fillTime(f.Time, p.o.Location)), p.c(strings.TrimPrefix(f.Coin, "xyz:")),
			p.cst(sz, st), p.c(Px(f.Px)), pnlCell, p.cm(dir), p.cm(USD(f.Fee)),
		})
	}
	cols := []column{
		{title: "time", keep: 6}, {title: "coin", keep: 10}, {title: "size", right: true, keep: 9},
		{title: "px", right: true, keep: 8}, {title: "pnl", right: true, keep: 7},
		{title: "dir", flex: true, minW: 4, keep: 2}, {title: "fee", right: true, keep: 3},
	}
	if p.o.Compact {
		cols, rows = cols[:5], cutCells(rows, 5)
	}
	c.body = p.more(p.table(cols, rows, p.w, nil), len(sorted), n)
	c.body = append([]string{p.meta([2]string{"fees", p.st.text.Render(USD(fees))}, [2]string{"", p.st.text.Render(strconv.Itoa(len(fs))) + " " + p.lbl(plural(len(fs), "fill", "fills"))})}, c.body...)
	return c
}

func fillTime(ms int64, loc *time.Location) string {
	if ms <= 0 {
		return dash
	}
	return time.UnixMilli(ms).In(locOr(loc)).Format("Jan 2 15:04")
}
