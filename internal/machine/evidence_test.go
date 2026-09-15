package machine

import (
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func TestRootedClosuresCaptureNearestLexicalEvidence(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	effect := &types.EffectInfo{Unique: sup.NextUnique(), Name: "Reader"}
	op := &types.EffectOp{Owner: effect, Name: "ask", Arity: 1, ParamTypes: []types.Type{b.Unit}, ResultType: b.Int}
	outer := core.EffectInstance{Unique: effect.Unique, Name: effect.Name, Captures: types.ScopeCapture(sup.FreshScope())}
	inner := outer
	inner.Captures = types.ScopeCapture(sup.FreshScope())
	fn := &types.TFun{Arg: b.Unit, Ret: b.Int, Control: types.Control{Polymorphic: true}}
	callback := &core.Lambda{Param: "unit", Ty: fn, Body: &core.Perform{Effect: inner, Op: op, Args: []core.Expr{&core.UnitLit{Ty: b.Unit}}, Ty: b.Int}}
	// A fixed Direct factory contains a handler and returns a Machine-capable
	// callback. Lexical scanning must enter both the factory and the handler.
	factory := &core.Lambda{Param: "ignored", Ty: &types.TFun{Arg: b.Unit, Ret: fn}, Body: &core.Handle{Effect: inner, Body: callback, Ty: fn}}
	d := &core.Def{Name: "make", EffectParams: []core.EffectInstance{outer}}
	builder := &builder{def: d, locals: map[string]types.Type{}, lambdas: map[*core.Lambda]bool{}}
	builder.registerMachineLambdas(factory)
	if len(builder.closures) != 1 {
		t.Fatalf("closures: %d", len(builder.closures))
	}
	captures := builder.closures[0].CapturedEvidence
	if len(captures) != 1 || !types.EqualCaptures(captures[0].Captures, inner.Captures) {
		t.Fatalf("captures: %#v", captures)
	}
}
