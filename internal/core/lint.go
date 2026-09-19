package core

import (
	"fmt"

	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/types"
)

// Lint asserts the Core invariants: after elaboration there are no
// metavariables anywhere (rigid variables are legal only when declared by
// the enclosing definition's TyParams), every operator has the types its Go
// emission requires, every application is consistent with its callee's
// instantiated type, and every effect row is empty except the concrete row
// retained on function arrows as their call-time evidence ABI. It runs in every test
// (and under a debug flag later) — instantiation plumbing bugs are the
// design's top risk, and this is the tripwire.
func Lint(p *Prog, b *types.Builtins) []error {
	return lint(p, nil, b, false, false)
}

// LintIn validates only p's owned definitions while using context as an
// already-validated signature and capture-contract environment. Dependency
// bodies are neither traversed nor revalidated.
func LintIn(p *Prog, context []Def, b *types.Builtins) []error {
	return lint(p, context, b, false, false)
}

// LintMachineInput checks semantic Core immediately before selective machine
// lowering. It admits compiler-only Suspend nodes and Machine transport while
// retaining every ordinary Core invariant. Ordinary Lint admits the same
// private nodes only when the resolved Stream.withProducer intrinsic is
// present; that declaration is the source activation boundary.
func LintMachineInput(p *Prog, b *types.Builtins) []error {
	return lint(p, nil, b, true, false)
}

// LintStageMachineInput applies the same ownership, capture, and transport
// proofs to a splice's execution program. Quote and TypeOf are values at this
// boundary only; the ordinary emission boundary continues to reject them.
func LintStageMachineInput(p *Prog, b *types.Builtins) []error {
	return lint(p, nil, b, true, true)
}

func lint(p *Prog, context []Def, b *types.Builtins, allowMachine, allowStage bool) []error {
	l := &linter{b: b, scope: map[string]bool{}, localTypes: map[string]types.Type{}, workers: map[string]*Def{},
		adts: map[int]*types.ADTInfo{}, effects: map[int]*types.EffectInfo{},
		tyParams: map[int]bool{}, evidence: map[int]int{}, evidenceCaptures: map[int][]types.CaptureSet{},
		captureVars: map[types.CaptureVar]bool{}, scopeIDs: map[types.ScopeID]bool{}, activeScopes: map[types.ScopeID]bool{},
		resumeIDs: map[types.ResumeID]bool{}, natives: p.Natives, intrinsics: p.Intrinsics,
		allowMachine: allowMachine || (p.Intrinsics[types.StreamWithProducerName] || p.Intrinsics[types.IteratorNextName]), allowStage: allowStage}
	for _, adt := range p.ADTs {
		l.adts[adt.Con.Unique] = adt
	}
	for _, eff := range p.Effects {
		if old := l.effects[eff.Unique]; old != nil {
			l.errorf("effect unique %d is shared by `%s` and `%s`", eff.Unique, old.Name, eff.Name)
		}
		l.effects[eff.Unique] = eff
	}
	// The worker table is complete up front (self-calls need it); the
	// no-shadow scope fills in SOURCE ORDER, matching the checker — a
	// param may legally coincide with a later definition's name.
	for i := range p.Defs {
		d := &p.Defs[i]
		if d.IsWorker() {
			l.workers[d.Name] = d
		}
	}
	for i := range context {
		d := &context[i]
		if d.IsWorker() {
			if _, owned := l.workers[d.Name]; !owned {
				l.workers[d.Name] = d
			}
		}
	}
	for i := range p.Defs {
		d := &p.Defs[i]
		where := "def " + d.Name
		l.defName = d.Name
		l.scope[d.Name] = true
		l.tyParams = map[int]bool{}
		for _, v := range d.TyParams {
			if !v.Rigid {
				l.errorf("%s: TyParams contains a non-rigid variable", where)
			}
			if l.tyParams[v.ID] {
				l.errorf("%s: TyParams contains duplicate variable %d", where, v.ID)
			}
			l.tyParams[v.ID] = true
		}
		l.typ(d.Type, where)
		l.control(d.Control, where)
		if d.IsWorker() {
			wantControl := ArrowControl(d.Type, len(d.Params))
			if d.Control != wantControl {
				l.errorf("%s: declared control %s disagrees with final arrow %s", where, ControlName(d.Control), ControlName(wantControl))
			}
		}
		if len(d.ParamCaptures) != len(d.Params) {
			l.errorf("%s: has %d parameter capture binders, want %d", where, len(d.ParamCaptures), len(d.Params))
		}
		for _, v := range d.ParamCaptures {
			if v == 0 || l.captureVars[v] {
				l.errorf("%s: invalid or duplicate capture variable %d", where, v)
			}
			l.captureVars[v] = true
		}
		lastEffect := -1
		for _, ev := range d.EffectParams {
			l.effectInstance(ev, where)
			if ev.Unique <= lastEffect {
				l.errorf("%s: evidence parameters are not in increasing effect-Unique order", where)
			}
			lastEffect = ev.Unique
			l.evidence[ev.Unique]++
			l.bindEvidenceCaptures(ev, where)
		}
		if d.RowParam != 0 {
			if l.captureVars[d.RowParam] {
				l.errorf("%s: duplicate residual capture variable %d", where, d.RowParam)
			}
			l.captureVars[d.RowParam] = true
		}
		for _, ev := range d.RowEffects {
			l.effectInstance(ev, where)
			l.evidence[ev.Unique]++
			l.bindEvidenceCaptures(ev, where)
		}
		// Every declared type parameter must be used by the value type or by
		// typed evidence (phantom effect parameters need not occur in Type).
		usedTyParams := map[int]bool{}
		for _, v := range types.RigidVarsIn(d.Type) {
			usedTyParams[v.ID] = true
		}
		for _, ev := range d.EffectParams {
			for _, a := range ev.Args {
				for _, v := range types.RigidVarsIn(a) {
					usedTyParams[v.ID] = true
				}
			}
		}
		for _, v := range d.TyParams {
			if !usedTyParams[v.ID] {
				l.errorf("%s: TyParams variable %d is unused", where, v.ID)
			}
		}
		if len(d.Params) > 0 {
			// The worker's type must peel exactly arity arrows, with the
			// body typed at the remainder; params enter the no-shadow scope.
			t := d.Type
			for _, param := range d.Params {
				fn, ok := t.(*types.TFun)
				if !ok {
					l.errorf("%s: fewer arrows than parameters", where)
					break
				}
				if param != "_" && l.scope[param] {
					l.errorf("%s: parameter `%s` shadows — the checker should have rejected this", where, param)
				}
				if param != "_" {
					l.scope[param] = true
					l.localTypes[param] = fn.Arg
				}
				t = fn.Ret
			}
			if !EqualValueRepresentation(t, d.Body.Type()) {
				l.errorf("%s: body type %s differs from peeled result %s",
					where, types.Show(d.Body.Type()), types.Show(t))
			}
			l.expr(d.Body, where)
			for _, param := range d.Params {
				if param != "_" {
					delete(l.scope, param)
					delete(l.localTypes, param)
				}
			}
		} else {
			if !EqualValueRepresentation(d.Type, d.Body.Type()) {
				l.errorf("%s: body type %s differs from def type %s",
					where, types.Show(d.Body.Type()), types.Show(d.Type))
			}
			l.expr(d.Body, where)
		}
		bodyControl := ExprControl(d.Body)
		if !controlBodyFits(bodyControl, d.Control) {
			l.errorf("%s: body control %s is not representable by contract %s", where, ControlName(bodyControl), ControlName(d.Control))
		}
		l.verifyControlANF(d.Body, true, where)
		for _, v := range d.ResultCaptures.Vars {
			if !l.captureVars[v] {
				l.errorf("%s: result capture summary references unbound variable %d", where, v)
			}
		}
		for _, ev := range d.EffectParams {
			l.evidence[ev.Unique]--
			l.unbindEvidenceCaptures(ev)
		}
		for _, ev := range d.RowEffects {
			l.evidence[ev.Unique]--
			l.unbindEvidenceCaptures(ev)
		}
		delete(l.captureVars, d.RowParam)
		for _, v := range d.ParamCaptures {
			delete(l.captureVars, v)
		}
	}
	if p.EntryDisplay != nil {
		l.tyParams = map[int]bool{}
		l.expr(p.EntryDisplay, "entry display")
		if !EqualValueRepresentation(p.EntryDisplay.Type(), b.String) {
			l.errorf("entry display must return String")
		}
	}
	l.errs = append(l.errs, verifyCapturesIn(p, context, b)...)
	l.errs = append(l.errs, CheckRowEvidence(p)...)
	return l.errs
}

// VerifyResumeStructure checks the handler control invariant without needing a
// complete program environment. Incremental elaboration uses it before Core is
// installed in the REPL; batch compilation runs the stronger full Lint after
// specialization.
func VerifyResumeStructure(e Expr) []error {
	l := &linter{resumeIDs: map[types.ResumeID]bool{}}
	var resumes []*ResumeTail
	Rewrite(e, func(t types.Type) types.Type { return t }, func(x Expr) Expr {
		switch x := x.(type) {
		case *Handle:
			for _, c := range x.Clauses {
				if c.Op != nil && c.Op.Abort {
					if c.ResumeID != 0 {
						l.errorf("abort handler clause has ResumeID %d", c.ResumeID)
					}
					if hasAnyResume(c.Body) {
						l.errorf("RESUME IN ABORT CLAUSE: `%s` contains ResumeTail", c.Op.Name)
					}
					continue
				}
				if c.ResumeID == 0 {
					l.errorf("handler clause has no ResumeID")
				} else if l.resumeIDs[c.ResumeID] {
					l.errorf("handler clause reuses ResumeID %d", c.ResumeID)
				} else {
					l.resumeIDs[c.ResumeID] = true
				}
				var state types.Type
				if x.State != nil {
					state = x.State.Ty
				}
				l.tailResume(c.Body, c.ResumeID, c.ResultType, x.Ty, state, "handler clause")
			}
		case *ResumeTail:
			resumes = append(resumes, x)
		}
		return x
	})
	for _, r := range resumes {
		if !l.resumeIDs[r.Owner] {
			l.errorf("ResumeTail owner %d has no handler clause", r.Owner)
		}
	}
	return l.errs
}

