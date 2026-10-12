package codegen

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// Invocation-only local lambdas can be called directly. Captured
// evidence or residual rows would need their definition-site environment;
// leave those closures intact. Core local names have no shadowing.
func (g *gen) rememberForwarder(let *core.Let) bool {
	if g.disableOptimizations {
		return false
	}
	if ref, ok := let.Rhs.(*core.VarRef); ok && ref.Local && !let.Rec {

		if lam := g.forwarders[ref.Name]; lam != nil {
			g.forwarders[let.Name] = lam
			return true
		}
	}
	lam, ok := let.Rhs.(*core.Lambda)
	if !ok || let.Rec {
		return false
	}
	contract := core.LocalCallbackABI(let.Body, let.Name, lam.Ty, g.defs)

	if contract.Arity == 0 {
		return false
	}
	// A flattened callback may discard only pure lambda creation between
	// arguments. Preserve arbitrary intermediate computations and their timing.
	current := lam
	for range contract.Arity - 1 {
		fn := current.Ty.(*types.TFun)
		if types.FunctionControl(fn) != (types.Control{}) || len(fn.Eff.Labels) != 0 || types.FunctionOpenRow(fn) {
			return false
		}
		next, ok := current.Body.(*core.Lambda)
		if !ok {
			return false
		}
		current = next
	}
	if len(core.FreeEvidence(lam)) != 0 || len(core.FreeRows(lam)) != 0 {
		return false
	}
	if g.forwarders == nil {
		g.forwarders = map[string]*core.Lambda{}
	}
	g.forwarders[let.Name] = lam
	return true
}
