// Package infer is the constraint-based type checker: constraint generation
// (constrain.go), unification (unify.go), and solving (solve.go). The
// explicit constraint list — rather than Algorithm W's inline unification —
// is what buys good errors now and typeclasses later.
package infer

import (
	"strings"

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
	// Include is used only for effect rows: Left must be a subset of Right.
	// It expresses composition without claiming the surrounding function call
	// performs exactly the callee's effects.
	Include bool
}

// Env maps top-level names to schemes; block scopes and function parameters
// are tracked separately by the constraint generator.
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
	Classes         map[string]*types.ClassInfo
	Methods         map[string]*types.MethodInfo
	Instances       []*InstanceInfo
	InstanceImports map[string]map[string]bool
	CurrentOwner    string
	PendingPreds    []types.Pred
	ExprSchemes     map[ast.Expr]types.Scheme
	Desugared       map[ast.Expr]ast.Expr
	PreludeInfos    []DeclInfo
	Aliases         map[string]string
	Sup             *types.Supply
	B               *types.Builtins
	Env             *Env
	Sub             Subst
	ExprTypes       map[ast.Expr]types.Type

	// Ctors is the constructor table (doc/design.md, "Type inference"), keyed by constructor name —
	// names are unique per module (types and constructors live in separate
	// namespaces, doc/reference.md, "Algebraic data types and matching"). Seeded with the builtin Bool constructors.
	Ctors map[string]*types.CtorInfo

	// ADTs maps a declared type's Unique to its constructor-table entry;
	// ADTOrder keeps declaration order for deterministic codegen. Bool is
	// predefined as an ordinary ADT (codegen special-cases it, doc/design.md, "Go backend and runtime") and is
	// deliberately absent from ADTOrder — no Go type is ever emitted for it.
	ADTs     map[int]*types.ADTInfo
	ADTOrder []*types.ADTInfo

	// TypeNames maps surface type names to their current types — the type
	// table's embryo, exactly as Ctors is for constructors. `type`
	// declarations and REPL generations extend it; identity stays the
	// TCon Unique underneath.
	TypeNames       map[string]types.Type
	Effects         map[string]*types.EffectInfo
	EffectsByUnique map[int]*types.EffectInfo
	Operations      map[string]*types.EffectOp
	IO              *types.EffectInfo
	Natives         map[string]*types.NativeInfo
	Operators       map[string]string
	BinNatives      map[*ast.BinOp]*types.NativeInfo

	OpCalls     map[*ast.App]*types.EffectOp
	HandleInfos map[*ast.Handle]*HandlerInfo
	ResumeCalls map[*ast.App]bool

	// Workers maps top-level function names to their syntactic parameter
	// count — the arity that drives doc/design.md, "Go backend and runtime" saturation analysis. Session
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
	// elaboration lifts a binding whose scheme quantifies (doc/design.md, "Go backend and runtime").
	BindSchemes map[*ast.LocalBind]types.Scheme

	// LiftGen numbers lambda-lifted definitions session-wide, so REPL
	// inputs across a session never collide (elaborate/lift.go).
	LiftGen int

	// MonoValues applies the block-binding monomorphism restriction to
	// top-level value declarations too. The REPL sets it: a prompt value is
	// a lazy memo cell (doc/design.md, "Interpreter and REPL"), evaluated once, so its type must stay a
	// monotype — a generalized value re-evaluates per use, which would
	// observably interact with redefinition (breaking the "old closures keep
	// old values" rule in doc/design.md, "Interpreter and REPL"). Batch modules are immutable, so re-evaluation is
	// unobservable there and top-level values generalize per doc/design.md, "Go backend and runtime".
	MonoValues bool

	// EntryName is the canonical symbol selected by the batch graph loader.
	// It remains "main" for the REPL and headerless single-file programs.
	EntryName string
}

