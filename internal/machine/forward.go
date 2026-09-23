package machine

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// forwardingReturn admits only identity assignments and Unit erasure. It
// cannot cross a state update, handler, cleanup, effect, or another call.
// Forwarding clauses therefore tail-transfer without retaining a redundant
// continuation, while frame creation still remains lazy and trampolined.
func forwardingReturn(blocks []Block, id BlockID, value Local) bool {
	aliases := map[string]bool{value.Name: true}
	isValue := func(e core.Expr) bool {
		if r, ok := e.(*core.VarRef); ok {
			return r.Local && aliases[r.Name]
		}
		if _, ok := e.(*core.UnitLit); ok {
			t, ok := value.Ty.(*types.TCon)
			return ok && t.Name == "()"
		}
		return false
	}
	for i := 0; i < 48; i++ {
		if int(id) < 0 || int(id) >= len(blocks) {
			return false
		}
		switch t := blocks[id].Term.(type) {
		case *Return:
			return isValue(t.Value)
		case *Eval:
			if !isValue(t.Value) {
				return false
			}
			aliases[t.Bind.Name] = true
			id = t.Next
		default:
			return false
		}
	}
	return false
}
