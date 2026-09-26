package codegen

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func TestOrdinaryEmitterDoesNotAcquireMachineRuntime(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	p := &core.Prog{Entry: "Main.main", Defs: []core.Def{{Name: "Main.main", Owner: "Main", Type: b.Int,
		Body: &core.IntLit{Val: 1, Ty: b.Int}}}}
	files, err := EmitProject(p, b, []Unit{{Name: "Main", Program: "Main", Entry: true}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(files[0].Data); strings.Contains(got, "Machine") {
		t.Fatalf("direct output acquired Machine support:\n%s", got)
	}
}
