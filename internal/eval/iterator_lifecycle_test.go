package eval

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/core/coretest"
	"github.com/waj/fango/internal/types"
)

// stageIterator installs the same checked advancement and owner boundary as
// source traversal. Its consumer pulls once, then scope exit closes production.
func stageIterator(t *testing.T, sup *types.Supply, b *types.Builtins, body core.Expr, defs []core.Def, natives map[string]*types.NativeInfo) (core.Expr, *Env) {
	t.Helper()
	p := coretest.SynchronousCursorScopeWith(sup, b)
	call := p.Defs[2].Body.(*core.App)
	producer := call.Args[0].(*core.Lambda)
	producer.Body.(*core.Lambda).Body = core.Rewrite(body, func(t types.Type) types.Type { return t }, func(e core.Expr) core.Expr {
		if suspension, ok := e.(*core.Suspend); ok {
			return coretest.Pause(producer, suspension.Request)
		}
		return e
	})
	p.Defs = append(p.Defs, defs...)
	p.Natives = natives
	mp := lowerMachineTest(t, p, b)
	env := NewEnv()
	env.DefineProg(p)
	if err := env.DefineMachineProg(mp); err != nil {
		t.Fatal(err)
	}
	return call, env
}

func TestIteratorPreservesCompileTimeNativeRestrictions(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	native := &types.NativeInfo{Name: "Test.sidecar", Module: "Test", Arity: 1,
		Scheme: types.Scheme{Body: &types.TFun{Arg: b.Unit, Ret: b.Unit}}}
	body := &core.NativeCall{Name: native.Name, Module: native.Module, Args: []core.Expr{&core.UnitLit{Ty: b.Unit}}, Ty: b.Unit}
	scope, env := stageIterator(t, sup, b, body, nil, map[string]*types.NativeInfo{native.Name: native})
	_, err := EvalCompileTime(context.Background(), scope, env, DefaultBudget)
	var unsafe *UnsafeNativeError
	if !errors.As(err, &unsafe) || unsafe.Name != native.Name {
		t.Fatalf("stage producer error = %v, want UnsafeNativeError for %s", err, native.Name)
	}
}

func TestIteratorSharesCompileTimeBudgetAcrossMachineTailCalls(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	control := types.Control{Transport: types.Machine}
	ty := &types.TFun{Arg: b.Unit, Ret: b.Unit, Control: control}
	call := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: "spin", Ty: ty},
		Args: []core.Expr{&core.UnitLit{Ty: b.Unit}}, Ty: b.Unit, Control: control}
	spin := core.Def{Name: "spin", Type: ty, Params: []string{"_"},
		ParamCaptures: []types.CaptureVar{sup.FreshCapture()}, Control: control, Body: call}
	scope, env := stageIterator(t, sup, b, call, []core.Def{spin}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := EvalCompileTime(ctx, scope, env, pollEvery)
	if !errors.Is(err, ErrStepBudget) {
		t.Fatalf("stage producer error = %v, want step budget", err)
	}
}

