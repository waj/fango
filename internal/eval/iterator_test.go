package eval

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/types"
)

func TestMachineIteratorSessionClosesSuspendedCleanupScope(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	resource := &core.VarRef{Name: "resource", Local: true, Ty: b.Int}
	body := &core.Let{Name: "first", Rhs: &core.Suspend{Request: resource, Ty: b.Unit}, Ty: b.Unit,
		Body: &core.Suspend{Request: resource, Ty: b.Unit}}
	bracket := &core.Bracket{Scope: sup.FreshScope(), Resource: "resource", ResourceTy: b.Int,
		Acquire: &core.IntLit{Val: 7, Ty: b.Int}, Release: &core.UnitLit{Ty: b.Unit}, Body: body, Ty: b.Unit,
		Control: types.Control{Transport: types.Machine}}
	p := &core.Prog{Defs: []core.Def{{Name: types.ScopeBracketName, Owner: "Scope", Type: b.Unit,
		Control: types.Control{Transport: types.Machine}, Body: bracket}}}
	mp, errs := machineir.Lower(p, b)
	if len(errs) != 0 {
		t.Fatalf("machine lowering: %v", errs)
	}
	env := NewEnv()
	env.DefineProg(p)
	it, err := StartMachineIterator(context.Background(), mp, types.ScopeBracketName, nil, env, NewIOContext(strings.NewReader(""), io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	value, yielded, exit, err := it.Next()
	if err != nil || !yielded || exit != nil || value != int64(7) {
		t.Fatalf("next = %#v, %v/%v/%v", value, yielded, exit, err)
	}
	if exit, err := it.Close(); err != nil || exit != nil {
		t.Fatalf("close = %#v, %v", exit, err)
	}
	if !it.session.finished || len(it.session.frames) != 0 || len(it.session.cleanups) != 0 {
		t.Fatalf("iterator retained machine state")
	}
}

func TestIteratorScopeRunsThroughInstalledMachineLowering(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	iterator := &types.TCon{Unique: sup.NextUnique(), Name: types.IteratorTypeName, Args: []types.Type{b.Int}}
	label := types.EffLabel{Unique: sup.NextUnique(), Name: types.GeneratorEffectName, Args: []types.Type{b.Int}, Suspension: true}
	producerTy := &types.TFun{Arg: b.Unit, Eff: types.Row{Labels: []types.EffLabel{label}}, Ret: b.Unit}
	consumerTy := &types.TFun{Arg: iterator, Ret: b.Unit}
	actionTy := &types.TFun{Arg: b.Int, Ret: b.Unit}
	forEachTy := &types.TFun{Arg: actionTy, Ret: &types.TFun{Arg: iterator, Ret: b.Unit}}
	ownerTy := &types.TFun{Arg: producerTy, Ret: &types.TFun{Arg: consumerTy, Ret: b.Unit}}
	owner := core.Def{Name: types.GeneratorWithIteratorName, Owner: "Main", Type: ownerTy,
		Params: []string{"producer", "consumer"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture(), sup.FreshCapture()},
		Body: &core.IteratorScope{
			Producer: &core.VarRef{Name: "producer", Local: true, Ty: producerTy},
			Consumer: &core.VarRef{Name: "consumer", Local: true, Ty: consumerTy},
			CursorTy: iterator, Ty: b.Unit,
		}}
	forEach := core.Def{Name: types.IteratorForEachName, Owner: "Main", Type: forEachTy,
		Params: []string{"action", "cursor"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture(), sup.FreshCapture()},
		Body: &core.IteratorForEach{
			Action: &core.VarRef{Name: "action", Local: true, Ty: actionTy}, Cursor: &core.VarRef{Name: "cursor", Local: true, Ty: iterator},
			Element: b.Int, Ty: b.Unit,
		}}
	producer := &core.Lambda{Param: "_", ParamCapture: sup.FreshCapture(), Ty: producerTy,
		Body: &core.Seq{First: &core.Suspend{Request: &core.VarRef{Name: "captured", Local: true, Ty: b.Int}, Ty: b.Unit},
			Then: &core.Suspend{Request: &core.IntLit{Val: 8, Ty: b.Int}, Ty: b.Unit}, Ty: b.Unit}}
	action := &core.Lambda{Param: "value", ParamCapture: sup.FreshCapture(), Ty: actionTy, Body: &core.UnitLit{Ty: b.Unit}}
	consumer := &core.Lambda{Param: "cursor", ParamCapture: sup.FreshCapture(), Ty: consumerTy,
		Body: &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: types.IteratorForEachName, Ty: forEachTy},
			Args: []core.Expr{action, &core.VarRef{Name: "cursor", Local: true, Ty: iterator}}, Ty: b.Unit}}
	call := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: owner.Name, Ty: ownerTy},
		Args: []core.Expr{producer, consumer}, Ty: b.Unit}
	main := core.Def{Name: "Main.main", Owner: "Main", Type: b.Unit,
		Body: &core.Let{Name: "captured", Rhs: &core.IntLit{Val: 7, Ty: b.Int}, Body: call, Ty: b.Unit}}
	p := &core.Prog{Entry: main.Name, Intrinsics: map[string]bool{types.GeneratorWithIteratorName: true, types.IteratorForEachName: true}, Defs: []core.Def{owner, forEach, main}}
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatalf("capture inference: %v", errs)
	}
	mp, errs := machineir.Lower(p, b)
	if len(errs) != 0 {
		t.Fatalf("machine lowering: %v", errs)
	}
	env := NewEnv()
	env.DefineProg(p)
	if err := env.DefineMachineProg(mp); err != nil {
		t.Fatal(err)
	}
	value, err := ForceIO(context.Background(), main.Name, env, NewIOContext(strings.NewReader(""), io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := value.(struct{}); !ok {
		t.Fatalf("iterator scope result = %#v, want Unit", value)
	}
}
