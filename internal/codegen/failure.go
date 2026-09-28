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
	method := map[string]string{types.FailureEffectName: "Effect", types.FailureOperationName: "Operation", types.FailureArgumentCountName: "ArgumentCount", types.FailureSuppressedName: "Suppressed"}[e.Name]
	return callExpr(&goast.SelectorExpr{X: args[0], Sel: ident(method)})
}