func NewChecker(sup *types.Supply, b *types.Builtins, env *Env) *Checker {
	ck := &Checker{
		Classes: map[string]*types.ClassInfo{}, Methods: map[string]*types.MethodInfo{},
		ExprSchemes: map[ast.Expr]types.Scheme{},
		Desugared:   map[ast.Expr]ast.Expr{},
		Aliases:     map[string]string{},
		Sup:         sup,
		B:           b,
		Env:         env,
		Sub:         Subst{},
		ExprTypes:   map[ast.Expr]types.Type{},
		Ctors:       map[string]*types.CtorInfo{},
		ADTs:        map[int]*types.ADTInfo{},
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
		Natives:         map[string]*types.NativeInfo{},
		Operators:       map[string]string{},
		BinNatives:      map[*ast.BinOp]*types.NativeInfo{},
		OpCalls:         map[*ast.App]*types.EffectOp{},
		HandleInfos:     map[*ast.Handle]*HandlerInfo{},
		ResumeCalls:     map[*ast.App]bool{},
		Workers:         map[string]int{},
		BindTypes:       map[*ast.LocalBind]types.Type{},
		PatTypes:        map[ast.Pattern]types.Type{},
		BindSchemes:     map[*ast.LocalBind]types.Scheme{},
		EntryName:       "main",
	}
	// Bool is an ordinary ADT in the checker (doc/design.md, "Type inference") — patterns, case
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
// structure used by binding-boundary generalization.
func (ck *Checker) Module(m *ast.Module) ([]DeclInfo, []diag.Error) {
	if m.InstanceImports != nil {
		ck.InstanceImports = m.InstanceImports
	}
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
	// Native schemes and operator bindings are graph-wide metadata and must
	// be available before ordinary definitions are inferred.
	for _, d := range m.Decls {
		vd, ok := d.(*ast.ValueDecl)
		if !ok || vd.Native == nil {
			continue
		}
		if ck.Env.Has(vd.Name) {
			errs = append(errs, diag.Errorf(vd.NameSpan, "MULTIPLE DEFINITIONS", "`%s` is defined more than once.", vd.Name))
			continue
		}
		errs = append(errs, ck.declareNative(vd)...)
	}
	for _, d := range m.Decls {
		inf, ok := d.(*ast.InfixDecl)
		if !ok {
			continue
		}
		if old := ck.Operators[inf.Op]; old != "" && old != inf.Target {
			errs = append(errs, diag.Errorf(inf.OpSpan, "NATIVE DECLARATION", "The operator (%s) is bound more than once.", inf.Op))
			continue
		}
		ck.Operators[inf.Op] = inf.Target
	}
	for _, d := range m.Decls {
		if cl, ok := d.(*ast.ClassDecl); ok {
			errs = append(errs, ck.ClassDecl(cl)...)
			continue
		}
		if td, ok := d.(*ast.TypeDecl); ok && len(td.Deriving) > 0 {
			ck.CurrentOwner = symbolModule(td.Name)
			ds, es := ck.DeriveDecl(td)
			infos = append(infos, ds...)
			errs = append(errs, es...)
			continue
		}
		if in, ok := d.(*ast.InstanceDecl); ok {
			ck.CurrentOwner = in.Owner
			ds, es := ck.InstanceDecl(in)
			infos = append(infos, ds...)
			errs = append(errs, es...)
			continue
		}
		vd, ok := d.(*ast.ValueDecl)
		if !ok || vd.Native != nil {
			continue
		}
		// Duplicate definitions are a batch-compilation error only: the
		// REPL redefines names freely (generational cells).
		if ck.Env.Has(vd.Name) {
			errs = append(errs, diag.Errorf(vd.NameSpan, "MULTIPLE DEFINITIONS",
				"`%s` is defined more than once.", vd.Name))
		}
		ck.CurrentOwner = symbolModule(vd.Name)
		info, declErrs := ck.Decl(vd)
		errs = append(errs, declErrs...)
		infos = append(infos, info)
	}
	return infos, errs
}

func (ck *Checker) declareNative(d *ast.ValueDecl) []diag.Error {
	if d.Ann == nil {
		return []diag.Error{diag.Errorf(d.NameSpan, "NATIVE DECLARATION", "A native declaration requires a type annotation.")}
	}
	if len(d.Ann.Preds) > 0 {
		return []diag.Error{diag.Errorf(d.NameSpan, "NATIVE DECLARATION", "Native declarations cannot require class constraints; use an ordinary constrained wrapper.")}
	}
	scope := ck.newNativeAnnScope()
	ty, errs := ck.ResolveTypeExpr(d.Ann.Type, scope)
	if ty == nil {
		return errs
	}
	arity := 0
	for t := ty; ; {
		f, ok := t.(*types.TFun)
		if !ok {
			break
		}
		arity++
		t = f.Ret
	}
	if arity == 0 {
		errs = append(errs, diag.Errorf(d.NameSpan, "NATIVE DECLARATION", "A native declaration must have a function type."))
	}
	vars := scope.Minted()
	sch := types.Scheme{Vars: vars, Preds: scope.Preds(), Body: ty}
	module := symbolModule(d.Name)
	if d.Native.Module != "" {
		module = d.Native.Module
	}
	n := &types.NativeInfo{Name: d.Name, Module: module, Scheme: sch, Arity: arity, Template: d.Native.Template}
	ck.Natives[d.Name] = n
	ck.Env.Bind(d.Name, sch)
	ck.Workers[d.Name] = arity
	return errs
}

func symbolModule(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[:i]
	}
	return ""
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
	if ed.Name == "IO.IO" || ed.Name == "IO" {
		ck.IO = info
	}
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
		if op.Native != nil {
			scope.native = true
		}
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
		sch := types.Scheme{Vars: vars, Preds: scope.Preds(), Body: ty}
		params := make([]types.Type, len(arrows))
		for i, a := range arrows {
			params[i] = a.Arg
		}
		local := append([]*types.TVar(nil), scope.Minted()...)
		meta := &types.EffectOp{Owner: info, Index: len(info.Ops), Name: op.Name, Scheme: sch,
			Arity: len(arrows), ParamTypes: params, ResultType: arrows[len(arrows)-1].Ret, LocalVars: local}
		if op.Native != nil {
			n := &types.NativeInfo{Name: op.Name, Module: symbolModule(op.Name), Scheme: sch, Arity: len(arrows), Template: op.Native.Template, Effect: info}
			meta.Native = n
			ck.Natives[op.Name] = n
		}
		info.Ops = append(info.Ops, meta)
		ck.Operations[op.Name] = meta
		ck.Env.Bind(op.Name, sch)
	}
	return errs
}

