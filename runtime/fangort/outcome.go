package fangort

// ExitRequest is the erased dispatch envelope at the generated-module
// boundary. Effect and Operation are compiler-checked descriptor identities;
// Payload is boxed only after Core has checked every producer against that
// descriptor. No generated consumer uses a failed type assertion as a type
// check.
type ExitRequest struct {
	Target *ExitTarget
	// Effect is the canonical declaration name, stable across import graphs.
	// Dispatch uses Target; compiler-local numeric identities never cross the ABI.
	Effect        string
	Operation     int
	OperationName string
	Payload       []any
	PayloadTypes  []*TypeDescriptor
	// Suppressed records exits a cleanup scope could not make primary: a
	// release that failed while the body was already exiting. Deterministic
	// inner-to-outer order; nothing reads it from Fango yet.
	Suppressed []*ExitRequest
}

// ExitTarget is deliberately non-zero-sized. A fresh pointer is one handler
// activation identity, including recursive activations of the same handler.
type ExitTarget struct {
	Marker  byte
	resolve func() *ExitTarget
}

// DeferredExitTarget is used only by checked residual-evidence projections.
// Exit requests resolve it before unwinding, so dispatch targets stay fixed.
func DeferredExitTarget(resolve func() *ExitTarget) *ExitTarget {
	return &ExitTarget{resolve: resolve}
}

func ResolveExitTarget(target *ExitTarget) *ExitTarget {
	for target != nil && target.resolve != nil {
		target = target.resolve()
	}
	return target
}

// Outcome is the Exit calling convention. Exit == nil denotes normal
// completion and Value contains the result. A non-nil Exit abandons Value.
type Outcome[A any] struct {
	Value A
	Exit  *ExitRequest
}

func Normal[A any](value A) Outcome[A] { return Outcome[A]{Value: value} }

// RequireNormal projects an Exit-family call whose Core contract proves Direct
// execution. This bridge is needed when a pure computation constructs a value
// with the Exit representation. An unexpected exit is a compiler invariant
// violation, never a failure that may be silently dropped.
func RequireNormal[A any](outcome Outcome[A]) A {
	if outcome.Exit != nil {
		panic("fango: Exit from a statically Direct call")
	}
	return outcome.Value
}

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
