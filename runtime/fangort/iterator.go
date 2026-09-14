package fangort

import "fmt"

// MachineIterator is the private pull owner used by E8's scoped iterator
// lowering. It owns exactly one E7 machine. Source programs cannot construct or
// copy this Go value; the source ownership checker controls the cursor passed
// to a consumer scope.
type MachineIterator struct {
	machine *Machine
	started bool
	done    bool
}

func StartMachineIterator(entry MachineFrame) *MachineIterator {
	return &MachineIterator{machine: StartMachine(entry)}
}

// Next advances to one suspension. A normal machine return ends iteration;
// a machine exit is reported separately. The yielded request is the iterator
// element and Unit is supplied when production continues.
func (it *MachineIterator) Next() (value any, yielded bool, exit *ExitRequest, err error) {
	if it == nil || it.machine == nil {
		return nil, false, nil, fmt.Errorf("fangort: iterator has no machine")
	}
	if it.done {
		return nil, false, nil, nil
	}
	var event MachineEvent
	if it.started {
		event, err = it.machine.Resume(UnitValue)
	} else {
		it.started = true
		event, err = it.machine.Run()
	}
	if err != nil {
		return nil, false, nil, err
	}
	if !event.Done {
		return event.Request, true, nil, nil
	}
	it.done = true
	return nil, false, event.Exit, nil
}

// Close consumes unfinished production and returns a cleanup failure, if any.
// It is idempotent so a lexical owner may defer it while also exhausting the
// iterator normally. Source-level duplicate advancement is still a static
// ownership error rather than a dynamic consumed-token check.
func (it *MachineIterator) Close() (*ExitRequest, error) {
	if it == nil || it.machine == nil || it.done {
		return nil, nil
	}
	it.done = true
	return it.machine.Abandon()
}

func (it *MachineIterator) Stats() MachineStats {
	if it == nil || it.machine == nil {
		return MachineStats{}
	}
	return it.machine.Stats()
}

// PullMachineIterator is the compiler-owned generated-code bridge. Driver
// protocol errors indicate malformed compiler output, so they remain inside
// the runtime rather than becoming generated panic-based control flow.
func PullMachineIterator(it *MachineIterator) (value any, yielded bool, exit *ExitRequest) {
	value, yielded, exit, err := it.Next()
	if err != nil {
		panic(err)
	}
	return value, yielded, exit
}

// CloseMachineIterator is the matching generated-code bridge for scope exit.
func CloseMachineIterator(it *MachineIterator) *ExitRequest {
	exit, err := it.Close()
	if err != nil {
		panic(err)
	}
	return exit
}

// AssertNoMachineExit guards a statically Direct owner path. Reaching it
// indicates a compiler/runtime protocol mismatch, never source-level control.
func AssertNoMachineExit(exit *ExitRequest) {
	if exit != nil {
		panic(exit)
	}
}
