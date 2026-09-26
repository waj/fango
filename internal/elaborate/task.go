package elaborate

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/types"
)

func (el *elab) taskSpawn(e ast.Expr, args []ast.Expr) core.Expr {
	fail := func(message string) core.Expr {
		el.errs = append(el.errs, diag.Errorf(e.Span(), "TASK BOUNDARY", "%s", message))
		return &core.UnitLit{Ty: el.ck.B.Unit}
	}
	if len(args) != 3 {
		return fail("Task.spawn must be called with a scope, a named worker, and input; it cannot be partially applied.")
	}
	worker, ok := args[1].(*ast.Var)
	if !ok {
		return fail("A task worker must be a named top-level function, not a closure or partial application.")
	}
	_, local := el.scopeIdx[worker.Name]
	if local || el.lifted[worker.Name] != nil || el.ck.Workers[worker.Name] != 2 || el.ck.Natives[worker.Name] != nil || types.Intrinsic(worker.Name) {
		return fail("A task worker must be a top-level function with context and input parameters.")
	}
	scheme, ok := el.ck.Env.Lookup(worker.Name)
	if !ok || len(scheme.Preds) != 0 {
		return fail("A task worker cannot require class dictionaries.")
	}
	workerType := el.zonkDefault(el.ck.ExprTypes[args[1]])
	params, result := core.PeelFun(workerType, 2)
	if !types.Transferable(params[1], el.ck.ADTs, el.ck.B) || !types.Transferable(result, el.ck.ADTs, el.ck.B) {
		return fail("Task input and output must be concrete immutable data; functions and native handles cannot cross the task boundary.")
	}
	ctor := func(name string) *types.CtorInfo {
		return el.ck.ADTs[el.ck.TypeNames[name].(*types.TCon).Unique].Ctors[0]
	}
	c := el.workerCallee(worker.Name, workerType, el.apply(el.ck.ExprTypes[args[1]]), 2)
	return &core.TaskSpawn{Scope: el.expr(args[0]), Input: el.expr(args[2]), Worker: worker.Name, WorkerType: workerType, TyArgs: c.tyArgs, ContextCtor: ctor("Task.Context"), ScopeCtor: ctor("Task.Scope"), TaskCtor: ctor("Task.Task"), Ty: el.zonkDefault(el.ck.ExprTypes[e])}
}
