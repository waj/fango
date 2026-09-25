package fangort

// stopPart is one suspended producer in an unfinished advancement chain.
// The cursor is closed only after that producer's release has completed.
type stopPart struct {
	machine *Machine
	cursor  *MachineIterator
	cleanup bool
}

type stopTraversalFrame struct {
	parts   []stopPart
	next    int
	waiting bool
	primary *ExitRequest
}

func (f *stopTraversalFrame) Step(m *Machine) MachineStep {
	if f.waiting {
		if exit, ok := m.TakeResult().(*ExitRequest); ok {
			f.primary = Suppress(f.primary, exit)
		}
		part := f.parts[f.next-1]
		if part.cursor != nil {
			part.cursor.finishClose()
		}
		f.waiting = false
	}
	if f.next == len(f.parts) {
		if f.primary != nil {
			return MachineStep{Kind: MachineExit, Exit: f.primary}
		}
		return MachineStep{Kind: MachineReturn, Value: Unit{}}
	}
	part := f.parts[f.next]
	f.next++
	f.waiting = true
	m.PushMachineCleanup(func() *Machine { return startStopMachine(part.machine, part.cleanup) })
	return MachineStep{Kind: MachinePopCleanup}
}

func (f *stopTraversalFrame) Clear() { f.parts = nil; f.primary = nil }

func startStopMachine(m *Machine, continueCleanup bool) *Machine {
	if m == nil || m.finished {
		return StartMachine(ImmediateMachine(func() (any, *ExitRequest) { return Unit{}, nil }))
	}
	if d := m.traversal; d != nil {
		m.traversal = nil
		parts := make([]stopPart, 0, len(d.pulls)+1)
		for _, pull := range d.pulls {
			if pull.cursor != nil {
				pull.cursor.evidence.Restore()
			}
		}
		active := d.active
		for i := len(d.pulls) - 1; i >= 0; i-- {
			pull := d.pulls[i]
			parts = append(parts, stopPart{machine: active, cursor: pull.cursor, cleanup: pull.cleanup})
			active = pull.caller
		}
		parts = append(parts, stopPart{machine: active})
		return StartMachine(&stopTraversalFrame{parts: parts})
	}
	if !continueCleanup {
		m.beginStopLocal()
	} else if m.waiting {
		m.replaySuspension = true
	}
	return m
}
