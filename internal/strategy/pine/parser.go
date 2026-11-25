package pine

import (
	"fmt"
	"strings"
)

// parser builds the AST from logical lines.
type parser struct {
	lines []line
	li    int // current line
	toks  []token
	ti    int // current token in toks
	errs  []error
	ids   int // call-site id counter
	funcs []*funcDef
}

type syntaxErr struct{ e *Error }

func (p *parser) fail(pos Pos, format string, args ...any) {
	panic(syntaxErr{&Error{Line: pos.Line, Col: pos.Col, Msg: fmt.Sprintf(format, args...)}})
}

var typeWords = map[string]bool{"int": true, "float": true, "bool": true, "string": true, "color": true}
var qualWords = map[string]bool{"series": true, "simple": true, "const": true, "input": true}
var unsupportedTypes = map[string]bool{"label": true, "line": true, "box": true, "table": true, "linefill": true,
	"array": true, "matrix": true, "map": true, "polyline": true, "chart.point": true}

// parseProgram parses top-level statements (level 0).
func (p *parser) parseProgram() []stmt {
	return p.parseBlock(0)
}

// parseBlock parses consecutive lines at exactly level; it stops at a
// shallower line. A deeper line here is an indentation error.
func (p *parser) parseBlock(level int) []stmt {
	var out []stmt
	for p.li < len(p.lines) {
		ln := p.lines[p.li]
		if ln.level < level {
			break
		}
		if ln.level > level {
			p.errs = append(p.errs, &Error{Line: ln.toks[0].pos.Line, Col: ln.toks[0].pos.Col, Msg: "unexpected indentation"})
			p.li++
			continue
		}
		if s := p.parseLineStmt(level); s != nil {
			out = append(out, s)
		}
	}
	return out
}

// parseLineStmt parses the statement starting at the current line,
// recovering to the next line on error.
func (p *parser) parseLineStmt(level int) (s stmt) {
	ln := p.lines[p.li]
	defer func() {
		if r := recover(); r != nil {
			se, ok := r.(syntaxErr)
			if !ok {
				panic(r)
			}
			p.errs = append(p.errs, se.e)
			s = nil
			// skip this line and any deeper block that belonged to it
			p.li++
			for p.li < len(p.lines) && p.lines[p.li].level > level {
				p.li++
			}
		}
	}()
	p.toks, p.ti = ln.toks, 0
	p.li++
	return p.stmt(level)
}

func (p *parser) peek() token {
	if p.ti < len(p.toks) {
		return p.toks[p.ti]
	}
	last := p.toks[len(p.toks)-1]
	return token{kind: tOp, text: "<eol>", pos: Pos{last.pos.Line, last.pos.Col + len(last.text)}}
}

func (p *parser) peekAt(k int) token {
	if p.ti+k < len(p.toks) {
		return p.toks[p.ti+k]
	}
	return token{kind: tOp, text: "<eol>"}
}

func (p *parser) next() token {
	t := p.peek()
	if p.ti < len(p.toks) {
		p.ti++
	}
	return t
}

func (p *parser) atEOL() bool { return p.ti >= len(p.toks) }

func (p *parser) expectOp(op string) token {
	t := p.next()
	if !t.is(op) {
		p.fail(t.pos, "expected %q, found %s", op, describe(t))
	}
	return t
}

func (p *parser) expectEOL() {
	if !p.atEOL() {
		t := p.peek()
		p.fail(t.pos, "unexpected %s", describe(t))
	}
}

func describe(t token) string {
	switch t.kind {
	case tStr:
		return fmt.Sprintf("string %q", t.text)
	case tNum:
		return "number " + t.text
	case tName:
		return fmt.Sprintf("%q", t.text)
	}
	if t.text == "<eol>" {
		return "end of line"
	}
	return fmt.Sprintf("%q", t.text)
}

