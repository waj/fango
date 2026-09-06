// Package ast is the surface syntax tree: sealed interfaces with marker
// methods, every node carrying a Span.
package ast

import "github.com/waj/fango/internal/source"

type Expr interface {
	isExpr()
	Span() source.Span
}

type IntLit struct {
	Value int64
	Sp    source.Span
	Raw   bool // compiler-internal Int payload for Num.fromInt
}

type FloatLit struct {
	Value float64
	Sp    source.Span
}

type StringLit struct {
	Value string
	Sp    source.Span
}

// UnitLit is the sole value of the Unit type, written ().
type UnitLit struct{ Sp source.Span }

type Var struct {
	Name string
	Sp   source.Span
}

// Ctor is a constructor reference, including builtin True and False.
type Ctor struct {
	Name string
	Sp   source.Span
}

// App is curried application: `f x y` is App(App(f, x), y).
type App struct {
	Fn, Arg Expr
}

// Neg is unary minus — a distinct node (not BinOp with a zero) so spans and
// error messages stay clean.
type Neg struct {
	Operand Expr
	Sp      source.Span // minus sign through the operand
}

type If struct {
	Cond, Then, Else Expr
	Sp               source.Span // the `if` keyword
}

// Block is a statement-style body (doc/design.md, "Language semantics"): binding lines followed by one
// result expression. The parser collapses zero-binding blocks to the plain
// result expression, so a Block always has at least one binding.
type Block struct {
	Binds  []LocalBind
	Items  []BlockItem // ordered; nil for legacy binding-only blocks
	Result Expr
}

type BlockItem struct {
	BindIndex int
	Expr      Expr
}

// Param is one function parameter. Worker arity is the syntactic parameter
// count (doc/design.md, "Go backend and runtime"), which is why parameters stay explicit rather than
// desugaring to lambdas.
type Param struct {
	Name string // "_" discards; "()" is a Unit pattern in handler clauses
	Sp   source.Span
}

type LocalBind struct {
	Name     string
	NameSpan source.Span
	Params   []Param // non-empty: a local function
	Ann      *TypeAnn
	Body     Expr
}

// Lambda is `\x -> e` / `\x y -> e` — multi-param in the AST for clean
// spans and dumps; typing and elaboration treat it as curried.
type Lambda struct {
	Params []Param
	Body   Expr
	Sp     source.Span // the backslash
}

// TypeAnn is a `name : Type` annotation line attached to the definition
// directly below it.
type TypeAnn struct {
	Type  TypeExpr
	Preds []PredExpr
	Sp    source.Span // colon through the end of the type
}

type PredExpr struct {
	Class string
	Ty    TypeExpr
	Sp    source.Span
}

type ClassDecl struct {
	Name     string
	NameSpan source.Span
	Param    Param
	Methods  []OpSig
}

type InstanceDecl struct {
	Head    PredExpr
	Preds   []PredExpr
	Methods []*ValueDecl
	Owner   string
}

func (*ClassDecl) isDecl()    {}
func (*InstanceDecl) isDecl() {}

// TypeExpr is the surface type grammar: ground names, `()`, `->` arrows,
// effect rows attached to arrows, and type variables.
type TypeExpr interface {
	isTypeExpr()
	Span() source.Span
}

type TName struct {
	Name string // "Int", "Bool", … — "()" for unit
	Sp   source.Span
}

type TVarName struct {
	Name string // lowercase: a type variable
	Sp   source.Span
}

// TFunExpr is a function arrow. Eff is nil for a plain `->` — pure as
// written — and non-nil only for `->{…}` (doc/design.md, "Functions and effects"). The dumper omits a nil
// row, keeping pure-arrow dumps compact and stable.
type TFunExpr struct {
	Arg, Ret TypeExpr
	Eff      *EffRow
}

// EffRow is the surface effect row on an arrow: `->{Console}`,
// `->{Db, Fail String}`, or `->{Console | e}` with an explicit open tail.
type EffRow struct {
	Labels []EffLabelExpr
	Tail   string      // "" when closed; a lowercase row variable otherwise
	TailSp source.Span // zero when Tail is ""
	Sp     source.Span // the `{` through the `}`
}

// EffLabelExpr is one label in a surface row: an effect name plus type
// arguments, `Fail String`. Args are type atoms, as in constructor payloads.
type EffLabelExpr struct {
	Name   string
	NameSp source.Span
	Args   []TypeExpr
}

func (r *EffRow) Span() source.Span { return r.Sp }

