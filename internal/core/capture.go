package core

import "github.com/waj/fango/internal/types"

// SubstituteCaptureVars rewrites compiler-only evidence capture metadata.
// It is used when callback ABI adaptation closes over concrete evidence while
// erasing an open row from the callback's callable type.
func SubstituteCaptureVars(e Expr, m map[types.CaptureVar]types.CaptureSet) Expr {
	if len(m) == 0 {
		return e
	}
	return Rewrite(e, func(t types.Type) types.Type { return t }, func(x Expr) Expr {
		sub := func(ev *EffectInstance) { ev.Captures = ev.Captures.Substitute(m) }
		switch x := x.(type) {
		case *Perform:
			sub(&x.Effect)
		case *ControlExit:
			sub(&x.Effect)
		case *Suspend:
			sub(&x.Owner)
		case *IteratorScope:
			sub(&x.Yield)
		case *Handle:
			sub(&x.Effect)
		case *App:
			for i := range x.EvidenceArgs {
				sub(&x.EvidenceArgs[i])
			}
		case *Lambda:
			for i := range x.EffectParams {
				sub(&x.EffectParams[i])
			}
		}
		return x
	})
}
