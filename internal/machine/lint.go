package machine

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// Lint independently checks the execution contract consumed by machine
// interpreters and emitters. In particular it recomputes liveness and frame
// layouts instead of trusting Lower's materialized proof data.
// Lint checks a unit's Machine IR against every family it calls. Imported
// families are declarations — parameters, evidence, rows, and result with no
// blocks of their own: their owner proved those when it lowered them.
func Lint(p *Prog) []error {
	declared := p.Declared
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
	for i := range declared {
		if workers[declared[i].Name] == nil {
			workers[declared[i].Name] = &declared[i]
		}
	}
	for i := range p.Workers {
		errs = append(errs, lintWorker(&p.Workers[i], workers)...)
	}
	for _, closure := range p.Closures {
		errs = append(errs, lintClosure(closure, workers)...)
	}
	return errs
}

func lintClosure(closure Closure, workers map[string]*Worker) []error {
	where := "machine closure " + closure.Worker
	w := workers[closure.Worker]
	if closure.Expr == nil || w == nil {
		return []error{fmt.Errorf("%s: missing lambda or worker", where)}
	}
	var errs []error
	if !slices.Equal(closure.CapturedRows, sortedRows(core.FreeRows(closure.Expr))) || closure.CallRow != closure.Expr.RowParam {
		errs = append(errs, fmt.Errorf("%s: missing or stale closure row bindings", where))
	}
	same := func(a, b core.EffectInstance) bool {
		if a.Unique != b.Unique || a.Name != b.Name || len(a.Args) != len(b.Args) || !types.EqualCaptures(a.Captures, b.Captures) {
			return false
		}
		for i := range a.Args {
			if !core.EqualValueRepresentation(a.Args[i], b.Args[i]) {
				return false
			}
		}
		// The lowered side is pinned to the protocol the closure reaches this
		// interpretation at, which is Direct only for a fixed Direct activation.
		return a.Control == (types.Control{Transport: Mode(b.Control)})
	}
	needed := core.FreeEvidence(closure.Expr)
	for _, ev := range closure.CapturedEvidence {
		want, found := needed[ev.Unique]
		if !found || !same(ev, want) {
			errs = append(errs, fmt.Errorf("%s: extra or stale captured evidence", where))
		}
		delete(needed, ev.Unique)
	}
	if len(needed) != 0 {
		errs = append(errs, fmt.Errorf("%s: missing captured evidence", where))
	}
	if len(closure.CallEvidence) != len(closure.Expr.EffectParams) {
		errs = append(errs, fmt.Errorf("%s: missing invocation evidence", where))
	} else {
		for i, ev := range closure.CallEvidence {
			if !same(ev, closure.Expr.EffectParams[i]) {
				errs = append(errs, fmt.Errorf("%s: stale invocation evidence", where))
			}
		}
	}
	all := append(slices.Clone(closure.CapturedEvidence), closure.CallEvidence...)
	if len(all) != len(w.EffectParams) {
		errs = append(errs, fmt.Errorf("%s: worker evidence arity disagrees with closure", where))
	} else {
		for i, ev := range all {
			if !same(ev, w.EffectParams[i]) {
				errs = append(errs, fmt.Errorf("%s: worker evidence differs from closure", where))
			}
		}
	}
	return errs
}

