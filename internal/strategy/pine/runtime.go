package pine

import (
	"fmt"
	"math"

	"github.com/maxmoneycash/hlp-cli/internal/domain"
	"github.com/maxmoneycash/hlp-cli/internal/strategy"
)

// layout describes the variable slots of the program or a function.
type layout struct {
	nslots  int
	slotVar []bool
}

func (l *layout) add(isVar bool) int {
	l.slotVar = append(l.slotVar, isVar)
	l.nslots++
	return l.nslots - 1
}

// program is a checked script ready to run.
type program struct {
	stmts  []stmt
	layout layout
	funcs  map[string]*funcDef
	flay   map[*funcDef]*layout
	inputs map[int]*inputDecl // call id -> input
	decl   scriptDecl
	plots  []string
	// plotTitle maps a plot() call site to its series name.
	plotTitle map[int]string
	usesVWAP  bool
}

// scriptDecl holds the strategy()/indicator() arguments that matter.
type scriptDecl struct {
	title      string
	isStrat    bool
	overlay    bool
	capital    float64
	qtyType    string // fixed | cash | percent_of_equity
	qtyValue   float64
	pyramiding int
	// marginLong and marginShort are strategy(margin_long=, margin_short=)
	// in percent (0 = not set: hlp uses 1x, TradingView's 100% default).
	marginLong, marginShort float64
}

// frame is the storage of one program or function-call instance.
type frame struct {
	lay   *layout
	slots []*series
	state map[int]*callState
	idx   map[int]*series
	kids  map[int]*frame
}

func newFrame(l *layout) *frame {
	f := &frame{lay: l, slots: make([]*series, l.nslots), state: map[int]*callState{}, idx: map[int]*series{}, kids: map[int]*frame{}}
	for i := range f.slots {
		f.slots[i] = newSeries(l.slotVar[i])
	}
	return f
}

// callState is per-call-site state for stateful builtins (ta.*).
type callState struct {
	data any
}

// rtError is a positioned runtime failure (raised with panic, recovered
// per bar).
type rtError struct{ e *Error }

func (rt *runtime) fail(p Pos, format string, args ...any) {
	panic(rtError{&Error{Line: p.Line, Col: p.Col, Msg: fmt.Sprintf(format, args...)}})
}

type flow int

const (
	flowNormal flow = iota
	flowBreak
	flowContinue
)

// runtime executes a program bar by bar against a strategy.Context.
type runtime struct {
	prog     *program
	root     *frame
	coin     string
	iv       domain.Interval
	inputs   map[int]value // call id -> bound input value (sources bound by name)
	srcInput map[int]string

	// current bar
	bar     int // bar_index
	cur     domain.Candle
	candle  []domain.Candle // ring of recent candles, indexed by bar % maxBarsBack
	live    bool            // false while catching up on history
	ctx     strategy.Context
	mintick float64
	szDec   int // the market's szDecimals (caps mintick decimals at 6 - szDec)

	// order bookkeeping
	expPos   float64 // position after this bar's queued market orders
	posEntry string  // entry id that opened the current position
	entries  int     // entries in the current direction (pyramiding)
	lastPos  float64
	loopOps  int
	reanchor []exitReq // tick-based exits waiting for the entry fill
	posHist  *series   // strategy.position_size history
	vwap     vwapState // the ta.vwap variable (hlc3, daily anchor)

	// pending are resting (limit/stop) entries by id. The entry id of an
	// open position is taken from the entry that FILLED: when the position
	// opens or flips while one of these rests in that direction, it is
	// the one that filled (spec A1).
	pending map[string]pendingEntry
	pseq    int
	// trails are active trailing exits by exit id; best is the most
	// favourable price since the position opened.
	trails         map[string]exitReq
	bestHi, bestLo float64

	// trades is the trade list kept from fills (trades.go).
	trades trades
	// obv is ta.obv's running total; obvHist its history.
	obv     float64
	obvHist *series
}

// pendingEntry is a resting strategy.entry / strategy.order.
type pendingEntry struct {
	long    bool
	oca     string
	ocaType string
	seq     int
	entry   bool // strategy.entry (sets the position's entry id)
}

