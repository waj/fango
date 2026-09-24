// Package infer is the constraint-based type checker: constraint generation
// (constrain.go), unification (unify.go), and solving (solve.go). The
// explicit constraint list — rather than Algorithm W's inline unification —
// is what buys good errors now and typeclasses later.
package infer

import (
	"fmt"
	"sort"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/fixity"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// Why says why two types had to match, so failures point at the right span
// with the right story. More kinds arrive with their features (IfCondition,
// CaseBranches, CallArg{N}, Annotation, …).
type WhyKind int

const (
	WhyOperand          WhyKind = iota // operands of a numeric operator must agree
	WhyDeclBody                        // a declaration body must match its (future) annotation
	WhyCall                            // a callee must be a function accepting the argument
	WhyIfCondition                     // an if condition must be Bool
	WhyIfBranches                      // then/else branches must agree
	WhyCompare                         // both sides of a comparison must agree
	WhyBoolOperand                     // both sides of && / || must be Bool
	WhyNegate                          // a negated operand must be a number
	WhyOpRequires                      // an operator fixes its operand type (/, ++)
	WhyAnnotation                      // a definition must match its type annotation
	WhyRecursion                       // recursive uses must match the definition
	WhyPattern                         // a pattern must match the scrutinee's type
	WhyCaseBranches                    // all case branches must produce the same type
	WhyEffectEscapes                   // a top-level value performs an unhandled effect
	WhyEffectMismatch                  // an annotation's effect row disagrees with its body
	WhyEffectNotAllowed                // an effect row does not fit the surrounding row
	WhySpliceOperand                   // `$(…)` needs an operand that evaluates to code
	WhyProjection                      // a projected field must fit the use it is put to
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
	// A registration's immediate charge waits for the stored computation's
	// row to be determined; equating a flexible tail with the surrounding row
	// would accidentally add the driver's own control effect to that child.
	WorkCharge bool
	// Project foreign owner obligations before ordinary row bounds close the
	// residual tails of a local coroutine boundary.
	ControlNeed bool
	// Subsume checks value compatibility, including variance-directed rows.
	Subsume   bool
	ADTs      map[int]*types.ADTInfo
	Invariant []types.Type
	// Bind holds the handler activations whose subjects lexically contain the
	// closure this constraint adapts, outermost first. It carries the handler
	// instance rule of doc/reference/effects.md: a label one of them handles
	// may be replaced by what that handler's clauses perform.
	Bind []*HandlerInfo
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

// Names returns the bound names in sorted order, so a caller that scans the
// environment does deterministic work.
func (e *Env) Names() []string {
	out := make([]string, 0, len(e.vars))
	for name := range e.vars {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Checker carries the session-scoped inference state: the fresh-variable
// supply, the accumulated substitution, and per-node solved types. The REPL
// keeps one Checker across many inputs; batch compilation uses one per run.
type Checker struct {
	moduleCheck *moduleCheck
	moduleName  string
	sourceLimit *int
	recursive   *recursiveInference

	Classes           map[string]*types.ClassInfo
	Methods           map[string]*types.MethodInfo
	Instances         []*InstanceInfo
	checkingInstance  *InstanceInfo
	InstanceImports   map[string]map[string]bool
	CurrentOwner      string
	PendingPreds      []types.Pred
	ExprSchemes       map[ast.Expr]types.Scheme
	ExprCaptures      map[ast.Expr]types.CaptureSet
	Desugared         map[ast.Expr]ast.Expr
	PreludeInfos      []DeclInfo
	PreludeOwners     map[string]bool
	Aliases           map[string]string
	Sup               *types.Supply
	B                 *types.Builtins
	Env               *Env
	Sub               Subst
	ExprTypes         map[ast.Expr]types.Type
	RecordUses        map[ast.Expr]*types.ADTInfo
	RecordPatternUses map[*ast.PRecord]*types.ADTInfo
	PinExprs          map[*ast.PPin]*ast.Var

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
	// IO is the ambient IO effect, and is nil when the bundled IO module is
	// not in the graph at all — which a module carrying `{-# no-prelude #-}`
	// and importing nothing can arrange. A `main` obligation naming IO is
	// then simply not imposed: there is no IO for the program to perform.
	IO      *types.EffectInfo
	Natives map[string]*types.NativeInfo
	// Intrinsics records bundled `native` declarations the compiler
	// implements as a Core node. They are deliberately absent from Natives:
	// nothing may lower one to a NativeCall or look for a sidecar.
	Intrinsics map[string]types.Scheme
	// Fixity is the graph-wide operator table. Module loading fills it and
	// resolves every operator run before inference; the REPL extends it as
	// the session declares operators.
	Fixity fixity.Table

	OpCalls      map[*ast.App]*types.EffectOp
	HandleInfos  map[*ast.Handle]*HandlerInfo
	ScopeSpans   map[types.ScopeID]source.Span
	ResumeCalls  map[*ast.App]bool
	ResumeOwners map[*ast.Resume]types.ResumeID
	ResumeGen    types.ResumeID

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
	BindSchemes      map[*ast.LocalBind]types.Scheme
	CaptureSummaries map[string]types.CaptureSummary

	// LiftGen numbers lambda-lifted definitions session-wide, so REPL
	// inputs across a session never collide (elaborate/lift.go).
	LiftGen int
	// PatternGen gives shared top-level destructuring subjects deterministic,
	// collision-proof names and keeps REPL generations isolated.
	PatternGen int

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

	// Templates numbers this compilation's quotes; QuoteTemplates and
	// QuoteHoles index back into it from the surface nodes elaboration and
	// constraint generation see (doc/design.md, "Compile-time
	// metaprogramming").
	Templates      *meta.Table
	QuoteTemplates map[*ast.Quote]int
	QuoteHoles     map[*ast.Quote][]*ast.Splice

	// Checked is the append-only log of completed dependency groups. Source
	// order is retained separately in Module results; the compile-time evaluator
	// installs newly completed groups together on demand.
	Checked []DeclInfo
	// CompletionGroups retains the dependency-group boundaries and stable
	// instance cutoffs used by the staging object boundary.
	CompletionGroups []CompletionGroup

	// CompileTime runs a splice operand. The driver installs it
	// (internal/staging): inference cannot, because running a splice needs
	// elaboration and the interpreter, both of which depend on this package.
	CompileTime CompileTimeEval

	// CompileTimeRollback discards the compile-time environment after a
	// Checkpoint restores an earlier completion log, so the next splice rebuilds it
	// from the declarations that actually survived.
	CompileTimeRollback func(checked, instances int)

	// Derivers maps a class to the compile-time generator that implements
	// `deriving` for it (doc/design.md, "Compile-time metaprogramming").
	Derivers map[string]*DeriverInfo

	// inferringContext is non-nil while a generated instance is being probed
	// for the context its own body needs. A residual predicate that would
	// otherwise be MISSING CONSTRAINT is collected here instead.
	inferringContext *[]types.Pred
}

// ADT implements meta.Schema, which is how a reflected type reaches its own
// declaration without the compile-time evaluator carrying the checker around.
func (ck *Checker) ADT(unique int) *types.ADTInfo { return ck.ADTs[unique] }

// MarkEffectScoped applies compiler-owned lifetime policy to an effect. The
// surface language deliberately has no annotation for this: State and resource
// APIs opt in when installed by the compiler/standard library.
func (ck *Checker) MarkEffectScoped(name string) bool {
	eff := ck.Effects[name]
	if eff == nil {
		return false
	}
	eff.Scoped = true
	for _, info := range ck.HandleInfos {
		if info.Effect.Unique == eff.Unique {
			info.Scoped = true
		}
	}
	return true
}

func (ck *Checker) MarkOperationBorrowsEvidence(name string) bool {
	op := ck.Operations[name]
	if op == nil {
		return false
	}
	op.BorrowsEvidence = true
	return true
}

func (ck *Checker) MarkOperationRetainsArguments(name string) bool {
	op := ck.Operations[name]
	if op == nil {
		return false
	}
	op.RetainsArguments = true
	return true
}

// CompileTimeEval elaborates and evaluates one already-checked splice
// operand, returning the *meta.Code it produced.
type CompileTimeEval func(operand ast.Expr) (any, []diag.Error)

type CompletionGroup struct {
	Infos  []DeclInfo
	Cutoff []DeclRef
}

func (ck *Checker) RecordChecked(infos []DeclInfo) {
	if len(infos) == 0 {
		return
	}
	ck.Checked = append(ck.Checked, infos...)
	limit := ck.instanceLimit()
	cutoff := make([]DeclRef, 0, limit)
	for _, instance := range ck.Instances[:limit] {
		cutoff = append(cutoff, instance.Ref)
	}
	ck.CompletionGroups = append(ck.CompletionGroups, CompletionGroup{Infos: append([]DeclInfo(nil), infos...), Cutoff: cutoff})
}

func NewChecker(sup *types.Supply, b *types.Builtins, env *Env) *Checker {
	ck := &Checker{
		Classes: map[string]*types.ClassInfo{}, Methods: map[string]*types.MethodInfo{},
		ExprSchemes:       map[ast.Expr]types.Scheme{},
		ExprCaptures:      map[ast.Expr]types.CaptureSet{},
		Desugared:         map[ast.Expr]ast.Expr{},
		Aliases:           map[string]string{},
		Sup:               sup,
		B:                 b,
		Env:               env,
		Sub:               Subst{},
		ExprTypes:         map[ast.Expr]types.Type{},
		RecordUses:        map[ast.Expr]*types.ADTInfo{},
		RecordPatternUses: map[*ast.PRecord]*types.ADTInfo{},
		PinExprs:          map[*ast.PPin]*ast.Var{},
		Ctors:             map[string]*types.CtorInfo{},
		ADTs:              map[int]*types.ADTInfo{},
		TypeNames: map[string]types.Type{
			"Int":    b.Int,
			"Float":  b.Float,
			"String": b.String,
			"Char":   b.Char,
			"Bool":   b.Bool,
			"()":     b.Unit,
		},
		Effects:          map[string]*types.EffectInfo{},
		EffectsByUnique:  map[int]*types.EffectInfo{},
		Operations:       map[string]*types.EffectOp{},
		Natives:          map[string]*types.NativeInfo{},
		Intrinsics:       map[string]types.Scheme{},
		Fixity:           fixity.Builtin(),
		OpCalls:          map[*ast.App]*types.EffectOp{},
		HandleInfos:      map[*ast.Handle]*HandlerInfo{},
		ScopeSpans:       map[types.ScopeID]source.Span{},
		ResumeCalls:      map[*ast.App]bool{},
		ResumeOwners:     map[*ast.Resume]types.ResumeID{},
		Workers:          map[string]int{},
		BindTypes:        map[*ast.LocalBind]types.Type{},
		PatTypes:         map[ast.Pattern]types.Type{},
		BindSchemes:      map[*ast.LocalBind]types.Scheme{},
		CaptureSummaries: map[string]types.CaptureSummary{},
		EntryName:        "main",
		Templates:        &meta.Table{},
		QuoteTemplates:   map[*ast.Quote]int{},
		QuoteHoles:       map[*ast.Quote][]*ast.Splice{},
		Derivers:         map[string]*DeriverInfo{},
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
	// BodySubst instantiates component variables absent from this declaration's
	// public scheme. It applies to occurrences, never to another callee's scheme.
	BodySubst map[int]types.Type
	Name      string
	NameSpan  source.Span
	Params    []ast.Pattern
	Equations []ast.Equation
	Type      types.Type // solved but not zonked; apply ck.Sub for the final type
	Body      ast.Expr

	// Scheme is the declaration's generalized type: Scheme.Vars are the
	// definition's type parameters (elaboration's Def.TyParams). Quantified
	// metas were bound to Scheme.Vars in ck.Sub at generalization time, so
	// zonked occurrence types mention the scheme's own rigid vars. With
	// AllowPoly off this is always the trivial Scheme{Body}.
	Scheme types.Scheme
	// Instance supplies self evidence inside an instance method.
	Instance      *InstanceInfo
	InstanceLimit int
}

type HandlerClauseInfo struct {
	Invocation      *types.EffLabel
	InvocationScope types.ScopeID
	Op              *types.EffectOp
	ParamTypes      []types.Type
	OpResult        types.Type
	ResumeID        types.ResumeID
}

type HandlerInfo struct {
	Effect     types.EffLabel
	Residual   types.Row
	Scope      types.ScopeID
	Scoped     bool
	Result     types.Type
	BodyResult types.Type
	StateType  types.Type
	Clauses    []HandlerClauseInfo
	// ClauseEffects are the rows the operation clauses perform, recorded as
	// the clause bodies are generated. A `resume` call is left out: it
	// returns to the perform site, whose remaining effects belong to that site
	// rather than to a closure bound to this activation.
	ClauseEffects []types.Type
}

// Module checks declarations: type headers first (so types may be mutually
// recursive regardless of order), then constructor fields, then value
// declaration dependency groups, retaining source-position contexts — the same call
// structure used by binding-boundary generalization.
func (ck *Checker) Module(m *ast.Module) ([]DeclInfo, []diag.Error) {
	// Visibility merges rather than replaces: a REPL session checks the
	// prelude first and each imported module graph afterwards, and every
	// owner keeps its own entry. A batch run starts empty, so it sees no
	// difference.
	if m.InstanceImports != nil {
		if ck.InstanceImports == nil {
			ck.InstanceImports = map[string]map[string]bool{}
		}
		for owner, visible := range m.InstanceImports {
			ck.InstanceImports[owner] = visible
		}
	}
	// Declarations run under their own module's owner; the caller's owner
	// (the prompt's, in the REPL) is restored afterwards.
	defer func(owner string) { ck.CurrentOwner = owner }(ck.CurrentOwner)
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
		if types.Intrinsic(vd.Name) {
			errs = append(errs, ck.declareIntrinsic(vd)...)
			continue
		}
		errs = append(errs, ck.declareNative(vd)...)
	}
	errs = append(errs, ck.resolveNativeBoundaries(m)...)
	// Fixity declarations carry no type and bind no name; the graph-wide
	// table is built during module loading. They reach here only so the
	// REPL can rebuild its table from the merged prelude.
	for _, d := range m.Decls {
		if fd, ok := d.(*ast.FixityDecl); ok && ck.Fixity != nil {
			errs = append(errs, ck.Fixity.Add(fd)...)
		}
	}
	batch := newModuleCheck(ck, m.Decls)
	previous := ck.moduleCheck
	ck.moduleCheck = batch
	defer func() { ck.moduleCheck = previous }()
	ds, es := batch.run()
	infos = append(infos, ds...)
	errs = append(errs, es...)
	return infos, errs
}

// PatternDecl checks and installs one top-level destructuring group. It is
// also the REPL entry point; callers use allowEffects=false there.
func (ck *Checker) PatternDecl(d *ast.PatternDecl, allowEffects bool) ([]DeclInfo, []diag.Error) {
	return ck.patternDecl(d, allowEffects)
}

func (ck *Checker) patternDecl(d *ast.PatternDecl, allowEffects bool) ([]DeclInfo, []diag.Error) {
	names := patternNames(d.Pattern, nil)
	if len(names) == 0 {
		return nil, []diag.Error{diag.Errorf(d.Pattern.Span(), "PATTERN BINDING", "A destructuring binding must bind at least one name.")}
	}
	var errs []diag.Error
	for _, name := range names {
		if types.SurfaceName(name) == "main" {
			errs = append(errs, diag.Errorf(d.Pattern.Span(), "BAD MAIN", "`main` must be a direct declaration, not nested inside a destructuring pattern."))
		}
		if ck.Env.Has(name) {
			errs = append(errs, diag.Errorf(d.Pattern.Span(), "MULTIPLE DEFINITIONS", "`%s` is defined more than once.", types.SurfaceName(name)))
		}
	}
	if expanded, es := ck.StageExpr(d.Body); len(es) > 0 {
		errs = append(errs, es...)
	} else {
		d.Body = expanded
	}
	owner := symbolModule(names[0])
	ck.PatternGen++
	subject := fmt.Sprintf("_pattern_%d", ck.PatternGen)
	if owner != "" {
		subject = owner + "." + subject
	}
	savedMono := ck.MonoValues
	ck.MonoValues = true
	defer func() { ck.MonoValues = savedMono }()
	subjectDecl := &ast.ValueDecl{Name: subject, NameSpan: d.Pattern.Span(), Body: d.Body}
	subjectInfo, es := ck.DeclWhere(subjectDecl, allowEffects)
	errs = append(errs, es...)
	ck.BindDecl(subjectInfo)
	infos := []DeclInfo{subjectInfo}
	var projected []DeclInfo
	for i, name := range names {
		pat, aliases := projectionPattern(d.Pattern)
		body := &ast.Case{Scrutinee: &ast.Var{Name: subject, Sp: d.Pattern.Span()}, Branches: []ast.CaseBranch{{Pattern: pat, Body: &ast.Var{Name: aliases[name], Sp: d.Pattern.Span()}}}, Sp: d.Pattern.Span()}
		vd := &ast.ValueDecl{Name: name, NameSpan: d.Pattern.Span(), Body: body}
		info, es := ck.DeclWhere(vd, allowEffects)
		// Every projection re-checks the same pattern against the same
		// subject, so only the first copy of a pattern diagnostic is news.
		if i == 0 {
			errs = append(errs, es...)
		}
		projected = append(projected, info)
	}
	for _, info := range projected {
		ck.BindDecl(info)
		infos = append(infos, info)
	}
	return infos, errs
}

func projectionPattern(p ast.Pattern) (ast.Pattern, map[string]string) {
	aliases := map[string]string{}
	n := 0
	var copy func(ast.Pattern) ast.Pattern
	copy = func(p ast.Pattern) ast.Pattern {
		switch p := p.(type) {
		case *ast.PVar:
			name := fmt.Sprintf("_pattern_var_%d", n)
			n++
			aliases[p.Name] = name
			return &ast.PVar{Name: name, Sp: p.Sp}
		case *ast.PWildcard:
			return &ast.PWildcard{Sp: p.Sp}
		case *ast.PUnit:
			return &ast.PUnit{Sp: p.Sp}
		case *ast.PInt:
			q := *p
			return &q
		case *ast.PFloat:
			q := *p
			return &q
		case *ast.PString:
			q := *p
			return &q
		case *ast.PChar:
			q := *p
			return &q
		case *ast.PPin:
			q := *p
			return &q
		case *ast.PCtor:
			q := *p
			q.Args = make([]ast.Pattern, len(p.Args))
			for i, a := range p.Args {
				q.Args[i] = copy(a)
			}
			return &q
		case *ast.PRecord:
			q := *p
			q.Fields = append([]ast.RecordPatternField(nil), p.Fields...)
			for i := range q.Fields {
				q.Fields[i].Pattern = copy(q.Fields[i].Pattern)
			}
			return &q
		default:
			panic("infer: unknown pattern in top-level projection")
		}
	}
	return copy(p), aliases
}

// declareIntrinsic types a compiler intrinsic. Its annotation is resolved in
// the ordinary annotation scope rather than the native scope, because an
// intrinsic is not a sidecar: its parameters are Fango functions and its
// effects are an open row, neither of which crosses a Go ABI.
func (ck *Checker) declareIntrinsic(d *ast.ValueDecl) []diag.Error {
	if d.Ann == nil {
		return []diag.Error{diag.Errorf(d.NameSpan, "NATIVE DECLARATION", "A native declaration requires a type annotation.")}
	}
	if len(d.Ann.Preds) > 0 {
		return []diag.Error{diag.Errorf(d.NameSpan, "NATIVE DECLARATION", "Native declarations cannot require class constraints; use an ordinary constrained wrapper.")}
	}
	scope := ck.NewAnnScope()
	ty, errs := ck.ResolveTypeExpr(d.Ann.Type, scope)
	if ty == nil {
		return errs
	}
	arity := types.IntrinsicArity(d.Name)
	// Elaboration reads the parameter types structurally when it builds the
	// body, so a bundled annotation that does not match the shape the compiler
	// implements is a declaration error rather than a later panic.
	params, rest := peelArrows(ty, arity)
	if rest == nil {
		errs = append(errs, diag.Errorf(d.NameSpan, "NATIVE DECLARATION",
			"The intrinsic `%s` takes %d parameters, so its annotation must have that many arrows.", ast.Spelling(d.Name), arity))
		return errs
	}
	for i, param := range params {
		functionParam := false
		switch d.Name {
		case types.ScopeBracketName:
			functionParam = true
		}
		if _, isFn := param.(*types.TFun); functionParam && !isFn {
			errs = append(errs, diag.Errorf(d.NameSpan, "NATIVE DECLARATION",
				"Parameter %d of the intrinsic `%s` must be a function.", i+1, ast.Spelling(d.Name)))
			return errs
		}
	}
	if d.Name == types.CoroutineFacetName || d.Name == types.CoroutineScopeName || d.Name == types.CoroutineCreateName || d.Name == types.CoroutineWithName || d.Name == types.CoroutineAdvanceName || d.Name == types.CoroutineCloseName {
		if !types.CoroutineShape(d.Name, ty) {
			return append(errs, diag.Errorf(d.NameSpan, "NATIVE DECLARATION", "The intrinsic %s has an invalid coroutine protocol.", ast.Spelling(d.Name)))
		}
	}
	if types.WorkIntrinsic(d.Name) && !types.WorkShape(d.Name, ty) {
		return append(errs, diag.Errorf(d.NameSpan, "NATIVE DECLARATION", "The intrinsic %s has an invalid work package protocol.", ast.Spelling(d.Name)))
	}
	if d.Name == types.ServiceRunName && !types.ServiceRunShape(ty) {
		return append(errs, diag.Errorf(d.NameSpan, "NATIVE DECLARATION", "Invalid service invocation scope protocol."))
	}
	if types.FailureInspection(d.Name) {
		pure := true
		arrow := ty
		for range arity {
			fn := arrow.(*types.TFun)
			pure = pure && len(fn.Eff.Labels) == 0 && fn.Eff.Tail == nil
			arrow = fn.Ret
		}
		if !pure || !types.FailureInspectionShape(d.Name, params, rest) {
			return append(errs, diag.Errorf(d.NameSpan, "NATIVE DECLARATION", "The intrinsic `%s` has an invalid failure inspection signature.", ast.Spelling(d.Name)))
		}
	}
	if types.CompletionIntrinsic(d.Name) && !types.CompletionDeclarationShape(d.Name, ty) {
		return append(errs, diag.Errorf(d.NameSpan, "NATIVE DECLARATION", "Invalid typed completion signature."))
	}
	if d.Name == types.FailAttemptReportName && !types.AttemptReportShape(ty) {
		return append(errs, diag.Errorf(d.NameSpan, "NATIVE DECLARATION", "The intrinsic `%s` must preserve the action's residual row and return `Result (Report error) value`.", ast.Spelling(d.Name)))
	}
	sch := types.Scheme{Vars: scope.Minted(), Preds: scope.Preds(), Body: ty}
	ck.Intrinsics[d.Name] = sch
	ck.Env.Bind(d.Name, sch)
	ck.Workers[d.Name] = arity
	return errs
}

// peelArrows splits n arrows off t, reporting nil when t has fewer.
func peelArrows(t types.Type, n int) ([]types.Type, types.Type) {
	args := make([]types.Type, 0, n)
	for range n {
		fn, ok := t.(*types.TFun)
		if !ok {
			return nil, nil
		}
		args = append(args, fn.Arg)
		t = fn.Ret
	}
	return args, t
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
	info := &types.EffectInfo{Unique: ck.Sup.NextUnique(), Name: ed.Name, Params: params, Suspension: ed.CompilerSuspension, Service: ed.Service, Invocation: ed.CompilerInvocation, Scoped: ed.CompilerInvocation}
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
	if len(ed.Ops) > 1 {
		discipline := ed.Ops[0].Abort
		for _, op := range ed.Ops[1:] {
			if op.Abort != discipline {
				errs = append(errs, diag.Errorf(op.NameSpan, "MIXED EFFECT DISCIPLINE",
					"Effect `%s` mixes abort-only and resumptive operations; the first abort release requires one discipline per effect.", ed.Name))
			}
		}
	}
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
		var invocation *types.EffLabel
		if info.Service {
			row := arrows[len(arrows)-1].Eff
			if len(row.Labels) != 1 || row.Tail != nil || len(row.Labels[0].Args) != 2 || ck.EffectsByUnique[row.Labels[0].Unique] == nil || !ck.EffectsByUnique[row.Labels[0].Unique].Invocation || op.Abort || op.Native != nil {
				errs = append(errs, diag.Errorf(op.NameSpan, "SERVICE PROTOCOL", "Each service operation must declare one fixed Service.Invocation request reply row and cannot be aborting or native."))
			} else {
				label := row.Labels[0]
				invocation = &label
			}
		}
		for i, arrow := range arrows {
			rowVars[i] = ck.Sup.FreshRigid(types.RowVar)
			arrow.Eff = types.Row{Tail: rowVars[i]}
		}
		inner := arrows[len(arrows)-1]
		inner.Eff.Labels = []types.EffLabel{{Unique: info.Unique, Name: info.Name, Args: labelArgs, Abort: op.Abort, Suspension: info.Suspension}}
		if invocation != nil {
			inner.Eff.Labels = append(inner.Eff.Labels, *invocation)
		}
		vars := append([]*types.TVar(nil), info.Params...)
		vars = append(vars, scope.Minted()...)
		vars = append(vars, rowVars...)
		sch := types.Scheme{Vars: vars, Preds: scope.Preds(), Body: ty}
		params := make([]types.Type, len(arrows))
		for i, a := range arrows {
			params[i] = a.Arg
		}
		local := append([]*types.TVar(nil), scope.Minted()...)
		if op.Abort {
			if op.Native != nil {
				errs = append(errs, diag.Errorf(op.NameSpan, "ABORT NATIVE", "Abort-only operation `%s` must be handled in Fango and cannot be native.", op.Name))
			}
			payloadUsesResult := false
			if len(local) == 1 {
				for _, p := range params {
					payloadUsesResult = payloadUsesResult || containsTypeVar(p, local[0].ID)
				}
			}
			if len(local) > 1 || len(local) == 1 && (!types.Equal(inner.Ret, local[0]) || payloadUsesResult) {
				errs = append(errs, diag.Errorf(op.NameSpan, "ABORT RESULT TYPE",
					"Abort-only operation `%s` may introduce exactly one operation-local type variable, used as its whole result type.", op.Name))
			}
		}
		meta := &types.EffectOp{Owner: info, Index: len(info.Ops), Name: op.Name, Scheme: sch,
			Arity: len(arrows), ParamTypes: params, ResultType: arrows[len(arrows)-1].Ret, LocalVars: local, Abort: op.Abort, Invocation: invocation}
		if op.Native != nil {
			n := &types.NativeInfo{Name: op.Name, Module: symbolModule(op.Name), Scheme: sch, Arity: len(arrows), Template: op.Native.Template, Effect: info}
			meta.Native = n
			ck.Natives[op.Name] = n
		}
		info.Ops = append(info.Ops, meta)
		ck.Operations[op.Name] = meta
		ck.Env.Bind(op.Name, sch)
	}
	if err := types.CheckServiceEffect(info, ck.EffectsByUnique); err != nil {
		errs = append(errs, diag.Errorf(ed.Sp, "SERVICE PROTOCOL", "%s", err))
	}
	return errs
}

func containsTypeVar(t types.Type, id int) bool {
	switch t := t.(type) {
	case *types.TVar:
		return t.ID == id
	case *types.TCon:
		for _, arg := range t.Args {
			if containsTypeVar(arg, id) {
				return true
			}
		}
	case *types.TFun:
		if containsTypeVar(t.Arg, id) || containsTypeVar(t.Ret, id) {
			return true
		}
		for _, label := range t.Eff.Labels {
			for _, arg := range label.Args {
				if containsTypeVar(arg, id) {
					return true
				}
			}
		}
	}
	return false
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
	errs = append(errs, ck.declareEffectOps(ed, false)...)
	return append(errs, ck.resolveNativeBoundaries(&ast.Module{Decls: []ast.Decl{ed}})...)
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
	adt := &types.ADTInfo{Resource: td.Resource, Shared: td.Shared, Con: con, Params: params, ParamKindsKnown: make([]bool, len(params))}
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
	if td.RecordFields != nil {
		if td.Shared {
			errs = append(errs, diag.Errorf(td.ResourceSpan, "SHARED RESOURCE", "A shared native resource must wrap exactly one Native.Any field."))
		}
		seenFields := map[string]bool{}
		fields := make([]types.Type, len(td.RecordFields))
		for i, f := range td.RecordFields {
			if seenFields[f.Name] {
				errs = append(errs, diag.Errorf(f.NameSpan, "RECORD FIELDS", "The field `%s` appears more than once in record `%s`.", f.Name, types.SurfaceName(td.Name)))
			}
			seenFields[f.Name] = true
			ty, fieldErrs := ck.ResolveTypeExpr(f.Type, scope)
			errs = append(errs, fieldErrs...)
			if ty == nil {
				ty = ck.B.Unit
			}
			fields[i] = ty
			adt.RecordFields = append(adt.RecordFields, types.RecordFieldInfo{Name: f.Name, Type: ty})
		}
		ctor := &types.CtorInfo{Name: td.Name + ".__record", Index: 0, Fields: fields, Result: result}
		adt.Ctors = append(adt.Ctors, ctor)
		ck.Ctors[ctor.Name] = ctor
		for i := range adt.Params {
			adt.ParamKindsKnown[i] = true
		}
		return errs
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
	errs = append(errs, markListRepr(adt, td.NameSpan)...)
	errs = append(errs, markBytesRepr(adt, td.NameSpan)...)
	errs = append(errs, markNativeAnyRepr(adt, td.NameSpan)...)
	if td.Shared && (len(adt.Ctors) != 1 || len(adt.Ctors[0].Fields) != 1 || !ck.isNativeAnyType(adt.Ctors[0].Fields[0])) {
		errs = append(errs, diag.Errorf(td.ResourceSpan, "SHARED RESOURCE", "A shared native resource must wrap exactly one Native.Any field."))
	}
	for i := range adt.Params {
		adt.ParamKindsKnown[i] = true
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
	if summary, ok := ck.CaptureSummaries[info.Name]; ok {
		info.Scheme.CaptureVars = append([]types.CaptureVar(nil), summary.Vars...)
		info.Scheme.Captures = summary.Captures
	}
	ck.Env.Bind(info.Name, info.Scheme)
	if len(info.Params) > 0 {
		ck.Workers[info.Name] = len(info.Params)
	} else {
		delete(ck.Workers, info.Name)
	}
}

func (ck *Checker) SetCaptureSummary(name string, vars []types.CaptureVar, captures types.CaptureSet) {
	summary := types.CaptureSummary{Vars: append([]types.CaptureVar(nil), vars...), Captures: captures}
	ck.CaptureSummaries[name] = summary
	if sch, ok := ck.Env.Lookup(name); ok {
		sch.CaptureVars, sch.Captures = summary.Vars, summary.Captures
		ck.Env.Bind(name, sch)
	}
}

type declInference struct {
	originalAnn          types.Type
	d                    *ast.ValueDecl
	g                    *generator
	ty, annTy            types.Type
	given                []types.Pred
	errs                 []diag.Error
	allowEffects, isMain bool
	info                 DeclInfo
}

// DeclWhere checks one declaration — annotation resolution, body inference
// (with parameter scoping and self-recursion for function definitions),
// and the annotation constraint — without binding it, so callers control
// whether a failed definition enters the environment (the REPL does not
// bind on error).
func (ck *Checker) DeclWhere(d *ast.ValueDecl, allowEffects bool) (DeclInfo, []diag.Error) {
	q := ck.prepareDecl(d, allowEffects)
	sub, _, es := q.g.solveConstraints(nil)
	ck.Sub = sub
	q.errs = append(q.errs, q.g.errs...)
	q.errs = append(q.errs, es...)
	q.g.errs = nil
	q.errs = append(q.errs, q.g.finishLocalAnnotations()...)
	return ck.finishDecl(q)
}

func (ck *Checker) prepareDecl(d *ast.ValueDecl, allowEffects bool) *declInference {
	var errs []diag.Error
	var given []types.Pred
	isMain := d.Name == ck.EntryName
	if isMain && len(d.Params) > 1 {
		errs = append(errs, diag.Errorf(d.NameSpan, "MAIN TAKES NO PARAMETERS",
			"`main` may be a value or a one-argument Unit function."))
	}
	if isMain && len(d.Params) == 1 {
		switch d.Params[0].(type) {
		case *ast.PUnit, *ast.PWildcard:
		default:
			errs = append(errs, diag.Errorf(d.Params[0].Span(), "MAIN TAKES NO PARAMETERS", "Function-style `main` must use `main()` or discard its Unit argument with `_`."))
		}
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
	originalAnn := annTy
	if ck.recursive != nil && annTy != nil {
		replacements := map[int]types.Type{}
		for _, v := range annScope.Minted() {
			replacements[v.ID] = ck.Sup.FreshVar(v.Kind)
		}
		annTy = types.SubstRigid(annTy, replacements)
		given = types.SubstPreds(given, replacements)
	}
	g := &generator{ck: ck, ambient: types.Row{Tail: ck.Sup.FreshVar(types.RowVar)}}
	var ty types.Type
	if len(d.Params) == 0 {
		ty = g.expr(d.Body)
		if !isMain && allowEffects {
			g.cs = append(g.cs, Constraint{Left: g.ambient, Right: types.Row{}, Span: d.Body.Span(), Why: Why{Kind: WhyEffectEscapes}})
		}
	} else if annTy != nil {
		ty = g.functionEquations(d.Name, d.NameSpan, declEquations(d), annTy)
		if isMain && len(d.Params) == 1 && ck.IO != nil {
			want := &types.TFun{Arg: ck.B.Unit, Eff: types.Row{Labels: []types.EffLabel{{Unique: ck.IO.Unique, Name: ck.IO.Name}}}, Ret: ck.B.Unit}
			g.cs = append(g.cs, Constraint{Left: ty, Right: want, Span: d.Body.Span(), Why: Why{Kind: WhyAnnotation, Name: "main"}})
		}
	} else {
		ty = g.functionEquations(d.Name, d.NameSpan, declEquations(d), nil)
		if isMain && len(d.Params) == 1 && ck.IO != nil {
			want := &types.TFun{Arg: ck.B.Unit, Eff: types.Row{Labels: []types.EffLabel{{Unique: ck.IO.Unique, Name: ck.IO.Name}}}, Ret: ck.B.Unit}
			g.cs = append(g.cs, Constraint{Left: ty, Right: want, Span: d.Body.Span(), Why: Why{Kind: WhyAnnotation, Name: "main"}})
		}
	}
	g.executionRoots = append(g.executionRoots, DeclInfo{Name: d.Name, NameSpan: d.NameSpan, Params: d.Params, Equations: d.Equations, Body: d.Body, Type: ty})
	return &declInference{originalAnn: originalAnn, d: d, g: g, ty: ty, annTy: annTy, given: given, errs: errs, allowEffects: allowEffects, isMain: isMain}
}

func (ck *Checker) finishDecl(q *declInference) (DeclInfo, []diag.Error) {
	d, g, ty, annTy, given, errs, allowEffects, isMain := q.d, q.g, q.ty, q.annTy, q.given, q.errs, q.allowEffects, q.isMain

	// An inferred record literal takes its nominal type from context, and a
	// declaration's annotation is that context. The annotation is normally
	// reconciled below, after the record fixed point, which would be too late;
	// when something is actually waiting on it, unify it first so the fixed
	// point can see through it. Doing this through the substitution rather than
	// by threading an expected type down the syntax covers every shape the
	// literal can sit in — a list element, a branch, a nested field — uniformly.
	// The gate keeps declarations that use no inferred record on exactly the
	// path they were on before.
	annPreSolved, effectsAgree := false, true
	if d.Ann != nil && annTy != nil && g.hasPendingInferredRecord() {
		// Capture the effect comparison against the pre-unification zonk: after
		// the solve the two sides are equal by construction, so an annotation
		// could otherwise claim effects the body never performs.
		effectsAgree = sameKnownEffects(ck.Sub.Apply(annTy), ck.Sub.Apply(ty))
		sub, _, solveErrs := Solve([]Constraint{{Left: annTy, Right: ty, Span: d.Body.Span(), Why: Why{Kind: WhyAnnotation, Name: d.Name}}}, nil, ck.Sub, ck.B, ck.Sup)
		ck.Sub = sub
		errs = append(errs, solveErrs...)
		annPreSolved = true
	}
	g.resolveRecords(true, 0)
	errs = append(errs, g.errs...)
	if d.Ann == nil && !isMain && ck.recursive == nil {
		ck.closeSingleRows(ty)
	}
	promptEffects := false
	if row, ok := ck.Sub.Apply(g.ambient).(types.Row); ok && len(row.Labels) > 0 {
		promptEffects = true
	}
	if !allowEffects && promptEffects {
		errs = append(errs, diag.Errorf(d.Body.Span(), "EFFECTFUL PROMPT DECLARATION", "Effectful declarations are not installed at the prompt; run the expression directly."))
	}
	if isMain && len(d.Params) == 0 && ck.IO != nil {
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
		if !annPreSolved {
			if !sameKnownEffects(ck.Sub.Apply(annTy), ck.Sub.Apply(ty)) {
				errs = append(errs, diag.Errorf(d.Ann.Sp, "EFFECT MISMATCH",
					"The effect row in the annotation for `%s` does not exactly match the effects performed by its body.", d.Name))
			}
			c := Constraint{Left: annTy, Right: ty, Span: d.Body.Span(), Why: Why{Kind: WhyAnnotation, Name: d.Name}}
			sub, _, solveErrs := Solve([]Constraint{c}, nil, ck.Sub, ck.B, ck.Sup)
			ck.Sub = sub
			errs = append(errs, solveErrs...)
		} else if !effectsAgree {
			errs = append(errs, diag.Errorf(d.Ann.Sp, "EFFECT MISMATCH",
				"The effect row in the annotation for `%s` does not exactly match the effects performed by its body.", d.Name))
		}
		ty = annTy
	}
	ty = ck.runnerControl(ty, len(d.Params), declEquations(d))
	info := DeclInfo{Name: d.Name, NameSpan: d.NameSpan, Params: d.Params, Equations: d.Equations, Type: ty, Body: d.Body, InstanceLimit: ck.instanceLimit()}
	if ck.recursive != nil {
		q.info = info
		q.errs = errs
		return info, errs
	}

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
	return sameKnownRowEffects(af.Eff, bf.Eff) && sameKnownEffects(af.Ret, bf.Ret)
}

func sameKnownRowEffects(a, b types.Row) bool {
	al, bl := types.SortedRow(a).Labels, types.SortedRow(b).Labels
	if len(al) != len(bl) {
		return false
	}
	for i := range al {
		if al[i].Unique != bl[i].Unique {
			return false
		}
	}
	return true
}

// closeSingleRows makes an inferred arrow pure when its open row occurs only
// once in the type. A row shared between a callback and the surrounding call
// remains quantified, preserving inferred higher-order effect polymorphism.
func (ck *Checker) closeSingleRows(t types.Type) {
	ck.closeSingleRowsExcept(t, nil)
}

func (ck *Checker) closeSingleRowsExcept(t types.Type, avoid map[int]bool) {
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
		if n == 1 && !avoid[id] {
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
// the checker's substitution. With allowEffects off the expression must type
// with an empty effect row — the rule that makes "no IO during compilation" a
// consequence of the effect system rather than a convention.
func (ck *Checker) ExprWhere(e ast.Expr, allowEffects bool) (types.Type, []diag.Error) {
	g := &generator{ck: ck, ambient: types.Row{Tail: ck.Sup.FreshVar(types.RowVar)}}
	ty := g.expr(e)
	g.executionRoots = append(g.executionRoots, DeclInfo{Name: "$expression", Body: e, Type: ty})
	var preds []types.Pred // the typeclass seam: always empty in the MVP
	sub, residual, solveErrs := g.solveConstraints(preds)
	ck.Sub = sub
	_ = residual
	errs := append(g.errs, solveErrs...)
	errs = append(errs, g.finishLocalAnnotations()...)
	g.errs = nil
	g.resolveRecords(true, 0)
	errs = append(errs, g.errs...)
	left, es := ck.reduceObligations(g.preds, nil)
	ids := map[int]bool{}
	collectVarIDs(ck.Sub.Apply(ty), ids)
	var ambiguous, visible []types.Pred
	for _, p := range left {
		if !mentionsAny(p.Ty, ids) {
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
	if !allowEffects {
		if row, ok := ck.Sub.Apply(g.ambient).(types.Row); ok && len(row.Labels) > 0 {
			es = append(es, diag.Errorf(e.Span(), "COMPILE-TIME EFFECT",
				"Compile-time code runs inside the compiler, so it must be pure, but\nthis performs `%s`.", types.SurfaceName(row.Labels[0].Name)))
		}
	}
	return ty, append(errs, es...)
}

type predObligation struct {
	pred types.Pred
	span source.Span
	op   string
}

// recordKind says what a deferred obligation becomes once its receiver names a
// nominal record. An access reads or replaces fields of a value that already
// has a type; a build or a match is an inferred `{ ... }` whose own type comes
// from context, so it additionally checks the schema the named forms check
// eagerly and fills the side table elaboration reads.
type recordKind int

const (
	recordAccess recordKind = iota
	recordBuild
	recordMatch
)

type recordObligation struct {
	kind       recordKind
	node       ast.Expr
	pat        *ast.PRecord
	receiver   types.Type
	result     types.Type
	field      string
	fieldSpan  source.Span
	candidates []string
	updates    []recordUpdateObligation
	resolved   bool
}

type recordUpdateObligation struct {
	name       string
	span       source.Span
	ty         types.Type
	candidates []string
	// bind carries the handler activations a lambda written in this field may
	// bind to. An inferred literal names no record, so its fields reach their
	// declared types through this obligation rather than through a field
	// constraint, and the rule would otherwise never see them.
	bind []*HandlerInfo
}

type generator struct {
	ck                *Checker
	locals            *blockScope
	cs                []Constraint
	errs              []diag.Error
	ambient           types.Row
	resumeType        types.Type
	resumeState       types.Type
	resumeID          types.ResumeID
	abortClause       bool
	preds             []predObligation
	records           []*recordObligation
	patternPins       *blockScope
	patternBinder     string
	annotationAmbient *types.Row
	localAnnotations  []localAnnotation
	executionRoots    []DeclInfo
	// Keep package row provenance before a local binding can solve/generalize
	// a partial application; whole-definition flow adds imported obligations.
	workRows []types.Type
	// subjectHandlers are the handlers whose subject the generator is inside,
	// outermost first, and lambdaBinders records that stack for every lambda
	// written there. clauseEffects collects into the handler currently having
	// its clauses generated, and is nil wherever the ambient row is not that
	// handler's clause row.
	subjectHandlers []*HandlerInfo
	lambdaBinders   map[ast.Expr][]*HandlerInfo
	clauseEffects   *[]types.Type
}

// performs records that the ambient row absorbs eff, and that the handler
// whose clauses are being generated performs it. A resume call carries the
// residual row of its own handler, which the continuation performs at the
// perform site, so it is not something the clause itself performs.
func (g *generator) performs(eff types.Type, span source.Span, resume bool) {
	g.cs = append(g.cs, Constraint{Left: eff, Right: g.ambient, Span: span, Why: Why{Kind: WhyCall}, Include: true})
	if g.clauseEffects != nil && !resume {
		*g.clauseEffects = append(*g.clauseEffects, eff)
	}
}

// argument builds the compatibility constraint for an argument or record
// field, carrying the handler activations a lambda written there may bind to.
func (g *generator) argument(actual, want types.Type, e ast.Expr) Constraint {
	return Constraint{Left: actual, Right: want, Span: e.Span(), Why: Why{Kind: WhyCall},
		Subsume: true, ADTs: g.ck.ADTs, Bind: g.lambdaBinders[e]}
}

// enterAmbient switches the ambient row and suspends clause-effect recording,
// which describes only what runs directly in the clause it belongs to.
func (g *generator) enterAmbient(row types.Row) (types.Row, *[]types.Type) {
	saved, sink := g.ambient, g.clauseEffects
	g.ambient, g.clauseEffects = row, nil
	return saved, sink
}

func (g *generator) leaveAmbient(row types.Row, sink *[]types.Type) {
	g.ambient, g.clauseEffects = row, sink
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
	case *ast.CharLit:
		ty = g.ck.B.Char
	case *ast.UnitLit:
		ty = g.ck.B.Unit
	case *ast.Var:
		if name := g.ck.Aliases[e.Name]; name != "" {
			e.Name = name
		}
		if ctor := g.ck.Ctors[e.Name]; ctor != nil && g.ck.ADTs[ctor.Result.Unique] != nil && g.ck.ADTs[ctor.Result.Unique].NativeIndexed {
			g.errs = append(g.errs, diag.Errorf(e.Sp, "NATIVE HANDLE REPRESENTATION", "Indexed native handles can only be created by their checked native operations."))
		}
		if localScheme, ok := g.locals.lookup(e.Name); ok {
			g.ck.ExprSchemes[e] = localScheme
			g.ck.ExprCaptures[e] = g.instantiateCaptures(localScheme)
			ty = g.instantiateAt(localScheme, e.Sp, e.Name)
		} else {
			scheme, ok := g.ck.Env.Lookup(e.Name)
			if !ok || !g.ck.valueVisible(e.Name) {
				g.errs = append(g.errs, diag.Errorf(e.Sp, "NAMING ERROR",
					"I don't know a value named `%s`.", e.Name))
				ty = g.ck.Sup.FreshVar(types.General) // recover with a hole
				break
			}
			g.ck.ExprSchemes[e] = scheme
			g.ck.ExprCaptures[e] = g.instantiateCaptures(scheme)
			// An operation in value position is not yet being performed. Its
			// predicates are checked when a saturated operation spine is formed;
			// this also lets main's ordinary shape check diagnose `main = print`.
			if op := g.ck.Operations[e.Name]; op != nil {
				ty = g.instantiate(scheme)
			} else {
				ty = g.instantiateAt(scheme, e.Sp, e.Name)
			}
			if op := g.ck.Operations[e.Name]; op != nil && len(op.LocalVars) > 0 && !op.Abort && op.Native == nil && !g.isDefaultPrint(op) {
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
		if g.ck.ADTs[info.Result.Unique] != nil && g.ck.ADTs[info.Result.Unique].NativeIndexed {
			g.errs = append(g.errs, diag.Errorf(e.Span(), "NATIVE HANDLE REPRESENTATION", "An indexed native handle's representation cannot be constructed in Fango."))
		}
		ty = result
		for i := len(fields) - 1; i >= 0; i-- {
			ty = &types.TFun{Arg: fields[i], Eff: types.Row{Tail: g.ck.Sup.FreshVar(types.RowVar)}, Ret: ty}
		}
	case *ast.RecordLit:
		if e.Name == "" {
			ty = g.inferredRecord(e, want)
			break
		}
		named, ok := g.ck.TypeNames[e.Name].(*types.TCon)
		adt := (*types.ADTInfo)(nil)
		if ok {
			adt = g.ck.ADTs[named.Unique]
		}
		if adt == nil || !adt.IsRecord() {
			g.errs = append(g.errs, g.unknownRecord(e.Name, e.NameSpan))
			ty = g.ck.Sup.FreshVar(types.General)
			for _, f := range e.Fields {
				g.expr(f.Value)
			}
			break
		}
		fieldTys, result := g.instantiateCtor(adt.Ctors[0])
		seen := map[string]bool{}
		provided := map[string]ast.RecordExprField{}
		for _, f := range e.Fields {
			if seen[f.Name] {
				g.errs = append(g.errs, diag.Errorf(f.NameSpan, "RECORD FIELDS", "The field `%s` is provided more than once.", f.Name))
			}
			seen[f.Name] = true
			provided[f.Name] = f
			ft := g.expr(f.Value)
			idx, _ := adt.RecordField(f.Name)
			if idx < 0 {
				g.errs = append(g.errs, diag.Errorf(f.NameSpan, "UNKNOWN FIELD", "Record `%s` has no field named `%s`.", types.SurfaceName(adt.Con.Name), f.Name))
			} else {
				g.cs = append(g.cs, g.argument(ft, fieldTys[idx], f.Value))
			}
		}
		for _, f := range adt.RecordFields {
			if _, ok := provided[f.Name]; !ok {
				g.errs = append(g.errs, diag.Errorf(e.NameSpan, "RECORD FIELDS", "Record `%s` is missing field `%s`.", types.SurfaceName(adt.Con.Name), f.Name))
			}
		}
		g.ck.RecordUses[e] = adt
		ty = result
	case *ast.RecordGet:
		receiver := g.expr(e.Record)
		result := g.ck.Sup.FreshVar(types.General)
		g.records = append(g.records, &recordObligation{node: e, receiver: receiver, result: result, field: e.Field, fieldSpan: e.FieldSpan, candidates: e.Records})
		ty = result
	case *ast.RecordUpdate:
		receiver := g.expr(e.Record)
		ob := &recordObligation{node: e, receiver: receiver, result: receiver, fieldSpan: e.Sp}
		seen := map[string]bool{}
		for _, f := range e.Fields {
			if seen[f.Name] {
				g.errs = append(g.errs, diag.Errorf(f.NameSpan, "RECORD FIELDS", "The field `%s` is updated more than once.", f.Name))
			}
			seen[f.Name] = true
			fieldTy := g.expr(f.Value)
			ob.updates = append(ob.updates, recordUpdateObligation{name: f.Name, span: f.NameSpan, ty: fieldTy, candidates: f.Records, bind: g.lambdaBinders[f.Value]})
		}
		if len(e.Fields) == 0 {
			g.errs = append(g.errs, diag.Errorf(e.Sp, "RECORD FIELDS", "A record update needs at least one replacement field."))
		}
		g.records = append(g.records, ob)
		ty = receiver
	case *ast.App:
		if name, n := g.intrinsicSpine(e); name != "" && n == types.IntrinsicArity(name) {
			ty = g.intrinsicCall(e, name)
			break
		}
		if op, n := g.operationSpine(e); op != nil && n == op.Arity {
			inst := g.instantiateAt(op.Scheme, e.Span(), op.Name)
			g.ck.ExprTypes[appHead(e)] = inst
			params, result := peelOperation(inst, op.Arity)
			args := appArgs(e)
			for i, a := range args {
				at := g.exprWant(a, params[i])
				g.cs = append(g.cs, g.argument(at, params[i], a))
			}
			cur := inst
			var last *types.TFun
			for range op.Arity {
				last = cur.(*types.TFun)
				cur = last.Ret
			}
			g.performs(last.Eff, e.Span(), false)
			ty = result
			g.ck.OpCalls[e] = op
			if len(op.LocalVars) > 0 && !op.Abort && op.Native == nil && !g.isDefaultPrint(op) {
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
		paramTy := g.ck.Sup.FreshVar(types.General)
		r := g.ck.Sup.FreshVar(types.General)
		callEff := types.Row{Tail: g.ck.Sup.FreshVar(types.RowVar)}
		g.cs = append(g.cs, Constraint{
			Left:  fnTy,
			Right: &types.TFun{Arg: paramTy, Eff: callEff, Ret: r},
			Span:  e.Fn.Span(),
			Why:   Why{Kind: WhyCall},
		})
		g.cs = append(g.cs, g.argument(argTy, paramTy, e.Arg))
		_, isResume := e.Fn.(*ast.Resume)
		g.performs(callEff, e.Span(), isResume)
		ty = r
		if op, n := g.operationSpine(e); op != nil && n < op.Arity {
			g.ck.OpCalls[e] = op
		}
		if isResume {
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
		ty = g.ck.Sup.FreshVar(types.General)
		g.cs = append(g.cs,
			Constraint{Left: thenTy, Right: ty, Span: e.Then.Span(), Why: Why{Kind: WhyIfBranches}, Subsume: true, ADTs: g.ck.ADTs},
			Constraint{Left: elseTy, Right: ty, Span: e.Else.Span(), Why: Why{Kind: WhyIfBranches}, Subsume: true, ADTs: g.ck.ADTs})
	case *ast.BinOp:
		ty = g.binOp(e)
	case *ast.Block:
		ty = g.block(e, want)
	case *ast.Case:
		ty = g.caseExpr(e)
	case *ast.Lambda:
		scope := &blockScope{parent: g.locals, names: map[string]types.Scheme{}}
		g.locals = scope
		oldPins := g.patternPins
		g.patternPins = scope.parent
		oldBinder := g.patternBinder
		g.patternBinder = "parameter"
		paramTys := g.bindParams(scope, e.Params)
		g.patternBinder = oldBinder
		g.patternPins = oldPins
		bodyAmbient := types.Row{Tail: g.ck.Sup.FreshVar(types.RowVar)}
		savedAmbient, savedSink := g.enterAmbient(bodyAmbient)
		// The enclosing declaration's annotation constrains its own arrow,
		// not a nested callback's handler clauses (which may call pause).
		savedAnnotationAmbient := g.annotationAmbient
		g.annotationAmbient = nil
		bodyTy := g.expr(e.Body)
		g.annotationAmbient = savedAnnotationAmbient
		g.leaveAmbient(savedAmbient, savedSink)
		g.locals = scope.parent
		funTy := g.wrapFunction(paramTys, bodyTy, bodyAmbient)
		funTy = g.ck.runnerControl(funTy, len(e.Params), []ast.Equation{{Params: e.Params, Body: e.Body}})
		// A closure written in a handler's subject may be bound to that
		// handler's activation where the position it goes to omits the label.
		if len(g.subjectHandlers) > 0 {
			if g.lambdaBinders == nil {
				g.lambdaBinders = map[ast.Expr][]*HandlerInfo{}
			}
			g.lambdaBinders[e] = append([]*HandlerInfo(nil), g.subjectHandlers...)
		}
		ty = funTy
	case *ast.Handle:
		ty = g.handle(e)
	case *ast.Resume:
		if g.resumeType == nil {
			if g.abortClause {
				g.errs = append(g.errs, diag.Errorf(e.Sp, "RESUME IN ABORT CLAUSE", "An abort-only operation clause cannot resume."))
			} else {
				g.errs = append(g.errs, diag.Errorf(e.Sp, "RESUME OUTSIDE A HANDLER", "`resume` is only available inside an operation clause."))
			}
			ty = g.ck.Sup.FreshVar(types.General)
		} else {
			if e.NextState != nil {
				if g.resumeState == nil {
					g.errs = append(g.errs, diag.Errorf(e.NextState.Span(), "STATELESS RESUME", "`resume value with nextState` is only available in a parameterized handler."))
					g.expr(e.NextState)
				} else {
					nextTy := g.exprWant(e.NextState, g.resumeState)
					g.cs = append(g.cs, Constraint{Left: nextTy, Right: g.resumeState, Span: e.NextState.Span(), Why: Why{Kind: WhyCall}})
				}
			} else if g.resumeState != nil {
				g.errs = append(g.errs, diag.Errorf(e.Sp, "MISSING NEXT STATE", "A parameterized handler must resume with its next state, like `resume value with current`."))
			}
			ty = g.resumeType
			g.ck.ResumeOwners[e] = g.resumeID
		}
	case *ast.Quote:
		// The quoted body is not checked here — its holes have no type yet.
		// It is checked when spliced, at the splice site. Only the holes,
		// which are evaluated with the quote, are checked now.
		code, codeErrs := g.ck.codeType(e.Sp)
		g.errs = append(g.errs, codeErrs...)
		for _, hole := range g.ck.QuoteHoles[e] {
			holeTy := g.expr(hole.Operand)
			g.cs = append(g.cs, Constraint{Left: holeTy, Right: code, Span: hole.Sp, Why: Why{Kind: WhySpliceOperand}})
			g.ck.ExprTypes[hole] = code
		}
		ty = code
	case *ast.Splice:
		// Staging replaced every splice before checking began, so one
		// reaching inference is a splice the stage rules already rejected.
		ty = g.ck.Sup.FreshVar(types.General)
	case *ast.TypeOf:
		repr := g.ck.TypeNames[TypeReprName]
		if repr == nil {
			g.errs = append(g.errs, diag.Errorf(e.Sp, "REFLECTION ERROR", "I cannot find the bundled `Meta.TypeRepr` type."))
			ty = g.ck.Sup.FreshVar(types.General)
			break
		}
		reflected, errs := g.ck.ResolveTypeExpr(e.Ty, g.ck.NewAnnScope())
		g.errs = append(g.errs, errs...)
		if !g.ck.reflectionClosed(reflected) {
			g.errs = append(g.errs, diag.Errorf(e.Sp, "REFLECTION ERROR", "`typeOf` requires a closed, fully applied type."))
		}
		e.Value = reflected
		ty = repr
	case *ast.MetaValue:
		ty = e.Ty
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
	if first.Owner.Invocation {
		g.errs = append(g.errs, diag.Errorf(e.Sp, "INVOCATION AUTHORITY", "Only Service.run may install producer invocation authority."))
	}
	if first.Owner.Service && e.State != nil {
		g.errs = append(g.errs, diag.Errorf(e.Sp, "SERVICE STATE", "Shared service evidence cannot own mutable handler state."))
	}
	if first.Owner.Suspension {
		g.errs = append(g.errs, diag.Errorf(e.Sp, "COMPILER-OWNED EFFECT",
			"Effect `%s` describes compiler-owned coroutine control and cannot be handled by an ordinary handler.", ast.Spelling(first.Owner.Name)))
	}
	residualVar := g.ck.Sup.FreshVar(types.RowVar)
	residual := types.Row{Tail: residualVar}
	labelArgs := make([]types.Type, len(first.Owner.Params))
	for i := range labelArgs {
		labelArgs[i] = g.ck.Sup.FreshVar(types.General)
	}
	label := types.EffLabel{Unique: first.Owner.Unique, Name: first.Owner.Name, Args: labelArgs, Abort: first.Abort, Suspension: first.Owner.Suspension}
	savedAmbient := g.ambient
	var stateTy types.Type
	if e.State != nil {
		stateTy = g.expr(e.State.Initial)
	}
	info := &HandlerInfo{Effect: label, Residual: residual, Scope: g.ck.Sup.FreshScope(), Scoped: first.Owner.Scoped || e.State != nil, Result: result, StateType: stateTy}
	// The subject is where a closure may be bound to this activation; the
	// state initializer above runs before the activation exists, and the
	// clauses below run outside it.
	g.subjectHandlers = append(g.subjectHandlers, info)
	savedSink := g.clauseEffects
	g.ambient, g.clauseEffects = types.Row{Labels: []types.EffLabel{label}, Tail: residualVar}, nil
	bodyTy := g.expr(e.Body)
	g.subjectHandlers = g.subjectHandlers[:len(g.subjectHandlers)-1]
	info.BodyResult = bodyTy
	clauseAmbient := residual
	if g.annotationAmbient != nil {
		clauseAmbient = *g.annotationAmbient
		g.cs = append(g.cs, Constraint{Left: clauseAmbient, Right: savedAmbient, Span: e.Span(), Why: Why{Kind: WhyCall}, Include: true})
	}
	g.ambient, g.clauseEffects = clauseAmbient, &info.ClauseEffects
	g.ck.ScopeSpans[info.Scope] = e.Sp
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
		if len(op.LocalVars) > 0 && !op.Abort && op.Native == nil && !g.isDefaultPrint(op) {
			g.errs = append(g.errs, diag.Errorf(cl.OpSpan, "OPERATION POLYMORPHISM NOT READY", "The operation `%s` has operation-local polymorphism that cannot be used at runtime until checkpoint 3.", op.Name))
		}
		if seen[op.Name] {
			g.errs = append(g.errs, diag.Errorf(cl.OpSpan, "DUPLICATE HANDLER CLAUSE", "The operation `%s` is handled more than once.", op.Name))
			continue
		}
		var resumeID types.ResumeID
		if !op.Abort {
			g.ck.ResumeGen++
			resumeID = g.ck.ResumeGen
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
		var invocation *types.EffLabel
		g.ambient = clauseAmbient
		if op.Invocation != nil {
			for _, extra := range last.Eff.Labels {
				if extra.Unique == op.Invocation.Unique {
					label := extra
					invocation = &label
					g.ambient.Labels = append(append([]types.EffLabel(nil), clauseAmbient.Labels...), extra)
				}
			}
		}
		eqs := cl.Equations
		if len(eqs) == 0 {
			eqs = []ast.Equation{{Params: cl.Params, Body: cl.Body, NameSpan: cl.OpSpan}}
		}
		for _, eq := range eqs {
			scope := &blockScope{parent: g.locals, names: map[string]types.Scheme{}}
			g.locals = scope
			if e.State != nil {
				if _, dup := scope.parent.lookup(e.State.Name); dup || g.ck.boundName(e.State.Name) {
					g.errs = append(g.errs, diag.Errorf(e.State.NameSpan, "SHADOWING",
						"The handler state `%s` shadows a name that is already defined —\nFango does not allow shadowing. Choose a different name.", e.State.Name))
				}
				scope.names[e.State.Name] = types.Scheme{Body: stateTy}
			}
			oldPins := g.patternPins
			g.patternPins = scope.parent
			for j, p := range eq.Params {
				var pt types.Type = g.ck.Sup.FreshVar(types.General)
				if j < len(paramTys) {
					pt = paramTys[j]
				}
				patTy := g.pattern(p, scope)
				g.cs = append(g.cs, Constraint{Left: patTy, Right: pt, Span: p.Span(), Why: Why{Kind: WhyPattern}})
			}
			g.patternPins = oldPins
			oldResume := g.resumeType
			oldResumeID := g.resumeID
			oldResumeState := g.resumeState
			oldAbortClause := g.abortClause
			if op.Abort {
				g.resumeType = nil
				g.abortClause = true
			} else {
				g.resumeType = &types.TFun{Arg: opResult, Eff: residual, Ret: result}
				g.abortClause = false
			}
			g.resumeID = resumeID
			g.resumeState = stateTy
			clTy := g.expr(eq.Body)
			g.resumeType = oldResume
			g.resumeID = oldResumeID
			g.resumeState = oldResumeState
			g.abortClause = oldAbortClause
			g.locals = scope.parent
			g.cs = append(g.cs, Constraint{Left: clTy, Right: result, Span: eq.Body.Span(), Why: Why{Kind: WhyCaseBranches}})
			if !op.Abort {
				if failure := g.tailResume(resumeID, eq.Body, true); failure != nil {
					pos := cl.OpSpan.StartPos()
					g.errs = append(g.errs, diag.Errorf(failure.span, failure.kind,
						"%s The owning operation clause starts at %d:%d.", failure.message, pos.Line, pos.Col))
				}
			}
		}
		var invocationScope types.ScopeID
		if invocation != nil {
			invocationScope = g.ck.Sup.FreshScope()
		}
		info.Clauses = append(info.Clauses, HandlerClauseInfo{Op: op, ParamTypes: paramTys, OpResult: opResult, ResumeID: resumeID, Invocation: invocation, InvocationScope: invocationScope})
	}
	g.ambient = clauseAmbient
	for _, op := range first.Owner.Ops {
		if !seen[op.Name] {
			g.errs = append(g.errs, diag.Errorf(e.Sp, "INCOMPLETE HANDLER", "The handler is missing a clause for `%s`.", op.Name))
		}
	}
	g.clauseEffects = nil
	if e.Return != nil {
		eqs := e.Return.Equations
		if len(eqs) == 0 {
			eqs = []ast.Equation{{Params: []ast.Pattern{e.Return.Param}, Body: e.Return.Body, NameSpan: e.Return.Sp}}
		}
		for _, eq := range eqs {
			scope := &blockScope{parent: g.locals, names: map[string]types.Scheme{}}
			g.locals = scope
			if e.State != nil {
				if _, dup := scope.parent.lookup(e.State.Name); dup || g.ck.boundName(e.State.Name) {
					g.errs = append(g.errs, diag.Errorf(e.State.NameSpan, "SHADOWING",
						"The handler state `%s` shadows a name that is already defined —\nFango does not allow shadowing. Choose a different name.", e.State.Name))
				}
				scope.names[e.State.Name] = types.Scheme{Body: stateTy}
			}
			oldPins := g.patternPins
			g.patternPins = scope.parent
			patTy := g.pattern(eq.Params[0], scope)
			g.patternPins = oldPins
			g.cs = append(g.cs, Constraint{Left: patTy, Right: bodyTy, Span: eq.Params[0].Span(), Why: Why{Kind: WhyPattern}})
			rt := g.expr(eq.Body)
			g.locals = scope.parent
			g.cs = append(g.cs, Constraint{Left: rt, Right: result, Span: eq.Body.Span(), Why: Why{Kind: WhyCaseBranches}})
		}
	} else {
		g.cs = append(g.cs, Constraint{Left: bodyTy, Right: result, Span: e.Body.Span(), Why: Why{Kind: WhyCaseBranches}})
	}
	// Only the handled label is removed. Any residual effects from the body,
	// clauses, or return clause compose into the surrounding expression.
	g.ambient, g.clauseEffects = savedAmbient, savedSink
	g.performs(residual, e.Span(), false)
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

type resumeFailure struct {
	kind, message string
	span          source.Span
}

// tailResume proves the discipline for one clause identity. Resumes owned by
// nested operation clauses are independent; bodies and return rows retain the
// surrounding identity and therefore cannot hide or duplicate its resume.
func (g *generator) tailResume(owner types.ResumeID, e ast.Expr, tail bool) *resumeFailure {
	if a, ok := e.(*ast.App); ok {
		if op := g.ck.OpCalls[a]; op != nil && op.Abort {
			// A saturated abort operation is an explicit exceptional terminal.
			// Its evaluated payload must still not contain this clause's resume.
			for _, arg := range appArgs(a) {
				if failure := g.tailResume(owner, arg, false); failure != nil {
					return failure
				}
			}
			return nil
		}
		if r, yes := a.Fn.(*ast.Resume); yes && g.ck.ResumeOwners[r] == owner {
			if failure := g.tailResume(owner, a.Arg, false); failure != nil {
				return failure
			}
			if r.NextState != nil {
				if failure := g.tailResume(owner, r.NextState, false); failure != nil {
					return failure
				}
			}
			if !tail {
				return &resumeFailure{"NON-TAIL RESUME", "`resume` must be the final action on every reachable clause path.", a.Span()}
			}
			return nil
		}
	}
	nontail := func(q ast.Expr) *resumeFailure {
		return g.tailResume(owner, q, false)
	}
	switch x := e.(type) {
	case *ast.If:
		if failure := nontail(x.Cond); failure != nil {
			return failure
		}
		if failure := g.tailResume(owner, x.Then, tail); failure != nil {
			return failure
		}
		if failure := g.tailResume(owner, x.Else, tail); failure != nil {
			return failure
		}
		return nil
	case *ast.Case:
		if failure := nontail(x.Scrutinee); failure != nil {
			return failure
		}
		for _, b := range x.Branches {
			if failure := g.tailResume(owner, b.Body, tail); failure != nil {
				return failure
			}
		}
		return nil
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
			if failure := nontail(q); failure != nil {
				return failure
			}
			if g.abortTerminal(q) {
				return nil
			}
		}
		return g.tailResume(owner, x.Result, tail)
	case *ast.App:
		if failure := nontail(x.Fn); failure != nil {
			return failure
		}
		if failure := nontail(x.Arg); failure != nil {
			return failure
		}
	case *ast.BinOp:
		if failure := nontail(x.L); failure != nil {
			return failure
		}
		if failure := nontail(x.R); failure != nil {
			return failure
		}
	case *ast.Neg:
		if failure := nontail(x.Operand); failure != nil {
			return failure
		}
	case *ast.RecordLit:
		for _, f := range x.Fields {
			if failure := nontail(f.Value); failure != nil {
				return failure
			}
		}
	case *ast.RecordGet:
		if failure := nontail(x.Record); failure != nil {
			return failure
		}
	case *ast.RecordUpdate:
		if failure := nontail(x.Record); failure != nil {
			return failure
		}
		for _, f := range x.Fields {
			if failure := nontail(f.Value); failure != nil {
				return failure
			}
		}
	case *ast.Lambda:
		if g.containsResume(owner, x.Body) {
			return &resumeFailure{"RESUME ESCAPES", "`resume` cannot be captured by a lambda.", x.Body.Span()}
		}
	case *ast.Handle:
		if failure := nontail(x.Body); failure != nil {
			return failure
		}
		if x.State != nil {
			if failure := nontail(x.State.Initial); failure != nil {
				return failure
			}
		}
		for i := range x.Clauses {
			cl := &x.Clauses[i]
			if len(cl.Equations) == 0 {
				if failure := nontail(cl.Body); failure != nil {
					return failure
				}
			} else {
				for _, eq := range cl.Equations {
					if failure := nontail(eq.Body); failure != nil {
						return failure
					}
				}
			}
		}
		if x.Return != nil {
			if len(x.Return.Equations) == 0 {
				if failure := nontail(x.Return.Body); failure != nil {
					return failure
				}
			} else {
				for _, eq := range x.Return.Equations {
					if failure := nontail(eq.Body); failure != nil {
						return failure
					}
				}
			}
		}
	case *ast.Resume:
		if x.NextState != nil {
			if failure := nontail(x.NextState); failure != nil {
				return failure
			}
		}
		if g.ck.ResumeOwners[x] == owner {
			return &resumeFailure{"RESUME ESCAPES", "`resume` must be applied directly to exactly one value.", x.Sp}
		}
	case *ast.Quote:
		for _, h := range g.ck.QuoteHoles[x] {
			if failure := nontail(h.Operand); failure != nil {
				return failure
			}
		}
	case *ast.Splice:
		if failure := nontail(x.Operand); failure != nil {
			return failure
		}
	case *ast.OpChain:
		for _, operand := range x.Operands {
			if failure := nontail(operand); failure != nil {
				return failure
			}
		}
	case *ast.IntLit, *ast.FloatLit, *ast.StringLit, *ast.CharLit, *ast.UnitLit,
		*ast.Var, *ast.Ctor, *ast.TypeOf, *ast.MetaValue:
		// Leaves cannot contain a resume occurrence.
	default:
		return &resumeFailure{"RESUME CHECK ERROR", fmt.Sprintf("The resume checker does not know expression %T.", e), e.Span()}
	}
	if tail {
		return &resumeFailure{"MISSING RESUME", "Every operation-clause path must end with exactly one call to `resume`.", e.Span()}
	}
	return nil
}

func (g *generator) abortTerminal(e ast.Expr) bool {
	a, ok := e.(*ast.App)
	return ok && g.ck.OpCalls[a] != nil && g.ck.OpCalls[a].Abort
}

func (g *generator) containsResume(owner types.ResumeID, e ast.Expr) bool {
	return g.tailResume(owner, e, false) != nil
}

// intrinsicSpine reports the compiler intrinsic at the head of an
// application spine, and how many arguments the spine applies.
func (g *generator) intrinsicSpine(e *ast.App) (string, int) {
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
		return "", n
	}
	if _, local := g.locals.lookup(v.Name); local {
		return "", n
	}
	if _, declared := g.ck.Intrinsics[v.Name]; !declared {
		return "", n
	}
	return v.Name, n
}

// intrinsicCall checks the saturated intrinsic spine while retaining its
// resolved identity for ownership and lowering. Callback arguments use the
// same directional compatibility as ordinary and partial applications.
func (g *generator) intrinsicCall(e *ast.App, name string) types.Type {
	sch := g.ck.Intrinsics[name]
	arity := types.IntrinsicArity(name)
	inst := g.instantiateAt(sch, e.Span(), name)
	g.ck.ExprTypes[appHead(e)] = inst
	params, result := peelOperation(inst, arity)
	if len(params) != arity {
		return result
	}
	cur := inst
	var last *types.TFun
	for range arity {
		last = cur.(*types.TFun)
		cur = last.Ret
	}
	for i, arg := range appArgs(e) {
		if i >= arity {
			break
		}
		at := g.exprWant(arg, params[i])
		g.cs = append(g.cs, g.argument(at, params[i], arg))
	}

	g.performs(last.Eff, e.Span(), false)
	if name == types.WorkPackName || name == types.WorkRegisterName {
		g.cs[len(g.cs)-1].WorkCharge = true
	}
	return result
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
func (g *generator) function(name string, nameSpan source.Span, params []ast.Pattern, body ast.Expr) types.Type {
	return g.functionEquations(name, nameSpan, []ast.Equation{{Params: params, Body: body, NameSpan: nameSpan}}, nil)
}

// functionWithAnnotatedParams uses only the annotation's argument types while
// independently inferring every arrow's effects. This lets callback effects
// flow into a higher-order body without allowing an overstated result row to
// manufacture effects the body never performs.
func (g *generator) functionWithAnnotatedParams(name string, nameSpan source.Span, params []ast.Pattern, body ast.Expr, ann types.Type) types.Type {
	return g.functionEquations(name, nameSpan, []ast.Equation{{Params: params, Body: body, NameSpan: nameSpan}}, ann)
}

func declEquations(d *ast.ValueDecl) []ast.Equation {
	if len(d.Equations) > 0 {
		return d.Equations
	}
	return []ast.Equation{{Params: d.Params, Body: d.Body, NameSpan: d.NameSpan}}
}

// functionEquations infers one shared worker type while giving each equation
// an independent pattern scope. All bodies contribute to the final arrow's
// effect row and must agree on one result type.
func (g *generator) functionEquations(name string, nameSpan source.Span, eqs []ast.Equation, ann types.Type) types.Type {
	outer := g.locals
	paramTys := make([]types.Type, len(eqs[0].Params))
	if ann != nil {
		cur := ann
		for i := range paramTys {
			fn, ok := cur.(*types.TFun)
			if !ok {
				g.errs = append(g.errs, diag.Errorf(nameSpan, "TYPE MISMATCH", "The annotation for `%s` has fewer function parameters than its definition.", name))
				return ann
			}
			paramTys[i] = fn.Arg
			cur = fn.Ret
		}
	} else {
		for i := range paramTys {
			paramTys[i] = g.ck.Sup.FreshVar(types.General)
		}
	}
	bodyAmbient := types.Row{Tail: g.ck.Sup.FreshVar(types.RowVar)}
	var annotationAmbient *types.Row
	if ann != nil {
		cur := ann
		for range paramTys {
			if fn, ok := cur.(*types.TFun); ok {
				r := fn.Eff
				annotationAmbient = &r
				cur = fn.Ret
			}
		}
	}
	resultTy := g.ck.Sup.FreshVar(types.General)
	funTy := g.wrapFunction(paramTys, resultTy, bodyAmbient)
	if g.ck.recursive != nil && outer == nil {
		if provisional := g.ck.recursive.types[name]; provisional != nil {
			g.cs = append(g.cs, Constraint{Left: provisional, Right: funTy, Span: nameSpan, Why: Why{Kind: WhyRecursion, Name: name}})
		}
	}
	for _, eq := range eqs {
		if len(eq.Params) != len(paramTys) {
			g.errs = append(g.errs, diag.Errorf(eq.NameSpan, "INCONSISTENT ARITY", "All equations for `%s` must have %d argument(s).", types.SurfaceName(name), len(paramTys)))
			continue
		}
		scope := &blockScope{parent: outer, names: map[string]types.Scheme{name: {Body: funTy}}}
		g.locals = scope
		oldPins := g.patternPins
		g.patternPins = outer
		oldBinder := g.patternBinder
		g.patternBinder = "parameter"
		for i, p := range eq.Params {
			pt := g.pattern(p, scope)
			g.cs = append(g.cs, Constraint{Left: pt, Right: paramTys[i], Span: p.Span(), Why: Why{Kind: WhyPattern}})
		}
		g.patternBinder = oldBinder
		g.patternPins = oldPins
		savedAnnotationAmbient := g.annotationAmbient
		g.annotationAmbient = annotationAmbient
		savedAmbient, savedSink := g.enterAmbient(bodyAmbient)
		bodyTy := g.expr(eq.Body)
		g.leaveAmbient(savedAmbient, savedSink)
		g.annotationAmbient = savedAnnotationAmbient
		g.cs = append(g.cs, Constraint{Left: resultTy, Right: bodyTy, Span: eq.NameSpan, Why: Why{Kind: WhyRecursion, Name: name}})
	}
	g.locals = outer
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
func (g *generator) bindParams(scope *blockScope, params []ast.Pattern) []types.Type {
	tys := make([]types.Type, len(params))
	for i, p := range params {
		tys[i] = g.pattern(p, scope)
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
		if bind.Pattern != nil {
			if len(patternNames(bind.Pattern, nil)) == 0 {
				g.errs = append(g.errs, diag.Errorf(bind.Pattern.Span(), "PATTERN BINDING", "A destructuring binding must bind at least one name."))
			}
			rhsTy := g.expr(bind.Body)
			oldPins := g.patternPins
			g.patternPins = g.locals
			patTy := g.pattern(bind.Pattern, g.locals)
			g.patternPins = oldPins
			g.cs = append(g.cs, Constraint{Left: patTy, Right: rhsTy, Span: bind.Pattern.Span(), Why: Why{Kind: WhyPattern}})
			g.ck.BindTypes[bind] = rhsTy
			g.ck.BindSchemes[bind] = types.Scheme{Body: rhsTy}
			continue
		}
		if _, dup := g.locals.lookup(bind.Name); dup || g.ck.boundName(bind.Name) {
			where := "at the top level"
			if dup {
				where = "earlier in this block"
			}
			g.errs = append(g.errs, diag.Errorf(bind.NameSpan, "SHADOWING",
				"The name `%s` is already defined %s — Fango does not allow\nshadowing. Choose a different name.", bind.Name, where))
		}
		var ty types.Type
		var annVars []*types.TVar
		var given []types.Pred
		predStart := len(g.preds)
		recordStart := len(g.records)
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
			eqs := bind.Equations
			if len(eqs) == 0 {
				eqs = []ast.Equation{{Params: bind.Params, Body: bind.Body, NameSpan: bind.NameSpan}}
			}
			ty = g.functionEquations(bind.Name, bind.NameSpan, eqs, annTy)
		} else if len(bind.Params) > 0 {
			eqs := bind.Equations
			if len(eqs) == 0 {
				eqs = []ast.Equation{{Params: bind.Params, Body: bind.Body, NameSpan: bind.NameSpan}}
			}
			ty = g.functionEquations(bind.Name, bind.NameSpan, eqs, nil)
		} else {
			ty = g.expr(bind.Body)
		}
		if bind.Ann != nil && annTy != nil {
			// Observe the inferred row before equality can populate it from the
			// annotation. Record obligations still resolve after annotation context.
			sub, _, errs := g.solveConstraints(nil)
			g.ck.Sub = sub
			g.errs = append(g.errs, errs...)
			g.cs = nil
			if g.sharedOpenEffects(ty) {
				// Check the type shape now, but do not let the annotation's
				// labels populate a body whose recursive callees are unfinished.
				shape := g.annotationShape(annTy, ty, g.scopeFreeIDs(), bind)
				g.deferLocalAnnotation(annTy, shape, annVars, bind)
				annTy = shape
			} else if !sameKnownEffects(g.ck.Sub.Apply(annTy), g.ck.Sub.Apply(ty)) {
				g.errs = append(g.errs, diag.Errorf(bind.Ann.Sp, "EFFECT MISMATCH", "The effect row in the annotation for `%s` does not exactly match the effects performed by its body.", bind.Name))
			}
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
			g.solveHere(recordStart)
			// Rows shared with an enclosing scope are still being inferred,
			// including self-recursive functions and mutual dependency groups.
			if bind.Ann == nil {
				g.ck.closeSingleRowsExcept(ty, g.scopeFreeIDs())
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
			eqs := bind.Equations
			if len(eqs) == 0 {
				eqs = []ast.Equation{{Params: bind.Params, Body: bind.Body}}
			}
			ty = g.ck.runnerControl(ty, len(bind.Params), eqs)
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
				if mentionsAny(p.Ty, quant) {
					if bind.Ann != nil {
						g.errs = append(g.errs, diag.Errorf(bind.NameSpan, "MISSING CONSTRAINT", "Add `%s` to the annotation.", types.ShowPred(p.Class, p.Ty)))
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
//
// from is the index of the first obligation belonging to this binding.
// Obligations raised by the enclosing declaration are still given a chance to
// make progress here, but they are not declared ambiguous: the code that would
// decide them may not have been reached yet.
func (g *generator) solveHere(from int) {
	if len(g.cs) == 0 {
		g.resolveRecords(g.ck.recursive == nil, from)
		return
	}
	sub, _, errs := g.solveConstraints(nil)
	g.ck.Sub = sub
	g.errs = append(g.errs, errs...)
	g.cs = nil
	g.resolveRecords(g.ck.recursive == nil, from)
}

// inferredRecord types `{ field = value, ... }`, whose nominal type is the one
// the context expects rather than one the source names. The literal's type is
// a fresh variable; anything that pins it — an annotation, a parameter type,
// an enclosing field, a unified branch — decides which record this is. A want
// already solved to a nominal type is constrained here so the mismatch is
// reported at the literal, and everything else waits for the fixed point.
// Field labels are never consulted: they check the schema once the type is
// known, they do not choose it.
func (g *generator) inferredRecord(e *ast.RecordLit, want types.Type) types.Type {
	recv := g.ck.Sup.FreshVar(types.General)
	if want != nil {
		if con, ok := g.ck.Sub.Apply(want).(*types.TCon); ok {
			g.cs = append(g.cs, Constraint{Left: recv, Right: con, Span: e.Sp, Why: Why{Kind: WhyCall}})
		}
	}
	ob := &recordObligation{kind: recordBuild, node: e, receiver: recv, result: recv, fieldSpan: e.Sp}
	seen := map[string]bool{}
	for _, f := range e.Fields {
		if seen[f.Name] {
			g.errs = append(g.errs, diag.Errorf(f.NameSpan, "RECORD FIELDS", "The field `%s` is provided more than once.", f.Name))
		}
		seen[f.Name] = true
		fieldTy := g.expr(f.Value)
		ob.updates = append(ob.updates, recordUpdateObligation{name: f.Name, span: f.NameSpan, ty: fieldTy, candidates: f.Records, bind: g.lambdaBinders[f.Value]})
	}
	g.records = append(g.records, ob)
	return recv
}

// inferredRecordPattern types `{ field = pattern, ... }`. Binders enter scope
// immediately, so a branch body sees them; only their types wait for the
// scrutinee to name a record.
func (g *generator) inferredRecordPattern(p *ast.PRecord, scope *blockScope) types.Type {
	recv := g.ck.Sup.FreshVar(types.General)
	ob := &recordObligation{kind: recordMatch, pat: p, receiver: recv, result: recv, fieldSpan: p.Sp}
	seen := map[string]bool{}
	for _, f := range p.Fields {
		if seen[f.Name] {
			g.errs = append(g.errs, diag.Errorf(f.NameSpan, "RECORD FIELDS", "The field `%s` appears more than once in this record pattern.", f.Name))
		}
		seen[f.Name] = true
		ob.updates = append(ob.updates, recordUpdateObligation{name: f.Name, span: f.NameSpan, ty: g.pattern(f.Pattern, scope), candidates: f.Records})
	}
	g.records = append(g.records, ob)
	return recv
}

// unknownRecord explains a capitalized name before `{` that is not a record
// type. A name in that position always names the record, so a constructor
// there is a user who meant to hand it an inferred literal; say so, because
// "I don't know a record type named `Wrap`" is true but unhelpful when `Wrap`
// is right there in scope.
func (g *generator) unknownRecord(name string, sp source.Span) diag.Error {
	if _, ok := g.ck.Ctors[name]; ok {
		return diag.Errorf(sp, "UNKNOWN RECORD", "`%s` is a constructor, not a record type, and a capitalized name\nbefore `{` always names the record being built. To hand `%s` an\ninferred record literal, parenthesize the literal: `%s ({ ... })`.",
			types.SurfaceName(name), types.SurfaceName(name), types.SurfaceName(name))
	}
	return diag.Errorf(sp, "UNKNOWN RECORD", "I don't know an exposed record type named `%s`.", types.SurfaceName(name))
}

// hasPendingInferredRecord reports whether some inferred `{ ... }` is still
// waiting to learn which record it is.
func (g *generator) hasPendingInferredRecord() bool {
	for _, ob := range g.records {
		if !ob.resolved && ob.kind != recordAccess {
			return true
		}
	}
	return false
}

// resolveRecords discharges field obligations to a fixed point. One
// obligation's receiver is often another's result — `ctor.fields` decides the
// element type a later `field.index` reads — so a single pass would make
// resolution depend on the order the obligations were collected in. Each pass
// solves what it learned, which is what lets the next one make progress;
// only when a pass learns nothing are the survivors genuinely ambiguous.
func (g *generator) resolveRecords(final bool, from int) {
	for g.recordPass(false, from) > 0 {
	}
	if final {
		g.recordPass(true, from)
	}
}

// from bounds which obligations may be reported as ambiguous on a final pass.
// Every obligation still participates: an outer one that resolves here is what
// lets an inner one make progress.
func (g *generator) recordPass(final bool, from int) int {
	resolved := 0
	var constraints []Constraint
	for i, ob := range g.records {
		if ob.resolved {
			continue
		}
		report := final && i >= from
		t := g.ck.Sub.Apply(ob.receiver)
		con, ok := t.(*types.TCon)
		if !ok {
			if report {
				if ob.kind == recordAccess {
					g.errs = append(g.errs, diag.Errorf(ob.fieldSpan, "AMBIGUOUS FIELD", "The record type is not known here; add a type annotation or provide a contextual record type."))
				} else {
					g.errs = append(g.errs, diag.Errorf(ob.fieldSpan, "AMBIGUOUS RECORD", "I cannot tell which record this is. Name its type, as in\n`Counts { ... }`, or add an annotation that gives it one. Field\nnames alone never choose a record type."))
				}
				ob.resolved = true
				resolved++
			}
			continue
		}
		adt := g.ck.ADTs[con.Unique]
		if adt == nil || !adt.IsRecord() {
			g.errs = append(g.errs, diag.Errorf(ob.fieldSpan, "NOT A RECORD", "A value of type `%s` has no record fields.", types.Show(t)))
			ob.resolved = true
			resolved++
			continue
		}
		visible := func(candidates []string) bool {
			// Parser-only clients such as the REPL have no module abstraction
			// boundary. The batch resolver marks even an inaccessible label with
			// a non-nil empty slice, preserving the distinction here.
			if candidates == nil {
				return true
			}
			for _, name := range candidates {
				if name == adt.Con.Name {
					return true
				}
			}
			return false
		}
		fieldTypes := adt.InstFields(adt.Ctors[0], con.Args)
		if ob.field != "" {
			idx, _ := adt.RecordField(ob.field)
			switch {
			// Existence first: a label this record does not have is unknown
			// whatever the module boundary says, and reporting it as private
			// would describe a field that does not exist.
			case idx < 0:
				g.errs = append(g.errs, diag.Errorf(ob.fieldSpan, "UNKNOWN FIELD", "Record `%s` has no field named `%s`.", types.SurfaceName(adt.Con.Name), ob.field))
			case !visible(ob.candidates):
				g.errs = append(g.errs, diag.Errorf(ob.fieldSpan, "PRIVATE RECORD FIELD", "The fields of record `%s` are not exposed to this module.", types.SurfaceName(adt.Con.Name)))
			default:
				// The stored field is what the projection has, and the use
				// site is what it must fit, so this is subsumption in that
				// direction rather than an equality. A field declared at a
				// pure arrow is usable in an effectful body for the same
				// reason a pure argument is: the row a caller allows is an
				// upper bound, not a description of the value. Unifying here
				// instead let the use site's ambient row reach the field
				// first, and a closed declaration then disagreed with it.
				constraints = append(constraints, Constraint{Left: fieldTypes[idx], Right: ob.result, Span: ob.fieldSpan, Why: Why{Kind: WhyProjection, Name: ob.field}, Subsume: true, ADTs: g.ck.ADTs})
			}
		}
		for _, u := range ob.updates {
			idx, _ := adt.RecordField(u.name)
			switch {
			case idx < 0:
				g.errs = append(g.errs, diag.Errorf(u.span, "UNKNOWN FIELD", "Record `%s` has no field named `%s`.", types.SurfaceName(adt.Con.Name), u.name))
			case !visible(u.candidates):
				g.errs = append(g.errs, diag.Errorf(u.span, "PRIVATE RECORD FIELD", "The fields of record `%s` are not exposed to this module.", types.SurfaceName(adt.Con.Name)))
			default:
				constraints = append(constraints, Constraint{Left: u.ty, Right: fieldTypes[idx], Span: u.span, Why: Why{Kind: WhyCall}, Subsume: ob.kind != recordMatch, ADTs: g.ck.ADTs, Bind: u.bind})
			}
		}
		if ob.kind == recordBuild {
			// A literal builds the whole value, so every declared field must be
			// there. A pattern is a partial view and omitted fields stay
			// implicit wildcards.
			provided := map[string]bool{}
			for _, u := range ob.updates {
				provided[u.name] = true
			}
			for _, f := range adt.RecordFields {
				if !provided[f.Name] {
					g.errs = append(g.errs, diag.Errorf(ob.fieldSpan, "RECORD FIELDS", "Record `%s` is missing field `%s`.", types.SurfaceName(adt.Con.Name), f.Name))
				}
			}
		}
		if ob.kind == recordMatch {
			g.ck.RecordPatternUses[ob.pat] = adt
		} else {
			g.ck.RecordUses[ob.node] = adt
		}
		ob.resolved = true
		resolved++
	}
	if len(constraints) > 0 {
		sub, _, errs := Solve(constraints, nil, g.ck.Sub, g.ck.B, g.ck.Sup)
		g.ck.Sub = sub
		g.errs = append(g.errs, errs...)
	}
	return resolved
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
		oldPins := g.patternPins
		g.patternPins = scope.parent
		patTy := g.pattern(br.Pattern, scope)
		g.patternPins = oldPins
		g.cs = append(g.cs, Constraint{
			Left: patTy, Right: scrutTy, Span: br.Pattern.Span(), Why: Why{Kind: WhyPattern},
		})
		bodyTy := g.expr(br.Body)
		g.locals = scope.parent
		g.cs = append(g.cs, Constraint{
			Left: bodyTy, Right: resultTy, Span: br.Body.Span(), Why: Why{Kind: WhyCaseBranches}, Subsume: true, ADTs: g.ck.ADTs,
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
	case *ast.PUnit:
		return g.ck.B.Unit
	case *ast.PVar:
		if _, dup := g.locals.lookup(p.Name); dup || g.ck.boundName(p.Name) {
			kind := g.patternBinder
			if kind == "" {
				kind = "pattern variable"
			}
			g.errs = append(g.errs, diag.Errorf(p.Sp, "SHADOWING",
				"The %s `%s` shadows a name that is already defined —\nFango does not allow shadowing. Choose a different name.", kind, p.Name))
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
	case *ast.PChar:
		return g.ck.B.Char
	case *ast.PPin:
		var sch types.Scheme
		var ok bool
		if g.patternPins != nil {
			sch, ok = g.patternPins.lookup(p.Name)
		}
		if !ok {
			sch, ok = g.ck.Env.Lookup(p.Name)
		}
		if !ok {
			g.errs = append(g.errs, diag.Errorf(p.NameSpan, "NAMING ERROR", "I don't know an existing value named `%s` to pin here.", types.SurfaceName(p.Name)))
			return g.ck.Sup.FreshVar(types.General)
		}
		v := &ast.Var{Name: p.Name, Sp: p.NameSpan}
		ty := g.instantiateAt(sch, p.Sp, p.Name)
		g.ck.ExprSchemes[v], g.ck.ExprTypes[v], g.ck.PinExprs[p] = sch, ty, v
		g.ck.ExprCaptures[v] = g.instantiateCaptures(sch)
		g.preds = append(g.preds, predObligation{pred: g.ck.StandardPred("Eq", ty), span: p.Sp})
		return ty
	case *ast.PRecord:
		if p.Name == "" {
			return g.inferredRecordPattern(p, scope)
		}
		named, ok := g.ck.TypeNames[p.Name].(*types.TCon)
		var adt *types.ADTInfo
		if ok {
			adt = g.ck.ADTs[named.Unique]
		}
		if adt == nil || !adt.IsRecord() {
			g.errs = append(g.errs, g.unknownRecord(p.Name, p.NameSpan))
			for _, f := range p.Fields {
				g.pattern(f.Pattern, scope)
			}
			return g.ck.Sup.FreshVar(types.General)
		}
		fieldTys, result := g.instantiateCtor(adt.Ctors[0])
		seen := map[string]bool{}
		for _, f := range p.Fields {
			if seen[f.Name] {
				g.errs = append(g.errs, diag.Errorf(f.NameSpan, "RECORD FIELDS", "The field `%s` appears more than once in this record pattern.", f.Name))
			}
			seen[f.Name] = true
			ft := g.pattern(f.Pattern, scope)
			idx, _ := adt.RecordField(f.Name)
			if idx < 0 {
				g.errs = append(g.errs, diag.Errorf(f.NameSpan, "UNKNOWN FIELD", "Record `%s` has no field named `%s`.", types.SurfaceName(adt.Con.Name), f.Name))
			} else {
				g.cs = append(g.cs, Constraint{Left: ft, Right: fieldTys[idx], Span: f.Pattern.Span(), Why: Why{Kind: WhyPattern}})
			}
		}
		g.ck.RecordPatternUses[p] = adt
		return result
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
		if adt := g.ck.ADTs[info.Result.Unique]; adt != nil && adt.NativeIndexed {
			g.errs = append(g.errs, diag.Errorf(p.Span(), "NATIVE HANDLE REPRESENTATION", "An indexed native handle's representation cannot be opened or re-indexed in Fango."))
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

// binOp types an operator application as the call it is: the operator is an
// ordinary value name, already canonical by the time inference runs, so
// `a + b` types exactly as `(+) a b`.
//
// `&&` and `||` are the exception to the operator-is-a-call rule: they have
// no implementing value because elaboration turns them into an `if` that
// leaves the right operand unevaluated (doc/reference.md, "Values and
// operators").
func (g *generator) binOp(e *ast.BinOp) types.Type {
	if fixity.IsShortCircuit(e.Op) {
		for _, side := range []ast.Expr{e.L, e.R} {
			ty := g.expr(side)
			g.cs = append(g.cs, Constraint{
				Left: ty, Right: g.ck.B.Bool, Span: side.Span(),
				Why: Why{Kind: WhyBoolOperand, Op: e.Op},
			})
		}
		return g.ck.B.Bool
	}
	app := &ast.App{Fn: &ast.App{Fn: &ast.Var{Name: e.Op, Sp: e.OpSpan}, Arg: e.L}, Arg: e.R}
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

func (g *generator) instantiateCaptures(s types.Scheme) types.CaptureSet {
	if len(s.CaptureVars) == 0 {
		return s.Captures
	}
	m := make(map[types.CaptureVar]types.CaptureVar, len(s.CaptureVars))
	for _, v := range s.CaptureVars {
		m[v] = g.ck.Sup.FreshCapture()
	}
	return types.SubstCaptureVars(s.Captures, m)
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
	t := types.SubstRigid(s.Body, m)
	if (op == types.WorkPackName || op == types.WorkRegisterName) && g.ck.Intrinsics[op].Body != nil {
		if fn, ok := t.(*types.TFun); ok {
			if last, ok := fn.Ret.(*types.TFun); ok {
				g.workRows = append(g.workRows, last.Eff)
			}
		}
	}
	return t
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
