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
	op := &types.EffectOp{Owner: eff, Index: 0, Name: "Main.fail", Arity: 1, ParamTypes: []types.Type{b.Int}, ResultType: b.Int, Abort: true}
	eff.Ops = []*types.EffectOp{op}
	exitControl := types.Control{Transport: types.Exit}
	label := types.EffLabel{Unique: eff.Unique, Name: eff.Name, Abort: true}
	exitTy := &types.TFun{Arg: b.Unit, Eff: types.Row{Labels: []types.EffLabel{label}}, Ret: b.Int, Control: exitControl}
	ev := core.EffectInstance{Unique: eff.Unique, Name: eff.Name, Captures: types.VarCapture(10), Control: exitControl}
	exit := &core.ControlExit{Effect: ev, Op: op, Payload: []core.Expr{&core.IntLit{Val: 7, Ty: b.Int}}, Ty: b.Int}
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
		{Name: "Main.main", Owner: "Main", Type: exitTy, Params: []string{"_"}, ParamCaptures: []types.CaptureVar{2}, EffectParams: []core.EffectInstance{ev}, Control: exitControl, Body: exitBody},
		{Name: "Main.unitMain", Owner: "Main", Type: exitUnitTy, Params: []string{"_"}, ParamCaptures: []types.CaptureVar{3}, EffectParams: []core.EffectInstance{ev}, Control: exitControl,
			Body: &core.Let{Name: "unused", Rhs: exit, Body: tick, Ty: b.Unit}},
	}}

	data, err := emitUnit(p, b, Unit{Name: "Main", Entry: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{
		"func V_Main_dot_id(v_x int64) int64",
		"func V_Main_dot_main_exit(ev_Main_dot_Fail Eff_Main_dot_Fail_exit) fangort.Outcome[int64]",
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

// A cleanup scope has no ABI of its own: the Direct family member sequences
// ordinary calls, and the Exit family member tests each Outcome and records a
// release failure it cannot make primary. Neither captures a continuation nor
// consults a consumed-state flag.
func TestCleanupScopeEmitsBothFamiliesWithoutAContinuation(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	result := &types.TVar{ID: sup.NextUnique(), Rigid: true}
	poly := types.Control{Polymorphic: true}
	acquireTy := &types.TFun{Arg: b.Unit, Ret: b.String, Control: poly}
	releaseTy := &types.TFun{Arg: b.String, Ret: b.Unit, Control: poly}
	useTy := &types.TFun{Arg: b.String, Ret: result, Control: poly}
	defTy := &types.TFun{Arg: acquireTy, Ret: &types.TFun{Arg: releaseTy,
		Ret: &types.TFun{Arg: useTy, Ret: result, Control: poly}}}
	call := func(fn string, fnTy *types.TFun, arg core.Expr) core.Expr {
		return &core.App{CalleeKind: core.Value, Callee: &core.VarRef{Name: fn, Local: true, Ty: fnTy},
			Args: []core.Expr{arg}, Ty: fnTy.Ret, Control: types.FunctionControl(fnTy)}
	}
	resource := func() core.Expr { return &core.VarRef{Name: "_resource", Local: true, Ty: b.String} }
	scope := &core.Bracket{
		Scope: 1, Resource: "_resource", ResourceTy: b.String,
		Acquire: call("_acquire", acquireTy, &core.UnitLit{Ty: b.Unit}),
		Release: call("_release", releaseTy, resource()),
		Body:    call("_use", useTy, resource()),
		Ty:      result, Control: poly,
	}
	p := &core.Prog{Entry: "Main.bracket", Defs: []core.Def{{
		Name: "Main.bracket", Owner: "Main", Type: defTy, TyParams: []*types.TVar{result},
		Params:        []string{"_acquire", "_release", "_use"},
		ParamCaptures: []types.CaptureVar{1, 2, 3},
		Control:       poly, Body: scope,
	}}}
	core.InferCaptures(p, b)

	data, err := emitUnit(p, b, Unit{Name: "Main", Entry: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{
		"func V_Main_dot_bracket[A0 any](t_acquire func(fangort.Unit) string, t_release func(string) fangort.Unit, t_use func(string) A0) A0",
		"func V_Main_dot_bracket_exit[A0 any](t_acquire func(fangort.Unit) fangort.Outcome[string]",
		"_ = t_release(t_resource)",
		"fangort.Suppress(",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated Go missing %q:\n%s", want, got)
		}
	}
	direct := got[strings.Index(got, "func V_Main_dot_bracket["):strings.Index(got, "func V_Main_dot_bracket_exit[")]
	if strings.Contains(direct, "fangort.Outcome") || strings.Contains(direct, "fangort.Propagate") {
		t.Fatalf("the direct family member acquired Outcome plumbing:\n%s", direct)
	}
	for _, unwanted := range []string{"go func", "chan ", "panic(", "Resume", "Discard"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("generated cleanup scope contains %q:\n%s", unwanted, got)
		}
	}
}