func newRuntime(p *program, coin string, iv domain.Interval, inputs map[int]value, src map[int]string) *runtime {
	return &runtime{prog: p, root: newFrame(&p.layout), coin: coin, iv: iv, inputs: inputs, srcInput: src,
		candle: make([]domain.Candle, 0, 512), bar: -1, posHist: newSeries(true), vwap: vwapState{day: -1},
		pending: map[string]pendingEntry{}, trails: map[string]exitReq{}, bestHi: math.NaN(), bestLo: math.NaN(),
		obvHist: newSeries(true)}
}

// candleAt returns the candle back bars ago (false when unavailable).
func (rt *runtime) candleAt(back int) (domain.Candle, bool) {
	b := rt.bar - back
	if back < 0 || b < 0 || back >= maxBarsBack {
		return domain.Candle{}, false
	}
	return rt.candle[b%maxBarsBack], true
}

// pushCandle makes c the current bar.
func (rt *runtime) pushCandle(c domain.Candle) {
	rt.bar++
	rt.cur = c
	if len(rt.candle) < maxBarsBack {
		rt.candle = append(rt.candle, c)
	} else {
		rt.candle[rt.bar%maxBarsBack] = c
	}
	if c.C > 0 {
		rt.mintick = tickFor(c.C, rt.szDec)
	}
	if prev, ok := rt.candleAt(1); ok {
		switch {
		case c.C > prev.C:
			rt.obv += c.V
		case c.C < prev.C:
			rt.obv -= c.V
		}
	}
	rt.obvHist.set(rt.bar, num(rt.obv))
}

// tickFor is syminfo.mintick at price px: Hyperliquid prices carry five
// significant figures (integer prices from 100000 up) and at most
// 6 - szDecimals decimals. It is recomputed every bar (spec A4).
func tickFor(px float64, szDec int) float64 {
	if px >= 100000 {
		return 1
	}
	t := math.Pow(10, math.Floor(math.Log10(px))-4)
	return math.Max(t, math.Pow(10, -float64(6-max(szDec, 0))))
}

// step runs the script on one closed bar.
func (rt *runtime) step(c domain.Candle, live bool) (err error) {
	rt.pushCandle(c)
	rt.live = live
	rt.loopOps = 0
	if rt.prog.usesVWAP {
		rt.vwap.update(c, (c.H+c.L+c.C)/3, dayKey(c.T) != rt.vwap.day)
	}
	if rt.ctx != nil {
		rt.posHist.set(rt.bar, num(rt.ctx.Position(rt.coin)))
	}
	if live && rt.ctx != nil {
		rt.syncTrades()
		pos := rt.ctx.Position(rt.coin)
		opened := pos != 0 && (rt.lastPos == 0 || (pos > 0) != (rt.lastPos > 0))
		switch {
		case pos == 0:
			rt.posEntry, rt.entries = "", 0
			rt.trails = map[string]exitReq{}
			rt.bestHi, rt.bestLo = math.NaN(), math.NaN()
		case opened:
			if rt.entries == 0 {
				rt.entries = 1
			}
			rt.bestHi, rt.bestLo = c.H, c.L
		default:
			rt.bestHi, rt.bestLo = math.Max(rt.bestHi, c.H), math.Min(rt.bestLo, c.L)
			if math.IsNaN(rt.bestHi) {
				rt.bestHi, rt.bestLo = c.H, c.L
			}
		}
		rt.settleEntries(opened, pos > 0)
		rt.lastPos = pos
		rt.expPos = pos
	}
	defer func() {
		if r := recover(); r != nil {
			re, ok := r.(rtError)
			if !ok {
				panic(r)
			}
			err = re.e
		}
	}()
	if live {
		rt.reanchorExits()
		rt.updateTrails()
	}
	rt.execBlock(rt.root, rt.prog.stmts)
	return nil
}

// execBlock runs statements; it returns the flow signal and the value of
// the last statement (a function's return value).
func (rt *runtime) execBlock(f *frame, ss []stmt) (flow, value) {
	last := naV
	for _, s := range ss {
		fl, v := rt.exec(f, s)
		last = v
		if fl != flowNormal {
			return fl, last
		}
	}
	return flowNormal, last
}

