package types

import "testing"

func TestOrdinaryStreamNamesDoNotSelectControl(t *testing.T) {
	for _, name := range []string{"Stream.Yield", "Renamed.Yield", "Iterator.Traversal"} {
		label := EffLabel{Unique: 2, Name: name}
		fn := &TFun{Eff: Row{Labels: []EffLabel{label}}}
		if got := FunctionControl(fn); got.Transport != Direct || !got.Polymorphic {
			t.Fatalf("ordinary effect %s: %+v", name, got)
		}
		if !RuntimeEvidenceEffect(label) {
			t.Fatalf("ordinary effect %s lost evidence", name)
		}
	}
}