func (l *linter) stageType(ty types.Type, name, where string) {
	con, ok := ty.(*types.TCon)
	if !ok || len(con.Args) != 0 {
		l.errorf("%s: stage value must have type %s", where, name)
		return
	}
	adt := l.adts[con.Unique]
	if adt == nil || adt.Con.Name != name || con.Name != name {
		l.errorf("%s: stage value must have declared type %s", where, name)
	}
}

type linter struct {
	b                *types.Builtins
	scope            map[string]bool       // def names + enclosing Let/param names: no shadowing
	localTypes       map[string]types.Type // binding ABIs; occurrences cannot retag a stored callback
	workers          map[string]*Def
	adts             map[int]*types.ADTInfo // declared ADTs: equatable via derived eq
	effects          map[int]*types.EffectInfo
	tyParams         map[int]bool // the enclosing def's declared rigid vars
	evidence         map[int]int
	evidenceCaptures map[int][]types.CaptureSet
	captureVars      map[types.CaptureVar]bool
	scopeIDs         map[types.ScopeID]bool
	activeScopes     map[types.ScopeID]bool
	natives          map[string]*types.NativeInfo
	intrinsics       map[string]bool
	resumeOwner      types.ResumeID
	resumeIDs        map[types.ResumeID]bool
	resumeArg        types.Type
	resumeRet        types.Type
	resumeState      types.Type
	defName          string
	allowMachine     bool
	allowStage       bool
	errs             []error
}

func (l *linter) errorf(format string, args ...any) {
	l.errs = append(l.errs, fmt.Errorf(format, args...))
}

// unique returns the TCon unique of a ground nullary type, or -1.
func (l *linter) unique(t types.Type) int {
	if con, ok := t.(*types.TCon); ok && len(con.Args) == 0 {
		return con.Unique
	}
	return -1
}

func (l *linter) numeric(t types.Type) bool {
	u := l.unique(t)
	return u == l.b.Int.Unique || u == l.b.Float.Unique
}

// printable mirrors elaborate.checkPrintable at the type level.
func (l *linter) printable(t types.Type) bool {
	switch l.unique(t) {
	case l.b.Int.Unique, l.b.Float.Unique, l.b.String.Unique, l.b.Char.Unique, l.b.Bool.Unique:
		return true
	}
	if con, ok := t.(*types.TCon); ok {
		if _, isADT := l.adts[con.Unique]; isADT {
			for _, a := range con.Args {
				if !l.printable(a) {
					return false
				}
			}
			return true
		}
	}
	return false
}

