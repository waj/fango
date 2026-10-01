package codegen

import (
	"fmt"
	goast "go/ast"
	"maps"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// scopeBracketCall lowers a saturated call of the cleanup-scope intrinsic
// whose three callbacks are literal lambdas into the straight-line scope
// sequence at the call site. The lambdas' row effects bind to the lexical
// evidence the call would have packed into its row, so the acquire, use, and
// release bodies perform their operations directly: no row is built, no
// callback records are allocated, and no evidence is looked up by name.
// Calls that forward an open row, pass callback values, or whose callbacks
// read their row keep the ordinary worker call.
func (g *gen) scopeBracketCall(e *core.App) (goast.Expr, bool) {
	ref, ok := e.Callee.(*core.VarRef)
	if !ok || ref.Name != types.ScopeBracketName || g.disableOptimizations || len(e.Args) != 3 || e.Row == nil || e.Row.From != 0 {
		return nil, false
	}
	lambdas := make([]*core.Lambda, 3)
	for i, arg := range e.Args {
		lam, ok := arg.(*core.Lambda)
		if !ok || len(lam.EffectParams) != 0 || (lam.RowParam != 0 && core.FreeRows(lam.Body)[lam.RowParam]) {
			return nil, false
		}
		for _, label := range types.SortedRow(lam.Ty.(*types.TFun).Eff).Labels {
			if types.RuntimeEvidenceEffect(label) {
				return nil, false
			}
		}
		lambdas[i] = lam
	}
	type binding struct {
		key   types.EffectKey
		value goast.Expr
		mode  types.Transport
	}
	var bindings []binding
	for _, lam := range lambdas {
		for _, ev := range lam.RowEffects {
			var supplied *core.EffectInstance
			for i := range e.Row.Effects {
				if e.Row.Effects[i].Key() == ev.Key() {
					supplied = &e.Row.Effects[i]
					break
				}
			}
			if supplied == nil {
				return nil, false
			}
			stack := g.evidence[supplied.Key()]
			if len(stack) == 0 {
				return nil, false
			}
			bindings = append(bindings, binding{ev.Key(), stack[len(stack)-1], g.currentEvidenceMode(supplied.Key())})
		}
	}
	for _, b := range bindings {
		g.evidence[b.key] = append(g.evidence[b.key], b.value)
		g.evidenceModes[b.key] = append(g.evidenceModes[b.key], b.mode)
	}
	defer func() {
		for _, b := range bindings {
			g.evidence[b.key] = g.evidence[b.key][:len(g.evidence[b.key])-1]
			g.evidenceModes[b.key] = g.evidenceModes[b.key][:len(g.evidenceModes[b.key])-1]
		}
	}()
	oldCallbacks, oldForwarders := g.flatCallbacks, g.forwarders
	g.flatCallbacks, g.forwarders = maps.Clone(oldCallbacks), maps.Clone(oldForwarders)
	defer func() { g.flatCallbacks, g.forwarders = oldCallbacks, oldForwarders }()
	resource := fmt.Sprintf("_scope%d", g.tmp)
	g.tmp++
	resourceTy := lambdas[0].Ty.(*types.TFun).Ret
	// A callback that names the resource binds its parameter to the scope's
	// resource variable; the binding is a plain alias after copy elision.
	bound := func(lam *core.Lambda) core.Expr {
		if lam.Param == "_" || lam.Param == "()" {
			return lam.Body
		}
		delete(g.flatCallbacks, lam.Param)
		delete(g.forwarders, lam.Param)
		return &core.Let{Name: lam.Param, Rhs: &core.VarRef{Name: resource, Local: true, Ty: resourceTy}, Body: lam.Body, Ty: lam.Body.Type()}
	}
	return g.bracketExpr(&core.Bracket{
		Resource:   resource,
		ResourceTy: resourceTy,
		Acquire:    lambdas[0].Body,
		Release:    bound(lambdas[1]),
		Body:       bound(lambdas[2]),
		Ty:         e.Ty,
		Control:    e.Control,
	}), true
}
