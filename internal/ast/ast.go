// Package ast is the surface syntax tree: sealed interfaces with marker
// methods, every node carrying a Span. Nodes for later slices (lambdas,
// let, case, type decls) are added when their features land.
package ast

import "github.com/waj/fango/internal/source"

type Expr interface {
	isExpr()
	Span() source.Span
}

type IntLit struct {
	Value int64
	Sp    source.Span
}

type FloatLit struct {
	Value float64
	Sp    source.Span
}

type StringLit struct {
	Value string
	Sp    source.Span
}

type Var struct {
	Name string
	Sp   source.Span
}

// Ctor is a constructor reference. Until `type` declarations land (S4) the
// only constructors are the builtin True and False.
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

// Block is a statement-style body (§3.6): binding lines followed by one
// result expression. The parser collapses zero-binding blocks to the plain
// result expression, so a Block always has at least one binding.
type Block struct {
	Binds  []LocalBind
	Result Expr
}

type LocalBind struct {
	Name     string
	NameSpan source.Span
	Ann      *TypeAnn // nil when unannotated
	Body     Expr
}

// TypeAnn is a `name : Type` annotation line attached to the definition
// directly below it.
type TypeAnn struct {
	Type TypeExpr
	Sp   source.Span // colon through the end of the type
}

// TypeExpr is the surface type grammar: ground names, `()`, `->` arrows
// (right-associative), and type variables (parsed now, rejected until
// polymorphism lands in S5).
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

type TFunExpr struct {
	Arg, Ret TypeExpr
}

func (*TName) isTypeExpr()    {}
func (*TVarName) isTypeExpr() {}
func (*TFunExpr) isTypeExpr() {}

func (t *TName) Span() source.Span    { return t.Sp }
func (t *TVarName) Span() source.Span { return t.Sp }
func (t *TFunExpr) Span() source.Span { return t.Arg.Span().Merge(t.Ret.Span()) }

// BinOp stays a distinct node rather than desugaring to App: inference
// special-cases numeric operators, and errors should point at the operator.
type BinOp struct {
	Op     string // "+", "-", "*", "/"
	OpSpan source.Span
	L, R   Expr
}

func (*IntLit) isExpr()    {}
func (*FloatLit) isExpr()  {}
func (*StringLit) isExpr() {}
func (*Var) isExpr()       {}
func (*Ctor) isExpr()      {}
func (*App) isExpr()       {}
func (*Neg) isExpr()       {}
func (*BinOp) isExpr()     {}
func (*If) isExpr()        {}
func (*Block) isExpr()     {}

func (e *IntLit) Span() source.Span    { return e.Sp }
func (e *FloatLit) Span() source.Span  { return e.Sp }
func (e *StringLit) Span() source.Span { return e.Sp }
func (e *Var) Span() source.Span       { return e.Sp }
func (e *Ctor) Span() source.Span      { return e.Sp }
func (e *App) Span() source.Span       { return e.Fn.Span().Merge(e.Arg.Span()) }
func (e *Neg) Span() source.Span       { return e.Sp }
func (e *BinOp) Span() source.Span     { return e.L.Span().Merge(e.R.Span()) }
func (e *If) Span() source.Span        { return e.Sp.Merge(e.Else.Span()) }
func (e *Block) Span() source.Span     { return e.Binds[0].NameSpan.Merge(e.Result.Span()) }

type Decl interface{ isDecl() }

type ValueDecl struct {
	Name     string
	NameSpan source.Span
	Ann      *TypeAnn // nil when unannotated
	Body     Expr
}

func (*ValueDecl) isDecl() {}

// ModuleHeader is parsed and ignored until the module system is designed.
type ModuleHeader struct {
	Name     string
	Exposing []string
}

type Module struct {
	Header *ModuleHeader
	Decls  []Decl
}