func (rt *runtime) exec(f *frame, s stmt) (flow, value) {
	switch s := s.(type) {
	case *exprStmt:
		return flowNormal, rt.eval(f, s.X)
	case *declStmt:
		sl := f.slots[s.slot]
		if s.Var {
			if sl.last == -1 {
				v := rt.coerce(s.Type, rt.eval(f, s.X), s.P)
				sl.set(rt.bar, v)
				return flowNormal, v
			}
			v := sl.get(rt.bar)
			sl.set(rt.bar, v)
			return flowNormal, v
		}
		v := rt.coerce(s.Type, rt.eval(f, s.X), s.P)
		if v.k == kTuple {
			rt.fail(s.P, "a function returning a tuple must be assigned to [a, b, ...]")
		}
		sl.set(rt.bar, v)
		return flowNormal, v
	case *tupleDecl:
		v := rt.eval(f, s.X)
		if v.k != kTuple {
			rt.fail(s.P, "right side does not return a tuple")
		}
		if len(v.t) != len(s.Names) {
			rt.fail(s.P, "tuple has %d values, %d names given", len(v.t), len(s.Names))
		}
		for i, sl := range s.slots {
			f.slots[sl].set(rt.bar, v.t[i])
		}
		return flowNormal, v
	case *assignStmt:
		v := rt.eval(f, s.X)
		sl := f.slots[s.slot]
		if s.Op != ":=" {
			cur := sl.get(rt.bar)
			v = rt.arith(s.P, s.Op[:1], cur, v)
		}
		sl.set(rt.bar, v)
		return flowNormal, v
	case *ifStmt:
		if rt.eval(f, s.Cond).truthy() {
			return rt.execBlock(f, s.Then)
		} else if s.Else != nil {
			return rt.execBlock(f, s.Else)
		}
		return flowNormal, naV
	case *forStmt:
		from := rt.eval(f, s.From)
		to := rt.eval(f, s.To)
		if from.isNa() || to.isNa() {
			return flowNormal, naV
		}
		step := 1.0
		if s.By != nil {
			step = math.Abs(rt.eval(f, s.By).f)
			if step == 0 || math.IsNaN(step) {
				rt.fail(s.P, "for loop step must be non-zero")
			}
		}
		if to.f < from.f {
			step = -step
		}
		last := naV
		for i := from.f; (step > 0 && i <= to.f+1e-9) || (step < 0 && i >= to.f-1e-9); i += step {
			rt.tick(s.P)
			f.slots[s.slot].set(rt.bar, num(i))
			fl, v := rt.execBlock(f, s.Body)
			last = v
			if fl == flowBreak {
				break
			}
		}
		return flowNormal, last
	case *whileStmt:
		last := naV
		for rt.eval(f, s.Cond).truthy() {
			rt.tick(s.P)
			fl, v := rt.execBlock(f, s.Body)
			last = v
			if fl == flowBreak {
				break
			}
		}
		return flowNormal, last
	case *breakStmt:
		return flowBreak, naV
	case *continueStmt:
		return flowContinue, naV
	case *switchStmt:
		var subj value
		if s.Subject != nil {
			subj = rt.eval(f, s.Subject)
		}
		for _, sc := range s.Cases {
			hit := sc.Cond == nil
			if !hit {
				v := rt.eval(f, sc.Cond)
				if s.Subject != nil {
					hit = equal(subj, v)
				} else {
					hit = v.truthy()
				}
			}
			if hit {
				return rt.execBlock(f, sc.Body)
			}
		}
		return flowNormal, naV
	case *funcDef:
		return flowNormal, naV
	}
	rt.fail(s.position(), "unsupported statement")
	return flowNormal, naV
}

// tick guards against runaway loops.
func (rt *runtime) tick(p Pos) {
	rt.loopOps++
	if rt.loopOps > 1_000_000 {
		rt.fail(p, "loop ran more than 1,000,000 iterations on one bar")
	}
}

func (rt *runtime) coerce(typ string, v value, p Pos) value {
	switch typ {
	case "int":
		if v.k == kNum && !math.IsNaN(v.f) {
			return num(math.Trunc(v.f))
		}
	case "bool":
		if v.k == kNum || v.k == kNa {
			return boolean(v.truthy())
		}
	case "float":
		if v.k == kBool {
			rt.fail(p, "cannot assign a bool to a float")
		}
		if v.k == kNa {
			return num(math.NaN())
		}
	}
	return v
}

