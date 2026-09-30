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

func TestSharedEvidenceBindingReuseAndShadowing(t *testing.T) {
	a := &EvidenceBinding{Name: "Read", Family: EvidenceFamily{Direct: 1}}
	b := &EvidenceBinding{Name: "Read", Family: EvidenceFamily{Direct: 2}}
	outer := ExtendEvidenceBindings(nil, a)
	if got := ExtendEvidenceBindings(outer, a); got != outer {
		t.Fatal("identical visible binding allocated an overlay")
	}
	if allocs := testing.AllocsPerRun(100, func() { _ = ExtendEvidenceBindings(outer, a) }); allocs != 0 {
		t.Fatalf("forwarding allocated %g times", allocs)
	}
	inner := ExtendEvidenceBindings(outer, b)
	if got := ExtendEvidenceBindings(inner, a); got == inner || RowEvidence[int](got, "Read", DirectEvidence) != 1 {
		t.Fatal("shadowed binding incorrectly reused")
	}
	if RowEvidence[int](inner, "Read", DirectEvidence) != 2 || RowEvidence[int](outer, "Read", DirectEvidence) != 1 {
		t.Fatal("extension mutated published rows")
	}
}

func TestDuplicateSharedBindingRejectedBeforeReuse(t *testing.T) {
	binding := &EvidenceBinding{Name: "Read", Family: EvidenceFamily{Direct: 1}}
	row := ExtendEvidenceBindings(nil, binding)
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate binding bypassed validation")
		}
	}()
	ExtendEvidenceBindings(row, binding, binding)
}
