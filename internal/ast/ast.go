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

type Var struct {
	Name string
	Sp   source.Span
}

// BinOp stays a distinct node rather than desugaring to App: inference
// special-cases numeric operators, and errors should point at the operator.
type BinOp struct {
	Op     string // "+", "-", "*", "/"
	OpSpan source.Span
	L, R   Expr
}

func (*IntLit) isExpr() {}
func (*Var) isExpr()    {}
func (*BinOp) isExpr()  {}

func (e *IntLit) Span() source.Span { return e.Sp }
func (e *Var) Span() source.Span    { return e.Sp }
func (e *BinOp) Span() source.Span  { return e.L.Span().Merge(e.R.Span()) }

type Decl interface{ isDecl() }

type ValueDecl struct {
	Name     string
	NameSpan source.Span
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
