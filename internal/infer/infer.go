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
	WhyOperand        WhyKind = iota // operands of a numeric operator must agree
	WhyDeclBody                      // a declaration body must match its (future) annotation
	WhyCall                          // a callee must be a function accepting the argument
	WhyIfCondition                   // an if condition must be Bool
	WhyIfBranches                    // then/else branches must agree
	WhyCompare                       // both sides of a comparison must agree
	WhyNegate                        // a negated operand must be a number
	WhyOpRequires                    // an operator fixes its operand type (/, ++)
	WhyAnnotation                    // a definition must match its type annotation
	WhyRecursion                     // recursive uses must match the definition
	WhyPattern                       // a pattern must match the scrutinee's type
	WhyCaseBranches                  // all case branches must produce the same type
	WhyEffectEscapes                 // a top-level value performs an unhandled effect
	WhyEffectMismatch                // an annotation's effect row disagrees with its body
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
	TypeNames       map[string]types.Type
	Effects         map[string]*types.EffectInfo
	EffectsByUnique map[int]*types.EffectInfo
	Operations      map[string]*types.EffectOp
	IO              *types.EffectInfo

	// PrintCalls marks App nodes recognized as the print builtin cheat, so
	// elaboration classifies them identically (one source of truth).
	PrintCalls  map[*ast.App]bool
	OpCalls     map[*ast.App]*types.EffectOp
	HandleInfos map[*ast.Handle]*HandlerInfo
	ResumeCalls map[*ast.App]bool

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

	// BindSchemes records each block binding's generalized scheme —
	// elaboration lifts a binding whose scheme quantifies (§8.4).
	BindSchemes map[*ast.LocalBind]types.Scheme

	// LiftGen numbers lambda-lifted definitions session-wide, so REPL
	// inputs across a session never collide (elaborate/lift.go).
	LiftGen int

	// MonoValues applies the block-binding monomorphism restriction to
	// top-level value declarations too. The REPL sets it: a prompt value is
	// a lazy memo cell (§9.2), evaluated once, so its type must stay a
	// monotype — a generalized value re-evaluates per use, which would
	// observably interact with redefinition (breaking §9.3's "old closures
	// keep old values"). Batch modules are immutable, so re-evaluation is
	// unobservable there and top-level values generalize per §8.4.
	MonoValues bool
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
		Effects:         map[string]*types.EffectInfo{},
		EffectsByUnique: map[int]*types.EffectInfo{},
		Operations:      map[string]*types.EffectOp{},
		PrintCalls:      map[*ast.App]bool{},
		OpCalls:         map[*ast.App]*types.EffectOp{},
		HandleInfos:     map[*ast.Handle]*HandlerInfo{},
		ResumeCalls:     map[*ast.App]bool{},
		Workers:         map[string]int{},
		BindTypes:       map[*ast.LocalBind]types.Type{},
		PatTypes:        map[ast.Pattern]types.Type{},
		BindSchemes:     map[*ast.LocalBind]types.Scheme{},
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
	ck.seedIO()
	return ck
}

func (ck *Checker) seedIO() {
	ioEff := &types.EffectInfo{Unique: ck.Sup.NextUnique(), Name: "IO"}
	ck.IO = ioEff
	ck.Effects[ioEff.Name], ck.EffectsByUnique[ioEff.Unique] = ioEff, ioEff
	label := types.EffLabel{Unique: ioEff.Unique, Name: ioEff.Name}
	row := func() types.Row {
		return types.Row{Labels: []types.EffLabel{label}, Tail: ck.Sup.FreshRigid(types.RowVar)}
	}
	readTy := &types.TFun{Arg: ck.B.Unit, Eff: row(), Ret: ck.B.String}
	read := &types.EffectOp{Owner: ioEff, Index: 0, Name: "readLine", Arity: 1,
		ParamTypes: []types.Type{ck.B.Unit}, ResultType: ck.B.String, Builtin: true}
	read.Scheme = types.Scheme{Vars: []*types.TVar{readTy.Eff.Tail.(*types.TVar)}, Body: readTy}
	a := ck.Sup.FreshRigid(types.General)
	printTy := &types.TFun{Arg: a, Eff: row(), Ret: ck.B.Unit}
	print := &types.EffectOp{Owner: ioEff, Index: 1, Name: "print", Arity: 1,
		ParamTypes: []types.Type{a}, ResultType: ck.B.Unit, LocalVars: []*types.TVar{a}, Builtin: true}
	print.Scheme = types.Scheme{Vars: []*types.TVar{a, printTy.Eff.Tail.(*types.TVar)}, Body: printTy}
	ioEff.Ops = []*types.EffectOp{read, print}
	for _, op := range ioEff.Ops {
		ck.Operations[op.Name] = op
		ck.Env.Bind(op.Name, op.Scheme)
	}
}

type DeclInfo struct {
	Name     string
	NameSpan source.Span
	Params   []ast.Param
	Type     types.Type // solved but not zonked; apply ck.Sub for the final type
	Body     ast.Expr

	// Scheme is the declaration's generalized type: Scheme.Vars are the
	// definition's type parameters (elaboration's Def.TyParams). Quantified
	// metas were bound to Scheme.Vars in ck.Sub at generalization time, so
	// zonked occurrence types mention the scheme's own rigid vars. With
	// AllowPoly off this is always the trivial Scheme{Body}.
	Scheme types.Scheme
}

