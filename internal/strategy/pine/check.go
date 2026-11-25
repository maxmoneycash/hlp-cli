package pine

import (
	"fmt"
	"math"
	"strings"
)

// inputDecl is one input.* call site.
type inputDecl struct {
	index   int    // into Script.Inputs
	varName string // variable it was assigned to, if any
}

// sourceNames are the values input.source accepts.
var sourceNames = map[string]bool{"open": true, "high": true, "low": true, "close": true, "volume": true,
	"hl2": true, "hlc3": true, "ohlc4": true, "hlcc4": true}

// constNames are builtin variables that are compile-time constants.
var constNames = map[string]bool{}

// initConstNames runs at the end of the builtins init.
func initConstNames() {
	for n := range builtinVars {
		if strings.HasPrefix(n, "color.") || strings.HasPrefix(n, "shape.") || strings.HasPrefix(n, "location.") {
			constNames[n] = true
		}
	}
	for n := range builtinVars {
		if strings.HasPrefix(n, "barmerge.") || strings.HasPrefix(n, "plot.") || strings.HasPrefix(n, "display.") {
			constNames[n] = true
		}
	}
	for _, n := range []string{"long", "short", "fixed", "cash", "percent_of_equity", "commission.percent",
		"commission.cash_per_contract", "commission.cash_per_order", "oca.none", "oca.cancel", "oca.reduce",
		"direction.all", "direction.long", "direction.short"} {
		constNames["strategy."+n] = true
	}
}

type checker struct {
	errs     []error
	prog     *program
	script   *Script
	funcs    map[string]*funcDef
	scopes   []map[string]int
	globals  map[string]int // top-level names (for function bodies)
	lay      *layout
	inFunc   *funcDef
	loops    int
	declName string
	plotN    int
	declared bool
	topLevel bool

	// simple marks top-level slots whose value cannot change from bar to
	// bar (constants, inputs and arithmetic on them); mutated marks slots
	// reassigned with := or +=. request.security may only read simple,
	// never-mutated variables.
	simple  map[int]bool
	mutated map[int]bool
	secs    []*callExpr
	inSec   bool
}

func (c *checker) errf(p Pos, format string, args ...any) {
	c.errs = append(c.errs, &Error{Line: p.Line, Col: p.Col, Msg: fmt.Sprintf(format, args...)})
}

func (c *checker) push() { c.scopes = append(c.scopes, map[string]int{}) }
func (c *checker) pop()  { c.scopes = c.scopes[:len(c.scopes)-1] }

func (c *checker) lookup(name string) (int, bool) {
	for i := len(c.scopes) - 1; i >= 0; i-- {
		if s, ok := c.scopes[i][name]; ok {
			return s, true
		}
	}
	return 0, false
}

func (c *checker) declare(p Pos, name string, isVar bool) int {
	if _, ok := builtinVars[name]; ok || name == "na" {
		c.errf(p, "%s is a built-in name and cannot be redeclared", name)
	}
	top := c.scopes[len(c.scopes)-1]
	if _, ok := top[name]; ok {
		c.errf(p, "%s is already declared in this scope (use := to reassign)", name)
	}
	slot := c.lay.add(isVar)
	top[name] = slot
	if c.inFunc == nil && len(c.scopes) == 1 {
		c.globals[name] = slot
	}
	return slot
}

// check runs name resolution, argument binding and subset checks.
func (c *checker) check(stmts []stmt) {
	c.funcs = map[string]*funcDef{}
	c.globals = map[string]int{}
	c.simple = map[int]bool{}
	c.mutated = map[int]bool{}
	for _, s := range stmts {
		if fd, ok := s.(*funcDef); ok {
			if _, dup := c.funcs[fd.Name]; dup {
				c.errf(fd.P, "function %s is declared twice", fd.Name)
			}
			if _, bi := builtins[fd.Name]; bi {
				c.errf(fd.P, "function %s shadows a built-in", fd.Name)
			}
			c.funcs[fd.Name] = fd
		}
	}
	c.prog.funcs = c.funcs
	c.prog.flay = map[*funcDef]*layout{}
	c.lay = &c.prog.layout
	c.push()
	for _, s := range stmts {
		if fd, ok := s.(*funcDef); ok {
			c.checkFunc(fd)
			continue
		}
		c.topLevel = true
		c.stmt(s)
	}
	c.pop()
	for _, e := range c.secs {
		for _, sl := range e.sec.slots {
			if c.mutated[sl.slot] {
				c.errf(sl.p, "request.security reads %s, which the script reassigns with := ; only constants and inputs can be passed into request.security", sl.name)
			}
		}
	}
}

