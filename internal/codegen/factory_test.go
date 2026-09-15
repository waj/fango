package codegen

import (
	"bytes"
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

func TestPureFactoryHasConsumerIndependentMachineRepresentation(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	poly := types.Control{Polymorphic: true}
	mc := types.Control{Transport: types.Machine}
	fn := &types.TFun{Arg: b.Int, Ret: b.Int, Control: poly}
	box := &types.TCon{Unique: sup.NextUnique(), Name: "Factory.Box"}
	ctor := &types.CtorInfo{Name: "Factory.Box", Result: box, Fields: []types.Type{fn}}
	adt := &types.ADTInfo{Con: box, Ctors: []*types.CtorInfo{ctor}}
	factoryTy := &types.TFun{Arg: fn, Ret: box}
	factory := core.Def{Name: "Factory.wrap", Owner: "Factory", Type: factoryTy, Params: []string{"callback"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture()}, Body: &core.App{CalleeKind: core.Ctor, Callee: &core.VarRef{Name: ctor.Name, Ty: ctor.ValueType()}, Ctor: ctor, Args: []core.Expr{&core.VarRef{Name: "callback", Local: true, Ty: fn}}, Ty: box}}
	runTy := &types.TFun{Arg: fn, Ret: b.Int, Control: mc}
	wrapped := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: factory.Name, Ty: factoryTy}, Args: []core.Expr{&core.VarRef{Name: "action", Local: true, Ty: fn}}, Ty: box}
	run := core.Def{Name: "Main.run", Owner: "Main", Type: runTy, Params: []string{"action"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture()}, Control: mc,
		Body: &core.Case{Scrut: wrapped, Bind: "box", Ty: b.Int, Tree: &core.SwitchCtor{Scrut: "box", ADT: adt, Cases: []core.CtorCase{{Ctor: ctor, Binds: []string{"callback"}, Tree: &core.Leaf{Body: &core.App{CalleeKind: core.Value, Callee: &core.VarRef{Name: "callback", Local: true, Ty: fn}, Args: []core.Expr{&core.IntLit{Val: 42, Ty: b.Int}}, Ty: b.Int, Control: poly}}}}}}}
	main := core.Def{Name: "Main.main", Owner: "Main", Type: b.Int, Body: &core.IntLit{Val: 0, Ty: b.Int}}
	p := &core.Prog{Entry: main.Name, ADTs: []*types.ADTInfo{adt}, Defs: []core.Def{factory, main, run}}
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatal(errs)
	}
	mp, errs := machineir.Lower(p, b)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	units := []Unit{{Name: "Factory"}, {Name: "Main", Imports: []string{"Factory"}, Entry: true}}
	files, err := EmitMachineProject(p, mp, b, units, false)
	if err != nil {
		t.Fatal(err)
	}
	direct := *p
	direct.Defs = append([]core.Def(nil), p.Defs[:2]...)
	ordinary, err := EmitProject(&direct, b, units, false)
	if err != nil {
		t.Fatal(err)
	}
	var dependency []byte
	for _, f := range files {
		if f.Path == "modules/Factory/module.go" {
			dependency = f.Data
		}
	}
	for _, f := range ordinary {
		if f.Path == "modules/Factory/module.go" && !bytes.Equal(f.Data, dependency) {
			t.Fatalf("factory depends on consumer:\n%s\n%s", f.Data, dependency)
		}
	}
	if !strings.Contains(string(dependency), "func V_Factory_dot_wrap_machine") || strings.Contains(string(dependency), "MachineFrame_Factory_dot_wrap") {
		t.Fatalf("factory changed execution transport:\n%s", dependency)
	}
	dir := t.TempDir()
	write := func(path string, data []byte) {
		t.Helper()
		path = filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte("module fangobuild\n\ngo 1.26\n"))
	for _, f := range files {
		write(f.Path, f.Data)
	}
	sources, err := runtimefiles.Packages("fangort")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range sources {
		write(f.Path, f.Data)
	}
	write("factory_test.go", []byte(`package main
import("testing";"fangobuild/fangort")
type pause struct {value int64; pc int}
func(p *pause) Clear(){*p=pause{}}
func(p *pause) Step(m *fangort.Machine) fangort.MachineStep {
 if p.pc==0 {p.pc=1;return fangort.MachineStep{Kind:fangort.MachineSuspend,Request:p.value}}
 return fangort.MachineStep{Kind:fangort.MachineReturn,Value:m.TakeResult()}
}
func TestFactory(t *testing.T){
 m:=fangort.StartMachine(MachineFrame_Main_dot_run(struct{Direct func(int64)int64;Exit func(int64)fangort.Outcome[int64];Machine func(int64)fangort.MachineFrame}{Machine:func(n int64)fangort.MachineFrame{return &pause{value:n}}}))
 e,err:=m.Run();if err!=nil || e.Done || e.Request!=int64(42){t.Fatalf("pause: %#v %v",e,err)}
 e,err=m.Resume(int64(73));if err!=nil || !e.Done || e.Value!=int64(73){t.Fatalf("return: %#v %v",e,err)}
}
`))
	cmd := exec.Command("go", "test", ".")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		for _, f := range files {
			t.Logf("%s:\n%s", f.Path, f.Data)
		}
		t.Fatalf("factory: %v\n%s", err, output)
	}
}
