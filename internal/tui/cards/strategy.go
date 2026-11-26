package cards

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"charm.land/glamour/v2"
	gansi "charm.land/glamour/v2/ansi"
	gstyles "charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"

	"github.com/maxmoneycash/hlp-cli/internal/chart"
	"github.com/maxmoneycash/hlp-cli/internal/core"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/hlpxyz"
	"github.com/maxmoneycash/hlp-cli/internal/theme"
	"github.com/maxmoneycash/hlp-cli/internal/tui/glyph"
)

// backtestCard: total return as the focal number, equity vs benchmarks,
// the traded market with buy/sell markers, metrics vs benchmarks.
func (p *painter) backtestCard(r *core.Result) card {
	d, ok := as[core.BacktestData](r.Data)
	if !ok {
		return p.fallbackCard(r)
	}
	ts := d.Tearsheet
	coin, mcs := backtestMarket(d)
	c := card{title: stratName(d.Strategy), sub: strings.TrimPrefix(coin, "xyz:"), warns: d.Warnings}
	if c.title == "" {
		c.title = r.Title
	}
	if len(ts.Curve) > 1 {
		c.focal, c.focalStyle, c.numeric = SignedPct(ts.Metrics.TotalReturn, 1), p.sign(ts.Metrics.TotalReturn), true
		if ts.Metrics.Days > 0 {
			c.deltaLabel = trimFloat(ts.Metrics.Days, 0) + "D"
		}
	}
	h := 14
	if p.o.Compact {
		h = 13 // at least 10 plot rows: a chart, not a strip
	}
	// The legend leads with the strategy itself (its line is the ink one),
	// then the benchmarks; the header keeps the traded market as its sub.
	subject := strings.ToUpper(c.title)
	if lipgloss.Width(subject) > 16 || subject == "" {
		subject = "STRATEGY"
	}
	c.body = append(c.body, p.curve(subject, ts.Curve, ts.Benchmarks, h, false)...)
	c.body = append(c.body, p.compactMetrics(ts.Metrics, domain.VaultSummary{}, ts.Benchmarks, false))
	if p.o.Compact {
		return c
	}
	if cs := mcs; len(cs) > 0 {
		// The whole backtest window, not its last screen: merge bars so
		// every fill lands on the chart.
		cs, per := fitCandles(cs, max((p.w-14)/2, 20))
		pairs := [][2]string{{"", p.lbl(strings.TrimPrefix(coin, "xyz:"))}, {"", p.st.text.Render(strconv.Itoa(len(d.Fills))) + " " + p.lbl("fills")}}
		if per > 1 {
			pairs = append(pairs, [2]string{"", p.st.text.Render(strconv.Itoa(per)) + " " + p.lbl("bars per candle")})
		}
		c.body = append(c.body, "", p.meta(pairs...))
		// The widest candles that still fit every one (merging rounds
		// the count down, so a fixed width leaves the left side empty).
		zoom := 1
		for _, z := range []int{6, 4, 2} {
			if len(cs)*z <= p.w-14 {
				zoom = z
				break
			}
		}
		g, _ := candlesFn(cs, chart.CandleOpts{
			Width: p.w, Height: 14, Style: p.o.ChartStyle, Skin: p.o.ChartSkin, Zoom: zoom, VolRows: 0, Cursor: -1,
			Markers: fillMarkers(cs, d.Fills, coin), Coin: coin, Location: p.o.Location,
		}, p.th)
		c.body = append(c.body, p.gridLines(g)...)
	}
	c.body = append(c.body, "", p.section("metrics"))
	c.body = append(c.body, p.metricsTable("strategy", ts.Metrics, ts.Benchmarks, p.w, true)...)
	if t := p.tradeStats(ts.Metrics); t != "" {
		c.body = append(c.body, t)
	}
	if len(ts.Monthly) > 0 {
		c.body = append(c.body, "", p.section("monthly %"))
		c.body = append(c.body, p.gridLines(heatmapFn(ts.Monthly, p.w, p.th))...)
	}
	return c
}

