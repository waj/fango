package eval

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
	"github.com/waj/fango/runtime/fangort"
)

func TestTasksShareMemoizedGlobals(t *testing.T) {
	env := NewEnv()
	env.Define("shared", &core.IntLit{Val: 42, Ty: intTy()})
	scope := fangort.NewAsyncScope(nil)
	var tasks []*fangort.AsyncTask
	for range 64 {
		tasks = append(tasks, fangort.SpawnAsync(scope, func(*fangort.AsyncScope) fangort.AsyncCompletion {
			value, err := EvalIO(context.Background(), &core.VarRef{Name: "shared", Ty: intTy()}, env, NewIOContext(nil, io.Discard))
			if err != nil {
				t.Error(err)
			}
			return fangort.AsyncCompletion{Value: value}
		}))
	}
	scope.Finish(fangort.AsyncCompletion{})
	for _, task := range tasks {
		if got := task.Wait().Value; got != int64(42) {
			t.Fatalf("result: %v", got)
		}
	}
}

func TestParallelMapHostCancellationOwnership(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ty := intTy()
	var body core.Expr = &core.IntLit{Val: 42, Ty: ty}
	for range pollEvery + 8 {
		body = &core.Neg{Operand: body, Ty: ty}
	}
	input := fangort.ListNil[Value]()
	for range 16 {
		input = fangort.ListCons(Value(int64(1)), input)
	}
	expr := &core.ParallelMap{
		Function: &core.Lambda{Param: "value", Body: body, Ty: &types.TFun{Arg: ty, Ret: ty}},
		Input:    &core.VarRef{Name: "input", Local: true},
	}
	for _, supervised := range []bool{false, true} {
		in := &interp{ctx: ctx, env: NewEnv(), out: io.Discard, evidence: map[int]*evidence{}, forcing: map[*Cell]bool{}}
		if supervised {
			in.pollOwned = 1
		}
		value, err := in.parallelMap(expr, &Frame{vars: map[string]Value{"input": input}})
		if supervised {
			if err != nil {
				t.Fatal(err)
			}
			if got := value.(fangort.List[Value]).Head(); got != int64(42) {
				t.Fatalf("result: %v", got)
			}
		} else if err == nil || !strings.Contains(err.Error(), "interrupted") {
			t.Fatalf("expected interruption, got %v", err)
		}
	}
}
