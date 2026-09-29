package infer

import (
	"maps"
	"sort"

	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/types"
)

// Solve is the constraint solver. The signature is the reserved typeclass
// seam from doc/design.md, "Type inference": predicates flow in and residual predicates flow
// out — always empty until typeclasses exist, but every caller is already
// shaped for them. sub is the substitution to extend (the session
// substitution for REPL use); bi identifies the number types for
// Number-kinded metavariable checks.
func Solve(cs []Constraint, ps []types.Pred, sub Subst, bi *types.Builtins, sup *types.Supply) (Subst, []types.Pred, []diag.Error) {
	// A failure and a deferred constraint both remember the index they came
	// from, because postponing work must not reorder diagnostics.
	type failure struct {
		at  int
		err diag.Error
	}
	type pending struct {
		at int
		c  Constraint
	}
	var failures []failure
	var vs map[int][]polarity
	solve := func(at int, c Constraint) bool {
		var m *mismatch
		if c.Include {
			left, lok := sub.Apply(c.Left).(types.Row)
			right, rok := sub.Apply(c.Right).(types.Row)
			if !lok || !rok {
				m = &mismatch{a: c.Left, b: c.Right, effect: true, note: "effect inclusion requires two rows"}
			} else {
				m = includeRows(left, right, sub, bi, sup)
			}
		} else {
			m = unify(c.Left, c.Right, sub, bi, sup)
		}
		if m != nil {
			failures = append(failures, failure{at: at, err: mismatchError(c, m, sub)})
			return false
		}
		return true
	}
	var deferred []pending
	var bounds []pending
	var scopes []pending
	for i, c := range cs {
		if c.Scope != nil {
			scopes = append(scopes, pending{at: i, c: c})
		} else if c.Subsume {
			if vs == nil {
				vs = variances(c.ADTs)
			}
			rows, m := subsumption(c, sub, bi, sup, vs)
			if m != nil {
				failures = append(failures, failure{at: i, err: mismatchError(c, m, sub)})
			} else {
				for _, r := range rows {
					bounds = append(bounds, pending{at: i, c: r})
				}
			}
		} else if c.Include || c.Why.Kind == WhyEffectEscapes {
			bounds = append(bounds, pending{at: i, c: c})
		} else {
			solve(i, c)
		}
	}
	var bound, ordinary []pending
	for _, p := range bounds {
		if bindable(p.c, sub) {
			bound = append(bound, p)
		} else {
			ordinary = append(ordinary, p)
		}
	}
	bounds = ordinary
	// Propagate explicit lower bounds before closing scoped rows. Otherwise
	// an outer handler's upper bound can close a callback's residual row
	// before a sibling constraint contributes (for example) Fail String.
	if len(scopes) > 0 {
		labels := func() int {
			n := 0
			for _, p := range bounds {
				for _, side := range []types.Type{p.c.Left, p.c.Right} {
					if r, ok := sub.Apply(side).(types.Row); ok {
						n += len(r.Labels)
					}
				}
			}
			return n
		}
		for {
			before := labels()
			for _, p := range bounds {
				left, lok := sub.Apply(p.c.Left).(types.Row)
				right, rok := sub.Apply(p.c.Right).(types.Row)
				if !lok || !rok || len(left.Labels) == 0 {
					continue
				}
				trial := maps.Clone(sub)
				if includeRows(types.Row{Labels: left.Labels}, right, trial, bi, sup) == nil {
					maps.Copy(sub, trial)
				}
			}
			if labels() == before {
				break
			}
		}
	}
	// In scoped groups, apply fixed-tail upper bounds before growing flexible tails. In particular,
	// a pure outer scope must be closed before nested callback rows compose;
	// otherwise an inner fresh label can be needlessly assigned to its tail.
	for len(scopes) > 0 {
		var rest []pending
		progress := false
		for _, p := range bounds {
			if bindable(p.c, sub) {
				bound = append(bound, p)
				progress = true
				continue
			}
			right, ok := sub.Apply(p.c.Right).(types.Row)
			tail, rigidTail := right.Tail.(*types.TVar)
			if ok && (right.Tail == nil || rigidTail && tail.Rigid) {
				solve(p.at, p.c)
				progress = true
			} else {
				rest = append(rest, p)
			}
		}
		bounds = rest
		if !progress {
			break
		}
	}
	for _, p := range bounds {
		i, constraint := p.at, p.c
		if bindable(constraint, sub) {
			bound = append(bound, p)
			continue
		}
		labels, tail, split := splitRigidTail(constraint, sub)
		if !split {
			solve(i, constraint)
			continue
		}
		// The labels go in now — that keeps the surrounding row open — and the
		// rigid tail waits, so a body's statement order cannot decide whether
		// the row can still take a label. A failed label leaves the tail
		// alone rather than reporting the same call twice.
		if len(labels) > 0 {
			c := constraint
			c.Left = types.Row{Labels: labels}
			if !solve(i, c) {
				continue
			}
		}
		c := constraint
		c.Left = types.Row{Tail: tail}
		deferred = append(deferred, pending{at: i, c: c})
	}
	// A deferred tail is solved as soon as something else has closed the
	// surrounding row's own tail — an annotation, most often. Each pass may
	// release another, so iterate while there is progress.
	for len(deferred) > 0 {
		var rest []pending
		for _, p := range deferred {
			if _, _, wait := splitRigidTail(p.c, sub); wait {
				rest = append(rest, p)
				continue
			}
			solve(p.at, p.c)
		}
		if len(rest) == len(deferred) {
			break
		}
		deferred = rest
	}
	// What is left performs nothing beyond the annotated tail, so binding the
	// surrounding row to that tail is the answer rather than a guess.
	for _, p := range deferred {
		solve(p.at, p.c)
	}
	for _, p := range bound {
		if err, failed := solveBound(p.c, sub, bi, sup); failed {
			failures = append(failures, failure{at: p.at, err: err})
		}
	}
	// Row-tail expansion can expose another application of the same effect.
	// Revisit each solved row so unresolved overlaps unify their arguments;
	// fully resolved applications with different arguments remain distinct.
	for i, c := range cs {
		// Scope obligations have roots rather than a pair of type operands.
		// Their types are constrained by the surrounding ordinary constraints.
		if c.Scope != nil {
			continue
		}
		for _, side := range []types.Type{c.Left, c.Right} {
			if m := reconcileRows(side, sub, bi, sup); m != nil {
				failures = append(failures, failure{at: i, err: mismatchError(c, m, sub)})
				break
			}
		}
	}
	// Scope checks must also observe substitutions introduced by reconciling
	// duplicate labels, not just those from the deferred row bounds.
	for _, p := range scopes {
		for _, err := range p.c.Scope.check(sub, p.c.Span) {
			failures = append(failures, failure{at: p.at, err: err})
		}
	}
	sort.SliceStable(failures, func(i, j int) bool { return failures[i].at < failures[j].at })
	errs := make([]diag.Error, 0, len(failures))
	for _, f := range failures {
		errs = append(errs, f.err)
	}
	return sub, ps, errs
}

