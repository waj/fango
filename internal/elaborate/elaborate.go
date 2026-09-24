// Package elaborate bridges the typed AST and Core: zonking (fully applying
// the solved substitution), defaulting residual metavariables (Number →
// Int, General → Unit), the post-defaulting ground checks that Elm's
// `comparable` kind flag would otherwise do (equatable/orderable/printable),
// and constant folding. Lambda-lifting, saturation analysis, and decision
// trees are implemented by the focused helpers in lift.go and match.go.
//
// Constant folding here is a correctness requirement, not an optimization:
// Go evaluates constant expressions exactly (arbitrary precision), so
// emitting literal arithmetic verbatim would make `1.0 / 0.0` a Go compile
// error, round `0.1 + 0.2` differently than the runtime, and overflow on
// `9223372036854775807 + 1`. Folding uses the same Go int64/float64
// operations the interpreter performs, so both backends agree by
// construction.
package elaborate

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/natives"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// Module elaborates checked declarations into a Core program. After it
// returns without errors, Core contains no metavariables — asserted by
// core.Lint.
func Module(infos []infer.DeclInfo, ck *infer.Checker) (*core.Prog, []diag.Error) {
	infos = append(append([]infer.DeclInfo{}, ck.PreludeInfos...), infos...)
	effects := make([]*types.EffectInfo, 0, len(ck.Effects))
	seenEffects := map[int]bool{}
	for _, eff := range ck.Effects {
		if seenEffects[eff.Unique] {
			continue
		}
		seenEffects[eff.Unique] = true
		effects = append(effects, eff)
	}
	sort.Slice(effects, func(i, j int) bool { return effects[i].Unique < effects[j].Unique })
	// A compile-time-only type is not emitted, for the same reason Bool is
	// not: no Go type corresponds to it. Code exists only inside the
	// compiler, so nothing downstream can name it.
	adts := make([]*types.ADTInfo, 0, len(ck.ADTOrder))
	for _, adt := range ck.ADTOrder {
		if !ck.IsCompileTimeOnly(adt.Con) {
			adts = append(adts, adt)
		}
	}
	p := &core.Prog{ADTs: adts, Effects: effects, Entry: ck.EntryName, Natives: ck.Natives, Intrinsics: intrinsicIdentities(ck)}
	var errs []diag.Error
	for _, inst := range ck.Instances {
		if ck.IsCompileTimeOnly(inst.Class.DictType(inst.Head)) {
			continue
		}
		d, es := instanceDefinition(inst, ck)
		p.Defs = append(p.Defs, d)
		errs = append(errs, es...)
	}
	p.Defs = append(p.Defs, IntrinsicDefs(ck)...)
	kept := infos[:0:0]
	for _, info := range infos {
		// A compile-time-only definition is not emitted: it exists only for
		// the compiler's own evaluator, which elaborates it separately
		// (doc/design.md, "Compile-time metaprogramming"). Nothing runtime
		// can reference it, because no runtime expression may have its type.
		if ck.IsCompileTimeOnly(ck.Sub.Apply(info.Type)) {
			continue
		}
		kept = append(kept, info)
	}
	infos = kept
	for _, info := range infos {
		owner := symbolOwner(info.Name)
		defs, declErrs := decl(info, ck, owner != "")
		for i := range defs {
			defs[i].Owner = owner
		}
		errs = append(errs, declErrs...)
		p.Defs = append(p.Defs, defs...)
		def := &defs[0]
		if def.Name == ck.EntryName && len(def.Params) == 0 {
			if _, isFn := def.Type.(*types.TFun); isFn {
				errs = append(errs, diag.Errorf(info.NameSpan, "BAD MAIN",
					"`main` must be a value, or use the supported function form `main _ : () ->{IO} ()`."))
			} else if len(def.TyParams) > 0 {
				errs = append(errs, diag.Errorf(info.NameSpan, "BAD MAIN",
					"`main` must be a concrete value, but its type `%s` still has\ntype variables in it.", types.Show(def.Type)))
			}
		}
	}
	for _, d := range p.Defs {
		if d.Name == p.Entry && len(d.Params) == 0 {
			p.EntryDisplay = Display(&core.VarRef{Name: d.Name, Ty: d.Type}, ck, d.Owner)
		}
	}
	if len(errs) == 0 {
		specializeScalars(p, infos, ck)
		bindRows(p.Defs, ck)
		errs = append(errs, captureDiagnostics(core.InferCaptures(p, ck.B), ck, source.Span{})...)
		installCaptureSummaries(p.Defs, ck)
		core.SummarizeABI(p, nil)
	}
	return p, errs
}

func intrinsicIdentities(ck *infer.Checker) map[string]bool {
	identities := make(map[string]bool, len(ck.Intrinsics))
	for name := range ck.Intrinsics {
		identities[name] = true
	}
	return identities
}

// Increment elaborates the modules one REPL import added to the checker: the
// given instances (a suffix of ck.Instances) and then the checked
// declarations, under exactly the rules Module applies to a program —
// compile-time-only definitions are not emitted, each Def records its owner,
// lifted names are stable per module, scalar specialization runs, and
// capture summaries are solved over the whole increment. Unlike Module it
// re-emits neither the prelude nor earlier instances, so a session installs
// each definition exactly once.
//
// intrinsics names the compiler intrinsics the increment declared; their
// synthesized definitions are installed with it, exactly as Module installs
// them for a program, so a module that calls `Scope.bracket` lints and runs.
// context holds the session's already-installed definitions, which the
// capture analysis reads for the call-site rules (core.InferCapturesIn).
func Increment(infos []infer.DeclInfo, instances []*infer.InstanceInfo, intrinsics []string, context []core.Def, ck *infer.Checker) ([]core.Def, []diag.Error) {
	var defs []core.Def
	var errs []diag.Error
	defs = append(defs, intrinsicDefsNamed(intrinsics, ck)...)
	for _, inst := range instances {
		if ck.IsCompileTimeOnly(inst.Class.DictType(inst.Head)) {
			continue
		}
		d, es := instanceDefinition(inst, ck)
		defs = append(defs, d)
		errs = append(errs, es...)
	}
	kept := infos[:0:0]
	for _, info := range infos {
		if !ck.IsCompileTimeOnly(ck.Sub.Apply(info.Type)) {
			kept = append(kept, info)
		}
	}
	for _, info := range kept {
		owner := symbolOwner(info.Name)
		ds, es := decl(info, ck, owner != "")
		for i := range ds {
			ds[i].Owner = owner
		}
		defs = append(defs, ds...)
		errs = append(errs, es...)
	}
	if len(errs) > 0 {
		return defs, errs
	}
	p := &core.Prog{ADTs: ck.ADTOrder, Effects: effectList(ck), Defs: defs, Natives: ck.Natives}
	specializeScalars(p, kept, ck)
	bindRows(p.Defs, ck)
	errs = append(errs, captureDiagnostics(core.InferCapturesIn(p, context, ck.B), ck, source.Span{})...)
	installCaptureSummaries(p.Defs, ck)
	core.SummarizeABI(p, context)
	return p.Defs, errs
}

// LintProg checks a set of definitions against the Core invariants in the
// context of the checker's current types, effects, and natives — the REPL's
// counterpart to the lint the batch pipeline runs on a whole program.
func LintProg(defs []core.Def, ck *infer.Checker) []error {
	return core.Lint(&core.Prog{ADTs: ck.ADTOrder, Effects: effectList(ck), Defs: defs, Natives: ck.Natives, Intrinsics: intrinsicIdentities(ck)}, ck.B)
}

// LintProgIn validates an owned module increment against installed dependency
// signatures and capture contracts without traversing dependency bodies.
// flowsProven says these exact definitions have already had their lifetime
// obligations discharged, which lets lint compare the contracts it
// reconstructs without discharging them a second time.
func LintProgIn(defs, context []core.Def, ck *infer.Checker, flowsProven bool) []error {
	return core.LintIn(&core.Prog{ADTs: ck.ADTOrder, Effects: effectList(ck), Defs: defs, Natives: ck.Natives,
		Intrinsics: intrinsicIdentities(ck), CaptureFlowsProven: flowsProven}, context, ck.B)
}