type HandlerClauseInfo struct {
	Op         *types.EffectOp
	ParamTypes []types.Type
	OpResult   types.Type
}

type HandlerInfo struct {
	Effect     types.EffLabel
	Result     types.Type
	BodyResult types.Type
	Clauses    []HandlerClauseInfo
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
		if ed, ok := d.(*ast.EffectDecl); ok {
			errs = append(errs, ck.declareEffectHeader(ed, true)...)
		}
		if td, ok := d.(*ast.TypeDecl); ok {
			if _, clash := ck.Effects[td.Name]; clash {
				errs = append(errs, diag.Errorf(td.NameSpan, "MULTIPLE DEFINITIONS",
					"The type `%s` collides with an effect of the same name.", td.Name))
			}
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
		if ed, ok := d.(*ast.EffectDecl); ok {
			errs = append(errs, ck.declareEffectOps(ed, true)...)
		}
	}
	for _, d := range m.Decls {
		if td, ok := d.(*ast.TypeDecl); ok {
			if adt := adts[td]; adt != nil {
				errs = append(errs, ck.declareTypeCtors(td, adt, true)...)
			}
		}
	}
	errs = append(errs, ck.checkRegularity(adts)...)
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

func (ck *Checker) declareEffectHeader(ed *ast.EffectDecl, batch bool) []diag.Error {
	var errs []diag.Error
	if _, exists := ck.TypeNames[ed.Name]; exists {
		errs = append(errs, diag.Errorf(ed.NameSpan, "MULTIPLE DEFINITIONS",
			"The effect `%s` collides with a type of the same name.", ed.Name))
	}
	if _, exists := ck.Effects[ed.Name]; exists && batch {
		errs = append(errs, diag.Errorf(ed.NameSpan, "MULTIPLE DEFINITIONS",
			"The effect `%s` is defined more than once.", ed.Name))
	}
	seen := map[string]bool{}
	params := make([]*types.TVar, len(ed.Params))
	for i, p := range ed.Params {
		if seen[p.Name] {
			errs = append(errs, diag.Errorf(p.Sp, "SHADOWING", "The effect parameter `%s` appears twice.", p.Name))
		}
		seen[p.Name] = true
		params[i] = ck.Sup.FreshRigid(types.General)
	}
	info := &types.EffectInfo{Unique: ck.Sup.NextUnique(), Name: ed.Name, Params: params}
	ck.Effects[ed.Name], ck.EffectsByUnique[info.Unique] = info, info
	return errs
}

func (ck *Checker) declareEffectOps(ed *ast.EffectDecl, batch bool) []diag.Error {
	info := ck.Effects[ed.Name]
	if info == nil {
		return nil
	}
	names := make([]string, len(ed.Params))
	for i, p := range ed.Params {
		names[i] = p.Name
	}
	seen := map[string]bool{}
	var errs []diag.Error
	for _, op := range ed.Ops {
		if seen[op.Name] || (batch && ck.Env.Has(op.Name)) {
			errs = append(errs, diag.Errorf(op.NameSpan, "MULTIPLE DEFINITIONS",
				"The operation `%s` collides with another value in this module.", op.Name))
			continue
		}
		seen[op.Name] = true
		scope := newEffectScope(names, info.Params, ck.Sup)
		ty, opErrs := ck.ResolveTypeExpr(op.Type, scope)
		errs = append(errs, opErrs...)
		if ty == nil {
			continue
		}
		fn, ok := ty.(*types.TFun)
		if !ok {
			errs = append(errs, diag.Errorf(op.NameSpan, "EFFECT OPERATION TYPE",
				"The operation `%s` must have a function type.", op.Name))
			continue
		}
		labelArgs := make([]types.Type, len(info.Params))
		for i, p := range info.Params {
			labelArgs[i] = p
		}
		var arrows []*types.TFun
		for cur := fn; ; {
			arrows = append(arrows, cur)
			next, ok := cur.Ret.(*types.TFun)
			if !ok {
				break
			}
			cur = next
		}
		rowVars := make([]*types.TVar, len(arrows))
		for i, arrow := range arrows {
			rowVars[i] = ck.Sup.FreshRigid(types.RowVar)
			arrow.Eff = types.Row{Tail: rowVars[i]}
		}
		inner := arrows[len(arrows)-1]
		inner.Eff.Labels = []types.EffLabel{{Unique: info.Unique, Name: info.Name, Args: labelArgs}}
		vars := append([]*types.TVar(nil), info.Params...)
		vars = append(vars, scope.Minted()...)
		vars = append(vars, rowVars...)
		sch := types.Scheme{Vars: vars, Body: ty}
		params := make([]types.Type, len(arrows))
		for i, a := range arrows {
			params[i] = a.Arg
		}
		local := append([]*types.TVar(nil), scope.Minted()...)
		meta := &types.EffectOp{Owner: info, Index: len(info.Ops), Name: op.Name, Scheme: sch,
			Arity: len(arrows), ParamTypes: params, ResultType: arrows[len(arrows)-1].Ret, LocalVars: local}
		info.Ops = append(info.Ops, meta)
		ck.Operations[op.Name] = meta
		ck.Env.Bind(op.Name, sch)
	}
	return errs
}

// TypeDecl checks and installs one type declaration — the REPL's entry
// point, where redefinition is allowed (a fresh generation, §9.3).
func (ck *Checker) TypeDecl(td *ast.TypeDecl) []diag.Error {
	adt, errs := ck.declareTypeHeader(td)
	if adt == nil {
		return errs
	}
	errs = append(errs, ck.declareTypeCtors(td, adt, false)...)
	return append(errs, ck.checkRegularity(map[*ast.TypeDecl]*types.ADTInfo{td: adt})...)
}

func (ck *Checker) EffectDecl(ed *ast.EffectDecl) []diag.Error {
	errs := ck.declareEffectHeader(ed, false)
	return append(errs, ck.declareEffectOps(ed, false)...)
}

// declareTypeHeader registers the type's name, unique, and parameters —
// before any constructor field resolves, so recursive and mutually recursive
// types work. Returns nil for declarations rejected wholesale.
func (ck *Checker) declareTypeHeader(td *ast.TypeDecl) (*types.ADTInfo, []diag.Error) {
	var errs []diag.Error
	params := make([]*types.TVar, len(td.Params))
	seen := map[string]bool{}
	for i, p := range td.Params {
		if seen[p.Name] {
			errs = append(errs, diag.Errorf(p.Sp, "SHADOWING",
				"The type parameter `%s` appears twice in `type %s` — parameters\nmust be distinct.", p.Name, td.Name))
		}
		seen[p.Name] = true
		params[i] = ck.Sup.FreshRigid(types.General)
	}
	con := &types.TCon{Unique: ck.Sup.NextUnique(), Name: td.Name}
	adt := &types.ADTInfo{Con: con, Params: params}
	ck.TypeNames[td.Name] = con
	ck.ADTs[con.Unique] = adt
	ck.ADTOrder = append(ck.ADTOrder, adt)
	return adt, errs
}

// declareTypeCtors resolves constructor fields and installs the
// constructors. batch reports duplicate constructor names as errors; the
// REPL path rebinds them (generational, like values).
func (ck *Checker) declareTypeCtors(td *ast.TypeDecl, adt *types.ADTInfo, batch bool) []diag.Error {
	var errs []diag.Error
	// Field types resolve in the closed scope of the declaration's
	// parameters; the constructors' result type is the type applied to its
	// own parameters (`Just : a -> Maybe a`).
	paramNames := make([]string, len(td.Params))
	for i, p := range td.Params {
		paramNames[i] = p.Name
	}
	scope := newCtorScope(paramNames, adt.Params)
	result := adt.Con
	if len(adt.Params) > 0 {
		args := make([]types.Type, len(adt.Params))
		for i, p := range adt.Params {
			args[i] = p
		}
		result = &types.TCon{Unique: adt.Con.Unique, Name: adt.Con.Name, Args: args}
	}
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
			t, fieldErrs := ck.ResolveTypeExpr(a, scope)
			errs = append(errs, fieldErrs...)
			if t == nil {
				t = ck.B.Unit // hole: errs is non-empty, elaboration never runs
			}
			fields[j] = t
		}
		info := &types.CtorInfo{Name: c.Name, Index: len(adt.Ctors), Fields: fields, Result: result}
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
	info, errs := ck.DeclWhere(d, true)
	ck.BindDecl(info)
	return info, errs
}

