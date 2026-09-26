package infer

import (
	"testing"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// The solver asks for execution needs once per fixed-point iteration. A
// repeat reuses the previous interpretation only when the definitions it
// builds are the same; a substitution that refines the root's type must be
// interpreted again, however many iterations came before.
func TestExecutionNeedsReinterpretWhenSubstitutionChangesRoot(t *testing.T) {
	sup := &types.Supply{}
	ck := NewChecker(sup, types.NewBuiltins(sup), NewEnv())
	ck.Intrinsics[types.WorkRunName] = types.Scheme{Body: ck.B.Unit}
	runs := 0
	ck.ObserveFlow = func(core.FlowRun) { runs++ }
	a := &types.TVar{ID: 1}
	root := DeclInfo{
		Name:   "id",
		Params: []ast.Pattern{&ast.PVar{Name: "x"}},
		Body:   &ast.Var{Name: "x"},
		Type:   &types.TFun{Arg: a, Ret: a},
	}
	g := &generator{ck: ck, executionRoots: []DeclInfo{root}}
	for _, step := range []struct {
		name string
		sub  Subst
		runs int
	}{
		{"first iteration", Subst{}, 1},
		{"unchanged", Subst{}, 1},
		{"root refined", Subst{1: ck.B.Int}, 2},
		{"refinement repeated", Subst{1: ck.B.Int}, 2},
		{"unrelated binding", Subst{1: ck.B.Int, 2: ck.B.Bool}, 2},
		{"root refined differently", Subst{1: ck.B.String}, 3},
		{"back to the first", Subst{}, 4},
	} {
		g.executionNeeds(step.sub)
		if runs != step.runs {
			t.Fatalf("%s: %d interpretations, want %d", step.name, runs, step.runs)
		}
	}
}
