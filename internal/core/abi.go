package core

import "github.com/waj/fango/internal/types"

// SummarizeABI computes body-derived ABI facts for p. Context is treated as
// installed dependency metadata: only its existing summaries are consulted.
func SummarizeABI(p *Prog, context []Def) {
	ExportWrapperTemplates(p)
	adts := map[int]*types.ADTInfo{}
	for _, adt := range p.ADTs {
		adts[adt.Con.Unique] = adt
	}
	defs := map[string]*Def{}
	owned := map[string]bool{}
	for i := range context {
		defs[context[i].Name] = &context[i]
	}
	for i := range p.Defs {
		defs[p.Defs[i].Name] = &p.Defs[i]
		owned[p.Defs[i].Name] = true
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
	var passive func(*Def, map[string]bool) bool
	passive = func(d *Def, seen map[string]bool) bool {
		if d == nil || d.Control != (types.Control{}) {
			return false
		}
		if !owned[d.Name] {
			return d.ABI.Valid && d.ABI.PassiveMachineFactory
		}
		if !d.ABI.NeedsFamily {
			return false
		}
		if seen[d.Name] {
			return true
		}
		seen[d.Name] = true
		ok := true
		InspectPruned(d.Body, func(e Expr) bool {
			switch e := e.(type) {
			case *Lambda:
				return false
			case *App:
				if e.CalleeKind == Value || e.Control != (types.Control{}) {
					ok = false
				}
				if e.CalleeKind == Worker {
					if ref, yes := e.Callee.(*VarRef); yes {
						callee := defs[ref.Name]
						if callee != nil && ((callee.ABI.Valid && callee.ABI.NeedsFamily) || needs(callee)) && !passive(callee, seen) {
							ok = false
						}
					}
				}
			}
			return true
		})
		return ok
	}
	for i := range p.Defs {
		p.Defs[i].ABI.PassiveMachineFactory = passive(&p.Defs[i], map[string]bool{})
	}
}
