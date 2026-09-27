package infer

import (
	"fmt"

	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// FreshEffect is a prototype generative effect identity. It uses the existing
// nominal row machinery: two allocations have distinct identities even when
// their diagnostic names agree. It is not an ordinary caller-selected type
// parameter. No new runtime representation is implied by this checker API.
//
// Source integration still needs a quantified callback contract and evidence
// lowering. In particular, callers must not publish this identity in a module
// scheme or turn it into a native effect with unchecked implementations.
type FreshEffect struct {
	label types.EffLabel
}

func NewFreshEffect(sup *types.Supply, name string) FreshEffect {
	return FreshEffect{label: types.EffLabel{Unique: sup.NextUnique(), Name: name}}
}

// Within extends an ambient row for the subject. It preserves all outer
// labels and the residual tail; it never authorizes removing another scope.
func (s FreshEffect) Within(outer types.Row) types.Row {
	labels := append([]types.EffLabel(nil), outer.Labels...)
	labels = append(labels, s.label)
	return types.SortedRow(types.Row{Labels: labels, Tail: outer.Tail})
}

// ScopeBoundary is the no-escape obligation for one scope. Result and Residual
// are outside the scope. Outer contains every type reachable from the outer
// environment, including monomorphic storage slots, recursive bindings,
// pending record obligations, and predicate types.
//
// Keep the obligation until the enclosing inference group is finalized. An
// intermediate Solve may leave metas open; later Solve calls must retain the
// obligation. It is deliberately a constraint rather than an eager check.
type ScopeBoundary struct {
	Effect   FreshEffect
	Result   types.Type
	Residual types.Row
	Outer    []types.Type
}

func (s *ScopeBoundary) check(sub Subst, sp source.Span) []diag.Error {
	roots := []struct {
		name string
		ty   types.Type
	}{{"result", s.Result}, {"residual effects", s.Residual}}
	for i, ty := range s.Outer {
		roots = append(roots, struct {
			name string
			ty   types.Type
		}{fmt.Sprintf("outer binding %d", i+1), ty})
	}
	var errs []diag.Error
	for _, root := range roots {
		if containsEffect(sub.Apply(root.ty), s.Effect.label.Unique) {
			errs = append(errs, diag.Errorf(sp, "SCOPE ESCAPE",
				"The local effect `%s` occurs in the scope's %s. Keep values and callbacks that require it inside the scope.",
				s.Effect.label.Name, root.name))
		}
	}
	return errs
}

// Inspect every type position, not just an arrow's immediate effect row.
// This includes latent callbacks, abstract/phantom ADT arguments, effect
// arguments, and substitutions reached through row tails. Abstract schemas
// cannot hide an instance selected by a caller without retaining it as an
// argument; existential packages are not part of the current type language.
func containsEffect(t types.Type, unique int) bool {
	switch t := t.(type) {
	case *types.TVar:
		return false
	case *types.TCon:
		for _, arg := range t.Args {
			if containsEffect(arg, unique) {
				return true
			}
		}
	case *types.TFun:
		return containsEffect(t.Arg, unique) || containsEffect(t.Eff, unique) || containsEffect(t.Ret, unique)
	case types.Row:
		for _, label := range t.Labels {
			if label.Unique == unique {
				return true
			}
			for _, arg := range label.Args {
				if containsEffect(arg, unique) {
					return true
				}
			}
		}
		return t.Tail != nil && containsEffect(t.Tail, unique)
	default:
		panic(fmt.Sprintf("infer.containsEffect: unhandled %T", t))
	}
	return false
}
