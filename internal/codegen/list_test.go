package codegen

import (
	"github.com/waj/fango/internal/types"
)

// listADT builds the bundled List's table rows as infer marks them.
func listADT(sup *types.Supply) *types.ADTInfo {
	param := &types.TVar{ID: sup.NextUnique(), Rigid: true}
	con := &types.TCon{Unique: sup.NextUnique(), Name: "List.List"}
	self := &types.TCon{Unique: con.Unique, Name: con.Name, Args: []types.Type{param}}
	adt := &types.ADTInfo{Con: con, Params: []*types.TVar{param}, ParamKindsKnown: []bool{true}, Repr: types.ReprList}
	adt.Ctors = []*types.CtorInfo{
		{Name: "List.Nil", Index: 0, Result: self, Repr: types.ReprList},
		{Name: "List.Cons", Index: 1, Fields: []types.Type{param, self}, Result: self, Repr: types.ReprList},
	}
	return adt
}
