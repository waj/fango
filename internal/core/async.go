package core

import "github.com/waj/fango/internal/types"

// AsyncLaunch invokes Call in a fresh child scope. Call's argument supplies the
// parent scope and is replaced with the child scope only at invocation.
type AsyncLaunch struct {
	Call                                                               *App
	ScopeCtor, TaskCtor, CompletedCtor, CancelledCtor, OkCtor, ErrCtor *types.CtorInfo
	Ty                                                                 types.Type
}

func (*AsyncLaunch) isExpr()            {}
func (e *AsyncLaunch) Type() types.Type { return e.Ty }

// AsyncRebase rebuilds the invocation's residual evidence against its explicit
// child Async and Fail boundaries before calling any user code.
type AsyncRebase struct{ Call *App }

func (*AsyncRebase) isExpr()            {}
func (e *AsyncRebase) Type() types.Type { return e.Call.Type() }

func (l *linter) asyncLaunch(e *AsyncLaunch, where string) {
	if e.Call == nil || e.Call.CalleeKind != Value || len(e.Call.Args) != 1 || !l.intrinsics[types.AsyncLaunchName] {
		l.errorf("%s: malformed Async launch", where)
		return
	}
	l.expr(e.Call, where)
	scope, ok := e.Call.Args[0].Type().(*types.TCon)
	if !ok || scope.Name != "Async.Scope" || len(scope.Args) != 1 || !l.nativeStorageWrapper(e.ScopeCtor) || e.ScopeCtor.Result.Unique != scope.Unique {
		l.errorf("%s: Async scope representation mismatch", where)
		return
	}
	task, ok := e.Ty.(*types.TCon)
	if !ok || task.Name != "Async.RawTask" || len(task.Args) != 1 || !l.nativeStorageWrapper(e.TaskCtor) || e.TaskCtor.Result.Unique != task.Unique {
		l.errorf("%s: Async task representation mismatch", where)
		return
	}
	outcome, ok := e.Call.Ty.(*types.TCon)
	if !ok || outcome.Name != "Async.Outcome" || len(outcome.Args) != 1 || !EqualValueRepresentation(outcome.Args[0], task.Args[0]) {
		l.errorf("%s: Async outcome representation mismatch", where)
		return
	}
	result, ok := task.Args[0].(*types.TCon)
	if !ok || result.Name != "Result.Result" || len(result.Args) != 2 || !types.Equal(result.Args[0], scope.Args[0]) {
		l.errorf("%s: Async failure index mismatch", where)
		return
	}
	for index, c := range []*types.CtorInfo{e.CompletedCtor, e.CancelledCtor, e.OkCtor, e.ErrCtor} {
		if c == nil || c.Result == nil {
			l.errorf("%s: missing Async result constructor", where)
			continue
		}
		names := []string{"Async.Completed", "Async.Cancelled", "Result.Ok", "Result.Err"}
		owner := outcome.Unique
		if index >= 2 {
			owner = result.Unique
		}
		fields := 1
		if index == 1 {
			fields = 0
		}
		if c.Name != names[index] || c.Result.Unique != owner || len(c.Fields) != fields {
			l.errorf("%s: Async result constructor role mismatch", where)
		}
		adt := l.adts[c.Result.Unique]
		if adt == nil || c.Index >= len(adt.Ctors) || c.Index < 0 || adt.Ctors[c.Index].Name != c.Name || !EqualValueRepresentation(adt.Ctors[c.Index].ValueType(), c.ValueType()) {
			l.errorf("%s: malformed Async result constructor", where)
		}
	}
	if len(e.Call.EvidenceArgs) != 0 {
		l.errorf("%s: Async launch wrapper requires only residual evidence", where)
	}
}
func (l *linter) asyncRebase(e *AsyncRebase, where string) {
	if e.Call == nil || e.Call.CalleeKind != Value || len(e.Call.Args) != 1 || !l.intrinsics[types.AsyncRebaseName] {
		l.errorf("%s: malformed Async rebase", where)
		return
	}
	l.expr(e.Call, where)
	if !types.Equal(e.Call.Args[0].Type(), l.b.Unit) {
		l.errorf("%s: Async rebase requires a thunk", where)
	}
	found := map[string]EffectInstance{}
	for _, ev := range e.Call.EvidenceArgs {
		found[ev.Name] = ev
	}
	a, aok := found["Async.Async"]
	f, fok := found["Fail.Fail"]
	if len(found) != 3 || found["Async.Cancellation"].Unique == 0 || !aok || !fok || len(a.Args) != 1 || len(f.Args) != 1 || !types.Equal(a.Args[0], f.Args[0]) {
		l.errorf("%s: Async rebase requires matching child Async/Fail evidence", where)
	}
}

// AsyncSupervise gives the source runner ownership of host cancellation through
// its cleanup. Compiled execution is an ordinary call; interpreter ticks do not
// insert cancellation points inside this boundary.
type AsyncSupervise struct{ Call *App }

func (*AsyncSupervise) isExpr()            {}
func (e *AsyncSupervise) Type() types.Type { return e.Call.Type() }
func (l *linter) asyncSupervise(e *AsyncSupervise, where string) {
	if e.Call == nil || e.Call.CalleeKind != Value || len(e.Call.Args) != 1 || !l.intrinsics[types.AsyncSuperviseName] {
		l.errorf("%s: malformed Async supervisor", where)
		return
	}
	l.expr(e.Call, where)
	if !types.Equal(e.Call.Args[0].Type(), l.b.Unit) {
		l.errorf("%s: Async supervisor requires a thunk", where)
	}
}
