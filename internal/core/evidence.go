package core

import "github.com/waj/fango/internal/types"

// FreeEvidence identifies evidence used by an expression, including its
// nested closures, after removing lexical handler and invocation binders.
func FreeEvidence(expr Expr) map[int]EffectInstance {
	free, bound := map[int]EffectInstance{}, map[int]int{}
	use := func(ev EffectInstance) {
		if ev.Unique != 0 && types.SurfaceName(ev.Name) != "IO" && bound[ev.Unique] == 0 {
			free[ev.Unique] = ev
		}
	}
	var visit func(Expr)
	visit = func(expr Expr) {
		InspectPruned(expr, func(e Expr) bool {
			switch e := e.(type) {
			case *Lambda:
				for _, ev := range e.EffectParams {
					bound[ev.Unique]++
				}
				visit(e.Body)
				for _, ev := range e.EffectParams {
					bound[ev.Unique]--
				}
				return false
			case *Handle:
				bound[e.Effect.Unique]++
				visit(e.Body)
				bound[e.Effect.Unique]--
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
				for _, ev := range e.EvidenceArgs {
					use(ev)
				}
			case *Perform:
				use(e.Effect)
			case *ControlExit:
				use(e.Effect)
			case *Suspend:
				use(e.Owner)
			}
			return true
		})
	}
	visit(expr)
	return free
}
