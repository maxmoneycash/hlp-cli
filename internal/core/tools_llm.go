package core

// LLM run plumbing: the cost guard, the OpenRouter model
// catalog (`hlp arena models`), prediction-market context (`hlp macro`,
// internal/macro) and the passive yardstick behind vsBench.

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/hyperliquid"
	"github.com/maxmoneycash/hlp-cli/internal/llm"
	"github.com/maxmoneycash/hlp-cli/internal/macro"
	"github.com/maxmoneycash/hlp-cli/internal/strategy"
)

// DefaultMaxLLMCalls is --max-llm-calls when unset.
const DefaultMaxLLMCalls = 200

// LLMGuard is embedded in every input that can run an LLM strategy more
// than once (backtest, paper, arena). Without LLM the run is refused with
// the estimate; with it the estimate is reported and calls stop at
// MaxLLMCalls across the whole run.
type LLMGuard struct {
	LLM         bool `json:"llm,omitempty" jsonschema:"confirm a run that calls an LLM more than once (LLM strategies in backtest, paper or arena); without it the run is refused with an estimated call count and cost"`
	MaxLLMCalls int  `json:"maxLlmCalls,omitempty" jsonschema:"stop every LLM call of this run after this many (default 200)"`
}

func (g LLMGuard) max() int {
	if g.MaxLLMCalls > 0 {
		return g.MaxLLMCalls
	}
	return DefaultMaxLLMCalls
}

// llmRun is the run-wide budget and macro source, handed to the strategy
// loader through the context.
type llmRun struct {
	budget *llm.Budget
	macro  strategy.MacroSource // nil for backtests (no look-ahead)
}

type llmRunKey struct{}

func withLLMRun(ctx context.Context, r *llmRun) context.Context {
	return context.WithValue(ctx, llmRunKey{}, r)
}

func llmRunFrom(ctx context.Context) *llmRun {
	r, _ := ctx.Value(llmRunKey{}).(*llmRun)
	return r
}

// newLLMRun builds the budget (priced from the OpenRouter catalog when
// available) and, for live purposes, the macro source.
func newLLMRun(env *Env, g LLMGuard, purpose string) *llmRun {
	if g.MaxLLMCalls < 0 {
		g.MaxLLMCalls = 0
	}
	cat := env.LLMCatalog
	pricer := func(name string) (llm.Price, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return cat.PriceFor(ctx, name)
	}
	r := &llmRun{budget: llm.NewBudget(g.max(), pricer)}
	if purpose != purposeBacktest && env.Macro != nil {
		r.macro = env.Macro
	}
	return r
}

// llmEstimate summarizes the forecast for the guard line.
type llmEstimate struct {
	Calls  int     // over the run (backtest) or per hour (live)
	Cost   float64 // USD at list price, priced models only
	Priced bool    // every remote model had a price
	Models []string
	Live   bool // open-ended: Calls and Cost are per hour
}

func (e llmEstimate) String(max int) string {
	var b strings.Builder
	if e.Live {
		fmt.Fprintf(&b, "~%d LLM calls per hour", e.Calls)
	} else {
		fmt.Fprintf(&b, "~%d LLM calls", e.Calls)
	}
	switch {
	case e.Priced && e.Cost == 0:
		b.WriteString(" (local models, no cost)")
	case e.Priced:
		fmt.Fprintf(&b, " (~$%.2f%s at list price)", e.Cost, map[bool]string{true: "/hour", false: ""}[e.Live])
	case e.Cost > 0:
		fmt.Fprintf(&b, " (~$%.2f%s for the priced models; others unknown)", e.Cost, map[bool]string{true: "/hour", false: ""}[e.Live])
	default:
		b.WriteString(" (price unknown)")
	}
	fmt.Fprintf(&b, " on %s; stops at --max-llm-calls %d", strings.Join(e.Models, ", "), max)
	if e.Live && e.Calls > 0 {
		fmt.Fprintf(&b, " (about %.0f h)", float64(max)/float64(e.Calls))
	}
	if !e.Live && e.Calls > max {
		fmt.Fprintf(&b, " (the run will hold its book after %d)", max)
	}
	return b.String()
}

