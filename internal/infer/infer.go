// Package infer is the constraint-based type checker: constraint generation
// (constrain.go), unification (unify.go), and solving (solve.go). The
// explicit constraint list — rather than Algorithm W's inline unification —
// is what buys good errors now and typeclasses later.
package infer

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// Why says why two types had to match, so failures point at the right span
// with the right story. More kinds arrive with their features (IfCondition,
// CaseBranches, CallArg{N}, Annotation, …).
type WhyKind int

const (
	WhyOperand  WhyKind = iota // operands of a numeric operator must agree
	WhyDeclBody                // a declaration body must match its (future) annotation
)

type Why struct {
	Kind WhyKind
	Op   string // operator text for WhyOperand
}

type Constraint struct {
	Left, Right types.Type
	Span        source.Span
	Why         Why
}

// Env maps names to schemes. S0 has a single flat scope (top level); local
// scopes arrive with let (S2) and lambdas (S3).
type Env struct {
	vars map[string]types.Scheme
}

func NewEnv() *Env { return &Env{vars: map[string]types.Scheme{}} }

func (e *Env) Lookup(name string) (types.Scheme, bool) {
	s, ok := e.vars[name]
	return s, ok
}

func (e *Env) Bind(name string, s types.Scheme) { e.vars[name] = s }
func (e *Env) Has(name string) bool             { _, ok := e.vars[name]; return ok }

// Checker carries the session-scoped inference state: the fresh-variable
// supply, the accumulated substitution, and per-node solved types. The REPL
// keeps one Checker across many inputs; batch compilation uses one per run.
type Checker struct {
	Sup       *types.Supply
	B         *types.Builtins
	Env       *Env
	Sub       Subst
	ExprTypes map[ast.Expr]types.Type
}

func NewChecker(sup *types.Supply, b *types.Builtins, env *Env) *Checker {
	return &Checker{
		Sup:       sup,
		B:         b,
		Env:       env,
		Sub:       Subst{},
		ExprTypes: map[ast.Expr]types.Type{},
	}
}

type DeclInfo struct {
	Name     string
	NameSpan source.Span
	Type     types.Type // solved but not zonked; apply ck.Sub for the final type
	Body     ast.Expr
}

// Module checks declarations in source order — solve-at-definition, the
// same call structure generalization will use from S2.
func (ck *Checker) Module(m *ast.Module) ([]DeclInfo, []diag.Error) {
	var infos []DeclInfo
	var errs []diag.Error
	for _, d := range m.Decls {
		vd := d.(*ast.ValueDecl)
		// Duplicate definitions are a batch-compilation error only: the
		// REPL redefines names freely (generational cells).
		if ck.Env.Has(vd.Name) {
			errs = append(errs, diag.Errorf(vd.NameSpan, "MULTIPLE DEFINITIONS",
				"`%s` is defined more than once.", vd.Name))
		}
		info, declErrs := ck.Decl(vd)
		errs = append(errs, declErrs...)
		infos = append(infos, info)
	}
	return infos, errs
}

// Decl checks one value declaration and binds it in the environment —
// rebinding an existing name is allowed (the REPL's redefinition path).
func (ck *Checker) Decl(d *ast.ValueDecl) (DeclInfo, []diag.Error) {
	var errs []diag.Error
	ty, exprErrs := ck.Expr(d.Body)
	errs = append(errs, exprErrs...)
	ck.Env.Bind(d.Name, types.Scheme{Body: ty})
	return DeclInfo{Name: d.Name, NameSpan: d.NameSpan, Type: ty, Body: d.Body}, errs
}

// Expr generates constraints for one expression and solves them into the
// checker's substitution. Also the REPL's entry point for expressions.
func (ck *Checker) Expr(e ast.Expr) (types.Type, []diag.Error) {
	g := &generator{ck: ck}
	ty := g.expr(e)
	var preds []types.Pred // the typeclass seam: always empty in the MVP
	sub, residual, solveErrs := Solve(g.cs, preds, ck.Sub, ck.B)
	ck.Sub = sub
	_ = residual // no typeclasses: nothing defers residual predicates yet
	return ty, append(g.errs, solveErrs...)
}

type generator struct {
	ck   *Checker
	cs   []Constraint
	errs []diag.Error
}

func (g *generator) expr(e ast.Expr) types.Type {
	var ty types.Type
	switch e := e.(type) {
	case *ast.IntLit:
		// Elm's rule: an integer literal is `number` (Int or Float).
		// Unconstrained numbers default to Int during elaboration.
		ty = g.ck.Sup.FreshVar(types.Number)
	case *ast.Var:
		scheme, ok := g.ck.Env.Lookup(e.Name)
		if !ok {
			g.errs = append(g.errs, diag.Errorf(e.Sp, "NAMING ERROR",
				"I don't know a value named `%s`.", e.Name))
			ty = g.ck.Sup.FreshVar(types.General) // recover with a hole
			break
		}
		ty = g.instantiate(scheme)
	case *ast.BinOp:
		lt := g.expr(e.L)
		rt := g.expr(e.R)
		n := g.ck.Sup.FreshVar(types.Number)
		why := Why{Kind: WhyOperand, Op: e.Op}
		g.cs = append(g.cs,
			Constraint{Left: lt, Right: n, Span: e.L.Span(), Why: why},
			Constraint{Left: rt, Right: n, Span: e.R.Span(), Why: why})
		ty = n
	default:
		panic("infer: unhandled expression node")
	}
	g.ck.ExprTypes[e] = ty
	return ty
}

func (g *generator) instantiate(s types.Scheme) types.Type {
	if s.NumVars == 0 {
		return s.Body
	}
	panic("infer: polymorphic instantiation arrives in S5")
}
