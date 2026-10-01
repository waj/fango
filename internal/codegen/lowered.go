package codegen

import (
	goast "go/ast"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/lower"
)

func (g *gen) loweredStmts(d *core.Def, block lower.Block, loop, unit bool) []goast.Stmt {
	var out []goast.Stmt
	for _, statement := range block {
		switch s := statement.(type) {
		case lower.Bind:
			out = append(out, g.letBindingStmts(s.Let)...)
		case lower.Eval:
			out = append(out, g.stmts(s.Value)...)
		case lower.Branch:
			// Preserve the established if-return spelling without inventing
			// expression closures at a branch join.
			out = append(out, &goast.IfStmt{Cond: g.expr(s.Condition, 0), Body: &goast.BlockStmt{List: g.loweredStmts(d, s.Then, loop, unit)}})
			out = append(out, g.loweredStmts(d, s.Else, loop, unit)...)
		case lower.Match:
			out = append(out, g.caseStmts(s.Case, func(e core.Expr) []goast.Stmt { return g.loweredStmts(d, lower.Tail(d, e, loop), loop, unit) })...)
		case lower.Loop:
			out = append(out, &goast.ForStmt{Body: &goast.BlockStmt{List: g.loweredStmts(d, s.Body, true, unit)}})
		case lower.Continue:
			out = append(out, g.tailJumpStmts(d, s.Call)...)
		case lower.Return:
			out = append(out, g.retStmtsFor(s.Value, unit)...)
		default:
			panic("codegen: unverified lower statement")
		}
	}
	return out
}
