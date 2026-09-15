package eval

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/types"
	"github.com/waj/fango/runtime/fangort"
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
	ownerScope := sup.FreshScope()
	yieldOwner := core.EffectInstance{Unique: label.Unique, Name: label.Name, Args: label.Args, Captures: types.ScopeCapture(ownerScope), Control: types.Control{Transport: types.Machine}}
	yieldParam := yieldOwner
	yieldParam.Captures = types.VarCapture(sup.FreshCapture())
	yieldEffect := &types.EffectInfo{Unique: label.Unique, Name: label.Name, Params: []*types.TVar{sup.FreshRigid(types.General)}, Suspension: true}
	producerTy := &types.TFun{Arg: b.Unit, Eff: types.Row{Labels: []types.EffLabel{label}}, Ret: b.Unit}
	consumerTy := &types.TFun{Arg: iterator, Ret: b.Unit}
	actionTy := &types.TFun{Arg: b.Int, Ret: b.Unit}
	forEachTy := &types.TFun{Arg: actionTy, Ret: &types.TFun{Arg: iterator, Ret: b.Unit}}
	ownerTy := &types.TFun{Arg: producerTy, Ret: &types.TFun{Arg: consumerTy, Ret: b.Unit}}
	owner := core.Def{Name: types.GeneratorWithIteratorName, Owner: "Main", Type: ownerTy,
		Params: []string{"producer", "consumer"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture(), sup.FreshCapture()},
		Body: &core.IteratorScope{Scope: ownerScope, Yield: yieldOwner,
			Producer: &core.VarRef{Name: "producer", Local: true, Ty: producerTy},
			Consumer: &core.VarRef{Name: "consumer", Local: true, Ty: consumerTy},
			CursorTy: iterator, Ty: b.Unit,
		}}
	forEach := core.Def{Name: types.IteratorForEachName, Owner: "Main", Type: forEachTy,
		Params: []string{"action", "cursor"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture(), sup.FreshCapture()},
		Body: &core.IteratorForEach{Access: types.ExclusiveAdvance,
			Action: &core.VarRef{Name: "action", Local: true, Ty: actionTy}, Cursor: &core.VarRef{Name: "cursor", Local: true, Ty: iterator},
			Element: b.Int, Ty: b.Unit,
		}}
	producer := &core.Lambda{Param: "_", ParamCapture: sup.FreshCapture(), Ty: producerTy, EffectParams: []core.EffectInstance{yieldParam},
		Body: &core.Seq{First: &core.Suspend{Owner: yieldParam, Request: &core.VarRef{Name: "captured", Local: true, Ty: b.Int}, Ty: b.Unit},
			Then: &core.Suspend{Owner: yieldParam, Request: &core.IntLit{Val: 8, Ty: b.Int}, Ty: b.Unit}, Ty: b.Unit}}
	action := &core.Lambda{Param: "value", ParamCapture: sup.FreshCapture(), Ty: actionTy, Body: &core.UnitLit{Ty: b.Unit}}
	consumer := &core.Lambda{Param: "cursor", ParamCapture: sup.FreshCapture(), Ty: consumerTy,
		Body: &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: types.IteratorForEachName, Ty: forEachTy},
			Args: []core.Expr{action, &core.VarRef{Name: "cursor", Local: true, Ty: iterator}}, Ty: b.Unit}}
	call := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: owner.Name, Ty: ownerTy},
		Args: []core.Expr{producer, consumer}, Ty: b.Unit}
	main := core.Def{Name: "Main.main", Owner: "Main", Type: b.Unit,
		Body: &core.Let{Name: "captured", Rhs: &core.IntLit{Val: 7, Ty: b.Int}, Body: call, Ty: b.Unit}}
	p := &core.Prog{Entry: main.Name, Intrinsics: map[string]bool{types.GeneratorWithIteratorName: true, types.IteratorForEachName: true}, Defs: []core.Def{owner, forEach, main}}
	p.Effects = append(p.Effects, yieldEffect)
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
	left, right := fangort.NewYieldOwner(), fangort.NewYieldOwner()
	for _, token := range []*fangort.YieldOwner{left, right} {
		session, err := startMachine(context.Background(), mp, mp.Closures[0].Worker, []Value{int64(7), struct{}{}},
			map[int]*evidence{label.Unique: {yieldOwner: token}}, env, NewIOContext(strings.NewReader(""), io.Discard), false)
		if err != nil {
			t.Fatal(err)
		}
		ambient := &evidence{yieldOwner: fangort.NewYieldOwner()}
		session.interp.evidence = map[int]*evidence{label.Unique: ambient}
		event, err := session.Run()
		if err != nil || event.Owner != token || event.Request != int64(7) || session.interp.evidence[label.Unique] != ambient {
			t.Fatalf("yield lost lexical evidence or changed caller evidence: %+v %v", event, err)
		}
		event, err = session.Resume(struct{}{})
		if err != nil || event.Owner != token || event.Request != int64(8) {
			t.Fatalf("resumption lost lexical owner: %+v %v", event, err)
		}
		if _, err := session.Abandon(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIteratorFoldThreadsAccumulatorAcrossYields(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	body := &core.Seq{
		First: &core.Suspend{Request: &core.IntLit{Val: 2, Ty: b.Int}, Ty: b.Unit},
		Then:  &core.Suspend{Request: &core.IntLit{Val: 3, Ty: b.Int}, Ty: b.Unit},
		Ty:    b.Unit,
	}
	p := &core.Prog{Defs: []core.Def{{Name: "Main.producer", Owner: "Main", Type: b.Unit,
		Control: types.Control{Transport: types.Machine}, Body: body}}}
	mp, errs := machineir.Lower(p, b)
	if len(errs) != 0 {
		t.Fatalf("machine lowering: %v", errs)
	}
	env := NewEnv()
	env.DefineProg(p)
	cursor, err := StartMachineIterator(context.Background(), mp, "Main.producer", nil, env, NewIOContext(strings.NewReader(""), io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	step := &core.Lambda{Param: "acc", Ty: &types.TFun{Arg: b.Int, Ret: b.Int}, Body: &core.NativeCall{
		Name: "Basics.+", Module: "Basics", Args: []core.Expr{
			&core.VarRef{Name: "value", Local: true, Ty: b.Int},
			&core.VarRef{Name: "acc", Local: true, Ty: b.Int},
		}, Ty: b.Int}}
	combine := &Closure{Param: "value", Body: step, Env: &Frame{vars: map[string]Value{}}, Evidence: map[int]*evidence{}}
	frame := &Frame{vars: map[string]Value{"combine": combine, "cursor": cursor}}
	fold := &core.IteratorFold{Access: types.ExclusiveAdvance,
		Combine: &core.VarRef{Name: "combine", Local: true, Ty: &types.TFun{Arg: b.Int, Ret: step.Ty}},
		Initial: &core.IntLit{Val: 0, Ty: b.Int}, Cursor: &core.VarRef{Name: "cursor", Local: true,
			Ty: &types.TCon{Unique: sup.NextUnique(), Name: types.IteratorTypeName, Args: []types.Type{b.Int}}},
		Element: b.Int, Accumulator: b.Int, Ty: b.Int,
	}
	in := &interp{ctx: context.Background(), env: env, out: io.Discard,
		ioctx: NewIOContext(strings.NewReader(""), io.Discard), evidence: map[int]*evidence{}}
	got, err := in.evalIteratorFold(fold, frame)
	if err != nil {
		t.Fatal(err)
	}
	if got != int64(5) {
		t.Fatalf("fold result = %#v, want 5", got)
	}
	if !cursor.done {
		t.Fatal("fold did not exhaust its iterator")
	}
}