// splitRigidTail decides whether c is an inclusion of the shape
// `{L | e} ⊆ ρ` — `e` an annotation's rigid tail, ρ a surrounding row that is
// still open — and if so returns the labels to include now and the tail to
// include later. includeRows answers such a constraint by binding ρ's tail to
// `e`, and a row that ends in a rigid tail cannot absorb an effect
// afterwards, so solving it in place would let the order of a body's calls
// decide whether it checks: an `{e}` call before an `{Exception ex | e}` one
// would close the row against `Exception`. The two halves mean the same thing
// as the whole — every effect the callee performs is available here — but the
// label half leaves the surrounding tail open for later constraints.
func splitRigidTail(c Constraint, sub Subst) (labels []types.EffLabel, tail *types.TVar, ok bool) {
	if !c.Include {
		return nil, nil, false
	}
	subrow, isRow := sub.Apply(c.Left).(types.Row)
	if !isRow || subrow.Tail == nil {
		return nil, nil, false
	}
	rigid, isVar := subrow.Tail.(*types.TVar)
	if !isVar || !rigid.Rigid || rigid.Kind != types.RowVar {
		return nil, nil, false
	}
	// Labels already in the surrounding row are no help: binding closes its
	// tail either way, so a label a later constraint adds would still clash.
	ambient, isRow := sub.Apply(c.Right).(types.Row)
	if !isRow || ambient.Tail == nil {
		return nil, nil, false
	}
	open, isVar := ambient.Tail.(*types.TVar)
	if !isVar || open.Rigid || open.Kind != types.RowVar {
		return nil, nil, false
	}
	return subrow.Labels, rigid, true
}

