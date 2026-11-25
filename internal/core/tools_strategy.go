package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/maxmoneycash/hlp/examples"
	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/hyperliquid"
	"github.com/maxmoneycash/hlp/internal/hyperliquid/ws"
	"github.com/maxmoneycash/hlp/internal/llm"
	"github.com/maxmoneycash/hlp/internal/metrics"
	"github.com/maxmoneycash/hlp/internal/pineforge"
	"github.com/maxmoneycash/hlp/internal/pyne"
	"github.com/maxmoneycash/hlp/internal/strategy"
	"github.com/maxmoneycash/hlp/internal/strategy/backtest"
	"github.com/maxmoneycash/hlp/internal/strategy/loader"
	"github.com/maxmoneycash/hlp/internal/strategy/paper"
	"github.com/maxmoneycash/hlp/internal/strategy/pine"
	"github.com/maxmoneycash/hlp/internal/strategy/sim"
	"github.com/maxmoneycash/hlp/internal/strategy/thesis"
)

// maxStrategyFile bounds strategy files read on behalf of agents and MCP.
const maxStrategyFile = loader.MaxFileBytes

// maxExternalBars caps the candles handed to an external engine (PYNE's
// own limit; PineForge gets the same most recent window).
const maxExternalBars = 5000

// agentSessionLifetime bounds paper sessions and arenas started by agent
// and MCP callers, so one injected tool call cannot run (and spend on a
// model) until the process exits (audit S8).
const agentSessionLifetime = 24 * time.Hour

// agentCaller reports whether ctx is an untrusted caller (agent, MCP or a
// ctx with no source): those read strategy files only under the allowed
// roots, start local-model bots only and get bounded sessions (audit S8).
// Only cli and tui are trusted, matching engine.go's trustedSource.
func agentCaller(ctx context.Context) bool {
	return !trustedSource(ctx)
}

// examplePrefix names a strategy embedded in the binary (examples/):
// example:ema-cross, example:hype-vs-alts, example:arena/ai-vs-thesis.yaml.
// Internally it is rooted as example:/<dir>/<file> so relative entrant
// paths inside an embedded arena file resolve within the embedded tree.
const examplePrefix = "example:"

// inlinePineName is the virtual file name of Pine source passed inline
// (the source input of run_backtest, run_pine and pine_check).
const inlinePineName = "inline-source.pine"

type inlineSourceKey struct{}

// withInlineSource carries inline Pine source for readStrategyFile.
func withInlineSource(ctx context.Context, src string) context.Context {
	return context.WithValue(ctx, inlineSourceKey{}, src)
}

// inlineSource moves an inline source input onto ctx and returns the
// virtual file name to load it by (file must be empty).
func inlineSource(ctx context.Context, file, src string) (context.Context, string, error) {
	if src == "" {
		return ctx, file, nil
	}
	if file != "" {
		return ctx, file, Errorf(CodeUsage, "pass either file or source, not both")
	}
	if len(src) > maxInlineSource {
		return ctx, file, Errorf(CodeUsage, "source is larger than %d KiB; save it under %s and pass file", maxInlineSource>>10, "<data dir>/strategies")
	}
	return withInlineSource(ctx, src), inlinePineName, nil
}

// maxInlineSource caps inline Pine source.
const maxInlineSource = 256 << 10

// normalizeExampleRef turns example:<name> into the rooted virtual path
// example:/<dir>/<file>. Bare names are looked up in pine/, thesis/,
// llm/ and arena/ in that order. Other refs are returned unchanged.
func normalizeExampleRef(ref string) string {
	if !strings.HasPrefix(ref, examplePrefix) {
		return ref
	}
	name := strings.TrimPrefix(filepath.ToSlash(strings.TrimPrefix(ref, examplePrefix)), "/")
	clean := path.Clean("/" + name)[1:]
	if _, err := fs.Stat(examples.FS, clean); err == nil {
		return examplePrefix + "/" + clean
	}
	for _, dir := range []string{"pine", "thesis", "llm", "arena"} {
		for _, ext := range []string{"", ".pine", ".yaml", ".yml", ".json"} {
			cand := dir + "/" + clean + ext
			if st, err := fs.Stat(examples.FS, cand); err == nil && !st.IsDir() {
				return examplePrefix + "/" + cand
			}
		}
	}
	return examplePrefix + "/" + clean
}

// exampleNames lists the embedded examples as example:<dir>/<name>.
func exampleNames() []string {
	var out []string
	_ = fs.WalkDir(examples.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, examplePrefix+strings.TrimSuffix(p, path.Ext(p)))
		}
		return nil
	})
	return out
}

// readExample reads an embedded example by its rooted virtual path.
func readExample(ref string) ([]byte, bool) {
	name := strings.TrimPrefix(filepath.ToSlash(strings.TrimPrefix(normalizeExampleRef(ref), examplePrefix)), "/")
	if !fs.ValidPath(name) {
		return nil, false
	}
	b, err := fs.ReadFile(examples.FS, name)
	return b, err == nil
}

// broadDir reports whether dir is too broad to serve as a strategy root
// for untrusted callers: the file-system root, the home directory or an
// ancestor of it. MCP hosts often start servers with cwd / or $HOME.
func broadDir(dir string) bool {
	dir = filepath.Clean(dir)
	if dir == filepath.VolumeName(dir)+string(filepath.Separator) || dir == "." {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	home = filepath.Clean(home)
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	if dir == home {
		return true
	}
	rel, err := filepath.Rel(dir, home)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// strategyRoots are the directories untrusted callers may read strategy
// files from: DataDir/strategies, config strategy.roots, and the working
// directory, or only its examples/ subtree when the working directory is
// /, $HOME or an ancestor of $HOME. The embedded examples
// (example:<name>) need no root.
func strategyRoots(env *Env) []string {
	var roots []string
	if env != nil && env.Paths.DataDir != "" {
		roots = append(roots, filepath.Join(env.Paths.DataDir, "strategies"))
	}
	if env != nil {
		for _, r := range env.Config.Strategy.Roots {
			if r = strings.TrimSpace(r); r != "" && filepath.IsAbs(r) && !broadDir(r) {
				roots = append(roots, r)
			}
		}
	}
	if wd, err := os.Getwd(); err == nil {
		if broadDir(wd) {
			roots = append(roots, filepath.Join(wd, "examples"))
		} else {
			roots = append(roots, wd)
		}
	}
	return roots
}

// strategyRefusal is the one error untrusted callers get for any path they
// may not read (missing, outside the roots, not a regular file, too large),
// so the strategy tools are not an existence or size oracle.
func strategyRefusal(env *Env, p string) *Error {
	roots := strategyRoots(env)
	return &Error{Code: CodeSafety, Message: "cannot read " + p + " as a strategy file",
		Hint:    "agents read strategy files only under " + strings.Join(roots, ", ") + "; or pass example:<name> (e.g. example:ema-cross) or inline Pine as source",
		Details: map[string]any{"roots": roots, "examples": exampleNames()}}
}

// checkStrategyPath refuses, for untrusted callers, a path whose real
// location (symlinks resolved) is outside strategyRoots. It runs before
// any Stat of the path, and every refusal is the same error.
func checkStrategyPath(ctx context.Context, env *Env, p string) error {
	if !agentCaller(ctx) {
		return nil
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return strategyRefusal(env, p)
	}
	// Resolve symlinks on the directory only: resolving the file itself
	// would distinguish a missing file from an outside one.
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return strategyRefusal(env, p)
	}
	real := filepath.Join(dir, filepath.Base(abs))
	if r, err := filepath.EvalSymlinks(real); err == nil {
		real = r
	}
	for _, root := range strategyRoots(env) {
		r, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(r, real); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return nil
		}
	}
	return strategyRefusal(env, p)
}

