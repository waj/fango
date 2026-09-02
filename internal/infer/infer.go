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
	WhyOperand      WhyKind = iota // operands of a numeric operator must agree
	WhyDeclBody                    // a declaration body must match its (future) annotation
	WhyCall                        // a callee must be a function accepting the argument
	WhyIfCondition                 // an if condition must be Bool
	WhyIfBranches                  // then/else branches must agree
	WhyCompare                     // both sides of a comparison must agree
	WhyNegate                      // a negated operand must be a number
	WhyOpRequires                  // an operator fixes its operand type (/, ++)
	WhyAnnotation                  // a definition must match its type annotation
	WhyRecursion                   // recursive uses must match the definition
	WhyPattern                     // a pattern must match the scrutinee's type
	WhyCaseBranches                // all case branches must produce the same type
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

	// Ctors is the constructor table (§7.2), keyed by constructor name —
	// names are unique per module (types and constructors live in separate
	// namespaces, §3.7). Seeded with the builtin Bool constructors.
	Ctors map[string]*types.CtorInfo

	// ADTs maps a declared type's Unique to its constructor-table entry;
	// ADTOrder keeps declaration order for deterministic codegen. Bool is
	// predefined as an ordinary ADT (codegen special-cases it, §8.1) and is
	// deliberately absent from ADTOrder — no Go type is ever emitted for it.
	ADTs     map[int]*types.ADTInfo
	ADTOrder []*types.ADTInfo

	// TypeNames maps surface type names to their current types — the type
	// table's embryo, exactly as Ctors is for constructors. `type`
	// declarations (S4) and REPL generations extend it; identity stays the
	// TCon Unique underneath.
	TypeNames map[string]types.Type

	// PrintCalls marks App nodes recognized as the print builtin cheat, so
	// elaboration classifies them identically (one source of truth).
	PrintCalls map[*ast.App]bool

	// Workers maps top-level function names to their syntactic parameter
	// count — the arity that drives §8.2 saturation analysis. Session
	// state like Ctors: populated at inference time (complete before
	// elaboration, which fib's self-call requires), extended by the REPL.
	Workers map[string]int

	// BindTypes records each block binding's full solved type (a local
	// function's curried type — ExprTypes only has its body's type).
	BindTypes map[*ast.LocalBind]types.Type

	// PatTypes records each pattern node's type — decision-tree compilation
	// needs pattern-variable types after solving, exactly as ExprTypes
	// serves expressions.
	PatTypes map[ast.Pattern]types.Type
}

func NewChecker(sup *types.Supply, b *types.Builtins, env *Env) *Checker {
	ck := &Checker{
		Sup:       sup,
		B:         b,
		Env:       env,
		Sub:       Subst{},
		ExprTypes: map[ast.Expr]types.Type{},
		Ctors:     map[string]*types.CtorInfo{},
		ADTs:      map[int]*types.ADTInfo{},
		TypeNames: map[string]types.Type{
			"Int":    b.Int,
			"Float":  b.Float,
			"String": b.String,
			"Bool":   b.Bool,
			"()":     b.Unit,
		},
		PrintCalls: map[*ast.App]bool{},
		Workers:    map[string]int{},
		BindTypes:  map[*ast.LocalBind]types.Type{},
		PatTypes:   map[ast.Pattern]types.Type{},
	}
	// Bool is an ordinary ADT in the checker (§7.2) — patterns, case
	// exhaustiveness, and the ctor table treat it like any declared type.
	boolADT := &types.ADTInfo{Con: b.Bool, Ctors: []*types.CtorInfo{
		{Name: "True", Index: 0, Result: b.Bool},
		{Name: "False", Index: 1, Result: b.Bool},
	}}
	ck.ADTs[b.Bool.Unique] = boolADT
	for _, c := range boolADT.Ctors {
		ck.Ctors[c.Name] = c
	}
	return ck
}

type DeclInfo struct {
	Name     string
	NameSpan source.Span
	Params   []ast.Param
	Type     types.Type // solved but not zonked; apply ck.Sub for the final type
	Body     ast.Expr
}