func (c *checker) checkFunc(fd *funcDef) {
	saveLay, saveScopes := c.lay, c.scopes
	l := &layout{}
	c.prog.flay[fd] = l
	c.lay = l
	c.inFunc = fd
	c.scopes = nil
	c.push()
	fd.pslots = nil
	for i, p := range fd.Params {
		if fd.Defaults != nil && fd.Defaults[i] != nil {
			if _, ok := constValue(fd.Defaults[i]); !ok {
				c.errf(fd.Defaults[i].position(), "default value of parameter %s must be a constant", p)
			}
		}
		fd.pslots = append(fd.pslots, c.declare(fd.P, p, false))
	}
	for _, s := range fd.Body {
		c.topLevel = false
		c.stmt(s)
	}
	c.pop()
	fd.nslots = l.nslots
	c.lay, c.scopes, c.inFunc = saveLay, saveScopes, nil
}

func (c *checker) block(ss []stmt) {
	c.push()
	for _, s := range ss {
		c.topLevel = false
		c.stmt(s)
	}
	c.pop()
}

func (c *checker) stmt(s stmt) {
	switch s := s.(type) {
	case *exprStmt:
		c.expr(s.X)
	case *declStmt:
		c.declName = s.Name
		c.expr(s.X)
		c.declName = ""
		s.slot = c.declare(s.P, s.Name, s.Var)
		if c.inFunc == nil && len(c.scopes) == 1 && !s.Var && c.isSimple(s.X) {
			c.simple[s.slot] = true
		}
	case *tupleDecl:
		c.expr(s.X)
		s.slots = nil
		for i, n := range s.Names {
			if n == "_" {
				s.slots = append(s.slots, c.lay.add(false))
				continue
			}
			s.slots = append(s.slots, c.declare(s.NameP[i], n, false))
		}
	case *assignStmt:
		c.expr(s.X)
		slot, ok := c.lookup(s.Name)
		if !ok {
			if _, bi := builtinVars[s.Name]; bi {
				c.errf(s.P, "cannot assign to built-in %s", s.Name)
			} else if _, g := c.globals[s.Name]; g && c.inFunc != nil {
				c.errf(s.P, "functions cannot modify global variable %s", s.Name)
			} else {
				c.errf(s.P, "%s is not declared; declare it first with =", s.Name)
			}
			return
		}
		s.slot = slot
		c.mutated[slot] = true
	case *switchStmt:
		if s.Subject != nil {
			c.expr(s.Subject)
		}
		for _, sc := range s.Cases {
			if sc.Cond != nil {
				c.expr(sc.Cond)
			}
			c.block(sc.Body)
		}
	case *ifStmt:
		c.expr(s.Cond)
		c.block(s.Then)
		if s.Else != nil {
			c.block(s.Else)
		}
	case *forStmt:
		c.expr(s.From)
		c.expr(s.To)
		if s.By != nil {
			c.expr(s.By)
		}
		c.push()
		s.slot = c.declare(s.P, s.Var, false)
		c.loops++
		for _, b := range s.Body {
			c.topLevel = false
			c.stmt(b)
		}
		c.loops--
		c.pop()
	case *whileStmt:
		c.expr(s.Cond)
		c.loops++
		c.block(s.Body)
		c.loops--
	case *breakStmt:
		if c.loops == 0 {
			c.errf(s.P, "break outside a loop")
		}
	case *continueStmt:
		if c.loops == 0 {
			c.errf(s.P, "continue outside a loop")
		}
	case *funcDef:
		c.errf(s.P, "functions must be declared at the top level")
	}
}