// readStrategyBytes reads one strategy ref: an embedded example, inline
// source, or a file (path-checked for untrusted callers before any Stat;
// capped at 1 MiB).
func readStrategyBytes(ctx context.Context, env *Env, p string) ([]byte, error) {
	if strings.HasPrefix(p, examplePrefix) {
		if b, ok := readExample(p); ok {
			return b, nil
		}
		return nil, &Error{Code: CodeUsage, Message: "no example named " + strings.TrimPrefix(p, examplePrefix),
			Hint: "examples: " + strings.Join(exampleNames(), ", ")}
	}
	if p == inlinePineName {
		if src, ok := ctx.Value(inlineSourceKey{}).(string); ok {
			return []byte(src), nil
		}
	}
	if err := checkStrategyPath(ctx, env, p); err != nil {
		return nil, err
	}
	b, err := loader.ReadCapped(p)
	if err != nil {
		if agentCaller(ctx) {
			return nil, strategyRefusal(env, p)
		}
		return nil, err
	}
	return b, nil
}

// strategyReader is the loader's ReadFile for this call: embedded
// examples, capped reads, plus the untrusted-caller path rule.
func strategyReader(ctx context.Context, env *Env) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		return readStrategyBytes(ctx, env, filepath.ToSlash(p))
	}
}

// readStrategyFile reads a .pine, .yaml, .yml or .json file: a regular
// file of at most 1 MiB. The extension whitelist keeps agents from using
// strategy tools to read arbitrary files; untrusted callers are also held
// to strategyRoots, and get one generic refusal for every failure.
func readStrategyFile(ctx context.Context, env *Env, p string) (string, error) {
	p = normalizeExampleRef(p)
	ext := strings.ToLower(filepath.Ext(p))
	switch ext {
	case ".pine", ".yaml", ".yml", ".json":
	default:
		return "", Errorf(CodeUsage, "%s: strategy files must be .pine, .yaml, .yml or .json", p)
	}
	if strings.HasPrefix(p, examplePrefix) || p == inlinePineName && ctx.Value(inlineSourceKey{}) != nil {
		b, err := readStrategyBytes(ctx, env, p)
		return string(b), err
	}
	if agentCaller(ctx) {
		b, err := readStrategyBytes(ctx, env, p)
		return string(b), err
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", &Error{Code: CodeUsage, Message: "cannot read " + p + ": " + strings.TrimPrefix(err.Error(), "stat "+p+": "), Hint: "examples live in examples/pine and examples/thesis, or run example:<name>"}
	}
	if !st.Mode().IsRegular() || st.Size() > maxStrategyFile {
		return "", Errorf(CodeUsage, "%s: not a strategy file (not a regular file, or larger than 1 MiB)", p)
	}
	b, err := loader.ReadCapped(p)
	if err != nil {
		return "", Errorf(CodeUsage, "cannot read %s: %v", p, err)
	}
	return string(b), nil
}

// parseRefusal hides a strategy file's content from untrusted callers:
// yaml/json errors echo field names and values (a credentials file read
// as a thesis would leak through the error).
func parseRefusal(ctx context.Context, file string, err error) *Error {
	if agentCaller(ctx) && !strings.HasPrefix(file, examplePrefix) && file != inlinePineName {
		return &Error{Code: CodeUsage, Message: file + " is not a valid strategy file", Hint: "run `hlp backtest` on it from a terminal to see the parse error"}
	}
	return &Error{Code: CodeUsage, Message: file + ": " + err.Error()}
}

// PineDiag is one positioned Pine diagnostic.
type PineDiag struct {
	Line int    `json:"line"`
	Col  int    `json:"col"`
	Msg  string `json:"msg"`
}

// PineDiags flattens a pine.Parse/Compile error (errors.Join of *pine.Error).
func PineDiags(err error) []PineDiag {
	if err == nil {
		return nil
	}
	var out []PineDiag
	var walk func(error)
	walk = func(e error) {
		if j, ok := e.(interface{ Unwrap() []error }); ok {
			for _, x := range j.Unwrap() {
				walk(x)
			}
			return
		}
		var pe *pine.Error
		if errors.As(e, &pe) {
			out = append(out, PineDiag{Line: pe.Line, Col: pe.Col, Msg: pe.Msg})
			return
		}
		out = append(out, PineDiag{Msg: e.Error()})
	}
	walk(err)
	return out
}

func pineError(file string, err error) *Error {
	return diagError(file, PineDiags(err), "hlp supports a Pine v5/v6 subset; run `hlp pine check "+file+"`")
}

func diagError(file string, ds []PineDiag, hint string) *Error {
	msg := fmt.Sprintf("%s: %d problem(s)", file, len(ds))
	if len(ds) > 0 {
		msg += fmt.Sprintf("; first at %d:%d: %s", ds[0].Line, ds[0].Col, ds[0].Msg)
	}
	return &Error{Code: CodeUsage, Message: msg, Details: ds, Hint: hint}
}

// pyneError maps a PYNE failure: script errors keep their line:col.
func pyneError(file string, err error) *Error {
	var se *pyne.ScriptError
	if errors.As(err, &se) && len(se.Diagnostics) > 0 {
		ds := make([]PineDiag, 0, len(se.Diagnostics))
		for _, d := range se.Diagnostics {
			ds = append(ds, PineDiag{Line: d.Line, Col: d.Col, Msg: d.Msg})
		}
		return diagError(file, ds, "PYNE rejected the script; run `hlp pine check "+file+" --engine pyne`")
	}
	if errors.As(err, &se) {
		return &Error{Code: CodeUsage, Message: file + ": " + err.Error()}
	}
	var te *pyne.TimeoutError
	if errors.As(err, &te) {
		return &Error{Code: CodeNetwork, Message: err.Error(), Hint: "PYNE ran too long; shorten the window with --days"}
	}
	return &Error{Code: CodeInternal, Message: err.Error()}
}

// pineForgeError maps a PineForge failure.
func pineForgeError(file string, err error) *Error {
	var pe *pineforge.Error
	if errors.As(err, &pe) {
		code := CodeInternal
		switch pe.Kind {
		case "compile", "transpile", "missing-input":
			code = CodeUsage
		}
		return &Error{Code: code, Message: file + ": " + pe.Error()}
	}
	var te *pineforge.TimeoutError
	if errors.As(err, &te) {
		return &Error{Code: CodeNetwork, Message: err.Error()}
	}
	return &Error{Code: CodeInternal, Message: err.Error()}
}

// rawResolver resolves through the registry, returning its raw errors (which
// wrap hyperliquid.ErrUnknownCoin) for packages that inspect them.
func (e *Env) rawResolver(ctx context.Context) func(string) (domain.Market, error) {
	return func(s string) (domain.Market, error) {
		if _, err := e.Resolve(ctx, s); err != nil {
			if ae := AsError(err); ae.Code == CodeUnknownCoin {
				return domain.Market{}, fmt.Errorf("%w: %s", hyperliquid.ErrUnknownCoin, s)
			}
			return domain.Market{}, err
		}
		return e.HL.Registry().Resolve(s)
	}
}

// szDecimals returns coin's size decimals from the registry (0 when
// unknown).
func (e *Env) szDecimals(ctx context.Context) func(string) int {
	return func(coin string) int {
		if m, err := e.rawResolver(ctx)(coin); err == nil {
			return m.SzDecimals
		}
		return 0
	}
}

// marketRules returns the exchange rules (lot size, $10 minimum, max
// leverage) for coins, from the registry; coins it cannot resolve get none.
func marketRules(ctx context.Context, env *Env, coins []string) map[string]sim.Rules {
	out := map[string]sim.Rules{}
	for _, c := range coins {
		if m, err := env.rawResolver(ctx)(c); err == nil {
			out[c] = sim.RulesFrom(m)
		}
	}
	return out
}

// loadedStrategy is a strategy ready to run, or, for an external engine's
// one-shot backtest, the Pine source it runs.
type loadedStrategy struct {
	S     strategy.Strategy // nil for an external backtest
	Warns []string
	Eng   engineSel
	// External backtest inputs.
	File, Src, Coin string
	Interval        domain.Interval
}

