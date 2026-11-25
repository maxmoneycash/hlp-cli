// Package loader turns what a user types (a .pine file, a thesis YAML, an
// LLM bot YAML, an inline thesis, "llm:ollama:qwen3:8b" or an arena file)
// into runnable strategies. core's backtest, paper and arena tools call it
// so every front-end (CLI, TUI, MCP, agent) accepts the same references.
package loader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/maxmoneycash/hlp-cli/internal/config"
	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/llm"
	"github.com/maxmoneycash/hlp-cli/internal/strategy"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/pine"
	"github.com/maxmoneycash/hlp-cli/internal/strategy/thesis"
)

// Kinds of strategy references.
const (
	KindPine   = "pine"
	KindThesis = "thesis"
	KindLLM    = "llm"
)

// Options control loading.
type Options struct {
	// Coin and Interval bind single-asset Pine scripts (default BTC, 1h).
	Coin     string
	Interval domain.Interval
	// Inputs override Pine input.* defaults by title or variable name.
	Inputs map[string]any
	// Resolve maps user coin names to markets ("SPY" -> xyz:SP500); when
	// set, theses are validated and canonicalized with it.
	Resolve func(string) (domain.Market, error)
	// Provider builds the model endpoint for an LLM bot. Nil uses
	// llm.New(ref.LLMConfig(), Secrets) when the bot names a model, else
	// Default.
	Provider func(ctx context.Context, ref strategy.ModelRef) (llm.Provider, error)
	// Default is the provider for bots that do not name a model (the
	// configured [llm] provider, or llm.Detect's local model).
	Default llm.Provider
	Secrets config.Secrets
	// ReadFile reads strategy files (default ReadCapped: a regular file of
	// at most MaxFileBytes).
	ReadFile func(string) ([]byte, error)
	// CompilePine, when set, builds Pine strategies instead of hlp's
	// builtin interpreter (core injects PYNE's Follower for
	// --engine pyne). It returns the strategy and warnings. The loader
	// never imports an external engine itself.
	CompilePine func(src, coin string, iv domain.Interval, inputs map[string]any) (strategy.Strategy, []string, error)
	// SzDecimals returns a coin's size decimals for the builtin engine's
	// syminfo.mintick (spec A4). Nil uses 0.
	SzDecimals func(coin string) int
	// RefuseRemote refuses bots on a remote (non-local, usually paid)
	// model. core sets it for agent and MCP callers unless
	// llm.allow_remote_bots is on (audit S8).
	RefuseRemote bool
	// Macro, when set, gives LLM bots prediction-market context (core sets
	// it for paper and arena runs only, never for backtests).
	Macro strategy.MacroSource
	// Wrap, when set, wraps every LLM provider the loader hands to a bot
	// (core's run-wide call budget and cost tally).
	Wrap func(llm.Provider) llm.Provider
}

// MaxFileBytes bounds every strategy, bot and arena file the loader reads.
const MaxFileBytes = 1 << 20

