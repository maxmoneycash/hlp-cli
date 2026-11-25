package pine

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/strategy"
)

// Error is a positioned diagnostic.
type Error struct {
	Line, Col int
	Msg       string
}

func (e *Error) Error() string { return fmt.Sprintf("%d:%d: %s", e.Line, e.Col, e.Msg) }

// Script is a parsed program.
type Script struct {
	Version int
	Title   string
	IsStrat bool
	Inputs  []Input

	// Overlay is strategy(overlay=...): plots belong on the price chart.
	Overlay bool
	// InitialCapital from strategy(initial_capital=...), default 10000.
	InitialCapital float64
	// QtyType and QtyValue are the default order size
	// (fixed | cash | percent_of_equity).
	QtyType  string
	QtyValue float64
	// Pyramiding is the max entries in one direction (0 = 1).
	Pyramiding int
	// Plots lists plot() series names in declaration order.
	Plots []string

	prog *program
}

// Input is a declared input.* with its default.
type Input struct {
	Name    string
	Kind    string // int | float | bool | string | source | color | time | timeframe
	Default any

	Options []any    `json:",omitempty"`
	Min     *float64 `json:",omitempty"`
	Max     *float64 `json:",omitempty"`
}

// Parse parses src. All syntax and unsupported-feature errors are returned
// together (errors.Join of *Error) so `hlp pine check` can list them all.
func Parse(src string) (*Script, error) {
	lines, version, errs := lex(src)
	if version != 0 && version < 5 {
		errs = append(errs, &Error{Line: 1, Col: 1, Msg: fmt.Sprintf("Pine v%d is not supported; hlp runs Pine v5/v6 (TradingView can convert scripts to v5)", version)})
	}
	if version > 6 {
		errs = append(errs, &Error{Line: 1, Col: 1, Msg: fmt.Sprintf("Pine v%d is newer than hlp supports (v5/v6)", version)})
	}
	if version == 0 {
		version = 5
	}
	p := &parser{lines: lines}
	stmts := p.parseProgram()
	errs = append(errs, p.errs...)
	s := &Script{Version: version}
	prog := &program{stmts: stmts, inputs: map[int]*inputDecl{}, plotTitle: map[int]string{}}
	c := &checker{prog: prog, script: s}
	c.check(stmts)
	errs = append(errs, c.errs...)
	if !c.declared && len(lines) > 0 {
		errs = append(errs, &Error{Line: 1, Col: 1, Msg: "missing strategy() or indicator() declaration"})
	}
	if len(lines) == 0 {
		errs = append(errs, &Error{Line: 1, Col: 1, Msg: "empty script"})
	}
	if len(errs) > 0 {
		sortErrors(errs)
		return nil, errors.Join(errs...)
	}
	d := prog.decl
	s.Title, s.IsStrat, s.Overlay = d.title, d.isStrat, d.overlay
	s.InitialCapital, s.QtyType, s.QtyValue, s.Pyramiding = d.capital, d.qtyType, d.qtyValue, d.pyramiding
	s.Plots = prog.plots
	s.prog = prog
	return s, nil
}

func sortErrors(errs []error) {
	sort.SliceStable(errs, func(i, j int) bool {
		a, aok := errs[i].(*Error)
		b, bok := errs[j].(*Error)
		if !aok || !bok {
			return aok
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Col < b.Col
	})
}

// Errors flattens an error returned by Parse into its positioned
// diagnostics (nil when err carries none).
func Errors(err error) []*Error {
	var out []*Error
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if pe, ok := e.(*Error); ok {
			out = append(out, pe)
			return
		}
		if j, ok := e.(interface{ Unwrap() []error }); ok {
			for _, x := range j.Unwrap() {
				walk(x)
			}
			return
		}
		walk(errors.Unwrap(e))
	}
	walk(err)
	return out
}

