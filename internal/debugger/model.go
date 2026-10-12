// Package debugger defines source debugging data and the transport-independent
// control protocol shared by the REPL and its interpreter worker.
package debugger

import "context"

const Version = 1

// Source is an immutable snapshot. Paths remain logical compiler paths; the
// frontend resolves them against its source and library roots.
type Source struct {
	Name, Hash, Text string
}

type Binding struct {
	Name, Runtime, Type string
}

// Site identifies a source expression and the lexical names visible before it
// executes. Positions are byte offsets and survive execution serialization.
type Site struct {
	Source                   *Source
	Start, End, Line, Column int
	Bindings                 []Binding
}

type Breakpoint struct {
	ID         int
	File, Hash string
	Line       int
}

type Config struct {
	Step        bool
	Breakpoints []Breakpoint
}

type Frame struct {
	ID   uint64
	Name string
	Site *Site
}

type Variable struct {
	Name, Type string
	Value      Value
	Handle     uint64
}

// Event is either a stop notification or an inspection response. Handles and
// frame indexes are meaningful only until the next resume command.
type Event struct {
	Kind, Reason, Error string
	Frames              []Frame
	Variables           []Variable
	More                bool
}

type Command struct {
	ReplaceBreakpoints bool
	Kind               string
	Frame              int
	Name               string
	Handle             uint64
	Offset             int
	Breakpoints        []Breakpoint
}

// Host runs on the frontend side of a stop. A host must return stop when its
// input ends or is interrupted, even if the evaluation context is still live.
type Host interface {
	DebugExchange(context.Context, Event) Command
}

// NativeScope describes the source bindings before a generated statement.
// Runtime names are Go identifiers supplied by the emitter, not reconstructed
// by the debugger. Multiple scopes can share a source line.
type NativeScope struct {
	Span     *NativeSpan `json:",omitempty"`
	File     string
	Line     int
	Bindings []Binding
}

// NativeSpan is a byte range in the binary's immutable source snapshot.
type NativeSpan struct{ Start, End int }

// NativeType describes emitted nominal layouts. Keys use fully qualified Go
// type names without generic arguments; the value names are source names.
type NativeType struct {
	Name, Kind   string
	Constructors []NativeConstructor
}
type NativeConstructor struct {
	Name   string
	Tag    int
	Fields []NativeField
}
type NativeField struct{ Name, Runtime string }
