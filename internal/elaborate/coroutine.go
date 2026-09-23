package elaborate

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/types"
)

func coroutineWithDef(ty types.Type, ck *infer.Checker) core.Def {
	args, result := core.PeelFun(ty, 2)
	producer, consumer := args[0].(*types.TFun), args[1].(*types.TFun)
	scope := ck.Sup.FreshScope()
	effect := func(name string) core.EffectInstance {
		e := ck.Effects[name]
		return core.EffectInstance{Unique: e.Unique, Name: e.Name, Captures: types.ScopeCapture(scope), Control: types.Control{Transport: types.Machine}}
	}
	control := core.ArrowControl(ty, 2)
	return core.Def{Name: types.CoroutineWithName, Owner: "Coroutine", Type: ty, TyParams: runtimeRigidVars(ty), Params: []string{"_producer", "_consumer"}, ParamCaptures: []types.CaptureVar{ck.Sup.FreshCapture(), ck.Sup.FreshCapture()}, Control: control,
		Body: &core.IteratorScope{Yield: effect(types.CoroutineSuspensionName), Traversal: effect(types.CoroutineDriveName), Scope: scope, Producer: &core.VarRef{Name: "_producer", Local: true, Ty: producer}, Consumer: &core.VarRef{Name: "_consumer", Local: true, Ty: consumer}, CursorTy: consumer.Arg, Ty: result, Control: control}}
}

func coroutineAdvanceDef(name string, ty types.Type, ck *infer.Checker) core.Def {
	arity := types.IntrinsicArity(name)
	args, result := core.PeelFun(ty, arity)
	d := core.Def{Name: name, Owner: "Coroutine", Type: ty, TyParams: runtimeRigidVars(ty), Params: []string{"_cursor"}, ParamCaptures: []types.CaptureVar{ck.Sup.FreshCapture()}, Control: core.ArrowControl(ty, arity)}
	n := &core.IteratorNext{Cursor: &core.VarRef{Name: "_cursor", Local: true, Ty: args[0]}, Access: types.ExclusiveAdvance, Ty: result, Close: name == types.CoroutineCloseName}
	if !n.Close {
		d.Params = append(d.Params, "_reply")
		d.ParamCaptures = append(d.ParamCaptures, ck.Sup.FreshCapture())
		n.Reply = &core.VarRef{Name: "_reply", Local: true, Ty: args[1]}
		n.Result = ck.ADTs[result.(*types.TCon).Unique]
	}
	d.Body = n
	return d
}
