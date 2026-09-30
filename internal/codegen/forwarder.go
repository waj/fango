package codegen

import "github.com/waj/fango/internal/core"

// Tiny invocation-only forwarding lambdas can be called directly. Captured
// evidence or residual rows would need their definition-site environment;
// leave those closures intact. Core local names have no shadowing.
func (g *gen) rememberForwarder(let *core.Let) bool {
	lam, ok := let.Rhs.(*core.Lambda)
	if !ok || let.Rec {
		return false
	}
	switch lam.Body.(type) {
	case *core.Perform, *core.App, *core.VarRef, *core.UnitLit:
	default:
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
