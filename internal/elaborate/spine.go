package elaborate

import (
	"fmt"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// Spine collapsing and saturation analysis (doc/design.md, "Go backend and runtime", doc/design.md, "Core and evidence invariants").
// Saturation is resolved exactly once, here. Core after elaboration
// contains only:
//
//   - App{Worker}: exactly arity args, callee a VarRef naming a worker
//   - App{Value}:  exactly one arg per application (typed indirect call)
//   - Lambda:      one currying step
//
// Both backends consume the same pre-chewed spines; the linter enforces
// the shape (including banning bare worker references, which proves every
// first-class use was eta-expanded).

// app elaborates the outermost application of a spine.
func (el *elab) app(e *ast.App) core.Expr {
	// Flatten the curried spine to (head, args…).
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
	head := cur
	args := make([]ast.Expr, len(rev))
	for i, a := range rev {
		args[len(rev)-1-i] = a
	}
	if v, ok := head.(*ast.Var); ok {
		if _, local := el.scopeIdx[v.Name]; local {
			res := el.expr(head)
			raw := el.apply(el.ck.ExprTypes[head])
			for _, a := range args {
				res = el.valueAppWithRow(res, el.expr(a), raw)
				raw = raw.(*types.TFun).Ret
			}
			return res
		}
	}
	if el.ck.ResumeCalls[e] {
		r := head.(*ast.Resume)
		var next core.Expr
		if r.NextState != nil {
			next = el.expr(r.NextState)
		}
		return &core.ResumeTail{Owner: el.ck.ResumeOwners[r], Value: el.expr(args[0]), NextState: next, ClauseResult: el.zonkDefault(el.ck.ExprTypes[e])}
	}
	if op := el.ck.OpCalls[e]; op != nil {
		return el.operationCall(op, el.zonkDefault(el.ck.ExprTypes[head]), el.apply(el.ck.ExprTypes[head]), args)
	}
	if v, ok := head.(*ast.Var); ok {
		if method := el.ck.Methods[v.Name]; method != nil {
			raw := el.apply(el.ck.ExprTypes[head])
			ta := matchTyArgs(method.Type, []*types.TVar{method.Class.Param}, raw)
			pred := types.Pred{Class: method.Class.Name, Ty: ta[0]}
			if in, _, _ := el.matchInstance(pred); el.givenDictionary(pred) == nil && in != nil {
				if in.IdentityMethods[method.Index] && len(args) == 1 {
					return el.expr(args[0])
				}
				if name := in.NativeMethods[method.Index]; name != "" {
					return el.nativeApply(el.ck.Natives[name], el.zonkDefault(raw), args)
				}
				name := in.Methods[method.Index]
				if arity, ok := el.ck.Workers[name]; ok {
					return el.workerCall(name, el.zonkDefault(raw), raw, arity, args)
				}
			}
		}
		if n := el.ck.Natives[v.Name]; n != nil && n.Effect == nil {
			return el.nativeApply(n, el.zonkDefault(el.ck.ExprTypes[head]), args)
		}
	}

	// Lifted local head? Its frees become leading arguments (lift.go).
	if v, ok := head.(*ast.Var); ok {
		if lf := el.lifted[v.Name]; lf != nil {
			occTy := el.zonkDefault(el.ck.ExprTypes[head])
			return el.calleeCall(el.liftedCallee(lf, occTy, el.apply(el.ck.ExprTypes[head])), args)
		}
	}

	// Known worker head?
	if v, ok := head.(*ast.Var); ok {
		if arity, isWorker := el.ck.Workers[v.Name]; isWorker {
			workerTy := el.zonkDefault(el.ck.ExprTypes[head])
			return el.workerCall(v.Name, workerTy, el.apply(el.ck.ExprTypes[head]), arity, args)
		}
	}

	// Constructor head? Same saturation discipline as workers (doc/design.md, "Go backend and runtime" item 5);
	// True/False fall through (they are nullary BoolLits and a type-correct
	// program never applies them).
	if c, ok := head.(*ast.Ctor); ok {
		if info, isCtor := el.ck.Ctors[c.Name]; isCtor && len(info.Fields) > 0 {
			occTy := el.zonkDefault(el.ck.ExprTypes[head])
			return el.calleeCall(el.ctorCallee(info, occTy), args)
		}
	}

	// Unknown callee: one typed indirect call per application.
	res := el.expr(head)
	raw := el.apply(el.ck.ExprTypes[head])
	for _, a := range args {
		res = el.valueAppWithRow(res, el.expr(a), raw)
		raw = raw.(*types.TFun).Ret
	}
	return res
}

func (el *elab) effectInstance(op *types.EffectOp, ty types.Type) core.EffectInstance {
	t := ty
	for i := 0; i < op.Arity; i++ {
		f := t.(*types.TFun)
		if i == op.Arity-1 {
			for _, l := range f.Eff.Labels {
				if l.Unique == op.Owner.Unique {
					return core.EffectInstance{Unique: l.Unique, Name: l.Name, Args: append([]types.Type(nil), l.Args...), Captures: el.evidenceCaptures(l.Unique), Control: el.evidenceControl(l.Unique)}
				}
			}
		}
		t = f.Ret
	}
	return core.EffectInstance{Unique: op.Owner.Unique, Name: op.Owner.Name, Captures: el.evidenceCaptures(op.Owner.Unique), Control: el.evidenceControl(op.Owner.Unique)}
}

func (el *elab) operationCall(op *types.EffectOp, opTy, rawTy types.Type, args []ast.Expr) core.Expr {
	if len(args) > op.Arity {
		res := el.operationCall(op, opTy, rawTy, args[:op.Arity])
		for i, a := range args[op.Arity:] {
			res = el.valueAppWithRow(res, el.expr(a), arrowAt(rawTy, op.Arity+i))
		}
		return res
	}
	argTys, ret := core.PeelFun(opTy, op.Arity)
	coreArgs := make([]core.Expr, 0, op.Arity)
	for _, a := range args {
		coreArgs = append(coreArgs, el.expr(a))
	}

	for i := len(args); i < op.Arity; i++ {
		n := fmt.Sprintf("_op%d", el.tmp)
		el.tmp++
		coreArgs = append(coreArgs, &core.VarRef{Name: n, Local: true, Ty: argTys[i]})
	}
	var effectParams []core.EffectInstance
	if len(args) < op.Arity {
		effectParams = el.bindEffectParams(executingEffects(arrowAt(opTy, len(args)), op.Arity-len(args)))
	}
	var body core.Expr
	if op.Owner.Suspension && (op.Name == types.StreamYieldName) {
		body = &core.Suspend{Owner: el.effectInstance(op, rawTy), Request: coreArgs[0], Ty: ret}
	} else if op.Abort {
		inst := el.effectInstance(op, rawTy)
		body = &core.ControlExit{Effect: inst, Op: op, Payload: coreArgs, Ty: ret}
	} else {
		inst := el.effectInstance(op, rawTy)
		body = &core.Perform{Op: op, Effect: inst, Args: coreArgs, Ty: ret, Control: inst.Control}
	}
	if len(effectParams) > 0 {
		el.popEvidence(effectParams)
	}
	for i := op.Arity - 1; i >= len(args); i-- {
		lam := &core.Lambda{Param: coreArgs[i].(*core.VarRef).Name, Body: body, Ty: arrowAt(opTy, i), ParamCapture: el.ck.Sup.FreshCapture()}
		if i == op.Arity-1 {
			lam.EffectParams = effectParams
		}
		body = lam
	}
	return body
}

func arrowAt(t types.Type, n int) types.Type {
	for i := 0; i < n; i++ {
		t = t.(*types.TFun).Ret
	}
	return t
}
func (el *elab) operationValue(op *types.EffectOp, ty, raw types.Type) core.Expr {
	return el.operationCall(op, ty, raw, nil)
}

func (el *elab) nativeApply(n *types.NativeInfo, nativeTy types.Type, args []ast.Expr) core.Expr {
	if len(args) > n.Arity {
		res := el.nativeApply(n, nativeTy, args[:n.Arity])
		for _, a := range args[n.Arity:] {
			res = el.valueApp(res, el.expr(a))
		}
		return res
	}
	argTys, ret := core.PeelFun(nativeTy, n.Arity)
	coreArgs := make([]core.Expr, 0, n.Arity)
	for _, a := range args {
		coreArgs = append(coreArgs, el.expr(a))
	}
	for i := len(args); i < n.Arity; i++ {
		name := fmt.Sprintf("_native%d", el.tmp)
		el.tmp++
		coreArgs = append(coreArgs, &core.VarRef{Name: name, Local: true, Ty: argTys[i]})
	}
	var body core.Expr = el.fold(&core.NativeCall{Name: n.Name, Module: n.Module, Args: coreArgs, Ty: ret})
	for i := n.Arity - 1; i >= len(args); i-- {
		body = &core.Lambda{Param: coreArgs[i].(*core.VarRef).Name, Body: body, Ty: arrowAt(nativeTy, i), ParamCapture: el.ck.Sup.FreshCapture()}
	}
	return body
}

func (el *elab) nativeValue(n *types.NativeInfo, ty types.Type) core.Expr {
	return el.nativeApply(n, ty, nil)
}

// valueApp is one typed indirect application: callee(arg).
func (el *elab) valueApp(callee, arg core.Expr) core.Expr {
	fn, ok := callee.Type().(*types.TFun)
	if !ok {
		panic(fmt.Sprintf("elaborate: applying a non-function type %s", types.Show(callee.Type())))
	}
	arg = el.adaptFunctionValue(arg, fn.Arg)
	app := &core.App{
		CalleeKind: core.Value,
		Callee:     callee,
		Args:       []core.Expr{arg},
		Ty:         fn.Ret,
		Control:    types.FunctionControl(fn),
	}
	if types.FunctionOpenRow(fn) {
		app.Row = &core.RowArgument{From: pendingRow}
	}
	for _, l := range types.SortedRow(fn.Eff).Labels {
		if types.RuntimeEvidenceEffect(l) {
			app.EvidenceArgs = append(app.EvidenceArgs, core.EffectInstance{Unique: l.Unique, Name: l.Name, Args: append([]types.Type(nil), l.Args...), Captures: el.evidenceCaptures(l.Unique), Control: el.evidenceControl(l.Unique)})
		}
	}
	return app
}

// callee is a known-arity application head: a top-level worker, a
// constructor, or a lifted local. All get the same saturation analysis; only
// the emitted App's kind and leading arguments differ.
type callee struct {
	kind     core.CalleeKind
	name     string
	ty       types.Type      // full curried type AT THIS OCCURRENCE (instantiated)
	arity    int             // total parameters, including pre
	ctor     *types.CtorInfo // when kind == core.Ctor
	tyArgs   []types.Type    // explicit instantiation (doc/design.md, "Go backend and runtime"); nil when monomorphic
	pre      []core.Expr     // lifted locals: the captured frees, already-atomic leading args
	evidence []core.EffectInstance
	row      *core.RowArgument
	raw      types.Type
}

func (el *elab) workerCallee(name string, workerTy, rawTy types.Type, arity int) callee {
	c := callee{kind: core.Worker, name: name, ty: workerTy, arity: arity, tyArgs: el.workerTyArgs(name, rawTy)}
	sch, _ := el.ck.Env.Lookup(name)
	if name == el.declName {
		sch = el.declScheme
	}
	c.ty = el.eraseRuntimeKinds(eraseRowsFrom(el.ck.Sub.Apply(sch.Body), rawTy))
	c.raw = rawTy
	if core.ArrowOpenRow(c.ty, arity) {
		c.row = el.residualArgument(arrowAt(rawTy, arity-1).(*types.TFun).Eff, arrowAt(c.ty, arity-1).(*types.TFun).Eff)
	}
	return el.addEvidence(c, sch, rawTy)
}

// ctorCallee builds a constructor callee at its occurrence type — the
// constructor's generic ValueType instantiated at this use.
func (el *elab) ctorCallee(info *types.CtorInfo, occTy types.Type) callee {
	c := callee{kind: core.Ctor, name: info.Name, ty: occTy,
		arity: len(info.Fields), ctor: info}
	if adt := el.ck.ADTs[info.Result.Unique]; adt != nil && len(adt.Params) > 0 {
		c.tyArgs = matchTyArgs(info.ValueType(), adt.Params, occTy)
	}
	return c
}

// workerTyArgs derives a worker occurrence's explicit instantiation by
// matching the callee's generic type against the occurrence type (doc/design.md, "Go backend and runtime").
// Matching — not recording at instantiate-time — is what also covers
// self-recursive calls, which never pass through the scheme.
func (el *elab) workerTyArgs(name string, rawOccTy types.Type) []types.Type {
	genTy, vars := el.calleeGeneric(name)
	if len(vars) == 0 {
		return nil
	}
	args := matchTyArgs(genTy, vars, rawOccTy)
	for i, arg := range args {
		args[i] = el.eraseRuntimeKinds(eraseRows(arg))
	}
	return args
}

// calleeGeneric is a callee's generic type and type parameters. The
// declaration being elaborated answers for itself: the REPL elaborates
// before binding, and a redefinition must not see its previous generation.
func (el *elab) calleeGeneric(name string) (types.Type, []*types.TVar) {
	var sch types.Scheme
	if name == el.declName {
		sch = el.declScheme
	} else if s, ok := el.ck.Env.Lookup(name); ok {
		sch = s
	} else {
		panic("elaborate: unknown callee `" + name + "`")
	}
	genTy := el.ck.Sub.Apply(sch.Body)
	return genTy, runtimeRigidVars(genTy)
}

// matchTyArgs reads an occurrence's explicit type arguments off its type:
// matching the callee's generic type against the (instantiated) occurrence
// type assigns each of the callee's type parameters. Total by construction —
// every type parameter occurs in the generic type it was collected from.
func matchTyArgs(genTy types.Type, vars []*types.TVar, occTy types.Type) []types.Type {
	m := map[int]types.Type{}
	matchType(genTy, occTy, m)
	out := make([]types.Type, len(vars))
	for i, v := range vars {
		t, ok := m[v.ID]
		if !ok {
			panic("elaborate: type parameter not determined by the occurrence type")
		}
		out[i] = t
	}
	return out
}

func matchType(gen, occ types.Type, m map[int]types.Type) {
	switch g := gen.(type) {
	case *types.TVar:
		if !g.Rigid {
			panic("elaborate: metavariable in a generic callee type")
		}
		if _, seen := m[g.ID]; !seen {
			m[g.ID] = occ
		}
	case *types.TCon:
		o, ok := occ.(*types.TCon)
		if !ok || len(o.Args) != len(g.Args) {
			panic(fmt.Sprintf("elaborate: occurrence type %s does not match generic %s",
				types.Show(occ), types.Show(gen)))
		}
		for i := range g.Args {
			matchType(g.Args[i], o.Args[i], m)
		}
	case *types.TFun:
		o, ok := occ.(*types.TFun)
		if !ok {
			panic(fmt.Sprintf("elaborate: occurrence type %s does not match generic %s",
				types.Show(occ), types.Show(gen)))
		}
		matchType(g.Arg, o.Arg, m)
		matchType(g.Eff, o.Eff, m)
		matchType(g.Ret, o.Ret, m)
	case types.Row:
		o, ok := occ.(types.Row)
		if !ok {
			panic(fmt.Sprintf("elaborate: occurrence type %s does not match generic %s", types.Show(occ), types.Show(gen)))
		}
		for _, gl := range g.Labels {
			for _, ol := range o.Labels {
				if gl.Unique != ol.Unique || len(gl.Args) != len(ol.Args) {
					continue
				}
				for i := range gl.Args {
					matchType(gl.Args[i], ol.Args[i], m)
				}
				break
			}
		}
	}
}

// nullaryValueUse is a use of a polymorphic top-level value — a nullary
// generic worker (doc/design.md, "Go backend and runtime"), instantiated and called per use.
func (el *elab) nullaryValueUse(name string, sch types.Scheme, occTy, rawTy types.Type) core.Expr {
	genTy := el.zonkDefault(sch.Body)
	vars := types.RigidVarsIn(genTy)
	c := callee{kind: core.Worker, name: name, ty: occTy, tyArgs: matchTyArgs(genTy, vars, occTy)}
	c = el.addEvidence(c, sch, rawTy)
	return c.saturatedApp(nil)
}

// saturatedApp builds the direct App for a fully applied callee.
func (c callee) saturatedApp(args []core.Expr) *core.App {
	_, ret := core.PeelFun(c.ty, c.arity)
	control := core.ArrowControl(c.ty, c.arity)
	if c.kind == core.Ctor {
		control = types.Control{}
	}
	return &core.App{
		CalleeKind:   c.kind,
		Callee:       &core.VarRef{Name: c.name, Ty: c.ty},
		Args:         append(append([]core.Expr{}, c.pre...), args...),
		TyArgs:       c.tyArgs,
		Ty:           ret,
		EvidenceArgs: c.evidence,
		Ctor:         c.ctor,
		Control:      control,
		Row:          c.row,
	}
}

// workerCall classifies a call to a known worker by saturation.
func (el *elab) workerCall(name string, workerTy, rawTy types.Type, arity int, args []ast.Expr) core.Expr {
	c := el.workerCallee(name, workerTy, rawTy, arity)
	c.evidence = el.workerEvidence(name, arity, c.tyArgs)
	return el.calleeCall(c, args)
}

func (el *elab) workerEvidence(name string, arity int, tyArgs []types.Type) []core.EffectInstance {
	var sch types.Scheme
	if name == el.declName {
		sch = el.declScheme
	} else {
		var ok bool
		sch, ok = el.ck.Env.Lookup(name)
		if !ok {
			panic("elaborate: missing worker scheme `" + name + "`")
		}
	}
	raw := el.ck.Sub.Apply(sch.Body)
	effects := executingEffects(raw, arity)
	vars := runtimeRigidVars(raw)
	if len(vars) == len(tyArgs) && len(vars) > 0 {
		m := make(map[int]types.Type, len(vars))
		for i, v := range vars {
			m[v.ID] = tyArgs[i]
		}
		for i := range effects {
			for j, a := range effects[i].Args {
				effects[i].Args[j] = types.SubstRigid(a, m)
			}
		}
	}
	for i := range effects {
		effects[i].Captures = el.evidenceCaptures(effects[i].Unique)
		effects[i].Control = el.evidenceControl(effects[i].Unique)
	}
	return effects
}

func (el *elab) calleeCall(c callee, args []ast.Expr) core.Expr {
	missing := c.arity - len(c.pre)
	argTys, _ := core.PeelFun(c.ty, c.arity)
	switch {
	case len(args) == missing: // saturated: a direct call / struct literal
		coreArgs := make([]core.Expr, len(args))
		for i, a := range args {
			coreArgs[i] = el.adaptFunctionValue(el.expr(a), argTys[len(c.pre)+i])
		}
		return c.saturatedApp(coreArgs)

	case len(args) > missing: // oversaturated: direct call, then indirect
		res := el.calleeCall(c, args[:missing])
		for i, a := range args[missing:] {
			if c.raw != nil {
				res = el.valueAppWithRow(res, el.expr(a), arrowAt(c.raw, c.arity+i))
			} else {
				res = el.valueApp(res, el.expr(a))
			}
		}
		return res

	default: // partial (including a bare reference via curried*Ref)
		return el.partial(c, args)
	}
}

// adaptFunctionValue retags an eta-expanded callback to a generic callee's
// erased row ABI. Its concrete handler evidence remains captured by the
// wrapper at the creation site.
func (el *elab) adaptFunctionValue(e core.Expr, want types.Type) core.Expr {
	actualFn, _ := e.Type().(*types.TFun)
	if sameValueABI(e.Type(), want) {
		return e
	}
	wantFn, wantOK := want.(*types.TFun)
	_, actualOK := e.Type().(*types.TFun)
	if !wantOK || !actualOK {
		return el.adaptNominalValue(e, want)
	}
	switch e := e.(type) {
	case *core.Lambda:
		if !sameValueABI(actualFn.Arg, wantFn.Arg) {
			break
		}
		// A wider explicit row needs fresh (possibly unused) evidence binders.
		// Build the wrapper below instead of changing this lambda's binding ABI.
		needsEvidence := false
		for _, label := range wantFn.Eff.Labels {
			if !types.RuntimeEvidenceEffect(label) {
				continue
			}
			found := false
			for _, ev := range e.EffectParams {
				found = found || ev.Unique == label.Unique
			}
			needsEvidence = needsEvidence || !found
		}
		if needsEvidence {
			break
		}
		var kept []core.EffectInstance
		sub := map[types.CaptureVar]types.CaptureSet{}
		for _, ev := range e.EffectParams {
			retained := false
			for _, label := range wantFn.Eff.Labels {
				if label.Unique == ev.Unique {
					retained = true
					break
				}
			}
			if retained {
				kept = append(kept, ev)
				continue
			}
			if len(el.evidence[ev.Unique]) == 0 && types.FunctionOpenRow(wantFn) {
				e.RowEffects = append(e.RowEffects, ev)
				continue
			}
			for _, v := range ev.Captures.Vars {
				sub[v] = el.evidenceCaptures(ev.Unique)
			}
		}
		e.Body = core.SubstituteCaptureVars(e.Body, sub)
		e.EffectParams = kept
		e.Body = el.adaptFunctionValue(e.Body, wantFn.Ret)
		e.Ty = want
		return e
	case *core.If:
		e.Then = el.adaptFunctionValue(e.Then, want)
		e.Else = el.adaptFunctionValue(e.Else, want)
		e.Ty = want
		return e
	}
	if !isAtom(e) {
		name := fmt.Sprintf("_adaptValue%d", el.tmp)
		el.tmp++
		body := el.adaptFunctionValue(&core.VarRef{Name: name, Local: true, Ty: e.Type()}, want)
		return &core.Let{Name: name, Rhs: e, Body: body, Ty: want}
	}

	// A local function reference cannot itself be retagged: its VarRef must
	// retain the binding's concrete arrow. Eta-expand it and retag the new
	// wrapper instead. This is representation-safe because erased open-row
	// effects are executed by the wrapped call (and any custom evidence is
	// supplied there), while the generic callee receives its row-erased ABI.
	name := fmt.Sprintf("_adapt%d", el.tmp)
	el.tmp++
	arg := &core.VarRef{Name: name, Local: true, Ty: wantFn.Arg}
	effectParams := el.bindEffectParams(executingEffects(want, 1))
	var rowEffects []core.EffectInstance
	for _, label := range actualFn.Eff.Labels {
		if types.RuntimeEvidenceEffect(label) && len(el.evidence[label.Unique]) == 0 && types.FunctionOpenRow(wantFn) {
			rowEffects = append(rowEffects, core.EffectInstance{Unique: label.Unique, Name: label.Name, Args: label.Args, Control: el.evidenceControl(label.Unique)})
		}
	}
	rowEffects = el.bindEffectParams(rowEffects)
	body := el.valueApp(e, arg)
	if app, ok := body.(*core.App); ok && app.Row != nil {
		app.Row = el.residualArgument(wantFn.Eff, actualFn.Eff)
		if types.FunctionOpenRow(wantFn) {
			app.Row.From = pendingRow
		}
	}
	el.popEvidence(rowEffects)
	el.popEvidence(effectParams)
	body = el.adaptFunctionValue(body, wantFn.Ret)
	return &core.Lambda{Param: name, Body: body, Ty: &types.TFun{Arg: wantFn.Arg, Eff: wantFn.Eff, Ret: wantFn.Ret, Control: wantFn.Control, OpenRow: wantFn.OpenRow},
		ParamCapture: el.ck.Sup.FreshCapture(), EffectParams: effectParams, RowEffects: rowEffects}
}

// partial eta-expands an unsaturated worker or constructor application into
// doc/design.md, "Go backend and runtime" item 4's "exactly one closure whose body calls the worker": non-atomic
// given arguments are hoisted into Lets (strictness — they must evaluate when
// the partial is created, not per call), then nested Lambdas supply the
// missing parameters around one saturated App.
func (el *elab) partial(c callee, given []ast.Expr) core.Expr {
	workerTy, arity := c.ty, c.arity
	argTys, _ := core.PeelFun(workerTy, arity)
	taken := len(c.pre) + len(given)

	// Elaborate given args; hoist non-atoms into temps. (The pre args — a
	// lifted local's captured frees — are VarRefs by construction and were
	// prepended by saturatedApp.)
	type hoist struct {
		name string
		rhs  core.Expr
	}
	var hoists []hoist
	coreArgs := make([]core.Expr, 0, arity)
	for i, a := range given {
		ca := el.adaptFunctionValue(el.expr(a), argTys[len(c.pre)+i])
		if isAtom(ca) {
			coreArgs = append(coreArgs, ca)
			continue
		}
		tmp := fmt.Sprintf("_a%d", el.tmp)
		el.tmp++
		hoists = append(hoists, hoist{tmp, ca})
		coreArgs = append(coreArgs, &core.VarRef{Name: tmp, Local: true, Ty: ca.Type()})
	}

	// Missing parameters become nested lambda params.
	missing := arity - taken
	lamParams := make([]string, missing)
	for i := range missing {
		lamParams[i] = fmt.Sprintf("_w%d", el.tmp)
		el.tmp++
		coreArgs = append(coreArgs, &core.VarRef{Name: lamParams[i], Local: true, Ty: argTys[taken+i]})
	}

	// Wrap lambdas innermost-out; each level's type is the remaining chain.
	lamTy := workerTy
	for range taken {
		lamTy = lamTy.(*types.TFun).Ret
	}
	var effectParams []core.EffectInstance
	if missing > 0 {
		effectParams = el.bindEffectParams(executingEffects(lamTy, missing))
		for i := range c.evidence {
			for _, ev := range effectParams {
				if c.evidence[i].Unique == ev.Unique {
					c.evidence[i].Captures = ev.Captures
					c.evidence[i].Control = ev.Control
				}
			}
		}
	}
	var body core.Expr = c.saturatedApp(coreArgs)
	if missing > 0 {
		el.popEvidence(effectParams)
	}
	tys := make([]types.Type, missing)
	t := lamTy
	for i := range missing {
		tys[i] = t
		t = t.(*types.TFun).Ret
	}
	for i := missing - 1; i >= 0; i-- {
		lam := &core.Lambda{Param: lamParams[i], Body: body, Ty: tys[i], ParamCapture: el.ck.Sup.FreshCapture()}
		if i == missing-1 {
			lam.EffectParams = effectParams
		}
		body = lam
	}

	// Hoisted args evaluate at partial-creation time: Lets wrap outside.
	for i := len(hoists) - 1; i >= 0; i-- {
		body = &core.Let{Name: hoists[i].name, Rhs: hoists[i].rhs, Body: body, Ty: body.Type()}
	}
	return body
}

// curriedWorkerRef is the k=0 case: a worker used first-class expands to
// its curried wrapper at the use site — demand-driven by construction.
func (el *elab) curriedWorkerRef(name string, workerTy, rawTy types.Type, arity int) core.Expr {
	c := el.workerCallee(name, workerTy, rawTy, arity)
	c.evidence = el.workerEvidence(name, arity, c.tyArgs)
	return el.partial(c, nil)
}

// ctorValue is a constructor in value position at its occurrence type:
// nullary constructors are the saturated zero-arg App (a zero-field struct
// in codegen); field-taking ones get the same curried-wrapper treatment as
// first-class workers.
func (el *elab) ctorValue(info *types.CtorInfo, occTy types.Type) core.Expr {
	if len(info.Fields) == 0 {
		return el.ctorCallee(info, occTy).saturatedApp(nil)
	}
	return el.partial(el.ctorCallee(info, occTy), nil)
}

// isAtom reports whether re-evaluating e is free (no work, no effects):
// literals and variable references skip ANF hoisting.
func isAtom(e core.Expr) bool {
	switch e.(type) {
	case *core.IntLit, *core.FloatLit, *core.StringLit, *core.CharLit, *core.BoolLit, *core.UnitLit, *core.VarRef:
		return true
	default:
		return false
	}
}