// BindDecl installs a checked declaration: the environment binding plus
// worker-table upkeep (redefining a worker as a value evicts its arity).
func (ck *Checker) BindDecl(info DeclInfo) {
	ck.Env.Bind(info.Name, info.Scheme)
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
	if d.Name == "main" && len(d.Params) > 1 {
		errs = append(errs, diag.Errorf(d.NameSpan, "MAIN TAKES NO PARAMETERS",
			"`main` may be a value or a one-argument Unit function."))
	}
	if d.Name == "main" && len(d.Params) == 1 && d.Params[0].Name != "_" {
		errs = append(errs, diag.Errorf(d.Params[0].Sp, "MAIN TAKES NO PARAMETERS", "Function-style `main` must discard its Unit argument with `_`."))
	}

	g := &generator{ck: ck, allowPrint: allowPrint, ambient: types.Row{Tail: ck.Sup.FreshVar(types.RowVar)}}
	var ty types.Type
	if len(d.Params) == 0 {
		ty = g.expr(d.Body)
		if d.Name != "main" {
			g.cs = append(g.cs, Constraint{Left: g.ambient, Right: types.Row{}, Span: d.Body.Span(), Why: Why{Kind: WhyEffectEscapes}})
		}
	} else {
		ty = g.function(d.Name, d.NameSpan, d.Params, d.Body)
		if d.Name == "main" && len(d.Params) == 1 {
			want := &types.TFun{Arg: ck.B.Unit, Eff: types.Row{Labels: []types.EffLabel{{Unique: ck.IO.Unique, Name: ck.IO.Name}}}, Ret: ck.B.Unit}
			g.cs = append(g.cs, Constraint{Left: ty, Right: want, Span: d.Body.Span(), Why: Why{Kind: WhyAnnotation, Name: "main"}})
		}
	}
	sub, _, solveErrs := Solve(g.cs, nil, ck.Sub, ck.B, ck.Sup)
	ck.Sub = sub
	errs = append(errs, g.errs...)
	errs = append(errs, solveErrs...)
	if !allowPrint && typeHasEffects(ck.Sub.Apply(ty)) {
		errs = append(errs, diag.Errorf(d.Body.Span(), "EFFECTFUL PROMPT DECLARATION", "Effectful declarations are not installed at the prompt; run the expression directly."))
	}
	if d.Name == "main" && len(d.Params) == 0 {
		row := ck.Sub.Apply(g.ambient).(types.Row)
		for _, l := range row.Labels {
			if l.Unique != ck.IO.Unique {
				errs = append(errs, diag.Errorf(d.Body.Span(), "UNHANDLED EFFECT", "Legacy `main` may perform IO, but `%s` is not handled.", l.Name))
			}
		}
	}

	if d.Ann != nil {
		// Skolemize-and-unify (§7.2): the annotation's variables resolve to
		// fresh rigid skolems, atomic in unification, so an annotation
		// claiming more polymorphism than the body delivers errors here.
		annTy, annErrs := ck.ResolveTypeExpr(d.Ann.Type, ck.NewAnnScope())
		errs = append(errs, annErrs...)
		if annTy != nil {
			if !sameKnownEffects(ck.Sub.Apply(annTy), ck.Sub.Apply(ty)) {
				errs = append(errs, diag.Errorf(d.Ann.Sp, "EFFECT MISMATCH",
					"The effect row in the annotation for `%s` does not exactly match the effects performed by its body.", d.Name))
			}
			c := Constraint{Left: annTy, Right: ty, Span: d.Body.Span(),
				Why: Why{Kind: WhyAnnotation, Name: d.Name}}
			sub, _, solveErrs := Solve([]Constraint{c}, nil, ck.Sub, ck.B, ck.Sup)
			ck.Sub = sub
			errs = append(errs, solveErrs...)
			ty = annTy
		}
	}
	info := DeclInfo{Name: d.Name, NameSpan: d.NameSpan, Params: d.Params, Type: ty, Body: d.Body}
	_, isLambda := d.Body.(*ast.Lambda)
	switch {
	case d.Name == "main":
		// main is the program's ground entry point (§8.4: its effect is
		// forced inside func main()) — it never generalizes. Unconstrained
		// variables in its type default like interior ones (Number → Int,
		// General → Unit), so `main = 1 + 2` stays an Int program.
		info.Scheme = types.Scheme{Body: ty}
	case ck.MonoValues && len(d.Params) == 0 && !isLambda:
		info.Scheme = types.Scheme{Body: ty}
	default:
		info.Scheme = ck.generalize(ty, nil)
	}
	return info, errs
}

