package core

import (
	"fmt"

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
	l := &linter{b: b, scope: map[string]bool{}, workers: map[string]*Def{},
		adts: map[int]*types.ADTInfo{}, effects: map[int]*types.EffectInfo{},
		tyParams: map[int]bool{}, evidence: map[int]int{}, evidenceCaptures: map[int][]types.CaptureSet{},
		captureVars: map[types.CaptureVar]bool{}, scopeIDs: map[types.ScopeID]bool{}, activeScopes: map[types.ScopeID]bool{},
		resumeIDs: map[types.ResumeID]bool{}, natives: p.Natives}
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
	for i := range p.Defs {
		d := &p.Defs[i]
		where := "def " + d.Name
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
				}
				t = fn.Ret
			}
			if !types.Equal(t, d.Body.Type()) {
				l.errorf("%s: body type %s differs from peeled result %s",
					where, types.Show(d.Body.Type()), types.Show(t))
			}
			l.expr(d.Body, where)
			for _, param := range d.Params {
				if param != "_" {
					delete(l.scope, param)
				}
			}
		} else {
			if !types.Equal(d.Type, d.Body.Type()) {
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
		for _, v := range d.ParamCaptures {
			delete(l.captureVars, v)
		}
	}
	if p.EntryDisplay != nil {
		l.tyParams = map[int]bool{}
		l.expr(p.EntryDisplay, "entry display")
		if !types.Equal(p.EntryDisplay.Type(), b.String) {
			l.errorf("entry display must return String")
		}
	}
	l.errs = append(l.errs, verifyCaptures(p, b)...)
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

type linter struct {
	b                *types.Builtins
	scope            map[string]bool // def names + enclosing Let/param names: no shadowing
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
	resumeOwner      types.ResumeID
	resumeIDs        map[types.ResumeID]bool
	resumeArg        types.Type
	resumeRet        types.Type
	resumeState      types.Type
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
		if !types.Equal(e.Operand.Type(), e.Ty) {
			l.errorf("%s: Neg operand type differs from result", where)
		}
		l.expr(e.Operand, where)
	case *Quote:
		// Compile-time-only definitions are dropped before the program is
		// linted, so a surviving Quote means the emission rule let one
		// through (doc/design.md, "Compile-time metaprogramming").
		l.errorf("%s: quote in emitted code — a compile-time-only value escaped", where)
	case *TypeOf:
		l.errorf("%s: typeOf in emitted code — a compile-time-only value escaped", where)
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
		if !types.Equal(e.Then.Type(), e.Ty) || !types.Equal(e.Else.Type(), e.Ty) {
			l.errorf("%s: If branches disagree with result type", where)
		}
		l.expr(e.Cond, where)
		l.expr(e.Then, where)
		l.expr(e.Else, where)
	case *Let:
		if !types.Equal(e.Ty, e.Body.Type()) {
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
			l.expr(e.Rhs, where)
		} else {
			l.expr(e.Rhs, where)
			l.scope[e.Name] = true
		}
		l.expr(e.Body, where)
		delete(l.scope, e.Name)
	case *Lambda:
		fn, ok := e.Ty.(*types.TFun)
		if !ok {
			l.errorf("%s: Lambda typed %s, want a function type", where, types.Show(e.Ty))
			return
		}
		if !types.Equal(fn.Ret, e.Body.Type()) {
			l.errorf("%s: Lambda body type %s differs from arrow result %s",
				where, types.Show(e.Body.Type()), types.Show(fn.Ret))
		}
		l.control(types.FunctionControl(fn), where)
		if e.Param != "_" && l.scope[e.Param] {
			l.errorf("%s: Lambda param `%s` shadows — the checker should have rejected this", where, e.Param)
		}
		if e.Param != "_" {
			l.scope[e.Param] = true
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
		l.expr(e.Body, where)
		if bodyControl := ExprControl(e.Body); !controlBodyFits(bodyControl, types.FunctionControl(fn)) {
			l.errorf("%s: Lambda body control %s is not representable by arrow %s", where, ControlName(bodyControl), ControlName(types.FunctionControl(fn)))
		}
		for _, ev := range e.EffectParams {
			l.evidence[ev.Unique]--
			l.unbindEvidenceCaptures(ev)
		}
		delete(l.captureVars, e.ParamCapture)
		if e.Param != "_" {
			delete(l.scope, e.Param)
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
				if !isPrint && i < len(wantArgs) && !types.Equal(a.Type(), wantArgs[i]) {
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
			if wantResult != nil && !types.Equal(e.Ty, wantResult) {
				l.errorf("%s: Perform `%s` typed %s, want %s", where, e.Op.Name, types.Show(e.Ty), types.Show(wantResult))
			}
		}
		for _, a := range e.Args {
			l.expr(a, where)
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
				if i < len(want) && !types.Equal(p.Type(), want[i]) {
					l.errorf("%s: ControlExit `%s` payload %d typed %s, want %s", where, e.Op.Name, i+1, types.Show(p.Type()), types.Show(want[i]))
				}
				l.expr(p, where)
			}
		}
	case *ResumeTail:
		if l.resumeOwner == 0 || e.Owner != l.resumeOwner || l.resumeArg == nil || l.resumeRet == nil {
			l.errorf("%s: ResumeTail owner %d is outside its handler clause", where, e.Owner)
		} else {
			if !types.Equal(e.Value.Type(), l.resumeArg) {
				l.errorf("%s: Resume argument typed %s, want %s", where, types.Show(e.Value.Type()), types.Show(l.resumeArg))
			}
			if !types.Equal(e.ClauseResult, l.resumeRet) {
				l.errorf("%s: ResumeTail clause result typed %s, want handler result %s", where, types.Show(e.ClauseResult), types.Show(l.resumeRet))
			}
		}
		l.expr(e.Value, where)
		if l.resumeState == nil && e.NextState != nil {
			l.errorf("%s: stateless ResumeTail has a next state", where)
		} else if l.resumeState != nil && e.NextState == nil {
			l.errorf("%s: stateful ResumeTail has no next state", where)
		} else if e.NextState != nil {
			if !types.Equal(e.NextState.Type(), l.resumeState) {
				l.errorf("%s: next handler state typed %s, want %s", where, types.Show(e.NextState.Type()), types.Show(l.resumeState))
			}
			l.expr(e.NextState, where)
		}
	case *Seq:
		if l.unique(e.First.Type()) != l.b.Unit.Unique {
			l.errorf("%s: Seq first expression is not Unit", where)
		}
		if !types.Equal(e.Ty, e.Then.Type()) {
			l.errorf("%s: Seq type differs from its final expression", where)
		}
		l.expr(e.First, where)
		l.expr(e.Then, where)
	case *Handle:
		l.control(e.Control, where)
		l.effectInstance(e.Effect, where)
		if e.State != nil {
			if e.State.Name == "" || e.State.Ty == nil || e.State.Initial == nil {
				l.errorf("%s: parameterized handler has incomplete state metadata", where)
			} else {
				if !types.Equal(e.State.Initial.Type(), e.State.Ty) {
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
				if i < len(wantParams) && !types.Equal(pt, wantParams[i]) {
					l.errorf("%s: handler clause `%s` param %d typed %s, want %s", where, c.Op.Name, i+1, types.Show(pt), types.Show(wantParams[i]))
				}
			}
			for i, p := range c.Params {
				if p == "()" && i < len(c.ParamTypes) && l.unique(c.ParamTypes[i]) != l.b.Unit.Unique {
					l.errorf("%s: handler clause `%s` uses () for a non-Unit parameter", where, c.Op.Name)
				}
			}
			if !c.Op.Abort && !types.Equal(c.ResultType, opResult) {
				l.errorf("%s: handler clause `%s` evidence result typed %s, want operation result %s", where, c.Op.Name, types.Show(c.ResultType), types.Show(opResult))
			}
			if !types.Equal(c.Body.Type(), e.Ty) {
				l.errorf("%s: handler clause `%s` body does not exactly match the handler type", where, c.Op.Name)
			}
			if e.State != nil {
				if l.scope[e.State.Name] {
					l.errorf("%s: handler state `%s` shadows", where, e.State.Name)
				}
				l.scope[e.State.Name] = true
			}
			for _, p := range c.Params {
				if p != "_" && p != "()" {
					if l.scope[p] {
						l.errorf("%s: handler parameter `%s` shadows", where, p)
					}
					l.scope[p] = true
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
			l.resumeOwner, l.resumeArg, l.resumeRet, l.resumeState = oldOwner, oldArg, oldRet, oldState
			for _, p := range c.Params {
				delete(l.scope, p)
			}
			if e.State != nil {
				delete(l.scope, e.State.Name)
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
			}
			if e.Return.Param == "()" && l.unique(e.Body.Type()) != l.b.Unit.Unique {
				l.errorf("%s: handler return clause uses () for a non-Unit result", where)
			}
			if e.Return.Param != "_" && e.Return.Param != "()" {
				l.scope[e.Return.Param] = true
			}
			l.expr(e.Return.Body, where)
			if !types.Equal(e.Return.Body.Type(), e.Ty) {
				l.errorf("%s: handler return clause does not exactly match the handler type", where)
			}
			delete(l.scope, e.Return.Param)
			if e.State != nil {
				delete(l.scope, e.State.Name)
			}
		}
	case *App:
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
				if !types.Equal(a.Type(), argTys[i]) {
					l.errorf("%s: App{Worker} `%s` arg %d typed %s, want %s",
						where, ref.Name, i+1, types.Show(a.Type()), types.Show(argTys[i]))
				}
				l.expr(a, where)
			}
			if !types.Equal(e.Ty, ret) {
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
			if !types.Equal(e.Args[0].Type(), fn.Arg) {
				l.errorf("%s: App{Value} arg typed %s, want %s",
					where, types.Show(e.Args[0].Type()), types.Show(fn.Arg))
			}
			if !types.Equal(e.Ty, fn.Ret) {
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
				if !types.Equal(ta, result.Args[i]) {
					l.errorf("%s: App{Ctor} `%s` type arg %d disagrees with its result type", where, e.Ctor.Name, i+1)
				}
			}
			fields := l.runtimeInstFields(adt, e.Ctor, result.Args)
			for i, a := range e.Args {
				if !types.Equal(a.Type(), fields[i]) {
					l.errorf("%s: App{Ctor} `%s` arg %d typed %s, want %s",
						where, e.Ctor.Name, i+1, types.Show(a.Type()), types.Show(fields[i]))
				}
				l.expr(a, where)
			}
		default:
			l.errorf("%s: App with unknown CalleeKind %d", where, e.CalleeKind)
		}
	case *Case:
		if e.Bind == "" {
			l.errorf("%s: Case without a scrutinee binder", where)
		}
		if l.scope[e.Bind] {
			l.errorf("%s: Case binder `%s` shadows", where, e.Bind)
		}
		l.expr(e.Scrut, where)
		l.scope[e.Bind] = true
		l.tree(e.Tree, e.Ty, where)
		delete(l.scope, e.Bind)
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
			if !types.Equal(x.Value.Type(), arg) {
				l.errorf("%s: ResumeTail argument typed %s, want %s", where, types.Show(x.Value.Type()), types.Show(arg))
			}
			if !types.Equal(x.ClauseResult, result) {
				l.errorf("%s: ResumeTail clause result typed %s, want %s", where, types.Show(x.ClauseResult), types.Show(result))
			}
			if state == nil && x.NextState != nil {
				l.errorf("%s: stateless clause owner %d supplies next state", where, owner)
			} else if state != nil && x.NextState == nil {
				l.errorf("%s: stateful clause owner %d omits next state", where, owner)
			} else if x.NextState != nil && !types.Equal(x.NextState.Type(), state) {
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
			return types.Equal(old, actual)
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
		if !types.Equal(t.Cond.Type(), l.b.Bool) {
			l.errorf("%s: pattern guard must be Bool", where)
		}
		l.tree(t.Then, want, where)
		l.tree(t.Else, want, where)
	case *Leaf:
		if !types.Equal(t.Body.Type(), want) {
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
			for _, bind := range c.Binds {
				if bind == "" {
					continue
				}
				if l.scope[bind] {
					l.errorf("%s: field binder `%s` shadows", where, bind)
				}
				l.scope[bind] = true
				bound = append(bound, bind)
			}
			l.tree(c.Tree, want, where)
			for _, bind := range bound {
				delete(l.scope, bind)
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
		if !types.Equal(a.Args[i], b.Args[i]) {
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
	if c.Transport == types.Machine {
		l.errorf("%s: Machine control survived before machine lowering is implemented", where)
	}
}

func controlInstance(actual, contract types.Control) bool {
	if actual == contract {
		return true
	}
	return contract.Polymorphic && actual.Transport >= contract.Transport && !actual.Polymorphic
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
		return &types.TFun{Arg: l.runtimeType(t.Arg), Eff: l.runtimeType(t.Eff).(types.Row), Ret: l.runtimeType(t.Ret), Control: t.Control}
	case types.Row:
		labels := make([]types.EffLabel, len(t.Labels))
		for i, label := range t.Labels {
			args := make([]types.Type, len(label.Args))
			for j, a := range label.Args {
				args[j] = l.runtimeType(a)
			}
			labels[i] = types.EffLabel{Unique: label.Unique, Name: label.Name, Args: args, Abort: label.Abort}
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
		if types.SurfaceName(l.Name) != "IO" {
			out = append(out, EffectInstance{Unique: l.Unique, Name: l.Name, Args: append([]types.Type(nil), l.Args...), Control: types.Control{Polymorphic: true}})
		}
	}
	return out
}