// Options are market details Compile cannot infer from the script.
type Options struct {
	// SzDecimals is the market's size precision (meta szDecimals): it caps
	// syminfo.mintick at 6 - SzDecimals decimals (spec A4).
	SzDecimals int
}

// Compile binds inputs (overrides by name) and returns a runnable strategy
// for one coin and interval. Input names match the input title or the
// variable it is assigned to, case-insensitively.
func Compile(s *Script, coin string, iv domain.Interval, inputs map[string]any) (strategy.Strategy, error) {
	return CompileWith(s, coin, iv, inputs, Options{})
}

// CompileWith is Compile with market details (szDecimals).
func CompileWith(s *Script, coin string, iv domain.Interval, inputs map[string]any, o Options) (strategy.Strategy, error) {
	if s == nil || s.prog == nil {
		return nil, errors.New("pine: compile needs a parsed script")
	}
	if coin == "" {
		return nil, errors.New("pine: compile needs a coin")
	}
	if !iv.Valid() {
		return nil, fmt.Errorf("pine: invalid interval %q", iv)
	}
	bound := map[int]value{}
	src := map[int]string{}
	vals := make([]any, len(s.Inputs))
	for i, in := range s.Inputs {
		vals[i] = in.Default
	}
	for k, raw := range inputs {
		idx := -1
		for i, in := range s.Inputs {
			if strings.EqualFold(in.Name, k) {
				idx = i
				break
			}
		}
		if idx < 0 {
			for _, d := range s.prog.inputs {
				if strings.EqualFold(d.varName, k) {
					idx = d.index
					break
				}
			}
		}
		if idx < 0 {
			var names []string
			for _, in := range s.Inputs {
				names = append(names, strconv.Quote(in.Name))
			}
			return nil, fmt.Errorf("pine: unknown input %q (inputs: %s)", k, strings.Join(names, ", "))
		}
		v, err := convertInput(s.Inputs[idx], raw)
		if err != nil {
			return nil, err
		}
		vals[idx] = v
	}
	for id, d := range s.prog.inputs {
		in := s.Inputs[d.index]
		switch v := vals[d.index].(type) {
		case float64:
			bound[id] = num(v)
		case int:
			bound[id] = num(float64(v))
		case bool:
			bound[id] = boolean(v)
		case string:
			if in.Kind == "source" {
				bound[id] = naV
				src[id] = v
			} else if in.Kind == "color" {
				continue // keep the script's color expression
			} else {
				bound[id] = str(v)
			}
		}
	}
	name := s.Title
	if name == "" {
		name = "Pine strategy"
	}
	return &pineStrategy{script: s, coin: coin, iv: iv, inputs: bound, src: src, name: name, szDec: o.SzDecimals}, nil
}

func convertInput(in Input, raw any) (any, error) {
	bad := func() error { return fmt.Errorf("pine: input %q expects %s, got %v", in.Name, in.Kind, raw) }
	var out any
	switch in.Kind {
	case "int", "float":
		var f float64
		switch v := raw.(type) {
		case int:
			f = float64(v)
		case int64:
			f = float64(v)
		case float64:
			f = v
		case string:
			x, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return nil, bad()
			}
			f = x
		default:
			return nil, bad()
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, bad()
		}
		if in.Min != nil && f < *in.Min {
			return nil, fmt.Errorf("pine: input %q must be >= %v", in.Name, *in.Min)
		}
		if in.Max != nil && f > *in.Max {
			return nil, fmt.Errorf("pine: input %q must be <= %v", in.Name, *in.Max)
		}
		if in.Kind == "int" {
			out = int(f)
		} else {
			out = f
		}
	case "bool":
		switch v := raw.(type) {
		case bool:
			out = v
		case string:
			b, err := strconv.ParseBool(strings.TrimSpace(v))
			if err != nil {
				return nil, bad()
			}
			out = b
		default:
			return nil, bad()
		}
	case "time":
		f, ok := parseTimeInput(raw)
		if !ok {
			return nil, fmt.Errorf("pine: input %q expects a time (epoch ms or \"2024-01-01T00:00:00\"), got %v", in.Name, raw)
		}
		out = f
	case "timeframe":
		v, ok := raw.(string)
		if !ok {
			return nil, bad()
		}
		if _, err := parseTF(v); err != nil {
			return nil, fmt.Errorf("pine: input %q: %v", in.Name, err)
		}
		out = strings.TrimSpace(v)
	case "source":
		v, ok := raw.(string)
		if !ok || !sourceNames[strings.ToLower(v)] {
			return nil, fmt.Errorf("pine: input %q expects a source (open, high, low, close, hl2, hlc3, ohlc4, hlcc4, volume), got %v", in.Name, raw)
		}
		out = strings.ToLower(v)
	default:
		out = fmt.Sprint(raw)
	}
	if len(in.Options) > 0 {
		ok := false
		for _, o := range in.Options {
			if fmt.Sprint(o) == fmt.Sprint(out) {
				ok = true
			}
		}
		if !ok {
			return nil, fmt.Errorf("pine: input %q must be one of %v", in.Name, in.Options)
		}
	}
	return out, nil
}