func (c *checker) expr(e expr) {
	switch e := e.(type) {
	case *ident:
		c.ident(e)
	case *indexExpr:
		c.expr(e.X)
		c.expr(e.N)
	case *unaryExpr:
		c.expr(e.X)
	case *binaryExpr:
		c.expr(e.X)
		c.expr(e.Y)
		if e.Op == "/" && c.script.Version == 5 && constInt(e.X) && constInt(e.Y) {
			e.intDiv = true
		}
	case *ternaryExpr:
		c.expr(e.Cond)
		c.expr(e.A)
		c.expr(e.B)
	case *tupleExpr:
		for _, x := range e.Elems {
			c.expr(x)
		}
	case *callExpr:
		c.call(e)
	case *stmtExpr:
		top := c.topLevel
		c.topLevel = false
		c.stmt(e.S)
		c.topLevel = top
	}
}

// isSimple reports an expression whose value is the same on every bar:
// literals, inputs, built-in constants and arithmetic on simple variables.
func (c *checker) isSimple(e expr) bool {
	switch e := e.(type) {
	case *numLit, *strLit, *boolLit, *naLit, *colorLit:
		return true
	case *ident:
		if constNames[e.Name] {
			return true
		}
		if e.ref.kind == refSlot {
			return c.simple[e.ref.slot]
		}
		return false
	case *unaryExpr:
		return c.isSimple(e.X)
	case *binaryExpr:
		return c.isSimple(e.X) && c.isSimple(e.Y)
	case *ternaryExpr:
		return c.isSimple(e.Cond) && c.isSimple(e.A) && c.isSimple(e.B)
	case *callExpr:
		if _, ok := c.prog.inputs[e.id]; ok {
			return true
		}
		if e.Fn == "timestamp" || strings.HasPrefix(e.Fn, "math.") {
			for _, a := range e.bound {
				if a != nil && !c.isSimple(a) {
					return false
				}
			}
			return e.bi != nil && !e.bi.stateful
		}
	}
	return false
}

func (c *checker) ident(e *ident) {
	if slot, ok := c.lookup(e.Name); ok {
		e.ref = ref{kind: refSlot, slot: slot}
		return
	}
	if c.inFunc != nil {
		if slot, ok := c.globals[e.Name]; ok {
			e.ref = ref{kind: refGlobal, slot: slot}
			c.inFunc.usesGlobals = true
			return
		}
	}
	if bv, ok := builtinVars[e.Name]; ok {
		e.ref = ref{kind: refBuiltin, bv: bv}
		if e.Name == "ta.vwap" {
			c.prog.usesVWAP = true
		}
		return
	}
	if why, ok := unsupportedReason(e.Name); ok {
		c.errf(e.P, "%s: %s", e.Name, why)
		return
	}
	if _, ok := builtins[e.Name]; ok {
		c.errf(e.P, "%s is a function; call it with ()", e.Name)
		return
	}
	if _, ok := c.funcs[e.Name]; ok {
		c.errf(e.P, "%s is a function; call it with ()", e.Name)
		return
	}
	c.errf(e.P, "undeclared identifier %s", e.Name)
}

