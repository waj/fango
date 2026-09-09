package eval

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// binOp builds an operator application the way elaboration does: a call to
// the operator's declared native, named by its canonical symbol. Core has no
// operator node — once fixity resolution has grouped a run, an operator is
// an ordinary value and `1 + 2` reaches Core as a call.
func binOp(op string, ty types.Type, l, r core.Expr) core.Expr {
	return &core.NativeCall{Name: "Basics." + op, Module: "Basics", Ty: ty, Args: []core.Expr{l, r}}
}

func intTy() *types.TCon    { return &types.TCon{Unique: 0, Name: "Int"} }
func floatTy() *types.TCon  { return &types.TCon{Unique: 1, Name: "Float"} }
func stringTy() *types.TCon { return &types.TCon{Unique: 2, Name: "String"} }
func boolTy() *types.TCon   { return &types.TCon{Unique: 3, Name: "Bool"} }
func unitTy() *types.TCon   { return &types.TCon{Unique: 4, Name: "()"} }

func writeExpr(text string) core.Expr {
	eff := &types.EffectInfo{Unique: 5, Name: "IO"}
	native := &types.NativeInfo{Name: "IO.write", Module: "IO", Arity: 1, Effect: eff}
	op := &types.EffectOp{Owner: eff, Name: "write", Arity: 1, ParamTypes: []types.Type{stringTy()}, ResultType: unitTy(), Native: native}
	return &core.Perform{Op: op, Effect: core.EffectInstance{Unique: eff.Unique, Name: eff.Name}, Args: []core.Expr{&core.StringLit{Val: text, Ty: stringTy()}}, Ty: unitTy()}
}

func ioReadExpr(name string, result types.Type) core.Expr {
	eff := &types.EffectInfo{Unique: 5, Name: "IO"}
	native := &types.NativeInfo{Name: "IO." + name, Module: "IO", Arity: 1, Effect: eff}
	op := &types.EffectOp{Owner: eff, Name: name, Arity: 1, ParamTypes: []types.Type{unitTy()}, ResultType: result, Native: native}
	return &core.Perform{Op: op, Effect: core.EffectInstance{Unique: eff.Unique, Name: eff.Name}, Args: []core.Expr{&core.UnitLit{Ty: unitTy()}}, Ty: result}
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
	e := binOp("+", ty, &core.IntLit{Val: 1, Ty: ty}, binOp("*", ty, &core.IntLit{Val: 2, Ty: ty}, &core.IntLit{Val: 3, Ty: ty}))
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
		{binOp("+", it, &core.IntLit{Val: 9223372036854775807, Ty: it}, &core.IntLit{Val: 1, Ty: it}), int64(-9223372036854775808)},
		// Float division is IEEE: no panic, +Inf.
		{binOp("/", ft, &core.FloatLit{Val: 1.0, Ty: ft}, &core.FloatLit{Val: 0.0, Ty: ft}), math.Inf(1)},
		{binOp("++", st, &core.StringLit{Val: "foo", Ty: st}, &core.StringLit{Val: "bar", Ty: st}), "foobar"},
		{binOp("<", bt, &core.StringLit{Val: "a", Ty: st}, &core.StringLit{Val: "b", Ty: st}), true},
		{binOp("==", bt, &core.BoolLit{Val: true, Ty: bt}, &core.BoolLit{Val: false, Ty: bt}), false},
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
	var buf bytes.Buffer
	e := writeExpr("12.56636\n")
	if _, err := Eval(context.Background(), e, NewEnv(), &buf); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "12.56636\n" {
		t.Errorf("printed %q, want %q", got, "12.56636\n")
	}
}

