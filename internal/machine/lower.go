package machine

import (
	"fmt"
	"sort"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// Lower selects the concrete Machine roots and every transport-polymorphic
// worker they call in Machine context, then lowers only that closed island.
// Direct and Exit definitions are absent from the result and remain on their
// existing backend path.
func Lower(p *core.Prog, b *types.Builtins) (*Prog, []error) {
	if errs := core.LintMachineInput(p, b); len(errs) != 0 {
		return nil, errs
	}
	defs := make(map[string]*core.Def, len(p.Defs))
	selected := map[string]bool{}
	for i := range p.Defs {
		d := &p.Defs[i]
		defs[d.Name] = d
		if d.Control.Transport == types.Machine {
			selected[d.Name] = true
		}
	}

	// A structured owner may hold a Machine callback inside an otherwise
	// Direct/Exit definition. Materialize those lambdas as frame factories
	// before closing the ordinary Machine-worker island.
	var rootedClosures []Closure
	var rootedAux []core.Def
	for i := range p.Defs {
		d := &p.Defs[i]
		if d.Control.Transport == types.Machine {
			continue
		}
		builder := &builder{def: d, locals: localRefTypes(d.Body), lambdas: map[*core.Lambda]bool{}, stateAux: map[string]bool{}}
		core.Inspect(d.Body, func(e core.Expr) {
			if lambda, ok := e.(*core.Lambda); ok {
				builder.registerMachineLambdas(lambda)
			}
		})
		rootedClosures = append(rootedClosures, builder.closures...)
		rootedAux = append(rootedAux, builder.aux...)
	}
	for i := range rootedAux {
		d := rootedAux[i]
		defs[d.Name] = &rootedAux[i]
		selected[d.Name] = true
	}

	// A polymorphic call resolves in its enclosing Machine context. Close the
	// selected set before lowering so every Call has a materialized callee.
	for changed := true; changed; {
		changed = false
		for name := range selected {
			d := defs[name]
			if d == nil {
				continue
			}
			core.Rewrite(d.Body, identityType, func(e core.Expr) core.Expr {
				app, ok := e.(*core.App)
				if !ok || app.CalleeKind != core.Worker || app.Control.Resolve(types.Machine) != types.Machine {
					return e
				}
				if ref, ok := app.Callee.(*core.VarRef); ok && defs[ref.Name] != nil && !selected[ref.Name] {
					selected[ref.Name] = true
					changed = true
				}
				return e
			})
		}
	}

	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	out := &Prog{Closures: rootedClosures}
	var errs []error
	stateWorkers := map[string]bool{}
	queue := append([]string(nil), names...)
	for len(queue) != 0 {
		name := queue[0]
		queue = queue[1:]
		w, aux, closures, stateAux, es := lowerWorker(defs[name], selected, stateWorkers[name])
		if len(es) != 0 {
			errs = append(errs, es...)
			continue
		}
		out.Workers = append(out.Workers, w)
		out.Closures = append(out.Closures, closures...)
		for i := range aux {
			d := aux[i]
			defs[d.Name] = &d
			selected[d.Name] = true
			stateWorkers[d.Name] = stateAux[d.Name]
			queue = append(queue, d.Name)
		}
	}
	if len(errs) == 0 {
		errs = append(errs, Lint(out)...)
	}
	return out, errs
}

func localRefTypes(e core.Expr) map[string]types.Type {
	out := map[string]types.Type{}
	core.Inspect(e, func(e core.Expr) {
		if ref, ok := e.(*core.VarRef); ok && ref.Local {
			out[ref.Name] = ref.Ty
		}
	})
	return out
}

func identityType(t types.Type) types.Type { return t }

type builder struct {
	def      *core.Def
	selected map[string]bool
	blocks   []Block
	locals   map[string]types.Type
	tmp      int
	errs     []error
	aux      []core.Def
	closures []Closure
	stateAux map[string]bool
	lambdas  map[*core.Lambda]bool
	lambdaN  int
}

func lowerWorker(d *core.Def, selected map[string]bool, stateToken bool) (Worker, []core.Def, []Closure, map[string]bool, []error) {
	b := &builder{def: d, selected: selected, locals: map[string]types.Type{}, lambdas: map[*core.Lambda]bool{}, stateAux: map[string]bool{}}
	argTys, result := core.PeelFun(d.Type, len(d.Params))
	params := make([]Local, len(d.Params))
	for i, name := range d.Params {
		name = b.localName(name)
		params[i] = Local{Name: name, Ty: argTys[i]}
		b.declare(params[i])
	}
	resultLocal := Local{Name: "_machine_result", Ty: result}
	b.declare(resultLocal)
	ret := b.add(&Return{Value: localRef(resultLocal)})
	entry := b.lowerInto(d.Body, resultLocal, ret)
	if len(b.errs) != 0 {
		return Worker{}, nil, nil, nil, b.errs
	}

	locals := make([]Local, 0, len(b.locals))
	for name, ty := range b.locals {
		locals = append(locals, Local{Name: name, Ty: ty})
	}
	sort.Slice(locals, func(i, j int) bool { return locals[i].Name < locals[j].Name })
	w := Worker{Name: d.Name, Owner: d.Owner, TyParams: d.TyParams, Params: params, EffectParams: d.EffectParams,
		Result: result, Entry: entry, Blocks: b.blocks, Locals: locals, Def: d, StateToken: stateToken}
	analyze(&w)
	return w, b.aux, b.closures, b.stateAux, nil
}

func (b *builder) lowerInto(e core.Expr, bind Local, next BlockID) BlockID {
	switch e := e.(type) {
	case *core.Let:
		name := b.localName(e.Name)
		local := Local{Name: name, Ty: e.Rhs.Type()}
		b.declare(local)
		body := b.lowerInto(e.Body, bind, next)
		return b.lowerInto(e.Rhs, local, body)
	case *core.Seq:
		body := b.lowerInto(e.Then, bind, next)
		discard := Local{Name: b.fresh("discard"), Ty: e.First.Type()}
		b.declare(discard)
		return b.lowerInto(e.First, discard, body)
	case *core.If:
		if machineControl(e.Cond) {
			b.errorf("%s: machine-producing If condition was not ANF-hoisted", b.def.Name)
			return next
		}
		thenBlock := b.lowerInto(e.Then, bind, next)
		elseBlock := b.lowerInto(e.Else, bind, next)
		return b.add(&Branch{Cond: e.Cond, Then: thenBlock, Else: elseBlock})
	case *core.Case:
		if machineControl(e.Scrut) {
			b.errorf("%s: machine-producing Case scrutinee was not ANF-hoisted", b.def.Name)
			return next
		}
		scrut := Local{Name: b.localName(e.Bind), Ty: e.Scrut.Type()}
		b.declare(scrut)
		tree := b.lowerTree(e.Tree, bind, next)
		return b.add(&Eval{Bind: scrut, Value: e.Scrut, Next: tree})
	case *core.Suspend:
		if machineControl(e.Request) {
			b.errorf("%s: suspension request itself requires Machine control", b.def.Name)
			return next
		}
		return b.add(&Suspend{Request: e.Request, Bind: bind, Next: next})
	case *core.ResumeTail:
		if e.NextState != nil {
			return b.add(&StateResume{Value: e.Value, NextState: e.NextState, Bind: bind, Next: next})
		}
		return b.lowerInto(e.Value, bind, next)
	case *core.Perform:
		if e.Control.Resolve(types.Machine) == types.Machine {
			for _, arg := range e.Args {
				if machineControl(arg) {
					b.errorf("%s: Machine operation argument was not ANF-hoisted", b.def.Name)
					return next
				}
			}
			return b.add(&Call{Operation: e.Op, Effect: e.Effect, Args: e.Args, Bind: bind, Next: next,
				Tail: b.isReturnOf(next, bind)})
		}
	case *core.App:
		if e.Control.Resolve(types.Machine) == types.Machine {
			ref, known := e.Callee.(*core.VarRef)
			if e.CalleeKind == core.Worker && (!known || !b.selected[ref.Name]) {
				b.errorf("%s: unresolved Machine worker call", b.def.Name)
				return next
			}
			if e.CalleeKind != core.Worker && e.CalleeKind != core.Value {
				b.errorf("%s: unsupported Machine call target", b.def.Name)
				return next
			}
			if e.CalleeKind == core.Value && machineControl(e.Callee) {
				b.errorf("%s: Machine indirect callee was not ANF-hoisted", b.def.Name)
				return next
			}
			for _, arg := range e.Args {
				if machineControl(arg) {
					b.errorf("%s: Machine call argument was not ANF-hoisted", b.def.Name)
					return next
				}
			}
			call := &Call{TyArgs: e.TyArgs, Args: e.Args, EvidenceArgs: e.EvidenceArgs, Bind: bind, Next: next}
			if e.CalleeKind == core.Worker {
				call.Callee = ref.Name
			} else {
				call.CalleeExpr = e.Callee
				b.registerMachineLambdas(e.Callee)
			}
			call.Tail = b.isReturnOf(next, bind)
			return b.add(call)
		}
	case *core.Bracket:
		if machineControl(e.Acquire) {
			b.errorf("%s: suspending cleanup acquisition is not implemented", b.def.Name)
			return next
		}
		if machineControl(e.Release) {
			b.errorf("%s: cleanup release may not suspend", b.def.Name)
			return next
		}
		resource := Local{Name: e.Resource, Ty: e.ResourceTy}
		b.declare(resource)
		pop := b.add(&PopCleanup{Next: next})
		body := b.lowerInto(e.Body, bind, pop)
		return b.add(&PushCleanup{Acquire: e.Acquire, Resource: resource, Release: e.Release, Next: body})
	case *core.Handle:
		return b.lowerHandle(e, bind, next)
	}
	b.registerMachineLambdas(e)
	if machineControl(e) {
		b.errorf("%s: machine lowering does not yet support %T", b.def.Name, e)
		return next
	}
	return b.add(&Eval{Bind: bind, Value: e, Next: next})
}

func (b *builder) lowerHandle(h *core.Handle, bind Local, next BlockID) BlockID {
	if len(h.Clauses) == 0 {
		b.errorf("%s: machine handler has no clauses", b.def.Name)
		return next
	}
	abort := h.Clauses[0].Op.Abort
	outer := append([]core.EffectInstance(nil), b.def.EffectParams...)
	inner := make([]core.EffectInstance, 0, len(outer)+1)
	for _, ev := range outer {
		if ev.Unique != h.Effect.Unique {
			inner = append(inner, ev)
		}
	}
	handled := h.Effect
	handled.Control = types.Control{Transport: types.Machine}
	inner = append(inner, handled)

	bodyCaptures := b.regionCaptures(h.Body)
	bodyWorker := b.addRegion("handle_body", bodyCaptures, nil, nil, h.Body.Type(), inner, h.Body, false)
	handlerBind, handlerNext := bind, next
	var stateResult Local
	if h.State != nil {
		stateResult = Local{Name: h.State.Name, Ty: h.State.Ty}
		b.declare(stateResult)
	}
	if h.Return != nil {
		handlerBind = Local{Name: b.fresh("handled"), Ty: h.Body.Type()}
		b.declare(handlerBind)
		transformed := h.Return.Body
		if h.Return.Param != "_" && h.Return.Param != "()" {
			transformed = &core.Let{Name: h.Return.Param, Rhs: localRef(handlerBind), Body: transformed, Ty: h.Ty}
		}
		handlerNext = b.lowerInto(transformed, bind, next)
	}
	term := &Handle{Node: h, BodyWorker: bodyWorker, BodyCaptures: bodyCaptures, Bind: handlerBind, Next: handlerNext,
		Abort: abort, AbortNext: next, AbortBind: bind,
		State: h.State, StateResult: stateResult}
	for _, clause := range h.Clauses {
		captures := b.regionCaptures(clause.Body)
		body := core.Rewrite(clause.Body, identityType, func(e core.Expr) core.Expr {
			if resume, ok := e.(*core.ResumeTail); ok && resume.Owner == clause.ResumeID && h.State == nil && !abort {
				return resume.Value
			}
			return e
		})
		paramNames := append([]string(nil), clause.Params...)
		paramTypes := append([]types.Type(nil), clause.ParamTypes...)
		stateName := ""
		var stateTy types.Type
		if h.State != nil {
			stateName, stateTy = h.State.Name, h.State.Ty
			paramNames = append([]string{stateName}, paramNames...)
			paramTypes = append([]types.Type{stateTy}, paramTypes...)
		}
		result := clause.ResultType
		if abort {
			result = h.Ty
		}
		worker := b.addRegion("handle_clause", captures, paramNames, paramTypes, result, outer, body, h.State != nil && !abort)
		term.Clauses = append(term.Clauses, HandlerClause{Op: clause.Op, Worker: worker, Captures: captures,
			StateName: stateName, StateTy: stateTy})
	}
	return b.add(term)
}

func (b *builder) regionCaptures(body core.Expr) []Local {
	free := freeLocalRefs(body)
	var captures []Local
	for name, ty := range b.locals {
		if free[name] {
			captures = append(captures, Local{Name: name, Ty: ty})
		}
	}
	sort.Slice(captures, func(i, j int) bool { return captures[i].Name < captures[j].Name })
	return captures
}

func (b *builder) addRegion(kind string, captures []Local, names []string, tys []types.Type, result types.Type, effects []core.EffectInstance, body core.Expr, stateToken bool) string {
	b.lambdaN++
	name := fmt.Sprintf("%s_machine_%s%d", b.def.Name, kind, b.lambdaN)
	params := make([]string, 0, len(captures)+len(names))
	paramTypes := make([]types.Type, 0, len(captures)+len(tys))
	for _, capture := range captures {
		params = append(params, capture.Name)
		paramTypes = append(paramTypes, capture.Ty)
	}
	for i, param := range names {
		if param == "()" || param == "_" {
			param = fmt.Sprintf("_machine_arg%d", i)
		}
		params = append(params, param)
		paramTypes = append(paramTypes, tys[i])
	}
	ty := result
	for i := len(paramTypes) - 1; i >= 0; i-- {
		ty = &types.TFun{Arg: paramTypes[i], Ret: ty}
	}
	b.aux = append(b.aux, core.Def{Name: name, Owner: b.def.Owner, Type: ty, TyParams: b.def.TyParams,
		Params: params, EffectParams: append([]core.EffectInstance(nil), effects...),
		Control: types.Control{Transport: types.Machine}, Body: body})
	b.stateAux[name] = stateToken
	return name
}

func (b *builder) registerMachineLambdas(e core.Expr) {
	lam, ok := e.(*core.Lambda)
	if !ok || b.lambdas[lam] {
		return
	}
	fn, ok := lam.Ty.(*types.TFun)
	if !ok || types.FunctionControl(fn).Resolve(types.Machine) != types.Machine {
		return
	}
	b.lambdas[lam] = true
	b.lambdaN++
	name := fmt.Sprintf("%s_machine_lambda%d", b.def.Name, b.lambdaN)
	free := freeLocalRefs(lam.Body)
	delete(free, lam.Param)
	var captures []Local
	for local, ty := range b.locals {
		if free[local] {
			captures = append(captures, Local{Name: local, Ty: ty})
		}
	}
	sort.Slice(captures, func(i, j int) bool { return captures[i].Name < captures[j].Name })
	callEvidence := append([]core.EffectInstance(nil), lam.EffectParams...)
	callEffects := map[int]bool{}
	for _, ev := range callEvidence {
		callEffects[ev.Unique] = true
	}
	var capturedEvidence []core.EffectInstance
	for _, ev := range b.def.EffectParams {
		if !callEffects[ev.Unique] {
			capturedEvidence = append(capturedEvidence, ev)
		}
	}
	params := make([]string, 0, len(captures)+1)
	ty := lam.Ty
	for i := len(captures) - 1; i >= 0; i-- {
		ty = &types.TFun{Arg: captures[i].Ty, Ret: ty}
	}
	for _, capture := range captures {
		params = append(params, capture.Name)
	}
	params = append(params, lam.Param)
	effects := append(append([]core.EffectInstance(nil), capturedEvidence...), callEvidence...)
	aux := core.Def{Name: name, Owner: b.def.Owner, Type: ty, TyParams: b.def.TyParams,
		Params: params, EffectParams: effects, Control: types.Control{Transport: types.Machine}, Body: lam.Body}
	b.aux = append(b.aux, aux)
	b.closures = append(b.closures, Closure{Expr: lam, Worker: name, Captures: captures,
		CapturedEvidence: capturedEvidence, CallEvidence: callEvidence})
}

func (b *builder) lowerTree(tree core.Tree, bind Local, next BlockID) BlockID {
	switch tree := tree.(type) {
	case *core.Leaf:
		return b.lowerInto(tree.Body, bind, next)
	case *core.Guard:
		if machineControl(tree.Cond) {
			b.errorf("%s: machine-producing decision-tree guard was not ANF-hoisted", b.def.Name)
			return next
		}
		thenBlock := b.lowerTree(tree.Then, bind, next)
		elseBlock := b.lowerTree(tree.Else, bind, next)
		return b.add(&Branch{Cond: tree.Cond, Then: thenBlock, Else: elseBlock})
	case *core.SwitchCtor:
		scrutTy, ok := b.locals[tree.Scrut].(*types.TCon)
		if !ok {
			b.errorf("%s: constructor switch scrutinee %q has no nominal local type", b.def.Name, tree.Scrut)
			return next
		}
		term := &SwitchCtor{Scrut: tree.Scrut, ADT: tree.ADT}
		for _, c := range tree.Cases {
			fields := tree.ADT.InstFields(c.Ctor, scrutTy.Args)
			binds := make([]Local, len(c.Binds))
			for i, name := range c.Binds {
				if name == "" {
					binds[i] = Local{Ty: fields[i]}
					continue
				}
				binds[i] = Local{Name: name, Ty: fields[i]}
				b.declare(binds[i])
			}
			term.Cases = append(term.Cases, CtorCase{Ctor: c.Ctor, Binds: binds, Next: b.lowerTree(c.Tree, bind, next)})
		}
		if tree.Default != nil {
			id := b.lowerTree(tree.Default, bind, next)
			term.Default = &id
		}
		return b.add(term)
	case *core.SwitchLit:
		term := &SwitchLit{Scrut: tree.Scrut, Default: b.lowerTree(tree.Default, bind, next)}
		for _, c := range tree.Cases {
			term.Cases = append(term.Cases, LitCase{Lit: c.Lit, Next: b.lowerTree(c.Tree, bind, next)})
		}
		return b.add(term)
	case *core.Unreachable:
		b.errorf("%s: reachable decision-tree Unreachable cannot enter Machine IR", b.def.Name)
		return next
	default:
		b.errorf("%s: unknown decision tree %T", b.def.Name, tree)
		return next
	}
}

func machineControl(e core.Expr) bool {
	return core.ExprControl(e).Resolve(types.Machine) == types.Machine
}

func (b *builder) add(term Term) BlockID {
	id := BlockID(len(b.blocks))
	b.blocks = append(b.blocks, Block{ID: id, Term: term})
	return id
}

func (b *builder) declare(local Local) {
	if old, ok := b.locals[local.Name]; ok {
		if !types.Equal(old, local.Ty) {
			b.errorf("%s: local %q has inconsistent types %s and %s", b.def.Name, local.Name, types.Show(old), types.Show(local.Ty))
		}
		return
	}
	b.locals[local.Name] = local.Ty
}

func (b *builder) localName(name string) string {
	if name == "_" || name == "()" || name == "" {
		return b.fresh("discard")
	}
	return name
}

func (b *builder) fresh(prefix string) string {
	name := fmt.Sprintf("_machine_%s%d", prefix, b.tmp)
	b.tmp++
	return name
}

func (b *builder) errorf(format string, args ...any) {
	b.errs = append(b.errs, fmt.Errorf(format, args...))
}

func (b *builder) isReturnOf(id BlockID, local Local) bool {
	if int(id) < 0 || int(id) >= len(b.blocks) {
		return false
	}
	ret, ok := b.blocks[id].Term.(*Return)
	if !ok {
		return false
	}
	ref, ok := ret.Value.(*core.VarRef)
	return ok && ref.Local && ref.Name == local.Name
}

func localRef(local Local) core.Expr {
	return &core.VarRef{Name: local.Name, Local: true, Ty: local.Ty}
}
