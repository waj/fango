package codegen

import (
	"fmt"
	goast "go/ast"
	gotoken "go/token"

	"github.com/waj/fango/internal/core"
)

// Tail-loop eligibility and jumps are decided by internal/lower.
// tailJumpStmts emits one rewritable self call as parameter reassignment
// plus continue. The fast path is a single simultaneous tuple assignment: Go
// evaluates every right-hand side before assigning — exactly the
// call-by-value simultaneity the call had. A param whose argument is a
// VarRef of that same param is skipped (dictionaries and stable args pass
// through unchanged); when everything elides the jump is a bare continue.
// Evidence params are never reassigned: identity evidence (the eligibility
// predicate) means they already hold the right values for every iteration.
//
// Unit formals have no Go param, but erasing a Unit argument never erases
// its evaluation: if any Unit arg is non-atomic, the jump uses the same
// ordered temporary prelude as workerCallExpr — args evaluate left to right
// into t_u%d temporaries (erased Unit args running as statements), then the
// params assign, then continue.
func (g *gen) tailJumpStmts(d *core.Def, e *core.App) []goast.Stmt {
	argTys, _ := core.PeelFun(d.Type, len(d.Params))
	needPrelude := false
	for i, a := range e.Args {
		if g.isUnit(argTys[i]) && !unitAtom(a) {
			needPrelude = true
		}
	}
	var stmts []goast.Stmt
	var lhs, rhs []goast.Expr
	for i, a := range e.Args {
		param := d.Params[i]
		if g.isUnit(argTys[i]) {
			stmts = append(stmts, g.stmts(a)...)
			continue
		}
		if v, ok := a.(*core.VarRef); ok && v.Name == param {
			continue
		}
		if param == "_" && pureAtom(a) {
			continue
		}
		if needPrelude {
			if param == "_" {
				stmts = append(stmts, assignBlank(g.expr(a, 0)))
				continue
			}
			name := fmt.Sprintf("t_u%d", g.tmp)
			g.tmp++
			stmts = append(stmts, varDeclStmt(name, g.workerArgumentType(d, i, a.Type(), g.control), g.workerArgument(d, i, a, g.control)))
			lhs, rhs = append(lhs, ident(mangleValue(param))), append(rhs, ident(name))
			continue
		}
		target := "_"
		if param != "_" {
			target = mangleValue(param)
		}
		lhs, rhs = append(lhs, ident(target)), append(rhs, g.workerArgument(d, i, a, g.control))
	}
	if len(lhs) > 0 {
		stmts = append(stmts, &goast.AssignStmt{Lhs: lhs, Tok: gotoken.ASSIGN, Rhs: rhs})
	}
	return append(stmts, &goast.BranchStmt{Tok: gotoken.CONTINUE})
}

// pureAtom reports whether evaluating e can have no observable effect —
// literals and variable references — so a discarded (`_` param) argument may
// be skipped entirely.
func pureAtom(e core.Expr) bool {
	switch e.(type) {
	case *core.IntLit, *core.FloatLit, *core.StringLit, *core.CharLit, *core.BoolLit, *core.UnitLit, *core.VarRef:
		return true
	default:
		return false
	}
}
