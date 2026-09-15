package eval

import (
	"context"
	"errors"
	"fmt"
	"maps"

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
	advance *cursorAdvanceRequest
	Owner   *fangort.YieldOwner
	Request Value
	Done    bool
	Value   Value
	Exit    *ExitRequest
}

type MachineStats struct {
	MaxPullDepth int
	Steps        int
	MaxDepth     int
	MaxFrameCap  int
	MaxLiveSlots int
	MaxCleanups  int
	MaxStates    int
}

type machineFrame struct {
	rows          rowEnv
	types         descriptorEnv
	worker        *machineir.Worker
	block         machineir.BlockID
	vars          map[string]Value
	evidence      map[int]*evidence
	returnBind    string
	returnState   string
	stateToken    int
	returnHandler bool
}

type machineClosure struct {
	rows     rowEnv
	types    descriptorEnv
	desc     *machineir.Closure
	values   []Value
	evidence map[int]*evidence
}

type machineOperation struct {
	rows       rowEnv
	types      descriptorEnv
	worker     *machineir.Worker
	values     []Value
	evidence   map[int]*evidence
	stateToken int
}

type machineHandler struct {
	target       *evidence
	frameDepth   int
	cleanupDepth int
	stateDepth   int
	term         *machineir.Handle
}

// MachineSession owns one suspended computation. Frame objects live separately
// from the append-grown pointer slice, and handler boundaries retain depths,
// so slice relocation cannot invalidate a live frame. The session, its frames,
// and Resume are compiler-internal Go APIs; no copyable continuation value
// exists in Fango or Core.
type MachineSession struct {
	program     *machineir.Prog
	traversal   *machineTraversal
	pendingExit *ExitRequest
	interp      *interp
	workers     map[string]*machineir.Worker
	closures    map[*core.Lambda]*machineir.Closure
	frames      []*machineFrame
	waiting     *machineir.Local
	finished    bool
	stats       MachineStats
	cleanups    []func() (*ExitRequest, error)
	states      []Value
	handlers    []machineHandler
}

// StartMachine validates and initializes an iterative machine evaluation.
// Call Run to reach the first suspension or completion.
func StartMachine(ctx context.Context, p *machineir.Prog, entry string, args []Value, env *Env, ioctx *IOContext) (*MachineSession, error) {
	return startMachine(ctx, p, entry, args, nil, env, ioctx, true)
}

func startMachine(ctx context.Context, p *machineir.Prog, entry string, args []Value, initialEvidence map[int]*evidence, env *Env, ioctx *IOContext, requireClosed bool) (*MachineSession, error) {
	if errs := machineir.Lint(p); len(errs) != 0 {
		return nil, fmt.Errorf("eval: malformed machine IR: %v", errs[0])
	}
	workers := make(map[string]*machineir.Worker, len(p.Workers))
	for i := range p.Workers {
		workers[p.Workers[i].Name] = &p.Workers[i]
	}
	closures := make(map[*core.Lambda]*machineir.Closure, len(p.Closures))
	for i := range p.Closures {
		closures[p.Closures[i].Expr] = &p.Closures[i]
	}
	worker := workers[entry]
	if worker == nil {
		return nil, fmt.Errorf("eval: unknown machine entry %q", entry)
	}
	if len(args) != len(worker.Params) {
		return nil, fmt.Errorf("eval: machine entry %q got %d arguments, want %d", entry, len(args), len(worker.Params))
	}
	if requireClosed && (len(worker.EffectParams) != 0 || len(worker.Rows) != 0) {
		return nil, fmt.Errorf("eval: machine entry %q requires lexical evidence", entry)
	}
	if !requireClosed {
		for _, ev := range worker.EffectParams {
			if initialEvidence[ev.Unique] == nil {
				return nil, fmt.Errorf("eval: machine entry %q lacks evidence `%s`", entry, ev.Name)
			}
		}
	}
	vars := make(map[string]Value, len(args)+len(worker.Frame)+1)
	for i, arg := range args {
		vars[worker.Params[i].Name] = arg
	}
	s := &MachineSession{
		program:  p,
		interp:   &interp{ctx: ctx, env: env, out: ioctx.Writer, ioctx: ioctx, evidence: map[int]*evidence{}},
		workers:  workers,
		closures: closures,
		frames:   []*machineFrame{{worker: worker, block: worker.Entry, vars: vars, evidence: cloneEvidence(initialEvidence), stateToken: -1}},
	}
	s.stats.MaxDepth = 1
	s.stats.MaxFrameCap = cap(s.frames)
	s.stats.MaxLiveSlots = len(vars)
	return s, nil
}