// TypeDecl checks and installs one type declaration — the REPL's entry
// point, where redefinition is allowed (a fresh generation, doc/design.md, "Interpreter and REPL").
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
func (ck *Checker) DeclWhere(d *ast.ValueDecl, allowEffects bool) (DeclInfo, []diag.Error) {
	var errs []diag.Error
	var given []types.Pred
	isMain := d.Name == ck.EntryName
	if isMain && len(d.Params) > 1 {
		errs = append(errs, diag.Errorf(d.NameSpan, "MAIN TAKES NO PARAMETERS",
			"`main` may be a value or a one-argument Unit function."))
	}
	if isMain && len(d.Params) == 1 && d.Params[0].Name != "_" && d.Params[0].Name != "()" {
		errs = append(errs, diag.Errorf(d.Params[0].Sp, "MAIN TAKES NO PARAMETERS", "Function-style `main` must use `main()` or discard its Unit argument with `_`."))
	}

	var annTy types.Type
	var annScope *TypeVars
	if d.Ann != nil {
		annScope = ck.NewAnnScope()
		var annErrs []diag.Error
		annTy, annErrs = ck.ResolveTypeExpr(d.Ann.Type, annScope)
		errs = append(errs, annErrs...)
		given, annErrs = ck.ResolvePreds(d.Ann.Preds, annScope)
		errs = append(errs, annErrs...)
	}
	g := &generator{ck: ck, ambient: types.Row{Tail: ck.Sup.FreshVar(types.RowVar)}}
	var ty types.Type
	if len(d.Params) == 0 {
		ty = g.expr(d.Body)
		if !isMain && allowEffects {
			g.cs = append(g.cs, Constraint{Left: g.ambient, Right: types.Row{}, Span: d.Body.Span(), Why: Why{Kind: WhyEffectEscapes}})
		}
	} else if annTy != nil {
		ty = g.functionWithAnnotatedParams(d.Name, d.NameSpan, d.Params, d.Body, annTy)
		if isMain && len(d.Params) == 1 {
			want := &types.TFun{Arg: ck.B.Unit, Eff: types.Row{Labels: []types.EffLabel{{Unique: ck.IO.Unique, Name: ck.IO.Name}}}, Ret: ck.B.Unit}
			g.cs = append(g.cs, Constraint{Left: ty, Right: want, Span: d.Body.Span(), Why: Why{Kind: WhyAnnotation, Name: "main"}})
		}
	} else {
		ty = g.function(d.Name, d.NameSpan, d.Params, d.Body)
		if isMain && len(d.Params) == 1 {
			want := &types.TFun{Arg: ck.B.Unit, Eff: types.Row{Labels: []types.EffLabel{{Unique: ck.IO.Unique, Name: ck.IO.Name}}}, Ret: ck.B.Unit}
			g.cs = append(g.cs, Constraint{Left: ty, Right: want, Span: d.Body.Span(), Why: Why{Kind: WhyAnnotation, Name: "main"}})
		}
	}
	sub, _, solveErrs := Solve(g.cs, nil, ck.Sub, ck.B, ck.Sup)
	ck.Sub = sub
	errs = append(errs, g.errs...)
	errs = append(errs, solveErrs...)
	if d.Ann == nil && !isMain {
		ck.closeSingleRows(ty)
	}
	promptEffects := false
	if row, ok := ck.Sub.Apply(g.ambient).(types.Row); ok && len(row.Labels) > 0 {
		promptEffects = true
	}
	if !allowEffects && promptEffects {
		errs = append(errs, diag.Errorf(d.Body.Span(), "EFFECTFUL PROMPT DECLARATION", "Effectful declarations are not installed at the prompt; run the expression directly."))
	}
	if isMain && len(d.Params) == 0 {
		row := ck.Sub.Apply(g.ambient).(types.Row)
		for _, l := range row.Labels {
			if l.Unique != ck.IO.Unique {
				errs = append(errs, diag.Errorf(d.Body.Span(), "UNHANDLED EFFECT", "Legacy `main` may perform IO, but `%s` is not handled.", l.Name))
			}
		}
	}

	if d.Ann != nil && annTy != nil {
		// Skolemize-and-unify (doc/design.md, "Type inference"): the annotation's variables resolve to
		// fresh rigid skolems, atomic in unification, so an annotation
		// claiming more polymorphism than the body delivers errors here.
		if !sameKnownEffects(ck.Sub.Apply(annTy), ck.Sub.Apply(ty)) {
			errs = append(errs, diag.Errorf(d.Ann.Sp, "EFFECT MISMATCH",
				"The effect row in the annotation for `%s` does not exactly match the effects performed by its body.", d.Name))
		}
		c := Constraint{Left: annTy, Right: ty, Span: d.Body.Span(), Why: Why{Kind: WhyAnnotation, Name: d.Name}}
		sub, _, solveErrs := Solve([]Constraint{c}, nil, ck.Sub, ck.B, ck.Sup)
		ck.Sub = sub
		errs = append(errs, solveErrs...)
		ty = annTy
	}
	info := DeclInfo{Name: d.Name, NameSpan: d.NameSpan, Params: d.Params, Type: ty, Body: d.Body}
	_, isLambda := d.Body.(*ast.Lambda)
	switch {
	case isMain:
		// main is the program's ground entry point (doc/design.md, "Go backend and runtime": a function form is
		// invoked inside func main()) — it never generalizes. Unconstrained
		// variables in its type default like interior ones (Number → Int,
		// General → Unit), so `main = 1 + 2` stays an Int program.
		info.Scheme = types.Scheme{Body: ty}
	case ck.MonoValues && len(d.Params) == 0 && !isLambda:
		info.Scheme = types.Scheme{Body: ty}
	default:
		info.Scheme = ck.generalize(ty, nil)
	}
	var predErrs []diag.Error
	info.Scheme, predErrs = ck.qualify(info.Scheme, g.preds, given, d.Ann != nil, d.NameSpan)
	errs = append(errs, predErrs...)
	return info, errs
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

// closeSingleRows makes an inferred arrow pure when its open row occurs only
// once in the type. A row shared between a callback and the surrounding call
// remains quantified, preserving inferred higher-order effect polymorphism.
func (ck *Checker) closeSingleRows(t types.Type) {
	t = ck.Sub.Apply(t)
	counts := map[int]int{}
	var count func(types.Type)
	count = func(t types.Type) {
		switch t := t.(type) {
		case *types.TVar:
			if t.Kind == types.RowVar && !t.Rigid {
				counts[t.ID]++
			}
		case *types.TCon:
			for _, a := range t.Args {
				count(a)
			}
		case *types.TFun:
			count(t.Arg)
			count(t.Eff)
			count(t.Ret)
		case types.Row:
			for _, l := range t.Labels {
				for _, a := range l.Args {
					count(a)
				}
			}
			if t.Tail != nil {
				count(t.Tail)
			}
		}
	}
	count(t)
	for id, n := range counts {
		if n == 1 {
			ck.Sub[id] = types.Row{}
		}
	}
}

// Expr checks an expression with prompt semantics (print allowed) — the
// REPL's expression entry point.
func (ck *Checker) Expr(e ast.Expr) (types.Type, []diag.Error) {
	return ck.ExprWhere(e, true)
}

// ExprWhere generates constraints for one expression and solves them into
// the checker's substitution. allowEffects controls the REPL declaration
// policy; expression checking itself always uses ordinary effect rows.
func (ck *Checker) ExprWhere(e ast.Expr, _ bool) (types.Type, []diag.Error) {
	g := &generator{ck: ck, ambient: types.Row{Tail: ck.Sup.FreshVar(types.RowVar)}}
	ty := g.expr(e)
	var preds []types.Pred // the typeclass seam: always empty in the MVP
	sub, residual, solveErrs := Solve(g.cs, preds, ck.Sub, ck.B, ck.Sup)
	ck.Sub = sub
	_ = residual
	errs := append(g.errs, solveErrs...)
	left, es := ck.reduceObligations(g.preds, nil)
	ids := map[int]bool{}
	collectVarIDs(ck.Sub.Apply(ty), ids)
	var ambiguous, visible []types.Pred
	for _, p := range left {
		if v, ok := p.Ty.(*types.TVar); ok && !ids[v.ID] {
			ambiguous = append(ambiguous, p)
		} else {
			visible = append(visible, p)
		}
	}
	es = append(es, ck.DefaultPreds(ambiguous, e.Span())...)
	// Defaulting may have solved metavariables a structural pred was blocked
	// on; reduce once more so the choice lands before the prompt reports it.
	var vobs []predObligation
	for _, p := range visible {
		vobs = append(vobs, predObligation{pred: p, span: e.Span()})
	}
	visible, ves := ck.reduceObligations(vobs, nil)
	es = append(es, ves...)
	ck.PendingPreds = ck.NormalizePreds(visible)
	return ty, append(errs, es...)
}

type predObligation struct {
	pred types.Pred
	span source.Span
	op   string
}

type generator struct {
	ck         *Checker
	locals     *blockScope
	cs         []Constraint
	errs       []diag.Error
	ambient    types.Row
	resumeType types.Type
	preds      []predObligation
}

func (g *generator) isDefaultPrint(op *types.EffectOp) bool {
	return op.Owner == g.ck.IO && types.SurfaceName(op.Name) == "print"
}

// blockScope is a block's local bindings, as schemes: parameters and
// pre-bound recursive names are trivial (monotype) schemes, while generalized
// generalized block bindings instantiate per use like top-level names.
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

func (g *generator) expr(e ast.Expr) types.Type { return g.exprWant(e, nil) }

func (g *generator) exprWant(e ast.Expr, want types.Type) types.Type {
	var ty types.Type
	switch e := e.(type) {
	case *ast.IntLit:
		if e.Raw {
			ty = g.ck.B.Int
			break
		}
		app := &ast.App{Fn: &ast.Var{Name: "Basics.fromInt", Sp: e.Sp}, Arg: &ast.IntLit{Value: e.Value, Sp: e.Sp, Raw: true}}
		g.ck.Desugared[e] = app
		ty = g.expr(app)
	case *ast.FloatLit:
		ty = g.ck.B.Float
	case *ast.StringLit:
		ty = g.ck.B.String
	case *ast.UnitLit:
		ty = g.ck.B.Unit
	case *ast.Var:
		if name := g.ck.Aliases[e.Name]; name != "" {
			e.Name = name
		}
		if localScheme, ok := g.locals.lookup(e.Name); ok {
			g.ck.ExprSchemes[e] = localScheme
			ty = g.instantiateAt(localScheme, e.Sp, e.Name)
		} else {
			scheme, ok := g.ck.Env.Lookup(e.Name)
			if !ok {
				g.errs = append(g.errs, diag.Errorf(e.Sp, "NAMING ERROR",
					"I don't know a value named `%s`.", e.Name))
				ty = g.ck.Sup.FreshVar(types.General) // recover with a hole
				break
			}
			g.ck.ExprSchemes[e] = scheme
			// An operation in value position is not yet being performed. Its
			// predicates are checked when a saturated operation spine is formed;
			// this also lets main's ordinary shape check diagnose `main = print`.
			if op := g.ck.Operations[e.Name]; op != nil {
				ty = g.instantiate(scheme)
			} else {
				ty = g.instantiateAt(scheme, e.Sp, e.Name)
			}
			if op := g.ck.Operations[e.Name]; op != nil && len(op.LocalVars) > 0 && op.Native == nil && !g.isDefaultPrint(op) {
				g.errs = append(g.errs, diag.Errorf(e.Sp, "OPERATION POLYMORPHISM NOT READY", "The operation `%s` has operation-local polymorphism that cannot be used at runtime until checkpoint 3.", op.Name))
			}
		}
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
			inst := g.instantiateAt(op.Scheme, e.Span(), op.Name)
			g.ck.ExprTypes[appHead(e)] = inst
			params, result := peelOperation(inst, op.Arity)
			args := appArgs(e)
			for i, a := range args {
				at := g.exprWant(a, params[i])
				g.cs = append(g.cs, Constraint{Left: at, Right: params[i], Span: a.Span(), Why: Why{Kind: WhyCall}})
			}
			cur := inst
			var last *types.TFun
			for range op.Arity {
				last = cur.(*types.TFun)
				cur = last.Ret
			}
			g.cs = append(g.cs, Constraint{Left: last.Eff, Right: g.ambient, Span: e.Span(), Why: Why{Kind: WhyCall}, Include: true})
			ty = result
			g.ck.OpCalls[e] = op
			if len(op.LocalVars) > 0 && op.Native == nil && !g.isDefaultPrint(op) {
				g.errs = append(g.errs, diag.Errorf(e.Span(), "OPERATION POLYMORPHISM NOT READY", "The operation `%s` has result polymorphism that is staged until checkpoint 3.", op.Name))
			}
			break
		}
		fnTy := g.expr(e.Fn)
		var argWant types.Type
		if fn, ok := g.ck.Sub.Apply(fnTy).(*types.TFun); ok {
			argWant = fn.Arg
		}
		argTy := g.exprWant(e.Arg, argWant)
		r := g.ck.Sup.FreshVar(types.General)
		callEff := types.Row{Tail: g.ck.Sup.FreshVar(types.RowVar)}
		g.cs = append(g.cs, Constraint{
			Left:  fnTy,
			Right: &types.TFun{Arg: argTy, Eff: callEff, Ret: r},
			Span:  e.Fn.Span(),
			Why:   Why{Kind: WhyCall},
		})
		g.cs = append(g.cs, Constraint{Left: callEff, Right: g.ambient, Span: e.Span(), Why: Why{Kind: WhyCall}, Include: true})
		ty = r
		if op, n := g.operationSpine(e); op != nil && n < op.Arity {
			g.ck.OpCalls[e] = op
		}
		if _, ok := e.Fn.(*ast.Resume); ok {
			g.ck.ResumeCalls[e] = true
		}
	case *ast.Neg:
		app := &ast.App{Fn: &ast.Var{Name: "Basics.negate", Sp: e.Sp}, Arg: e.Operand}
		g.ck.Desugared[e] = app
		ty = g.expr(app)
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
		ty = g.block(e, want)
	case *ast.Case:
		ty = g.caseExpr(e)
	case *ast.Lambda:
		scope := &blockScope{parent: g.locals, names: map[string]types.Scheme{}}
		g.locals = scope
		paramTys := g.bindParams(scope, e.Params)
		savedAmbient := g.ambient
		bodyAmbient := types.Row{Tail: g.ck.Sup.FreshVar(types.RowVar)}
		g.ambient = bodyAmbient
		bodyTy := g.expr(e.Body)
		g.ambient = savedAmbient
		g.locals = scope.parent
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
		if len(op.LocalVars) > 0 && op.Native == nil && !g.isDefaultPrint(op) {
			g.errs = append(g.errs, diag.Errorf(cl.OpSpan, "OPERATION POLYMORPHISM NOT READY", "The operation `%s` has operation-local polymorphism that cannot be used at runtime until checkpoint 3.", op.Name))
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
				if _, dup := g.locals.lookup(p.Name); dup || g.ck.Env.Has(p.Name) {
					g.errs = append(g.errs, diag.Errorf(p.Sp, "SHADOWING", "The handler parameter `%s` shadows a name that is already defined.", p.Name))
				}
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
			if _, dup := g.locals.lookup(e.Return.Param.Name); dup || g.ck.Env.Has(e.Return.Param.Name) {
				g.errs = append(g.errs, diag.Errorf(e.Return.Param.Sp, "SHADOWING", "The handler return parameter `%s` shadows a name that is already defined.", e.Return.Param.Name))
			}
			scope.names[e.Return.Param.Name] = types.Scheme{Body: bodyTy}
		}
		rt := g.expr(e.Return.Body)
		g.locals = scope.parent
		g.cs = append(g.cs, Constraint{Left: rt, Right: result, Span: e.Return.Body.Span(), Why: Why{Kind: WhyCaseBranches}})
	} else {
		g.cs = append(g.cs, Constraint{Left: bodyTy, Right: result, Span: e.Body.Span(), Why: Why{Kind: WhyCaseBranches}})
	}
	// Only the handled label is removed. Any residual effects from the body,
	// clauses, or return clause compose into the surrounding expression.
	g.cs = append(g.cs, Constraint{Left: residual, Right: savedAmbient, Span: e.Span(), Why: Why{Kind: WhyCall}, Include: true})
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
	resumes, err := resumePaths(e, tail)
	if err != "" {
		return err
	}
	if tail && !resumes {
		return "Every operation-clause path must end with exactly one call to `resume`."
	}
	return ""
}

