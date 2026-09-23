package eval

import "fmt"

func (s *MachineSession) resolveStopExit(exit *ExitRequest) (*ExitRequest, error) {
	for exit != nil {
		pending := exit
		s.stopRouting = true
		caught, err := s.catchExit(exit)
		s.stopRouting = false
		if err != nil {
			return exit, err
		}
		if !caught {
			return exit, nil
		}
		pending = s.stopCaught
		s.stopCaught = nil
		last := len(s.frames) - 1
		frame := s.frames[last]
		s.frames[last] = nil
		s.frames = s.frames[:last]
		clause := &MachineSession{program: s.program, interp: s.interp, workers: s.workers, closures: s.closures, frames: []*machineFrame{frame}, states: append([]Value(nil), s.states...), parentStateOwner: s, parentStateCount: len(s.states)}
		event, err := clause.Run()
		if err != nil {
			return suppress(pending, event.Exit), err
		}
		if !event.Done {
			cleanup, _ := clause.Abandon()
			return suppress(pending, cleanup), fmt.Errorf("eval: suspension escaped a synchronous cleanup handler")
		}
		exit = event.Exit
		if exit != nil {
			exit = suppress(exit, pending)
		}
	}
	return nil, nil
}

func (s *MachineSession) stateAt(token int) Value {
	if s.parentStateOwner != nil && token >= 0 && token < s.parentStateCount {
		return s.parentStateOwner.stateAt(token)
	}
	return s.states[token]
}
func (s *MachineSession) setStateAt(token int, value Value) {
	if s.parentStateOwner != nil && token >= 0 && token < s.parentStateCount {
		s.parentStateOwner.setStateAt(token, value)
		return
	}
	s.states[token] = value
}