func (in *interp) startMachineClosure(p *machineir.Prog, closure *machineClosure, arg Value, callEvidence map[int]*evidence, row *fangort.EvidenceRow) (*MachineSession, error) {
	if closure == nil || closure.desc == nil {
		return nil, fmt.Errorf("eval: invalid Machine callback")
	}
	evidence := cloneEvidence(closure.evidence)
	for _, ev := range closure.desc.CallEvidence {
		value := callEvidence[ev.Unique]
		if value == nil {
			return nil, fmt.Errorf("eval: Machine callback lacks call evidence `%s`", ev.Name)
		}
		evidence[ev.Unique] = value
	}
	args := append(append([]Value(nil), closure.values...), arg)
	session, err := startMachine(in.ctx, p, closure.desc.Worker, args, evidence, in.env, in.ioctx, false)
	if err != nil {
		return nil, err
	}
	// Share execution policy and accounting with the caller. In particular,
	// stage-safe traversal cannot acquire a runtime interpreter.
	session.interp = in
	session.frames[0].types = closure.types
	frame := session.frames[0]
	frame.rows = maps.Clone(closure.rows)
	if frame.rows == nil {
		frame.rows = rowEnv{}
	}
	for id, value := range bindInvocationRow(frame.worker.RowParam, frame.worker.RowEffects, row, frame.evidence) {
		frame.rows[id] = value
	}
	return session, nil
}

