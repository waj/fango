package elaborate

import (
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func TestANFHoistsControlProducingBindingRegion(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	region := &core.Let{Name: "value", Rhs: &core.ControlExit{Ty: b.Int}, Body: &core.VarRef{Name: "value", Local: true, Ty: b.Int}, Ty: b.Int}
	normal, hoists := (&elab{}).anfSlot(region)
	ref, ok := normal.(*core.VarRef)
	if !ok || len(hoists) != 1 || ref.Name != hoists[0].name {
		t.Fatalf("control region was not hoisted: %T, %#v", normal, hoists)
	}
	kept := false
	core.InspectPruned(hoists[0].rhs, func(expr core.Expr) bool {
		if binding, ok := expr.(*core.Let); ok && binding.Name == "value" {
			body, ok := binding.Body.(*core.VarRef)
			kept = ok && body.Name == binding.Name
		}
		return true
	})
	if !kept || core.ExprControl(hoists[0].rhs).Transport != types.Exit {
		t.Fatalf("binding or exit was lost: %#v", hoists[0].rhs)
	}
}
