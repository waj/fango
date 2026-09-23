package types

import "testing"

func TestOrdinaryStreamNamesDoNotSelectControl(t *testing.T) {
	for _, name := range []string{"Stream.Yield", "Renamed.Yield", "Iterator.Traversal"} {
		label := EffLabel{Unique: 2, Name: name}
		fn := &TFun{Eff: Row{Labels: []EffLabel{label}}}
		if got := FunctionControl(fn); got.Transport == Machine || !got.Polymorphic {
			t.Fatalf("ordinary effect %s: %+v", name, got)
		}
		if !RuntimeEvidenceEffect(label) {
			t.Fatalf("ordinary effect %s lost evidence", name)
		}
	}
}
func TestCoroutineControlRequiresCompilerOwnedIdentity(t *testing.T) {
	for _, name := range []string{CoroutineDriveName, CoroutineSuspensionName} {
		label := EffLabel{Name: name, Suspension: true}
		if RuntimeEvidenceEffect(label) {
			t.Fatal("owned control acquired runtime evidence")
		}
		if FunctionControl(&TFun{Eff: Row{Labels: []EffLabel{label}}}).Transport != Machine {
			t.Fatal("control did not select Machine")
		}
		label.Suspension = false
		if !RuntimeEvidenceEffect(label) {
			t.Fatal("ordinary spelling lost runtime evidence")
		}
	}
}
