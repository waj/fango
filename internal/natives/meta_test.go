package natives

import (
	"testing"

	"github.com/waj/fango/internal/types"
)

// A row variable instantiated at a row splices into the row it ends, so a
// field `A ->{State, e} B` reflected at `e := {Console}` reads as one closed
// row, and at an open row keeps that row's own tail.
func TestFlattenRowsSplicesRowTails(t *testing.T) {
	console := types.EffLabel{Unique: 10, Name: "Console"}
	state := types.EffLabel{Unique: 11, Name: "State"}
	e := &types.TVar{ID: 1, Kind: types.RowVar, Rigid: true}
	open := &types.TVar{ID: 2, Kind: types.RowVar}
	field := &types.TFun{Arg: &types.TCon{Name: "Int"}, Eff: types.Row{Labels: []types.EffLabel{state}, Tail: e}, Ret: &types.TCon{Name: "Int"}}
	for _, row := range []types.Row{{}, {Labels: []types.EffLabel{console}}, {Labels: []types.EffLabel{console}, Tail: open}} {
		got := flattenRows(types.SubstRigid(field, map[int]types.Type{e.ID: row})).(*types.TFun)
		if _, nested := got.Eff.Tail.(types.Row); nested {
			t.Errorf("tail is still a row: %#v", got.Eff)
		}
		if got.Eff.Tail != row.Tail || len(got.Eff.Labels) != len(row.Labels)+1 {
			t.Errorf("flattened at %v = %s", row, types.Show(got))
		}
	}
}
