package fangort

import (
	"fmt"
	"strings"
	"testing"
)

func TestFixedEvidenceForkPreservesStateAndHonorsOverrides(t *testing.T) {
	origin := &EvidenceOrigin{Name: "Local"}
	shared := NewHandlerState(1)
	origin.Fixed = NewEvidenceBinding(EvidenceFamily{Origin: origin, Direct: shared})
	row := ExtendEvidenceValue(EvidenceValue{}, origin.Fixed)
	child := NewEvidenceFork(nil).Value(row)
	if ValueEvidence[*HandlerState[int]](child, "Local", DirectEvidence) != shared {
		t.Fatal("fixed state was copied")
	}
	replacement := &EvidenceOrigin{Name: "Local"}
	other := NewHandlerState(2)
	overrides := ExtendEvidenceRow(nil, EvidenceBinding{Name: "Local", Family: EvidenceFamily{Origin: replacement, Direct: other}})
	if ForkEvidence[*HandlerState[int]](NewEvidenceFork(overrides), origin, DirectEvidence) != other {
		t.Fatal("fixed family bypassed an override")
	}
}

func TestEvidenceForkRebasesDependenciesAndPreservesAliases(t *testing.T) {
	parentFail := &EvidenceOrigin{Name: "Fail", Arguments: []*TypeDescriptor{NominalType("String", true)}}
	childFail := &EvidenceOrigin{Name: "Fail", Arguments: []*TypeDescriptor{NominalType("String", true)}}
	replacement := EvidenceFamily{Origin: childFail, Direct: 42, Exit: 42}
	builds := 0
	shared := NewHandlerState(7)
	database := &EvidenceOrigin{Name: "Database"}
	database.Rebuild = func(f *EvidenceFork) EvidenceFamily {
		builds++
		if ForkEvidence[int](f, parentFail, DirectEvidence) != 42 {
			t.Fatal("wrong failure boundary")
		}
		return EvidenceFamily{Origin: database, Direct: shared, Exit: shared}
	}
	row := ExtendEvidenceRow(nil, EvidenceBinding{Name: "first", Family: EvidenceFamily{Origin: database}}, EvidenceBinding{Name: "alias", Family: EvidenceFamily{Origin: database}})
	fork := NewEvidenceFork(ExtendEvidenceRow(nil, EvidenceBinding{Name: "Fail", Family: replacement}))
	rebuilt := fork.Row(row)
	first := RowEvidence[*HandlerState[int]](rebuilt, "first", DirectEvidence)
	alias := RowEvidence[*HandlerState[int]](rebuilt, "alias", DirectEvidence)
	if first != shared || alias != shared || builds != 1 {
		t.Fatal("activation aliases or state were copied")
	}
	first.Store(9)
	if shared.Snapshot() != 9 {
		t.Fatal("state is not shared")
	}
}

func TestEvidenceForkRejectsUnsupportedAndMismatchedAborts(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(fmt.Sprint(mismatch), func(t *testing.T) {
			var overrides *EvidenceRow
			origin := &EvidenceOrigin{Name: "Stop"}
			want := "cannot inherit abort handler Stop"
			if mismatch {
				origin = &EvidenceOrigin{Name: "Fail", Arguments: []*TypeDescriptor{NominalType("Int", true)}}
				replacement := &EvidenceOrigin{Name: "Fail", Arguments: []*TypeDescriptor{NominalType("String", true)}}
				overrides = ExtendEvidenceRow(nil, EvidenceBinding{Name: "Fail", Family: EvidenceFamily{Origin: replacement}})
				want = "cannot inherit abort handler Fail"
			}
			row := ExtendEvidenceRow(nil, EvidenceBinding{Name: origin.Name, Family: EvidenceFamily{Origin: origin}})
			deferred := DeferredEvidenceOrigin(row, origin.Name, origin.Arguments...)
			defer func() {
				if got := fmt.Sprint(recover()); !strings.Contains(got, want) {
					t.Fatalf("panic %q, want %q", got, want)
				}
			}()
			NewEvidenceFork(overrides).Family(deferred)
			t.Fatal("unsupported abort inherited")
		})
	}
}