// guardLLM applies decision 11 to strats. bars is the decision bars a
// backtest covers; 0 means an open-ended live run (paper, arena), which is
// estimated per hour. It returns the estimate line for meta warnings, or a
// USAGE error naming the estimate when g.LLM is not set.
func guardLLM(ctx context.Context, env *Env, g LLMGuard, strats []strategy.Strategy, bars int) (string, error) {
	var ests []strategy.CallEstimate
	for _, s := range strats {
		ce, ok := s.(strategy.CallEstimator)
		if !ok {
			continue
		}
		n := bars
		if n == 0 {
			iv := s.Spec().Interval
			if iv == "" {
				iv = domain.I1h
			}
			n = max(1, int(time.Hour/iv.Duration()))
		}
		ests = append(ests, ce.EstimateCalls(n))
	}
	if len(ests) == 0 {
		return "", nil
	}
	e := llmEstimate{Priced: true, Live: bars == 0}
	seen := map[string]bool{}
	for _, x := range ests {
		e.Calls += x.Calls
		if !seen[x.Model] {
			seen[x.Model] = true
			e.Models = append(e.Models, x.Model)
		}
		if x.Local {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		p, ok := env.LLMCatalog.PriceFor(pctx, x.Model)
		cancel()
		if !ok {
			e.Priced = false
			continue
		}
		e.Cost += p.Cost(x.InputTokens, x.OutputTokens)
	}
	line := e.String(g.max())
	if e.Calls <= 1 && !e.Live {
		return "", nil // a single call needs no confirmation
	}
	if !g.LLM {
		return "", &Error{Code: CodeConfirmRequired, Message: "this run makes " + line,
			Hint: "add --llm to confirm (MCP/JSON: \"llm\": true); --max-llm-calls N caps calls for the whole run (default 200)"}
	}
	if env.Progress != nil {
		env.Progress("llm: " + line)
	}
	return "llm: " + line, nil
}

// backtestBars is the decision bars a backtest of s covers.
func backtestBars(s strategy.Strategy, days int) int {
	iv := s.Spec().Interval
	if iv == "" {
		iv = domain.I1h
	}
	if days <= 0 {
		return 5000 - s.Spec().Warmup
	}
	return max(1, int(time.Duration(days)*24*time.Hour/iv.Duration()))
}

// llmStats returns the budget snapshot when the run used a model.
func (r *llmRun) stats() *llm.BudgetStats {
	if r == nil {
		return nil
	}
	s := r.budget.Stats()
	if s.Calls == 0 && s.Refused == 0 {
		return nil
	}
	return &s
}

// ---- hlp arena models ----

// ModelsInput is the input of list_llm_models (`hlp arena models`).
type ModelsInput struct {
	Query string `json:"query,omitempty" jsonschema:"filter by id or name, e.g. claude, gpt, llama, :free"`
	Limit int    `json:"limit,omitempty" jsonschema:"max rows (default 50)"`
	Free  bool   `json:"free,omitempty" jsonschema:"only models with a zero list price"`
	Sort  string `json:"sort,omitempty" jsonschema:"id (default), price (cheapest first) or new (newest first)"`
}

// ModelsData backs `hlp arena models`.
type ModelsData struct {
	Models  []ModelRow `json:"models"`
	Total   int        `json:"total"`   // matches before the limit
	Fetched int64      `json:"fetched"` // ms, when the catalog was fetched
	Source  string     `json:"source"`  // "openrouter"
}

// ModelRow is one model with its arena entrant spelling.
type ModelRow struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Entrant       string  `json:"entrant"` // llm:openrouter/<id>
	InPerM        float64 `json:"inPerM"`  // USD per 1M input tokens
	OutPerM       float64 `json:"outPerM"` // USD per 1M output tokens
	Priced        bool    `json:"priced"`  // false = variable / unpublished price
	ContextLength int     `json:"contextLength,omitempty"`
	Created       int64   `json:"created,omitempty"` // unix seconds
}

// Pairs renders the KV card: entrant -> price.
func (d *ModelsData) Pairs() [][2]string {
	out := make([][2]string, 0, len(d.Models)+1)
	for _, m := range d.Models {
		price := "price varies"
		if m.Priced {
			price = fmt.Sprintf("$%.2f in / $%.2f out per 1M", m.InPerM, m.OutPerM)
			if m.InPerM == 0 && m.OutPerM == 0 {
				price = "free"
			}
		}
		out = append(out, [2]string{m.Entrant, price})
	}
	if d.Total > len(d.Models) {
		out = append(out, [2]string{"…", fmt.Sprintf("%d more (use --query or --limit)", d.Total-len(d.Models))})
	}
	return out
}

