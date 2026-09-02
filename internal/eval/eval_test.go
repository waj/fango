package eval

import (
	"context"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func intTy() *types.TCon { return &types.TCon{Unique: 0, Name: "Int"} }

func TestArith(t *testing.T) {
	ty := intTy()
	// 1 + 2 * 3
	e := &core.BinOp{Op: "+", Ty: ty,
		L: &core.IntLit{Val: 1, Ty: ty},
		R: &core.BinOp{Op: "*", Ty: ty,
			L: &core.IntLit{Val: 2, Ty: ty},
			R: &core.IntLit{Val: 3, Ty: ty}}}
	v, err := Eval(context.Background(), e, NewEnv())
	if err != nil {
		t.Fatal(err)
	}
	if v != int64(7) {
		t.Errorf("got %v, want 7", v)
	}
}

func TestLazyMemoCells(t *testing.T) {
	ty := intTy()
	env := NewEnv()
	env.Define("x", &core.IntLit{Val: 40, Ty: ty})
	env.Define("y", &core.BinOp{Op: "+", Ty: ty,
		L: &core.VarRef{Name: "x", Ty: ty},
		R: &core.IntLit{Val: 2, Ty: ty}})
	v, err := Force(context.Background(), "y", env)
	if err != nil {
		t.Fatal(err)
	}
	if v != int64(42) {
		t.Errorf("got %v, want 42", v)
	}
	// Redefining x must not change the already-forced y (memoization), but
	// forcing x afterwards sees the new body — the REPL cell model.
	env.Define("x", &core.IntLit{Val: 100, Ty: ty})
	v, _ = Force(context.Background(), "y", env)
	if v != int64(42) {
		t.Errorf("memoized y changed: got %v", v)
	}
	v, _ = Force(context.Background(), "x", env)
	if v != int64(100) {
		t.Errorf("redefined x: got %v", v)
	}
}

func TestSelfDependency(t *testing.T) {
	ty := intTy()
	env := NewEnv()
	env.Define("x", &core.BinOp{Op: "+", Ty: ty,
		L: &core.VarRef{Name: "x", Ty: ty},
		R: &core.IntLit{Val: 1, Ty: ty}})
	if _, err := Force(context.Background(), "x", env); err == nil {
		t.Error("expected a self-dependency error")
	}
}

// Switch totality (DESIGN.md §13 risk: semantic drift): every Core node
// kind must be *named* in the interpreter's switch — nodes from future
// slices answer with a deliberate "arrives in Sn" error, never the generic
// unhandled fallback.
func TestSwitchTotality(t *testing.T) {
	ty := intTy()
	nodes := []core.Expr{
		&core.IntLit{Val: 1, Ty: ty},
		&core.VarRef{Name: "missing", Ty: ty},
		&core.BinOp{Op: "+", Ty: ty, L: &core.IntLit{Val: 1, Ty: ty}, R: &core.IntLit{Val: 2, Ty: ty}},
		&core.App{CalleeKind: core.Worker, Ty: ty},
	}
	for _, n := range nodes {
		_, err := Eval(context.Background(), n, NewEnv())
		if err != nil && strings.Contains(err.Error(), "unhandled Core node") {
			t.Errorf("%T hit the unhandled fallback — add it to the interpreter switch", n)
		}
	}
}

func TestCancellation(t *testing.T) {
	ty := intTy()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Deep enough to cross the poll interval.
	var e core.Expr = &core.IntLit{Val: 0, Ty: ty}
	for i := 0; i < pollEvery+8; i++ {
		e = &core.BinOp{Op: "+", Ty: ty, L: e, R: &core.IntLit{Val: 1, Ty: ty}}
	}
	if _, err := Eval(ctx, e, NewEnv()); err == nil {
		t.Error("expected interruption")
	}
}
