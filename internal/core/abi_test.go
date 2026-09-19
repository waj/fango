package core

import (
	"testing"

	"github.com/waj/fango/internal/types"
)

func TestABISummaryUsesDependencyMetadataWithoutBody(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	callback := &types.TFun{Arg: b.Unit, Eff: types.Row{Labels: []types.EffLabel{{Unique: 99, Name: "E"}}}, Ret: b.Unit}
	dep := Def{Name: "Dep.factory", Type: &types.TFun{Arg: callback, Ret: callback}, Params: []string{"f"}, ABI: ABISummary{Valid: true, NeedsFamily: true, PassiveMachineFactory: true}}
	own := Def{Name: "Main.factory", Type: dep.Type, Params: []string{"f"}, Body: &App{Callee: &VarRef{Name: dep.Name, Ty: dep.Type}, CalleeKind: Worker, Args: []Expr{&VarRef{Name: "f", Local: true, Ty: callback}}, Ty: callback}}
	p := &Prog{Defs: []Def{own}}
	SummarizeABI(p, []Def{dep})
	if !p.Defs[0].ABI.Valid || !p.Defs[0].ABI.NeedsFamily || !p.Defs[0].ABI.PassiveMachineFactory {
		t.Fatalf("summary = %#v", p.Defs[0].ABI)
	}
}
