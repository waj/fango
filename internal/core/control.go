package core

import "github.com/waj/fango/internal/types"

// ArrowControl returns the transport attached to the arrow executed after
// arity applications. A multi-parameter worker executes at its final arrow.
func ArrowControl(t types.Type, arity int) types.Control {
	if arity == 0 {
		return types.Control{}
	}
	var fn *types.TFun
	for range arity {
		fn = t.(*types.TFun)
		t = fn.Ret
	}
	return types.FunctionControl(fn)
}

// ExprControl is the protocol produced by evaluating e in value position.
// Composite nodes join their evaluated children; lambdas and constructors
// merely build values, so their latent arrow protocols do not execute here.
func ExprControl(e Expr) types.Control {
	if e == nil {
		return types.Control{}
	}
	join := func(es ...Expr) types.Control {
		cs := make([]types.Control, 0, len(es))
		for _, e := range es {
			cs = append(cs, ExprControl(e))
		}
		return types.JoinControl(cs...)
	}
	switch e := e.(type) {
	case *IntLit, *FloatLit, *StringLit, *CharLit, *UnitLit, *BoolLit,
		*VarRef, *Lambda, *TypeOf:
		return types.Control{}
	case *ControlExit:
		return types.Control{Transport: types.Exit}
	case *Suspend:
		return types.JoinControl(types.Control{Transport: types.Machine}, ExprControl(e.Request))
	case *IteratorNext:
		return types.JoinControl(types.Control{Transport: types.Machine}, ExprControl(e.Cursor))
	case *IteratorScope:
		// Producer's latent Machine protocol is consumed by this owner rather
		// than joined into the enclosing computation.
		return types.JoinControl(e.Control, ExprControl(e.Producer), ExprControl(e.Consumer))

	case *Neg:
		return ExprControl(e.Operand)
	case *NativeCall:
		return join(e.Args...)
	case *FailureInspect:
		return join(e.Args...)
	case *Quote:
		return join(e.Holes...)
	case *If:
		return types.JoinControl(ExprControl(e.Cond), ExprControl(e.Then), ExprControl(e.Else))
	case *Perform:
		return types.JoinControl(e.Control, join(e.Args...))
	case *ResumeTail:
		return types.JoinControl(ExprControl(e.Value), ExprControl(e.NextState))
	case *Seq:
		return types.JoinControl(ExprControl(e.First), ExprControl(e.Then))
	case *Bracket:
		// A cleanup scope re-propagates every exit it intercepts, so unlike an
		// abort handler it consumes nothing: all three children join.
		return types.JoinControl(e.Control, ExprControl(e.Acquire), ExprControl(e.Release), ExprControl(e.Body))
	case *Let:
		return types.JoinControl(ExprControl(e.Rhs), ExprControl(e.Body))
	case *App:
		parts := []types.Control{e.Control, ExprControl(e.Callee)}
		for _, a := range e.Args {
			parts = append(parts, ExprControl(a))
		}
		return types.JoinControl(parts...)
	case *Handle:
		parts := []types.Control{e.Control}
		if e.State != nil {
			parts = append(parts, ExprControl(e.State.Initial))
		}
		if len(e.Clauses) == 0 || e.Clauses[0].Op == nil || !e.Clauses[0].Op.Abort {
			// Resumptive evidence may select an Exit ABI member. Abort-only body
			// exits are instead consumed by this boundary.
			parts = append(parts, ExprControl(e.Body))
			for _, c := range e.Clauses {
				parts = append(parts, ExprControl(c.Body))
			}
			if e.Return != nil {
				parts = append(parts, ExprControl(e.Return.Body))
			}
		}
		return types.JoinControl(parts...)
	case *Case:
		return types.JoinControl(ExprControl(e.Scrut), treeControl(e.Tree))
	default:
		return types.Control{}
	}
}

func treeControl(t Tree) types.Control {
	switch t := t.(type) {
	case nil, *Unreachable:
		return types.Control{}
	case *Leaf:
		return ExprControl(t.Body)
	case *Guard:
		return types.JoinControl(ExprControl(t.Cond), treeControl(t.Then), treeControl(t.Else))
	case *SwitchCtor:
		parts := []types.Control{treeControl(t.Default)}
		for _, c := range t.Cases {
			parts = append(parts, treeControl(c.Tree))
		}
		return types.JoinControl(parts...)
	case *SwitchLit:
		parts := []types.Control{treeControl(t.Default)}
		for _, c := range t.Cases {
			parts = append(parts, treeControl(c.Tree))
		}
		return types.JoinControl(parts...)
	default:
		return types.Control{}
	}
}

func ControlName(c types.Control) string {
	name := "direct"
	switch c.Transport {
	case types.Exit:
		name = "exit"
	case types.Machine:
		name = "machine"
	}
	if c.Polymorphic {
		if c.Transport == types.Direct {
			return "poly"
		}
		return name + "+poly"
	}
	return name
}
