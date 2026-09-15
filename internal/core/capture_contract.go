package core

import (
	"fmt"
	"github.com/waj/fango/internal/types"
)

// inferCaptureContract erases scalar computation while preserving all capture
// and access paths. Lint rebuilds this graph independently from semantic Core.
func inferCaptureContract(d *Def) *types.CaptureContract {
	b := captureBuilder{}
	c := &types.CaptureContract{Params: append([]string(nil), d.Params...), Body: b.expr(d.Body)}
	for _, ev := range d.EffectParams {
		c.Effects = append(c.Effects, ev.Unique)
	}
	for _, tv := range d.TyParams {
		c.TypeParams = append(c.TypeParams, tv.ID)
	}
	return c
}

type captureBuilder struct{ next int }

func (b *captureBuilder) node(kind string, ty types.Type) *types.CaptureFlow {
	b.next++
	return &types.CaptureFlow{ID: b.next, Kind: kind, Type: ty}
}
func (b *captureBuilder) expr(e Expr) *types.CaptureFlow {
	if e == nil {
		return nil
	}
	n := b.node("scalar", e.Type())
	children := func(es ...Expr) {
		for _, x := range es {
			n.Children = append(n.Children, b.expr(x))
		}
	}
	effects := func(es []EffectInstance) {
		for _, ev := range es {
			n.Effects = append(n.Effects, ev.Unique)
		}
	}
	switch e := e.(type) {
	case *IntLit, *FloatLit, *StringLit, *CharLit, *UnitLit, *BoolLit, *TypeOf:
		// These values cannot carry a runtime capability.
	case *VarRef:
		n.Origin = e.Origin
		n.Kind, n.Name, n.TypeArgs = "global", e.Name, e.TyArgs
		if e.Local {
			n.Kind = "var"
		}
	case *NativeCall:
		n.Kind = "native"
		children(e.Args...)
	case *Quote:
		n.Kind = "native"
		children(e.Holes...)
	case *Neg:
		children(e.Operand)
	case *If:
		n.Kind = "branch"
		children(e.Cond, e.Then, e.Else)
	case *Seq:
		n.Kind = "seq"
		children(e.First, e.Then)
	case *Let:
		n.Kind, n.Name, n.Rec = "let", e.Name, e.Rec
		children(e.Rhs, e.Body)
	case *Lambda:
		n.Kind, n.Name = "lambda", e.Param
		effects(e.EffectParams)
		children(e.Body)
	case *App:
		n.Origin = e.Origin
		n.Kind, n.TypeArgs = "call", e.TyArgs
		effects(e.EvidenceArgs)
		if e.CalleeKind == Ctor {
			n.Kind = "ctor"
			if e.Ctor != nil {
				n.Index = e.Ctor.Index
			}
		} else {
			children(e.Callee)
		}
		children(e.Args...)
	case *Perform:
		n.Origin = e.Origin
		n.Kind, n.Index = "perform", e.Op.Index
		n.Effects = []int{e.Effect.Unique}
		n.Borrow, n.Retain = e.Op.BorrowsEvidence, e.Op.RetainsArguments
		children(e.Args...)
	case *ControlExit:
		n.Origin = e.Origin
		n.Kind, n.Index = "exit", e.Op.Index
		n.Effects = []int{e.Effect.Unique}
		children(e.Payload...)
	case *ResumeTail:
		n.Kind, n.Index = "resume", int(e.Owner)
		children(e.Value, e.NextState)
	case *Handle:
		n.Kind, n.Scope, n.Scoped = "handle", e.Scope, e.Scoped
		n.Effects = []int{e.Effect.Unique}
		if e.State != nil {
			n.Name = e.State.Name
			children(e.State.Initial)
		} else {
			children(nil)
		}
		children(e.Body)
		if e.Return != nil {
			n.Names = []string{e.Return.Param}
			children(e.Return.Body)
		} else {
			children(nil)
		}
		for _, cl := range e.Clauses {
			n.Clauses = append(n.Clauses, types.CaptureClause{Index: cl.Op.Index, Names: cl.Params, Types: cl.ParamTypes, Body: b.expr(cl.Body)})
		}
	case *Bracket:
		n.Kind, n.Scope, n.Name = "scope", e.Scope, e.Resource
		n.TypeArgs = []types.Type{e.ResourceTy}
		children(e.Acquire, e.Body, e.Release)
	case *Case:
		n.Kind, n.Name = "case", e.Bind
		children(e.Scrut)
		n.Children = append(n.Children, b.tree(e.Tree))
	case *Suspend:
		n.Kind = "suspend"
		if e.Owner.Unique != 0 {
			effects([]EffectInstance{e.Owner})
		}
		children(e.Request)
	case *IteratorScope:
		n.Kind, n.Scope, n.Scoped = "iterator", e.Scope, true
		n.TypeArgs = []types.Type{e.CursorTy}
		if e.Yield.Unique != 0 {
			effects([]EffectInstance{e.Yield})
		}
		children(e.Producer, e.Consumer)
	case *IteratorForEach:
		n.Kind, n.Access = "foreach", e.Access
		children(e.Action, e.Cursor)
	case *IteratorFold:
		n.Kind, n.Access = "fold", e.Access
		children(e.Combine, e.Initial, e.Cursor)
	default:
		panic(fmt.Sprintf("capture contract: unhandled Core expression %T", e))
	}
	return n
}
func (b *captureBuilder) tree(t Tree) *types.CaptureFlow {
	if t == nil {
		return nil
	}
	n := b.node("choice", nil)
	switch t := t.(type) {
	case *Unreachable:
	case *Leaf:
		return b.expr(t.Body)
	case *Guard:
		n.Kind = "branch"
		n.Children = []*types.CaptureFlow{b.expr(t.Cond), b.tree(t.Then), b.tree(t.Else)}
	case *SwitchCtor:
		n.Kind, n.Name = "switch", t.Scrut
		for _, c := range t.Cases {
			n.Clauses = append(n.Clauses, types.CaptureClause{Index: c.Ctor.Index, Names: c.Binds, Types: c.Ctor.Fields, Body: b.tree(c.Tree)})
		}
		n.Children = []*types.CaptureFlow{b.tree(t.Default)}
	case *SwitchLit:
		for _, c := range t.Cases {
			n.Children = append(n.Children, b.tree(c.Tree))
		}
		n.Children = append(n.Children, b.tree(t.Default))
	default:
		panic(fmt.Sprintf("capture contract: unhandled Core tree %T", t))
	}
	return n
}