func (p *painter) tradeStats(m domain.Metrics) string {
	var pairs [][2]string
	if m.WinRateKnown || m.WinRate > 0 {
		pairs = append(pairs, [2]string{"win", p.st.text.Render(Pct(m.WinRate, 0))})
	}
	if m.ProfitFactorKnown || m.ProfitFactor > 0 {
		pairs = append(pairs, [2]string{"pf", p.st.text.Render(Num(m.ProfitFactor, 2))})
	}
	if m.BestMonth != 0 || m.WorstMonth != 0 {
		pairs = append(pairs, [2]string{"best", p.sign(m.BestMonth).Render(SignedPct(m.BestMonth, 1))},
			[2]string{"worst", p.sign(m.WorstMonth).Render(SignedPct(m.WorstMonth, 1))})
	}
	return p.meta(pairs...)
}

// stratName is a strategy's display name: a script file reads as its
// words ("ema-cross.pine" is EMA CROSS once the title upper-cases it).
func stratName(s string) string {
	for _, ext := range []string{".pine", ".js", ".ts", ".py"} {
		s = strings.TrimSuffix(s, ext)
	}
	if !strings.Contains(s, " ") {
		s = strings.NewReplacer("-", " ", "_", " ").Replace(s)
	}
	return s
}

// backtestMarket picks the market to chart: the one with the most fills.
func backtestMarket(d core.BacktestData) (string, []domain.Candle) {
	if len(d.Candles) == 0 {
		return "", nil
	}
	// the main leg: the coin with the most traded notional (a basket's
	// largest weight), not the one that happened to rebalance most often
	count := map[string]float64{}
	for _, f := range d.Fills {
		count[f.Coin] += math.Abs(f.Sz * f.Px)
	}
	keys := make([]string, 0, len(d.Candles))
	for k := range d.Candles {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if count[keys[i]] == count[keys[j]] {
			return keys[i] < keys[j]
		}
		return count[keys[i]] > count[keys[j]]
	})
	return keys[0], d.Candles[keys[0]]
}

// fillMarkers attaches each fill to the candle whose bucket contains it.
// fitCandles merges consecutive candles so at most n remain, returning the
// merged series and how many source bars each candle holds.
func fitCandles(cs []domain.Candle, n int) ([]domain.Candle, int) {
	if n <= 0 || len(cs) <= n {
		return cs, 1
	}
	per := (len(cs) + n - 1) / n
	out := make([]domain.Candle, 0, n)
	for i := 0; i < len(cs); i += per {
		j := min(i+per, len(cs))
		b := cs[i]
		for _, x := range cs[i+1 : j] {
			b.H, b.L, b.V = math.Max(b.H, x.H), math.Min(b.L, x.L), b.V+x.V
			b.C = x.C
		}
		out = append(out, b)
	}
	return out, per
}

func fillMarkers(cs []domain.Candle, fills []domain.Fill, coin string) []chart.Marker {
	if len(cs) == 0 {
		return nil
	}
	var out []chart.Marker
	for _, f := range fills {
		if f.Coin != coin {
			continue
		}
		i := sort.Search(len(cs), func(i int) bool { return cs[i].T > f.Time }) - 1
		if i < 0 {
			continue
		}
		out = append(out, chart.Marker{T: cs[i].T, Buy: f.Side == domain.Buy})
	}
	return out
}

