// Package core is the explicitly-typed IR both backends consume: codegen
// walks it syntax-directedly, and the interpreter (internal/eval) executes
// it. Elaboration guarantees its invariants (lint.go): no metavariables,
// ground types on numeric operators, empty effect rows.
package core

import "github.com/waj/fango/internal/types"

type Prog struct {
	// ADTs lists declared types in declaration order — codegen emits marker
	// interfaces, constructor structs, and derived eq/show from it. Bool is
	// absent (native Go bool forever, doc/design.md, "Go backend and runtime").
	ADTs    []*types.ADTInfo
	Effects []*types.EffectInfo
	Defs    []Def
	Natives map[string]*types.NativeInfo
	// Entry selects the entry module's main definition by canonical symbol.
	Entry        string
	EntryDisplay Expr // optional, pure String observation used by tests and tooling
}

type EffectInstance struct {
	Unique int
	Name   string
	Args   []types.Type
}

type Def struct {
	Name  string
	Owner string     // defining source module; empty for headerless files and REPL inputs
	Type  types.Type // the full curried fango type

	// TyParams are the definition's quantified type variables (rigid, first
	// occurrence order in Type) — Go type parameters at codegen. Non-empty
	// TyParams with empty Params is a nullary generic worker (doc/design.md, "Go backend and runtime"): emitted
	// as a function, re-evaluated per use.
	TyParams []*types.TVar

	Params       []string // non-empty ⇒ worker (doc/design.md, "Go backend and runtime"); uncurried Go signature = peeling len(Params) arrows off Type
	EffectParams []EffectInstance
	Body         Expr
}

// IsWorker reports whether the definition emits as a function: it has term
// parameters, or it is a polymorphic value (nullary generic worker, doc/design.md, "Go backend and runtime").
func (d *Def) IsWorker() bool { return len(d.Params) > 0 || len(d.TyParams) > 0 }

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

type CharLit struct {
	Val rune
	Ty  types.Type
}

type UnitLit struct{ Ty types.Type }

// BoolLit is permanent, not an interim ADT stand-in: doc/design.md, "Go backend and runtime" special-cases
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

type Perform struct {
	Op     *types.EffectOp
	Effect EffectInstance
	Args   []Expr
	Ty     types.Type
}
type HandlerClause struct {
	Op         *types.EffectOp
	Params     []string
	ParamTypes []types.Type
	ResultType types.Type
	Body       Expr
}
type ReturnClause struct {
	Param string
	Body  Expr
}
type Handle struct {
	Body           Expr
	Effect         EffectInstance
	Clauses        []HandlerClause
	Return         *ReturnClause
	TailResumptive bool
	Ty             types.Type
}
type Resume struct {
	Value Expr
	Ty    types.Type
}
type Seq struct {
	First, Then Expr
	Ty          types.Type
}

// Let is one block binding (doc/design.md, "Language semantics"): bind Name to Rhs, continue with Body.
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
	Local  bool // resolves a lexical binding even if a later global shares its spelling
	Ty     types.Type
	TyArgs []types.Type // explicit generic instantiation; empty when monomorphic
}

type BinOp struct {
	Op   string
	Ty   types.Type // ground result type: the operator compiles natively
	L, R Expr
}

// Quote builds a compile-time-only code value: a template index into the
// compilation's quote table plus one expression per hole, evaluated eagerly
// in source order. Only the interpreter ever executes one, and only while
// the compiler is running a splice — a Quote reaching the Core linter means
// a compile-time-only value leaked into emitted code.
type Quote struct {
	Template int
	Holes    []Expr
	Ty       types.Type
}

// NativeCall is a saturated call to a declaration-backed primitive.
type NativeCall struct {
	Name   string
	Module string
	Args   []Expr
	Ty     types.Type
}

// CalleeKind classifies application spines after saturation analysis
// (see doc/design.md, "Core and evidence invariants"). Elaboration of
// function applications produces this distinction.
type CalleeKind int

const (
	Worker CalleeKind = iota // saturated call to a known top-level worker
	Ctor                     // constructor application
	Value                    // typed indirect call through a function value
)

type App struct {
	CalleeKind   CalleeKind
	Callee       Expr
	Args         []Expr
	TyArgs       []types.Type
	Ty           types.Type
	EvidenceArgs []EffectInstance

	// Ctor identifies the constructor when CalleeKind == Ctor (always
	// saturated: len(Args) == len(Ctor.Fields); partial applications were
	// eta-expanded like workers, doc/design.md, "Go backend and runtime" item 5).
	Ctor *types.CtorInfo
}

// Case evaluates Scrut once, binds it to Bind, and descends the decision
// tree (doc/design.md, "Core and evidence invariants"). Elaboration compiled the branches: each scrutinee position
// is examined once, pattern variables became Lets inside the leaves.
type Case struct {
	Scrut Expr
	Bind  string
	Tree  Tree
	Ty    types.Type
}

// Tree is a decision-tree node: Maranget-compiled pattern matching.
type Tree interface{ isTree() }

type Guard struct {
	Cond       Expr
	Then, Else Tree
}
type Unreachable struct{}

func (*Guard) isTree()       {}
func (*Unreachable) isTree() {}

