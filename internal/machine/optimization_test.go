package machine

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
	"testing"
)

func TestImmediateMatchRejectsEscapingResults(t *testing.T) {
	b := types.NewBuiltins(&types.Supply{})
	ref := func(s string) core.Expr { return &core.VarRef{Name: s, Local: true, Ty: b.Int} }
	match := &SwitchCtor{Scrut: "alias"}
	w := &Worker{Optimized: true, Blocks: []Block{
		{ID: 0, Term: &Eval{Bind: Local{Name: "alias", Ty: b.Int}, Value: ref("result"), Next: 1}},
		{ID: 1, Term: match},
		{ID: 2, Term: &Return{Value: &core.IntLit{Val: 0, Ty: b.Int}}},
	}}
	if immediateMatch(w, "result", 0) != match {
		t.Fatal("single consuming match not recognized")
	}
	w.Blocks[2].Term = &Return{Value: ref("result")}
	if immediateMatch(w, "result", 0) != nil {
		t.Fatal("escaping result was eliminated")
	}
	w.Blocks[2].Term = &Return{Value: ref("alias")}
	if immediateMatch(w, "result", 0) != nil {
		t.Fatal("escaping alias was eliminated")
	}
}

func TestForwardingReturnDoesNotCrossWork(t *testing.T) {
	b := types.NewBuiltins(&types.Supply{})
	value := Local{Name: "reply", Ty: b.Unit}
	blocks := []Block{{ID: 0, Term: &Eval{Bind: Local{Name: "answer", Ty: b.Unit}, Value: &core.UnitLit{Ty: b.Unit}, Next: 1}},
		{ID: 1, Term: &Return{Value: &core.VarRef{Name: "answer", Local: true, Ty: b.Unit}}}}
	if !forwardingReturn(blocks, 0, value) {
		t.Fatal("Unit forwarding not recognized")
	}
	blocks[0].Term = &PopCleanup{Next: 1}
	if forwardingReturn(blocks, 0, value) {
		t.Fatal("cleanup elided")
	}
	value.Ty = b.Int
	blocks[0].Term = &Eval{Bind: Local{Name: "answer", Ty: b.Unit}, Value: &core.UnitLit{Ty: b.Unit}, Next: 1}
	if forwardingReturn(blocks, 0, value) {
		t.Fatal("non-Unit result erased")
	}
}