func (l *linter) expr(e Expr, where string) {
	l.typ(e.Type(), where)
	switch e := e.(type) {
	case *IntLit:
		// An integer literal in a Number-generic body stays at the rigid
		// var's type: Go untyped constants are assignable to the type-set
		// param, the interpreter promotes (doc/design.md, "Interpreter and REPL").
		if l.unique(e.Ty) != l.b.Int.Unique {
			l.errorf("%s: IntLit typed %s", where, types.Show(e.Ty))
		}
	case *FloatLit:
		if l.unique(e.Ty) != l.b.Float.Unique {
			l.errorf("%s: FloatLit typed %s", where, types.Show(e.Ty))
		}
	case *StringLit:
		if l.unique(e.Ty) != l.b.String.Unique {
			l.errorf("%s: StringLit typed %s", where, types.Show(e.Ty))
		}
	case *CharLit:
		if l.unique(e.Ty) != l.b.Char.Unique {
			l.errorf("%s: CharLit typed %s", where, types.Show(e.Ty))
		}
	case *UnitLit:
		if l.unique(e.Ty) != l.b.Unit.Unique {
			l.errorf("%s: UnitLit typed %s", where, types.Show(e.Ty))
		}
	case *BoolLit:
		if l.unique(e.Ty) != l.b.Bool.Unique {
			l.errorf("%s: BoolLit typed %s", where, types.Show(e.Ty))
		}
	case *VarRef:
		if bound := l.localTypes[e.Name]; bound != nil && !EqualValueRepresentation(bound, e.Ty) {
			l.errorf("%s: reference `%s` changes its binding type from %s to %s", where, e.Name, types.Show(bound), types.Show(e.Ty))
		}
		// A worker name may appear ONLY as an App{Worker} callee (that
		// case does not recurse here): a bare reference means elaboration
		// failed to eta-expand a first-class use.
		if e.Local && !l.scope[e.Name] {
			l.errorf("%s: local reference `%s` is unbound", where, e.Name)
		}
		if _, isWorker := l.workers[e.Name]; isWorker && !e.Local {
			l.errorf("%s: bare reference to worker `%s` — first-class uses must be eta-expanded", where, e.Name)
		}
	case *Neg:
		if !l.numeric(e.Ty) {
			l.errorf("%s: Neg typed %s, want Int, Float, or a number variable", where, types.Show(e.Ty))
		}
		if !EqualValueRepresentation(e.Operand.Type(), e.Ty) {
			l.errorf("%s: Neg operand type differs from result", where)
		}
		l.expr(e.Operand, where)
	case *Quote:
		// Compile-time-only definitions are dropped before the program is
		// linted, so a surviving Quote means the emission rule let one
		// through (doc/design.md, "Compile-time metaprogramming").
		if !l.allowStage {
			l.errorf("%s: quote in emitted code — a compile-time-only value escaped", where)
		} else {
			l.stageType(e.Ty, "Meta.Code", where)
			if e.Template < 0 {
				l.errorf("%s: quote has invalid template identity", where)
			}
			for _, hole := range e.Holes {
				l.expr(hole, where)
				l.stageType(hole.Type(), "Meta.Code", where)
			}
		}
	case *TypeOf:
		if !l.allowStage {
			l.errorf("%s: typeOf in emitted code — a compile-time-only value escaped", where)
		} else {
			switch repr := e.Repr.(type) {
			case *meta.TypeRepr:
				l.stageType(e.Ty, "Meta.TypeRepr", where)
				if repr == nil || repr.Type == nil {
					l.errorf("%s: typeOf has no checked type representation", where)
				}
			case *meta.Code:
				// Derivers also inject already-checked Code arguments using
				// this constant node, rather than executing another quote.
				l.stageType(e.Ty, "Meta.Code", where)
				if repr == nil {
					l.errorf("%s: stage code constant is nil", where)
				}
			default:
				l.errorf("%s: invalid stage constant %T", where, e.Repr)
			}
		}
	case *NativeCall:
		n := l.natives[e.Name]
		if n == nil {
			l.errorf("%s: unknown native `%s`", where, e.Name)
		} else if len(e.Args) != n.Arity {
			l.errorf("%s: native `%s` arity mismatch", where, e.Name)
		} else {
			decl := n.Scheme.Body
			sub := map[int]types.Type{}
			for i, arg := range e.Args {
				fn, ok := decl.(*types.TFun)
				if !ok || !matchNativeType(fn.Arg, arg.Type(), sub) {
					l.errorf("%s: native `%s` argument %d does not instantiate its declaration", where, e.Name, i+1)
					break
				}
				decl = fn.Ret
			}
			if !matchNativeType(decl, e.Ty, sub) {
				l.errorf("%s: native `%s` result does not instantiate its declaration", where, e.Name)
			}
		}
		for _, a := range e.Args {
			l.expr(a, where)
		}
	case *If:
		if l.unique(e.Cond.Type()) != l.b.Bool.Unique {
			l.errorf("%s: If condition typed %s, want Bool", where, types.Show(e.Cond.Type()))
		}
		if !EqualValueRepresentation(e.Then.Type(), e.Ty) || !EqualValueRepresentation(e.Else.Type(), e.Ty) {
			l.errorf("%s: If branches disagree with result type", where)
		}
		l.expr(e.Cond, where)
		l.expr(e.Then, where)
		l.expr(e.Else, where)
	case *Let:
		if !EqualValueRepresentation(e.Ty, e.Body.Type()) {
			l.errorf("%s: Let type differs from its body", where)
		}
		if l.scope[e.Name] {
			l.errorf("%s: Let shadows `%s` — the checker should have rejected this", where, e.Name)
		}
		if e.Rec {
			if _, ok := e.Rhs.(*Lambda); !ok {
				l.errorf("%s: recursive Let `%s` whose Rhs is not a Lambda", where, e.Name)
			}
			l.scope[e.Name] = true // in scope inside its own Rhs
			l.localTypes[e.Name] = e.Rhs.Type()
			l.expr(e.Rhs, where)
		} else {
			l.expr(e.Rhs, where)
			l.scope[e.Name] = true
			l.localTypes[e.Name] = e.Rhs.Type()
		}
		l.expr(e.Body, where)
		delete(l.scope, e.Name)
		delete(l.localTypes, e.Name)
	case *Lambda:
		fn, ok := e.Ty.(*types.TFun)
		if !ok {
			l.errorf("%s: Lambda typed %s, want a function type", where, types.Show(e.Ty))
			return
		}
		if !EqualValueRepresentation(fn.Ret, e.Body.Type()) {
			l.errorf("%s: Lambda body type %s differs from arrow result %s",
				where, types.Show(e.Body.Type()), types.Show(fn.Ret))
		}
		l.control(types.FunctionControl(fn), where)
		if e.Param != "_" && l.scope[e.Param] {
			l.errorf("%s: Lambda param `%s` shadows — the checker should have rejected this", where, e.Param)
		}
		if e.Param != "_" {
			l.scope[e.Param] = true
			l.localTypes[e.Param] = fn.Arg
		}
		if e.ParamCapture == 0 || l.captureVars[e.ParamCapture] {
			l.errorf("%s: Lambda has invalid capture parameter %d", where, e.ParamCapture)
		}
		l.captureVars[e.ParamCapture] = true
		wantEvidence := rowEvidence(fn.Eff)
		if len(e.EffectParams) != len(wantEvidence) {
			l.errorf("%s: Lambda has %d evidence capture binders, want %d", where, len(e.EffectParams), len(wantEvidence))
		}
		for i, ev := range e.EffectParams {
			l.effectInstance(ev, where)
			if i < len(wantEvidence) && !equalEffectInstance(ev, wantEvidence[i]) {
				l.errorf("%s: Lambda evidence parameter %d disagrees with its function type", where, i+1)
			}
			l.evidence[ev.Unique]++
			l.bindEvidenceCaptures(ev, where)
		}
		if e.RowParam != 0 {
			if l.captureVars[e.RowParam] {
				l.errorf("%s: duplicate residual capture variable %d", where, e.RowParam)
			}
			l.captureVars[e.RowParam] = true
		}
		for _, ev := range e.RowEffects {
			l.effectInstance(ev, where)
			l.evidence[ev.Unique]++
			l.bindEvidenceCaptures(ev, where)
		}
		l.expr(e.Body, where)
		if bodyControl := ExprControl(e.Body); !controlBodyFits(bodyControl, types.FunctionControl(fn)) {
			l.errorf("%s: Lambda body control %s is not representable by arrow %s", where, ControlName(bodyControl), ControlName(types.FunctionControl(fn)))
		}
		for _, ev := range e.EffectParams {
			l.evidence[ev.Unique]--
			l.unbindEvidenceCaptures(ev)
		}
		for _, ev := range e.RowEffects {
			l.evidence[ev.Unique]--
			l.unbindEvidenceCaptures(ev)
		}
		delete(l.captureVars, e.RowParam)
		delete(l.captureVars, e.ParamCapture)
		if e.Param != "_" {
			delete(l.scope, e.Param)
			delete(l.localTypes, e.Param)
		}
	case *Perform:
		l.control(e.Control, where)
		if e.Control != e.Effect.Control {
			l.errorf("%s: Perform control %s disagrees with its evidence %s", where, ControlName(e.Control), ControlName(e.Effect.Control))
		}
		if e.Op == nil || e.Op.Owner.Unique != e.Effect.Unique {
			l.errorf("%s: malformed Perform evidence", where)
		} else if e.Op.Abort {
			l.errorf("%s: abort-only operation `%s` was not lowered to ControlExit", where, e.Op.Name)
		} else if !l.operationBelongs(e.Op) {
			l.errorf("%s: Perform operation `%s` is not declared by its effect", where, e.Op.Name)
		}
		l.effectInstance(e.Effect, where)
		if e.Op != nil && !e.Op.Builtin && e.Op.Native == nil && l.evidence[e.Effect.Unique] == 0 {
			l.errorf("%s: Perform `%s` has no lexical evidence", where, e.Op.Name)
		} else if e.Op != nil && !e.Op.Builtin && e.Op.Native == nil {
			l.evidenceAvailable(e.Effect, where)
		}
		if e.Op != nil && len(e.Args) != e.Op.Arity {
			l.errorf("%s: Perform `%s` arity mismatch", where, e.Op.Name)
		}
		if e.Op != nil && len(e.Args) == e.Op.Arity {
			wantArgs, wantResult := l.operationTypes(e.Op, e.Effect)
			isPrint := types.SurfaceName(e.Op.Owner.Name) == "IO" && types.SurfaceName(e.Op.Name) == "print"
			for i, a := range e.Args {
				if !isPrint && i < len(wantArgs) && !EqualValueRepresentation(a.Type(), wantArgs[i]) {
					l.errorf("%s: Perform `%s` arg %d typed %s, want %s", where, e.Op.Name, i+1, types.Show(a.Type()), types.Show(wantArgs[i]))
				}
			}
			if isPrint {
				if len(e.Args) == 1 && !l.printable(e.Args[0].Type()) {
					l.errorf("%s: IO.print argument typed %s, not printable", where, types.Show(e.Args[0].Type()))
				}
			} else if len(e.Op.LocalVars) > 0 {
				l.errorf("%s: operation-local polymorphism survived into Core for `%s`", where, e.Op.Name)
			}
			if wantResult != nil && !EqualValueRepresentation(e.Ty, wantResult) {
				l.errorf("%s: Perform `%s` typed %s, want %s", where, e.Op.Name, types.Show(e.Ty), types.Show(wantResult))
			}
		}
		for _, a := range e.Args {
			l.expr(a, where)
		}
	case *Suspend:
		if !l.allowMachine {
			l.errorf("%s: compiler-only suspension reached ordinary Core", where)
		}
		if e.Request == nil {
			l.errorf("%s: suspension has no request", where)
			return
		}
		if e.Owner.Unique != 0 {
			l.effectInstance(e.Owner, where)
			effect := l.effects[e.Owner.Unique]
			if effect == nil || !effect.Suspension || effect.Name != e.Owner.Name || e.Owner.Control.Transport != types.Machine {
				l.errorf("%s: suspension has invalid owner effect", where)
			}
			l.evidenceAvailable(e.Owner, where)
			if len(e.Owner.Args) != 1 || !EqualValueRepresentation(e.Owner.Args[0], e.Request.Type()) || l.unique(e.Ty) != l.b.Unit.Unique {
				l.errorf("%s: owned yield request/result disagrees with owner type", where)
			}
		} else {
			for _, effect := range l.effects {
				if effect.Suspension {
					l.errorf("%s: source suspension lacks lexical owner evidence", where)
					break
				}
			}
		}
		if c := ExprControl(e.Request); c.Transport != types.Direct || c.Polymorphic {
			l.errorf("%s: suspension request control %s is not direct", where, ControlName(c))
		}
		l.expr(e.Request, where)
	case *IteratorScope:
		l.control(e.Control, where)
		if e.Scope == 0 || l.scopeIDs[e.Scope] {
			l.errorf("%s: iterator scope has invalid or reused scope identity %d", where, e.Scope)
		}
		l.scopeIDs[e.Scope] = true
		if !l.intrinsics[l.defName] || (l.defName != types.StreamWithProducerName) {
			l.errorf("%s: iterator scope outside the declared `%s` intrinsic", where, types.StreamWithProducerName)
		}
		cursor, ok := e.CursorTy.(*types.TCon)
		if !ok || cursor.Name != types.IteratorTypeName || len(cursor.Args) != 2 {
			l.errorf("%s: iterator scope cursor typed %s, want `%s a e`", where, types.Show(e.CursorTy), types.IteratorTypeName)
		}
		producer, ok := e.Producer.Type().(*types.TFun)
		if e.Yield.Unique != 0 {
			l.effectInstance(e.Yield, where)
			if effect := l.effects[e.Yield.Unique]; effect == nil || !effect.Suspension || e.Yield.Control.Transport != types.Machine {
				l.errorf("%s: iterator scope has invalid Yield evidence", where)
			}
			if cursor == nil || len(cursor.Args) != 2 || len(e.Yield.Args) != 1 || !EqualValueRepresentation(cursor.Args[0], e.Yield.Args[0]) {
				l.errorf("%s: iterator cursor element disagrees with Yield owner", where)
			}
		}
		if !ok {
			l.errorf("%s: iterator producer is not a function", where)
		} else {
			if l.unique(producer.Arg) != l.b.Unit.Unique || l.unique(producer.Ret) != l.b.Unit.Unique {
				l.errorf("%s: iterator producer must have shape `() -> ()`", where)
			}
			if types.FunctionControl(producer).Transport != types.Machine {
				l.errorf("%s: iterator producer does not use Machine transport", where)
			}
			for _, label := range producer.Eff.Labels {
				if label.Suspension && (e.Yield.Unique != label.Unique || e.Yield.Name != label.Name || len(e.Yield.Args) != 1 || len(label.Args) != 1 || !EqualValueRepresentation(e.Yield.Args[0], label.Args[0]) || !types.EqualCaptures(e.Yield.Captures, types.ScopeCapture(e.Scope))) {
					l.errorf("%s: iterator scope lacks matching lexical Yield ownership", where)
				}
			}
		}
		consumer, ok := e.Consumer.Type().(*types.TFun)
		if !ok {
			l.errorf("%s: iterator consumer is not a function", where)
		} else {
			if !EqualValueRepresentation(consumer.Arg, e.CursorTy) || !EqualValueRepresentation(consumer.Ret, e.Ty) {
				l.errorf("%s: iterator consumer type disagrees with cursor or result", where)
			}
			if e.Traversal.Unique == 0 {
				if want := types.FunctionControl(consumer); e.Control != want {
					l.errorf("%s: iterator scope control %s disagrees with consumer %s", where, ControlName(e.Control), ControlName(want))
				}
			} else {
				l.effectInstance(e.Traversal, where)
				effect := l.effects[e.Traversal.Unique]
				if effect == nil || effect.Name != types.IteratorTraversalEffectName || !effect.Suspension || e.Yield.Unique == 0 || e.Traversal.Control.Transport != types.Machine || len(e.Traversal.Args) != 0 || !types.EqualCaptures(e.Traversal.Captures, types.ScopeCapture(e.Scope)) {
					l.errorf("%s: iterator scope has invalid Traversal ownership", where)
				}
				found := false
				residual := &types.TFun{Eff: types.Row{Tail: consumer.Eff.Tail}}
				for _, label := range consumer.Eff.Labels {
					if label.Unique == e.Traversal.Unique {
						found = true
					} else {
						residual.Eff.Labels = append(residual.Eff.Labels, label)
					}
				}
				minimum := types.FunctionControl(residual)
				if !found || minimum.Transport > e.Control.Transport || minimum.Polymorphic && !e.Control.Polymorphic || l.workers[l.defName] == nil || ArrowControl(l.workers[l.defName].Type, len(l.workers[l.defName].Params)) != e.Control {
					l.errorf("%s: iterator scope has stale residual Traversal control", where)
				}
			}
		}
		l.expr(e.Producer, where)
		l.expr(e.Consumer, where)

	case *IteratorNext:
		if !l.intrinsics[types.IteratorNextName] || l.defName != types.IteratorNextName {
			l.errorf("%s: advancement outside the declared Iterator.next intrinsic", where)
		}
		if e.Access != types.ExclusiveAdvance {
			l.errorf("%s: cursor advancement lacks exclusive access proof", where)
		}
		if e.Cursor == nil {
			l.errorf("%s: cursor advancement has no cursor", where)
		} else {
			if err := CheckCursorResult(e.Cursor.Type(), e.Ty, e.Result); err != nil {
				l.errorf("%s: %v", where, err)
			}
			l.expr(e.Cursor, where)
		}

	case *FailureInspect:
		if !l.intrinsics[e.Name] || l.defName != e.Name || !types.FailureInspection(e.Name) {
			l.errorf("%s: failure inspection outside its declared intrinsic", where)
		}
		args := make([]types.Type, len(e.Args))
		for i, arg := range e.Args {
			if arg != nil {
				args[i] = arg.Type()
				l.expr(arg, where)
			}
		}
		if !types.FailureInspectionShape(e.Name, args, e.Ty) {
			l.errorf("%s: invalid failure inspection signature", where)
			return
		}
		failure := args[len(args)-1].(*types.TCon)
		if adt := l.adts[failure.Unique]; adt == nil || adt.Con.Name != types.FailureTypeName {
			l.errorf("%s: failure inspection has no declared snapshot identity", where)
		}
		if e.Name == types.FailureArgumentName {
			result := e.Ty.(*types.TCon)
			adt := e.Result
			if adt == nil || l.adts[result.Unique] != adt || adt.Con.Name != "Maybe.Maybe" || len(adt.Params) != 1 || len(adt.Ctors) != 2 ||
				adt.Ctors[0].Name != "Maybe.Nothing" || len(adt.Ctors[0].Fields) != 0 || adt.Ctors[1].Name != "Maybe.Just" || len(adt.Ctors[1].Fields) != 1 || !EqualValueRepresentation(adt.InstFields(adt.Ctors[1], result.Args)[0], result.Args[0]) {
				l.errorf("%s: failure inspection has missing or stale Maybe packaging proof", where)
			}
		} else if e.Result != nil {
			l.errorf("%s: non-argument failure inspection carries a packaging proof", where)
		}
	case *ControlExit:
		l.control(e.Effect.Control, where)
		l.effectInstance(e.Effect, where)
		if e.Effect.Control.Transport != types.Exit {
			l.errorf("%s: ControlExit evidence is not Exit transport", where)
		}
		if e.Op == nil || e.Op.Owner.Unique != e.Effect.Unique || !l.operationBelongs(e.Op) {
			l.errorf("%s: ControlExit has an undeclared operation descriptor", where)
		} else if !e.Op.Abort {
			l.errorf("%s: ControlExit operation `%s` is resumptive", where, e.Op.Name)
		} else {
			if l.evidence[e.Effect.Unique] == 0 {
				l.errorf("%s: ControlExit `%s` has no lexical evidence", where, e.Op.Name)
			} else {
				l.evidenceAvailable(e.Effect, where)
			}
			want, _ := l.operationTypes(e.Op, e.Effect)
			if len(e.Payload) != len(want) {
				l.errorf("%s: ControlExit `%s` payload arity mismatch", where, e.Op.Name)
			}
			for i, p := range e.Payload {
				if i < len(want) && !EqualValueRepresentation(p.Type(), want[i]) {
					l.errorf("%s: ControlExit `%s` payload %d typed %s, want %s", where, e.Op.Name, i+1, types.Show(p.Type()), types.Show(want[i]))
				}
				l.expr(p, where)
			}
		}
	case *ResumeTail:
		if l.resumeOwner == 0 || e.Owner != l.resumeOwner || l.resumeArg == nil || l.resumeRet == nil {
			l.errorf("%s: ResumeTail owner %d is outside its handler clause", where, e.Owner)
		} else {
			if !EqualValueRepresentation(e.Value.Type(), l.resumeArg) {
				l.errorf("%s: Resume argument typed %s, want %s", where, types.Show(e.Value.Type()), types.Show(l.resumeArg))
			}
			if !EqualValueRepresentation(e.ClauseResult, l.resumeRet) {
				l.errorf("%s: ResumeTail clause result typed %s, want handler result %s", where, types.Show(e.ClauseResult), types.Show(l.resumeRet))
			}
		}
		l.expr(e.Value, where)
		if l.resumeState == nil && e.NextState != nil {
			l.errorf("%s: stateless ResumeTail has a next state", where)
		} else if l.resumeState != nil && e.NextState == nil {
			l.errorf("%s: stateful ResumeTail has no next state", where)
		} else if e.NextState != nil {
			if !EqualValueRepresentation(e.NextState.Type(), l.resumeState) {
				l.errorf("%s: next handler state typed %s, want %s", where, types.Show(e.NextState.Type()), types.Show(l.resumeState))
			}
			l.expr(e.NextState, where)
		}
	case *Seq:
		if l.unique(e.First.Type()) != l.b.Unit.Unique {
			l.errorf("%s: Seq first expression is not Unit", where)
		}
		if !EqualValueRepresentation(e.Ty, e.Then.Type()) {
			l.errorf("%s: Seq type differs from its final expression", where)
		}
		l.expr(e.First, where)
		l.expr(e.Then, where)
	case *Bracket:
		l.control(e.Control, where)
		if e.Scope == 0 || l.scopeIDs[e.Scope] {
			l.errorf("%s: cleanup scope has invalid or reused scope identity %d", where, e.Scope)
		}
		l.scopeIDs[e.Scope] = true
		// Elaboration is the only producer, and it emits exactly one scope, as
		// the body of the bundled intrinsic. A Bracket anywhere else would mean
		// a transform copied or moved a pending cleanup obligation.
		if l.defName != types.ScopeBracketName {
			l.errorf("%s: cleanup scope outside the `%s` intrinsic", where, types.ScopeBracketName)
		}
		if e.Resource == "" || e.ResourceTy == nil || e.Acquire == nil || e.Release == nil || e.Body == nil {
			l.errorf("%s: cleanup scope has incomplete metadata", where)
			return
		}
		if !EqualValueRepresentation(e.Acquire.Type(), e.ResourceTy) {
			l.errorf("%s: cleanup scope acquires %s, want resource %s", where, types.Show(e.Acquire.Type()), types.Show(e.ResourceTy))
		}
		if l.unique(e.Release.Type()) != l.b.Unit.Unique {
			l.errorf("%s: cleanup scope release typed %s, want ()", where, types.Show(e.Release.Type()))
		}
		if !EqualValueRepresentation(e.Body.Type(), e.Ty) {
			l.errorf("%s: cleanup scope type differs from its body", where)
		}
		if want := types.JoinControl(ExprControl(e.Acquire), ExprControl(e.Release), ExprControl(e.Body)); e.Control != want {
			l.errorf("%s: cleanup scope control %s disagrees with its children %s", where, ControlName(e.Control), ControlName(want))
		}
		l.expr(e.Acquire, where)
		if l.scope[e.Resource] {
			l.errorf("%s: cleanup scope resource `%s` shadows", where, e.Resource)
		}
		l.scope[e.Resource] = true
		l.localTypes[e.Resource] = e.ResourceTy
		l.activeScopes[e.Scope] = true
		l.expr(e.Body, where)
		l.expr(e.Release, where)
		delete(l.activeScopes, e.Scope)
		delete(l.scope, e.Resource)
		delete(l.localTypes, e.Resource)
	case *Handle:
		l.control(e.Control, where)
		l.effectInstance(e.Effect, where)
		if effect := l.effects[e.Effect.Unique]; effect != nil && effect.Suspension {
			l.errorf("%s: ordinary handler intercepts compiler-owned suspension effect", where)
		}
		if e.State != nil {
			if e.State.Name == "" || e.State.Ty == nil || e.State.Initial == nil {
				l.errorf("%s: parameterized handler has incomplete state metadata", where)
			} else {
				if !EqualValueRepresentation(e.State.Initial.Type(), e.State.Ty) {
					l.errorf("%s: initial handler state typed %s, want %s", where, types.Show(e.State.Initial.Type()), types.Show(e.State.Ty))
				}
				l.expr(e.State.Initial, where)
			}
		}
		if e.Scope == 0 || l.scopeIDs[e.Scope] {
			l.errorf("%s: handler has invalid or reused scope identity %d", where, e.Scope)
		}
		l.scopeIDs[e.Scope] = true
		if !types.EqualCaptures(e.Effect.Captures, types.ScopeCapture(e.Scope)) {
			l.errorf("%s: handler evidence does not name its scope identity", where)
		}
		if eff := l.effects[e.Effect.Unique]; eff != nil && e.Scoped != (eff.Scoped || e.State != nil) {
			l.errorf("%s: handler scoped policy disagrees with effect `%s`", where, eff.Name)
		}
		l.activeScopes[e.Scope] = true
		l.evidence[e.Effect.Unique]++
		l.evidenceCaptures[e.Effect.Unique] = append(l.evidenceCaptures[e.Effect.Unique], e.Effect.Captures)
		l.expr(e.Body, where)
		l.evidence[e.Effect.Unique]--
		l.evidenceCaptures[e.Effect.Unique] = l.evidenceCaptures[e.Effect.Unique][:len(l.evidenceCaptures[e.Effect.Unique])-1]
		delete(l.activeScopes, e.Scope)
		seen := map[string]bool{}
		for _, c := range e.Clauses {
			report := l.defName == types.FailAttemptReportName && l.intrinsics[types.FailAttemptReportName]
			if report != (c.SuppressedParam != "") || (c.SuppressedParam == "") != (c.SuppressedType == nil) {
				l.errorf("%s: missing or misplaced suppressed failure binding", where)
			}
			if c.SuppressedParam != "" {
				valid := report && c.Op != nil && c.Op.Abort && e.Effect.Name == "Fail.Fail"
				list, ok := c.SuppressedType.(*types.TCon)
				valid = valid && ok && list.Name == "List.List" && len(list.Args) == 1
				if valid {
					failure, ok := list.Args[0].(*types.TCon)
					valid = ok && failure.Name == types.FailureTypeName && len(failure.Args) == 0 && l.adts[failure.Unique] != nil && l.adts[failure.Unique].Con.Name == types.FailureTypeName && l.adts[list.Unique] != nil && l.adts[list.Unique].Repr == types.ReprList
				}
				if !valid {
					l.errorf("%s: invalid suppressed failure packaging proof", where)
				}
				if l.scope[c.SuppressedParam] {
					l.errorf("%s: suppressed failure binding shadows", where)
				}
				l.scope[c.SuppressedParam] = true
				l.localTypes[c.SuppressedParam] = c.SuppressedType
			}
			if c.Op == nil || c.Op.Owner.Unique != e.Effect.Unique {
				l.errorf("%s: handler clause has wrong effect", where)
				continue
			}
			if !l.operationBelongs(c.Op) {
				l.errorf("%s: handler clause operation `%s` is not declared by its effect", where, c.Op.Name)
				continue
			}
			if seen[c.Op.Name] {
				l.errorf("%s: duplicate handler clause for `%s`", where, c.Op.Name)
			}
			seen[c.Op.Name] = true
			wantParams, opResult := l.operationTypes(c.Op, e.Effect)
			if len(c.Params) != c.Op.Arity || len(c.ParamTypes) != c.Op.Arity {
				l.errorf("%s: handler clause `%s` arity mismatch", where, c.Op.Name)
			}
			for i, pt := range c.ParamTypes {
				if i < len(wantParams) && !EqualValueRepresentation(pt, wantParams[i]) {
					l.errorf("%s: handler clause `%s` param %d typed %s, want %s", where, c.Op.Name, i+1, types.Show(pt), types.Show(wantParams[i]))
				}
			}
			for i, p := range c.Params {
				if p == "()" && i < len(c.ParamTypes) && l.unique(c.ParamTypes[i]) != l.b.Unit.Unique {
					l.errorf("%s: handler clause `%s` uses () for a non-Unit parameter", where, c.Op.Name)
				}
			}
			if !c.Op.Abort && !EqualValueRepresentation(c.ResultType, opResult) {
				l.errorf("%s: handler clause `%s` evidence result typed %s, want operation result %s", where, c.Op.Name, types.Show(c.ResultType), types.Show(opResult))
			}
			if !EqualValueRepresentation(c.Body.Type(), e.Ty) {
				l.errorf("%s: handler clause `%s` body does not exactly match the handler type", where, c.Op.Name)
			}
			if e.State != nil {
				if l.scope[e.State.Name] {
					l.errorf("%s: handler state `%s` shadows", where, e.State.Name)
				}
				l.scope[e.State.Name] = true
				l.localTypes[e.State.Name] = e.State.Ty
			}
			for i, p := range c.Params {
				if p != "_" && p != "()" {
					if l.scope[p] {
						l.errorf("%s: handler parameter `%s` shadows", where, p)
					}
					l.scope[p] = true
					if i < len(c.ParamTypes) {
						l.localTypes[p] = c.ParamTypes[i]
					}
				}
			}
			var state types.Type
			if e.State != nil {
				state = e.State.Ty
			}
			oldOwner, oldArg, oldRet, oldState := l.resumeOwner, l.resumeArg, l.resumeRet, l.resumeState
			if c.Op.Abort {
				if c.ResumeID != 0 {
					l.errorf("%s: abort clause `%s` has a ResumeID", where, c.Op.Name)
				}
				if hasAnyResume(c.Body) {
					l.errorf("%s: RESUME IN ABORT CLAUSE: `%s` contains ResumeTail", where, c.Op.Name)
				}
			} else {
				if c.ResumeID == 0 {
					l.errorf("%s: handler clause `%s` has no ResumeID", where, c.Op.Name)
				} else if l.resumeIDs[c.ResumeID] {
					l.errorf("%s: handler clause `%s` reuses ResumeID %d", where, c.Op.Name, c.ResumeID)
				} else {
					l.resumeIDs[c.ResumeID] = true
				}
				l.tailResume(c.Body, c.ResumeID, opResult, e.Ty, state, where)
				l.resumeOwner, l.resumeArg, l.resumeRet, l.resumeState = c.ResumeID, opResult, e.Ty, state
			}
			l.expr(c.Body, where)
			delete(l.scope, c.SuppressedParam)
			delete(l.localTypes, c.SuppressedParam)
			l.resumeOwner, l.resumeArg, l.resumeRet, l.resumeState = oldOwner, oldArg, oldRet, oldState
			for _, p := range c.Params {
				delete(l.scope, p)
				delete(l.localTypes, p)
			}
			if e.State != nil {
				delete(l.scope, e.State.Name)
				delete(l.localTypes, e.State.Name)
			}
		}
		if eff := l.effects[e.Effect.Unique]; eff != nil {
			for _, op := range eff.Ops {
				if !seen[op.Name] {
					l.errorf("%s: handler is missing a clause for `%s`", where, op.Name)
				}
			}
		}
		if e.Return != nil {
			if e.State != nil {
				l.scope[e.State.Name] = true
				l.localTypes[e.State.Name] = e.State.Ty
			}
			if e.Return.Param == "()" && l.unique(e.Body.Type()) != l.b.Unit.Unique {
				l.errorf("%s: handler return clause uses () for a non-Unit result", where)
			}
			if e.Return.Param != "_" && e.Return.Param != "()" {
				l.scope[e.Return.Param] = true
				l.localTypes[e.Return.Param] = e.Body.Type()
			}
			l.expr(e.Return.Body, where)
			if !EqualValueRepresentation(e.Return.Body.Type(), e.Ty) {
				l.errorf("%s: handler return clause does not exactly match the handler type", where)
			}
			delete(l.scope, e.Return.Param)
			delete(l.localTypes, e.Return.Param)
			if e.State != nil {
				delete(l.scope, e.State.Name)
				delete(l.localTypes, e.State.Name)
			}
		}
	case *App:
		if con, ok := e.Ty.(*types.TCon); ok && con.Name == types.FailureTypeName && e.CalleeKind == Ctor {
			l.errorf("%s: opaque failure snapshot constructed as an ordinary ADT", where)
		}
		l.control(e.Control, where)
		switch e.CalleeKind {
		case Worker:
			ref, ok := e.Callee.(*VarRef)
			if !ok {
				l.errorf("%s: App{Worker} callee is %T, want a VarRef", where, e.Callee)
				return
			}
			def, isWorker := l.workers[ref.Name]
			if !isWorker {
				l.errorf("%s: App{Worker} callee `%s` is not a worker", where, ref.Name)
				return
			}
			if len(e.Args) != len(def.Params) {
				l.errorf("%s: App{Worker} `%s` has %d args, arity is %d",
					where, ref.Name, len(e.Args), len(def.Params))
				return
			}
			if len(e.TyArgs) != len(def.TyParams) {
				l.errorf("%s: App{Worker} `%s` has %d type args, callee declares %d type params",
					where, ref.Name, len(e.TyArgs), len(def.TyParams))
				return
			}
			if len(e.EvidenceArgs) != len(def.EffectParams) {
				l.errorf("%s: App{Worker} `%s` has %d evidence args, callee declares %d", where, ref.Name, len(e.EvidenceArgs), len(def.EffectParams))
			}
			// Check against the callee's type INSTANTIATED at this call's
			// explicit type arguments — the doc/design.md, "Go backend and runtime" invariant.
			calleeTy := def.Type
			if len(e.TyArgs) > 0 {
				m := make(map[int]types.Type, len(def.TyParams))
				for i, v := range def.TyParams {
					m[v.ID] = e.TyArgs[i]
				}
				calleeTy = types.SubstRigid(calleeTy, m)
			}
			calleeTy = l.runtimeType(calleeTy)
			for i, ev := range e.EvidenceArgs {
				l.effectInstance(ev, where)
				if i >= len(def.EffectParams) {
					continue
				}
				want := def.EffectParams[i]
				if len(e.TyArgs) > 0 {
					m := make(map[int]types.Type, len(def.TyParams))
					for j, v := range def.TyParams {
						m[v.ID] = e.TyArgs[j]
					}
					want = substEffectInstance(want, m)
				}
				if !equalEffectInstance(ev, want) {
					l.errorf("%s: App{Worker} `%s` evidence arg %d disagrees with the callee (got %s/%s, want %s/%s)", where, ref.Name, i+1,
						effectArgsText(ev), ControlName(ev.Control), effectArgsText(want), ControlName(want.Control))
				}
				if l.evidence[ev.Unique] == 0 {
					l.errorf("%s: App{Worker} `%s` passes unavailable lexical evidence `%s`", where, ref.Name, ev.Name)
				} else {
					l.evidenceAvailable(ev, where)
				}
			}
			argTys, ret := PeelFun(calleeTy, len(def.Params))
			wantControl := ArrowControl(calleeTy, len(def.Params))
			if !controlInstance(e.Control, wantControl) {
				l.errorf("%s: App{Worker} `%s` control %s disagrees with callee %s", where, ref.Name, ControlName(e.Control), ControlName(wantControl))
			}
			for i, a := range e.Args {
				if !EqualValueRepresentation(a.Type(), argTys[i]) {
					l.errorf("%s: App{Worker} `%s` arg %d typed %s, want %s",
						where, ref.Name, i+1, types.Show(a.Type()), types.Show(argTys[i]))
				}
				l.expr(a, where)
			}
			if !EqualValueRepresentation(e.Ty, ret) {
				l.errorf("%s: App{Worker} `%s` typed %s, want %s",
					where, ref.Name, types.Show(e.Ty), types.Show(ret))
			}
		case Value:
			if len(e.Args) != 1 {
				l.errorf("%s: App{Value} must apply exactly one argument, got %d", where, len(e.Args))
				return
			}
			fn, ok := e.Callee.Type().(*types.TFun)
			if !ok {
				l.errorf("%s: App{Value} callee typed %s, want a function type",
					where, types.Show(e.Callee.Type()))
				return
			}
			if !EqualValueRepresentation(e.Args[0].Type(), fn.Arg) {
				l.errorf("%s: App{Value} arg typed %s, want %s",
					where, types.Show(e.Args[0].Type()), types.Show(fn.Arg))
			}
			if !EqualValueRepresentation(e.Ty, fn.Ret) {
				l.errorf("%s: App{Value} typed %s, want %s",
					where, types.Show(e.Ty), types.Show(fn.Ret))
			}
			wantControl := types.FunctionControl(fn)
			if !controlInstance(e.Control, wantControl) {
				l.errorf("%s: App{Value} control %s disagrees with arrow %s", where, ControlName(e.Control), ControlName(wantControl))
			}
			wantEvidence := rowEvidence(fn.Eff)
			if len(e.EvidenceArgs) != len(wantEvidence) {
				l.errorf("%s: App{Value} has %d evidence args, function requires %d", where, len(e.EvidenceArgs), len(wantEvidence))
			}
			for i, ev := range e.EvidenceArgs {
				l.effectInstance(ev, where)
				if i < len(wantEvidence) && !equalEffectInstance(ev, wantEvidence[i]) {
					l.errorf("%s: App{Value} evidence arg %d disagrees with its function type", where, i+1)
				}
				if l.evidence[ev.Unique] == 0 {
					l.errorf("%s: App{Value} passes unavailable lexical evidence `%s`", where, ev.Name)
				} else {
					l.evidenceAvailable(ev, where)
				}
			}
			l.expr(e.Callee, where)
			l.expr(e.Args[0], where)
		case Ctor:
			if e.Control != (types.Control{}) {
				l.errorf("%s: App{Ctor} must use direct control", where)
			}
			if e.Ctor == nil {
				l.errorf("%s: App{Ctor} without constructor info", where)
				return
			}
			if len(e.Args) != len(e.Ctor.Fields) {
				l.errorf("%s: App{Ctor} `%s` has %d args, constructor takes %d — partials must be eta-expanded",
					where, e.Ctor.Name, len(e.Args), len(e.Ctor.Fields))
				return
			}
			adt := l.adts[e.Ctor.Result.Unique]
			if adt == nil && e.Ctor.Result.Unique == l.b.Bool.Unique {
				// True/False never reach App{Ctor} (they are BoolLits).
				l.errorf("%s: App{Ctor} at Bool", where)
				return
			}
			if adt == nil {
				l.errorf("%s: App{Ctor} `%s` belongs to an undeclared type", where, e.Ctor.Name)
				return
			}
			result, ok := e.Ty.(*types.TCon)
			if !ok || result.Unique != adt.Con.Unique || len(result.Args) != len(adt.Params) {
				l.errorf("%s: App{Ctor} `%s` typed %s, want a `%s` value",
					where, e.Ctor.Name, types.Show(e.Ty), adt.Con.Name)
				return
			}
			if len(e.TyArgs) != len(adt.Params) {
				l.errorf("%s: App{Ctor} `%s` has %d type args, type declares %d params",
					where, e.Ctor.Name, len(e.TyArgs), len(adt.Params))
				return
			}
			for i, ta := range e.TyArgs {
				if !EqualValueRepresentation(ta, result.Args[i]) {
					l.errorf("%s: App{Ctor} `%s` type arg %d disagrees with its result type", where, e.Ctor.Name, i+1)
				}
			}
			fields := l.runtimeInstFields(adt, e.Ctor, result.Args)
			for i, a := range e.Args {
				if !EqualValueRepresentation(a.Type(), fields[i]) {
					l.errorf("%s: App{Ctor} `%s` arg %d typed %s, want %s",
						where, e.Ctor.Name, i+1, types.Show(a.Type()), types.Show(fields[i]))
				}
				l.expr(a, where)
			}
		default:
			l.errorf("%s: App with unknown CalleeKind %d", where, e.CalleeKind)
		}
	case *Case:
		if con, ok := e.Scrut.Type().(*types.TCon); ok && con.Name == types.FailureTypeName {
			l.errorf("%s: opaque failure snapshot matched as an ordinary ADT", where)
		}
		if e.Bind == "" {
			l.errorf("%s: Case without a scrutinee binder", where)
		}
		if l.scope[e.Bind] {
			l.errorf("%s: Case binder `%s` shadows", where, e.Bind)
		}
		l.expr(e.Scrut, where)
		l.scope[e.Bind] = true
		l.localTypes[e.Bind] = e.Scrut.Type()
		l.tree(e.Tree, e.Ty, where)
		delete(l.scope, e.Bind)
		delete(l.localTypes, e.Bind)
	default:
		l.errorf("%s: unhandled Core node %T", where, e)
	}
}

