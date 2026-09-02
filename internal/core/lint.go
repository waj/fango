package core

import (
	"fmt"

	"github.com/waj/fango/internal/types"
)

// Lint asserts the Core invariants: after elaboration there are no
// metavariables anywhere, numeric operators have ground types, and every
// effect row is empty. It runs in every test (and under a debug flag later)
// — instantiation plumbing bugs are the design's top risk, and this is the
// tripwire.
func Lint(p *Prog) []error {
	var errs []error
	for _, d := range p.Defs {
		errs = append(errs, lintType(d.Type, "def "+d.Name)...)
		errs = append(errs, lintExpr(d.Body, "def "+d.Name)...)
	}
	return errs
}

func lintExpr(e Expr, where string) []error {
	var errs []error
	errs = append(errs, lintType(e.Type(), where)...)
	switch e := e.(type) {
	case *IntLit, *VarRef:
	case *BinOp:
		if con, ok := e.Ty.(*types.TCon); !ok || len(con.Args) != 0 {
			errs = append(errs, fmt.Errorf("%s: BinOp %s has non-ground type %s", where, e.Op, types.Show(e.Ty)))
		}
		errs = append(errs, lintExpr(e.L, where)...)
		errs = append(errs, lintExpr(e.R, where)...)
	case *App:
		errs = append(errs, lintExpr(e.Callee, where)...)
		for _, a := range e.Args {
			errs = append(errs, lintExpr(a, where)...)
		}
	default:
		errs = append(errs, fmt.Errorf("%s: unhandled Core node %T", where, e))
	}
	return errs
}

func lintType(t types.Type, where string) []error {
	switch t := t.(type) {
	case *types.TVar:
		return []error{fmt.Errorf("%s: metavariable survived elaboration", where)}
	case *types.TCon:
		var errs []error
		for _, a := range t.Args {
			errs = append(errs, lintType(a, where)...)
		}
		return errs
	case *types.TFun:
		var errs []error
		if !t.Eff.Empty() {
			errs = append(errs, fmt.Errorf("%s: non-empty effect row before S7", where))
		}
		errs = append(errs, lintType(t.Arg, where)...)
		errs = append(errs, lintType(t.Ret, where)...)
		return errs
	default:
		return []error{fmt.Errorf("%s: unhandled type %T", where, t)}
	}
}
