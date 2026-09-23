package elaborate

import (
	"testing"

	"github.com/waj/fango/internal/types"
)

func TestRuntimeRigidVarsEraseNominalRowsButKeepExecutingEvidence(t *testing.T) {
	value := &types.TVar{ID: 1, Rigid: true}
	phantom := &types.TVar{ID: 2, Rigid: true}
	effectArg := &types.TVar{ID: 3, Rigid: true}
	rowTail := &types.TVar{ID: 4, Rigid: true, Kind: types.RowVar}
	row := func(arg types.Type) types.Row {
		return types.Row{Labels: []types.EffLabel{{Name: "Fail", Args: []types.Type{arg}, Abort: true}}}
	}
	completion := &types.TCon{Name: types.CompletionTypeName, Args: []types.Type{value, row(phantom)}}
	wrapper := &types.TCon{Name: "Wrapper", Args: []types.Type{completion, rowTail}}
	fn := &types.TFun{Arg: wrapper, Eff: row(effectArg), Ret: value}
	got := runtimeRigidVars(fn)
	if len(got) != 2 || got[0] != value || got[1] != effectArg {
		t.Fatalf("runtime parameters: %v", got)
	}
	// A variable erased from the nominal row still needs a runtime parameter
	// when the same variable also supplies executing evidence.
	fn.Eff = row(phantom)
	got = runtimeRigidVars(fn)
	if len(got) != 2 || got[0] != value || got[1] != phantom {
		t.Fatalf("executing parameter erased: %v", got)
	}
}