// tailResume independently proves the lowering contract consumed by both
// backends. Only control-flow tails may contain the clause's ResumeTail; all
// evaluated operands must be free of that owner.
func (l *linter) tailResume(e Expr, owner types.ResumeID, arg, result, state types.Type, where string) {
	noResume := func(x Expr, slot string) bool {
		if hasResumeOwner(x, owner) {
			l.errorf("%s: NON-TAIL RESUME: clause owner %d occurs in %s", where, owner, slot)
			return false
		}
		return true
	}
	var tail func(Expr)
	var tree func(Tree)
	tail = func(x Expr) {
		switch x := x.(type) {
		case *ControlExit:
			for _, p := range x.Payload {
				noResume(p, "an exit payload")
			}
			return
		case *ResumeTail:
			if x.Owner != owner {
				l.errorf("%s: MISSING RESUME: clause owner %d ends in owner %d", where, owner, x.Owner)
				return
			}
			noResume(x.Value, "a resume argument")
			if x.NextState != nil {
				noResume(x.NextState, "a next-state expression")
			}
			if !EqualValueRepresentation(x.Value.Type(), arg) {
				l.errorf("%s: ResumeTail argument typed %s, want %s", where, types.Show(x.Value.Type()), types.Show(arg))
			}
			if !EqualValueRepresentation(x.ClauseResult, result) {
				l.errorf("%s: ResumeTail clause result typed %s, want %s", where, types.Show(x.ClauseResult), types.Show(result))
			}
			if state == nil && x.NextState != nil {
				l.errorf("%s: stateless clause owner %d supplies next state", where, owner)
			} else if state != nil && x.NextState == nil {
				l.errorf("%s: stateful clause owner %d omits next state", where, owner)
			} else if x.NextState != nil && !EqualValueRepresentation(x.NextState.Type(), state) {
				l.errorf("%s: next state typed %s, want %s", where, types.Show(x.NextState.Type()), types.Show(state))
			}
		case *Let:
			noResume(x.Rhs, "a Let right-hand side")
			if _, exits := x.Rhs.(*ControlExit); exits {
				return
			}
			tail(x.Body)
		case *Seq:
			noResume(x.First, "a Seq prefix")
			tail(x.Then)
		case *If:
			noResume(x.Cond, "an If condition")
			tail(x.Then)
			tail(x.Else)
		case *Case:
			noResume(x.Scrut, "a Case scrutinee")
			tree(x.Tree)
		default:
			if hasResumeOwner(x, owner) {
				l.errorf("%s: NON-TAIL RESUME: clause owner %d occurs beneath %T", where, owner, x)
			} else {
				l.errorf("%s: MISSING RESUME: clause owner %d has a normal path ending in %T", where, owner, x)
			}
		}
	}
	tree = func(t Tree) {
		switch t := t.(type) {
		case *Unreachable:
			return
		case *Leaf:
			tail(t.Body)
		case *Guard:
			noResume(t.Cond, "a decision-tree guard")
			tree(t.Then)
			tree(t.Else)
		case *SwitchCtor:
			for _, c := range t.Cases {
				tree(c.Tree)
			}
			if t.Default != nil {
				tree(t.Default)
			}
		case *SwitchLit:
			for _, c := range t.Cases {
				tree(c.Tree)
			}
			if t.Default != nil {
				tree(t.Default)
			}
		default:
			l.errorf("%s: unknown decision tree %T while checking resume owner %d", where, t, owner)
		}
	}
	tail(e)
}

