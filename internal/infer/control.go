package infer

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/types"
)

// A pure handler runner can still consume a caller's controlled callback.
// Handling its effect does not turn an Exit-family callback into a Direct
// function value. Keep the runner's transport polymorphic independently of
// its (still pure) source effect row.
func (ck *Checker) runnerControl(ty types.Type, arity int, equations []ast.Equation) types.Type {
	if arity == 0 {
		return ty
	}
	t := ck.Sub.Apply(ty)
	cur := t
	controlled := false
	var last *types.TFun
	for range arity {
		fn, ok := cur.(*types.TFun)
		if !ok {
			return ty
		}
		controlled = controlled || ck.controlledValue(fn.Arg, map[int]bool{})
		last, cur = fn, fn.Ret
	}
	for _, eq := range equations {
		if ck.serviceActivation(eq.Body) {
			last.Control = types.Control{Transport: types.Machine}
			return t
		}
	}
	if !controlled || types.FunctionControl(last).Polymorphic {
		return ty
	}
	for _, eq := range equations {
		if ck.controlledCalls(eq.Body) {
			last.Control.Polymorphic = true
			return t
		}
	}
	return ty
}

// Installing service evidence constructs Machine operation workers even when
// the subject only returns a bound callable. The factory's source row stays
// pure; its transport still has to support constructing those workers.
func (ck *Checker) serviceActivation(e ast.Expr) bool {
	if d := ck.Desugared[e]; d != nil {
		return ck.serviceActivation(d)
	}
	call := ck.serviceActivation
	switch e := e.(type) {
	case *ast.Handle:
		if len(e.Clauses) > 0 {
			if op := ck.Operations[e.Clauses[0].Op]; op != nil && op.Owner.Service {
				return true
			}
		}
		if call(e.Body) || e.State != nil && call(e.State.Initial) {
			return true
		}
		for _, c := range e.Clauses {
			if call(c.Body) {
				return true
			}
			for _, eq := range c.Equations {
				if call(eq.Body) {
					return true
				}
			}
		}
		if e.Return != nil {
			if call(e.Return.Body) {
				return true
			}
			for _, eq := range e.Return.Equations {
				if call(eq.Body) {
					return true
				}
			}
		}
	case *ast.App:
		if ty := ck.ExprTypes[e.Fn]; ty != nil {
			if fn, ok := ck.Sub.Apply(ty).(*types.TFun); ok && types.FunctionControl(fn).Transport == types.Machine && fn.Eff.Empty() {
				return true
			}
		}
		return call(e.Fn) || call(e.Arg)
	case *ast.If:
		return call(e.Cond) || call(e.Then) || call(e.Else)
	case *ast.Block:
		for _, b := range e.Binds {
			if len(b.Params) == 0 && call(b.Body) {
				return true
			}
		}
		for _, item := range e.Items {
			if item.Expr != nil && call(item.Expr) {
				return true
			}
		}
		return call(e.Result)
	case *ast.Case:
		if call(e.Scrutinee) {
			return true
		}
		for _, b := range e.Branches {
			if call(b.Body) {
				return true
			}
		}
	case *ast.RecordLit:
		for _, field := range e.Fields {
			if call(field.Value) {
				return true
			}
		}
	case *ast.RecordGet:
		return call(e.Record)
	case *ast.RecordUpdate:
		if call(e.Record) {
			return true
		}
		for _, field := range e.Fields {
			if call(field.Value) {
				return true
			}
		}
	case *ast.Neg:
		return call(e.Operand)
	case *ast.BinOp:
		return call(e.L) || call(e.R)
	}
	return false
}

func (ck *Checker) controlledValue(t types.Type, seen map[int]bool) bool {
	switch t := t.(type) {
	case *types.TFun:
		return types.FunctionControl(t).Polymorphic || ck.controlledValue(t.Arg, seen) || ck.controlledValue(t.Ret, seen)
	case *types.TCon:
		if seen[t.Unique] {
			return false
		}
		seen[t.Unique] = true
		defer delete(seen, t.Unique)
		if adt := ck.ADTs[t.Unique]; adt != nil {
			for _, c := range adt.Ctors {
				for _, f := range c.Fields {
					if ck.controlledValue(types.SubstRigid(f, adt.ParamSubst(t.Args)), seen) {
						return true
					}
				}
			}
		}
	}
	return false
}

func (ck *Checker) controlledCalls(e ast.Expr) bool {
	if d := ck.Desugared[e]; d != nil {
		return ck.controlledCalls(d)
	}
	call := ck.controlledCalls
	switch e := e.(type) {
	case *ast.App:
		if ty := ck.ExprTypes[e.Fn]; ty != nil {
			if fn, ok := ck.Sub.Apply(ty).(*types.TFun); ok && types.FunctionControl(fn).Polymorphic {
				return true
			}
		}
		return call(e.Fn) || call(e.Arg)
	case *ast.If:
		return call(e.Cond) || call(e.Then) || call(e.Else)
	case *ast.Case:
		if call(e.Scrutinee) {
			return true
		}
		for _, b := range e.Branches {
			if call(b.Body) {
				return true
			}
		}
	case *ast.Block:
		for _, b := range e.Binds {
			if len(b.Params) == 0 && call(b.Body) {
				return true
			}
		}
		for _, item := range e.Items {
			if item.Expr != nil && call(item.Expr) {
				return true
			}
		}
		return call(e.Result)
	case *ast.Handle:
		if e.Return != nil {
			if call(e.Return.Body) {
				return true
			}
			for _, eq := range e.Return.Equations {
				if call(eq.Body) {
					return true
				}
			}
		}
		if call(e.Body) {
			return true
		}
		if e.State != nil && call(e.State.Initial) {
			return true
		}
		for _, c := range e.Clauses {
			if call(c.Body) {
				return true
			}
			for _, eq := range c.Equations {
				if call(eq.Body) {
					return true
				}
			}
		}
	case *ast.RecordLit:
		for _, f := range e.Fields {
			if call(f.Value) {
				return true
			}
		}
	case *ast.RecordGet:
		return call(e.Record)
	case *ast.RecordUpdate:
		if call(e.Record) {
			return true
		}
		for _, f := range e.Fields {
			if call(f.Value) {
				return true
			}
		}
	case *ast.Neg:
		return call(e.Operand)
	case *ast.BinOp:
		return call(e.L) || call(e.R)
	}
	return false
}
