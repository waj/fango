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
	MachineAdvance
	MachineRun
	MachinePopCleanup
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
	Reply    any
	Close    bool
	Evidence *EvidenceRow
	Cursor   *MachineIterator
	Owner    *YieldOwner
	Kind     MachineStepKind
	Frame    MachineFrame
	Value    any
	Request  any
	Exit     *ExitRequest
}

// InvalidMachineStep is emitted only for supposedly unreachable generated
// control-flow states. Keeping the trap in the runtime prevents generated Go
// from using panic as a continuation or control-transfer representation.
func InvalidMachineStep(message string) MachineStep { panic(message) }

type MachineEvent struct {
	reply    any
	close    bool
	evidence *EvidenceRow
	advance  *MachineIterator
	cleanup  *Machine
	Owner    *YieldOwner
	Request  any
	Done     bool
	Value    any
	Exit     *ExitRequest
}

type MachineStats struct {
	MaxPullDepth int
	Steps        uint64
	MaxDepth     int
	MaxFrameCap  int
	MaxCleanups  int
	MaxStates    int
}

// MachineCleanup is a definition-site Direct/Exit release closure. A nil
// result is successful cleanup.
type MachineCleanup func() *ExitRequest

type machineCleanupEntry struct {
	sync  MachineCleanup
	start func() *Machine
}

// Machine owns the explicit caller-frame stack. Frames are interface values
// pointing at separately allocated typed frame objects; handler boundaries
// retain integer depths, never pointers to slice slots. Growing frames may
// therefore relocate its backing array without invalidating live state. The
// pending result register is consumed by the parent frame after a call return
// or by the suspended frame after Resume. Generated code uses TakeResult
// exactly once on that edge.
// terminalCause is never an effect payload. Owner stop bypasses user handlers
// and completion capture, while typed cleanup failures remain ordinary exits.
type terminalCause uint8

const (
	terminalNormal terminalCause = iota
	terminalAbort
	terminalOwnerStop
)

type Machine struct {
	pendingRun       func() (any, *ExitRequest)
	stopRouting      bool
	parentStateOwner *Machine
	parentStateCount int
	cause            terminalCause
	traversal        *machineTraversal
	frames           []MachineFrame
	cleanups         []machineCleanupEntry
	drain            *machineDrain
	states           []any
	handlers         []machineHandler
	caught           *ExitRequest
	result           any
	cursorResult     CursorResult
	hasCursorResult  bool
	waiting          bool
	replaySuspension bool
	lastSuspend      MachineEvent
	finished         bool
	stats            MachineStats
}

type machineDrain struct {
	primary *ExitRequest
	depth   int
	handler int
	pending *ExitRequest
	clause  bool
	prior   *ExitRequest
}

type machineHandler struct {
	completion   bool
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
func (m *Machine) runLocal() (event MachineEvent, err error) {
	if m.finished {
		return MachineEvent{}, fmt.Errorf("fangort: machine already completed")
	}
	if m.waiting && m.replaySuspension {
		m.replaySuspension = false
		return m.lastSuspend, nil
	}
	if m.waiting {
		return MachineEvent{}, fmt.Errorf("fangort: suspended machine must be resumed")
	}
	defer func() {
		if err != nil && !m.finished {
			exit, _ := m.abandonLocal()
			event.Exit = Suppress(event.Exit, exit)
			event.Done = true
		}
	}()
	if len(m.frames) == 0 || m.frames[len(m.frames)-1] == nil {
		return MachineEvent{}, fmt.Errorf("fangort: machine has no entry frame")
	}
	for len(m.frames) != 0 {
		if m.drain != nil {
			if event, done := m.advanceDrain(); done {
				if event.Done || event.cleanup != nil {
					return event, nil
				}
			}
			if m.drain != nil {
				continue
			}
		}
		m.stats.Steps++
		active := m.frames[len(m.frames)-1]
		var step MachineStep
		if m.pendingRun != nil {
			run := m.pendingRun
			m.pendingRun = nil
			value, exit := run()
			if exit == nil {
				m.result = value
				continue
			}
			step = MachineStep{Kind: MachineExit, Exit: exit}
		} else {
			step = active.Step(m)
		}
		switch step.Kind {
		case MachineContinue:
			continue
		case MachineRun:
			run, ok := step.Value.(func() (any, *ExitRequest))
			if !ok || run == nil {
				return MachineEvent{}, fmt.Errorf("fangort: machine invocation has no body")
			}
			m.pendingRun = run
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
					m.cause = terminalAbort
					m.clearFrames()
					m.finished = true
					return MachineEvent{Done: true, Exit: exit}, nil
				}
				m.clearFrames()
				m.finished = true
				return MachineEvent{Done: true, Value: step.Value}, nil
			}
			m.result = step.Value
		case MachineSuspend:
			m.waiting = true
			m.lastSuspend = MachineEvent{Owner: step.Owner, Request: step.Request}
			return m.lastSuspend, nil
		case MachineAdvance:
			if step.Cursor == nil {
				return MachineEvent{}, fmt.Errorf("fangort: advancement has no cursor")
			}
			return MachineEvent{advance: step.Cursor, evidence: step.Evidence, reply: step.Reply, close: step.Close}, nil
		case MachinePopCleanup:
			if len(m.cleanups) == 0 {
				return MachineEvent{}, fmt.Errorf("fangort: machine cleanup stack underflow")
			}
			i := len(m.cleanups) - 1
			cleanup := m.cleanups[i]
			m.cleanups[i] = machineCleanupEntry{}
			m.cleanups = m.cleanups[:i]
			if cleanup.start != nil {
				child := cleanup.start()
				if child == nil {
					return MachineEvent{}, fmt.Errorf("fangort: machine cleanup factory returned nil")
				}
				return MachineEvent{cleanup: child}, nil
			}
			if exit := cleanup.sync(); exit != nil {
				m.result = exit
			} else {
				m.result = nil
			}
		case MachineExit:
			m.beginExitDrain(step.Exit)
			continue
		default:
			return MachineEvent{}, fmt.Errorf("fangort: invalid machine step %d", step.Kind)
		}
	}
	return MachineEvent{}, fmt.Errorf("fangort: machine exhausted without completion")
}

