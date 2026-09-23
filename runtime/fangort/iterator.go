package fangort

import (
	"errors"
	"fmt"
)

// YieldOwner is a lexical suspension destination. Its nonzero size ensures
// separately allocated live owners have distinct pointer identities.
type YieldOwner struct{ marker byte }

func NewYieldOwner() *YieldOwner { return &YieldOwner{marker: 1} }

// MachineIterator is the private pull owner used by E8's scoped iterator
// lowering. It owns exactly one machine. Source programs can alias the opaque
// cursor within its scope; checked capture and access contracts govern those
// aliases before the private runtime representation is selected.
type MachineIterator struct {
	start    func(any) MachineFrame
	exchange bool
	evidence *CursorEvidence
	busy     bool
	owner    *YieldOwner
	machine  *Machine
	started  bool
	done     bool
}

func StartMachineIterator(entry MachineFrame) *MachineIterator {
	return &MachineIterator{machine: StartMachine(entry)}
}

func StartOwnedMachineIterator(owner *YieldOwner, entry MachineFrame) *MachineIterator {
	return &MachineIterator{owner: owner, machine: StartMachine(entry)}
}

func StartCursorWithEvidence(owner *YieldOwner, evidence *CursorEvidence, entry MachineFrame) *MachineIterator {
	return &MachineIterator{owner: owner, evidence: evidence, machine: StartMachine(entry)}
}

// Next advances to one suspension. A normal machine return ends iteration;
// a machine exit is reported separately. The yielded request is the iterator
// element and Unit is supplied when production continues.
func (it *MachineIterator) Next() (value any, yielded bool, exit *ExitRequest, err error) {
	return it.NextWithEvidence(nil)
}

func (it *MachineIterator) NextWithEvidence(row *EvidenceRow) (value any, yielded bool, exit *ExitRequest, err error) {
	if it == nil || it.machine == nil {
		return nil, false, nil, fmt.Errorf("fangort: iterator has no machine")
	}
	if it.done {
		return nil, false, nil, nil
	}
	if it.busy {
		return nil, false, nil, fmt.Errorf("fangort: overlapping cursor advancement")
	}
	it.evidence.Bind(row)
	defer func() {
		if it.done {
			it.evidence.Clear()
		} else {
			it.evidence.Restore()
		}
	}()
	var event MachineEvent
	if it.started {
		event, err = it.machine.Resume(UnitValue)
	} else {
		it.started = true
		event, err = it.machine.Run()
	}
	if err != nil {
		it.done = true
		return nil, false, event.Exit, err
	}
	if !event.Done {
		if event.Owner != it.owner {
			exit, closeErr := it.Close()
			return nil, false, exit, errors.Join(fmt.Errorf("fangort: suspension reached a different cursor owner"), closeErr)
		}
		return event.Request, true, nil, nil
	}
	it.done = true
	return nil, false, event.Exit, nil
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

// RunCursorConsumer is the checked Direct/Exit boundary for an owned Traversal.
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

// AssertNoMachineExit guards a statically Direct owner path. Reaching it
// indicates a compiler/runtime protocol mismatch, never source-level control.
func AssertNoMachineExit(exit *ExitRequest) {
	if exit != nil {
		panic(exit)
	}
}
