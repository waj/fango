package fangort

// PolyRequest is the checked ABI of a source-defined operation with
// operation-local type variables. The compiler constructs the payload after
// adapting its values to the clause's uniform representation.
type PolyRequest struct {
	Types []*TypeDescriptor
	Args  []any
}

// PolyReply carries the clause's result and its independently constructed
// source type identity. The caller checks it before restoring its value type.
type PolyReply struct {
	Type  *TypeDescriptor
	Value any
}

func CheckPolyReply(reply PolyReply, expected *TypeDescriptor) any {
	if !sameDescriptor(reply.Type, expected) {
		panic("fangort: polymorphic operation returned a value at the wrong type")
	}
	return reply.Value
}

func DecodePoly[T any](reply PolyReply, expected *TypeDescriptor, decode func(any) T) T {
	return decode(CheckPolyReply(reply, expected))
}

func DecodePolyOutcome[T any](out Outcome[PolyReply], expected *TypeDescriptor, decode func(any) T) Outcome[T] {
	if out.Exit != nil {
		return Propagate[T](out.Exit)
	}
	return Normal(decode(CheckPolyReply(out.Value, expected)))
}

func PolyOutcome[A any](out Outcome[A], descriptor *TypeDescriptor) Outcome[PolyReply] {
	if out.Exit != nil {
		return Propagate[PolyReply](out.Exit)
	}
	return Normal(PolyReply{Type: descriptor, Value: out.Value})
}