func hasResumeOwner(e Expr, owner types.ResumeID) bool {
	found := false
	Rewrite(e, func(t types.Type) types.Type { return t }, func(x Expr) Expr {
		if r, ok := x.(*ResumeTail); ok && r.Owner == owner {
			found = true
		}
		return x
	})
	return found
}

func hasAnyResume(e Expr) bool {
	found := false
	Rewrite(e, func(t types.Type) types.Type { return t }, func(x Expr) Expr {
		if _, ok := x.(*ResumeTail); ok {
			found = true
		}
		return x
	})
	return found
}

func matchNativeType(pattern, actual types.Type, sub map[int]types.Type) bool {
	switch p := pattern.(type) {
	case *types.TVar:
		if old := sub[p.ID]; old != nil {
			return EqualValueRepresentation(old, actual)
		}
		sub[p.ID] = actual
		return true
	case *types.TCon:
		a, ok := actual.(*types.TCon)
		if !ok || p.Unique != a.Unique || len(p.Args) != len(a.Args) {
			return false
		}
		for i := range p.Args {
			if !matchNativeType(p.Args[i], a.Args[i], sub) {
				return false
			}
		}
		return true
	case *types.TFun:
		a, ok := actual.(*types.TFun)
		return ok && matchNativeType(p.Arg, a.Arg, sub) && matchNativeType(p.Eff, a.Eff, sub) && matchNativeType(p.Ret, a.Ret, sub)
	case types.Row:
		a, ok := actual.(types.Row)
		if !ok || len(p.Labels) != len(a.Labels) {
			return false
		}
		for i := range p.Labels {
			if p.Labels[i].Unique != a.Labels[i].Unique || len(p.Labels[i].Args) != len(a.Labels[i].Args) {
				return false
			}
			for j := range p.Labels[i].Args {
				if !matchNativeType(p.Labels[i].Args[j], a.Labels[i].Args[j], sub) {
					return false
				}
			}
		}
		if p.Tail == nil {
			return a.Tail == nil
		}
		return a.Tail != nil && matchNativeType(p.Tail, a.Tail, sub)
	default:
		return false
	}
}

