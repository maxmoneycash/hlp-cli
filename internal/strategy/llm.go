package strategy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/maxmoneycash/hlp-cli/internal/config"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/llm"
)

// LLMConfig configures an LLM-driven strategy (an "AI vault" bot).
type LLMConfig struct {
	Name     string          `yaml:"name"`
	Universe []string        `yaml:"universe"`
	Interval domain.Interval `yaml:"interval"`  // decision cadence, default 1h
	Prompt   string          `yaml:"prompt"`    // the thesis in plain English
	MaxGross float64         `yaml:"max_gross"` // clamp on sum |w|, default 2
	Lookback int             `yaml:"lookback"`  // bars of context per coin, default 48

	// Model picks the endpoint for this bot (arena entrants each bring
	// their own). Empty means "whatever provider the caller passes".
	Model ModelRef `yaml:"model,omitempty"`
	// Temperature for decisions (default 0.2).
	Temperature *float64 `yaml:"temperature,omitempty"`
	// MaxCalls caps model calls per run. 0 = unlimited for local models and
	// 500 for remote (paid) ones, so a long backtest cannot run up a bill.
	MaxCalls int `yaml:"max_calls,omitempty"`
	// Timeout per model call (default 90s).
	Timeout time.Duration `yaml:"timeout,omitempty"`
	// MaxTokens per reply (default 400 for weights, 900 for decisions).
	MaxTokens int `yaml:"max_tokens,omitempty"`

	// Mode is the reply format: "weights" (a target book, the default) or
	// "decisions" (per-coin BUY/SELL/CLOSE/HOLD trades with usd, leverage,
	// confidence, a one-line why and the evidence; see decide.go). Scout
	// implies decisions.
	Mode string `yaml:"mode,omitempty"`
	// Scout turns on the two-step loop: the model first nominates up to
	// Scout.Max coins from Universe (the candidate pool), then decides on
	// those plus its open positions.
	Scout *ScoutConfig `yaml:"scout,omitempty"`
	// MaxLeverage caps one position's notional at this multiple of equity
	// in decisions mode (default 3, at most 20). The exchange's own max
	// leverage and margin check still apply in the engine.
	MaxLeverage float64 `yaml:"max_leverage,omitempty"`
	// MinConfidence drops decisions below this confidence (0..1, default 0).
	MinConfidence float64 `yaml:"min_confidence,omitempty"`
	// Macro: false keeps prediction-market context out of this bot's
	// prompts (default: included in paper and arena runs when the host
	// provides it; never in backtests).
	Macro *bool `yaml:"macro,omitempty"`

	// Host-provided, never read from files.

	// MacroSource supplies the labelled prediction-market block. core sets
	// it for paper and arena runs only.
	MacroSource MacroSource `yaml:"-" json:"-"`
	// Resolve maps a model-nominated name ("SPY", "sp500") to a registry
	// coin ("xyz:SP500"); nominations are still checked against Universe.
	Resolve func(string) (string, error) `yaml:"-" json:"-"`
}

// Reply formats.
const (
	ModeWeights   = "weights"
	ModeDecisions = "decisions"
)

// ScoutConfig configures the scout step.
type ScoutConfig struct {
	Max   int `yaml:"max"`   // coins nominated per scout (default 4, at most 12)
	Every int `yaml:"every"` // scout every N decision bars, reusing picks between (default 1)
}

// MacroSource returns the prediction-market prompt block for a bot trading
// coins, or "" (internal/macro.Client implements it). It must fail soft.
type MacroSource interface {
	MacroContext(ctx context.Context, coins []string) string
}

// ModelRef names an LLM endpoint. In YAML it is either a mapping
// {provider, model} or the short string "provider:model", e.g.
// "ollama:qwen3:8b" or "lmstudio:llama-3.1-8b".
//
// BaseURL and APIKeyEnv exist for the struct's JSON/YAML shape only: strategy
// files are untrusted (shared bots, arena files) and may not set them, since
// that would let a file send any env var, including HLP_AGENT_KEY, to any
// URL (audit S1). Endpoints and keys come only from config.toml [llm].
type ModelRef struct {
	Provider  string `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model     string `yaml:"model,omitempty" json:"model,omitempty"`
	BaseURL   string `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	APIKeyEnv string `yaml:"api_key_env,omitempty" json:"api_key_env,omitempty"`
}

