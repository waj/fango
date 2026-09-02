// Package elaborate bridges the typed AST and Core: zonking (fully applying
// the solved substitution) and defaulting residual metavariables (Number →
// Int, General → Unit). Lambda-lifting, saturation analysis, and decision
// trees join in later slices (lift.go, match.go).
package elaborate

import (
	"fmt"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/types"
)

// Module elaborates checked declarations into a Core program. After it
// returns, Core contains no metavariables — asserted by core.Lint.
func Module(infos []infer.DeclInfo, ck *infer.Checker) (*core.Prog, []diag.Error) {
	p := &core.Prog{}
	for _, info := range infos {
		p.Defs = append(p.Defs, Decl(info, ck))
	}
	return p, nil
}

// Decl elaborates one declaration — also the REPL's per-input entry point.
func Decl(info infer.DeclInfo, ck *infer.Checker) core.Def {
	return core.Def{
		Name: info.Name,
		Type: zonkDefault(info.Type, ck),
		Body: Expr(info.Body, ck),
	}
}

// Expr elaborates one expression against the checker's solved types.
func Expr(e ast.Expr, ck *infer.Checker) core.Expr {
	ty := zonkDefault(ck.ExprTypes[e], ck)
	switch e := e.(type) {
	case *ast.IntLit:
		return &core.IntLit{Val: e.Value, Ty: ty}
	case *ast.Var:
		return &core.VarRef{Name: e.Name, Ty: ty}
	case *ast.BinOp:
		return &core.BinOp{Op: e.Op, Ty: ty, L: Expr(e.L, ck), R: Expr(e.R, ck)}
	default:
		panic(fmt.Sprintf("elaborate: unhandled AST node %T", e))
	}
}

// zonkDefault applies the substitution, then defaults any metavariable
// still free: Number-kinded → Int, general → Unit (DESIGN.md §7.3, §8.4).
// Defaults are recorded in the checker's substitution so every other
// occurrence of the same variable — including environment schemes held by
// a live REPL session — resolves identically.
func zonkDefault(t types.Type, ck *infer.Checker) types.Type {
	t = ck.Sub.Apply(t)
	defaultFree(t, ck)
	return ck.Sub.Apply(t)
}

func defaultFree(t types.Type, ck *infer.Checker) {
	switch t := t.(type) {
	case *types.TVar:
		switch t.Kind {
		case types.Number:
			ck.Sub[t.ID] = ck.B.Int
		case types.General:
			ck.Sub[t.ID] = ck.B.Unit
		default:
			panic("elaborate: row variables arrive in S7")
		}
	case *types.TCon:
		for _, a := range t.Args {
			defaultFree(a, ck)
		}
	case *types.TFun:
		defaultFree(t.Arg, ck)
		defaultFree(t.Ret, ck)
	}
}
