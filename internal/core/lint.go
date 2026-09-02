package core

import (
	"fmt"

	"github.com/waj/fango/internal/types"
)

// Lint asserts the Core invariants: after elaboration there are no
// metavariables anywhere, every operator has the ground types its Go
// emission requires, and every effect row is empty. It runs in every test
// (and under a debug flag later) — instantiation plumbing bugs are the
// design's top risk, and this is the tripwire.
func Lint(p *Prog, b *types.Builtins) []error {
	l := &linter{b: b, scope: map[string]bool{}}
	for _, d := range p.Defs {
		l.scope[d.Name] = true
	}
	for _, d := range p.Defs {
		l.typ(d.Type, "def "+d.Name)
		l.expr(d.Body, "def "+d.Name)
	}
	return l.errs
}

type linter struct {
	b     *types.Builtins
	scope map[string]bool // def names + enclosing Let names: no shadowing
	errs  []error
}

func (l *linter) errorf(format string, args ...any) {
	l.errs = append(l.errs, fmt.Errorf(format, args...))
}

// unique returns the TCon unique of a ground scalar type, or -1.
func (l *linter) unique(t types.Type) int {
	if con, ok := t.(*types.TCon); ok && len(con.Args) == 0 {
		return con.Unique
	}
	return -1
}

func (l *linter) expr(e Expr, where string) {
	l.typ(e.Type(), where)
	switch e := e.(type) {
	case *IntLit:
		if l.unique(e.Ty) != l.b.Int.Unique {
			l.errorf("%s: IntLit typed %s", where, types.Show(e.Ty))
		}
	case *FloatLit:
		if l.unique(e.Ty) != l.b.Float.Unique {
			l.errorf("%s: FloatLit typed %s", where, types.Show(e.Ty))
		}
	case *StringLit:
		if l.unique(e.Ty) != l.b.String.Unique {
			l.errorf("%s: StringLit typed %s", where, types.Show(e.Ty))
		}
	case *BoolLit:
		if l.unique(e.Ty) != l.b.Bool.Unique {
			l.errorf("%s: BoolLit typed %s", where, types.Show(e.Ty))
		}
	case *VarRef:
	case *Neg:
		if u := l.unique(e.Ty); u != l.b.Int.Unique && u != l.b.Float.Unique {
			l.errorf("%s: Neg typed %s, want Int or Float", where, types.Show(e.Ty))
		}
		if l.unique(e.Operand.Type()) != l.unique(e.Ty) {
			l.errorf("%s: Neg operand type differs from result", where)
		}
		l.expr(e.Operand, where)
	case *BinOp:
		l.binOp(e, where)
	case *If:
		if l.unique(e.Cond.Type()) != l.b.Bool.Unique {
			l.errorf("%s: If condition typed %s, want Bool", where, types.Show(e.Cond.Type()))
		}
		if types.Show(e.Then.Type()) != types.Show(e.Ty) || types.Show(e.Else.Type()) != types.Show(e.Ty) {
			l.errorf("%s: If branches disagree with result type", where)
		}
		l.expr(e.Cond, where)
		l.expr(e.Then, where)
		l.expr(e.Else, where)
	case *Let:
		if types.Show(e.Ty) != types.Show(e.Body.Type()) {
			l.errorf("%s: Let type differs from its body", where)
		}
		if l.scope[e.Name] {
			l.errorf("%s: Let shadows `%s` — the checker should have rejected this", where, e.Name)
		}
		l.scope[e.Name] = true
		l.expr(e.Rhs, where)
		l.expr(e.Body, where)
		delete(l.scope, e.Name)
	case *Print:
		u := l.unique(e.Arg.Type())
		if u != l.b.Int.Unique && u != l.b.Float.Unique && u != l.b.String.Unique && u != l.b.Bool.Unique {
			l.errorf("%s: Print argument typed %s, not printable", where, types.Show(e.Arg.Type()))
		}
		if l.unique(e.Ty) != l.b.Unit.Unique {
			l.errorf("%s: Print typed %s, want ()", where, types.Show(e.Ty))
		}
		l.expr(e.Arg, where)
	case *App:
		l.expr(e.Callee, where)
		for _, a := range e.Args {
			l.expr(a, where)
		}
	default:
		l.errorf("%s: unhandled Core node %T", where, e)
	}
}

func (l *linter) binOp(e *BinOp, where string) {
	lu, ru, res := l.unique(e.L.Type()), l.unique(e.R.Type()), l.unique(e.Ty)
	numeric := func(u int) bool { return u == l.b.Int.Unique || u == l.b.Float.Unique }
	orderable := func(u int) bool { return numeric(u) || u == l.b.String.Unique }
	equatable := func(u int) bool { return orderable(u) || u == l.b.Bool.Unique }

	switch e.Op {
	case "+", "-", "*":
		if !numeric(res) || lu != res || ru != res {
			l.errorf("%s: BinOp %s has non-numeric or mismatched types", where, e.Op)
		}
	case "/":
		if res != l.b.Float.Unique || lu != res || ru != res {
			l.errorf("%s: BinOp / must be Float throughout", where)
		}
	case "++":
		if res != l.b.String.Unique || lu != res || ru != res {
			l.errorf("%s: BinOp ++ must be String throughout", where)
		}
	case "==", "/=":
		if res != l.b.Bool.Unique || lu != ru || !equatable(lu) {
			l.errorf("%s: BinOp %s wants matching equatable operands and Bool result", where, e.Op)
		}
	case "<", ">", "<=", ">=":
		if res != l.b.Bool.Unique || lu != ru || !orderable(lu) {
			l.errorf("%s: BinOp %s wants matching orderable operands and Bool result", where, e.Op)
		}
	default:
		l.errorf("%s: unhandled operator %q", where, e.Op)
	}
	l.expr(e.L, where)
	l.expr(e.R, where)
}

func (l *linter) typ(t types.Type, where string) {
	switch t := t.(type) {
	case *types.TVar:
		l.errorf("%s: metavariable survived elaboration", where)
	case *types.TCon:
		for _, a := range t.Args {
			l.typ(a, where)
		}
	case *types.TFun:
		if !t.Eff.Empty() {
			l.errorf("%s: non-empty effect row before S7", where)
		}
		l.typ(t.Arg, where)
		l.typ(t.Ret, where)
	default:
		l.errorf("%s: unhandled type %T", where, t)
	}
}
