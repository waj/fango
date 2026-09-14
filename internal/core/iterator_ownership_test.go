package core

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/types"
)

func TestIteratorOwnershipRejectsDuplicateAndEscapingCursorUses(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	cursorTy := &types.TCon{Unique: sup.NextUnique(), Name: "Iterator.Iterator", Args: []types.Type{b.Int}}
	consumerTy := &types.TFun{Arg: cursorTy, Ret: b.Unit}
	forEachTy := &types.TFun{Arg: cursorTy, Ret: b.Unit}
	consume := func(cursor *VarRef) Expr {
		return &App{CalleeKind: Worker, Callee: &VarRef{Name: "Iterator.forEach", Ty: forEachTy}, Args: []Expr{cursor}, Ty: b.Unit}
	}
	withIteratorTy := &types.TFun{Arg: &types.TFun{Arg: b.Unit, Ret: b.Unit}, Ret: &types.TFun{Arg: consumerTy, Ret: b.Unit}}
	call := func(body Expr) Expr {
		producerTy := withIteratorTy.Arg
		return &App{CalleeKind: Worker, Callee: &VarRef{Name: types.GeneratorWithIteratorName, Ty: withIteratorTy}, Args: []Expr{
			&Lambda{Param: "_", ParamCapture: sup.FreshCapture(), Ty: producerTy, Body: &UnitLit{Ty: b.Unit}},
			&Lambda{Param: "cursor", ParamCapture: sup.FreshCapture(), Ty: consumerTy, Body: body},
		}, Ty: b.Unit}
	}
	cursor := func() *VarRef { return &VarRef{Name: "cursor", Local: true, Ty: cursorTy} }
	tests := []struct {
		name string
		body Expr
		want string
	}{
		{name: "once", body: consume(cursor())},
		{name: "duplicate", body: &Seq{First: consume(cursor()), Then: consume(cursor()), Ty: b.Unit}, want: "consumed 2 times"},
		{name: "alias", body: &Let{Name: "alias", Rhs: cursor(), Body: &UnitLit{Ty: b.Unit}, Ty: b.Unit}, want: "escapes or is used"},
		{name: "captured", body: &Lambda{Param: "_", ParamCapture: sup.FreshCapture(), Ty: &types.TFun{Arg: b.Unit, Ret: cursorTy}, Body: cursor()}, want: "escapes or is used"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Prog{Defs: []Def{{Name: "Main.main", Body: call(tt.body)}}}
			errs := verifyIteratorOwnership(p)
			got := ""
			for _, err := range errs {
				got += err.Error() + "\n"
			}
			if tt.want == "" && got != "" {
				t.Fatalf("unexpected errors: %s", got)
			}
			if tt.want != "" && !strings.Contains(got, tt.want) {
				t.Fatalf("errors = %q, want %q", got, tt.want)
			}
		})
	}
}
