package elaborate

import (
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/types"
)

func TestRowErasureRetainsResidualArrowIndependentlyOfTransport(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	reader := types.EffLabel{Unique: sup.NextUnique(), Name: "Reader"}
	open := &types.TFun{Arg: b.Unit, Ret: b.Int, Eff: types.Row{Tail: sup.FreshRigid(types.RowVar)}}
	closed := &types.TFun{Arg: b.Unit, Ret: b.Int, Eff: types.Row{Labels: []types.EffLabel{reader}}}
	for _, test := range []struct {
		name           string
		origin, solved *types.TFun
		want           bool
	}{
		{"abstract", open, open, true},
		{"instantiated", open, closed, true},
		{"closed resumptive", closed, closed, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			erased := eraseRowsFrom(test.origin, test.solved).(*types.TFun)
			if erased.Eff.Tail != nil || erased.OpenRow != test.want {
				t.Fatalf("erased row = %+v, open = %v", erased.Eff, erased.OpenRow)
			}
			if !types.FunctionControl(erased).Polymorphic {
				t.Fatal("transport polymorphism was lost")
			}
			again := eraseRows(types.SubstRigid(erased, map[int]types.Type{})).(*types.TFun)
			if again.OpenRow != test.want {
				t.Fatal("substitution/repeated erasure lost residual row")
			}
		})
	}
	// A pure factory stays closed even when it returns an open callback.
	factory := eraseRows(&types.TFun{Arg: b.Unit, Ret: open}).(*types.TFun)
	if factory.OpenRow || types.FunctionControl(factory).Polymorphic || !factory.Ret.(*types.TFun).OpenRow {
		t.Fatal("callback row affected factory execution")
	}
}

func TestCallbackAdaptationRetainsErasedResidualArrow(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	el := &elab{ck: infer.NewChecker(sup, b, infer.NewEnv())}
	actual := &types.TFun{Arg: b.Unit, Ret: b.Int, Control: types.Control{Polymorphic: true}}
	want := *actual
	want.OpenRow = true
	lam := &core.Lambda{Param: "unit", Ty: actual, Body: &core.IntLit{Val: 1, Ty: b.Int}}
	adapted := el.adaptFunctionValue(lam, &want)
	if !adapted.Type().(*types.TFun).OpenRow {
		t.Fatal("equal surface types skipped residual-row adaptation")
	}
}

func TestCallbackAdaptationChecksCurriedResultABI(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	el := &elab{ck: infer.NewChecker(sup, b, infer.NewEnv())}
	closed := &types.TFun{Arg: b.Int, Ret: b.Int}
	open := &types.TFun{Arg: b.Int, Ret: b.Int, OpenRow: true}
	actual := &types.TFun{Arg: b.Unit, Ret: closed}
	want := &types.TFun{Arg: b.Unit, Ret: open}
	inner := &core.Lambda{Param: "value", Ty: closed, Body: &core.IntLit{Val: 1, Ty: b.Int}}
	outer := &core.Lambda{Param: "unit", Ty: actual, Body: inner}
	adapted := el.adaptFunctionValue(outer, want).(*core.Lambda)
	if !types.FunctionOpenRow(adapted.Body.Type().(*types.TFun)) {
		t.Fatal("curried result lost its residual callback ABI")
	}
}
