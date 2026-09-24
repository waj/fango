package codegen

import (
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func TestMachineSelfTailReusesFrameAndSwapsArguments(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	control := types.Control{Transport: types.Machine}
	fn := &types.TFun{Arg: b.Int, Ret: &types.TFun{Arg: b.Int, Ret: b.Unit, Control: control}}
	a := &core.VarRef{Name: "a", Local: true, Ty: b.Int}
	c := &core.VarRef{Name: "b", Local: true, Ty: b.Int}
	loop := core.Def{Name: "Main.loop", Owner: "Main", Type: fn, Control: control, Params: []string{"a", "b"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture(), sup.FreshCapture()},
		Body: &core.Seq{First: &core.Suspend{Request: a, Ty: b.Unit}, Ty: b.Unit, Then: &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: "Main.loop", Ty: fn}, Args: []core.Expr{c, a}, Ty: b.Unit, Control: control}}}
	p := &core.Prog{Entry: "Main.main", Defs: []core.Def{{Name: "Main.main", Owner: "Main", Type: b.Int, Body: &core.IntLit{Val: 0, Ty: b.Int}}, loop}}
	runCoroutineProof(t, p, b, `package main
import("testing";"fangobuild/fangort")
func TestReuse(t *testing.T){
 f:=MachineFrame_Main_dot_loop(11,22).(*machineFrame_Main_dot_loop)
 m:=fangort.StartMachine(f)
 e,err:=m.Run()
 for i:=0;i<1000;i++ {
  want:=int64(11);if i%2!=0{want=22}
  if err!=nil||e.Done||e.Request!=want||f.L_a!=want{t.Fatalf("iteration %d: %#v %v frame=%#v",i,e,err,f)}
  e,err=m.Resume(fangort.UnitValue)
 }
 if m.Stats().MaxDepth!=1{t.Fatal(m.Stats())}
 if exit,err:=m.Abandon();exit!=nil||err!=nil{t.Fatalf("abandon: %v %v",exit,err)}
 if f.L_a!=0||f.L_b!=0{t.Fatalf("retained arguments: %#v",f)}
}
`)
}
