package core

import "github.com/waj/fango/internal/types"

// TaskSpawn is the one concurrent invocation boundary. Worker is a named,
// closed top-level function; only Scope and Input execute in the parent.
type TaskSpawn struct {
	Scope, Input                     Expr
	Worker                           string
	WorkerType                       types.Type
	TyArgs                           []types.Type
	ContextCtor, TaskCtor, ScopeCtor *types.CtorInfo
	Ty                               types.Type
}

func (*TaskSpawn) isExpr()            {}
func (e *TaskSpawn) Type() types.Type { return e.Ty }

func (l *linter) taskSpawn(e *TaskSpawn, where string) {
	if e.Scope == nil || e.Input == nil || e.WorkerType == nil || e.ContextCtor == nil || e.ScopeCtor == nil || e.TaskCtor == nil {
		l.errorf("%s: malformed task spawn", where)
		return
	}
	l.expr(e.Scope, where)
	l.expr(e.Input, where)
	d := l.workers[e.Worker]
	if d == nil || len(d.Params) != 2 || len(d.EffectParams) != 0 || d.RowParam != 0 || len(d.TyParams) != len(e.TyArgs) || d.Control != (types.Control{}) || l.natives[e.Worker] != nil || l.intrinsics[e.Worker] {
		l.errorf("%s: task requires a closed named worker", where)
		return
	}
	if !l.intrinsics[types.TaskSpawnName] {
		l.errorf("%s: task spawn intrinsic is not declared", where)
	}
	sub := map[int]types.Type{}
	for i, v := range d.TyParams {
		if !types.Transferable(e.TyArgs[i], l.adts, l.b) {
			l.errorf("%s: task type argument is not transferable", where)
			return
		}
		sub[v.ID] = e.TyArgs[i]
	}
	worker := types.SubstRigid(d.Type, sub)
	if !EqualValueRepresentation(worker, e.WorkerType) {
		l.errorf("%s: task worker type mismatch", where)
		return
	}
	var params []types.Type
	result := worker
	for range 2 {
		fn, ok := result.(*types.TFun)
		if !ok || types.FunctionControl(fn) != (types.Control{}) || types.FunctionOpenRow(fn) {
			l.errorf("%s: task worker requires two Direct arrows", where)
			return
		}
		for _, label := range fn.Eff.Labels {
			if types.RuntimeEvidenceEffect(label) {
				l.errorf("%s: task worker requires closed IO effects", where)
				return
			}
		}
		params = append(params, fn.Arg)
		result = fn.Ret
	}
	if !types.Equal(params[1], e.Input.Type()) || !types.Transferable(params[1], l.adts, l.b) || !types.Transferable(result, l.adts, l.b) {
		l.errorf("%s: task input/output is not transferable", where)
	}
	con, ok := e.Ty.(*types.TCon)
	if !ok || con.Name != "Task.Task" || len(con.Args) != 1 || !types.Equal(con.Args[0], result) {
		l.errorf("%s: task result type mismatch", where)
	}
	ctx, ok := params[0].(*types.TCon)
	if !ok || ctx.Name != "Task.Context" {
		l.errorf("%s: task context mismatch", where)
	}
	scope, ok := e.Scope.Type().(*types.TCon)
	if !ok || scope.Name != "Task.Scope" {
		l.errorf("%s: task scope mismatch", where)
	}
	l.taskConstructor(e.ContextCtor, params[0], "Task.Context", 0, where)
	l.taskConstructor(e.ScopeCtor, e.Scope.Type(), "Task.Scope", 0, where)
	l.taskConstructor(e.TaskCtor, e.Ty, "Task.Task", 1, where)
}

func (l *linter) taskConstructor(c *types.CtorInfo, instance types.Type, name string, arity int, where string) {
	con, ok := instance.(*types.TCon)
	if !ok || con.Name != name || len(con.Args) != arity || c.Result == nil || c.Result.Unique != con.Unique {
		l.errorf("%s: task constructor type mismatch", where)
		return
	}
	adt := l.adts[con.Unique]
	if adt == nil || adt.Con.Name != name || adt.Repr != types.ReprADT || len(adt.Params) != arity || len(adt.Ctors) != 1 || len(c.Fields) != 1 {
		l.errorf("%s: task constructor declaration mismatch", where)
		return
	}
	declared := adt.Ctors[0]
	field, ok := c.Fields[0].(*types.TCon)
	if !ok || l.adts[field.Unique] == nil || l.adts[field.Unique].Repr != types.ReprNativeAny || c.Name != declared.Name || c.Index != declared.Index || c.Repr != declared.Repr || !EqualValueRepresentation(c.ValueType(), declared.ValueType()) {
		l.errorf("%s: task constructor representation mismatch", where)
	}
}
