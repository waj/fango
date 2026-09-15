// Package eval is the tree-walking Core interpreter: the REPL's execution
// engine and the differential test oracle for the compiled backend. Both
// backends implement the same Core semantics; the e2e suite diffs them.
package eval

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/nativehost"
	"github.com/waj/fango/internal/natives"
	"github.com/waj/fango/internal/types"
	"github.com/waj/fango/runtime/fangort"
)

// Value is the interpreter's uniform representation (doc/design.md, "Interpreter and REPL"):
//
//	Int          int64
//	Float        float64
//	String       string
//	Bool         bool
//	constructors *CtorVal
//	functions    *Closure, *Partial
//
// The switch in Eval and the compiled backend's unboxed representations
// must agree; the differential suite is the referee.
type Value = any

// ExitRequest is a language-level control value, deliberately separate from
// Go errors used for interpreter failures. Payload values have already been
// checked against Op by Core lint.
type ExitRequest struct {
	Target       *evidence
	Op           *types.EffectOp
	Payload      []Value
	PayloadTypes []*fangort.TypeDescriptor
	// Suppressed mirrors fangort.ExitRequest.Suppressed: exits a cleanup
	// scope could not make primary, in inner-to-outer order.
	Suppressed []*ExitRequest
}

// suppress returns primary carrying secondary, copying rather than editing
// an exit the scope is only forwarding.
func suppress(primary, secondary *ExitRequest) *ExitRequest {
	if secondary == nil {
		return primary
	}
	if primary == nil {
		return secondary
	}
	joined := *primary
	joined.Suppressed = make([]*ExitRequest, 0, len(primary.Suppressed)+1)
	joined.Suppressed = append(joined.Suppressed, primary.Suppressed...)
	joined.Suppressed = append(joined.Suppressed, secondary)
	return &joined
}

type Outcome struct {
	Value Value
	Exit  *ExitRequest
}

func asExit(v Value) (*ExitRequest, bool) {
	exit, ok := v.(*ExitRequest)
	return exit, ok
}

// Cell is a lazily-memoized top-level binding (doc/design.md, "Interpreter and REPL") — the final
// session model, not a shortcut: the REPL's generational redefinition
// replaces cells wholesale.
type Cell struct {
	Body    core.Expr
	memo    Value
	forced  bool
	forcing bool
}

// CtorVal is a constructed ADT value: the constructor's table row plus its
// field values — the interpreter's analogue of the compiled backend's
// per-constructor struct.
type CtorVal struct {
	Ctor   *types.CtorInfo
	Fields []Value
}

// Closure is the interpreter's only function value: one currying step,
// mirroring core.Lambda. Workers are applied directly (App{Worker}) and
// never materialize as values — elaboration eta-expanded every first-class
// use, so *Partial from the doc/design.md, "Interpreter and REPL" sketch is not needed.
type Closure struct {
	Param      string
	Body       core.Expr
	Env        *Frame
	Evidence   map[int]*evidence
	control    types.Control
	rowParam   types.CaptureVar
	rowEffects []core.EffectInstance
	// A transport-polymorphic value retains both its ordinary interpretation
	// and its checked Machine factory. Choosing a call protocol does not change
	// the representation of a stored callback.
	machine *machineClosure
}

type IOContext struct {
	Reader *bufio.Reader
	Writer io.Writer
	Args   []string
	Dir    string
	// Natives selects the sidecar worker for this session. Nil uses the
	// bundled standard-library worker lazily.
	Natives *nativehost.Executor
}

func NewIOContext(r io.Reader, w io.Writer) *IOContext {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(r)
	}
	return &IOContext{Reader: br, Writer: w, Dir: "."}
}

func (c *IOContext) HasInput() (bool, error) {
	_, err := c.Reader.Peek(1)
	if err == io.EOF {
		return false, nil
	}
	return err == nil, err
}

func (c *IOContext) ReadInputLine() ([]byte, error) { return c.Reader.ReadBytes('\n') }

func (c *IOContext) WriteOutput(data []byte) error {
	_, err := c.Writer.Write(data)
	return err
}

func (c *IOContext) Arguments() []string { return c.Args }

func (c *IOContext) WorkingDirectory() string { return c.Dir }

func (c *IOContext) nativeExecutor() (*nativehost.Executor, error) {
	if c.Natives != nil {
		return c.Natives, nil
	}
	return nativehost.Bundled()
}

type evidence struct {
	row        *fangort.EvidenceRow
	rowEffect  int
	yieldOwner *fangort.YieldOwner
	handler    *core.Handle
	frame      *Frame
	outer      map[int]*evidence
	state      Value
	machineOps map[int]*machineOperation
}

