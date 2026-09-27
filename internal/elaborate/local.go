package elaborate

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/types"
)

// localRunDef is one general isolated-state boundary. Its cell uses the same
// typed native storage as IO references. The scoped source callback contract
// prevents that private mutable state from escaping or being used as pure.
// Neither scope identities nor locks are introduced in the generated program.
func localRunDef(name string, ty types.Type, ck *infer.Checker) core.Def {
	el := &elab{ck: ck}
	args, result := core.PeelFun(ty, 2)
	useTy := args[1].(*types.TFun)
	cellTy := useTy.Arg.(*types.TCon)
	cell := ck.ADTs[cellTy.Unique]
	fields := cell.InstFields(cell.Ctors[0], cellTy.Args)
	for i, field := range fields {
		fields[i] = el.eraseRuntimeKinds(eraseRows(field))
	}
	ref := ck.TypeNames["Runtime.Ref.Ref"].(*types.TCon)
	refTy := &types.TCon{Unique: ref.Unique, Name: ref.Name, Args: []types.Type{args[0]}}
	refValue := func() core.Expr { return &core.VarRef{Name: "_cell", Local: true, Ty: refTy} }
	native := func(name string, args []core.Expr, result types.Type) core.Expr {
		n := ck.Natives[name]
		return &core.NativeCall{Name: name, Module: n.Module, Args: args, Ty: result, Storage: n.Storage}
	}
	readTy, writeTy := fields[0].(*types.TFun), fields[1].(*types.TFun)
	read := &core.Lambda{Param: "_unit", Ty: readTy, ParamCapture: ck.Sup.FreshCapture(),
		Body: native("Runtime.Ref.read", []core.Expr{refValue()}, args[0])}
	write := &core.Lambda{Param: "_value", Ty: writeTy, ParamCapture: ck.Sup.FreshCapture(),
		Body: native("Runtime.Ref.write", []core.Expr{refValue(), &core.VarRef{Name: "_value", Local: true, Ty: args[0]}}, ck.B.Unit)}
	ctorTy := &types.TFun{Arg: readTy, Ret: &types.TFun{Arg: writeTy, Ret: cellTy}}
	value := &core.App{CalleeKind: core.Ctor, Callee: &core.VarRef{Name: cell.Ctors[0].Name, Ty: ctorTy},
		Args: []core.Expr{read, write}, TyArgs: cellTy.Args, Ctor: cell.Ctors[0], Ty: cellTy}
	body := &core.App{CalleeKind: core.Value, Callee: &core.VarRef{Name: "_use", Local: true, Ty: useTy},
		Args: []core.Expr{value}, Ty: result, Control: types.FunctionControl(useTy)}
	return core.Def{Name: name, Owner: symbolOwner(name), Type: ty, TyParams: runtimeRigidVars(ty),
		Params: []string{"_initial", "_use"}, ParamCaptures: []types.CaptureVar{ck.Sup.FreshCapture(), ck.Sup.FreshCapture()},
		Control: core.ArrowControl(ty, 2),
		Body: &core.Let{Name: "_cell", Rhs: native("Runtime.Ref.new", []core.Expr{&core.VarRef{Name: "_initial", Local: true, Ty: args[0]}}, refTy),
			Body: body, Ty: result}}
}
