package eval

import (
	"bytes"
	"context"
	"io"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// binOp builds an operator application the way elaboration does: a call to
// the operator's declared native, named by its canonical symbol. Core has no
// operator node — once fixity resolution has grouped a run, an operator is
// an ordinary value and `1 + 2` reaches Core as a call.
// binOp calls the scalar Basics native implementing op at the operands'
// type, as an instance method body would; `/` and `++` are natives of their own.
func binOp(op string, ty types.Type, l, r core.Expr) core.Expr {
	name := "Basics." + op
	if op != "/" && op != "++" {
		prefix := strings.ToLower(l.Type().(*types.TCon).Name)
		name = "Basics." + prefix + map[string]string{"+": "Add", "-": "Sub", "*": "Mul", "<": "Lt", "==": "Eq"}[op]
	}
	return &core.NativeCall{Name: name, Module: "Basics", Ty: ty, Args: []core.Expr{l, r}}
}

func intTy() *types.TCon    { return &types.TCon{Unique: 0, Name: "Int"} }
func floatTy() *types.TCon  { return &types.TCon{Unique: 1, Name: "Float"} }
func stringTy() *types.TCon { return &types.TCon{Unique: 2, Name: "String"} }
func boolTy() *types.TCon   { return &types.TCon{Unique: 3, Name: "Bool"} }
func unitTy() *types.TCon   { return &types.TCon{Unique: 4, Name: "()"} }

func writeExpr(text string) core.Expr {
	handle := &core.NativeCall{Name: "IO.standardHandle", Module: "IO", Ty: unitTy(), Args: []core.Expr{&core.IntLit{Val: 1, Ty: intTy()}}}
	return &core.NativeCall{Name: "IO.writeHandle", Module: "IO", Ty: unitTy(), Args: []core.Expr{handle, &core.StringLit{Val: text, Ty: stringTy()}}}
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

func TestConcurrentLazyGlobalForce(t *testing.T) {
	env := NewEnv()
	env.Define("answer", &core.IntLit{Val: 42, Ty: intTy()})
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := Force(context.Background(), "answer", env, io.Discard)
			if err != nil || got != int64(42) {
				t.Errorf("force = %v, %v", got, err)
			}
		}()
	}
	close(start)
	wg.Wait()
}

func TestConcurrentLazyGlobalWaitCycle(t *testing.T) {
	env := NewEnv()
	a, b, c := new(Cell), new(Cell), new(Cell)
	if !env.beginCellWait(a, b) || !env.beginCellWait(b, c) {
		t.Fatal("independent waits should be admitted")
	}
	if env.beginCellWait(c, a) {
		t.Fatal("cross-goroutine force cycle was admitted")
	}
	env.endCellWait(b, c)
	if !env.beginCellWait(c, a) {
		t.Fatal("completed wait still blocks an acyclic force")
	}
	env.endCellWait(a, b)
	env.endCellWait(c, a)
}

type forceBarrierWriter struct {
	entered atomic.Int32
	ready   chan struct{}
}

func (w *forceBarrierWriter) Write(p []byte) (int, error) {
	if w.entered.Add(1) == 2 {
		close(w.ready)
	}
	<-w.ready
	return len(p), nil
}

