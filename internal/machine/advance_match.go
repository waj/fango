package machine

import "github.com/waj/fango/internal/core"

// AdvanceMatch proves that the result object is used only to select one of
// its protocol cases. Aliases are permitted; escaping or multiply observed
// results must retain their ordinary representation. The checked IR remains
// the reference; emitters may bind payloads directly on the selected edge.
func AdvanceMatch(w *Worker, advance *CursorAdvance) *SwitchCtor {
	if advance.Close || advance.Result == nil {
		return nil
	}
	match := immediateMatch(w, advance.Bind.Name, advance.Next)
	if match == nil || match.ADT != advance.Result || match.Default != nil || len(match.Cases) != len(advance.Result.Ctors) {
		return nil
	}
	return match
}

// ConstructorMatch also removes a nonescaping constructor immediately
// inspected by a case, including adapters between two different ADTs.
func ConstructorMatch(w *Worker, eval *Eval) *CtorCase {
	a, ok := eval.Value.(*core.App)
	if !ok || a.CalleeKind != core.Ctor || a.Ctor == nil {
		return nil
	}
	match := immediateMatch(w, eval.Bind.Name, eval.Next)
	if match == nil {
		return nil
	}
	for i := range match.Cases {
		if match.Cases[i].Ctor == a.Ctor {
			return &match.Cases[i]
		}
	}
	return nil
}

func immediateMatch(w *Worker, bind string, next BlockID) *SwitchCtor {
	if !w.Optimized {
		return nil
	}
	aliases := map[string]bool{bind: true}
	skipped := map[BlockID]bool{}
	for len(skipped) <= 48 {
		if int(next) < 0 || int(next) >= len(w.Blocks) || skipped[next] {
			return nil
		}
		skipped[next] = true
		switch t := w.Blocks[next].Term.(type) {
		case *Eval:
			r, ok := t.Value.(*core.VarRef)
			if !ok || !r.Local || !aliases[r.Name] {
				return nil
			}
			aliases[t.Bind.Name] = true
			next = t.Next
		case *SwitchCtor:
			if !aliases[t.Scrut] {
				return nil
			}
			for _, block := range w.Blocks {
				if skipped[block.ID] {
					continue
				}
				for name := range termUses(block.Term, aliases) {
					if aliases[name] {
						return nil
					}
				}
			}
			return t
		default:
			return nil
		}
	}
	return nil
}
