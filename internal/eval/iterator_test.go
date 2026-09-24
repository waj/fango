package eval

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/core/coretest"
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
	p := &core.Prog{Defs: []core.Def{{Name: types.ScopeBracketName, Owner: "Runtime.Scope", Type: b.Unit,
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
	p, b := coretest.SynchronousCursorScope()
	main := &p.Defs[2]
	call := main.Body.(*core.App)
	producer := call.Args[0].(*core.Lambda)
	body := producer.Body.(*core.Lambda)
	body.Body = &core.Seq{First: coretest.Pause(producer, &core.VarRef{Name: "captured", Local: true, Ty: b.Int}), Then: coretest.Pause(producer, &core.IntLit{Val: 8, Ty: b.Int}), Ty: b.Unit}
	main.Body = &core.Let{Name: "captured", Rhs: &core.IntLit{Val: 7, Ty: b.Int}, Body: call, Ty: main.Type}
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
	if item, ok := value.(*CtorVal); !ok || item.Ctor.Name != "Runtime.Coroutine.Suspended" || item.Fields[0] != int64(7) {
		t.Fatalf("iterator scope result = %#v, want Just 7", value)
	}
	producerWorker := ""
	for _, closure := range mp.Closures {
		if closure.Expr == body {
			producerWorker = closure.Worker
		}
	}
	if producerWorker == "" {
		t.Fatal("producer closure was not lowered")
	}
	left, right := fangort.NewYieldOwner(), fangort.NewYieldOwner()
	for _, token := range []*fangort.YieldOwner{left, right} {
		pause := &Closure{pauseOwner: token, control: types.Control{Transport: types.Machine}}
		var args []Value
		for _, closure := range mp.Closures {
			if closure.Expr != body {
				continue
			}
			for _, capture := range closure.Captures {
				if capture.Name == "captured" {
					args = append(args, int64(7))
				} else {
					args = append(args, pause)
				}
			}
		}
		args = append(args, struct{}{})
		session, err := startMachine(context.Background(), mp, producerWorker, args,
			nil, env, NewIOContext(strings.NewReader(""), io.Discard), false)
		if err != nil {
			t.Fatal(err)
		}
		ambient := &evidence{yieldOwner: fangort.NewYieldOwner()}
		session.interp.evidence = map[int]*evidence{999: ambient}
		event, err := session.Run()
		if err != nil || event.Owner != token || event.Request != int64(7) || session.interp.evidence[999] != ambient {
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
