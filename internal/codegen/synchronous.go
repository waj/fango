package codegen

import (
	"fmt"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
	goast "go/ast"
)

// synchronousMachineCallback installs an Exit member under the checked Scope
// non-suspension obligation. Widening may have hidden the callback's synchronous
// implementation behind Machine calls, so this boundary drives its frame and
// preserves failure. The callback value is evaluated exactly once.
func (g *gen) synchronousMachineCallback(arg core.Expr) goast.Expr {
	fn := arg.Type().(*types.TFun)
	name := fmt.Sprintf("t_sync%d", g.tmp)
	g.tmp++
	param := fmt.Sprintf("t_syncarg%d", g.tmp)
	g.tmp++
	params := []paramSpec{}
	args := []goast.Expr{}
	if types.FunctionOpenRow(fn) {
		row := fmt.Sprintf("t_syncrow%d", g.tmp)
		g.tmp++
		params = append(params, paramSpec{name: row, typ: g.rowType()})
		args = append(args, ident(row))
	}
	params = append(params, paramSpec{name: param, typ: g.goType(fn.Arg)})
	args = append(args, ident(param))
	frame := callExpr(selector("fangort", "StartFrame"), callExpr(callbackMember(ident(name), types.Machine), args...))
	outcome := callExpr(indexExpr(selector("fangort", "RunSynchronousMachine"), []goast.Expr{g.goType(fn.Ret)}), frame)
	exit := funcLitParams(params, g.outcomeType(fn.Ret), []goast.Stmt{returnStmt(outcome)})
	value := &goast.CompositeLit{Type: g.goType(fn), Elts: []goast.Expr{&goast.KeyValueExpr{Key: ident("Exit"), Value: exit}}}
	return callExpr(funcLitParams([]paramSpec{{name: name, typ: g.goType(fn)}}, g.goType(fn), []goast.Stmt{returnStmt(value)}), g.machineExpr(arg))
}
