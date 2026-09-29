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
		resolved := sub.Apply(root.ty)
		if s.Effect.label.Unique != 0 && types.ContainsScopedEffect(resolved, s.Effect.label.Unique) {
			errs = append(errs, diag.Errorf(sp, "SCOPE ESCAPE",
				"The local effect `%s` occurs in the scope's %s. Keep values and callbacks that require it inside the scope.",
				s.Effect.label.Name, root.name))
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
