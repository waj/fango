// Package nativewire defines the private protocol shared by the interpreter
// and its native worker process.
package nativewire

type Value struct {
	Kind string
	I    int64
	F    float64
	S    string
	R    int32
	B    bool
}

type Message struct {
	Kind   string
	Name   string
	Auth   string
	Error  string
	Panic  string
	Code   int
	Args   []Value
	Value  Value
	Data   []byte
	Values []string
	Bool   bool
	// Failure is a fallible native's classified error. It is deliberately
	// separate from Error (an infrastructure fault) and Panic (a native
	// panic): it is an ordinary language value, not a failure of the worker.
	Failure *Failure
}

// Failure mirrors fangort.IOFailure on the wire.
type Failure struct {
	Kind    int64
	Path    string
	Message string
}
