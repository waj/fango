package infer

import (
	"fmt"
	"sort"

	"github.com/waj/fango/internal/types"
)

// Subst maps metavariable IDs to types. Bindings may chain (var → var);
// walk resolves one level of chains, Apply zonks deeply.
type Subst map[int]types.Type

// walk resolves t through the substitution until it is not a bound
// metavariable. It does not descend into structure. Rigid vars are never
// bound (their IDs are never Subst keys), and walk stops on them defensively
// so a stray write could not silently solve a skolem.
func (s Subst) walk(t types.Type) types.Type {
	for {
		v, ok := t.(*types.TVar)
		if !ok || v.Rigid {
			return t
		}
		bound, ok := s[v.ID]
		if !ok {
			return t
		}
		t = bound
	}
}

// Apply fully substitutes t (zonking).
func (s Subst) Apply(t types.Type) types.Type {
	t = s.walk(t)
	switch t := t.(type) {
	case *types.TVar:
		return t
	case *types.TCon:
		if len(t.Args) == 0 {
			return t
		}
		args := make([]types.Type, len(t.Args))
		for i, a := range t.Args {
			args[i] = s.Apply(a)
		}
		return &types.TCon{Unique: t.Unique, Name: t.Name, Args: args}
	case *types.TFun:
		return &types.TFun{Arg: s.Apply(t.Arg), Eff: s.applyRow(t.Eff), Ret: s.Apply(t.Ret), Control: t.Control, OpenRow: t.OpenRow}
	case types.Row:
		return s.applyRow(t)
	default:
		panic(fmt.Sprintf("infer.Subst.Apply: unhandled %T", t))
	}
}

func (s Subst) applyRow(r types.Row) types.Row {
	labels := make([]types.EffLabel, len(r.Labels))
	for i, l := range r.Labels {
		args := make([]types.Type, len(l.Args))
		for j, a := range l.Args {
			args[j] = s.Apply(a)
		}
		labels[i] = types.EffLabel{Unique: l.Unique, Name: l.Name, Args: args, Abort: l.Abort, Scoped: l.Scoped, Binding: l.Binding}
	}
	var tail types.Type
	if r.Tail != nil {
		tail = s.Apply(r.Tail)
		if extra, ok := tail.(types.Row); ok {
			labels = append(labels, extra.Labels...)
			tail = extra.Tail
		}
	}
	sorted := types.SortedRow(types.Row{Labels: labels, Tail: tail})
	// Row-tail expansion can expose the same label through both the prefix
	// and the substituted tail. Canonicalize identical occurrences; retain
	// conflicting parameterizations so unifyRows can diagnose them.
	canonical := sorted.Labels[:0]
	for _, label := range sorted.Labels {
		if len(canonical) > 0 && equalEffLabel(canonical[len(canonical)-1], label) {
			continue
		}
		canonical = append(canonical, label)
	}
	sorted.Labels = canonical
	return sorted
}

func equalEffLabel(a, b types.EffLabel) bool {
	if a.Unique != b.Unique || len(a.Args) != len(b.Args) {
		return false
	}
	for i := range a.Args {
		if !types.Equal(a.Args[i], b.Args[i]) {
			return false
		}
	}
	return true
}

// mismatch is a leaf unification failure; Solve dresses it in a diagnostic
// using the constraint's Why and Span.
type mismatch struct {
	a, b   types.Type
	note   string // extra context, e.g. "a Number literal cannot be String"
	effect bool
}

