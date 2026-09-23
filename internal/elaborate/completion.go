package elaborate

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/types"
)

func completionDef(name string, ty types.Type, ck *infer.Checker) core.Def {
	args, result := core.PeelFun(ty, 1)
	control := core.ArrowControl(ty, 1)
	node := &core.Completion{Name: name, Value: &core.VarRef{Name: "_value", Local: true, Ty: args[0]}, Ty: result, Control: control}
	if name == types.CompletionFailureName {
		node.Result = ck.ADTs[result.(*types.TCon).Unique]
	}
	return core.Def{Name: name, Owner: symbolOwner(name), Type: ty, TyParams: runtimeRigidVars(ty), Params: []string{"_value"}, ParamCaptures: []types.CaptureVar{ck.Sup.FreshCapture()}, Control: control, Body: node}
}
