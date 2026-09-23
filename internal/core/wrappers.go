package core

import "github.com/waj/fango/internal/types"

const WrapperNodeLimit = 48

// WrapperSize is intentionally a whitelist. Templates introduce no binders
// for owners, evidence, callbacks, state, cleanup, or staging capabilities.
// Their checked semantic calls remain in Core; execution lowering may expand
// them after Core lint, retaining the original ownership proof.
func WrapperSize(d *Def, body Expr) int {
	if body == nil || len(d.Params) == 0 || len(d.EffectParams) != 0 || len(d.RowEffects) != 0 || d.Control.Resolve(types.Machine) != types.Machine {
		return 0
	}
	n, valid := 0, true
	Inspect(body, func(e Expr) {
		n++
		switch e := e.(type) {
		case *VarRef, *IntLit, *FloatLit, *CharLit, *StringLit, *BoolLit, *UnitLit, *Case, *If, *Seq:
		case *Let:
			valid = valid && !e.Rec
		case *App:
			valid = valid && e.CalleeKind != Value && len(e.EvidenceArgs) == 0
		case *CoroutineAdvance:
		default:
			valid = false
		}
	})
	if !valid || n > WrapperNodeLimit {
		return 0
	}
	return n
}

// ExportWrapperTemplates is deterministic and excludes recursive call cycles.
// The template participates in the owning object's ABI fingerprint, so an
// importer cannot keep code expanded from an obsolete body.
func ExportWrapperTemplates(p *Prog) {
	defs := map[string]*Def{}
	for i := range p.Defs {
		d := &p.Defs[i]
		defs[d.Name] = d
		d.InlineBody = nil
	}
	var recursive func(string, string, map[string]bool) bool
	recursive = func(root, name string, seen map[string]bool) bool {
		if seen[name] {
			return false
		}
		seen[name] = true
		d := defs[name]
		if d == nil {
			return false
		}
		found := false
		Inspect(d.Body, func(e Expr) {
			if a, ok := e.(*App); ok && a.CalleeKind == Worker {
				if r, ok := a.Callee.(*VarRef); ok && !found {
					found = r.Name == root || recursive(root, r.Name, seen)
				}
			}
		})
		return found
	}
	for i := range p.Defs {
		d := &p.Defs[i]
		if WrapperSize(d, d.Body) > 0 && !recursive(d.Name, d.Name, map[string]bool{}) {
			d.InlineBody = d.Body
		}
	}
}
