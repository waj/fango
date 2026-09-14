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

// ImmediateMachine adapts a non-suspending Direct or Exit callback to the
// machine frame protocol. The callback is invoked once by the dispatcher; it
// is not a continuation and cannot be resumed or copied from Fango source.
func ImmediateMachine(run func() (any, *ExitRequest)) MachineFrame {
	return &immediateMachineFrame{run: run}
}

type immediateMachineFrame struct {
	run func() (any, *ExitRequest)
}

func (f *immediateMachineFrame) Step(*Machine) MachineStep {
	value, exit := f.run()
	if exit != nil {
		return MachineStep{Kind: MachineExit, Exit: exit}
	}
	return MachineStep{Kind: MachineReturn, Value: value}
}

func (f *immediateMachineFrame) Clear() { f.run = nil }

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
	MaxFrameCap int
	MaxCleanups int
	MaxStates   int
}

// MachineCleanup is a definition-site Direct/Exit release closure. E7 does
// not permit it to suspend. A nil result is successful cleanup.
type MachineCleanup func() *ExitRequest

// Machine owns the explicit caller-frame stack. Frames are interface values
// pointing at separately allocated typed frame objects; handler boundaries
// retain integer depths, never pointers to slice slots. Growing frames may
// therefore relocate its backing array without invalidating live state. The
// pending result register is consumed by the parent frame after a call return
// or by the suspended frame after Resume. Generated code uses TakeResult
// exactly once on that edge.
type Machine struct {
	frames   []MachineFrame
	cleanups []MachineCleanup
	states   []any
	handlers []machineHandler
	caught   *ExitRequest
	result   any
	waiting  bool
	finished bool
	stats    MachineStats
}

type machineHandler struct {
	target       *ExitTarget
	frameDepth   int
	cleanupDepth int
	stateDepth   int
}

func StartMachine(entry MachineFrame) *Machine {
	m := &Machine{frames: []MachineFrame{entry}}
	m.stats.MaxFrameCap = cap(m.frames)
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
			if cap(m.frames) > m.stats.MaxFrameCap {
				m.stats.MaxFrameCap = cap(m.frames)
			}
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
			if m.routeExit(step.Exit) {
				continue
			}
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

// Abandon consumes an unfinished private machine, runs every pending cleanup,
// and clears all frame, handler, and state storage. E8's owned consumers use
// this operation when they stop pulling before normal completion.
func (m *Machine) Abandon() (*ExitRequest, error) {
	if m.finished {
		return nil, fmt.Errorf("fangort: machine already completed")
	}
	exit := m.unwind(nil, 0)
	m.clearFrames()
	m.waiting = false
	m.finished = true
	return exit, nil
}

func (m *Machine) TakeResult() any {
	value := m.result
	m.result = nil
	return value
}

func (m *Machine) TakeCaughtExit() *ExitRequest {
	exit := m.caught
	m.caught = nil
	return exit
}

func (m *Machine) Stats() MachineStats { return m.stats }

func (m *Machine) PushState(value any) int {
	token := len(m.states)
	m.states = append(m.states, value)
	if len(m.states) > m.stats.MaxStates {
		m.stats.MaxStates = len(m.states)
	}
	return token
}

func (m *Machine) State(token int) any {
	if token < 0 || token >= len(m.states) {
		panic("fangort: invalid machine state token")
	}
	return m.states[token]
}

// TopStateToken identifies the innermost live handler state cell. It is used
// only while routing an abort into that handler's clause frame.
func (m *Machine) TopStateToken() int {
	if len(m.states) == 0 {
		panic("fangort: machine state stack underflow")
	}
	return len(m.states) - 1
}

func (m *Machine) SetState(token int, value any) {
	if token < 0 || token >= len(m.states) {
		panic("fangort: invalid machine state token")
	}
	m.states[token] = value
}

func (m *Machine) PopState() any {
	if len(m.states) == 0 {
		panic("fangort: machine state stack underflow")
	}
	i := len(m.states) - 1
	value := m.states[i]
	m.states[i] = nil
	m.states = m.states[:i]
	return value
}

func (m *Machine) PushHandler(target *ExitTarget) {
	if target == nil {
		panic("fangort: machine handler has no target")
	}
	m.handlers = append(m.handlers, machineHandler{target: target, frameDepth: len(m.frames),
		cleanupDepth: len(m.cleanups), stateDepth: len(m.states)})
}

func (m *Machine) PopHandler() {
	if len(m.handlers) == 0 {
		panic("fangort: machine handler stack underflow")
	}
	m.handlers = m.handlers[:len(m.handlers)-1]
}

func (m *Machine) routeExit(exit *ExitRequest) bool {
	for i := len(m.handlers) - 1; i >= 0; i-- {
		h := m.handlers[i]
		if exit.Target != h.target {
			continue
		}
		exit = m.unwind(exit, h.cleanupDepth)
		for len(m.frames) > h.frameDepth {
			last := len(m.frames) - 1
			m.frames[last].Clear()
			m.frames[last] = nil
			m.frames = m.frames[:last]
		}
		for len(m.states) > h.stateDepth {
			last := len(m.states) - 1
			m.states[last] = nil
			m.states = m.states[:last]
		}
		m.handlers = m.handlers[:i]
		m.result = nil
		m.caught = exit
		return true
	}
	return false
}

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
	for i := range m.states {
		m.states[i] = nil
	}
	m.states = nil
	m.handlers = nil
	m.caught = nil
}
