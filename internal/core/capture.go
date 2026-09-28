package core

import "github.com/waj/fango/internal/types"

// SubstituteCaptureVars rewrites compiler-only evidence capture metadata.
// It is used when callback ABI adaptation closes over concrete evidence while
// erasing an open row from the callback's callable type. Evidence naming a
// substituted variable also adopts the transport given for it: after discharge
// the body calls that activation's record, whose protocol is its clauses' and
// not the polymorphic one an abstract capability stood for. A caller that only
// renames variables passes no transports.
func SubstituteCaptureVars(e Expr, m map[types.CaptureVar]types.CaptureSet, control map[types.CaptureVar]types.Control) Expr {
	if len(m) == 0 {
		return e
	}
	return Rewrite(e, func(t types.Type) types.Type { return t }, func(x Expr) Expr {
		sub := func(ev *EffectInstance) {
			for _, v := range ev.Captures.Vars {
				if c, ok := control[v]; ok {
					ev.Control = c
					break
				}
			}
			ev.Captures = ev.Captures.Substitute(m)
		}
		if row := ExpressionRow(x); row != nil {
			for i := range row.Effects {
				sub(&row.Effects[i])
			}
		}
		switch x := x.(type) {
		case *Perform:
			sub(&x.Effect)
			// A Perform repeats its evidence's protocol as its own; Core lint
			// checks that the two still agree.
			x.Control = x.Effect.Control
		case *ControlExit:
			sub(&x.Effect)

		case *Handle:
			sub(&x.Effect)
		case *App:
			for i := range x.EvidenceArgs {
				sub(&x.EvidenceArgs[i])
			}
		case *Lambda:
			for i := range x.RowEffects {
				sub(&x.RowEffects[i])
			}
			for i := range x.EffectParams {
				sub(&x.EffectParams[i])
			}
		}
		return x
	})
}