// Env holds top-level cells and workers.
type Env struct {
	adts    map[int]*types.ADTInfo
	cells   map[string]*Cell
	workers map[string]*core.Def
	machine *machineir.Prog
	// machineClosures preserves the semantic Lambda identity used by the
	// selective lowerer. Direct evaluation can therefore materialize a frame
	// factory when such a callback crosses a structured owner boundary.
	machineClosures map[*core.Lambda]*machineir.Closure
	entry           string
	// tails caches core.DetectTailLoop per *core.Def (nil = ineligible).
	// Pointer identity means REPL redefinition invalidates naturally: a new
	// generation is a new *core.Def.
	tails map[*core.Def]*core.TailLoop
	// natives is the program's sidecar declaration metadata, keyed by
	// canonical name. A sidecar call reads its boundary shapes from here
	// (doc/design.md, "Go backend and runtime").
	natives map[string]*types.NativeInfo

	// Templates is the compiler's quote table, installed only for the
	// compile-time environment. The Meta natives that assemble generated
	// code need their arguments as trees, and expanding one needs the table.
	Templates *meta.Table
}

// Expand renders a compile-time code value. It answers nil outside the
// compiler's own evaluator, where no quote table exists.
func (e *Env) Expand(c *meta.Code) ast.Expr {
	if e == nil || e.Templates == nil {
		return nil
	}
	return e.Templates.Expand(c)
}

// Frame holds block-local bindings (doc/design.md, "Language semantics") — eager values, unlike the lazy
// top-level cells. Function parameters extend the same chain.
type Frame struct {
	rows    rowEnv
	types   descriptorEnv
	parent  *Frame
	vars    map[string]Value
	mutable bool // Machine locals are pruned and overwritten between transitions.
}

// closureFrame snapshots only referenced locals when a closure crosses a
// mutable Machine frame. Ordinary Core environments remain immutable chains.
// Core forbids shadowing, so a body-local reference cannot select an outer
// binding of the same name here.
func closureFrame(lam *core.Lambda, fr *Frame) *Frame {
	mutable := false
	for f := fr; f != nil; f = f.parent {
		mutable = mutable || f.mutable
	}
	if !mutable {
		return fr
	}
	vars := map[string]Value{}
	core.Inspect(lam.Body, func(e core.Expr) {
		if ref, ok := e.(*core.VarRef); ok && ref.Local && ref.Name != lam.Param {
			if value, found := fr.lookup(ref.Name); found {
				vars[ref.Name] = value
			}
		}
	})
	return &Frame{vars: vars, types: fr.descriptors(), rows: fr.closureRows(lam)}
}

func (in *interp) makeClosure(lam *core.Lambda, fr *Frame, desc *machineir.Closure) (*Closure, error) {
	closure := in.plainClosure(lam, closureFrame(lam, fr))
	if desc == nil {
		return closure, nil
	}
	values := make([]Value, len(desc.Captures))
	for i, capture := range desc.Captures {
		value, ok := fr.lookup(capture.Name)
		if !ok {
			return nil, fmt.Errorf("eval: Machine callback capture `%s` is unavailable", capture.Name)
		}
		values[i] = value
	}
	captured := make(map[int]*evidence, len(desc.CapturedEvidence))
	for _, ev := range desc.CapturedEvidence {
		if in.evidence[ev.Unique] == nil {
			return nil, fmt.Errorf("eval: Machine callback evidence `%s` is unavailable", ev.Name)
		}
		captured[ev.Unique] = in.evidence[ev.Unique]
	}
	closure.machine = &machineClosure{desc: desc, values: values, evidence: captured, types: fr.descriptors(), rows: fr.closureRows(lam)}
	return closure, nil
}

func (f *Frame) lookup(name string) (Value, bool) {
	for ; f != nil; f = f.parent {
		if v, ok := f.vars[name]; ok {
			return v, true
		}
	}
	return nil, false
}

func NewEnv() *Env {
	return &Env{adts: map[int]*types.ADTInfo{}, cells: map[string]*Cell{}, workers: map[string]*core.Def{}, machineClosures: map[*core.Lambda]*machineir.Closure{}, tails: map[*core.Def]*core.TailLoop{}, natives: map[string]*types.NativeInfo{}}
}

// tailLoop reports (and caches) whether def executes as a frame-reuse loop.
// The interpreter shares the compiled backend's predicate — including the
// capture exclusion its own fresh frames would not need — so both backends
// optimize the same set of definitions.
func (e *Env) tailLoop(def *core.Def) *core.TailLoop {
	if tl, ok := e.tails[def]; ok {
		return tl
	}
	tl, _ := core.DetectTailLoop(def)
	e.tails[def] = tl
	return tl
}

// Define installs (or replaces — REPL redefinition) a top-level value
// binding, evicting any worker of the same name.
func (e *Env) Define(name string, body core.Expr) {
	e.cells[name] = &Cell{Body: body}
	delete(e.workers, name)
}

// DefineWorker installs (or replaces) a top-level function definition.
func (e *Env) DefineWorker(d *core.Def) {
	e.workers[d.Name] = d
	delete(e.cells, d.Name)
}

// DefineProg installs every definition of a Core program. Nullary generic
// workers (polymorphic values, doc/design.md, "Go backend and runtime") register as workers: their zero-arg
// calls re-evaluate the body per use, matching the compiled cost rule.
func (e *Env) DefineProg(p *core.Prog) {
	for _, adt := range p.ADTs {
		e.adts[adt.Con.Unique] = adt
	}
	e.entry = p.Entry
	if e.entry == "" {
		e.entry = "main"
	}
	for name, n := range p.Natives {
		e.natives[name] = n
	}
	for i := range p.Defs {
		d := &p.Defs[i]
		if d.IsWorker() {
			e.DefineWorker(d)
		} else {
			e.Define(d.Name, d.Body)
		}
	}
}