// Module checks declarations: type headers first (so types may be mutually
// recursive regardless of order), then constructor fields, then value
// declarations in source order — solve-at-definition, the same call
// structure generalization will use from S2.
func (ck *Checker) Module(m *ast.Module) ([]DeclInfo, []diag.Error) {
	var infos []DeclInfo
	var errs []diag.Error
	adts := map[*ast.TypeDecl]*types.ADTInfo{}
	for _, d := range m.Decls {
		if td, ok := d.(*ast.TypeDecl); ok {
			// Duplicate types are a batch-compilation error only: the REPL
			// redefines types freely (generational uniques).
			if _, dup := ck.TypeNames[td.Name]; dup {
				errs = append(errs, diag.Errorf(td.NameSpan, "MULTIPLE DEFINITIONS",
					"The type `%s` is defined more than once.", td.Name))
			}
			adt, headerErrs := ck.declareTypeHeader(td)
			errs = append(errs, headerErrs...)
			if adt != nil {
				adts[td] = adt
			}
		}
	}
	for _, d := range m.Decls {
		if td, ok := d.(*ast.TypeDecl); ok {
			if adt := adts[td]; adt != nil {
				errs = append(errs, ck.declareTypeCtors(td, adt, true)...)
			}
		}
	}
	for _, d := range m.Decls {
		vd, ok := d.(*ast.ValueDecl)
		if !ok {
			continue
		}
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

// TypeDecl checks and installs one type declaration — the REPL's entry
// point, where redefinition is allowed (a fresh generation, §9.3).
func (ck *Checker) TypeDecl(td *ast.TypeDecl) []diag.Error {
	adt, errs := ck.declareTypeHeader(td)
	if adt == nil {
		return errs
	}
	return append(errs, ck.declareTypeCtors(td, adt, false)...)
}

// declareTypeHeader registers the type's name and unique — before any
// constructor field resolves, so recursive and mutually recursive types
// work. Returns nil for declarations rejected wholesale (type parameters).
func (ck *Checker) declareTypeHeader(td *ast.TypeDecl) (*types.ADTInfo, []diag.Error) {
	if len(td.Params) > 0 {
		return nil, []diag.Error{diag.Errorf(td.NameSpan, "UNSUPPORTED TYPE PARAMETERS",
			"`%s` declares type parameters — parameterized types arrive with\npolymorphism (S5). For now types must be monomorphic.", td.Name)}
	}
	con := &types.TCon{Unique: ck.Sup.NextUnique(), Name: td.Name}
	adt := &types.ADTInfo{Con: con}
	ck.TypeNames[td.Name] = con
	ck.ADTs[con.Unique] = adt
	ck.ADTOrder = append(ck.ADTOrder, adt)
	return adt, nil
}

// declareTypeCtors resolves constructor fields and installs the
// constructors. batch reports duplicate constructor names as errors; the
// REPL path rebinds them (generational, like values).
func (ck *Checker) declareTypeCtors(td *ast.TypeDecl, adt *types.ADTInfo, batch bool) []diag.Error {
	var errs []diag.Error
	for _, c := range td.Ctors {
		if prev, dup := ck.Ctors[c.Name]; dup && batch {
			errs = append(errs, diag.Errorf(c.NameSpan, "MULTIPLE DEFINITIONS",
				"The constructor `%s` is already defined by type `%s` —\nconstructor names must be unique across a module.",
				c.Name, prev.Result.Name))
		}
		if adt.CtorNamed(c.Name) != nil {
			// Same-type duplicate: an error even in the REPL.
			errs = append(errs, diag.Errorf(c.NameSpan, "MULTIPLE DEFINITIONS",
				"The constructor `%s` appears twice in `type %s`.", c.Name, td.Name))
			continue
		}
		fields := make([]types.Type, len(c.Args))
		for j, a := range c.Args {
			t, fieldErrs := ck.ResolveTypeExpr(a)
			errs = append(errs, fieldErrs...)
			if t == nil {
				t = ck.B.Unit // hole: errs is non-empty, elaboration never runs
			}
			fields[j] = t
		}
		info := &types.CtorInfo{Name: c.Name, Index: len(adt.Ctors), Fields: fields, Result: adt.Con}
		adt.Ctors = append(adt.Ctors, info)
		ck.Ctors[c.Name] = info
	}
	return errs
}

// Decl checks one value declaration and binds it in the environment —
// rebinding an existing name is allowed (the REPL's redefinition path).
// Only main's body may use the print cheat (§10.5's top-level purity,
// enforced ad hoc until effects land in S7).
func (ck *Checker) Decl(d *ast.ValueDecl) (DeclInfo, []diag.Error) {
	info, errs := ck.DeclWhere(d, d.Name == "main")
	ck.BindDecl(info)
	return info, errs
}

// BindDecl installs a checked declaration: the environment binding plus
// worker-table upkeep (redefining a worker as a value evicts its arity).
func (ck *Checker) BindDecl(info DeclInfo) {
	ck.Env.Bind(info.Name, types.Scheme{Body: info.Type})
	if len(info.Params) > 0 {
		ck.Workers[info.Name] = len(info.Params)
	} else {
		delete(ck.Workers, info.Name)
	}
}

// DeclWhere checks one declaration — annotation resolution, body inference
// (with parameter scoping and self-recursion for function definitions),
// and the annotation constraint — without binding it, so callers control
// whether a failed definition enters the environment (the REPL does not
// bind on error).
func (ck *Checker) DeclWhere(d *ast.ValueDecl, allowPrint bool) (DeclInfo, []diag.Error) {
	var errs []diag.Error
	if d.Name == "main" && len(d.Params) > 0 {
		errs = append(errs, diag.Errorf(d.NameSpan, "MAIN TAKES NO PARAMETERS",
			"Until effects land (S7), `main` is a value, not a function."))
	}

	g := &generator{ck: ck, allowPrint: allowPrint}
	var ty types.Type
	if len(d.Params) == 0 {
		ty = g.expr(d.Body)
	} else {
		ty = g.function(d.Name, d.NameSpan, d.Params, d.Body)
	}
	sub, _, solveErrs := Solve(g.cs, nil, ck.Sub, ck.B)
	ck.Sub = sub
	errs = append(errs, g.errs...)
	errs = append(errs, solveErrs...)

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
	return DeclInfo{Name: d.Name, NameSpan: d.NameSpan, Params: d.Params, Type: ty, Body: d.Body}, errs
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
		info, ok := g.ck.Ctors[e.Name]
		if !ok {
			g.errs = append(g.errs, diag.Errorf(e.Sp, "NAMING ERROR",
				"I don't know a constructor named `%s`.", e.Name))
			ty = g.ck.Sup.FreshVar(types.General)
			break
		}
		ty = info.ValueType()
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
	case *ast.Case:
		ty = g.caseExpr(e)
	case *ast.Lambda:
		// Functions are pure until effects land: print cannot be smuggled
		// into a function value (§10.5).
		saved := g.allowPrint
		g.allowPrint = false
		scope := &blockScope{parent: g.locals, names: map[string]types.Type{}}
		g.locals = scope
		paramTys := g.bindParams(scope, e.Params)
		bodyTy := g.expr(e.Body)
		g.locals = scope.parent
		g.allowPrint = saved
		funTy := bodyTy
		for i := len(paramTys) - 1; i >= 0; i-- {
			funTy = &types.TFun{Arg: paramTys[i], Ret: funTy}
		}
		ty = funTy
	default:
		panic("infer: unhandled expression node")
	}
	g.ck.ExprTypes[e] = ty
	return ty
}

// function checks a function definition (top-level or block-local): the
// name is pre-bound to a fresh monotype in the same scope as the params so
// the body's self-references type — monomorphic recursion. The fresh var
// lives in the block scope, never in Env, so failed REPL definitions need
// no rollback and redefinition resolves self-references to the new body.
func (g *generator) function(name string, nameSpan source.Span, params []ast.Param, body ast.Expr) types.Type {
	self := g.ck.Sup.FreshVar(types.General)
	scope := &blockScope{parent: g.locals, names: map[string]types.Type{name: self}}
	g.locals = scope
	defer func() { g.locals = scope.parent }()

	paramTys := g.bindParams(scope, params)
	bodyTy := g.expr(body)

	funTy := bodyTy
	for i := len(paramTys) - 1; i >= 0; i-- {
		funTy = &types.TFun{Arg: paramTys[i], Ret: funTy}
	}
	g.cs = append(g.cs, Constraint{Left: self, Right: funTy, Span: nameSpan,
		Why: Why{Kind: WhyRecursion, Name: name}})
	return funTy
}

// bindParams enters parameters into scope with the no-shadowing rule:
// duplicates in the list, the function's own name, enclosing locals, and
// top-level names are all rejected.
func (g *generator) bindParams(scope *blockScope, params []ast.Param) []types.Type {
	tys := make([]types.Type, len(params))
	for i, p := range params {
		if _, dup := g.locals.lookup(p.Name); dup || g.ck.Env.Has(p.Name) {
			g.errs = append(g.errs, diag.Errorf(p.Sp, "SHADOWING",
				"The parameter `%s` shadows a name that is already defined —\nfango does not allow shadowing. Choose a different name.", p.Name))
		}
		pv := g.ck.Sup.FreshVar(types.General)
		scope.names[p.Name] = pv
		tys[i] = pv
	}
	return tys
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
		var ty types.Type
		if len(bind.Params) > 0 {
			ty = g.function(bind.Name, bind.NameSpan, bind.Params, bind.Body)
		} else {
			ty = g.expr(bind.Body)
		}
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
		g.ck.BindTypes[bind] = ty
		g.locals.names[bind.Name] = ty
	}
	return g.expr(e.Result)
}

