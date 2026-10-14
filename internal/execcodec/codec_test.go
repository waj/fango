package execcodec

import (
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/debugger"
	"github.com/waj/fango/internal/source"
)

func TestPayloadKeepsDebugSourceAndScope(t *testing.T) {
	site := &debugger.Site{Source: &debugger.Source{Name: "Probe.fango", Hash: "content hash", Text: "f x = x + 1"}, Start: 6, End: 11, Line: 1, Column: 7,
		Bindings: []debugger.Binding{{Name: "x", Runtime: "x$1", Type: "Int"}}}
	e := &core.IntLit{Val: 1}
	e.SetDebugSite(site)
	data, err := Encode(&Payload{Expr: e, Debug: &debugger.Config{Step: true}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	got := p.Expr.DebugSite()
	if p.Debug == nil || !p.Debug.Step || got == nil || *got.Source != *site.Source || got.Start != site.Start || got.End != site.End || got.Bindings[0] != site.Bindings[0] {
		t.Fatalf("lost debug metadata: %+v", got)
	}
}

// Call origins survive the trip to the worker as file and line, which is
// what the interpreter's stack needs, while every other span is dropped.
func TestPayloadKeepsOriginLines(t *testing.T) {
	file := source.NewFile("Main.fango", []byte("module Main exposing (main)\n\nmain() = print (f 1)\n"))
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