// ParseModelRef parses "provider:model" ("ollama:qwen3:8b"). OpenRouter
// models are "openrouter/<vendor>/<model>" or "openrouter:<vendor>/<model>"
// (OpenRouter ids may themselves contain ':', e.g. ":free"). A bare model
// name means provider "ollama".
func ParseModelRef(s string) ModelRef {
	s = strings.TrimSpace(s)
	if len(s) > len("openrouter/") && strings.EqualFold(s[:len("openrouter/")], "openrouter/") {
		return ModelRef{Provider: "openrouter", Model: s[len("openrouter/"):]}
	}
	if i := strings.Index(s, ":"); i > 0 {
		switch p := strings.ToLower(s[:i]); p {
		case "ollama", "lmstudio", "openai-compatible", "openai", "anthropic", "claude", "openrouter":
			return ModelRef{Provider: p, Model: s[i+1:]}
		}
	}
	if s == "" {
		return ModelRef{}
	}
	return ModelRef{Provider: "ollama", Model: s}
}

// UnmarshalYAML accepts the mapping or the short string form.
func (m *ModelRef) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*m = ParseModelRef(n.Value)
		return nil
	}
	type plain ModelRef
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*m = ModelRef(p)
	return nil
}

// IsZero reports an unset reference.
func (m ModelRef) IsZero() bool { return m == ModelRef{} }

// String renders "provider:model".
func (m ModelRef) String() string {
	if m.IsZero() {
		return ""
	}
	return m.Provider + ":" + m.Model
}

// errModelEndpoint rejects endpoint overrides in strategy files (audit S1).
var errModelEndpoint = errors.New("llm strategy: model.base_url and model.api_key_env are not allowed in strategy files; configure endpoints in config.toml [llm]")

// LLMConfig converts the reference into the llm package's config. Only the
// provider and model carry over: a strategy-sourced reference never picks
// the endpoint URL or the env var whose value is sent as the API key
// (audit S1), even if a caller skipped validation.
func (m ModelRef) LLMConfig() config.LLMConfig {
	return config.LLMConfig{Provider: m.Provider, Model: m.Model}
}

// ParseLLMConfig decodes an LLM bot YAML (examples/llm/*.yaml) and applies
// defaults. Unknown fields are errors.
func ParseLLMConfig(b []byte) (LLMConfig, error) {
	var c LLMConfig
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("llm strategy: %w", err)
	}
	c.defaults()
	return c, c.validate()
}

func (c *LLMConfig) defaults() {
	if c.Interval == "" {
		c.Interval = domain.I1h
	}
	if c.MaxGross == 0 {
		c.MaxGross = 2
	}
	if c.Lookback == 0 {
		c.Lookback = 48
	}
	if c.Name == "" {
		c.Name = "LLM bot"
		if !c.Model.IsZero() {
			c.Name = c.Model.Model
		}
	}
	if c.Timeout == 0 {
		c.Timeout = 90 * time.Second
	}
	if c.Scout != nil {
		c.Mode = ModeDecisions
		if c.Scout.Max == 0 {
			c.Scout.Max = 4
		}
		if c.Scout.Every == 0 {
			c.Scout.Every = 1
		}
	}
	if c.Mode == "" {
		c.Mode = ModeWeights
	}
	if c.MaxLeverage == 0 {
		c.MaxLeverage = 3
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = 400
		if c.Mode == ModeDecisions {
			c.MaxTokens = 900
		}
	}
}