// AssembleModuleProgram joins already checked and owner-linted module Core.
// It performs only entry-specific validation and creates the whole-program
// metadata consumed by lowering and emission.
func AssembleModuleProgram(defs []core.Def, infos []infer.DeclInfo, entry string, ck *infer.Checker) (*core.Prog, []diag.Error) {
	adts := make([]*types.ADTInfo, 0, len(ck.ADTOrder))
	for _, adt := range ck.ADTOrder {
		if !ck.IsCompileTimeOnly(adt.Con) {
			adts = append(adts, adt)
		}
	}
	p := &core.Prog{ADTs: adts, Effects: effectList(ck), Defs: defs, Entry: entry, Natives: ck.Natives, Intrinsics: intrinsicIdentities(ck), CaptureContractsChecked: true}
	spans := map[string]source.Span{}
	for _, info := range infos {
		spans[info.Name] = info.NameSpan
	}
	var errs []diag.Error
	for i := range p.Defs {
		d := &p.Defs[i]
		if d.Name != entry {
			continue
		}
		if len(d.Params) == 0 {
			if _, isFn := d.Type.(*types.TFun); isFn {
				errs = append(errs, diag.Errorf(spans[d.Name], "BAD MAIN", "`main` must be a value, or use the supported function form `main _ : () ->{IO} ()`."))
			} else if len(d.TyParams) > 0 {
				errs = append(errs, diag.Errorf(spans[d.Name], "BAD MAIN", "`main` must be a concrete value, but its type `%s` still has\ntype variables in it.", types.Show(d.Type)))
			}
			p.EntryDisplay = Display(&core.VarRef{Name: d.Name, Ty: d.Type}, ck, d.Owner)
		}
	}
	return p, errs
}

// Decl elaborates one declaration — also the REPL's per-input entry point.
// The first returned Def is the declaration itself; any further Defs are
// lambda-lifted polymorphic block bindings (doc/design.md, "Go backend and runtime", lift.go).
func Decl(info infer.DeclInfo, ck *infer.Checker) ([]core.Def, []diag.Error) {
	return DeclIn(info, nil, ck)
}

// DeclIn is Decl with the session's installed definitions as capture-analysis
// context, so a prompt definition's calls into installed runners are checked.
func DeclIn(info infer.DeclInfo, context []core.Def, ck *infer.Checker) ([]core.Def, []diag.Error) {
	return DeclsIn([]infer.DeclInfo{info}, context, ck)
}

// DeclsIn elaborates complete dependency groups before analyzing captures.
func DeclsIn(infos []infer.DeclInfo, context []core.Def, ck *infer.Checker) ([]core.Def, []diag.Error) {
	var defs []core.Def
	var errs []diag.Error
	for _, info := range infos {
		owner := symbolOwner(info.Name)
		ds, es := decl(info, ck, owner != "")
		for i := range ds {
			ds[i].Owner = owner
		}
		defs = append(defs, ds...)
		errs = append(errs, es...)
	}
	var info infer.DeclInfo
	if len(infos) > 0 {
		info = infos[0]
	}
	if len(errs) == 0 {
		p := &core.Prog{ADTs: ck.ADTOrder, Effects: effectList(ck), Defs: defs, Natives: ck.Natives}
		bindRows(p.Defs, ck)
		errs = append(errs, captureDiagnostics(core.InferCapturesIn(p, context, ck.B), ck, info.NameSpan)...)
		installCaptureSummaries(defs, ck)
		for i := range defs {
			for _, err := range core.VerifyResumeStructure(defs[i].Body) {
				errs = append(errs, diag.Errorf(info.NameSpan, "INTERNAL RESUME INVARIANT", "%v", err))
			}
		}
	}
	return defs, errs
}

func decl(info infer.DeclInfo, ck *infer.Checker, stableLifts bool) ([]core.Def, []diag.Error) {
	el := newElab(ck, info.Name, info.Scheme)
	el.bodySubst = info.BodySubst
	el.selfInstance = info.Instance
	el.instanceLimit = info.InstanceLimit
	el.stableLifts = stableLifts
	rawType := ck.Sub.Apply(info.Type)
	el.defaultFree(rawType)
	rawType = ck.Sub.Apply(rawType)
	defType := el.eraseRuntimeKinds(eraseRows(rawType))
	effectParams := el.bindEffectParams(executingEffects(rawType, len(info.Params)))
	dictNames, dictTypes := el.bindDictionaries(info.Scheme.Preds)
	var params []string
	var body core.Expr
	if len(info.Params) > 0 {
		argTys, retTy := core.PeelFun(defType, len(info.Params))
		eqs := equationRows(info.Equations, info.Params, info.Body, info.NameSpan)
		params, body = el.workerBody(eqs, argTys, info.NameSpan, "function")
		body = el.adaptFunctionValue(body, retTy)
	} else {
		body = el.expr(info.Body)
		body = el.adaptFunctionValue(body, defType)
	}
	allParams := append(dictNames, params...)
	paramCaptures := make([]types.CaptureVar, len(allParams))
	for i := range paramCaptures {
		paramCaptures[i] = ck.Sup.FreshCapture()
	}
	def := core.Def{
		SourceType:    prependTypes(dictTypes, rawType),
		Name:          info.Name,
		Type:          prependTypes(dictTypes, defType),
		TyParams:      runtimeRigidVars(rawType),
		Params:        allParams,
		ParamCaptures: paramCaptures,
		EffectParams:  effectParams,
		Control:       core.ArrowControl(prependTypes(dictTypes, defType), len(allParams)),
		Body:          el.anf(body),
	}
	if control := core.ExprControl(def.Body); !def.IsWorker() && control.Transport == types.Machine {
		def.Control = control
	}
	return append([]core.Def{def}, el.aux...), el.errs
}

// IntrinsicDefs supplies bodies for the compiler intrinsics the checker
// declared. An intrinsic has no equations to elaborate: its meaning is a
// Core node, so the definition is built here rather than read from source.
// The compile-time evaluator installs the same definitions, so a splice sees
// the intrinsic the batch pipeline emits.
func IntrinsicDefs(ck *infer.Checker) []core.Def {
	names := make([]string, 0, len(ck.Intrinsics))
	for name := range ck.Intrinsics {
		names = append(names, name)
	}
	return intrinsicDefsNamed(names, ck)
}

func intrinsicDefsNamed(names []string, ck *infer.Checker) []core.Def {
	names = append([]string(nil), names...)
	sort.Strings(names)
	defs := make([]core.Def, 0, len(names))
	for _, name := range names {
		if _, declared := ck.Intrinsics[name]; !declared {
			continue
		}
		ty := (&elab{ck: ck}).eraseRuntimeKinds(eraseRows(ck.Intrinsics[name].Body))
		if types.WorkIntrinsic(name) {
			defs = append(defs, workDef(name, ty, ck))
		} else if name == types.ServiceRunName {
			defs = append(defs, serviceRunDef(ty, ck))
		} else if name == types.ScopeBracketName {
			// The declaration keeps its open row tail; Core does not.
			defs = append(defs, scopeBracketDef(name, ty, ck))
		} else if name == types.CoroutineFacetName || name == types.CoroutineScopeName || name == types.CoroutineCreateName {
			defs = append(defs, coroutineDynamicDef(name, ty, ck))
		} else if name == types.CoroutineWithName {
			defs = append(defs, coroutineWithDef(ty, ck))
		} else if name == types.CoroutineAdvanceName || name == types.CoroutineCloseName {
			defs = append(defs, coroutineAdvanceDef(name, ty, ck))
		} else if types.CompletionIntrinsic(name) {
			defs = append(defs, completionDef(name, ty, ck))
		} else if types.FailureInspection(name) {
			defs = append(defs, failureInspectDef(name, ty, ck))
		} else if name == types.FailAttemptReportName {
			defs = append(defs, attemptReportDef(name, ty, ck))
		}
		if len(defs) > 0 && defs[len(defs)-1].Name == name {
			defs[len(defs)-1].SourceType = ck.Intrinsics[name].Body
		}
	}
	bindRows(defs, ck)
	return defs
}

