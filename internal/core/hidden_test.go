package core

import (
	"github.com/waj/fango/internal/types"
	"strings"
	"testing"
)

func hiddenFixture() (*Prog, *types.Builtins, *App, *Case) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	declared, opened := sup.FreshRigid(types.General), sup.FreshRigid(types.General)
	con := &types.TCon{Name: "Box", Unique: sup.NextUnique()}
	ctor := &types.CtorInfo{Name: "Box", Result: con, Hidden: []*types.TVar{declared}, Fields: []types.Type{declared}}
	adt := &types.ADTInfo{Con: con, Ctors: []*types.CtorInfo{ctor}}
	pack := &App{CalleeKind: Ctor, Ctor: ctor, Callee: &VarRef{Name: "Box", Ty: &types.TFun{Arg: b.Int, Ret: con}}, Witnesses: []types.Type{b.Int}, Args: []Expr{&IntLit{Val: 1, Ty: b.Int}}, Ty: con}
	open := &Case{Scrut: pack, Bind: "box", Ty: con, Tree: &SwitchCtor{Scrut: "box", ADT: adt, Cases: []CtorCase{{Ctor: ctor, Hidden: []*types.TVar{opened}, Binds: []string{"value"}, Tree: &Leaf{Body: &App{CalleeKind: Ctor, Ctor: ctor, Callee: &VarRef{Name: "Box", Ty: &types.TFun{Arg: opened, Ret: con}}, Witnesses: []types.Type{opened}, Args: []Expr{&VarRef{Name: "value", Ty: opened, Local: true}}, Ty: con}}}}}}
	return &Prog{ADTs: []*types.ADTInfo{adt}, Defs: []Def{{Name: "main", Type: con, Body: open}}}, b, pack, open
}

func TestLintHiddenProofs(t *testing.T) {
	p, b, _, _ := hiddenFixture()
	if errs := Lint(p, b); len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, tc := range []struct {
		name, diagnostic string
		mutate           func(*Prog, *types.Builtins, *App, *Case)
	}{
		{"missing witness", "witness count", func(_ *Prog, _ *types.Builtins, a *App, _ *Case) { a.Witnesses = nil }},
		{"wrong witness", "arg 1 typed", func(_ *Prog, b *types.Builtins, a *App, _ *Case) { a.Witnesses[0] = b.Bool }},
		{"missing binder", "binder count", func(_ *Prog, _ *types.Builtins, _ *App, c *Case) { c.Tree.(*SwitchCtor).Cases[0].Hidden = nil }},
		{"nonrigid binder", "fresh ordinary", func(_ *Prog, _ *types.Builtins, _ *App, c *Case) {
			c.Tree.(*SwitchCtor).Cases[0].Hidden[0].Rigid = false
		}},
		{"row binder", "fresh ordinary", func(_ *Prog, _ *types.Builtins, _ *App, c *Case) {
			c.Tree.(*SwitchCtor).Cases[0].Hidden[0].Kind = types.RowVar
		}},
		{"escape", "rigid variable", func(p *Prog, _ *types.Builtins, _ *App, c *Case) {
			branch := &c.Tree.(*SwitchCtor).Cases[0]
			c.Ty = branch.Hidden[0]
			p.Defs[0].Type = c.Ty
			branch.Tree = &Leaf{Body: &VarRef{Name: "value", Local: true, Ty: c.Ty}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, b, a, c := hiddenFixture()
			tc.mutate(p, b, a, c)
			if got := lintText(p, b); !strings.Contains(got, tc.diagnostic) {
				t.Fatalf("wanted %s, got %s", tc.diagnostic, got)
			}
		})
	}
}