func typeHasEffects(t types.Type) bool {
	if f, ok := t.(*types.TFun); ok {
		return len(f.Eff.Labels) > 0 || typeHasEffects(f.Ret)
	}
	return false
}

func sameKnownEffects(a, b types.Type) bool {
	af, aok := a.(*types.TFun)
	bf, bok := b.(*types.TFun)
	if !aok || !bok {
		return true
	}
	al, bl := types.SortedRow(af.Eff).Labels, types.SortedRow(bf.Eff).Labels
	if len(al) != len(bl) {
		return false
	}
	for i := range al {
		if al[i].Unique != bl[i].Unique {
			return false
		}
	}
	return sameKnownEffects(af.Ret, bf.Ret)
}

// Expr checks an expression with prompt semantics (print allowed) — the
// REPL's expression entry point.
func (ck *Checker) Expr(e ast.Expr) (types.Type, []diag.Error) {
	return ck.ExprWhere(e, true)
}

// ExprWhere generates constraints for one expression and solves them into
// the checker's substitution. allowPrint gates the print builtin cheat.
func (ck *Checker) ExprWhere(e ast.Expr, allowPrint bool) (types.Type, []diag.Error) {
	g := &generator{ck: ck, allowPrint: allowPrint, ambient: types.Row{Tail: ck.Sup.FreshVar(types.RowVar)}}
	ty := g.expr(e)
	var preds []types.Pred // the typeclass seam: always empty in the MVP
	sub, residual, solveErrs := Solve(g.cs, preds, ck.Sub, ck.B, ck.Sup)
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
	ambient    types.Row
	resumeType types.Type
}

// blockScope is a block's local bindings, as schemes: parameters and
// pre-bound recursive names are trivial (monotype) schemes, while generalized
// block bindings quantify (S5) and instantiate per use like top-level names.
type blockScope struct {
	parent *blockScope
	names  map[string]types.Scheme
}

