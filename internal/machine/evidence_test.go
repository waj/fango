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
	root := &builder{def: d, locals: map[string]types.Type{}, lambdas: map[*core.Lambda]bool{}}
	root.registerMachineLambdas(factory)
	if len(root.closures) != 1 || root.closures[0].Expr != factory || len(root.aux) != 1 {
		t.Fatalf("factory closure: %+v, auxiliaries: %+v", root.closures, root.aux)
	}
	aux := &root.aux[0]
	nested := &builder{def: aux, locals: localRefTypes(aux.Body), lambdas: map[*core.Lambda]bool{}}
	nested.registerMachineLambdas(aux.Body)
	if len(nested.closures) != 1 || nested.closures[0].Expr != callback {
		t.Fatalf("callback closure: %+v", nested.closures)
	}
	captures := nested.closures[0].CapturedEvidence
	if len(captures) != 1 || !types.EqualCaptures(captures[0].Captures, inner.Captures) {
		t.Fatalf("captures: %#v", captures)
	}
}
