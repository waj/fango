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
	Name   string
	Type   types.Type // the full curried fango type
	Params []string   // non-empty ⇒ worker (§8.2); uncurried Go signature = peeling len(Params) arrows off Type
	Body   Expr
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

// Let is one block binding (§3.6): bind Name to Rhs, continue with Body.
// Elaboration folds a Block's bindings into a right-nested Let chain;
// bindings evaluate eagerly in order in both backends. Rec marks a
// self-recursive local function (Rhs must be a Lambda; codegen emits the
// declare-then-assign idiom, eval ties the frame cycle).
type Let struct {
	Name string
	Rhs  Expr
	Body Expr
	Rec  bool
	Ty   types.Type // == Body.Type(), linted
}

// Lambda is one currying step: exactly one parameter, mirroring the
// compositional type mapping T⟦a->b⟧ = func(A) B — one Go func literal,
// one interpreter closure. Multi-parameter surface lambdas nest.
type Lambda struct {
	Param string
	Body  Expr
	Ty    types.Type // a TFun; Ty.Ret == Body type
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
func (*Let) isExpr()       {}
func (*Lambda) isExpr()    {}
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
func (e *Let) Type() types.Type       { return e.Ty }
func (e *Lambda) Type() types.Type    { return e.Ty }
func (e *App) Type() types.Type       { return e.Ty }

// Mentions reports whether name occurs in e. No-shadowing makes a plain
// occurrence check exact: nothing inside e can rebind name. Used by the
// elaborator (Rec detection) and codegen (unused-binding keep-alives).
func Mentions(e Expr, name string) bool {
	switch e := e.(type) {
	case *VarRef:
		return e.Name == name
	case *Neg:
		return Mentions(e.Operand, name)
	case *BinOp:
		return Mentions(e.L, name) || Mentions(e.R, name)
	case *If:
		return Mentions(e.Cond, name) || Mentions(e.Then, name) || Mentions(e.Else, name)
	case *Print:
		return Mentions(e.Arg, name)
	case *Let:
		return Mentions(e.Rhs, name) || Mentions(e.Body, name)
	case *Lambda:
		return Mentions(e.Body, name)
	case *App:
		if Mentions(e.Callee, name) {
			return true
		}
		for _, a := range e.Args {
			if Mentions(a, name) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// PeelFun splits n arrows off a curried type, returning the argument types
// and the remainder. Panics if t has fewer arrows — a linted invariant.
func PeelFun(t types.Type, n int) ([]types.Type, types.Type) {
	args := make([]types.Type, 0, n)
	for range n {
		fn, ok := t.(*types.TFun)
		if !ok {
			panic("core.PeelFun: not enough arrows — arity out of sync with type")
		}
		args = append(args, fn.Arg)
		t = fn.Ret
	}
	return args, t
}
