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
	Name, Type, Value string
	Handle            uint64
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
