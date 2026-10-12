package nativedebug

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/debugger"
	"github.com/waj/fango/internal/debugsession"
)

func TestSessionSourceSnapshotsAndStructuredInspection(t *testing.T) {
	rpc := &inspectionRPC{args: []variable{{Name: "v_x", Type: "int64", Value: "20"}}}
	const text = "main() = x\n"
	c := &Controller{RPC: rpc, Manifest: &Manifest{Sources: map[string]string{"Main.fango": text},
		Scopes: []debugger.NativeScope{{File: "Main.fango", Line: 1, Span: &debugger.NativeSpan{Start: 9, End: 10}, Bindings: []debugger.Binding{{Name: "x", Runtime: "v_x", Type: "Int"}}}}},
		State: State{CurrentThread: &Thread{Location: Location{File: "Main.fango", Line: 1}}}}
	b := &SessionBackend{Controller: c}
	s := debugsession.New(b)
	event := s.Accept(b.Snapshot())
	loc := event.Frames[0].Location
	if loc.Precision != debugsession.Exact || loc.Text != text || len(loc.Hash) != 64 {
		t.Fatal(loc)
	}
	sources, err := s.Execute(debugsession.Request{Kind: "sources"})
	if err != nil || len(sources.Sources) != 1 || sources.Sources[0].Hash != loc.Hash {
		t.Fatal(sources, err)
	}
	values, err := s.Execute(debugsession.Request{Kind: "print", Arg: "x", Epoch: event.Epoch})
	if err != nil || len(values.Variables) != 1 || debugger.RenderValue(values.Variables[0].Value) != "20" || values.Variables[0].Type != "Int" {
		t.Fatal(values, err)
	}
	raw, err := s.Execute(debugsession.Request{Kind: "raw", RawCommand: "print", Arg: "v_x"})
	if err != nil || len(raw.Raw) != 1 || raw.Raw[0].Name != "v_x" {
		t.Fatal(raw, err)
	}
	_, err = s.Execute(debugsession.Request{Kind: "expand", Handle: 1})
	if err == nil || !strings.Contains(err.Error(), "invalid or expired") {
		t.Fatal(err)
	}
}

func TestSessionMoveFailureRejectsStaleInspection(t *testing.T) {
	c, rpc := controller()
	rpc.failCreateAt = 1
	b := &SessionBackend{Controller: c}
	s := debugsession.New(b)
	s.Accept(b.Snapshot())
	if _, err := s.Execute(debugsession.Request{Kind: "next"}); err == nil || s.State() != "unknown" {
		t.Fatal(s.State(), err)
	}
	if _, err := s.Execute(debugsession.Request{Kind: "locals"}); err == nil {
		t.Fatal("inspected unverified process state")
	}
}
