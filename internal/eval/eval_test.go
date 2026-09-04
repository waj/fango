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

func printExpr(arg core.Expr) core.Expr {
	eff := &types.EffectInfo{Unique: 5, Name: "IO"}
	op := &types.EffectOp{Owner: eff, Name: "print", Arity: 1, ParamTypes: []types.Type{arg.Type()}, ResultType: unitTy(), Builtin: true}
	return &core.Perform{Op: op, Effect: core.EffectInstance{Unique: eff.Unique, Name: eff.Name}, Args: []core.Expr{arg}, Ty: unitTy()}
}

func readLineExpr() core.Expr {
	eff := &types.EffectInfo{Unique: 5, Name: "IO"}
	op := &types.EffectOp{Owner: eff, Name: "readLine", Arity: 1, ParamTypes: []types.Type{unitTy()}, ResultType: stringTy(), Builtin: true}
	return &core.Perform{Op: op, Effect: core.EffectInstance{Unique: eff.Unique, Name: eff.Name}, Args: []core.Expr{&core.UnitLit{Ty: unitTy()}}, Ty: stringTy()}
}

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
	e := printExpr(&core.BinOp{Op: "*", Ty: ft,
		L: &core.FloatLit{Val: 3.14159, Ty: ft},
		R: &core.FloatLit{Val: 4.0, Ty: ft}})
	if _, err := Eval(context.Background(), e, NewEnv(), &buf); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "12.56636\n" {
		t.Errorf("printed %q, want %q", got, "12.56636\n")
	}
}

