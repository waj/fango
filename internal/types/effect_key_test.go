package types

import "testing"

// Installing a cached module renumbers identities in numeric order, so key
// order must agree with numeric order across digit boundaries.
func TestEffectKeyOrderIsNumeric(t *testing.T) {
	for _, pair := range [][2]int{{9, 10}, {95, 120}, {950, 990}, {999, 1000}} {
		if !(AppliedEffectKey(pair[0], nil) < AppliedEffectKey(pair[1], nil)) {
			t.Errorf("effect %d does not sort before %d", pair[0], pair[1])
		}
		lo := []Type{&TCon{Unique: pair[0]}}
		hi := []Type{&TCon{Unique: pair[1]}}
		if !(AppliedEffectKey(1, lo) < AppliedEffectKey(1, hi)) {
			t.Errorf("argument %d does not sort before %d", pair[0], pair[1])
		}
	}
}