// unify makes a and b equal under sub, binding metavariables in place.
// Returns nil on success.
func unify(a, b types.Type, sub Subst, bi *types.Builtins, sup *types.Supply) *mismatch {
	a, b = sub.walk(a), sub.walk(b)

	// A row with no labels and an open tail is just its tail. Normalizing
	// here lets a fresh row metavariable wrapped by unifyRows bind against a
	// rigid annotation tail (e.g. the `e` in `(() ->{Ask | e} a) ->{e} a`)
	// instead of tripping the rigid-vs-structure mismatch below.
	if ar, ok := a.(types.Row); ok && len(ar.Labels) == 0 && ar.Tail != nil {
		return unify(ar.Tail, b, sub, bi, sup)
	}
	if br, ok := b.(types.Row); ok && len(br.Labels) == 0 && br.Tail != nil {
		return unify(a, br.Tail, sub, bi, sup)
	}

	// Metas bind; rigid vars (skolems, scheme-bound vars) are atomic: equal
	// only to themselves, a mismatch against everything else — the direction
	// that keeps an annotation's variables fully general (doc/design.md, "Type inference").
	if av, ok := a.(*types.TVar); ok && !av.Rigid {
		return bindVar(av, b, sub, bi)
	}
	if bv, ok := b.(*types.TVar); ok && !bv.Rigid {
		return bindVar(bv, a, sub, bi)
	}
	if av, ok := a.(*types.TVar); ok {
		if bv, ok := b.(*types.TVar); ok && bv.ID == av.ID {
			return nil
		}
		return rigidMismatch(a, b, av)
	}
	if bv, ok := b.(*types.TVar); ok {
		return rigidMismatch(a, b, bv)
	}

	switch a := a.(type) {
	case *types.TCon:
		bcon, ok := b.(*types.TCon)
		if !ok || a.Unique != bcon.Unique || len(a.Args) != len(bcon.Args) {
			return &mismatch{a: a, b: b}
		}
		for i := range a.Args {
			if m := unify(a.Args[i], bcon.Args[i], sub, bi, sup); m != nil {
				return m
			}
		}
		return nil
	case *types.TFun:
		bfun, ok := b.(*types.TFun)
		if !ok {
			return &mismatch{a: a, b: b}
		}
		if m := unify(a.Arg, bfun.Arg, sub, bi, sup); m != nil {
			return m
		}
		if m := unifyRows(a.Eff, bfun.Eff, sub, bi, sup); m != nil {
			return m
		}
		return unify(a.Ret, bfun.Ret, sub, bi, sup)
	case types.Row:
		br, ok := b.(types.Row)
		if !ok {
			return &mismatch{a: a, b: b}
		}
		return unifyRows(a, br, sub, bi, sup)
	default:
		panic(fmt.Sprintf("infer.unify: unhandled %T", a))
	}
}

// rigidMismatch explains a failure to make the rigid annotation variable v
// equal to something else. A rigid row tail gets its own story: whatever the
// caller instantiates it with, the body may not add an effect to it.
func rigidMismatch(a, b types.Type, v *types.TVar) *mismatch {
	if v.Kind == types.RowVar {
		return &mismatch{a: a, b: b, effect: true,
			note: "an annotation's open row tail cannot absorb an effect the annotation does not list"}
	}
	return &mismatch{a: a, b: b, note: "a type variable from an annotation must stay fully general"}
}

// bindVar binds metavariable v to t, respecting kinds and the occurs check.
// t is already walked and is not a bound variable.
func bindVar(v *types.TVar, t types.Type, sub Subst, bi *types.Builtins) *mismatch {
	if tv, ok := t.(*types.TVar); ok && tv.ID == v.ID {
		return nil
	}
	if occurs(v, t, sub) {
		return &mismatch{a: v, b: t, note: "this would create an infinite type"}
	}
	switch v.Kind {
	case types.General:
		if tv, ok := t.(*types.TVar); ok && tv.Kind == types.RowVar {
			return &mismatch{a: v, b: t, note: "an effect row cannot be used as a value type"}
		}
		if _, ok := t.(types.Row); ok {
			return &mismatch{a: v, b: t, note: "an effect row cannot be used as a value type"}
		}
		sub[v.ID] = t
		return nil
	case types.RowVar:
		switch t := t.(type) {
		case *types.TVar:
			if t.Kind != types.RowVar {
				return &mismatch{a: v, b: t, note: "an effect row cannot be a value type"}
			}
			sub[v.ID] = t
			return nil
		case types.Row:
			sub[v.ID] = t
			return nil
		default:
			return &mismatch{a: v, b: t, note: "an effect row cannot be a value type"}
		}
	default:
		panic("infer: unknown variable kind")
	}
}

