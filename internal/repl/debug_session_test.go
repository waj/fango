package repl

import (
	"bytes"
	"testing"

	"github.com/waj/fango/internal/debugger"
	"github.com/waj/fango/internal/debugsession"
)

func TestInterpreterSessionDeferredInspectionAndExpansion(t *testing.T) {
	var out bytes.Buffer
	s := &Session{out: &out, debug: newReplDebug()}
	s.ensureDebugSession()
	front, b := s.debug.terminal, s.debug.backend
	source := &debugger.Source{Name: "Main.fango", Hash: "snapshot", Text: "x\n"}
	site := &debugger.Site{Source: source, Start: 0, End: 1, Line: 1, Column: 1}
	front.Accept(b.event(debugger.Event{Kind: "stopped", Frames: []debugger.Frame{{ID: 3, Name: "inner", Site: site}, {ID: 4, Name: "outer", Site: site}}}))
	if _, err := front.Handle("frame 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := front.Handle("print x"); err != nil {
		t.Fatal(err)
	}
	if b.pending.Kind != "print" || b.pending.Frame != 1 {
		t.Fatal(b.pending)
	}
	b.pending = nil
	response := b.event(debugger.Event{Kind: "inspection", Variables: []debugger.Variable{{Name: "x", Type: "List Int", Value: "[1, 2]", Handle: 9001}}})
	event := front.Session.Accept(response)
	handle := event.Variables[0].Handle
	if handle == 0 || handle == 9001 {
		t.Fatal("backend handle escaped", event)
	}
	if _, err := front.Session.Execute(debugsession.Request{Kind: "expand", Handle: handle, Offset: 50}); err != nil {
		t.Fatal(err)
	}
	if b.pending.Kind != "expand" || b.pending.Frame != 1 || b.pending.Handle != 9001 || b.pending.Offset != 50 {
		t.Fatal(b.pending)
	}
	b.pending = nil
	front.Accept(b.event(debugger.Event{Kind: "inspection"}))
	if _, err := front.Handle("next"); err != nil {
		t.Fatal(err)
	}
	if b.pending.Kind != "next" || b.pending.Frame != 1 || !b.pending.ReplaceBreakpoints {
		t.Fatal(b.pending)
	}
	b.pending = nil
	front.Accept(b.event(debugger.Event{Kind: "stopped", Frames: []debugger.Frame{{ID: 3, Name: "inner", Site: site}}}))
	if _, err := front.Session.Execute(debugsession.Request{Kind: "expand", Handle: handle}); err == nil {
		t.Fatal("expanded stale handle")
	}
	if _, err := front.Handle("stop"); err != nil || b.pending.Kind != "stop" || s.debug.quit {
		t.Fatal(err, "stop terminated the REPL")
	}
	b.pending = nil
	front.Session.Accept(debugsession.Event{Kind: "completed"})
	if _, err := front.Session.Execute(debugsession.Request{Kind: "quit"}); err != nil || !s.debug.quit || b.pending.Kind != "stop" {
		t.Fatal("quit did not stop evaluation", err)
	}
}
