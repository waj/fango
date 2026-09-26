package codegen

import (
	goast "go/ast"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func (g *gen) taskSpawn(e *core.TaskSpawn) goast.Expr {
	params, result := core.PeelFun(e.WorkerType, 2)
	g.usesFangort = true
	call := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: e.Worker, Ty: e.WorkerType}, TyArgs: e.TyArgs, Ty: result, Args: []core.Expr{&core.VarRef{Name: "taskContext", Local: true, Ty: params[0]}, &core.VarRef{Name: "taskInput", Local: true, Ty: params[1]}}}
	old := g.control
	g.control = types.Direct
	invoke := g.workerCallExpr(call)
	g.control = old
	if g.isUnit(result) {
		invoke = callExpr(funcLit(g.goType(result), []goast.Stmt{exprStmt(invoke), returnStmt(g.unitValue())}))
	}
	run := funcLitParams([]paramSpec{{name: "context", typ: &goast.StarExpr{X: selector("fangort", "TaskContext")}}}, ident("any"), []goast.Stmt{
		varDeclStmt(mangleValue("taskContext"), g.goType(params[0]), g.ctorValue(e.ContextCtor, nil, ident("context"))),
		returnStmt(callExpr(selector("fangort", "PackNativeValue"), invoke)),
	})
	scope := &goast.TypeAssertExpr{X: g.unwrapBoundary(e.ScopeCtor, ident("scope")), Type: &goast.StarExpr{X: selector("fangort", "TaskScope")}}
	spawned := callExpr(selector("fangort", "SpawnTask"), scope, run)
	body := []goast.Stmt{varDeclStmt("scope", g.goType(e.Scope.Type()), g.expr(e.Scope, 0)), varDeclStmt(mangleValue("taskInput"), g.goType(params[1]), g.expr(e.Input, 0)), assignBlank(ident(mangleValue("taskInput"))), returnStmt(g.ctorValue(e.TaskCtor, g.boundaryTypeArgs(e.TaskCtor, e.Ty), spawned))}
	return callExpr(funcLit(g.goType(e.Ty), body))
}