// paperCard: a running paper session. Equity is the focal number.
func (p *painter) paperCard(r *core.Result) card {
	d, ok := as[core.PaperData](r.Data)
	if !ok {
		return p.fallbackCard(r)
	}
	c := card{title: stratName(d.Strategy), tagOwnLine: true}
	if !p.o.Compact {
		c.sub = d.ID
	}
	if c.title == "" {
		c.title = "Paper"
	}
	eq := d.Equity.Last()
	if eq == 0 {
		eq = d.Account.Equity
	}
	c.focal, c.focalStyle, c.numeric = USD(eq), p.st.bold, true
	if len(d.Equity) > 1 && d.Equity[0].V > 0 {
		ret := d.Equity.Last()/d.Equity[0].V - 1
		c.delta, c.deltaStyle = SignedPct(ret, 1), p.sign(ret)
	}
	status := p.st.dim.Render(glyph.Off + " STOPPED")
	if d.Running {
		status = p.st.text.Render(glyph.On) + " " + p.lbl("running")
	}
	if d.Running && d.Error != "" {
		status = p.st.warn.Render(glyph.Note + " FAILING")
	}
	c.body = append(c.body, status)
	if d.Error != "" {
		c.body = append(c.body, wrapLines([]string{p.st.down.Render(glyph.On) + " " + p.st.text.Render(d.Error)}, p.w)...)
		if h := errHint(d.Error); h != "" {
			c.body = append(c.body, "  "+p.st.dim.Render(h))
		}
	}
	if len(d.Equity) > 1 {
		h := 8
		if p.o.Compact {
			h = 6
		}
		g := lineFn(chart.LineOpts{Width: p.w, Height: h, Series: []chart.Named{{Name: "equity", Series: d.Equity}},
			Area: true, Baseline: d.Equity[0].V, Location: p.o.Location}, p.th)
		c.body = append(c.body, p.gridLines(g)...)
	}
	if ps := p.positions(d.Account.Positions, p.w, p.rowLimit(10)); len(ps) > 0 {
		c.body = append(c.body, ps...)
		c.tagOwnLine = false
	}
	if len(d.Fills) > 0 && !p.o.Compact {
		last := d.Fills[len(d.Fills)-1]
		sz := "+" + Size(last.Sz)
		if last.Side == domain.Sell {
			sz = minus + Size(last.Sz)
		}
		c.body = append(c.body, p.meta([2]string{"last fill", p.st.text.Render(sz + " " + last.Coin + " @ " + Px(last.Px))},
			[2]string{"", p.st.muted.Render(fillTime(last.Time, p.o.Location))}))
	}
	return c
}

// arenaCard: the leaderboard. The leader's return is the focal number.
func (p *painter) arenaCard(r *core.Result) card {
	d, ok := as[core.ArenaData](r.Data)
	if !ok {
		return p.fallbackCard(r)
	}
	c := card{title: "Arena"}
	es := append([]core.ArenaEntrant(nil), d.Entrants...)
	// An entrant still waiting for its first bar (or failed) has no return
	// yet: it ranks below every running one and never leads.
	// Stats need 2 marks: before that a return, Sharpe and drawdown are
	// placeholders (0.00 / 0.0%) that read as broken, so they show "·".
	running := func(e core.ArenaEntrant) bool { return !e.Waiting && e.Error == "" && len(e.Equity) >= 2 }
	sort.SliceStable(es, func(i, j int) bool {
		if ri, rj := running(es[i]), running(es[j]); ri != rj {
			return ri
		}
		return es[i].Metrics.TotalReturn > es[j].Metrics.TotalReturn
	})
	if len(es) == 0 {
		c.focal, c.focalStyle = "NO ENTRANTS", p.st.muted
		return c
	}
	// Header: ARENA / LEADER NAME  +0.04%. A pane draws the leader's
	// return as the big number, with the name after it.
	if running(es[0]) {
		lead := es[0].Metrics.TotalReturn
		// row 1 names the leader: the header says only its return
		c.focal, c.focalStyle, c.deltaLabel = SignedPct(lead, 1), p.sign(lead), "leader"
		c.hero, c.heroSmall = SignedPct(lead, 1), termSafe(es[0].Name)
	} else {
		c.focal, c.focalStyle = "WAITING", p.st.dim
	}
	rows := make([][]cell, 0, len(es))
	for i, e := range es {
		vals := e.Equity.Values()
		sp := p.cm("")
		if len(vals) > 1 {
			sp = p.cst(lineSpark(vals, 12), p.st.muted)
		}
		ret := p.cs(SignedPct(e.Metrics.TotalReturn, 1), e.Metrics.TotalReturn)
		sharpe := p.c(Num(e.Metrics.Sharpe, 2))
		dd := p.cst(SignedPct(e.Metrics.MaxDD, 1), p.ddStyle(e.Metrics.MaxDD))
		if !running(e) {
			// no marks yet: dim dots, never a fake 0.00 / 0.0%
			ret, sharpe, dd = p.cst("·", p.st.dim), p.cst("·", p.st.dim), p.cst("·", p.st.dim)
		}
		switch {
		case e.Error != "":
			ret = p.cst(glyph.On+" ERROR", p.st.down)
			c.notes = append(c.notes, p.st.down.Render(glyph.On)+" "+p.st.text.Render(termSafe(e.Name)+": "+e.Error)+p.st.dim.Render(errHint(e.Error)))
		case e.Waiting:
			ret = p.cst("WAITING", p.st.dim)
		}
		rows = append(rows, []cell{
			p.cm(strconv.Itoa(i + 1)), p.c(e.Name), ret,
			sharpe, dd, sp, p.cst(strings.ToUpper(e.Kind), p.st.label),
		})
	}
	cols := []column{
		{title: "#", right: true, keep: 50}, {title: "entrant", flex: true, minW: 10, keep: 100},
		{title: "return", right: true, keep: 90}, {title: "sharpe", right: true, keep: 40}, {title: "maxdd", right: true, keep: 45},
		{title: "equity", keep: 30}, {title: "kind", keep: 20},
	}
	if p.o.Compact {
		cols, rows = cols[:5], cutCells(rows, 5)
	}
	n := min(len(rows), p.rowLimit(len(rows)))
	c.body = append(p.arenaRace(es, running), p.more(p.table(cols, rows[:n], p.w, nil), len(rows), n)...)
	if note := strings.TrimSpace(es[0].LastNote); note != "" && es[0].Error == "" {
		c.notes = append(c.notes, p.lbl("leader note")+" "+p.st.muted.Render(termSafe(note)))
	}
	return c
}