func (c *checker) call(e *callExpr) {
	if e.Fn == "request.security" {
		if c.inSec {
			c.errf(e.P, "nested request.security calls are not supported")
			return
		}
		c.inSec = true
		defer func() { c.inSec = false }()
	}
	for _, a := range e.Args {
		c.expr(a)
	}
	for _, n := range e.Named {
		c.expr(n.X)
	}
	if fd, ok := c.funcs[e.Fn]; ok {
		if c.inFunc == fd {
			c.errf(e.P, "recursive call to %s is not allowed in Pine", e.Fn)
			return
		}
		if c.inSec && fd.usesGlobals {
			c.errf(e.P, "%s reads top-level variables, which request.security cannot see; pass them as arguments", e.Fn)
		}
		if len(e.Args) > len(fd.Params) {
			c.errf(e.P, "%s takes %d argument(s), got %d", e.Fn, len(fd.Params), len(e.Args))
			return
		}
		bound := make([]expr, len(fd.Params))
		copy(bound, e.Args)
		for _, n := range e.Named {
			i := indexOf(fd.Params, n.Name)
			if i < 0 {
				c.errf(n.P, "%s has no parameter %q", e.Fn, n.Name)
				continue
			}
			if bound[i] != nil {
				c.errf(n.P, "argument %q given twice", n.Name)
				continue
			}
			bound[i] = n.X
		}
		for i, b := range bound {
			if b != nil {
				continue
			}
			if fd.Defaults != nil && fd.Defaults[i] != nil {
				bound[i] = fd.Defaults[i]
				continue
			}
			c.errf(e.P, "%s: missing argument %q", e.Fn, fd.Params[i])
		}
		e.bound = bound
		e.fn = fd
		return
	}
	if why, ok := unsupportedReason(e.Fn); ok {
		c.errf(e.P, "%s: %s", e.Fn, why)
		return
	}
	bi, ok := builtins[e.Fn]
	if !ok {
		if e.Fn == "study" || e.Fn == "security" {
			c.errf(e.P, "%s is Pine v4 or older; hlp runs Pine v5/v6 (//@version=5)", e.Fn)
			return
		}
		if strings.HasPrefix(e.Fn, "ta.") || strings.HasPrefix(e.Fn, "math.") || strings.HasPrefix(e.Fn, "str.") || strings.HasPrefix(e.Fn, "strategy.") {
			c.errf(e.P, "%s is not supported yet in hlp's Pine subset", e.Fn)
			return
		}
		c.errf(e.P, "unknown function %s", e.Fn)
		return
	}
	e.bi = bi
	if bi.variadic {
		if len(e.Args) < bi.required {
			c.errf(e.P, "%s needs at least %d argument(s)", e.Fn, bi.required)
		}
		e.bound = e.Args
		return
	}
	if len(e.Args) > len(bi.params) {
		c.errf(e.Args[len(bi.params)].position(), "too many arguments to %s (max %d)", e.Fn, len(bi.params))
		return
	}
	bound := make([]expr, len(bi.params))
	copy(bound, e.Args)
	for _, n := range e.Named {
		i := indexOf(bi.params, n.Name)
		if i < 0 {
			c.errf(n.P, "%s has no argument %q", e.Fn, n.Name)
			continue
		}
		if bound[i] != nil {
			c.errf(n.P, "argument %q given twice", n.Name)
			continue
		}
		bound[i] = n.X
	}
	for i := 0; i < bi.required; i++ {
		if bound[i] == nil {
			c.errf(e.P, "%s: missing argument %q", e.Fn, bi.params[i])
		}
	}
	e.bound = bound
	switch {
	case e.Fn == "strategy.exit":
		off := bound[indexOf(bi.params, "trail_offset")]
		for _, n := range []string{"trail_price", "trail_points"} {
			if a := bound[indexOf(bi.params, n)]; a != nil && off == nil {
				c.errf(a.position(), "strategy.exit %s needs trail_offset (how far the stop trails behind the best price)", n)
			}
		}
		if off != nil && bound[indexOf(bi.params, "trail_price")] == nil && bound[indexOf(bi.params, "trail_points")] == nil {
			c.errf(off.position(), "strategy.exit trail_offset needs trail_price or trail_points (when the trailing stop activates)")
		}
	case e.Fn == "strategy" || e.Fn == "indicator":
		c.declaration(e)
	case e.Fn == "plot":
		c.plotN++
		title := fmt.Sprintf("Plot %d", c.plotN)
		if t, ok := constStr(bound[1]); ok && t != "" {
			title = t
		}
		c.prog.plotTitle[e.id] = title
		c.prog.plots = append(c.prog.plots, title)
	case strings.HasPrefix(e.Fn, "input"):
		c.input(e)
	case e.Fn == "request.security":
		c.security(e)
	case c.inSec && (strings.HasPrefix(e.Fn, "strategy.") || e.Fn == "plot" || e.Fn == "plotshape" || e.Fn == "plotchar"):
		c.errf(e.P, "%s cannot be called inside request.security", e.Fn)
	}
}

