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

func TestMachineSessionSuspendsAndResumesWithoutRecursiveFrames(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	x := &core.VarRef{Name: "x", Local: true, Ty: b.Int}
	body := &core.Let{Name: "x", Rhs: machineSuspend(b, machineInt(b, 1)), Ty: b.Int,
		Body: &core.Let{Name: "ignored", Rhs: machineSuspend(b, x), Ty: b.Int, Body: x}}
	p := &core.Prog{Defs: []core.Def{{Name: "main", Type: b.Int,
		Control: types.Control{Transport: types.Machine}, Body: body}}}
	mp := lowerMachineTest(t, p, b)
	session := startMachineTest(t, p, mp, "main", nil)

	event, err := session.Run()
	if err != nil || event.Done || event.Request != int64(1) {
		t.Fatalf("first event = %#v, %v; want request 1", event, err)
	}
	event, err = session.Resume(int64(41))
	if err != nil || event.Done || event.Request != int64(41) {
		t.Fatalf("second event = %#v, %v; want request 41", event, err)
	}
	event, err = session.Resume(int64(99))
	if err != nil || !event.Done || event.Exit != nil || event.Value != int64(41) {
		t.Fatalf("completion = %#v, %v; want value 41", event, err)
	}
	if stats := session.Stats(); stats.MaxDepth != 1 {
		t.Fatalf("maximum frame depth = %d, want 1", stats.MaxDepth)
	}
}

func TestMachineRetainsOnlyCapturedLocalsInReturnedDirectCallback(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	fn := &types.TFun{Arg: b.Unit, Ret: b.Int}
	callback := &core.Lambda{Param: "_", ParamCapture: sup.FreshCapture(), Ty: fn,
		Body: &core.VarRef{Name: "kept", Local: true, Ty: b.Int}}
	body := &core.Let{Name: "kept", Rhs: &core.IntLit{Val: 42, Ty: b.Int}, Ty: fn,
		Body: &core.Let{Name: "discarded", Rhs: &core.StringLit{Val: strings.Repeat("x", 1024), Ty: b.String}, Ty: fn,
			Body: &core.Let{Name: "callback", Rhs: callback, Ty: fn,
				Body: &core.Seq{First: &core.Suspend{Request: &core.IntLit{Val: 1, Ty: b.Int}, Ty: b.Unit},
					Then: &core.VarRef{Name: "callback", Local: true, Ty: fn}, Ty: fn}}}}
	p := &core.Prog{Defs: []core.Def{{Name: "producer", Type: fn, Control: types.Control{Transport: types.Machine}, Body: body}}}
	mp := lowerMachineTest(t, p, b)
	session := startMachineTest(t, p, mp, "producer", nil)
	if _, err := session.Run(); err != nil {
		t.Fatal(err)
	}
	event, err := session.Resume(struct{}{})
	if err != nil || !event.Done {
		t.Fatalf("completion = %#v, %v", event, err)
	}
	closure, ok := event.Value.(*Closure)
	if !ok || closure.Env == nil || len(closure.Env.vars) != 1 || closure.Env.parent != nil {
		t.Fatalf("returned callback retained more than its captured local: %#v", event.Value)
	}
	value, err := session.interp.callClosure(closure, struct{}{})
	if err != nil || value != int64(42) {
		t.Fatalf("callback after producer completion = %#v, %v", value, err)
	}
}

