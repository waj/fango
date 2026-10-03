// Package nativewire defines the private protocol shared by the interpreter
// and its native worker process.
package nativewire

// Value is one native argument or result. Kind names which field carries it;
// "bytes" is the bundled Bytes, which crosses as an ordinary []byte. gob does
// not distinguish a nil slice from an empty one, so an empty Bytes arrives
// nil — harmless, because Bytes.empty is nil.
type Value struct {
	Kind  string
	I     int64
	F     float64
	S     string
	R     int32
	B     bool
	Bytes []byte
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
	// Host IO replies also preserve the classification here, alongside Error,
	// so the proxy can reconstruct portable error identity before relabeling.
	Failure *Failure
}

// Failure mirrors fangort.IOFailure on the wire.
type Failure struct {
	Kind    int64
	Path    string
	Message string
}
