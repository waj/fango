package codegen

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
	goast "go/ast"
)

func (g *gen) coroutineStart(producer core.Expr, owner, row goast.Expr) goast.Expr {
	factoryTy := producer.Type().(*types.TFun)
	pauseTy := factoryTy.Arg.(*types.TFun)
	bodyTy := factoryTy.Ret.(*types.TFun)
	pause := &goast.CompositeLit{Type: g.callbackType(pauseTy), Elts: []goast.Expr{
		&goast.KeyValueExpr{Key: ident("Machine"), Value: funcLitParams([]paramSpec{{name: "request", typ: g.goType(pauseTy.Arg)}}, selector("fangort", "MachineFrame"), []goast.Stmt{
			returnStmt(callExpr(selector("fangort", "SuspendMachine"), ident("pauseOwner"), ident("request"))),
		})},
	}}
	args := []goast.Expr{}
	if types.FunctionOpenRow(bodyTy) {
		args = append(args, callExpr(selector("forwarding", "Row")))
	}
	args = append(args, &goast.TypeAssertExpr{X: ident("input"), Type: g.goType(bodyTy.Arg)})
	factory := funcLitParams([]paramSpec{{name: "input", typ: ident("any")}}, selector("fangort", "MachineFrame"), []goast.Stmt{
		varDeclStmt("body", g.goType(bodyTy), callExpr(callbackMember(ident("producer"), types.Direct), pause)),
		returnStmt(callExpr(callbackMember(ident("body"), types.Machine), args...)),
	})
	return callExpr(funcLitParams([]paramSpec{
		{name: "producer", typ: g.goType(factoryTy)},
		{name: "pauseOwner", typ: &goast.StarExpr{X: selector("fangort", "YieldOwner")}},
		{name: "forwarding", typ: &goast.StarExpr{X: selector("fangort", "CursorEvidence")}},
	}, &goast.StarExpr{X: selector("fangort", "MachineIterator")}, []goast.Stmt{
		returnStmt(callExpr(selector("fangort", "StartMachineCoroutine"), ident("pauseOwner"), ident("forwarding"), factory)),
	}), g.machineExpr(producer), owner, row)
}

func (g *gen) coroutineScopeExpr(e *core.IteratorScope) goast.Expr {
	overall := e.Control.Resolve(g.control)
	oldControl, oldResult := g.control, g.resultType
	g.control, g.resultType = overall, e.Ty
	defer func() { g.control, g.resultType = oldControl, oldResult }()
	stmts := []goast.Stmt{
		varDeclStmt("boundary", g.rowType(), g.rowArgument(e.Row)),
		varDeclStmt("owner", &goast.StarExpr{X: selector("fangort", "YieldOwner")}, callExpr(selector("fangort", "NewYieldOwner"))),
		varDeclStmt("forwarding", &goast.StarExpr{X: selector("fangort", "CursorEvidence")}, callExpr(selector("fangort", "NewCursorEvidence"), ident("boundary"))),
		varDeclStmt("coroutine", &goast.StarExpr{X: selector("fangort", "MachineIterator")}, g.coroutineStart(e.Producer, ident("owner"), ident("forwarding"))),
	}
	args := []goast.Expr{}
	if core.ArrowOpenRow(e.Consumer.Type(), 1) {
		args = append(args, ident("boundary"))
	}
	args = append(args, ident("coroutine"))
	frame := callExpr(callbackMember(g.machineExpr(e.Consumer), types.Machine), args...)
	result := callExpr(indexExpr(selector("fangort", "RunCursorConsumer"), []goast.Expr{g.goType(e.Ty)}), ident("coroutine"), frame)
	resultTy := g.outcomeType(e.Ty)
	if overall == types.Direct {
		result = callExpr(selector("fangort", "RequireNormal"), result)
		resultTy = g.goType(e.Ty)
	}
	stmts = append(stmts, returnStmt(result))
	return callExpr(funcLit(resultTy, stmts))
}
