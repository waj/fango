package fangort

import "fmt"

// MachineStepKind is the private generated-code protocol for E7 execution
// frames. It is runtime state, not a source-level continuation state check.
type MachineStepKind uint8

const (
	MachineContinue MachineStepKind = iota
	MachineCall
	MachineTailCall
	MachineReturn
	MachineSuspend
	MachineExit
)

// MachineFrame is implemented by module-owned generated frame types. Step
// performs local work only until the next inter-frame transition; it must not
// invoke another frame's Step recursively. Clear drops completed references.
type MachineFrame interface {
	Step(*Machine) MachineStep
	Clear()
}

type MachineStep struct {
	Kind    MachineStepKind
	Frame   MachineFrame
	Value   any
	Request any
	Exit    *ExitRequest
}

type MachineEvent struct {
	Request any
	Done    bool
	Value   any
	Exit    *ExitRequest
}

type MachineStats struct {
	Steps       uint64
	MaxDepth    int
	MaxCleanups int
}

// MachineCleanup is a definition-site Direct/Exit release closure. E7 does
// not permit it to suspend. A nil result is successful cleanup.
type MachineCleanup func() *ExitRequest

// Machine owns the explicit caller-frame stack. The pending result register is
// consumed by the parent frame after a call return or by the suspended frame
// after Resume. Generated code uses TakeResult exactly once on that edge.
type Machine struct {
	frames   []MachineFrame
	cleanups []MachineCleanup
	result   any
	waiting  bool
	finished bool
	stats    MachineStats
}

func StartMachine(entry MachineFrame) *Machine {
	m := &Machine{frames: []MachineFrame{entry}}
	if entry != nil {
		m.stats.MaxDepth = 1
	}
	return m
}

// Run dispatches iteratively until suspension or completion.
func (m *Machine) Run() (MachineEvent, error) {
	if m.finished {
		return MachineEvent{}, fmt.Errorf("fangort: machine already completed")
	}
	if m.waiting {
		return MachineEvent{}, fmt.Errorf("fangort: suspended machine must be resumed")
	}
	if len(m.frames) == 0 || m.frames[len(m.frames)-1] == nil {
		return MachineEvent{}, fmt.Errorf("fangort: machine has no entry frame")
	}
	for len(m.frames) != 0 {
		m.stats.Steps++
		active := m.frames[len(m.frames)-1]
		step := active.Step(m)
		switch step.Kind {
		case MachineContinue:
			continue
		case MachineCall:
			if step.Frame == nil {
				return MachineEvent{}, fmt.Errorf("fangort: machine call has no frame")
			}
			m.frames = append(m.frames, step.Frame)
			if len(m.frames) > m.stats.MaxDepth {
				m.stats.MaxDepth = len(m.frames)
			}
		case MachineTailCall:
			if step.Frame == nil {
				return MachineEvent{}, fmt.Errorf("fangort: machine tail call has no frame")
			}
			active.Clear()
			m.frames[len(m.frames)-1] = step.Frame
		case MachineReturn:
			active.Clear()
			m.frames[len(m.frames)-1] = nil
			m.frames = m.frames[:len(m.frames)-1]
			if len(m.frames) == 0 {
				if exit := m.unwind(nil, 0); exit != nil {
					m.result = nil
					m.finished = true
					return MachineEvent{Done: true, Exit: exit}, nil
				}
				m.result = nil
				m.finished = true
				return MachineEvent{Done: true, Value: step.Value}, nil
			}
			m.result = step.Value
		case MachineSuspend:
			m.waiting = true
			return MachineEvent{Request: step.Request}, nil
		case MachineExit:
			step.Exit = m.unwind(step.Exit, 0)
			m.clearFrames()
			m.finished = true
			return MachineEvent{Done: true, Exit: step.Exit}, nil
		default:
			return MachineEvent{}, fmt.Errorf("fangort: invalid machine step %d", step.Kind)
		}
	}
	return MachineEvent{}, fmt.Errorf("fangort: machine exhausted without completion")
}

func (m *Machine) Resume(value any) (MachineEvent, error) {
	if m.finished {
		return MachineEvent{}, fmt.Errorf("fangort: machine already completed")
	}
	if !m.waiting {
		return MachineEvent{}, fmt.Errorf("fangort: machine is not suspended")
	}
	m.result = value
	m.waiting = false
	return m.Run()
}

func (m *Machine) TakeResult() any {
	value := m.result
	m.result = nil
	return value
}

func (m *Machine) Stats() MachineStats { return m.stats }

// PushCleanup registers an acquired scope before its body may suspend.
func (m *Machine) PushCleanup(cleanup MachineCleanup) {
	m.cleanups = append(m.cleanups, cleanup)
	if len(m.cleanups) > m.stats.MaxCleanups {
		m.stats.MaxCleanups = len(m.cleanups)
	}
}

// CleanupDepth snapshots the lexical cleanup boundary owned by a frame.
func (m *Machine) CleanupDepth() int { return len(m.cleanups) }

// PopCleanup completes the innermost normal scope.
func (m *Machine) PopCleanup() *ExitRequest {
	if len(m.cleanups) == 0 {
		panic("fangort: machine cleanup stack underflow")
	}
	return m.unwind(nil, len(m.cleanups)-1)
}

// UnwindTo runs scopes inside-out down to depth. Primary remains the answer;
// cleanup failures are appended using the synchronous bracket ordering.
func (m *Machine) UnwindTo(primary *ExitRequest, depth int) *ExitRequest {
	if depth < 0 || depth > len(m.cleanups) {
		panic("fangort: invalid machine cleanup depth")
	}
	return m.unwind(primary, depth)
}

func (m *Machine) unwind(primary *ExitRequest, depth int) *ExitRequest {
	for len(m.cleanups) > depth {
		i := len(m.cleanups) - 1
		cleanup := m.cleanups[i]
		m.cleanups[i] = nil
		m.cleanups = m.cleanups[:i]
		if secondary := cleanup(); secondary != nil {
			primary = Suppress(primary, secondary)
		}
	}
	return primary
}

func (m *Machine) clearFrames() {
	for i := len(m.frames) - 1; i >= 0; i-- {
		if m.frames[i] != nil {
			m.frames[i].Clear()
			m.frames[i] = nil
		}
	}
	m.frames = nil
	m.result = nil
	for i := range m.cleanups {
		m.cleanups[i] = nil
	}
	m.cleanups = nil
}