// ReadCapped reads path when it is a regular file of at most MaxFileBytes
// (audit S8: an arena entrant "pine: /dev/zero" must not exhaust memory).
func ReadCapped(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if st.Size() > MaxFileBytes {
		return nil, fmt.Errorf("%s is larger than 1 MiB", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxFileBytes {
		return nil, fmt.Errorf("%s is larger than 1 MiB", path)
	}
	return b, nil
}

// Loaded is a ready strategy plus what it was made from.
type Loaded struct {
	Strategy strategy.Strategy
	Kind     string // pine | thesis | llm
	Source   string // the reference it was loaded from
	Name     string
	Warnings []string

	Pine   *pine.Script        `json:"-"`
	Thesis *thesis.Spec        `json:"-"`
	LLM    *strategy.LLMConfig `json:"-"`
	Model  strategy.ModelRef   `json:"-"`
}

// Load resolves one strategy reference:
//
//	path/to/x.pine                       Pine strategy on Options.Coin/Interval
//	path/to/x.yaml|.yml|.json            thesis, or LLM bot when it has a prompt
//	llm:path/to/bot.yaml                 LLM bot
//	llm:ollama:qwen3:8b                  default LLM bot on that model
//	thesis:long BTC, short DOGE          inline thesis (the prefix is optional
//	                                     when the text starts with long/short)
func Load(ctx context.Context, ref string, o Options) (*Loaded, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("loader: empty strategy reference")
	}
	if o.ReadFile == nil {
		o.ReadFile = ReadCapped
	}
	if o.Interval == "" {
		o.Interval = domain.I1h
	}
	lower := strings.ToLower(ref)
	switch {
	case strings.HasPrefix(lower, "llm:"):
		rest := strings.TrimSpace(ref[4:])
		if isFile(rest) {
			b, err := o.ReadFile(rest)
			if err != nil {
				return nil, fmt.Errorf("loader: %w", err)
			}
			return loadLLM(ctx, rest, b, strategy.ModelRef{}, "", o)
		}
		cfg := DefaultLLMConfig()
		m := strategy.ParseModelRef(rest)
		cfg.Model = m
		if m.Model != "" {
			cfg.Name = m.Model
		}
		return buildLLM(ctx, ref, cfg, o)
	case strings.HasPrefix(lower, "thesis:"):
		return loadThesisText(ref, strings.TrimSpace(ref[7:]), o)
	case strings.HasPrefix(lower, "long ") || strings.HasPrefix(lower, "short "):
		return loadThesisText(ref, ref, o)
	}
	b, err := o.ReadFile(ref)
	if err != nil {
		return nil, fmt.Errorf("loader: %w", err)
	}
	switch strings.ToLower(filepath.Ext(ref)) {
	case ".pine", ".pinescript", ".txt":
		return loadPine(ref, b, o)
	case ".yaml", ".yml", ".json":
		switch sniff(b) {
		case KindLLM:
			return loadLLM(ctx, ref, b, strategy.ModelRef{}, "", o)
		case "arena":
			return nil, fmt.Errorf("loader: %s is an arena file; run it with `hlp arena %s`", ref, ref)
		}
		spec, err := thesis.Parse(b)
		if err != nil {
			return nil, err
		}
		return finishThesis(ref, spec, o)
	}
	if bytes.Contains(b, []byte("strategy(")) || bytes.Contains(b, []byte("indicator(")) {
		return loadPine(ref, b, o)
	}
	return nil, fmt.Errorf("loader: cannot tell what %s is (use .pine, or .yaml for a thesis / LLM bot)", ref)
}

func isFile(s string) bool {
	switch strings.ToLower(filepath.Ext(s)) {
	case ".yaml", ".yml", ".json":
		return true
	}
	return false
}

// sniff classifies a YAML/JSON document by its top-level keys.
func sniff(b []byte) string {
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		return ""
	}
	if _, ok := m["entrants"]; ok {
		return "arena"
	}
	if _, ok := m["prompt"]; ok {
		return KindLLM
	}
	return KindThesis
}

func loadPine(ref string, b []byte, o Options) (*Loaded, error) {
	coin := o.Coin
	if coin == "" {
		coin = "BTC"
	}
	if o.Resolve != nil {
		if m, err := o.Resolve(coin); err == nil {
			coin = m.Coin
		} else {
			return nil, fmt.Errorf("loader: %s: %w", coin, err)
		}
	}
	if o.CompilePine != nil {
		st, warns, err := o.CompilePine(string(b), coin, o.Interval, o.Inputs)
		if err != nil {
			return nil, err
		}
		return &Loaded{Strategy: st, Kind: KindPine, Source: ref, Name: st.Spec().Name, Warnings: warns}, nil
	}
	s, err := pine.Parse(string(b))
	if err != nil {
		return nil, err
	}
	var po pine.Options
	if o.SzDecimals != nil {
		po.SzDecimals = o.SzDecimals(coin)
	}
	st, err := pine.CompileWith(s, coin, o.Interval, o.Inputs, po)
	if err != nil {
		return nil, err
	}
	var warns []string
	if !s.IsStrat {
		warns = append(warns, "indicator() script: plots only, no trades")
	}
	return &Loaded{Strategy: st, Kind: KindPine, Source: ref, Name: st.Spec().Name, Pine: s, Warnings: warns}, nil
}

