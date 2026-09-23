package fangort

import "fmt"

// CursorResult is the private advancement result register. Generated code
// packages Value/Present into Maybe and propagates Exit before using Value.
type CursorResult struct {
	Finished bool
	Value    any
	Present  bool
	Exit     *ExitRequest
}

type machinePull struct {
	caller *Machine
	cursor *MachineIterator
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
				exit, err := cursor.Close()
				if err != nil {
					return MachineEvent{}, err
				}
				d.active.result = CursorResult{Exit: exit}
				continue
			}
			if cursor.done {
				d.active.result = CursorResult{}
				continue
			}
			if err := cursor.begin(event.reply); err != nil {
				return MachineEvent{}, err
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
			if cursor.started {
				if err := d.active.resumeLocal(event.reply); err != nil {
					return MachineEvent{}, err
				}
			}
			cursor.started = true
			if len(d.pulls) > m.stats.MaxPullDepth {
				m.stats.MaxPullDepth = len(d.pulls)
			}
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
			pull.cursor.done, pull.cursor.busy = true, false
			pull.cursor.evidence.Clear()
			d.active = pull.caller
			d.active.result = CursorResult{Exit: event.Exit, Value: event.Value, Finished: event.Exit == nil}
			continue
		}
		matched := -1
		for i := len(d.pulls) - 1; i >= 0; i-- {
			if d.pulls[i].cursor.owner == event.Owner {
				matched = i
				break
			}
		}
		if matched < 0 {
			d.suspended = true
			return event, nil
		}
		pull := d.pulls[matched]
		pull.cursor.machine.traversal = &machineTraversal{active: d.active,
			pulls: append([]machinePull(nil), d.pulls[matched+1:]...), suspended: true}
		for i := matched; i < len(d.pulls); i++ {
			d.pulls[i] = machinePull{}
		}
		d.pulls = d.pulls[:matched]
		pull.cursor.busy = false
		pull.cursor.evidence.Restore()
		d.active = pull.caller
		d.active.result = CursorResult{Value: event.Request, Present: true}
	}
}

func (m *Machine) Abandon() (*ExitRequest, error) {
	if m.finished && m.traversal == nil {
		return nil, fmt.Errorf("fangort: machine already completed")
	}
	d := m.traversal
	m.traversal = nil
	if d == nil {
		return m.abandonLocal()
	}
	var primary *ExitRequest
	// Restore every unfinished cursor before any cleanup runs. Inner cleanup
	// can itself refer through an enclosing cursor's residual row.
	for _, pull := range d.pulls {
		pull.cursor.evidence.Restore()
	}
	active := d.active
	for i := len(d.pulls) - 1; i >= -1; i-- {
		if !active.finished {
			exit, _ := active.abandonLocal()
			primary = Suppress(primary, exit)
		}
		if i >= 0 {
			pull := d.pulls[i]
			d.pulls[i] = machinePull{}
			pull.cursor.done, pull.cursor.busy = true, false
			pull.cursor.evidence.Clear()
			active = pull.caller
		}
	}
	return primary, nil
}