func TestEvalIOReadLine(t *testing.T) {
	ioctx := NewIOContext(strings.NewReader("hello\r\nlast"), io.Discard)
	for _, want := range []string{"hello\r\n", "last"} {
		has, err := EvalIO(context.Background(), ioReadExpr("hasInput", boolTy()), NewEnv(), ioctx)
		if err != nil || has != true {
			t.Fatalf("hasInput = %v, %v; want true, nil", has, err)
		}
		got, err := EvalIO(context.Background(), ioReadExpr("readRawLine", stringTy()), NewEnv(), ioctx)
		if err != nil || got != want {
			t.Fatalf("readRawLine = %q, %v; want %q, nil", got, err, want)
		}
	}
	got, err := EvalIO(context.Background(), ioReadExpr("hasInput", boolTy()), NewEnv(), ioctx)
	if err != nil || got != false {
		t.Fatalf("hasInput at EOF = %v, %v; want false, nil", got, err)
	}
	wantErr := io.ErrUnexpectedEOF
	func() {
		defer func() {
			if got := fmt.Sprint(recover()); !strings.Contains(got, wantErr.Error()) {
				t.Fatalf("readLine panic = %q, want %q", got, wantErr)
			}
		}()
		_, _ = EvalIO(context.Background(), ioReadExpr("hasInput", boolTy()), NewEnv(), NewIOContext(failingReader{wantErr}, io.Discard))
	}()
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestLetFrames(t *testing.T) {
	it := intTy()
	// v = 40; w = v + 1; w * 2  →  82
	e := &core.Let{Name: "v", Ty: it,
		Rhs: &core.IntLit{Val: 40, Ty: it},
		Body: &core.Let{Name: "w", Ty: it,
			Rhs:  binOp("+", it, &core.VarRef{Name: "v", Ty: it}, &core.IntLit{Val: 1, Ty: it}),
			Body: binOp("*", it, &core.VarRef{Name: "w", Ty: it}, &core.IntLit{Val: 2, Ty: it})}}
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
	ut := unitTy()
	var buf bytes.Buffer
	e := &core.Let{Name: "a", Ty: ut,
		Rhs: writeExpr("1\n"),
		Body: &core.Let{Name: "b", Ty: ut,
			Rhs:  writeExpr("2\n"),
			Body: writeExpr("3\n")}}
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
	env.Define("y", binOp("+", ty, &core.VarRef{Name: "x", Ty: ty}, &core.IntLit{Val: 2, Ty: ty}))
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
	env.Define("x", binOp("+", ty, &core.VarRef{Name: "x", Ty: ty}, &core.IntLit{Val: 1, Ty: ty}))
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
		binOp("+", it, one, one),
		&core.If{Cond: &core.BoolLit{Val: true, Ty: bt}, Then: one, Else: one, Ty: it},
		writeExpr("1\n"),
		&core.ControlExit{Target: 1, Op: &types.EffectOp{Owner: &types.EffectInfo{Unique: 9, Name: "Fail"}, Index: 0, Name: "fail"}, Ty: it},
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

func TestEvalOutcomePropagatesBeforeFollowingExpression(t *testing.T) {
	it := intTy()
	eff := &types.EffectInfo{Unique: 9, Name: "Fail"}
	op := &types.EffectOp{Owner: eff, Index: 0, Name: "fail", Arity: 1, ParamTypes: []types.Type{it}, ResultType: it}
	eff.Ops = []*types.EffectOp{op}
	exit := &core.ControlExit{Target: 7, Op: op, Payload: []core.Expr{&core.IntLit{Val: 41, Ty: it}}, Ty: it}
	expr := &core.Let{Name: "never", Rhs: exit, Ty: it,
		Body: &core.NativeCall{Name: "Basics.+", Module: "Basics", Args: []core.Expr{&core.IntLit{Val: 1, Ty: it}, &core.IntLit{Val: 1, Ty: it}}, Ty: it}}

	outcome, err := EvalOutcome(context.Background(), expr, NewEnv(), NewIOContext(strings.NewReader(""), io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Exit == nil || outcome.Exit.Target != 7 || outcome.Exit.Op != op || len(outcome.Exit.Payload) != 1 || outcome.Exit.Payload[0] != int64(41) {
		t.Fatalf("outcome = %#v, want checked exit request", outcome)
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
		Body: binOp("+", it, &core.VarRef{Name: "x", Ty: it}, &core.VarRef{Name: "y", Ty: it}),
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
			Cond: binOp("<", bt, &core.VarRef{Name: "n", Ty: it}, &core.IntLit{Val: 1, Ty: it}),
			Then: &core.IntLit{Val: 0, Ty: it},
			Else: binOp("+", it, &core.VarRef{Name: "n", Ty: it}, &core.App{CalleeKind: core.Value,
				Callee: &core.VarRef{Name: "go", Ty: fnTy},
				Args:   []core.Expr{binOp("-", it, &core.VarRef{Name: "n", Ty: it}, &core.IntLit{Val: 1, Ty: it})},
				Ty:     it})}}
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
		e = binOp("+", ty, e, &core.IntLit{Val: 1, Ty: ty})
	}
	if _, err := Eval(ctx, e, NewEnv(), io.Discard); err == nil {
		t.Error("expected interruption")
	}
}
