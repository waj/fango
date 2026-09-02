package eval

import (
	"bytes"
	"context"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func intTy() *types.TCon    { return &types.TCon{Unique: 0, Name: "Int"} }
func floatTy() *types.TCon  { return &types.TCon{Unique: 1, Name: "Float"} }
func stringTy() *types.TCon { return &types.TCon{Unique: 2, Name: "String"} }
func boolTy() *types.TCon   { return &types.TCon{Unique: 3, Name: "Bool"} }
func unitTy() *types.TCon   { return &types.TCon{Unique: 4, Name: "()"} }

func run(t *testing.T, e core.Expr) Value {
	t.Helper()
	v, err := Eval(context.Background(), e, NewEnv(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestArith(t *testing.T) {
	ty := intTy()
	// 1 + 2 * 3
	e := &core.BinOp{Op: "+", Ty: ty,
		L: &core.IntLit{Val: 1, Ty: ty},
		R: &core.BinOp{Op: "*", Ty: ty,
			L: &core.IntLit{Val: 2, Ty: ty},
			R: &core.IntLit{Val: 3, Ty: ty}}}
	if v := run(t, e); v != int64(7) {
		t.Errorf("got %v, want 7", v)
	}
}

func TestTypedOps(t *testing.T) {
	it, ft, st, bt := intTy(), floatTy(), stringTy(), boolTy()
	cases := []struct {
		e    core.Expr
		want Value
	}{
		// Int wraps like int64.
		{&core.BinOp{Op: "+", Ty: it,
			L: &core.IntLit{Val: 9223372036854775807, Ty: it},
			R: &core.IntLit{Val: 1, Ty: it}}, int64(-9223372036854775808)},
		// Float division is IEEE: no panic, +Inf.
		{&core.BinOp{Op: "/", Ty: ft,
			L: &core.FloatLit{Val: 1.0, Ty: ft},
			R: &core.FloatLit{Val: 0.0, Ty: ft}}, math.Inf(1)},
		{&core.BinOp{Op: "++", Ty: st,
			L: &core.StringLit{Val: "foo", Ty: st},
			R: &core.StringLit{Val: "bar", Ty: st}}, "foobar"},
		{&core.BinOp{Op: "<", Ty: bt,
			L: &core.StringLit{Val: "a", Ty: st},
			R: &core.StringLit{Val: "b", Ty: st}}, true},
		{&core.BinOp{Op: "==", Ty: bt,
			L: &core.BoolLit{Val: true, Ty: bt},
			R: &core.BoolLit{Val: false, Ty: bt}}, false},
		{&core.Neg{Operand: &core.FloatLit{Val: 2.5, Ty: ft}, Ty: ft}, -2.5},
		{&core.If{Ty: it,
			Cond: &core.BoolLit{Val: true, Ty: bt},
			Then: &core.IntLit{Val: 1, Ty: it},
			Else: &core.IntLit{Val: 2, Ty: it}}, int64(1)},
	}
	for _, c := range cases {
		if v := run(t, c.e); v != c.want {
			t.Errorf("got %v, want %v", v, c.want)
		}
	}
}

// The untaken branch must not evaluate — its reference to a missing name
// would error if it did.
func TestIfBranchLaziness(t *testing.T) {
	it, bt := intTy(), boolTy()
	e := &core.If{Ty: it,
		Cond: &core.BoolLit{Val: true, Ty: bt},
		Then: &core.IntLit{Val: 1, Ty: it},
		Else: &core.VarRef{Name: "missing", Ty: it}}
	if v := run(t, e); v != int64(1) {
		t.Errorf("got %v, want 1", v)
	}
}

func TestPrintWritesThroughFangort(t *testing.T) {
	ft := floatTy()
	var buf bytes.Buffer
	e := &core.Print{Ty: unitTy(),
		Arg: &core.BinOp{Op: "*", Ty: ft,
			L: &core.FloatLit{Val: 3.14159, Ty: ft},
			R: &core.FloatLit{Val: 4.0, Ty: ft}}}
	if _, err := Eval(context.Background(), e, NewEnv(), &buf); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "12.56636\n" {
		t.Errorf("printed %q, want %q", got, "12.56636\n")
	}
}

func TestLazyMemoCells(t *testing.T) {
	ty := intTy()
	env := NewEnv()
	env.Define("x", &core.IntLit{Val: 40, Ty: ty})
	env.Define("y", &core.BinOp{Op: "+", Ty: ty,
		L: &core.VarRef{Name: "x", Ty: ty},
		R: &core.IntLit{Val: 2, Ty: ty}})
	v, err := Force(context.Background(), "y", env, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if v != int64(42) {
		t.Errorf("got %v, want 42", v)
	}
	// Redefining x must not change the already-forced y (memoization), but
	// forcing x afterwards sees the new body — the REPL cell model.
	env.Define("x", &core.IntLit{Val: 100, Ty: ty})
	v, _ = Force(context.Background(), "y", env, io.Discard)
	if v != int64(42) {
		t.Errorf("memoized y changed: got %v", v)
	}
	v, _ = Force(context.Background(), "x", env, io.Discard)
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
	if _, err := Force(context.Background(), "x", env, io.Discard); err == nil {
		t.Error("expected a self-dependency error")
	}
}

// Switch totality (DESIGN.md §13 risk: semantic drift): every Core node
// kind must be *named* in the interpreter's switch — nodes from future
// slices answer with a deliberate "arrives in Sn" error, never the generic
// unhandled fallback.
func TestSwitchTotality(t *testing.T) {
	it, ft, st, bt, ut := intTy(), floatTy(), stringTy(), boolTy(), unitTy()
	one := &core.IntLit{Val: 1, Ty: it}
	nodes := []core.Expr{
		one,
		&core.FloatLit{Val: 1.5, Ty: ft},
		&core.StringLit{Val: "s", Ty: st},
		&core.BoolLit{Val: true, Ty: bt},
		&core.VarRef{Name: "missing", Ty: it},
		&core.Neg{Operand: one, Ty: it},
		&core.BinOp{Op: "+", Ty: it, L: one, R: one},
		&core.If{Cond: &core.BoolLit{Val: true, Ty: bt}, Then: one, Else: one, Ty: it},
		&core.Print{Arg: one, Ty: ut},
		&core.App{CalleeKind: core.Worker, Ty: it},
	}
	for _, n := range nodes {
		_, err := Eval(context.Background(), n, NewEnv(), io.Discard)
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
	if _, err := Eval(ctx, e, NewEnv(), io.Discard); err == nil {
		t.Error("expected interruption")
	}
}
