package eval

import (
	"context"
	"fmt"

	"github.com/waj/fango/internal/core"
	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/types"
	"github.com/waj/fango/runtime/fangort"
)

// MachineEvent is the private host-facing state of an E7 execution machine.
// Request is non-nil only when execution has suspended. Done distinguishes a
// completed nil/Unit-like value from suspension. This protocol is not exposed
// to Fango source; E8 supplies the ownership contract for source consumers.
type MachineEvent struct {
	Request Value
	Done    bool
	Value   Value
	Exit    *ExitRequest
}

type MachineStats struct {
	Steps        int
	MaxDepth     int
	MaxLiveSlots int
	MaxCleanups  int
}

type machineFrame struct {
	worker     *machineir.Worker
	block      machineir.BlockID
	vars       map[string]Value
	returnBind string
}

// MachineSession owns one suspended computation. The session, its frames, and
// Resume are compiler-internal Go APIs; no copyable continuation value exists
// in Fango or Core.
type MachineSession struct {
	interp   *interp
	workers  map[string]*machineir.Worker
	frames   []*machineFrame
	waiting  *machineir.Local
	finished bool
	stats    MachineStats
	cleanups []func() (*ExitRequest, error)
}

// StartMachine validates and initializes an iterative machine evaluation.
// Call Run to reach the first suspension or completion.
func StartMachine(ctx context.Context, p *machineir.Prog, entry string, args []Value, env *Env, ioctx *IOContext) (*MachineSession, error) {
	if errs := machineir.Lint(p); len(errs) != 0 {
		return nil, fmt.Errorf("eval: malformed machine IR: %v", errs[0])
	}
	workers := make(map[string]*machineir.Worker, len(p.Workers))
	for i := range p.Workers {
		workers[p.Workers[i].Name] = &p.Workers[i]
	}
	worker := workers[entry]
	if worker == nil {
		return nil, fmt.Errorf("eval: unknown machine entry %q", entry)
	}
	if len(args) != len(worker.Params) {
		return nil, fmt.Errorf("eval: machine entry %q got %d arguments, want %d", entry, len(args), len(worker.Params))
	}
	vars := make(map[string]Value, len(args)+len(worker.Frame)+1)
	for i, arg := range args {
		vars[worker.Params[i].Name] = arg
	}
	s := &MachineSession{
		interp:  &interp{ctx: ctx, env: env, out: ioctx.Writer, ioctx: ioctx, evidence: map[int]*evidence{}},
		workers: workers,
		frames:  []*machineFrame{{worker: worker, block: worker.Entry, vars: vars}},
	}
	s.stats.MaxDepth = 1
	s.stats.MaxLiveSlots = len(vars)
	return s, nil
}

