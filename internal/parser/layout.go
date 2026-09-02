package parser

import "github.com/waj/fango/internal/source"

// The offside rule, isolated per DESIGN.md §5. This is the full, final API:
// a stack of indentation contexts plus two predicates. S0 exercises only the
// top-level rule (context column 1); `let` (S2) and `case` (S4) push
// contexts at their binding/branch columns.

type ctxKind int

const (
	ctxDecl ctxKind = iota // top-level declarations, column 1
	ctxLet                 // let-binding alignment (first client: S2)
	ctxCase                // case-branch alignment (first client: S4)
)

type layoutCtx struct {
	kind ctxKind
	col  int
}

type layout struct {
	stack []layoutCtx
}

func (l *layout) push(kind ctxKind, col int) {
	l.stack = append(l.stack, layoutCtx{kind, col})
}

func (l *layout) pop() {
	l.stack = l.stack[:len(l.stack)-1]
}

func (l *layout) innermost() layoutCtx {
	return l.stack[len(l.stack)-1]
}

type offsideResult int

const (
	offContinue offsideResult = iota // right of the context column: same item
	offSibling                       // exactly at the column: next item
	offEnd                           // left of the column: context is over
)

// checkOffside classifies a token position against the innermost context.
func (l *layout) checkOffside(pos source.Pos) offsideResult {
	ctx := l.innermost()
	switch {
	case pos.Col > ctx.col:
		return offContinue
	case pos.Col == ctx.col:
		return offSibling
	default:
		return offEnd
	}
}

// atBranchCol reports whether the position sits exactly at the innermost
// context's alignment column — the "starts a new branch/binding" predicate.
func (l *layout) atBranchCol(pos source.Pos) bool {
	return pos.Col == l.innermost().col
}
