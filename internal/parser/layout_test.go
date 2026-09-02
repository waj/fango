package parser

import (
	"testing"

	"github.com/waj/fango/internal/source"
)

// Direct unit tests on the layout predicates: rules 2 and 3 have no parser
// client until S4/S2, so the predicates are pinned down here (DESIGN.md §13,
// honest costs).
func TestCheckOffside(t *testing.T) {
	var l layout
	l.push(ctxDecl, 1)
	l.push(ctxCase, 5)

	cases := []struct {
		col  int
		want offsideResult
	}{
		{9, offContinue},
		{6, offContinue},
		{5, offSibling},
		{4, offEnd},
		{1, offEnd},
	}
	for _, c := range cases {
		if got := l.checkOffside(source.Pos{Line: 1, Col: c.col}); got != c.want {
			t.Errorf("col %d: got %v, want %v", c.col, got, c.want)
		}
	}

	l.pop() // back to the top-level context
	if got := l.checkOffside(source.Pos{Line: 1, Col: 1}); got != offSibling {
		t.Errorf("top-level col 1: got %v, want offSibling", got)
	}
	if got := l.checkOffside(source.Pos{Line: 1, Col: 2}); got != offContinue {
		t.Errorf("top-level col 2: got %v, want offContinue", got)
	}
}

func TestAtBranchCol(t *testing.T) {
	var l layout
	l.push(ctxDecl, 1)
	l.push(ctxLet, 7)
	if !l.atBranchCol(source.Pos{Line: 3, Col: 7}) {
		t.Error("col 7 should be at the branch column")
	}
	if l.atBranchCol(source.Pos{Line: 3, Col: 8}) {
		t.Error("col 8 should not be at the branch column")
	}
}
