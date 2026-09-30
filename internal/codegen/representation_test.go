package codegen

import (
	"testing"

	"github.com/waj/fango/internal/types"
)

func TestProductLayoutExpandsGenericFieldsAndBreaksCycles(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	a := &types.TVar{ID: sup.NextUnique(), Rigid: true}
	boxCon := &types.TCon{Unique: sup.NextUnique(), Name: "Main.Box"}
	box := &types.ADTInfo{Con: boxCon, Params: []*types.TVar{a}, Ctors: []*types.CtorInfo{{Name: "Main.Box", Result: boxCon, Fields: []types.Type{a}}}}
	selfCon := &types.TCon{Unique: sup.NextUnique(), Name: "Main.Self"}
	self := &types.ADTInfo{Con: selfCon, Ctors: []*types.CtorInfo{{Name: "Main.Self", Result: selfCon}}}
	list := listADT(sup)
	maybeCon := &types.TCon{Unique: sup.NextUnique(), Name: "Maybe.Maybe"}
	maybe := &types.ADTInfo{Con: maybeCon, Params: []*types.TVar{a}, Ctors: []*types.CtorInfo{{Name: "Maybe.Nothing", Result: maybeCon}, {Name: "Maybe.Just", Index: 1, Result: maybeCon, Fields: []types.Type{a}}}}
	g := &gen{b: b, adts: map[int]*types.ADTInfo{boxCon.Unique: box, selfCon.Unique: self, list.Con.Unique: list, maybeCon.Unique: maybe}}
	for _, tt := range []struct {
		name  string
		field types.Type
		value bool
	}{
		{"scalar", b.Int, true},
		{"direct recursion", selfCon, false},
		{"generic recursion", &types.TCon{Unique: boxCon.Unique, Name: boxCon.Name, Args: []types.Type{selfCon}}, false},
		{"tagged recursion", &types.TCon{Unique: maybeCon.Unique, Name: maybeCon.Name, Args: []types.Type{selfCon}}, false},
		{"list indirection", &types.TCon{Unique: list.Con.Unique, Name: list.Con.Name, Args: []types.Type{selfCon}}, true},
		{"function indirection", &types.TFun{Arg: b.Unit, Ret: selfCon}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			self.Ctors[0].Fields = []types.Type{tt.field}
			if got := g.valueProduct(self); got != tt.value {
				t.Fatalf("value layout = %v, want %v", got, tt.value)
			}
		})
	}
	if !g.valueProduct(box) {
		t.Fatal("generic Box itself must have a value layout")
	}
	callback := &types.TFun{Arg: b.Unit, Ret: b.Int}
	self.Ctors[0].Fields = []types.Type{callback, callback, callback}
	if g.valueProduct(self) {
		t.Fatal("large callable bundle should be shared by pointer")
	}
}
