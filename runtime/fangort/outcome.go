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
	// Trace is captured at the abort site only when its target requests it.
	// TraceCaptured distinguishes an empty captured stack from no capture.
	Trace         []Frame
	TraceCaptured bool
	// Suppressed records exits a cleanup scope could not make primary: a
	// release that failed while the body was already exiting. Deterministic
	// inner-to-outer order; reporting handlers expose detached snapshots.
	Suppressed []*ExitRequest
}

// ExitTarget is deliberately non-zero-sized. A fresh pointer is one handler
// activation identity, including recursive activations of the same handler.
type ExitTarget struct {
	Marker byte
	// CaptureTrace is immutable for the lifetime of this activation.
	CaptureTrace bool
	resolve      func() *ExitTarget
}

// CaptureExit attaches diagnostics before the caller's stack unwinds. Target
// must already be resolved, so the policy belongs to the actual activation.
// Forwarding an existing request never captures it again.
func CaptureExit(exit *ExitRequest) *ExitRequest {
	if !exit.TraceCaptured && exit.Target != nil && exit.Target.CaptureTrace {
		exit.Trace = Capture(0)
		exit.TraceCaptured = true
	}
	return exit
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

func MapOutcome[A, B any](out Outcome[A], convert func(A) B) Outcome[B] {
	if out.Exit != nil {
		return Propagate[B](out.Exit)
	}
	return Normal(convert(out.Value))
}

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
