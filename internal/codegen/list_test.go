package codegen

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
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

// The compiler's own derived eq and show for List delegate to the runtime
// rather than walking an emitted constructor struct, while keeping the name,
// owner, and generic signature the emitted versions have. That signature is
// what lets generic.go's element-op synthesis stay untouched.
//
// Nothing in the bundled library uses `$eq`/`$show` today, so this path has no
// fixture coverage; the native template below is the only way to reach it.
func TestDerivedEqAndShowForListDelegateToTheRuntime(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	adt := listADT(sup)
	listInt := &types.TCon{Unique: adt.Con.Unique, Name: adt.Con.Name, Args: []types.Type{b.Int}}

	eqTmpl := "$eq($1, $2)"
	showTmpl := "$show($1)"
	natives := map[string]*types.NativeInfo{
		"List.sameList": {Name: "List.sameList", Module: "Main", Arity: 2, Template: &eqTmpl,
			Scheme: types.Scheme{Body: &types.TFun{Arg: listInt, Ret: &types.TFun{Arg: listInt, Ret: b.Bool}}}},
		"List.showList": {Name: "List.showList", Module: "Main", Arity: 1, Template: &showTmpl,
			Scheme: types.Scheme{Body: &types.TFun{Arg: listInt, Ret: b.String}}},
	}
	xs := &core.VarRef{Name: "xs", Local: true, Ty: listInt}
	ys := &core.VarRef{Name: "ys", Local: true, Ty: listInt}
	p := &core.Prog{
		Entry: "Main.main", ADTs: []*types.ADTInfo{adt}, Natives: natives,
		Defs: []core.Def{
			{Name: "Main.main", Owner: "Main", Params: []string{"_"}, ParamCaptures: []types.CaptureVar{1},
				Type: &types.TFun{Arg: b.Unit, Ret: b.Unit}, Body: &core.UnitLit{Ty: b.Unit}},
			{Name: "List.same", Owner: "List", Params: []string{"xs", "ys"}, ParamCaptures: []types.CaptureVar{1, 2},
				Type: &types.TFun{Arg: listInt, Ret: &types.TFun{Arg: listInt, Ret: b.Bool}},
				Body: &core.NativeCall{Name: "List.sameList", Module: "List", Args: []core.Expr{xs, ys}, Ty: b.Bool}},
			{Name: "List.render", Owner: "List", Params: []string{"xs"}, ParamCaptures: []types.CaptureVar{1},
				Type: &types.TFun{Arg: listInt, Ret: b.String},
				Body: &core.NativeCall{Name: "List.showList", Module: "List", Args: []core.Expr{xs}, Ty: b.String}},
		},
	}

	// A definition belongs to its source module, so the derived pair is
	// emitted by List and the uses by Main.
	emit := func(u Unit) string {
		t.Helper()
		data, err := emitUnit(p, b, u, false)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	list := emit(Unit{Name: "List"})
	emit(Unit{Name: "Main", Program: "Main", Entry: true}) // the entry unit must still emit

	for _, c := range []struct{ where, got, want string }{
		// The same exported names and generic signature an emitted pair has.
		{"List", list, "func EqT_List_dot_List[A0 any](eq0 func(A0, A0) bool, a, b fangort.List[A0]) bool"},
		{"List", list, "return fangort.ListEq(eq0, a, b)"},
		{"List", list, "func ShowT_List_dot_List[A0 any](show0 func(A0, bool) string, v fangort.List[A0], nested bool) string"},
		{"List", list, "return fangort.ListShow(show0, v, nested)"},
		// Because that signature did not move, the call-site element-op
		// synthesis is unchanged: it instantiates at the element type and
		// passes the ordinary scalar helpers.
		{"List", list, "EqT_List_dot_List[int64](eqInt, v_xs, v_ys)"},
		{"List", list, "ShowT_List_dot_List[int64](showInt, v_xs, false)"},
	} {
		if !strings.Contains(c.got, c.want) {
			t.Fatalf("generated %s is missing %q:\n%s", c.where, c.want, c.got)
		}
	}
	for _, unwanted := range []string{"type T_List_dot_List", "isT_List_dot_List", "C_List_dot_Cons", "C_List_dot_Nil"} {
		if strings.Contains(list, unwanted) {
			t.Fatalf("List still emits %q; it has a runtime representation:\n%s", unwanted, list)
		}
	}
}