// resumePaths proves the current tail-resumptive discipline structurally. The bool is
// true only when every normal path through e terminates in one tail resume;
// any resume encountered in an evaluated subexpression is rejected. Nested
// lambdas are traversed too, so staging a resume in a closure cannot evade
// the check.
func resumePaths(e ast.Expr, tail bool) (bool, string) {
	if a, ok := e.(*ast.App); ok {
		if _, yes := a.Fn.(*ast.Resume); yes {
			if _, err := resumePaths(a.Arg, false); err != "" {
				return false, err
			}
			if !tail {
				return false, "`resume` must be the final action on every reachable clause path."
			}
			return true, ""
		}
	}
	nontail := func(q ast.Expr) string {
		_, err := resumePaths(q, false)
		return err
	}
	switch x := e.(type) {
	case *ast.If:
		if err := nontail(x.Cond); err != "" {
			return false, err
		}
		a, err := resumePaths(x.Then, tail)
		if err != "" {
			return false, err
		}
		b, err := resumePaths(x.Else, tail)
		return a && b, err
	case *ast.Case:
		if err := nontail(x.Scrutinee); err != "" {
			return false, err
		}
		all := len(x.Branches) > 0
		for _, b := range x.Branches {
			ok, err := resumePaths(b.Body, tail)
			if err != "" {
				return false, err
			}
			all = all && ok
		}
		return all, ""
	case *ast.Block:
		items := x.Items
		if len(items) == 0 {
			for i := range x.Binds {
				items = append(items, ast.BlockItem{BindIndex: i})
			}
		}
		for _, item := range items {
			q := item.Expr
			if q == nil {
				q = x.Binds[item.BindIndex].Body
			}
			if err := nontail(q); err != "" {
				return false, err
			}
		}
		return resumePaths(x.Result, tail)
	case *ast.App:
		if err := nontail(x.Fn); err != "" {
			return false, err
		}
		if err := nontail(x.Arg); err != "" {
			return false, err
		}
	case *ast.BinOp:
		if err := nontail(x.L); err != "" {
			return false, err
		}
		if err := nontail(x.R); err != "" {
			return false, err
		}
	case *ast.Neg:
		if err := nontail(x.Operand); err != "" {
			return false, err
		}
	case *ast.Lambda:
		// A resume in a closure is never the current clause's tail action.
		if err := nontail(x.Body); err != "" {
			return false, err
		}
	case *ast.Handle:
		// Its handled expression and return clause are evaluated as parts of
		// this expression. Inner operation clauses bind their own resume and
		// are checked independently by handle().
		if err := nontail(x.Body); err != "" {
			return false, err
		}
		if x.Return != nil {
			if err := nontail(x.Return.Body); err != "" {
				return false, err
			}
		}
	case *ast.Resume:
		return false, "`resume` must be applied to exactly one value."
	}
	return false, ""
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
	scope := &blockScope{parent: g.locals, names: map[string]types.Scheme{name: {Body: g.ck.Sup.FreshVar(types.General)}}}
	g.locals = scope
	defer func() { g.locals = scope.parent }()

	paramTys := g.bindParams(scope, params)
	savedAmbient := g.ambient
	bodyAmbient := types.Row{Tail: g.ck.Sup.FreshVar(types.RowVar)}
	resultTy := g.ck.Sup.FreshVar(types.General)
	funTy := g.wrapFunction(paramTys, resultTy, bodyAmbient)
	scope.names[name] = types.Scheme{Body: funTy}
	g.ambient = bodyAmbient
	bodyTy := g.expr(body)
	g.ambient = savedAmbient

	g.cs = append(g.cs, Constraint{Left: resultTy, Right: bodyTy, Span: nameSpan,
		Why: Why{Kind: WhyRecursion, Name: name}})
	return funTy
}

