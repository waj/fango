package machine

import (
	"fmt"
	"slices"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// Lint independently checks the execution contract consumed by machine
// interpreters and emitters. In particular it recomputes liveness and frame
// layouts instead of trusting Lower's materialized proof data.
func Lint(p *Prog) []error {
	workers := map[string]*Worker{}
	var errs []error
	for i := range p.Workers {
		w := &p.Workers[i]
		if w.Name == "" {
			errs = append(errs, fmt.Errorf("machine worker %d has no name", i))
		}
		if workers[w.Name] != nil {
			errs = append(errs, fmt.Errorf("duplicate machine worker %q", w.Name))
		}
		workers[w.Name] = w
	}
	for i := range p.Workers {
		errs = append(errs, lintWorker(&p.Workers[i], workers)...)
	}
	return errs
}

func lintWorker(w *Worker, workers map[string]*Worker) []error {
	where := "machine worker " + w.Name
	var errs []error
	locals := map[string]types.Type{}
	for _, local := range w.Locals {
		if local.Name == "" || local.Ty == nil {
			errs = append(errs, fmt.Errorf("%s: incomplete local metadata", where))
			continue
		}
		if locals[local.Name] != nil {
			errs = append(errs, fmt.Errorf("%s: duplicate local %q", where, local.Name))
		}
		locals[local.Name] = local.Ty
	}
	for _, param := range w.Params {
		if ty := locals[param.Name]; ty == nil || !types.Equal(ty, param.Ty) {
			errs = append(errs, fmt.Errorf("%s: parameter %q is absent or mistyped in locals", where, param.Name))
		}
	}
	if int(w.Entry) < 0 || int(w.Entry) >= len(w.Blocks) {
		errs = append(errs, fmt.Errorf("%s: invalid entry block %d", where, w.Entry))
	}
	for i := range w.Blocks {
		block := &w.Blocks[i]
		blockWhere := fmt.Sprintf("%s block %d", where, block.ID)
		if block.ID != BlockID(i) {
			errs = append(errs, fmt.Errorf("%s: id disagrees with position %d", blockWhere, i))
		}
		if block.Term == nil {
			errs = append(errs, fmt.Errorf("%s: no terminator", blockWhere))
			continue
		}
		for _, succ := range successors(block.Term) {
			if int(succ) < 0 || int(succ) >= len(w.Blocks) {
				errs = append(errs, fmt.Errorf("%s: invalid successor %d", blockWhere, succ))
			}
		}
		checkExpr := func(e core.Expr, slot string, allowExit bool) {
			if e == nil {
				errs = append(errs, fmt.Errorf("%s: missing %s", blockWhere, slot))
				return
			}
			control := core.ExprControl(e)
			if control.Polymorphic || control.Transport == types.Machine || (!allowExit && control.Transport != types.Direct) {
				errs = append(errs, fmt.Errorf("%s: %s has unsupported %s control", blockWhere, slot, core.ControlName(control)))
			}
			for name := range freeLocalRefs(e) {
				if locals[name] == nil {
					errs = append(errs, fmt.Errorf("%s: %s uses unknown local %q", blockWhere, slot, name))
				}
			}
		}
		checkBind := func(bind Local) {
			if ty := locals[bind.Name]; ty == nil || bind.Ty == nil || !types.Equal(ty, bind.Ty) {
				errs = append(errs, fmt.Errorf("%s: result local %q is absent or mistyped", blockWhere, bind.Name))
			}
		}
		switch term := block.Term.(type) {
		case *Eval:
			checkBind(term.Bind)
			checkExpr(term.Value, "evaluated expression", true)
			if term.Value != nil && term.Bind.Ty != nil && !types.Equal(term.Value.Type(), term.Bind.Ty) {
				errs = append(errs, fmt.Errorf("%s: evaluated result type disagrees with binding", blockWhere))
			}
		case *Branch:
			checkExpr(term.Cond, "branch condition", false)
		case *SwitchCtor:
			if term.ADT == nil {
				errs = append(errs, fmt.Errorf("%s: constructor switch has no ADT", blockWhere))
			}
			if locals[term.Scrut] == nil {
				errs = append(errs, fmt.Errorf("%s: constructor switch uses unknown local %q", blockWhere, term.Scrut))
			}
			for _, c := range term.Cases {
				if c.Ctor == nil || term.ADT == nil || c.Ctor.Result.Unique != term.ADT.Con.Unique {
					errs = append(errs, fmt.Errorf("%s: constructor switch has a foreign or missing case", blockWhere))
					continue
				}
				if len(c.Binds) != len(c.Ctor.Fields) {
					errs = append(errs, fmt.Errorf("%s: constructor %q binding arity mismatch", blockWhere, c.Ctor.Name))
				}
				for _, bind := range c.Binds {
					if bind.Name == "" {
						continue
					}
					checkBind(bind)
				}
			}
		case *SwitchLit:
			if locals[term.Scrut] == nil {
				errs = append(errs, fmt.Errorf("%s: literal switch uses unknown local %q", blockWhere, term.Scrut))
			}
			for i, c := range term.Cases {
				checkExpr(c.Lit, fmt.Sprintf("literal case %d", i+1), false)
				if c.Lit != nil && locals[term.Scrut] != nil && !types.Equal(c.Lit.Type(), locals[term.Scrut]) {
					errs = append(errs, fmt.Errorf("%s: literal case %d is mistyped", blockWhere, i+1))
				}
			}
		case *Suspend:
			checkBind(term.Bind)
			checkExpr(term.Request, "suspension request", false)
		case *Call:
			checkBind(term.Bind)
			for i, arg := range term.Args {
				checkExpr(arg, fmt.Sprintf("call argument %d", i+1), false)
			}
			callee := workers[term.Callee]
			if callee == nil {
				errs = append(errs, fmt.Errorf("%s: unknown machine callee %q", blockWhere, term.Callee))
			} else {
				if len(term.Args) != len(callee.Params) {
					errs = append(errs, fmt.Errorf("%s: call to %q has %d arguments, want %d", blockWhere, term.Callee, len(term.Args), len(callee.Params)))
				}
				for i, arg := range term.Args {
					if i < len(callee.Params) && !types.Equal(arg.Type(), callee.Params[i].Ty) {
						errs = append(errs, fmt.Errorf("%s: call argument %d to %q is mistyped", blockWhere, i+1, term.Callee))
					}
				}
				if term.Bind.Ty != nil && !types.Equal(term.Bind.Ty, callee.Result) {
					errs = append(errs, fmt.Errorf("%s: call result from %q is mistyped", blockWhere, term.Callee))
				}
			}
		case *PushCleanup:
			checkBind(term.Resource)
			checkExpr(term.Acquire, "cleanup acquisition", true)
			checkExpr(term.Release, "cleanup release", true)
			if term.Acquire != nil && term.Resource.Ty != nil && !types.Equal(term.Acquire.Type(), term.Resource.Ty) {
				errs = append(errs, fmt.Errorf("%s: cleanup acquisition and resource types differ", blockWhere))
			}
			if term.Release != nil {
				if control := core.ExprControl(term.Release); control.Transport == types.Machine || control.Polymorphic {
					errs = append(errs, fmt.Errorf("%s: cleanup release may suspend", blockWhere))
				}
			}
		case *PopCleanup:
		case *Return:
			checkExpr(term.Value, "return value", true)
			if term.Value != nil && !types.Equal(term.Value.Type(), w.Result) {
				errs = append(errs, fmt.Errorf("%s: return type %s, want %s", blockWhere, types.Show(term.Value.Type()), types.Show(w.Result)))
			}
		default:
			errs = append(errs, fmt.Errorf("%s: unknown terminator %T", blockWhere, block.Term))
		}
	}

	if len(errs) != 0 {
		return errs
	}
	reachable := map[BlockID]bool{}
	var visit func(BlockID)
	visit = func(id BlockID) {
		if reachable[id] {
			return
		}
		reachable[id] = true
		for _, succ := range successors(w.Blocks[id].Term) {
			visit(succ)
		}
	}
	visit(w.Entry)
	for _, block := range w.Blocks {
		if !reachable[block.ID] {
			errs = append(errs, fmt.Errorf("%s: unreachable block %d", where, block.ID))
		}
	}
	errs = append(errs, lintCleanupDepths(w, reachable)...)

	expected := *w
	expected.Blocks = append([]Block(nil), w.Blocks...)
	expected.Frame = nil
	analyze(&expected)
	for i := range w.Blocks {
		if !slices.Equal(w.Blocks[i].LiveIn, expected.Blocks[i].LiveIn) {
			errs = append(errs, fmt.Errorf("%s block %d: LiveIn %v, want %v", where, i, w.Blocks[i].LiveIn, expected.Blocks[i].LiveIn))
		}
		if !slices.Equal(w.Blocks[i].LiveOut, expected.Blocks[i].LiveOut) {
			errs = append(errs, fmt.Errorf("%s block %d: LiveOut %v, want %v", where, i, w.Blocks[i].LiveOut, expected.Blocks[i].LiveOut))
		}
	}
	if !equalLocals(w.Frame, expected.Frame) {
		errs = append(errs, fmt.Errorf("%s: frame layout %v, want %v", where, localNames(w.Frame), localNames(expected.Frame)))
	}
	return errs
}

// Cleanup depth is structural proof data even though it does not need a field
// in the current IR. Every normal edge entering a block must carry the same
// lexical depth, Pop must own an active scope, and workers must return with no
// cleanup belonging to them. Dynamic exits may occur in expression slots and
// are handled by the runtime unwind path instead of a normal successor edge.
func lintCleanupDepths(w *Worker, reachable map[BlockID]bool) []error {
	depths := make([]int, len(w.Blocks))
	for i := range depths {
		depths[i] = -1
	}
	depths[w.Entry] = 0
	work := []BlockID{w.Entry}
	var errs []error
	for len(work) != 0 {
		id := work[0]
		work = work[1:]
		if !reachable[id] {
			continue
		}
		depth := depths[id]
		block := &w.Blocks[id]
		nextDepth := depth
		switch block.Term.(type) {
		case *PushCleanup:
			nextDepth++
		case *PopCleanup:
			if depth == 0 {
				errs = append(errs, fmt.Errorf("machine worker %s block %d: cleanup stack underflow", w.Name, id))
				continue
			}
			nextDepth--
		case *Return:
			if depth != 0 {
				errs = append(errs, fmt.Errorf("machine worker %s block %d: returns with %d pending cleanup(s)", w.Name, id, depth))
			}
		}
		for _, succ := range successors(block.Term) {
			if depths[succ] == -1 {
				depths[succ] = nextDepth
				work = append(work, succ)
				continue
			}
			if depths[succ] != nextDepth {
				errs = append(errs, fmt.Errorf("machine worker %s block %d: cleanup depth is %d on one edge and %d on another", w.Name, succ, depths[succ], nextDepth))
			}
		}
	}
	return errs
}

func equalLocals(a, b []Local) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || !types.Equal(a[i].Ty, b[i].Ty) {
			return false
		}
	}
	return true
}

func localNames(locals []Local) []string {
	out := make([]string, len(locals))
	for i, local := range locals {
		out[i] = local.Name
	}
	return out
}
