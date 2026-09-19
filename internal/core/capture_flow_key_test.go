package core

import (
	"encoding/json"
	"testing"

	"github.com/waj/fango/internal/types"
)

// The sharing key encodes a type substitution one type at a time and memoizes
// the result, so it must agree with encoding the whole map at once — including
// key order, the nil map, and Row, which is a value type holding a slice and so
// cannot be memoized by identity like the pointer-shaped types.
func TestFlowKeyTypeEncodingMatchesJSON(t *testing.T) {
	a := &captureAnalyzer{}
	row := types.Row{Labels: []types.EffLabel{{Name: "Fail"}}}
	for _, substitution := range []map[int]types.Type{
		nil,
		{},
		{1: &types.TCon{Unique: 7, Name: "Int"}},
		{1: &types.TVar{ID: 99, Rigid: true}},
		// Keys are ordered by their quoted form, so a two-digit key sorts
		// before a single-digit one.
		{1: &types.TCon{Unique: 7, Name: "Int"}, 10: row, 2: &types.TVar{ID: 3}},
		{4: &types.TFun{Arg: &types.TCon{Unique: 7, Name: "Int"}, Eff: row, Ret: row}},
	} {
		want, err := json.Marshal(substitution)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(a.appendTypes(nil, substitution)); got != string(want) {
			t.Errorf("appendTypes(%v) = %s, want %s", substitution, got, want)
		}
	}
}

// Memoizing a type's encoding must not let two distinct types share one entry.
func TestFlowKeyTypeMemoDistinguishes(t *testing.T) {
	a := &captureAnalyzer{}
	first := string(a.typeJSON(&types.TCon{Unique: 7, Name: "Int"}))
	second := string(a.typeJSON(&types.TCon{Unique: 8, Name: "Int"}))
	if first == second {
		t.Errorf("distinct nominal uniques encoded alike: %s", first)
	}
	rigid := string(a.typeJSON(&types.TVar{ID: 99, Rigid: true}))
	if loose := string(a.typeJSON(&types.TVar{ID: 99})); rigid == loose {
		t.Errorf("rigid and inference variables encoded alike: %s", rigid)
	}
}