// tree checks decision-tree invariants: tested variables are in scope, ctor
// cases belong to their ADT in strictly increasing declaration order,
// coverage and Default agree, and every leaf produces the Case's type.
func (l *linter) tree(t Tree, want types.Type, where string) {
	switch t := t.(type) {
	case *Unreachable:
	case *Guard:
		l.expr(t.Cond, where)
		if !EqualValueRepresentation(t.Cond.Type(), l.b.Bool) {
			l.errorf("%s: pattern guard must be Bool", where)
		}
		l.tree(t.Then, want, where)
		l.tree(t.Else, want, where)
	case *Leaf:
		if !EqualValueRepresentation(t.Body.Type(), want) {
			l.errorf("%s: case leaf typed %s, want %s",
				where, types.Show(t.Body.Type()), types.Show(want))
		}
		l.expr(t.Body, where)
	case *SwitchCtor:
		if !l.scope[t.Scrut] {
			l.errorf("%s: SwitchCtor tests `%s`, which is not in scope", where, t.Scrut)
		}
		if len(t.Cases) == 0 {
			l.errorf("%s: SwitchCtor with no cases", where)
		}
		prevIdx := -1
		for _, c := range t.Cases {
			if c.Ctor.Result.Unique != t.ADT.Con.Unique {
				l.errorf("%s: SwitchCtor case `%s` belongs to `%s`, not `%s`",
					where, c.Ctor.Name, c.Ctor.Result.Name, t.ADT.Con.Name)
			}
			if c.Ctor.Index <= prevIdx {
				l.errorf("%s: SwitchCtor cases out of declaration order at `%s`", where, c.Ctor.Name)
			}
			prevIdx = c.Ctor.Index
			if len(c.Binds) != len(c.Ctor.Fields) {
				l.errorf("%s: SwitchCtor case `%s` has %d binders, constructor has %d fields",
					where, c.Ctor.Name, len(c.Binds), len(c.Ctor.Fields))
				continue
			}
			var bound []string
			var fields []types.Type
			if scrut, ok := l.localTypes[t.Scrut].(*types.TCon); ok && scrut.Unique == t.ADT.Con.Unique && len(scrut.Args) == len(t.ADT.Params) {
				fields = l.runtimeInstFields(t.ADT, c.Ctor, scrut.Args)
			}
			for i, bind := range c.Binds {
				if bind == "" {
					continue
				}
				if l.scope[bind] {
					l.errorf("%s: field binder `%s` shadows", where, bind)
				}
				l.scope[bind] = true
				if i < len(fields) {
					l.localTypes[bind] = fields[i]
				}
				bound = append(bound, bind)
			}
			l.tree(c.Tree, want, where)
			for _, bind := range bound {
				delete(l.scope, bind)
				delete(l.localTypes, bind)
			}
		}
		covered := len(t.Cases) == len(t.ADT.Ctors)
		if covered && t.Default != nil {
			l.errorf("%s: SwitchCtor covers `%s` fully but still has a Default", where, t.ADT.Con.Name)
		}
		if !covered && t.Default == nil {
			l.errorf("%s: SwitchCtor covers `%s` partially and has no Default", where, t.ADT.Con.Name)
		}
		if t.Default != nil {
			l.tree(t.Default, want, where)
		}
	case *SwitchLit:
		if !l.scope[t.Scrut] {
			l.errorf("%s: SwitchLit tests `%s`, which is not in scope", where, t.Scrut)
		}
		if t.Default == nil {
			l.errorf("%s: SwitchLit without a Default — literals never exhaust a type", where)
		}
		for _, c := range t.Cases {
			switch c.Lit.(type) {
			case *IntLit, *FloatLit, *StringLit, *CharLit:
			default:
				l.errorf("%s: SwitchLit case is %T, want a literal", where, c.Lit)
			}
			l.tree(c.Tree, want, where)
		}
		if t.Default != nil {
			l.tree(t.Default, want, where)
		}
	default:
		l.errorf("%s: unhandled tree node %T", where, t)
	}
}