// caseExpr constrains a case: every pattern matches the scrutinee's type,
// every branch body matches the case's result type. Pattern variables scope
// over their branch's body only.
func (g *generator) caseExpr(e *ast.Case) types.Type {
	scrutTy := g.expr(e.Scrutinee)
	resultTy := g.ck.Sup.FreshVar(types.General)
	for i := range e.Branches {
		br := &e.Branches[i]
		scope := &blockScope{parent: g.locals, names: map[string]types.Type{}}
		g.locals = scope
		patTy := g.pattern(br.Pattern, scope)
		g.cs = append(g.cs, Constraint{
			Left: patTy, Right: scrutTy, Span: br.Pattern.Span(), Why: Why{Kind: WhyPattern},
		})
		bodyTy := g.expr(br.Body)
		g.locals = scope.parent
		g.cs = append(g.cs, Constraint{
			Left: bodyTy, Right: resultTy, Span: br.Body.Span(), Why: Why{Kind: WhyCaseBranches},
		})
	}
	return resultTy
}

// pattern types one pattern, binding its variables into scope with the
// no-shadowing rule (which also catches `Pair x x`).
func (g *generator) pattern(p ast.Pattern, scope *blockScope) types.Type {
	ty := g.patternInner(p, scope)
	g.ck.PatTypes[p] = ty
	return ty
}

