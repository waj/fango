package repl

import (
	"fmt"
	"sort"

	"github.com/waj/fango/internal/debugger"
	"github.com/waj/fango/internal/debugsession"
	"github.com/waj/fango/internal/debugui"
)

// interpreterSession adapts the reverse pause/reply protocol. Execute queues
// a wire command; the REPL host delivers it after returning from the prompt.
// Inspection responses arrive in the next exchange, without re-entering or
// replaying evaluation and without moving inspected values out of the worker.
type interpreterSession struct {
	responseEpoch uint64
	owner         *Session
	pending       *debugger.Command
	frames        []debugsession.Frame
}

func (b *interpreterSession) Capabilities() debugsession.Capabilities {
	return debugsession.Capabilities{Name: "interpreter", Frame: true, Expand: true, Stop: true}
}
func (b *interpreterSession) Interrupt() error {
	return fmt.Errorf("cancel the interpreter evaluation instead")
}

func (s *Session) ensureDebugSession() {
	if s.debug.terminal != nil {
		return
	}
	backend := &interpreterSession{owner: s}
	s.debug.backend = backend
	s.debug.terminal = &debugui.Terminal{Session: debugsession.New(backend), Features: debugui.Interpreter(), Out: s.out}
}
func (b *interpreterSession) event(event debugger.Event) debugsession.Event {
	out := debugsession.Event{Kind: event.Kind, Reason: event.Reason, Error: event.Error, Variables: event.Variables, More: event.More}
	for _, f := range event.Frames {
		frame := debugsession.Frame{ID: f.ID, Name: f.Name}
		if site := f.Site; site != nil && site.Source != nil {
			frame.Location = debugsession.Location{File: site.Source.Name, Hash: site.Source.Hash, Text: site.Source.Text,
				Line: site.Line, Column: site.Column, Start: site.Start, End: site.End, Precision: debugsession.Exact}
		}
		out.Frames = append(out.Frames, frame)
	}
	if event.Kind == "stopped" {
		b.frames = out.Frames
	} else if event.Kind == "inspection" {
		out.Epoch = b.responseEpoch
		if event.Error != "" && b.owner.debug.terminal.Session.State() == "running" {
			// The evaluator rejected a resume command and is still paused.
			out.Kind, out.Reason, out.Frames = "stopped", "resume failed", b.frames
		}
	}
	return out
}
func (b *interpreterSession) breakpoint(bp *replBreakpoint) debugsession.Breakpoint {
	status := "pending import"
	if bp.invalid {
		status = "source changed"
	} else if bp.Hash != "" {
		status = "verified"
	} else if b.owner.debug.sources[bp.File] != nil {
		status = "unresolved: no executable site on this line"
	}
	return debugsession.Breakpoint{ID: bp.ID, File: bp.File, Hash: bp.Hash, Line: bp.Line, Status: status}
}

func (b *interpreterSession) Execute(req debugsession.Request) (debugsession.Event, error) {
	s := b.owner
	d := s.debug
	switch req.Kind {
	case "continue", "step", "next", "finish", "stop", "quit", "locals", "print", "expand":
		cmd := debugger.Command{Kind: req.Kind, Frame: req.Frame, Name: req.Arg, Handle: req.Handle, Offset: req.Offset}
		event := debugsession.Event{Kind: "pending"}
		switch req.Kind {
		case "continue", "step", "next", "finish", "stop":
			cmd.Breakpoints, cmd.ReplaceBreakpoints = s.debugBreakpoints(), true
			event.Kind = "running"
		case "quit":
			d.quit = true
			cmd.Kind = "stop"
			event.Kind, event.Reason = "terminated", "quit"
		}
		b.responseEpoch = req.Epoch
		b.pending = &cmd
		return event, nil
	case "stack":
		return debugsession.Event{Kind: "frames", Frames: b.frames}, nil
	case "break", "breaks", "delete", "sources":
		event := debugsession.Event{Messages: s.debugRefresh(nil)}
		switch req.Kind {
		case "break":
			file := s.debugFile(req.File)
			var bp *replBreakpoint
			for _, old := range d.breakpoints {
				if old.File == file && old.Line == req.Line {
					bp = old
					break
				}
			}
			if bp == nil {
				d.nextID++
				bp = &replBreakpoint{Breakpoint: debugger.Breakpoint{ID: d.nextID, File: file, Line: req.Line}}
				d.breakpoints = append(d.breakpoints, bp)
			}
			bp.Hash, bp.invalid = "", false
			if src := d.sources[file]; src != nil && src.lines[req.Line] {
				bp.Hash, bp.invalid = src.source.Hash, src.changed
			}
			event.Kind, event.Breakpoints = "breakpoints", []debugsession.Breakpoint{b.breakpoint(bp)}
		case "breaks":
			event.Kind = "breakpoints"
			for _, bp := range d.breakpoints {
				event.Breakpoints = append(event.Breakpoints, b.breakpoint(bp))
			}
		case "delete":
			for i, bp := range d.breakpoints {
				if bp.ID == req.Index {
					d.breakpoints = append(d.breakpoints[:i], d.breakpoints[i+1:]...)
					event.Kind, event.Index = "deleted", req.Index
					return event, nil
				}
			}
			return event, fmt.Errorf("unknown breakpoint ID")
		case "sources":
			event.Kind = "sources"
			var names []string
			for name := range d.sources {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				event.Sources = append(event.Sources, *d.sources[name].source)
			}
		}
		return event, nil
	}
	return debugsession.Event{}, fmt.Errorf("unsupported interpreter request %q", req.Kind)
}