// arenaRace draws the race: one equity line per running entrant, rebased
// to its start, the leader in ink and the field muted (the landing's BOT
// ARENA), above the table. Nothing until two entrants have 2 marks: a
// race of one line is a chart, not a race.
func (p *painter) arenaRace(es []core.ArenaEntrant, running func(core.ArenaEntrant) bool) []string {
	var series []chart.Named
	for _, e := range es {
		if !running(e) {
			continue
		}
		col := p.th.Muted
		if len(series) == 0 {
			col = p.th.Text
		}
		series = append(series, chart.Named{Name: shortName(termSafe(e.Name)), Series: e.Equity, Col: col})
	}
	if len(series) < 2 || p.o.Plain && p.o.Compact {
		return nil
	}
	h := 10
	if p.o.Compact {
		h = 8
	}
	if p.o.Height > 0 {
		// Full screen: the race takes the room the leaderboard leaves
		// (its header row, one row per entrant, the card's header and
		// notes, and the legend and axis of the chart itself).
		h = max(h, p.o.Height-len(es)-10)
	}
	g := lineFn(chart.LineOpts{Width: p.w, Height: h, Series: series, Percent: true, Baseline: 1,
		Location: p.o.Location}, p.th)
	return append(p.gridLines(g), "")
}

// errHint adds a fix to errors whose cause is usually local setup.
func errHint(err string) string {
	if strings.Contains(err, "cannot reach the model server") || strings.Contains(err, "timeout connecting to the model server") {
		return "  ollama serve"
	}
	return ""
}

// orderCard: the order preview / dry-run result. Network and DRY RUN
// badges sit on the first line so nobody mistakes a preview for a fill.
func (p *painter) orderCard(r *core.Result) card {
	d, ok := as[core.OrderPreview](r.Data)
	if !ok {
		return p.fallbackCard(r)
	}
	return p.orderPreview(d, r.Title)
}

// OrderPreview renders the preview used by the TUI confirm card.
func OrderPreview(d core.OrderPreview, o Opts) string {
	o = o.normalized()
	p := newPainter(o)
	c := p.orderPreview(d, "")
	c.noFooter = true
	// The confirm card is the most security-critical screen: run the same
	// last-line escape filter as every Render output (SECURITY S3).
	return p.finish(p.frame(c, domain.Meta{}))
}