func (rt *runtime) eval(f *frame, e expr) value {
	switch e := e.(type) {
	case *numLit:
		return num(e.V)
	case *strLit:
		return str(e.V)
	case *boolLit:
		return boolean(e.V)
	case *naLit:
		return naV
	case *colorLit:
		return value{k: kColor, s: e.V}
	case *ident:
		switch e.ref.kind {
		case refSlot:
			return f.slots[e.ref.slot].get(rt.bar)
		case refBuiltin:
			return e.ref.bv.get(rt, 0)
		case refGlobal:
			return rt.root.slots[e.ref.slot].get(rt.bar)
		}
		rt.fail(e.P, "unresolved name %s", e.Name)
	case *indexExpr:
		nv := rt.eval(f, e.N)
		if nv.isNa() {
			rt.fail(e.P, "history offset is na")
		}
		n := int(nv.f)
		if n < 0 {
			rt.fail(e.P, "history offset must be >= 0 (no look-ahead)")
		}
		if id, ok := e.X.(*ident); ok {
			switch id.ref.kind {
			case refSlot:
				return f.slots[id.ref.slot].get(rt.bar - n)
			case refBuiltin:
				return id.ref.bv.get(rt, n)
			case refGlobal:
				return rt.root.slots[id.ref.slot].get(rt.bar - n)
			}
		}
		v := rt.eval(f, e.X)
		s := f.idx[e.id]
		if s == nil {
			s = newSeries(false)
			f.idx[e.id] = s
		}
		s.set(rt.bar, v)
		return s.get(rt.bar - n)
	case *unaryExpr:
		v := rt.eval(f, e.X)
		switch e.Op {
		case "not":
			if v.k == kNa {
				return naV
			}
			return boolean(!v.truthy())
		case "-":
			if v.k == kNum || v.k == kNa {
				return num(-v.f)
			}
			rt.fail(e.P, "cannot negate %s", kindName(v))
		case "+":
			return v
		}
	case *binaryExpr:
		switch e.Op {
		case "and":
			x := rt.eval(f, e.X)
			if !x.truthy() {
				return falseV
			}
			return boolean(rt.eval(f, e.Y).truthy())
		case "or":
			x := rt.eval(f, e.X)
			if x.truthy() {
				return trueV
			}
			return boolean(rt.eval(f, e.Y).truthy())
		}
		x, y := rt.eval(f, e.X), rt.eval(f, e.Y)
		switch e.Op {
		case "==", "!=":
			eq := equal(x, y)
			if e.Op == "!=" {
				if x.isNa() || y.isNa() {
					return falseV
				}
				return boolean(!eq)
			}
			return boolean(eq)
		case "<", ">", "<=", ">=":
			if x.isNa() || y.isNa() {
				return falseV
			}
			if x.k == kStr || y.k == kStr {
				rt.fail(e.P, "cannot compare strings with %s", e.Op)
			}
			switch e.Op {
			case "<":
				return boolean(x.f < y.f)
			case ">":
				return boolean(x.f > y.f)
			case "<=":
				return boolean(x.f <= y.f)
			default:
				return boolean(x.f >= y.f)
			}
		}
		v := rt.arith(e.P, e.Op, x, y)
		if e.intDiv && v.k == kNum {
			v.f = math.Trunc(v.f)
		}
		return v
	case *ternaryExpr:
		if rt.eval(f, e.Cond).truthy() {
			return rt.eval(f, e.A)
		}
		return rt.eval(f, e.B)
	case *tupleExpr:
		t := make([]value, len(e.Elems))
		for i, x := range e.Elems {
			t[i] = rt.eval(f, x)
		}
		return value{k: kTuple, t: t}
	case *callExpr:
		return rt.call(f, e)
	case *stmtExpr:
		_, v := rt.exec(f, e.S)
		return v
	}
	rt.fail(e.position(), "unsupported expression")
	return naV
}

func equal(x, y value) bool {
	if x.isNa() || y.isNa() {
		return false
	}
	if x.k == kStr || y.k == kStr || x.k == kColor || y.k == kColor {
		return x.s == y.s && x.k == y.k
	}
	return x.f == y.f
}

