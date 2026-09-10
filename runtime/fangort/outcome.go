package fangort

// ExitRequest is the erased dispatch envelope at the generated-module
// boundary. Effect and Operation are compiler-checked descriptor identities;
// Payload is boxed only after Core has checked every producer against that
// descriptor. No generated consumer uses a failed type assertion as a type
// check.
type ExitRequest struct {
	Target    *ExitTarget
	Effect    int
	Operation int
	Payload   []any
}

// ExitTarget is deliberately non-zero-sized. A fresh pointer is one handler
// activation identity, including recursive activations of the same handler.
type ExitTarget struct{ Marker byte }

// Outcome is the Exit calling convention. Exit == nil denotes normal
// completion and Value contains the result. A non-nil Exit abandons Value.
type Outcome[A any] struct {
	Value A
	Exit  *ExitRequest
}

func Normal[A any](value A) Outcome[A] { return Outcome[A]{Value: value} }

func Propagate[A any](exit *ExitRequest) Outcome[A] { return Outcome[A]{Exit: exit} }
