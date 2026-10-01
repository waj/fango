package codegen

import "github.com/waj/fango/internal/core"

// Invocation-only local lambdas can be called directly. Captured
// evidence or residual rows would need their definition-site environment;
// leave those closures intact. Core local names have no shadowing.
func (g *gen) rememberForwarder(let *core.Let) bool {
	if g.disableOptimizations {
		return false
	}
	lam, ok := let.Rhs.(*core.Lambda)
	if !ok || let.Rec {
		return false
	}
	contract := core.LocalCallbackABI(let.Body, let.Name, lam.Ty, g.defs)
	if contract.Arity != 1 {
		return false
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