func kindName(v value) string {
	switch v.k {
	case kNum:
		return "a number"
	case kBool:
		return "a bool"
	case kStr:
		return "a string"
	case kColor:
		return "a color"
	case kTuple:
		return "a tuple"
	}
	return "na"
}

func (rt *runtime) arith(p Pos, op string, x, y value) value {
	if op == "+" && (x.k == kStr || y.k == kStr) {
		if x.k == kStr && y.k == kStr {
			return str(x.s + y.s)
		}
		rt.fail(p, "cannot add %s and %s (use str.tostring)", kindName(x), kindName(y))
	}
	for _, v := range []value{x, y} {
		if v.k == kStr || v.k == kColor || v.k == kTuple {
			rt.fail(p, "operator %s needs numbers, got %s", op, kindName(v))
		}
	}
	a, b := x.f, y.f
	switch op {
	case "+":
		return num(a + b)
	case "-":
		return num(a - b)
	case "*":
		return num(a * b)
	case "/":
		if b == 0 {
			return naV
		}
		return num(a / b)
	case "%":
		if b == 0 {
			return naV
		}
		return num(math.Mod(a, b))
	}
	rt.fail(p, "unknown operator %s", op)
	return naV
}

// call evaluates a builtin or user function call.
func (rt *runtime) call(f *frame, c *callExpr) value {
	if c.sec != nil {
		return rt.security(f, c)
	}
	if c.fn != nil {
		args := make([]value, len(c.bound))
		for i, a := range c.bound {
			args[i] = rt.eval(f, a)
		}
		kid := f.kids[c.id]
		if kid == nil {
			kid = newFrame(rt.prog.flay[c.fn])
			f.kids[c.id] = kid
		}
		for i, sl := range c.fn.pslots {
			kid.slots[sl].set(rt.bar, args[i])
		}
		_, v := rt.execBlock(kid, c.fn.Body)
		return v
	}
	if v, ok := rt.inputs[c.id]; ok {
		if src, ok := rt.srcInput[c.id]; ok {
			return sourceValue(rt, src, 0)
		}
		return v
	}
	args := make([]value, len(c.bound))
	for i, a := range c.bound {
		if a == nil {
			args[i] = missingV
			continue
		}
		args[i] = rt.eval(f, a)
	}
	st := f.state[c.id]
	if st == nil && c.bi.stateful {
		st = &callState{}
		f.state[c.id] = st
	}
	return c.bi.fn(rt, st, args, c)
}

// ---- strategy binding ----

// sizeFor converts Pine qty semantics into contracts at the current close.
func (rt *runtime) sizeFor(qty value) float64 {
	px := rt.cur.C
	if !qty.missing() && !qty.isNa() {
		return qty.f
	}
	d := rt.prog.decl
	switch d.qtyType {
	case "cash":
		if px > 0 {
			return d.qtyValue / px
		}
	case "percent_of_equity":
		if px > 0 {
			return rt.ctx.Equity() * d.qtyValue / 100 / px
		}
	default:
		return d.qtyValue
	}
	return 0
}

func (rt *runtime) order(p Pos, o strategy.Order) {
	if !rt.live || rt.ctx == nil {
		return
	}
	o.Coin = rt.coin
	if err := rt.ctx.Order(o); err != nil {
		rt.fail(p, "%v", err)
	}
	if o.Kind == strategy.Market || o.Kind == "" {
		sz := o.Size
		if sz == 0 && o.ReduceOnly {
			sz = math.Abs(rt.expPos)
		}
		if o.Side == domain.Buy {
			rt.expPos += sz
		} else {
			rt.expPos -= sz
		}
		if o.ReduceOnly && math.Abs(rt.expPos) < 1e-12 {
			rt.expPos = 0
		}
	}
}

// entryRef is the reference price for tick-based exits: the position's
// average entry, or the current close when the entry has not filled yet
// (estimated = true).
func (rt *runtime) entryRef() (float64, bool) {
	if pi, ok := rt.ctx.(strategy.PositionInfo); ok {
		pos := rt.ctx.Position(rt.coin)
		if pos != 0 && (pos > 0) == (rt.expPos > 0) && math.Abs(pos-rt.expPos) < 1e-9*math.Max(1, math.Abs(pos)) {
			if e := pi.EntryPrice(rt.coin); e > 0 {
				return e, false
			}
		}
	}
	return rt.cur.C, true
}