func (c LLMConfig) validate() error {
	if len(c.Universe) == 0 {
		return errors.New("llm strategy: universe is empty")
	}
	seen := map[string]bool{}
	for _, u := range c.Universe {
		if u == "" || seen[u] {
			return fmt.Errorf("llm strategy: bad or duplicate coin %q in universe", u)
		}
		seen[u] = true
	}
	if !c.Interval.Valid() {
		return fmt.Errorf("llm strategy: invalid interval %q", c.Interval)
	}
	if c.MaxGross <= 0 || c.MaxGross > 50 || math.IsNaN(c.MaxGross) {
		return fmt.Errorf("llm strategy: max_gross must be in (0, 50], got %v", c.MaxGross)
	}
	if c.Lookback < 2 || c.Lookback > 1000 {
		return fmt.Errorf("llm strategy: lookback must be 2..1000 bars, got %d", c.Lookback)
	}
	if strings.TrimSpace(c.Prompt) == "" {
		return errors.New("llm strategy: prompt (the thesis) is empty")
	}
	if c.Model.BaseURL != "" || c.Model.APIKeyEnv != "" {
		return errModelEndpoint
	}
	if c.Mode != ModeWeights && c.Mode != ModeDecisions {
		return fmt.Errorf("llm strategy: mode must be weights or decisions, got %q", c.Mode)
	}
	if c.Scout != nil && (c.Scout.Max < 1 || c.Scout.Max > 12 || c.Scout.Every < 1 || c.Scout.Every > 1000) {
		return fmt.Errorf("llm strategy: scout.max must be 1..12 and scout.every 1..1000")
	}
	if c.MaxLeverage < 1 || c.MaxLeverage > 20 || math.IsNaN(c.MaxLeverage) {
		return fmt.Errorf("llm strategy: max_leverage must be in [1, 20], got %v", c.MaxLeverage)
	}
	if c.MinConfidence < 0 || c.MinConfidence > 1 || math.IsNaN(c.MinConfidence) {
		return fmt.Errorf("llm strategy: min_confidence must be in [0, 1], got %v", c.MinConfidence)
	}
	return nil
}

// macroOn reports whether this bot wants macro context.
func (c LLMConfig) macroOn() bool { return c.MacroSource != nil && (c.Macro == nil || *c.Macro) }

// NewLLM returns a Strategy that, each decision bar, sends a compact market
// snapshot (OHLCV digest, funding, current weights, equity) and asks the
// model for a JSON object {"weights":{coin:w}, "note":"why"}; it validates,
// clamps to MaxGross and the universe, then calls SetTargets and Logf(note).
// Invalid replies keep the current book and log the error. Paper/backtest
// only until the Phase-2 gate.
func NewLLM(p llm.Provider, cfg LLMConfig) (Strategy, error) {
	if p == nil {
		return nil, errors.New("llm strategy: no model provider (configure [llm] or run a local model with Ollama / LM Studio)")
	}
	cfg.defaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.MaxCalls == 0 && !p.Local() {
		cfg.MaxCalls = 500
	}
	return &llmStrategy{p: p, cfg: cfg}, nil
}

// Noter is implemented by strategies that explain their last decision
// (shown in the arena leaderboard).
type Noter interface{ LastNote() string }

// Failer is implemented by strategies that can fail without stopping
// (an unreachable model server): LastFailure is the latest failure, ""
// once a call succeeds again. Paper sessions show it as the entrant's error.
type Failer interface{ LastFailure() string }

type llmStrategy struct {
	p        llm.Provider
	cfg      LLMConfig
	calls    int
	note     string
	failure  string
	exhaust  bool
	startEq  float64
	failures int

	// decisions mode
	bars     int                      // decision bars seen
	picks    []string                 // latest scout nominations
	decision atomic.Pointer[Decision] // read by the arena card from another goroutine
}

// ModelIDer is implemented by strategies driven by one model: the id every
// decision and order of theirs is tagged with ("openrouter:anthropic/claude-sonnet-5").
type ModelIDer interface{ ModelID() string }

// ModelID implements ModelIDer.
func (s *llmStrategy) ModelID() string { return s.p.Name() }

func (s *llmStrategy) Spec() Spec {
	return Spec{Name: s.cfg.Name, Kind: "llm", Universe: append([]string(nil), s.cfg.Universe...),
		Interval: s.cfg.Interval, Warmup: min(s.cfg.Lookback, 24)}
}

