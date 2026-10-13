package elaborate

import (
	"testing"

	"github.com/waj/fango/internal/types"
)

func TestGenericEffectArgumentKeepsNominalShape(t *testing.T) {
	token := &types.TVar{ID: 1, Rigid: true}
	value := &types.TVar{ID: 2, Rigid: true}
	unit := &types.TCon{Unique: 1, Name: "Unit"}
	text := &types.TCon{Unique: 2, Name: "String"}
	char := &types.TCon{Unique: 3, Name: "Char"}
	parseError := func(arg types.Type) types.Type {
		return &types.TCon{Unique: 4, Name: "Error", Args: []types.Type{arg}}
	}
	fail := func(arg types.Type) types.EffLabel {
		return types.EffLabel{Unique: 5, Name: "Fail", Abort: true, Args: []types.Type{arg}}
	}
	input := func(arg types.Type) types.EffLabel {
		return types.EffLabel{Unique: 6, Name: "Parse", Args: []types.Type{arg}}
	}
	// The token parameter is determined only by evidence. An unrelated Fail
	// String must not instantiate Error token, even before token is assigned.
	generic := &types.TFun{Arg: unit, Ret: value, Eff: types.Row{Labels: []types.EffLabel{fail(parseError(token)), input(token)}}}
	actual := &types.TFun{Arg: unit, Ret: char, Eff: types.Row{Labels: []types.EffLabel{fail(text), fail(parseError(char)), input(char)}}}
	got := matchTyArgs(generic, []*types.TVar{token, value}, actual)
	if !types.Equal(got[0], char) || !types.Equal(got[1], char) {
		t.Fatalf("type arguments: %v", got)
	}
	// A token already assigned to a rigid occurrence variable is a fixed
	// identity, even when the first same-shaped label uses another variable.
	a := &types.TVar{ID: 30, Rigid: true}
	b := &types.TVar{ID: 31, Rigid: true}
	generic.Arg = token
	actual.Arg = b
	actual.Eff = types.Row{Labels: []types.EffLabel{fail(parseError(a)), fail(parseError(b)), input(b)}}
	if selected := matchAssignedRowLabel(fail(parseError(token)), actual.Eff.Labels, make([]bool, 3), map[int]types.Type{token.ID: b}); selected != 1 {
		t.Fatalf("selected effect %d, want the application with the assigned rigid argument", selected)
	}
	got = matchTyArgs(generic, []*types.TVar{token, value}, actual)
	if !types.Equal(got[0], b) || !types.Equal(got[1], char) {
		t.Fatalf("rigid occurrence arguments: %v", got)
	}
	// Preserve distinctions inside function-valued effect arguments as well.
	io := types.Row{Labels: []types.EffLabel{{Unique: 7, Name: "IO"}}}
	patternFn := &types.TFun{Arg: unit, Eff: io, Ret: token}
	pureFn := &types.TFun{Arg: unit, Ret: char}
	ioFn := &types.TFun{Arg: unit, Eff: io, Ret: char}
	labels := []types.EffLabel{fail(pureFn), fail(ioFn)}
	if selected := matchAssignedRowLabel(fail(patternFn), labels, make([]bool, 2), nil); selected != 1 {
		t.Fatalf("selected effect %d, want the function with IO", selected)
	}
}
