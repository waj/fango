package types

import "testing"

func TestStreamYieldEffectSelectsMachineWithOwnerEvidence(t *testing.T) {
	label := EffLabel{Unique: 2, Name: StreamYieldEffectName, Suspension: true}
	fn := &TFun{Arg: &TCon{Unique: 1, Name: "String"}, Eff: Row{Labels: []EffLabel{label}}, Ret: &TCon{Unique: 3, Name: "()"}}
	if got := FunctionControl(fn); got.Transport != Machine {
		t.Fatalf("StreamYield function control = %+v, want Machine", got)
	}
	if !RuntimeEvidenceEffect(label) {
		t.Fatal("StreamYield suspension effect must carry lexical owner evidence")
	}
	ordinary := label
	ordinary.Suspension = false
	if got := FunctionControl(&TFun{Arg: fn.Arg, Eff: Row{Labels: []EffLabel{ordinary}}, Ret: fn.Ret}); got.Transport == Machine {
		t.Fatalf("ordinary effect with StreamYield spelling selected Machine: %+v", got)
	}
	if !RuntimeEvidenceEffect(ordinary) {
		t.Fatal("ordinary effect with StreamYield spelling lost runtime evidence")
	}
}

func TestTraversalEvidenceRequiresCompilerOwnedIdentity(t *testing.T) {
	label := EffLabel{Name: IteratorTraversalEffectName, Suspension: true}
	if RuntimeEvidenceEffect(label) {
		t.Fatal("owned Traversal acquired runtime evidence")
	}
	label.Suspension = false
	if !RuntimeEvidenceEffect(label) {
		t.Fatal("ordinary Traversal spelling lost runtime evidence")
	}
}
