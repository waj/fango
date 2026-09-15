package codegen

import (
	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/runtimefiles"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/core/coretest"
	"github.com/waj/fango/internal/types"
)

func TestGeneratedTypedCursorAdvancement(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	a := sup.FreshRigid(types.General)
	con := &types.TCon{Unique: sup.NextUnique(), Name: "Maybe.Maybe", Args: []types.Type{a}}
	nothing := &types.CtorInfo{Name: "Maybe.Nothing", Index: 0, Result: con}
	just := &types.CtorInfo{Name: "Maybe.Just", Index: 1, Fields: []types.Type{a}, Result: con}
	adt := &types.ADTInfo{Con: con, Params: []*types.TVar{a}, Ctors: []*types.CtorInfo{nothing, just}}
	result := &types.TCon{Unique: con.Unique, Name: con.Name, Args: []types.Type{b.Int}}
	cursor := &types.TCon{Unique: sup.NextUnique(), Name: types.IteratorTypeName, Args: []types.Type{b.Int, b.Unit}}
	control := types.Control{Transport: types.Machine}
	fn := &types.TFun{Arg: cursor, Ret: result, Control: control}
	p := &core.Prog{Entry: "Main.producer", Intrinsics: map[string]bool{types.IteratorNextName: true}, ADTs: []*types.ADTInfo{adt}, Defs: []core.Def{
		{Name: "Main.producer", Owner: "Main", Type: b.Unit, Control: control, Body: &core.Seq{
			First: &core.Suspend{Request: &core.IntLit{Val: 42, Ty: b.Int}, Ty: b.Unit}, Then: &core.UnitLit{Ty: b.Unit}, Ty: b.Unit}},
		{Name: types.IteratorNextName, Owner: "Iterator", Type: fn, Params: []string{"cursor"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture()}, Control: control,
			Body: &core.IteratorNext{Cursor: &core.VarRef{Name: "cursor", Local: true, Ty: cursor}, Result: adt, Access: types.ExclusiveAdvance, Ty: result}},
	}}
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatal(errs)
	}
	mp, errs := machineir.Lower(p, b)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	files, err := EmitMachineProject(p, mp, b, []Unit{{Name: "Maybe"}, {Name: "Iterator", Imports: []string{"Maybe"}}, {Name: "Main", Imports: []string{"Maybe", "Iterator"}, Entry: true}}, false)
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
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
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
	write("cursor_test.go", []byte(`package main
import (
 "testing"
 "fangobuild/fangort"
 m_Iterator "fangobuild/modules/Iterator"
 m_Maybe "fangobuild/modules/Maybe"
)
func TestPulls(t *testing.T) {
 cursor := fangort.StartMachineIterator(MachineFrame_Main_dot_producer())
 for i:=0;i<4;i++ {
  caller := fangort.StartMachine(m_Iterator.MachineFrame_Iterator_dot_next(cursor))
  event, err := caller.Run()
  if err != nil || !event.Done || event.Exit != nil {t.Fatalf("pull %d: %#v %v", i,event,err)}
  if i==0 {
   value,ok:=event.Value.(*m_Maybe.C_Maybe_dot_Just[int64])
   if !ok || value.F0 !=42 {t.Fatalf("first pull: %#v", event.Value)}
  } else {
   if _,ok:=event.Value.(*m_Maybe.C_Maybe_dot_Nothing[int64]); !ok {t.Fatalf("exhausted: %#v", event.Value)}
  }
 }
}
`))
	cmd := exec.Command("go", "test", ".")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		for _, file := range files {
			t.Logf("%s:\n%s", file.Path, file.Data)
		}
		t.Fatalf("generated advancement: %v\n%s", err, output)
	}
}

func TestGeneratedCursorScopeClosesBeforeReturning(t *testing.T) {
	p, b := coretest.CursorScope()
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatal(errs)
	}
	mp, errs := machineir.Lower(p, b)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	files, err := EmitMachineProject(p, mp, b, []Unit{{Name: "Maybe"}, {Name: "Iterator", Imports: []string{"Maybe"}}, {Name: "Stream", Imports: []string{"Maybe", "Iterator"}}, {Name: "Main", Imports: []string{"Maybe", "Iterator", "Stream"}, Entry: true}}, false)
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
	for _, file := range files {
		write(file.Path, file.Data)
	}
	for _, file := range sources {
		write(file.Path, file.Data)
	}
	write("cursor_test.go", []byte(`package main
import (
 "testing"
 "fangobuild/fangort"
 m_Maybe "fangobuild/modules/Maybe"
)
func TestScope(t *testing.T) {
 for _,abandon:=range []bool{false,true} {
  m:=fangort.StartMachine(MachineFrame_Main_dot_main())
  event,err:=m.Run()
  if err!=nil || event.Done || event.Request!=int64(99) {t.Fatalf("consumer pause: %#v %v",event,err)}
  if m.Stats().MaxCleanups!=1 {t.Fatalf("owners: %#v",m.Stats())}
  if abandon {
   if exit,err:=m.Abandon();exit!=nil || err!=nil {t.Fatalf("abandon: %#v %v",exit,err)}
  } else {
   event,err=m.Resume(fangort.UnitValue)
   if err!=nil || !event.Done || event.Exit!=nil {t.Fatalf("completion: %#v %v",event,err)}
   value,ok:=event.Value.(*m_Maybe.C_Maybe_dot_Just[int64])
   if !ok || value.F0!=42 {t.Fatalf("result: %#v",event.Value)}
  }
 }
}
`))
	cmd := exec.Command("go", "test", ".")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		for _, f := range files {
			t.Logf("%s:\n%s", f.Path, f.Data)
		}
		t.Fatalf("generated cursor scope: %v\n%s", err, output)
	}
}

func TestGeneratedSynchronousCursorScope(t *testing.T) {
	p, b := coretest.SynchronousCursorScope()
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatal(errs)
	}
	mp, errs := machineir.Lower(p, b)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	units := []Unit{{Name: "Maybe"}, {Name: "Iterator", Imports: []string{"Maybe"}}, {Name: "Stream", Imports: []string{"Maybe", "Iterator"}}, {Name: "Main", Imports: []string{"Maybe", "Iterator", "Stream"}, Entry: true}}
	files, err := EmitMachineProject(p, mp, b, units, false)
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
	sources, err := runtimefiles.Packages("fangort")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range sources {
		write(f.Path, f.Data)
	}
	write("cursor_test.go", []byte(`package main
import("testing";m_Maybe "fangobuild/modules/Maybe")
func TestSynchronous(t *testing.T){
 value,ok:=V_Main_dot_main.(*m_Maybe.C_Maybe_dot_Just[int64])
 if !ok || value.F0!=42 {t.Fatalf("result: %#v",V_Main_dot_main)}
}
`))
	cmd := exec.Command("go", "test", ".")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		for _, f := range files {
			t.Logf("%s:\n%s", f.Path, f.Data)
		}
		t.Fatalf("synchronous scope: %v\n%s", err, output)
	}
}