// loadStrategy builds a Strategy from a file (.pine, thesis .yaml/.json,
// llm:<config.yaml|model>) or an inline thesis. The engine choice applies
// to Pine files only. For an external engine and purposeBacktest the
// strategy is not built: the caller runs the engine over the candles.
func loadStrategy(ctx context.Context, env *Env, file, inline, coin string, iv domain.Interval, opt EngineOpt, purpose string) (*loadedStrategy, error) {
	switch {
	case file != "" && inline != "":
		return nil, Errorf(CodeUsage, "pass either a file or an inline thesis, not both")
	case strings.HasPrefix(file, "llm:"):
		s, w, err := loadLLMStrategy(ctx, env, strings.TrimPrefix(file, "llm:"), iv)
		if err != nil {
			return nil, err
		}
		return &loadedStrategy{S: s, Warns: w, Eng: engineSel{Kind: EngineBuiltin}}, nil
	case inline != "":
		spec, err := thesis.ParseText(inline)
		if err != nil {
			return nil, &Error{Code: CodeUsage, Message: err.Error(), Hint: `e.g. "long xyz:SP500 1.5x, short DOGE FARTCOIN TRUMP, rebalance 1w"`}
		}
		s, w, err := thesisStrategy(ctx, env, spec, iv)
		if err != nil {
			return nil, err
		}
		return &loadedStrategy{S: s, Warns: w, Eng: engineSel{Kind: EngineBuiltin}}, nil
	case file == "":
		return nil, &Error{Code: CodeUsage, Message: "a strategy is required", Hint: "pass a .pine file, a thesis .yaml, or --thesis \"long BTC, short memes\""}
	}
	file = normalizeExampleRef(file)
	src, err := readStrategyFile(ctx, env, file)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(filepath.Ext(file), ".pine") {
		spec, err := thesis.Parse([]byte(src))
		if err != nil {
			return nil, parseRefusal(ctx, file, err)
		}
		s, w, err := thesisStrategy(ctx, env, spec, iv)
		if err != nil {
			return nil, err
		}
		return &loadedStrategy{S: s, Warns: w, Eng: engineSel{Kind: EngineBuiltin}}, nil
	}
	if coin == "" {
		return nil, &Error{Code: CodeUsage, Message: "Pine strategies run on one market", Hint: "add --coin BTC (or any market)"}
	}
	eng, warns, err := resolveEngine(ctx, env, opt, purpose)
	if err != nil {
		return nil, err
	}
	m, err := env.Resolve(ctx, coin)
	if err != nil {
		return nil, err
	}
	if iv == "" {
		iv = domain.I1h
	}
	if !iv.Valid() {
		return nil, &Error{Code: CodeUsage, Message: fmt.Sprintf("interval %q is not supported", iv), Hint: "use one of " + intervalList()}
	}
	ls := &loadedStrategy{Warns: warns, Eng: eng, File: file, Src: src, Coin: m.Coin, Interval: iv}
	switch eng.Kind {
	case EnginePyne:
		if purpose == purposeBacktest {
			return ls, nil
		}
		f, err := eng.Pyne.Compile(ctx, src, m.Coin, iv, nil)
		if err != nil {
			return nil, pyneError(file, err)
		}
		ls.S = f
		ls.Warns = append(ls.Warns, "engine: PYNE "+eng.Version+" (external): one pyne run per bar, orders follow its position at hlp's costs")
		return ls, nil
	case EnginePineForge:
		return ls, nil // backtest only (resolveEngine refuses the rest)
	}
	script, err := pine.Parse(src)
	if err != nil {
		return nil, pineError(file, err)
	}
	s, err := pine.CompileWith(script, m.Coin, iv, nil, pine.Options{SzDecimals: m.SzDecimals})
	if err != nil {
		return nil, pineError(file, err)
	}
	ls.S = s
	return ls, nil
}

func thesisStrategy(ctx context.Context, env *Env, spec thesis.Spec, iv domain.Interval) (strategy.Strategy, []string, error) {
	if iv != "" {
		if !iv.Valid() {
			return nil, nil, &Error{Code: CodeUsage, Message: fmt.Sprintf("interval %q is not supported", iv), Hint: "use one of " + intervalList()}
		}
		spec.Interval = iv
	}
	warns, err := spec.Validate(env.rawResolver(ctx))
	if err != nil {
		msg := err.Error()
		if !strings.HasPrefix(msg, "thesis") {
			msg = "thesis: " + msg
		}
		return nil, warns, &Error{Code: CodeUsage, Message: msg}
	}
	s := spec.Strategy()
	if s == nil {
		return nil, warns, Errorf(CodeInternal, "thesis produced no strategy")
	}
	return s, warns, nil
}

// loaderOptions wires the strategy loader to this Env: coin resolution,
// secrets, capped (and for agents, rooted) file reads, the configured [llm]
// provider as the default model for bots that do not name one, the Pine
// engine (eng) and the remote-model rule. Bots that name a model
// (model: ollama:llama3.1:8b) get that model.
func loaderOptions(ctx context.Context, env *Env, coin string, iv domain.Interval, eng engineSel) loader.Options {
	o := loader.Options{Coin: coin, Interval: iv, Resolve: env.rawResolver(ctx), Secrets: env.Secrets,
		ReadFile: strategyReader(ctx, env), SzDecimals: env.szDecimals(ctx),
		RefuseRemote: agentCaller(ctx) && !env.Config.LLM.AllowRemoteBots}
	if env.Config.LLM.Provider != "" {
		if p, err := llm.New(env.Config.LLM, env.Secrets); err == nil && p != nil {
			o.Default = p
		}
	}
	if r := llmRunFrom(ctx); r != nil {
		o.Wrap = r.budget.Wrap
		if r.macro != nil {
			o.Macro = r.macro
		}
	}
	if eng.Kind == EnginePyne && eng.Pyne != nil {
		pe := eng.Pyne
		o.CompilePine = func(src, coin string, iv domain.Interval, inputs map[string]any) (strategy.Strategy, []string, error) {
			f, err := pe.Compile(ctx, src, coin, iv, inputs)
			if err != nil {
				return nil, nil, err
			}
			return f, []string{"engine: PYNE " + pe.Version}, nil
		}
	}
	return o
}

// loadLLMStrategy builds an LLM bot from llm:<config.yaml> (its own model:
// field wins) or llm:<provider:model>, falling back to config [llm] and then
// a detected local model (Ollama, LM Studio).
func loadLLMStrategy(ctx context.Context, env *Env, spec string, iv domain.Interval) (strategy.Strategy, []string, error) {
	if strings.TrimSpace(spec) == "" {
		if env.Config.LLM.Provider == "" {
			return nil, nil, &Error{Code: CodeUsage, Message: "LLM strategies need a model", Hint: "use llm:<bot.yaml> with a model: field, llm:ollama:<model>, or set [llm] in config.toml (local Ollama or LM Studio needs no key)"}
		}
		spec = env.Config.LLM.Provider + ":" + env.Config.LLM.Model
	}
	ext := strings.ToLower(filepath.Ext(spec))
	if ext == ".yaml" || ext == ".yml" {
		if _, err := readStrategyFile(ctx, env, spec); err != nil {
			return nil, nil, err
		}
	}
	l, err := loader.Load(ctx, "llm:"+spec, loaderOptions(ctx, env, "", iv, engineSel{Kind: EngineBuiltin}))
	if err != nil {
		var ce *Error
		if errors.As(err, &ce) {
			return nil, nil, ce
		}
		return nil, nil, &Error{Code: CodeUsage, Message: strings.TrimPrefix(err.Error(), "loader: "),
			Hint: "set model: in the bot YAML, pass llm:ollama:<model>, or configure [llm] in config.toml"}
	}
	return l.Strategy, l.Warnings, nil
}

// ---- backtest ----

