package fangort

import (
	"errors"
	"fmt"
)

// Host-driven fixture adapters; source programs use typed coroutine exchange.
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
