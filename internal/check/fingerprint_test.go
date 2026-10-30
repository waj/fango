package check

import (
	"testing"

	"github.com/waj/fango/internal/types"
)

// The first variable a session allocates has ID 0, and which declaration gets
// it depends on whether the session checked or decoded its Prelude.
func TestFingerprintNumbersTheZeroTypeVariable(t *testing.T) {
	digest := func(ids ...int) string {
		var vars []*types.TVar
		for _, id := range ids {
			vars = append(vars, &types.TVar{ID: id, Rigid: true})
		}
		return canonicalDigest(vars, &ModuleObject{})
	}
	if digest(0) != digest(7) {
		t.Fatal("a variable numbered 0 hashes differently from any other")
	}
	if digest(0, 7) != digest(7, 3) {
		t.Fatal("fresh numbering changed fingerprint")
	}
	if digest(0, 7) == digest(0, 0) {
		t.Fatal("distinct variables have the fingerprint of a shared one")
	}
}

func TestFingerprintKeepsAbsentCaptureDistinct(t *testing.T) {
	digest := func(ids ...types.CaptureVar) string { return canonicalDigest(ids, &ModuleObject{}) }
	if digest(0, 5) == digest(5, 5) {
		t.Fatal("an absent capture hashes like an allocated one")
	}
}

func TestFingerprintIgnoresNominalRepresentation(t *testing.T) {
	type pair struct{ A, B *types.TCon }
	digest := func(v pair) string { return canonicalDigest(v, &ModuleObject{}) }
	shared := &types.TCon{Unique: 1, Name: "M.T"}
	if digest(pair{shared, shared}) != digest(pair{&types.TCon{Unique: 2, Name: "M.T"}, &types.TCon{Unique: 2, Name: "M.T"}}) {
		t.Fatal("sharing a nominal node changed the fingerprint")
	}
	empty := &types.TCon{Name: "M.T", Args: []types.Type{}}
	if digest(pair{A: empty}) != digest(pair{A: &types.TCon{Name: "M.T"}}) {
		t.Fatal("an empty argument list hashes differently from a nil one")
	}
	if digest(pair{A: &types.TCon{Name: "M.T"}}) == digest(pair{A: &types.TCon{Name: "M.U"}}) {
		t.Fatal("distinct nominals have one fingerprint")
	}
}
