package macro

import (
	"context"
	"encoding/json"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---- Hyperliquid HIP-4 ----

// macroRe picks HIP-4 questions about the macro regime.
var macroRe = regexp.MustCompile(`(?i)\b(fed|fomc|rate cut|rate hike|interest rate|cpi|inflation|recession|gdp|unemployment|payrolls|tariff)\b`)

// maxHIP4 bounds the HIP-4 signals kept per refresh; perPerp bounds the
// price questions kept per perp (HIP-4 lists many short-dated strikes).
const (
	maxHIP4 = 24
	perPerp = 2
	// minHorizon drops price questions that settle sooner than this: a
	// 10-minute strike says nothing about an hourly decision.
	minHorizon = time.Hour
)

// hip4Signals keeps binary price questions on Hyperliquid perps (per perp,
// the perPerp latest-settling strikes at least minHorizon out) and macro
// questions; sports and other templates are dropped. The YES side's mid is
// the implied probability.
func hip4Signals(os []Outcome, now time.Time) []Signal {
	type cand struct {
		s   Signal
		due string
	}
	var price, other []cand
	for _, o := range os {
		kv := parseKV(o.Description)
		name := strings.TrimPrefix(o.Name, "template:")
		yes, ok := o.Mids[0]
		if !ok || yes <= 0 || yes >= 1 || math.IsNaN(yes) {
			continue
		}
		coin := "#" + strconv.Itoa(10*o.ID)
		switch {
		case name == "binaryPrice" && kv["perp"] != "" && kv["threshold"] != "":
			if t, err := time.Parse("20060102-1504", kv["time"]); err == nil && t.Sub(now) < minHorizon {
				continue
			}
			q := kv["perp"] + " above " + kv["threshold"]
			if t := kvTime(kv["time"]); t != "" {
				q += " at " + t
			}
			price = append(price, cand{Signal{Source: SourceHyperliquid, Theme: clean(kv["perp"], 30) + " price", Question: clean(q, maxQuestion),
				Prob: yes, Coin: clean(kv["perp"], 30), Market: coin}, kv["time"]})
		case strings.HasPrefix(name, "sports"):
			continue
		case macroRe.MatchString(o.Name + " " + o.Description):
			q := kv["question"]
			if q == "" {
				q = strings.TrimSpace(name + " " + kv["subject"])
			}
			other = append(other, cand{Signal{Source: SourceHyperliquid, Theme: "Macro", Question: clean(q, maxQuestion), Prob: yes, Market: coin}, kv["time"]})
		}
	}
	// Latest-settling strikes first, at most perPerp per perp.
	sort.SliceStable(price, func(i, j int) bool { return price[i].due > price[j].due })
	n := map[string]int{}
	var cs []cand
	for _, c := range price {
		if n[c.s.Coin] < perPerp {
			n[c.s.Coin]++
			cs = append(cs, c)
		}
	}
	cs = append(cs, other...)
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].due < cs[j].due }) // soonest first
	out := make([]Signal, 0, min(len(cs), maxHIP4))
	for i := 0; i < len(cs) && i < maxHIP4; i++ {
		out = append(out, cs[i].s)
	}
	return out
}

func parseKV(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, "|") {
		if k, v, ok := strings.Cut(part, ":"); ok {
			out[k] = v
		}
	}
	return out
}

// kvTime renders "20261120-2100" as "2026-11-20 21:00 UTC".
func kvTime(s string) string {
	t, err := time.Parse("20060102-1504", s)
	if err != nil {
		return ""
	}
	return t.Format("2006-01-02 15:04") + " UTC"
}

// ---- choosing a market ----

// A prediction market earns a place in a perps bot's prompt when three
// things hold: the crowd is still unsure, people are trading it now, and it
// settles soon enough to matter for positions held hours to days. score
// multiplies one factor for each, so a market that fails any of them
// scores near zero:
//
//	doubt   = 4p(1-p)            1 at 50%, 0.36 at 10% or 90%, 0 at certainty
//	tape    = log(1 + volume24h) 0 when nothing traded in the last day
//	horizon = 1 / (1 + days/30)  half weight a month out
//
// The log keeps one heavily traded strike from drowning out a closer call.
// Markets with no volume in the last day are not scored at all: their
// price is an old print, not today's odds.
const (
	// maxHorizon drops markets that settle too far out to move a perps
	// book this week.
	maxHorizon = 180 * 24 * time.Hour
	// unknownDays is the horizon assumed when a venue gives no date.
	unknownDays = 30.0
)

// score rates one market (prob 0..1, 24h volume, settlement time; zero
// settles = unknown) at now. ok is false when the market is not eligible.
func score(prob, vol24h float64, settles, now time.Time) (float64, bool) {
	if !(prob > 0 && prob < 1) || !(vol24h > 0) {
		return 0, false
	}
	days := unknownDays
	if !settles.IsZero() {
		left := settles.Sub(now)
		if left < minHorizon || left > maxHorizon {
			return 0, false
		}
		days = left.Hours() / 24
	}
	return 4 * prob * (1 - prob) * math.Log1p(vol24h) / (1 + days/30), true
}