// scopeBracketDef builds the cleanup-scope intrinsic: three callback
// parameters, and a body that acquires, runs the use callback on the
// borrowed resource, and releases. The three applications are indirect calls
// through function values whose row is an abstract tail, so they carry no
// evidence arguments — each callback closed over its own evidence where it
// was written, which is why a finalizer runs with its lexical outer evidence
// rather than whatever handler stack was installed at the exit point.
func scopeBracketDef(name string, ty types.Type, ck *infer.Checker) core.Def {
	args, result := core.PeelFun(ty, 3)
	acquireTy := args[0].(*types.TFun)
	releaseTy := args[1].(*types.TFun)
	useTy := args[2].(*types.TFun)

	params := []string{"_acquire", "_release", "_use"}
	resource := "_resource"
	call := func(fn string, fnTy *types.TFun, arg core.Expr) core.Expr {
		return &core.App{CalleeKind: core.Value, Callee: &core.VarRef{Name: fn, Local: true, Ty: fnTy},
			Args: []core.Expr{arg}, Ty: fnTy.Ret, Control: types.FunctionControl(fnTy)}
	}
	borrowed := func() core.Expr { return &core.VarRef{Name: resource, Local: true, Ty: acquireTy.Ret} }

	acquire := call(params[0], acquireTy, &core.UnitLit{Ty: acquireTy.Arg})
	release := call(params[1], releaseTy, borrowed())
	body := call(params[2], useTy, borrowed())

	paramCaptures := make([]types.CaptureVar, len(params))
	for i := range paramCaptures {
		paramCaptures[i] = ck.Sup.FreshCapture()
	}
	return core.Def{
		Name:          name,
		Owner:         symbolOwner(name),
		Type:          ty,
		TyParams:      runtimeRigidVars(ty),
		Params:        params,
		ParamCaptures: paramCaptures,
		Control:       core.ArrowControl(ty, len(params)),
		Body: &core.Bracket{
			Scope:      ck.Sup.FreshScope(),
			Resource:   resource,
			ResourceTy: acquireTy.Ret,
			Acquire:    acquire,
			Release:    release,
			Body:       body,
			Ty:         result,
			Control:    types.JoinControl(core.ExprControl(acquire), core.ExprControl(release), core.ExprControl(body)),
		},
	}
}

func symbolOwner(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[:i]
	}
	return ""
}

// runtimeRigidVars includes value-type variables and variables appearing
// only in executing effect-label arguments, while excluding erased nominal
// row indexes and row-tail variables.
// The former still parameterize typed evidence even when an effect parameter
// is phantom in every operation signature.
func runtimeRigidVars(t types.Type) []*types.TVar {
	var out []*types.TVar
	seen := map[int]bool{}
	var walk func(types.Type)
	walk = func(t types.Type) {
		switch t := t.(type) {
		case *types.TVar:
			if t.Rigid && t.Kind != types.RowVar && !seen[t.ID] {
				seen[t.ID] = true
				out = append(out, t)
			}
		case *types.TCon:
			for _, a := range t.Args {
				if _, rowIndex := a.(types.Row); rowIndex {
					continue
				}
				walk(a)
			}
		case *types.TFun:
			walk(t.Arg)
			walk(t.Eff)
			walk(t.Ret)
		case types.Row:
			for _, l := range t.Labels {
				for _, a := range l.Args {
					walk(a)
				}
			}
		}
	}
	walk(t)
	return out
}

func executingEffects(t types.Type, arity int) []core.EffectInstance {
	if arity == 0 {
		return nil
	}
	var f *types.TFun
	for i := 0; i < arity; i++ {
		f = t.(*types.TFun)
		t = f.Ret
	}
	row := types.SortedRow(f.Eff)
	out := make([]core.EffectInstance, 0, len(row.Labels))
	seen := map[int]bool{}
	for _, l := range row.Labels {
		if types.RuntimeEvidenceEffect(l) && !seen[l.Unique] {
			control := types.Control{Polymorphic: true}
			if l.Abort {
				control = types.Control{Transport: types.Exit}
			}
			if l.Suspension || l.Name == types.ServiceInvocationName {
				control = types.Control{Transport: types.Machine}
			}
			out = append(out, core.EffectInstance{Unique: l.Unique, Name: l.Name, Args: append([]types.Type(nil), l.Args...), Control: control})
			seen[l.Unique] = true
		}
	}
	return out
}

func rowControl(row types.Row, ck *infer.Checker) types.Control {
	var out types.Control
	if row.Tail != nil {
		out.Polymorphic = true
	}
	for _, label := range row.Labels {
		abort := label.Abort
		if eff := ck.EffectsByUnique[label.Unique]; eff != nil && len(eff.Ops) > 0 {
			abort = eff.Ops[0].Abort
		}
		if label.Suspension || label.Name == types.ServiceInvocationName {
			out.Transport = types.Machine
		} else if abort {
			out.Transport = types.Exit
		} else if types.SurfaceName(label.Name) != "IO" {
			out.Polymorphic = true
		}
	}
	return out
}

// Expr elaborates one expression against the checker's solved types. The
// returned aux Defs are lambda-lifted polymorphic block bindings (REPL
// inputs can contain blocks); the caller must install them before
// evaluating the expression.
func Expr(e ast.Expr, ck *infer.Checker) (core.Expr, []core.Def, []diag.Error) {
	return ExprIn(e, nil, ck)
}

// ExprIn is Expr with the session's installed definitions as capture-analysis
// context, so a prompt expression's calls into installed runners are checked.
func ExprIn(e ast.Expr, context []core.Def, ck *infer.Checker) (core.Expr, []core.Def, []diag.Error) {
	if errs := ck.DefaultPreds(ck.PendingPreds, e.Span()); len(errs) > 0 {
		return nil, nil, errs
	}
	el := newElab(ck, "", types.Scheme{})
	el.owner = ck.CurrentOwner
	ce := el.anf(el.expr(e))
	if len(el.errs) == 0 {
		defs := append([]core.Def(nil), el.aux...)
		defs = append(defs, core.Def{Name: "_expression", Type: ce.Type(), Control: core.ExprControl(ce), Body: ce})
		bindRows(defs, ck)
		p := &core.Prog{ADTs: ck.ADTOrder, Effects: effectList(ck), Defs: defs, Natives: ck.Natives}
		el.errs = append(el.errs, captureDiagnostics(core.InferCapturesIn(p, context, ck.B), ck, e.Span())...)
		for _, err := range core.VerifyResumeStructure(ce) {
			el.errs = append(el.errs, diag.Errorf(e.Span(), "INTERNAL RESUME INVARIANT", "%v", err))
		}
		for i := range el.aux {
			for _, err := range core.VerifyResumeStructure(el.aux[i].Body) {
				el.errs = append(el.errs, diag.Errorf(e.Span(), "INTERNAL RESUME INVARIANT", "%v", err))
			}
		}
	}
	return ce, el.aux, el.errs
}

// effectList orders the checker's effects the way the whole-program path
// does. The checker holds them in a map, so without this the order of a
// program's effect declarations — and of the Go types emitted from them —
// would vary from run to run.
func effectList(ck *infer.Checker) []*types.EffectInfo {
	out := make([]*types.EffectInfo, 0, len(ck.EffectsByUnique))
	for _, eff := range ck.EffectsByUnique {
		out = append(out, eff)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Unique < out[j].Unique })
	return out
}

func installCaptureSummaries(defs []core.Def, ck *infer.Checker) {
	for i := range defs {
		d := &defs[i]
		vars := append([]types.CaptureVar(nil), d.ParamCaptures...)
		for _, ev := range d.EffectParams {
			vars = append(vars, ev.Captures.Vars...)
		}
		ck.SetCaptureSummary(d.Name, vars, d.ResultCaptures)
		summary := ck.CaptureSummaries[d.Name]
		summary.Contract = d.CaptureContract
		ck.CaptureSummaries[d.Name] = summary
		if sch, ok := ck.Env.Lookup(d.Name); ok {
			sch.CaptureContract = d.CaptureContract
			ck.Env.Bind(d.Name, sch)
		}
	}
}

