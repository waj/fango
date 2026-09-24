package elaborate

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/types"
)

func sourceLambdas(expr core.Expr, sourceType types.Type, count int) core.Expr {
	root := expr
	for range count {
		lambda, ok := expr.(*core.Lambda)
		if !ok {
			return root
		}
		arrow, ok := sourceType.(*types.TFun)
		if !ok {
			return root
		}
		lambda.SourceType = sourceType
		expr, sourceType = lambda.Body, arrow.Ret
	}
	return root
}

func workDef(name string, ty types.Type, ck *infer.Checker) core.Def {
	arity := types.IntrinsicArity(name)
	args, result := core.PeelFun(ty, arity)
	rawArgs, _ := core.PeelFun(ck.Intrinsics[name].Body, arity)
	d := core.Def{Name: name, Owner: "Work", Type: ty, TyParams: runtimeRigidVars(ty), Control: core.ArrowControl(ty, arity)}
	refs := make([]core.Expr, arity)
	for i, n := range []string{"_owner", "_work", "_reply"}[:arity] {
		d.Params = append(d.Params, n)
		d.ParamCaptures = append(d.ParamCaptures, ck.Sup.FreshCapture())
		refs[i] = &core.VarRef{Name: n, Local: true, Ty: args[i]}
	}
	switch name {
	case types.WorkOwnerName:
		d.Body = &core.Work{Kind: "registry-owner", Args: refs, Ty: result}
	case types.WorkRegisterName:
		d.Body = &core.Work{Kind: "register", Args: refs, SourceRow: rawArgs[1].(*types.TFun).Ret.(*types.TFun).Eff.Tail, Ty: result}
	case types.WorkRunName:
		fn := args[0].(*types.TFun)
		resource := &core.VarRef{Name: "_budget", Local: true, Ty: fn.Arg}
		body := &core.App{CalleeKind: core.Value, Callee: refs[0], Args: []core.Expr{resource}, Ty: result, Control: types.FunctionControl(fn)}
		d.Body = &core.Bracket{Scope: ck.Sup.FreshScope(), Resource: "_budget", ResourceTy: fn.Arg, Acquire: &core.Work{Kind: "begin", SourceRow: rawArgs[0].(*types.TFun).Arg.(*types.TCon).Args[0], Ty: fn.Arg}, Release: &core.Work{Kind: "end", Args: []core.Expr{resource}, Ty: ck.B.Unit}, Body: body, Ty: result, Control: d.Control}
	case types.WorkFacetName:
		d.Body = &core.Work{Kind: "facet", Args: refs, Ty: result}
	case types.WorkPackName:
		d.Body = &core.Work{Kind: "pack", SourceRow: rawArgs[1].(*types.TCon).Args[3], Args: refs, Ty: result}
	case types.WorkAdvanceName, types.WorkCloseName:
		q, r, a, _ := types.WorkProtocol(args[1])
		con := ck.TypeNames[types.CoroutineTypeName].(*types.TCon)
		cursor := &types.TCon{Unique: con.Unique, Name: con.Name, Args: []types.Type{q, r, a, ck.B.Unit}}
		open := &core.Work{Kind: "open", Args: refs[:2], Ty: cursor}
		n := &core.CoroutineAdvance{Cursor: open, Close: name == types.WorkCloseName, Access: types.ExclusiveAdvance, Ty: result}
		if !n.Close {
			n.Reply = refs[2]
			n.Result = ck.ADTs[result.(*types.TCon).Unique]
		}
		d.Body = n
	}
	return d
}
