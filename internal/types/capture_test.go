package types

import "testing"

func TestCaptureSetUnionAndSubstitution(t *testing.T) {
	got := UnionCaptures(
		CaptureSet{Scopes: []ScopeID{3, 1}, Vars: []CaptureVar{4}},
		CaptureSet{Scopes: []ScopeID{1, 2}, Vars: []CaptureVar{5, 4}},
	)
	want := CaptureSet{Scopes: []ScopeID{1, 2, 3}, Vars: []CaptureVar{4, 5}}
	if !EqualCaptures(got, want) {
		t.Fatalf("union = %#v, want %#v", got, want)
	}
	got = CaptureSet{Scopes: []ScopeID{1}, Vars: []CaptureVar{4, 5}}.Substitute(map[CaptureVar]CaptureSet{4: ScopeCapture(9)})
	want = CaptureSet{Scopes: []ScopeID{1, 9}, Vars: []CaptureVar{5}}
	if !EqualCaptures(got, want) {
		t.Fatalf("substitution = %#v, want %#v", got, want)
	}
}

func TestCaptureSetWithoutBoundIdentities(t *testing.T) {
	got := (CaptureSet{Scopes: []ScopeID{1, 2}, Vars: []CaptureVar{3, 4}}).Without(
		map[ScopeID]bool{1: true}, map[CaptureVar]bool{4: true})
	want := CaptureSet{Scopes: []ScopeID{2}, Vars: []CaptureVar{3}}
	if !EqualCaptures(got, want) {
		t.Fatalf("without = %#v, want %#v", got, want)
	}
}