func (p *painter) orderPreview(d core.OrderPreview, title string) card {
	c := card{title: title}
	if c.title == "" {
		c.title = orderTitle(d)
	}
	if d.NotionalUSD > 0 {
		c.focal, c.focalStyle, c.numeric = USD(d.NotionalUSD), p.st.bold, true
	}
	// Network and DRY RUN sit beside the title so nobody mistakes a
	// preview for a fill: MAINNET is a gold pill on a live mainnet order,
	// TESTNET a quiet one.
	var badges []string
	switch {
	case d.Network == domain.Testnet:
		badges = append(badges, p.badge("TESTNET", false))
	case !d.DryRun:
		badges = append(badges, p.badge("MAINNET", true))
	}
	if d.DryRun {
		badges = append(badges, p.badge("DRY RUN", false))
	}
	c.pill = strings.Join(badges, " ")
	var kv [][2]string
	if d.Coin != "" {
		kv = append(kv, [2]string{"market", d.Coin})
	}
	if d.Side != "" {
		side := "buy"
		if d.Side == domain.Sell {
			side = "sell"
		}
		kv = append(kv, [2]string{"side", side})
	}
	if d.Size != "" {
		kv = append(kv, [2]string{"size", d.Size})
	}
	if d.Action == "order" || d.Px != "" {
		px := d.Px
		if px == "" {
			px = "market (IOC)"
		}
		kv = append(kv, [2]string{"price", px})
	}
	for _, t := range d.Triggers {
		label := "take profit"
		if t.Kind == "sl" {
			label = "stop loss"
		}
		v := t.TriggerPx + " · " + t.Size + " reduce-only, market"
		if t.Through {
			v += " · now"
		}
		kv = append(kv, [2]string{label, v})
	}
	if d.Vault != "" {
		kv = append(kv, [2]string{"vault", ShortAddr(d.Vault)})
	}
	c.body = append(c.body, p.kvLines(kv, p.w)...)
	// The confirm screen keeps its warnings whole: they are the reason to
	// stop, not chrome.
	for _, w := range d.Warnings {
		c.body = append(c.body, wrapLines([]string{p.st.warn.Render(glyph.Note+" ") + p.st.text.Render(w)}, p.w)...)
	}
	if len(d.Checks) > 0 {
		ck := p.checks(d.Checks, p.w, false)
		c.body = append(c.body, ck[1:]...) // risk checks speak for themselves; no section label
	}
	switch {
	case d.DryRun && len(d.Envelope) > 0:
		c.body = append(c.body, p.lbl("not sent")+p.st.muted.Render("   "+strconv.Itoa(len(d.Envelope))+" bytes unsigned"))
	case d.DryRun:
		c.body = append(c.body, p.lbl("preview only"))
	case len(d.Response) > 0:
		c.body = append(c.body, p.st.text.Render(glyph.On)+" "+p.lbl("sent")+"   "+p.st.muted.Render(compactJSON(d.Response, p.w-12)))
	}
	return c
}

func orderTitle(d core.OrderPreview) string {
	switch d.Action {
	case "order", "":
		side := "Buy"
		if d.Side == domain.Sell {
			side = "Sell"
		}
		t := side
		if d.Size != "" {
			t += " " + d.Size
		}
		return strings.TrimSpace(t + " " + d.Coin)
	case "cancel":
		return strings.TrimSpace("Cancel " + d.Coin)
	case "vault_deposit":
		return "Deposit into vault"
	case "vault_withdraw":
		return "Withdraw from vault"
	case "close_all":
		return "Close all positions"
	case "leverage":
		return strings.TrimSpace("Leverage " + d.Coin)
	}
	return d.Action
}

func compactJSON(raw json.RawMessage, width int) string {
	s := strings.Join(strings.Fields(string(raw)), " ")
	if r := []rune(s); len(r) > width && width > 1 {
		s = string(r[:width-1]) + "…"
	}
	return s
}

