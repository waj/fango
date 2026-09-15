package eval

import (
	"errors"
	"fmt"

	machineir "github.com/waj/fango/internal/machine"
)

// cursorAdvanceRequest retains the checked result packaging at the transfer.
type cursorAdvanceRequest struct {
	cursor *MachineIteratorSession
	term   *machineir.CursorAdvance
}

type machinePull struct {
	caller *MachineSession
	term   *machineir.CursorAdvance
	cursor *MachineIteratorSession
}

// A traversal owns the currently executing producer and its explicit callers.
// A yield to an enclosing cursor parks the unfinished inner advancements with
// that cursor, preserving their exclusive borrows until they actually finish.
type machineTraversal struct {
	active    *MachineSession
	pulls     []machinePull
	suspended bool
}

func (m *MachineSession) Run() (MachineEvent, error) {
	if m.finished {
		return MachineEvent{}, fmt.Errorf("eval: machine already completed")
	}
	if m.traversal == nil {
		m.traversal = &machineTraversal{active: m}
	}
	if m.traversal.suspended || m.waiting != nil {
		return MachineEvent{}, fmt.Errorf("eval: suspended machine must be resumed")
	}
	return m.drive()
}

func (m *MachineSession) Resume(value Value) (MachineEvent, error) {
	if m.finished {
		return MachineEvent{}, fmt.Errorf("eval: machine already completed")
	}
	if m.traversal == nil || !m.traversal.suspended {
		return MachineEvent{}, fmt.Errorf("eval: machine is not suspended")
	}
	if err := m.traversal.active.resumeLocal(value); err != nil {
		return MachineEvent{}, err
	}
	m.traversal.suspended = false
	return m.drive()
}

func (m *MachineSession) drive() (event MachineEvent, err error) {
	d := m.traversal
	defer func() {
		if err != nil {
			exit, closeErr := m.Abandon()
			err = errors.Join(err, closeErr)
			event.Exit = suppress(event.Exit, exit)
			event.Done = true
		}
	}()
	for {
		event, err = d.active.runLocal()
		if err != nil {
			return event, err
		}
		if request := event.advance; request != nil {
			cursor := request.cursor
			if cursor.busy {
				return MachineEvent{}, fmt.Errorf("eval: overlapping cursor advancement")
			}
			if cursor.done {
				d.active.completeAdvance(request.term, nil, false, nil)
				continue
			}
			if cursor.session == nil {
				return MachineEvent{}, fmt.Errorf("eval: cursor has no producer")
			}
			cursor.busy = true
			d.pulls = append(d.pulls, machinePull{caller: d.active, cursor: cursor, term: request.term})
			if parked := cursor.session.traversal; parked != nil {
				cursor.session.traversal = nil
				d.active = parked.active
				d.pulls = append(d.pulls, parked.pulls...)
			} else {
				d.active = cursor.session
			}
			if cursor.started {
				if err := d.active.resumeLocal(struct{}{}); err != nil {
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
			d.active = pull.caller
			d.active.completeAdvance(pull.term, nil, false, event.Exit)
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
		pull.cursor.session.traversal = &machineTraversal{active: d.active,
			pulls: append([]machinePull(nil), d.pulls[matched+1:]...), suspended: true}
		for i := matched; i < len(d.pulls); i++ {
			d.pulls[i] = machinePull{}
		}
		d.pulls = d.pulls[:matched]
		pull.cursor.busy = false
		d.active = pull.caller
		d.active.completeAdvance(pull.term, event.Request, true, nil)
	}
}

func (m *MachineSession) Abandon() (*ExitRequest, error) {
	if m.finished && m.traversal == nil {
		return nil, fmt.Errorf("eval: machine already completed")
	}
	d := m.traversal
	m.traversal = nil
	if d == nil {
		return m.abandonLocal()
	}
	var primary *ExitRequest
	var failure error
	active := d.active
	for i := len(d.pulls) - 1; i >= -1; i-- {
		if !active.finished {
			exit, err := active.abandonLocal()
			failure = errors.Join(failure, err)
			primary = suppress(primary, exit)
		}
		if i >= 0 {
			pull := d.pulls[i]
			d.pulls[i] = machinePull{}
			pull.cursor.done, pull.cursor.busy = true, false
			active = pull.caller
		}
	}
	return primary, failure
}

func (m *MachineSession) completeAdvance(term *machineir.CursorAdvance, value Value, present bool, exit *ExitRequest) {
	if exit != nil {
		m.pendingExit = exit
		return
	}
	result := &CtorVal{Ctor: term.Result.Ctors[0]}
	if present {
		result = &CtorVal{Ctor: term.Result.Ctors[1], Fields: []Value{value}}
	}
	m.frames[len(m.frames)-1].vars[term.Bind.Name] = result
}
