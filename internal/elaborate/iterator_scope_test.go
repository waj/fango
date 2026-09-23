package elaborate

import (
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/types"
)

func TestCoroutineIntrinsicBuildsOwnedCoreBoundary(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	ck := infer.NewChecker(sup, b, infer.NewEnv())
	elem := sup.FreshRigid(types.General)
	result := sup.FreshRigid(types.General)
	row := sup.FreshRigid(types.RowVar)
	iterator := &types.TCon{Unique: sup.NextUnique(), Name: types.CoroutineTypeName, Args: []types.Type{elem, b.Unit, b.Unit, row}}
	suspension := types.EffLabel{Unique: sup.NextUnique(), Name: types.CoroutineSuspensionName, Suspension: true}
	traversal := types.EffLabel{Unique: sup.NextUnique(), Name: types.CoroutineDriveName, Suspension: true}
	for _, label := range []types.EffLabel{suspension, traversal} {
		ck.Effects[label.Name] = &types.EffectInfo{Unique: label.Unique, Name: label.Name, Suspension: true}
	}
	pause := &types.TFun{Arg: elem, Eff: types.Row{Labels: []types.EffLabel{suspension}}, Ret: b.Unit}
	producer := &types.TFun{Arg: pause, Ret: &types.TFun{Arg: b.Unit, Eff: types.Row{Labels: []types.EffLabel{suspension}, Tail: row}, Ret: b.Unit}}
	consumer := &types.TFun{Arg: iterator, Eff: types.Row{Labels: []types.EffLabel{traversal}, Tail: row}, Ret: result}
	ty := &types.TFun{Arg: producer, Ret: &types.TFun{Arg: consumer, Eff: types.Row{Tail: row}, Ret: result}}
	ck.Intrinsics[types.CoroutineWithName] = types.Scheme{Vars: []*types.TVar{elem, result, row}, Body: ty}

	defs := IntrinsicDefs(ck)
	if len(defs) != 1 {
		t.Fatalf("intrinsic defs = %d, want 1", len(defs))
	}
	boundary, ok := defs[0].Body.(*core.CoroutineScope)
	if !ok {
		t.Fatalf("intrinsic body = %T, want *core.CoroutineScope", defs[0].Body)
	}
	erased := &types.TCon{Unique: iterator.Unique, Name: iterator.Name, Args: []types.Type{elem, b.Unit, b.Unit, b.Unit}}
	if !types.Equal(boundary.CursorTy, erased) || !types.Equal(boundary.Type(), result) {
		t.Fatalf("boundary cursor/result = %s / %s", types.Show(boundary.CursorTy), types.Show(boundary.Type()))
	}
	p := &core.Prog{Defs: defs, Intrinsics: map[string]bool{types.CoroutineWithName: true}}
	p.Effects = []*types.EffectInfo{ck.Effects[suspension.Name], ck.Effects[traversal.Name]}
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatalf("capture inference: %v", errs)
	}
	if errs := core.Lint(p, b); len(errs) != 0 {
		t.Fatalf("Core lint: %v", errs)
	}
}