// Run advances until the next suspension, normal completion, or exit.
func (s *MachineSession) Run() (MachineEvent, error) {
	if s.finished {
		return MachineEvent{}, fmt.Errorf("eval: machine session already completed")
	}
	if s.waiting != nil {
		return MachineEvent{}, fmt.Errorf("eval: suspended machine must be resumed")
	}
	for len(s.frames) != 0 {
		s.stats.Steps++
		if s.stats.Steps%pollEvery == 0 {
			select {
			case <-s.interp.ctx.Done():
				return MachineEvent{}, fmt.Errorf("interrupted")
			default:
			}
		}
		frame := s.frames[len(s.frames)-1]
		block := &frame.worker.Blocks[frame.block]
		locals := &Frame{vars: frame.vars}
		eval := func(expr core.Expr) (Value, error) {
			return s.interp.eval(expr, locals)
		}
		switch term := block.Term.(type) {
		case *machineir.Eval:
			value, err := eval(term.Value)
			if err != nil {
				return MachineEvent{}, err
			}
			if exit, ok := asExit(value); ok {
				return s.finishExit(exit)
			}
			frame.vars[term.Bind.Name] = value
			frame.block = term.Next
		case *machineir.Branch:
			value, err := eval(term.Cond)
			if err != nil {
				return MachineEvent{}, err
			}
			if exit, ok := asExit(value); ok {
				return s.finishExit(exit)
			}
			if value.(bool) {
				frame.block = term.Then
			} else {
				frame.block = term.Else
			}
		case *machineir.SwitchCtor:
			value := frame.vars[term.Scrut]
			matched := false
			for _, c := range term.Cases {
				var fields []Value
				switch value := value.(type) {
				case bool:
					want := "False"
					if value {
						want = "True"
					}
					if types.SurfaceName(c.Ctor.Name) != want {
						continue
					}
				case fangort.List[Value]:
					if value.IsEmpty() {
						if c.Ctor.Index != 0 {
							continue
						}
					} else {
						if c.Ctor.Index != 1 {
							continue
						}
						fields = []Value{value.Head(), value.Tail()}
					}
				case *CtorVal:
					if value.Ctor.Index != c.Ctor.Index {
						continue
					}
					fields = value.Fields
				default:
					return MachineEvent{}, fmt.Errorf("eval: machine constructor switch on %T", value)
				}
				for i, bind := range c.Binds {
					if bind.Name != "" {
						frame.vars[bind.Name] = fields[i]
					}
				}
				frame.block = c.Next
				matched = true
				break
			}
			if !matched {
				if term.Default == nil {
					return MachineEvent{}, fmt.Errorf("eval: exhaustive machine constructor switch did not match")
				}
				frame.block = *term.Default
			}
		case *machineir.SwitchLit:
			value := frame.vars[term.Scrut]
			frame.block = term.Default
			for _, c := range term.Cases {
				literal, err := eval(c.Lit)
				if err != nil {
					return MachineEvent{}, err
				}
				if value == literal {
					frame.block = c.Next
					break
				}
			}
		case *machineir.Suspend:
			request, err := eval(term.Request)
			if err != nil {
				return MachineEvent{}, err
			}
			if exit, ok := asExit(request); ok {
				return s.finishExit(exit)
			}
			frame.block = term.Next
			s.prune(frame, block.LiveOut, term.Bind.Name)
			bind := term.Bind
			s.waiting = &bind
			return MachineEvent{Request: request}, nil
		case *machineir.Call:
			callee := s.workers[term.Callee]
			values := make([]Value, len(term.Args))
			for i, arg := range term.Args {
				value, err := eval(arg)
				if err != nil {
					return MachineEvent{}, err
				}
				if exit, ok := asExit(value); ok {
					return s.finishExit(exit)
				}
				values[i] = value
			}
			childVars := make(map[string]Value, len(values)+len(callee.Frame)+1)
			for i, value := range values {
				childVars[callee.Params[i].Name] = value
			}
			child := &machineFrame{worker: callee, block: callee.Entry, vars: childVars, returnBind: term.Bind.Name}
			if term.Tail {
				child.returnBind = frame.returnBind
				clearMachineFrame(frame)
				s.frames[len(s.frames)-1] = child
			} else {
				frame.block = term.Next
				s.prune(frame, block.LiveOut, term.Bind.Name)
				s.frames = append(s.frames, child)
				if len(s.frames) > s.stats.MaxDepth {
					s.stats.MaxDepth = len(s.frames)
				}
			}
		case *machineir.PushCleanup:
			resource, err := eval(term.Acquire)
			if err != nil {
				return MachineEvent{}, err
			}
			if exit, ok := asExit(resource); ok {
				return s.finishExit(exit)
			}
			frame.vars[term.Resource.Name] = resource
			releaseVars := make(map[string]Value, len(frame.vars))
			for name, value := range frame.vars {
				releaseVars[name] = value
			}
			releaseEvidence := cloneEvidence(s.interp.evidence)
			s.cleanups = append(s.cleanups, func() (*ExitRequest, error) {
				saved := s.interp.evidence
				s.interp.evidence = cloneEvidence(releaseEvidence)
				value, err := s.interp.eval(term.Release, &Frame{vars: releaseVars})
				s.interp.evidence = saved
				if err != nil {
					return nil, err
				}
				exit, _ := asExit(value)
				return exit, nil
			})
			if len(s.cleanups) > s.stats.MaxCleanups {
				s.stats.MaxCleanups = len(s.cleanups)
			}
			frame.block = term.Next
		case *machineir.PopCleanup:
			exit, err := s.popCleanup(nil)
			if err != nil {
				return MachineEvent{}, err
			}
			if exit != nil {
				return s.finishExit(exit)
			}
			frame.block = term.Next
		case *machineir.Return:
			value, err := eval(term.Value)
			if err != nil {
				return MachineEvent{}, err
			}
			if exit, ok := asExit(value); ok {
				return s.finishExit(exit)
			}
			bind := frame.returnBind
			clearMachineFrame(frame)
			s.frames = s.frames[:len(s.frames)-1]
			if len(s.frames) == 0 {
				s.finished = true
				return MachineEvent{Done: true, Value: value}, nil
			}
			s.frames[len(s.frames)-1].vars[bind] = value
		default:
			return MachineEvent{}, fmt.Errorf("eval: unknown machine terminator %T", block.Term)
		}
	}
	return MachineEvent{}, fmt.Errorf("eval: machine exhausted without completion")
}