func runBacktest(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*BacktestInput)
	if p.Capital < 0 || p.FeesBps < 0 || p.Days < 0 {
		return nil, Errorf(CodeUsage, "capital, fees-bps and days must be positive")
	}
	ctx, file, err := inlineSource(ctx, p.File, p.Source)
	if err != nil {
		return nil, err
	}
	run := newLLMRun(env, p.LLMGuard, purposeBacktest)
	ctx = withLLMRun(ctx, run)
	ls, err := loadStrategy(ctx, env, file, p.Thesis, p.Coin, p.Interval, p.EngineOpt, purposeBacktest)
	if err != nil {
		return nil, err
	}
	var llmLine string
	if ls.S != nil {
		if llmLine, err = guardLLM(ctx, env, p.LLMGuard, []strategy.Strategy{ls.S}, backtestBars(ls.S, p.Days)); err != nil {
			return nil, err
		}
	}
	capital := p.Capital
	if capital == 0 {
		capital = 10_000
	}
	costs := strategy.DefaultCosts
	if p.FeesBps > 0 {
		costs.TakerBps = p.FeesBps
	}
	now := env.now()
	var start int64
	benchFrom := now.AddDate(-1, 0, 0)
	if p.Days > 0 {
		start = now.Add(-time.Duration(p.Days) * 24 * time.Hour).UnixMilli()
		benchFrom = time.UnixMilli(start)
	}
	names := p.Bench
	if len(names) == 0 {
		names = []string{"HLP", "BTC", "HYPE"}
	}
	meta := env.meta("backtest")
	meta.Engine = ls.Eng.Kind
	if llmLine != "" {
		meta.Warnings = append(meta.Warnings, llmLine)
	}
	bench := map[string]domain.Series{}
	series := make([]domain.Series, len(names))
	errs := make([]error, len(names))
	parallel(len(names), func(i int) {
		series[i], errs[i] = benchmarkCurve(ctx, env, names[i], benchFrom, now)
	})
	for i, n := range names {
		if errs[i] != nil {
			meta.Warnings = append(meta.Warnings, "benchmark "+strings.ToUpper(n)+": "+AsError(errs[i]).Message)
			continue
		}
		bench[strings.ToUpper(n)] = series[i]
	}
	opts := backtest.Options{Capital: capital, Start: start, Costs: costs, Benchmarks: bench, Now: env.now}
	switch {
	case env.ProgressN != nil:
		opts.Progress = func(stage string, done, total int) {
			env.ProgressN("fetching "+stage+" history", done, total)
		}
	case env.Progress != nil:
		opts.Progress = func(stage string, done, total int) {
			env.Progress(fmt.Sprintf("fetching %s history %d/%d", stage, done, total))
		}
	}
	var res *backtest.Result
	if ls.S == nil {
		res, err = externalBacktest(ctx, env, ls, opts)
	} else {
		opts.Markets = marketRules(ctx, env, ls.S.Spec().Universe)
		res, err = backtest.Run(ctx, ls.S, env.HL, opts)
	}
	if err != nil {
		return nil, err
	}
	data := &BacktestData{
		Strategy:      res.Spec.Name,
		Engine:        ls.Eng.Kind,
		EngineVersion: ls.Eng.Version,
		Tearsheet:     res.Tearsheet,
		Fills:         res.Fills,
		Candles:       res.Candles,
		Markers:       res.Markers,
		Plots:         res.Plots,
		Logs:          res.Logs,
		Warnings:      append(ls.Warns, res.Warnings...),
	}
	if data.Strategy == "" && ls.S != nil {
		data.Strategy = ls.S.Spec().Name
	}
	if res.Spec.Kind == "thesis" {
		// Win rate and profit factor count only the realized P&L of
		// rebalance trims; they ignore funding and the open legs, so for a
		// rebalancing basket they contradict the return. They are only
		// meaningful for discrete-trade (Pine) strategies.
		data.Tearsheet.Metrics.WinRate, data.Tearsheet.Metrics.ProfitFactor = 0, 0
		data.Tearsheet.Metrics.WinRateKnown, data.Tearsheet.Metrics.ProfitFactorKnown = false, false
	}
	if ls.Eng.Kind != EngineBuiltin {
		meta.Warnings = append(meta.Warnings, engineWarning(ls.Eng))
	}
	// Keep the tearsheet's own warnings (a short window makes annualized
	// metrics unreliable, AGENT-UX #24) when its meta is replaced.
	for _, w := range res.Tearsheet.Meta.Warnings {
		if !containsString(meta.Warnings, w) {
			meta.Warnings = append(meta.Warnings, w)
		}
	}
	// External engines build their own tearsheet: apply the short-window
	// rule here too, from the metrics span or the candles simulated.
	if w := metrics.ShortWindowWarning(backtestDays(res)); w != "" && !containsString(meta.Warnings, w) {
		meta.Warnings = append(meta.Warnings, w)
	}
	if a := res.Account; a != (sim.Stats{}) {
		data.Account = &a
	}
	benchName := "ew"
	if len(p.Bench) > 0 {
		benchName = p.Bench[0]
	}
	if b := backtestBench(benchName, res.Spec.Universe, res.Candles, data.Tearsheet); b != nil {
		data.Bench, data.VsBench = b, vsBench(data.Tearsheet.Metrics.TotalReturn, b)
	}
	if m, ok := ls.S.(strategy.ModelIDer); ok && ls.S != nil {
		data.Model = m.ModelID()
	}
	data.LLM = run.stats()
	meta.Warnings = dropPrefixDupes(meta.Warnings)
	data.Tearsheet.Meta = meta
	return &Result{Card: CardBacktest, Title: "Backtest " + data.Strategy, Data: data, Meta: meta}, nil
}

// dropPrefixDupes removes a warning that starts with another, shorter one
// ("engine: PYNE 0.7.0 (AGPL-3.0, external)" beside "engine: PYNE 0.7.0"),
// keeping the short form in meta; data.warnings keeps the long one.
func dropPrefixDupes(ws []string) []string {
	out := ws[:0:0]
	for i, w := range ws {
		dup := false
		for j, o := range ws {
			if i != j && len(w) > len(o) && strings.HasPrefix(w, o) {
				dup = true
				break
			}
		}
		if !dup && !containsString(out, w) {
			out = append(out, w)
		}
	}
	return out
}

// engineWarning is the meta warning naming an external engine.
func engineWarning(e engineSel) string {
	switch e.Kind {
	case EnginePyne:
		return "engine: PYNE " + e.Version
	case EnginePineForge:
		return "engine: PineForge " + e.Version
	}
	return "engine: " + e.Kind
}

// externalBacktest runs a Pine file once through PYNE or PineForge over
// the most recent (at most 5000) closed candles. Their brokers use the
// script's own commission settings: hlp's fees, slippage, funding,
// szDecimals and leverage are not applied, and the result says so.
// backtestDays is the span a backtest covered: the tearsheet's Days, else
// the widest candle series.
func backtestDays(res *backtest.Result) float64 {
	if d := res.Tearsheet.Metrics.Days; d > 0 {
		return d
	}
	var span int64
	for _, cs := range res.Candles {
		if len(cs) > 1 {
			span = max(span, cs[len(cs)-1].T-cs[0].T)
		}
	}
	return float64(span) / float64(24*time.Hour/time.Millisecond)
}