// DefineMachineProg installs the selective lowering that corresponds to the
// semantic Core definitions already in this environment. It does not replace
// Direct/Exit workers; it supplies frame factories for Machine callbacks held
// by structured owner nodes.
func (e *Env) DefineMachineProg(p *machineir.Prog) error {
	if errs := machineir.Lint(p); len(errs) != 0 {
		return fmt.Errorf("eval: malformed machine IR: %v", errs[0])
	}
	e.machine = p
	e.machineClosures = make(map[*core.Lambda]*machineir.Closure, len(p.Closures))
	for i := range p.Closures {
		e.machineClosures[p.Closures[i].Expr] = &p.Closures[i]
	}
	return nil
}

// interp carries the cancellation context and the print destination; ctx is
// polled every pollEvery evaluation steps so Ctrl-C interrupts runaway REPL
// expressions.
type interp struct {
	ctx      context.Context
	env      *Env
	out      io.Writer
	ioctx    *IOContext
	evidence map[int]*evidence
	steps    int

	// compileTime restricts the interpreter to what a compiler may run: a
	// step budget, and no native that observes external state or lives in a Go
	// sidecar the interpreter cannot load (doc/design.md,
	// "Compile-time metaprogramming").
	compileTime bool
	budget      int
}

const pollEvery = 4096

// ErrStepBudget and UnsafeNativeError are the two ways compile-time
// evaluation stops short. Callers turn them into diagnostics at the splice
// site.
var ErrStepBudget = errors.New("compile-time step budget exhausted")

// UnsafeNativeError names a native the compiler declined to run while
// expanding a splice, and why.
type UnsafeNativeError struct{ Name, Reason string }

func (e *UnsafeNativeError) Error() string {
	return fmt.Sprintf("native `%s` %s", e.Name, e.Reason)
}

// DefaultBudget bounds compile-time evaluation. It is deliberately generous:
// the point is to turn a non-terminating deriver into a diagnostic rather
// than a hung build, not to ration ordinary generation.
const DefaultBudget = 10_000_000

// EvalCompileTime runs e with the compile-time restrictions. It is how the
// compiler executes a splice operand; ordinary programs never take this path.
func EvalCompileTime(ctx context.Context, e core.Expr, env *Env, budget int) (Value, error) {
	ioctx := NewIOContext(strings.NewReader(""), io.Discard)
	in := &interp{ctx: ctx, env: env, out: ioctx.Writer, ioctx: ioctx,
		evidence: map[int]*evidence{}, compileTime: true, budget: budget}
	return in.eval(e, nil)
}

// Eval evaluates a Core expression under env. Print output goes to out —
// the REPL passes its own writer, the differential harness a buffer.
func Eval(ctx context.Context, e core.Expr, env *Env, out io.Writer) (Value, error) {
	return EvalIO(ctx, e, env, NewIOContext(strings.NewReader(""), out))
}

func EvalIO(ctx context.Context, e core.Expr, env *Env, ioctx *IOContext) (Value, error) {
	return (&interp{ctx: ctx, env: env, out: ioctx.Writer, ioctx: ioctx, evidence: map[int]*evidence{}}).eval(e, nil)
}

// EvalOutcome exposes the interpreter's control protocol to compiler tests and
// later control handlers without conflating a language exit with an internal
// evaluator error.
func EvalOutcome(ctx context.Context, e core.Expr, env *Env, ioctx *IOContext) (Outcome, error) {
	v, err := EvalIO(ctx, e, env, ioctx)
	if err != nil {
		return Outcome{}, err
	}
	if exit, ok := asExit(v); ok {
		return Outcome{Exit: exit}, nil
	}
	return Outcome{Value: v}, nil
}

// Force evaluates (and memoizes) the named top-level binding.
func Force(ctx context.Context, name string, env *Env, out io.Writer) (Value, error) {
	return ForceIO(ctx, name, env, NewIOContext(strings.NewReader(""), out))
}

func ForceIO(ctx context.Context, name string, env *Env, ioctx *IOContext) (Value, error) {
	return (&interp{ctx: ctx, env: env, out: ioctx.Writer, ioctx: ioctx, evidence: map[int]*evidence{}}).force(name)
}

// tick is shared by Core evaluation, tail loops, and producer machines. A
// traversal must not reset its caller's stage budget or cancellation counter.
func (in *interp) tick() error {
	in.steps++
	if in.steps%pollEvery == 0 {
		select {
		case <-in.ctx.Done():
			return fmt.Errorf("interrupted")
		default:
		}
		if in.budget > 0 && in.steps > in.budget {
			return ErrStepBudget
		}
	}
	return nil
}

