package core

import (
	"fmt"
	"strings"
	"testing"

	"github.com/waj/fango/internal/types"
)

func TestScopedCallProof(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		mutate     func(*App, *types.TFun, *types.TFun)
	}{
		{name: "valid"},
		{"missing signature", "missing source signature", func(c *App, _, _ *types.TFun) { c.SourceType = nil }},
		{"partial call", "arity mismatch", func(c *App, _, _ *types.TFun) { c.Args = nil }},
		{"missing permission", "exactly one fresh", func(_ *App, _ *types.TFun, cb *types.TFun) { cb.Eff = types.Row{} }},
		{"extra permission", "exactly one fresh", func(_ *App, _ *types.TFun, cb *types.TFun) {
			cb.Eff.Labels = append(cb.Eff.Labels, types.EffLabel{Unique: 1001, Name: "local", Scoped: true})
		}},
		{"returned callback", "scope escapes", func(_ *App, fn, cb *types.TFun) { fn.Ret = cb }},
		{"callback result", "scope escapes", func(_ *App, _ *types.TFun, cb *types.TFun) {
			cb.Ret = &types.TFun{Arg: cb.Arg, Eff: cb.Eff, Ret: cb.Ret}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sup := &types.Supply{}
			b := types.NewBuiltins(sup)
			cb := &types.TFun{Arg: b.Unit, Ret: b.Int, Eff: types.Row{Labels: []types.EffLabel{{Unique: 1000, Name: "local", Scoped: true}}}}
			fn := &types.TFun{Arg: cb, Ret: b.Int}
			call := &App{CalleeKind: Worker, Callee: &VarRef{Name: "run"}, Args: []Expr{&UnitLit{Ty: b.Unit}}, SourceType: fn, Ty: b.Int}
			if tc.mutate != nil {
				tc.mutate(call, fn, cb)
			}
			p := &Prog{Defs: []Def{{Name: "main", Body: call}}}
			errs := checkScopedCalls(p, []Def{{Name: "run", Params: []string{"use"}, Scoped: true}})
			if tc.want == "" {
				if len(errs) > 0 {
					t.Fatal(errs)
				}
			} else if !strings.Contains(fmt.Sprint(errs), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, errs)
			}
		})
	}
}

func TestScopedPermissionMustErase(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	l := &linter{b: b}
	l.typ(&types.TFun{Arg: b.Unit, Ret: b.Int, Eff: types.Row{Labels: []types.EffLabel{{Unique: 1000, Name: "local", Scoped: true}}}}, "test")
	if !strings.Contains(fmt.Sprint(l.errs), "scoped permission survived erasure") {
		t.Fatal(l.errs)
	}
}
