package parser

import "github.com/waj/fango/internal/source"

// Layout contexts keep the first item column as a continuation boundary.
// The parser separately recognizes outdented siblings and branch heads.

type ctxKind int

const (
	ctxDecl   ctxKind = iota // top-level declarations, column 1
	ctxBlock                 // first block item column
	ctxCase                  // first case branch column
	ctxHandle                // first handler clause column
	ctxParen                 // expression enclosed by `(` and `)`
)

type layoutCtx struct {
	kind  ctxKind
	col   int
	owner int // enclosing layout boundary for case/handler; zero otherwise
}

type layout struct {
	stack []layoutCtx
}

func (l *layout) push(kind ctxKind, col int) {
	l.stack = append(l.stack, layoutCtx{kind: kind, col: col})
}

func (l *layout) pushBranch(kind ctxKind, col, owner int) {
	l.stack = append(l.stack, layoutCtx{kind: kind, col: col, owner: owner})
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
