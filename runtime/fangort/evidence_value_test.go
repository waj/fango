package fangort

import "testing"

var evidenceValueSink EvidenceValue

func TestEvidenceValueInlineAllocationAndShadowing(t *testing.T) {
	a := &EvidenceBinding{Name: "A", Family: EvidenceFamily{Direct: 1}}
	b := &EvidenceBinding{Name: "B", Family: EvidenceFamily{Direct: 2}}
	shadow := &EvidenceBinding{Name: "A", Family: EvidenceFamily{Direct: 3}}
	outer := ExtendEvidenceValue(EvidenceValue{}, a, b)
	if n := testing.AllocsPerRun(100, func() { evidenceValueSink = ExtendEvidenceValue(outer, shadow) }); n != 0 {
		t.Fatalf("inline shadow allocated %g", n)
	}
	if ValueEvidence[int](outer, "A", DirectEvidence) != 1 || ValueEvidence[int](evidenceValueSink, "A", DirectEvidence) != 3 {
		t.Fatal("shadowing mutated its parent")
	}
	if n := testing.AllocsPerRun(100, func() { evidenceValueSink = ExtendEvidenceValue(EvidenceValue{}, a) }); n != 0 {
		t.Fatalf("single binding allocated %g", n)
	}
}

func TestEvidenceValueOverflowAndAppliedNames(t *testing.T) {
	integer := NominalType("Int", true)
	text := NominalType("String", true)
	a := &EvidenceBinding{Name: "Read", Arguments: []*TypeDescriptor{integer}, Family: EvidenceFamily{Direct: 1}}
	b := &EvidenceBinding{Name: "Read", Arguments: []*TypeDescriptor{text}, Family: EvidenceFamily{Direct: 2}}
	c := &EvidenceBinding{Name: "C", Family: EvidenceFamily{Direct: 3}}
	row := ExtendEvidenceValue(EvidenceValue{}, a, b, c)
	if ValueEvidence[int](row, "Read", DirectEvidence, integer) != 1 || ValueEvidence[int](row, "Read", DirectEvidence, text) != 2 || ValueEvidence[int](row, "C", DirectEvidence) != 3 {
		t.Fatal("overflow lost bindings")
	}
	shadow := &EvidenceBinding{Name: "Read", Arguments: a.Arguments, Family: EvidenceFamily{Direct: 4}}
	inner := ExtendEvidenceValue(row, shadow)
	if ValueEvidence[int](row, "Read", DirectEvidence, integer) != 1 || ValueEvidence[int](inner, "Read", DirectEvidence, integer) != 4 {
		t.Fatal("overflow shadow changed outer row")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate accepted")
		}
	}()
	ExtendEvidenceValue(inner, shadow, shadow)
}
