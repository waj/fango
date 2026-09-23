package check

import (
	"path/filepath"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func TestWorkLatentRowsSurviveModuleObjects(t *testing.T) {
	for _, name := range []string{"work_latent", "work_scoped_effect"} {
		t.Run(name, func(t *testing.T) { checkWorkLatentRows(t, name) })
	}
}

func checkWorkLatentRows(t *testing.T, name string) {
	entry := filepath.Join("..", "..", "testdata", "run", name+".fango")
	cache := newMemoryObjectCache()
	compileEvents(t, entry, cache)
	result, events := compileEvents(t, entry, cache)
	if events["checked-cache-hit"]["Work"] != 1 {
		t.Fatalf("Work object was not restored: %#v", events)
	}
	scheme, ok := result.Checker.Env.Lookup("schedule")
	if !ok {
		t.Fatal("schedule missing")
	}
	fn, ok := scheme.Body.(*types.TFun)
	if !ok {
		t.Fatalf("not a function: %s", types.Show(scheme.Body))
	}
	cursor, ok := fn.Arg.(*types.TCon)
	if !ok || cursor.Name != types.CoroutineTypeName {
		t.Fatalf("not a coroutine: %s", types.Show(fn.Arg))
	}
	row, ok := cursor.Args[3].(types.Row)
	if !ok {
		if v, variable := cursor.Args[3].(*types.TVar); variable && v.Kind == types.RowVar {
			row = types.Row{Tail: v}
		} else {
			t.Fatalf("missing residual row: %s", types.Show(cursor.Args[3]))
		}
	}
	has := func(row types.Row, name string) bool {
		for _, label := range row.Labels {
			if label.Name == name {
				return true
			}
		}
		return false
	}
	if len(row.Labels) != 0 || row.Tail == nil || !types.Equal(row.Tail, fn.Eff.Tail) || !has(fn.Eff, types.CoroutineDriveName) || len(fn.Eff.Labels) != 1 {
		t.Fatalf("incorrect latent row: %s", types.Show(scheme.Body))
	}
	for _, d := range result.Program.Defs {
		if d.Name == types.WorkPackName {
			if d.SourceType == nil || d.CaptureContract == nil || d.CaptureContract.SourceType == nil {
				t.Fatal("object discarded Work source contract")
			}
		}
	}
	if errs := core.LintMachineInput(result.Program, result.Checker.B); len(errs) != 0 {
		t.Fatal(errs)
	}
}