func captureDiagnostics(errs []error, ck *infer.Checker, fallback source.Span) []diag.Error {
	// A capture summary has no source position of its own, so an escape is
	// reported at the definition whose body holds the offending call.
	at := func(name string) source.Span {
		for _, info := range ck.Checked {
			if info.Name == name {
				return info.NameSpan
			}
		}
		return fallback
	}
	out := make([]diag.Error, 0, len(errs))
	for _, err := range errs {
		var flow core.CaptureFlowError
		var access core.CursorAccessError
		var suspension core.SuspensionError
		var control core.OwnerControlError
		switch {
		case errors.As(err, &control):
			sp := control.Span
			if sp.File == nil {
				sp = at(control.In)
			}
			out = append(out, diag.Errorf(sp, "FOREIGN COROUTINE CONTROL", "%s", control.Detail()))
		case errors.As(err, &suspension):
			sp := suspension.Span
			if sp.File == nil {
				sp = at(suspension.In)
			}
			out = append(out, diag.Errorf(sp, "SUSPENDING RESOURCE CALLBACK", "%s", suspension.Detail()))
		case errors.As(err, &access):
			sp := access.Span
			if sp.File == nil {
				sp = at(access.In)
			}
			out = append(out, diag.Errorf(sp, "ITERATOR ADVANCEMENT CONFLICT", "%s", access.Detail()))
		case errors.As(err, &flow):
			title := "RESOURCE ESCAPES"
			if flow.State {
				title = "STATE RESULT ESCAPES"
			}
			sp := flow.Span
			if sp.File == nil {
				sp = ck.ScopeSpans[flow.Scope]
			}
			if sp.File == nil {
				sp = at(flow.In)
			}
			out = append(out, diag.Errorf(sp, title, "%s", flow.Detail()))
		default:
			out = append(out, diag.Errorf(fallback, "CAPTURE CHECK ERROR", "%v", err))
		}
	}
	return out
}

type elab struct {
	bodySubst     map[int]types.Type
	selfInstance  *infer.InstanceInfo
	instanceLimit int
	evidencePath  []types.Pred
	dicts         []dictionary
	owner         string
	ck            *infer.Checker
	errs          []diag.Error
	tmp           int // fresh-name counter for spine temporaries, per Decl/Expr

	// declName/declScheme identify the declaration being elaborated: its
	// self-references must instantiate against THIS scheme (the REPL
	// elaborates before binding, so the environment may hold a previous
	// generation).
	declName   string
	declScheme types.Scheme

	// scope tracks the AST-level locals in scope (params, block bindings,
	// pattern variables) with zonked types — the free-variable universe for
	// lambda-lifting (lift.go).
	scope    []scopeVar
	scopeIdx map[string]int

	// lifted maps a generalized block binding's source name to its lifted
	// top-level definition; aux accumulates those definitions.
	lifted map[string]*liftedLocal
	aux    []core.Def

	stableLifts bool
	liftSeq     int

	// evidence is a lexical stack per nominal effect. Concrete handler
	// activations carry a scope identity; function/lambda parameters carry a
	// capture variable. This metadata is erased by both runtime backends.
	evidence      map[int][]core.EffectInstance
	valueAdapters []valueAdapter
}

func newElab(ck *infer.Checker, declName string, declScheme types.Scheme) *elab {
	return &elab{ck: ck, declName: declName, declScheme: declScheme,
		instanceLimit: ck.StageInstanceLimit(),
		owner:         symbolOwner(declName),
		scopeIdx:      map[string]int{}, lifted: map[string]*liftedLocal{}, evidence: map[int][]core.EffectInstance{}}
}

func (el *elab) bindEffectParams(effects []core.EffectInstance) []core.EffectInstance {
	out := make([]core.EffectInstance, len(effects))
	for i, ev := range effects {
		ev.Captures = types.VarCapture(el.ck.Sup.FreshCapture())
		if eff := el.ck.EffectsByUnique[ev.Unique]; eff != nil && len(eff.Ops) > 0 && eff.Ops[0].Abort {
			ev.Control = types.Control{Transport: types.Exit}
		} else if ev.Control.Transport != types.Exit {
			ev.Control.Polymorphic = true
		}
		out[i] = ev
		el.evidence[ev.Unique] = append(el.evidence[ev.Unique], ev)
	}
	return out
}

func (el *elab) pushEvidence(effects []core.EffectInstance) {
	for _, ev := range effects {
		el.evidence[ev.Unique] = append(el.evidence[ev.Unique], ev)
	}
}

func (el *elab) popEvidence(effects []core.EffectInstance) {
	for i := len(effects) - 1; i >= 0; i-- {
		u := effects[i].Unique
		el.evidence[u] = el.evidence[u][:len(el.evidence[u])-1]
	}
}

func (el *elab) evidenceCaptures(unique int) types.CaptureSet {
	stack := el.evidence[unique]
	if len(stack) == 0 {
		return types.CaptureSet{}
	}
	return stack[len(stack)-1].Captures
}

func (el *elab) evidenceControl(unique int) types.Control {
	stack := el.evidence[unique]
	if len(stack) == 0 {
		return types.Control{}
	}
	return stack[len(stack)-1].Control
}

type scopeVar struct {
	name string
	ty   types.Type // zonked at binding time
}

type localPatternLet struct {
	pattern ast.Pattern
	rhs     core.Expr
	subject string
	ty      types.Type
}

func (el *elab) pushScope(name string, ty types.Type) {
	el.scopeIdx[name] = len(el.scope)
	el.scope = append(el.scope, scopeVar{name, ty})
}

func (el *elab) popScope(n int) {
	for i := len(el.scope) - n; i < len(el.scope); i++ {
		delete(el.scopeIdx, el.scope[i].name)
	}
	el.scope = el.scope[:len(el.scope)-n]
}

// lambda nests a multi-parameter surface lambda into single-param Core
// Lambdas, peeling one arrow per parameter off the (ground) function type.
func (el *elab) lambda(params []ast.Pattern, body ast.Expr, funTy types.Type) core.Expr {
	argTys, retTy := core.PeelFun(funTy, len(params))
	effectParams := el.bindEffectParams(executingEffects(funTy, len(params)))
	names := make([]string, len(params))
	var inner core.Expr
	if plainPatterns(params) {
		bound := 0
		for i, p := range params {
			names[i] = corePatternParam(p)
			if v, ok := p.(*ast.PVar); ok {
				el.pushScope(v.Name, argTys[i])
				bound++
			}
		}
		inner = el.expr(body)
		el.popScope(bound)
	} else {
		occs := make([]occurrence, len(params))
		for i := range params {
			names[i] = fmt.Sprintf("_arg%d", el.tmp)
			el.tmp++
			occs[i] = occurrence{name: names[i], ty: argTys[i]}
		}
		inner = el.matchPatternRows([][]ast.Pattern{params}, []ast.Expr{body}, []source.Span{params[0].Span()}, occs, params[0].Span(), "lambda")
	}
	inner = el.adaptFunctionValue(inner, retTy)
	el.popEvidence(effectParams)
	cur := funTy
	funs := make([]*types.TFun, len(params))
	for i := range params {
		funs[i] = cur.(*types.TFun)
		cur = funs[i].Ret
	}
	for i := len(params) - 1; i >= 0; i-- {
		lam := &core.Lambda{Param: names[i], Body: inner, Ty: funs[i], ParamCapture: el.ck.Sup.FreshCapture()}
		if i == len(params)-1 {
			lam.EffectParams = effectParams
		}
		inner = lam
	}
	return inner
}

func (el *elab) lambdaEquations(eqs []ast.Equation, funTy types.Type, at source.Span, context string) core.Expr {
	argTys, _ := core.PeelFun(funTy, len(eqs[0].Params))
	effectParams := el.bindEffectParams(executingEffects(funTy, len(eqs[0].Params)))
	names := make([]string, len(argTys))
	occs := make([]occurrence, len(argTys))
	for i, ty := range argTys {
		names[i] = fmt.Sprintf("_arg%d", el.tmp)
		el.tmp++
		occs[i] = occurrence{name: names[i], ty: ty}
	}
	patterns := make([][]ast.Pattern, len(eqs))
	bodies := make([]ast.Expr, len(eqs))
	spans := make([]source.Span, len(eqs))
	for i, eq := range eqs {
		patterns[i], bodies[i], spans[i] = eq.Params, eq.Body, eq.NameSpan
	}
	inner := el.matchPatternRows(patterns, bodies, spans, occs, at, context)
	el.popEvidence(effectParams)
	cur := funTy
	funs := make([]*types.TFun, len(names))
	for i := range names {
		funs[i] = cur.(*types.TFun)
		cur = funs[i].Ret
	}
	for i := len(names) - 1; i >= 0; i-- {
		lam := &core.Lambda{Param: names[i], Body: inner, Ty: funs[i], ParamCapture: el.ck.Sup.FreshCapture()}
		if i == len(names)-1 {
			lam.EffectParams = effectParams
		}
		inner = lam
	}
	return inner
}

