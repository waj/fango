package lower

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func TestModuleRejectsStaleCallableContractWithoutChangingCore(t *testing.T) {
	supply := &types.Supply{}
	b := types.NewBuiltins(supply)
	fn := &types.TFun{Arg: b.Int, Ret: b.Int}
	call := &core.App{CalleeKind: core.Value, Callee: &core.VarRef{Name: "f", Local: true, Ty: fn}, Args: []core.Expr{&core.IntLit{Val: 1, Ty: b.Int}}, Ty: b.Int}
	p := &core.Prog{Defs: []core.Def{{Name: "M.use", Owner: "M", Type: &types.TFun{Arg: fn, Ret: b.Int}, Params: []string{"f"}, Body: call}}}
	core.SummarizeABI(p, nil)
	before := core.Dump(p)
	first, err := Module(p, "M")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Module(p, "M")
	if err != nil {
		t.Fatal(err)
	}
	if core.Dump(p) != before || Dump(first, p.Defs) != Dump(second, p.Defs) {
		t.Fatal("lowering mutated Core or produced unstable decisions")
	}
	p.Defs[0].ABI.Callbacks[0].Arity = 2
	if _, err := Module(p, "M"); err == nil || !strings.Contains(err.Error(), "stale callback") {
		t.Fatalf("got %v, want stale contract rejection", err)
	}
}

func TestVerifierRejectsContinueOutsideItsLoop(t *testing.T) {
	b := types.NewBuiltins(&types.Supply{})
	d := &core.Def{Name: "M.f", Type: b.Int}
	f := &Function{Definition: d, Body: Block{Continue{&core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: d.Name}}}}}
	if err := Verify(f); err == nil {
		t.Fatal("accepted a continue outside a loop")
	}
}

func TestVerifierRejectsMissingOrMismatchedResult(t *testing.T) {
	b := types.NewBuiltins(&types.Supply{})
	d := &core.Def{Name: "M.f", Type: b.Int}
	for _, body := range []Block{nil, {Eval{&core.UnitLit{Ty: b.Unit}}}, {Return{&core.UnitLit{Ty: b.Unit}}}, {Return{&core.IntLit{Val: 1, Ty: b.Int}}, Eval{&core.UnitLit{Ty: b.Unit}}}} {
		if err := Verify(&Function{Definition: d, Body: body}); err == nil {
			t.Fatalf("accepted invalid block %#v", body)
		}
	}
}
