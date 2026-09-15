package eval

import (
	"context"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/types"
	"github.com/waj/fango/runtime/fangort"
)

func machineRowFixture() (*machineir.Prog, int) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	eff := &types.EffectInfo{Unique: sup.NextUnique(), Name: "Fail"}
	op := &types.EffectOp{Owner: eff, Name: "fail", Abort: true, Arity: 1, ParamTypes: []types.Type{b.String}, ResultType: b.Unit}
	ev := core.EffectInstance{Unique: eff.Unique, Name: eff.Name, Captures: types.VarCapture(13), Control: types.Control{Transport: types.Exit}}
	fn := &types.TFun{Arg: b.Unit, Ret: b.Unit, OpenRow: true, Control: types.Control{Transport: types.Machine}}
	unit := &core.UnitLit{Ty: b.Unit}
	fail := &core.ControlExit{Op: op, Effect: ev, Payload: []core.Expr{&core.StringLit{Val: "failed", Ty: b.String}}, Ty: b.Unit}
	helperDef := &core.Def{Name: "helper", Type: fn, Params: []string{"unit"}, RowParam: 12, RowEffects: []core.EffectInstance{ev}, Body: fail}
	call := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: "helper", Ty: fn}, Args: []core.Expr{unit}, Ty: b.Unit, Row: &core.RowArgument{From: 11}, Control: types.Control{Transport: types.Machine}}
	producerDef := &core.Def{Name: "producer", Type: fn, Params: []string{"unit"}, RowParam: 11, Body: call}
	param, done, resume := machineir.Local{Name: "unit", Ty: b.Unit}, machineir.Local{Name: "done", Ty: b.Unit}, machineir.Local{Name: "resume", Ty: b.Unit}
	ref := &core.VarRef{Name: "done", Local: true, Ty: b.Unit}
	deferred := ev
	deferred.Control = types.Control{Transport: types.Machine}
	p := &machineir.Prog{Workers: []machineir.Worker{
		{Name: "producer", Params: []machineir.Local{param}, Result: b.Unit, RowParam: 11, Rows: []types.CaptureVar{11}, Def: producerDef, Locals: []machineir.Local{param, done, resume}, Blocks: []machineir.Block{
			{ID: 0, Term: &machineir.Suspend{Request: &core.IntLit{Val: 9, Ty: b.Int}, Bind: resume, Next: 1}},
			{ID: 1, Term: &machineir.Call{Callee: "helper", Args: []core.Expr{unit}, Row: call.Row, Bind: done, Next: 2}, LiveOut: []string{"done"}},
			{ID: 2, Term: &machineir.Return{Value: ref}, LiveIn: []string{"done"}},
		}},
		{Name: "helper", Params: []machineir.Local{param}, Result: b.Unit, RowParam: 12, Rows: []types.CaptureVar{12}, RowEffects: []core.EffectInstance{deferred}, Def: helperDef, Locals: []machineir.Local{param, done}, Blocks: []machineir.Block{
			{ID: 0, Term: &machineir.Eval{Value: fail, Bind: done, Next: 1}, LiveOut: []string{"done"}},
			{ID: 1, Term: &machineir.Return{Value: ref}, LiveIn: []string{"done"}},
		}},
	}}
	return p, eff.Unique
}