// fundCard: an hlp.xyz protocol strategy vault (HyperEVM). NAV is focal.
func (p *painter) fundCard(r *core.Result) card {
	d, ok := as[hlpxyz.PVaultStatus](r.Data)
	if !ok {
		return p.fallbackCard(r)
	}
	st := d.LastKeeperRun.State
	c := card{title: r.Title}
	if !p.o.Compact {
		c.sub = ShortAddr(d.Vault)
	}
	if c.title == "" {
		c.title = "Strategy vault"
	}
	if d.ChainID == 998 {
		c.pill = p.badge("TESTNET", true)
	}
	nav := st.NAV
	if nav == 0 {
		nav = d.LastBotRun.NavUSD
	}
	c.focal, c.focalStyle, c.numeric = USD(nav), p.st.bold, true
	if st.SharePrice > 0 {
		c.delta, c.deltaStyle = Num(st.SharePrice, 4), p.st.text
	}
	mode := "live"
	if d.LastBotRun.Paper || d.Paper {
		mode = "paper"
	}
	if p.o.Compact {
		c.body = append(c.body, p.meta(
			[2]string{"", p.lbl(strings.ToLower(st.Phase))},
			[2]string{"cutoff", p.st.text.Render(strings.TrimPrefix(cutoff(st.SecondsToCutoff), " · cutoff in "))},
			[2]string{"gross", p.st.text.Render(Num(d.LastBotRun.GrossX, 2) + "X")},
			[2]string{"", p.lbl(mode)},
		))
		return c
	}
	chain := "HyperEVM " + strconv.Itoa(d.ChainID)
	if d.ChainID == 998 {
		chain += " (testnet)"
	}
	kv := [][2]string{
		{"chain", chain},
		{"phase", strings.ToLower(st.Phase) + cutoff(st.SecondsToCutoff)},
		{"idle", USD(st.Idle)},
		{"pending", USD(st.PendingDeposits) + " in · " + Num(st.PendingRedeemShare, 2) + " shares out"},
		{"core equity", USD(st.CorePerpEquity) + " · withdrawable " + USD(st.CoreWithdrawable)},
		{"bot", mode + " · gross " + Num(d.LastBotRun.GrossX, 2) + "x"},
	}
	if d.LastBotRun.Agent.Registered {
		kv = append(kv, [2]string{"agent", trimFloat(d.LastBotRun.Agent.DaysLeft, 0) + " days left"})
	}
	if d.LastKeeperRun.At > 0 {
		kv = append(kv, [2]string{"keeper", "ran " + fillTime(d.LastKeeperRun.At*1000, p.o.Location)})
	}
	c.body = append(c.body, p.kvLines(kv, p.w)...)
	if len(d.LastBotRun.Book) > 0 {
		rows := make([][]cell, 0, len(d.LastBotRun.Book))
		for _, b := range d.LastBotRun.Book {
			dir, s := "FLAT", p.st.muted
			switch {
			case b.Dir > 0:
				dir, s = "LONG", p.st.up
			case b.Dir < 0:
				dir, s = "SHORT", p.st.down
			}
			rows = append(rows, []cell{p.c(b.Coin), p.cst(dir, s), p.c(strconv.Itoa(b.Units)), p.c(Px(b.Mark)), p.cm(Px(b.Stop)), p.cm(Px(b.Target))})
		}
		cols := []column{
			{title: "book", flex: true, minW: 5, keep: 100}, {title: "dir", keep: 90}, {title: "units", right: true, keep: 50},
			{title: "mark", right: true, keep: 80}, {title: "stop", right: true, keep: 70}, {title: "target", right: true, keep: 60},
		}
		c.body = append(c.body, "")
		c.body = append(c.body, p.table(cols, rows, p.w, nil)...)
	}
	return c
}

func cutoff(sec int) string {
	if sec <= 0 {
		return ""
	}
	return " · cutoff in " + (time.Duration(sec) * time.Second).String()
}

// mdRenderer is one cached glamour renderer. Building one costs ~30 µs and
// 39 KB (PERFORMANCE #2); its ANSI renderer keeps per-render state, so
// renders through one instance are serialized.
type mdRenderer struct {
	mu sync.Mutex
	tr *glamour.TermRenderer
}

type mdKey struct {
	w     int
	dark  bool
	theme string
}

var mdRenderers sync.Map // mdKey -> *mdRenderer

// mdWidth is the widest agent prose line (DESIGN.md section 6.1).
const mdWidth = 72

func markdownRenderer(w int, th theme.Theme) *mdRenderer {
	k := mdKey{w, th.Dark, th.Name}
	if r, ok := mdRenderers.Load(k); ok {
		return r.(*mdRenderer)
	}
	cfg := gstyles.DarkStyleConfig
	if !th.Dark {
		cfg = gstyles.LightStyleConfig
	}
	zero := uint(0)
	cfg.Document.Margin = &zero
	cfg.Document.BlockPrefix = ""
	cfg.Document.BlockSuffix = ""
	cfg.Paragraph = gansi.StyleBlock{}
	// Rush prose (DESIGN.md section 5.2, Agent replies): muted text, so a
	// reply never competes with the card above it; headings no louder
	// than bold ink, inline code and strong text in ink with no
	// background, links underlined ink, list items with a plain dash.
	ink := hexOf(th.Text)
	cfg.Document.Color = hexOf(th.Muted)
	yes := true
	cfg.Strong = gansi.StylePrimitive{Color: ink, Bold: &yes}
	bold := gansi.StyleBlock{StylePrimitive: gansi.StylePrimitive{Color: ink, Bold: &yes}}
	cfg.Heading = gansi.StyleBlock{StylePrimitive: gansi.StylePrimitive{Color: ink, Bold: &yes, BlockSuffix: "\n"}}
	cfg.H1, cfg.H2, cfg.H3, cfg.H4, cfg.H5, cfg.H6 = bold, bold, bold, bold, bold, bold
	cfg.Code = gansi.StyleBlock{StylePrimitive: gansi.StylePrimitive{Color: ink}}
	cfg.Link = gansi.StylePrimitive{Color: ink, Underline: &yes}
	cfg.LinkText = gansi.StylePrimitive{Color: ink}
	cfg.Item = gansi.StylePrimitive{BlockPrefix: "- "}
	tr, err := glamour.NewTermRenderer(glamour.WithStyles(cfg), glamour.WithWordWrap(w))
	if err != nil {
		return nil
	}
	r, _ := mdRenderers.LoadOrStore(k, &mdRenderer{tr: tr})
	return r.(*mdRenderer)
}

