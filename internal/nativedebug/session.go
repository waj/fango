package nativedebug

import (
	"crypto/sha256"
	"fmt"
	"sort"

	"github.com/waj/fango/internal/debugger"
	"github.com/waj/fango/internal/debugsession"
	"github.com/waj/fango/runtime/fangort"
)

// SessionBackend adapts Delve without owning terminal IO or its child process.
// The launcher remains responsible for terminating the child on session exit.
type SessionBackend struct {
	Controller *Controller
	// Visible frame indexes map to actual Delve activations for this pause.
	nativeFrames []int
}

func (b *SessionBackend) Capabilities() debugsession.Capabilities {
	return debugsession.Capabilities{Name: "native", Frame: true, Expand: true, Args: true, Raw: true, Interrupt: true}
}
func (b *SessionBackend) Interrupt() error { return b.Controller.Interrupt() }

func (b *SessionBackend) location(file string, line int) debugsession.Location {
	c := b.Controller
	loc := debugsession.Location{File: file, Line: line, Text: c.Manifest.Sources[file]}
	if text, ok := c.Manifest.Sources[file]; ok {
		loc.Hash = fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	}
	if span, enclosing := c.currentSpan(file, line); span != nil {
		loc.Start, loc.End, loc.Precision = span.Start, span.End, debugsession.Exact
		if enclosing {
			loc.Precision = debugsession.Enclosing
		}
	}
	return loc
}

// Snapshot is available after launch and after every synchronous move.
func (b *SessionBackend) Snapshot() debugsession.Event {
	c := b.Controller
	c.handles = nil
	b.nativeFrames = nil
	if c.State.Exited {
		return debugsession.Event{Kind: "terminated", Reason: "exited", ExitStatus: c.State.ExitStatus}
	}
	event := debugsession.Event{Kind: "stopped", Reason: c.State.StopReason}
	frames, err := b.frames()
	if err != nil {
		event.Error = err.Error()
	}
	event.Frames = frames
	if len(frames) == 0 && c.State.CurrentThread != nil {
		t := c.State.CurrentThread
		event.Frames = []debugsession.Frame{{Location: b.location(t.File, t.Line)}}
	}
	if t := c.State.CurrentThread; t != nil {
		if _, ok := c.Manifest.Sources[t.File]; !ok {
			event.Messages = []string{"generated or native code; use step to reach Fango"}
		}
	}
	return event
}
func (b *SessionBackend) frames() ([]debugsession.Frame, error) {
	frames, err := b.Controller.Frames()
	if err != nil {
		return nil, err
	}
	var result []debugsession.Frame
	var indexes []int
	for i, f := range frames {
		_, source := b.Controller.Manifest.Sources[f.File]
		// Keep a native stop visible instead of attributing its PC to a caller.
		if !source && i != 0 {
			continue
		}
		name := "<unknown Fango function>"
		if !source {
			name = "<native> " + nativeFrameName(f)
		} else if symbol, ok := fangort.DemangleWithEntryModule(nativeFrameName(f), b.Controller.Manifest.EntryModule); ok {
			name = symbol.String()
		}
		result = append(result, debugsession.Frame{Name: name, Location: b.location(f.File, f.Line)})
		indexes = append(indexes, i)
	}
	// Publish only a complete stack; failed refreshes retain the old mapping.
	b.nativeFrames = indexes
	return result, nil
}

func nativeFrameName(f Frame) string {
	if f.Function == nil {
		return "?"
	}
	return f.Function.Name
}

func (b *SessionBackend) nativeFrame(index int) (int, error) {
	if b.nativeFrames == nil {
		if _, err := b.frames(); err != nil {
			return 0, err
		}
	}
	if index < 0 || index >= len(b.nativeFrames) {
		return 0, fmt.Errorf("source frame %d is unavailable", index)
	}
	return b.nativeFrames[index], nil
}

func (b *SessionBackend) breakpoint(bp Breakpoint) debugsession.Breakpoint {
	return debugsession.Breakpoint{ID: bp.ID, File: bp.File, Line: bp.Line, Hash: b.location(bp.File, bp.Line).Hash, Status: "verified"}
}

func (b *SessionBackend) Execute(req debugsession.Request) (debugsession.Event, error) {
	c := b.Controller
	switch req.Kind {
	case "next", "finish", "locals", "args", "print", "raw":
		if req.Kind != "raw" || req.RawCommand != "stack" {
			frame, err := b.nativeFrame(req.Frame)
			if err != nil {
				return debugsession.Event{}, err
			}
			req.Frame = frame
		}
	}
	switch req.Kind {
	case "continue", "step", "next", "finish":
		b.nativeFrames = nil
		err := c.moveFrame(req.Kind, req.Frame)
		if err != nil {
			var out struct{ State State }
			if stateErr := c.call("State", map[string]any{"NonBlocking": true}, &out); stateErr != nil {
				return debugsession.Event{}, err
			}
			c.State = out.State
			if out.State.Running {
				return debugsession.Event{Kind: "running"}, err
			}
		}
		return b.Snapshot(), err
	case "quit":
		c.handles = nil
		b.nativeFrames = nil
		return debugsession.Event{Kind: "terminated", Reason: "quit"}, nil
	case "break":
		bp, err := c.Break(fmt.Sprintf("%s:%d", req.File, req.Line))
		if err != nil {
			return debugsession.Event{}, err
		}
		return debugsession.Event{Kind: "breakpoints", Breakpoints: []debugsession.Breakpoint{b.breakpoint(bp)}}, nil
	case "breaks":
		event := debugsession.Event{Kind: "breakpoints"}
		var ids []int
		for id := range c.users {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		for _, id := range ids {
			event.Breakpoints = append(event.Breakpoints, b.breakpoint(c.users[id]))
		}
		return event, nil
	case "delete":
		if err := c.Delete(req.Index); err != nil {
			return debugsession.Event{}, err
		}
		return debugsession.Event{Kind: "deleted", Index: req.Index}, nil
	case "stack":
		frames, err := b.frames()
		if err != nil {
			return debugsession.Event{}, err
		}
		return debugsession.Event{Kind: "frames", Frames: frames}, nil
	case "locals", "args", "print":
		values, err := c.variables(req.Kind, req.Arg, req.Frame)
		return debugsession.Event{Kind: "inspection", Variables: values}, err
	case "expand":
		values, more, err := c.expand(req.Handle, req.Offset)
		return debugsession.Event{Kind: "inspection", Variables: values, More: more}, err
	case "raw":
		if req.RawCommand == "stack" {
			frames, err := c.Frames()
			if err != nil {
				return debugsession.Event{}, err
			}
			event := debugsession.Event{Kind: "raw-frames"}
			for _, f := range frames {
				event.Frames = append(event.Frames, debugsession.Frame{Name: nativeFrameName(f), Location: b.location(f.File, f.Line)})
			}
			return event, nil
		}
		values, err := c.rawVariables(req.RawCommand, req.Arg, req.Frame)
		return debugsession.Event{Kind: "raw", Raw: values}, err
	case "sources":
		event := debugsession.Event{Kind: "sources"}
		var names []string
		for name := range c.Manifest.Sources {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			loc := b.location(name, 0)
			event.Sources = append(event.Sources, debugger.Source{Name: name, Hash: loc.Hash, Text: loc.Text})
		}
		return event, nil
	}
	return debugsession.Event{}, fmt.Errorf("unsupported native request %q", req.Kind)
}
