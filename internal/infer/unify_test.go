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

// Effect inclusion must not depend on the order the constraints arrive in. A
// body that calls a `{e}` function before an `{A | e}` one produces `{e} ⊆ ρ`
// first; solving that eagerly would bind ρ := e and leave nowhere to put `A`.
func TestInclusionIntoOpenRowIsOrderIndependent(t *testing.T) {
	label := types.EffLabel{Unique: 10, Name: "A"}
	for _, name := range []string{"plain first", "labelled first"} {
		t.Run(name, func(t *testing.T) {
			sup := &types.Supply{}
			b := types.NewBuiltins(sup)
			e := sup.FreshRigid(types.RowVar)
			ambient := types.Row{Tail: sup.FreshVar(types.RowVar)}
			plain := Constraint{Left: types.Row{Tail: e}, Right: ambient, Why: Why{Kind: WhyCall}, Include: true}
			labelled := Constraint{Left: types.Row{Labels: []types.EffLabel{label}, Tail: e}, Right: ambient, Why: Why{Kind: WhyCall}, Include: true}
			cs := []Constraint{plain, labelled}
			if name == "labelled first" {
				cs = []Constraint{labelled, plain}
			}
			sub, _, errs := Solve(cs, nil, Subst{}, b, sup)
			if len(errs) > 0 {
				t.Fatalf("solve: %v", errs)
			}
			got := sub.Apply(ambient)
			want := types.Row{Labels: []types.EffLabel{label}, Tail: e}
			if !types.Equal(got, want) {
				t.Fatalf("ambient row = %s, want %s", types.Show(got), types.Show(want))
			}
		})
	}
}

// The surrounding row still closes onto the annotation's tail when nothing
// else contributes a label, so a body that only performs `{e}` keeps it.
func TestInclusionClosesOnAnnotationTailWhenUnconstrained(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	e := sup.FreshRigid(types.RowVar)
	ambient := types.Row{Tail: sup.FreshVar(types.RowVar)}
	c := Constraint{Left: types.Row{Tail: e}, Right: ambient, Why: Why{Kind: WhyCall}, Include: true}
	sub, _, errs := Solve([]Constraint{c}, nil, Subst{}, b, sup)
	if len(errs) > 0 {
		t.Fatalf("solve: %v", errs)
	}
	if got := sub.Apply(ambient); !types.Equal(got, types.Row{Tail: e}) {
		t.Fatalf("ambient row = %s, want the annotation tail", types.Show(got))
	}
}

// An inclusion whose row has both labels and a rigid tail contributes its
// labels immediately and its tail last, so a later `{IO}`-style call can
// still widen the surrounding row.
func TestInclusionWithLabelsLeavesRoomForMore(t *testing.T) {
	note := types.EffLabel{Unique: 10, Name: "Note"}
	io := types.EffLabel{Unique: 11, Name: "IO"}
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	e := sup.FreshRigid(types.RowVar)
	ambient := types.Row{Tail: sup.FreshVar(types.RowVar)}
	callee := Constraint{Left: types.Row{Labels: []types.EffLabel{note}, Tail: e}, Right: ambient, Why: Why{Kind: WhyCall}, Include: true}
	printing := Constraint{Left: types.Row{Labels: []types.EffLabel{io}}, Right: ambient, Why: Why{Kind: WhyCall}, Include: true}
	sub, _, errs := Solve([]Constraint{callee, printing}, nil, Subst{}, b, sup)
	if len(errs) > 0 {
		t.Fatalf("solve: %v", errs)
	}
	got := sub.Apply(ambient)
	want := types.Row{Labels: []types.EffLabel{note, io}, Tail: e}
	if !types.Equal(got, want) {
		t.Fatalf("ambient row = %s, want %s", types.Show(got), types.Show(want))
	}
}