func (s *llmStrategy) Init(ctx Context) error {
	s.calls, s.note, s.exhaust, s.failures = 0, "", false, 0
	s.bars, s.picks = 0, nil
	s.decision.Store(nil)
	s.startEq = ctx.Equity()
	return nil
}

// LastFailure is the latest model-call failure ("" after a success).
func (s *llmStrategy) LastFailure() string { return s.failure }

// LastNote returns the model's latest explanation.
func (s *llmStrategy) LastNote() string { return s.note }

func (s *llmStrategy) OnBar(ctx Context) error {
	if s.cfg.MaxCalls > 0 && s.calls >= s.cfg.MaxCalls {
		if !s.exhaust {
			s.exhaust = true
			ctx.Logf("llm: call budget of %d reached; holding the current book", s.cfg.MaxCalls)
		}
		return nil
	}
	if s.startEq == 0 {
		s.startEq = ctx.Equity()
	}
	if s.cfg.Mode == ModeDecisions {
		return s.onBarDecisions(ctx)
	}
	temp := 0.2
	if s.cfg.Temperature != nil {
		temp = *s.cfg.Temperature
	}
	req := llm.Request{
		Model:       s.cfg.Model.Model,
		System:      s.systemPrompt(),
		Messages:    []llm.Message{{Role: llm.User, Content: s.snapshot(ctx, s.cfg.Universe) + s.macroBlock(ctx, s.cfg.Universe)}},
		MaxTokens:   s.cfg.MaxTokens,
		Temperature: &temp,
		JSON:        true,
	}
	parent := StdContext(ctx)
	if parent.Err() != nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(parent, s.cfg.Timeout)
	defer cancel()
	s.calls++
	msg, err := s.p.Chat(cctx, req, nil)
	if err != nil {
		if parent.Err() != nil {
			return nil // stopped
		}
		if s.budgetSpent(ctx, err) {
			return nil
		}
		s.failures++
		s.failure = fmt.Sprintf("%s call failed: %v", s.p.Name(), err)
		ctx.Logf("llm: %s call failed (%v); holding the current book", s.p.Name(), err)
		return nil
	}
	s.failure = ""
	w, note, err := ParseWeights(msg.Content, s.cfg.Universe, s.cfg.MaxGross)
	if err != nil {
		ctx.Logf("llm: invalid reply (%v); holding the current book", err)
		return nil
	}
	for _, c := range s.cfg.Universe {
		if _, ok := w[c]; !ok && ctx.Position(c) != 0 {
			w[c] = 0
		}
	}
	if note == "" {
		note = "(no note)"
	}
	s.note = note
	ctx.Logf("%s | %s", note, formatWeights(w))
	return ctx.SetTargets(w)
}

func (s *llmStrategy) systemPrompt() string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %q, the portfolio manager of a paper-trading Hyperliquid perpetuals vault.\n", s.cfg.Name)
	fmt.Fprintf(&b, "Thesis and style:\n%s\n\n", strings.TrimSpace(s.cfg.Prompt))
	fmt.Fprintf(&b, "Every %s you receive a market snapshot and decide the whole target book.\n", s.cfg.Interval)
	b.WriteString("Reply with ONLY one JSON object, no prose, no code fences:\n")
	b.WriteString(`{"weights": {"COIN": weight, ...}, "note": "one short sentence explaining the decision"}` + "\n")
	fmt.Fprintf(&b, "Rules: weights are signed fractions of equity (0.5 = 50%% long, -0.25 = 25%% short); "+
		"the sum of absolute weights must be <= %g; use only these coins: %s; a coin you omit is closed; "+
		"{} keeps you flat. Fees are 0.045%% per trade and funding is paid hourly, so avoid needless churn.\n",
		s.cfg.MaxGross, strings.Join(s.cfg.Universe, ", "))
	if s.cfg.macroOn() {
		b.WriteString(macroRule + "\n")
	}
	return b.String()
}

