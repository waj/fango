package core

import (
	"github.com/waj/fango/internal/types"
	"testing"
)

func TestWrapperTemplatesAreBoundedAndExcludeCycles(t *testing.T) {
	b := types.NewBuiltins(&types.Supply{})
	call := func(name string) Expr {
		return &App{CalleeKind: Worker, Callee: &VarRef{Name: name}, Args: []Expr{&VarRef{Name: "x", Local: true, Ty: b.Int}}, Ty: b.Int}
	}
	p := &Prog{Defs: []Def{
		{Name: "identity", Params: []string{"x"}, Control: types.Control{Transport: types.Machine}, Body: &VarRef{Name: "x", Local: true, Ty: b.Int}},
		{Name: "left", Params: []string{"x"}, Control: types.Control{Transport: types.Machine}, Body: call("right")},
		{Name: "right", Params: []string{"x"}, Control: types.Control{Transport: types.Machine}, Body: call("left")},
		{Name: "owner", Params: []string{"x"}, Control: types.Control{Transport: types.Machine}, Body: &CoroutineScope{Ty: b.Int}},
	}}
	ExportWrapperTemplates(p)
	if p.Defs[0].InlineBody == nil {
		t.Fatal("identity wrapper not exported")
	}
	for _, d := range p.Defs[1:] {
		if d.InlineBody != nil {
			t.Fatalf("unsafe template exported: %s", d.Name)
		}
	}
	large := p.Defs[0]
	for i := 0; i < WrapperNodeLimit; i++ {
		large.Body = &Seq{First: &UnitLit{Ty: b.Unit}, Then: large.Body, Ty: b.Int}
	}
	if WrapperSize(&large, large.Body) != 0 {
		t.Fatal("oversized template accepted")
	}
}