func lintWorker(w *Worker, workers map[string]*Worker) []error {
	where := "machine worker " + w.Name
	var errs []error
	if w.Def != nil {
		if w.RowParam != w.Def.RowParam || !slices.Equal(w.Rows, workerRows(w.Def)) {
			errs = append(errs, fmt.Errorf("%s: missing or stale worker row bindings", where))
		}
		if !reflect.DeepEqual(w.RowEffects, machineEvidence(w.Def.RowEffects)) {
			errs = append(errs, fmt.Errorf("%s: missing or stale deferred row evidence", where))
		}
	} else if w.RowParam != 0 || len(w.Rows) != 0 || len(w.RowEffects) != 0 {
		errs = append(errs, fmt.Errorf("%s: row bindings lack a source definition", where))
	}
	wantSynchronous := []int(nil)
	if w.Name == types.FailAttemptReportName && !core.CaptureContractCurrent(w.Def) {
		errs = append(errs, fmt.Errorf("%s: missing or stale failure report source contract", where))
	}
	if w.Name == types.ScopeBracketName && (len(w.Params) == 3 || len(w.SynchronousParams) > 0 || w.Def != nil && len(w.Def.Params) == 3) {
		wantSynchronous = []int{0, 1}
		if w.Def == nil {
			errs = append(errs, fmt.Errorf("%s: missing synchronous source contract", where))
		} else if scope, ok := w.Def.Body.(*core.Bracket); !ok || scope.Scope == 0 || !core.CaptureContractCurrent(w.Def) {
			errs = append(errs, fmt.Errorf("%s: stale synchronous source contract", where))
		}
	}
	if !slices.Equal(w.SynchronousParams, wantSynchronous) {
		errs = append(errs, fmt.Errorf("%s: invalid synchronous callback parameters", where))
	}
	for _, i := range w.SynchronousParams {
		if i < 0 || i >= len(w.Params) {
			errs = append(errs, fmt.Errorf("%s: invalid synchronous parameter index", where))
			continue
		}
		fn, ok := w.Params[i].Ty.(*types.TFun)
		if !ok || types.FunctionControl(fn) != (types.Control{Transport: types.Exit}) {
			errs = append(errs, fmt.Errorf("%s: synchronous parameter must use Exit transport", where))
		}
	}
	tyParams := map[int]bool{}
	for _, param := range w.TyParams {
		if param == nil || !param.Rigid || param.Kind == types.RowVar || tyParams[param.ID] {
			errs = append(errs, fmt.Errorf("%s: malformed or duplicate type parameter", where))
			continue
		}
		tyParams[param.ID] = true
	}
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
		if ty := locals[param.Name]; ty == nil || !core.EqualValueRepresentation(ty, param.Ty) {
			errs = append(errs, fmt.Errorf("%s: parameter %q is absent or mistyped in locals", where, param.Name))
		}
	}
	seenEvidence := map[int]bool{}
	seenCursorScopes := map[types.ScopeID]bool{}
	for _, ev := range append(slices.Clone(w.EffectParams), w.RowEffects...) {
		if ev.Unique == 0 || ev.Name == "" || seenEvidence[ev.Unique] {
			errs = append(errs, fmt.Errorf("%s: malformed or duplicate evidence parameter", where))
		}
		seenEvidence[ev.Unique] = true
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
			if ty := locals[bind.Name]; ty == nil || bind.Ty == nil || !core.EqualValueRepresentation(ty, bind.Ty) {
				errs = append(errs, fmt.Errorf("%s: result local %q is absent or mistyped", blockWhere, bind.Name))
			}
		}
		checkRow := func(row *core.RowArgument, required bool) {
			if (row != nil) != required {
				errs = append(errs, fmt.Errorf("%s: missing or unexpected row argument", blockWhere))
			}
			if row == nil {
				return
			}
			if row.From != 0 && !slices.Contains(w.Rows, row.From) {
				errs = append(errs, fmt.Errorf("%s: unavailable residual row", blockWhere))
			}
			last := 0
			for _, ev := range row.Effects {
				if ev.Unique <= last || !seenEvidence[ev.Unique] {
					errs = append(errs, fmt.Errorf("%s: invalid residual evidence argument", blockWhere))
				}
				last = ev.Unique
				for _, expected := range append(slices.Clone(w.EffectParams), w.RowEffects...) {
					if expected.Unique != ev.Unique {
						continue
					}
					actual := ev
					// A worker reaches each interpretation at the protocol
					// machineEvidence pinned it to.
					actual.Control = types.Control{Transport: Mode(ev.Control)}
					if !core.EqualEvidenceActivation(actual, expected) {
						errs = append(errs, fmt.Errorf("%s: stale residual evidence activation", blockWhere))
					}
				}
			}
		}
		switch term := block.Term.(type) {
		case *Eval:
			checkBind(term.Bind)
			checkExpr(term.Value, "evaluated expression", true)
			if completion, ok := term.Value.(*core.Completion); ok {
				checkRow(completion.Row, completion.Name != types.CompletionFailureName)
				valid := completion.Value != nil && types.CompletionShape(completion.Name, completion.Value.Type(), completion.Ty)
				if !valid || w.Name != completion.Name || !core.CaptureContractCurrent(w.Def) || completion.Name == types.CompletionCaptureName {
					errs = append(errs, fmt.Errorf("%s: completion elimination lacks its current intrinsic proof", blockWhere))
				}
			}
			if term.Value != nil && term.Bind.Ty != nil && !core.EqualValueRepresentation(term.Value.Type(), term.Bind.Ty) {
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
				if c.Lit != nil && locals[term.Scrut] != nil && !core.EqualValueRepresentation(c.Lit.Type(), locals[term.Scrut]) {
					errs = append(errs, fmt.Errorf("%s: literal case %d is mistyped", blockWhere, i+1))
				}
			}
		case *Suspend:
			checkBind(term.Bind)
			checkExpr(term.Request, "suspension request", false)
		case *CursorAdvance:
			checkRow(term.Row, true)
			checkBind(term.Bind)
			checkExpr(term.Cursor, "cursor operand", false)
			if term.Reply != nil {
				checkExpr(term.Reply, "coroutine reply", false)
			}
			if term.Access != types.ExclusiveAdvance {
				errs = append(errs, fmt.Errorf("%s: cursor advancement lacks exclusive access proof", blockWhere))
			}
			if term.Cursor != nil {
				if err := core.CheckCoroutineAdvance(term.Cursor.Type(), term.Reply, term.Close, term.Bind.Ty, term.Result); err != nil {
					errs = append(errs, fmt.Errorf("%s: %v", blockWhere, err))
				}
			}
		case *Call:
			requiresRow := false
			if term.CalleeExpr != nil {
				requiresRow = core.ArrowOpenRow(term.CalleeExpr.Type(), 1)
			}
			if callee := workers[term.Callee]; callee != nil {
				requiresRow = callee.RowParam != 0
			}
			checkRow(term.Row, requiresRow)
			wantSynchronous := []int(nil)
			if term.Callee == types.ScopeBracketName {
				wantSynchronous = []int{0, 1}
			}
			if !slices.Equal(term.SynchronousArgs, wantSynchronous) {
				errs = append(errs, fmt.Errorf("%s: missing or stale synchronous argument obligations", blockWhere))
			}

			checkBind(term.Bind)
			for i, arg := range term.Args {
				checkExpr(arg, fmt.Sprintf("call argument %d", i+1), false)
			}
			if term.Operation != nil {
				if term.Effect.Unique == 0 || !seenEvidence[term.Effect.Unique] {
					errs = append(errs, fmt.Errorf("%s: machine operation %q has unavailable evidence", blockWhere, term.Operation.Name))
				}
				if len(term.Args) != len(term.Operation.ParamTypes) {
					errs = append(errs, fmt.Errorf("%s: machine operation %q argument arity mismatch", blockWhere, term.Operation.Name))
				}
				break
			}
			if term.Callee == "" {
				checkExpr(term.CalleeExpr, "indirect callee", false)
				fn, ok := term.CalleeExpr.Type().(*types.TFun)
				if !ok {
					errs = append(errs, fmt.Errorf("%s: indirect machine callee is not a function", blockWhere))
				} else {
					if len(term.Args) != 1 || !core.EqualValueRepresentation(term.Args[0].Type(), fn.Arg) {
						errs = append(errs, fmt.Errorf("%s: indirect machine call argument disagrees with function", blockWhere))
					}
					validResult := core.EqualValueRepresentation(term.Bind.Ty, fn.Ret)
					if term.Capture {
						validResult = !term.Tail && types.CompletionShape(types.CompletionCaptureName, fn, term.Bind.Ty)
						if w.Name != types.CompletionCaptureName || !core.CaptureContractCurrent(w.Def) {
							errs = append(errs, fmt.Errorf("%s: completion capture lacks its current intrinsic proof", blockWhere))
						}
					}
					if !validResult {
						errs = append(errs, fmt.Errorf("%s: indirect machine call result disagrees with function", blockWhere))
					}
					wantEvidence := 0
					for _, label := range types.SortedRow(fn.Eff).Labels {
						if types.RuntimeEvidenceEffect(label) {
							wantEvidence++
						}
					}
					if len(term.EvidenceArgs) != wantEvidence {
						errs = append(errs, fmt.Errorf("%s: indirect machine call has %d evidence arguments, want %d", blockWhere, len(term.EvidenceArgs), wantEvidence))
					}
				}
				for _, ev := range term.EvidenceArgs {
					if !seenEvidence[ev.Unique] {
						errs = append(errs, fmt.Errorf("%s: indirect machine call passes unavailable evidence %q", blockWhere, ev.Name))
					}
				}
				break
			}
			callee := workers[term.Callee]
			if callee == nil {
				errs = append(errs, fmt.Errorf("%s: unknown machine callee %q", blockWhere, term.Callee))
			} else {
				if len(term.TyArgs) != len(callee.TyParams) {
					errs = append(errs, fmt.Errorf("%s: call to %q has %d type arguments, want %d", blockWhere, term.Callee, len(term.TyArgs), len(callee.TyParams)))
				}
				sub := make(map[int]types.Type, len(callee.TyParams))
				for i, param := range callee.TyParams {
					if i < len(term.TyArgs) {
						sub[param.ID] = term.TyArgs[i]
					}
				}
				if len(term.Args) != len(callee.Params) {
					errs = append(errs, fmt.Errorf("%s: call to %q has %d arguments, want %d", blockWhere, term.Callee, len(term.Args), len(callee.Params)))
				}
				if len(term.EvidenceArgs) != len(callee.EffectParams) {
					errs = append(errs, fmt.Errorf("%s: call to %q has %d evidence arguments, want %d", blockWhere, term.Callee, len(term.EvidenceArgs), len(callee.EffectParams)))
				}
				for i, ev := range term.EvidenceArgs {
					if !seenEvidence[ev.Unique] {
						errs = append(errs, fmt.Errorf("%s: call to %q passes unavailable evidence %q", blockWhere, term.Callee, ev.Name))
					}
					if i < len(callee.EffectParams) {
						want := callee.EffectParams[i]
						if ev.Unique != want.Unique || len(ev.Args) != len(want.Args) {
							errs = append(errs, fmt.Errorf("%s: call evidence %d to %q disagrees with callee", blockWhere, i+1, term.Callee))
						} else {
							for j := range ev.Args {
								if !core.EqualValueRepresentation(ev.Args[j], types.SubstRigid(want.Args[j], sub)) {
									errs = append(errs, fmt.Errorf("%s: call evidence %d type argument to %q disagrees with callee", blockWhere, i+1, term.Callee))
								}
							}
						}
					}
				}
				for i, arg := range term.Args {
					if i < len(callee.Params) && !core.EqualValueRepresentation(arg.Type(), types.SubstRigid(callee.Params[i].Ty, sub)) {
						errs = append(errs, fmt.Errorf("%s: call argument %d to %q is mistyped: got %s, want %s", blockWhere, i+1, term.Callee, types.Show(arg.Type()), types.Show(types.SubstRigid(callee.Params[i].Ty, sub))))
					}
				}
				if term.Bind.Ty != nil && !core.EqualValueRepresentation(term.Bind.Ty, types.SubstRigid(callee.Result, sub)) {
					errs = append(errs, fmt.Errorf("%s: call result from %q is mistyped", blockWhere, term.Callee))
				}
			}
		case *Handle:
			if w.Name == types.FailAttemptReportName && (w.Def == nil || w.Def.Body != term.Node) {
				errs = append(errs, fmt.Errorf("%s: failure report handler differs from its source proof", blockWhere))
			}
			checkBind(term.Bind)
			if term.Abort {
				checkBind(term.AbortBind)
				if term.Node != nil && !core.EqualValueRepresentation(term.AbortBind.Ty, term.Node.Ty) {
					errs = append(errs, fmt.Errorf("%s: abort result binding has the wrong type", blockWhere))
				}
			}
			if term.Node == nil || (len(term.Clauses) == 0) != term.Ordinary {
				errs = append(errs, fmt.Errorf("%s: malformed or unsupported machine handler", blockWhere))
				break
			}
			if term.Ordinary && (term.Abort || Mode(term.Node.Effect.Control) != types.Direct) {
				errs = append(errs, fmt.Errorf("%s: ordinary handler clauses at a machine activation", blockWhere))
			}
			if !core.EqualValueRepresentation(term.Bind.Ty, term.Node.Body.Type()) {
				errs = append(errs, fmt.Errorf("%s: normal handler result binding has the wrong type", blockWhere))
			}
			if term.State != nil {
				checkBind(term.StateResult)
				checkExpr(term.State.Initial, "handler initial state", true)
			}
			body := workers[term.BodyWorker]
			if body == nil || !core.EqualValueRepresentation(body.Result, term.Node.Body.Type()) {
				errs = append(errs, fmt.Errorf("%s: machine handler body worker is missing or mistyped", blockWhere))
			} else if len(body.Params) != len(term.BodyCaptures) {
				errs = append(errs, fmt.Errorf("%s: machine handler body capture arity mismatch", blockWhere))
			} else {
				for i, capture := range term.BodyCaptures {
					if !core.EqualValueRepresentation(body.Params[i].Ty, capture.Ty) {
						errs = append(errs, fmt.Errorf("%s: machine handler body capture %d is mistyped", blockWhere, i+1))
					}
				}
			}
			for _, clause := range term.Clauses {
				worker := workers[clause.Worker]
				if clause.Op == nil || worker == nil {
					errs = append(errs, fmt.Errorf("%s: machine handler clause worker is missing", blockWhere))
					continue
				}
				if clause.Op.Abort != term.Abort {
					errs = append(errs, fmt.Errorf("%s: machine handler mixes abort and resumptive clauses", blockWhere))
				}
				var source *core.HandlerClause
				for i := range term.Node.Clauses {
					if term.Node.Clauses[i].Op == clause.Op {
						source = &term.Node.Clauses[i]
						break
					}
				}
				if source == nil {
					errs = append(errs, fmt.Errorf("%s: machine handler clause has no source metadata", blockWhere))
					continue
				}
				wantResult := source.ResultType
				if term.Abort {
					wantResult = term.Node.Ty
				}
				if !core.EqualValueRepresentation(worker.Result, wantResult) {
					errs = append(errs, fmt.Errorf("%s: machine handler clause worker is mistyped", blockWhere))
				}
				wantParams := len(clause.Captures) + len(source.ParamTypes)
				if source.SuppressedParam != "" {
					wantParams++
					if !term.Abort || len(worker.Params) == 0 || worker.Params[len(worker.Params)-1].Name != source.SuppressedParam || !core.EqualValueRepresentation(worker.Params[len(worker.Params)-1].Ty, source.SuppressedType) {
						errs = append(errs, fmt.Errorf("%s: machine report has missing or stale suppressed payload binding", blockWhere))
					}
				}
				if clause.StateName != "" {
					wantParams++
				}
				if len(worker.Params) != wantParams {
					errs = append(errs, fmt.Errorf("%s: machine handler clause parameter arity mismatch", blockWhere))
				}
			}
			if term.ReturnWorker != "" {
				worker := workers[term.ReturnWorker]
				if worker == nil || !core.EqualValueRepresentation(worker.Result, term.Node.Ty) {
					errs = append(errs, fmt.Errorf("%s: machine handler return worker is missing or mistyped", blockWhere))
				}
			}
		case *StateResume:
			if !w.StateToken {
				errs = append(errs, fmt.Errorf("%s: state resume occurs in a worker without a state token", blockWhere))
			}
			checkExpr(term.Value, "state resume value", false)
			checkExpr(term.NextState, "state resume update", false)
			checkBind(term.Bind)
		case *PushCleanup:
			checkBind(term.Resource)
			checkExpr(term.Acquire, "cleanup acquisition", true)
			checkExpr(term.Release, "cleanup release", true)
			if term.Acquire != nil && term.Resource.Ty != nil && !core.EqualValueRepresentation(term.Acquire.Type(), term.Resource.Ty) {
				errs = append(errs, fmt.Errorf("%s: cleanup acquisition and resource types differ", blockWhere))
			}
			if term.Release != nil {
				if control := core.ExprControl(term.Release); control.Transport == types.Machine || control.Polymorphic {
					errs = append(errs, fmt.Errorf("%s: cleanup release may suspend", blockWhere))
				}
			}
		case *CursorOpen:
			checkRow(term.Row, types.CoroutineScopeType(term.Cursor.Ty) || term.Producer != nil && core.ArrowOpenRow(term.Producer.Type(), 2))
			checkBind(term.Cursor)
			checkExpr(term.Producer, "cursor producer", false)
			if term.Scope == 0 || seenCursorScopes[term.Scope] {
				errs = append(errs, fmt.Errorf("%s: invalid or reused cursor scope", blockWhere))
			}
			seenCursorScopes[term.Scope] = true
			if types.CoroutineScopeType(term.Cursor.Ty) {
				if _, ok := term.Producer.(*core.UnitLit); !ok {
					errs = append(errs, fmt.Errorf("%s: invalid coroutine owner/producer type", blockWhere))
				}
			}
			if term.Producer == nil {
				errs = append(errs, fmt.Errorf("%s: missing coroutine producer", blockWhere))
			} else if err := core.CheckCoroutineProducer(term.Cursor.Ty, term.Producer.Type()); !types.CoroutineScopeType(term.Cursor.Ty) && err != nil {
				errs = append(errs, fmt.Errorf("%s: %v", blockWhere, err))
			}
			if term.Yield.Unique == 0 || term.Yield.Name != types.CoroutineSuspensionName || len(term.Yield.Args) != 0 || term.Yield.Control.Transport != types.Machine || !types.EqualCaptures(term.Yield.Captures, types.ScopeCapture(term.Scope)) {
				errs = append(errs, fmt.Errorf("%s: invalid coroutine owner", blockWhere))
			}
		case *CursorClose:
		case *PopCleanup:
		case *Return:
			checkExpr(term.Value, "return value", true)
			if term.Value != nil && !core.EqualValueRepresentation(term.Value.Type(), w.Result) {
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
	stacks := make([][]types.ScopeID, len(w.Blocks))
	seen := make([]bool, len(w.Blocks))
	seen[w.Entry] = true
	work := []BlockID{w.Entry}
	var errs []error
	for len(work) != 0 {
		id := work[0]
		work = work[1:]
		if !reachable[id] {
			continue
		}
		stack := slices.Clone(stacks[id])
		block := &w.Blocks[id]
		pop := func(want types.ScopeID) {
			if len(stack) == 0 {
				errs = append(errs, fmt.Errorf("machine worker %s block %d: cleanup stack underflow", w.Name, id))
			} else if stack[len(stack)-1] != want {
				errs = append(errs, fmt.Errorf("machine worker %s block %d: cleanup owner mismatch", w.Name, id))
			} else {
				stack = stack[:len(stack)-1]
			}
		}
		switch term := block.Term.(type) {
		case *PushCleanup:
			stack = append(stack, 0)
		case *CursorOpen:
			stack = append(stack, term.Scope)
		case *PopCleanup:
			pop(0)
		case *CursorClose:
			if term.Scope == 0 {
				errs = append(errs, fmt.Errorf("machine worker %s block %d: cursor closure lacks owner", w.Name, id))
			}
			pop(term.Scope)
		case *Return:
			if len(stack) != 0 {
				errs = append(errs, fmt.Errorf("machine worker %s block %d: returns with %d pending cleanup(s)", w.Name, id, len(stack)))
			}
		}
		for _, succ := range successors(block.Term) {
			if !seen[succ] {
				seen[succ] = true
				stacks[succ] = slices.Clone(stack)
				work = append(work, succ)
			} else if !slices.Equal(stacks[succ], stack) {
				errs = append(errs, fmt.Errorf("machine worker %s block %d: cleanup depth or ownership differs between incoming edges", w.Name, succ))
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
		if a[i].Name != b[i].Name || !core.EqualValueRepresentation(a[i].Ty, b[i].Ty) {
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