func externalBacktest(ctx context.Context, env *Env, ls *loadedStrategy, o backtest.Options) (*backtest.Result, error) {
	if o.Progress != nil {
		o.Progress("candles", 0, 1)
	}
	cs, warns, err := backtest.History(ctx, env.HL, ls.Coin, ls.Interval, o)
	if err != nil {
		return nil, err
	}
	if o.Progress != nil {
		o.Progress("candles", 1, 1)
	}
	if len(cs) > maxExternalBars {
		warns = append(warns, fmt.Sprintf("external engines take at most %d bars: ran the most recent %d of %d", maxExternalBars, maxExternalBars, len(cs)))
		cs = cs[len(cs)-maxExternalBars:]
	}
	var res *backtest.Result
	switch ls.Eng.Kind {
	case EnginePyne:
		res, _, err = ls.Eng.Pyne.Backtest(ctx, ls.Src, ls.Coin, ls.Interval, cs, nil)
		if err != nil {
			return nil, pyneError(ls.File, err)
		}
	case EnginePineForge:
		res, _, err = ls.Eng.Forge.Backtest(ctx, ls.Src, ls.Coin, ls.Interval, cs, pineforge.Options{})
		if err != nil {
			return nil, pineForgeError(ls.File, err)
		}
	default:
		return nil, Errorf(CodeInternal, "no external engine selected")
	}
	res.Warnings = append(res.Warnings, warns...)
	// Rebuild the tearsheet with the benchmarks and hlp's clock.
	var closed []float64
	for _, f := range res.Fills {
		if f.ClosedPnL != 0 {
			closed = append(closed, f.ClosedPnL-f.Fee)
		}
	}
	ts := metrics.Tearsheet(res.Equity, metrics.TearsheetOpts{Title: res.Spec.Name, Benchmarks: o.Benchmarks, ClosedPnL: closed, Now: o.Now()})
	if ts.Title == "" {
		ts.Title = res.Spec.Name
	}
	ts.Meta = res.Tearsheet.Meta
	ts.Meta.Warnings = append([]string(nil), res.Warnings...)
	res.Tearsheet = ts
	return res, nil
}

// ---- pine run ----

// runPine is run_backtest for one Pine file on one market.
func runPine(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*PineRunInput)
	ctx, file, err := inlineSource(ctx, p.File, p.Source)
	if err != nil {
		return nil, err
	}
	p.File = normalizeExampleRef(file)
	if p.File == "" || !strings.EqualFold(filepath.Ext(p.File), ".pine") {
		return nil, Errorf(CodeUsage, "pine run needs a .pine file, got %q", p.File)
	}
	if p.Coin == "" {
		return nil, &Error{Code: CodeUsage, Message: "Pine strategies run on one market", Hint: "add --coin BTC (or any market)"}
	}
	res, err := runBacktest(ctx, env, &BacktestInput{File: p.File, Coin: p.Coin, Interval: p.Interval,
		Days: p.Days, Capital: p.Capital, FeesBps: p.FeesBps, EngineOpt: p.EngineOpt})
	if err != nil {
		return nil, err
	}
	if d, ok := res.Data.(*BacktestData); ok {
		res.Title = "Pine " + d.Strategy
	}
	return res, nil
}

// ---- pine check ----

// PineInputInfo is one input() declared by a Pine script.
type PineInputInfo struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Default any    `json:"default"`
}

// PineCheckData is the result of pine_check: a typed object for agents
// (--select works on it) that also renders as a key/value card.
type PineCheckData struct {
	File    string          `json:"file"`
	Status  string          `json:"status"`
	Version string          `json:"version"`
	Title   string          `json:"title,omitempty"`
	Kind    string          `json:"kind"` // strategy | indicator
	Inputs  []PineInputInfo `json:"inputs"`
	Compile string          `json:"compile,omitempty"` // "BTC 1h ok" when a coin was given
	// Engine is the engine that checked the script (builtin or pyne).
	Engine        string `json:"engine,omitempty"`
	EngineVersion string `json:"engineVersion,omitempty"`
	// Warnings are the engine's non-error diagnostics ("12:3: lint ...").
	Warnings []string `json:"warnings,omitempty"`
}

// Pairs is the key/value layout of the card renderer.
func (d *PineCheckData) Pairs() [][2]string {
	rows := [][2]string{{"file", d.File}, {"status", d.Status}, {"version", d.Version}}
	if d.Title != "" {
		rows = append(rows, [2]string{"title", d.Title})
	}
	rows = append(rows, [2]string{"kind", d.Kind})
	for _, in := range d.Inputs {
		rows = append(rows, [2]string{"input " + in.Name, fmt.Sprintf("%s = %v", in.Type, in.Default)})
	}
	if d.Compile != "" {
		rows = append(rows, [2]string{"compile", d.Compile})
	}
	if d.Engine != "" && d.Engine != EngineBuiltin {
		rows = append(rows, [2]string{"engine", strings.TrimSpace(d.Engine + " " + d.EngineVersion)})
	}
	for _, w := range d.Warnings {
		rows = append(rows, [2]string{"warning", w})
	}
	return rows
}

var (
	pineVersionRE = regexp.MustCompile(`(?m)^//\s*@version\s*=\s*(\d+)`)
	pineDeclRE    = regexp.MustCompile(`(?m)^(strategy|indicator|study)\s*\(\s*(?:title\s*=\s*)?"((?:[^"\\]|\\.)*)"`)
)

func runPineCheck(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*PineInput)
	ctx, file, err := inlineSource(ctx, p.File, p.Source)
	if err != nil {
		return nil, err
	}
	p.File = normalizeExampleRef(file)
	if p.File == "" {
		return nil, Errorf(CodeUsage, "a .pine file is required")
	}
	if !strings.EqualFold(filepath.Ext(p.File), ".pine") {
		return nil, Errorf(CodeUsage, "%s is not a .pine file", p.File)
	}
	eng, warns, err := resolveEngine(ctx, env, p.EngineOpt, purposeCheck)
	if err != nil {
		return nil, err
	}
	src, err := readStrategyFile(ctx, env, p.File)
	if err != nil {
		return nil, err
	}
	meta := env.meta("pine")
	meta.Engine = eng.Kind
	meta.Warnings = append(meta.Warnings, warns...)
	if eng.Kind == EnginePyne {
		d, err := pyneCheck(ctx, env, eng, p, src)
		if err != nil {
			return nil, err
		}
		meta.Warnings = append(meta.Warnings, engineWarning(eng))
		return &Result{Card: CardKV, Title: "Pine check", Data: d, Meta: meta}, nil
	}
	script, err := pine.Parse(src)
	if err != nil {
		return nil, pineError(p.File, err)
	}
	d := &PineCheckData{File: p.File, Status: "ok", Version: fmt.Sprintf("v%d", script.Version), Title: script.Title,
		Kind: "indicator", Inputs: []PineInputInfo{}, Engine: EngineBuiltin}
	if script.IsStrat {
		d.Kind = "strategy"
	}
	for _, inp := range script.Inputs {
		d.Inputs = append(d.Inputs, PineInputInfo{Name: inp.Name, Type: inp.Kind, Default: inp.Default})
	}
	if p.Coin != "" {
		m, err := env.Resolve(ctx, p.Coin)
		if err != nil {
			return nil, err
		}
		iv := p.Interval
		if iv == "" {
			iv = domain.I1h
		}
		if _, err := pine.CompileWith(script, m.Coin, iv, nil, pine.Options{SzDecimals: m.SzDecimals}); err != nil {
			return nil, pineError(p.File, err)
		}
		d.Compile = m.Coin + " " + string(iv) + " ok"
	}
	return &Result{Card: CardKV, Title: "Pine check", Data: d, Meta: meta}, nil
}