// sourceValue reads a named price source back bars ago.
func sourceValue(rt *runtime, name string, back int) value {
	if bv, ok := builtinVars[name]; ok {
		return bv.get(rt, back)
	}
	return naV
}

// pineStrategy adapts a compiled script to strategy.Strategy.
type pineStrategy struct {
	script *Script
	coin   string
	iv     domain.Interval
	inputs map[int]value
	src    map[int]string
	name   string
	szDec  int
	rt     *runtime
	lastT  int64
	seen   bool
}

// leverager is implemented by the sim engine: the script's own leverage
// (100 / margin_*) caps positions below the market's max leverage.
type leverager interface {
	SetLeverage(coin string, long, short float64)
}

// Leverage returns the script's leverage for longs and shorts
// (100 / margin_long, 100 / margin_short); 1x when a margin is not set,
// TradingView's 100% default. Version 5 scripts get the same 1x: in
// TradingView v5 a margin of 0 means unlimited, hlp does not model that.
func (s *Script) Leverage() (long, short float64) {
	lev := func(m float64) float64 {
		if m <= 0 {
			return 1
		}
		return 100 / m
	}
	if s == nil || s.prog == nil {
		return 1, 1
	}
	return lev(s.prog.decl.marginLong), lev(s.prog.decl.marginShort)
}

func (ps *pineStrategy) Spec() strategy.Spec {
	return strategy.Spec{Name: ps.name, Kind: "pine", Universe: []string{ps.coin}, Interval: ps.iv}
}

func (ps *pineStrategy) Init(ctx strategy.Context) error {
	ps.rt = newRuntime(ps.script.prog, ps.coin, ps.iv, ps.inputs, ps.src)
	ps.rt.szDec = ps.szDec
	ps.seen = false
	if l, ok := ctx.(leverager); ok && ps.script.IsStrat {
		long, short := ps.script.Leverage()
		l.SetLeverage(ps.coin, long, short)
	}
	return nil
}

// OnBar runs the script on every bar it has not seen yet. Bars before the
// newest one replay silently (indicators warm up, no orders, no plots), so
// a paper session started with history behaves like a chart that has
// already been loaded.
func (ps *pineStrategy) OnBar(ctx strategy.Context) error {
	if ps.rt == nil {
		_ = ps.Init(ctx)
	}
	ps.rt.ctx = ctx
	bars := ctx.Bars(ps.coin)
	i := 0
	if ps.seen {
		i = sort.Search(len(bars), func(k int) bool { return bars[k].T > ps.lastT })
	}
	for ; i < len(bars); i++ {
		live := i == len(bars)-1
		if err := ps.rt.step(bars[i], live); err != nil {
			return err
		}
		ps.lastT, ps.seen = bars[i].T, true
	}
	return nil
}
