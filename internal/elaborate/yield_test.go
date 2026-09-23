package elaborate

import (
	"testing"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

func TestStreamYieldElaboratesToOrdinaryOperation(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	ck := infer.NewChecker(sup, b, infer.NewEnv())
	elem := sup.FreshRigid(types.General)
	row := sup.FreshRigid(types.RowVar)
	effect := &types.EffectInfo{Unique: sup.NextUnique(), Name: "Stream.Yield", Params: []*types.TVar{elem}}
	label := types.EffLabel{Unique: effect.Unique, Name: effect.Name, Args: []types.Type{elem}}
	ty := &types.TFun{Arg: elem, Eff: types.Row{Labels: []types.EffLabel{label}, Tail: row}, Ret: b.Unit}
	op := &types.EffectOp{Owner: effect, Name: "Stream.yield", Scheme: types.Scheme{Vars: []*types.TVar{elem, row}, Body: ty}, Arity: 1, ParamTypes: []types.Type{elem}, ResultType: b.Unit}
	effect.Ops = []*types.EffectOp{op}
	ck.Effects[effect.Name] = effect
	ck.EffectsByUnique[effect.Unique] = effect
	ck.Operations[op.Name] = op
	ck.Env.Bind(op.Name, op.Scheme)

	sp := source.Span{}
	call := &ast.App{Fn: &ast.Var{Name: op.Name, Sp: sp}, Arg: &ast.StringLit{Value: "one", Sp: sp}}
	if _, errs := ck.Expr(call); len(errs) != 0 {
		t.Fatalf("inference diagnostics: %+v", errs)
	}
	got, _, errs := Expr(call, ck)
	if len(errs) != 0 {
		t.Fatalf("elaboration diagnostics: %+v", errs)
	}
	perform, ok := got.(*core.Perform)
	if !ok {
		t.Fatalf("yield Core = %T, want *core.Perform", got)
	}
	if value, ok := perform.Args[0].(*core.StringLit); !ok || value.Val != "one" || !types.Equal(perform.Type(), b.Unit) {
		t.Fatalf("perform = %#v", perform)
	}
	if control := core.ExprControl(perform); control.Transport == types.Machine {
		t.Fatalf("ordinary yield selected Machine without handler evidence: %+v", control)
	}
}