func (s *blockScope) lookup(name string) (types.Scheme, bool) {
	for ; s != nil; s = s.parent {
		if t, ok := s.names[name]; ok {
			return t, true
		}
	}
	return types.Scheme{}, false
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
	case *ast.UnitLit:
		ty = g.ck.B.Unit
	case *ast.Var:
		if g.isPrintCheat(e) {
			g.errs = append(g.errs, diag.Errorf(e.Sp, "PRINT NEEDS AN ARGUMENT",
				"`print` is a builtin that must be applied to exactly one\nargument, like `print (1 + 2)`. Using it as a value arrives with\neffects (S7)."))
			ty = g.ck.Sup.FreshVar(types.General)
			break
		}
		if localScheme, ok := g.locals.lookup(e.Name); ok {
			ty = g.instantiate(localScheme)
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
		fields, result := g.instantiateCtor(info)
		ty = result
		for i := len(fields) - 1; i >= 0; i-- {
			ty = &types.TFun{Arg: fields[i], Eff: types.Row{Tail: g.ck.Sup.FreshVar(types.RowVar)}, Ret: ty}
		}
	case *ast.App:
		if op, n := g.operationSpine(e); op != nil && n == op.Arity {
			inst := g.instantiate(op.Scheme)
			g.ck.ExprTypes[appHead(e)] = inst
			params, result := peelOperation(inst, op.Arity)
			args := appArgs(e)
			for i, a := range args {
				at := g.expr(a)
				g.cs = append(g.cs, Constraint{Left: at, Right: params[i], Span: a.Span(), Why: Why{Kind: WhyCall}})
			}
			cur := inst
			var last *types.TFun
			for range op.Arity {
				last = cur.(*types.TFun)
				cur = last.Ret
			}
			g.cs = append(g.cs, Constraint{Left: last.Eff, Right: g.ambient, Span: e.Span(), Why: Why{Kind: WhyCall}})
			g.ck.OpCalls[e] = op
			if len(op.LocalVars) > 0 && !(op.Owner == g.ck.IO && op.Name == "print") {
				g.errs = append(g.errs, diag.Errorf(e.Span(), "OPERATION POLYMORPHISM NOT READY", "The operation `%s` has result polymorphism that is staged until checkpoint 3.", op.Name))
			}
			ty = result
			break
		}
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
		callEff := types.Row{}
		if f, ok := g.ck.Sub.Apply(fnTy).(*types.TFun); ok && len(f.Eff.Labels) > 0 {
			callEff = g.ambient
		}
		if _, isLambda := e.Fn.(*ast.Lambda); isLambda {
			callEff = g.ambient
		}
		g.cs = append(g.cs, Constraint{
			Left:  fnTy,
			Right: &types.TFun{Arg: argTy, Eff: callEff, Ret: r},
			Span:  e.Fn.Span(),
			Why:   Why{Kind: WhyCall},
		})
		if op, n := g.operationSpine(e); op != nil && n == op.Arity {
			g.ck.OpCalls[e] = op
			if len(op.LocalVars) > 0 && !(op.Owner == g.ck.IO && op.Name == "print") {
				g.errs = append(g.errs, diag.Errorf(e.Span(), "OPERATION POLYMORPHISM NOT READY",
					"The operation `%s` has result polymorphism that is staged until checkpoint 3.", op.Name))
			}
		}
		if _, ok := e.Fn.(*ast.Resume); ok {
			g.ck.ResumeCalls[e] = true
		}
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
		scope := &blockScope{parent: g.locals, names: map[string]types.Scheme{}}
		g.locals = scope
		paramTys := g.bindParams(scope, e.Params)
		savedAmbient := g.ambient
		bodyAmbient := types.Row{Tail: g.ck.Sup.FreshVar(types.RowVar)}
		g.ambient = bodyAmbient
		bodyTy := g.expr(e.Body)
		g.ambient = savedAmbient
		g.locals = scope.parent
		g.allowPrint = saved
		funTy := g.wrapFunction(paramTys, bodyTy, bodyAmbient)
		ty = funTy
	case *ast.Handle:
		ty = g.handle(e)
	case *ast.Resume:
		if g.resumeType == nil {
			g.errs = append(g.errs, diag.Errorf(e.Sp, "RESUME OUTSIDE A HANDLER", "`resume` is only available inside an operation clause."))
			ty = g.ck.Sup.FreshVar(types.General)
		} else {
			ty = g.resumeType
		}
	default:
		panic("infer: unhandled expression node")
	}
	g.ck.ExprTypes[e] = ty
	return ty
}

func appArgs(e *ast.App) []ast.Expr {
	var rev []ast.Expr
	var cur ast.Expr = e
	for {
		a, ok := cur.(*ast.App)
		if !ok {
			break
		}
		rev = append(rev, a.Arg)
		cur = a.Fn
	}
	out := make([]ast.Expr, len(rev))
	for i, a := range rev {
		out[len(rev)-1-i] = a
	}
	return out
}
func appHead(e *ast.App) ast.Expr {
	var cur ast.Expr = e
	for {
		a, ok := cur.(*ast.App)
		if !ok {
			return cur
		}
		cur = a.Fn
	}
}

