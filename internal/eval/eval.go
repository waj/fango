// Package eval is the tree-walking Core interpreter: the REPL's execution
// engine and the differential test oracle for the compiled backend. Both
// backends implement the same Core semantics; the e2e suite diffs them.
package eval

import (
	"context"
	"fmt"
	"io"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/runtime/fangort"
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

// Env holds top-level cells.
type Env struct {
	cells map[string]*Cell
}

// Frame holds block-local bindings (§3.6) — eager values, unlike the lazy
// top-level cells. Function parameters (S3) extend the same chain.
type Frame struct {
	parent *Frame
	vars   map[string]Value
}

func (f *Frame) lookup(name string) (Value, bool) {
	for ; f != nil; f = f.parent {
		if v, ok := f.vars[name]; ok {
			return v, true
		}
	}
	return nil, false
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

// interp carries the cancellation context and the print destination; ctx is
// polled every pollEvery evaluation steps so Ctrl-C interrupts runaway REPL
// expressions.
type interp struct {
	ctx   context.Context
	env   *Env
	out   io.Writer
	steps int
}

const pollEvery = 4096

// Eval evaluates a Core expression under env. Print output goes to out —
// the REPL passes its own writer, the differential harness a buffer.
func Eval(ctx context.Context, e core.Expr, env *Env, out io.Writer) (Value, error) {
	return (&interp{ctx: ctx, env: env, out: out}).eval(e, nil)
}

// Force evaluates (and memoizes) the named top-level binding.
func Force(ctx context.Context, name string, env *Env, out io.Writer) (Value, error) {
	return (&interp{ctx: ctx, env: env, out: out}).force(name)
}

func (in *interp) eval(e core.Expr, fr *Frame) (Value, error) {
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
	case *core.FloatLit:
		return e.Val, nil
	case *core.StringLit:
		return e.Val, nil
	case *core.BoolLit:
		return e.Val, nil
	case *core.VarRef:
		if v, ok := fr.lookup(e.Name); ok {
			return v, nil
		}
		return in.force(e.Name)
	case *core.Let:
		// Eager, in order — identical to the compiled backend's locals.
		v, err := in.eval(e.Rhs, fr)
		if err != nil {
			return nil, err
		}
		return in.eval(e.Body, &Frame{parent: fr, vars: map[string]Value{e.Name: v}})
	case *core.Neg:
		v, err := in.eval(e.Operand, fr)
		if err != nil {
			return nil, err
		}
		switch v := v.(type) {
		case int64:
			return -v, nil
		case float64:
			return -v, nil
		default:
			return nil, fmt.Errorf("eval: negating a %T", v)
		}
	case *core.BinOp:
		l, err := in.eval(e.L, fr)
		if err != nil {
			return nil, err
		}
		r, err := in.eval(e.R, fr)
		if err != nil {
			return nil, err
		}
		return applyBinOp(e.Op, l, r)
	case *core.If:
		cond, err := in.eval(e.Cond, fr)
		if err != nil {
			return nil, err
		}
		if cond.(bool) {
			return in.eval(e.Then, fr)
		}
		return in.eval(e.Else, fr)
	case *core.Print:
		v, err := in.eval(e.Arg, fr)
		if err != nil {
			return nil, err
		}
		// The one shared formatting implementation (fangort), with only
		// the newline added locally — mirroring fangort.PrintX.
		var s string
		switch v := v.(type) {
		case int64:
			s = fangort.ShowInt(v)
		case float64:
			s = fangort.ShowFloat(v)
		case string:
			s = fangort.ShowString(v)
		case bool:
			s = fangort.ShowBool(v)
		default:
			return nil, fmt.Errorf("eval: printing a %T", v)
		}
		if _, err := fmt.Fprintln(in.out, s); err != nil {
			return nil, err
		}
		return struct{}{}, nil
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
	v, err := in.eval(cell.Body, nil)
	cell.forcing = false
	if err != nil {
		return nil, err
	}
	cell.memo, cell.forced = v, true
	return v, nil
}

// applyBinOp dispatches on the left value's dynamic type — bijective with
// the solved Core type. Int arithmetic wraps (int64), Float is IEEE (±Inf,
// NaN, no panics) — the exact semantics elaborate's constant folder and the
// compiled backend's native operators implement.
func applyBinOp(op string, l, r Value) (Value, error) {
	switch lv := l.(type) {
	case int64:
		rv := r.(int64)
		switch op {
		case "+":
			return lv + rv, nil
		case "-":
			return lv - rv, nil
		case "*":
			return lv * rv, nil
		case "==":
			return lv == rv, nil
		case "/=":
			return lv != rv, nil
		case "<":
			return lv < rv, nil
		case ">":
			return lv > rv, nil
		case "<=":
			return lv <= rv, nil
		case ">=":
			return lv >= rv, nil
		}
	case float64:
		rv := r.(float64)
		switch op {
		case "+":
			return lv + rv, nil
		case "-":
			return lv - rv, nil
		case "*":
			return lv * rv, nil
		case "/":
			return lv / rv, nil
		case "==":
			return lv == rv, nil
		case "/=":
			return lv != rv, nil
		case "<":
			return lv < rv, nil
		case ">":
			return lv > rv, nil
		case "<=":
			return lv <= rv, nil
		case ">=":
			return lv >= rv, nil
		}
	case string:
		rv := r.(string)
		switch op {
		case "++":
			return lv + rv, nil
		case "==":
			return lv == rv, nil
		case "/=":
			return lv != rv, nil
		case "<":
			return lv < rv, nil
		case ">":
			return lv > rv, nil
		case "<=":
			return lv <= rv, nil
		case ">=":
			return lv >= rv, nil
		}
	case bool:
		rv := r.(bool)
		switch op {
		case "==":
			return lv == rv, nil
		case "/=":
			return lv != rv, nil
		}
	}
	return nil, fmt.Errorf("eval: (%s) on %T values — the linter should have rejected this", op, l)
}