// equationRows normalizes a definition's rows: an ungrouped definition is the
// one-row group its single parameter vector and body describe.
func equationRows(eqs []ast.Equation, params []ast.Pattern, body ast.Expr, at source.Span) []ast.Equation {
	if len(eqs) > 0 {
		return eqs
	}
	return []ast.Equation{{Params: params, Body: body, NameSpan: at}}
}

// workerBody lowers one equation group into a worker's parameter names and
// body. A lone identifier-only row keeps its source parameter names, so
// single-equation functions retain their current Core shape and the
// optimizations that read it; anything else binds deterministic hidden
// parameters and dispatches through one decision tree.
func (el *elab) workerBody(eqs []ast.Equation, argTys []types.Type, at source.Span, context string) ([]string, core.Expr) {
	params := make([]string, len(argTys))
	if len(eqs) == 1 && plainPatterns(eqs[0].Params) {
		bound := 0
		for i, p := range eqs[0].Params {
			params[i] = corePatternParam(p)
			if v, ok := p.(*ast.PVar); ok {
				el.pushScope(v.Name, argTys[i])
				bound++
			}
		}
		body := el.expr(eqs[0].Body)
		el.popScope(bound)
		return params, body
	}
	occs := make([]occurrence, len(params))
	for i := range params {
		params[i] = fmt.Sprintf("_arg%d", el.tmp)
		el.tmp++
		occs[i] = occurrence{name: params[i], ty: argTys[i]}
	}
	patterns := make([][]ast.Pattern, len(eqs))
	bodies := make([]ast.Expr, len(eqs))
	spans := make([]source.Span, len(eqs))
	for i, eq := range eqs {
		patterns[i], bodies[i], spans[i] = eq.Params, eq.Body, eq.NameSpan
	}
	return params, el.matchPatternRows(patterns, bodies, spans, occs, at, context)
}

func plainPatterns(ps []ast.Pattern) bool {
	for _, p := range ps {
		switch p.(type) {
		case *ast.PVar, *ast.PWildcard, *ast.PUnit:
		default:
			return false
		}
	}
	return true
}

func corePatternParam(p ast.Pattern) string {
	if v, ok := p.(*ast.PVar); ok {
		return v.Name
	}
	return "_"
}

func handlerPatternParam(p ast.Pattern) string {
	if _, ok := p.(*ast.PUnit); ok {
		return "()"
	}
	return corePatternParam(p)
}

type patternName struct {
	name    string
	pattern ast.Pattern
}

func inferPatternNames(p ast.Pattern) []patternName {
	var out []patternName
	var walk func(ast.Pattern)
	walk = func(p ast.Pattern) {
		switch p := p.(type) {
		case *ast.PVar:
			out = append(out, patternName{name: p.Name, pattern: p})
		case *ast.PCtor:
			for _, a := range p.Args {
				walk(a)
			}
		case *ast.PRecord:
			for _, f := range p.Fields {
				walk(f.Pattern)
			}
		}
	}
	walk(p)
	return out
}