func TestMachineSessionUsesExplicitCallerFrame(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	machineControl := types.Control{Transport: types.Machine}
	helperTy := &types.TFun{Arg: b.Int, Eff: types.Row{}, Ret: b.Int, Control: machineControl}
	helper := core.Def{Name: "helper", Type: helperTy, Params: []string{"n"},
		ParamCaptures: []types.CaptureVar{sup.FreshCapture()}, Control: machineControl,
		Body: machineSuspend(b, &core.VarRef{Name: "n", Local: true, Ty: b.Int})}
	call := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: "helper", Ty: helperTy},
		Args: []core.Expr{machineInt(b, 7)}, Ty: b.Int, Control: machineControl}
	x := &core.VarRef{Name: "x", Local: true, Ty: b.Int}
	mainBody := &core.Let{Name: "x", Rhs: call, Ty: b.Int,
		Body: &core.Let{Name: "ignored", Rhs: machineSuspend(b, x), Ty: b.Int, Body: x}}
	main := core.Def{Name: "main", Type: b.Int, Control: machineControl, Body: mainBody}
	p := &core.Prog{Defs: []core.Def{helper, main}}
	mp := lowerMachineTest(t, p, b)
	session := startMachineTest(t, p, mp, "main", nil)

	event, err := session.Run()
	if err != nil || event.Request != int64(7) {
		t.Fatalf("callee suspension = %#v, %v", event, err)
	}
	event, err = session.Resume(int64(11))
	if err != nil || event.Request != int64(11) {
		t.Fatalf("caller suspension = %#v, %v", event, err)
	}
	event, err = session.Resume(int64(0))
	if err != nil || !event.Done || event.Value != int64(11) {
		t.Fatalf("completion = %#v, %v", event, err)
	}
	if stats := session.Stats(); stats.MaxDepth != 2 {
		t.Fatalf("maximum frame depth = %d, want 2", stats.MaxDepth)
	}
}

