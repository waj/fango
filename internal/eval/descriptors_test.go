package eval

import (
	"context"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
	"github.com/waj/fango/runtime/fangort"
	"testing"
)

func TestGenericFailureDescriptorsSurviveReturnedClosure(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	a, c := sup.FreshRigid(types.General), sup.FreshRigid(types.General)
	effect := &types.EffectInfo{Unique: sup.NextUnique(), Name: "Fail.Fail", Params: []*types.TVar{a}}
	op := &types.EffectOp{Owner: effect, Name: "Fail.fail", Abort: true, ParamTypes: []types.Type{a}, ResultType: b.Unit}
	effect.Ops = []*types.EffectOp{op}
	ev := func(arg types.Type) core.EffectInstance {
		return core.EffectInstance{Unique: effect.Unique, Name: effect.Name, Args: []types.Type{arg}}
	}
	raiseTy := &types.TFun{Arg: a, Ret: b.Unit}
	raise := core.Def{Name: "raise", Type: raiseTy, TyParams: []*types.TVar{a}, Params: []string{"error"}, Body: &core.ControlExit{Effect: ev(a), Op: op, Payload: []core.Expr{&core.VarRef{Name: "error", Local: true, Ty: a}}, Ty: b.Unit}}
	callbackTy := &types.TFun{Arg: b.Unit, Ret: b.Unit}
	body := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: raise.Name, Ty: &types.TFun{Arg: c, Ret: b.Unit}}, TyArgs: []types.Type{c}, EvidenceArgs: []core.EffectInstance{ev(c)}, Args: []core.Expr{&core.VarRef{Name: "value", Local: true, Ty: c}}, Ty: b.Unit}
	makeTy := &types.TFun{Arg: c, Ret: callbackTy}
	make := core.Def{Name: "make", Type: makeTy, TyParams: []*types.TVar{c}, Params: []string{"value"}, Body: &core.Lambda{Param: "unit", Body: body, Ty: callbackTy}}
	env := NewEnv()
	env.DefineProg(&core.Prog{Defs: []core.Def{raise, make}})
	in := &interp{ctx: context.Background(), env: env, evidence: map[int]*evidence{effect.Unique: {}}}
	factory := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: make.Name, Ty: makeTy}, TyArgs: []types.Type{b.Int}, Args: []core.Expr{&core.IntLit{Val: 42, Ty: b.Int}}, Ty: callbackTy}
	closure, err := in.eval(factory, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := &core.App{CalleeKind: core.Value, Callee: &core.VarRef{Name: "saved", Local: true, Ty: callbackTy}, Args: []core.Expr{&core.UnitLit{Ty: b.Unit}}, EvidenceArgs: []core.EffectInstance{ev(b.Int)}, Ty: b.Unit}
	result, err := in.eval(call, &Frame{vars: map[string]Value{"saved": closure}})
	if err != nil {
		t.Fatal(err)
	}
	exit, ok := result.(*ExitRequest)
	if !ok {
		t.Fatalf("result %T", result)
	}
	expected, err := in.typeDescriptor(b.Int, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := fangort.FailureArgument[Value](0, snapshotFailure(exit), expected)
	if !ok || got != int64(42) {
		t.Fatalf("generic payload: %#v %v", got, ok)
	}
	wrong, _ := in.typeDescriptor(b.String, nil)
	if _, ok := fangort.FailureArgument[Value](0, snapshotFailure(exit), wrong); ok {
		t.Fatal("generic argument lost its type")
	}
}

func TestFailureDescriptorsDistinguishNominalGenerations(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	env := NewEnv()
	old := &types.TCon{Unique: sup.NextUnique(), Name: "Private.Error"}
	next := &types.TCon{Unique: sup.NextUnique(), Name: old.Name}
	for _, con := range []*types.TCon{old, next} {
		env.adts[con.Unique] = &types.ADTInfo{Con: con, Ctors: []*types.CtorInfo{{Name: "Private.Error", Result: con, Fields: []types.Type{b.Int}}}}
	}
	in := &interp{env: env}
	before, _ := in.typeDescriptor(old, nil)
	after, _ := in.typeDescriptor(next, nil)
	value := &CtorVal{Ctor: env.adts[old.Unique].Ctors[0], Fields: []Value{int64(7)}}
	snapshot := snapshotFailure(&ExitRequest{Payload: []Value{value}, PayloadTypes: []*fangort.TypeDescriptor{before}})
	if got, ok := fangort.FailureArgument[Value](0, snapshot, before); !ok || got != value {
		t.Fatal("private generation payload rejected")
	}
	if _, ok := fangort.FailureArgument[Value](0, snapshot, after); ok {
		t.Fatal("new generation matched an old payload")
	}
}
