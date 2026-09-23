package fangort

// Completion retains a typed result or detached abort data, never an exit
// target. Its source row index is checked before erasure.
type Completion[A any] struct {
	value   A
	failure *Failure
}

func CaptureCompletion[A any](outcome Outcome[A]) Completion[A] {
	return Completion[A]{value: outcome.Value, failure: SnapshotFailure(outcome.Exit)}
}

func (c Completion[A]) Failure() *Failure { return c.failure }

func ReplayCompletion[A any](c Completion[A], row *EvidenceRow) Outcome[A] {
	if c.failure == nil {
		return Normal(c.value)
	}
	return Propagate[A](ReplayFailure(c.failure, row))
}

// ReplayFailure selects a module-owned typed adapter from current evidence.
// Suppressed snapshots remain detached: only the primary is executed.
func ReplayFailure(failure *Failure, row *EvidenceRow) *ExitRequest {
	for current := row; current != nil; current = current.tail {
		if family, ok := current.fields[failure.effect]; ok {
			if family.AbortReplay == nil {
				panic("fango: completion has no typed replay adapter")
			}
			exit := family.AbortReplay(failure)
			if exit == nil {
				panic("fango: stale completion operation proof")
			}
			for _, suppressed := range failure.suppressed {
				exit.Suppressed = append(exit.Suppressed, detachedExit(suppressed))
			}
			return exit
		}
	}
	panic("fango: completion effect absent from observing row")
}

func detachedExit(f *Failure) *ExitRequest {
	exit := &ExitRequest{Effect: f.effect, Operation: f.operationIndex, OperationName: f.operation, Payload: append([]any(nil), f.arguments...), PayloadTypes: append([]*TypeDescriptor(nil), f.descriptors...)}
	for _, child := range f.suppressed {
		exit.Suppressed = append(exit.Suppressed, detachedExit(child))
	}
	return exit
}

// CompletionPayload is a compiler-only typed projection. Unlike public
// inspection it permits opaque payloads, preserving their checked captures.
func CompletionPayload[A any](f *Failure, index int, expected *TypeDescriptor) A {
	if index < 0 || index >= len(f.arguments) || index >= len(f.descriptors) || !sameDescriptor(f.descriptors[index], expected) {
		panic("fango: stale completion payload proof")
	}
	value, ok := f.arguments[index].(A)
	if !ok {
		panic("fango: completion payload representation disagrees with proof")
	}
	return value
}

func CompletionOperation(f *Failure, effect, operation string, arity int) bool {
	return f != nil && f.effect == effect && f.operation == operation && len(f.arguments) == arity && len(f.descriptors) == arity
}

// CompletionMachine installs a private catch boundary below local handlers.
// It stays on the dispatcher stack across suspension and unwinds cleanup
// before publishing the detached failure.
func CompletionMachine[A any](child MachineFrame) MachineFrame {
	return &completionFrame[A]{child: child}
}

type completionFrame[A any] struct {
	child   MachineFrame
	started bool
}

func (f *completionFrame[A]) Step(m *Machine) MachineStep {
	if !f.started {
		f.started = true
		m.pushCompletion()
		return MachineStep{Kind: MachineCall, Frame: f.child}
	}
	if exit := m.TakeCaughtExit(); exit != nil {
		return MachineStep{Kind: MachineReturn, Value: CaptureCompletion(Propagate[A](exit))}
	}
	m.PopHandler()
	value, ok := m.TakeResult().(A)
	if !ok {
		panic("fango: completion result representation disagrees with proof")
	}
	return MachineStep{Kind: MachineReturn, Value: CaptureCompletion(Normal(value))}
}
func (f *completionFrame[A]) Clear() { f.child = nil }

func (m *Machine) pushCompletion() {
	m.handlers = append(m.handlers, machineHandler{completion: true, frameDepth: len(m.frames), cleanupDepth: len(m.cleanups), stateDepth: len(m.states)})
}
