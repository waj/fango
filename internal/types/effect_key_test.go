package types

import "testing"

// Identities separate generations of one name, across digit boundaries too.
func TestEffectOrderSeparatesGenerationsNumerically(t *testing.T) {
	for _, pair := range [][2]int{{9, 10}, {95, 120}, {950, 990}, {999, 1000}} {
		if !(AppliedEffectOrder("E", pair[0], nil) < AppliedEffectOrder("E", pair[1], nil)) {
			t.Errorf("effect %d does not sort before %d", pair[0], pair[1])
		}
		lo := []Type{&TCon{Name: "T", Unique: pair[0]}}
		hi := []Type{&TCon{Name: "T", Unique: pair[1]}}
		if !(AppliedEffectOrder("E", 1, lo) < AppliedEffectOrder("E", 1, hi)) {
			t.Errorf("argument %d does not sort before %d", pair[0], pair[1])
		}
	}
}

func TestEffectOrderIgnoresAllocation(t *testing.T) {
	if !(AppliedEffectOrder("A.E", 20, nil) < AppliedEffectOrder("B.E", 10, nil)) {
		t.Error("identities outrank effect names")
	}
	if !(AppliedEffectOrder("A.E", 1, nil) < AppliedEffectOrder("A.E2", 1, nil)) {
		t.Error("a name does not sort before its extensions")
	}
	a, b := []Type{&TCon{Name: "A.T", Unique: 20}}, []Type{&TCon{Name: "B.T", Unique: 10}}
	if !(AppliedEffectOrder("E", 1, a) < AppliedEffectOrder("E", 1, b)) {
		t.Error("identities outrank argument names")
	}
}