func loadThesisText(ref, text string, o Options) (*Loaded, error) {
	spec, err := thesis.ParseText(text)
	if err != nil {
		return nil, err
	}
	return finishThesis(ref, spec, o)
}

func finishThesis(ref string, spec thesis.Spec, o Options) (*Loaded, error) {
	var warns []string
	if o.Resolve != nil {
		r, w, err := spec.Resolved(o.Resolve)
		if err != nil {
			return nil, err
		}
		spec, warns = r, w
	}
	return &Loaded{Strategy: spec.Strategy(), Kind: KindThesis, Source: ref, Name: spec.Name, Thesis: &spec, Warnings: warns}, nil
}

func loadLLM(ctx context.Context, ref string, b []byte, model strategy.ModelRef, name string, o Options) (*Loaded, error) {
	cfg, err := strategy.ParseLLMConfig(b)
	if err != nil {
		return nil, err
	}
	if !model.IsZero() {
		cfg.Model = model
	}
	if name != "" {
		cfg.Name = name
	}
	return buildLLM(ctx, ref, cfg, o)
}

func buildLLM(ctx context.Context, ref string, cfg strategy.LLMConfig, o Options) (*Loaded, error) {
	var warns []string
	cfg.Universe = append([]string(nil), cfg.Universe...)
	if o.Resolve != nil {
		var keep []string
		seen := map[string]bool{}
		for _, c := range cfg.Universe {
			m, err := o.Resolve(c)
			if err != nil {
				if cfg.Scout == nil {
					return nil, fmt.Errorf("loader: llm universe %s: %w", c, err)
				}
				// A scout pool may name markets this network lacks
				// (HIP-3 listings come and go): skip them.
				warns = append(warns, fmt.Sprintf("scout pool: %s is not a live market, skipped", c))
				continue
			}
			if !seen[m.Coin] {
				seen[m.Coin] = true
				keep = append(keep, m.Coin)
			}
		}
		if len(keep) == 0 {
			return nil, errors.New("loader: llm universe: no live market in the pool")
		}
		cfg.Universe = keep
		resolve := o.Resolve
		cfg.Resolve = func(s string) (string, error) {
			m, err := resolve(s)
			return m.Coin, err
		}
	}
	cfg.MacroSource = o.Macro
	p, err := provider(ctx, cfg.Model, o)
	if err != nil {
		return nil, err
	}
	if o.Wrap != nil {
		p = o.Wrap(p)
	}
	if !p.Local() {
		if o.RefuseRemote {
			return nil, fmt.Errorf("loader: %s is a remote (paid) model; agent and MCP callers may only start bots on local models unless llm.allow_remote_bots = true", p.Name())
		}
		warns = append(warns, fmt.Sprintf("%s is a remote (paid) model: calls are capped at max_calls", p.Name()))
	}
	st, err := strategy.NewLLM(p, cfg)
	if err != nil {
		return nil, err
	}
	return &Loaded{Strategy: st, Kind: KindLLM, Source: ref, Name: cfg.Name, LLM: &cfg, Model: cfg.Model, Warnings: warns}, nil
}

func provider(ctx context.Context, ref strategy.ModelRef, o Options) (llm.Provider, error) {
	if ref.IsZero() {
		if o.Default != nil {
			return o.Default, nil
		}
		if p := llm.Detect(ctx); p != nil {
			return p, nil
		}
		return nil, errors.New("loader: no LLM configured and no local model found (start Ollama or LM Studio, or set [llm] in config)")
	}
	if o.Provider != nil {
		return o.Provider(ctx, ref)
	}
	p, err := llm.New(ref.LLMConfig(), o.Secrets)
	if err != nil {
		return nil, fmt.Errorf("loader: model %s: %w", ref, err)
	}
	if p == nil {
		return nil, fmt.Errorf("loader: model %s: no provider", ref)
	}
	return p, nil
}

