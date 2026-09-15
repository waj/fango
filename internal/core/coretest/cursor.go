// Package coretest supplies semantic fixtures shared by backend proof tests.
package coretest

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// CursorScope runs a producer and consumer through the private owner boundary.
// The consumer pulls once, suspends to the host, and then exits early.
func CursorScope() (*core.Prog, *types.Builtins) {
	return cursorScope(false)
}

// SynchronousCursorScope handles Traversal, leaving a Direct result contract.
func SynchronousCursorScope() (*core.Prog, *types.Builtins) {
	return cursorScope(true)
}

func cursorScope(synchronous bool) (*core.Prog, *types.Builtins) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	control := types.Control{Transport: types.Machine}
	a := sup.FreshRigid(types.General)
	con := &types.TCon{Unique: sup.NextUnique(), Name: "Maybe.Maybe", Args: []types.Type{a}}
	nothing := &types.CtorInfo{Name: "Maybe.Nothing", Index: 0, Result: con}
	just := &types.CtorInfo{Name: "Maybe.Just", Index: 1, Fields: []types.Type{a}, Result: con}
	adt := &types.ADTInfo{Con: con, Params: []*types.TVar{a}, Ctors: []*types.CtorInfo{nothing, just}}
	maybe := &types.TCon{Unique: con.Unique, Name: con.Name, Args: []types.Type{b.Int}}
	cursor := &types.TCon{Unique: sup.NextUnique(), Name: types.IteratorTypeName, Args: []types.Type{b.Int}}
	producerTy := &types.TFun{Arg: b.Unit, Ret: b.Unit, Control: control}
	consumerTy := &types.TFun{Arg: cursor, Ret: maybe, Control: control}
	next := core.Def{Name: types.IteratorNextName, Owner: "Iterator", Type: consumerTy, Params: []string{"cursor"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture()}, Control: control,
		Body: &core.IteratorNext{Cursor: &core.VarRef{Name: "cursor", Local: true, Ty: cursor}, Result: adt, Access: types.ExclusiveAdvance, Ty: maybe}}
	scopeTy := &types.TFun{Arg: producerTy, Ret: &types.TFun{Arg: consumerTy, Ret: maybe, Control: control}}
	scope := core.Def{Name: types.GeneratorWithIteratorName, Owner: "Generator", Type: scopeTy, Params: []string{"producer", "consumer"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture(), sup.FreshCapture()}, Control: control,
		Body: &core.IteratorScope{Scope: sup.FreshScope(), Producer: &core.VarRef{Name: "producer", Local: true, Ty: producerTy}, Consumer: &core.VarRef{Name: "consumer", Local: true, Ty: consumerTy}, CursorTy: cursor, Ty: maybe, Control: control}}
	producer := &core.Lambda{Param: "unit", ParamCapture: sup.FreshCapture(), Ty: producerTy, Body: &core.Seq{
		First: &core.Suspend{Request: &core.IntLit{Val: 42, Ty: b.Int}, Ty: b.Unit}, Then: &core.UnitLit{Ty: b.Unit}, Ty: b.Unit}}
	pull := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: next.Name, Ty: consumerTy}, Args: []core.Expr{&core.VarRef{Name: "it", Local: true, Ty: cursor}}, Ty: maybe, Control: control}
	consumer := &core.Lambda{Param: "it", ParamCapture: sup.FreshCapture(), Ty: consumerTy, Body: &core.Let{Name: "item", Rhs: pull, Ty: maybe, Body: &core.Seq{
		First: &core.Suspend{Request: &core.IntLit{Val: 99, Ty: b.Int}, Ty: b.Unit}, Then: &core.VarRef{Name: "item", Local: true, Ty: maybe}, Ty: maybe}}}
	main := core.Def{Name: "Main.main", Owner: "Main", Type: maybe, Control: control, Body: &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: scope.Name, Ty: scopeTy}, Args: []core.Expr{producer, consumer}, Ty: maybe, Control: control}}
	p := &core.Prog{Entry: main.Name, ADTs: []*types.ADTInfo{adt}, Intrinsics: map[string]bool{next.Name: true, scope.Name: true}, Defs: []core.Def{next, scope, main}}
	if synchronous {
		owner := scope.Body.(*core.IteratorScope)
		yield := types.EffLabel{Unique: sup.NextUnique(), Name: types.GeneratorEffectName, Args: []types.Type{b.Int}, Suspension: true}
		traversal := types.EffLabel{Unique: sup.NextUnique(), Name: types.IteratorTraversalEffectName, Suspension: true}
		producerTy.Eff.Labels = []types.EffLabel{yield}
		consumerTy.Eff.Labels = []types.EffLabel{traversal}
		owner.Yield = core.EffectInstance{Unique: yield.Unique, Name: yield.Name, Args: yield.Args, Captures: types.ScopeCapture(owner.Scope), Control: control}
		owner.Traversal = core.EffectInstance{Unique: traversal.Unique, Name: traversal.Name, Captures: types.ScopeCapture(owner.Scope), Control: control}
		param := owner.Yield
		param.Captures = types.VarCapture(sup.FreshCapture())
		producer.EffectParams = []core.EffectInstance{param}
		producer.Body.(*core.Seq).First.(*core.Suspend).Owner = param
		consumer.Body.(*core.Let).Body = &core.VarRef{Name: "item", Local: true, Ty: maybe}
		owner.Control = types.Control{}
		scopeTy.Ret.(*types.TFun).Control = types.Control{}
		p.Defs[1].Control = types.Control{}
		p.Defs[2].Control = types.Control{}
		p.Defs[2].Body.(*core.App).Control = types.Control{}
		p.Effects = []*types.EffectInfo{
			{Unique: yield.Unique, Name: yield.Name, Params: []*types.TVar{sup.FreshRigid(types.General)}, Suspension: true},
			{Unique: traversal.Unique, Name: traversal.Name, Suspension: true},
		}
	}
	return p, b
}
