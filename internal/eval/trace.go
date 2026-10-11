package eval

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/runtime/fangort"
)

// The interpreter keeps the call stack compiled programs read from the Go
// line table: one frame per worker call, closure call, or forced cell, each
// remembering the origin of the call it is currently making, so a crash or
// `Trace.capture` reports the same functions and lines as generated code
// (doc/design/repl.md, "Call stack").
type traceFrame struct {
	name string // as a trace prints it
	site source.Span
	// lambda marks a function literal's frame; literals nested in it keep
	// its name, as Go's closure naming keeps the enclosing function's.
	lambda bool
	// cell marks a forced top-level value. Generated Go initializes those in
	// package init, which a trace never shows, so the frame stays hidden
	// and only names the lambdas created under it.
	cell bool
}

// TraceError is a runtime failure with the Fango stack it unwound through.
// Its message carries the trace, so it survives the worker boundary as text.
type TraceError struct {
	Err    error
	Frames []fangort.Frame
}

func (e *TraceError) Error() string {
	if len(e.Frames) == 0 {
		return e.Err.Error()
	}
	return e.Err.Error() + "\n\nFango trace:\n" + strings.TrimSuffix(fangort.FormatTrace(e.Frames), "\n")
}

func (e *TraceError) Unwrap() error { return e.Err }

// pushFrame enters a worker, named as a trace prints it.
func (in *interp) pushFrame(name string) {
	in.trace = append(in.trace, traceFrame{name: fangort.SymbolName(name)})
}

func (in *interp) pushLambda(label string) {
	in.trace = append(in.trace, traceFrame{name: label, lambda: true})
}

func (in *interp) pushCell(name string) {
	in.trace = append(in.trace, traceFrame{name: name, cell: true})
}

func (in *interp) popFrame() {
	in.trace = in.trace[:len(in.trace)-1]
}

// callSite records where the executing frame is about to call. An
// expression evaluated outside any function, such as a prompt input, gets a
// root frame so its own line is reported.
func (in *interp) callSite(sp source.Span) {
	if sp.File == nil {
		return
	}
	if len(in.trace) == 0 {
		in.trace = append(in.trace, traceFrame{name: "<expression>"})
	}
	in.trace[len(in.trace)-1].site = sp
}

// lambdaLabel names a closure created now the way generated Go's frames do:
// a function literal inside a worker is that worker's lambda; one inside a
// top-level value's initializer belongs only to its module.
func (in *interp) lambdaLabel() string {
	if len(in.trace) == 0 {
		return "<lambda>"
	}
	top := in.trace[len(in.trace)-1]
	if top.lambda {
		return top.name
	}
	if !top.cell {
		return top.name + " (lambda)"
	}
	if i := strings.LastIndexByte(top.name, '.'); i >= 0 {
		return top.name[:i] + ".<lambda>"
	}
	return "<lambda>"
}

// frames renders the stack innermost first.
func (in *interp) frames() []fangort.Frame {
	out := make([]fangort.Frame, 0, len(in.trace))
	for i := len(in.trace) - 1; i >= 0; i-- {
		frame := in.trace[i]
		if frame.cell {
			continue
		}
		rendered := fangort.Frame{Function: frame.name}
		if frame.site.File != nil {
			rendered.File = frame.site.File.Name
			rendered.Line = frame.site.StartPos().Line
		}
		if n := len(out); n != 0 && out[n-1] == rendered {
			continue
		}
		out = append(out, rendered)
	}
	return out
}

// traced attaches the current stack to a failure the first time it passes a
// frame boundary. Exits, cancellation, and the compile-time restrictions are
// control flow or classified diagnostics, not crashes, and stay as they are.
func (in *interp) traced(err error) error {
	if err == nil || in.compileTime {
		return err
	}
	var already *TraceError
	var exit *fangort.ExitError
	var unsafe *UnsafeNativeError
	if errors.As(err, &already) || errors.As(err, &exit) || errors.As(err, &unsafe) ||
		errors.Is(err, ErrStepBudget) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &TraceError{Err: err, Frames: in.frames()}
}

// traceCaptureName is the native `Trace.capture` reads; the interpreter
// answers it from its own stack, since the sidecar would see only Go frames.
const traceCaptureName = "Trace.captureRaw"

// recoverPanic converts a Go panic inside evaluation into a traced failure
// for the deferred caller; the frames are still on the stack because
// unwinding runs no pops.
func (in *interp) recoverPanic(err *error) {
	if p := recover(); p != nil {
		*err = in.recovered(p)
	}
}

// recovered is a panic as a traced failure.
func (in *interp) recovered(p any) error {
	if err, ok := p.(error); ok {
		return in.traced(err)
	}
	return in.traced(fmt.Errorf("%v", p))
}

// captureFrames is `Trace.captureRaw` in the interpreter: the frames above
// this module's own, innermost first.
func (in *interp) captureFrames() []fangort.Frame {
	frames := in.frames()
	for len(frames) > 0 && strings.HasPrefix(frames[0].Function, "Trace.") {
		frames = frames[1:]
	}
	return frames
}

// inherit gives a child evaluator the stack it was started from.
func (in *interp) inherit(parent *interp) {
	in.trace = append([]traceFrame(nil), parent.trace...)
}
