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
	p.Effects = []*types.EffectInfo{{Unique: boundary.Yield.Unique, Name: boundary.Yield.Name, Params: []*types.TVar{elem}, Suspension: true}}
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatalf("capture inference: %v", errs)
	}
	if errs := core.Lint(p, b); len(errs) != 0 {
		t.Fatalf("Core lint: %v", errs)
	}
}

func TestIteratorForEachIntrinsicBuildsTerminalCore(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	ck := infer.NewChecker(sup, b, infer.NewEnv())
	elem := sup.FreshRigid(types.General)
	row := sup.FreshRigid(types.RowVar)
	iterator := &types.TCon{Unique: sup.NextUnique(), Name: types.IteratorTypeName, Args: []types.Type{elem}}
	action := &types.TFun{Arg: elem, Eff: types.Row{Tail: row}, Ret: b.Unit}
	ty := &types.TFun{Arg: action, Ret: &types.TFun{Arg: iterator, Eff: types.Row{Tail: row}, Ret: b.Unit}}
	ck.Intrinsics[types.IteratorForEachName] = types.Scheme{Vars: []*types.TVar{elem, row}, Body: ty}

	defs := IntrinsicDefs(ck)
	if len(defs) != 1 {
		t.Fatalf("intrinsic defs = %d, want 1", len(defs))
	}
	terminal, ok := defs[0].Body.(*core.IteratorForEach)
	if !ok {
		t.Fatalf("intrinsic body = %T, want *core.IteratorForEach", defs[0].Body)
	}
	if !types.Equal(terminal.Element, elem) || !types.Equal(terminal.Type(), b.Unit) {
		t.Fatalf("terminal element/result = %s / %s", types.Show(terminal.Element), types.Show(terminal.Type()))
	}
	p := &core.Prog{Defs: defs, Intrinsics: map[string]bool{types.IteratorForEachName: true}}
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatalf("capture inference: %v", errs)
	}
	if errs := core.Lint(p, b); len(errs) != 0 {
		t.Fatalf("Core lint: %v", errs)
	}
}

func TestIteratorFoldIntrinsicBuildsTerminalCore(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	ck := infer.NewChecker(sup, b, infer.NewEnv())
	elem := sup.FreshRigid(types.General)
	acc := sup.FreshRigid(types.General)
	row := sup.FreshRigid(types.RowVar)
	iterator := &types.TCon{Unique: sup.NextUnique(), Name: types.IteratorTypeName, Args: []types.Type{elem}}
	step := &types.TFun{Arg: acc, Eff: types.Row{Tail: row}, Ret: acc}
	combine := &types.TFun{Arg: elem, Ret: step}
	ty := &types.TFun{Arg: combine, Ret: &types.TFun{Arg: acc, Ret: &types.TFun{Arg: iterator, Eff: types.Row{Tail: row}, Ret: acc}}}
	ck.Intrinsics[types.IteratorFoldName] = types.Scheme{Vars: []*types.TVar{elem, acc, row}, Body: ty}

	defs := IntrinsicDefs(ck)
	if len(defs) != 1 {
		t.Fatalf("intrinsic defs = %d, want 1", len(defs))
	}
	terminal, ok := defs[0].Body.(*core.IteratorFold)
	if !ok {
		t.Fatalf("intrinsic body = %T, want *core.IteratorFold", defs[0].Body)
	}
	if !types.Equal(terminal.Element, elem) || !types.Equal(terminal.Accumulator, acc) {
		t.Fatalf("terminal element/accumulator = %s / %s", types.Show(terminal.Element), types.Show(terminal.Accumulator))
	}
	p := &core.Prog{Defs: defs, Intrinsics: map[string]bool{types.IteratorFoldName: true}}
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatalf("capture inference: %v", errs)
	}
	if errs := core.Lint(p, b); len(errs) != 0 {
		t.Fatalf("Core lint: %v", errs)
	}
}
