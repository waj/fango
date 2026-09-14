package types

import "testing"

func TestGeneratorEffectSelectsMachineWithoutRuntimeEvidence(t *testing.T) {
	label := EffLabel{Unique: 2, Name: GeneratorEffectName, Suspension: true}
	fn := &TFun{Arg: &TCon{Unique: 1, Name: "String"}, Eff: Row{Labels: []EffLabel{label}}, Ret: &TCon{Unique: 3, Name: "()"}}
	if got := FunctionControl(fn); got.Transport != Machine {
		t.Fatalf("Generator function control = %+v, want Machine", got)
	}
	if RuntimeEvidenceEffect(label) {
		t.Fatal("Generator suspension effect must not acquire runtime evidence")
	}
	ordinary := label
	ordinary.Suspension = false
	if got := FunctionControl(&TFun{Arg: fn.Arg, Eff: Row{Labels: []EffLabel{ordinary}}, Ret: fn.Ret}); got.Transport == Machine {
		t.Fatalf("ordinary effect with Generator spelling selected Machine: %+v", got)
	}
	if !RuntimeEvidenceEffect(ordinary) {
		t.Fatal("ordinary effect with Generator spelling lost runtime evidence")
	}
}