// parseTime reads an RFC 3339 timestamp ("" or junk = zero).
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t
}

// ---- Polymarket (gamma-api) ----

// polyTopic is one Polymarket theme: the tag to list, a pattern an event
// or market must match to count, and an optional pattern that rules it
// out (other central banks under the rates tag, for example).
type polyTopic struct {
	tag, theme string
	want, skip *regexp.Regexp
}

// polyTopics cover what moves Hyperliquid's books from outside: US rate
// policy (crypto and the HIP-3 equity perps both trade it), the two largest
// crypto assets' own price markets, and US trade policy, which has driven
// the index perps' sharpest days.
var polyTopics = []polyTopic{
	{tag: "fed-rates", theme: "FOMC", want: regexp.MustCompile(`(?i)\bfed\b|fomc|interest rate|rate (cut|hike)`),
		skip: regexp.MustCompile(`(?i)\b(ecb|boe|boj|rba|snb|bank of (england|japan|canada))\b`)},
	{tag: "crypto", theme: "BTC odds", want: regexp.MustCompile(`(?i)\bbitcoin\b|\bbtc\b`), skip: coinFlipRe},
	{tag: "crypto", theme: "ETH odds", want: regexp.MustCompile(`(?i)\bethereum\b|\beth\b`), skip: coinFlipRe},
	{tag: "tariffs", theme: "US tariffs", want: regexp.MustCompile(`(?i)tariff|duty rate`)},
}

// coinFlipRe matches short "up or down" price markets: they open at 50%
// by construction, so their odds say nothing the chart does not.
var coinFlipRe = regexp.MustCompile(`(?i)up or down`)

type polyMarket struct {
	Question          string          `json:"question"`
	Slug              string          `json:"slug"`
	EndDate           string          `json:"endDate"`
	Outcomes          json.RawMessage `json:"outcomes"`      // JSON string '["Yes","No"]' or array
	OutcomePrices     json.RawMessage `json:"outcomePrices"` // JSON string '["0.72","0.28"]' or array
	OneDayPriceChange *float64        `json:"oneDayPriceChange"`
	Volume24hr        float64         `json:"volume24hr"`
}

type polyEvent struct {
	Title   string       `json:"title"`
	Slug    string       `json:"slug"`
	EndDate string       `json:"endDate"`
	Markets []polyMarket `json:"markets"`
}

func (c *Client) polymarket(ctx context.Context) ([]Signal, error) {
	byTag := map[string][]polyEvent{}
	var firstErr error
	ok := 0
	for _, tp := range polyTopics {
		if _, done := byTag[tp.tag]; done {
			continue
		}
		q := url.Values{"closed": {"false"}, "limit": {"20"}, "order": {"volume24hr"}, "ascending": {"false"}, "tag_slug": {tp.tag}}
		var evs []polyEvent
		if err := c.getJSON(ctx, c.o.PolymarketURL+"/events?"+q.Encode(), &evs); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if ctx.Err() != nil {
				break
			}
			continue
		}
		ok++
		byTag[tp.tag] = evs
	}
	if ok == 0 && firstErr != nil {
		return nil, firstErr
	}
	now := c.o.Now()
	var out []Signal
	for _, tp := range polyTopics {
		if s, found := polyBest(byTag[tp.tag], tp, now); found {
			out = append(out, s)
		}
	}
	return out, nil
}

// polyBest returns the highest-scoring market of the topic's events.
func polyBest(evs []polyEvent, tp polyTopic, now time.Time) (Signal, bool) {
	var best *polyMarket
	var bestEv polyEvent
	var bestP, bestScore float64
	for _, e := range evs {
		for i := range e.Markets {
			m := &e.Markets[i]
			text := e.Title + " " + m.Question
			if !tp.want.MatchString(text) || (tp.skip != nil && tp.skip.MatchString(text)) {
				continue
			}
			p, ok := yesProb(*m)
			if !ok {
				continue
			}
			end := parseTime(m.EndDate)
			if end.IsZero() {
				end = parseTime(e.EndDate)
			}
			sc, ok := score(p, m.Volume24hr, end, now)
			if ok && sc > bestScore {
				best, bestEv, bestP, bestScore = m, e, p, sc
			}
		}
	}
	if best == nil {
		return Signal{}, false
	}
	q := best.Question
	if q == "" {
		q = bestEv.Title
	}
	s := Signal{Source: SourcePolymarket, Theme: tp.theme, Question: clean(q, maxQuestion), Prob: bestP,
		Volume24h: best.Volume24hr, Market: clean(best.Slug, 80)}
	if d := best.OneDayPriceChange; d != nil && math.Abs(*d) < 1 {
		v := roundPP(*d)
		s.Move24h = &v
	}
	return s, true
}