func TestMachineSessionEnforcesPrivateDriverProtocol(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	p := &core.Prog{Defs: []core.Def{{Name: "main", Type: b.Int,
		Control: types.Control{Transport: types.Machine}, Body: machineSuspend(b, machineInt(b, 1))}}}
	mp := lowerMachineTest(t, p, b)
	session := startMachineTest(t, p, mp, "main", nil)
	if _, err := session.Resume(int64(1)); err == nil || !strings.Contains(err.Error(), "not suspended") {
		t.Fatalf("Resume before Run error = %v", err)
	}
	if _, err := session.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Run(); err == nil || !strings.Contains(err.Error(), "must be resumed") {
		t.Fatalf("second Run error = %v", err)
	}
	if _, err := session.Resume(int64(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Run(); err == nil || !strings.Contains(err.Error(), "already completed") {
		t.Fatalf("Run after completion error = %v", err)
	}
}

func TestMachineSessionSuspendsInsideDecisionTree(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	option := &types.TCon{Unique: sup.NextUnique(), Name: "Main.Option"}
	none := &types.CtorInfo{Name: "Main.None", Index: 0, Result: option}
	some := &types.CtorInfo{Name: "Main.Some", Index: 1, Fields: []types.Type{b.Int}, Result: option}
	adt := &types.ADTInfo{Con: option, Ctors: []*types.CtorInfo{none, some}}
	someValue := &core.App{CalleeKind: core.Ctor, Callee: &core.VarRef{Name: some.Name, Ty: some.ValueType()},
		Args: []core.Expr{machineInt(b, 7)}, Ctor: some, Ty: option}
	value := &core.VarRef{Name: "value", Local: true, Ty: b.Int}
	body := &core.Case{Scrut: someValue, Bind: "_scrut", Ty: b.Int, Tree: &core.SwitchCtor{
		Scrut: "_scrut", ADT: adt, Cases: []core.CtorCase{
			{Ctor: none, Tree: &core.Leaf{Body: machineSuspend(b, machineInt(b, 0))}},
			{Ctor: some, Binds: []string{"value"}, Tree: &core.Leaf{Body: &core.Let{
				Name: "ignored", Rhs: machineSuspend(b, value), Ty: b.Int, Body: value,
			}}},
		},
	}}
	p := &core.Prog{ADTs: []*types.ADTInfo{adt}, Defs: []core.Def{{Name: "main", Type: b.Int,
		Control: types.Control{Transport: types.Machine}, Body: body}}}
	mp := lowerMachineTest(t, p, b)
	session := startMachineTest(t, p, mp, "main", nil)
	event, err := session.Run()
	if err != nil || event.Done || event.Request != int64(7) {
		t.Fatalf("decision-tree suspension = %#v, %v", event, err)
	}
	event, err = session.Resume(int64(99))
	if err != nil || !event.Done || event.Value != int64(7) {
		t.Fatalf("decision-tree completion = %#v, %v", event, err)
	}
	if got := localNamesForEval(mp.Workers[0].Frame); len(got) != 1 || got[0] != "value" {
		t.Fatalf("decision-tree frame = %v, want [value]", got)
	}
}

func TestMachineSessionTraversesDeepTreeWithExplicitFrames(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	control := types.Control{Transport: types.Machine}
	treeTy := &types.TCon{Unique: sup.NextUnique(), Name: "Main.Tree"}
	empty := &types.CtorInfo{Name: "Main.Empty", Index: 0, Result: treeTy}
	node := &types.CtorInfo{Name: "Main.Node", Index: 1, Fields: []types.Type{treeTy, b.Int, treeTy}, Result: treeTy}
	adt := &types.ADTInfo{Con: treeTy, Ctors: []*types.CtorInfo{empty, node}}
	walkTy := &types.TFun{Arg: treeTy, Ret: b.Unit, Control: control}
	callWalk := func(name string) core.Expr {
		return &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: "Main.walk", Ty: walkTy},
			Args: []core.Expr{&core.VarRef{Name: name, Local: true, Ty: treeTy}}, Ty: b.Unit, Control: control}
	}
	walkBody := &core.Case{Scrut: &core.VarRef{Name: "tree", Local: true, Ty: treeTy}, Bind: "_tree", Ty: b.Unit,
		Tree: &core.SwitchCtor{Scrut: "_tree", ADT: adt, Cases: []core.CtorCase{
			{Ctor: empty, Tree: &core.Leaf{Body: &core.UnitLit{Ty: b.Unit}}},
			{Ctor: node, Binds: []string{"left", "value", "right"}, Tree: &core.Leaf{Body: &core.Seq{
				First: callWalk("left"), Ty: b.Unit, Then: &core.Seq{
					First: &core.Suspend{Request: &core.VarRef{Name: "value", Local: true, Ty: b.Int}, Ty: b.Unit},
					Then:  callWalk("right"), Ty: b.Unit,
				},
			}}},
		}}}
	walk := core.Def{Name: "Main.walk", Owner: "Main", Type: walkTy, Params: []string{"tree"},
		ParamCaptures: []types.CaptureVar{sup.FreshCapture()}, Control: control, Body: walkBody}
	p := &core.Prog{ADTs: []*types.ADTInfo{adt}, Defs: []core.Def{walk}}
	mp := lowerMachineTest(t, p, b)

	const depth = 2000
	emptyValue := &CtorVal{Ctor: empty}
	var tree Value = emptyValue
	for i := depth - 1; i >= 0; i-- {
		tree = &CtorVal{Ctor: node, Fields: []Value{tree, int64(i), emptyValue}}
	}
	session := startMachineTest(t, p, mp, "Main.walk", []Value{tree})
	event, err := session.Run()
	for want := 0; want < depth; want++ {
		if err != nil || event.Done || event.Request != int64(depth-1-want) {
			t.Fatalf("yield %d = %#v, %v", want, event, err)
		}
		event, err = session.Resume(struct{}{})
	}
	if err != nil || !event.Done || event.Exit != nil {
		t.Fatalf("tree completion = %#v, %v", event, err)
	}
	if got := session.Stats().MaxDepth; got != depth+1 {
		t.Fatalf("maximum tree frame depth = %d, want %d", got, depth+1)
	}
	if got := session.Stats().MaxFrameCap; got < depth+1 {
		t.Fatalf("maximum frame capacity = %d, want at least %d", got, depth+1)
	}
}