func runModels(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*ModelsInput)
	if env.LLMCatalog == nil {
		return nil, &Error{Code: CodeUsage, Message: "the OpenRouter model catalog is not available offline"}
	}
	ms, fetched, err := env.LLMCatalog.Models(ctx)
	meta := env.meta("openrouter")
	if err != nil {
		if len(ms) == 0 {
			return nil, &Error{Code: CodeAPI, Message: "OpenRouter model catalog: " + err.Error(), Hint: "try again later; the list is public and needs no key"}
		}
		meta.Warnings = append(meta.Warnings, "showing the cached catalog: "+err.Error())
	}
	q := strings.ToLower(strings.TrimSpace(p.Query))
	var rows []ModelRow
	for _, m := range ms {
		if q != "" && !strings.Contains(strings.ToLower(m.ID), q) && !strings.Contains(strings.ToLower(m.Name), q) {
			continue
		}
		if p.Free && !(m.Price.Known && m.Price.InPerM == 0 && m.Price.OutPerM == 0) {
			continue
		}
		rows = append(rows, ModelRow{ID: m.ID, Name: m.Name, Entrant: m.Ref(), InPerM: m.Price.InPerM, OutPerM: m.Price.OutPerM,
			Priced: m.Price.Known, ContextLength: m.ContextLength, Created: m.Created})
	}
	switch strings.ToLower(p.Sort) {
	case "", "id":
	case "price":
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].Priced != rows[j].Priced {
				return rows[i].Priced
			}
			return rows[i].InPerM+rows[i].OutPerM < rows[j].InPerM+rows[j].OutPerM
		})
	case "new":
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Created > rows[j].Created })
	default:
		return nil, Errorf(CodeUsage, "sort must be id, price or new, got %q", p.Sort)
	}
	total := len(rows)
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	meta.AsOf = fetched
	return &Result{Card: CardKV, Title: fmt.Sprintf("OpenRouter models (%d)", total),
		Data: &ModelsData{Models: rows, Total: total, Fetched: fetched.UnixMilli(), Source: "openrouter"}, Meta: meta}, nil
}

// ---- hlp macro ----

// MacroInput is the input of get_macro (`hlp macro`).
type MacroInput struct {
	Coins []string `json:"coins,omitempty" jsonschema:"rank HIP-4 price questions on these coins first"`
}

// MacroData backs `hlp macro`: what LLM strategies see as context.
type MacroData struct {
	Enabled bool           `json:"enabled"`
	Signals []macro.Signal `json:"signals"`
	Fetched int64          `json:"fetched,omitempty"` // ms
	Errors  []string       `json:"errors,omitempty"`  // per-source soft failures
	Prompt  string         `json:"prompt,omitempty"`  // the block an LLM strategy receives
}

// Pairs renders the KV card.
func (d *MacroData) Pairs() [][2]string {
	if !d.Enabled {
		return [][2]string{{"macro", "off (config macro.disabled)"}}
	}
	out := make([][2]string, 0, len(d.Signals)+len(d.Errors))
	for _, s := range d.Signals {
		v := fmt.Sprintf("%.0f%% YES", s.Prob*100)
		if s.Move24h != nil {
			v += fmt.Sprintf(" (24h %+.1fpp)", *s.Move24h*100)
		}
		out = append(out, [2]string{s.Source + " · " + s.Theme, v + " · " + s.Question})
	}
	for _, e := range d.Errors {
		out = append(out, [2]string{"unavailable", e})
	}
	if len(out) == 0 {
		out = append(out, [2]string{"macro", "no curated markets right now"})
	}
	return out
}

func runMacro(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*MacroInput)
	meta := env.meta("macro")
	meta.Warnings = append(meta.Warnings, "prediction-market odds are context, not a trading signal")
	if env.Macro == nil {
		return &Result{Card: CardKV, Title: "Macro context", Data: &MacroData{}, Meta: meta}, nil
	}
	var coins []string
	for _, c := range p.Coins {
		if m, err := env.Resolve(ctx, c); err == nil {
			coins = append(coins, m.Coin)
		} else {
			coins = append(coins, c)
		}
	}
	snap := env.Macro.Snapshot(ctx)
	sigs := macro.Select(snap.Signals, coins, 0)
	d := &MacroData{Enabled: true, Signals: sigs, Fetched: snap.Fetched, Errors: snap.Errors}
	if d.Signals == nil {
		d.Signals = []macro.Signal{}
	}
	d.Prompt = macro.Format(macro.Select(sigs, coins, macro.MaxSignals), time.UnixMilli(snap.Fetched))
	return &Result{Card: CardKV, Title: "Macro context (prediction markets)", Data: d, Meta: meta}, nil
}

