package codegen

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func TestExitWorkerEmitsOutcomePropagationWhileDirectWorkerStaysPlain(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	eff := &types.EffectInfo{Unique: sup.NextUnique(), Name: "Main.Fail"}
	op := &types.EffectOp{Owner: eff, Index: 0, Name: "Main.fail", Arity: 1, ParamTypes: []types.Type{b.Int}, ResultType: b.Int}
	eff.Ops = []*types.EffectOp{op}
	exitControl := types.Control{Transport: types.Exit}
	exitTy := &types.TFun{Arg: b.Unit, Ret: b.Int, Control: exitControl}
	exit := &core.ControlExit{Target: 3, Op: op, Payload: []core.Expr{&core.IntLit{Val: 7, Ty: b.Int}}, Ty: b.Int}
	exitBody := &core.Let{Name: "x", Rhs: exit, Body: &core.VarRef{Name: "x", Local: true, Ty: b.Int}, Ty: b.Int}
	directTy := &types.TFun{Arg: b.Int, Ret: b.Int}
	directUnitTy := &types.TFun{Arg: b.Unit, Ret: b.Unit}
	exitUnitTy := &types.TFun{Arg: b.Unit, Ret: b.Unit, Control: exitControl}
	tick := &core.App{
		CalleeKind: core.Worker,
		Callee:     &core.VarRef{Name: "Main.tick", Ty: directUnitTy},
		Args:       []core.Expr{&core.UnitLit{Ty: b.Unit}},
		Ty:         b.Unit,
	}
	p := &core.Prog{Entry: "Main.main", Effects: []*types.EffectInfo{eff}, Defs: []core.Def{
		{Name: "Main.id", Owner: "Main", Type: directTy, Params: []string{"x"}, ParamCaptures: []types.CaptureVar{1}, Body: &core.VarRef{Name: "x", Local: true, Ty: b.Int}},
		{Name: "Main.tick", Owner: "Main", Type: directUnitTy, Params: []string{"_"}, ParamCaptures: []types.CaptureVar{2}, Body: &core.UnitLit{Ty: b.Unit}},
		{Name: "Main.main", Owner: "Main", Type: exitTy, Params: []string{"_"}, ParamCaptures: []types.CaptureVar{2}, Control: exitControl, Body: exitBody},
		{Name: "Main.unitMain", Owner: "Main", Type: exitUnitTy, Params: []string{"_"}, ParamCaptures: []types.CaptureVar{3}, Control: exitControl,
			Body: &core.Let{Name: "unused", Rhs: exit, Body: tick, Ty: b.Unit}},
	}}

	data, err := emitUnit(p, b, Unit{Name: "Main", Entry: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{
		"func V_Main_dot_id(v_x int64) int64",
		"func V_Main_dot_main_exit() fangort.Outcome[int64]",
		"fangort.Propagate[int64]",
		"if t_outcome0.Exit != nil",
		"V_Main_dot_tick()\n\treturn fangort.Normal[fangort.Unit](fangort.UnitValue)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated Go missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "func V_Main_dot_id(v_x int64) fangort.Outcome") {
		t.Fatalf("direct worker acquired Outcome ABI:\n%s", got)
	}
}