func indexOf(xs []string, x string) int {
	for i, s := range xs {
		if s == x {
			return i
		}
	}
	return -1
}

// declaration records strategy()/indicator() arguments.
func (c *checker) declaration(e *callExpr) {
	if c.declared {
		c.errf(e.P, "only one strategy() or indicator() declaration is allowed")
		return
	}
	if !c.topLevel || c.inFunc != nil {
		c.errf(e.P, "%s() must be a top-level statement", e.Fn)
	}
	c.declared = true
	d := &c.prog.decl
	d.isStrat = e.Fn == "strategy"
	d.title, _ = constStr(e.bound[0])
	d.qtyType, d.qtyValue, d.capital, d.pyramiding = "fixed", 1, 10000, 0
	at := func(n string) expr { return e.bound[indexOf(builtins[e.Fn].params, n)] }
	if v, ok := constBool(at("overlay")); ok {
		d.overlay = v
	}
	if !d.isStrat {
		return
	}
	if x := at("initial_capital"); x != nil {
		if v, ok := constNum(x); ok && v > 0 {
			d.capital = v
		} else {
			c.errf(x.position(), "initial_capital must be a positive number")
		}
	}
	if x := at("default_qty_type"); x != nil {
		v, ok := constStr(x)
		v = strings.TrimPrefix(v, "strategy.")
		if !ok || (v != "fixed" && v != "cash" && v != "percent_of_equity") {
			c.errf(x.position(), "default_qty_type must be strategy.fixed, strategy.cash or strategy.percent_of_equity")
		} else {
			d.qtyType = v
			if v == "percent_of_equity" {
				d.qtyValue = 100
			}
		}
	}
	if x := at("default_qty_value"); x != nil {
		if v, ok := constNum(x); ok && v > 0 {
			d.qtyValue = v
		} else {
			c.errf(x.position(), "default_qty_value must be a positive number")
		}
	}
	if x := at("pyramiding"); x != nil {
		if v, ok := constNum(x); ok && v >= 0 {
			d.pyramiding = int(v)
		} else {
			c.errf(x.position(), "pyramiding must be a number >= 0")
		}
	}
	for n, dst := range map[string]*float64{"margin_long": &d.marginLong, "margin_short": &d.marginShort} {
		if x := at(n); x != nil {
			if v, ok := constNum(x); ok && v >= 0 {
				*dst = v
			} else {
				c.errf(x.position(), "%s must be a number >= 0 (percent of the position value)", n)
			}
		}
	}
	for _, n := range []string{"calc_on_every_tick", "process_orders_on_close", "use_bar_magnifier"} {
		if x := at(n); x != nil {
			if v, ok := constBool(x); ok && v {
				c.errf(x.position(), "%s=true is not supported (hlp fills at the next bar open)", n)
			}
		}
	}
}

