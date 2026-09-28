package fangort

import (
	"fmt"
	"strings"
	"testing"
)

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
				want = "incompatible inherited handler Fail"
			}
			row := ExtendEvidenceRow(nil, EvidenceBinding{Name: origin.Name, Family: EvidenceFamily{Origin: origin}})
			deferred := DeferredEvidenceOrigin(row, origin.Name)
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