func (m *Machine) beginExitDrain(exit *ExitRequest) {
	handler, depth := -1, 0
	for i := len(m.handlers) - 1; i >= 0; i-- {
		h := m.handlers[i]
		if m.cause == terminalOwnerStop && h.completion || !h.completion && exit.Target != h.target {
			continue
		}
		handler, depth = i, h.cleanupDepth
		break
	}
	m.drain = &machineDrain{primary: exit, depth: depth, handler: handler}
	if handler < 0 {
		m.cause = terminalAbort
	}
}

func (m *Machine) completeCleanup(exit *ExitRequest) {
	if m.drain != nil {
		if m.cause == terminalOwnerStop {
			if m.drain.clause {
				m.drain.clause = false
				if exit != nil {
					exit = Suppress(exit, m.drain.prior)
				}
				m.drain.prior = nil
			}
			m.drain.pending = Suppress(m.drain.pending, exit)
			return
		}
		m.drain.primary = Suppress(m.drain.primary, exit)
		return
	}
	if exit != nil {
		m.result = exit
	} else {
		m.result = nil
	}
}

// advanceDrain runs one cleanup at a time. An asynchronous release is driven
// by the enclosing traversal, so the original owner and primary exit remain
// live until that release completes.
func (m *Machine) advanceDrain() (MachineEvent, bool) {
	d := m.drain
	limit := d.depth
	if m.cause == terminalOwnerStop && d.pending != nil {
		match := -1
		for i := len(m.handlers) - 1; i >= 0; i-- {
			h := m.handlers[i]
			if !h.completion && h.target == d.pending.Target {
				match = i
				break
			}
		}
		if match < 0 {
			d.primary = Suppress(d.primary, d.pending)
			d.pending = nil
		} else if len(m.cleanups) <= m.handlers[match].cleanupDepth {
			pending := d.pending
			d.pending = nil
			m.stopRouting = true
			caught := m.routeExit(pending)
			m.stopRouting = false
			if !caught {
				panic("fangort: cleanup handler disappeared during stop")
			}
			step := m.frames[len(m.frames)-1].Step(m)
			if step.Kind != MachineCall || step.Frame == nil {
				panic("fangort: invalid cleanup abort clause entry")
			}
			clause := StartMachine(step.Frame)
			clause.states = append([]any(nil), m.states...)
			clause.parentStateOwner, clause.parentStateCount = m, len(m.states)
			d.clause, d.prior = true, pending
			return MachineEvent{cleanup: clause}, true
		} else {
			limit = m.handlers[match].cleanupDepth
		}
	}
	for len(m.cleanups) > limit {
		i := len(m.cleanups) - 1
		cleanup := m.cleanups[i]
		m.cleanups[i] = machineCleanupEntry{}
		m.cleanups = m.cleanups[:i]
		if cleanup.start != nil {
			return MachineEvent{cleanup: cleanup.start()}, true
		}
		secondary := cleanup.sync()
		if secondary != nil && m.cause == terminalOwnerStop {
			d.pending = Suppress(d.pending, secondary)
			return MachineEvent{}, false
		}
		d.primary = Suppress(d.primary, secondary)
	}
	if d.pending != nil {
		return MachineEvent{}, false
	}
	m.drain = nil
	if d.handler < 0 {
		m.clearFrames()
		m.finished = true
		return MachineEvent{Done: true, Exit: d.primary}, true
	}
	h := m.handlers[d.handler]
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
	m.handlers = m.handlers[:d.handler]
	m.result = nil
	m.TakeCursorResult()
	m.caught = d.primary
	return MachineEvent{}, false
}