func (el *elab) expr(e ast.Expr) (out core.Expr) {
	defer func() {
		if e == nil {
			return
		}
		switch x := out.(type) {
		case *core.App:
			x.Origin = e.Span()
		case *core.VarRef:
			x.Origin = e.Span()
		case *core.Perform:
			x.Origin = e.Span()
		case *core.ControlExit:
			x.Origin = e.Span()
		}
	}()
	if desugared := el.ck.Desugared[e]; desugared != nil {
		return el.expr(desugared)
	}
	ty := el.zonkDefault(el.ck.ExprTypes[e])
	switch e := e.(type) {
	case *ast.IntLit:
		// An integer literal whose solved type is Float (`1 + 0.5`) is a
		// Float literal — Elm's number rule made concrete.
		if el.unique(ty) == el.ck.B.Float.Unique {
			return &core.FloatLit{Val: float64(e.Value), Ty: ty}
		}
		return &core.IntLit{Val: e.Value, Ty: ty}
	case *ast.FloatLit:
		return &core.FloatLit{Val: e.Value, Ty: ty}
	case *ast.StringLit:
		return &core.StringLit{Val: e.Value, Ty: ty}
	case *ast.CharLit:
		return &core.CharLit{Val: e.Value, Ty: ty}
	case *ast.UnitLit:
		return &core.UnitLit{Ty: ty}
	case *ast.Var:
		if index, local := el.scopeIdx[e.Name]; local {
			return &core.VarRef{Name: e.Name, Ty: el.scope[index].ty, Local: true}
		}
		if method := el.ck.Methods[e.Name]; method != nil {
			return el.methodValue(method, el.ck.ExprTypes[e])
		}
		if op := el.ck.Operations[e.Name]; op != nil {

			return el.operationValue(op, ty, el.apply(el.ck.ExprTypes[e]))
		}
		if n := el.ck.Natives[e.Name]; n != nil {
			return el.nativeValue(n, ty, el.apply(el.ck.ExprTypes[e]))
		}
		// A lifted local in first-class position gets the same curried-
		// wrapper treatment as a worker (its frees are the leading args).
		if lf := el.lifted[e.Name]; lf != nil {
			return el.partial(el.liftedCallee(lf, ty, el.apply(el.ck.ExprTypes[e])), nil)
		}
		// A worker name in first-class position (not an application head —
		// spine.go intercepts those) eta-expands into its curried wrapper.
		if arity, isWorker := el.ck.Workers[e.Name]; isWorker {
			return el.curriedWorkerRef(e.Name, ty, el.apply(el.ck.ExprTypes[e]), arity)
		}
		// A polymorphic top-level value compiled to a nullary generic worker
		// (doc/design.md, "Go backend and runtime"): every use is an instantiated zero-argument call.
		if sch, ok := el.ck.Env.Lookup(e.Name); ok && hasRuntimeVars(sch) {
			return el.nullaryValueUse(e.Name, sch, ty, el.apply(el.ck.ExprTypes[e]))
		}
		return &core.VarRef{Name: e.Name, Ty: ty}
	case *ast.Ctor:
		switch e.Name {
		case "True":
			return &core.BoolLit{Val: true, Ty: ty}
		case "False":
			return &core.BoolLit{Val: false, Ty: ty}
		default:
			info, ok := el.ck.Ctors[e.Name]
			if !ok {
				panic("elaborate: unknown constructor `" + e.Name + "` — the checker should have rejected this")
			}
			return el.ctorValue(info, ty)
		}
	case *ast.Quote:
		holes := make([]core.Expr, 0, len(el.ck.QuoteHoles[e]))
		for _, hole := range el.ck.QuoteHoles[e] {
			holes = append(holes, el.expr(hole.Operand))
		}
		// A quote with no template was never staged, which happens only after
		// staging already reported an error. Index -1 keeps it from silently
		// expanding as some other quote's template.
		template, staged := el.ck.QuoteTemplates[e]
		if !staged {
			template = -1
		}
		return &core.Quote{Template: template, Holes: holes, Ty: ty}
	case *ast.TypeOf:
		return &core.TypeOf{Repr: el.ck.Reflect(el.ck.Sub.Apply(e.Value), e.Visible), Ty: ty}
	case *ast.MetaValue:
		return &core.TypeOf{Repr: e.Value, Ty: ty}
	case *ast.RecordLit:
		return el.recordLiteral(e, ty)
	case *ast.RecordGet:
		return el.recordGet(e, ty)
	case *ast.RecordUpdate:
		return el.recordUpdate(e, ty)
	case *ast.App:
		return el.app(e)
	case *ast.Neg:
		return el.fold(&core.Neg{Operand: el.expr(e.Operand), Ty: ty})
	case *ast.If:
		return &core.If{
			Cond: el.expr(e.Cond),
			Then: el.adaptFunctionValue(el.expr(e.Then), ty),
			Else: el.adaptFunctionValue(el.expr(e.Else), ty),
			Ty:   ty,
		}
	case *ast.BinOp:
		// `&&` and `||` are the only operators that reach here: they become
		// an `if`, so the right operand — and its effects — only run when
		// the left operand does not decide the result. Core has no boolean
		// operator, and no operator node at all.
		//
		// Every other operator is an ordinary value, so inference desugared
		// it to an application and the Desugared lookup above already took
		// that branch.
		cond := el.expr(e.L)
		if e.Op == "&&" {
			return &core.If{Cond: cond, Then: el.expr(e.R), Else: &core.BoolLit{Val: false, Ty: ty}, Ty: ty}
		}
		return &core.If{Cond: cond, Then: &core.BoolLit{Val: true, Ty: ty}, Else: el.expr(e.R), Ty: ty}
	case *ast.Lambda:
		el.defaultFree(el.apply(el.ck.ExprTypes[e]))
		raw := el.apply(el.ck.ExprTypes[e])
		return sourceLambdas(el.lambda(e.Params, e.Body, el.eraseRuntimeKinds(eraseRows(raw))), raw, len(e.Params))
	case *ast.Case:
		return el.caseExpr(e, ty)
	case *ast.Handle:
		return el.handleExpr(e, ty)
	case *ast.Resume:
		panic("elaborate: bare resume")
	case *ast.Block:
		// Fold bindings into a right-nested Let chain; every level carries
		// the block's (result) type. RHSs elaborate in source order so
		// defaulting is deterministic. Local functions become (possibly
		// recursive) Lets of nested Lambdas. Generalized bindings do not
		// become Lets at all: they lambda-lift to top-level generic
		// definitions (doc/design.md, "Go backend and runtime", lift.go) and their uses rewrite to calls.
		var order []any
		pushed := 0
		var liftedHere []string
		items := e.Items
		if len(items) == 0 {
			for i := range e.Binds {
				items = append(items, ast.BlockItem{BindIndex: i})
			}
		}
		for _, item := range items {
			if item.Expr != nil {
				order = append(order, el.expr(item.Expr))
				continue
			}
			bind := &e.Binds[item.BindIndex]
			if bind.Pattern != nil {
				rhs := el.expr(bind.Body)
				subject := fmt.Sprintf("_bind%d", el.tmp)
				el.tmp++
				pushed += el.pushPatternVars(bind.Pattern, rhs.Type())
				order = append(order, localPatternLet{pattern: bind.Pattern, rhs: rhs, subject: subject, ty: rhs.Type()})
				continue
			}
			bindTy := el.ck.BindTypes[bind]
			if sch := el.ck.BindSchemes[bind]; hasRuntimeVars(sch) {
				el.liftBinding(bind, sch)
				liftedHere = append(liftedHere, bind.Name)
				continue
			}
			zonked := el.zonkDefault(bindTy)
			isFn := len(bind.Params) > 0
			if isFn {
				// In scope inside its own body (recursion) — and inside any
				// lift the body contains.
				el.pushScope(bind.Name, zonked)
				pushed++
			}
			var rhs core.Expr
			if len(bind.Params) > 0 {
				if len(bind.Equations) > 0 {
					rhs = el.lambdaEquations(bind.Equations, zonked, bind.NameSpan, "local function")
				} else {
					rhs = el.lambda(bind.Params, bind.Body, zonked)
				}
			} else {
				rhs = el.expr(bind.Body)
			}
			if len(bind.Params) > 0 {
				rhs = sourceLambdas(rhs, el.apply(bindTy), len(bind.Params))
			}
			if !isFn {
				el.pushScope(bind.Name, rhs.Type())
				pushed++
			}
			let := &core.Let{
				Name: bind.Name,
				Rhs:  rhs,
				Rec:  isFn && core.Mentions(rhs, bind.Name),
			}
			order = append(order, let)
		}
		body := el.expr(e.Result)
		el.popScope(pushed)
		for _, name := range liftedHere {
			delete(el.lifted, name)
		}
		for i := len(order) - 1; i >= 0; i-- {
			switch x := order[i].(type) {
			case *core.Let:
				x.Body = body
				x.Ty = body.Type()
				body = x
			case core.Expr:
				body = &core.Seq{First: x, Then: body, Ty: body.Type()}
			case localPatternLet:
				body = el.bindPatternCore(x.pattern, x.rhs, x.subject, x.ty, body)
			}
		}
		return body
	default:
		panic(fmt.Sprintf("elaborate: unhandled AST node %T", e))
	}
}

func (el *elab) recordCtorApp(adt *types.ADTInfo, args []core.Expr, result types.Type) core.Expr {
	ctor := adt.Ctors[0]
	con := result.(*types.TCon)
	fieldTypes := adt.InstFields(ctor, con.Args)
	var calleeTy types.Type = result
	for i := len(fieldTypes) - 1; i >= 0; i-- {
		calleeTy = &types.TFun{Arg: fieldTypes[i], Ret: calleeTy}
	}
	return &core.App{CalleeKind: core.Ctor, Callee: &core.VarRef{Name: ctor.Name, Ty: calleeTy}, Args: args, TyArgs: append([]types.Type(nil), con.Args...), Ty: result, Ctor: ctor}
}

func (el *elab) recordLiteral(e *ast.RecordLit, ty types.Type) core.Expr {
	adt := el.ck.RecordUses[e]
	values := map[string]core.Expr{}
	var binds []struct {
		name  string
		value core.Expr
	}
	for _, f := range e.Fields {
		name := fmt.Sprintf("_record%d", el.tmp)
		el.tmp++
		value := el.expr(f.Value)
		values[f.Name] = &core.VarRef{Name: name, Ty: value.Type(), Local: true}
		binds = append(binds, struct {
			name  string
			value core.Expr
		}{name, value})
	}
	args := make([]core.Expr, len(adt.RecordFields))
	fieldTypes := adt.InstFields(adt.Ctors[0], ty.(*types.TCon).Args)
	for i, f := range adt.RecordFields {
		args[i] = el.adaptFunctionValue(values[f.Name], el.eraseRuntimeKinds(eraseRows(fieldTypes[i])))
	}
	body := el.recordCtorApp(adt, args, ty)
	for i := len(binds) - 1; i >= 0; i-- {
		body = &core.Let{Name: binds[i].name, Rhs: binds[i].value, Body: body, Ty: ty}
	}
	return body
}

func (el *elab) recordGet(e *ast.RecordGet, ty types.Type) core.Expr {
	adt := el.ck.RecordUses[e]
	bind := fmt.Sprintf("_record%d", el.tmp)
	el.tmp++
	binds := make([]string, len(adt.RecordFields))
	idx, _ := adt.RecordField(e.Field)
	field := fmt.Sprintf("_field%d", el.tmp)
	el.tmp++
	binds[idx] = field
	record := el.expr(e.Record)
	// The binder holds what the constructor stores, so an effect-indexed
	// field projected at a wider row is adapted here rather than retagged,
	// the same way a function value flowing into a wider position is.
	bound := ty
	if con, ok := record.Type().(*types.TCon); ok && len(con.Args) == len(adt.Params) {
		bound = el.eraseRuntimeKinds(eraseRows(adt.InstFields(adt.Ctors[0], con.Args)[idx]))
	}
	leaf := el.adaptFunctionValue(&core.VarRef{Name: field, Ty: bound, Local: true}, ty)
	return &core.Case{Scrut: record, Bind: bind, Ty: ty, Tree: &core.SwitchCtor{Scrut: bind, ADT: adt, Cases: []core.CtorCase{{Ctor: adt.Ctors[0], Binds: binds, Tree: &core.Leaf{Body: leaf}}}}}
}

