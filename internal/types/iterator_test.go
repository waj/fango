package types

import "testing"

func TestCoroutineAdvanceRequiresIdenticalResidualRow(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*TFun)
	}{
		{"missing residual", func(fn *TFun) { fn.Ret.(*TFun).Eff.Tail = nil }},
		{"different residual", func(fn *TFun) { fn.Ret.(*TFun).Eff.Tail = &TVar{ID: 20, Kind: RowVar, Rigid: true} }},
		{"extra effect", func(fn *TFun) {
			next := fn.Ret.(*TFun)
			next.Eff.Labels = append(next.Eff.Labels, EffLabel{Name: "IO"})
		}},
		{"ordinary Drive", func(fn *TFun) { fn.Ret.(*TFun).Eff.Labels[0].Suspension = false }},
		{"abort Drive", func(fn *TFun) { fn.Ret.(*TFun).Eff.Labels[0].Abort = true }},
		{"parameterized Drive", func(fn *TFun) { fn.Ret.(*TFun).Eff.Labels[0].Args = []Type{fn.Arg} }},
		{"different request", func(fn *TFun) { fn.Ret.(*TFun).Ret.(*TCon).Args[0] = &TVar{ID: 21, Rigid: true} }},
		{"different reply", func(fn *TFun) { fn.Ret.(*TFun).Arg = &TVar{ID: 22, Rigid: true} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, r, z := &TVar{ID: 1, Rigid: true}, &TVar{ID: 3, Rigid: true}, &TVar{ID: 4, Rigid: true}
			e := &TVar{ID: 2, Kind: RowVar, Rigid: true}
			fn := &TFun{Arg: &TCon{Name: CoroutineTypeName, Args: []Type{a, r, z, e}}, Ret: &TFun{Arg: r, Eff: Row{Labels: []EffLabel{{Name: CoroutineDriveName, Suspension: true}}, Tail: e}, Ret: &TCon{Name: CoroutineStepName, Args: []Type{a, z}}}}
			if !CoroutineShape(CoroutineAdvanceName, fn) {
				t.Fatal("valid advance rejected")
			}
			test.edit(fn)
			if CoroutineShape(CoroutineAdvanceName, fn) {
				t.Fatal("invalid advance accepted")
			}
		})
	}
}