// beginStopLocal discards the suspended production edge and enters cleanup.
// Its owner and frames remain live until the last release completes.
func (m *Machine) beginStopLocal() {
	if m.finished || m.drain != nil {
		return
	}
	m.cause = terminalOwnerStop
	m.waiting = false
	m.pendingRun = nil
	m.drain = &machineDrain{depth: 0, handler: -1}
}

func (m *Machine) resumeLocal(value any) error {
	if m.finished {
		return fmt.Errorf("fangort: machine already completed")
	}
	if !m.waiting {
		return fmt.Errorf("fangort: machine is not suspended")
	}
	m.result = value
	m.waiting = false
	m.lastSuspend = MachineEvent{}
	return nil
}

// Abandon consumes an unfinished private machine, runs every pending cleanup,
// and clears all frame, handler, and state storage. E8's owned consumers use
// this operation when they stop pulling before normal completion.
func (m *Machine) abandonLocal() (*ExitRequest, error) {
	if m.finished {
		return nil, fmt.Errorf("fangort: machine already completed")
	}
	m.cause = terminalOwnerStop
	exit := m.unwind(nil, 0)
	m.clearFrames()
	m.waiting = false
	m.finished = true
	return exit, nil
}

func (m *Machine) TakeResult() any {
	if m.hasCursorResult {
		return m.TakeCursorResult()
	}
	value := m.result
	m.result = nil
	return value
}

// TakeCursorResult consumes the advancement register without boxing it into
// the general call-result interface. The compatibility path in TakeResult is
// also used by handwritten frames.
func (m *Machine) TakeCursorResult() CursorResult {
	value := m.cursorResult
	m.cursorResult = CursorResult{}
	m.hasCursorResult = false
	return value
}

func (m *Machine) setCursorResult(value CursorResult) {
	m.result = nil
	m.cursorResult = value
	m.hasCursorResult = true
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
	if m.parentStateOwner != nil && token >= 0 && token < m.parentStateCount {
		return m.parentStateOwner.State(token)
	}
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
	if m.parentStateOwner != nil && token >= 0 && token < m.parentStateCount {
		m.parentStateOwner.SetState(token, value)
		return
	}
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
		if m.cause == terminalOwnerStop && h.completion {
			continue
		}
		if !h.completion && exit.Target != h.target {
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
		m.TakeCursorResult()
		m.caught = exit
		return true
	}
	return false
}

// PushCleanup registers an acquired scope before its body may suspend.
func (m *Machine) PushCleanup(cleanup MachineCleanup) {
	m.cleanups = append(m.cleanups, machineCleanupEntry{sync: cleanup})
	if len(m.cleanups) > m.stats.MaxCleanups {
		m.stats.MaxCleanups = len(m.cleanups)
	}
}

// PushMachineCleanup registers a release that may suspend. The factory starts
// only after the scope exits and retains definition-site evidence until then.
func (m *Machine) PushMachineCleanup(start func() *Machine) {
	m.cleanups = append(m.cleanups, machineCleanupEntry{start: start})
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
		m.cleanups[i] = machineCleanupEntry{}
		m.cleanups = m.cleanups[:i]
		if cleanup.start != nil {
			panic("fangort: suspending cleanup requires machine drain")
		}
		if secondary := cleanup.sync(); secondary != nil {
			if m.cause == terminalOwnerStop && !m.stopRouting {
				secondary = m.resolveStopExit(secondary)
			}
			primary = Suppress(primary, secondary)
		}
	}
	return primary
}

func (m *Machine) clearFrames() {
	m.pendingRun = nil
	m.drain = nil
	m.parentStateOwner = nil
	m.parentStateCount = 0
	for i := len(m.frames) - 1; i >= 0; i-- {
		if m.frames[i] != nil {
			m.frames[i].Clear()
			m.frames[i] = nil
		}
	}
	m.frames = nil
	m.result = nil
	m.TakeCursorResult()
	for i := range m.cleanups {
		m.cleanups[i] = machineCleanupEntry{}
	}
	m.cleanups = nil
	for i := range m.states {
		m.states[i] = nil
	}
	m.states = nil
	m.handlers = nil
	m.caught = nil
}

// RunSynchronousMachine executes a callback whose non-suspension obligation
// was checked before effect-row widening. Its outcome can still be an exit.
func RunSynchronousMachine[A any](entry MachineFrame) Outcome[A] {
	m := StartMachine(entry)
	event, err := m.Run()
	if err != nil {
		panic(err)
	}
	if !event.Done {
		_, _ = m.Abandon()
		panic("fangort: suspension escaped a checked synchronous callback")
	}
	if event.Exit != nil {
		return Propagate[A](event.Exit)
	}
	return Normal(event.Value.(A))
}
