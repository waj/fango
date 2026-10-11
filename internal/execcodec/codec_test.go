package execcodec

import (
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/source"
)

// Call origins survive the trip to the worker as file and line, which is
// what the interpreter's stack needs, while every other span is dropped.
func TestPayloadKeepsOriginLines(t *testing.T) {
	file := source.NewFile("Main.fango", []byte("module Main exposing (main)\n\nmain = f 1\n"))
	at := func(start, end int) source.Span { return source.Span{File: file, Start: start, End: end} }
	call := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: "Main.f", Origin: at(36, 37)},
		Args: []core.Expr{&core.IntLit{Val: 1}}, Origin: at(36, 39)}
	data, err := Encode(&Payload{Expr: call})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	app := decoded.Expr.(*core.App)
	if app.Origin.File == nil || !app.Origin.File.LineOnly() || app.Origin.File.Name != "Main.fango" || app.Origin.StartPos().Line != 3 {
		t.Fatalf("call origin decoded as %+v", app.Origin)
	}
	if ref := app.Callee.(*core.VarRef); ref.Origin.File != nil {
		t.Fatalf("variable origin was kept: %+v", ref.Origin)
	}
}
