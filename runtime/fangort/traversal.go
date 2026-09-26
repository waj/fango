package fangort

import "fmt"

// CursorResult is the private advancement result register. Generated code
// packages Value/Present into Maybe and propagates Exit before using Value.
type CursorResult struct {
	Finished bool
	Polled   bool
	Value    any
	Present  bool
	Exit     *ExitRequest
}

type machinePull struct {
	caller  *Machine
	cursor  *MachineIterator
	cleanup bool
	close   bool
}

// A traversal owns the currently executing producer and its explicit callers.
// A yield to an enclosing cursor parks the unfinished inner advancements with
// that cursor, preserving their exclusive borrows until they actually finish.
type machineTraversal struct {
	active    *Machine
	pulls     []machinePull
	suspended bool
}

func (m *Machine) Run() (MachineEvent, error) {
	if m.finished {
		return MachineEvent{}, fmt.Errorf("fangort: machine already completed")
	}
	if m.traversal == nil {
		m.traversal = &machineTraversal{active: m}
	}
	if m.traversal.suspended || m.waiting {
		return MachineEvent{}, fmt.Errorf("fangort: suspended machine must be resumed")
	}
	return m.drive()
}

func (m *Machine) Resume(value any) (MachineEvent, error) {
	if m.finished {
		return MachineEvent{}, fmt.Errorf("fangort: machine already completed")
	}
	if m.traversal == nil || !m.traversal.suspended {
		return MachineEvent{}, fmt.Errorf("fangort: machine is not suspended")
	}
	if err := m.traversal.active.resumeLocal(value); err != nil {
		return MachineEvent{}, err
	}
	m.traversal.suspended = false
	return m.drive()
}

