package elaborate

import (
	"fmt"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/types"
)

func failureInspectDef(name string, ty types.Type, ck *infer.Checker) core.Def {
	args, result := core.PeelFun(ty, types.IntrinsicArity(name))
	d := core.Def{Name: name, Owner: symbolOwner(name), Type: ty, TyParams: runtimeRigidVars(ty)}
	projection := &core.FailureInspect{Name: name, Ty: result}
	for i, arg := range args {
		param := fmt.Sprintf("_argument%d", i)
		d.Params = append(d.Params, param)
		d.ParamCaptures = append(d.ParamCaptures, ck.Sup.FreshCapture())
		projection.Args = append(projection.Args, &core.VarRef{Name: param, Local: true, Ty: arg})
	}
	if name == types.FailureArgumentName {
		projection.Result = ck.ADTs[result.(*types.TCon).Unique]
	}
	d.Body = projection
	return d
}

func attemptReportDef(name string, ty types.Type, ck *infer.Checker) core.Def {
	args, result := core.PeelFun(ty, 1)
	action := args[0].(*types.TFun)
	resultTy := result.(*types.TCon)
	reportTy := resultTy.Args[0].(*types.TCon)
	reportADT := ck.ADTs[reportTy.Unique]
	resultADT := ck.ADTs[resultTy.Unique]
	fields := reportADT.InstFields(reportADT.Ctors[0], reportTy.Args)
	scope := ck.Sup.FreshScope()
	fail := ck.Effects["Fail.Fail"]
	ev := core.EffectInstance{Unique: fail.Unique, Name: fail.Name, Args: reportTy.Args, Captures: types.ScopeCapture(scope), Control: types.Control{Transport: types.Exit}}
	ref := func(name string, ty types.Type) core.Expr { return &core.VarRef{Name: name, Local: true, Ty: ty} }
	ctor := func(adt *types.ADTInfo, index int, ty *types.TCon, args ...core.Expr) core.Expr {
		c := adt.Ctors[index]
		return &core.App{CalleeKind: core.Ctor, Callee: &core.VarRef{Name: c.Name, Ty: prependTypes(adt.InstFields(c, ty.Args), ty)}, Args: args, TyArgs: ty.Args, Ty: ty, Ctor: c}
	}
	report := ctor(reportADT, 0, reportTy, ref("_primary", fields[0]), ref("_suppressed", fields[1]))
	// Result declares Err before Ok; resolve by constructor name rather than
	// depending on layout when constructing the semantic Core proof.
	var errIndex, okIndex int
	for i, c := range resultADT.Ctors {
		if c.Name == "Result.Err" {
			errIndex = i
		}
		if c.Name == "Result.Ok" {
			okIndex = i
		}
	}
	body := &core.App{CalleeKind: core.Value, Callee: ref("_action", action), Args: []core.Expr{&core.UnitLit{Ty: ck.B.Unit}}, EvidenceArgs: []core.EffectInstance{ev}, Ty: action.Ret, Control: types.FunctionControl(action)}
	control := core.ArrowControl(ty, 1)
	return core.Def{Name: name, Owner: symbolOwner(name), Type: ty, TyParams: runtimeRigidVars(ty), Params: []string{"_action"}, ParamCaptures: []types.CaptureVar{ck.Sup.FreshCapture()}, Control: control,
		Body: &core.Handle{Body: body, Effect: ev, Scope: scope, Ty: result, Control: control,
			Clauses: []core.HandlerClause{{Op: fail.Ops[0], Params: []string{"_primary"}, ParamTypes: []types.Type{fields[0]}, SuppressedParam: "_suppressed", SuppressedType: fields[1], ResultType: action.Ret, Body: ctor(resultADT, errIndex, resultTy, report)}},
			Return:  &core.ReturnClause{Param: "_value", Body: ctor(resultADT, okIndex, resultTy, ref("_value", action.Ret))}}}
}