// pyneCheck runs `pyne diagnostics`: errors become a USAGE error with
// line:col details (the builtin engine's form), warnings are listed.
func pyneCheck(ctx context.Context, env *Env, eng engineSel, p *PineInput, src string) (*PineCheckData, error) {
	ds, err := eng.Pyne.Check(ctx, src)
	if err != nil {
		return nil, pyneError(p.File, err)
	}
	if errs := pyne.Errors(ds); len(errs) > 0 {
		out := make([]PineDiag, 0, len(errs))
		for _, d := range errs {
			out = append(out, PineDiag{Line: d.Line, Col: d.Col, Msg: d.Msg})
		}
		return nil, diagError(p.File, out, "PYNE "+eng.Version+" rejected the script")
	}
	d := &PineCheckData{File: p.File, Status: "ok", Version: "v5", Kind: "indicator", Inputs: []PineInputInfo{},
		Engine: EnginePyne, EngineVersion: eng.Version}
	if m := pineVersionRE.FindStringSubmatch(src); m != nil {
		d.Version = "v" + m[1]
	}
	if m := pineDeclRE.FindStringSubmatch(src); m != nil {
		d.Title = m[2]
		if m[1] == "strategy" {
			d.Kind = "strategy"
		}
	}
	// The builtin parser, when it accepts the script, lists the inputs.
	if script, err := pine.Parse(src); err == nil {
		for _, inp := range script.Inputs {
			d.Inputs = append(d.Inputs, PineInputInfo{Name: inp.Name, Type: inp.Kind, Default: inp.Default})
		}
	}
	for _, x := range ds {
		if x.Severity != "error" {
			d.Warnings = append(d.Warnings, x.String())
		}
	}
	if p.Coin != "" {
		m, err := env.Resolve(ctx, p.Coin)
		if err != nil {
			return nil, err
		}
		iv := p.Interval
		if iv == "" {
			iv = domain.I1h
		}
		d.Compile = m.Coin + " " + string(iv) + " ok"
	}
	return d, nil
}

// ---- paper ----

// liveFeed adapts the Env (REST reader + WebSocket) into a paper.Feed.
type liveFeed struct{ env *Env }

func (f liveFeed) Candles(ctx context.Context, coin string, iv domain.Interval, n int, end int64) ([]domain.Candle, error) {
	return f.env.HL.Candles(ctx, coin, iv, n, end)
}

func (f liveFeed) Funding(ctx context.Context, coin string, start, end int64) ([]domain.FundingPoint, error) {
	return f.env.HL.Funding(ctx, coin, start, end)
}

func (f liveFeed) Book(ctx context.Context, coin string) (domain.Book, error) {
	return f.env.HL.Book(ctx, coin, hyperliquid.BookOpts{})
}

// Bars emits each closed bar: the live candle stream carries the open bar;
// a bar is closed when a newer open time arrives.
func (f liveFeed) Bars(ctx context.Context, coin string, iv domain.Interval) (<-chan domain.Candle, error) {
	if f.env.Stream == nil {
		return nil, Errorf(CodeNetwork, "live data needs the WebSocket (unavailable offline)")
	}
	ch, cancel, err := ws.Candles(ctx, f.env.Stream, coin, iv)
	if err != nil {
		return nil, err
	}
	out := make(chan domain.Candle, 4)
	go func() {
		defer close(out)
		defer cancel()
		var cur domain.Candle
		have := false
		for {
			select {
			case <-ctx.Done():
				return
			case c, ok := <-ch:
				if !ok {
					return
				}
				if have && c.T > cur.T {
					select {
					case out <- cur:
					case <-ctx.Done():
						return
					}
				}
				if !have || c.T >= cur.T {
					cur, have = c, true
				}
			}
		}
	}()
	return out, nil
}

// sessions holds paper sessions and arenas started in this process (TUI,
// MCP, agent). They run until stopped or the process exits; state persists
// under DataDir/paper.
var sessions = struct {
	sync.Mutex
	paper  map[string]paper.Session
	arenas map[string]paper.Arena
}{paper: map[string]paper.Session{}, arenas: map[string]paper.Arena{}}

// paperDir is the data dir handed to the paper package, which adds its own
// "paper" directory: sessions land in DataDir/paper/<id>.json.
func paperDir(env *Env) string { return env.Paths.DataDir }

// PaperFromSnapshot converts a paper snapshot into the card payload.
func PaperFromSnapshot(s paper.Snapshot) *PaperData {
	return &PaperData{ID: s.ID, Strategy: s.Strategy, Equity: s.Equity, Account: s.Account, Fills: s.Fills, Running: s.Running,
		Error: s.Err, LastNote: s.Note, Capital: s.Capital}
}

func paperResult(env *Env, s paper.Snapshot) *Result {
	meta := env.meta("paper")
	if s.Err != "" {
		meta.Warnings = append(meta.Warnings, s.Err)
	}
	return &Result{Card: CardPaper, Title: "Paper " + s.ID, Data: PaperFromSnapshot(s), Meta: meta}
}

// sessionContext is the context a paper session or arena runs on: it
// outlives the call that started it (TUI, MCP), and for agent and MCP
// callers it ends after agentSessionLifetime (audit S8).
func sessionContext(ctx context.Context) context.Context {
	sctx := context.WithoutCancel(ctx)
	if !agentCaller(ctx) {
		return sctx
	}
	sctx, cancel := context.WithCancel(sctx)
	time.AfterFunc(agentSessionLifetime, cancel)
	return sctx
}

// sessionRules gives a paper engine Hyperliquid's exchange rules.
func sessionRules(ctx context.Context, env *Env, s strategy.Strategy) map[string]sim.Rules {
	return marketRules(ctx, env, s.Spec().Universe)
}

func startPaper(ctx context.Context, env *Env, p *PaperInput) (paper.Session, error) {
	ctx = withLLMRun(ctx, newLLMRun(env, p.LLMGuard, purposePaper))
	ls, err := loadStrategy(ctx, env, p.File, "", p.Coin, "", p.EngineOpt, purposePaper)
	if err != nil {
		return nil, err
	}
	s := ls.S
	if _, err := guardLLM(ctx, env, p.LLMGuard, []strategy.Strategy{s}, 0); err != nil {
		return nil, err
	}
	capital := p.Capital
	if capital == 0 {
		capital = 10_000
	}
	if capital < 0 {
		return nil, Errorf(CodeUsage, "capital must be positive")
	}
	// Sessions outlive the call that started them (TUI/MCP); Stop ends them.
	sess, err := paper.Start(sessionContext(ctx), s, liveFeed{env}, paper.Options{ID: p.ID, Capital: capital, Costs: strategy.DefaultCosts,
		DataDir: paperDir(env), Markets: sessionRules(ctx, env, s)})
	if err != nil {
		return nil, err
	}
	sessions.Lock()
	sessions.paper[sess.Snapshot().ID] = sess
	sessions.Unlock()
	return sess, nil
}

func runPaper(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*PaperInput)
	switch strings.ToLower(p.Action) {
	case "start":
		sess, err := startPaper(ctx, env, p)
		if err != nil {
			return nil, err
		}
		return paperResult(env, sess.Snapshot()), nil
	case "status", "":
		snap, err := findPaper(env, p.ID)
		if err != nil {
			return nil, err
		}
		return paperResult(env, snap), nil
	case "stop":
		if p.ID == "" {
			return nil, Errorf(CodeUsage, "stop needs --id")
		}
		sessions.Lock()
		sess, ok := sessions.paper[p.ID]
		delete(sessions.paper, p.ID)
		sessions.Unlock()
		if !ok {
			return nil, &Error{Code: CodeNotFound, Message: "paper session " + p.ID + " is not running in this process", Hint: "sessions started by `hlp paper start` stop when that command exits"}
		}
		snap := sess.Snapshot()
		if err := sess.Stop(); err != nil {
			return nil, err
		}
		snap.Running = false
		return paperResult(env, snap), nil
	case "list":
		snaps, err := listPaper(env)
		if err != nil {
			return nil, err
		}
		rows := make([][2]string, 0, len(snaps))
		for _, s := range snaps {
			state := "stopped"
			if s.Running {
				state = "running"
			}
			rows = append(rows, [2]string{s.ID, fmt.Sprintf("%s · equity $%.2f · %s", s.Strategy, s.Equity.Last(), state)})
		}
		return &Result{Card: CardKV, Title: "Paper sessions", Data: rows, Meta: env.meta("paper")}, nil
	}
	return nil, Errorf(CodeUsage, "action must be start, status, stop or list, got %q", p.Action)
}

