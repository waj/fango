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

func TestGeneratedMachineFrameExecutes(t *testing.T) {
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
	helperTy := &types.TFun{Arg: b.Int, Ret: b.Int, Control: control}
	helper := core.Def{Name: "Dep.helper", Owner: "Dep", Type: helperTy, Params: []string{"n"},
		ParamCaptures: []types.CaptureVar{sup.FreshCapture()}, Control: control,
		Body: &core.Suspend{Request: &core.VarRef{Name: "n", Local: true, Ty: b.Int}, Ty: b.Int}}
	call := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: "Dep.helper", Ty: helperTy},
		Args: []core.Expr{&core.IntLit{Val: 7, Ty: b.Int}}, Ty: b.Int, Control: control}
	main := core.Def{Name: "Main.main", Owner: "Main", Type: b.Int, Control: control, Body: call}
	p := &core.Prog{Entry: "Main.main", Defs: []core.Def{helper, main}}
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
	if !strings.Contains(dep, "func MachineFrame_Dep_dot_helper(v_n int64) fangort.MachineFrame") {
		t.Fatalf("dependency does not export its frame constructor:\n%s", dep)
	}
	if !strings.Contains(mainGo, `m_Dep "fangobuild/modules/Dep"`) ||
		!strings.Contains(mainGo, "m_Dep.MachineFrame_Dep_dot_helper(7)") {
		t.Fatalf("entry does not call the dependency-owned constructor:\n%s", mainGo)
	}
}