// budgetSpent handles llm.ErrBudget (the run's --max-llm-calls): it logs
// once, stops further calls and reports true.
func (s *llmStrategy) budgetSpent(ctx Context, err error) bool {
	if !errors.Is(err, llm.ErrBudget) {
		return false
	}
	if !s.exhaust {
		s.exhaust = true
		ctx.Logf("llm: the run's LLM call budget is spent; holding the current book")
	}
	s.cfg.MaxCalls, s.failure = s.calls, ""
	return true
}

// snapshot renders the compact market digest of coins sent to the model.
func (s *llmStrategy) snapshot(ctx Context, coins []string) string {
	var b strings.Builder
	eq := ctx.Equity()
	t := time.UnixMilli(ctx.Now()).UTC().Format("2006-01-02 15:04")
	ret := 0.0
	if s.startEq > 0 {
		ret = eq/s.startEq - 1
	}
	gross := 0.0
	for _, c := range s.cfg.Universe {
		gross += math.Abs(ctx.Position(c) * ctx.Price(c))
	}
	if eq > 0 {
		gross /= eq
	}
	fmt.Fprintf(&b, "time %s UTC | bar %s | equity $%.2f (%+.2f%% since start) | gross %.2fx\n", t, s.cfg.Interval, eq, ret*100, gross)
	fi, hasFunding := ctx.(FundingInfo)
	lb := s.cfg.Lookback
	fmt.Fprintf(&b, "coin | last | 1 bar | 6 bars | %d bars | high/low %d | ann. vol | funding APR | weight\n", lb, lb)
	for _, c := range coins {
		bars := ctx.Bars(c)
		if len(bars) == 0 {
			fmt.Fprintf(&b, "%s | no data yet\n", c)
			continue
		}
		if len(bars) > lb {
			bars = bars[len(bars)-lb:]
		}
		last := bars[len(bars)-1].C
		chg := func(k int) string {
			if len(bars) <= k || bars[len(bars)-1-k].C == 0 {
				return "n/a"
			}
			return fmt.Sprintf("%+.2f%%", (last/bars[len(bars)-1-k].C-1)*100)
		}
		hi, lo := bars[0].H, bars[0].L
		for _, x := range bars {
			hi, lo = math.Max(hi, x.H), math.Min(lo, x.L)
		}
		vol := annVol(bars, s.cfg.Interval)
		fund := "n/a"
		if hasFunding {
			fund = fmt.Sprintf("%+.1f%%", fi.FundingRate(c)*24*365*100)
		}
		w := 0.0
		if eq > 0 {
			w = ctx.Position(c) * last / eq
		}
		fmt.Fprintf(&b, "%s | %s | %s | %s | %s | %s/%s | %.0f%% | %s | %+.3f\n", c, fmtPx(last), chg(1), chg(6),
			chg(len(bars)-1), fmtPx(hi), fmtPx(lo), vol*100, fund, w)
	}
	b.WriteString("recent closes (oldest first, up to 24 points):\n")
	for _, c := range coins {
		bars := ctx.Bars(c)
		if len(bars) == 0 {
			continue
		}
		if len(bars) > lb {
			bars = bars[len(bars)-lb:]
		}
		step := max(1, (len(bars)+23)/24)
		var pts []string
		for i := len(bars) - 1; i >= 0; i -= step {
			pts = append(pts, fmtPx(bars[i].C))
		}
		for i, j := 0, len(pts)-1; i < j; i, j = i+1, j-1 {
			pts[i], pts[j] = pts[j], pts[i]
		}
		fmt.Fprintf(&b, "%s: %s\n", c, strings.Join(pts, " "))
	}
	return b.String()
}

func annVol(bars []domain.Candle, iv domain.Interval) float64 {
	if len(bars) < 3 {
		return 0
	}
	var rs []float64
	for i := 1; i < len(bars); i++ {
		if bars[i-1].C > 0 {
			rs = append(rs, math.Log(bars[i].C/bars[i-1].C))
		}
	}
	m := 0.0
	for _, r := range rs {
		m += r
	}
	m /= float64(len(rs))
	v := 0.0
	for _, r := range rs {
		v += (r - m) * (r - m)
	}
	v /= float64(max(len(rs)-1, 1))
	per := float64(365*24*time.Hour) / float64(iv.Duration())
	return math.Sqrt(v * per)
}

