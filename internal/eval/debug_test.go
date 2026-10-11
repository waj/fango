package eval

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/debugger"
	"github.com/waj/fango/runtime/fangort"
)

type debugHostFunc func(context.Context, debugger.Event) debugger.Command

func (f debugHostFunc) DebugExchange(ctx context.Context, e debugger.Event) debugger.Command {
	return f(ctx, e)
}

func debugExpr(e core.Expr, bindings ...debugger.Binding) core.Expr {
	e.SetDebugSite(&debugger.Site{Source: &debugger.Source{Name: "test", Hash: "test", Text: "x"}, Start: 0, End: 1, Line: 1, Column: 1, Bindings: bindings})
	return e
}

func TestDebugStopReleasesOnceWithoutReplayingIO(t *testing.T) {
	var out bytes.Buffer
	ctx := NewIOContext(strings.NewReader(""), &out)
	ctx.Debug = &debugger.Config{Step: true}
	stops := 0
	ctx.DebugHost = debugHostFunc(func(_ context.Context, e debugger.Event) debugger.Command {
		stops++
		return debugger.Command{Kind: "stop"}
	})
	e := &core.Bracket{Scope: 1, Resource: "resource", ResourceTy: intTy(), Acquire: &core.IntLit{Val: 1, Ty: intTy()},
		Release: debugExpr(writeExpr("released\n")), Body: &core.Seq{First: writeExpr("before\n"), Then: debugExpr(&core.IntLit{Val: 42, Ty: intTy()}), Ty: intTy()}, Ty: intTy()}
	_, err := EvalIO(context.Background(), e, NewEnv(), ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("stop error = %v", err)
	}
	if stops != 1 || out.String() != "before\nreleased\n" {
		t.Fatalf("stop replayed work or cleanup: %d, %q", stops, out.String())
	}
}

func TestDebugLocalsRejectInvalidHandle(t *testing.T) {
	ctx := NewIOContext(strings.NewReader(""), io.Discard)
	ctx.Debug = &debugger.Config{Step: true}
	phase := 0
	ctx.DebugHost = debugHostFunc(func(_ context.Context, e debugger.Event) debugger.Command {
		phase++
		switch phase {
		case 1:
			return debugger.Command{Kind: "locals"}
		case 2:
			if len(e.Variables) != 1 || e.Variables[0].Name != "x" || e.Variables[0].Value != "41" {
				t.Fatalf("locals: %+v", e)
			}
			return debugger.Command{Kind: "expand", Handle: 0}
		case 3:
			if e.Error != "invalid or expired value handle" {
				t.Fatalf("inspection: %+v", e)
			}
			return debugger.Command{Kind: "continue"}
		default:
			t.Fatal("unexpected stop")
		}
		return debugger.Command{Kind: "stop"}
	})
	e := &core.Let{Name: "x$1", Rhs: &core.IntLit{Val: 41, Ty: intTy()}, Body: debugExpr(&core.VarRef{Name: "x$1", Local: true, Ty: intTy()}, debugger.Binding{Name: "x", Runtime: "x$1", Type: "Int"}), Ty: intTy()}
	v, err := EvalIO(context.Background(), e, NewEnv(), ctx)
	if err != nil || v != int64(41) || phase != 3 {
		t.Fatalf("eval: %v %v phase %d", v, err, phase)
	}
}

func TestDebugHandlesExpireAcrossStops(t *testing.T) {
	ctx := NewIOContext(strings.NewReader(""), io.Discard)
	ctx.Debug = &debugger.Config{Step: true}
	env := NewEnv()
	env.DefineValue("list", nil, fangort.ListCons(Value(int64(7)), fangort.List[Value]{}))
	binding := debugger.Binding{Name: "xs", Runtime: "xs", Type: "List Int"}
	first := debugExpr(&core.IntLit{Val: 1, Ty: intTy()}, binding)
	second := debugExpr(&core.IntLit{Val: 2, Ty: intTy()}, binding)
	second.DebugSite().Start = 1
	second.DebugSite().End = 2
	second.DebugSite().Source.Text = "xx"
	e := &core.Let{Name: "xs", Rhs: &core.VarRef{Name: "list"}, Body: &core.Seq{First: first, Then: second, Ty: intTy()}, Ty: intTy()}
	phase := 0
	var handle uint64
	ctx.DebugHost = debugHostFunc(func(_ context.Context, event debugger.Event) debugger.Command {
		phase++
		switch phase {
		case 1:
			return debugger.Command{Kind: "locals"}
		case 2:
			handle = event.Variables[0].Handle
			if handle == 0 {
				t.Fatal("list has no handle")
			}
			return debugger.Command{Kind: "expand", Handle: handle}
		case 3:
			if len(event.Variables) != 1 || event.Variables[0].Value != "7" {
				t.Fatalf("expansion: %+v", event)
			}
			return debugger.Command{Kind: "step"}
		case 4:
			return debugger.Command{Kind: "expand", Handle: handle}
		case 5:
			if event.Error != "invalid or expired value handle" {
				t.Fatalf("old handle survived: %+v", event)
			}
			return debugger.Command{Kind: "continue"}
		default:
			t.Fatal("unexpected exchange")
		}
		return debugger.Command{Kind: "stop"}
	})
	value, err := EvalIO(context.Background(), e, env, ctx)
	if err != nil || value != int64(2) || phase != 5 {
		t.Fatalf("%v %v phase %d", value, err, phase)
	}
}

type poisonStringer struct{}

func (poisonStringer) String() string { panic("inspection called user code") }

func TestDebugBoundedStructuralInspection(t *testing.T) {
	in := &interp{env: NewEnv(), debug: &debugExecution{handles: map[uint64]debugValue{}}}
	if got := in.debugPreview(poisonStringer{}, 2); got != "<opaque>" {
		t.Fatal(got)
	}
	if got := in.debugPreview(strings.Repeat("界", 1000), 2); got != "\""+strings.Repeat("界", 256)+"\"…" {
		t.Fatal("string preview not bounded")
	}
	values := fangort.List[Value]{}
	for i := int64(99); i >= 0; i-- {
		values = fangort.ListCons(Value(i), values)
	}
	preview := in.debugVariable("xs", "List Int", values)
	if preview.Handle == 0 || !strings.HasSuffix(preview.Value, ", …]") {
		t.Fatalf("list preview: %+v", preview)
	}
	children, more := in.debugChildren(values, 50)
	if len(children) != 50 || more || children[0].Value != "50" || children[49].Value != "99" {
		t.Fatalf("list page: %+v %v", children, more)
	}
}

func TestDebugRejectsTaskCreationBeforeOperands(t *testing.T) {
	ctx := NewIOContext(strings.NewReader(""), io.Discard)
	ctx.Debug = &debugger.Config{}
	ctx.DebugHost = debugHostFunc(func(context.Context, debugger.Event) debugger.Command { return debugger.Command{Kind: "continue"} })
	for _, e := range []core.Expr{&core.AsyncLaunch{}, &core.ParallelMap{}} {
		_, err := EvalIO(context.Background(), e, NewEnv(), ctx)
		if err == nil || !strings.Contains(err.Error(), "concurrent task creation is not supported") {
			t.Fatalf("%T: %v", e, err)
		}
	}
}