func mismatchError(c Constraint, m *mismatch, sub Subst) diag.Error {
	if c.Subsume && m.effect {
		c.Why.Kind = WhyEffectNotAllowed
	}
	if c.Why.Kind == WhyAnnotation && m.effect {
		c.Why.Kind = WhyEffectMismatch
	}
	// An inclusion constraint compares two rows, so the value-shaped stories
	// (WhyCall above all) would print an effect row where a type belongs.
	if c.Include && c.Why.Kind == WhyCall {
		c.Why.Kind = WhyEffectNotAllowed
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
	case WhyProjection:
		e = diag.Errorf(c.Span, "TYPE MISMATCH", "The field `%s` holds:\n\n    %s\n\nbut it is used here as:\n\n    %s", c.Why.Name, left, right)
	case WhyCall:
		if c.Subsume {
			e = diag.Errorf(c.Span, "TYPE MISMATCH", "This argument has type:\n\n    %s\n\nbut the function expects:\n\n    %s", left, right)
		} else if _, ok := sub.Apply(c.Left).(*types.TFun); ok {
			e = diag.Errorf(c.Span, "TYPE MISMATCH", "This function's argument type does not match this application.\nThe function has type:\n\n    %s\n\nbut this application requires:\n\n    %s", left, right)
		} else {
			e = diag.Errorf(c.Span, "TYPE MISMATCH", "This is not a function, so I cannot give it an argument.\nIt has type:\n\n    %s", left)
		}
	case WhyIfCondition:
		e = diag.Errorf(c.Span, "TYPE MISMATCH",
			"An `if` condition must be a Bool, but this one is:\n\n    %s", left)
	case WhyIfBranches:
		e = diag.Errorf(c.Span, "TYPE MISMATCH",
			"The branches of this `if` do not match. The `else` branch is:\n\n    %s\n\nbut the `then` branch is:\n\n    %s",
			left, right)
	case WhyBoolOperand:
		e = diag.Errorf(c.Span, "TYPE MISMATCH",
			"Both sides of (%s) must be a Bool, but this one is:\n\n    %s",
			c.Why.Op, left)
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
			"Fango's (%s) only works on %s, but this operand is:\n\n    %s",
			c.Why.Op, c.Why.Want, left)
		if c.Why.Op == "/" && left == "Int" {
			e.Notes = append(e.Notes,
				"Note: there is no automatic Int-to-Float conversion — use a\nFloat value here, like `2.0` instead of `2`.")
		}
	case WhySpliceOperand:
		e = diag.Errorf(c.Span, "TYPE MISMATCH",
			"A splice pastes generated code, so `$(…)` needs an operand that builds\ncode with `quote`, but this one is:\n\n    %s", left)
	case WhyEffectEscapes:
		e = diag.Errorf(c.Span, "UNHANDLED EFFECT",
			"This top-level value performs an effect that is not handled.\nTop-level bindings must be pure; move the call into a function or add a handler.")
	case WhyEffectNotAllowed:
		e = diag.Errorf(c.Span, "EFFECT MISMATCH",
			"This expression performs effects the surrounding function does not allow.\nIt performs:\n\n    %s\n\nbut only these effects are available here:\n\n    %s", left, right)
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

// reconcileRows resolves duplicate or uncertain applications in one row.
func reconcileRows(t types.Type, sub Subst, bi *types.Builtins, sup *types.Supply) *mismatch {
	switch t := sub.Apply(t).(type) {
	case *types.TFun:
		for _, part := range []types.Type{t.Arg, t.Eff, t.Ret} {
			if m := reconcileRows(part, sub, bi, sup); m != nil {
				return m
			}
		}
	case *types.TCon:
		for _, arg := range t.Args {
			if m := reconcileRows(arg, sub, bi, sup); m != nil {
				return m
			}
		}
	case types.Row:
		for i, label := range t.Labels {
			for _, first := range t.Labels[:i] {
				if first.Unique != label.Unique || (!unresolvedEffectArgs(first.Args) && !unresolvedEffectArgs(label.Args)) {
					continue
				}
				if len(first.Args) != len(label.Args) {
					return &mismatch{a: t, b: t, effect: true, note: "the same effect label has different arity"}
				}
				for i := range first.Args {
					if m := unify(first.Args[i], label.Args[i], sub, bi, sup); m != nil {
						m.effect = true
						m.note = "unresolved applications of the same effect must have consistent arguments"
						return m
					}
				}
			}
			for _, arg := range label.Args {
				if m := reconcileRows(arg, sub, bi, sup); m != nil {
					return m
				}
			}
		}
		if t.Tail != nil {
			return reconcileRows(t.Tail, sub, bi, sup)
		}
	}
	return nil
}
