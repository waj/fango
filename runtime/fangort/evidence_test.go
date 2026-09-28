package fangort

import (
	"testing"
)

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
