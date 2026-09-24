package fangort

import (
	"errors"
	"fmt"
)

// YieldOwner is a lexical suspension destination. Its nonzero size ensures
// separately allocated live owners have distinct pointer identities.
type YieldOwner struct{ marker byte }

func NewYieldOwner() *YieldOwner { return &YieldOwner{marker: 1} }

// MachineIterator is the private pull owner used by typed coroutine
// lowering. It owns exactly one machine. Source programs can alias the opaque
// cursor within its scope; checked capture and access contracts govern those
// aliases before the private runtime representation is selected.
type MachineIterator struct {
	registered                   bool
	work                         *WorkOwner
	registry                     bool
	parent, previous, next, last *MachineIterator
	start                        func(any) MachineFrame
	evidence                     *CursorEvidence
	busy                         bool
	owner                        *YieldOwner
	machine                      *Machine
	started                      bool
	done                         bool
}

// Close consumes unfinished production and returns a cleanup failure, if any.
// It is idempotent so a lexical owner may defer it while also exhausting the
// iterator normally. Overlapping advancement is checked statically; repeated
// sequential reads observe stable exhaustion.
func (it *MachineIterator) Close() (*ExitRequest, error) {
	if it == nil || it.done {
		return nil, nil
	}
	if it.busy {
		return nil, fmt.Errorf("fangort: overlapping coroutine close")
	}
	it.done = true
	defer it.clearRegistered()
	it.unlink()
	if it.registry {
		it.work.closed = true
		defer it.evidence.Clear()
		var primary *ExitRequest
		var failure error
		for it.last != nil {
			child := it.last
			exit, err := child.Close()
			primary = Suppress(primary, exit)
			failure = errors.Join(failure, err)
			// A corrupt overlapping close must not trap the cleanup driver.
			child.unlink()
		}
		return primary, failure
	}
	it.start = nil
	if it.machine == nil {
		it.evidence.Clear()
		return nil, nil
	}
	it.evidence.Restore()
	exit, err := it.machine.Abandon()
	it.evidence.Clear()
	return exit, err
}

func (it *MachineIterator) Stats() MachineStats {
	if it == nil || it.machine == nil {
		return MachineStats{}
	}
	return it.machine.Stats()
}

// CloseMachineIterator is the matching generated-code bridge for scope exit.
func CloseMachineIterator(it *MachineIterator) *ExitRequest {
	exit, err := it.Close()
	if err != nil {
		panic(err)
	}
	return exit
}

// RunCursorConsumer is the checked Direct/Exit boundary for owned Drive control.
// Advancement is handled by the dispatcher; residual suspension is forbidden
// by the scope's Core control contract. Register closure before the first step
// so internal failures also unwind the producer.
func RunCursorConsumer[A any](it *MachineIterator, entry MachineFrame) Outcome[A] {
	m := StartMachine(entry)
	m.PushCleanup(func() *ExitRequest { return CloseMachineIterator(it) })
	event, err := m.Run()
	if err != nil {
		panic(err)
	}
	if !event.Done {
		_, _ = m.Abandon()
		panic("fangort: foreign suspension escaped a synchronous cursor scope")
	}
	if event.Exit != nil {
		return Propagate[A](event.Exit)
	}
	return Normal(event.Value.(A))
}

func (it *MachineIterator) unlink() {
	if it.parent == nil {
		return
	}
	if it.previous != nil {
		it.previous.next = it.next
	}
	if it.next != nil {
		it.next.previous = it.previous
	} else {
		it.parent.last = it.previous
	}
	it.parent, it.previous, it.next = nil, nil, nil
}

func NewCoroutineScope(evidence *CursorEvidence) *MachineIterator {
	return &MachineIterator{registry: true, work: &WorkOwner{}, evidence: evidence}
}
func RegisterCoroutine(scope, child *MachineIterator) *MachineIterator {
	if scope == nil || !scope.registry || scope.done {
		panic("coroutine allocation requires a live scope")
	}
	child.registered = true
	child.parent, child.previous = scope, scope.last
	if scope.last != nil {
		scope.last.next = child
	}
	scope.last = child
	return child
}

func CoroutineWorkOwner(scope *MachineIterator) *WorkOwner { return scope.work }

func CoroutineScopeEvidence(scope *MachineIterator) *EvidenceRow { return scope.evidence.Row() }

// Dynamic terminal handles do not retain even an empty execution session (and,
// in the interpreter, its environment). Lexical host cursors retain statistics.
func (it *MachineIterator) clearRegistered() {
	if it.registered {
		it.machine = nil
		it.start = nil
		it.owner = nil
		it.evidence.Clear()
	}
}