// DefaultScoutPool is the candidate pool of bare "llm:<model>" entrants:
// crypto majors plus HIP-3 equity-index and stock perps. Markets missing
// on the network are skipped at load.
var DefaultScoutPool = []string{"BTC", "ETH", "SOL", "HYPE", "DOGE", "xyz:SP500", "xyz:XYZ100", "xyz:NVDA", "xyz:TSLA", "xyz:GOLD"}

// DefaultLLMConfig is the bot used for bare "llm:<model>" entrants
// ("llm:openrouter/anthropic/claude-sonnet-5"): 1h decisions, a neutral
// discretionary-trader prompt, and the two-step loop: a scout nominates up
// to 4 coins from DefaultScoutPool, then the model decides on those and
// its open positions (decisions mode, 3x per position, 2x gross).
func DefaultLLMConfig() strategy.LLMConfig {
	return strategy.LLMConfig{
		Name:        "LLM trader",
		Universe:    append([]string(nil), DefaultScoutPool...),
		Interval:    domain.I1h,
		MaxGross:    2,
		MaxLeverage: 3,
		Lookback:    48,
		Scout:       &strategy.ScoutConfig{Max: 4, Every: 1},
		Prompt: "You are a disciplined discretionary trader of crypto and equity-index perps. Trade on a multi-hour horizon. " +
			"Size positions by conviction and volatility, cut losers quickly, and stay flat when there is no edge.",
	}
}

// ---- arenas ----

// ArenaSpec is an arena file (examples/arena/*.yaml).
type ArenaSpec struct {
	Name     string          `yaml:"name"`
	Capital  float64         `yaml:"capital"`
	Interval domain.Interval `yaml:"interval"` // default for Pine entrants
	Entrants []EntrantSpec   `yaml:"entrants"`
	// Vaults are real vaults to watch head to head (addresses or names).
	Vaults []string `yaml:"vaults,omitempty"`
	// Bench is the buy-and-hold benchmark for vsBench: a coin (BTC, HYPE,
	// SP500) or "ew" (equal weight of the entrants' coins, the default).
	Bench string `yaml:"bench,omitempty"`
}

// EntrantSpec is one arena entrant. Set exactly one of Pine, Thesis or LLM.
type EntrantSpec struct {
	Name     string          `yaml:"name,omitempty"`
	Pine     string          `yaml:"pine,omitempty"`
	Coin     string          `yaml:"coin,omitempty"`
	Interval domain.Interval `yaml:"interval,omitempty"`
	Inputs   map[string]any  `yaml:"inputs,omitempty"`
	Thesis   string          `yaml:"thesis,omitempty"` // file or inline text
	LLM      string          `yaml:"llm,omitempty"`    // bot file or provider:model
	Model    string          `yaml:"model,omitempty"`  // overrides the bot's model
}

// ParseArena decodes and checks an arena file.
func ParseArena(b []byte) (ArenaSpec, error) {
	var a ArenaSpec
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&a); err != nil {
		return a, fmt.Errorf("arena: %w", err)
	}
	if a.Name == "" {
		a.Name = "Arena"
	}
	if a.Capital == 0 {
		a.Capital = 10000
	}
	if a.Capital < 0 {
		return a, errors.New("arena: capital must be positive")
	}
	if a.Interval == "" {
		a.Interval = domain.I1h
	}
	if !a.Interval.Valid() {
		return a, fmt.Errorf("arena: invalid interval %q", a.Interval)
	}
	if len(a.Entrants)+len(a.Vaults) < 2 {
		return a, errors.New("arena: needs at least two entrants")
	}
	for i, e := range a.Entrants {
		n := 0
		for _, s := range []string{e.Pine, e.Thesis, e.LLM} {
			if s != "" {
				n++
			}
		}
		if n != 1 {
			return a, fmt.Errorf("arena: entrant %d must set exactly one of pine, thesis or llm", i+1)
		}
		if e.Interval != "" && !e.Interval.Valid() {
			return a, fmt.Errorf("arena: entrant %d: invalid interval %q", i+1, e.Interval)
		}
	}
	return a, nil
}

