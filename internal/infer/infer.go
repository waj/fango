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
	WhyOperand     WhyKind = iota // operands of a numeric operator must agree
	WhyDeclBody                   // a declaration body must match its (future) annotation
	WhyCall                       // a callee must be a function accepting the argument
	WhyIfCondition                // an if condition must be Bool
	WhyIfBranches                 // then/else branches must agree
	WhyCompare                    // both sides of a comparison must agree
	WhyNegate                     // a negated operand must be a number
	WhyOpRequires                 // an operator fixes its operand type (/, ++)
	WhyAnnotation                 // a definition must match its type annotation
)

type Why struct {
	Kind WhyKind
	Op   string // operator text for operator-related kinds
	Want string // required type name for WhyOpRequires
	Name string // annotated name for WhyAnnotation
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

	// Ctors is the constructor table's embryo (§7.2): S4 replaces the value
	// types with real schemes from `type` declarations; until then only the
	// builtin Bool constructors exist.
	Ctors map[string]types.Type

	// TypeNames maps surface type names to their current types — the type
	// table's embryo, exactly as Ctors is for constructors. `type`
	// declarations (S4) and REPL generations extend it; identity stays the
	// TCon Unique underneath.
	TypeNames map[string]types.Type

	// PrintCalls marks App nodes recognized as the print builtin cheat, so
	// elaboration classifies them identically (one source of truth).
	PrintCalls map[*ast.App]bool
}

