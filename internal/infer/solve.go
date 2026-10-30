package infer

import (
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
		m  *mismatch
		// callback marks a row bound of a scoped runner's selected
		// callback: what the passed function performs must fit its row.
		callback bool
	}
	var failures []failure
	var vs map[int][]polarity
	var ambiguous []pending
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
		if m != nil && m.ambiguous != nil {
			ambiguous = append(ambiguous, pending{at: at, c: c, m: m})
			return true
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
					bounds = append(bounds, pending{at: i, c: r, callback: c.ScopedCallback})
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
	// Propagate explicit lower bounds before closing residual rows. Otherwise
	// an outer call's upper bound can close a callback's residual row before
	// a sibling constraint contributes a label already present on the call.
	{
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
				if awaitsAmbient(p.c, sub) {
					// An unresolved application waits for the ambient row, but
					// independent concrete labels can already propagate.
					var resolved []types.EffLabel
					for _, label := range left.Labels {
						if !unresolvedEffectArgs(label.Args) {
							resolved = append(resolved, label)
						}
					}
					left.Labels = resolved
				}
				sub.trial(sup, func() *mismatch {
					return includeRows(types.Row{Labels: left.Labels}, right, sub, bi, sup)
				})
			}
			if labels() == before {
				break
			}
		}
	}
	// In scoped groups, apply fixed-tail upper bounds before growing flexible tails. In particular,
	// a pure outer scope must be closed before nested callback rows compose;
	// otherwise an inner fresh label can be needlessly assigned to its tail.
	// A scoped callback's own upper bound goes first too, even though the
	// runner's ambient tail in it is flexible. A use of that ambient row in
	// the callback body, such as a reader operation's `{#s}`, is also an
	// inclusion into the body's row; solved first, it would equate the two
	// and hand the callback's explicit labels, which the runner handles, to
	// the ambient tail and so to the caller.
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
			if ok && (right.Tail == nil || rigidTail && tail.Rigid || p.callback && hasScopedPermission(right)) {
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
	// An inclusion whose own labels still have unresolved arguments, going
	// into a row that may still grow, waits for the other inclusions. Solved
	// early, its unresolved application would pair with whichever sibling
	// occurrence of the effect had already reached that row, and a body's
	// statement order would decide which of several applications it means.
	// Solved after the others, it meets the row's final applications, so one
	// candidate is taken and several are reported as ambiguous.
	var awaiting []pending
	place := func(p pending, postpone bool) {
		i, constraint := p.at, p.c
		if bindable(constraint, sub) {
			bound = append(bound, p)
			return
		}
		if postpone && awaitsAmbient(constraint, sub) {
			awaiting = append(awaiting, p)
			return
		}
		labels, tail, split := splitRigidTail(constraint, sub)
		if !split {
			solve(i, constraint)
			return
		}
		// The labels go in now — that keeps the surrounding row open — and the
		// rigid tail waits, so a body's statement order cannot decide whether
		// the row can still take a label. A failed label leaves the tail
		// alone rather than reporting the same call twice.
		if len(labels) > 0 {
			c := constraint
			c.Left = types.Row{Labels: labels}
			if !solve(i, c) {
				return
			}
		}
		c := constraint
		c.Left = types.Row{Tail: tail}
		deferred = append(deferred, pending{at: i, c: c})
	}
	for _, p := range bounds {
		place(p, true)
	}
	for _, p := range awaiting {
		place(p, false)
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
	// An ambiguous application waits for other constraints to determine its
	// arguments. Each pass may settle another, so retry while there is
	// progress; what remains is reported rather than chosen by label order.
	for len(ambiguous) > 0 {
		retry := ambiguous
		ambiguous = nil
		for _, p := range retry {
			solve(p.at, p.c)
		}
		if len(ambiguous) == len(retry) {
			break
		}
	}
	for _, p := range ambiguous {
		failures = append(failures, failure{at: p.at, err: ambiguousError(p.c, p.m, sub)})
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
				if m.ambiguous != nil {
					failures = append(failures, failure{at: i, err: ambiguousError(c, m, sub)})
				} else {
					failures = append(failures, failure{at: i, err: mismatchError(c, m, sub)})
				}
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
// `{L, e} ⊆ ρ` — `e` an annotation's rigid tail, ρ a surrounding row that is
// still open — and if so returns the labels to include now and the tail to
// include later. includeRows answers such a constraint by binding ρ's tail to
// `e`, and a row that ends in a rigid tail cannot absorb an effect
// afterwards, so solving it in place would let the order of a body's calls
// decide whether it checks: an `{e}` call before an `{Exception ex, e}` one
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

// ambiguousError reports an effect application that could mean more than one
// application of its effect in the same row.
func ambiguousError(c Constraint, m *mismatch, sub Subst) diag.Error {
	p := types.NewPrinter().ForMessage(sub.Apply(m.b))
	name := types.SurfaceName(m.ambiguous.Name)
	return diag.Errorf(c.Span, "AMBIGUOUS EFFECT",
		"`%s` appears more than once in this row, with different arguments:\n\n    %s\n\nand nothing here says which one is meant. Name it in a type the body can see:\na pattern on its payload, or a helper whose callback parameter lists only\nthat application, such as\n\n    (() ->{%s T, e} a) ->{e} Result T a",
		name, p.Type(sub.Apply(m.b)), name)
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
			if unresolvedEffectArgs(label.Args) {
				same := 0
				for j, other := range t.Labels {
					if j != i && other.Unique == label.Unique {
						same++
					}
				}
				if same > 1 {
					return &mismatch{a: t, b: t, effect: true, ambiguous: &t.Labels[i]}
				}
			}
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

// awaitsAmbient reports an inclusion whose subrow names an application with
// unresolved arguments and whose superrow may still gain applications: its
// tail is a flexible row variable. Such a constraint is solved after the
// other inclusions so the final row, not a sibling occurrence, decides
// whether the application is unique.
func awaitsAmbient(c Constraint, sub Subst) bool {
	left, lok := sub.Apply(c.Left).(types.Row)
	right, rok := sub.Apply(c.Right).(types.Row)
	if !lok || !rok {
		return false
	}
	tail, ok := right.Tail.(*types.TVar)
	if !ok || tail.Rigid {
		return false
	}
	for _, label := range left.Labels {
		if unresolvedEffectArgs(label.Args) {
			return true
		}
	}
	return false
}