// Leaf runs one branch body.
type Leaf struct {
	Body Expr
}

// SwitchCtor discriminates on the constructor held in the variable Scrut.
// Cases appear in constructor-declaration order. Default is non-nil only
// when Cases doesn't cover the ADT (a variable or wildcard took the rest);
// with full coverage codegen turns the LAST case into Go's `default:`.
type SwitchCtor struct {
	Scrut   string
	ADT     *types.ADTInfo
	Cases   []CtorCase
	Default Tree
}

type CtorCase struct {
	Ctor  *types.CtorInfo
	Binds []string // one per field; "" = unused in the subtree
	Tree  Tree
}

// SwitchLit discriminates on literal equality (Int, Float, String, or Char
// scrutinee). Default is always non-nil: literals never exhaust a type.
type SwitchLit struct {
	Scrut   string
	Cases   []LitCase
	Default Tree
}

type LitCase struct {
	Lit  Expr // *IntLit, *FloatLit, *StringLit, or *CharLit
	Tree Tree
}

func (*Leaf) isTree()       {}
func (*SwitchCtor) isTree() {}
func (*SwitchLit) isTree()  {}

func (*IntLit) isExpr()     {}
func (*FloatLit) isExpr()   {}
func (*StringLit) isExpr()  {}
func (*CharLit) isExpr()    {}
func (*UnitLit) isExpr()    {}
func (*BoolLit) isExpr()    {}
func (*VarRef) isExpr()     {}
func (*Neg) isExpr()        {}
func (*BinOp) isExpr()      {}
func (*NativeCall) isExpr() {}
func (*Quote) isExpr()      {}
func (*If) isExpr()         {}
func (*Perform) isExpr()    {}
func (*Handle) isExpr()     {}
func (*Resume) isExpr()     {}
func (*Seq) isExpr()        {}
func (*Let) isExpr()        {}
func (*Lambda) isExpr()     {}
func (*App) isExpr()        {}
func (*Case) isExpr()       {}

func (e *IntLit) Type() types.Type     { return e.Ty }
func (e *FloatLit) Type() types.Type   { return e.Ty }
func (e *StringLit) Type() types.Type  { return e.Ty }
func (e *CharLit) Type() types.Type    { return e.Ty }
func (e *UnitLit) Type() types.Type    { return e.Ty }
func (e *BoolLit) Type() types.Type    { return e.Ty }
func (e *VarRef) Type() types.Type     { return e.Ty }
func (e *Neg) Type() types.Type        { return e.Ty }
func (e *BinOp) Type() types.Type      { return e.Ty }
func (e *NativeCall) Type() types.Type { return e.Ty }
func (e *Quote) Type() types.Type      { return e.Ty }
func (e *If) Type() types.Type         { return e.Ty }
func (e *Perform) Type() types.Type    { return e.Ty }
func (e *Handle) Type() types.Type     { return e.Ty }
func (e *Resume) Type() types.Type     { return e.Ty }
func (e *Seq) Type() types.Type        { return e.Ty }
func (e *Let) Type() types.Type        { return e.Ty }
func (e *Lambda) Type() types.Type     { return e.Ty }
func (e *App) Type() types.Type        { return e.Ty }
func (e *Case) Type() types.Type       { return e.Ty }

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
	case *NativeCall:
		for _, a := range e.Args {
			if Mentions(a, name) {
				return true
			}
		}
		return false
	case *Quote:
		for _, h := range e.Holes {
			if Mentions(h, name) {
				return true
			}
		}
		return false
	case *If:
		return Mentions(e.Cond, name) || Mentions(e.Then, name) || Mentions(e.Else, name)
	case *Perform:
		for _, a := range e.Args {
			if Mentions(a, name) {
				return true
			}
		}
		return false
	case *Handle:
		if Mentions(e.Body, name) {
			return true
		}
		for _, c := range e.Clauses {
			if Mentions(c.Body, name) {
				return true
			}
		}
		return e.Return != nil && Mentions(e.Return.Body, name)
	case *Resume:
		return Mentions(e.Value, name)
	case *Seq:
		return Mentions(e.First, name) || Mentions(e.Then, name)
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
	case *Case:
		return Mentions(e.Scrut, name) || TreeMentions(e.Tree, name)
	default:
		return false
	}
}

// TreeMentions reports whether name occurs in a decision tree — as a tested
// scrutinee variable or anywhere in a leaf body. Codegen uses it to skip
// emitting unused field binders (Go rejects unused locals).
func TreeMentions(t Tree, name string) bool {
	switch t := t.(type) {
	case *Guard:
		return Mentions(t.Cond, name) || TreeMentions(t.Then, name) || TreeMentions(t.Else, name)
	case *Leaf:
		return Mentions(t.Body, name)
	case *SwitchCtor:
		if t.Scrut == name {
			return true
		}
		for _, c := range t.Cases {
			if TreeMentions(c.Tree, name) {
				return true
			}
		}
		return t.Default != nil && TreeMentions(t.Default, name)
	case *SwitchLit:
		if t.Scrut == name {
			return true
		}
		for _, c := range t.Cases {
			if TreeMentions(c.Tree, name) {
				return true
			}
		}
		return TreeMentions(t.Default, name)
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