// hlOutcomes adapts the Hyperliquid client to macro.OutcomeSource: live
// HIP-4 outcomes from outcomeMeta, priced from allMids ("#N" keys).
type hlOutcomes struct{ c *hyperliquid.Client }

func (h hlOutcomes) Outcomes(ctx context.Context) ([]macro.Outcome, error) {
	om, err := h.c.OutcomeMeta(ctx)
	if err != nil {
		return nil, err
	}
	mids, err := h.c.AllMids(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]macro.Outcome, 0, len(om.Outcomes))
	for _, o := range om.Outcomes {
		mo := macro.Outcome{ID: o.Outcome, Name: o.Name, Description: o.Description, Mids: map[int]float64{}}
		for s, side := range o.SideSpecs {
			mo.Sides = append(mo.Sides, side.Name)
			if d, ok := mids[fmt.Sprintf("#%d", 10*o.Outcome+s)]; ok {
				mo.Mids[s] = d.Float()
			}
		}
		out = append(out, mo)
	}
	return out, nil
}

// ---- passive yardstick (vsBench) ----
//
// vsBench answers "did the bot beat simply holding what it trades?". The
// yardstick defaults to an equal split of starting money across every coin
// the entrants may trade, bought on the first bar and never touched, so a
// bot is measured against the market it chose rather than an unrelated
// index. The comparison is a growth ratio, not a difference of returns:
// +10% against a yardstick that fell 50% is a 120% edge (1.10/0.50), while
// +10% against +5% is about 4.8%. That stays meaningful when the two
// returns are far apart, and it is what an investor holding the bot
// instead of the yardstick would have gained.

// BenchInfo is the passive yardstick behind vsBench.
type BenchInfo struct {
	Name   string   `json:"name"`            // "EW(BTC,ETH)", "BTC", "xyz:SP500", "HLP"
	Coins  []string `json:"coins,omitempty"` // equal-weight members
	Return float64  `json:"return"`          // buy-and-hold return over the window (fraction)
	From   int64    `json:"from,omitempty"`  // ms
	To     int64    `json:"to,omitempty"`    // ms
}

// vsBench is the strategy's growth relative to the yardstick's over the
// same window, (1+ret)/(1+bench) - 1, as a pointer for JSON; nil without a
// yardstick or when the yardstick was wiped out.
func vsBench(ret float64, b *BenchInfo) *float64 {
	if b == nil || math.IsNaN(b.Return) || math.IsNaN(ret) || math.IsInf(ret, 0) || b.Return <= -1 {
		return nil
	}
	v := (1+ret)/(1+b.Return) - 1
	return &v
}

// isEW reports the equal-weight spelling.
func isEW(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "ew", "equal", "universe", "entrants":
		return true
	}
	return false
}