func TestEvalIOReadLine(t *testing.T) {
	ioctx := NewIOContext(strings.NewReader("hello\r\nlast"), io.Discard)
	for _, want := range []string{"hello", "last", ""} {
		got, err := EvalIO(context.Background(), readLineExpr(), NewEnv(), ioctx)
		if err != nil || got != want {
			t.Fatalf("readLine = %q, %v; want %q, nil", got, err, want)
		}
	}
	wantErr := io.ErrUnexpectedEOF
	_, err := EvalIO(context.Background(), readLineExpr(), NewEnv(), NewIOContext(failingReader{wantErr}, io.Discard))
	if err != wantErr {
		t.Fatalf("readLine error = %v, want %v", err, wantErr)
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestLetFrames(t *testing.T) {
	it := intTy()
	// v = 40; w = v + 1; w * 2  →  82
	e := &core.Let{Name: "v", Ty: it,
		Rhs: &core.IntLit{Val: 40, Ty: it},
		Body: &core.Let{Name: "w", Ty: it,
			Rhs: &core.BinOp{Op: "+", Ty: it,
				L: &core.VarRef{Name: "v", Ty: it},
				R: &core.IntLit{Val: 1, Ty: it}},
			Body: &core.BinOp{Op: "*", Ty: it,
				L: &core.VarRef{Name: "w", Ty: it},
				R: &core.IntLit{Val: 2, Ty: it}}}}
	if v := run(t, e); v != int64(82) {
		t.Errorf("got %v, want 82", v)
	}
}

// A frame binding wins over a top-level cell of the same name — relevant
// for the REPL, where a session name and a (batch-checked) local could
// coincide textually even though the checker forbids user shadowing.
func TestFrameBeatsCell(t *testing.T) {
	it := intTy()
	env := NewEnv()
	env.Define("v", &core.IntLit{Val: 1, Ty: it})
	e := &core.Let{Name: "v", Ty: it,
		Rhs:  &core.IntLit{Val: 2, Ty: it},
		Body: &core.VarRef{Name: "v", Ty: it}}
	v, err := Eval(context.Background(), e, env, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if v != int64(2) {
		t.Errorf("got %v, want the frame's 2", v)
	}
}

// Bindings evaluate eagerly in order — print side effects prove it.
func TestLetEagerOrder(t *testing.T) {
	it, ut := intTy(), unitTy()
	var buf bytes.Buffer
	e := &core.Let{Name: "a", Ty: ut,
		Rhs: printExpr(&core.IntLit{Val: 1, Ty: it}),
		Body: &core.Let{Name: "b", Ty: ut,
			Rhs:  printExpr(&core.IntLit{Val: 2, Ty: it}),
			Body: printExpr(&core.IntLit{Val: 3, Ty: it})}}
	if _, err := Eval(context.Background(), e, NewEnv(), &buf); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "1\n2\n3\n" {
		t.Errorf("printed %q, want 1 2 3 in order", got)
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

// Switch totality (see doc/design.md, "Testing and performance"): every
// current Core node kind must be named in the interpreter's switch rather
// than reaching the generic unhandled fallback.
func TestSwitchTotality(t *testing.T) {
	it, ft, st, bt := intTy(), floatTy(), stringTy(), boolTy()
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
		printExpr(one),
		&core.Let{Name: "v", Rhs: one, Body: one, Ty: it},
		&core.Lambda{Param: "x", Body: one, Ty: &types.TFun{Arg: it, Ret: it}},
		&core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: "nope", Ty: it}, Ty: it},
	}
	for _, n := range nodes {
		_, err := Eval(context.Background(), n, NewEnv(), io.Discard)
		if err != nil && strings.Contains(err.Error(), "unhandled Core node") {
			t.Errorf("%T hit the unhandled fallback — add it to the interpreter switch", n)
		}
	}
}

func TestWorkersAndClosures(t *testing.T) {
	it := intTy()
	fnTy := &types.TFun{Arg: it, Ret: it}
	env := NewEnv()
	// add x y = x + y (worker, arity 2)
	env.DefineWorker(&core.Def{
		Name: "add", Params: []string{"x", "y"},
		Type: &types.TFun{Arg: it, Ret: fnTy},
		Body: &core.BinOp{Op: "+", Ty: it,
			L: &core.VarRef{Name: "x", Ty: it},
			R: &core.VarRef{Name: "y", Ty: it}},
	})
	// Saturated direct call: add 40 2.
	sat := &core.App{CalleeKind: core.Worker,
		Callee: &core.VarRef{Name: "add", Ty: &types.TFun{Arg: it, Ret: fnTy}},
		Args:   []core.Expr{&core.IntLit{Val: 40, Ty: it}, &core.IntLit{Val: 2, Ty: it}},
		Ty:     it}
	if v, err := Eval(context.Background(), sat, env, io.Discard); err != nil || v != int64(42) {
		t.Errorf("saturated: got %v, %v", v, err)
	}
	// Partial via eta-expansion: (\_w0 -> add 1 _w0) applied to 41.
	partial := &core.Lambda{Param: "_w0", Ty: fnTy,
		Body: &core.App{CalleeKind: core.Worker,
			Callee: &core.VarRef{Name: "add", Ty: &types.TFun{Arg: it, Ret: fnTy}},
			Args:   []core.Expr{&core.IntLit{Val: 1, Ty: it}, &core.VarRef{Name: "_w0", Ty: it}},
			Ty:     it}}
	call := &core.App{CalleeKind: core.Value, Callee: partial,
		Args: []core.Expr{&core.IntLit{Val: 41, Ty: it}}, Ty: it}
	if v, err := Eval(context.Background(), call, env, io.Discard); err != nil || v != int64(42) {
		t.Errorf("partial: got %v, %v", v, err)
	}
}

func TestRecursiveLet(t *testing.T) {
	it := intTy()
	bt := boolTy()
	fnTy := &types.TFun{Arg: it, Ret: it}
	// go n = if n < 1 then 0 else n + go (n - 1);  go 4 == 10
	lam := &core.Lambda{Param: "n", Ty: fnTy,
		Body: &core.If{Ty: it,
			Cond: &core.BinOp{Op: "<", Ty: bt,
				L: &core.VarRef{Name: "n", Ty: it}, R: &core.IntLit{Val: 1, Ty: it}},
			Then: &core.IntLit{Val: 0, Ty: it},
			Else: &core.BinOp{Op: "+", Ty: it,
				L: &core.VarRef{Name: "n", Ty: it},
				R: &core.App{CalleeKind: core.Value,
					Callee: &core.VarRef{Name: "go", Ty: fnTy},
					Args: []core.Expr{&core.BinOp{Op: "-", Ty: it,
						L: &core.VarRef{Name: "n", Ty: it},
						R: &core.IntLit{Val: 1, Ty: it}}},
					Ty: it}}}}
	e := &core.Let{Name: "go", Rec: true, Rhs: lam, Ty: it,
		Body: &core.App{CalleeKind: core.Value,
			Callee: &core.VarRef{Name: "go", Ty: fnTy},
			Args:   []core.Expr{&core.IntLit{Val: 4, Ty: it}}, Ty: it}}
	if v := run(t, e); v != int64(10) {
		t.Errorf("letrec: got %v, want 10", v)
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