func TestIteratorSharesBudgetAcrossRepeatedTraversals(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	scope, env := stageIterator(t, sup, b, &core.Suspend{Request: &core.IntLit{Val: 7, Ty: b.Int}, Ty: b.Unit}, nil, nil)
	ioctx := NewIOContext(strings.NewReader(""), io.Discard)
	in := &interp{ctx: context.Background(), env: env, out: io.Discard, ioctx: ioctx,
		evidence: map[int]*evidence{}, compileTime: true, budget: pollEvery}
	for range pollEvery {
		_, err := in.eval(scope, nil)
		if errors.Is(err, ErrStepBudget) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("reopening traversal reset the stage budget")
}

func TestMachineErrorClosesCursorAndPreservesCleanupFailures(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	p := &core.Prog{Defs: []core.Def{{Name: "producer", Type: b.Unit,
		Control: types.Control{Transport: types.Machine}, Body: &core.Suspend{Request: &core.IntLit{Val: 7, Ty: b.Int}, Ty: b.Unit}}}}
	mp := lowerMachineTest(t, p, b)
	session := startMachineTest(t, p, mp, "producer", nil)
	it := &MachineIteratorSession{session: session}
	_, yielded, _, err := it.Next()
	if err != nil || !yielded {
		t.Fatalf("initial pull: yielded %v, error %v", yielded, err)
	}
	innerErr, outerErr := errors.New("inner evaluator error"), errors.New("outer evaluator error")
	innerExit, outerExit := &ExitRequest{Payload: []Value{"inner"}}, &ExitRequest{Payload: []Value{"outer"}}
	var order []string
	session.cleanups = append(session.cleanups,
		machineCleanupEntry{sync: func() (*ExitRequest, error) { order = append(order, "outer"); return outerExit, outerErr }},
		machineCleanupEntry{sync: func() (*ExitRequest, error) { order = append(order, "inner"); return innerExit, innerErr }})
	// Force the next dispatch to exceed the caller's budget before executing
	// another producer instruction. Cleanup still drains every acquired scope.
	session.interp.steps, session.interp.budget = pollEvery-1, 1
	_, yielded, exit, err := it.Next()
	if yielded || !errors.Is(err, ErrStepBudget) || !errors.Is(err, innerErr) || !errors.Is(err, outerErr) {
		t.Fatalf("failed pull: yielded %v, exit %#v, error %v", yielded, exit, err)
	}
	if exit == nil || exit.Payload[0] != "inner" || len(exit.Suppressed) != 1 || exit.Suppressed[0] != outerExit {
		t.Fatalf("cleanup failures = %#v", exit)
	}
	if !reflect.DeepEqual(order, []string{"inner", "outer"}) {
		t.Fatalf("cleanup order = %v", order)
	}
	if !session.finished || len(session.frames)+len(session.cleanups)+len(session.handlers)+len(session.states) != 0 || session.waiting != nil {
		t.Fatal("failed producer retained live execution state")
	}
	for range 3 {
		value, yielded, exit, err := it.Next()
		if value != nil || yielded || exit != nil || err != nil {
			t.Fatalf("read after failure: %#v, %v, %#v, %v", value, yielded, exit, err)
		}
	}
	if exit, err := it.Close(); exit != nil || err != nil || len(order) != 2 {
		t.Fatalf("close after failure: %#v, %v, order %v", exit, err, order)
	}
}

func TestMachineExitRetainsPrimaryWhenCleanupEvaluationFails(t *testing.T) {
	primary := &ExitRequest{Payload: []Value{"body"}}
	secondary := &ExitRequest{Payload: []Value{"cleanup"}}
	errCleanup := errors.New("cleanup evaluator error")
	session := &MachineSession{cleanups: []machineCleanupEntry{
		{sync: func() (*ExitRequest, error) { return secondary, nil }},
		{sync: func() (*ExitRequest, error) { return nil, errCleanup }},
	}}
	event, err := session.finishExit(primary)
	if !event.Done || !errors.Is(err, errCleanup) || event.Exit == nil || event.Exit.Payload[0] != "body" || len(event.Exit.Suppressed) != 1 || event.Exit.Suppressed[0] != secondary {
		t.Fatalf("completion = %#v, %v", event, err)
	}
	if len(primary.Suppressed) != 0 {
		t.Fatal("cleanup mutated the original failure")
	}
}

func TestMachineRunRestoresCallerEvidence(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	p := &core.Prog{Defs: []core.Def{{Name: "producer", Type: b.Unit,
		Control: types.Control{Transport: types.Machine}, Body: &core.Suspend{Request: &core.IntLit{Val: 7, Ty: b.Int}, Ty: b.Unit}}}}
	mp := lowerMachineTest(t, p, b)
	session := startMachineTest(t, p, mp, "producer", nil)
	caller, producer := &evidence{}, &evidence{}
	session.interp.evidence = map[int]*evidence{42: caller}
	session.frames[0].evidence = map[int]*evidence{42: producer}
	if _, err := session.Run(); err != nil {
		t.Fatal(err)
	}
	if session.interp.evidence[42] != caller {
		t.Fatal("yield replaced the caller's evidence with producer evidence")
	}
	if _, err := session.Resume(struct{}{}); err != nil {
		t.Fatal(err)
	}
	if session.interp.evidence[42] != caller {
		t.Fatal("normal completion replaced the caller's evidence")
	}
}
