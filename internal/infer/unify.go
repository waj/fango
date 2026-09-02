package infer

import (
	"fmt"

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
		if !t.Eff.Empty() {
			panic("infer: non-empty effect row before S7")
		}
		return &types.TFun{Arg: s.Apply(t.Arg), Eff: t.Eff, Ret: s.Apply(t.Ret)}
	default:
		panic(fmt.Sprintf("infer.Subst.Apply: unhandled %T", t))
	}
}

// mismatch is a leaf unification failure; Solve dresses it in a diagnostic
// using the constraint's Why and Span.
type mismatch struct {
	a, b types.Type
	note string // extra context, e.g. "a Number literal cannot be String"
}

// unify makes a and b equal under sub, binding metavariables in place.
// Returns nil on success.
func unify(a, b types.Type, sub Subst, bi *types.Builtins) *mismatch {
	a, b = sub.walk(a), sub.walk(b)

	// Metas bind; rigid vars (skolems, scheme-bound vars) are atomic: equal
	// only to themselves, a mismatch against everything else — the direction
	// that keeps an annotation's variables fully general (§7.2).
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
		return &mismatch{a: a, b: b, note: "a type variable from an annotation must stay fully general"}
	}
	if _, ok := b.(*types.TVar); ok {
		return &mismatch{a: a, b: b, note: "a type variable from an annotation must stay fully general"}
	}

	switch a := a.(type) {
	case *types.TCon:
		bcon, ok := b.(*types.TCon)
		if !ok || a.Unique != bcon.Unique || len(a.Args) != len(bcon.Args) {
			return &mismatch{a: a, b: b}
		}
		for i := range a.Args {
			if m := unify(a.Args[i], bcon.Args[i], sub, bi); m != nil {
				return m
			}
		}
		return nil
	case *types.TFun:
		bfun, ok := b.(*types.TFun)
		if !ok {
			return &mismatch{a: a, b: b}
		}
		if !a.Eff.Empty() || !bfun.Eff.Empty() {
			panic("infer: row unification arrives in S7")
		}
		if m := unify(a.Arg, bfun.Arg, sub, bi); m != nil {
			return m
		}
		return unify(a.Ret, bfun.Ret, sub, bi)
	default:
		panic(fmt.Sprintf("infer.unify: unhandled %T", a))
	}
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
		sub[v.ID] = t
		return nil
	case types.Number:
		switch t := t.(type) {
		case *types.TVar:
			if t.Kind == types.Number {
				sub[v.ID] = t
				return nil
			}
			if t.Rigid {
				// A General rigid var is an annotation variable claiming
				// full generality — a Number obligation cannot narrow it
				// (the reverse binding would silently solve the skolem).
				return &mismatch{a: v, b: t, note: "the annotation says this can be any type, but it is used as a number"}
			}
			// Keep the Number kind: bind the general var to the
			// number var, not the other way around.
			sub[t.ID] = v
			return nil
		case *types.TCon:
			if t.Unique == bi.Int.Unique || t.Unique == bi.Float.Unique {
				sub[v.ID] = t
				return nil
			}
			return &mismatch{a: v, b: t, note: fmt.Sprintf("`%s` is not a number type", t.Name)}
		default:
			return &mismatch{a: v, b: t, note: "only Int and Float are number types"}
		}
	default:
		panic("infer: row variables arrive in S7")
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
		return occurs(v, t.Arg, sub) || occurs(v, t.Ret, sub)
	default:
		return false
	}
}