func TestMachineSessionKeepsCleanupPendingAcrossSuspension(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	resource := &core.VarRef{Name: "resource", Local: true, Ty: b.Int}
	body := &core.Let{Name: "ignored", Rhs: &core.Suspend{Request: resource, Ty: b.Unit}, Ty: b.Int, Body: resource}
	bracket := &core.Bracket{Scope: sup.FreshScope(), Resource: "resource", ResourceTy: b.Int,
		Acquire: machineInt(b, 1), Release: &core.UnitLit{Ty: b.Unit}, Body: body, Ty: b.Int,
		Control: types.Control{Transport: types.Machine}}
	p := &core.Prog{Defs: []core.Def{{Name: types.ScopeBracketName, Owner: "Scope", Type: b.Int,
		Control: types.Control{Transport: types.Machine}, Body: bracket}}}
	mp := lowerMachineTest(t, p, b)
	session := startMachineTest(t, p, mp, types.ScopeBracketName, nil)
	event, err := session.Run()
	if err != nil || event.Done || event.Request != int64(1) {
		t.Fatalf("cleanup-body suspension = %#v, %v", event, err)
	}
	if len(session.cleanups) != 1 {
		t.Fatalf("pending cleanups = %d, want 1", len(session.cleanups))
	}
	event, err = session.Resume(struct{}{})
	if err != nil || !event.Done || event.Value != int64(1) {
		t.Fatalf("cleanup completion = %#v, %v", event, err)
	}
	if len(session.cleanups) != 0 || session.Stats().MaxCleanups != 1 {
		t.Fatalf("cleanup stack/stats = %d/%#v", len(session.cleanups), session.Stats())
	}
	abandoned := startMachineTest(t, p, mp, types.ScopeBracketName, nil)
	if event, err := abandoned.Run(); err != nil || event.Done {
		t.Fatalf("abandon setup = %#v, %v", event, err)
	}
	if exit, err := abandoned.Abandon(); err != nil || exit != nil {
		t.Fatalf("abandon = %#v, %v", exit, err)
	}
	if len(abandoned.cleanups) != 0 || len(abandoned.frames) != 0 || !abandoned.finished {
		t.Fatalf("abandoned session retained state: cleanups=%d frames=%d finished=%v", len(abandoned.cleanups), len(abandoned.frames), abandoned.finished)
	}
}

func TestMachineSessionRunsResumptiveHandlerClause(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	eff := &types.EffectInfo{Unique: sup.NextUnique(), Name: "Main.Ask"}
	op := &types.EffectOp{Owner: eff, Index: 0, Name: "Main.ask", Arity: 1,
		ParamTypes: []types.Type{b.Unit}, ResultType: b.Int}
	eff.Ops = []*types.EffectOp{op}
	scope := sup.FreshScope()
	ev := core.EffectInstance{Unique: eff.Unique, Name: eff.Name, Captures: types.ScopeCapture(scope),
		Control: types.Control{Transport: types.Machine}}
	clause := &core.Let{Name: "pause", Rhs: &core.Suspend{Request: &core.IntLit{Val: 5, Ty: b.Int}, Ty: b.Unit}, Ty: b.Int,
		Body: &core.ResumeTail{Owner: 1, Value: &core.IntLit{Val: 40, Ty: b.Int}, ClauseResult: b.Int}}
	perform := func() core.Expr {
		return &core.Perform{Op: op, Effect: ev, Args: []core.Expr{&core.UnitLit{Ty: b.Unit}}, Ty: b.Int,
			Control: types.Control{Transport: types.Machine}}
	}
	h := &core.Handle{Body: &core.Let{Name: "first", Rhs: perform(), Body: perform(), Ty: b.Int}, Effect: ev, Scope: scope, Ty: b.Int,
		Control: types.Control{Transport: types.Machine}, Clauses: []core.HandlerClause{{Op: op, ResumeID: 1,
			Params: []string{"()"}, ParamTypes: []types.Type{b.Unit}, ResultType: b.Int, Body: clause}}}
	p := &core.Prog{Effects: []*types.EffectInfo{eff}, Defs: []core.Def{{Name: "Main.main", Owner: "Main", Type: b.Int,
		Control: types.Control{Transport: types.Machine}, Body: h}}}
	mp := lowerMachineTest(t, p, b)
	session := startMachineTest(t, p, mp, "Main.main", nil)
	event, err := session.Run()
	if err != nil || event.Done || event.Request != int64(5) {
		t.Fatalf("handler suspension = %#v, %v", event, err)
	}
	event, err = session.Resume(struct{}{})
	if err != nil || event.Done || event.Request != int64(5) {
		t.Fatalf("second handler suspension = %#v, %v", event, err)
	}
	event, err = session.Resume(struct{}{})
	if err != nil || !event.Done || event.Value != int64(40) {
		t.Fatalf("handler completion = %#v, %v", event, err)
	}
}

