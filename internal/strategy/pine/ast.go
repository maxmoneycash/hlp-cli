package pine

// ---- expressions ----

type expr interface{ position() Pos }

type numLit struct {
	P     Pos
	V     float64
	IsInt bool
}

type strLit struct {
	P Pos
	V string
}

type boolLit struct {
	P Pos
	V bool
}

type naLit struct{ P Pos }

type colorLit struct {
	P Pos
	V string
}

// ident is a name reference, resolved by the checker.
type ident struct {
	P    Pos
	Name string
	ref  ref
}

type namedArg struct {
	Name string
	P    Pos
	X    expr
}

// callExpr calls a builtin or a user function. id is a unique call site.
type callExpr struct {
	P     Pos
	Fn    string
	Args  []expr
	Named []namedArg
	id    int
	bi    *builtin // resolved builtin
	fn    *funcDef // resolved user function
	bound []expr   // resolved argument list (builtin or user parameter order)
	sec   *secInfo // request.security call (resolved by the checker)
}

// indexExpr is the history operator x[n].
type indexExpr struct {
	P  Pos
	X  expr
	N  expr
	id int
}

type unaryExpr struct {
	P  Pos
	Op string
	X  expr
}

type binaryExpr struct {
	P    Pos
	Op   string
	X, Y expr
	// intDiv: a v5 "const int / const int" truncates (spec B8; v6
	// returns the float quotient).
	intDiv bool
}

type ternaryExpr struct {
	P          Pos
	Cond, A, B expr
}

type tupleExpr struct {
	P     Pos
	Elems []expr
}

// stmtExpr is an if or switch statement used as an expression
// (x = switch kind ...): its value is the taken branch's last value, na
// when no branch runs.
type stmtExpr struct {
	P Pos
	S stmt
}

func (e *stmtExpr) position() Pos { return e.P }

func (e *numLit) position() Pos      { return e.P }
func (e *strLit) position() Pos      { return e.P }
func (e *boolLit) position() Pos     { return e.P }
func (e *naLit) position() Pos       { return e.P }
func (e *colorLit) position() Pos    { return e.P }
func (e *ident) position() Pos       { return e.P }
func (e *callExpr) position() Pos    { return e.P }
func (e *indexExpr) position() Pos   { return e.P }
func (e *unaryExpr) position() Pos   { return e.P }
func (e *binaryExpr) position() Pos  { return e.P }
func (e *ternaryExpr) position() Pos { return e.P }
func (e *tupleExpr) position() Pos   { return e.P }

// ---- statements ----

type stmt interface{ position() Pos }

// declStmt: [var|varip] [type] name = x
type declStmt struct {
	P    Pos
	Var  bool
	Type string
	Name string
	X    expr
	slot int
}

// tupleDecl: [a, b, c] = x
type tupleDecl struct {
	P     Pos
	Names []string
	NameP []Pos
	X     expr
	slots []int
}

// assignStmt: name := x, name += x, ...
type assignStmt struct {
	P    Pos
	Name string
	Op   string
	X    expr
	slot int
}

type ifStmt struct {
	P    Pos
	Cond expr
	Then []stmt
	Else []stmt
}

type forStmt struct {
	P            Pos
	Var          string
	From, To, By expr
	Body         []stmt
	slot         int
}

// switchCase is one arm; Cond is nil for the default arm (=> ...).
type switchCase struct {
	P    Pos
	Cond expr
	Body []stmt
}

// switchStmt: switch [subject] with "value => body" arms. Without a
// subject each arm's Cond is a condition; the first true arm runs.
type switchStmt struct {
	P       Pos
	Subject expr
	Cases   []switchCase
}

func (s *switchStmt) position() Pos { return s.P }

type whileStmt struct {
	P    Pos
	Cond expr
	Body []stmt
}

type breakStmt struct{ P Pos }
type continueStmt struct{ P Pos }

type exprStmt struct {
	P Pos
	X expr
}

// funcDef: name(params) => body. The value of the last statement is
// returned.
type funcDef struct {
	P        Pos
	Name     string
	Params   []string
	Defaults []expr // nil entries: required parameter
	Body     []stmt
	// usesGlobals: the body reads a top-level variable (set by the checker).
	usesGlobals bool
	nslots      int
	pslots      []int
}

func (s *declStmt) position() Pos     { return s.P }
func (s *tupleDecl) position() Pos    { return s.P }
func (s *assignStmt) position() Pos   { return s.P }
func (s *ifStmt) position() Pos       { return s.P }
func (s *forStmt) position() Pos      { return s.P }
func (s *whileStmt) position() Pos    { return s.P }
func (s *breakStmt) position() Pos    { return s.P }
func (s *continueStmt) position() Pos { return s.P }
func (s *exprStmt) position() Pos     { return s.P }
func (s *funcDef) position() Pos      { return s.P }

// refKind says what an identifier resolved to.
type refKind int

const (
	refNone    refKind = iota
	refSlot            // variable slot in the current frame
	refBuiltin         // builtin variable (close, bar_index, color.red, ...)
	refGlobal          // top-level variable read from inside a function
)

type ref struct {
	kind refKind
	slot int
	bv   *builtinVar
}