// yesProb is a binary market's YES probability (0..1).
func yesProb(m polyMarket) (float64, bool) {
	prices := strList(m.OutcomePrices)
	if len(prices) == 0 {
		return 0, false
	}
	idx := 0
	for i, o := range strList(m.Outcomes) {
		if strings.EqualFold(strings.TrimSpace(o), "yes") {
			idx = i
			break
		}
	}
	if idx >= len(prices) {
		return 0, false
	}
	p, err := strconv.ParseFloat(strings.TrimSpace(prices[idx]), 64)
	if err != nil || !(p > 0 && p < 1) {
		return 0, false
	}
	return p, true
}

// strList decodes a JSON array, or a JSON string holding one (gamma-api
// encodes outcomes and outcomePrices as strings).
func strList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = json.RawMessage(s)
	}
	var arr []any
	if json.Unmarshal(raw, &arr) != nil {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		switch x := v.(type) {
		case string:
			out = append(out, x)
		case float64:
			out = append(out, strconv.FormatFloat(x, 'f', -1, 64))
		default:
			out = append(out, "")
		}
	}
	return out
}

// ---- Kalshi (public market data) ----

// kalshiSeries are the scheduled US releases that reprice rates, and with
// them crypto and the equity perps, on the day they land: the FOMC
// decision, monthly CPI and the payrolls report. Each series lists every
// upcoming release and strike; score keeps the one closest to being in
// play.
var kalshiSeries = []struct{ ticker, theme string }{
	{"KXFEDDECISION", "FOMC"},
	{"KXCPI", "US CPI"},
	{"KXPAYROLLS", "US payrolls"},
}

type kalshiMarket struct {
	Ticker         string          `json:"ticker"`
	Title          string          `json:"title"`
	Subtitle       string          `json:"subtitle"`
	YesSubTitle    string          `json:"yes_sub_title"`
	CloseTime      string          `json:"close_time"`
	LastPriceUSD   json.RawMessage `json:"last_price_dollars"` // "0.4200"
	PrevPriceUSD   json.RawMessage `json:"previous_price_dollars"`
	LastPriceCents *float64        `json:"last_price"` // older API: cents
	PrevPriceCents *float64        `json:"previous_price"`
	Volume24hFP    json.RawMessage `json:"volume_24h_fp"`
	Volume24h      *float64        `json:"volume_24h"`
}

// kalshiPrice reads a dollars field, falling back to the older cents field.
func kalshiPrice(dollars json.RawMessage, cents *float64) (float64, bool) {
	if v, ok := num(dollars); ok {
		return v, true
	}
	if cents != nil {
		return *cents / 100, true
	}
	return 0, false
}

func (m kalshiMarket) volume() float64 {
	if v, ok := num(m.Volume24hFP); ok {
		return v
	}
	if m.Volume24h != nil {
		return *m.Volume24h
	}
	return 0
}

func num(raw json.RawMessage) (float64, bool) {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" || s == "null" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

func (c *Client) kalshi(ctx context.Context) ([]Signal, error) {
	var out []Signal
	var firstErr error
	ok := 0
	for _, se := range kalshiSeries {
		q := url.Values{"series_ticker": {se.ticker}, "status": {"open"}, "limit": {"200"}}
		var v struct {
			Markets []kalshiMarket `json:"markets"`
		}
		if err := c.getJSON(ctx, c.o.KalshiURL+"/markets?"+q.Encode(), &v); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if ctx.Err() != nil {
				break
			}
			continue
		}
		ok++
		if s, found := kalshiBest(v.Markets, se.theme, c.o.Now()); found {
			out = append(out, s)
		}
	}
	if ok == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// kalshiBest returns the series' highest-scoring market.
func kalshiBest(ms []kalshiMarket, theme string, now time.Time) (Signal, bool) {
	var best *kalshiMarket
	var bestP, bestScore float64
	for i := range ms {
		m := &ms[i]
		p, ok := kalshiPrice(m.LastPriceUSD, m.LastPriceCents)
		if !ok {
			continue
		}
		sc, ok := score(p, m.volume(), parseTime(m.CloseTime), now)
		if ok && sc > bestScore {
			best, bestP, bestScore = m, p, sc
		}
	}
	if best == nil {
		return Signal{}, false
	}
	q := best.Title
	if sub := best.YesSubTitle; sub != "" {
		q += " - " + sub
	} else if best.Subtitle != "" {
		q += " - " + best.Subtitle
	}
	s := Signal{Source: SourceKalshi, Theme: theme, Question: clean(q, maxQuestion), Prob: bestP,
		Volume24h: best.volume(), Market: clean(best.Ticker, 60)}
	// A previous price of 0 or 1 is a market that had not traded yet, not
	// a real starting point for a move.
	if prev, ok := kalshiPrice(best.PrevPriceUSD, best.PrevPriceCents); ok && prev > 0 && prev < 1 {
		v := roundPP(bestP - prev)
		s.Move24h = &v
	}
	return s, true
}