func TestMachineResidualRowSurvivesSuspensionAndHelperCall(t *testing.T) {
	p, effect := machineRowFixture()
	if errs := machineir.Lint(p); len(errs) != 0 {
		t.Fatal(errs)
	}
	ctx := NewIOContext(strings.NewReader(""), io.Discard)
	session, err := startMachine(context.Background(), p, "producer", []Value{struct{}{}}, nil, NewEnv(), ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	row := fangort.NewCursorEvidence(nil)
	session.frames[0].rows = rowEnv{11: row.Row()}
	cursor := &MachineIteratorSession{session: session, evidence: row}
	first, second := &evidence{}, &evidence{}
	argument := func(target *evidence) *fangort.EvidenceRow {
		return fangort.ExtendEvidenceRow(nil, fangort.EvidenceBinding{Name: strconv.Itoa(effect), Family: fangort.EvidenceFamily{Machine: target}})
	}
	value, present, exit, err := cursor.NextWithEvidence(argument(first))
	if err != nil || exit != nil || !present || value != int64(9) {
		t.Fatalf("first pull: %v %v %v %v", value, present, exit, err)
	}
	_, present, exit, err = cursor.NextWithEvidence(argument(second))
	if err != nil || present || exit == nil || exit.Target != second {
		t.Fatalf("failed pull: %v %v %v", present, exit, err)
	}
	_, present, exit, err = cursor.NextWithEvidence(argument(first))
	if err != nil || present || exit != nil || len(session.frames) != 0 {
		t.Fatalf("exhausted: %v %v %v", present, exit, err)
	}
}

func TestMachineResidualRowProofRejectsDroppedTransport(t *testing.T) {
	for _, edit := range []func(*machineir.Prog){
		func(p *machineir.Prog) { p.Workers[0].Rows = nil },
		func(p *machineir.Prog) { p.Workers[0].Blocks[1].Term.(*machineir.Call).Row = nil },
		func(p *machineir.Prog) { p.Workers[0].Blocks[1].Term.(*machineir.Call).Row.From = 99 },
		func(p *machineir.Prog) { p.Workers[1].RowEffects = nil },
	} {
		p, _ := machineRowFixture()
		edit(p)
		if errs := machineir.Lint(p); len(errs) == 0 {
			t.Fatal("dropped row transport accepted")
		}
	}
}

func TestMachineResidualRowCallsOrdinaryResumptiveEvidence(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	eff := &types.EffectInfo{Unique: sup.NextUnique(), Name: "Reader"}
	op := &types.EffectOp{Owner: eff, Name: "ask", Arity: 1, ParamTypes: []types.Type{b.Unit}, ResultType: b.Int}
	ev := core.EffectInstance{Unique: eff.Unique, Name: eff.Name, Control: types.Control{Transport: types.Machine}}
	unit := &core.UnitLit{Ty: b.Unit}
	perform := &core.Perform{Op: op, Effect: ev, Args: []core.Expr{unit}, Ty: b.Int, Control: ev.Control}
	d := &core.Def{Name: "ask", Type: &types.TFun{Arg: b.Unit, Ret: b.Int, OpenRow: true, Control: ev.Control}, Params: []string{"unit"}, RowParam: 1, RowEffects: []core.EffectInstance{ev}, Body: perform}
	result := machineir.Local{Name: "value", Ty: b.Int}
	param := machineir.Local{Name: "unit", Ty: b.Unit}
	p := &machineir.Prog{Workers: []machineir.Worker{{Name: "ask", Params: []machineir.Local{param}, RowParam: 1, RowEffects: d.RowEffects, Rows: []types.CaptureVar{1}, Def: d, Result: b.Int, Locals: []machineir.Local{param, result}, Blocks: []machineir.Block{
		{ID: 0, Term: &machineir.Call{Operation: op, Effect: ev, Args: []core.Expr{unit}, Bind: result, Next: 1}, LiveOut: []string{"value"}},
		{ID: 1, Term: &machineir.Return{Value: &core.VarRef{Name: "value", Local: true, Ty: b.Int}}, LiveIn: []string{"value"}},
	}}}}
	session, err := startMachine(context.Background(), p, "ask", []Value{struct{}{}}, nil, NewEnv(), NewIOContext(strings.NewReader(""), io.Discard), false)
	if err != nil {
		t.Fatal(err)
	}
	handler := &evidence{handler: &core.Handle{Clauses: []core.HandlerClause{{Op: op, Params: []string{"unit"}, ResumeID: 1, Body: &core.ResumeTail{Owner: 1, Value: &core.IntLit{Val: 42, Ty: b.Int}, ClauseResult: b.Int}}}}}
	row := fangort.ExtendEvidenceRow(nil, fangort.EvidenceBinding{Name: strconv.Itoa(eff.Unique), Family: fangort.EvidenceFamily{Machine: handler}})
	frame := session.frames[0]
	frame.rows = bindInvocationRow(1, d.RowEffects, row, frame.evidence)
	event, err := session.Run()
	if err != nil || !event.Done || event.Exit != nil || event.Value != int64(42) {
		t.Fatalf("ordinary interpretation: %+v %v", event, err)
	}
}