// benchCoins is the equal-weight set: every entrant's traded coins.
func benchCoins(strats []strategy.Strategy) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range strats {
		for _, c := range s.Spec().Universe {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// backtestBench computes the benchmark over the backtest window. EW and
// coins the backtest simulated use its own candles (the exact window);
// other names use the tearsheet's benchmark curve.
func backtestBench(name string, universe []string, candles map[string][]domain.Candle, ts domain.Tearsheet) *BenchInfo {
	ret := func(cs []domain.Candle) (float64, int64, int64, bool) {
		if len(cs) < 2 || cs[0].O <= 0 {
			return 0, 0, 0, false
		}
		return cs[len(cs)-1].C/cs[0].O - 1, cs[0].T, cs[len(cs)-1].T, true
	}
	if isEW(name) {
		b := &BenchInfo{}
		sum, n := 0.0, 0
		for _, c := range universe {
			if r, from, to, ok := ret(candles[c]); ok {
				sum += r
				n++
				b.Coins = append(b.Coins, c)
				if b.From == 0 || from < b.From {
					b.From = from
				}
				b.To = max(b.To, to)
			}
		}
		if n == 0 {
			return nil
		}
		b.Return = sum / float64(n)
		b.Name = "EW(" + strings.Join(b.Coins, ",") + ")"
		if n == 1 {
			b.Name, b.Coins = b.Coins[0], nil
		}
		return b
	}
	up := strings.ToUpper(strings.TrimSpace(name))
	for c, cs := range candles {
		if strings.EqualFold(c, name) || strings.EqualFold(strings.TrimPrefix(strings.ToUpper(c), "XYZ:"), up) {
			if r, from, to, ok := ret(cs); ok {
				return &BenchInfo{Name: c, Return: r, From: from, To: to}
			}
		}
	}
	for _, bm := range ts.Benchmarks {
		if strings.EqualFold(bm.Name, up) && len(bm.Curve) > 1 {
			return &BenchInfo{Name: bm.Name, Return: bm.Metrics.TotalReturn, From: bm.Curve[0].T, To: bm.Curve[len(bm.Curve)-1].T}
		}
	}
	return nil
}

// arenaBench tracks buy-and-hold of the benchmark coins from the arena's
// start. Prices come from allMids (one memoized call per dex) when the
// reader has it, else the latest 1m candle; they refresh at most every
// benchRefresh. It fails soft: no price, no vsBench.
type arenaBench struct {
	env   *Env
	name  string
	coins []string

	mu      sync.Mutex
	start   map[string]float64
	last    map[string]float64
	from    int64
	fetched time.Time
}

const benchRefresh = 15 * time.Second

func newArenaBench(ctx context.Context, env *Env, name string, strats []strategy.Strategy) *arenaBench {
	b := &arenaBench{env: env, start: map[string]float64{}, last: map[string]float64{}}
	if isEW(name) {
		b.coins = benchCoins(strats)
		if len(b.coins) == 0 {
			b.coins = []string{"BTC"}
		}
	} else {
		c := strings.TrimSpace(name)
		if m, err := env.Resolve(ctx, c); err == nil {
			c = m.Coin
		}
		b.coins, b.name = []string{c}, c
	}
	if len(b.coins) > 1 {
		b.name = "EW(" + strings.Join(b.coins, ",") + ")"
	} else if b.name == "" {
		b.name = b.coins[0]
	}
	b.refresh(ctx, true)
	return b
}

// info returns the benchmark now (nil until start prices exist).
func (b *arenaBench) info(ctx context.Context) *BenchInfo {
	if b == nil {
		return nil
	}
	b.refresh(ctx, false)
	b.mu.Lock()
	defer b.mu.Unlock()
	sum, n := 0.0, 0
	for _, c := range b.coins {
		s, l := b.start[c], b.last[c]
		if s > 0 && l > 0 {
			sum += l/s - 1
			n++
		}
	}
	if n == 0 {
		return nil
	}
	bi := &BenchInfo{Name: b.name, Return: sum / float64(n), From: b.from, To: b.fetched.UnixMilli()}
	if len(b.coins) > 1 {
		bi.Coins = append([]string(nil), b.coins...)
	}
	return bi
}

func (b *arenaBench) refresh(ctx context.Context, force bool) {
	b.mu.Lock()
	now := b.env.now()
	if !force && now.Sub(b.fetched) < benchRefresh {
		b.mu.Unlock()
		return
	}
	b.fetched = now
	b.mu.Unlock()
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	px := b.prices(pctx)
	b.mu.Lock()
	defer b.mu.Unlock()
	for c, p := range px {
		if p <= 0 {
			continue
		}
		if b.start[c] == 0 {
			b.start[c] = p
			if b.from == 0 {
				b.from = now.UnixMilli()
			}
		}
		b.last[c] = p
	}
}

type midsReader interface {
	AllMids(ctx context.Context, dex string) (map[string]hyperliquid.Dec, error)
}

func (b *arenaBench) prices(ctx context.Context) map[string]float64 {
	out := map[string]float64{}
	if mr, ok := b.env.HL.(midsReader); ok {
		byDex := map[string]map[string]hyperliquid.Dec{}
		for _, c := range b.coins {
			dex := ""
			if d, _, ok := strings.Cut(c, ":"); ok {
				dex = d
			}
			mids, seen := byDex[dex]
			if !seen {
				mids, _ = mr.AllMids(ctx, dex)
				byDex[dex] = mids
			}
			if v, ok := mids[c]; ok {
				out[c] = v.Float()
			}
		}
	}
	for _, c := range b.coins {
		if out[c] > 0 {
			continue
		}
		if cs, err := b.env.HL.Candles(ctx, c, domain.I1m, 1, 0); err == nil && len(cs) > 0 {
			out[c] = cs[len(cs)-1].C
		}
	}
	return out
}