func (in *interp) eval(e core.Expr, fr *Frame) (Value, error) {
	if err := in.tick(); err != nil {
		return nil, err
	}
	switch e := e.(type) {
	case *core.IntLit:
		return e.Val, nil
	case *core.FloatLit:
		return e.Val, nil
	case *core.StringLit:
		return e.Val, nil
	case *core.CharLit:
		return e.Val, nil
	case *core.UnitLit:
		return struct{}{}, nil
	case *core.BoolLit:
		return e.Val, nil
	case *core.VarRef:
		if v, ok := fr.lookup(e.Name); ok {
			return v, nil
		}
		return in.force(e.Name)
	case *core.Let:
		if e.Rec {
			// A self-recursive local function: build the closure over its
			// own frame, then install it — the frame ties the cycle,
			// mirroring codegen's declare-then-assign idiom.
			lam, ok := e.Rhs.(*core.Lambda)
			if !ok {
				return nil, fmt.Errorf("eval: recursive Let `%s` without a Lambda RHS", e.Name)
			}
			frame := &Frame{parent: closureFrame(lam, fr), vars: map[string]Value{}}
			frame.vars[e.Name] = in.plainClosure(lam, frame)
			return in.eval(e.Body, frame)
		}
		// Eager, in order — identical to the compiled backend's locals.
		v, err := in.eval(e.Rhs, fr)
		if err != nil {
			return nil, err
		}
		if _, ok := asExit(v); ok {
			return v, nil
		}
		return in.eval(e.Body, &Frame{parent: fr, vars: map[string]Value{e.Name: v}})
	case *core.Lambda:
		return in.makeClosure(e, fr, in.env.machineClosures[e])
	case *core.Neg:
		v, err := in.eval(e.Operand, fr)
		if err != nil {
			return nil, err
		}
		if _, ok := asExit(v); ok {
			return v, nil
		}
		switch v := v.(type) {
		case int64:
			return -v, nil
		case float64:
			return -v, nil
		default:
			return nil, fmt.Errorf("eval: negating a %T", v)
		}
	case *core.Quote:
		// Strict, so holes evaluate eagerly and in source order — the same
		// rule every other argument list follows.
		holes := make([]*meta.Code, len(e.Holes))
		for i, h := range e.Holes {
			v, err := in.eval(h, fr)
			if err != nil {
				return nil, err
			}
			if _, ok := asExit(v); ok {
				return v, nil
			}
			code, ok := v.(*meta.Code)
			if !ok {
				return nil, fmt.Errorf("eval: quote hole evaluated to a %T, want code", v)
			}
			holes[i] = code
		}
		return &meta.Code{Template: e.Template, Holes: holes}, nil
	case *core.TypeOf:
		return e.Repr, nil
	case *core.FailureInspect:
		return in.inspectFailure(e, fr)
	case *core.NativeCall:
		args := make([]Value, len(e.Args))
		for i, a := range e.Args {
			v, err := in.eval(a, fr)
			if err != nil {
				return nil, err
			}
			if _, ok := asExit(v); ok {
				return v, nil
			}
			args[i] = v
		}
		if !in.compileTime {
			executor, err := in.ioctx.nativeExecutor()
			if err != nil {
				return nil, err
			}
			// The worker registers sidecar functions under their link module,
			// which a headerless entry's bare canonical symbol does not carry.
			key := e.Name
			if e.Module != "" {
				key = e.Module + "." + types.SurfaceName(e.Name)
			}
			if executor.Has(key) {
				return in.sidecarCall(executor, key, in.env.natives[e.Name], args)
			}
		}
		if spec, ok := natives.Lookup(e.Name); ok && !spec.Effect {
			if in.compileTime && !spec.CompileTimeSafe {
				return nil, &UnsafeNativeError{Name: e.Name, Reason: "observes external or nondeterministic state"}
			}
			return spec.Eval(in.nativeRuntime(), args)
		}
		if in.compileTime {
			return nil, &UnsafeNativeError{Name: e.Name, Reason: "is implemented by a Go sidecar the interpreter cannot load"}
		}
		module := e.Module
		if module == "" {
			module = e.Name
		}
		if i := strings.LastIndexByte(module, '.'); i >= 0 {
			module = module[:i]
		}
		return nil, fmt.Errorf("native sidecar for module `%s` is not installed in this interpreter session", module)
	case *core.If:
		cond, err := in.eval(e.Cond, fr)
		if err != nil {
			return nil, err
		}
		if _, ok := asExit(cond); ok {
			return cond, nil
		}
		if cond.(bool) {
			return in.eval(e.Then, fr)
		}
		return in.eval(e.Else, fr)
	case *core.Perform:
		args := make([]Value, len(e.Args))
		for i, a := range e.Args {
			v, err := in.eval(a, fr)
			if err != nil {
				return nil, err
			}
			if _, ok := asExit(v); ok {
				return v, nil
			}
			args[i] = v
		}
		if ev := resolveEvidence(in.evidence[e.Effect.Unique]); ev != nil && ev.handler != nil {
			if e.Op.Abort {
				return nil, fmt.Errorf("eval: abort-only operation `%s` reached Perform", e.Op.Name)
			}
			var clause *core.HandlerClause
			for i := range ev.handler.Clauses {
				if ev.handler.Clauses[i].Op.Name == e.Op.Name {
					clause = &ev.handler.Clauses[i]
					break
				}
			}
			if clause == nil {
				return nil, fmt.Errorf("eval: handler missing operation `%s`", e.Op.Name)
			}
			vars := map[string]Value{}
			if ev.handler.State != nil {
				vars[ev.handler.State.Name] = ev.state
			}
			for i, p := range clause.Params {
				if p != "_" && p != "()" {
					vars[p] = args[i]
				}
			}
			saved := in.evidence
			in.evidence = cloneEvidence(ev.outer)
			v, err := in.evalResumeTail(clause.Body, &Frame{parent: ev.frame, vars: vars}, clause.ResumeID, ev)
			in.evidence = saved
			return v, err
		}
		key := types.SurfaceName(e.Op.Owner.Name) + "." + types.SurfaceName(e.Op.Name)
		if e.Op.Native != nil {
			key = e.Op.Native.Name
		}
		if !in.compileTime && e.Op.Native != nil && e.Op.Native.Template == nil {
			executor, err := in.ioctx.nativeExecutor()
			if err != nil {
				return nil, err
			}
			if executor.Has(key) {
				return in.sidecarCall(executor, key, e.Op.Native, args)
			}
		}
		if spec, ok := natives.Lookup(key); ok && spec.Effect {
			if in.compileTime {
				return nil, &UnsafeNativeError{Name: key, Reason: "performs an effect"}
			}
			return spec.Eval(in.nativeRuntime(), args)
		}
		return nil, fmt.Errorf("eval: unhandled effect operation `%s.%s`", e.Effect.Name, e.Op.Name)
	case *core.ControlExit:
		payload := make([]Value, len(e.Payload))
		descriptors := make([]*fangort.TypeDescriptor, len(e.Payload))
		for i, p := range e.Payload {
			v, err := in.eval(p, fr)
			if err != nil {
				return nil, err
			}
			if _, ok := asExit(v); ok {
				return v, nil
			}
			payload[i] = v
			descriptor, err := in.typeDescriptor(p.Type(), fr)
			if err != nil {
				return nil, err
			}
			descriptors[i] = descriptor
		}
		target := resolveEvidence(in.evidence[e.Effect.Unique])
		if target == nil {
			return nil, fmt.Errorf("eval: missing abort evidence for `%s.%s`", e.Effect.Name, e.Op.Name)
		}
		return &ExitRequest{Target: target, Op: e.Op, Payload: payload, PayloadTypes: descriptors}, nil
	case *core.Suspend:
		return nil, fmt.Errorf("eval: compiler-only suspension reached recursive evaluator")
	case *core.IteratorScope:
		return in.evalIteratorScope(e, fr)

	case *core.ResumeTail:
		return nil, fmt.Errorf("eval: ResumeTail outside verified handler-clause evaluation")
	case *core.Seq:
		first, err := in.eval(e.First, fr)
		if err != nil {
			return nil, err
		}
		if _, ok := asExit(first); ok {
			return first, nil
		}
		return in.eval(e.Then, fr)
	case *core.Bracket:
		acquired, err := in.eval(e.Acquire, fr)
		if err != nil {
			return nil, err
		}
		if _, isExit := asExit(acquired); isExit {
			// Nothing was acquired, so there is nothing to release.
			return acquired, nil
		}
		inner := &Frame{parent: fr, vars: map[string]Value{e.Resource: acquired}}
		body, bodyErr := in.eval(e.Body, inner)
		release, releaseErr := in.eval(e.Release, inner)
		if bodyErr != nil {
			// An interpreter failure is not a language exit; release still ran,
			// and the original failure is what the caller sees.
			return nil, bodyErr
		}
		if releaseErr != nil {
			return nil, releaseErr
		}
		bodyExit, bodyExits := asExit(body)
		releaseExit, releaseExits := asExit(release)
		switch {
		case bodyExits && releaseExits:
			return suppress(bodyExit, releaseExit), nil
		case bodyExits:
			return body, nil
		case releaseExits:
			return release, nil
		}
		return body, nil
	case *core.Handle:
		var state Value
		var err error
		if e.State != nil {
			state, err = in.eval(e.State.Initial, fr)
			if err != nil {
				return nil, err
			}
			if _, ok := asExit(state); ok {
				return state, nil
			}
		}
		outer := cloneEvidence(in.evidence)
		in.evidence[e.Effect.Unique] = &evidence{handler: e, frame: fr, outer: outer, state: state}
		installed := in.evidence[e.Effect.Unique]
		v, err := in.eval(e.Body, fr)
		in.evidence = outer
		if err != nil {
			return nil, err
		}
		if exit, ok := asExit(v); ok {
			if exit.Target != installed {
				return v, nil
			}
			var clause *core.HandlerClause
			for i := range e.Clauses {
				if e.Clauses[i].Op == exit.Op {
					clause = &e.Clauses[i]
					break
				}
			}
			if clause == nil || !clause.Op.Abort {
				return nil, fmt.Errorf("eval: abort target missing clause `%s`", exit.Op.Name)
			}
			vars := map[string]Value{}
			if clause.SuppressedParam != "" {
				vars[clause.SuppressedParam] = failureList(snapshotFailure(exit).Suppressed())
			}
			if e.State != nil {
				vars[e.State.Name] = installed.state
			}
			for i, p := range clause.Params {
				if p != "_" && p != "()" {
					vars[p] = exit.Payload[i]
				}
			}
			return in.eval(clause.Body, &Frame{parent: installed.frame, vars: vars})
		}
		if e.Return == nil {
			return v, nil
		}
		vars := map[string]Value{}
		if e.State != nil {
			vars[e.State.Name] = installed.state
		}
		if e.Return.Param != "_" && e.Return.Param != "()" {
			vars[e.Return.Param] = v
		}
		return in.eval(e.Return.Body, &Frame{parent: fr, vars: vars})
	case *core.App:
		switch e.CalleeKind {
		case core.Worker:
			// Saturation was resolved in elaboration; anything off here is
			// an internal error, not a user program's fault.
			ref := e.Callee.(*core.VarRef)
			def, ok := in.env.workers[ref.Name]
			if !ok {
				return nil, fmt.Errorf("eval: unknown worker `%s`", ref.Name)
			}
			if len(e.Args) != len(def.Params) {
				return nil, fmt.Errorf("eval: worker `%s` arity mismatch — the linter should have caught this", ref.Name)
			}
			vars := make(map[string]Value, len(e.Args))
			for i, a := range e.Args {
				v, err := in.eval(a, fr)
				if err != nil {
					return nil, err
				}
				if _, ok := asExit(v); ok {
					return v, nil
				}
				vars[def.Params[i]] = v
			}
			// Evidence arguments are explicit Core even though the interpreter
			// represents their values as a map. Restricting the worker to that
			// map gives it the same lexical (not dynamically scoped) behavior
			// as the generated Go parameters.
			callEvidence := make(map[int]*evidence, len(e.EvidenceArgs))
			for _, arg := range e.EvidenceArgs {
				ev := in.evidence[arg.Unique]
				if ev == nil {
					return nil, fmt.Errorf("eval: missing evidence `%s` for worker `%s`", arg.Name, ref.Name)
				}
				callEvidence[arg.Unique] = ev
			}
			row, rowErr := in.argumentRow(e.Row, fr)
			if rowErr != nil {
				return nil, rowErr
			}
			rows := bindInvocationRow(def.RowParam, def.RowEffects, row, callEvidence)
			saved := in.evidence
			in.evidence = callEvidence
			descriptors, typeErr := in.instantiateDescriptors(def.TyParams, e.TyArgs, fr)
			if typeErr != nil {
				in.evidence = saved
				return nil, typeErr
			}
			// Workers see no caller locals — matching compiled scoping.
			var out Value
			var err error
			if in.env.tailLoop(def) != nil {
				// Self tail calls run as a frame-reuse loop (doc/design.md,
				// "Interpreter and REPL") — constant Go stack, like the
				// compiled backend's for-loop rewrite.
				out, err = in.evalTailLoop(def, vars, descriptors, rows)
			} else {
				out, err = in.eval(def.Body, &Frame{vars: vars, types: descriptors, rows: rows})
			}
			in.evidence = saved
			return out, err
		case core.Value:
			calleeV, err := in.eval(e.Callee, fr)
			if err != nil {
				return nil, err
			}
			if _, ok := asExit(calleeV); ok {
				return calleeV, nil
			}
			c, ok := calleeV.(*Closure)
			if !ok {
				return nil, fmt.Errorf("eval: applying a %T — the linter should have caught this", calleeV)
			}
			if len(e.Args) != 1 {
				return nil, fmt.Errorf("eval: App{Value} with %d args — the linter should have caught this", len(e.Args))
			}
			v, err := in.eval(e.Args[0], fr)
			if err != nil {
				return nil, err
			}
			if _, ok := asExit(v); ok {
				return v, nil
			}
			saved := in.evidence
			callEvidence := cloneEvidence(c.Evidence)
			for _, arg := range e.EvidenceArgs {
				ev := in.evidence[arg.Unique]
				if ev == nil {
					return nil, fmt.Errorf("eval: missing evidence `%s` for function call", arg.Name)
				}
				callEvidence[arg.Unique] = ev
			}
			row, rowErr := in.argumentRow(e.Row, fr)
			if rowErr != nil {
				return nil, rowErr
			}
			rows := bindInvocationRow(c.rowParam, c.rowEffects, row, callEvidence)
			in.evidence = callEvidence
			out, err := in.eval(c.Body, &Frame{parent: c.Env, vars: map[string]Value{c.Param: v}, rows: rows})
			in.evidence = saved
			return out, err
		case core.Ctor:
			fields := make([]Value, len(e.Args))
			for i, a := range e.Args {
				v, err := in.eval(a, fr)
				if err != nil {
					return nil, err
				}
				if _, ok := asExit(v); ok {
					return v, nil
				}
				fields[i] = v
			}
			if e.Ctor.Repr == types.ReprList {
				// The bundled List shares the compiled backend's runtime
				// representation (doc/roadmap-list.md). The discriminator is on
				// the constructor because there is no ADT table here.
				if len(fields) == 0 {
					return fangort.ListNil[Value](), nil
				}
				return fangort.ListCons(fields[0], fields[1].(fangort.List[Value])), nil
			}
			return &CtorVal{Ctor: e.Ctor, Fields: fields}, nil
		default:
			return nil, fmt.Errorf("eval: App with unknown CalleeKind")
		}
	case *core.Case:
		v, err := in.eval(e.Scrut, fr)
		if err != nil {
			return nil, err
		}
		if _, ok := asExit(v); ok {
			return v, nil
		}
		frame := &Frame{parent: fr, vars: map[string]Value{e.Bind: v}}
		return in.tree(e.Tree, frame, in.eval)
	default:
		return nil, fmt.Errorf("eval: unhandled Core node %T", e)
	}
}