func (g *generator) patternInner(p ast.Pattern, scope *blockScope) types.Type {
	switch p := p.(type) {
	case *ast.PWildcard:
		return g.ck.Sup.FreshVar(types.General)
	case *ast.PVar:
		if _, dup := g.locals.lookup(p.Name); dup || g.ck.Env.Has(p.Name) {
			g.errs = append(g.errs, diag.Errorf(p.Sp, "SHADOWING",
				"The pattern variable `%s` shadows a name that is already defined —\nfango does not allow shadowing. Choose a different name.", p.Name))
		}
		pv := g.ck.Sup.FreshVar(types.General)
		scope.names[p.Name] = pv
		return pv
	case *ast.PInt:
		// Like integer literals: a `number` pattern (Int or Float).
		return g.ck.Sup.FreshVar(types.Number)
	case *ast.PFloat:
		return g.ck.B.Float
	case *ast.PString:
		return g.ck.B.String
	case *ast.PCtor:
		info, ok := g.ck.Ctors[p.Name]
		if !ok {
			g.errs = append(g.errs, diag.Errorf(p.NameSpan, "NAMING ERROR",
				"I don't know a constructor named `%s`.", p.Name))
			for _, a := range p.Args {
				g.pattern(a, scope) // still bind their variables: fewer cascades
			}
			return g.ck.Sup.FreshVar(types.General)
		}
		if len(p.Args) != len(info.Fields) {
			g.errs = append(g.errs, diag.Errorf(p.Span(), "PATTERN ARITY",
				"The `%s` constructor takes %d argument(s), but this pattern\ngives it %d.", p.Name, len(info.Fields), len(p.Args)))
			for _, a := range p.Args {
				g.pattern(a, scope)
			}
			return info.Result
		}
		for i, a := range p.Args {
			argTy := g.pattern(a, scope)
			g.cs = append(g.cs, Constraint{
				Left: argTy, Right: info.Fields[i], Span: a.Span(), Why: Why{Kind: WhyPattern},
			})
		}
		return info.Result
	default:
		panic("infer: unhandled pattern node")
	}
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