// hexOf is a theme colour as the "#rrggbb" glamour wants (nil when unset).
func hexOf(c theme.Color) *string {
	if !c.True.Valid {
		return nil
	}
	s := c.True.String()
	return &s
}

// WrapPlain soft-wraps unrendered text to width with terminal controls
// stripped: the streaming tail the TUI shows between markdown renders.
func WrapPlain(s string, width int) []string {
	s = domain.CleanText(s)
	if s == "" {
		return nil
	}
	return wrapLines(strings.Split(strings.TrimRight(s, "\n"), "\n"), max(width, 8))
}

// strategyCard: an hlp.xyz quant strategy's live target book plus a
// one-line backtest summary (core.StrategyData, what get_strategy
// returns), the strategy list (`hlp strat` with no id), or a bare
// hlpxyz.QuantLive from older transcripts.
func (p *painter) strategyCard(r *core.Result) card {
	var static *hlpxyz.QuantStatic
	d, ok := as[hlpxyz.QuantLive](r.Data)
	if sd, isSD := as[core.StrategyData](r.Data); isSD {
		if len(sd.Strategies) > 0 {
			return p.strategyListCard(r, sd.Strategies)
		}
		d, static, ok = sd.Live, sd.Static, true
	}
	if !ok {
		return p.fallbackCard(r)
	}
	c := card{title: r.Title}
	if !p.o.Compact {
		c.sub = d.Source
	}
	if c.title == "" {
		c.title = d.ID
	}
	c.focal, c.focalStyle, c.numeric = Num(d.Gross, 2)+"X", p.st.bold, true
	c.deltaLabel = "gross"
	type pos struct {
		coin, label string
		w           float64
	}
	ps := make([]pos, 0, len(d.Positions))
	net, peak := 0.0, 0.0
	for _, x := range d.Positions {
		ps = append(ps, pos{x.Coin, x.Label, x.Weight})
		net += x.Weight
		peak = math.Max(peak, math.Abs(x.Weight))
	}
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].w > ps[j].w })
	barW := max(min(p.w-40, 24), 0)
	n := min(len(ps), p.rowLimit(len(ps)))
	rows := make([][]cell, 0, n)
	for _, x := range ps[:n] {
		label := x.coin
		if x.label != "" && x.label != x.coin && !p.o.Compact {
			// Plain text: the table strips escape codes from cells, so a
			// pre-styled label would print its colour codes as text.
			label += " " + x.label
		}
		row := []cell{p.c(label), p.cs(fmtWeight(x.w), x.w)}
		if barW > 0 && peak > 0 {
			row = append(row, p.cs(meter(math.Abs(x.w)/peak, barW), x.w))
		}
		rows = append(rows, row)
	}
	cols := []column{{title: "book", flex: true, minW: 6, keep: 100}, {title: "weight", right: true, keep: 90}}
	if barW > 0 && peak > 0 {
		cols = append(cols, column{title: "", keep: 10})
	}
	// Meta: NET +12.0%   CAGR +31.2%   MAX DD −18.4%   SHARPE 1.21
	// (backtest, full period; the pane adds the start and out-of-sample).
	pairs := [][2]string{{"net", p.sign(net).Render(fmtWeight(net))}}
	if q, ok := core.QuantPeriodOf(static, "all"); ok {
		pairs = append(pairs, [2]string{"CAGR", p.sign(q.CAGR).Render(SignedPct(q.CAGR, 1))},
			[2]string{"max dd", p.ddStyle(-math.Abs(q.MaxDD)).Render(SignedPct(-math.Abs(q.MaxDD), 1))},
			[2]string{"sharpe", p.st.text.Render(Num(q.Sharpe, 2))})
		if !p.o.Compact {
			if q.Start != "" {
				c.notes = append(c.notes, p.lbl("since")+" "+p.st.muted.Render(q.Start))
			}
			if oos, ok := core.QuantPeriodOf(static, "oos"); ok {
				c.notes = append(c.notes, p.lbl("oos CAGR")+" "+p.sign(oos.CAGR).Render(SignedPct(oos.CAGR, 1)))
			}
		}
	}
	c.body = append([]string{p.meta(pairs...)}, p.more(p.table(cols, rows, p.w, nil), len(ps), n)...)
	if d.ComputedAt > 0 && !p.o.Compact {
		c.notes = append(c.notes, p.lbl("computed")+" "+p.st.muted.Render(fillTime(d.ComputedAt, p.o.Location)))
	}
	return c
}

