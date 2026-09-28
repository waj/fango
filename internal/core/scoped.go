package core

import (
	"fmt"

	"github.com/waj/fango/internal/types"
)

// Source checking proves environmental independence before row erasure. Core
// retains the quantified declaration and instantiated call signatures to reject
// missing boundaries and escaping permission in outward call signatures.
func checkScopedCalls(p *Prog, context []Def) []error {
	defs := map[string]Def{}
	for _, d := range context {
		defs[d.Name] = d
	}
	for _, d := range p.Defs {
		defs[d.Name] = d
	}
	var errs []error
	for _, d := range p.Defs {
		Inspect(d.Body, func(e Expr) {
			call, ok := e.(*App)
			if !ok || call.CalleeKind != Worker {
				return
			}
			ref, ok := call.Callee.(*VarRef)
			if !ok || !defs[ref.Name].Scoped {
				return
			}
			fail := func(reason string) {
				errs = append(errs, fmt.Errorf("def %s: scoped call to %s: %s", d.Name, ref.Name, reason))
			}
			if len(call.Args) == 0 || len(call.Args) != len(defs[ref.Name].Params) {
				fail("arity mismatch")
				return
			}
			raw := call.SourceType
			var args []types.Type
			var base types.Row
			for range call.Args {
				fn, ok := raw.(*types.TFun)
				if !ok {
					fail("missing source signature")
					return
				}
				args, base, raw = append(args, fn.Arg), fn.Eff, fn.Ret
			}
			callback, ok := args[len(args)-1].(*types.TFun)
			if !ok {
				fail("missing callback signature")
				return
			}
			outside := append(append([]types.Type(nil), args[:len(args)-1]...), raw, base, callback.Ret)
			var fresh []types.EffLabel
			for _, label := range callback.Eff.Labels {
				if label.Scoped && !types.ContainsScopedEffect(base, label.Unique) {
					fresh = append(fresh, label)
				}
			}
			if len(fresh) != 1 {
				fail("callback must introduce exactly one fresh permission")
				return
			}
			for _, t := range outside {
				if types.ContainsScopedEffect(t, fresh[0].Unique) {
					fail("scope escapes in outward signature")
					return
				}
			}
		})
	}
	return errs
}
