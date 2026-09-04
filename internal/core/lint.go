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
// retained on a nullary computation as its force-time evidence ABI. It runs in every test
// (and under a debug flag later) — instantiation plumbing bugs are the
// design's top risk, and this is the tripwire.
func Lint(p *Prog, b *types.Builtins) []error {
	l := &linter{b: b, scope: map[string]bool{}, workers: map[string]*Def{},
		adts: map[int]*types.ADTInfo{}, effects: map[int]*types.EffectInfo{},
		tyParams: map[int]bool{}, evidence: map[int]int{}}
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
		lastEffect := -1
		for _, ev := range d.EffectParams {
			l.effectInstance(ev, where)
			if ev.Unique <= lastEffect {
				l.errorf("%s: evidence parameters are not in increasing effect-Unique order", where)
			}
			lastEffect = ev.Unique
			l.evidence[ev.Unique]++
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
		for _, ev := range d.EffectParams {
			l.evidence[ev.Unique]--
		}
	}
	return l.errs
}

type linter struct {
	b         *types.Builtins
	scope     map[string]bool // def names + enclosing Let/param names: no shadowing
	workers   map[string]*Def
	adts      map[int]*types.ADTInfo // declared ADTs: equatable via derived eq
	effects   map[int]*types.EffectInfo
	tyParams  map[int]bool // the enclosing def's declared rigid vars
	evidence  map[int]int
	resumeArg types.Type
	resumeRet types.Type
	errs      []error
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

// numberVar reports whether t is a (declared) Number-kinded rigid variable —
// numeric operators compile natively on its Go type-set constraint (§7.3).
func (l *linter) numberVar(t types.Type) bool {
	v, ok := t.(*types.TVar)
	return ok && v.Rigid && v.Kind == types.Number
}

func (l *linter) numeric(t types.Type) bool {
	u := l.unique(t)
	return u == l.b.Int.Unique || u == l.b.Float.Unique || l.numberVar(t)
}

func (l *linter) orderable(t types.Type) bool {
	return l.numeric(t) || l.unique(t) == l.b.String.Unique
}

// equatable: scalars, Number rigid vars, and declared ADTs whose type
// arguments are themselves equatable. Functions and General rigid vars are
// not (§8.6 — the latter until typeclasses).
func (l *linter) equatable(t types.Type) bool {
	if l.orderable(t) || l.unique(t) == l.b.Bool.Unique {
		return true
	}
	if con, ok := t.(*types.TCon); ok {
		if _, isADT := l.adts[con.Unique]; isADT {
			for _, a := range con.Args {
				if !l.equatable(a) {
					return false
				}
			}
			return true
		}
	}
	return false
}

// printable mirrors elaborate.checkPrintable at the type level.
func (l *linter) printable(t types.Type) bool {
	switch l.unique(t) {
	case l.b.Int.Unique, l.b.Float.Unique, l.b.String.Unique, l.b.Bool.Unique:
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
		// param, the interpreter promotes (§9.5).
		if l.unique(e.Ty) != l.b.Int.Unique && !l.numberVar(e.Ty) {
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
		if _, isWorker := l.workers[e.Name]; isWorker {
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
	case *BinOp:
		l.binOp(e, where)
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
		if e.Param != "_" && l.scope[e.Param] {
			l.errorf("%s: Lambda param `%s` shadows — the checker should have rejected this", where, e.Param)
		}
		if e.Param != "_" {
			l.scope[e.Param] = true
		}
		for _, ev := range rowEvidence(fn.Eff) {
			l.effectInstance(ev, where)
			l.evidence[ev.Unique]++
		}
		l.expr(e.Body, where)
		for _, ev := range rowEvidence(fn.Eff) {
			l.evidence[ev.Unique]--
		}
		if e.Param != "_" {
			delete(l.scope, e.Param)
		}
	case *Perform:
		if e.Op == nil || e.Op.Owner.Unique != e.Effect.Unique {
			l.errorf("%s: malformed Perform evidence", where)
		} else if !l.operationBelongs(e.Op) {
			l.errorf("%s: Perform operation `%s` is not declared by its effect", where, e.Op.Name)
		}
		l.effectInstance(e.Effect, where)
		if e.Op != nil && !e.Op.Builtin && l.evidence[e.Effect.Unique] == 0 {
			l.errorf("%s: Perform `%s` has no lexical evidence", where, e.Op.Name)
		}
		if e.Op != nil && len(e.Args) != e.Op.Arity {
			l.errorf("%s: Perform `%s` arity mismatch", where, e.Op.Name)
		}
		if e.Op != nil && len(e.Args) == e.Op.Arity {
			wantArgs, wantResult := l.operationTypes(e.Op, e.Effect)
			isPrint := e.Op.Owner.Name == "IO" && e.Op.Name == "print"
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
	case *Resume:
		if l.resumeArg == nil || l.resumeRet == nil {
			l.errorf("%s: Resume outside a handler clause", where)
		} else {
			if !types.Equal(e.Value.Type(), l.resumeArg) {
				l.errorf("%s: Resume argument typed %s, want %s", where, types.Show(e.Value.Type()), types.Show(l.resumeArg))
			}
			if !types.Equal(e.Ty, l.resumeRet) {
				l.errorf("%s: Resume typed %s, want handler result %s", where, types.Show(e.Ty), types.Show(l.resumeRet))
			}
		}
		l.expr(e.Value, where)
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
		if !e.TailResumptive {
			l.errorf("%s: checkpoint-2 Handle is not tail resumptive", where)
		}
		l.effectInstance(e.Effect, where)
		l.evidence[e.Effect.Unique]++
		l.expr(e.Body, where)
		l.evidence[e.Effect.Unique]--
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
			if !types.Equal(c.ResultType, opResult) {
				l.errorf("%s: handler clause `%s` evidence result typed %s, want operation result %s", where, c.Op.Name, types.Show(c.ResultType), types.Show(opResult))
			}
			if !types.Equal(c.Body.Type(), e.Ty) {
				l.errorf("%s: handler clause `%s` body does not exactly match the handler type", where, c.Op.Name)
			}
			for _, p := range c.Params {
				if p != "_" && p != "()" {
					if l.scope[p] {
						l.errorf("%s: handler parameter `%s` shadows", where, p)
					}
					l.scope[p] = true
				}
			}
			oldArg, oldRet := l.resumeArg, l.resumeRet
			l.resumeArg, l.resumeRet = opResult, e.Ty
			l.expr(c.Body, where)
			l.resumeArg, l.resumeRet = oldArg, oldRet
			for _, p := range c.Params {
				delete(l.scope, p)
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
		}
	case *App:
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
			// explicit type arguments — the §8.4 invariant.
			calleeTy := def.Type
			if len(e.TyArgs) > 0 {
				m := make(map[int]types.Type, len(def.TyParams))
				for i, v := range def.TyParams {
					m[v.ID] = e.TyArgs[i]
				}
				calleeTy = types.SubstRigid(calleeTy, m)
			}
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
					l.errorf("%s: App{Worker} `%s` evidence arg %d disagrees with the callee", where, ref.Name, i+1)
				}
				if l.evidence[ev.Unique] == 0 {
					l.errorf("%s: App{Worker} `%s` passes unavailable lexical evidence `%s`", where, ref.Name, ev.Name)
				}
			}
			argTys, ret := PeelFun(calleeTy, len(def.Params))
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
			wantEvidence := rowEvidence(fn.Eff)
			if len(e.EvidenceArgs) != len(wantEvidence) {
				l.errorf("%s: App{Value} has %d evidence args, computation requires %d", where, len(e.EvidenceArgs), len(wantEvidence))
			}
			for i, ev := range e.EvidenceArgs {
				l.effectInstance(ev, where)
				if i < len(wantEvidence) && !equalEffectInstance(ev, wantEvidence[i]) {
					l.errorf("%s: App{Value} evidence arg %d disagrees with its computation type", where, i+1)
				}
				if l.evidence[ev.Unique] == 0 {
					l.errorf("%s: App{Value} passes unavailable lexical evidence `%s`", where, ev.Name)
				}
			}
			l.expr(e.Callee, where)
			l.expr(e.Args[0], where)
		case Ctor:
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
			fields := adt.InstFields(e.Ctor, result.Args)
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

// tree checks decision-tree invariants: tested variables are in scope, ctor
// cases belong to their ADT in strictly increasing declaration order,
// coverage and Default agree, and every leaf produces the Case's type.
func (l *linter) tree(t Tree, want types.Type, where string) {
	switch t := t.(type) {
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
			case *IntLit, *FloatLit, *StringLit:
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

func (l *linter) binOp(e *BinOp, where string) {
	lt, rt := e.L.Type(), e.R.Type()
	operandsAgree := types.Equal(lt, rt)

	switch e.Op {
	case "+", "-", "*":
		if !l.numeric(e.Ty) || !types.Equal(lt, e.Ty) || !types.Equal(rt, e.Ty) {
			l.errorf("%s: BinOp %s has non-numeric or mismatched types", where, e.Op)
		}
	case "/":
		if l.unique(e.Ty) != l.b.Float.Unique || !types.Equal(lt, e.Ty) || !types.Equal(rt, e.Ty) {
			l.errorf("%s: BinOp / must be Float throughout", where)
		}
	case "++":
		if l.unique(e.Ty) != l.b.String.Unique || !types.Equal(lt, e.Ty) || !types.Equal(rt, e.Ty) {
			l.errorf("%s: BinOp ++ must be String throughout", where)
		}
	case "==", "/=":
		if l.unique(e.Ty) != l.b.Bool.Unique || !operandsAgree || !l.equatable(lt) {
			l.errorf("%s: BinOp %s wants matching equatable operands and Bool result", where, e.Op)
		}
	case "<", ">", "<=", ">=":
		if l.unique(e.Ty) != l.b.Bool.Unique || !operandsAgree || !l.orderable(lt) {
			l.errorf("%s: BinOp %s wants matching orderable operands and Bool result", where, e.Op)
		}
	default:
		l.errorf("%s: unhandled operator %q", where, e.Op)
	}
	l.expr(e.L, where)
	l.expr(e.R, where)
}

func (l *linter) effectInstance(e EffectInstance, where string) {
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
	out := EffectInstance{Unique: e.Unique, Name: e.Name, Args: make([]types.Type, len(e.Args))}
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
	return true
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
		unit, isUnit := t.Arg.(*types.TCon)
		if !t.Eff.Empty() && (!isUnit || unit.Name != "()" || t.Eff.Tail != nil) {
			l.errorf("%s: source effect row survived elaboration", where)
		}
		for _, ev := range rowEvidence(t.Eff) {
			l.effectInstance(ev, where)
		}
		l.typ(t.Arg, where)
		l.typ(t.Ret, where)
	default:
		l.errorf("%s: unhandled type %T", where, t)
	}
}

func rowEvidence(r types.Row) []EffectInstance {
	var out []EffectInstance
	for _, l := range types.SortedRow(r).Labels {
		if l.Name != "IO" {
			out = append(out, EffectInstance{Unique: l.Unique, Name: l.Name, Args: append([]types.Type(nil), l.Args...)})
		}
	}
	return out
}
