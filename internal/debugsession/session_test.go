package debugsession

import (
	"fmt"
	"testing"

	"github.com/waj/fango/internal/debugger"
)

type testBackend struct {
	caps       Capabilities
	requests   []Request
	response   Event
	err        error
	interrupts int
}

func (b *testBackend) Capabilities() Capabilities { return b.caps }
func (b *testBackend) Execute(r Request) (Event, error) {
	b.requests = append(b.requests, r)
	return b.response, b.err
}
func (b *testBackend) Interrupt() error { b.interrupts++; return nil }
func paused(b *testBackend) *Session {
	s := New(b)
	s.Accept(Event{Kind: "stopped", Frames: []Frame{{ID: 10, Name: "inner"}, {ID: 20, Name: "outer"}}})
	return s
}

func TestSelectedFrameAndHandleLifetimes(t *testing.T) {
	b := &testBackend{caps: Capabilities{Name: "test", Frame: true, Expand: true}, response: Event{Kind: "inspection"}}
	s := paused(b)
	epoch := s.Epoch()
	if _, err := s.Execute(Request{Kind: "frame", Index: 1}); err != nil {
		t.Fatal(err)
	}
	values := []debugger.Variable{{Name: "value", Handle: 77}}
	b.response.Variables = values
	event, err := s.Execute(Request{Kind: "print", Arg: "value"})
	if err != nil {
		t.Fatal(err)
	}
	handle := event.Variables[0].Handle
	if values[0].Handle != 77 || handle == 0 || b.requests[0].Frame != 1 {
		t.Fatal("lost backend/frame identity", event, b.requests)
	}
	b.response = Event{Kind: "inspection"}
	if _, err := s.Execute(Request{Kind: "expand", Handle: handle, Offset: 50}); err != nil {
		t.Fatal(err)
	}
	if r := b.requests[1]; r.Handle != 77 || r.Offset != 50 || r.Frame != 1 {
		t.Fatal(r)
	}
	b.response = Event{Kind: "running"}
	if _, err := s.Execute(Request{Kind: "next"}); err != nil {
		t.Fatal(err)
	}
	if b.requests[2].Frame != 1 {
		t.Fatal("resume lost selected frame")
	}
	if _, err := s.Execute(Request{Kind: "print", Arg: "value"}); err == nil {
		t.Fatal("inspection while running")
	}
	s.Accept(Event{Kind: "stopped", Frames: []Frame{{ID: 30, Name: "new"}}})
	if s.SelectedFrame() != 0 || s.Epoch() == epoch {
		t.Fatal("stop did not reset selection")
	}
	before := len(b.requests)
	for _, r := range []Request{{Kind: "expand", Handle: handle}, {Kind: "frame", Index: 1}, {Kind: "print", Arg: "value", Epoch: epoch}} {
		if _, err := s.Execute(r); err == nil {
			t.Fatal("accepted stale request", r)
		}
	}
	if len(b.requests) != before {
		t.Fatal("stale request reached backend")
	}
	b.response = Event{Kind: "inspection", Variables: values}
	next, err := s.Execute(Request{Kind: "print", Arg: "value"})
	if err != nil || next.Variables[0].Handle == handle {
		t.Fatal("reused expired handle", next, err)
	}
}

func TestDeferredResponsesAndCompletion(t *testing.T) {
	b := &testBackend{caps: Capabilities{Name: "test", Expand: true}, response: Event{Kind: "pending"}}
	s := paused(b)
	if _, err := s.Execute(Request{Kind: "locals"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(Request{Kind: "continue"}); err == nil {
		t.Fatal("overlapped pending response")
	}
	response := s.Accept(Event{Kind: "inspection", Epoch: s.Epoch(), Variables: []debugger.Variable{{Handle: 123}}})
	handle := response.Variables[0].Handle
	s.Accept(Event{Kind: "completed"})
	if _, err := s.Execute(Request{Kind: "expand", Handle: handle}); err == nil {
		t.Fatal("expanded after completion")
	}
	s.Accept(Event{Kind: "running"})
	s.Accept(Event{Kind: "stopped"})
	epoch := s.Epoch()
	stale := s.Accept(Event{Kind: "inspection", Epoch: epoch - 1, Variables: []debugger.Variable{{Handle: 123}}})
	if stale.Error != "stale debug response" || s.Epoch() != epoch {
		t.Fatal(stale)
	}
	b.response = Event{Kind: "terminated"}
	if _, err := s.Execute(Request{Kind: "quit"}); err != nil {
		t.Fatal(err)
	}
	if event := s.Accept(Event{Kind: "stopped"}); event.Error == "" || s.State() != "terminated" {
		t.Fatal("reopened ended session")
	}
}

func TestCapabilityAndRequestValidation(t *testing.T) {
	b := &testBackend{caps: Capabilities{Name: "minimal"}}
	s := paused(b)
	for _, r := range []Request{
		{Kind: "frame", Index: 0}, {Kind: "expand", Handle: 1}, {Kind: "raw", RawCommand: "print", Arg: "x"},
		{Kind: "args"}, {Kind: "stop"}, {Kind: "break", File: "X", Line: 0}, {Kind: "delete", Index: -1},
		{Kind: "print", Arg: "x + 1"}, {Kind: "next", Arg: "extra"}, {Kind: "unknown"},
	} {
		if _, err := s.Execute(r); err == nil {
			t.Fatal("accepted", r)
		}
	}
	if len(b.requests) != 0 {
		t.Fatal("invalid request reached backend")
	}
	if err := s.Interrupt(); err == nil || b.interrupts != 0 {
		t.Fatal("unsupported interrupt")
	}
}

func TestMovementFailureAndInterrupt(t *testing.T) {
	b := &testBackend{caps: Capabilities{Name: "native", Interrupt: true}, err: fmt.Errorf("lost connection")}
	s := paused(b)
	if _, err := s.Execute(Request{Kind: "next"}); err == nil || s.State() != "unknown" {
		t.Fatal(err, s.State())
	}
	if _, err := s.Execute(Request{Kind: "locals"}); err == nil {
		t.Fatal("inspected uncertain state")
	}
	if err := s.Interrupt(); err != nil || b.interrupts != 1 {
		t.Fatal(err)
	}
	b.err = nil
	b.response = Event{Kind: "terminated"}
	if _, err := s.Execute(Request{Kind: "quit"}); err != nil {
		t.Fatal(err)
	}
}