func fmtPx(p float64) string {
	switch {
	case p >= 1000:
		return fmt.Sprintf("%.0f", p)
	case p >= 1:
		return fmt.Sprintf("%.3f", p)
	default:
		return fmt.Sprintf("%.6f", p)
	}
}

func formatWeights(w map[string]float64) string {
	cs := make([]string, 0, len(w))
	for c, v := range w {
		if v != 0 {
			cs = append(cs, c)
		}
	}
	if len(cs) == 0 {
		return "flat"
	}
	sort.Slice(cs, func(i, j int) bool {
		if w[cs[i]] != w[cs[j]] {
			return w[cs[i]] > w[cs[j]]
		}
		return cs[i] < cs[j]
	})
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = fmt.Sprintf("%s %+.2f", c, w[c])
	}
	return strings.Join(parts, " ")
}

// ParseWeights extracts and validates a model reply. It accepts the JSON
// object anywhere in the text (code fences and chatter are ignored), either
// {"weights":{...},"note":"..."} or a bare {"COIN": w} map. Coins match
// the universe case-insensitively; coins outside it are rejected, weights
// must be finite and |w| is clamped to maxGross, then the whole book is
// scaled down if sum |w| > maxGross.
func ParseWeights(reply string, universe []string, maxGross float64) (map[string]float64, string, error) {
	obj, err := extractJSON(reply)
	if err != nil {
		return nil, "", err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(obj, &raw); err != nil {
		return nil, "", fmt.Errorf("reply is not a JSON object: %w", err)
	}
	note := ""
	if n, ok := raw["note"]; ok {
		_ = json.Unmarshal(n, &note)
	}
	ws := raw
	if w, ok := raw["weights"]; ok {
		ws = nil
		if string(w) != "null" {
			if err := json.Unmarshal(w, &ws); err != nil {
				return nil, note, fmt.Errorf("weights is not an object: %w", err)
			}
		}
	} else {
		delete(ws, "note")
		delete(ws, "reason")
		delete(ws, "rationale")
	}
	canon := map[string]string{}
	for _, u := range universe {
		canon[strings.ToUpper(u)] = u
	}
	out := map[string]float64{}
	for k, rv := range ws {
		var v float64
		if err := json.Unmarshal(rv, &v); err != nil {
			var s string
			if json.Unmarshal(rv, &s) == nil {
				if _, err := fmt.Sscanf(strings.TrimSpace(s), "%g", &v); err != nil {
					return nil, note, fmt.Errorf("weight for %s is not a number", k)
				}
			} else {
				return nil, note, fmt.Errorf("weight for %s is not a number", k)
			}
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, note, fmt.Errorf("weight for %s is not finite", k)
		}
		c, ok := canon[strings.ToUpper(strings.TrimSpace(k))]
		if !ok {
			return nil, note, fmt.Errorf("%s is not in the universe (%s)", k, strings.Join(universe, ", "))
		}
		out[c] = math.Max(-maxGross, math.Min(maxGross, v))
	}
	g := 0.0
	for _, v := range out {
		g += math.Abs(v)
	}
	if g > maxGross && g > 0 {
		k := maxGross / g
		for c := range out {
			out[c] *= k
		}
	}
	return out, strings.TrimSpace(note), nil
}

// extractJSON returns the first balanced {...} object in s.
func extractJSON(s string) ([]byte, error) {
	start := strings.Index(s, "{")
	if start < 0 {
		return nil, errors.New("no JSON object in reply")
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		ch := s[i]
		switch {
		case esc:
			esc = false
		case ch == '\\' && inStr:
			esc = true
		case ch == '"':
			inStr = !inStr
		case inStr:
		case ch == '{':
			depth++
		case ch == '}':
			depth--
			if depth == 0 {
				return []byte(s[start : i+1]), nil
			}
		}
	}
	return nil, errors.New("unterminated JSON object in reply")
}
