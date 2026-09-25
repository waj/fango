package eval

type stopPart struct {
	session *MachineSession
	cursor  *MachineIteratorSession
	cleanup bool
}

func newServiceSession(in *interp, service func(*MachineSession) (MachineEvent, error)) *MachineSession {
	return &MachineSession{interp: in, service: service}
}

func startStopSession(m *MachineSession, continueCleanup bool) *MachineSession {
	if m.finished {
		return newServiceSession(m.interp, func(*MachineSession) (MachineEvent, error) { return MachineEvent{Done: true}, nil })
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
			parts = append(parts, stopPart{session: active, cursor: pull.cursor, cleanup: pull.cleanup})
			active = pull.caller
		}
		parts = append(parts, stopPart{session: active})
		index := 0
		var primary *ExitRequest
		waiting := false
		return newServiceSession(m.interp, func(s *MachineSession) (MachineEvent, error) {
			if waiting && s.serviceReady {
				primary = suppress(primary, s.serviceExit)
				s.serviceExit, s.serviceReady = nil, false
				if cursor := parts[index-1].cursor; cursor != nil {
					cursor.finishClose()
				}
				waiting = false
			}
			if index == len(parts) {
				return MachineEvent{Done: true, Exit: primary}, nil
			}
			part := parts[index]
			index++
			waiting = true
			return MachineEvent{cleanup: startStopSession(part.session, part.cleanup)}, nil
		})
	}
	if !continueCleanup {
		m.cause = terminalOwnerStop
		m.waiting = nil
		m.pendingExit = nil
		m.drain = &machineDrain{handler: -1}
	} else if m.waiting != nil {
		m.replaySuspension = true
	}
	return m
}

func startCloseIterator(in *interp, it *MachineIteratorSession) *MachineSession {
	if it == nil || it.done {
		return newServiceSession(in, func(*MachineSession) (MachineEvent, error) { return MachineEvent{Done: true}, nil })
	}
	if it.closing {
		panic("eval: overlapping coroutine close")
	}
	it.closing = true
	it.stopped = true
	it.start = nil
	if it.registry {
		it.work.closed = true
	} else {
		it.evidence.Restore()
	}
	waiting := false
	started := false
	var primary *ExitRequest
	return newServiceSession(in, func(s *MachineSession) (MachineEvent, error) {
		if waiting && s.serviceReady {
			primary = suppress(primary, s.serviceExit)
			s.serviceExit, s.serviceReady = nil, false
			waiting = false
		}
		if it.registry {
			if child := it.last; child != nil {
				waiting = true
				return MachineEvent{cleanup: startCloseIterator(in, child)}, nil
			}
		} else if it.session != nil && !started {
			child := it.session
			started = true
			waiting = true
			return MachineEvent{cleanup: startStopSession(child, false)}, nil
		}
		it.finishClose()
		return MachineEvent{Done: true, Exit: primary}, nil
	})
}