// listPaper merges persisted sessions with live in-process ones.
func listPaper(env *Env) ([]paper.Snapshot, error) {
	snaps, err := paper.List(paperDir(env))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	sessions.Lock()
	for id, s := range sessions.paper {
		live := s.Snapshot()
		found := false
		for i := range snaps {
			if snaps[i].ID == id {
				snaps[i], found = live, true
			}
		}
		if !found {
			snaps = append(snaps, live)
		}
	}
	sessions.Unlock()
	sort.SliceStable(snaps, func(i, j int) bool { return snaps[i].Started > snaps[j].Started })
	return snaps, nil
}

func findPaper(env *Env, id string) (paper.Snapshot, error) {
	if id != "" {
		sessions.Lock()
		s, ok := sessions.paper[id]
		sessions.Unlock()
		if ok {
			return s.Snapshot(), nil
		}
	}
	snaps, err := listPaper(env)
	if err != nil {
		return paper.Snapshot{}, err
	}
	for _, s := range snaps {
		if id == "" || s.ID == id {
			return s, nil
		}
	}
	if id == "" {
		return paper.Snapshot{}, &Error{Code: CodeNotFound, Message: "no paper sessions yet", Hint: "start one: hlp paper start examples/pine/ema-cross.pine --coin BTC"}
	}
	return paper.Snapshot{}, &Error{Code: CodeNotFound, Message: "no paper session " + id, Hint: "list them with `hlp paper list`"}
}

func streamPaper(ctx context.Context, env *Env, in any, emit func(*Result)) error {
	p := in.(*PaperInput)
	var sess paper.Session
	switch strings.ToLower(p.Action) {
	case "start":
		s, err := startPaper(ctx, env, p)
		if err != nil {
			return err
		}
		sess = s
		// a streamed start owns the session: stop (and persist) when the stream ends
		defer func() {
			sessions.Lock()
			delete(sessions.paper, s.Snapshot().ID)
			sessions.Unlock()
			_ = s.Stop()
		}()
	case "status", "":
		sessions.Lock()
		sess = sessions.paper[p.ID]
		sessions.Unlock()
	}
	if sess == nil {
		res, err := runPaper(ctx, env, in)
		if err != nil {
			return err
		}
		emit(res)
		return nil
	}
	emit(paperResult(env, sess.Snapshot()))
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case snap, ok := <-sess.Updates():
			if !ok {
				return nil
			}
			emit(paperResult(env, snap))
		}
	}
}

// ---- arena ----

func entrantSpec(e string) (file, coin string) {
	if i := strings.LastIndex(e, "@"); i > 0 && strings.HasSuffix(strings.ToLower(e[:i]), ".pine") {
		return e[:i], e[i+1:]
	}
	return e, ""
}

// arenaFile loads an arena YAML (examples/arena/*.yaml): entrants with
// paths relative to the file, capital, and optional real vaults.
func arenaFile(ctx context.Context, env *Env, file string, eng engineSel) (loader.ArenaSpec, []*loader.Loaded, error) {
	file = normalizeExampleRef(file)
	if _, err := readStrategyFile(ctx, env, file); err != nil {
		return loader.ArenaSpec{}, nil, err
	}
	spec, ls, err := loader.LoadArena(ctx, file, loaderOptions(ctx, env, "", "", eng))
	if err != nil {
		var ce *Error
		if errors.As(err, &ce) {
			return spec, nil, ce
		}
		if msg := err.Error(); agentCaller(ctx) && (strings.Contains(msg, "yaml:") || strings.Contains(msg, "json:")) {
			return spec, nil, parseRefusal(ctx, file, err) // decode errors echo file content
		}
		return spec, nil, &Error{Code: CodeUsage, Message: strings.TrimPrefix(err.Error(), "loader: "), Hint: "see example:arena/ai-vs-thesis"}
	}
	return spec, ls, nil
}

// arenaRun is a started arena with what its leaderboard needs.
type arenaRun struct {
	a      paper.Arena
	kinds  map[string]string
	strats map[string]strategy.Strategy // by display name
	run    *llmRun
	bench  *arenaBench
	warn   string // the --llm estimate line
}

// data builds the leaderboard payload for snaps.
func (r *arenaRun) data(ctx context.Context, snaps []paper.Snapshot) *ArenaData {
	d := ArenaFromSnapshots(snaps, r.kinds)
	d.Bench = r.bench.info(ctx)
	d.LLM = r.run.stats()
	for i := range d.Entrants {
		e := &d.Entrants[i]
		e.VsBench = vsBench(e.Metrics.TotalReturn, d.Bench)
		s := r.strats[e.Name]
		if m, ok := s.(strategy.ModelIDer); ok {
			e.Model = m.ModelID()
		}
		if dc, ok := s.(strategy.Decider); ok {
			e.Decision = dc.LastDecision()
		}
	}
	return d
}

func (r *arenaRun) result(ctx context.Context, env *Env, snaps []paper.Snapshot) *Result {
	meta := env.meta("paper")
	if r.warn != "" {
		meta.Warnings = append(meta.Warnings, r.warn)
	}
	return &Result{Card: CardArena, Title: "Arena", Data: r.data(ctx, snaps), Meta: meta}
}

func startArena(ctx context.Context, env *Env, p *ArenaInput) (*arenaRun, error) {
	eng, _, err := resolveEngine(ctx, env, p.EngineOpt, purposeArena)
	if err != nil {
		return nil, err
	}
	run := newLLMRun(env, p.LLMGuard, purposeArena)
	ctx = withLLMRun(ctx, run)
	var strats []strategy.Strategy
	kinds := map[string]string{}
	capital, bench := p.Capital, p.Bench
	if p.File != "" {
		spec, ls, err := arenaFile(ctx, env, p.File, eng)
		if err != nil {
			return nil, err
		}
		for _, l := range ls {
			strats = append(strats, l.Strategy)
			kinds[l.Strategy.Spec().Name] = l.Strategy.Spec().Kind
		}
		if len(strats) < 2 {
			return nil, &Error{Code: CodeUsage, Message: p.File + " needs at least 2 strategy entrants"}
		}
		if capital == 0 {
			capital = spec.Capital
		}
		if bench == "" {
			bench = spec.Bench
		}
	} else {
		if len(p.Entrants) < 2 {
			return nil, &Error{Code: CodeUsage, Message: "an arena needs at least 2 entrants", Hint: "e.g. --entrants examples/pine/ema-cross.pine@BTC,examples/thesis/long-spy-short-memes.yaml,llm:qwen3:8b"}
		}
		for _, e := range p.Entrants {
			file, coin := entrantSpec(e)
			if coin == "" && strings.EqualFold(filepath.Ext(file), ".pine") {
				coin = "BTC"
			}
			eo := p.EngineOpt
			eo.Engine = eng.Kind // resolved once for the whole arena
			ls, err := loadStrategy(ctx, env, file, "", coin, "", eo, purposeArena)
			if err != nil {
				ae := AsError(err)
				ae.Message = e + ": " + ae.Message
				return nil, ae
			}
			s := ls.S
			strats = append(strats, s)
			kinds[s.Spec().Name] = s.Spec().Kind
		}
		if capital == 0 {
			capital = 10_000
		}
	}
	warn, err := guardLLM(ctx, env, p.LLMGuard, strats, 0)
	if err != nil {
		return nil, err
	}
	a, err := paper.StartArena(sessionContext(ctx), strats, liveFeed{env}, paper.Options{Capital: capital, Costs: strategy.DefaultCosts,
		DataDir: paperDir(env), Markets: arenaRules(ctx, env, strats)})
	if err != nil {
		return nil, err
	}
	byName := map[string]strategy.Strategy{}
	for _, s := range strats {
		byName[s.Spec().Name] = s
	}
	return &arenaRun{a: a, kinds: kinds, strats: byName, run: run, warn: warn,
		bench: newArenaBench(ctx, env, bench, strats)}, nil
}