// evalResumeTail is the interpreter counterpart of codegen's clause emitter.
// It accepts only the control skeleton proved by Core lint and returns the
// operation result carried by the terminal ResumeTail.
func (in *interp) evalResumeTail(e core.Expr, fr *Frame, owner types.ResumeID, ev *evidence) (Value, error) {
	switch e := e.(type) {
	case *core.ControlExit:
		return in.eval(e, fr)
	case *core.ResumeTail:
		if e.Owner != owner {
			return nil, fmt.Errorf("eval: ResumeTail owner %d does not match handler clause %d", e.Owner, owner)
		}
		value, err := in.eval(e.Value, fr)
		if err != nil {
			return nil, err
		}
		if _, ok := asExit(value); ok {
			return value, nil
		}
		if e.NextState != nil {
			next, err := in.eval(e.NextState, fr)
			if err != nil {
				return nil, err
			}
			if _, ok := asExit(next); ok {
				return next, nil
			}
			ev.state = next
		}
		return value, nil
	case *core.Let:
		if e.Rec {
			lam, ok := e.Rhs.(*core.Lambda)
			if !ok {
				return nil, fmt.Errorf("eval: recursive Let `%s` without a Lambda RHS", e.Name)
			}
			frame := &Frame{parent: closureFrame(lam, fr), vars: map[string]Value{}}
			frame.vars[e.Name] = in.plainClosure(lam, frame)
			return in.evalResumeTail(e.Body, frame, owner, ev)
		}
		v, err := in.eval(e.Rhs, fr)
		if err != nil {
			return nil, err
		}
		if _, ok := asExit(v); ok {
			return v, nil
		}
		return in.evalResumeTail(e.Body, &Frame{parent: fr, vars: map[string]Value{e.Name: v}}, owner, ev)
	case *core.Seq:
		first, err := in.eval(e.First, fr)
		if err != nil {
			return nil, err
		}
		if _, ok := asExit(first); ok {
			return first, nil
		}
		return in.evalResumeTail(e.Then, fr, owner, ev)
	case *core.If:
		cond, err := in.eval(e.Cond, fr)
		if err != nil {
			return nil, err
		}
		if _, ok := asExit(cond); ok {
			return cond, nil
		}
		if cond.(bool) {
			return in.evalResumeTail(e.Then, fr, owner, ev)
		}
		return in.evalResumeTail(e.Else, fr, owner, ev)
	case *core.Case:
		v, err := in.eval(e.Scrut, fr)
		if err != nil {
			return nil, err
		}
		if _, ok := asExit(v); ok {
			return v, nil
		}
		frame := &Frame{parent: fr, vars: map[string]Value{e.Bind: v}}
		return in.tree(e.Tree, frame, func(body core.Expr, leafFrame *Frame) (Value, error) {
			return in.evalResumeTail(body, leafFrame, owner, ev)
		})
	default:
		return nil, fmt.Errorf("eval: non-tail-resumptive handler clause node %T", e)
	}
}

