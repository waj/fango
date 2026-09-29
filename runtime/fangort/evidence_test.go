package fangort

import (
	"testing"
)

func TestEvidenceRowsDistinguishAppliedEffects(t *testing.T) {
	intType := NominalType("Int", true)
	stringType := NominalType("String", true)
	row := ExtendEvidenceRow(nil,
		EvidenceBinding{Name: "Read", Arguments: []*TypeDescriptor{intType}, Family: EvidenceFamily{Direct: 42}},
		EvidenceBinding{Name: "Read", Arguments: []*TypeDescriptor{stringType}, Family: EvidenceFamily{Direct: "text"}},
	)
	if got := RowEvidence[int](row, "Read", DirectEvidence, intType); got != 42 {
		t.Fatalf("Int = %d", got)
	}
	if got := RowEvidence[string](row, "Read", DirectEvidence, stringType); got != "text" {
		t.Fatalf("String = %q", got)
	}
}

func TestEvidenceRowsShadowWithoutMutatingLexicalBindings(t *testing.T) {
	outer := ExtendEvidenceRow(nil, EvidenceBinding{Name: "Reader", Family: EvidenceFamily{Direct: 1}})
	inner := ExtendEvidenceRow(outer, EvidenceBinding{Name: "Reader", Family: EvidenceFamily{Direct: 2}})
	if RowEvidence[int](outer, "Reader", DirectEvidence) != 1 || RowEvidence[int](inner, "Reader", DirectEvidence) != 2 {
		t.Fatal("row extension changed lexical evidence")
	}
	if ExtendEvidenceRow(outer) != outer {
		t.Fatal("empty forwarding allocated another row")
	}
}

func TestEvidenceRowsWithMoreThanTwoBindings(t *testing.T) {
	bindings := []EvidenceBinding{
		{Name: "A", Family: EvidenceFamily{Direct: 1}},
		{Name: "B", Family: EvidenceFamily{Direct: 2}},
		{Name: "C", Family: EvidenceFamily{Direct: 3}},
	}
	row := ExtendEvidenceRow(nil, bindings...)
	for index, binding := range bindings {
		if got := RowEvidence[int](row, binding.Name, DirectEvidence); got != index+1 {
			t.Fatalf("%s = %d", binding.Name, got)
		}
	}
}