// body parses an indented block following the current line.
func (p *parser) body(level int, at Pos) []stmt {
	if p.li >= len(p.lines) || p.lines[p.li].level <= level {
		p.fail(at, "expected an indented block")
	}
	return p.parseBlock(p.lines[p.li].level)
}

func (p *parser) stmt(level int) stmt {
	t := p.peek()
	if t.kind == tName {
		switch t.text {
		case "if":
			return p.ifStmt(level)
		case "for":
			return p.forStmt(level)
		case "while":
			p.next()
			cond := p.expr()
			p.expectEOL()
			return &whileStmt{P: t.pos, Cond: cond, Body: p.body(level, t.pos)}
		case "break":
			p.next()
			p.expectEOL()
			return &breakStmt{P: t.pos}
		case "continue":
			p.next()
			p.expectEOL()
			return &continueStmt{P: t.pos}
		case "switch":
			return p.switchStmt(level)
		case "import", "export", "method", "type", "enum":
			p.fail(t.pos, "%s is not supported (libraries, methods and user types are outside hlp's Pine subset)", t.text)
		case "return":
			p.fail(t.pos, "Pine has no return statement: the last expression of a function is its value")
		}
		// function definition: name(params) =>
		if p.peekAt(1).is("(") && p.isFuncDef() {
			return p.funcDef(level)
		}
		if s := p.declOrAssign(level); s != nil {
			return s
		}
	}
	if t.is("[") && p.isTupleDecl() {
		return p.tupleDecl()
	}
	x := p.expr()
	p.expectEOL()
	return &exprStmt{P: t.pos, X: x}
}

// isFuncDef scans for "name ( ... ) =>".
func (p *parser) isFuncDef() bool {
	depth := 0
	for k := p.ti + 1; k < len(p.toks); k++ {
		t := p.toks[k]
		if t.is("(") {
			depth++
		} else if t.is(")") {
			depth--
			if depth == 0 {
				return k+1 < len(p.toks) && p.toks[k+1].is("=>")
			}
		}
	}
	return false
}

func (p *parser) isTupleDecl() bool {
	for k := p.ti; k < len(p.toks); k++ {
		if p.toks[k].is("]") {
			return k+1 < len(p.toks) && (p.toks[k+1].is("=") || p.toks[k+1].is(":="))
		}
	}
	return false
}

func (p *parser) funcDef(level int) stmt {
	name := p.next()
	if level != 0 {
		p.fail(name.pos, "functions must be declared at the top level")
	}
	p.expectOp("(")
	var params []string
	var defs []expr
	for !p.peek().is(")") {
		// optional type/qualifier words before the parameter name
		for {
			t := p.peek()
			if t.kind == tName && (typeWords[t.text] || qualWords[t.text]) && p.peekAt(1).kind == tName {
				p.next()
				continue
			}
			break
		}
		pt := p.next()
		if pt.kind != tName {
			p.fail(pt.pos, "expected a parameter name, found %s", describe(pt))
		}
		var def expr
		if p.peek().is("=") {
			p.next()
			def = p.ternary()
		} else if len(defs) > 0 && defs[len(defs)-1] != nil {
			p.fail(pt.pos, "parameter %s needs a default value: parameters after one with a default must have defaults too", pt.text)
		}
		params = append(params, pt.text)
		defs = append(defs, def)
		if p.peek().is(",") {
			p.next()
		} else if !p.peek().is(")") {
			p.fail(p.peek().pos, "expected , or ) in parameter list")
		}
	}
	p.expectOp(")")
	p.expectOp("=>")
	fd := &funcDef{P: name.pos, Name: name.text, Params: params}
	for _, d := range defs {
		if d != nil {
			fd.Defaults = defs
			break
		}
	}
	if p.atEOL() {
		fd.Body = p.body(level, name.pos)
	} else {
		x := p.expr()
		p.expectEOL()
		fd.Body = []stmt{&exprStmt{P: x.position(), X: x}}
	}
	p.funcs = append(p.funcs, fd)
	return fd
}