func (el *elab) recordUpdate(e *ast.RecordUpdate, ty types.Type) core.Expr {
	adt := el.ck.RecordUses[e]
	bind := fmt.Sprintf("_record%d", el.tmp)
	el.tmp++
	old := make([]string, len(adt.RecordFields))
	args := make([]core.Expr, len(adt.RecordFields))
	con := ty.(*types.TCon)
	fieldTypes := adt.InstFields(adt.Ctors[0], con.Args)
	for i := range old {
		old[i] = fmt.Sprintf("_field%d", el.tmp)
		el.tmp++
		args[i] = &core.VarRef{Name: old[i], Ty: fieldTypes[i], Local: true}
	}
	var lets []struct {
		name  string
		value core.Expr
	}
	for _, f := range e.Fields {
		name := fmt.Sprintf("_record%d", el.tmp)
		el.tmp++
		value := el.expr(f.Value)
		idx, _ := adt.RecordField(f.Name)
		value = el.adaptFunctionValue(value, el.eraseRuntimeKinds(eraseRows(fieldTypes[idx])))
		old[idx] = ""
		args[idx] = &core.VarRef{Name: name, Ty: value.Type(), Local: true}
		lets = append(lets, struct {
			name  string
			value core.Expr
		}{name, value})
	}
	leaf := el.recordCtorApp(adt, args, ty)
	for i := len(lets) - 1; i >= 0; i-- {
		leaf = &core.Let{Name: lets[i].name, Rhs: lets[i].value, Body: leaf, Ty: ty}
	}
	return &core.Case{Scrut: el.expr(e.Record), Bind: bind, Ty: ty, Tree: &core.SwitchCtor{Scrut: bind, ADT: adt, Cases: []core.CtorCase{{Ctor: adt.Ctors[0], Binds: old, Tree: &core.Leaf{Body: leaf}}}}}
}

func (el *elab) handleExpr(e *ast.Handle, ty types.Type) core.Expr {
	info := el.ck.HandleInfos[e]
	if info == nil {
		panic("elaborate: missing handler info")
	}
	clauses := make([]core.HandlerClause, len(e.Clauses))
	for i := range e.Clauses {
		cl, ci := &e.Clauses[i], info.Clauses[i]
		params := make([]string, len(cl.Params))
		pts := make([]types.Type, len(ci.ParamTypes))
		for j, p := range ci.ParamTypes {
			pts[j] = el.zonkDefault(p)
		}
		var clauseBody core.Expr
		var invocation *core.EffectInstance
		implicit := ""
		var implicitType types.Type
		if ci.Invocation != nil {
			label := el.apply(types.Row{Labels: []types.EffLabel{*ci.Invocation}}).(types.Row).Labels[0]
			scope := ci.InvocationScope
			ev := core.EffectInstance{Unique: label.Unique, Name: label.Name, Args: label.Args, Captures: types.ScopeCapture(scope), Control: types.Control{Transport: types.Machine}}
			invocation = &ev
			implicit = fmt.Sprintf("_invocation%d", el.tmp)
			el.tmp++
			implicitType = types.InvocationCallback(label)
			el.pushEvidence([]core.EffectInstance{ev})
		}
		pushed := 0
		if e.State != nil {
			el.pushScope(e.State.Name, el.zonkDefault(info.StateType))
			pushed++
		}
		if len(cl.Equations) == 0 && plainPatterns(cl.Params) {
			for j, p := range cl.Params {
				params[j] = handlerPatternParam(p)
				if v, ok := p.(*ast.PVar); ok {
					el.pushScope(v.Name, pts[j])
					pushed++
				}
			}
			clauseBody = el.expr(cl.Body)
		} else {
			occs := make([]occurrence, len(params))
			for j := range params {
				params[j] = fmt.Sprintf("_arg%d", el.tmp)
				el.tmp++
				occs[j] = occurrence{name: params[j], ty: pts[j]}
			}
			eqs := cl.Equations
			if len(eqs) == 0 {
				eqs = []ast.Equation{{Params: cl.Params, Body: cl.Body, NameSpan: cl.OpSpan}}
			}
			patterns := make([][]ast.Pattern, len(eqs))
			bodies := make([]ast.Expr, len(eqs))
			spans := make([]source.Span, len(eqs))
			for j, eq := range eqs {
				patterns[j], bodies[j], spans[j] = eq.Params, eq.Body, eq.NameSpan
			}
			clauseBody = el.matchPatternRows(patterns, bodies, spans, occs, cl.OpSpan, "handler clause")
		}
		el.popScope(pushed)
		if invocation != nil {
			el.popEvidence([]core.EffectInstance{*invocation})
			params = append(params, implicit)
			pts = append(pts, implicitType)
			answer := clauseBody.Type()
			valueType := el.zonkDefault(ci.OpResult)
			value := serviceResumeValue(clauseBody, ci.ResumeID, valueType)
			value = invocationHandler(value, &core.VarRef{Name: implicit, Local: true, Ty: implicitType}, *invocation, el.ck)
			name := implicit + "_result"
			clauseBody = &core.Let{Name: name, Rhs: value, Body: &core.ResumeTail{Owner: ci.ResumeID, Value: &core.VarRef{Name: name, Local: true, Ty: valueType}, ClauseResult: answer}, Ty: answer}
		}
		clauses[i] = core.HandlerClause{Op: ci.Op, ResumeID: ci.ResumeID, Params: params, ParamTypes: pts,
			ResultType: el.zonkDefault(ci.OpResult), Body: clauseBody}
	}
	var ret *core.ReturnClause
	if e.Return != nil {
		bodyTy := el.zonkDefault(info.BodyResult)
		name := corePatternParam(e.Return.Param)
		var retBody core.Expr
		pushed := 0
		if e.State != nil {
			el.pushScope(e.State.Name, el.zonkDefault(info.StateType))
			pushed++
		}
		if v, ok := e.Return.Param.(*ast.PVar); ok && len(e.Return.Equations) == 0 {
			el.pushScope(v.Name, bodyTy)
			retBody = el.expr(e.Return.Body)
			pushed++
		} else if plainPatterns([]ast.Pattern{e.Return.Param}) && len(e.Return.Equations) == 0 {
			retBody = el.expr(e.Return.Body)
		} else {
			name = fmt.Sprintf("_arg%d", el.tmp)
			el.tmp++
			eqs := e.Return.Equations
			if len(eqs) == 0 {
				eqs = []ast.Equation{{Params: []ast.Pattern{e.Return.Param}, Body: e.Return.Body, NameSpan: e.Return.Sp}}
			}
			patterns := make([][]ast.Pattern, len(eqs))
			bodies := make([]ast.Expr, len(eqs))
			spans := make([]source.Span, len(eqs))
			for j, eq := range eqs {
				patterns[j], bodies[j], spans[j] = eq.Params, eq.Body, eq.NameSpan
			}
			retBody = el.matchPatternRows(patterns, bodies, spans, []occurrence{{name: name, ty: bodyTy}}, e.Return.Sp, "handler return clause")
		}
		el.popScope(pushed)
		ret = &core.ReturnClause{Param: name, Body: retBody}
	}
	// An installed activation's transport is its own clauses', not the
	// enclosing context's. The interpretation is known here, so a handler
	// whose clauses neither exit nor suspend stays Direct inside an Exit or
	// Machine worker, and a perform widens to the caller's protocol the way
	// Direct evidence passed to a wider callee already does. A closure bound
	// to the activation depends on this: its own row fixes its transport, and
	// the record it captures has to exist there.
	var control types.Control
	for _, clause := range clauses {
		control = types.JoinControl(control, core.ExprControl(clause.Body))
	}
	if info.Effect.Abort {
		control = types.Control{Transport: types.Exit}
	}
	inst := core.EffectInstance{Unique: info.Effect.Unique, Name: info.Effect.Name, Captures: types.ScopeCapture(info.Scope), Control: control}
	for _, a := range info.Effect.Args {
		inst.Args = append(inst.Args, el.zonkDefault(a))
	}
	el.pushEvidence([]core.EffectInstance{inst})
	body := el.expr(e.Body)
	el.popEvidence([]core.EffectInstance{inst})
	var state *core.HandlerState
	if e.State != nil {
		state = &core.HandlerState{Name: e.State.Name, Initial: el.expr(e.State.Initial), Ty: el.zonkDefault(info.StateType)}
	}
	residualType := el.apply(info.Residual)
	el.defaultFree(residualType)
	residual := el.ck.Sub.Apply(residualType).(types.Row)
	resultControl := rowControl(residual, el.ck)
	for _, clause := range clauses {
		resultControl = types.JoinControl(resultControl, core.ExprControl(clause.Body))
	}
	if ret != nil {
		resultControl = types.JoinControl(resultControl, core.ExprControl(ret.Body))
	}
	return &core.Handle{Body: body, State: state, Effect: inst, Scope: info.Scope, Scoped: info.Scoped, Clauses: clauses, Return: ret, Ty: ty, Control: resultControl}
}

