package core

import "github.com/waj/fango/internal/types"

// FreeEvidence identifies evidence used by an expression, including its
// nested closures, after removing lexical handler and invocation binders.
func FreeEvidence(expr Expr) map[types.EffectKey]EffectInstance {
	free, bound := map[types.EffectKey]EffectInstance{}, map[types.EffectKey]int{}
	use := func(ev EffectInstance) {
		if ev.Unique != 0 && types.SurfaceName(ev.Name) != "IO" && bound[ev.Key()] == 0 {
			free[ev.Key()] = ev
		}
	}
	var visit func(Expr)
	visit = func(expr Expr) {
		InspectPruned(expr, func(e Expr) bool {
			switch e := e.(type) {
			case *Lambda:
				for _, ev := range append(append([]EffectInstance(nil), e.EffectParams...), e.RowEffects...) {
					bound[ev.Key()]++
				}
				visit(e.Body)
				for _, ev := range append(append([]EffectInstance(nil), e.EffectParams...), e.RowEffects...) {
					bound[ev.Key()]--
				}
				return false
			case *Handle:
				bound[e.Effect.Key()]++
				visit(e.Body)
				bound[e.Effect.Key()]--
				for _, clause := range e.Clauses {
					visit(clause.Body)
				}
				if e.Return != nil {
					visit(e.Return.Body)
				}
				if e.State != nil {
					visit(e.State.Initial)
				}
				return false
			case *App:
				if e.Row != nil {
					for _, ev := range e.Row.Effects {
						use(ev)
					}
				}
				for _, ev := range e.EvidenceArgs {
					use(ev)
				}
			case *Perform:
				use(e.Effect)
			case *ControlExit:
				use(e.Effect)

			}
			return true
		})
	}
	visit(expr)
	return free
}