func (l *linter) effectInstance(e EffectInstance, where string) {
	l.control(e.Control, where)
	eff := l.effects[e.Unique]
	if eff == nil {
		l.errorf("%s: evidence names unknown effect `%s` (%d)", where, e.Name, e.Unique)
		return
	}
	if e.Name != eff.Name {
		l.errorf("%s: evidence name `%s` disagrees with effect `%s`", where, e.Name, eff.Name)
	}
	if len(e.Args) != len(eff.Params) {
		l.errorf("%s: evidence `%s` has %d type args, effect declares %d", where, e.Name, len(e.Args), len(eff.Params))
	}
	for _, a := range e.Args {
		l.typ(a, where)
	}
	for i, id := range e.Captures.Scopes {
		if id == 0 || i > 0 && e.Captures.Scopes[i-1] >= id {
			l.errorf("%s: malformed evidence scope captures", where)
		}
	}
	for i, id := range e.Captures.Vars {
		if id == 0 || i > 0 && e.Captures.Vars[i-1] >= id {
			l.errorf("%s: malformed evidence capture variables", where)
		}
	}
}

func (l *linter) bindEvidenceCaptures(e EffectInstance, where string) {
	if len(e.Captures.Scopes) != 0 || len(e.Captures.Vars) != 1 {
		l.errorf("%s: evidence parameter `%s` must bind one capture variable", where, e.Name)
	}
	for _, v := range e.Captures.Vars {
		if l.captureVars[v] {
			l.errorf("%s: duplicate capture variable %d", where, v)
		}
		l.captureVars[v] = true
	}
	l.evidenceCaptures[e.Unique] = append(l.evidenceCaptures[e.Unique], e.Captures)
}

func (l *linter) unbindEvidenceCaptures(e EffectInstance) {
	stack := l.evidenceCaptures[e.Unique]
	if len(stack) > 0 {
		l.evidenceCaptures[e.Unique] = stack[:len(stack)-1]
	}
	for _, v := range e.Captures.Vars {
		delete(l.captureVars, v)
	}
}

func (l *linter) evidenceAvailable(e EffectInstance, where string) {
	stack := l.evidenceCaptures[e.Unique]
	if len(stack) == 0 || !types.EqualCaptures(stack[len(stack)-1], e.Captures) {
		l.errorf("%s: evidence `%s` names an unavailable scope/capture", where, e.Name)
	}
}

func (l *linter) operationTypes(op *types.EffectOp, inst EffectInstance) ([]types.Type, types.Type) {
	m := make(map[int]types.Type, len(op.Owner.Params))
	for i, p := range op.Owner.Params {
		if i < len(inst.Args) {
			m[p.ID] = inst.Args[i]
		}
	}
	args := make([]types.Type, len(op.ParamTypes))
	for i, p := range op.ParamTypes {
		args[i] = types.SubstRigid(p, m)
	}
	return args, types.SubstRigid(op.ResultType, m)
}

func (l *linter) operationBelongs(op *types.EffectOp) bool {
	eff := l.effects[op.Owner.Unique]
	return eff != nil && op.Index >= 0 && op.Index < len(eff.Ops) && eff.Ops[op.Index] == op
}

func substEffectInstance(e EffectInstance, m map[int]types.Type) EffectInstance {
	out := EffectInstance{Unique: e.Unique, Name: e.Name, Args: make([]types.Type, len(e.Args)), Captures: e.Captures, Control: e.Control}
	for i, a := range e.Args {
		out.Args[i] = types.SubstRigid(a, m)
	}
	return out
}