// functionWithAnnotatedParams uses only the annotation's argument types while
// independently inferring every arrow's effects. This lets callback effects
// flow into a higher-order body without allowing an overstated result row to
// manufacture effects the body never performs.
func (g *generator) functionWithAnnotatedParams(name string, nameSpan source.Span, params []ast.Param, body ast.Expr, ann types.Type) types.Type {
	scope := &blockScope{parent: g.locals, names: map[string]types.Scheme{name: {Body: g.ck.Sup.FreshVar(types.General)}}}
	g.locals = scope
	defer func() { g.locals = scope.parent }()
	cur := ann
	paramTys := make([]types.Type, len(params))
	for i, p := range params {
		fn, ok := cur.(*types.TFun)
		if !ok {
			g.errs = append(g.errs, diag.Errorf(nameSpan, "TYPE MISMATCH", "The annotation for `%s` has fewer function parameters than its definition.", name))
			return ann
		}
		paramTys[i] = fn.Arg
		if p.Name == "()" {
			g.cs = append(g.cs, Constraint{Left: fn.Arg, Right: g.ck.B.Unit, Span: p.Sp, Why: Why{Kind: WhyAnnotation, Name: name}})
		} else if p.Name != "_" {
			if _, dup := g.locals.lookup(p.Name); dup || g.ck.Env.Has(p.Name) {
				g.errs = append(g.errs, diag.Errorf(p.Sp, "SHADOWING", "The parameter `%s` shadows a name that is already defined.", p.Name))
			}
			scope.names[p.Name] = types.Scheme{Body: fn.Arg}
		}
		cur = fn.Ret
	}
	savedAmbient := g.ambient
	bodyAmbient := types.Row{Tail: g.ck.Sup.FreshVar(types.RowVar)}
	resultTy := g.ck.Sup.FreshVar(types.General)
	funTy := g.wrapFunction(paramTys, resultTy, bodyAmbient)
	scope.names[name] = types.Scheme{Body: funTy}
	g.ambient = bodyAmbient
	bodyTy := g.expr(body)
	g.ambient = savedAmbient
	g.cs = append(g.cs, Constraint{Left: resultTy, Right: bodyTy, Span: nameSpan, Why: Why{Kind: WhyRecursion, Name: name}})
	return funTy
}

