package core

import (
	"github.com/waj/fango/internal/types"
	"strings"
	"testing"
)

func TestWorkSourceCallCannotEraseExecutableEffects(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	io := types.Row{Labels: []types.EffLabel{{Unique: 101, Name: "IO.IO"}}}
	runtime := &types.TFun{Arg: b.Unit, Eff: io, Ret: b.Unit}
	claimed := &types.TFun{Arg: b.Unit, Ret: b.Unit}
	p := &Prog{Intrinsics: map[string]bool{types.WorkRunName: true}, Defs: []Def{{
		Name: "main", Type: runtime, SourceType: runtime, Params: []string{"unit"},
		Body: &App{Callee: &VarRef{Name: "print", Ty: runtime}, Args: []Expr{&UnitLit{Ty: b.Unit}}, Ty: b.Unit, SourceType: claimed},
	}}}
	errs := SourceEffectErrors(p)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "source call proof disagrees") {
		t.Fatalf("forged pure call accepted: %v", errs)
	}
	// A generic open-row ABI legitimately omits a call's concrete IO label;
	// the separately checked source row still charges it to the caller.
	open := &types.TFun{Arg: b.Unit, Ret: b.Unit, OpenRow: true}
	if !sourceValueRepresentation(runtime, open, true) || sourceValueRepresentation(claimed, runtime, true) {
		t.Fatal("source/runtime row instantiation is not directional")
	}
}

func TestWorkRowProofNeverTreatsUnknownAsEmpty(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	e := sup.FreshRigid(types.RowVar)
	other := sup.FreshRigid(types.RowVar)
	fail := func(t types.Type) types.Row {
		return types.Row{Labels: []types.EffLabel{{Unique: 100, Name: "Fail.Fail", Args: []types.Type{t}, Abort: true}}}
	}
	for _, tc := range []struct {
		name         string
		budget, need types.Type
		want         bool
	}{
		{"empty", types.Row{}, types.Row{}, true},
		{"unknown need", types.Row{}, e, false},
		{"unknown budget", e, fail(b.Int), false},
		{"same symbolic row", e, e, true},
		{"different symbolic rows", e, other, false},
		{"label missing", types.Row{}, fail(b.Int), false},
		{"label included", fail(b.Int), fail(b.Int), true},
		{"substituted nested row", types.Row{Tail: fail(b.Int)}, fail(b.Int), true},
		{"nominal parameter conflict", fail(b.Int), fail(b.String), false},
		{"missing proof", nil, types.Row{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := workRowIncludes(tc.budget, tc.need); got != tc.want {
				t.Fatalf("includes=%v want %v", got, tc.want)
			}
		})
	}
}

func TestWorkSourceRowRetainsNominalArgumentIndex(t *testing.T) {
	sup := &types.Supply{}
	e := sup.FreshRigid(types.RowVar)
	child := types.Row{Labels: []types.EffLabel{{Unique: 1, Name: "Fail.Fail"}}}
	driver := types.Row{Labels: append(append([]types.EffLabel(nil), child.Labels...), types.EffLabel{Unique: 2, Name: types.CoroutineDriveName, Suspension: true})}
	con := func(row types.Type) types.Type {
		return &types.TCon{Unique: 3, Name: types.CoroutineTypeName, Args: []types.Type{row}}
	}
	pattern := &types.TFun{Arg: con(e), Eff: types.Row{Tail: e}}
	actual := &types.TFun{Arg: con(child), Eff: driver}
	m := map[int]types.Type{}
	matchSourceRows(pattern, actual, m)
	if !types.Equal(m[e.ID], child) {
		t.Fatalf("stored row was widened to %s", types.Show(m[e.ID]))
	}
}
