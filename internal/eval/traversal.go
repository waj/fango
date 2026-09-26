package eval

import (
	"errors"
	"fmt"

	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/runtime/fangort"
)

// cursorAdvanceRequest retains the checked result packaging at the transfer.
type cursorAdvanceRequest struct {
	input  Value
	row    *fangort.EvidenceRow
	cursor *MachineIteratorSession
	term   *machineir.CursorAdvance
}

type machinePull struct {
	caller  *MachineSession
	term    *machineir.CursorAdvance
	cursor  *MachineIteratorSession
	cleanup bool
	close   bool
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
			if request.term.Close {
				callerPoll := d.active.poll
				d.pulls = append(d.pulls, machinePull{caller: d.active, cursor: cursor, term: request.term, close: true})
				if cursor.polled && cursor.closeSession != nil {
					parked := cursor.closeSession.traversal
					cursor.closeSession.traversal = nil
					d.active = parked.active
					d.pulls = append(d.pulls, parked.pulls...)
				} else {
					cursor.closeSession = startCloseIterator(d.active.interp, cursor)
					d.active = cursor.closeSession
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
				d.active.completeAdvance(request.term, nil, false, false, false, nil)
				continue
			}
			if err := cursor.begin(request.input); err != nil {
				return MachineEvent{}, err
			}
			cursor.busy = true
			cursor.evidence.Bind(request.row)
			d.pulls = append(d.pulls, machinePull{caller: d.active, cursor: cursor, term: request.term})
			if parked := cursor.session.traversal; parked != nil {
				cursor.session.traversal = nil
				d.active = parked.active
				d.pulls = append(d.pulls, parked.pulls...)
			} else {
				d.active = cursor.session
			}
			if cursor.poll != nil {
				d.active.poll = cursor.poll
			} else if d.active.poll == nil {
				d.active.poll = d.pulls[len(d.pulls)-1].caller.poll
			}
			if cursor.started {
				if !cursor.polled {
					if err := d.active.resumeLocal(request.input); err != nil {
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
					pull.cursor.stopExit = detachCompletionExit(event.Exit)
					event.Exit = nil
				}
				d.active.completeAdvance(pull.term, nil, false, false, false, event.Exit)
				continue
			}
			pull.cursor.done, pull.cursor.busy = true, false
			if pull.cursor.reportStop && event.Exit != nil {
				pull.cursor.stopExit = detachCompletionExit(event.Exit)
				event.Exit = nil
			}
			pull.cursor.unlink()
			pull.cursor.evidence.Clear()
			pull.cursor.clearRegistered()
			d.active = pull.caller
			d.active.completeAdvance(pull.term, event.Value, false, event.Exit == nil && !pull.cursor.stopped, false, event.Exit)
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
			root := pull.cursor.session
			if pull.close {
				root = pull.cursor.closeSession
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
			d.active.completeAdvance(pull.term, nil, false, false, true, nil)
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
		pull.cursor.session.traversal = &machineTraversal{active: d.active,
			pulls: append([]machinePull(nil), d.pulls[matched+1:]...), suspended: true}
		for i := matched; i < len(d.pulls); i++ {
			d.pulls[i] = machinePull{}
		}
		d.pulls = d.pulls[:matched]
		pull.cursor.busy = false
		pull.cursor.evidence.Restore()
		d.active = pull.caller
		d.active.completeAdvance(pull.term, event.Request, true, false, false, nil)
	}
}

func (m *MachineSession) Abandon() (*ExitRequest, error) {
	if m.finished && m.traversal == nil {
		return nil, fmt.Errorf("eval: machine already completed")
	}
	stop := startStopSession(m, false)
	event, err := stop.Run()
	if err != nil {
		return nil, err
	}
	if !event.Done {
		return nil, fmt.Errorf("eval: suspending abandonment requires an execution driver")
	}
	return event.Exit, nil
}

func (m *MachineSession) completeAdvance(term *machineir.CursorAdvance, value Value, present, finished, polled bool, exit *ExitRequest) {
	if exit != nil {
		m.pendingExit = exit
		return
	}
	if term.Close && term.Result == nil {
		m.frames[len(m.frames)-1].vars[term.Bind.Name] = struct{}{}
		return
	}
	index := 2
	if present {
		index = 0
	} else if finished {
		index = 1
	} else if polled {
		index = 3
	}
	result := &CtorVal{Ctor: term.Result.Ctors[index]}
	if index != 2 {
		result.Fields = []Value{value}
	}
	m.frames[len(m.frames)-1].vars[term.Bind.Name] = result
}