func NewChecker(sup *types.Supply, b *types.Builtins, env *Env) *Checker {
	return &Checker{
		Sup:       sup,
		B:         b,
		Env:       env,
		Sub:       Subst{},
		ExprTypes: map[ast.Expr]types.Type{},
		Ctors: map[string]types.Type{
			"True":  b.Bool,
			"False": b.Bool,
		},
		TypeNames: map[string]types.Type{
			"Int":    b.Int,
			"Float":  b.Float,
			"String": b.String,
			"Bool":   b.Bool,
			"()":     b.Unit,
		},
		PrintCalls: map[*ast.App]bool{},
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
// Only main's body may use the print cheat (§10.5's top-level purity,
// enforced ad hoc until effects land in S7).
func (ck *Checker) Decl(d *ast.ValueDecl) (DeclInfo, []diag.Error) {
	info, errs := ck.DeclWhere(d, d.Name == "main")
	ck.Env.Bind(d.Name, types.Scheme{Body: info.Type})
	return info, errs
}

// DeclWhere checks one declaration — annotation resolution, body inference,
// and the annotation constraint — without binding it, so callers control
// whether a failed definition enters the environment (the REPL does not
// bind on error).
func (ck *Checker) DeclWhere(d *ast.ValueDecl, allowPrint bool) (DeclInfo, []diag.Error) {
	var errs []diag.Error
	ty, exprErrs := ck.ExprWhere(d.Body, allowPrint)
	errs = append(errs, exprErrs...)
	if d.Ann != nil {
		annTy, annErrs := ck.ResolveTypeExpr(d.Ann.Type)
		errs = append(errs, annErrs...)
		if annTy != nil {
			// S5: skolemize quantified annotation variables here before
			// unifying (skolemize-and-unify checking, §7.2).
			c := Constraint{Left: annTy, Right: ty, Span: d.Body.Span(),
				Why: Why{Kind: WhyAnnotation, Name: d.Name}}
			sub, _, solveErrs := Solve([]Constraint{c}, nil, ck.Sub, ck.B)
			ck.Sub = sub
			errs = append(errs, solveErrs...)
			ty = annTy
		}
	}
	return DeclInfo{Name: d.Name, NameSpan: d.NameSpan, Type: ty, Body: d.Body}, errs
}

// Expr checks an expression with prompt semantics (print allowed) — the
// REPL's expression entry point.
func (ck *Checker) Expr(e ast.Expr) (types.Type, []diag.Error) {
	return ck.ExprWhere(e, true)
}

// ExprWhere generates constraints for one expression and solves them into
// the checker's substitution. allowPrint gates the print builtin cheat.
func (ck *Checker) ExprWhere(e ast.Expr, allowPrint bool) (types.Type, []diag.Error) {
	g := &generator{ck: ck, allowPrint: allowPrint}
	ty := g.expr(e)
	var preds []types.Pred // the typeclass seam: always empty in the MVP
	sub, residual, solveErrs := Solve(g.cs, preds, ck.Sub, ck.B)
	ck.Sub = sub
	_ = residual // no typeclasses: nothing defers residual predicates yet
	return ty, append(g.errs, solveErrs...)
}

type generator struct {
	ck         *Checker
	allowPrint bool
	locals     *blockScope
	cs         []Constraint
	errs       []diag.Error
}

// blockScope is a block's local bindings. The parent pointer is S3
// readiness (nested function bodies); S2 depth never exceeds one.
type blockScope struct {
	parent *blockScope
	names  map[string]types.Type
}

func (s *blockScope) lookup(name string) (types.Type, bool) {
	for ; s != nil; s = s.parent {
		if t, ok := s.names[name]; ok {
			return t, true
		}
	}
	return nil, false
}

// isPrintCheat reports whether a Var is the print builtin: the name
// `print`, not shadowed by a user binding (top-level or block-local).
func (g *generator) isPrintCheat(e ast.Expr) bool {
	v, ok := e.(*ast.Var)
	if !ok || v.Name != "print" || g.ck.Env.Has("print") {
		return false
	}
	_, local := g.locals.lookup("print")
	return !local
}

func (g *generator) expr(e ast.Expr) types.Type {
	var ty types.Type
	switch e := e.(type) {
	case *ast.IntLit:
		// Elm's rule: an integer literal is `number` (Int or Float).
		// Unconstrained numbers default to Int during elaboration.
		ty = g.ck.Sup.FreshVar(types.Number)
	case *ast.FloatLit:
		ty = g.ck.B.Float
	case *ast.StringLit:
		ty = g.ck.B.String
	case *ast.Var:
		if g.isPrintCheat(e) {
			g.errs = append(g.errs, diag.Errorf(e.Sp, "PRINT NEEDS AN ARGUMENT",
				"`print` is a builtin that must be applied to exactly one\nargument, like `print (1 + 2)`. Using it as a value arrives with\neffects (S7)."))
			ty = g.ck.Sup.FreshVar(types.General)
			break
		}
		if localTy, ok := g.locals.lookup(e.Name); ok {
			ty = localTy
			break
		}
		scheme, ok := g.ck.Env.Lookup(e.Name)
		if !ok {
			g.errs = append(g.errs, diag.Errorf(e.Sp, "NAMING ERROR",
				"I don't know a value named `%s`.", e.Name))
			ty = g.ck.Sup.FreshVar(types.General) // recover with a hole
			break
		}
		ty = g.instantiate(scheme)
	case *ast.Ctor:
		ctorTy, ok := g.ck.Ctors[e.Name]
		if !ok {
			g.errs = append(g.errs, diag.Errorf(e.Sp, "NAMING ERROR",
				"I don't know a constructor named `%s` — custom types arrive\nwith `type` declarations (S4).", e.Name))
			ty = g.ck.Sup.FreshVar(types.General)
			break
		}
		ty = ctorTy
	case *ast.App:
		if g.isPrintCheat(e.Fn) {
			if !g.allowPrint {
				g.errs = append(g.errs, diag.Errorf(e.Fn.Span(), "PRINT NOT ALLOWED HERE",
					"For now, only `main` can print — `print` inside other\ndefinitions arrives with effects (S7)."))
			}
			g.expr(e.Arg) // type the argument; showability checked post-defaulting
			g.ck.PrintCalls[e] = true
			ty = g.ck.B.Unit
			break
		}
		fnTy := g.expr(e.Fn)
		argTy := g.expr(e.Arg)
		r := g.ck.Sup.FreshVar(types.General)
		g.cs = append(g.cs, Constraint{
			Left:  fnTy,
			Right: &types.TFun{Arg: argTy, Ret: r},
			Span:  e.Fn.Span(),
			Why:   Why{Kind: WhyCall},
		})
		ty = r
	case *ast.Neg:
		opTy := g.expr(e.Operand)
		n := g.ck.Sup.FreshVar(types.Number)
		g.cs = append(g.cs, Constraint{
			Left: opTy, Right: n, Span: e.Operand.Span(), Why: Why{Kind: WhyNegate},
		})
		ty = n
	case *ast.If:
		condTy := g.expr(e.Cond)
		g.cs = append(g.cs, Constraint{
			Left: condTy, Right: g.ck.B.Bool, Span: e.Cond.Span(), Why: Why{Kind: WhyIfCondition},
		})
		thenTy := g.expr(e.Then)
		elseTy := g.expr(e.Else)
		g.cs = append(g.cs, Constraint{
			Left: elseTy, Right: thenTy, Span: e.Else.Span(), Why: Why{Kind: WhyIfBranches},
		})
		ty = thenTy
	case *ast.BinOp:
		ty = g.binOp(e)
	case *ast.Block:
		ty = g.block(e)
	default:
		panic("infer: unhandled expression node")
	}
	g.ck.ExprTypes[e] = ty
	return ty
}

// block checks a statement body: each binding is a solve-at-binding point
// in principle (monomorphic until S5), scoped sequentially, with shadowing
// forbidden against both earlier bindings and the top level.
func (g *generator) block(e *ast.Block) types.Type {
	g.locals = &blockScope{parent: g.locals, names: map[string]types.Type{}}
	defer func() { g.locals = g.locals.parent }()

	for i := range e.Binds {
		bind := &e.Binds[i]
		if _, dup := g.locals.lookup(bind.Name); dup || g.ck.Env.Has(bind.Name) {
			where := "at the top level"
			if dup {
				where = "earlier in this block"
			}
			g.errs = append(g.errs, diag.Errorf(bind.NameSpan, "SHADOWING",
				"The name `%s` is already defined %s — fango does not allow\nshadowing. Choose a different name.", bind.Name, where))
		}
		ty := g.expr(bind.Body)
		if bind.Ann != nil {
			annTy, annErrs := g.ck.ResolveTypeExpr(bind.Ann.Type)
			g.errs = append(g.errs, annErrs...)
			if annTy != nil {
				// S5: skolemize here, as at the top level.
				g.cs = append(g.cs, Constraint{Left: annTy, Right: ty,
					Span: bind.Body.Span(), Why: Why{Kind: WhyAnnotation, Name: bind.Name}})
				ty = annTy
			}
		}
		g.locals.names[bind.Name] = ty
	}
	return g.expr(e.Result)
}

func (g *generator) binOp(e *ast.BinOp) types.Type {
	lt := g.expr(e.L)
	rt := g.expr(e.R)
	switch e.Op {
	case "+", "-", "*":
		n := g.ck.Sup.FreshVar(types.Number)
		why := Why{Kind: WhyOperand, Op: e.Op}
		g.cs = append(g.cs,
			Constraint{Left: lt, Right: n, Span: e.L.Span(), Why: why},
			Constraint{Left: rt, Right: n, Span: e.R.Span(), Why: why})
		return n
	case "/":
		why := Why{Kind: WhyOpRequires, Op: e.Op, Want: "Float"}
		g.cs = append(g.cs,
			Constraint{Left: lt, Right: g.ck.B.Float, Span: e.L.Span(), Why: why},
			Constraint{Left: rt, Right: g.ck.B.Float, Span: e.R.Span(), Why: why})
		return g.ck.B.Float
	case "++":
		why := Why{Kind: WhyOpRequires, Op: e.Op, Want: "String"}
		g.cs = append(g.cs,
			Constraint{Left: lt, Right: g.ck.B.String, Span: e.L.Span(), Why: why},
			Constraint{Left: rt, Right: g.ck.B.String, Span: e.R.Span(), Why: why})
		return g.ck.B.String
	case "==", "/=", "<", ">", "<=", ">=":
		// Operands must agree; the equatable/orderable check happens
		// post-defaulting in elaborate, where the type is ground.
		a := g.ck.Sup.FreshVar(types.General)
		why := Why{Kind: WhyCompare, Op: e.Op}
		g.cs = append(g.cs,
			Constraint{Left: lt, Right: a, Span: e.L.Span(), Why: why},
			Constraint{Left: rt, Right: a, Span: e.R.Span(), Why: why})
		return g.ck.B.Bool
	default:
		panic("infer: unhandled operator " + e.Op)
	}
}

func (g *generator) instantiate(s types.Scheme) types.Type {
	if s.NumVars == 0 {
		return s.Body
	}
	panic("infer: polymorphic instantiation arrives in S5")
}