func (m *Machine) drive() (event MachineEvent, err error) {
	d := m.traversal
	defer func() {
		if err != nil {
			exit, _ := m.Abandon()
			event.Exit = Suppress(event.Exit, exit)
			event.Done = true
		}
	}()
	for {
		event, err = d.active.runLocal()
		if err != nil {
			return event, err
		}
		if cursor := event.advance; cursor != nil {
			if cursor.busy {
				return MachineEvent{}, fmt.Errorf("fangort: overlapping cursor advancement")
			}
			if event.close {
				callerPoll := d.active.poll
				d.pulls = append(d.pulls, machinePull{caller: d.active, cursor: cursor, close: true})
				if cursor.polled && cursor.closeMachine != nil {
					parked := cursor.closeMachine.traversal
					cursor.closeMachine.traversal = nil
					d.active = parked.active
					d.pulls = append(d.pulls, parked.pulls...)
				} else {
					cursor.closeMachine = StartCloseMachineIterator(cursor)
					d.active = cursor.closeMachine
				}
				cursor.polled = false
				if cursor.reportStop && cursor.poll != nil {
					d.active.poll = cursor.poll
				} else {
					d.active.poll = callerPoll
				}
				continue
			}
			if cursor.done {
				d.active.setCursorResult(CursorResult{})
				continue
			}
			if err := cursor.begin(event.reply); err != nil {
				return MachineEvent{}, err
			}
			if !cursor.started && len(d.active.states) != 0 {
				cursor.machine.states = append([]any(nil), d.active.states...)
				cursor.machine.parentStateOwner = d.active
				cursor.machine.parentStateCount = len(d.active.states)
			}
			cursor.busy = true
			cursor.evidence.Bind(event.evidence)
			d.pulls = append(d.pulls, machinePull{caller: d.active, cursor: cursor})
			if parked := cursor.machine.traversal; parked != nil {
				cursor.machine.traversal = nil
				d.active = parked.active
				d.pulls = append(d.pulls, parked.pulls...)
			} else {
				d.active = cursor.machine
			}
			if cursor.poll != nil {
				d.active.poll = cursor.poll
			} else if d.active.poll == nil {
				d.active.poll = d.pulls[len(d.pulls)-1].caller.poll
			}
			if cursor.started {
				if !cursor.polled {
					if err := d.active.resumeLocal(event.reply); err != nil {
						return MachineEvent{}, err
					}
				}
				cursor.polled = false
			}
			cursor.started = true
			if len(d.pulls) > m.stats.MaxPullDepth {
				m.stats.MaxPullDepth = len(d.pulls)
			}
			continue
		}
		if cleanup := event.cleanup; cleanup != nil {
			cleanup.poll = d.active.poll
			d.pulls = append(d.pulls, machinePull{caller: d.active, cleanup: true})
			d.active = cleanup
			continue
		}
		if event.Done {
			if len(d.pulls) == 0 {
				m.traversal = nil
				return event, nil
			}
			i := len(d.pulls) - 1
			pull := d.pulls[i]
			d.pulls[i] = machinePull{}
			d.pulls = d.pulls[:i]
			if pull.cleanup {
				d.active = pull.caller
				d.active.completeCleanup(event.Exit)
				continue
			}
			if pull.close {
				d.active = pull.caller
				if pull.cursor.reportStop && event.Exit != nil {
					pull.cursor.stopFailure = SnapshotFailure(event.Exit)
					event.Exit = nil
				}
				d.active.setCursorResult(CursorResult{Exit: event.Exit})
				continue
			}
			pull.cursor.done, pull.cursor.busy = true, false
			if pull.cursor.reportStop && event.Exit != nil {
				pull.cursor.stopFailure = SnapshotFailure(event.Exit)
				event.Exit = nil
			}
			pull.cursor.unlink()
			pull.cursor.evidence.Clear()
			pull.cursor.clearRegistered()
			d.active = pull.caller
			d.active.setCursorResult(CursorResult{Exit: event.Exit, Value: event.Value, Finished: event.Exit == nil && !pull.cursor.stopped})
			continue
		}
		if event.poll != nil {
			matched := -1
			for i := len(d.pulls) - 1; i >= 0; i-- {
				if d.pulls[i].cursor != nil && d.pulls[i].cursor.poll == event.poll {
					matched = i
					break
				}
			}
			if matched < 0 {
				return event, nil
			}
			pull := d.pulls[matched]
			root := pull.cursor.machine
			if pull.close {
				root = pull.cursor.closeMachine
			}
			root.traversal = &machineTraversal{active: d.active,
				pulls: append([]machinePull(nil), d.pulls[matched+1:]...)}
			for i := matched; i < len(d.pulls); i++ {
				d.pulls[i] = machinePull{}
			}
			d.pulls = d.pulls[:matched]
			pull.cursor.busy = false
			pull.cursor.polled = true
			pull.cursor.evidence.Restore()
			d.active = pull.caller
			d.active.setCursorResult(CursorResult{Polled: true})
			continue
		}
		matched := -1
		for i := len(d.pulls) - 1; i >= 0; i-- {
			if d.pulls[i].cursor != nil && d.pulls[i].cursor.owner == event.Owner {
				matched = i
				break
			}
		}
		if matched < 0 {
			d.suspended = true
			return event, nil
		}
		pull := d.pulls[matched]
		// An innermost pause needs only the producer's saved result edge.
		// Allocate parked traversal state only for a foreign pause that also
		// retains unfinished inner advancements.
		if matched+1 < len(d.pulls) {
			pull.cursor.machine.traversal = &machineTraversal{active: d.active,
				pulls: append([]machinePull(nil), d.pulls[matched+1:]...), suspended: true}
		}
		for i := matched; i < len(d.pulls); i++ {
			d.pulls[i] = machinePull{}
		}
		d.pulls = d.pulls[:matched]
		pull.cursor.busy = false
		pull.cursor.evidence.Restore()
		d.active = pull.caller
		d.active.setCursorResult(CursorResult{Value: event.Request, Present: true})
	}
}

func (m *Machine) Abandon() (*ExitRequest, error) {
	if m.finished && m.traversal == nil {
		return nil, fmt.Errorf("fangort: machine already completed")
	}
	stop := startStopMachine(m, false)
	event, err := stop.Run()
	if err != nil {
		return nil, err
	}
	if !event.Done {
		return nil, fmt.Errorf("fangort: suspending abandonment requires an execution driver")
	}
	return event.Exit, nil
}
