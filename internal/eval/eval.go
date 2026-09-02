// Package eval is the tree-walking Core interpreter: the REPL's execution
// engine and the differential test oracle for the compiled backend. Both
// backends implement the same Core semantics; the e2e suite diffs them.
package eval

import (
	"context"
	"fmt"

	"github.com/waj/fango/internal/core"
)

// Value is the interpreter's uniform representation (DESIGN.md §9.5):
//
//	Int          int64
//	Float        float64   (S1)
//	String       string    (S1)
//	Bool         bool      (S1)
//	constructors *CtorVal  (S4)
//	functions    *Closure, *Partial (S3)
//
// The switch in Eval and the compiled backend's unboxed representations
// must agree; the differential suite is the referee.
type Value = any

// Cell is a lazily-memoized top-level binding (DESIGN.md §9.2) — the final
// session model, not a shortcut: the REPL's generational redefinition
// replaces cells wholesale.
type Cell struct {
	Body    core.Expr
	memo    Value
	forced  bool
	forcing bool
}

// Env holds top-level cells. Local frames arrive with lambdas (S3).
type Env struct {
	cells map[string]*Cell
}

func NewEnv() *Env { return &Env{cells: map[string]*Cell{}} }

// Define installs (or replaces — REPL redefinition) a top-level binding.
func (e *Env) Define(name string, body core.Expr) {
	e.cells[name] = &Cell{Body: body}
}

// DefineProg installs every definition of a Core program.
func (e *Env) DefineProg(p *core.Prog) {
	for _, d := range p.Defs {
		e.Define(d.Name, d.Body)
	}
}

// interp carries the cancellation context; ctx is polled every pollEvery
// evaluation steps so Ctrl-C interrupts runaway REPL expressions.
type interp struct {
	ctx   context.Context
	env   *Env
	steps int
}

const pollEvery = 4096

// Eval evaluates a Core expression under env.
func Eval(ctx context.Context, e core.Expr, env *Env) (Value, error) {
	return (&interp{ctx: ctx, env: env}).eval(e)
}

// Force evaluates (and memoizes) the named top-level binding.
func Force(ctx context.Context, name string, env *Env) (Value, error) {
	return (&interp{ctx: ctx, env: env}).force(name)
}

func (in *interp) eval(e core.Expr) (Value, error) {
	in.steps++
	if in.steps%pollEvery == 0 {
		select {
		case <-in.ctx.Done():
			return nil, fmt.Errorf("interrupted")
		default:
		}
	}
	switch e := e.(type) {
	case *core.IntLit:
		return e.Val, nil
	case *core.VarRef:
		return in.force(e.Name)
	case *core.BinOp:
		l, err := in.eval(e.L)
		if err != nil {
			return nil, err
		}
		r, err := in.eval(e.R)
		if err != nil {
			return nil, err
		}
		return applyBinOp(e.Op, l, r)
	case *core.App:
		// Unreachable until elaboration produces App nodes.
		return nil, fmt.Errorf("eval: core.App arrives in S3")
	default:
		return nil, fmt.Errorf("eval: unhandled Core node %T", e)
	}
}

func (in *interp) force(name string) (Value, error) {
	cell, ok := in.env.cells[name]
	if !ok {
		return nil, fmt.Errorf("eval: undefined name `%s` (checker should have caught this)", name)
	}
	if cell.forced {
		return cell.memo, nil
	}
	if cell.forcing {
		return nil, fmt.Errorf("eval: `%s` depends on itself", name)
	}
	cell.forcing = true
	v, err := in.eval(cell.Body)
	cell.forcing = false
	if err != nil {
		return nil, err
	}
	cell.memo, cell.forced = v, true
	return v, nil
}

func applyBinOp(op string, l, r Value) (Value, error) {
	li, lok := l.(int64)
	ri, rok := r.(int64)
	if !lok || !rok {
		return nil, fmt.Errorf("eval: (%s) on non-Int values %T, %T", op, l, r)
	}
	switch op {
	case "+":
		return li + ri, nil
	case "-":
		return li - ri, nil
	case "*":
		return li * ri, nil
	default:
		return nil, fmt.Errorf("eval: unhandled operator %q", op)
	}
}
