// Package debugsession coordinates debugger backends without terminal IO.
// A host serializes Execute and Accept; Interrupt may run concurrently.
package debugsession

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/waj/fango/internal/debugger"
)

type Capabilities struct {
	Name                                      string
	Frame, Expand, Args, Raw, Stop, Interrupt bool
}

func (c Capabilities) Supports(kind string) bool {
	switch kind {
	case "frame":
		return c.Frame
	case "expand":
		return c.Expand
	case "args":
		return c.Args
	case "raw":
		return c.Raw
	case "stop":
		return c.Stop
	}
	return true
}

type Precision uint8

const (
	LineOnly Precision = iota
	Exact
	Enclosing
)

type Location struct {
	File, Hash, Text         string
	Line, Column, Start, End int
	Precision                Precision
}

func (l Location) String() string {
	if l.Column > 0 {
		return fmt.Sprintf("%s:%d:%d", l.File, l.Line, l.Column)
	}
	return fmt.Sprintf("%s:%d", l.File, l.Line)
}

type Frame struct {
	ID       uint64
	Name     string
	Location Location
}
type Breakpoint struct {
	ID         int
	File, Hash string
	Line       int
	Status     string
}

// Request uses canonical command names. Epoch optionally guards a request
// against a stale stop. Frame is filled by Session from its selected frame.
type Request struct {
	Kind, Arg, File, RawCommand string
	Index, Line, Offset, Frame  int
	Handle, Epoch               uint64
}

// RawValue preserves backend inspection data without formatting it into text.
type RawValue struct {
	Name, Type, Value, Unreadable string
	Children                      []RawValue
	Truncated                     bool
}

// Events are structured responses. A backend may return "pending" and later
// deliver its response through Accept (the interpreter pause/reply protocol).
// "running", "stopped", "completed", and "terminated" are lifecycle events.
type Event struct {
	Kind, Reason, Error string
	Epoch               uint64
	Frames              []Frame
	Variables           []debugger.Variable
	Raw                 []RawValue
	Breakpoints         []Breakpoint
	Sources             []debugger.Source
	Messages            []string
	Index, ExitStatus   int
	More                bool
}

// Execute must return a stopped/terminated event after synchronous movement,
// or a running event if the next stop/completion will arrive asynchronously.
// On movement failure it should supply a valid state event when available;
// otherwise the session enters an unknown state and rejects inspection.
// Interrupt is the only method allowed concurrently with Execute.
type Backend interface {
	Capabilities() Capabilities
	Execute(Request) (Event, error)
	Interrupt() error
}

type Session struct {
	closed            atomic.Bool
	backend           Backend
	capabilities      Capabilities
	state             string
	frames            []Frame
	selected          int
	epoch, nextHandle uint64
	handles           map[uint64]uint64
	pending           bool
}

func New(backend Backend) *Session {
	return &Session{backend: backend, capabilities: backend.Capabilities(), state: "idle"}
}
func (s *Session) Capabilities() Capabilities { return s.capabilities }
func (s *Session) State() string              { return s.state }
func (s *Session) Epoch() uint64              { return s.epoch }
func (s *Session) SelectedFrame() int         { return s.selected }

func (s *Session) invalidate() {
	s.handles = nil
	s.frames = nil
	s.selected = 0
	s.pending = false
}

// Accept consumes one backend event. Handles published to clients are session
// tokens, never backend addresses/IDs, and cannot alias a later suspension.
func (s *Session) Accept(event Event) Event {
	if s.state == "terminated" && event.Kind != "terminated" {
		return Event{Kind: "error", Error: "debug session has ended", Epoch: s.epoch}
	}
	if event.Kind != "stopped" && event.Kind != "running" && event.Kind != "completed" && event.Kind != "terminated" && event.Epoch != 0 && event.Epoch != s.epoch {
		return Event{Kind: "error", Error: "stale debug response", Epoch: s.epoch}
	}
	if (event.Kind == "inspection" || event.Kind == "frames" || event.Kind == "raw" || event.Kind == "raw-frames") && s.state != "stopped" {
		return Event{Kind: "error", Error: "debugger is not stopped", Epoch: s.epoch}
	}
	switch event.Kind {
	case "stopped":
		s.invalidate()
		s.epoch++
		s.state = "stopped"
		s.frames = append([]Frame(nil), event.Frames...)
	case "running", "completed", "terminated":
		s.invalidate()
		s.state = event.Kind
		if event.Kind == "terminated" {
			s.closed.Store(true)
		}
	case "frames":
		s.frames = append([]Frame(nil), event.Frames...)
		if s.selected >= len(s.frames) {
			s.selected = 0
		}
	case "pending":
		s.pending = true
	}
	if event.Kind != "pending" {
		s.pending = false
	}
	if len(event.Variables) > 0 {
		event.Variables = append([]debugger.Variable(nil), event.Variables...)
		for i := range event.Variables {
			v := &event.Variables[i]
			if v.Handle == 0 {
				continue
			}
			if s.state != "stopped" {
				v.Handle = 0
				continue
			}
			if s.handles == nil {
				s.handles = map[uint64]uint64{}
			}
			s.nextHandle++
			s.handles[s.nextHandle] = v.Handle
			v.Handle = s.nextHandle
		}
	}
	event.Epoch = s.epoch
	return event
}