func TestMachineSessionRunsStatefulHandler(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	eff := &types.EffectInfo{Unique: sup.NextUnique(), Name: "Main.Cell"}
	op := &types.EffectOp{Owner: eff, Index: 0, Name: "Main.get", Arity: 1,
		ParamTypes: []types.Type{b.Unit}, ResultType: b.Int}
	eff.Ops = []*types.EffectOp{op}
	scope := sup.FreshScope()
	ev := core.EffectInstance{Unique: eff.Unique, Name: eff.Name, Captures: types.ScopeCapture(scope),
		Control: types.Control{Transport: types.Machine}}
	h := &core.Handle{Body: &core.Perform{Op: op, Effect: ev, Args: []core.Expr{&core.UnitLit{Ty: b.Unit}}, Ty: b.Int,
		Control: types.Control{Transport: types.Machine}}, Effect: ev, Scope: scope, Scoped: true, Ty: b.Int,
		Control: types.Control{Transport: types.Machine}, State: &core.HandlerState{Name: "current", Initial: &core.IntLit{Val: 1, Ty: b.Int}, Ty: b.Int},
		Clauses: []core.HandlerClause{{Op: op, ResumeID: 1, Params: []string{"()"}, ParamTypes: []types.Type{b.Unit}, ResultType: b.Int,
			Body: &core.ResumeTail{Owner: 1, Value: &core.IntLit{Val: 40, Ty: b.Int}, NextState: &core.IntLit{Val: 41, Ty: b.Int}, ClauseResult: b.Int}}},
		Return: &core.ReturnClause{Param: "_", Body: &core.VarRef{Name: "current", Local: true, Ty: b.Int}}}
	p := &core.Prog{Effects: []*types.EffectInfo{eff}, Defs: []core.Def{{Name: "Main.main", Owner: "Main", Type: b.Int,
		Control: types.Control{Transport: types.Machine}, Body: h}}}
	mp := lowerMachineTest(t, p, b)
	session := startMachineTest(t, p, mp, "Main.main", nil)
	event, err := session.Run()
	if err != nil || !event.Done || event.Value != int64(41) || session.Stats().MaxStates != 1 {
		t.Fatalf("stateful handler = %#v, %v, stats %#v", event, err, session.Stats())
	}
}

func TestMachineSessionRoutesAbortToClause(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	eff := &types.EffectInfo{Unique: sup.NextUnique(), Name: "Main.Fail"}
	op := &types.EffectOp{Owner: eff, Index: 0, Name: "Main.fail", Arity: 1, ParamTypes: []types.Type{b.Int}, ResultType: b.Int, Abort: true}
	eff.Ops = []*types.EffectOp{op}
	scope := sup.FreshScope()
	ev := core.EffectInstance{Unique: eff.Unique, Name: eff.Name, Captures: types.ScopeCapture(scope), Control: types.Control{Transport: types.Exit}}
	body := &core.ControlExit{Effect: ev, Op: op, Payload: []core.Expr{&core.IntLit{Val: 9, Ty: b.Int}}, Ty: b.Int}
	clauseBody := &core.Let{Name: "pause", Rhs: &core.Suspend{Request: &core.IntLit{Val: 5, Ty: b.Int}, Ty: b.Unit}, Ty: b.Bool,
		Body: &core.BoolLit{Val: true, Ty: b.Bool}}
	h := &core.Handle{Body: body, Effect: ev, Scope: scope, Ty: b.Bool, Control: types.Control{Transport: types.Machine},
		Clauses: []core.HandlerClause{{Op: op, Params: []string{"value"}, ParamTypes: []types.Type{b.Int}, ResultType: b.Int, Body: clauseBody}},
		Return:  &core.ReturnClause{Param: "normal", Body: &core.BoolLit{Val: false, Ty: b.Bool}}}
	p := &core.Prog{Effects: []*types.EffectInfo{eff}, Defs: []core.Def{{Name: "Main.main", Owner: "Main", Type: b.Bool, Control: types.Control{Transport: types.Machine}, Body: h}}}
	mp := lowerMachineTest(t, p, b)
	session := startMachineTest(t, p, mp, "Main.main", nil)
	event, err := session.Run()
	if err != nil || event.Done || event.Request != int64(5) {
		t.Fatalf("abort clause suspension = %#v, %v", event, err)
	}
	event, err = session.Resume(struct{}{})
	if err != nil || !event.Done || event.Exit != nil || event.Value != true {
		t.Fatalf("abort clause completion = %#v, %v", event, err)
	}
}