// arenaRules merges every entrant's exchange rules.
func arenaRules(ctx context.Context, env *Env, strats []strategy.Strategy) map[string]sim.Rules {
	var coins []string
	for _, s := range strats {
		coins = append(coins, s.Spec().Universe...)
	}
	return marketRules(ctx, env, coins)
}

// ArenaFromSnapshots builds the leaderboard payload (sorted by return).
func ArenaFromSnapshots(snaps []paper.Snapshot, kinds map[string]string) *ArenaData {
	out := &ArenaData{}
	for _, s := range snaps {
		e := ArenaEntrant{Name: s.Strategy, Kind: kinds[s.Strategy], Equity: s.Equity}
		if e.Name == "" {
			e.Name = s.ID
		}
		// Metrics start from the starting capital, so an entrant that moved
		// before its second equity point still shows its return.
		curve := s.Equity
		if s.Capital > 0 && len(curve) > 0 && curve[0].V != s.Capital {
			t0 := s.Started
			if t0 <= 0 || t0 >= curve[0].T {
				t0 = curve[0].T - 1
			}
			curve = append(domain.Series{{T: t0, V: s.Capital}}, curve...)
		}
		e.Metrics = metrics.Compute(metrics.Normalize(curve), nil)
		e.LastNote = s.Note
		if n := len(s.Logs); e.LastNote == "" && n > 0 {
			e.LastNote = s.Logs[n-1]
		}
		e.Error = s.Err
		e.Interval = s.Interval
		e.Waiting = s.Err == "" && len(s.Fills) == 0 && len(s.Equity) <= 1
		out.Entrants = append(out.Entrants, e)
	}
	sort.SliceStable(out.Entrants, func(i, j int) bool {
		return out.Entrants[i].Metrics.TotalReturn > out.Entrants[j].Metrics.TotalReturn
	})
	return out
}

func vaultArena(ctx context.Context, env *Env, p *ArenaInput) (*Result, error) {
	if len(p.Vaults) < 2 || len(p.Vaults) > 8 {
		return nil, Errorf(CodeUsage, "a vault arena takes 2-8 vaults, got %d", len(p.Vaults))
	}
	n := len(p.Vaults)
	ents := make([]ArenaEntrant, n)
	errs := make([]error, n)
	ix := newVaultIndex(env)
	parallel(n, func(i int) {
		addr, row, err := ix.resolve(ctx, p.Vaults[i])
		if err != nil {
			errs[i] = err
			return
		}
		d, _, err := buildVault(ctx, env, addr, row, vaultOpts{window: "1m", light: true})
		if err != nil {
			errs[i] = err
			return
		}
		curve := metrics.Normalize(curveOf(d))
		name := d.Summary.Name
		if name == "" {
			name = shortAddr(addr)
		}
		ents[i] = ArenaEntrant{Name: name, Kind: "vault", Equity: curve, Metrics: metrics.Compute(curve, nil)}
	})
	for i, err := range errs {
		if err != nil {
			e := AsError(err)
			e.Message = p.Vaults[i] + ": " + e.Message
			return nil, e
		}
	}
	sort.SliceStable(ents, func(i, j int) bool { return ents[i].Metrics.TotalReturn > ents[j].Metrics.TotalReturn })
	meta := env.meta("hyperliquid")
	bench, err := vaultArenaBench(ctx, env, p.Bench, ents)
	if err != nil {
		meta.Warnings = append(meta.Warnings, "benchmark: "+AsError(err).Message)
	}
	for i := range ents {
		ents[i].VsBench = vsBench(ents[i].Metrics.TotalReturn, bench)
	}
	return &Result{Card: CardArena, Title: "Vault arena (30d)", Data: &ArenaData{Entrants: ents, Bench: bench}, Meta: meta}, nil
}

// vaultArenaBench is buy-and-hold of bench (default BTC: vaults trade no
// declared coins) over the vault curves' common window.
func vaultArenaBench(ctx context.Context, env *Env, name string, ents []ArenaEntrant) (*BenchInfo, error) {
	if isEW(name) {
		name = "BTC"
	}
	var from, to int64
	for _, e := range ents {
		if len(e.Equity) < 2 {
			continue
		}
		from = max(from, e.Equity[0].T)
		if t := e.Equity[len(e.Equity)-1].T; to == 0 || t < to {
			to = t
		}
	}
	if from == 0 || to <= from {
		return nil, nil
	}
	curve, err := benchmarkCurve(ctx, env, name, time.UnixMilli(from), time.UnixMilli(to))
	if err != nil {
		return nil, err
	}
	var first, last domain.Point
	for _, pt := range curve {
		if pt.T < from || pt.V <= 0 {
			continue
		}
		if first.T == 0 {
			first = pt
		}
		if pt.T <= to+24*int64(time.Hour/time.Millisecond) {
			last = pt
		}
	}
	if first.V <= 0 || last.T <= first.T {
		return nil, nil
	}
	return &BenchInfo{Name: strings.ToUpper(name), Return: last.V/first.V - 1, From: first.T, To: last.T}, nil
}

// refuseVaultEngine rejects --engine pineforge on a vault-bound arena
// (other people's capital is Commercial Use under PineForge's licence).
func refuseVaultEngine(env *Env, opt EngineOpt) error {
	name, err := engineName(env, EngineOpt{Engine: opt.Engine})
	if err != nil {
		return err
	}
	if name == EnginePineForge && opt.Engine != "" {
		return &Error{Code: CodeUsage, Message: "PineForge cannot be used for vault-bound runs (other people's capital is Commercial Use under its licence)",
			Hint: "drop --engine pineforge"}
	}
	return nil
}

// arenaVaultsFromFile fills p.Vaults from an arena file that lists only
// real vaults, so it runs as a vault arena.
func arenaVaultsFromFile(ctx context.Context, env *Env, p *ArenaInput) error {
	if p.File == "" || len(p.Vaults) > 0 {
		return nil
	}
	p.File = normalizeExampleRef(p.File)
	src, err := readStrategyFile(ctx, env, p.File)
	if err != nil {
		return err
	}
	spec, err := loader.ParseArena([]byte(src))
	if err != nil {
		return parseRefusal(ctx, p.File, err)
	}
	if len(spec.Entrants) == 0 {
		p.Vaults, p.File = spec.Vaults, ""
	}
	return nil
}

func runArena(ctx context.Context, env *Env, in any) (*Result, error) {
	p := in.(*ArenaInput)
	if err := arenaVaultsFromFile(ctx, env, p); err != nil {
		return nil, err
	}
	if len(p.Vaults) > 0 && len(p.Entrants) == 0 && p.File == "" {
		if err := refuseVaultEngine(env, p.EngineOpt); err != nil {
			return nil, err
		}
		return vaultArena(ctx, env, p)
	}
	ar, err := startArena(ctx, env, p)
	if err != nil {
		return nil, err
	}
	sessions.Lock()
	sessions.arenas[fmt.Sprintf("arena-%d", env.now().UnixNano())] = ar.a
	sessions.Unlock()
	return ar.result(ctx, env, ar.a.Leaderboard()), nil
}

func streamArena(ctx context.Context, env *Env, in any, emit func(*Result)) error {
	p := in.(*ArenaInput)
	if err := arenaVaultsFromFile(ctx, env, p); err != nil {
		return err
	}
	if len(p.Vaults) > 0 && len(p.Entrants) == 0 && p.File == "" {
		if err := refuseVaultEngine(env, p.EngineOpt); err != nil {
			return err
		}
		res, err := vaultArena(ctx, env, p)
		if err != nil {
			return err
		}
		emit(res)
		return nil
	}
	ar, err := startArena(ctx, env, p)
	if err != nil {
		return err
	}
	defer ar.a.Stop()
	emit(ar.result(ctx, env, ar.a.Leaderboard()))
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case snaps, ok := <-ar.a.Updates():
			if !ok {
				return nil
			}
			emit(ar.result(ctx, env, snaps))
		}
	}
}