// TApp is type application, `Maybe Int`. The head is always an uppercase
// name — type variables cannot head applications (no higher kinds, doc/design.md, "Go backend and runtime").
type TApp struct {
	Name   string
	NameSp source.Span
	Args   []TypeExpr // non-empty
}

func (*TName) isTypeExpr()    {}
func (*TVarName) isTypeExpr() {}
func (*TFunExpr) isTypeExpr() {}
func (*TApp) isTypeExpr()     {}

func (t *TName) Span() source.Span    { return t.Sp }
func (t *TVarName) Span() source.Span { return t.Sp }
func (t *TFunExpr) Span() source.Span { return t.Arg.Span().Merge(t.Ret.Span()) }
func (t *TApp) Span() source.Span     { return t.NameSp.Merge(t.Args[len(t.Args)-1].Span()) }

// BinOp stays a distinct node rather than desugaring to App: inference
// special-cases numeric operators, and errors should point at the operator.
type BinOp struct {
	Op     string // "+", "-", "*", "/"
	OpSpan source.Span
	L, R   Expr
}

// Case is `case scrutinee of` followed by branches aligned at the column of
// the first pattern token (layout rule 2, doc/reference.md, "Source layout and names"). Branch bodies are statement
// blocks (doc/design.md, "Language semantics") or inline expressions.
type Case struct {
	Scrutinee Expr
	Branches  []CaseBranch
	Sp        source.Span // the `case` keyword
}

type CaseBranch struct {
	Pattern Pattern
	Body    Expr
}

// Handle is `handle <expr> of` followed by operation clauses aligned at the
// column of the first clause token — layout rule 2, shared with `case`
// (see doc/design.md, "Functions and effects").
type Handle struct {
	Body    Expr
	Clauses []HandleClause
	Return  *ReturnClause // optional `return x -> …`
	Sp      source.Span   // the `handle` keyword
}

// HandleClause is one operation clause, `print s -> …`. Params bind the
// operation's arguments; `resume` is in scope in the body.
type HandleClause struct {
	Op     string
	OpSpan source.Span
	Params []Param
	Body   Expr
}

// ReturnClause is the optional `return x -> …` clause wrapping the handled
// body's normal result. `return` is a *contextual* keyword — it is not
// reserved, so it stays usable as an ordinary name everywhere else.
type ReturnClause struct {
	Param Param
	Body  Expr
	Sp    source.Span // the `return` token
}

// Resume is the one-shot continuation bound inside a handler clause. It
// parses as an expression head, so `resume ()` is ordinary application.
type Resume struct {
	Sp source.Span
}

// Pattern is the surface pattern grammar (doc/reference.md, "Algebraic data types and matching"): variables, wildcard,
// literals, and constructor patterns with nested argument patterns.
type Pattern interface {
	isPattern()
	Span() source.Span
}

type PVar struct {
	Name string
	Sp   source.Span
}

type PWildcard struct {
	Sp source.Span
}

type PInt struct {
	Value int64
	Sp    source.Span
}

type PFloat struct {
	Value float64
	Sp    source.Span
}

type PString struct {
	Value string
	Sp    source.Span
}

type PCtor struct {
	Name     string
	NameSpan source.Span
	Args     []Pattern
}

func (*PVar) isPattern()      {}
func (*PWildcard) isPattern() {}
func (*PInt) isPattern()      {}
func (*PFloat) isPattern()    {}
func (*PString) isPattern()   {}
func (*PCtor) isPattern()     {}

func (p *PVar) Span() source.Span      { return p.Sp }
func (p *PWildcard) Span() source.Span { return p.Sp }
func (p *PInt) Span() source.Span      { return p.Sp }
func (p *PFloat) Span() source.Span    { return p.Sp }
func (p *PString) Span() source.Span   { return p.Sp }
func (p *PCtor) Span() source.Span {
	if len(p.Args) == 0 {
		return p.NameSpan
	}
	return p.NameSpan.Merge(p.Args[len(p.Args)-1].Span())
}

func (*IntLit) isExpr()    {}
func (*FloatLit) isExpr()  {}
func (*StringLit) isExpr() {}
func (*UnitLit) isExpr()   {}
func (*Var) isExpr()       {}
func (*Ctor) isExpr()      {}
func (*App) isExpr()       {}
func (*Neg) isExpr()       {}
func (*BinOp) isExpr()     {}
func (*If) isExpr()        {}
func (*Block) isExpr()     {}
func (*Lambda) isExpr()    {}
func (*Case) isExpr()      {}
func (*Handle) isExpr()    {}
func (*Resume) isExpr()    {}