func TestMachineSessionTailCallPreservesHandlerReturnOwnership(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	control := types.Control{Transport: types.Machine}
	eff := &types.EffectInfo{Unique: sup.NextUnique(), Name: "Main.Ask"}
	op := &types.EffectOp{Owner: eff, Index: 0, Name: "Main.ask", Arity: 1, ParamTypes: []types.Type{b.Unit}, ResultType: b.Int}
	eff.Ops = []*types.EffectOp{op}
	scope := sup.FreshScope()
	ev := core.EffectInstance{Unique: eff.Unique, Name: eff.Name, Captures: types.ScopeCapture(scope), Control: control}
	helperControl := types.Control{Transport: types.Machine, Polymorphic: true}
	helperTy := &types.TFun{Arg: b.Unit, Eff: types.Row{Labels: []types.EffLabel{{Unique: eff.Unique, Name: eff.Name}}}, Ret: b.Int, Control: helperControl}
	evidenceCapture := sup.FreshCapture()
	helperEvidence := ev
	helperEvidence.Captures = types.VarCapture(evidenceCapture)
	helperEvidence.Control = helperControl
	helper := core.Def{Name: "Main.helper", Owner: "Main", Type: helperTy, Params: []string{"unit"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture()}, EffectParams: []core.EffectInstance{helperEvidence}, Control: helperControl,
		Body: &core.Perform{Op: op, Effect: helperEvidence, Args: []core.Expr{&core.UnitLit{Ty: b.Unit}}, Ty: b.Int, Control: helperControl}}
	call := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: helper.Name, Ty: helperTy}, Args: []core.Expr{&core.UnitLit{Ty: b.Unit}}, EvidenceArgs: []core.EffectInstance{ev}, Ty: b.Int, Control: control}
	h := &core.Handle{Body: call, Effect: ev, Scope: scope, Ty: b.Int, Control: control,
		Clauses: []core.HandlerClause{{Op: op, ResumeID: 1, Params: []string{"()"}, ParamTypes: []types.Type{b.Unit}, ResultType: b.Int,
			Body: &core.ResumeTail{Owner: 1, Value: &core.IntLit{Val: 17, Ty: b.Int}, ClauseResult: b.Int}}}}
	p := &core.Prog{Effects: []*types.EffectInfo{eff}, Defs: []core.Def{helper, {Name: "Main.main", Owner: "Main", Type: b.Int, Control: control, Body: h}}}
	mp := lowerMachineTest(t, p, b)
	session := startMachineTest(t, p, mp, "Main.main", nil)
	event, err := session.Run()
	if err != nil || !event.Done || event.Value != int64(17) {
		t.Fatalf("completion = %#v, %v", event, err)
	}
	if len(session.handlers) != 0 {
		t.Fatalf("handler activations after completion = %d, want 0", len(session.handlers))
	}
}