// strategyListCard: every quant strategy with its full-period backtest.
func (p *painter) strategyListCard(r *core.Result, rows []core.StrategySummary) card {
	c := card{title: "Strategies", focal: strconv.Itoa(len(rows)), focalStyle: p.st.bold}
	cells := make([][]cell, 0, len(rows))
	for _, x := range rows {
		name := x.Name
		if name == "" {
			name = x.ID
		}
		if !x.OK {
			cells = append(cells, []cell{p.c(x.ID), p.cm(name), p.cm(dash), p.cm(dash), p.cm(dash)})
			continue
		}
		dd := -math.Abs(x.MaxDD)
		cells = append(cells, []cell{p.c(x.ID), p.cm(name), p.cs(SignedPct(x.CAGR, 1), x.CAGR), p.cst(SignedPct(dd, 1), p.ddStyle(dd)), p.c(Num(x.Sharpe, 2))})
	}
	cols := []column{
		{title: "id", keep: 100}, {title: "name", flex: true, minW: 8, keep: 40},
		{title: "cagr", right: true, keep: 90}, {title: "maxdd", right: true, keep: 70}, {title: "sharpe", right: true, keep: 80},
	}
	n := min(len(cells), p.rowLimit(len(cells)))
	c.body = p.more(p.table(cols, cells[:n], p.w, nil), len(cells), n)
	return c
}

// kvCard: aligned key/value pairs (asset-id, schema summaries).
func (p *painter) kvCard(r *core.Result) card {
	pairs, ok := as[[][2]string](r.Data)
	if pr, isPairs := r.Data.(interface{ Pairs() [][2]string }); !ok && isPairs {
		pairs, ok = pr.Pairs(), true
	}
	if !ok {
		if m, ok2 := r.Data.(map[string]string); ok2 {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				pairs = append(pairs, [2]string{k, m[k]})
			}
		} else {
			return p.fallbackCard(r)
		}
	}
	return card{title: r.Title, body: p.kvLines(pairs, p.w)}
}

// textCard: markdown through glamour (plain text when Plain).
func (p *painter) textCard(r *core.Result) card {
	s, ok := as[string](r.Data)
	if !ok {
		return p.fallbackCard(r)
	}
	return card{title: r.Title, body: Markdown(s, p.o)}
}

// Markdown renders markdown at the card's inner width, with no outer
// margin (the card provides the inset). Plain mode keeps the source text,
// soft-wrapped, which is what agents and pipes want.
//
// The text is model output or other third-party prose, so terminal
// controls are stripped first (SECURITY S3).
func Markdown(s string, o Opts) []string {
	o = o.normalized()
	s = domain.CleanText(s)
	w := min(o.Width-2*Inset, mdWidth)
	if o.Plain {
		return wrapLines(strings.Split(strings.TrimRight(s, "\n"), "\n"), w)
	}
	r := markdownRenderer(w, o.Theme)
	if r == nil {
		return wrapLines(strings.Split(s, "\n"), w)
	}
	r.mu.Lock()
	out, err := r.tr.Render(s)
	r.mu.Unlock()
	if err != nil {
		return wrapLines(strings.Split(s, "\n"), w)
	}
	lines := strings.Split(out, "\n")
	for len(lines) > 0 && strings.TrimSpace(stripANSI(lines[0])) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(stripANSI(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return lines
}
