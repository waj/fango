package infer

import (
	"testing"

	"github.com/waj/fango/internal/types"
)

func matchTestTypes() (box func(types.Type) *types.TCon, pair func(a, b types.Type) *types.TCon, intT, boolT *types.TCon, rigid func(id int) *types.TVar, meta func(id int) *types.TVar) {
	box = func(a types.Type) *types.TCon { return &types.TCon{Unique: 100, Name: "Box", Args: []types.Type{a}} }
	pair = func(a, b types.Type) *types.TCon {
		return &types.TCon{Unique: 101, Name: "Pair", Args: []types.Type{a, b}}
	}
	intT = &types.TCon{Unique: 1, Name: "Int"}
	boolT = &types.TCon{Unique: 2, Name: "Bool"}
	rigid = func(id int) *types.TVar { return &types.TVar{ID: id, Rigid: true} }
	meta = func(id int) *types.TVar { return &types.TVar{ID: id} }
	return
}

func TestMatchHead(t *testing.T) {
	box, pair, intT, boolT, rigid, meta := matchTestTypes()
	a, b := rigid(10), rigid(11)
	for _, tc := range []struct {
		name string
		pat  types.Type
		ty   types.Type
		want headMatch
	}{
		{"var binds concrete", box(a), box(intT), headYes},
		{"var binds metavariable", box(a), box(meta(50)), headYes},
		{"concrete matches", box(intT), box(intT), headYes},
		{"concrete mismatch", box(intT), box(boolT), headNo},
		{"concrete vs metavariable", box(intT), box(meta(50)), headBlocked},
		{"concrete vs rigid skolem", box(intT), box(rigid(50)), headNo},
		{"nested match", box(pair(intT, a)), box(pair(intT, boolT)), headYes},
		{"nested mismatch", box(pair(intT, a)), box(pair(boolT, boolT)), headNo},
		{"nested blocked", box(pair(intT, a)), box(pair(meta(50), boolT)), headBlocked},
		{"repeated var consistent", pair(a, a), pair(intT, intT), headYes},
		{"repeated var inconsistent", pair(a, a), pair(intT, boolT), headNo},
		{"repeated var undecided", pair(a, a), pair(intT, meta(50)), headBlocked},
		{"repeated distinct metas undecided", pair(a, a), pair(meta(50), meta(51)), headBlocked},
		{"repeated same meta consistent", pair(a, a), pair(meta(50), meta(50)), headYes},
	} {
		if got := matchHead(tc.pat, tc.ty, map[int]types.Type{}); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	m := map[int]types.Type{}
	if matchHead(pair(a, b), pair(intT, box(boolT)), m) != headYes {
		t.Fatal("expected match")
	}
	if !types.Equal(m[a.ID], intT) || !types.Equal(m[b.ID], box(boolT)) {
		t.Errorf("bindings not recorded: %v", m)
	}
}

func TestHeadsUnify(t *testing.T) {
	box, pair, intT, boolT, rigid, _ := matchTestTypes()
	a, b := rigid(10), rigid(11)
	for _, tc := range []struct {
		name string
		x, y types.Type
		want bool
	}{
		{"incomparable but unifiable", pair(intT, a), pair(b, intT), true},
		{"disjoint concrete", box(intT), box(boolT), false},
		{"generic vs specific", box(a), box(intT), true},
		{"alpha equivalent", box(a), box(b), true},
		{"occurs check", box(a), box(box(a)), false},
	} {
		if got := headsUnify(tc.x, tc.y); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestInstanceKeyDistinguishesBuiltinUnit(t *testing.T) {
	box, _, _, _, _, _ := matchTestTypes()
	builtin := &types.TCon{Unique: 1, Name: "()"}
	user := &types.TCon{Unique: 2, Name: "Unit"}
	if canonicalHeadKey(builtin) == canonicalHeadKey(user) || canonicalHeadKey(box(builtin)) == canonicalHeadKey(box(user)) {
		t.Fatal("builtin Unit and a nominal type named Unit share an instance symbol")
	}
}

func TestHeadSpecificity(t *testing.T) {
	box, pair, intT, _, rigid, _ := matchTestTypes()
	a, b, c, d := rigid(10), rigid(11), rigid(12), rigid(13)
	if !headAtLeastAsSpecific(box(intT), box(a)) {
		t.Error("Box Int should be at least as specific as Box a")
	}
	if headAtLeastAsSpecific(box(a), box(intT)) {
		t.Error("Box a should not be at least as specific as Box Int")
	}
	if !headAtLeastAsSpecific(pair(a, a), pair(c, d)) || headAtLeastAsSpecific(pair(c, d), pair(a, a)) {
		t.Error("Pair a a should be strictly more specific than Pair c d")
	}
	if !headAtLeastAsSpecific(box(a), box(b)) || !headAtLeastAsSpecific(box(b), box(a)) {
		t.Error("alpha-equivalent heads should be equally specific")
	}
}

func TestContextIdentityAndInclusion(t *testing.T) {
	_, pair, _, _, rigid, _ := matchTestTypes()
	a, b, c, d := rigid(100), rigid(101), rigid(102), rigid(103)
	head, renamed := pair(a, b), pair(c, d)
	left := []types.Pred{{Class: "Show", Ty: a}}
	right := []types.Pred{{Class: "Show", Ty: b}}
	both := append(append([]types.Pred{}, left...), right...)
	if canonicalContextKey(head, left) == canonicalContextKey(head, right) {
		t.Fatal("different head variables share a context symbol")
	}
	if canonicalContextKey(head, both) != canonicalContextKey(renamed, []types.Pred{{Class: "Show", Ty: d}, {Class: "Show", Ty: c}}) {
		t.Fatal("alpha-renaming or context order changed identity")
	}
	if !contextIncludes(head, both, renamed, []types.Pred{{Class: "Show", Ty: c}}) || contextIncludes(head, left, head, both) {
		t.Fatal("incorrect subset ordering")
	}
}