func (p *parser) tupleDecl() stmt {
	open := p.expectOp("[")
	td := &tupleDecl{P: open.pos}
	for {
		t := p.next()
		if t.kind != tName {
			p.fail(t.pos, "expected a variable name in tuple, found %s", describe(t))
		}
		td.Names = append(td.Names, t.text)
		td.NameP = append(td.NameP, t.pos)
		if p.peek().is(",") {
			p.next()
			continue
		}
		break
	}
	p.expectOp("]")
	eq := p.next()
	if eq.is(":=") {
		p.fail(eq.pos, "tuple reassignment with := is not supported; declare new names with =")
	}
	td.X = p.expr()
	p.expectEOL()
	return td
}

// declOrAssign handles "[var] [qual] [type] name = x" and "name := x".
// It returns nil (without consuming) when the line is not one.
func (p *parser) declOrAssign(level int) stmt {
	start := p.ti
	first := p.peek()
	isVar := false
	if first.text == "var" || first.text == "varip" {
		isVar = true
		p.next()
	}
	typ := ""
	for {
		t := p.peek()
		nx := p.peekAt(1)
		if t.kind == tName && qualWords[t.text] && nx.kind == tName {
			p.next()
			continue
		}
		if t.kind == tName && (typeWords[t.text] || unsupportedTypes[baseType(t.text)]) && nx.kind == tName {
			if unsupportedTypes[baseType(t.text)] {
				p.fail(t.pos, "type %s is not supported (hlp's Pine subset has no drawings, arrays, maps or matrices)", t.text)
			}
			typ = t.text
			p.next()
			continue
		}
		if t.kind == tName && unsupportedTypes[baseType(t.text)] && nx.is("<") {
			p.fail(t.pos, "type %s<...> is not supported (no arrays, maps or matrices in hlp's Pine subset)", t.text)
		}
		break
	}
	name := p.peek()
	op := p.peekAt(1)
	if name.kind != tName || !(op.is("=") || op.is(":=") || op.is("+=") || op.is("-=") || op.is("*=") || op.is("/=") || op.is("%=")) {
		if isVar || typ != "" {
			p.fail(name.pos, "expected a variable declaration")
		}
		p.ti = start
		return nil
	}
	p.next()
	p.next()
	var x expr
	switch {
	case p.peek().isName("if"):
		at := p.peek().pos
		x = &stmtExpr{P: at, S: p.ifStmt(level)}
	case p.peek().isName("switch"):
		at := p.peek().pos
		x = &stmtExpr{P: at, S: p.switchStmt(level)}
	case p.peek().isName("for") || p.peek().isName("while"):
		p.fail(p.peek().pos, "%s used as an expression is not supported yet; assign inside the loop instead", p.peek().text)
	case p.atEOL():
		p.fail(op.pos, "missing value after %s", op.text)
	default:
		x = p.expr()
		p.expectEOL()
	}
	if op.is("=") {
		if strings.Contains(name.text, ".") {
			p.fail(name.pos, "cannot declare a dotted name %q", name.text)
		}
		return &declStmt{P: name.pos, Var: isVar, Type: typ, Name: name.text, X: x}
	}
	if isVar || typ != "" {
		p.fail(op.pos, "use = to declare a variable; := only reassigns")
	}
	return &assignStmt{P: name.pos, Name: name.text, Op: op.text, X: x}
}

func baseType(s string) string {
	if i := strings.Index(s, "."); i > 0 && s != "chart.point" {
		return s[:i]
	}
	return s
}

func (p *parser) ifStmt(level int) stmt {
	t := p.next() // if
	cond := p.expr()
	p.expectEOL()
	is := &ifStmt{P: t.pos, Cond: cond, Then: p.body(level, t.pos)}
	if p.li < len(p.lines) && p.lines[p.li].level == level && p.lines[p.li].toks[0].isName("else") {
		ln := p.lines[p.li]
		p.toks, p.ti = ln.toks, 1
		p.li++
		if p.peek().isName("if") {
			is.Else = []stmt{p.ifStmt(level)}
		} else {
			p.expectEOL()
			is.Else = p.body(level, ln.toks[0].pos)
		}
	}
	return is
}