// Run advances until the next suspension, normal completion, or exit.
func (s *MachineSession) runLocal() (event MachineEvent, err error) {
	if s.finished {
		return MachineEvent{}, fmt.Errorf("eval: machine session already completed")
	}
	if s.waiting != nil {
		return MachineEvent{}, fmt.Errorf("eval: suspended machine must be resumed")
	}
	// Production runs with its lexical evidence. Restore the caller's evidence
	// after each yield or failure, including when both share an interpreter.
	savedEvidence := s.interp.evidence
	defer func() {
		if err != nil && !s.finished {
			closeExit, closeErr := s.abandonLocal()
			event.Exit = suppress(event.Exit, closeExit)
			event.Done = true
			err = errors.Join(err, closeErr)
		}
		s.interp.evidence = savedEvidence
	}()
	if exit := s.pendingExit; exit != nil {
		s.pendingExit = nil
		if caught, err := s.catchExit(exit); err != nil {
			return MachineEvent{Exit: exit}, err
		} else if !caught {
			return s.finishExit(exit)
		}
	}
	for len(s.frames) != 0 {
		s.stats.Steps++
		if err := s.interp.tick(); err != nil {
			return MachineEvent{}, err
		}
		frame := s.frames[len(s.frames)-1]
		s.interp.evidence = frame.evidence
		block := &frame.worker.Blocks[frame.block]
		locals := &Frame{vars: frame.vars, types: frame.types, rows: frame.rows, mutable: true}
		eval := func(expr core.Expr) (Value, error) {
			if lam, ok := expr.(*core.Lambda); ok {
				return s.interp.makeClosure(lam, locals, s.closures[lam])
			}
			return s.interp.eval(expr, locals)
		}
		switch term := block.Term.(type) {
		case *machineir.Eval:
			value, err := eval(term.Value)
			if err != nil {
				return MachineEvent{}, err
			}
			if exit, ok := asExit(value); ok {
				if caught, err := s.catchExit(exit); err != nil {
					return MachineEvent{}, err
				} else if caught {
					continue
				}
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
				if caught, err := s.catchExit(exit); err != nil {
					return MachineEvent{}, err
				} else if caught {
					continue
				}
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
			var owner *fangort.YieldOwner
			if term.Owner.Unique != 0 {
				ev := resolveEvidence(frame.evidence[term.Owner.Unique])
				if ev == nil || ev.yieldOwner == nil {
					return MachineEvent{}, fmt.Errorf("eval: suspension has no lexical owner")
				}
				owner = ev.yieldOwner
			}
			request, err := eval(term.Request)
			if err != nil {
				return MachineEvent{}, err
			}
			if exit, ok := asExit(request); ok {
				if caught, err := s.catchExit(exit); err != nil {
					return MachineEvent{}, err
				} else if caught {
					continue
				}
				return s.finishExit(exit)
			}
			frame.block = term.Next
			s.prune(frame, block.LiveOut, term.Bind.Name)
			bind := term.Bind
			s.waiting = &bind
			return MachineEvent{Owner: owner, Request: request}, nil
		case *machineir.CursorAdvance:
			value, err := eval(term.Cursor)
			if err != nil {
				return MachineEvent{}, err
			}
			cursor, ok := value.(*MachineIteratorSession)
			if !ok {
				return MachineEvent{}, fmt.Errorf("eval: advancement operand is %T", value)
			}
			row, rowErr := s.interp.argumentRow(term.Row, locals)
			if rowErr != nil {
				return MachineEvent{}, rowErr
			}
			frame.block = term.Next
			s.prune(frame, block.LiveOut, term.Bind.Name)
			return MachineEvent{advance: &cursorAdvanceRequest{cursor: cursor, term: term, row: row}}, nil
		case *machineir.Call:
			callee := s.workers[term.Callee]
			var closure *machineClosure
			var synchronous *Closure
			var operation *machineOperation
			if term.Operation != nil {
				ev := resolveEvidence(frame.evidence[term.Effect.Unique])
				if ev != nil && ev.handler != nil && ev.machineOps == nil {
					// A Direct/Exit interpretation may be passed to a Machine
					// producer. Invoke it synchronously with its lexical evidence.
					value, err := eval(&core.Perform{Op: term.Operation, Effect: term.Effect, Args: term.Args, Ty: term.Bind.Ty})
					if err != nil {
						return MachineEvent{}, err
					}
					if exit, ok := asExit(value); ok {
						if caught, err := s.catchExit(exit); err != nil {
							return MachineEvent{Exit: exit}, err
						} else if caught {
							continue
						}
						return s.finishExit(exit)
					}
					s.prune(frame, block.LiveOut, term.Bind.Name)
					frame.vars[term.Bind.Name], frame.block = value, term.Next
					continue
				}
				if ev == nil || ev.machineOps == nil {
					return MachineEvent{}, fmt.Errorf("eval: machine operation %q has no evidence", term.Operation.Name)
				}
				operation = ev.machineOps[term.Operation.Index]
				if operation == nil {
					return MachineEvent{}, fmt.Errorf("eval: machine handler missing operation %q", term.Operation.Name)
				}
				callee = operation.worker
			} else if term.Callee == "" {
				value, err := eval(term.CalleeExpr)
				if err != nil {
					return MachineEvent{}, err
				}
				fn, ok := value.(*Closure)
				if !ok || fn.machine == nil && fn.control.Transport == types.Machine {
					return MachineEvent{}, fmt.Errorf("eval: indirect machine call of %T", value)
				}
				closure = fn.machine
				if closure != nil {
					callee = s.workers[closure.desc.Worker]
				} else {
					synchronous = fn
				}
			}
			values := make([]Value, len(term.Args))
			for i, arg := range term.Args {
				value, err := eval(arg)
				if err != nil {
					return MachineEvent{}, err
				}
				if exit, ok := asExit(value); ok {
					if caught, err := s.catchExit(exit); err != nil {
						return MachineEvent{}, err
					} else if caught {
						continue
					}
					return s.finishExit(exit)
				}
				values[i] = value
			}
			row, rowErr := s.interp.argumentRow(term.Row, locals)
			if rowErr != nil {
				return MachineEvent{}, rowErr
			}
			if synchronous != nil {
				if len(values) != 1 {
					return MachineEvent{}, fmt.Errorf("eval: synchronous Machine adapter requires one argument")
				}
				callEvidence := cloneEvidence(synchronous.Evidence)
				for _, ev := range term.EvidenceArgs {
					callEvidence[ev.Unique] = frame.evidence[ev.Unique]
				}
				rows := bindInvocationRow(synchronous.rowParam, synchronous.rowEffects, row, callEvidence)
				s.interp.evidence = callEvidence
				value, err := s.interp.eval(synchronous.Body, &Frame{parent: synchronous.Env, vars: map[string]Value{synchronous.Param: values[0]}, rows: rows})
				s.interp.evidence = frame.evidence
				if err != nil {
					return MachineEvent{}, err
				}
				if exit, ok := asExit(value); ok {
					if caught, err := s.catchExit(exit); err != nil {
						return MachineEvent{Exit: exit}, err
					} else if caught {
						continue
					}
					return s.finishExit(exit)
				}
				s.prune(frame, block.LiveOut, term.Bind.Name)
				frame.vars[term.Bind.Name] = value
				frame.block = term.Next
				continue
			}
			if closure != nil {
				values = append(append([]Value(nil), closure.values...), values...)
			} else if operation != nil {
				prefix := append([]Value(nil), operation.values...)
				if operation.stateToken >= 0 {
					prefix = append(prefix, s.states[operation.stateToken])
				}
				values = append(prefix, values...)
			}
			childVars := make(map[string]Value, len(values)+len(callee.Frame)+1)
			for i, value := range values {
				childVars[callee.Params[i].Name] = value
			}
			childEvidence := make(map[int]*evidence, len(term.EvidenceArgs))
			if closure != nil {
				for unique, ev := range closure.evidence {
					childEvidence[unique] = ev
				}
			}
			if operation != nil {
				for unique, ev := range operation.evidence {
					childEvidence[unique] = ev
				}
			}
			for _, arg := range term.EvidenceArgs {
				ev := frame.evidence[arg.Unique]
				if ev == nil {
					return MachineEvent{}, fmt.Errorf("eval: missing machine evidence %q for %q", arg.Name, term.Callee)
				}
				childEvidence[arg.Unique] = ev
			}
			child := &machineFrame{worker: callee, block: callee.Entry, vars: childVars, evidence: childEvidence,
				returnBind: term.Bind.Name, stateToken: -1}
			if closure != nil {
				child.rows = maps.Clone(closure.rows)
				child.types = closure.types
			} else if operation != nil {
				child.rows = maps.Clone(operation.rows)
				child.types = operation.types
			} else {
				var err error
				child.types, err = s.interp.instantiateDescriptors(callee.TyParams, term.TyArgs, locals)
				if err != nil {
					return MachineEvent{}, err
				}
			}
			if child.rows == nil {
				child.rows = rowEnv{}
			}
			for id, value := range bindInvocationRow(callee.RowParam, callee.RowEffects, row, childEvidence) {
				child.rows[id] = value
			}
			if operation != nil {
				child.stateToken = operation.stateToken
			}
			if term.Tail {
				child.returnBind = frame.returnBind
				child.returnState = frame.returnState
				child.returnHandler = frame.returnHandler
				clearMachineFrame(frame)
				s.frames[len(s.frames)-1] = child
			} else {
				frame.block = term.Next
				s.prune(frame, block.LiveOut, term.Bind.Name)
				s.frames = append(s.frames, child)
				if cap(s.frames) > s.stats.MaxFrameCap {
					s.stats.MaxFrameCap = cap(s.frames)
				}
				if len(s.frames) > s.stats.MaxDepth {
					s.stats.MaxDepth = len(s.frames)
				}
			}
		case *machineir.Handle:
			h := term.Node
			stateToken := -1
			if term.State != nil {
				initial, err := eval(term.State.Initial)
				if err != nil {
					return MachineEvent{}, err
				}
				stateToken = len(s.states)
				s.states = append(s.states, initial)
				if len(s.states) > s.stats.MaxStates {
					s.stats.MaxStates = len(s.states)
				}
			}
			installed := &evidence{machineOps: map[int]*machineOperation{}}
			for _, clause := range term.Clauses {
				worker := s.workers[clause.Worker]
				values := make([]Value, len(clause.Captures))
				for i, capture := range clause.Captures {
					values[i] = frame.vars[capture.Name]
				}
				outer := make(map[int]*evidence, len(worker.EffectParams))
				for _, param := range worker.EffectParams {
					outer[param.Unique] = frame.evidence[param.Unique]
				}
				installed.machineOps[clause.Op.Index] = &machineOperation{worker: worker, values: values, evidence: outer, types: frame.types, rows: frame.rows,
					stateToken: stateToken}
			}
			bodyWorker := s.workers[term.BodyWorker]
			childVars := make(map[string]Value, len(term.BodyCaptures)+len(bodyWorker.Frame)+1)
			for i, capture := range term.BodyCaptures {
				childVars[bodyWorker.Params[i].Name] = frame.vars[capture.Name]
			}
			childEvidence := make(map[int]*evidence, len(bodyWorker.EffectParams))
			for _, param := range bodyWorker.EffectParams {
				if param.Unique == h.Effect.Unique {
					childEvidence[param.Unique] = installed
				} else {
					childEvidence[param.Unique] = frame.evidence[param.Unique]
				}
			}
			child := &machineFrame{worker: bodyWorker, block: bodyWorker.Entry, vars: childVars, types: frame.types, rows: frame.rows,
				evidence: childEvidence, returnBind: term.Bind.Name, returnState: term.StateResult.Name, stateToken: -1}
			if term.Abort {
				s.handlers = append(s.handlers, machineHandler{target: installed, frameDepth: len(s.frames),
					cleanupDepth: len(s.cleanups), stateDepth: len(s.states), term: term})
				child.returnHandler = true
			}
			frame.block = term.Next
			s.prune(frame, block.LiveOut, term.Bind.Name)
			s.frames = append(s.frames, child)
			if cap(s.frames) > s.stats.MaxFrameCap {
				s.stats.MaxFrameCap = cap(s.frames)
			}
			if len(s.frames) > s.stats.MaxDepth {
				s.stats.MaxDepth = len(s.frames)
			}
		case *machineir.PushCleanup:
			resource, err := eval(term.Acquire)
			if err != nil {
				return MachineEvent{}, err
			}
			if exit, ok := asExit(resource); ok {
				if caught, err := s.catchExit(exit); err != nil {
					return MachineEvent{}, err
				} else if caught {
					continue
				}
				return s.finishExit(exit)
			}
			frame.vars[term.Resource.Name] = resource
			releaseVars := make(map[string]Value, len(frame.vars))
			for name, value := range frame.vars {
				releaseVars[name] = value
			}
			releaseEvidence := cloneEvidence(s.interp.evidence)
			releaseTypes := frame.types
			releaseRows := frame.rows
			s.cleanups = append(s.cleanups, func() (*ExitRequest, error) {
				saved := s.interp.evidence
				s.interp.evidence = cloneEvidence(releaseEvidence)
				value, err := s.interp.eval(term.Release, &Frame{vars: releaseVars, types: releaseTypes, rows: releaseRows})
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
		case *machineir.CursorOpen:
			value, err := eval(term.Producer)
			if err != nil {
				return MachineEvent{}, err
			}
			producer, ok := value.(*Closure)
			if !ok || producer.machine == nil {
				return MachineEvent{}, fmt.Errorf("eval: cursor producer is not a Machine callback")
			}
			callEvidence := cloneEvidence(s.interp.evidence)
			var owner *fangort.YieldOwner
			if term.Yield.Unique != 0 {
				owner = fangort.NewYieldOwner()
				callEvidence[term.Yield.Unique] = &evidence{yieldOwner: owner}
			}
			row, rowErr := s.interp.argumentRow(term.Row, locals)
			if rowErr != nil {
				return MachineEvent{}, rowErr
			}
			cursorEvidence := fangort.NewCursorEvidence(row)
			producerSession, err := s.interp.startMachineClosure(s.program, producer.machine, struct{}{}, callEvidence, cursorEvidence.Row())
			if err != nil {
				return MachineEvent{}, err
			}
			cursor := &MachineIteratorSession{owner: owner, session: producerSession, evidence: cursorEvidence}
			frame.vars[term.Cursor.Name] = cursor
			s.cleanups = append(s.cleanups, cursor.Close)
			if len(s.cleanups) > s.stats.MaxCleanups {
				s.stats.MaxCleanups = len(s.cleanups)
			}
			frame.block = term.Next
		case *machineir.CursorClose, *machineir.PopCleanup:
			exit, err := s.popCleanup(nil)
			if err != nil {
				return MachineEvent{}, err
			}
			if exit != nil {
				if caught, err := s.catchExit(exit); err != nil {
					return MachineEvent{}, err
				} else if caught {
					continue
				}
				return s.finishExit(exit)
			}
			switch close := term.(type) {
			case *machineir.CursorClose:
				frame.block = close.Next
			case *machineir.PopCleanup:
				frame.block = close.Next
			}
		case *machineir.StateResume:
			value, err := eval(term.Value)
			if err != nil {
				return MachineEvent{}, err
			}
			next, err := eval(term.NextState)
			if err != nil {
				return MachineEvent{}, err
			}
			if frame.stateToken < 0 || frame.stateToken >= len(s.states) {
				return MachineEvent{}, fmt.Errorf("eval: invalid machine handler state token")
			}
			s.states[frame.stateToken] = next
			frame.vars[term.Bind.Name] = value
			frame.block = term.Next
		case *machineir.Return:
			value, err := eval(term.Value)
			if err != nil {
				return MachineEvent{}, err
			}
			if exit, ok := asExit(value); ok {
				if caught, err := s.catchExit(exit); err != nil {
					return MachineEvent{}, err
				} else if caught {
					continue
				}
				return s.finishExit(exit)
			}
			bind, stateName, returnHandler := frame.returnBind, frame.returnState, frame.returnHandler
			clearMachineFrame(frame)
			s.frames[len(s.frames)-1] = nil
			s.frames = s.frames[:len(s.frames)-1]
			if returnHandler {
				if len(s.handlers) == 0 {
					return MachineEvent{}, fmt.Errorf("eval: machine handler stack underflow")
				}
				s.handlers = s.handlers[:len(s.handlers)-1]
			}
			if len(s.frames) == 0 {
				exit, err := s.unwind(nil)
				s.clear()
				if exit != nil || err != nil {
					return MachineEvent{Done: true, Exit: exit}, err
				}
				return MachineEvent{Done: true, Value: value}, nil
			}
			s.frames[len(s.frames)-1].vars[bind] = value
			if stateName != "" {
				i := len(s.states) - 1
				s.frames[len(s.frames)-1].vars[stateName] = s.states[i]
				s.states[i] = nil
				s.states = s.states[:i]
			}
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
func (s *MachineSession) resumeLocal(value Value) error {
	if s.finished {
		return fmt.Errorf("eval: machine session already completed")
	}
	if s.waiting == nil || len(s.frames) == 0 {
		return fmt.Errorf("eval: machine is not suspended")
	}
	frame := s.frames[len(s.frames)-1]
	frame.vars[s.waiting.Name] = value
	s.waiting = nil
	return nil
}

// Abandon consumes an unfinished private interpreter machine and discharges
// its cleanup stack. It is the interpreter counterpart of fangort.Abandon.
func (s *MachineSession) abandonLocal() (*ExitRequest, error) {
	if s.finished {
		return nil, fmt.Errorf("eval: machine session already completed")
	}
	exit, err := s.unwind(nil)
	s.clear()
	return exit, err
}

// clear drops live execution state on every terminal path. Program
// descriptors and high-water statistics remain available for diagnostics.
func (s *MachineSession) clear() {
	for i, frame := range s.frames {
		clearMachineFrame(frame)
		s.frames[i] = nil
	}
	s.frames = nil
	for i := range s.states {
		s.states[i] = nil
	}
	s.states = nil
	s.cleanups = nil
	s.handlers = nil
	s.waiting = nil
	s.pendingExit = nil
	s.finished = true
}

func (s *MachineSession) Stats() MachineStats { return s.stats }

func (s *MachineSession) finishExit(exit *ExitRequest) (MachineEvent, error) {
	exit, err := s.unwind(exit)
	s.clear()
	return MachineEvent{Done: true, Exit: exit}, err
}

// catchExit transfers an abort to its dynamically nearest Machine handler.
// It mirrors fangort.Machine's routing discipline: cleanup is unwound only to
// the matched boundary, nested frames/state are discarded, and the matching
// handler itself is consumed before its clause runs.
func (s *MachineSession) catchExit(exit *ExitRequest) (bool, error) {
	for i := len(s.handlers) - 1; i >= 0; i-- {
		h := s.handlers[i]
		if exit.Target != h.target {
			continue
		}
		var err error
		exit, err = s.unwindTo(exit, h.cleanupDepth)
		if err != nil {
			return false, err
		}
		for len(s.frames) > h.frameDepth {
			last := len(s.frames) - 1
			clearMachineFrame(s.frames[last])
			s.frames[last] = nil
			s.frames = s.frames[:last]
		}
		for len(s.states) > h.stateDepth {
			last := len(s.states) - 1
			s.states[last] = nil
			s.states = s.states[:last]
		}
		s.handlers = s.handlers[:i]
		var clause machineir.HandlerClause
		found := false
		for _, c := range h.term.Clauses {
			if c.Op == exit.Op {
				clause = c
				found = true
				break
			}
		}
		if !found {
			return false, fmt.Errorf("eval: abort target missing clause %q", exit.Op.Name)
		}
		worker := s.workers[clause.Worker]
		if worker == nil {
			return false, fmt.Errorf("eval: unknown abort clause worker %q", clause.Worker)
		}
		owner := s.frames[len(s.frames)-1]
		vars := make(map[string]Value, len(worker.Params))
		param := 0
		for _, capture := range clause.Captures {
			vars[worker.Params[param].Name] = owner.vars[capture.Name]
			param++
		}
		stateToken := -1
		if clause.StateName != "" {
			stateToken = len(s.states) - 1
			vars[worker.Params[param].Name] = s.states[stateToken]
			param++
		}
		for j, value := range exit.Payload {
			vars[worker.Params[param+j].Name] = value
		}
		for _, source := range h.term.Node.Clauses {
			if source.Op == clause.Op && source.SuppressedParam != "" {
				vars[source.SuppressedParam] = failureList(snapshotFailure(exit).Suppressed())
			}
		}
		evidence := make(map[int]*evidence, len(worker.EffectParams))
		for _, ev := range worker.EffectParams {
			evidence[ev.Unique] = owner.evidence[ev.Unique]
		}
		child := &machineFrame{worker: worker, block: worker.Entry, vars: vars, evidence: evidence, types: owner.types, rows: owner.rows,
			returnBind: h.term.AbortBind.Name, returnState: h.term.StateResult.Name, stateToken: stateToken}
		owner.block = h.term.AbortNext
		s.frames = append(s.frames, child)
		if cap(s.frames) > s.stats.MaxFrameCap {
			s.stats.MaxFrameCap = cap(s.frames)
		}
		if len(s.frames) > s.stats.MaxDepth {
			s.stats.MaxDepth = len(s.frames)
		}
		return true, nil
	}
	return false, nil
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
	return suppress(primary, secondary), err
}

func (s *MachineSession) unwind(primary *ExitRequest) (*ExitRequest, error) {
	return s.unwindTo(primary, 0)
}

func (s *MachineSession) unwindTo(primary *ExitRequest, depth int) (*ExitRequest, error) {
	var errs []error
	for len(s.cleanups) > depth {
		var err error
		primary, err = s.popCleanup(primary)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return primary, errors.Join(errs...)
}

func clearMachineFrame(frame *machineFrame) {
	for name := range frame.vars {
		delete(frame.vars, name)
	}
	frame.worker = nil
	frame.vars = nil
	frame.evidence = nil
	frame.rows = nil
	frame.types = nil
}
