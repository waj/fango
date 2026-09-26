package eval

import (
	"context"
	"io"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
	"github.com/waj/fango/runtime/fangort"
)

func TestTasksShareMemoizedGlobals(t *testing.T) {
	env := NewEnv()
	env.Define("shared", &core.IntLit{Val: 42, Ty: intTy()})
	worker := &core.Def{Name: "worker", Params: []string{"context", "input"}, Type: &types.TFun{Arg: unitTy(), Ret: &types.TFun{Arg: unitTy(), Ret: intTy()}}, Body: &core.VarRef{Name: "shared", Ty: intTy()}}
	env.DefineWorker(worker)
	scope := fangort.NewTaskScope()
	defer scope.Close()
	fr := &Frame{vars: map[string]Value{"scope": &CtorVal{Fields: []Value{scope}}}}
	in := &interp{ctx: context.Background(), env: env, out: io.Discard, evidence: map[int]*evidence{}, forcing: map[*Cell]bool{}}
	spawn := &core.TaskSpawn{Scope: &core.VarRef{Name: "scope", Local: true}, Input: &core.UnitLit{Ty: unitTy()}, Worker: worker.Name}
	var tasks []*fangort.Task
	for range 64 {
		value, err := in.spawnTask(spawn, fr)
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, value.(*CtorVal).Fields[0].(*fangort.Task))
	}
	scope.Join()
	for _, task := range tasks {
		if task.Status() != 0 {
			t.Fatalf("task failed: %s", task.Fault())
		}
		if got := fangort.UnpackNativeValue[Value](task.Value()); got != int64(42) {
			t.Fatalf("result: %v", got)
		}
	}
}
