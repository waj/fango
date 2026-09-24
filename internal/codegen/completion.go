package codegen

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
	goast "go/ast"
	gotoken "go/token"
)

func (g *gen) completionExpr(e *core.Completion) goast.Expr {
	g.usesFangort = true
	mode := e.Control.Resolve(g.control)
	value := g.expr(e.Value, 0)
	switch e.Name {
	case types.CompletionDropSuspensionName, types.CompletionDropDriveName:
		return value
	case types.CompletionFromFailureName:
		con := e.Ty.(*types.TCon)
		return callExpr(indexExpr(selector("fangort", "CompletionFromFailure"), []goast.Expr{g.goType(con.Args[0])}), value)
	case types.CompletionCaptureName, types.NativeRequestImmediateName:
		fn := e.Value.Type().(*types.TFun)
		args := []goast.Expr{}
		if e.Row != nil {
			args = append(args, g.rowArgument(e.Row))
		}
		args = append(args, g.unitValue())
		invoke := callExpr(callbackMember(value, mode), args...)
		if mode == types.Direct {
			if g.isUnit(fn.Ret) {
				invoke = callExpr(funcLit(g.goType(fn.Ret), []goast.Stmt{exprStmt(invoke), returnStmt(g.unitValue())}))
			}
			invoke = g.normalOutcome(fn.Ret, invoke)
		}
		captured := callExpr(indexExpr(selector("fangort", "CaptureCompletion"), []goast.Expr{g.goType(fn.Ret)}), invoke)
		if mode == types.Exit {
			return g.normalOutcome(e.Ty, captured)
		}
		return captured
	case types.CompletionReplayName:
		outcome := callExpr(indexExpr(selector("fangort", "ReplayCompletion"), []goast.Expr{g.goType(e.Ty)}), value, g.rowArgument(e.Row))
		if mode == types.Direct {
			return callExpr(selector("fangort", "RequireNormal"), outcome)
		}
		return outcome
	case types.CompletionFailureName:
		con := e.Ty.(*types.TCon)
		return callExpr(funcLit(g.goType(e.Ty), []goast.Stmt{
			varDeclStmt("failure", &goast.StarExpr{X: selector("fangort", "Failure")}, callExpr(&goast.SelectorExpr{X: value, Sel: ident("Failure")})),
			&goast.IfStmt{Cond: &goast.BinaryExpr{X: ident("failure"), Op: gotoken.NEQ, Y: ident("nil")}, Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(g.ctorValue(e.Result.Ctors[1], con.Args, ident("failure")))}}},
			returnStmt(g.ctorValue(e.Result.Ctors[0], con.Args)),
		}))
	}
	panic("codegen: unknown completion operation")
}

// abortReplayAdapter is owned by the module that constructs this concrete
// evidence row. The complete payload is projected at its checked source types.
func (g *gen) abortReplayAdapter(ev core.EffectInstance, evidence goast.Expr) goast.Expr {
	effect := g.effects[ev.Unique]
	if effect == nil || len(effect.Ops) == 0 || !effect.Ops[0].Abort {
		return nil
	}
	sub := map[int]types.Type{}
	for i, param := range effect.Params {
		sub[param.ID] = ev.Args[i]
	}
	var body []goast.Stmt
	for _, op := range effect.Ops {
		var payload, descriptors []goast.Expr
		for i, param := range op.ParamTypes {
			ty := types.SubstRigid(param, sub)
			descriptor := g.typeDescriptor(ty)
			descriptors = append(descriptors, descriptor)
			payload = append(payload, callExpr(indexExpr(selector("fangort", "CompletionPayload"), []goast.Expr{g.goType(ty)}), ident("failure"), intLit(int64(i)), descriptor))
		}
		exit := &goast.UnaryExpr{Op: gotoken.AND, X: &goast.CompositeLit{Type: selector("fangort", "ExitRequest"), Elts: []goast.Expr{
			&goast.KeyValueExpr{Key: ident("Target"), Value: callExpr(selector("fangort", "ResolveExitTarget"), &goast.SelectorExpr{X: evidence, Sel: ident("Target")})},
			&goast.KeyValueExpr{Key: ident("Effect"), Value: stringLit(effect.Name)},
			&goast.KeyValueExpr{Key: ident("Operation"), Value: intLit(int64(op.Index))},
			&goast.KeyValueExpr{Key: ident("OperationName"), Value: stringLit(op.Name)},
			&goast.KeyValueExpr{Key: ident("Payload"), Value: &goast.CompositeLit{Type: &goast.ArrayType{Elt: ident("any")}, Elts: payload}},
			&goast.KeyValueExpr{Key: ident("PayloadTypes"), Value: &goast.CompositeLit{Type: &goast.ArrayType{Elt: g.descriptorType()}, Elts: descriptors}},
		}}}
		body = append(body, &goast.IfStmt{Cond: callExpr(selector("fangort", "CompletionOperation"), ident("failure"), stringLit(effect.Name), stringLit(op.Name), intLit(int64(len(op.ParamTypes)))), Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(exit)}}})
	}
	// An unrecognized operation is a rejected adapter projection. The runtime
	// validates this result before constructing the replay, just as it checks
	// payload descriptors before projecting their concrete representations.
	body = append(body, returnStmt(ident("nil")))
	return funcLitParams([]paramSpec{{name: "failure", typ: &goast.StarExpr{X: selector("fangort", "Failure")}}}, &goast.StarExpr{X: selector("fangort", "ExitRequest")}, body)
}
