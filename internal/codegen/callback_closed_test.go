package codegen

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
	"testing"
)

func TestClosedCallbackChecksTermsAndDescriptors(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	a := sup.FreshRigid(types.General)
	g := &gen{tyParamNames: map[int]string{a.ID: "A0"}}
	fn := &types.TFun{Arg: b.Int, Ret: b.Int}
	ref := func(name string, ty types.Type) core.Expr { return &core.VarRef{Name: name, Local: true, Ty: ty} }
	for _, tc := range []struct {
		name   string
		body   core.Expr
		closed bool
	}{
		{"own argument", ref("x", b.Int), true},
		{"local binding", &core.Let{Name: "y", Rhs: ref("x", b.Int), Body: ref("y", b.Int), Ty: b.Int}, true},
		{"outer capture", ref("outer", b.Int), false},
		{"initializer capture", &core.Let{Name: "outer", Rhs: ref("outer", b.Int), Body: ref("outer", b.Int), Ty: b.Int}, false},
		{"descriptor capture", &core.Let{Name: "y", Rhs: ref("outer", a), Body: ref("x", b.Int), Ty: b.Int}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lam := &core.Lambda{Param: "x", Body: tc.body, Ty: fn}
			if got := g.closedMonomorphicCallback(lam); got != tc.closed {
				t.Fatalf("closed=%t want %t", got, tc.closed)
			}
		})
	}
}
