package types

import "testing"

func TestIteratorNextRequiresIdenticalResidualRow(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*TFun)
	}{
		{"missing residual", func(fn *TFun) { fn.Eff.Tail = nil }},
		{"different residual", func(fn *TFun) { fn.Eff.Tail = &TVar{ID: 20, Kind: RowVar, Rigid: true} }},
		{"extra effect", func(fn *TFun) { fn.Eff.Labels = append(fn.Eff.Labels, EffLabel{Name: "IO"}) }},
		{"ordinary Traversal", func(fn *TFun) { fn.Eff.Labels[0].Suspension = false }},
		{"abort Traversal", func(fn *TFun) { fn.Eff.Labels[0].Abort = true }},
		{"parameterized Traversal", func(fn *TFun) { fn.Eff.Labels[0].Args = []Type{fn.Arg} }},
		{"different element", func(fn *TFun) { fn.Ret.(*TCon).Args[0] = &TVar{ID: 21, Rigid: true} }},
		{"non-row parameter", func(fn *TFun) { fn.Eff.Tail.(*TVar).Kind = General }},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := &TVar{ID: 1, Rigid: true}
			e := &TVar{ID: 2, Kind: RowVar, Rigid: true}
			fn := &TFun{
				Arg: &TCon{Name: IteratorTypeName, Args: []Type{a, e}},
				Eff: Row{Labels: []EffLabel{{Name: IteratorTraversalEffectName, Suspension: true}}, Tail: e},
				Ret: &TCon{Name: "Maybe.Maybe", Args: []Type{a}},
			}
			if !IteratorNextShape(fn) {
				t.Fatal("valid next signature rejected")
			}
			test.edit(fn)
			if IteratorNextShape(fn) {
				t.Fatal("invalid next signature accepted")
			}
		})
	}
}
