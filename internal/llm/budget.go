package llm

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrBudget is returned by a Budget-wrapped provider once the run's call
// cap is reached (--max-llm-calls). Strategies treat it
// as "hold the current book" and stop calling.
var ErrBudget = errors.New("llm call budget reached (--max-llm-calls)")

// Budget is a run-wide LLM call cap and cost tally shared by every
// provider it wraps (all arena entrants, scout and decide calls alike).
// The zero value is unlimited; it is safe for concurrent use.
type Budget struct {
	mu      sync.Mutex
	max     int
	calls   int
	refused int
	in, out int
	cost    float64
	unknown map[string]bool
	price   func(name string) (Price, bool)
	byModel map[string]*ModelUsage
}

// ModelUsage is one model's share of a Budget.
type ModelUsage struct {
	Model        string  `json:"model"`
	Calls        int     `json:"calls"`
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	CostUSD      float64 `json:"costUsd"`
	Priced       bool    `json:"priced"`
}

// BudgetStats is a Budget snapshot (shown in arena and backtest results).
type BudgetStats struct {
	Max          int          `json:"maxCalls"` // 0 = unlimited
	Calls        int          `json:"calls"`
	Refused      int          `json:"refused,omitempty"` // calls stopped by the cap
	InputTokens  int          `json:"inputTokens"`
	OutputTokens int          `json:"outputTokens"`
	CostUSD      float64      `json:"costUsd"`            // at list price, for priced models
	Unpriced     []string     `json:"unpriced,omitempty"` // models with no known price
	Models       []ModelUsage `json:"models,omitempty"`
}

// NewBudget caps calls at max (0 = unlimited). price, when set, prices
// usage per provider name for the cost tally.
func NewBudget(max int, price func(name string) (Price, bool)) *Budget {
	return &Budget{max: max, price: price}
}

// Wrap returns p counted against b. A nil b or p returns p unchanged.
func (b *Budget) Wrap(p Provider) Provider {
	if b == nil || p == nil {
		return p
	}
	if bp, ok := p.(*budgeted); ok && bp.b == b {
		return p
	}
	return &budgeted{p: p, b: b}
}

// Stats returns a snapshot.
func (b *Budget) Stats() BudgetStats {
	if b == nil {
		return BudgetStats{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s := BudgetStats{Max: b.max, Calls: b.calls, Refused: b.refused, InputTokens: b.in, OutputTokens: b.out, CostUSD: b.cost}
	for n := range b.unknown {
		s.Unpriced = append(s.Unpriced, n)
	}
	for _, m := range b.byModel {
		s.Models = append(s.Models, *m)
	}
	sortStrings(s.Unpriced)
	sortUsage(s.Models)
	return s
}

// reserve takes one call from the budget.
func (b *Budget) reserve() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.max > 0 && b.calls >= b.max {
		b.refused++
		return false
	}
	b.calls++
	return true
}

func (b *Budget) record(name string, u *Usage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.byModel == nil {
		b.byModel = map[string]*ModelUsage{}
	}
	m := b.byModel[name]
	if m == nil {
		m = &ModelUsage{Model: name}
		b.byModel[name] = m
	}
	m.Calls++
	if u == nil {
		return
	}
	b.in += u.InputTokens
	b.out += u.OutputTokens
	m.InputTokens += u.InputTokens
	m.OutputTokens += u.OutputTokens
	if b.price == nil {
		return
	}
	if p, ok := b.price(name); ok {
		c := p.Cost(u.InputTokens, u.OutputTokens)
		b.cost += c
		m.CostUSD += c
		m.Priced = true
		return
	}
	if b.unknown == nil {
		b.unknown = map[string]bool{}
	}
	b.unknown[name] = true
}

type budgeted struct {
	p Provider
	b *Budget
}

func (w *budgeted) Name() string { return w.p.Name() }
func (w *budgeted) Local() bool  { return w.p.Local() }

func (w *budgeted) Chat(ctx context.Context, req Request, onChunk func(Chunk)) (Message, error) {
	if !w.b.reserve() {
		return Message{}, ErrBudget
	}
	var usage *Usage
	msg, err := w.p.Chat(ctx, req, func(c Chunk) {
		if c.Usage != nil {
			u := *c.Usage
			usage = &u
		}
		if onChunk != nil {
			onChunk(c)
		}
	})
	w.b.record(w.p.Name(), usage)
	return msg, err
}

// String and Format print the provider name only (see Anthropic.String).
func (w *budgeted) String() string             { return w.Name() }
func (w *budgeted) Format(f fmt.State, _ rune) { _, _ = fmt.Fprint(f, w.Name()) }

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func sortUsage(s []ModelUsage) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].Model < s[j-1].Model; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
