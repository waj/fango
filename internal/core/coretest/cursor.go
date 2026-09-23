// Package coretest supplies semantic fixtures shared by backend proof tests.
package coretest

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func CursorScope() (*core.Prog, *types.Builtins)            { return cursorScope(false) }
func SynchronousCursorScope() (*core.Prog, *types.Builtins) { return cursorScope(true) }
func cursorScope(synchronous bool) (*core.Prog, *types.Builtins) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	return cursorScopeWith(sup, b, synchronous), b
}
func SynchronousCursorScopeWith(sup *types.Supply, b *types.Builtins) *core.Prog {
	return cursorScopeWith(sup, b, true)
}

// Pause invokes the producer's typed, lexically captured pause capability.
func Pause(factory *core.Lambda, request core.Expr) core.Expr {
	pause := factory.Ty.(*types.TFun).Arg.(*types.TFun)
	return &core.App{CalleeKind: core.Value, Callee: &core.VarRef{Name: factory.Param, Local: true, Ty: pause}, Args: []core.Expr{request}, Ty: pause.Ret, Control: types.Control{Transport: types.Machine}}
}

func cursorScopeWith(sup *types.Supply, b *types.Builtins, synchronous bool) *core.Prog {
	control := types.Control{Transport: types.Machine}
	a, z := sup.FreshRigid(types.General), sup.FreshRigid(types.General)
	con := &types.TCon{Unique: sup.NextUnique(), Name: types.CoroutineStepName, Args: []types.Type{a, z}}
	suspended := &types.CtorInfo{Name: "Coroutine.Suspended", Index: 0, Fields: []types.Type{a}, Result: con}
	finished := &types.CtorInfo{Name: "Coroutine.Finished", Index: 1, Fields: []types.Type{z}, Result: con}
	closed := &types.CtorInfo{Name: "Coroutine.Closed", Index: 2, Result: con}
	adt := &types.ADTInfo{Con: con, Params: []*types.TVar{a, z}, Ctors: []*types.CtorInfo{suspended, finished, closed}}
	step := &types.TCon{Unique: con.Unique, Name: con.Name, Args: []types.Type{b.Int, b.Unit}}
	cursor := &types.TCon{Unique: sup.NextUnique(), Name: types.CoroutineTypeName, Args: []types.Type{b.Int, b.Unit, b.Unit, b.Unit}}
	pauseTy := &types.TFun{Arg: b.Int, Ret: b.Unit, Control: control}
	bodyTy := &types.TFun{Arg: b.Unit, Ret: b.Unit, Control: control}
	producerTy := &types.TFun{Arg: pauseTy, Ret: bodyTy}
	consumerTy := &types.TFun{Arg: cursor, Ret: step, Control: control}
	advanceTy := &types.TFun{Arg: cursor, Ret: &types.TFun{Arg: b.Unit, Ret: step, Control: control}}
	next := core.Def{Name: types.CoroutineAdvanceName, Owner: "Coroutine", Type: advanceTy, Params: []string{"cursor", "reply"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture(), sup.FreshCapture()}, Control: control,
		Body: &core.CoroutineAdvance{Row: &core.RowArgument{}, Cursor: &core.VarRef{Name: "cursor", Local: true, Ty: cursor}, Reply: &core.VarRef{Name: "reply", Local: true, Ty: b.Unit}, Result: adt, Access: types.ExclusiveAdvance, Ty: step}}
	boundaryControl := control
	if synchronous {
		boundaryControl = types.Control{}
	}
	scopeTy := &types.TFun{Arg: producerTy, Ret: &types.TFun{Arg: consumerTy, Ret: step, Control: boundaryControl}}
	owner := sup.FreshScope()
	suspension := core.EffectInstance{Unique: sup.NextUnique(), Name: types.CoroutineSuspensionName, Captures: types.ScopeCapture(owner), Control: control}
	drive := core.EffectInstance{Unique: sup.NextUnique(), Name: types.CoroutineDriveName, Captures: types.ScopeCapture(owner), Control: control}
	scope := core.Def{Name: types.CoroutineWithName, Owner: "Coroutine", Type: scopeTy, Params: []string{"producer", "consumer"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture(), sup.FreshCapture()}, Control: boundaryControl,
		Body: &core.CoroutineScope{Scope: owner, Yield: suspension, Traversal: drive, Producer: &core.VarRef{Name: "producer", Local: true, Ty: producerTy}, Consumer: &core.VarRef{Name: "consumer", Local: true, Ty: consumerTy}, CursorTy: cursor, Ty: step, Control: boundaryControl}}
	producer := &core.Lambda{Param: "pause", ParamCapture: sup.FreshCapture(), Ty: producerTy}
	producer.Body = &core.Lambda{Param: "unit", ParamCapture: sup.FreshCapture(), Ty: bodyTy, Body: &core.Seq{First: Pause(producer, &core.IntLit{Val: 42, Ty: b.Int}), Then: &core.UnitLit{Ty: b.Unit}, Ty: b.Unit}}
	pull := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: next.Name, Ty: advanceTy}, Args: []core.Expr{&core.VarRef{Name: "it", Local: true, Ty: cursor}, &core.UnitLit{Ty: b.Unit}}, Ty: step, Control: control}
	var answer core.Expr = &core.VarRef{Name: "item", Local: true, Ty: step}
	if !synchronous {
		answer = &core.Seq{First: &core.Suspend{Request: &core.IntLit{Val: 99, Ty: b.Int}, Ty: b.Unit}, Then: answer, Ty: step}
	}
	consumer := &core.Lambda{Param: "it", ParamCapture: sup.FreshCapture(), Ty: consumerTy, Body: &core.Let{Name: "item", Rhs: pull, Body: answer, Ty: step}}
	main := core.Def{Name: "Main.main", Owner: "Main", Type: step, Control: boundaryControl, Body: &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: scope.Name, Ty: scopeTy}, Args: []core.Expr{producer, consumer}, Ty: step, Control: boundaryControl}}
	return &core.Prog{Entry: main.Name, ADTs: []*types.ADTInfo{adt}, Intrinsics: map[string]bool{next.Name: true, scope.Name: true}, Defs: []core.Def{next, scope, main}, Effects: []*types.EffectInfo{{Unique: suspension.Unique, Name: suspension.Name, Suspension: true}, {Unique: drive.Unique, Name: drive.Name, Suspension: true}}}
}
