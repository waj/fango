package eval

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/types"
)

func TestMachineIteratorSessionClosesSuspendedCleanupScope(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	resource := &core.VarRef{Name: "resource", Local: true, Ty: b.Int}
	body := &core.Let{Name: "first", Rhs: &core.Suspend{Request: resource, Ty: b.Unit}, Ty: b.Unit,
		Body: &core.Suspend{Request: resource, Ty: b.Unit}}
	bracket := &core.Bracket{Scope: sup.FreshScope(), Resource: "resource", ResourceTy: b.Int,
		Acquire: &core.IntLit{Val: 7, Ty: b.Int}, Release: &core.UnitLit{Ty: b.Unit}, Body: body, Ty: b.Unit,
		Control: types.Control{Transport: types.Machine}}
	p := &core.Prog{Defs: []core.Def{{Name: types.ScopeBracketName, Owner: "Scope", Type: b.Unit,
		Control: types.Control{Transport: types.Machine}, Body: bracket}}}
	mp, errs := machineir.Lower(p, b)
	if len(errs) != 0 {
		t.Fatalf("machine lowering: %v", errs)
	}
	env := NewEnv()
	env.DefineProg(p)
	it, err := StartMachineIterator(context.Background(), mp, types.ScopeBracketName, nil, env, NewIOContext(strings.NewReader(""), io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	value, yielded, exit, err := it.Next()
	if err != nil || !yielded || exit != nil || value != int64(7) {
		t.Fatalf("next = %#v, %v/%v/%v", value, yielded, exit, err)
	}
	if exit, err := it.Close(); err != nil || exit != nil {
		t.Fatalf("close = %#v, %v", exit, err)
	}
	if !it.session.finished || len(it.session.frames) != 0 || len(it.session.cleanups) != 0 {
		t.Fatalf("iterator retained machine state")
	}
}
