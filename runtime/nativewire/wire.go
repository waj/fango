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
}