// input registers an input.* call with its default.
func (c *checker) input(e *callExpr) {
	kind := strings.TrimPrefix(strings.TrimPrefix(e.Fn, "input"), ".")
	in := Input{Kind: kind}
	if t, ok := constStr(e.bound[1]); ok && t != "" {
		in.Name = t
	} else if c.declName != "" {
		in.Name = c.declName
	} else {
		in.Name = fmt.Sprintf("input %d", len(c.script.Inputs)+1)
	}
	def := e.bound[0]
	switch kind {
	case "source":
		id, ok := def.(*ident)
		if !ok || !sourceNames[id.Name] {
			c.errf(def.position(), "input.source default must be a price source (close, hl2, ...)")
			return
		}
		in.Default = id.Name
	case "color":
		in.Default = "color"
	case "timeframe":
		v, ok := constStr(def)
		if !ok {
			c.errf(def.position(), "input.timeframe default must be a string such as \"D\" or \"240\"")
			return
		}
		if _, err := parseTF(v); err != nil {
			c.errf(def.position(), "%v", err)
			return
		}
		in.Default = v
	case "time":
		v, ok := constNum(def)
		if !ok {
			c.errf(def.position(), "input.time default must be a constant timestamp, e.g. timestamp(\"2024-01-01T00:00:00\")")
			return
		}
		in.Default = v
	default:
		v, ok := constValue(def)
		if !ok {
			c.errf(def.position(), "input default must be a constant")
			return
		}
		in.Default = v
		if kind == "" {
			switch v.(type) {
			case bool:
				in.Kind = "bool"
			case string:
				in.Kind = "string"
			default:
				in.Kind = "float"
			}
		}
		if kind == "price" {
			in.Kind = "float"
		}
		if kind == "text_area" {
			in.Kind = "string"
		}
		if in.Kind == "int" {
			if f, ok := v.(float64); ok {
				in.Default = int(f)
			}
		}
	}
	if x := e.bound[indexOf(builtins[e.Fn].params, "options")]; x != nil {
		if te, ok := x.(*tupleExpr); ok {
			for _, el := range te.Elems {
				if v, ok := constValue(el); ok {
					in.Options = append(in.Options, v)
				}
			}
		}
	}
	if x := e.bound[indexOf(builtins[e.Fn].params, "minval")]; x != nil {
		if v, ok := constNum(x); ok {
			in.Min = &v
		}
	}
	if x := e.bound[indexOf(builtins[e.Fn].params, "maxval")]; x != nil {
		if v, ok := constNum(x); ok {
			in.Max = &v
		}
	}
	c.prog.inputs[e.id] = &inputDecl{index: len(c.script.Inputs), varName: c.declName}
	c.script.Inputs = append(c.script.Inputs, in)
}

// ---- constant folding for declarations and inputs ----

func constValue(e expr) (any, bool) {
	switch e := e.(type) {
	case *numLit:
		return e.V, true
	case *strLit:
		return e.V, true
	case *boolLit:
		return e.V, true
	case *unaryExpr:
		if e.Op == "-" {
			if v, ok := constNum(e.X); ok {
				return -v, true
			}
		}
	case *ident:
		if constNames[e.Name] {
			return builtinVars[e.Name].get(nil, 0).s, true
		}
	case *callExpr:
		if e.Fn == "timestamp" && len(e.Named) == 0 {
			args := make([]value, len(e.Args))
			for i, a := range e.Args {
				v, ok := constValue(a)
				if !ok {
					return nil, false
				}
				switch v := v.(type) {
				case float64:
					args[i] = num(v)
				case string:
					args[i] = str(v)
				default:
					return nil, false
				}
			}
			if ms, err := timestampOf(args); err == nil && !math.IsNaN(ms) {
				return ms, true
			}
		}
	case *binaryExpr:
		x, ok1 := constNum(e.X)
		y, ok2 := constNum(e.Y)
		if ok1 && ok2 {
			switch e.Op {
			case "+":
				return x + y, true
			case "-":
				return x - y, true
			case "*":
				return x * y, true
			case "/":
				if y != 0 {
					return x / y, true
				}
			}
		}
	}
	return nil, false
}

func constNum(e expr) (float64, bool) {
	v, ok := constValue(e)
	f, isF := v.(float64)
	return f, ok && isF && !math.IsNaN(f)
}

func constStr(e expr) (string, bool) {
	if e == nil {
		return "", false
	}
	v, ok := constValue(e)
	s, isS := v.(string)
	return s, ok && isS
}

func constBool(e expr) (bool, bool) {
	if e == nil {
		return false, false
	}
	v, ok := constValue(e)
	b, isB := v.(bool)
	return b, ok && isB
}

// constInt reports whether x is a "const int" expression: integer literals
// combined with + - * % and unary minus (spec B8: in v5 a division of two
// of them truncates).
func constInt(x expr) bool {
	switch e := x.(type) {
	case *numLit:
		return e.IsInt
	case *unaryExpr:
		return (e.Op == "-" || e.Op == "+") && constInt(e.X)
	case *binaryExpr:
		switch e.Op {
		case "+", "-", "*", "%":
			return constInt(e.X) && constInt(e.Y)
		case "/":
			return e.intDiv
		}
	}
	return false
}