func (g *generator) handle(e *ast.Handle) types.Type {
	result := g.ck.Sup.FreshVar(types.General)
	if len(e.Clauses) == 0 {
		return result
	}
	first := g.ck.Operations[e.Clauses[0].Op]
	if first == nil {
		g.errs = append(g.errs, diag.Errorf(e.Clauses[0].OpSpan, "UNKNOWN OPERATION", "I don't know an operation named `%s`.", e.Clauses[0].Op))
		g.expr(e.Body)
		return result
	}
	if first.Owner == g.ck.IO {
		g.errs = append(g.errs, diag.Errorf(e.Sp, "BUILTIN IO HANDLING NOT READY", "Handlers for builtin IO are staged until polymorphic print evidence is available."))
	}
	residualVar := g.ck.Sup.FreshVar(types.RowVar)
	residual := types.Row{Tail: residualVar}
	labelArgs := make([]types.Type, len(first.Owner.Params))
	for i := range labelArgs {
		labelArgs[i] = g.ck.Sup.FreshVar(types.General)
	}
	label := types.EffLabel{Unique: first.Owner.Unique, Name: first.Owner.Name, Args: labelArgs}
	savedAmbient := g.ambient
	g.ambient = types.Row{Labels: []types.EffLabel{label}, Tail: residualVar}
	bodyTy := g.expr(e.Body)
	g.ambient = residual
	info := &HandlerInfo{Effect: label, Result: result, BodyResult: bodyTy}
	seen := map[string]bool{}
	for i := range e.Clauses {
		cl := &e.Clauses[i]
		op := g.ck.Operations[cl.Op]
		if op == nil {
			g.errs = append(g.errs, diag.Errorf(cl.OpSpan, "UNKNOWN OPERATION", "I don't know an operation named `%s`.", cl.Op))
			continue
		}
		if op.Owner != first.Owner {
			g.errs = append(g.errs, diag.Errorf(cl.OpSpan, "MIXED HANDLER EFFECTS", "All clauses in a handler must belong to `%s`.", first.Owner.Name))
			continue
		}
		if seen[op.Name] {
			g.errs = append(g.errs, diag.Errorf(cl.OpSpan, "DUPLICATE HANDLER CLAUSE", "The operation `%s` is handled more than once.", op.Name))
			continue
		}
		seen[op.Name] = true
		inst := g.instantiate(op.Scheme)
		paramTys, opResult := peelOperation(inst, op.Arity)
		cur := inst
		var last *types.TFun
		for range op.Arity {
			last = cur.(*types.TFun)
			cur = last.Ret
		}
		if len(last.Eff.Labels) > 0 {
			for j, a := range last.Eff.Labels[0].Args {
				if j < len(label.Args) {
					g.cs = append(g.cs, Constraint{Left: a, Right: label.Args[j], Span: cl.OpSpan, Why: Why{Kind: WhyEffectMismatch}})
				}
			}
		}
		if len(cl.Params) != op.Arity {
			g.errs = append(g.errs, diag.Errorf(cl.OpSpan, "HANDLER ARITY", "The operation `%s` takes %d argument(s), but this clause has %d.", op.Name, op.Arity, len(cl.Params)))
		}
		scope := &blockScope{parent: g.locals, names: map[string]types.Scheme{}}
		g.locals = scope
		for j, p := range cl.Params {
			var pt types.Type = g.ck.Sup.FreshVar(types.General)
			if j < len(paramTys) {
				pt = paramTys[j]
			}
			if p.Name == "()" {
				g.cs = append(g.cs, Constraint{Left: pt, Right: g.ck.B.Unit, Span: p.Sp, Why: Why{Kind: WhyPattern}})
			} else if p.Name != "_" {
				scope.names[p.Name] = types.Scheme{Body: pt}
			}
		}
		oldResume := g.resumeType
		g.resumeType = &types.TFun{Arg: opResult, Eff: residual, Ret: result}
		clTy := g.expr(cl.Body)
		g.resumeType = oldResume
		g.locals = scope.parent
		g.cs = append(g.cs, Constraint{Left: clTy, Right: result, Span: cl.Body.Span(), Why: Why{Kind: WhyCaseBranches}})
		if err := tailResume(cl.Body, true); err != "" {
			g.errs = append(g.errs, diag.Errorf(cl.Body.Span(), "GENERAL CONTINUATIONS NOT READY", "%s", err))
		}
		info.Clauses = append(info.Clauses, HandlerClauseInfo{Op: op, ParamTypes: paramTys, OpResult: opResult})
	}
	for _, op := range first.Owner.Ops {
		if !seen[op.Name] {
			g.errs = append(g.errs, diag.Errorf(e.Sp, "INCOMPLETE HANDLER", "The handler is missing a clause for `%s`.", op.Name))
		}
	}
	if e.Return != nil {
		scope := &blockScope{parent: g.locals, names: map[string]types.Scheme{}}
		g.locals = scope
		if e.Return.Param.Name == "()" {
			g.cs = append(g.cs, Constraint{Left: bodyTy, Right: g.ck.B.Unit, Span: e.Return.Param.Sp, Why: Why{Kind: WhyPattern}})
		} else if e.Return.Param.Name != "_" {
			scope.names[e.Return.Param.Name] = types.Scheme{Body: bodyTy}
		}
		rt := g.expr(e.Return.Body)
		g.locals = scope.parent
		g.cs = append(g.cs, Constraint{Left: rt, Right: result, Span: e.Return.Body.Span(), Why: Why{Kind: WhyCaseBranches}})
	} else {
		g.cs = append(g.cs, Constraint{Left: bodyTy, Right: result, Span: e.Body.Span(), Why: Why{Kind: WhyCaseBranches}})
	}
	g.ambient = savedAmbient
	g.ck.HandleInfos[e] = info
	return result
}

func peelOperation(t types.Type, n int) ([]types.Type, types.Type) {
	args := make([]types.Type, 0, n)
	for range n {
		f, ok := t.(*types.TFun)
		if !ok {
			return args, t
		}
		args = append(args, f.Arg)
		t = f.Ret
	}
	return args, t
}

