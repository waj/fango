package eval

import (
	"context"
	"io"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func TestDeferredAbortEvidenceIsSuppliedWhenClosureIsInvoked(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	effect := &types.EffectInfo{Unique: sup.NextUnique(), Name: "Fail"}
	op := &types.EffectOp{Owner: effect, Name: "fail", Abort: true, Arity: 1, ParamTypes: []types.Type{b.String}, ResultType: b.String}
	effect.Ops = []*types.EffectOp{op}
	formal := core.EffectInstance{Unique: effect.Unique, Name: effect.Name, Captures: types.VarCapture(2), Control: types.Control{Transport: types.Exit}}
	fn := &types.TFun{Arg: b.Unit, Ret: b.String, OpenRow: true, Control: types.Control{Polymorphic: true, Transport: types.Exit}}
	lam := &core.Lambda{Param: "unit", Ty: fn, RowParam: 1, RowEffects: []core.EffectInstance{formal}, Body: &core.ControlExit{Effect: formal, Op: op, Payload: []core.Expr{&core.StringLit{Val: "payload", Ty: b.String}}, Ty: b.String}}
	in := &interp{ctx: context.Background(), env: NewEnv(), out: io.Discard, evidence: map[types.EffectKey]*evidence{}}
	value, err := in.eval(lam, nil)
	if err != nil {
		t.Fatal(err)
	}
	closure := value.(*Closure)
	if len(closure.Evidence) != 0 {
		t.Fatal("construction retained invocation evidence")
	}
	for i, answer := range []string{"first", "second"} {
		actual := formal
		actual.Captures = types.ScopeCapture(types.ScopeID(i + 1))
		call := &core.App{CalleeKind: core.Value, Callee: &core.VarRef{Name: "callback", Local: true, Ty: fn}, Args: []core.Expr{&core.UnitLit{Ty: b.Unit}}, Ty: b.String, Row: &core.RowArgument{Effects: []core.EffectInstance{actual}}}
		handle := &core.Handle{Scope: types.ScopeID(i + 1), Effect: actual, Body: call, Ty: b.String, Clauses: []core.HandlerClause{{Op: op, Params: []string{"error"}, ParamTypes: []types.Type{b.String}, ResultType: b.String, Body: &core.StringLit{Val: answer, Ty: b.String}}}}
		got, err := in.eval(handle, &Frame{vars: map[string]Value{"callback": closure}})
		if err != nil || got != answer {
			t.Fatalf("invocation %d: %v, %v", i, got, err)
		}
	}
	// Explicit definition-site evidence remains fixed even when an invocation
	// supplies another interpretation in its residual row.
	outer := &evidence{}
	in.evidence[formal.Key()] = outer
	lam.RowEffects = nil
	fixed, err := in.eval(lam, nil)
	if err != nil {
		t.Fatal(err)
	}
	in.evidence[formal.Key()] = &evidence{}
	call := &core.App{CalleeKind: core.Value, Callee: &core.VarRef{Name: "callback", Local: true, Ty: fn}, Args: []core.Expr{&core.UnitLit{Ty: b.Unit}}, Ty: b.String, Row: &core.RowArgument{Effects: []core.EffectInstance{formal}}}
	got, err := in.eval(call, &Frame{vars: map[string]Value{"callback": fixed}})
	exit, ok := asExit(got)
	if err != nil || !ok || exit.Target != outer {
		t.Fatalf("lexical exit=%v, err=%v", got, err)
	}
}

func TestTailLoopRequiresIdentityResidualRow(t *testing.T) {
	d := &core.Def{Name: "loop", RowParam: 3}
	call := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: "loop"}, Row: &core.RowArgument{From: 3}}
	if !core.IsTailLoopCall(d, call) {
		t.Fatal("identity row forwarding rejected")
	}
	call.Row.From = 4
	if core.IsTailLoopCall(d, call) {
		t.Fatal("changed residual row discarded by tail loop")
	}
	call.Row.From = 3
	call.Row.Effects = []core.EffectInstance{{Unique: 5}}
	if core.IsTailLoopCall(d, call) {
		t.Fatal("residual overlay discarded by tail loop")
	}
}
