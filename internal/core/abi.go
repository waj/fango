package core

import "github.com/waj/fango/internal/types"

// SummarizeABI computes body-derived ABI facts for p. Context is treated as
// installed dependency metadata: only its existing summaries are consulted.
func SummarizeABI(p *Prog, context []Def) {
	adts := map[int]*types.ADTInfo{}
	for _, adt := range p.ADTs {
		adts[adt.Con.Unique] = adt
	}
	needs := func(d *Def) bool {
		if d == nil {
			return false
		}
		args, ret := PeelFun(d.Type, len(d.Params))
		for _, arg := range args {
			if types.ControlledRepresentation(arg, adts) {
				return true
			}
		}
		return types.ControlledRepresentation(ret, adts)
	}
	for i := range p.Defs {
		d := &p.Defs[i]
		d.ABI = ABISummary{Valid: true, NeedsFamily: needs(d)}
		args, _ := PeelFun(d.Type, len(d.Params))
		controlled := map[string]bool{}
		for j, arg := range args {
			if j < len(d.Params) && types.ControlledRepresentation(arg, adts) {
				controlled[d.Params[j]] = true
			}
		}
		InspectPruned(d.Body, func(e Expr) bool {
			if _, ok := e.(*Lambda); ok {
				return false
			}
			if app, ok := e.(*App); ok {
				for name := range controlled {
					if Mentions(app.Callee, name) {
						d.ABI.CallsControlledArg = true
					}
				}
			}
			return true
		})
	}
}