func (g *generator) wrapFunction(params []types.Type, ret types.Type, bodyRow types.Row) types.Type {
	funTy := ret
	for i := len(params) - 1; i >= 0; i-- {
		eff := types.Row{}
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
		if p.Name == "()" {
			tys[i] = g.ck.B.Unit
			continue
		}
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
// in principle, scoped sequentially, with shadowing
// forbidden against both earlier bindings and the top level.
func (g *generator) block(e *ast.Block, want types.Type) types.Type {
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
		var annVars []*types.TVar
		var given []types.Pred
		predStart := len(g.preds)
		var annTy types.Type
		if bind.Ann != nil {
			annScope := g.ck.NewAnnScope()
			var annErrs []diag.Error
			annTy, annErrs = g.ck.ResolveTypeExpr(bind.Ann.Type, annScope)
			g.errs = append(g.errs, annErrs...)
			given, annErrs = g.ck.ResolvePreds(bind.Ann.Preds, annScope)
			g.errs = append(g.errs, annErrs...)
			annVars = annScope.Minted()
		}
		if len(bind.Params) > 0 && annTy != nil {
			ty = g.functionWithAnnotatedParams(bind.Name, bind.NameSpan, bind.Params, bind.Body, annTy)
		} else if len(bind.Params) > 0 {
			ty = g.function(bind.Name, bind.NameSpan, bind.Params, bind.Body)
		} else {
			ty = g.expr(bind.Body)
		}
		if bind.Ann != nil && annTy != nil {
			// Skolemize-and-unify, as at the top level.
			g.cs = append(g.cs, Constraint{Left: annTy, Right: ty,
				Span: bind.Body.Span(), Why: Why{Kind: WhyAnnotation, Name: bind.Name}})
			ty = annTy
		}
		scheme := types.Scheme{Body: ty}
		// Monomorphism restriction for block bindings: only syntactic
		// functions and lambda literals generalize locally. A generalized
		// binding lambda-lifts and re-evaluates per use (doc/design.md, "Go backend and runtime") — fine for
		// function values, but a *value* binding must keep the eager
		// evaluate-once semantics in doc/design.md, "Language semantics", which Number-kinded
		// generalization (`k = 10 : number`) would otherwise silently break
		// for every numeric local. Top-level values still generalize (doc/design.md, "Go backend and runtime"
		// accepts nullary generic values; the top-level purity check keeps
		// re-evaluation unobservable).
		_, isLambda := bind.Body.(*ast.Lambda)
		if len(bind.Params) > 0 || isLambda {
			// Solve-at-binding (doc/design.md, "Type inference"): discharge this binding's constraints
			// into the substitution now, so generalization sees solved types
			// and later bindings can use this one polymorphically.
			g.solveHere()
			if bind.Ann == nil {
				g.ck.closeSingleRows(ty)
			}
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
			// Only obligations involving this binding's quantified variables
			// move into its scheme; captured obligations stay with the parent.
			quant := map[int]bool{}
			for _, v := range scheme.Vars {
				quant[v.ID] = true
			}
			left, es := g.ck.reduceObligations(g.preds[predStart:], given)
			g.errs = append(g.errs, es...)
			g.preds = g.preds[:predStart]
			for _, p := range left {
				if v, ok := p.Ty.(*types.TVar); ok && quant[v.ID] {
					if bind.Ann != nil {
						g.errs = append(g.errs, diag.Errorf(bind.NameSpan, "MISSING CONSTRAINT", "Add `%s %s` to the annotation.", types.SurfaceName(p.Class), types.Show(p.Ty)))
					} else {
						scheme.Preds = append(scheme.Preds, p)
					}
				} else {
					g.preds = append(g.preds, predObligation{pred: p, span: bind.NameSpan})
				}
			}
			if bind.Ann != nil {
				scheme.Preds = given
			}
			scheme.Preds = g.ck.NormalizePreds(scheme.Preds)
		}
		g.ck.BindTypes[bind] = ty
		g.ck.BindSchemes[bind] = scheme
		g.locals.names[bind.Name] = scheme
	}
	return g.exprWant(e.Result, want)
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
		v := g.ck.Sup.FreshVar(types.General)
		g.preds = append(g.preds, predObligation{pred: g.ck.StandardPred("Num", v), span: p.Sp}, predObligation{pred: g.ck.StandardPred("Eq", v), span: p.Sp})
		return v
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
	name := g.ck.Operators[e.Op]
	if name == "" {
		g.errs = append(g.errs, diag.Errorf(e.OpSpan, "MISSING OPERATOR", "No declaration implements (%s).", e.Op))
		return g.ck.Sup.FreshVar(types.General)
	}
	app := &ast.App{Fn: &ast.App{Fn: &ast.Var{Name: name, Sp: e.OpSpan}, Arg: e.L}, Arg: e.R}
	g.ck.Desugared[e] = app
	return g.expr(app)
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

func (g *generator) instantiateAt(s types.Scheme, sp source.Span, op string) types.Type {
	if len(s.Vars) == 0 {
		for _, p := range s.Preds {
			g.preds = append(g.preds, predObligation{pred: p, span: sp, op: op})
		}
		return s.Body
	}
	m := make(map[int]types.Type, len(s.Vars))
	for _, v := range s.Vars {
		m[v.ID] = g.ck.Sup.FreshVar(v.Kind)
	}
	for _, p := range types.SubstPreds(s.Preds, m) {
		g.preds = append(g.preds, predObligation{pred: p, span: sp, op: op})
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
