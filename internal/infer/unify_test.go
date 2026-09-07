package infer

// Unification's own unit tests reach into the solver directly, so they stay
// inside the package. Everything that installs the prelude lives in the
// external test package, which may import the compile-time evaluator.

import (
	"testing"

	"github.com/waj/fango/internal/types"
)

func TestOpenRowUnification(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	e1, e2 := sup.FreshVar(types.RowVar), sup.FreshVar(types.RowVar)
	a := types.EffLabel{Unique: 10, Name: "A"}
	bb := types.EffLabel{Unique: 11, Name: "B"}
	sub := Subst{}
	if m := unify(types.Row{Labels: []types.EffLabel{a}, Tail: e1}, types.Row{Labels: []types.EffLabel{bb}, Tail: e2}, sub, b, sup); m != nil {
		t.Fatalf("unify open rows: %v", m)
	}
	left := sub.Apply(types.Row{Labels: []types.EffLabel{a}, Tail: e1})
	right := sub.Apply(types.Row{Labels: []types.EffLabel{bb}, Tail: e2})
	if !types.Equal(left, right) {
		t.Fatalf("rows did not converge: %v != %v", left, right)
	}
}

// Class constraints are independent of unification's ordinary type kind.
func TestOrdinaryVariablesHaveNoNumericKind(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	for _, ty := range []types.Type{b.Int, b.Float, b.String, b.Bool} {
		if m := unify(sup.FreshVar(types.General), ty, Subst{}, b, sup); m != nil {
			t.Errorf("ordinary variable should unify with %s", types.Show(ty))
		}
	}
}

func TestUnifyOccurs(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	sub := Subst{}
	v := sup.FreshVar(types.General)
	fn := &types.TFun{Arg: v, Ret: b.Int}
	if m := unify(v, fn, sub, b, &types.Supply{}); m == nil {
		t.Error("occurs check should reject v ~ (v -> Int)")
	}
}

func TestUnifyTConIdentityIsUnique(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	sub := Subst{}
	// Same name, different unique — a redefined REPL type must not unify.
	otherInt := &types.TCon{Unique: sup.NextUnique(), Name: "Int"}
	if m := unify(b.Int, otherInt, sub, b, &types.Supply{}); m == nil {
		t.Error("TCons with equal names but different uniques must not unify")
	}
}
