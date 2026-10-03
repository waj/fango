package infer

import (
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/types"
)

// Binding a closure to a handler activation. A closure written in a handler's
// subject performs the handled effect, so its row names that label. Where the
// position it flows into omits the label, the label is replaced by a fresh local permission and what the
// handler's clauses perform: the closure then reaches this activation at every
// call instead of whichever handler is innermost when it is called.
// Elaboration completes the binding by substituting the activation's captures
// into the closure's body, the same discharge that gives a `Runtime.Scope.bracket`
// release closure its definition-site evidence.
//
// [Reference](../../doc/reference/effects.md), "Binding a closure to a handler
// activation"; [design](../../doc/design/effects.md), "Binding a closure to an
// activation".

// boundHandler is the innermost handler of label u whose subject contains the
// closure this constraint adapts. Nested activations of one effect are
// distinct, and the innermost one is the evidence elaboration will capture.
func boundHandler(bind []*HandlerInfo, label types.EffLabel) *HandlerInfo {
	for i := len(bind) - 1; i >= 0; i-- {
		for _, handled := range bind[i].Effects {
			if sameOrUnresolvedEffect(handled, label) {
				return bind[i]
			}
		}
	}
	return nil
}

func rowHasApplication(r types.Row, label types.EffLabel) bool {
	for _, l := range r.Labels {
		if sameOrUnresolvedEffect(l, label) {
			return true
		}
	}
	return false
}

func sameOrUnresolvedEffect(a, b types.EffLabel) bool {
	return a.Unique == b.Unique && (types.EffectLabelKey(a) == types.EffectLabelKey(b) || unresolvedEffectArgs(a.Args) || unresolvedEffectArgs(b.Args))
}

// rowAbsorbs reports whether a row can still gain a label it does not name.
// One that can needs no binding: ordinary inclusion already answers it.
func rowAbsorbs(r types.Row) bool {
	v, ok := r.Tail.(*types.TVar)
	return ok && !v.Rigid && v.Kind == types.RowVar
}

func constraintRows(c Constraint, sub Subst) (types.Row, types.Row, bool) {
	left, lok := sub.Apply(c.Left).(types.Row)
	right, rok := sub.Apply(c.Right).(types.Row)
	return left, right, lok && rok
}

// bindable reports whether the binding rule may have to answer this inclusion,
// which is what makes it this rule's constraint rather than a mismatch. It is
// decided before the ordinary solver runs, because a failing row unification
// can bind variables on its way to the failure.
//
// A position whose row is still open is a candidate too, rather than an
// absorption settled on the spot. A closure inside a row-indexed record or
// constructor reaches its field through the container's row argument, which is
// a fresh variable until the container itself is checked; absorbing the label
// there carries it out on the container's type, where the inclusion that
// finally rejects it names no handler to bind to. Whether such a position can
// absorb is not known until the group is solved, and solveBound answers the
// ones that still can by ordinary inclusion.
func bindable(c Constraint, sub Subst) bool {
	if !c.Include || len(c.Bind) == 0 {
		return false
	}
	left, right, ok := constraintRows(c, sub)
	if !ok {
		return false
	}
	for _, l := range left.Labels {
		if !rowHasApplication(right, l) && boundHandler(c.Bind, l) != nil {
			return true
		}
	}
	return false
}

// clauseRow is what a handler's clauses perform, which is what calling a
// closure bound to its activation performs. A row variable still open there
// carries nothing more, so only a rigid tail survives into the answer.
func clauseRow(info *HandlerInfo, sub Subst) types.Row {
	var out types.Row
	for _, eff := range info.ClauseEffects {
		r, ok := sub.Apply(eff).(types.Row)
		if !ok {
			continue
		}
		for _, l := range r.Labels {
			if !rowHasApplication(out, l) {
				out.Labels = append(out.Labels, l)
			}
		}
		if v, ok := r.Tail.(*types.TVar); ok && v.Rigid && out.Tail == nil {
			out.Tail = v
		}
	}
	return out
}

// solveBound replaces every bound label with its handler's clause row and
// includes the result, so the adapted arrow states what calling the closure
// really performs. It runs after the ordinary bounds, because the clause
// bodies are generated after the subject that holds the closure.
func solveBound(c Constraint, sub Subst, bi *types.Builtins, sup *types.Supply) (diag.Error, bool) {
	left, right, ok := constraintRows(c, sub)
	if !ok {
		return mismatchError(c, &mismatch{a: c.Left, b: c.Right, effect: true, note: "effect inclusion requires two rows"}, sub), true
	}
	if rowAbsorbs(right) {
		if m := includeRows(left, right, sub, bi, sup); m != nil {
			return mismatchError(c, m, sub), true
		}
		return diag.Error{}, false
	}
	var adapted, clauses types.Row
	for _, l := range left.Labels {
		info := boundHandler(c.Bind, l)
		if info == nil || rowHasApplication(right, l) {
			adapted.Labels = append(adapted.Labels, l)
			continue
		}
		if l.Abort {
			return diag.Errorf(c.Span, "BOUND ABORT OPERATION",
				"This closure performs `%s`, an abort-only effect, and the position it goes to\ndoes not allow that effect. Binding it to the handler whose subject holds it\nwould be the only way to accept it, and an abort cannot be bound.\n\nAn abort unwinds to its own handler activation, so a bound abort called after\nthat activation finished would unwind to a target nothing awaits. Keep `%s`\nin this arrow's row, or handle it where it is performed.",
				types.SurfaceName(l.Name), types.SurfaceName(l.Name)), true
		}
		row := info.Permission.Within(clauseRow(info, sub))
		for _, cl := range row.Labels {
			if !rowHasApplication(clauses, cl) {
				clauses.Labels = append(clauses.Labels, cl)
			}
		}
		if row.Tail != nil && clauses.Tail == nil {
			clauses.Tail = row.Tail
		}
	}
	for _, l := range clauses.Labels {
		if !rowHasApplication(adapted, l) {
			adapted.Labels = append(adapted.Labels, l)
		}
	}
	adapted.Tail = left.Tail
	m := includeRows(adapted, right, sub, bi, sup)
	if m == nil && clauses.Tail != nil {
		m = includeRows(types.Row{Tail: clauses.Tail}, right, sub, bi, sup)
	}
	if m == nil {
		return diag.Error{}, false
	}
	p := types.NewPrinter()
	return diag.Errorf(c.Span, "HANDLER BINDING EFFECTS",
		"Binding this closure to the handler whose subject holds it makes every call to it\nrequire its scoped permission and run its clauses, which perform:\n\n    %s\n\nbut only these effects are available here:\n\n    %s",
		p.Type(clauses), p.Type(sub.Apply(c.Right))), true
}
