package eval

import (
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func TestClosuresDiscardUnusedAndInvocationEvidence(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	outer, call := core.EffectInstance{Unique: 10, Name: "Outer"}, core.EffectInstance{Unique: 11, Name: "Call"}
	in := &interp{evidence: map[types.EffectKey]*evidence{outer.Key(): {}, call.Key(): {}, types.AppliedEffectKey(12, nil): {}}}
	lam := &core.Lambda{Param: "unit", EffectParams: []core.EffectInstance{call}, Ty: &types.TFun{Arg: b.Unit, Ret: b.Int},
		Body: &core.Seq{First: &core.Perform{Effect: call, Ty: b.Unit}, Then: &core.Perform{Effect: outer, Ty: b.Int}, Ty: b.Int}}
	captured := in.closureEvidence(lam)
	if len(captured) != 1 || captured[outer.Key()] != in.evidence[outer.Key()] {
		t.Fatalf("captured evidence: %#v", captured)
	}
}
