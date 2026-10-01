package core

import (
	"testing"

	"github.com/waj/fango/internal/types"
)

func TestCallbackContractsPreserveExecutionAndEscapeBoundaries(t *testing.T) {
	supply := &types.Supply{}
	b := types.NewBuiltins(supply)
	poly := types.Control{Polymorphic: true}
	callback := &types.TFun{Arg: b.Int, Ret: &types.TFun{Arg: b.Int, Ret: b.Int, Control: poly}}
	ref := func() Expr { return &VarRef{Name: "f", Local: true, Ty: callback} }
	atom := &IntLit{Val: 1, Ty: b.Int}
	call := func(arg Expr, control types.Control) Expr {
		first := &App{CalleeKind: Value, Callee: ref(), Args: []Expr{atom}, Ty: callback.Ret}
		return &App{CalleeKind: Value, Callee: first, Args: []Expr{arg}, Ty: b.Int, Control: control}
	}
	for _, tc := range []struct {
		name string
		body Expr
		want int
	}{
		{"saturated", call(atom, poly), 2},
		{"later computation", call(&NativeCall{Name: "compute", Ty: b.Int}, poly), 0},
		{"stored result", ref(), 0},
		{"partial application", &App{CalleeKind: Value, Callee: ref(), Args: []Expr{atom}, Ty: callback.Ret}, 0},
		{"captured", &Lambda{Param: "x", Body: call(atom, poly), Ty: &types.TFun{Arg: b.Int, Ret: b.Int}}, 0},
		{"mixed transport", &Seq{First: call(atom, types.Control{}), Then: call(atom, poly), Ty: b.Int}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := Def{Name: "use", Params: []string{"f"}, Type: &types.TFun{Arg: callback, Ret: tc.body.Type()}, Body: tc.body}
			p := &Prog{Defs: []Def{d}}
			SummarizeABI(p, nil)
			got := p.Defs[0].ABI.Callbacks[0]
			if got.Arity != tc.want {
				t.Fatalf("contract = %+v, want arity %d", got, tc.want)
			}
			if tc.want > 0 {
				if mode, ok := got.Mode(types.Exit); !ok || mode != types.Exit {
					t.Fatalf("Exit contract = %+v", got)
				}
			}
		})
	}
}

func TestCallbackContractsSolveRecursiveForwardingWithoutDependencyBodies(t *testing.T) {
	supply := &types.Supply{}
	b := types.NewBuiltins(supply)
	fn := &types.TFun{Arg: b.Int, Ret: b.Int}
	worker := &types.TFun{Arg: fn, Ret: b.Int}
	forward := func(name string) Expr {
		return &App{CalleeKind: Worker, Callee: &VarRef{Name: name, Ty: worker}, Args: []Expr{&VarRef{Name: "f", Local: true, Ty: fn}}, Ty: b.Int}
	}
	call := &App{CalleeKind: Value, Callee: &VarRef{Name: "f", Local: true, Ty: fn}, Args: []Expr{&IntLit{Val: 1, Ty: b.Int}}, Ty: b.Int}
	p := &Prog{Defs: []Def{
		{Name: "relay", Type: worker, Params: []string{"f"}, Body: forward("loop")},
		{Name: "loop", Type: worker, Params: []string{"f"}, Body: &Seq{First: call, Then: forward("relay"), Ty: b.Int}},
	}}
	SummarizeABI(p, nil)
	for _, d := range p.Defs {
		if mode, ok := d.ABI.Callbacks[0].Mode(types.Exit); !ok || mode != types.Direct {
			t.Fatalf("%s: %+v", d.Name, d.ABI.Callbacks)
		}
	}
	dependency := p.Defs[0]
	dependency.Body = nil
	dependent := &Prog{Defs: []Def{{Name: "importer", Type: worker, Params: []string{"f"}, Body: forward("relay")}}}
	SummarizeABI(dependent, []Def{dependency})
	if got := dependent.Defs[0].ABI.Callbacks[0]; got != dependency.ABI.Callbacks[0] {
		t.Fatalf("imported forwarding = %+v, want %+v", got, dependency.ABI.Callbacks[0])
	}
}