// LoadArena parses an arena file and loads every entrant. Relative paths
// resolve against the arena file's directory.
func LoadArena(ctx context.Context, path string, o Options) (ArenaSpec, []*Loaded, error) {
	if o.ReadFile == nil {
		o.ReadFile = ReadCapped
	}
	b, err := o.ReadFile(path)
	if err != nil {
		return ArenaSpec{}, nil, fmt.Errorf("arena: %w", err)
	}
	a, err := ParseArena(b)
	if err != nil {
		return a, nil, err
	}
	dir := filepath.Dir(path)
	rel := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(dir, p)
	}
	var out []*Loaded
	for i, e := range a.Entrants {
		eo := o
		eo.Interval = a.Interval
		if e.Interval != "" {
			eo.Interval = e.Interval
		}
		if e.Coin != "" {
			eo.Coin = e.Coin
		}
		eo.Inputs = e.Inputs
		var l *Loaded
		switch {
		case e.Pine != "":
			l, err = Load(ctx, rel(e.Pine), eo)
		case e.Thesis != "":
			if isFile(e.Thesis) {
				l, err = Load(ctx, rel(e.Thesis), eo)
			} else {
				l, err = loadThesisText(e.Thesis, e.Thesis, eo)
			}
		case e.LLM != "":
			if isFile(e.LLM) {
				var bb []byte
				bb, err = eo.ReadFile(rel(e.LLM))
				if err == nil {
					l, err = loadLLM(ctx, rel(e.LLM), bb, strategy.ParseModelRef(e.Model), e.Name, eo)
				}
			} else {
				ref := e.LLM
				if e.Model != "" {
					ref = e.Model
				}
				cfg := DefaultLLMConfig()
				cfg.Model = strategy.ParseModelRef(ref)
				cfg.Name = cfg.Model.Model
				if e.Name != "" {
					cfg.Name = e.Name
				}
				l, err = buildLLM(ctx, "llm:"+ref, cfg, eo)
			}
		}
		if err != nil {
			return a, nil, fmt.Errorf("arena: entrant %d: %w", i+1, err)
		}
		if e.Name != "" && l.Kind != KindLLM {
			l.Strategy = renamed{l.Strategy, e.Name}
		}
		if e.Name != "" {
			l.Name = e.Name
		}
		out = append(out, l)
	}
	return a, out, nil
}

// renamed overrides a strategy's display name.
type renamed struct {
	strategy.Strategy
	name string
}

func (r renamed) Spec() strategy.Spec {
	s := r.Strategy.Spec()
	s.Name = r.name
	return s
}

// LastFailure forwards to the wrapped strategy when it reports failures.
func (r renamed) LastFailure() string {
	if f, ok := r.Strategy.(strategy.Failer); ok {
		return f.LastFailure()
	}
	return ""
}

// LastDecision forwards to the wrapped strategy when it records decisions.
func (r renamed) LastDecision() *strategy.Decision {
	if d, ok := r.Strategy.(strategy.Decider); ok {
		return d.LastDecision()
	}
	return nil
}

// EstimateCalls forwards to the wrapped strategy when it calls a model.
func (r renamed) EstimateCalls(bars int) strategy.CallEstimate {
	if e, ok := r.Strategy.(strategy.CallEstimator); ok {
		return e.EstimateCalls(bars)
	}
	return strategy.CallEstimate{}
}

// ModelID forwards to the wrapped strategy when one model drives it.
func (r renamed) ModelID() string {
	if m, ok := r.Strategy.(strategy.ModelIDer); ok {
		return m.ModelID()
	}
	return ""
}

// LastNote forwards to the wrapped strategy when it explains itself.
func (r renamed) LastNote() string {
	if n, ok := r.Strategy.(strategy.Noter); ok {
		return n.LastNote()
	}
	return ""
}