func (e *IntLit) Span() source.Span    { return e.Sp }
func (e *FloatLit) Span() source.Span  { return e.Sp }
func (e *StringLit) Span() source.Span { return e.Sp }
func (e *UnitLit) Span() source.Span   { return e.Sp }
func (e *Var) Span() source.Span       { return e.Sp }
func (e *Ctor) Span() source.Span      { return e.Sp }
func (e *App) Span() source.Span       { return e.Fn.Span().Merge(e.Arg.Span()) }
func (e *Neg) Span() source.Span       { return e.Sp }
func (e *BinOp) Span() source.Span     { return e.L.Span().Merge(e.R.Span()) }
func (e *If) Span() source.Span        { return e.Sp.Merge(e.Else.Span()) }
func (e *Block) Span() source.Span {
	if len(e.Items) > 0 && e.Items[0].Expr != nil {
		return e.Items[0].Expr.Span().Merge(e.Result.Span())
	}
	return e.Binds[0].NameSpan.Merge(e.Result.Span())
}
func (e *Lambda) Span() source.Span { return e.Sp.Merge(e.Body.Span()) }
func (e *Case) Span() source.Span {
	return e.Sp.Merge(e.Branches[len(e.Branches)-1].Body.Span())
}
func (e *Handle) Span() source.Span {
	if e.Return != nil {
		return e.Sp.Merge(e.Return.Body.Span())
	}
	return e.Sp.Merge(e.Clauses[len(e.Clauses)-1].Body.Span())
}
func (e *Resume) Span() source.Span { return e.Sp }

type Decl interface{ isDecl() }

type ValueDecl struct {
	Name     string
	NameSpan source.Span
	Params   []Param  // non-empty: a function definition (worker; see doc/design.md, "Go backend and runtime")
	Ann      *TypeAnn // nil when unannotated
	Body     Expr
	Native   *NativeBody
}

func (*ValueDecl) isDecl() {}

// TypeDecl is a custom-type declaration (doc/reference.md, "Algebraic data types and matching"). The RHS is always a list of
// constructor alternatives being defined. Params declare polymorphic ADTs.
type TypeDecl struct {
	Name     string
	NameSpan source.Span
	Params   []Param // type parameters (lowercase)
	Ctors    []CtorDef
	Deriving []TName
}

// CtorDef is one constructor alternative. Args are type atoms: named types
// or parenthesized type expressions.
type CtorDef struct {
	Name     string
	NameSpan source.Span
	Args     []TypeExpr
}

func (*TypeDecl) isDecl() {}

// EffectDecl declares an effect and its operations (doc/design.md, "Functions and effects"):
//
//	effect Console
//	    print    : String -> ()
//	    readLine : () -> String
//
// Params are the effect's type parameters (`effect Fail e`), mirroring
// TypeDecl.Params. Operation signatures are ordinary type expressions; the
// checker attaches the effect's own label to the operation's arrow.
type EffectDecl struct {
	Name     string
	NameSpan source.Span
	Params   []Param
	Ops      []OpSig
}

// OpSig is one operation signature line inside an `effect` declaration.
type OpSig struct {
	Name     string
	NameSpan source.Span
	Type     TypeExpr
	Native   *NativeBody
}

// NativeBody marks a declaration implemented outside ordinary fango source.
// Template is nil for sidecar call form and non-nil for bundled inline form.
type NativeBody struct {
	Template *string
	Module   string // sidecar link module, filled by module resolution
	Sp       source.Span
}

// InfixDecl binds one of the parser's fixed binary tokens to a bundled native.
type InfixDecl struct {
	Op, Target         string
	OpSpan, TargetSpan source.Span
}

func (*InfixDecl) isDecl() {}

func (*EffectDecl) isDecl() {}

type ModuleHeader struct {
	Name     string
	NameSpan source.Span
	Exposing Exposing
}

type Exposing struct {
	All   bool
	Items []ExposeItem
}

type ExposeItem struct {
	Name string
	All  bool
	Sp   source.Span
}

type Import struct {
	Module     string
	ModuleSpan source.Span
	Alias      string
	AliasSpan  source.Span
	Exposing   *Exposing
}

type Module struct {
	InstanceImports map[string]map[string]bool
	Header          *ModuleHeader
	Imports         []Import
	Decls           []Decl
}