func equalEffectInstance(a, b EffectInstance) bool {
	if a.Unique != b.Unique || a.Name != b.Name || len(a.Args) != len(b.Args) {
		return false
	}
	for i := range a.Args {
		if !EqualValueRepresentation(a.Args[i], b.Args[i]) {
			return false
		}
	}
	return a.Control == b.Control || b.Control.Polymorphic ||
		(a.Control.Resolve(types.Direct) == types.Direct && b.Control.Resolve(types.Exit) == types.Exit)
}

func effectArgsText(e EffectInstance) string {
	out := e.Name
	for _, arg := range e.Args {
		out += " " + types.Show(arg)
	}
	return out
}

func (l *linter) control(c types.Control, where string) {
	if !c.Valid() {
		l.errorf("%s: invalid control transport %d", where, c.Transport)
	}
	if c.Transport == types.Machine && !l.allowMachine {
		l.errorf("%s: Machine control survived before machine lowering is implemented", where)
	}
}

// controlInstance reports whether a call's control is an instance of its
// callee's contract. A polymorphic contract admits any actual whose lower
// bound is at least its own: a fixed Direct or Exit call, or a call that is
// itself polymorphic with a higher lower bound, as when an open-row caller
// also carries an abort label. The caller's own emission already resolves
// such a call in Exit context — a definition whose lower bound is Exit emits
// only its Exit member — so the callee's Exit member is always what runs.
func controlInstance(actual, contract types.Control) bool {
	if actual == contract {
		return true
	}
	return contract.Polymorphic && actual.Transport >= contract.Transport
}

func controlBodyFits(body, contract types.Control) bool {
	if body.Transport > contract.Transport && !contract.Polymorphic {
		return false
	}
	return true
}

// verifyControlANF checks the proof boundary consumed by Exit emission: a
// control-producing expression may occur only where generated statements can
// test its Outcome before evaluating the next source expression.
func (l *linter) verifyControlANF(e Expr, tail bool, where string) {
	if e == nil {
		return
	}
	directSlot := func(x Expr, slot string) {
		if c := ExprControl(x); c.Transport != types.Direct || c.Polymorphic {
			l.errorf("%s: control-producing %s was not ANF-hoisted", where, slot)
		}
		l.verifyControlANF(x, false, where)
	}
	switch e := e.(type) {
	case *Let:
		l.verifyControlANF(e.Rhs, true, where)
		l.verifyControlANF(e.Body, tail, where)
	case *Seq:
		l.verifyControlANF(e.First, true, where)
		l.verifyControlANF(e.Then, tail, where)
	case *If:
		directSlot(e.Cond, "If condition")
		l.verifyControlANF(e.Then, tail, where)
		l.verifyControlANF(e.Else, tail, where)
	case *Case:
		directSlot(e.Scrut, "Case scrutinee")
		l.verifyControlTree(e.Tree, tail, where)
	case *Neg:
		directSlot(e.Operand, "negation operand")
	case *NativeCall:
		for _, a := range e.Args {
			directSlot(a, "native argument")
		}
	case *FailureInspect:
		for _, a := range e.Args {
			directSlot(a, "failure inspection argument")
		}
	case *Quote:
		for _, h := range e.Holes {
			directSlot(h, "quote hole")
		}
	case *Perform:
		for _, a := range e.Args {
			directSlot(a, "operation argument")
		}
	case *ControlExit:
		for _, p := range e.Payload {
			directSlot(p, "exit payload")
		}
	case *Suspend:
		directSlot(e.Request, "suspension request")
	case *IteratorNext:
		directSlot(e.Cursor, "cursor advancement operand")
	case *App:
		if e.CalleeKind == Value {
			directSlot(e.Callee, "indirect callee")
		}
		for _, a := range e.Args {
			directSlot(a, "call argument")
		}
	case *ResumeTail:
		directSlot(e.Value, "resume value")
		if e.NextState != nil {
			directSlot(e.NextState, "next state")
		}
	case *Bracket:
		// Every slot is emitted as a statement whose Outcome the scope tests,
		// so all three may produce control.
		l.verifyControlANF(e.Acquire, true, where)
		l.verifyControlANF(e.Body, true, where)
		l.verifyControlANF(e.Release, true, where)
	case *IteratorScope:
		// The node owns invocation. Its two stored expressions only construct
		// callback values; their latent transports are represented by their
		// function types and by IteratorScope.Control.
		directSlot(e.Producer, "iterator producer")
		directSlot(e.Consumer, "iterator consumer")

	case *Handle:
		if e.State != nil {
			directSlot(e.State.Initial, "handler initial state")
		}
		l.verifyControlANF(e.Body, true, where)
		for _, c := range e.Clauses {
			l.verifyControlANF(c.Body, true, where)
		}
		if e.Return != nil {
			l.verifyControlANF(e.Return.Body, true, where)
		}
	case *Lambda:
		l.verifyControlANF(e.Body, true, where)
	default:
		if !tail {
			_ = tail
		}
	}
}

func (l *linter) verifyControlTree(t Tree, tail bool, where string) {
	switch t := t.(type) {
	case nil, *Unreachable:
	case *Leaf:
		l.verifyControlANF(t.Body, tail, where)
	case *Guard:
		if c := ExprControl(t.Cond); c.Transport != types.Direct || c.Polymorphic {
			l.errorf("%s: control-producing decision-tree guard was not ANF-hoisted", where)
		}
		l.verifyControlANF(t.Cond, false, where)
		l.verifyControlTree(t.Then, tail, where)
		l.verifyControlTree(t.Else, tail, where)
	case *SwitchCtor:
		for _, c := range t.Cases {
			l.verifyControlTree(c.Tree, tail, where)
		}
		l.verifyControlTree(t.Default, tail, where)
	case *SwitchLit:
		for _, c := range t.Cases {
			l.verifyControlTree(c.Tree, tail, where)
		}
		l.verifyControlTree(t.Default, tail, where)
	}
}

func (l *linter) typ(t types.Type, where string) {
	switch t := t.(type) {
	case *types.TVar:
		if !t.Rigid {
			l.errorf("%s: metavariable survived elaboration", where)
		} else if !l.tyParams[t.ID] {
			l.errorf("%s: rigid variable not declared by the definition's TyParams", where)
		}
	case *types.TCon:
		// Runtime inspection uses canonical nominal names, so they are proof
		// data as well as diagnostic text. A type occurrence cannot rename a
		// declaration while keeping its Unique (or claim a builtin's name).
		for _, builtin := range []*types.TCon{l.b.Int, l.b.Float, l.b.String, l.b.Char, l.b.Bool, l.b.Unit} {
			if (t.Unique == builtin.Unique || t.Name == builtin.Name) && (t.Unique != builtin.Unique || t.Name != builtin.Name || len(t.Args) != 0) {
				l.errorf("%s: stale nominal type identity for `%s`", where, t.Name)
			}
		}
		if adt := l.adts[t.Unique]; adt != nil && t.Name != adt.Con.Name {
			l.errorf("%s: nominal type name `%s` disagrees with declaration `%s`", where, t.Name, adt.Con.Name)
		}
		for _, a := range t.Args {
			l.typ(a, where)
		}
	case *types.TFun:
		if t.Eff.Tail != nil {
			l.errorf("%s: source effect row survived elaboration", where)
		}
		l.control(types.FunctionControl(t), where)
		for _, ev := range rowEvidence(t.Eff) {
			l.effectInstance(ev, where)
		}
		l.typ(t.Arg, where)
		l.typ(t.Ret, where)
	default:
		l.errorf("%s: unhandled type %T", where, t)
	}
}

func (l *linter) runtimeType(t types.Type) types.Type {
	switch t := t.(type) {
	case *types.TVar:
		if t.Kind == types.RowVar {
			return l.b.Unit
		}
		return t
	case *types.TCon:
		args := make([]types.Type, len(t.Args))
		adt := l.adts[t.Unique]
		for i, a := range t.Args {
			if adt != nil && i < len(adt.Params) && adt.Params[i].Kind == types.RowVar {
				args[i] = l.b.Unit
			} else {
				args[i] = l.runtimeType(a)
			}
		}
		return &types.TCon{Unique: t.Unique, Name: t.Name, Args: args}
	case *types.TFun:
		return &types.TFun{Arg: l.runtimeType(t.Arg), Eff: l.runtimeType(t.Eff).(types.Row), Ret: l.runtimeType(t.Ret), Control: t.Control, OpenRow: types.FunctionOpenRow(t)}
	case types.Row:
		labels := make([]types.EffLabel, len(t.Labels))
		for i, label := range t.Labels {
			args := make([]types.Type, len(label.Args))
			for j, a := range label.Args {
				args[j] = l.runtimeType(a)
			}
			labels[i] = types.EffLabel{Unique: label.Unique, Name: label.Name, Args: args, Abort: label.Abort, Suspension: label.Suspension}
		}
		return types.Row{Labels: labels}
	default:
		return t
	}
}

func (l *linter) runtimeInstFields(adt *types.ADTInfo, c *types.CtorInfo, args []types.Type) []types.Type {
	fields := adt.InstFields(c, args)
	for i := range fields {
		fields[i] = l.runtimeType(fields[i])
	}
	return fields
}

func rowEvidence(r types.Row) []EffectInstance {
	var out []EffectInstance
	for _, l := range types.SortedRow(r).Labels {
		if types.RuntimeEvidenceEffect(l) {
			out = append(out, EffectInstance{Unique: l.Unique, Name: l.Name, Args: append([]types.Type(nil), l.Args...), Control: types.Control{Polymorphic: true}})
		}
	}
	return out
}