func TestConcurrentLazyGlobalForceCycleDoesNotDeadlock(t *testing.T) {
	env := NewEnv()
	for _, pair := range [][2]string{{"a", "b"}, {"b", "a"}} {
		env.Define(pair[0], &core.Seq{First: writeExpr(pair[0]),
			Then: &core.VarRef{Name: pair[1], Ty: intTy()}, Ty: intTy()})
	}
	writer := &forceBarrierWriter{ready: make(chan struct{})}
	results := make(chan error, 2)
	for _, name := range []string{"a", "b"} {
		go func(name string) {
			_, err := ForceIO(context.Background(), name, env, NewIOContext(strings.NewReader(""), writer))
			results <- err
		}(name)
	}
	for range 2 {
		select {
		case err := <-results:
			if err == nil || !strings.Contains(err.Error(), "depends on itself") {
				t.Errorf("force cycle = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cross-worker force cycle deadlocked")
		}
	}
}

func TestConcurrentIOContextOutput(t *testing.T) {
	var output bytes.Buffer
	ioctx := NewIOContext(strings.NewReader(""), &output)
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ioctx.WriteOutput([]byte("x")); err != nil {
				t.Error(err)
			}
			if _, err := ioctx.Write([]byte("y")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := output.Len(); got != 128 {
		t.Fatalf("output length = %d, want 128", got)
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
		has, err := ioctx.HasInput()
		if err != nil || !has {
			t.Fatalf("hasInput = %v, %v", has, err)
		}
		got, err := ioctx.ReadInputLine()
		if err != nil && err != io.EOF || string(got) != want {
			t.Fatalf("readLine = %q, %v", got, err)
		}
	}
	if got, err := ioctx.HasInput(); err != nil || got {
		t.Fatalf("hasInput at EOF = %v, %v", got, err)
	}
	wantErr := io.ErrUnexpectedEOF
	if _, err := NewIOContext(failingReader{wantErr}, io.Discard).HasInput(); err != wantErr {
		t.Fatalf("read failure = %v", err)
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
		&core.ControlExit{Effect: core.EffectInstance{Unique: 9, Name: "Fail", Control: types.Control{Transport: types.Exit}}, Op: &types.EffectOp{Owner: &types.EffectInfo{Unique: 9, Name: "Fail"}, Index: 0, Name: "fail", Abort: true}, Ty: it},
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

func TestEvalOutcomeRejectsMissingAbortEvidence(t *testing.T) {
	it := intTy()
	eff := &types.EffectInfo{Unique: 9, Name: "Fail"}
	op := &types.EffectOp{Owner: eff, Index: 0, Name: "fail", Arity: 1, ParamTypes: []types.Type{it}, ResultType: it, Abort: true}
	eff.Ops = []*types.EffectOp{op}
	exit := &core.ControlExit{Effect: core.EffectInstance{Unique: eff.Unique, Name: eff.Name, Control: types.Control{Transport: types.Exit}}, Op: op, Payload: []core.Expr{&core.IntLit{Val: 41, Ty: it}}, Ty: it}
	expr := &core.Let{Name: "never", Rhs: exit, Ty: it,
		Body: &core.NativeCall{Name: "Basics.+", Module: "Basics", Args: []core.Expr{&core.IntLit{Val: 1, Ty: it}, &core.IntLit{Val: 1, Ty: it}}, Ty: it}}

	outcome, err := EvalOutcome(context.Background(), expr, NewEnv(), NewIOContext(strings.NewReader(""), io.Discard))
	if err == nil || !strings.Contains(err.Error(), "missing abort evidence") {
		t.Fatalf("outcome = %#v, err = %v, want missing abort evidence", outcome, err)
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
	// Partial via eta-expansion: `{ _w0 -> add 1 _w0 }` applied to 41.
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

// Nested cleanup scopes whose releases both fail keep the innermost failure
// primary and record the rest in the order they were abandoned.
func TestCleanupScopeRecordsSuppressedReleaseFailures(t *testing.T) {
	it, ut := intTy(), unitTy()
	eff := &types.EffectInfo{Unique: 9, Name: "Fail"}
	op := &types.EffectOp{Owner: eff, Index: 0, Name: "fail", Arity: 1, ParamTypes: []types.Type{it}, ResultType: it, Abort: true}
	eff.Ops = []*types.EffectOp{op}
	ev := core.EffectInstance{Unique: eff.Unique, Name: eff.Name, Captures: types.ScopeCapture(1), Control: types.Control{Transport: types.Exit}}
	raise := func(code int64, ty types.Type) core.Expr {
		return &core.ControlExit{Effect: ev, Op: op, Payload: []core.Expr{&core.IntLit{Val: code, Ty: it}}, Ty: ty}
	}
	// The inner scope's body succeeds and its release fails, so that failure
	// is primary; the outer release then fails while the exit is in flight.
	inner := &core.Bracket{Scope: 2, Resource: "_inner", ResourceTy: it,
		Acquire: &core.IntLit{Val: 0, Ty: it}, Release: raise(2, ut),
		Body: &core.IntLit{Val: 1, Ty: it}, Ty: it, Control: types.Control{Transport: types.Exit}}
	outer := &core.Bracket{Scope: 3, Resource: "_outer", ResourceTy: it,
		Acquire: &core.IntLit{Val: 0, Ty: it}, Release: raise(3, ut),
		Body: inner, Ty: it, Control: types.Control{Transport: types.Exit}}
	handled := &core.Handle{
		Body: outer, Effects: []core.EffectInstance{ev}, Scope: 1, Ty: it,
		Clauses: []core.HandlerClause{{Op: op, Params: []string{"code"}, ParamTypes: []types.Type{it},
			ResultType: it, Body: &core.VarRef{Name: "code", Local: true, Ty: it}}},
	}

	value, err := EvalIO(context.Background(), handled, NewEnv(), NewIOContext(strings.NewReader(""), io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	if value != int64(2) {
		t.Fatalf("handler answer = %v, want the inner release failure (2)", value)
	}

	// The handler consumes the request, so check the envelope it carried
	// directly: the outer release joins the inner one, innermost first.
	body := &ExitRequest{Op: op, Payload: []Value{int64(1)}}
	innerRelease := &ExitRequest{Op: op, Payload: []Value{int64(2)}}
	outerRelease := &ExitRequest{Op: op, Payload: []Value{int64(3)}}
	joined := suppress(suppress(body, innerRelease), outerRelease)
	if len(body.Suppressed) != 0 {
		t.Fatalf("the forwarded request was edited in place: %v", body.Suppressed)
	}
	if len(joined.Suppressed) != 2 || joined.Suppressed[0] != innerRelease || joined.Suppressed[1] != outerRelease {
		t.Fatalf("suppressed = %v, want inner-to-outer order", joined.Suppressed)
	}
	if joined.Payload[0] != int64(1) {
		t.Fatalf("primary payload = %v, want the body failure", joined.Payload[0])
	}
}