func (s *MachineSession) prune(frame *machineFrame, live []string, definedOnResume string) {
	keep := make(map[string]bool, len(live))
	for _, name := range live {
		if name != definedOnResume {
			keep[name] = true
		}
	}
	for name := range frame.vars {
		if !keep[name] {
			delete(frame.vars, name)
		}
	}
	if len(frame.vars) > s.stats.MaxLiveSlots {
		s.stats.MaxLiveSlots = len(frame.vars)
	}
}

// Resume supplies the result of the last suspension and advances again.
func (s *MachineSession) Resume(value Value) (MachineEvent, error) {
	if s.finished {
		return MachineEvent{}, fmt.Errorf("eval: machine session already completed")
	}
	if s.waiting == nil || len(s.frames) == 0 {
		return MachineEvent{}, fmt.Errorf("eval: machine is not suspended")
	}
	frame := s.frames[len(s.frames)-1]
	frame.vars[s.waiting.Name] = value
	s.waiting = nil
	return s.Run()
}

func (s *MachineSession) Stats() MachineStats { return s.stats }

func (s *MachineSession) finishExit(exit *ExitRequest) (MachineEvent, error) {
	var err error
	exit, err = s.unwind(exit)
	if err != nil {
		return MachineEvent{}, err
	}
	for _, frame := range s.frames {
		clearMachineFrame(frame)
	}
	s.frames = nil
	s.waiting = nil
	s.finished = true
	return MachineEvent{Done: true, Exit: exit}, nil
}

func (s *MachineSession) popCleanup(primary *ExitRequest) (*ExitRequest, error) {
	if len(s.cleanups) == 0 {
		return nil, fmt.Errorf("eval: machine cleanup stack underflow")
	}
	i := len(s.cleanups) - 1
	cleanup := s.cleanups[i]
	s.cleanups[i] = nil
	s.cleanups = s.cleanups[:i]
	secondary, err := cleanup()
	if err != nil {
		return nil, err
	}
	return suppress(primary, secondary), nil
}

func (s *MachineSession) unwind(primary *ExitRequest) (*ExitRequest, error) {
	var err error
	for len(s.cleanups) != 0 {
		primary, err = s.popCleanup(primary)
		if err != nil {
			return nil, err
		}
	}
	return primary, nil
}

func clearMachineFrame(frame *machineFrame) {
	for name := range frame.vars {
		delete(frame.vars, name)
	}
	frame.worker = nil
	frame.vars = nil
}