// switchStmt parses "switch [subject]" and its arms, one per line at the
// next indentation level: "value => expr", "value =>" + an indented block,
// or "=> ..." for the default arm.
func (p *parser) switchStmt(level int) stmt {
	t := p.next() // switch
	ss := &switchStmt{P: t.pos}
	if !p.atEOL() {
		ss.Subject = p.expr()
	}
	p.expectEOL()
	if p.li >= len(p.lines) || p.lines[p.li].level <= level {
		p.fail(t.pos, "switch needs its arms on the following lines, indented")
	}
	arm := p.lines[p.li].level
	for p.li < len(p.lines) && p.lines[p.li].level == arm {
		ln := p.lines[p.li]
		p.toks, p.ti = ln.toks, 0
		p.li++
		sc := switchCase{P: ln.toks[0].pos}
		if p.peek().is("=>") {
			p.next()
		} else {
			sc.Cond = p.expr()
			if !p.peek().is("=>") {
				p.fail(p.peek().pos, "expected => after the switch case, found %s", describe(p.peek()))
			}
			p.next()
		}
		if p.atEOL() {
			sc.Body = p.body(arm, sc.P)
		} else {
			sc.Body = []stmt{p.stmt(arm)}
		}
		if sc.Cond == nil {
			for _, prev := range ss.Cases {
				if prev.Cond == nil {
					p.fail(sc.P, "switch has more than one default arm (=>)")
				}
			}
		}
		ss.Cases = append(ss.Cases, sc)
	}
	if p.li < len(p.lines) && p.lines[p.li].level > level && p.lines[p.li].level != arm {
		ln := p.lines[p.li]
		p.fail(ln.toks[0].pos, "switch arms must all have the same indentation")
	}
	return ss
}

func (p *parser) forStmt(level int) stmt {
	t := p.next() // for
	v := p.next()
	if v.kind != tName {
		p.fail(v.pos, "expected a loop variable")
	}
	if p.peek().isName("in") {
		p.fail(p.peek().pos, "for ... in loops need arrays, which are not supported yet")
	}
	p.expectOp("=")
	from := p.expr()
	if !p.peek().isName("to") {
		p.fail(p.peek().pos, "expected to")
	}
	p.next()
	to := p.expr()
	var by expr
	if p.peek().isName("by") {
		p.next()
		by = p.expr()
	}
	p.expectEOL()
	return &forStmt{P: t.pos, Var: v.text, From: from, To: to, By: by, Body: p.body(level, t.pos)}
}

// ---- expressions (precedence climbing) ----

func (p *parser) expr() expr { return p.ternary() }

func (p *parser) ternary() expr {
	c := p.orExpr()
	if p.peek().is("?") {
		q := p.next()
		a := p.ternary()
		p.expectOp(":")
		b := p.ternary()
		return &ternaryExpr{P: q.pos, Cond: c, A: a, B: b}
	}
	return c
}

func (p *parser) orExpr() expr {
	x := p.andExpr()
	for p.peek().isName("or") {
		t := p.next()
		x = &binaryExpr{P: t.pos, Op: "or", X: x, Y: p.andExpr()}
	}
	return x
}

func (p *parser) andExpr() expr {
	x := p.eqExpr()
	for p.peek().isName("and") {
		t := p.next()
		x = &binaryExpr{P: t.pos, Op: "and", X: x, Y: p.eqExpr()}
	}
	return x
}

func (p *parser) eqExpr() expr {
	x := p.cmpExpr()
	for p.peek().is("==") || p.peek().is("!=") {
		t := p.next()
		x = &binaryExpr{P: t.pos, Op: t.text, X: x, Y: p.cmpExpr()}
	}
	return x
}

