package codegen

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
	goast "go/ast"
	gotoken "go/token"
)

func (g *gen) asyncRebase(e *core.AsyncRebase) goast.Expr {
	g.usesFangort = true
	args := []goast.Expr{ident("nil")}
	for _, ev := range e.Call.EvidenceArgs {
		stack := g.evidence[ev.Key()]
		args = append(args, &goast.CompositeLit{Type: selector("fangort", "EvidenceBinding"), Elts: []goast.Expr{
			&goast.KeyValueExpr{Key: ident("Name"), Value: stringLit(ev.Name)},
			&goast.KeyValueExpr{Key: ident("Arguments"), Value: &goast.CompositeLit{Type: &goast.ArrayType{Elt: g.descriptorType()}, Elts: g.typeDescriptorArgs(ev.Args)}},
			&goast.KeyValueExpr{Key: ident("Family"), Value: g.evidenceFamily(ev, stack[len(stack)-1], g.currentEvidenceMode(ev.Key()))},
		}})
	}
	overrides := callExpr(selector("fangort", "ExtendEvidenceRow"), args...)
	row := callExpr(&goast.SelectorExpr{X: callExpr(selector("fangort", "NewEvidenceFork"), overrides), Sel: ident("Value")}, g.rowArgument(e.Call.Row))
	// Negative binders are private emission temporaries, never serialized Core.
	restore := g.pushRow(-2, ident("asyncRow"))
	defer restore()
	call := *e.Call
	call.Row = &core.RowArgument{From: -2}
	result := g.goType(e.Type())
	if core.ExprControl(e).Resolve(g.control) == types.Exit {
		result = g.outcomeType(e.Type())
	}
	return callExpr(funcLit(result, []goast.Stmt{varDeclStmt("asyncRow", g.rowType(), row), returnStmt(g.expr(&call, 0))}))
}

func (g *gen) asyncLaunch(e *core.AsyncLaunch) goast.Expr {
	g.usesFangort = true
	call := e.Call
	scopeTy := call.Args[0].Type()
	outcomeTy := call.Ty.(*types.TCon)
	resultTy := outcomeTy.Args[0].(*types.TCon)
	ctorType := func(c *types.CtorInfo, args []types.Type) goast.Expr {
		return &goast.StarExpr{X: indexExpr(g.ctorRef(c), g.goTypes(args))}
	}
	completion := func(fields ...goast.Expr) goast.Expr {
		return &goast.CompositeLit{Type: selector("fangort", "AsyncCompletion"), Elts: fields}
	}
	kv := func(k string, v goast.Expr) goast.Expr { return &goast.KeyValueExpr{Key: ident(k), Value: v} }
	body := []goast.Stmt{
		varDeclStmt("asyncOwner", g.goType(scopeTy), g.expr(call.Args[0], 0)),
		varDeclStmt("asyncBody", g.goType(call.Callee.Type()), g.expr(call.Callee, 0)),
		varDeclStmt("asyncRow", g.rowType(), g.rowArgument(call.Row)),
		// Publish inherited activations before the goroutine exists.
		exprStmt(callExpr(selector("fangort", "ShareEvidenceValue"), ident("asyncRow"))),
	}
	invokeArgs := []goast.Expr{}
	if call.Row != nil {
		invokeArgs = append(invokeArgs, ident("asyncRow"))
	}
	invokeArgs = append(invokeArgs, g.ctorValue(e.ScopeCtor, g.boundaryTypeArgs(e.ScopeCtor, scopeTy), ident("child")))
	mode := call.Control.Resolve(types.Exit)
	invoke := callExpr(callbackMember(ident("asyncBody"), mode), invokeArgs...)
	if mode == types.Exit {
		g.markOutcomeCall(invoke.(*goast.CallExpr), g.goType(call.Ty))
		invoke = callExpr(selector("fangort", "RequireNormal"), invoke)
	}
	failed := binExpr(gotoken.EQL, selector("result", "Tag"), intLit(int64(e.ErrCtor.Index)))
	failure := selector("result", representationField(g.adts[e.ErrCtor.Result.Unique], e.ErrCtor, 0))
	success := []goast.Stmt{
		varDeclStmt("result", g.goType(resultTy), selector("completed", "F0")),
		ifStmt(failed, []goast.Stmt{returnStmt(completion(kv("Value", callExpr(selector("fangort", "PackNativeValue"), ident("result"))), kv("Failure", callExpr(selector("fangort", "PackNativeValue"), failure)), kv("Failed", ident("true"))))}, nil),
		returnStmt(completion(kv("Value", callExpr(selector("fangort", "PackNativeValue"), ident("result"))))),
	}
	run := funcLitParams([]paramSpec{{name: "child", typ: &goast.StarExpr{X: selector("fangort", "AsyncScope")}}}, selector("fangort", "AsyncCompletion"), []goast.Stmt{
		varDeclStmt("outcome", g.goType(outcomeTy), invoke),
		&goast.IfStmt{Init: &goast.AssignStmt{Lhs: []goast.Expr{ident("completed"), ident("ok")}, Tok: gotoken.DEFINE, Rhs: []goast.Expr{&goast.TypeAssertExpr{X: ident("outcome"), Type: ctorType(e.CompletedCtor, outcomeTy.Args)}}}, Cond: ident("ok"), Body: &goast.BlockStmt{List: success}},
		returnStmt(completion(kv("Cancelled", ident("true")))),
	})
	owner := &goast.TypeAssertExpr{X: g.unwrapBoundaryAt(e.ScopeCtor, scopeTy, ident("asyncOwner")), Type: &goast.StarExpr{X: selector("fangort", "AsyncScope")}}
	spawned := callExpr(selector("fangort", "SpawnAsync"), owner, run)
	body = append(body, assignBlank(ident("asyncRow")), returnStmt(g.ctorValue(e.TaskCtor, g.boundaryTypeArgs(e.TaskCtor, e.Ty), spawned)))
	return callExpr(funcLit(g.goType(e.Ty), body))
}

func (g *gen) parallelMap(e *core.ParallelMap) goast.Expr {
	fn := e.Function.Type().(*types.TFun)
	g.usesFangort = true
	invoke := callExpr(callbackMember(ident("mapBody"), types.Direct), ident("input"))
	worker := funcLitParams([]paramSpec{{name: "input", typ: g.goType(fn.Arg)}}, g.goType(fn.Ret), []goast.Stmt{returnStmt(invoke)})
	mapped := callExpr(indexExpr(selector("fangort", "ParallelMap"), g.goTypes([]types.Type{fn.Arg, fn.Ret})), worker, g.expr(e.Input, 0))
	return callExpr(funcLit(g.goType(e.Ty), []goast.Stmt{varDeclStmt("mapBody", g.goType(fn), g.expr(e.Function, 0)), returnStmt(mapped)}))
}
