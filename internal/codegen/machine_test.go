package codegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/runtimefiles"
	"github.com/waj/fango/internal/types"
)

func TestMachineEmitterBuildsIterativeTypedFrame(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	x := &core.VarRef{Name: "x", Local: true, Ty: b.Int}
	body := &core.Let{Name: "x", Rhs: &core.Suspend{Request: &core.IntLit{Val: 1, Ty: b.Int}, Ty: b.Int}, Ty: b.Int,
		Body: &core.Let{Name: "ignored", Rhs: &core.Suspend{Request: x, Ty: b.Int}, Ty: b.Int, Body: x}}
	option := &types.TCon{Unique: sup.NextUnique(), Name: "Main.Option"}
	none := &types.CtorInfo{Name: "Main.None", Index: 0, Result: option}
	some := &types.CtorInfo{Name: "Main.Some", Index: 1, Fields: []types.Type{b.Int}, Result: option}
	adt := &types.ADTInfo{Con: option, Ctors: []*types.CtorInfo{none, some}}
	someValue := &core.App{CalleeKind: core.Ctor, Callee: &core.VarRef{Name: some.Name, Ty: some.ValueType()},
		Args: []core.Expr{&core.IntLit{Val: 7, Ty: b.Int}}, Ctor: some, Ty: option}
	value := &core.VarRef{Name: "value", Local: true, Ty: b.Int}
	matchBody := &core.Case{Scrut: someValue, Bind: "_scrut", Ty: b.Int, Tree: &core.SwitchCtor{
		Scrut: "_scrut", ADT: adt, Cases: []core.CtorCase{
			{Ctor: none, Tree: &core.Leaf{Body: &core.IntLit{Val: 0, Ty: b.Int}}},
			{Ctor: some, Binds: []string{"value"}, Tree: &core.Leaf{Body: &core.Let{Name: "ignored2",
				Rhs: &core.Suspend{Request: value, Ty: b.Int}, Ty: b.Int, Body: value}}},
		},
	}}
	resource := &core.VarRef{Name: "resource", Local: true, Ty: b.Int}
	bracketBody := &core.Let{Name: "ignored3", Rhs: &core.Suspend{Request: resource, Ty: b.Unit}, Ty: b.Int, Body: resource}
	bracket := &core.Bracket{Scope: sup.FreshScope(), Resource: "resource", ResourceTy: b.Int,
		Acquire: &core.IntLit{Val: 3, Ty: b.Int}, Release: &core.UnitLit{Ty: b.Unit}, Body: bracketBody, Ty: b.Int,
		Control: types.Control{Transport: types.Machine}}
	p := &core.Prog{Entry: "Main.main", ADTs: []*types.ADTInfo{adt}, Defs: []core.Def{
		{Name: "Main.main", Owner: "Main", Type: b.Int, Control: types.Control{Transport: types.Machine}, Body: body},
		{Name: "Main.match", Owner: "Main", Type: b.Int, Control: types.Control{Transport: types.Machine}, Body: matchBody},
		{Name: types.ScopeBracketName, Owner: "Scope", Type: b.Int, Control: types.Control{Transport: types.Machine}, Body: bracket},
	}}
	mp, errs := machineir.Lower(p, b)
	if len(errs) != 0 {
		t.Fatalf("machine lowering: %v", errs)
	}
	files, err := EmitMachineProject(p, mp, b, []Unit{{Name: "Scope"}, {Name: "Main", Imports: []string{"Scope"}, Entry: true}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2", len(files))
	}
	totalBytes := 0
	for _, file := range files {
		totalBytes += len(file.Data)
	}
	t.Logf("generated Machine source size: %d bytes", totalBytes)
	got := string(files[0].Data)
	for _, want := range []string{
		"type machineFrame_Main_dot_main struct",
		"func MachineFrame_Main_dot_main() fangort.MachineFrame",
		"func (f *machineFrame_Main_dot_main) Step(m *fangort.Machine) fangort.MachineStep",
		"for {",
		"switch f.PC",
		"Kind: fangort.MachineSuspend",
		"m.TakeResult().(int64)",
		"func (f *machineFrame_Main_dot_main) Clear()",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated Machine Go missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, ".Step(m)") || strings.Contains(got, "go func") || strings.Contains(got, "chan ") {
		t.Fatalf("generated Machine Go contains recursive/concurrent dispatch:\n%s", got)
	}
}

func TestOrdinaryEmitterDoesNotAcquireMachineRuntime(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	p := &core.Prog{Entry: "Main.main", Defs: []core.Def{{Name: "Main.main", Owner: "Main", Type: b.Int,
		Body: &core.IntLit{Val: 1, Ty: b.Int}}}}
	files, err := EmitProject(p, b, []Unit{{Name: "Main", Entry: true}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(files[0].Data); strings.Contains(got, "Machine") {
		t.Fatalf("direct output acquired Machine support:\n%s", got)
	}
}

func TestIteratorOwnerRootsMachineProducerInDirectCaller(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	iterator := &types.TCon{Unique: sup.NextUnique(), Name: types.IteratorTypeName, Args: []types.Type{b.Int}}
	label := types.EffLabel{Unique: sup.NextUnique(), Name: types.GeneratorEffectName, Args: []types.Type{b.Int}, Suspension: true}
	producerTy := &types.TFun{Arg: b.Unit, Eff: types.Row{Labels: []types.EffLabel{label}}, Ret: b.Unit}
	consumerTy := &types.TFun{Arg: iterator, Ret: b.Unit}
	actionTy := &types.TFun{Arg: b.Int, Ret: b.Unit}
	forEachTy := &types.TFun{Arg: actionTy, Ret: &types.TFun{Arg: iterator, Ret: b.Unit}}
	stepTy := &types.TFun{Arg: b.Int, Ret: b.Int}
	combineTy := &types.TFun{Arg: b.Int, Ret: stepTy}
	foldTy := &types.TFun{Arg: combineTy, Ret: &types.TFun{Arg: b.Int, Ret: &types.TFun{Arg: iterator, Ret: b.Int}}}
	ownerTy := &types.TFun{Arg: producerTy, Ret: &types.TFun{Arg: consumerTy, Ret: b.Unit}}
	owner := core.Def{Name: types.GeneratorWithIteratorName, Owner: "Main", Type: ownerTy,
		Params: []string{"producer", "consumer"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture(), sup.FreshCapture()},
		Body: &core.IteratorScope{Scope: sup.FreshScope(),
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
	fold := core.Def{Name: types.IteratorFoldName, Owner: "Main", Type: foldTy,
		Params: []string{"combine", "initial", "cursor"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture(), sup.FreshCapture(), sup.FreshCapture()},
		Body: &core.IteratorFold{Access: types.ExclusiveAdvance,
			Combine: &core.VarRef{Name: "combine", Local: true, Ty: combineTy}, Initial: &core.VarRef{Name: "initial", Local: true, Ty: b.Int},
			Cursor: &core.VarRef{Name: "cursor", Local: true, Ty: iterator}, Element: b.Int, Accumulator: b.Int, Ty: b.Int,
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
	p := &core.Prog{Entry: main.Name, Intrinsics: map[string]bool{
		types.GeneratorWithIteratorName: true, types.IteratorForEachName: true, types.IteratorFoldName: true,
	}, Defs: []core.Def{owner, forEach, fold, main}}
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatalf("capture inference: %v", errs)
	}
	mp, errs := machineir.Lower(p, b)
	if len(errs) != 0 {
		t.Fatalf("machine lowering: %v", errs)
	}
	files, err := EmitMachineProject(p, mp, b, []Unit{{Name: "Main", Entry: true}}, false)
	if err != nil {
		t.Fatal(err)
	}
	generated := string(files[0].Data)
	for _, want := range []string{"fangort.StartMachineIterator", "fangort.PullMachineIterator", "V_Iterator_dot_fold", "t_iteratorAccumulator", "MachineFrame_Main_dot_main_machine_lambda", "v_captured int64 = 7"} {
		if !strings.Contains(generated, want) {
			t.Fatalf("generated iterator owner missing %q:\n%s", want, generated)
		}
	}

	runtimeSources, err := runtimefiles.Packages("fangort")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func(path string, data []byte) {
		t.Helper()
		path = filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte("module fangobuild\n\ngo 1.26\n"))
	for _, file := range files {
		write(file.Path, file.Data)
	}
	for _, file := range runtimeSources {
		write(file.Path, file.Data)
	}
	write("iterator_owner_test.go", []byte("package main\n\nimport \"testing\"\n\nfunc TestIteratorOwner(t *testing.T) { main() }\n"))
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(dir, "gocache"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated iterator-owner project failed: %v\n%s\n%s", err, output, generated)
	}
}

func TestGeneratedMachineFrameExecutes(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	poly := types.Control{Polymorphic: true}
	polyCallback := &types.TFun{Arg: b.Int, Ret: b.Int, Control: poly}
	applyCallback := core.Def{Name: "Main.applyCallback", Owner: "Main",
		Type:   &types.TFun{Arg: polyCallback, Ret: b.Int, Control: types.Control{Transport: types.Machine}},
		Params: []string{"action"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture()}, Control: types.Control{Transport: types.Machine},
		Body: &core.App{CalleeKind: core.Value, Callee: &core.VarRef{Name: "action", Local: true, Ty: polyCallback},
			Args: []core.Expr{&core.IntLit{Val: 17, Ty: b.Int}}, Ty: b.Int, Control: poly}}
	x := &core.VarRef{Name: "x", Local: true, Ty: b.Int}
	body := &core.Let{Name: "x", Rhs: &core.Suspend{Request: &core.IntLit{Val: 1, Ty: b.Int}, Ty: b.Int}, Ty: b.Int,
		Body: &core.Let{Name: "ignored", Rhs: &core.Suspend{Request: x, Ty: b.Int}, Ty: b.Int, Body: x}}
	option := &types.TCon{Unique: sup.NextUnique(), Name: "Main.Option"}
	none := &types.CtorInfo{Name: "Main.None", Index: 0, Result: option}
	some := &types.CtorInfo{Name: "Main.Some", Index: 1, Fields: []types.Type{b.Int}, Result: option}
	adt := &types.ADTInfo{Con: option, Ctors: []*types.CtorInfo{none, some}}
	someValue := &core.App{CalleeKind: core.Ctor, Callee: &core.VarRef{Name: some.Name, Ty: some.ValueType()},
		Args: []core.Expr{&core.IntLit{Val: 7, Ty: b.Int}}, Ctor: some, Ty: option}
	value := &core.VarRef{Name: "value", Local: true, Ty: b.Int}
	matchBody := &core.Case{Scrut: someValue, Bind: "_scrut", Ty: b.Int, Tree: &core.SwitchCtor{
		Scrut: "_scrut", ADT: adt, Cases: []core.CtorCase{
			{Ctor: none, Tree: &core.Leaf{Body: &core.IntLit{Val: 0, Ty: b.Int}}},
			{Ctor: some, Binds: []string{"value"}, Tree: &core.Leaf{Body: &core.Let{Name: "ignored2",
				Rhs: &core.Suspend{Request: value, Ty: b.Int}, Ty: b.Int, Body: value}}},
		},
	}}
	resource := &core.VarRef{Name: "resource", Local: true, Ty: b.Int}
	bracketBody := &core.Let{Name: "ignored3", Rhs: &core.Suspend{Request: resource, Ty: b.Unit}, Ty: b.Int, Body: resource}
	bracket := &core.Bracket{Scope: sup.FreshScope(), Resource: "resource", ResourceTy: b.Int,
		Acquire: &core.IntLit{Val: 3, Ty: b.Int}, Release: &core.UnitLit{Ty: b.Unit}, Body: bracketBody, Ty: b.Int,
		Control: types.Control{Transport: types.Machine}}
	callbackTy := &types.TFun{Arg: b.Int, Ret: b.Int, Control: types.Control{Transport: types.Machine}}
	callbackLambda := &core.Lambda{Param: "ignoredArg", ParamCapture: sup.FreshCapture(), Ty: callbackTy,
		Body: &core.Suspend{Request: &core.VarRef{Name: "captured", Local: true, Ty: b.Int}, Ty: b.Int}}
	callbackBody := &core.Let{Name: "captured", Rhs: &core.IntLit{Val: 8, Ty: b.Int}, Ty: b.Int,
		Body: &core.Let{Name: "callback", Rhs: callbackLambda, Ty: b.Int,
			Body: &core.App{CalleeKind: core.Value, Callee: &core.VarRef{Name: "callback", Local: true, Ty: callbackTy},
				Args: []core.Expr{&core.IntLit{Val: 0, Ty: b.Int}}, Ty: b.Int, Control: types.Control{Transport: types.Machine}}}}
	genericVar := sup.FreshVar(types.General)
	genericVar.Rigid = true
	genericTy := &types.TFun{Arg: genericVar, Ret: genericVar, Control: types.Control{Transport: types.Machine}}
	generic := core.Def{Name: "Main.generic", Owner: "Main", Type: genericTy, TyParams: []*types.TVar{genericVar},
		Params: []string{"value"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture()}, Control: types.Control{Transport: types.Machine},
		Body: &core.Suspend{Request: &core.VarRef{Name: "value", Local: true, Ty: genericVar}, Ty: genericVar}}
	genericCall := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: generic.Name, Ty: genericTy, TyArgs: []types.Type{b.Int}},
		TyArgs: []types.Type{b.Int}, Args: []core.Expr{&core.IntLit{Val: 9, Ty: b.Int}}, Ty: b.Int, Control: types.Control{Transport: types.Machine}}
	eff := &types.EffectInfo{Unique: sup.NextUnique(), Name: "Main.Ask"}
	op := &types.EffectOp{Owner: eff, Index: 0, Name: "Main.ask", Arity: 1, ParamTypes: []types.Type{b.Unit}, ResultType: b.Int}
	eff.Ops = []*types.EffectOp{op}
	handlerScope := sup.FreshScope()
	handlerEvidence := core.EffectInstance{Unique: eff.Unique, Name: eff.Name, Captures: types.ScopeCapture(handlerScope),
		Control: types.Control{Transport: types.Machine}}
	handlerClause := &core.Let{Name: "handlerPause", Rhs: &core.Suspend{Request: &core.IntLit{Val: 10, Ty: b.Int}, Ty: b.Unit}, Ty: b.Int,
		Body: &core.ResumeTail{Owner: 1, Value: &core.IntLit{Val: 44, Ty: b.Int}, ClauseResult: b.Int}}
	perform := func() core.Expr {
		return &core.Perform{Op: op, Effect: handlerEvidence, Args: []core.Expr{&core.UnitLit{Ty: b.Unit}}, Ty: b.Int,
			Control: types.Control{Transport: types.Machine}}
	}
	handler := &core.Handle{Body: &core.Let{Name: "first", Rhs: perform(), Body: perform(), Ty: b.Int}, Effect: handlerEvidence, Scope: handlerScope, Ty: b.Int,
		Control: types.Control{Transport: types.Machine}, Clauses: []core.HandlerClause{{Op: op, ResumeID: 1,
			Params: []string{"()"}, ParamTypes: []types.Type{b.Unit}, ResultType: b.Int, Body: handlerClause}},
		Return: &core.ReturnClause{Param: "handled", Body: &core.Let{Name: "returnPause", Rhs: &core.Suspend{Request: &core.IntLit{Val: 12, Ty: b.Int}, Ty: b.Unit}, Ty: b.Int, Body: &core.VarRef{Name: "handled", Local: true, Ty: b.Int}}}}
	fail := &types.EffectInfo{Unique: sup.NextUnique(), Name: "Main.Fail"}
	failOp := &types.EffectOp{Owner: fail, Index: 0, Name: "Main.fail", Arity: 1, ParamTypes: []types.Type{b.Int}, ResultType: b.Int, Abort: true}
	fail.Ops = []*types.EffectOp{failOp}
	failScope := sup.FreshScope()
	failEvidence := core.EffectInstance{Unique: fail.Unique, Name: fail.Name, Captures: types.ScopeCapture(failScope), Control: types.Control{Transport: types.Exit}}
	abort := &core.Handle{Body: &core.ControlExit{Effect: failEvidence, Op: failOp, Payload: []core.Expr{&core.IntLit{Val: 12, Ty: b.Int}}, Ty: b.Int}, Effect: failEvidence, Scope: failScope, Ty: b.Bool, Control: types.Control{Transport: types.Machine}, Clauses: []core.HandlerClause{{Op: failOp, Params: []string{"n"}, ParamTypes: []types.Type{b.Int}, ResultType: b.Int, Body: &core.Let{Name: "pause", Rhs: &core.Suspend{Request: &core.IntLit{Val: 11, Ty: b.Int}, Ty: b.Unit}, Ty: b.Bool, Body: &core.BoolLit{Val: true, Ty: b.Bool}}}}, Return: &core.ReturnClause{Param: "normal", Body: &core.BoolLit{Val: false, Ty: b.Bool}}}
	cell := &types.EffectInfo{Unique: sup.NextUnique(), Name: "Main.Cell", Scoped: true}
	get := &types.EffectOp{Owner: cell, Index: 0, Name: "Main.get", Arity: 1, ParamTypes: []types.Type{b.Unit}, ResultType: b.Int}
	cell.Ops = []*types.EffectOp{get}
	cellScope := sup.FreshScope()
	cellEvidence := core.EffectInstance{Unique: cell.Unique, Name: cell.Name, Captures: types.ScopeCapture(cellScope), Control: types.Control{Transport: types.Machine}}
	stateful := &core.Handle{Body: &core.Perform{Op: get, Effect: cellEvidence, Args: []core.Expr{&core.UnitLit{Ty: b.Unit}}, Ty: b.Int, Control: types.Control{Transport: types.Machine}},
		Effect: cellEvidence, Scope: cellScope, Scoped: true, Ty: b.Int, Control: types.Control{Transport: types.Machine},
		State: &core.HandlerState{Name: "current", Initial: &core.IntLit{Val: 1, Ty: b.Int}, Ty: b.Int},
		Clauses: []core.HandlerClause{{Op: get, ResumeID: 2, Params: []string{"()"}, ParamTypes: []types.Type{b.Unit}, ResultType: b.Int,
			Body: &core.ResumeTail{Owner: 2, Value: &core.IntLit{Val: 40, Ty: b.Int}, NextState: &core.IntLit{Val: 41, Ty: b.Int}, ClauseResult: b.Int}}},
		Return: &core.ReturnClause{Param: "_", Body: &core.VarRef{Name: "current", Local: true, Ty: b.Int}}}
	p := &core.Prog{Entry: "Main.main", ADTs: []*types.ADTInfo{adt}, Effects: []*types.EffectInfo{eff, fail, cell}, Defs: []core.Def{
		{Name: "Main.main", Owner: "Main", Type: b.Int, Control: types.Control{Transport: types.Machine}, Body: body},
		{Name: "Main.match", Owner: "Main", Type: b.Int, Control: types.Control{Transport: types.Machine}, Body: matchBody},
		{Name: "Main.callback", Owner: "Main", Type: b.Int, Control: types.Control{Transport: types.Machine}, Body: callbackBody},
		applyCallback,
		generic,
		{Name: "Main.genericCall", Owner: "Main", Type: b.Int, Control: types.Control{Transport: types.Machine}, Body: genericCall},
		{Name: "Main.handler", Owner: "Main", Type: b.Int, Control: types.Control{Transport: types.Machine}, Body: handler},
		{Name: "Main.abort", Owner: "Main", Type: b.Bool, Control: types.Control{Transport: types.Machine}, Body: abort},
		{Name: "Main.stateful", Owner: "Main", Type: b.Int, Control: types.Control{Transport: types.Machine}, Body: stateful},
		{Name: types.ScopeBracketName, Owner: "Scope", Type: b.Int, Control: types.Control{Transport: types.Machine}, Body: bracket},
	}}
	mp, errs := machineir.Lower(p, b)
	if len(errs) != 0 {
		t.Fatalf("machine lowering: %v", errs)
	}
	files, err := EmitMachineProject(p, mp, b, []Unit{{Name: "Scope"}, {Name: "Main", Imports: []string{"Scope"}, Entry: true}}, false)
	if err != nil {
		t.Fatal(err)
	}
	runtimeSources, err := runtimefiles.Packages("fangort")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func(path string, data []byte) {
		t.Helper()
		path = filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte("module fangobuild\n\ngo 1.26\n"))
	for _, file := range files {
		write(file.Path, file.Data)
	}
	for _, file := range runtimeSources {
		write(file.Path, file.Data)
	}
	write("machine_test.go", []byte(`package main

import (
    "testing"
    "fangobuild/fangort"
    m_Scope "fangobuild/modules/Scope"
)

func TestFixture(t *testing.T) {
    m := fangort.StartMachine(MachineFrame_Main_dot_main())
    event, err := m.Run()
    if err != nil || event.Done || event.Request != int64(1) { t.Fatalf("first: %#v %v", event, err) }
    event, err = m.Resume(int64(41))
    if err != nil || event.Done || event.Request != int64(41) { t.Fatalf("second: %#v %v", event, err) }
    event, err = m.Resume(int64(99))
    if err != nil || !event.Done || event.Value != int64(41) { t.Fatalf("done: %#v %v", event, err) }

    match := fangort.StartMachine(MachineFrame_Main_dot_match())
    event, err = match.Run()
    if err != nil || event.Done || event.Request != int64(7) { t.Fatalf("match: %#v %v", event, err) }
    event, err = match.Resume(int64(99))
    if err != nil || !event.Done || event.Value != int64(7) { t.Fatalf("match done: %#v %v", event, err) }

    scoped := fangort.StartMachine(m_Scope.MachineFrame_Scope_dot_bracket())
    event, err = scoped.Run()
    if err != nil || event.Done || event.Request != int64(3) || scoped.Stats().MaxCleanups != 1 { t.Fatalf("scope: %#v %v", event, err) }
    event, err = scoped.Resume(fangort.UnitValue)
    if err != nil || !event.Done || event.Value != int64(3) { t.Fatalf("scope done: %#v %v", event, err) }

    iterator := fangort.StartMachineIterator(m_Scope.MachineFrame_Scope_dot_bracket())
    yieldedValue, yielded, iteratorExit, err := iterator.Next()
    if err != nil || !yielded || iteratorExit != nil || yieldedValue != int64(3) { t.Fatalf("iterator: %#v %v/%v/%v", yieldedValue, yielded, iteratorExit, err) }
    if iterator.Stats().MaxCleanups != 1 { t.Fatalf("iterator cleanup stats: %#v", iterator.Stats()) }
    if iteratorExit, err = iterator.Close(); err != nil || iteratorExit != nil { t.Fatalf("iterator close: %#v %v", iteratorExit, err) }

    callback := fangort.StartMachine(MachineFrame_Main_dot_callback())
    event, err = callback.Run()
    if err != nil || event.Done || event.Request != int64(8) { t.Fatalf("callback: %#v %v", event, err) }
    event, err = callback.Resume(int64(42))
    if err != nil || !event.Done || event.Value != int64(42) { t.Fatalf("callback done: %#v %v", event, err) }

    polymorphic := fangort.StartMachine(MachineFrame_Main_dot_applyCallback(func(value int64) fangort.MachineFrame {
        return MachineFrame_Main_dot_generic[int64](value)
    }))
    event, err = polymorphic.Run()
    if err != nil || event.Done || event.Request != int64(17) { t.Fatalf("polymorphic callback: %#v %v", event, err) }
    event, err = polymorphic.Resume(int64(71))
    if err != nil || !event.Done || event.Value != int64(71) { t.Fatalf("polymorphic callback done: %#v %v", event, err) }

    generic := fangort.StartMachine(MachineFrame_Main_dot_genericCall())
    event, err = generic.Run()
    if err != nil || event.Done || event.Request != int64(9) { t.Fatalf("generic: %#v %v", event, err) }
    event, err = generic.Resume(int64(43))
    if err != nil || !event.Done || event.Value != int64(43) { t.Fatalf("generic done: %#v %v", event, err) }

    handler := fangort.StartMachine(MachineFrame_Main_dot_handler())
    event, err = handler.Run()
    if err != nil || event.Done || event.Request != int64(10) { t.Fatalf("handler: %#v %v", event, err) }
    event, err = handler.Resume(fangort.UnitValue)
    if err != nil || event.Done || event.Request != int64(10) { t.Fatalf("handler second: %#v %v", event, err) }
    event, err = handler.Resume(fangort.UnitValue)
    if err != nil || event.Done || event.Request != int64(12) { t.Fatalf("handler return: %#v %v", event, err) }
    event, err = handler.Resume(fangort.UnitValue)
    if err != nil || !event.Done || event.Value != int64(44) { t.Fatalf("handler done: %#v %v", event, err) }

    abort := fangort.StartMachine(MachineFrame_Main_dot_abort())
    event, err = abort.Run()
    if err != nil || event.Done || event.Request != int64(11) { t.Fatalf("abort: %#v %v", event, err) }
    event, err = abort.Resume(fangort.UnitValue)
    if err != nil || !event.Done || event.Exit != nil || event.Value != true { t.Fatalf("abort done: %#v %v", event, err) }

    stateful := fangort.StartMachine(MachineFrame_Main_dot_stateful())
    event, err = stateful.Run()
    if err != nil || !event.Done || event.Value != int64(41) || stateful.Stats().MaxStates != 1 { t.Fatalf("stateful: %#v %v %#v", event, err, stateful.Stats()) }
}
`))
	cmd := exec.Command("go", "test", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(dir, "gocache"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated Machine project failed: %v\n%s", err, output)
	}
}

func TestMachineFramesCrossModuleThroughExportedConstructors(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	control := types.Control{Transport: types.Machine}
	callbackTy := &types.TFun{Arg: b.Int, Ret: b.Int, Control: control}
	invokeTy := &types.TFun{Arg: callbackTy, Ret: b.Int, Control: control}
	invoke := core.Def{Name: "Dep.invoke", Owner: "Dep", Type: invokeTy, Params: []string{"callback"},
		ParamCaptures: []types.CaptureVar{sup.FreshCapture()}, Control: control,
		Body: &core.App{CalleeKind: core.Value, Callee: &core.VarRef{Name: "callback", Local: true, Ty: callbackTy},
			Args: []core.Expr{&core.IntLit{Val: 7, Ty: b.Int}}, Ty: b.Int, Control: control}}
	callback := &core.Lambda{Param: "n", ParamCapture: sup.FreshCapture(), Ty: callbackTy,
		Body: &core.Suspend{Request: &core.VarRef{Name: "n", Local: true, Ty: b.Int}, Ty: b.Int}}
	call := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: "Dep.invoke", Ty: invokeTy},
		Args: []core.Expr{&core.VarRef{Name: "callback", Local: true, Ty: callbackTy}}, Ty: b.Int, Control: control}
	main := core.Def{Name: "Main.main", Owner: "Main", Type: b.Int, Control: control,
		Body: &core.Let{Name: "callback", Rhs: callback, Body: call, Ty: b.Int}}
	p := &core.Prog{Entry: "Main.main", Defs: []core.Def{invoke, main}}
	mp, errs := machineir.Lower(p, b)
	if len(errs) != 0 {
		t.Fatalf("machine lowering: %v", errs)
	}
	units := []Unit{{Name: "Dep"}, {Name: "Main", Imports: []string{"Dep"}, Entry: true}}
	files, err := EmitMachineProject(p, mp, b, units, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2", len(files))
	}
	var dep, mainGo string
	for _, file := range files {
		switch file.Path {
		case "modules/Dep/module.go":
			dep = string(file.Data)
		case "main.go":
			mainGo = string(file.Data)
		}
	}
	if !strings.Contains(dep, "func MachineFrame_Dep_dot_invoke(") {
		t.Fatalf("dependency does not export its frame constructor:\n%s", dep)
	}
	if !strings.Contains(mainGo, `m_Dep "fangobuild/modules/Dep"`) ||
		!strings.Contains(mainGo, "m_Dep.MachineFrame_Dep_dot_invoke(") {
		t.Fatalf("entry does not call the dependency-owned constructor:\n%s", mainGo)
	}
	runtimeSources, err := runtimefiles.Packages("fangort")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func(path string, data []byte) {
		path = filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte("module fangobuild\n\ngo 1.26\n"))
	for _, file := range files {
		write(file.Path, file.Data)
	}
	for _, file := range runtimeSources {
		write(file.Path, file.Data)
	}
	write("machine_cross_module_test.go", []byte(`package main
import (
    "testing"
    "fangobuild/fangort"
)
func TestCrossModuleCallback(t *testing.T) {
    m := fangort.StartMachine(MachineFrame_Main_dot_main())
    event, err := m.Run()
    if err != nil || event.Done || event.Request != int64(7) { t.Fatalf("request = %#v, %v", event, err) }
    event, err = m.Resume(int64(31))
    if err != nil || !event.Done || event.Value != int64(31) { t.Fatalf("completion = %#v, %v", event, err) }
}
`))
	cmd := exec.Command("go", "test", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(dir, "gocache"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated cross-module callback failed: %v\n%s", err, output)
	}
}