func occurs(v *types.TVar, t types.Type, sub Subst) bool {
	t = sub.walk(t)
	switch t := t.(type) {
	case *types.TVar:
		return !t.Rigid && t.ID == v.ID
	case *types.TCon:
		for _, a := range t.Args {
			if occurs(v, a, sub) {
				return true
			}
		}
		return false
	case *types.TFun:
		return occurs(v, t.Arg, sub) || occurs(v, t.Eff, sub) || occurs(v, t.Ret, sub)
	case types.Row:
		for _, l := range t.Labels {
			for _, a := range l.Args {
				if occurs(v, a, sub) {
					return true
				}
			}
		}
		return t.Tail != nil && occurs(v, t.Tail, sub)
	default:
		return false
	}
}

func unifyRows(a, b types.Row, sub Subst, bi *types.Builtins, sup *types.Supply) *mismatch {
	a, b = sub.applyRow(a), sub.applyRow(b)
	am, bm := map[int]types.EffLabel{}, map[int]types.EffLabel{}
	for _, l := range a.Labels {
		if _, dup := am[l.Unique]; dup {
			return &mismatch{a: a, b: b, effect: true, note: "an effect may appear at most once in a row (distinct-label rule)"}
		}
		am[l.Unique] = l
	}
	for _, l := range b.Labels {
		if _, dup := bm[l.Unique]; dup {
			return &mismatch{a: a, b: b, effect: true, note: "an effect may appear at most once in a row (distinct-label rule)"}
		}
		bm[l.Unique] = l
	}
	var left, right []types.EffLabel
	for u, al := range am {
		if bl, ok := bm[u]; ok {
			if len(al.Args) != len(bl.Args) {
				return &mismatch{a: a, b: b, effect: true, note: "the same effect label has different arity"}
			}
			for i := range al.Args {
				if m := unify(al.Args[i], bl.Args[i], sub, bi, sup); m != nil {
					m.effect = true
					m.note = "a parameterized effect may appear only once in a row, with one consistent set of arguments (distinct-label rule)"
					return m
				}
			}
		} else {
			left = append(left, al)
		}
	}
	for u, bl := range bm {
		if _, ok := am[u]; !ok {
			right = append(right, bl)
		}
	}
	sort.Slice(left, func(i, j int) bool { return left[i].Unique < left[j].Unique })
	sort.Slice(right, func(i, j int) bool { return right[i].Unique < right[j].Unique })
	lt, rt := a.Tail, b.Tail
	if len(left) == 0 && len(right) == 0 {
		switch {
		case lt == nil && rt == nil:
			return nil
		case lt == nil:
			return unify(rt, types.Row{}, sub, bi, sup)
		case rt == nil:
			return unify(lt, types.Row{}, sub, bi, sup)
		default:
			return unify(lt, rt, sub, bi, sup)
		}
	}
	if lv, lok := lt.(*types.TVar); lok {
		if rv, rok := rt.(*types.TVar); rok && lv.ID == rv.ID {
			return &mismatch{a: a, b: b, effect: true, note: "the same open row cannot contain conflicting effect labels"}
		}
	}
	switch {
	case lt == nil && rt == nil:
		if len(left) > 0 || len(right) > 0 {
			return &mismatch{a: a, b: b, effect: true, note: "these closed effect rows contain different effects"}
		}
		return nil
	case lt == nil:
		if len(right) > 0 {
			return &mismatch{a: a, b: b, effect: true, note: "a closed effect row cannot absorb additional effects"}
		}
		return unify(rt, types.Row{Labels: left}, sub, bi, sup)
	case rt == nil:
		if len(left) > 0 {
			return &mismatch{a: a, b: b, effect: true, note: "a closed effect row cannot absorb additional effects"}
		}
		return unify(lt, types.Row{Labels: right}, sub, bi, sup)
	default:
		rho := sup.FreshVar(types.RowVar)
		if m := unify(lt, types.Row{Labels: right, Tail: rho}, sub, bi, sup); m != nil {
			return m
		}
		return unify(rt, types.Row{Labels: left, Tail: rho}, sub, bi, sup)
	}
}