func tailResume(e ast.Expr, tail bool) string {
	if a, ok := e.(*ast.App); ok {
		if _, yes := a.Fn.(*ast.Resume); yes {
			if s := tailResume(a.Arg, false); s != "" {
				return s
			}
			if !tail {
				return "`resume` must be the final action on every reachable clause path."
			}
			return ""
		}
	}
	switch x := e.(type) {
	case *ast.If:
		if s := tailResume(x.Cond, false); s != "" {
			return s
		}
		if s := tailResume(x.Then, tail); s != "" {
			return s
		}
		return tailResume(x.Else, tail)
	case *ast.Case:
		if s := tailResume(x.Scrutinee, false); s != "" {
			return s
		}
		for _, b := range x.Branches {
			if s := tailResume(b.Body, tail); s != "" {
				return s
			}
		}
		return ""
	case *ast.Block:
		if len(x.Items) > 0 {
			for _, item := range x.Items {
				var q ast.Expr
				if item.Expr != nil {
					q = item.Expr
				} else {
					q = x.Binds[item.BindIndex].Body
				}
				if s := tailResume(q, false); s != "" {
					return s
				}
			}
		} else {
			for _, b := range x.Binds {
				if s := tailResume(b.Body, false); s != "" {
					return s
				}
			}
		}
		return tailResume(x.Result, tail)
	case *ast.App:
		if s := tailResume(x.Fn, false); s != "" {
			return s
		}
		return tailResume(x.Arg, false)
	case *ast.BinOp:
		if s := tailResume(x.L, false); s != "" {
			return s
		}
		return tailResume(x.R, false)
	}
	if tail {
		return "Every operation-clause path must end with exactly one call to `resume`."
	}
	return ""
}

func (g *generator) operationSpine(e *ast.App) (*types.EffectOp, int) {
	n := 0
	var cur ast.Expr = e
	for {
		a, ok := cur.(*ast.App)
		if !ok {
			break
		}
		n++
		cur = a.Fn
	}
	v, ok := cur.(*ast.Var)
	if !ok {
		return nil, n
	}
	if _, local := g.locals.lookup(v.Name); local {
		return nil, n
	}
	return g.ck.Operations[v.Name], n
}

// function checks a function definition (top-level or block-local): the
// name is pre-bound to a fresh monotype in the same scope as the params so
// the body's self-references type — monomorphic recursion. The fresh var
// lives in the block scope, never in Env, so failed REPL definitions need
// no rollback and redefinition resolves self-references to the new body.
func (g *generator) function(name string, nameSpan source.Span, params []ast.Param, body ast.Expr) types.Type {
	self := g.ck.Sup.FreshVar(types.General)
	scope := &blockScope{parent: g.locals, names: map[string]types.Scheme{name: {Body: self}}}
	g.locals = scope
	defer func() { g.locals = scope.parent }()

	paramTys := g.bindParams(scope, params)
	savedAmbient := g.ambient
	bodyAmbient := types.Row{Tail: g.ck.Sup.FreshVar(types.RowVar)}
	g.ambient = bodyAmbient
	bodyTy := g.expr(body)
	g.ambient = savedAmbient

	funTy := g.wrapFunction(paramTys, bodyTy, bodyAmbient)
	g.cs = append(g.cs, Constraint{Left: self, Right: funTy, Span: nameSpan,
		Why: Why{Kind: WhyRecursion, Name: name}})
	return funTy
}

func (g *generator) wrapFunction(params []types.Type, ret types.Type, bodyRow types.Row) types.Type {
	funTy := ret
	for i := len(params) - 1; i >= 0; i-- {
		eff := types.Row{Tail: g.ck.Sup.FreshVar(types.RowVar)}
		if i == len(params)-1 {
			eff = bodyRow
		}
		funTy = &types.TFun{Arg: params[i], Eff: eff, Ret: funTy}
	}
	return funTy
}

// bindParams enters parameters into scope with the no-shadowing rule:
// duplicates in the list, the function's own name, enclosing locals, and
// top-level names are all rejected.
func (g *generator) bindParams(scope *blockScope, params []ast.Param) []types.Type {
	tys := make([]types.Type, len(params))
	for i, p := range params {
		if p.Name == "_" {
			tys[i] = g.ck.Sup.FreshVar(types.General)
			continue
		}
		if _, dup := g.locals.lookup(p.Name); dup || g.ck.Env.Has(p.Name) {
			g.errs = append(g.errs, diag.Errorf(p.Sp, "SHADOWING",
				"The parameter `%s` shadows a name that is already defined —\nfango does not allow shadowing. Choose a different name.", p.Name))
		}
		pv := g.ck.Sup.FreshVar(types.General)
		scope.names[p.Name] = types.Scheme{Body: pv}
		tys[i] = pv
	}
	return tys
}

