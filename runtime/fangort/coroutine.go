package fangort

import "fmt"

// StartMachineCoroutine retains a checked producer factory without invoking it.
// Its argument and both suspension edges have the protocol checked by Core and
// Machine lint; generated module code performs their concrete projections.
func StartMachineCoroutine(owner *YieldOwner, evidence *CursorEvidence, start func(any) MachineFrame) *MachineIterator {
	return &MachineIterator{owner: owner, evidence: evidence, start: start}
}

func (it *MachineIterator) begin(input any) error {
	if it.start != nil {
		start := it.start
		it.start = nil
		entry := start(input)
		if entry == nil {
			it.done = true
			it.unlink()
			it.evidence.Clear()
			it.clearRegistered()
			return fmt.Errorf("fangort: coroutine factory returned no frame")
		}
		it.machine = StartMachine(entry)
	}
	if it.machine == nil {
		return fmt.Errorf("fangort: coroutine has no producer")
	}
	return nil
}

// SuspendMachine is the Machine member of an owner's scoped pause callback.
// It contains no public continuation and can only be resumed by the dispatcher.
func SuspendMachine(owner *YieldOwner, request any) MachineFrame {
	return &pauseFrame{owner: owner, request: request}
}

type pauseFrame struct {
	owner   *YieldOwner
	request any
	waiting bool
}

func (f *pauseFrame) Step(m *Machine) MachineStep {
	if !f.waiting {
		f.waiting = true
		return MachineStep{Kind: MachineSuspend, Owner: f.owner, Request: f.request}
	}
	return MachineStep{Kind: MachineReturn, Value: m.TakeResult()}
}
func (f *pauseFrame) Clear() { f.owner = nil; f.request = nil }
