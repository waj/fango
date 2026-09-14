package elaborate

import (
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/types"
)

func TestWithIteratorIntrinsicBuildsOwnedCoreBoundary(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	ck := infer.NewChecker(sup, b, infer.NewEnv())
	elem := sup.FreshRigid(types.General)
	result := sup.FreshRigid(types.General)
	row := sup.FreshRigid(types.RowVar)
	iterator := &types.TCon{Unique: sup.NextUnique(), Name: types.IteratorTypeName, Args: []types.Type{elem}}
	producer := &types.TFun{Arg: b.Unit, Eff: types.Row{Labels: []types.EffLabel{{Unique: sup.NextUnique(), Name: types.GeneratorEffectName, Args: []types.Type{elem}, Suspension: true}}, Tail: row}, Ret: b.Unit}
	consumer := &types.TFun{Arg: iterator, Eff: types.Row{Tail: row}, Ret: result}
	ty := &types.TFun{Arg: producer, Ret: &types.TFun{Arg: consumer, Eff: types.Row{Tail: row}, Ret: result}}
	ck.Intrinsics[types.GeneratorWithIteratorName] = types.Scheme{Vars: []*types.TVar{elem, result, row}, Body: ty}

	defs := IntrinsicDefs(ck)
	if len(defs) != 1 {
		t.Fatalf("intrinsic defs = %d, want 1", len(defs))
	}
	boundary, ok := defs[0].Body.(*core.IteratorScope)
	if !ok {
		t.Fatalf("intrinsic body = %T, want *core.IteratorScope", defs[0].Body)
	}
	if !types.Equal(boundary.CursorTy, iterator) || !types.Equal(boundary.Type(), result) {
		t.Fatalf("boundary cursor/result = %s / %s", types.Show(boundary.CursorTy), types.Show(boundary.Type()))
	}
	p := &core.Prog{Defs: defs, Intrinsics: map[string]bool{types.GeneratorWithIteratorName: true}}
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatalf("capture inference: %v", errs)
	}
	if errs := core.Lint(p, b); len(errs) != 0 {
		t.Fatalf("Core lint: %v", errs)
	}
}
