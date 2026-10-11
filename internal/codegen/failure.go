package codegen

import (
	goast "go/ast"
	gotoken "go/token"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func (g *gen) failureInspectExpr(e *core.FailureInspect) goast.Expr {
	args := make([]goast.Expr, len(e.Args))
	for i, arg := range e.Args {
		args[i] = g.expr(arg, 0)
	}
	if e.Name == types.FailureArgumentName {
		result := e.Ty.(*types.TCon)
		g.usesFangort = true
		inspect := callExpr(indexExpr(selector("fangort", "FailureArgument"), []goast.Expr{g.goType(result.Args[0])}), args[0], args[1], g.typeDescriptor(result.Args[0]))
		return callExpr(funcLit(g.goType(e.Ty), []goast.Stmt{
			&goast.AssignStmt{Lhs: []goast.Expr{ident("payload"), ident("present")}, Tok: gotoken.DEFINE, Rhs: []goast.Expr{inspect}},
			&goast.IfStmt{Cond: ident("present"), Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(g.ctorValue(e.Result.Ctors[1], result.Args, ident("payload")))}}},
			returnStmt(g.ctorValue(e.Result.Ctors[0], result.Args)),
		}))
	}
	if e.Name == types.FailureTraceName {
		result := e.Ty.(*types.TCon)
		read := callExpr(&goast.SelectorExpr{X: args[0], Sel: ident("Trace")})
		return callExpr(funcLit(g.goType(e.Ty), []goast.Stmt{
			&goast.AssignStmt{Lhs: []goast.Expr{ident("frames"), ident("captured")}, Tok: gotoken.DEFINE, Rhs: []goast.Expr{read}},
			&goast.IfStmt{Cond: ident("captured"), Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(g.ctorValue(e.Result.Ctors[1], result.Args, g.traceListExpr(ident("frames"), result.Args[0])))}}},
			returnStmt(g.ctorValue(e.Result.Ctors[0], result.Args)),
		}))
	}
	method := map[string]string{types.FailureEffectName: "Effect", types.FailureOperationName: "Operation", types.FailureArgumentCountName: "ArgumentCount", types.FailureSuppressedName: "Suppressed"}[e.Name]
	return callExpr(&goast.SelectorExpr{X: args[0], Sel: ident(method)})
}

func (g *gen) traceListExpr(frames goast.Expr, ty types.Type) goast.Expr {
	frameTy := ty.(*types.TCon).Args[0].(*types.TCon)
	ctor := g.adts[frameTy.Unique].Ctors[0]
	makeFrame := funcLitParams([]paramSpec{{"function", ident("string")}, {"file", ident("string")}, {"line", ident("int64")}}, g.goType(frameTy), []goast.Stmt{
		returnStmt(g.ctorValue(ctor, nil, ident("function"), ident("file"), ident("line"))),
	})
	g.usesFangort = true
	return callExpr(indexExpr(selector("fangort", "TraceList"), []goast.Expr{g.goType(frameTy)}), frames, makeFrame)
}
