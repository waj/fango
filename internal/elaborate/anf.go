package elaborate

import (
	"fmt"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// ANF hoisting (doc/design.md, "Core and evidence invariants"): Case and If are statement-shaped in Go, so
// inside function bodies they must not sit in expression slots (an argument,
// an operand, a condition) where codegen's only recourse is an IIFE closure.
// This pass floats each such node into a Let directly above the statement
// that used it — codegen then emits `var tmp τ; switch … { tmp = … }` — and
// leaves tail positions (worker bodies, Let bodies, branch tails) alone.
//
// Hoists accumulate left to right and wrap in the same order, preserving
// source evaluation order now that expression slots may perform effects.
//
// The IIFE fallback remains for Lets in expression slots (top-level value
// initializers have no statement context).

// anf rewrites a definition body (tail position).
func (el *elab) anf(e core.Expr) core.Expr {
	switch e := e.(type) {
	case *core.Let:
		rhs, hoists := el.anfAssign(e.Rhs)
		body := el.anf(e.Body)
		out := core.Expr(&core.Let{Name: e.Name, Rhs: rhs, Body: body, Rec: e.Rec, Ty: body.Type()})
		return wrapHoists(hoists, out)
	case *core.If:
		cond, hoists := el.anfSlot(e.Cond)
		out := core.Expr(&core.If{Cond: cond, Then: el.anf(e.Then), Else: el.anf(e.Else), Ty: e.Ty})
		return wrapHoists(hoists, out)
	case *core.Case:
		scrut, hoists := el.anfSlot(e.Scrut)
		out := core.Expr(&core.Case{Scrut: scrut, Bind: e.Bind, Tree: el.anfTree(e.Tree), Ty: e.Ty})
		return wrapHoists(hoists, out)
	case *core.Lambda:
		return &core.Lambda{SourceType: e.SourceType, Param: e.Param, Body: el.anf(e.Body), Ty: e.Ty,
			ParamCapture: e.ParamCapture, EffectParams: e.EffectParams, RowParam: e.RowParam, RowEffects: e.RowEffects}
	case *core.Handle:
		clauses := make([]core.HandlerClause, len(e.Clauses))
		for i, c := range e.Clauses {
			clauses[i] = c
			clauses[i].Body = el.anf(c.Body)
		}
		var ret *core.ReturnClause
		if e.Return != nil {
			ret = &core.ReturnClause{Param: e.Return.Param, Body: el.anf(e.Return.Body)}
		}
		var state *core.HandlerState
		if e.State != nil {
			state = &core.HandlerState{Name: e.State.Name, Initial: el.anf(e.State.Initial), Ty: e.State.Ty}
		}
		return &core.Handle{Body: el.anf(e.Body), State: state, Effects: e.Effects, Scope: e.Scope, Scoped: e.Scoped, Clauses: clauses, Return: ret, Ty: e.Ty, Control: e.Control}
	case *core.Bracket:
		// Each slot is emitted as statements inside the scope, so each
		// normalizes in its own region: a hoist out of the release would run
		// before the body it is meant to follow.
		return &core.Bracket{Scope: e.Scope, Resource: e.Resource, ResourceTy: e.ResourceTy,
			Acquire: el.anf(e.Acquire), Release: el.anf(e.Release), Body: el.anf(e.Body),
			Ty: e.Ty, Control: e.Control}
	case *core.Seq:
		return &core.Seq{First: el.anf(e.First), Then: el.anf(e.Then), Ty: e.Ty}
	default:
		out, hoists := el.anfExprChildren(e)
		return wrapHoists(hoists, out)
	}
}

// anfAssign rewrites a Let right-hand side: codegen assigns it into a
// declared variable, so Case and If may head it directly (their innards
// normalize as tails). Hoists from the RHS's slots propagate to the caller
// and become Lets above the enclosing binding.
func (el *elab) anfAssign(e core.Expr) (core.Expr, []hoist) {
	switch e := e.(type) {
	case *core.If:
		cond, hoists := el.anfSlot(e.Cond)
		return &core.If{Cond: cond, Then: el.anf(e.Then), Else: el.anf(e.Else), Ty: e.Ty}, hoists
	case *core.Case:
		scrut, hoists := el.anfSlot(e.Scrut)
		return &core.Case{Scrut: scrut, Bind: e.Bind, Tree: el.anfTree(e.Tree), Ty: e.Ty}, hoists
	default:
		return el.anfSlot(e)
	}
}

type hoist struct {
	name string
	rhs  core.Expr
}

func wrapHoists(hoists []hoist, body core.Expr) core.Expr {
	for i := len(hoists) - 1; i >= 0; i-- {
		body = &core.Let{Name: hoists[i].name, Rhs: hoists[i].rhs, Body: body, Ty: body.Type()}
	}
	return body
}

// anfSlot rewrites an expression slot: a Case or If heading the slot is
// normalized, hoisted to a fresh Let, and replaced by its variable.
func (el *elab) anfSlot(e core.Expr) (core.Expr, []hoist) {
	switch e := e.(type) {
	case *core.Case, *core.If:
		norm, hoists := el.anfAssign(e)
		name := fmt.Sprintf("_h%d", el.tmp)
		el.tmp++
		return &core.VarRef{Name: name, Local: true, Ty: norm.Type()}, append(hoists, hoist{name: name, rhs: norm})
	case *core.Let:
		// Keep the binding region intact, but lift a control-producing region
		// into statements so its exit is checked before the enclosing call.
		out := el.anf(e)
		if control := core.ExprControl(out); control.Transport != types.Direct || control.Polymorphic {
			name := fmt.Sprintf("_control%d", el.tmp)
			el.tmp++
			return &core.VarRef{Name: name, Local: true, Ty: out.Type()}, []hoist{{name: name, rhs: out}}
		}
		return out, nil
	case *core.Lambda:
		return &core.Lambda{SourceType: e.SourceType, Param: e.Param, Body: el.anf(e.Body), Ty: e.Ty,
			ParamCapture: e.ParamCapture, EffectParams: e.EffectParams, RowParam: e.RowParam, RowEffects: e.RowEffects}, nil
	case *core.Handle, *core.Seq:
		out := el.anf(e)
		if control := core.ExprControl(out); control.Transport != types.Direct || control.Polymorphic {
			name := fmt.Sprintf("_control%d", el.tmp)
			el.tmp++
			return &core.VarRef{Name: name, Local: true, Ty: out.Type()}, []hoist{{name: name, rhs: out}}
		}
		return out, nil
	default:
		out, hoists := el.anfExprChildren(e)
		if control := core.ExprControl(out); control.Transport != types.Direct || control.Polymorphic {
			name := fmt.Sprintf("_control%d", el.tmp)
			el.tmp++
			return &core.VarRef{Name: name, Local: true, Ty: out.Type()}, append(hoists, hoist{name: name, rhs: out})
		}
		return out, hoists
	}
}

// anfExprChildren rebuilds a non-binding node with each child treated as an
// expression slot, accumulating the children's hoists left to right.
func (el *elab) anfExprChildren(e core.Expr) (core.Expr, []hoist) {
	var hoists []hoist
	slot := func(c core.Expr) core.Expr {
		out, h := el.anfSlot(c)
		hoists = append(hoists, h...)
		return out
	}
	switch e := e.(type) {
	case *core.IntLit, *core.FloatLit, *core.RegexLit, *core.StringLit, *core.CharLit, *core.BoolLit, *core.UnitLit, *core.VarRef, *core.TypeOf:
		return e, nil
	case *core.ControlExit:
		payload := make([]core.Expr, len(e.Payload))
		for i, p := range e.Payload {
			payload[i] = slot(p)
		}
		return &core.ControlExit{Origin: e.Origin, Effect: e.Effect, Op: e.Op, Payload: payload, Ty: e.Ty}, hoists
	case *core.Neg:
		return &core.Neg{Operand: slot(e.Operand), Ty: e.Ty}, hoists
	case *core.ParallelMap:
		return &core.ParallelMap{Function: slot(e.Function), Input: slot(e.Input), Ty: e.Ty}, hoists
	case *core.AsyncLaunch:
		n := *e
		c := *e.Call
		c.Callee = slot(c.Callee)
		c.Args = []core.Expr{slot(c.Args[0])}
		n.Call = &c
		return &n, hoists
	case *core.AsyncRebase:
		c := *e.Call
		c.Callee = slot(c.Callee)
		c.Args = []core.Expr{slot(c.Args[0])}
		return &core.AsyncRebase{Call: &c}, hoists
	case *core.AsyncSupervise:
		c := *e.Call
		c.Callee = slot(c.Callee)
		c.Args = []core.Expr{slot(c.Args[0])}
		return &core.AsyncSupervise{Call: &c}, hoists
	case *core.NativeCall:
		args := make([]core.Expr, len(e.Args))
		for i, a := range e.Args {
			args[i] = slot(a)
		}
		return &core.NativeCall{Name: e.Name, Module: e.Module, Storage: e.Storage, Args: args, Ty: e.Ty}, hoists

	case *core.FailureInspect:
		args := make([]core.Expr, len(e.Args))
		for i, a := range e.Args {
			args[i] = slot(a)
		}
		return &core.FailureInspect{Name: e.Name, Args: args, Result: e.Result, Ty: e.Ty}, hoists

	case *core.AttributeLookup:
		n := *e
		n.Bag = slot(e.Bag)
		return &n, hoists
	case *core.Quote:
		holes := make([]core.Expr, len(e.Holes))
		for i, h := range e.Holes {
			holes[i] = slot(h)
		}
		return &core.Quote{Template: e.Template, Holes: holes, Ty: e.Ty}, hoists
	case *core.Perform:
		args := make([]core.Expr, len(e.Args))
		for i, a := range e.Args {
			args[i] = slot(a)
		}
		return &core.Perform{Origin: e.Origin, Op: e.Op, Effect: e.Effect, LocalTypes: e.LocalTypes, Args: args, Ty: e.Ty, Control: e.Control}, hoists

	case *core.ResumeTail:
		var next core.Expr
		if e.NextState != nil {
			// Keep the next-state computation beneath ResumeTail. Codegen must
			// evaluate the operation result first, even when the update is
			// statement-shaped control flow.
			next = el.anf(e.NextState)
		}
		return &core.ResumeTail{Owner: e.Owner, Value: slot(e.Value), NextState: next, ClauseResult: e.ClauseResult}, hoists
	case *core.App:
		callee := e.Callee
		if e.CalleeKind == core.Value {
			callee = slot(e.Callee)
		}
		args := make([]core.Expr, len(e.Args))
		for i, a := range e.Args {
			args[i] = slot(a)
		}
		return &core.App{SourceType: e.SourceType, Origin: e.Origin, CalleeKind: e.CalleeKind, Callee: callee, Args: args,
			TyArgs: e.TyArgs, Ty: e.Ty, Ctor: e.Ctor, EvidenceArgs: e.EvidenceArgs, Control: e.Control, Row: e.Row}, hoists
	default:
		panic(fmt.Sprintf("elaborate: anf unhandled node %T", e))
	}
}

// anfTree normalizes every leaf body as a tail.
func (el *elab) anfTree(t core.Tree) core.Tree {
	switch t := t.(type) {
	case *core.Unreachable:
		return t
	case *core.Guard:
		return &core.Guard{Cond: el.anf(t.Cond), Then: el.anfTree(t.Then), Else: el.anfTree(t.Else)}
	case *core.Leaf:
		return &core.Leaf{Body: el.anf(t.Body)}
	case *core.SwitchCtor:
		cases := make([]core.CtorCase, len(t.Cases))
		for i, c := range t.Cases {
			cases[i] = core.CtorCase{Ctor: c.Ctor, Binds: c.Binds, Tree: el.anfTree(c.Tree)}
		}
		var def core.Tree
		if t.Default != nil {
			def = el.anfTree(t.Default)
		}
		return &core.SwitchCtor{Scrut: t.Scrut, ADT: t.ADT, Cases: cases, Default: def}
	case *core.SwitchLit:
		cases := make([]core.LitCase, len(t.Cases))
		for i, c := range t.Cases {
			cases[i] = core.LitCase{Lit: c.Lit, Tree: el.anfTree(c.Tree)}
		}
		return &core.SwitchLit{Scrut: t.Scrut, Cases: cases, Default: el.anfTree(t.Default)}
	default:
		panic(fmt.Sprintf("elaborate: anf unhandled tree node %T", t))
	}
}