func (p *parser) cmpExpr() expr {
	x := p.addExpr()
	for p.peek().is("<") || p.peek().is(">") || p.peek().is("<=") || p.peek().is(">=") {
		t := p.next()
		x = &binaryExpr{P: t.pos, Op: t.text, X: x, Y: p.addExpr()}
	}
	return x
}

func (p *parser) addExpr() expr {
	x := p.mulExpr()
	for p.peek().is("+") || p.peek().is("-") {
		t := p.next()
		x = &binaryExpr{P: t.pos, Op: t.text, X: x, Y: p.mulExpr()}
	}
	return x
}

func (p *parser) mulExpr() expr {
	x := p.unary()
	for p.peek().is("*") || p.peek().is("/") || p.peek().is("%") {
		t := p.next()
		x = &binaryExpr{P: t.pos, Op: t.text, X: x, Y: p.unary()}
	}
	return x
}

func (p *parser) unary() expr {
	t := p.peek()
	if t.is("-") || t.is("+") || t.isName("not") {
		p.next()
		op := t.text
		return &unaryExpr{P: t.pos, Op: op, X: p.unary()}
	}
	return p.postfix()
}

func (p *parser) postfix() expr {
	x := p.primary()
	for {
		t := p.peek()
		switch {
		case t.is("["):
			p.next()
			n := p.expr()
			p.expectOp("]")
			p.ids++
			x = &indexExpr{P: t.pos, X: x, N: n, id: p.ids}
		case t.is("."):
			p.fail(t.pos, "method calls and field access are not supported")
		default:
			return x
		}
	}
}

func (p *parser) primary() expr {
	t := p.next()
	switch t.kind {
	case tNum:
		return &numLit{P: t.pos, V: t.num, IsInt: t.isInt}
	case tStr:
		return &strLit{P: t.pos, V: t.text}
	case tColor:
		return &colorLit{P: t.pos, V: t.text}
	case tName:
		switch t.text {
		case "true":
			return &boolLit{P: t.pos, V: true}
		case "false":
			return &boolLit{P: t.pos, V: false}
		case "if", "for", "while", "switch", "var", "varip", "else", "and", "or", "to", "by":
			p.fail(t.pos, "unexpected keyword %q", t.text)
		}
		if p.peek().is("<") && unsupportedTypes[baseType(t.text)] {
			p.fail(t.pos, "%s is not supported (no arrays, maps or matrices in hlp's Pine subset)", t.text)
		}
		if p.peek().is("(") {
			return p.call(t)
		}
		if t.text == "na" {
			return &naLit{P: t.pos}
		}
		return &ident{P: t.pos, Name: t.text}
	case tOp:
		switch t.text {
		case "(":
			x := p.expr()
			p.expectOp(")")
			return x
		case "[":
			te := &tupleExpr{P: t.pos}
			for !p.peek().is("]") {
				te.Elems = append(te.Elems, p.expr())
				if p.peek().is(",") {
					p.next()
				} else if !p.peek().is("]") {
					p.fail(p.peek().pos, "expected , or ] in tuple")
				}
			}
			p.expectOp("]")
			return te
		}
	}
	p.fail(t.pos, "unexpected %s", describe(t))
	return nil
}

func (p *parser) call(name token) expr {
	p.expectOp("(")
	p.ids++
	c := &callExpr{P: name.pos, Fn: name.text, id: p.ids}
	for !p.peek().is(")") {
		if p.peek().kind == tName && p.peekAt(1).is("=") {
			n := p.next()
			p.next()
			c.Named = append(c.Named, namedArg{Name: n.text, P: n.pos, X: p.expr()})
		} else {
			if len(c.Named) > 0 {
				p.fail(p.peek().pos, "positional argument after a named argument")
			}
			c.Args = append(c.Args, p.expr())
		}
		if p.peek().is(",") {
			p.next()
		} else if !p.peek().is(")") {
			p.fail(p.peek().pos, "expected , or ) in call to %s, found %s", name.text, describe(p.peek()))
		}
	}
	p.expectOp(")")
	return c
}