func (s *Session) Execute(req Request) (Event, error) {
	if err := s.validate(req); err != nil {
		return Event{}, err
	}
	if req.Kind == "frame" {
		if req.Index >= len(s.frames) {
			return Event{}, fmt.Errorf("invalid frame")
		}
		s.selected = req.Index
		return Event{Kind: "frame", Index: s.selected, Frames: []Frame{s.frames[s.selected]}, Epoch: s.epoch}, nil
	}
	req.Frame, req.Epoch = s.selected, s.epoch
	if req.Kind == "expand" {
		handle, ok := s.handles[req.Handle]
		if !ok {
			return Event{}, fmt.Errorf("invalid or expired value handle")
		}
		req.Handle = handle
	}
	moving := req.Kind == "continue" || req.Kind == "step" || req.Kind == "next" || req.Kind == "finish" || req.Kind == "stop" || req.Kind == "quit"
	if moving {
		s.invalidate()
		s.state = "running"
	}
	event, err := s.backend.Execute(req)
	if err != nil && moving && event.Kind == "" {
		s.state = "unknown"
	}
	return s.Accept(event), err
}

func (s *Session) validate(r Request) error {
	switch r.Kind {
	case "break", "breaks", "delete", "sources", "quit", "stack", "frame", "args", "locals", "print", "expand", "raw", "step", "next", "finish", "continue", "stop":
	default:
		return fmt.Errorf("unknown session request %q", r.Kind)
	}
	if !s.capabilities.Supports(r.Kind) {
		return fmt.Errorf("%s is not supported by the %s debugger", r.Kind, s.capabilities.Name)
	}
	if s.state == "terminated" {
		return fmt.Errorf("debug session has ended")
	}
	if s.pending {
		return fmt.Errorf("debugger response is pending")
	}
	switch r.Kind {
	case "quit":
	case "break", "breaks", "delete", "sources":
		if s.state == "running" || s.state == "unknown" {
			return fmt.Errorf("debugger is not stopped")
		}
	default:
		if s.state != "stopped" {
			return fmt.Errorf("debugger is not stopped")
		}
	}
	switch r.Kind {
	case "continue", "step", "next", "finish", "stop", "quit", "stack", "args", "locals", "breaks", "sources":
		if r.Arg != "" {
			return fmt.Errorf("%s takes no arguments", r.Kind)
		}
	}
	if r.Epoch != 0 && r.Epoch != s.epoch {
		return fmt.Errorf("stale debug stop")
	}
	switch r.Kind {
	case "break":
		if strings.TrimSpace(r.File) == "" || r.Line < 1 {
			return fmt.Errorf("invalid breakpoint location")
		}
	case "delete":
		if r.Index < 1 {
			return fmt.Errorf("invalid breakpoint ID")
		}
	case "frame":
		if r.Index < 0 {
			return fmt.Errorf("invalid frame")
		}
	case "print":
		if len(strings.Fields(r.Arg)) != 1 {
			return fmt.Errorf("print requires one binding name; expressions are not evaluated")
		}
	case "expand":
		if r.Handle == 0 || r.Offset < 0 {
			return fmt.Errorf("invalid expansion request")
		}
	case "raw":
		switch r.RawCommand {
		case "print":
			if r.Arg == "" {
				return fmt.Errorf("missing raw expression")
			}
		case "args", "locals", "stack":
			if r.Arg != "" {
				return fmt.Errorf("raw %s takes no arguments", r.RawCommand)
			}
		default:
			return fmt.Errorf("unknown raw inspection request")
		}
	}
	return nil
}

func (s *Session) Interrupt() error {
	if s.closed.Load() {
		return fmt.Errorf("debug session has ended")
	}
	if !s.capabilities.Interrupt {
		return fmt.Errorf("interrupt is not supported by the %s debugger; cancel the evaluation instead", s.capabilities.Name)
	}
	// No mutable session state is read here: execution owns it until the
	// backend reports its resulting stop.
	return s.backend.Interrupt()
}