func hasRuntimeVars(s types.Scheme) bool {
	for _, v := range s.Vars {
		if v.Kind != types.RowVar {
			return true
		}
	}
	return false
}

func (el *elab) unique(t types.Type) int {
	if con, ok := t.(*types.TCon); ok && len(con.Args) == 0 {
		return con.Unique
	}
	return -1
}

// fold constant-folds arithmetic over literal operands with the exact
// operations eval uses (wrapping int64, IEEE float64) — see the package
// comment for why this is load-bearing.
func (el *elab) fold(e core.Expr) core.Expr {
	switch e := e.(type) {
	case *core.Neg:
		switch op := e.Operand.(type) {
		case *core.IntLit:
			return &core.IntLit{Val: -op.Val, Ty: e.Ty}
		case *core.FloatLit:
			return &core.FloatLit{Val: -op.Val, Ty: e.Ty}
		}
		return e
	case *core.NativeCall:
		spec, ok := natives.Lookup(e.Name)
		if !ok || !spec.Foldable || len(e.Args) != spec.Arity {
			return e
		}
		literal := func(x core.Expr) (any, bool) {
			switch x := x.(type) {
			case *core.IntLit:
				return x.Val, true
			case *core.FloatLit:
				return x.Val, true
			default:
				return nil, false
			}
		}
		args := make([]any, len(e.Args))
		for i, a := range e.Args {
			var ok bool
			args[i], ok = literal(a)
			if !ok {
				return e
			}
		}
		v, err := spec.Eval(&natives.Runtime{}, args)
		if err != nil {
			return e
		}
		switch v := v.(type) {
		case int64:
			return &core.IntLit{Val: v, Ty: e.Ty}
		case float64:
			return &core.FloatLit{Val: v, Ty: e.Ty}
		}
		return e
	default:
		return e
	}
}

// zonkDefault applies the substitution, then defaults any metavariable
// still free: Number-kinded → Int, general → Unit, row tails → empty
// (see doc/design.md, "Type inference", "Go backend and runtime", and
// "Core and evidence invariants"). Open row tails are erased here; concrete
// labels remain on every arrow because indirect calls use them as their
// evidence ABI.
// Defaults are recorded in the checker's substitution so every other
// occurrence of the same variable — including environment schemes held by
// a live REPL session — resolves identically.
func (el *elab) zonkDefault(t types.Type) types.Type {
	origin := t
	t = el.apply(t)
	el.defaultFree(t)
	return el.eraseRuntimeKinds(eraseRowsFrom(origin, el.apply(t)))
}

// eraseRuntimeKinds replaces row-kinded ADT arguments with Unit. Row
// parameters remain present in source inference and nominal identity, but
// they carry no runtime value; using a stable phantom argument keeps Core's
// existing source-arity checks while preventing RowVar values from reaching
// Core or Go.
func (el *elab) eraseRuntimeKinds(t types.Type) types.Type {
	switch t := t.(type) {
	case *types.TVar:
		if t.Kind == types.RowVar {
			return el.ck.B.Unit
		}
		return t
	case *types.TCon:
		args := make([]types.Type, len(t.Args))
		adt := el.ck.ADTs[t.Unique]
		for i, a := range t.Args {
			if adt != nil && i < len(adt.Params) && adt.Params[i].Kind == types.RowVar {
				args[i] = el.ck.B.Unit
			} else {
				args[i] = el.eraseRuntimeKinds(a)
			}
		}
		return &types.TCon{Unique: t.Unique, Name: t.Name, Args: args}
	case *types.TFun:
		return &types.TFun{Arg: el.eraseRuntimeKinds(t.Arg), Eff: el.eraseRuntimeKinds(t.Eff).(types.Row), Ret: el.eraseRuntimeKinds(t.Ret), Control: t.Control, OpenRow: types.FunctionOpenRow(t)}
	case types.Row:
		labels := make([]types.EffLabel, len(t.Labels))
		for i, l := range t.Labels {
			args := make([]types.Type, len(l.Args))
			for j, a := range l.Args {
				args[j] = el.eraseRuntimeKinds(a)
			}
			labels[i] = types.EffLabel{Unique: l.Unique, Name: l.Name, Args: args, Abort: l.Abort, Suspension: l.Suspension}
		}
		return types.Row{Labels: labels}
	default:
		return t
	}
}

func eraseRows(t types.Type) types.Type {
	return eraseRowsFrom(t, t)
}

// eraseRowsFrom retains only labels written into the arrow before solving.
// Labels absorbed later through an open row tail describe an allowed caller
// context, not effects the function closure itself must receive as evidence.
func eraseRowsFrom(origin, t types.Type) types.Type {
	switch t := t.(type) {
	case *types.TVar:
		return t
	case *types.TCon:
		if len(t.Args) == 0 {
			return t
		}
		args := make([]types.Type, len(t.Args))
		for i, a := range t.Args {
			var oa types.Type = a
			if oc, ok := origin.(*types.TCon); ok && i < len(oc.Args) {
				oa = oc.Args[i]
			}
			args[i] = eraseRowsFrom(oa, a)
		}
		return &types.TCon{Unique: t.Unique, Name: t.Name, Args: args}
	case *types.TFun:
		of, ok := origin.(*types.TFun)
		if !ok {
			of = t
		}
		arg, ret := eraseRowsFrom(of.Arg, t.Arg), eraseRowsFrom(of.Ret, t.Ret)
		var eff types.Row
		for _, l := range types.SortedRow(t.Eff).Labels {
			keep := false
			for _, ol := range of.Eff.Labels {
				if ol.Unique == l.Unique {
					keep = true
					break
				}
			}
			if !keep {
				continue
			}
			args := make([]types.Type, len(l.Args))
			for i, a := range l.Args {
				args[i] = eraseRowsFrom(a, a)
			}
			eff.Labels = append(eff.Labels, types.EffLabel{Unique: l.Unique, Name: l.Name, Args: args, Abort: l.Abort, Suspension: l.Suspension})
		}
		control := types.FunctionControl(t)
		return &types.TFun{Arg: arg, Eff: eff, Ret: ret, Control: control, OpenRow: types.FunctionOpenRow(of)}
	case types.Row:
		return types.Row{}
	default:
		return t
	}
}

func (el *elab) defaultFree(t types.Type) {
	switch t := t.(type) {
	case *types.TVar:
		if t.Rigid {
			// Scheme-bound: the definition's own type parameter, not a
			// residual meta. Defaulting it would poison the substitution.
			return
		}
		switch t.Kind {
		case types.General:
			el.ck.Sub[t.ID] = el.ck.B.Unit
		case types.RowVar:
			el.ck.Sub[t.ID] = types.Row{}
		}
	case *types.TCon:
		for _, a := range t.Args {
			el.defaultFree(a)
		}
	case *types.TFun:
		el.defaultFree(t.Arg)
		el.defaultFree(t.Eff)
		el.defaultFree(t.Ret)
	case types.Row:
		for _, l := range t.Labels {
			for _, a := range l.Args {
				el.defaultFree(a)
			}
		}
		if t.Tail != nil {
			el.defaultFree(t.Tail)
		}
	}
}

// apply zonks a local occurrence and instantiates any recursive-component
// variables that this worker does not bind. Global schemes remain untouched.
func (el *elab) apply(t types.Type) types.Type {
	t = el.ck.Sub.Apply(t)
	if len(el.bodySubst) == 0 {
		return t
	}
	return types.SubstRigid(t, el.bodySubst)
}