func TestMachineSessionRunsNestedHandlersWithLexicalEvidence(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	control := types.Control{Transport: types.Machine}
	makeEffect := func(name string) (*types.EffectInfo, *types.EffectOp, core.EffectInstance, types.ScopeID) {
		eff := &types.EffectInfo{Unique: sup.NextUnique(), Name: name}
		op := &types.EffectOp{Owner: eff, Index: 0, Name: name + ".ask", Arity: 1, ParamTypes: []types.Type{b.Unit}, ResultType: b.Int}
		eff.Ops = []*types.EffectOp{op}
		scope := sup.FreshScope()
		return eff, op, core.EffectInstance{Unique: eff.Unique, Name: eff.Name, Captures: types.ScopeCapture(scope), Control: control}, scope
	}
	outerEff, outerOp, outerEv, outerScope := makeEffect("Main.Outer")
	innerEff, innerOp, innerEv, innerScope := makeEffect("Main.Inner")
	perform := func(op *types.EffectOp, ev core.EffectInstance) core.Expr {
		return &core.Perform{Op: op, Effect: ev, Args: []core.Expr{&core.UnitLit{Ty: b.Unit}}, Ty: b.Int, Control: control}
	}
	clause := func(op *types.EffectOp, request, result int64, resume types.ResumeID) core.HandlerClause {
		return core.HandlerClause{Op: op, ResumeID: resume, Params: []string{"()"}, ParamTypes: []types.Type{b.Unit}, ResultType: b.Int,
			Body: &core.Let{Name: "pause", Rhs: &core.Suspend{Request: &core.IntLit{Val: request, Ty: b.Int}, Ty: b.Unit}, Ty: b.Int,
				Body: &core.ResumeTail{Owner: resume, Value: &core.IntLit{Val: result, Ty: b.Int}, ClauseResult: b.Int}}}
	}
	inner := &core.Handle{Body: &core.Let{Name: "innerResult", Rhs: perform(innerOp, innerEv), Body: perform(outerOp, outerEv), Ty: b.Int},
		Effect: innerEv, Scope: innerScope, Ty: b.Int, Control: control, Clauses: []core.HandlerClause{clause(innerOp, 10, 11, 1)}}
	outer := &core.Handle{Body: inner, Effect: outerEv, Scope: outerScope, Ty: b.Int, Control: control, Clauses: []core.HandlerClause{clause(outerOp, 20, 22, 2)}}
	p := &core.Prog{Effects: []*types.EffectInfo{outerEff, innerEff}, Defs: []core.Def{{Name: "Main.main", Owner: "Main", Type: b.Int, Control: control, Body: outer}}}
	session := startMachineTest(t, p, lowerMachineTest(t, p, b), "Main.main", nil)
	event, err := session.Run()
	if err != nil || event.Request != int64(10) {
		t.Fatalf("inner event = %#v, %v", event, err)
	}
	event, err = session.Resume(struct{}{})
	if err != nil || event.Request != int64(20) {
		t.Fatalf("outer event = %#v, %v", event, err)
	}
	event, err = session.Resume(struct{}{})
	if err != nil || !event.Done || event.Value != int64(22) {
		t.Fatalf("completion = %#v, %v", event, err)
	}
}

func lowerMachineTest(t *testing.T, p *core.Prog, b *types.Builtins) *machineir.Prog {
	t.Helper()
	mp, errs := machineir.Lower(p, b)
	if len(errs) != 0 {
		t.Fatalf("machine lowering: %v", errs)
	}
	return mp
}

func startMachineTest(t *testing.T, p *core.Prog, mp *machineir.Prog, entry string, args []Value) *MachineSession {
	t.Helper()
	env := NewEnv()
	env.DefineProg(p)
	if err := env.DefineMachineProg(mp); err != nil {
		t.Fatal(err)
	}
	session, err := StartMachine(context.Background(), mp, entry, args, env, NewIOContext(strings.NewReader(""), io.Discard))
	if err != nil {
		t.Fatalf("StartMachine: %v", err)
	}
	return session
}

func machineInt(b *types.Builtins, value int64) core.Expr {
	return &core.IntLit{Val: value, Ty: b.Int}
}

func machineSuspend(b *types.Builtins, request core.Expr) core.Expr {
	return &core.Suspend{Request: request, Ty: b.Int}
}

func localNamesForEval(locals []machineir.Local) []string {
	out := make([]string, len(locals))
	for i, local := range locals {
		out[i] = local.Name
	}
	return out
}
