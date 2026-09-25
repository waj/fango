package machine

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func synchronousScopeFixture(t *testing.T) (*Prog, *types.Builtins) {
	t.Helper()
	sup, b := testBuiltins()
	poly := types.Control{Polymorphic: true}
	acquire := &types.TFun{Arg: b.Unit, Ret: b.Int, Control: poly}
	release := &types.TFun{Arg: b.Int, Ret: b.Unit, Control: poly}
	body := &types.TFun{Arg: b.Int, Ret: b.Int, Control: poly}
	resource := &core.VarRef{Name: "resource", Local: true, Ty: b.Int}
	invoke := func(name string, fn *types.TFun, arg core.Expr) core.Expr {
		return &core.App{CalleeKind: core.Value, Callee: &core.VarRef{Name: name, Local: true, Ty: fn}, Args: []core.Expr{arg}, Ty: fn.Ret, Control: poly}
	}
	d := core.Def{Name: types.ScopeBracketName, Type: &types.TFun{Arg: acquire, Ret: &types.TFun{Arg: release, Ret: &types.TFun{Arg: body, Ret: b.Int, Control: poly}}}, Params: []string{"acquire", "release", "body"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture(), sup.FreshCapture(), sup.FreshCapture()}, Control: poly,
		Body: &core.Bracket{Scope: sup.FreshScope(), Resource: "resource", ResourceTy: b.Int, Acquire: invoke("acquire", acquire, &core.UnitLit{Ty: b.Unit}), Release: invoke("release", release, resource), Body: invoke("body", body, resource), Ty: b.Int, Control: poly}}
	p := &core.Prog{Defs: []core.Def{d}, Intrinsics: map[string]bool{types.ScopeBracketName: true}}
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatal(errs)
	}
	mp, errs := Lower(p, b)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	return mp, b
}

func TestMachineScopeChecksSynchronousCallbackProofs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		damage func(*Worker)
		want   string
	}{
		{"valid", func(*Worker) {}, ""},
		{"missing parameter proof", func(w *Worker) { w.SynchronousParams = nil }, "invalid synchronous callback parameters"},
		{"wrong slot", func(w *Worker) { w.SynchronousParams = []int{0} }, "invalid synchronous callback parameters"},
		{"stale scope", func(w *Worker) { w.Def.Body.(*core.Bracket).Scope++ }, "stale synchronous source contract"},
		{"stale acquisition", func(w *Worker) {
			w.Def.Body.(*core.Bracket).Acquire.(*core.App).Callee.(*core.VarRef).Name = "unchecked"
		}, "stale synchronous source contract"},
		{"missing source", func(w *Worker) { w.Def = nil }, "missing synchronous source contract"},
		{"machine release", func(w *Worker) { w.Params[1].Ty.(*types.TFun).Control = types.Control{Transport: types.Machine} }, "synchronous parameter must use Exit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := synchronousScopeFixture(t)
			tc.damage(&p.Workers[0])
			var messages []string
			for _, err := range Lint(p) {
				messages = append(messages, err.Error())
			}
			got := strings.Join(messages, "\n")
			if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("errors %q, want %q", got, tc.want)
			}
		})
	}
}
