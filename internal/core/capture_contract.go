package core

import (
	"fmt"
	"github.com/waj/fango/internal/types"
	"reflect"
)

// CaptureContractCurrent independently reconstructs the ownership/access
// graph retained by later execution IR. Missing metadata is never a proof.
func CaptureContractCurrent(d *Def) bool {
	return d != nil && d.CaptureContract != nil && reflect.DeepEqual(d.CaptureContract, inferCaptureContract(d))
}

// inferCaptureContract erases scalar computation while preserving all capture
// and access paths. Lint rebuilds this graph independently from semantic Core.
func inferCaptureContract(d *Def) *types.CaptureContract {
	b := captureBuilder{sourceType: d.SourceType}
	c := &types.CaptureContract{SourceType: d.SourceType, Params: append([]string(nil), d.Params...), Body: b.expr(d.Body), RowParam: d.RowParam}
	for _, ev := range d.RowEffects {
		c.RowEffects = append(c.RowEffects, ev.Unique)
	}
	for _, ev := range d.EffectParams {
		c.Effects = append(c.Effects, ev.Unique)
	}
	for _, tv := range d.TyParams {
		c.TypeParams = append(c.TypeParams, tv.ID)
	}
	return c
}

type captureBuilder struct {
	next       int
	sourceType types.Type
}

func (b *captureBuilder) node(kind string, ty types.Type) *types.CaptureFlow {
	b.next++
	return &types.CaptureFlow{ID: b.next, Kind: kind, Type: ty}
}
func (b *captureBuilder) expr(e Expr) *types.CaptureFlow {
	if e == nil {
		return nil
	}
	n := b.node("scalar", e.Type())
	if row := ExpressionRow(e); row != nil {
		n.Row = &types.CaptureRow{From: row.From}
		for _, ev := range row.Effects {
			n.Row.Effects = append(n.Row.Effects, ev.Unique)
		}
	}
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
		if e.Storage.Kind != "" {
			n.Kind = "native-" + e.Storage.Kind
			n.NativeStorage = e.Storage
		}
		if e.RetainsRequest {
			n.Kind = "native-request"
		}
		children(e.Args...)
	case *Work:
		n.Kind = "work-" + e.Kind
		n.SourceType = e.SourceRow
		children(e.Args...)
	case *FailureInspect:
		n.Kind = "native"
		children(e.Args...)
	case *Completion:
		n.Kind = "completion_failure"
		if e.Name == types.CompletionDropSuspensionName || e.Name == types.CompletionDropDriveName {
			n.Kind = "completion_drop"
		}
		if e.Name == types.CompletionReplayName {
			n.Kind = "completion_replay"
		}
		if e.Name == types.CompletionFromFailureName {
			n.Kind = "completion_from_failure"
		}
		if types.CapturesCompletion(e.Name) {
			n.Kind = "completion_capture"
		}
		if e.Name == types.NativeRequestImmediateName {
			n.Kind = "completion_immediate"
		}
		children(e.Value)
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
		n.SourceType = e.SourceType
		n.Kind, n.Name = "lambda", e.Param
		n.RowParam = e.RowParam
		for _, ev := range e.RowEffects {
			n.Deferred = append(n.Deferred, ev.Unique)
		}
		effects(e.EffectParams)
		children(e.Body)
	case *App:
		n.SourceType = e.SourceType
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
		n.Service = e.Op.Invocation != nil
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
			n.Clauses = append(n.Clauses, types.CaptureClause{Index: cl.Op.Index, Names: cl.Params, Types: cl.ParamTypes, Suppressed: cl.SuppressedParam, Body: b.expr(cl.Body)})
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
		children(e.Request)
	case *CoroutineScope:
		n.Kind, n.Scope, n.Scoped = "coroutine", e.Scope, true
		if types.CoroutineScopeType(e.CursorTy) {
			n.Kind = "coroutine-scope"
			if fn, ok := b.sourceType.(*types.TFun); ok {
				if driver, ok := fn.Arg.(*types.TFun); ok && types.CoroutineScopeType(driver.Arg) {
					n.SourceType = driver.Arg.(*types.TCon).Args[0]
				}
			}
		}
		n.TypeArgs = []types.Type{e.CursorTy}
		if first, ok := b.sourceType.(*types.TFun); ok {
			if second, ok := first.Ret.(*types.TFun); ok {
				if driver, ok := second.Arg.(*types.TFun); ok {
					if cursor, ok := driver.Arg.(*types.TCon); ok && cursor.Name == types.CoroutineTypeName && len(cursor.Args) == 4 {
						n.SourceType = cursor.Args[3]
					}
				}
			}
		}
		if e.Yield.Unique != 0 {
			effects([]EffectInstance{e.Yield})
		}
		if e.Traversal.Unique != 0 {
			effects([]EffectInstance{e.Traversal})
		}
		children(e.Producer, e.Consumer)

	case *CoroutineAdvance:
		n.Kind, n.Access = "advance", e.Access
		if e.Close {
			n.Kind = "close"
		}
		children(e.Cursor, e.Reply)

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
