package infer

import (
	"fmt"

	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// FreshEffect is a generative permission label for one scoped callback call.
// Ordinary row inclusion composes it with other scopes and residual effects.
// Its identity is rigid and erased before runtime evidence lowering.
type FreshEffect struct {
	label types.EffLabel
}

func NewFreshEffect(sup *types.Supply, name string) FreshEffect {
	return FreshEffect{label: types.EffLabel{Unique: sup.NextUnique(), Name: name, Scoped: true}}
}

// NewHandlerPermission is available in the subject but is added to a callable
// only when that callable is bound to this activation.
func NewHandlerPermission(sup *types.Supply, name string) FreshEffect {
	permission := NewFreshEffect(sup, name)
	permission.label.Binding = true
	return permission
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
	Effect    FreshEffect
	LocalVars []*types.TVar // operation-local skolems forbidden outside their clause
	Operation string
	Result    types.Type
	Residual  types.Row
	Outer     []types.Type
	// Subject is what a handler's handled code evaluates to, and Handled the
	// effects the handler discharges: see uncalledSubject.
	Subject     types.Type
	SubjectSpan source.Span
	Handled     []types.EffLabel
}

// uncalledSubject reports handled code that is only a function needing the very
// effects the handler discharges. Nothing performs them while the handler is
// active, so the handler can do nothing: this is `handle action on` written for
// `handle action() on`.
func (s *ScopeBoundary) uncalledSubject(sub Subst) []diag.Error {
	if s.Subject == nil {
		return nil
	}
	fn, ok := sub.Apply(s.Subject).(*types.TFun)
	if !ok {
		return nil
	}
	for cur := fn; cur != nil; cur, _ = cur.Ret.(*types.TFun) {
		for _, l := range cur.Eff.Labels {
			for _, handled := range s.Handled {
				if l.Unique == handled.Unique && !l.Scoped {
					e := diag.Errorf(s.SubjectSpan, "UNCALLED HANDLER SUBJECT", "This handler handles `%s`, but the code it handles is a function that nobody calls,\nso nothing performs `%s` while the handler is active.", types.SurfaceName(l.Name), types.SurfaceName(l.Name))
					e.Notes = append(e.Notes, "Call it here, as in `handle action() on`.")
					return []diag.Error{e}
				}
			}
		}
	}
	return nil
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
	errs := s.uncalledSubject(sub)
	if len(errs) > 0 {
		return errs
	}
	for _, root := range roots {
		resolved := sub.Apply(root.ty)
		if s.Effect.label.Unique != 0 && types.ContainsScopedEffect(resolved, s.Effect.label.Unique) {
			errs = append(errs, diag.Errorf(sp, "SCOPE ESCAPE",
				"Something that only works inside this scope reaches the scope's %s, so it would outlive the scope.\nKeep values and callbacks that need the scope inside it, and return what you get from them instead.",
				root.name))
		}
		if len(s.LocalVars) != 0 {
			used := types.RigidVarsIn(resolved)
			for _, local := range s.LocalVars {
				for _, found := range used {
					if found.ID == local.ID {
						errs = append(errs, diag.Errorf(sp, "OPERATION TYPE ESCAPES",
							"The operation-local type of `%s` occurs in the handler's %s.", s.Operation, root.name))
					}
				}
			}
		}
	}
	return errs
}
