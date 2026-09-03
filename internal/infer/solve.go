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
func Solve(cs []Constraint, ps []types.Pred, sub Subst, bi *types.Builtins, sup *types.Supply) (Subst, []types.Pred, []diag.Error) {
	var errs []diag.Error
	for _, c := range cs {
		if m := unify(c.Left, c.Right, sub, bi, sup); m != nil {
			errs = append(errs, mismatchError(c, m, sub))
		}
	}
	return sub, ps, errs
}

func mismatchError(c Constraint, m *mismatch, sub Subst) diag.Error {
	if c.Why.Kind == WhyAnnotation && m.effect {
		c.Why.Kind = WhyEffectMismatch
	}
	p := types.NewPrinter()
	// The constraint's own sides give the top-level story; the mismatch
	// pair (m) is the leaf that failed, surfaced via m.note when set.
	left := p.Type(sub.Apply(c.Left))
	right := p.Type(sub.Apply(c.Right))
	var e diag.Error
	switch c.Why.Kind {
	case WhyOperand:
		e = diag.Errorf(c.Span, "TYPE MISMATCH",
			"I cannot use (%s) with this operand:\n\n    %s\n\nIt does not match the other side:\n\n    %s",
			c.Why.Op, left, right)
	case WhyCall:
		e = diag.Errorf(c.Span, "TYPE MISMATCH",
			"This is not a function, so I cannot give it an argument.\nIt has type:\n\n    %s", left)
	case WhyIfCondition:
		e = diag.Errorf(c.Span, "TYPE MISMATCH",
			"An `if` condition must be a Bool, but this one is:\n\n    %s", left)
	case WhyIfBranches:
		e = diag.Errorf(c.Span, "TYPE MISMATCH",
			"The branches of this `if` do not match. The `else` branch is:\n\n    %s\n\nbut the `then` branch is:\n\n    %s",
			left, right)
	case WhyCompare:
		e = diag.Errorf(c.Span, "TYPE MISMATCH",
			"Both sides of (%s) must be the same type, but this side is:\n\n    %s\n\nand the other side is:\n\n    %s",
			c.Why.Op, left, right)
	case WhyNegate:
		e = diag.Errorf(c.Span, "TYPE MISMATCH",
			"I can only negate numbers, but this is:\n\n    %s", left)
	case WhyRecursion:
		e = diag.Errorf(c.Span, "TYPE MISMATCH",
			"The recursive uses of `%s` do not match its definition.\nRecursive uses need:\n\n    %s\n\nbut the definition builds:\n\n    %s",
			c.Why.Name, left, right)
	case WhyAnnotation:
		e = diag.Errorf(c.Span, "TYPE MISMATCH",
			"The type annotation for `%s` says it is:\n\n    %s\n\nbut the body I found is:\n\n    %s",
			c.Why.Name, left, right)
	case WhyPattern:
		e = diag.Errorf(c.Span, "TYPE MISMATCH",
			"This pattern matches values of type:\n\n    %s\n\nbut it needs to match:\n\n    %s", left, right)
	case WhyCaseBranches:
		e = diag.Errorf(c.Span, "TYPE MISMATCH",
			"The branches of this `case` do not match. This branch is:\n\n    %s\n\nbut the earlier branches are:\n\n    %s",
			left, right)
	case WhyOpRequires:
		e = diag.Errorf(c.Span, "TYPE MISMATCH",
			"fango's (%s) only works on %s, but this operand is:\n\n    %s",
			c.Why.Op, c.Why.Want, left)
		if c.Why.Op == "/" && left == "Int" {
			e.Notes = append(e.Notes,
				"Note: there is no automatic Int-to-Float conversion — use a\nFloat value here, like `2.0` instead of `2`.")
		}
	case WhyEffectEscapes:
		e = diag.Errorf(c.Span, "UNHANDLED EFFECT",
			"This top-level value performs an effect that is not handled.\nTop-level bindings must be pure; move the call into a function or add a handler.")
	case WhyEffectMismatch:
		e = diag.Errorf(c.Span, "EFFECT MISMATCH",
			"The effect row in this annotation does not match the effects performed by its body.\nThe annotation says:\n\n    %s\n\nbut the body requires:\n\n    %s", left, right)
	default:
		e = diag.Errorf(c.Span, "TYPE MISMATCH",
			"These types do not match:\n\n    %s\n\nand\n\n    %s", left, right)
	}
	if m.note != "" {
		e.Notes = append(e.Notes, "Note: "+m.note)
	}
	return e
}