func cloneEvidence(src map[int]*evidence) map[int]*evidence {
	dst := make(map[int]*evidence, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (in *interp) showValue(v Value) (string, error) {
	var s string
	switch v := v.(type) {
	case int64:
		s = fangort.ShowInt(v)
	case float64:
		s = fangort.ShowFloat(v)
	case string:
		s = fangort.ShowString(v)
	case rune:
		s = fangort.ShowChar(v)
	case bool:
		s = fangort.ShowBool(v)
	case *CtorVal:
		s = showCtorVal(v, false)
	case fangort.List[Value]:
		s = fangort.ListShow(showFieldValueNested, v, false)
	default:
		return "", fmt.Errorf("eval: printing a %T", v)
	}
	return s, nil
}

func (in *interp) nativeRuntime() *natives.Runtime {
	return &natives.Runtime{Reader: in.ioctx.Reader, Writer: in.ioctx.Writer, Args: in.ioctx.Args, Dir: in.ioctx.Dir, Equal: eqValue, Show: in.showValue, Expand: in.env.Expand}
}

// tree walks a decision tree, mirroring the compiled backend's switches.
// The leaf callback decides what a matched branch does with its body: plain
// evaluation (in.eval) everywhere except inside a tail-call loop, where
// tailStep keeps walking the tail skeleton.
func (in *interp) tree(t core.Tree, fr *Frame, leaf func(core.Expr, *Frame) (Value, error)) (Value, error) {
	switch t := t.(type) {
	case *core.Unreachable:
		return nil, fmt.Errorf("unreachable pattern match")
	case *core.Guard:
		v, err := in.eval(t.Cond, fr)
		if err != nil {
			return nil, err
		}
		if _, ok := asExit(v); ok {
			return v, nil
		}
		if v.(bool) {
			return in.tree(t.Then, fr, leaf)
		}
		return in.tree(t.Else, fr, leaf)
	case *core.Leaf:
		return leaf(t.Body, fr)
	case *core.SwitchCtor:
		v, ok := fr.lookup(t.Scrut)
		if !ok {
			return nil, fmt.Errorf("eval: tree scrutinee `%s` unbound — the linter should have caught this", t.Scrut)
		}
		if b, isBool := v.(bool); isBool {
			// Bool is an ordinary ADT in the checker but a native bool value
			// here, exactly as in codegen (doc/design.md, "Go backend and runtime").
			want := "False"
			if b {
				want = "True"
			}
			for _, c := range t.Cases {
				if c.Ctor.Name == want {
					return in.tree(c.Tree, fr, leaf)
				}
			}
			return in.tree(t.Default, fr, leaf)
		}
		if l, isList := v.(fangort.List[Value]); isList {
			// As with Bool above: an ordinary ADT to the checker, a runtime
			// representation here and in generated Go.
			want, binds := listNilIndex, []Value(nil)
			if !l.IsEmpty() {
				want, binds = listConsIndex, []Value{l.Head(), l.Tail()}
			}
			for _, c := range t.Cases {
				if c.Ctor.Index != want {
					continue
				}
				vars := map[string]Value{}
				for i, bind := range c.Binds {
					if bind != "" {
						vars[bind] = binds[i]
					}
				}
				return in.tree(c.Tree, &Frame{parent: fr, vars: vars}, leaf)
			}
			return in.tree(t.Default, fr, leaf)
		}
		cv, isCtor := v.(*CtorVal)
		if !isCtor {
			return nil, fmt.Errorf("eval: SwitchCtor on a %T — the linter should have caught this", v)
		}
		for _, c := range t.Cases {
			if c.Ctor.Index != cv.Ctor.Index {
				continue
			}
			vars := map[string]Value{}
			for i, bind := range c.Binds {
				if bind != "" {
					vars[bind] = cv.Fields[i]
				}
			}
			return in.tree(c.Tree, &Frame{parent: fr, vars: vars}, leaf)
		}
		if t.Default == nil {
			return nil, fmt.Errorf("eval: no case for constructor `%s` and no default — exhaustiveness is broken", cv.Ctor.Name)
		}
		return in.tree(t.Default, fr, leaf)
	case *core.SwitchLit:
		v, ok := fr.lookup(t.Scrut)
		if !ok {
			return nil, fmt.Errorf("eval: tree scrutinee `%s` unbound — the linter should have caught this", t.Scrut)
		}
		for _, c := range t.Cases {
			var match bool
			switch lit := c.Lit.(type) {
			case *core.IntLit:
				match = v == lit.Val
			case *core.FloatLit:
				match = v == lit.Val
			case *core.StringLit:
				match = v == lit.Val
			case *core.CharLit:
				match = v == lit.Val
			default:
				return nil, fmt.Errorf("eval: SwitchLit case is %T — the linter should have caught this", c.Lit)
			}
			if match {
				return in.tree(c.Tree, fr, leaf)
			}
		}
		return in.tree(t.Default, fr, leaf)
	default:
		return nil, fmt.Errorf("eval: unhandled tree node %T", t)
	}
}

func (in *interp) force(name string) (Value, error) {
	cell, ok := in.env.cells[name]
	if !ok {
		if d, isWorker := in.env.workers[name]; isWorker {
			if name == in.env.entry && len(d.Params) == 1 {
				return in.eval(d.Body, &Frame{vars: map[string]Value{d.Params[0]: struct{}{}}})
			}
			return nil, fmt.Errorf("eval: bare reference to worker `%s` — the linter should have caught this", name)
		}
		return nil, fmt.Errorf("eval: undefined name `%s` (checker should have caught this)", name)
	}
	if cell.forced {
		return cell.memo, nil
	}
	if cell.forcing {
		return nil, fmt.Errorf("eval: `%s` depends on itself", name)
	}
	cell.forcing = true
	v, err := in.eval(cell.Body, nil)
	cell.forcing = false
	if err != nil {
		return nil, err
	}
	if _, ok := asExit(v); ok {
		return v, nil
	}
	cell.memo, cell.forced = v, true
	return v, nil
}

// eqValue is structural equality — the interpreter's mirror of the derived
// eqT_X functions (doc/design.md, "Go backend and runtime"). Function-containing types were rejected by the
// checker, so every reachable field compares.
// listNilIndex and listConsIndex mirror the bundled declaration's layout,
// which infer's markListRepr verifies before any of this runs.
const (
	listNilIndex  = 0
	listConsIndex = 1
)

func eqValue(l, r Value) bool {
	if ll, ok := l.(fangort.List[Value]); ok {
		// Must precede the scalar fallback: List is deliberately not
		// comparable with Go ==, so reaching the fallback would panic rather
		// than answer.
		return fangort.ListEq(eqValue, ll, r.(fangort.List[Value]))
	}
	if lc, ok := l.(*CtorVal); ok {
		rc := r.(*CtorVal)
		if lc.Ctor.Index != rc.Ctor.Index {
			return false
		}
		for i := range lc.Fields {
			if !eqValue(lc.Fields[i], rc.Fields[i]) {
				return false
			}
		}
		return true
	}
	return l == r // scalars: identical to the native Go operators
}
