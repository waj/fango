package elaborate

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/types"
)

func (el *elab) asyncIntrinsic(e ast.Expr, name string, args []ast.Expr, head ast.Expr) core.Expr {
	if len(args) != types.IntrinsicArity(name) {
		el.errs = append(el.errs, diag.Errorf(e.Span(), "ASYNC BOUNDARY", "%s must be fully applied.", name))
		return &core.UnitLit{Ty: el.ck.B.Unit}
	}
	raw := el.apply(el.ck.ExprTypes[head])
	index := 0
	if name == types.AsyncLaunchName {
		index = 1
		raw = raw.(*types.TFun).Ret
	}
	wantRaw := raw.(*types.TFun).Arg
	// Use the declaration's open callback ABI, not the concrete argument's row.
	scheme := el.ck.Intrinsics[name]
	sub := map[int]types.Type{}
	matchType(scheme.Body, el.apply(el.ck.ExprTypes[head]), sub)
	declared := el.eraseRuntimeKinds(eraseRows(scheme.Body))
	if index == 1 {
		declared = declared.(*types.TFun).Ret
	}
	want := types.SubstRigid(declared.(*types.TFun).Arg, sub)
	body := el.adaptFunctionValue(el.expr(args[index]), want, el.apply(el.ck.ExprTypes[args[index]]), wantRaw)
	var argument core.Expr = &core.UnitLit{Ty: el.ck.B.Unit}
	if index == 1 {
		argument = el.expr(args[0])
	}
	call := el.valueAppWithRow(body, argument, wantRaw).(*core.App)
	if name == types.AsyncSuperviseName {
		return &core.AsyncSupervise{Call: call}
	}
	if index == 0 {
		return &core.AsyncRebase{Call: call}
	}
	ctor := func(name string) *types.CtorInfo { return el.ck.Ctors[name] }
	return &core.AsyncLaunch{Call: call, Ty: el.zonkDefault(el.ck.ExprTypes[e]), ScopeCtor: ctor("Async.Scope"), TaskCtor: ctor("Async.RawTask"), CompletedCtor: ctor("Async.Completed"), CancelledCtor: ctor("Async.Cancelled")}
}

// Directly known unsupported aborts are source errors. Dependencies hidden in
// installed handlers or a generic row are checked when child evidence is built.
func (el *elab) checkAsyncJob(job ast.Expr) {
	row, ok := el.callbackRequirements(job)
	if !ok {
		return
	}
	for _, label := range row.Labels {
		if label.Scoped {
			el.errs = append(el.errs, diag.Errorf(job.Span(), "ASYNC BOUNDARY", "A task cannot inherit a local scoped permission; install its handler inside the task."))
			continue
		}
		effect := el.ck.Effects[label.Name]
		if effect != nil && len(effect.Ops) > 0 && effect.Ops[0].Abort {
			el.errs = append(el.errs, diag.Errorf(job.Span(), "ASYNC BOUNDARY", "A task cannot inherit abort effect %s; handle it inside the task.", label.Name))
		}
	}
}

func parallelMapDef(name string, ty types.Type, ck *infer.Checker) core.Def {
	args, result := core.PeelFun(ty, 2)
	return core.Def{Name: name, Owner: symbolOwner(name), Type: ty, TyParams: runtimeRigidVars(ty), Params: []string{"_function", "_input"}, ParamCaptures: []types.CaptureVar{ck.Sup.FreshCapture(), ck.Sup.FreshCapture()}, Body: &core.ParallelMap{Function: &core.VarRef{Name: "_function", Local: true, Ty: args[0]}, Input: &core.VarRef{Name: "_input", Local: true, Ty: args[1]}, Ty: result}}
}