// block checks a statement body: each binding is a solve-at-binding point
// in principle (monomorphic until S5), scoped sequentially, with shadowing
// forbidden against both earlier bindings and the top level.
func (g *generator) block(e *ast.Block) types.Type {
	g.locals = &blockScope{parent: g.locals, names: map[string]types.Scheme{}}
	defer func() { g.locals = g.locals.parent }()

	items := e.Items
	if len(items) == 0 {
		for i := range e.Binds {
			items = append(items, ast.BlockItem{BindIndex: i})
		}
	}
	for _, item := range items {
		if item.Expr != nil {
			st := g.expr(item.Expr)
			g.cs = append(g.cs, Constraint{Left: st, Right: g.ck.B.Unit, Span: item.Expr.Span(), Why: Why{Kind: WhyDeclBody}})
			continue
		}
		bind := &e.Binds[item.BindIndex]
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
		var annVars []*types.TVar
		if bind.Ann != nil {
			// Skolemize-and-unify, as at the top level.
			annScope := g.ck.NewAnnScope()
			annTy, annErrs := g.ck.ResolveTypeExpr(bind.Ann.Type, annScope)
			g.errs = append(g.errs, annErrs...)
			if annTy != nil {
				g.cs = append(g.cs, Constraint{Left: annTy, Right: ty,
					Span: bind.Body.Span(), Why: Why{Kind: WhyAnnotation, Name: bind.Name}})
				ty = annTy
				annVars = annScope.Minted()
			}
		}
		scheme := types.Scheme{Body: ty}
		// Monomorphism restriction for block bindings: only syntactic
		// functions and lambda literals generalize locally. A generalized
		// binding lambda-lifts and re-evaluates per use (§8.4) — fine for
		// function values, but a *value* binding's whole point is §3.6's
		// eager evaluate-once-at-its-line semantics, which Number-kinded
		// generalization (`k = 10 : number`) would otherwise silently break
		// for every numeric local. Top-level values still generalize (§8.4
		// accepts nullary generic values; S7's purity check keeps
		// re-evaluation unobservable).
		_, isLambda := bind.Body.(*ast.Lambda)
		if len(bind.Params) > 0 || isLambda {
			// Solve-at-binding (§7.2): discharge this binding's constraints
			// into the substitution now, so generalization sees solved types
			// and later bindings can use this one polymorphically.
			g.solveHere()
			// Skolem escape: this annotation's variables must not leak into
			// enclosing bindings (that would grant the enclosing definition
			// polymorphism its body doesn't have).
			for _, sk := range annVars {
				if g.scopeMentions(sk.ID) {
					g.errs = append(g.errs, diag.Errorf(bind.Ann.Type.Span(), "ANNOTATION TOO GENERAL",
						"The annotation for `%s` claims a type variable that the enclosing\ndefinition pins down — the annotation is more general than the body\nallows.", bind.Name))
				}
			}
			scheme = g.ck.generalize(ty, g.scopeFreeIDs())
		}
		g.ck.BindTypes[bind] = ty
		g.ck.BindSchemes[bind] = scheme
		g.locals.names[bind.Name] = scheme
	}
	return g.expr(e.Result)
}

// solveHere discharges the accumulated constraints into the checker's
// substitution — the solve-at-binding point.
func (g *generator) solveHere() {
	if len(g.cs) == 0 {
		return
	}
	sub, _, errs := Solve(g.cs, nil, g.ck.Sub, g.ck.B, g.ck.Sup)
	g.ck.Sub = sub
	g.errs = append(g.errs, errs...)
	g.cs = nil
}

// scopeMentions reports whether any enclosing local binding's zonked type
// mentions the variable id.
func (g *generator) scopeMentions(id int) bool {
	for s := g.locals; s != nil; s = s.parent {
		for _, sch := range s.names {
			if g.ck.mentionsVar(sch.Body, id) {
				return true
			}
		}
	}
	return false
}

// caseExpr constrains a case: every pattern matches the scrutinee's type,
// every branch body matches the case's result type. Pattern variables scope
// over their branch's body only.
func (g *generator) caseExpr(e *ast.Case) types.Type {
	scrutTy := g.expr(e.Scrutinee)
	resultTy := g.ck.Sup.FreshVar(types.General)
	for i := range e.Branches {
		br := &e.Branches[i]
		scope := &blockScope{parent: g.locals, names: map[string]types.Scheme{}}
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
		scope.names[p.Name] = types.Scheme{Body: pv}
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
		fields, result := g.instantiateCtor(info)
		if len(p.Args) != len(fields) {
			g.errs = append(g.errs, diag.Errorf(p.Span(), "PATTERN ARITY",
				"The `%s` constructor takes %d argument(s), but this pattern\ngives it %d.", p.Name, len(fields), len(p.Args)))
			for _, a := range p.Args {
				g.pattern(a, scope)
			}
			return result
		}
		for i, a := range p.Args {
			argTy := g.pattern(a, scope)
			g.cs = append(g.cs, Constraint{
				Left: argTy, Right: fields[i], Span: a.Span(), Why: Why{Kind: WhyPattern},
			})
		}
		return result
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

// instantiate replaces a scheme's quantified variables with fresh metas
// (kinds preserved) — each use site of a polymorphic name gets its own copy.
func (g *generator) instantiate(s types.Scheme) types.Type {
	if len(s.Vars) == 0 {
		return s.Body
	}
	m := make(map[int]types.Type, len(s.Vars))
	for _, v := range s.Vars {
		m[v.ID] = g.ck.Sup.FreshVar(v.Kind)
	}
	return types.SubstRigid(s.Body, m)
}

// instantiateCtor returns a constructor's field and result types with the
// owning type's parameters replaced by fresh metas — `Just : ∀a. a -> Maybe a`
// used at a fresh `a` per occurrence, in expressions and patterns alike.
func (g *generator) instantiateCtor(info *types.CtorInfo) ([]types.Type, types.Type) {
	adt := g.ck.ADTs[info.Result.Unique]
	if adt == nil || len(adt.Params) == 0 {
		return info.Fields, info.Result
	}
	m := make(map[int]types.Type, len(adt.Params))
	args := make([]types.Type, len(adt.Params))
	for i, p := range adt.Params {
		f := g.ck.Sup.FreshVar(p.Kind)
		m[p.ID] = f
		args[i] = f
	}
	fields := make([]types.Type, len(info.Fields))
	for i, f := range info.Fields {
		fields[i] = types.SubstRigid(f, m)
	}
	result := &types.TCon{Unique: info.Result.Unique, Name: info.Result.Name, Args: args}
	return fields, result
}
