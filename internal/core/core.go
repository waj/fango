// Package core is the explicitly-typed IR both backends consume: codegen
// walks it syntax-directedly, and the interpreter (internal/eval) executes
// it. Elaboration guarantees its invariants (lint.go): no metavariables,
// ground types on numeric operators, empty effect rows.
package core

import "github.com/waj/fango/internal/types"

type Prog struct {
	Defs []Def
}

type Def struct {
	Name string
	Type types.Type
	Body Expr
}

type Expr interface {
	isExpr()
	Type() types.Type
}

type IntLit struct {
	Val int64
	Ty  types.Type
}

type FloatLit struct {
	Val float64
	Ty  types.Type
}

type StringLit struct {
	Val string
	Ty  types.Type
}

// BoolLit is permanent, not an interim ADT stand-in: §8.1 special-cases
// Bool in codegen forever (native Go bool), and eval's Value stays bool.
type BoolLit struct {
	Val bool
	Ty  types.Type
}

type Neg struct {
	Operand Expr
	Ty      types.Type // ground Int or Float
}

type If struct {
	Cond, Then, Else Expr
	Ty               types.Type
}

// Print is the builtin cheat (through S6; S7 replaces it with the IO
// effect). Statement-only by construction: its Unit result is neither
// printable nor equatable, so no expression position can contain it.
type Print struct {
	Arg Expr
	Ty  types.Type // always Unit
}

type VarRef struct {
	Name   string
	Ty     types.Type
	TyArgs []types.Type // explicit instantiation; empty until S5
}

type BinOp struct {
	Op   string
	Ty   types.Type // ground result type: the operator compiles natively
	L, R Expr
}

// CalleeKind classifies application spines after saturation analysis
// (DESIGN.md §8.7). Declared in S0 so the shape is frozen; the first
// producer is elaboration of function applications in S3.
type CalleeKind int

const (
	Worker CalleeKind = iota // saturated call to a known top-level worker
	Ctor                     // constructor application (S4)
	Value                    // typed indirect call through a function value
)

type App struct {
	CalleeKind CalleeKind
	Callee     Expr
	Args       []Expr
	TyArgs     []types.Type
	Ty         types.Type
}

func (*IntLit) isExpr()    {}
func (*FloatLit) isExpr()  {}
func (*StringLit) isExpr() {}
func (*BoolLit) isExpr()   {}
func (*VarRef) isExpr()    {}
func (*Neg) isExpr()       {}
func (*BinOp) isExpr()     {}
func (*If) isExpr()        {}
func (*Print) isExpr()     {}
func (*App) isExpr()       {}

func (e *IntLit) Type() types.Type    { return e.Ty }
func (e *FloatLit) Type() types.Type  { return e.Ty }
func (e *StringLit) Type() types.Type { return e.Ty }
func (e *BoolLit) Type() types.Type   { return e.Ty }
func (e *VarRef) Type() types.Type    { return e.Ty }
func (e *Neg) Type() types.Type       { return e.Ty }
func (e *BinOp) Type() types.Type     { return e.Ty }
func (e *If) Type() types.Type        { return e.Ty }
func (e *Print) Type() types.Type     { return e.Ty }
func (e *App) Type() types.Type       { return e.Ty }
