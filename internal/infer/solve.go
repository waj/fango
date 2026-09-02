package infer

import (
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/types"
)

// Solve is the constraint solver. The signature is the reserved typeclass
// seam from DESIGN.md §7.2: predicates flow in and residual predicates flow
// out — always empty until typeclasses exist, but every caller is already
// shaped for them. sub is the substitution to extend (the session
// substitution for REPL use); bi identifies the number types for
// Number-kinded metavariable checks.
func Solve(cs []Constraint, ps []types.Pred, sub Subst, bi *types.Builtins) (Subst, []types.Pred, []diag.Error) {
	var errs []diag.Error
	for _, c := range cs {
		if m := unify(c.Left, c.Right, sub, bi); m != nil {
			errs = append(errs, mismatchError(c, m, sub))
		}
	}
	return sub, ps, errs
}

func mismatchError(c Constraint, m *mismatch, sub Subst) diag.Error {
	p := types.NewPrinter()
	got := p.Type(sub.Apply(m.a))
	want := p.Type(sub.Apply(m.b))
	var e diag.Error
	switch c.Why.Kind {
	case WhyOperand:
		e = diag.Errorf(c.Span, "TYPE MISMATCH",
			"I cannot use (%s) with this operand:\n\n    %s\n\nIt does not match the other side:\n\n    %s",
			c.Why.Op, got, want)
	default:
		e = diag.Errorf(c.Span, "TYPE MISMATCH",
			"These types do not match:\n\n    %s\n\nand\n\n    %s", got, want)
	}
	if m.note != "" {
		e.Notes = append(e.Notes, "Note: "+m.note)
	}
	return e
}
