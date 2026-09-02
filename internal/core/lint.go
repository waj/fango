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
	l := &linter{b: b, scope: map[string]bool{}, workers: map[string]*Def{}}
	for i := range p.Defs {
		d := &p.Defs[i]
		l.scope[d.Name] = true
		if len(d.Params) > 0 {
			l.workers[d.Name] = d
		}
	}
	for i := range p.Defs {
		d := &p.Defs[i]
		where := "def " + d.Name
		l.typ(d.Type, where)
		if len(d.Params) > 0 {
			// The worker's type must peel exactly arity arrows, with the
			// body typed at the remainder; params enter the no-shadow scope.
			t := d.Type
			for _, param := range d.Params {
				fn, ok := t.(*types.TFun)
				if !ok {
					l.errorf("%s: fewer arrows than parameters", where)
					break
				}
				if l.scope[param] {
					l.errorf("%s: parameter `%s` shadows — the checker should have rejected this", where, param)
				}
				l.scope[param] = true
				t = fn.Ret
			}
			if types.Show(t) != types.Show(d.Body.Type()) {
				l.errorf("%s: body type %s differs from peeled result %s",
					where, types.Show(d.Body.Type()), types.Show(t))
			}
			l.expr(d.Body, where)
			for _, param := range d.Params {
				delete(l.scope, param)
			}
		} else {
			l.expr(d.Body, where)
		}
	}
	return l.errs
}

type linter struct {
	b       *types.Builtins
	scope   map[string]bool // def names + enclosing Let/param names: no shadowing
	workers map[string]*Def
	errs    []error
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
		// A worker name may appear ONLY as an App{Worker} callee (that
		// case does not recurse here): a bare reference means elaboration
		// failed to eta-expand a first-class use.
		if _, isWorker := l.workers[e.Name]; isWorker {
			l.errorf("%s: bare reference to worker `%s` — first-class uses must be eta-expanded", where, e.Name)
		}
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
		if e.Rec {
			if _, ok := e.Rhs.(*Lambda); !ok {
				l.errorf("%s: recursive Let `%s` whose Rhs is not a Lambda", where, e.Name)
			}
			l.scope[e.Name] = true // in scope inside its own Rhs
			l.expr(e.Rhs, where)
		} else {
			l.expr(e.Rhs, where)
			l.scope[e.Name] = true
		}
		l.expr(e.Body, where)
		delete(l.scope, e.Name)
	case *Lambda:
		fn, ok := e.Ty.(*types.TFun)
		if !ok {
			l.errorf("%s: Lambda typed %s, want a function type", where, types.Show(e.Ty))
			return
		}
		if types.Show(fn.Ret) != types.Show(e.Body.Type()) {
			l.errorf("%s: Lambda body type %s differs from arrow result %s",
				where, types.Show(e.Body.Type()), types.Show(fn.Ret))
		}
		if l.scope[e.Param] {
			l.errorf("%s: Lambda param `%s` shadows — the checker should have rejected this", where, e.Param)
		}
		l.scope[e.Param] = true
		l.expr(e.Body, where)
		delete(l.scope, e.Param)
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
		switch e.CalleeKind {
		case Worker:
			ref, ok := e.Callee.(*VarRef)
			if !ok {
				l.errorf("%s: App{Worker} callee is %T, want a VarRef", where, e.Callee)
				return
			}
			def, isWorker := l.workers[ref.Name]
			if !isWorker {
				l.errorf("%s: App{Worker} callee `%s` is not a worker", where, ref.Name)
				return
			}
			if len(e.Args) != len(def.Params) {
				l.errorf("%s: App{Worker} `%s` has %d args, arity is %d",
					where, ref.Name, len(e.Args), len(def.Params))
				return
			}
			argTys, ret := PeelFun(def.Type, len(def.Params))
			for i, a := range e.Args {
				if types.Show(a.Type()) != types.Show(argTys[i]) {
					l.errorf("%s: App{Worker} `%s` arg %d typed %s, want %s",
						where, ref.Name, i+1, types.Show(a.Type()), types.Show(argTys[i]))
				}
				l.expr(a, where)
			}
			if types.Show(e.Ty) != types.Show(ret) {
				l.errorf("%s: App{Worker} `%s` typed %s, want %s",
					where, ref.Name, types.Show(e.Ty), types.Show(ret))
			}
		case Value:
			if len(e.Args) != 1 {
				l.errorf("%s: App{Value} must apply exactly one argument, got %d", where, len(e.Args))
				return
			}
			fn, ok := e.Callee.Type().(*types.TFun)
			if !ok {
				l.errorf("%s: App{Value} callee typed %s, want a function type",
					where, types.Show(e.Callee.Type()))
				return
			}
			if types.Show(e.Args[0].Type()) != types.Show(fn.Arg) {
				l.errorf("%s: App{Value} arg typed %s, want %s",
					where, types.Show(e.Args[0].Type()), types.Show(fn.Arg))
			}
			if types.Show(e.Ty) != types.Show(fn.Ret) {
				l.errorf("%s: App{Value} typed %s, want %s",
					where, types.Show(e.Ty), types.Show(fn.Ret))
			}
			l.expr(e.Callee, where)
			l.expr(e.Args[0], where)
		default:
			l.errorf("%s: App{Ctor} arrives in S4", where)
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
