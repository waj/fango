package core

import (
	"testing"

	"github.com/waj/fango/internal/types"
)

func TestABISummaryUsesDependencyMetadataWithoutBody(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	callback := &types.TFun{Arg: b.Unit, Eff: types.Row{Labels: []types.EffLabel{{Unique: 99, Name: "E"}}}, Ret: b.Unit}
	dep := Def{Name: "Dep.factory", Type: &types.TFun{Arg: callback, Ret: callback}, Params: []string{"f"}, ABI: ABISummary{Valid: true, NeedsFamily: true}}
	own := Def{Name: "Main.factory", Type: dep.Type, Params: []string{"f"}, Body: &App{Callee: &VarRef{Name: dep.Name, Ty: dep.Type}, CalleeKind: Worker, Args: []Expr{&VarRef{Name: "f", Local: true, Ty: callback}}, Ty: callback}}
	p := &Prog{Defs: []Def{own}}
	SummarizeABI(p, []Def{dep})
	if !p.Defs[0].ABI.Valid || !p.Defs[0].ABI.NeedsFamily {
		t.Fatalf("summary = %#v", p.Defs[0].ABI)
	}
}

func TestABISummaryDistinguishesReturningAndExecutingCallbacks(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	callback := &types.TFun{Arg: b.Unit, Eff: types.Row{Labels: []types.EffLabel{{Unique: 99, Name: "E"}}}, Ret: b.Int}
	factory := &types.TFun{Arg: b.Unit, Ret: callback}
	for _, test := range []struct {
		name    string
		arg     types.Type
		result  types.Type
		control types.Control
		calls   bool
	}{
		{"factory", factory, callback, types.Control{}, false},
		{"executor", callback, b.Int, types.FunctionControl(callback), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := Def{Name: test.name, Type: &types.TFun{Arg: test.arg, Ret: test.result}, Params: []string{"f"}, Body: &App{Callee: &VarRef{Name: "f", Local: true, Ty: test.arg}, CalleeKind: Value, Args: []Expr{&UnitLit{Ty: b.Unit}}, Ty: test.result, Control: test.control}}
			p := &Prog{Defs: []Def{d}}
			SummarizeABI(p, nil)
			if !p.Defs[0].ABI.NeedsFamily || p.Defs[0].ABI.CallsControlledArg != test.calls {
				t.Fatalf("summary = %#v", p.Defs[0].ABI)
			}
		})
	}
}
