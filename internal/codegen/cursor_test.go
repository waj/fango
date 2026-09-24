package codegen

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/core/coretest"
	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/runtimefiles"
	"github.com/waj/fango/internal/types"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runCoroutineProof(t *testing.T, p *core.Prog, b *types.Builtins, source string) {
	t.Helper()
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatal(errs)
	}
	mp, errs := machineir.Lower(p, b)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	files, err := EmitMachineProject(p, mp, b, []Unit{{Name: "Runtime.Coroutine"}, {Name: "Main", Program: "Main", Imports: []string{"Runtime.Coroutine"}, Entry: true}}, false)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := runtimefiles.Packages("fangort")
	if err != nil {
		t.Fatal(err)
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
	for _, f := range sources {
		write(f.Path, f.Data)
	}
	write("entries/Main/coroutine_test.go", []byte(source))
	cmd := exec.Command("go", "test", "./entries/Main")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		for _, f := range files {
			t.Logf("%s:\n%s", f.Path, f.Data)
		}
		t.Fatalf("generated coroutine: %v\n%s", err, output)
	}
}
func TestGeneratedTypedCoroutineAdvancement(t *testing.T) {
	p, b := coretest.SynchronousCursorScope()
	producer := core.Def{Name: "Main.producer", Owner: "Main", Type: b.Unit, Control: types.Control{Transport: types.Machine}, Body: &core.Seq{First: &core.Suspend{Request: &core.IntLit{Val: 42, Ty: b.Int}, Ty: b.Unit}, Then: &core.UnitLit{Ty: b.Unit}, Ty: b.Unit}}
	p.Defs = []core.Def{p.Defs[0], producer}
	p.Entry = producer.Name
	runCoroutineProof(t, p, b, `package main
import("testing";"fangobuild/fangort";c "fangobuild/modules/Runtime/Coroutine")
func TestPulls(t *testing.T){
 cursor:=fangort.StartMachineCoroutine(nil,nil,func(any)fangort.MachineFrame{return MachineFrame_Main_dot_producer()})
 for i:=0;i<4;i++ {
  m:=fangort.StartMachine(c.MachineFrame_Runtime_dot_Coroutine_dot_advance(cursor,fangort.UnitValue))
  event,err:=m.Run();if err!=nil||!event.Done||event.Exit!=nil {t.Fatalf("pull %d: %#v %v",i,event,err)}
  switch i {
  case 0:v,ok:=event.Value.(*c.C_Runtime_dot_Coroutine_dot_Suspended[int64,fangort.Unit]);if !ok||v.F0!=42 {t.Fatalf("suspension: %#v",event.Value)}
  case 1:if _,ok:=event.Value.(*c.C_Runtime_dot_Coroutine_dot_Finished[int64,fangort.Unit]);!ok {t.Fatalf("finished: %#v",event.Value)}
  default:if _,ok:=event.Value.(*c.C_Runtime_dot_Coroutine_dot_Closed[int64,fangort.Unit]);!ok {t.Fatalf("closed: %#v",event.Value)}
  }
 }
}
`)
}
func TestGeneratedCoroutineScopeClosesBeforeReturning(t *testing.T) {
	p, b := coretest.CursorScope()
	runCoroutineProof(t, p, b, `package main
import("testing";"fangobuild/fangort";c "fangobuild/modules/Runtime/Coroutine")
func TestScope(t *testing.T){
 for _,abandon:=range []bool{false,true} {
  m:=fangort.StartMachine(MachineFrame_Main_dot_main());event,err:=m.Run()
  if err!=nil||event.Done||event.Request!=int64(99) {t.Fatalf("pause: %#v %v",event,err)}
  if m.Stats().MaxCleanups!=1 {t.Fatalf("owners: %#v",m.Stats())}
  if abandon {if exit,err:=m.Abandon();exit!=nil||err!=nil {t.Fatalf("abandon: %#v %v",exit,err)}} else {
   event,err=m.Resume(fangort.UnitValue);if err!=nil||!event.Done||event.Exit!=nil {t.Fatalf("completion: %#v %v",event,err)}
   v,ok:=event.Value.(*c.C_Runtime_dot_Coroutine_dot_Suspended[int64,fangort.Unit]);if !ok||v.F0!=42 {t.Fatalf("result: %#v",event.Value)}
  }
 }
}
`)
}
func TestGeneratedSynchronousCoroutineScope(t *testing.T) {
	p, b := coretest.SynchronousCursorScope()
	runCoroutineProof(t, p, b, `package main
import("testing";"fangobuild/fangort";c "fangobuild/modules/Runtime/Coroutine")
func TestScope(t *testing.T){
 v,ok:=V_Main_dot_main.(*c.C_Runtime_dot_Coroutine_dot_Suspended[int64,fangort.Unit]);if !ok||v.F0!=42 {t.Fatalf("result: %#v",v)}
}
`)
}
