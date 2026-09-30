package codegen

import (
	"fmt"
	goast "go/ast"
	gotoken "go/token"

	"github.com/waj/fango/internal/core"
)

// Self tail calls compile to loops (doc/design.md, "Go backend and runtime"):
// when core.DetectTailLoop accepts a worker, its body emits as `for { … }`
// and every rewritable self call becomes a parameter reassignment plus
// `continue`. loopStmts is a separate walker rather than a flag consulted by
// retStmtsFor, so a `continue` can never leak into a func literal — lambda
// and expression-context emission still go through retStmts, which stays
// loop-free. `continue` inside the Go switch/type-switch that decision trees
// emit targets the enclosing `for`, so case leaves need no labels.

// loopStmts mirrors retStmtsFor exactly — same Let/If/Case statement shapes,
// so ineligible leaves and non-tail subtrees emit byte-identically — plus two
// cases: Seq runs its first part as statements and stays in the walker (no
// IIFE inside loops), and a rewritable self call emits the jump.
func (g *gen) loopStmts(d *core.Def, e core.Expr, unitResult bool) []goast.Stmt {
	switch e := e.(type) {
	case *core.Let:
		return append(g.letBindingStmts(e), g.loopStmts(d, e.Body, unitResult)...)
	case *core.If:
		stmts := []goast.Stmt{&goast.IfStmt{
			Cond: g.expr(e.Cond, 0),
			Body: &goast.BlockStmt{List: g.loopStmts(d, e.Then, unitResult)},
		}}
		return append(stmts, g.loopStmts(d, e.Else, unitResult)...)
	case *core.Case:
		return g.caseStmts(e, func(x core.Expr) []goast.Stmt { return g.loopStmts(d, x, unitResult) })
	case *core.Seq:
		return append(g.stmts(e.First), g.loopStmts(d, e.Then, unitResult)...)
	case *core.App:
		if core.IsTailLoopCall(d, e) {
			return g.tailJumpStmts(d, e)
		}
	}
	return g.retStmtsFor(e, unitResult)
}

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
			stmts = append(stmts, varDeclStmt(name, g.goType(a.Type()), g.expr(a, 0)))
			lhs, rhs = append(lhs, ident(mangleValue(param))), append(rhs, ident(name))
			continue
		}
		target := "_"
		if param != "_" {
			target = mangleValue(param)
		}
		lhs, rhs = append(lhs, ident(target)), append(rhs, g.expr(a, 0))
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