func TestEvidenceForkIgnoresShadowedRows(t *testing.T) {
	old := &EvidenceOrigin{Name: "Database"}
	current := &EvidenceOrigin{Name: "Database"}
	current.Rebuild = func(*EvidenceFork) EvidenceFamily { return EvidenceFamily{Origin: current, Direct: 1} }
	outer := ExtendEvidenceRow(nil, EvidenceBinding{Name: "Database", Family: EvidenceFamily{Origin: old}})
	inner := ExtendEvidenceRow(outer, EvidenceBinding{Name: "Database", Family: EvidenceFamily{Origin: current}})
	if RowEvidence[int](NewEvidenceFork(nil).Row(inner), "Database", DirectEvidence) != 1 {
		t.Fatal("wrong shadowed activation")
	}
}

func TestEvidenceForkPreservesAllInlineAndOverflowBindings(t *testing.T) {
	var bindings []EvidenceBinding
	for index, name := range []string{"A", "B", "C"} {
		origin := &EvidenceOrigin{Name: name}
		value := index + 1
		origin.Rebuild = func(*EvidenceFork) EvidenceFamily {
			return EvidenceFamily{Origin: origin, Direct: value}
		}
		bindings = append(bindings, EvidenceBinding{Name: name, Family: EvidenceFamily{Origin: origin}})
	}
	row := NewEvidenceFork(nil).Row(ExtendEvidenceRow(nil, bindings...))
	for index, binding := range bindings {
		if got := RowEvidence[int](row, binding.Name, DirectEvidence); got != index+1 {
			t.Fatalf("%s = %d", binding.Name, got)
		}
	}
}

func TestEvidenceForkPreservesDistinctApplicationsOfOneEffect(t *testing.T) {
	intType, stringType := NominalType("Int", true), NominalType("String", true)
	makeOrigin := func(arg *TypeDescriptor, value int) *EvidenceOrigin {
		origin := &EvidenceOrigin{Name: "Read", Arguments: []*TypeDescriptor{arg}}
		origin.Rebuild = func(*EvidenceFork) EvidenceFamily { return EvidenceFamily{Origin: origin, Direct: value} }
		return origin
	}
	first, second := makeOrigin(intType, 1), makeOrigin(stringType, 2)
	row := ExtendEvidenceRow(nil,
		EvidenceBinding{Name: "Read", Arguments: first.Arguments, Family: EvidenceFamily{Origin: first, Direct: 1}},
		EvidenceBinding{Name: "Read", Arguments: second.Arguments, Family: EvidenceFamily{Origin: second, Direct: 2}},
	)
	child := NewEvidenceFork(nil).Row(row)
	if RowEvidence[int](child, "Read", DirectEvidence, intType) != 1 || RowEvidence[int](child, "Read", DirectEvidence, stringType) != 2 {
		t.Fatal("fork merged distinct effect applications")
	}
}

func TestShareEvidencePublishesDependenciesAndGuardsInheritance(t *testing.T) {
	cell := NewHandlerState(0)
	dependency := &EvidenceOrigin{Name: "Dependency"}
	dependency.Share = cell.Share
	dependency.Fixed = NewEvidenceBinding(EvidenceFamily{Origin: dependency, Direct: cell})
	shares := 0
	database := &EvidenceOrigin{Name: "Database"}
	database.Share = func() {
		shares++
		ShareOrigin(dependency)
	}
	database.Rebuild = func(*EvidenceFork) EvidenceFamily {
		return EvidenceFamily{Origin: database, Direct: cell, Exit: cell}
	}
	row := ExtendEvidenceValue(EvidenceValue{}, NewEvidenceBinding(EvidenceFamily{Origin: database}))
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("inherited an unpublished activation")
			}
		}()
		NewEvidenceFork(nil).Family(database)
	}()
	ShareEvidenceValue(row)
	ShareEvidenceValue(row)
	if shares != 1 || !cell.shared || !dependency.shared {
		t.Fatalf("publication ran %d times; cell %v dependency %v", shares, cell.shared, dependency.shared)
	}
	fork := NewEvidenceFork(nil)
	if fork.Family(database).Direct != cell || fork.Family(dependency).Direct != cell {
		t.Fatal("published activations were not inherited")
	}
	deferred := DeferredEvidenceOrigin(ExtendEvidenceRow(nil, EvidenceBinding{Name: "Local", Family: EvidenceFamily{Origin: &EvidenceOrigin{Name: "Local", Share: cell.Share}}}), "Local")
	ShareOrigin(deferred)
	if resolved := deferred.Resolve(); !resolved.shared {
		t.Fatal("deferred origin was not followed")
	}
}