// includeRows constrains every effect in subrow to occur in superrow while
// preserving any effects already present in superrow. An open subrow may be
// weakened to the complete surrounding row; a closed subrow contributes
// only its explicit labels.
func includeRows(subrow, superrow types.Row, sub Subst, bi *types.Builtins, sup *types.Supply) *mismatch {
	subrow, superrow = sub.applyRow(subrow), sub.applyRow(superrow)
	if left, ok := subrow.Tail.(*types.TVar); ok {
		if right, ok := superrow.Tail.(*types.TVar); ok && left.ID == right.ID {
			for _, row := range []types.Row{subrow, superrow} {
				seen := map[int]bool{}
				for _, label := range row.Labels {
					if seen[label.Unique] {
						return &mismatch{a: subrow, b: superrow, effect: true, note: "an effect may appear at most once in a row (distinct-label rule)"}
					}
					seen[label.Unique] = true
				}
			}
			// A shared tail is an inclusion bound, not an equality. A callback
			// can add a label to an open shared view even when that label also
			// appears explicitly in its own row.
			missing := []types.EffLabel{}
			for _, label := range subrow.Labels {
				found := false
				for _, allowed := range superrow.Labels {
					if label.Unique != allowed.Unique {
						continue
					}
					found = true
					if m := unifyRows(types.Row{Labels: []types.EffLabel{label}}, types.Row{Labels: []types.EffLabel{allowed}}, sub, bi, sup); m != nil {
						return m
					}
				}
				if !found {
					missing = append(missing, label)
				}
			}
			if len(missing) == 0 {
				return nil
			}
			if !left.Rigid {
				return bindVar(left, types.Row{Labels: missing, Tail: sup.FreshVar(types.RowVar)}, sub, bi)
			}
		}
	}
	if subrow.Tail != nil {
		if len(subrow.Labels) == 0 {
			if sv, ok := subrow.Tail.(*types.TVar); ok && sv.Rigid && sv.Kind == types.RowVar {
				if tv, ok := superrow.Tail.(*types.TVar); ok && tv.Rigid && tv.ID == sv.ID {
					return nil
				}
				if tv, ok := superrow.Tail.(*types.TVar); ok && !tv.Rigid && tv.Kind == types.RowVar {
					return bindVar(tv, sv, sub, bi)
				}
			}
		}
		// A fresh permission is an available capability, not an effect to
		// invent when widening an unrelated open callback. In particular a
		// handler's subject may bind closures, but an ordinary action passed
		// into that subject must not acquire its local permission merely by
		// being called there. Actual uses contribute the permission as an
		// explicit lower bound.
		allowed := superrow
		allowed.Labels = nil
		for _, label := range superrow.Labels {
			if !label.Binding || rowHasLabel(subrow, label.Unique) {
				allowed.Labels = append(allowed.Labels, label)
			}
		}
		return unifyRows(subrow, allowed, sub, bi, sup)
	}
	seen := map[int]bool{}
	for _, label := range subrow.Labels {
		if seen[label.Unique] {
			return &mismatch{a: subrow, b: superrow, effect: true, note: "an effect may appear at most once in a row (distinct-label rule)"}
		}
		seen[label.Unique] = true
		rest := sup.FreshVar(types.RowVar)
		if m := unifyRows(superrow, types.Row{Labels: []types.EffLabel{label}, Tail: rest}, sub, bi, sup); m != nil {
			return m
		}
		superrow = sub.applyRow(superrow)
	}
	return nil
}
