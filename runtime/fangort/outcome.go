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
	// Suppressed records exits a cleanup scope could not make primary: a
	// release that failed while the body was already exiting. Deterministic
	// inner-to-outer order; nothing reads it from fango yet.
	Suppressed []*ExitRequest
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

// Suppress returns primary carrying secondary as a suppressed exit. It
// copies: the primary request is reachable from the frame that raised it,
// so a cleanup scope must not edit an exit it is only forwarding.
func Suppress(primary, secondary *ExitRequest) *ExitRequest {
	if secondary == nil {
		return primary
	}
	if primary == nil {
		return secondary
	}
	joined := *primary
	joined.Suppressed = make([]*ExitRequest, 0, len(primary.Suppressed)+1)
	joined.Suppressed = append(joined.Suppressed, primary.Suppressed...)
	joined.Suppressed = append(joined.Suppressed, secondary)
	return &joined
}
