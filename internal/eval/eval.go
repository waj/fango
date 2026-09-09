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
	Param    string
	Body     core.Expr
	Env      *Frame
	Evidence map[int]*evidence
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
	handler *core.Handle
	frame   *Frame
	outer   map[int]*evidence
}

// Env holds top-level cells and workers.
type Env struct {
	cells   map[string]*Cell
	workers map[string]*core.Def
	entry   string
	// tails caches core.DetectTailLoop per *core.Def (nil = ineligible).
	// Pointer identity means REPL redefinition invalidates naturally: a new
	// generation is a new *core.Def.
	tails map[*core.Def]*core.TailLoop

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
	parent *Frame
	vars   map[string]Value
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
	return &Env{cells: map[string]*Cell{}, workers: map[string]*core.Def{}, tails: map[*core.Def]*core.TailLoop{}}
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
	e.entry = p.Entry
	if e.entry == "" {
		e.entry = "main"
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
	// step budget, and no native that observes process-global state or lives
	// in a Go sidecar the interpreter cannot load (doc/design.md,
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

// Force evaluates (and memoizes) the named top-level binding.
func Force(ctx context.Context, name string, env *Env, out io.Writer) (Value, error) {
	return ForceIO(ctx, name, env, NewIOContext(strings.NewReader(""), out))
}

func ForceIO(ctx context.Context, name string, env *Env, ioctx *IOContext) (Value, error) {
	return (&interp{ctx: ctx, env: env, out: ioctx.Writer, ioctx: ioctx, evidence: map[int]*evidence{}}).force(name)
}

func (in *interp) eval(e core.Expr, fr *Frame) (Value, error) {
	in.steps++
	if in.steps%pollEvery == 0 {
		select {
		case <-in.ctx.Done():
			return nil, fmt.Errorf("interrupted")
		default:
		}
		if in.budget > 0 && in.steps > in.budget {
			return nil, ErrStepBudget
		}
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
			frame := &Frame{parent: fr, vars: map[string]Value{}}
			frame.vars[e.Name] = &Closure{Param: lam.Param, Body: lam.Body, Env: frame, Evidence: cloneEvidence(in.evidence)}
			return in.eval(e.Body, frame)
		}
		// Eager, in order — identical to the compiled backend's locals.
		v, err := in.eval(e.Rhs, fr)
		if err != nil {
			return nil, err
		}
		return in.eval(e.Body, &Frame{parent: fr, vars: map[string]Value{e.Name: v}})
	case *core.Lambda:
		return &Closure{Param: e.Param, Body: e.Body, Env: fr, Evidence: cloneEvidence(in.evidence)}, nil
	case *core.Neg:
		v, err := in.eval(e.Operand, fr)
		if err != nil {
			return nil, err
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
			code, ok := v.(*meta.Code)
			if !ok {
				return nil, fmt.Errorf("eval: quote hole evaluated to a %T, want code", v)
			}
			holes[i] = code
		}
		return &meta.Code{Template: e.Template, Holes: holes}, nil
	case *core.TypeOf:
		return e.Repr, nil
	case *core.NativeCall:
		args := make([]Value, len(e.Args))
		for i, a := range e.Args {
			v, err := in.eval(a, fr)
			if err != nil {
				return nil, err
			}
			args[i] = v
		}
		if !in.compileTime {
			executor, err := in.ioctx.nativeExecutor()
			if err != nil {
				return nil, err
			}
			if executor.Has(e.Name) {
				return executor.Call(in.ctx, in.ioctx, e.Name, args)
			}
		}
		if spec, ok := natives.Lookup(e.Name); ok && !spec.Effect {
			if in.compileTime && !spec.CompileTimeSafe {
				return nil, &UnsafeNativeError{Name: e.Name, Reason: "observes process-global state the compiler shares"}
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
			args[i] = v
		}
		if ev := in.evidence[e.Effect.Unique]; ev != nil && ev.handler != nil {
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
			for i, p := range clause.Params {
				if p != "_" && p != "()" {
					vars[p] = args[i]
				}
			}
			saved := in.evidence
			in.evidence = cloneEvidence(ev.outer)
			v, err := in.evalResumeTail(clause.Body, &Frame{parent: ev.frame, vars: vars}, clause.ResumeID)
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
				return executor.Call(in.ctx, in.ioctx, key, args)
			}
		}
		if spec, ok := natives.Lookup(key); ok && spec.Effect {
			if in.compileTime {
				return nil, &UnsafeNativeError{Name: key, Reason: "performs an effect"}
			}
			return spec.Eval(in.nativeRuntime(), args)
		}
		return nil, fmt.Errorf("eval: unhandled effect operation `%s.%s`", e.Effect.Name, e.Op.Name)
	case *core.ResumeTail:
		return nil, fmt.Errorf("eval: ResumeTail outside verified handler-clause evaluation")
	case *core.Seq:
		if _, err := in.eval(e.First, fr); err != nil {
			return nil, err
		}
		return in.eval(e.Then, fr)
	case *core.Handle:
		outer := cloneEvidence(in.evidence)
		in.evidence[e.Effect.Unique] = &evidence{handler: e, frame: fr, outer: outer}
		v, err := in.eval(e.Body, fr)
		in.evidence = outer
		if err != nil {
			return nil, err
		}
		if e.Return == nil {
			return v, nil
		}
		vars := map[string]Value{}
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
			saved := in.evidence
			in.evidence = callEvidence
			// Workers see no caller locals — matching compiled scoping.
			var out Value
			var err error
			if in.env.tailLoop(def) != nil {
				// Self tail calls run as a frame-reuse loop (doc/design.md,
				// "Interpreter and REPL") — constant Go stack, like the
				// compiled backend's for-loop rewrite.
				out, err = in.evalTailLoop(def, vars)
			} else {
				out, err = in.eval(def.Body, &Frame{vars: vars})
			}
			in.evidence = saved
			return out, err
		case core.Value:
			calleeV, err := in.eval(e.Callee, fr)
			if err != nil {
				return nil, err
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
			saved := in.evidence
			callEvidence := cloneEvidence(c.Evidence)
			for _, arg := range e.EvidenceArgs {
				ev := in.evidence[arg.Unique]
				if ev == nil {
					return nil, fmt.Errorf("eval: missing evidence `%s` for function call", arg.Name)
				}
				callEvidence[arg.Unique] = ev
			}
			in.evidence = callEvidence
			out, err := in.eval(c.Body, &Frame{parent: c.Env, vars: map[string]Value{c.Param: v}})
			in.evidence = saved
			return out, err
		case core.Ctor:
			fields := make([]Value, len(e.Args))
			for i, a := range e.Args {
				v, err := in.eval(a, fr)
				if err != nil {
					return nil, err
				}
				fields[i] = v
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
		frame := &Frame{parent: fr, vars: map[string]Value{e.Bind: v}}
		return in.tree(e.Tree, frame, in.eval)
	default:
		return nil, fmt.Errorf("eval: unhandled Core node %T", e)
	}
}

// evalResumeTail is the interpreter counterpart of codegen's clause emitter.
// It accepts only the control skeleton proved by Core lint and returns the
// operation result carried by the terminal ResumeTail.
func (in *interp) evalResumeTail(e core.Expr, fr *Frame, owner types.ResumeID) (Value, error) {
	switch e := e.(type) {
	case *core.ResumeTail:
		if e.Owner != owner {
			return nil, fmt.Errorf("eval: ResumeTail owner %d does not match handler clause %d", e.Owner, owner)
		}
		return in.eval(e.Value, fr)
	case *core.Let:
		if e.Rec {
			lam, ok := e.Rhs.(*core.Lambda)
			if !ok {
				return nil, fmt.Errorf("eval: recursive Let `%s` without a Lambda RHS", e.Name)
			}
			frame := &Frame{parent: fr, vars: map[string]Value{}}
			frame.vars[e.Name] = &Closure{Param: lam.Param, Body: lam.Body, Env: frame, Evidence: cloneEvidence(in.evidence)}
			return in.evalResumeTail(e.Body, frame, owner)
		}
		v, err := in.eval(e.Rhs, fr)
		if err != nil {
			return nil, err
		}
		return in.evalResumeTail(e.Body, &Frame{parent: fr, vars: map[string]Value{e.Name: v}}, owner)
	case *core.Seq:
		if _, err := in.eval(e.First, fr); err != nil {
			return nil, err
		}
		return in.evalResumeTail(e.Then, fr, owner)
	case *core.If:
		cond, err := in.eval(e.Cond, fr)
		if err != nil {
			return nil, err
		}
		if cond.(bool) {
			return in.evalResumeTail(e.Then, fr, owner)
		}
		return in.evalResumeTail(e.Else, fr, owner)
	case *core.Case:
		v, err := in.eval(e.Scrut, fr)
		if err != nil {
			return nil, err
		}
		frame := &Frame{parent: fr, vars: map[string]Value{e.Bind: v}}
		return in.tree(e.Tree, frame, func(body core.Expr, leafFrame *Frame) (Value, error) {
			return in.evalResumeTail(body, leafFrame, owner)
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
	cell.memo, cell.forced = v, true
	return v, nil
}

// eqValue is structural equality — the interpreter's mirror of the derived
// eqT_X functions (doc/design.md, "Go backend and runtime"). Function-containing types were rejected by the
// checker, so every reachable field compares.
func eqValue(l, r Value) bool {
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
