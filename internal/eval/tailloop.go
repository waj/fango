// Self tail calls run as frame-reuse loops (doc/design.md, "Interpreter and
// REPL"): App{Worker} evaluation of an eligible definition iterates instead
// of recursing through Go frames, so deep loop-by-recursion drivers cost
// constant stack in this backend too — the differential suite's deep
// fixtures prove the compiled rewrite only because the interpreter loops.
package eval

import (
	"fmt"

	"github.com/waj/fango/internal/core"
)

// tailJump is tailStep's private sentinel: the leaf reached a rewritable
// self call and vars is the next iteration's frame. It can never collide
// with a user value — no Core expression evaluates to a *tailJump.
type tailJump struct {
	vars map[string]Value
}

// evalTailLoop runs an eligible worker's body as a loop. Evidence needs no
// per-iteration work: the tail skeleton never enters a Handle, and identity
// evidence (the eligibility predicate) guarantees the worker's own evidence
// map — installed by the caller — is already correct for every iteration.
func (in *interp) evalTailLoop(def *core.Def, vars map[string]Value) (Value, error) {
	for {
		// A fully-trivial jump (`f x = f x`) evaluates almost nothing, so
		// count an explicit step per iteration to keep the every-N
		// cancellation poll firing.
		in.steps++
		if in.steps%pollEvery == 0 {
			select {
			case <-in.ctx.Done():
				return nil, fmt.Errorf("interrupted")
			default:
			}
			if in.budget > 0 && in.steps > in.budget {
				return nil, ErrStepBudget
			}
		}
		v, err := in.tailStep(def, def.Body, &Frame{vars: vars})
		if err != nil {
			return nil, err
		}
		if _, ok := asExit(v); ok {
			return v, nil
		}
		if j, ok := v.(*tailJump); ok {
			vars = j.vars
			continue
		}
		return v, nil
	}
}

// tailStep walks the tail skeleton — Let bodies, If branches, Case leaf
// bodies, Seq tails — and either returns the iteration's final value or a
// *tailJump carrying the next frame. Everything off the skeleton evaluates
// ordinarily.
func (in *interp) tailStep(def *core.Def, e core.Expr, fr *Frame) (Value, error) {
	switch e := e.(type) {
	case *core.Let:
		if e.Rec {
			lam, ok := e.Rhs.(*core.Lambda)
			if !ok {
				return nil, fmt.Errorf("eval: recursive Let `%s` without a Lambda RHS", e.Name)
			}
			frame := &Frame{parent: fr, vars: map[string]Value{}}
			frame.vars[e.Name] = &Closure{Param: lam.Param, Body: lam.Body, Env: frame, Evidence: cloneEvidence(in.evidence)}
			return in.tailStep(def, e.Body, frame)
		}
		v, err := in.eval(e.Rhs, fr)
		if err != nil {
			return nil, err
		}
		if _, ok := asExit(v); ok {
			return v, nil
		}
		return in.tailStep(def, e.Body, &Frame{parent: fr, vars: map[string]Value{e.Name: v}})
	case *core.If:
		cond, err := in.eval(e.Cond, fr)
		if err != nil {
			return nil, err
		}
		if _, ok := asExit(cond); ok {
			return cond, nil
		}
		if cond.(bool) {
			return in.tailStep(def, e.Then, fr)
		}
		return in.tailStep(def, e.Else, fr)
	case *core.Seq:
		first, err := in.eval(e.First, fr)
		if err != nil {
			return nil, err
		}
		if _, ok := asExit(first); ok {
			return first, nil
		}
		return in.tailStep(def, e.Then, fr)
	case *core.Case:
		v, err := in.eval(e.Scrut, fr)
		if err != nil {
			return nil, err
		}
		if _, ok := asExit(v); ok {
			return v, nil
		}
		frame := &Frame{parent: fr, vars: map[string]Value{e.Bind: v}}
		return in.tree(e.Tree, frame, func(body core.Expr, leafFr *Frame) (Value, error) {
			return in.tailStep(def, body, leafFr)
		})
	case *core.App:
		if e.CalleeKind == core.Worker && core.IsTailLoopCall(def, e) {
			vars := make(map[string]Value, len(e.Args))
			for i, a := range e.Args {
				v, err := in.eval(a, fr)
				if err != nil {
					return nil, err
				}
				if _, ok := asExit(v); ok {
					return v, nil
				}
				vars[def.Params[i]] = v
			}
			return &tailJump{vars: vars}, nil
		}
		return in.eval(e, fr)
	default:
		return in.eval(e, fr)
	}
}
