package core

import (
	"fmt"
	"sort"

	"github.com/waj/fango/internal/types"
)

// InferCaptures computes each definition's symbolic result summary and checks
// scoped Handle results. It runs after elaboration transforms; Lint recomputes
// the same contract from a clean fixed point rather than trusting the summary.
func InferCaptures(p *Prog, b *types.Builtins) []error {
	return InferCapturesIn(p, nil, b)
}

// InferCapturesIn analyzes p with context supplying the definitions p may
// call but does not contain — the modules a REPL session has already
// installed. Context definitions contribute their names, parameters, and
// solved summaries to the call-site rules and are never re-solved, so a
// prompt calling `State.run` or `File.withFile` is checked exactly as the
// same call inside a program would be.
func InferCapturesIn(p *Prog, context []Def, b *types.Builtins) []error {
	a := newCaptureAnalyzer(p, b)
	for i := range context {
		if _, own := a.defs[context[i].Name]; !own {
			a.defs[context[i].Name] = &context[i]
		}
	}
	a.solve()
	return a.checkScopes()
}

func verifyCaptures(p *Prog, b *types.Builtins) []error {
	copyProg := *p
	copyProg.Defs = append([]Def(nil), p.Defs...)
	for i := range copyProg.Defs {
		copyProg.Defs[i].ResultCaptures = types.CaptureSet{}
	}
	a := newCaptureAnalyzer(&copyProg, b)
	a.solve()
	var errs []error
	for i := range p.Defs {
		if !types.EqualCaptures(p.Defs[i].ResultCaptures, copyProg.Defs[i].ResultCaptures) {
			errs = append(errs, fmt.Errorf("def %s: result capture summary is stale", p.Defs[i].Name))
		}
	}
	return append(errs, a.checkScopes()...)
}

type captureResult struct {
	value types.CaptureSet
	uses  types.CaptureSet
}

type captureAnalyzer struct {
	p                 *Prog
	b                 *types.Builtins
	defs              map[string]*Def
	adts              map[int]*types.ADTInfo
	adtsByName        map[string]*types.ADTInfo // canonical name -> ADT, for compiler-known resources
	nextVar           types.CaptureVar
	escapes           map[types.ScopeID]bool
	scoped            map[types.ScopeID]bool
	checking          bool
	current           *Def
	clauseVars        map[*Handle][][]types.CaptureVar
	badStateResult    string
	badResourceResult string
}

func newCaptureAnalyzer(p *Prog, b *types.Builtins) *captureAnalyzer {
	a := &captureAnalyzer{p: p, b: b, defs: map[string]*Def{}, adts: map[int]*types.ADTInfo{}, adtsByName: map[string]*types.ADTInfo{}, escapes: map[types.ScopeID]bool{}, scoped: map[types.ScopeID]bool{}, clauseVars: map[*Handle][][]types.CaptureVar{}}
	for i := range p.Defs {
		d := &p.Defs[i]
		a.defs[d.Name] = d
		for _, v := range d.ParamCaptures {
			if v > a.nextVar {
				a.nextVar = v
			}
		}
		for _, ev := range d.EffectParams {
			for _, v := range ev.Captures.Vars {
				if v > a.nextVar {
					a.nextVar = v
				}
			}
		}
	}
	for _, adt := range p.ADTs {
		a.adts[adt.Con.Unique] = adt
		a.adtsByName[adt.Con.Name] = adt
	}
	for i := range p.Defs {
		Rewrite(p.Defs[i].Body, func(t types.Type) types.Type { return t }, func(e Expr) Expr {
			switch e := e.(type) {
			case *Handle:
				if e.Scoped {
					a.scoped[e.Scope] = true
				}
			case *Bracket:
				a.scoped[e.Scope] = true
			}
			return e
		})
	}
	return a
}

func (a *captureAnalyzer) currentName() string {
	if a.current == nil {
		return "?"
	}
	return a.current.Name
}

func (a *captureAnalyzer) fresh() types.CaptureVar { a.nextVar++; return a.nextVar }

// clauseBinders allocates one placeholder per handler clause parameter, once
// per handler, and reports them as a set to discharge at the boundary.
func (a *captureAnalyzer) clauseBinders(e *Handle) map[types.CaptureVar]bool {
	vars, ok := a.clauseVars[e]
	if !ok {
		vars = make([][]types.CaptureVar, len(e.Clauses))
		for i, clause := range e.Clauses {
			vars[i] = make([]types.CaptureVar, len(clause.Params))
			for j := range clause.Params {
				vars[i][j] = a.fresh()
			}
		}
		a.clauseVars[e] = vars
	}
	binders := map[types.CaptureVar]bool{}
	for _, row := range vars {
		for _, v := range row {
			binders[v] = true
		}
	}
	return binders
}

func (a *captureAnalyzer) solve() {
	// Capture equations contain union only, so the least fixed point is
	// finite: scopes and parameter/evidence variables are the whole universe.
	limit := len(a.p.Defs)*8 + 8
	for range limit {
		changed := false
		for i := range a.p.Defs {
			d := &a.p.Defs[i]
			got := a.definition(d).value
			joined := types.UnionCaptures(d.ResultCaptures, got)
			if !types.EqualCaptures(joined, d.ResultCaptures) {
				d.ResultCaptures = joined
				changed = true
			}
		}
		if !changed {
			return
		}
	}
}

func (a *captureAnalyzer) checkScopes() []error {
	a.escapes = map[types.ScopeID]bool{}
	a.checking = true
	for i := range a.p.Defs {
		a.definition(&a.p.Defs[i])
	}
	a.checking = false
	var errs []error
	scopes := make([]types.ScopeID, 0, len(a.escapes))
	for scope := range a.escapes {
		scopes = append(scopes, scope)
	}
	sort.Slice(scopes, func(i, j int) bool { return scopes[i] < scopes[j] })
	for _, scope := range scopes {
		errs = append(errs, ScopeEscapeError{Scope: scope})
	}
	if a.badStateResult != "" {
		errs = append(errs, StateResultEscapeError{In: a.badStateResult})
	}
	if a.badResourceResult != "" {
		errs = append(errs, ResourceResultEscapeError{In: a.badResourceResult})
	}
	return errs
}

type ScopeEscapeError struct{ Scope types.ScopeID }

// In names the definition whose body holds the offending call.
type StateResultEscapeError struct{ In string }

func (StateResultEscapeError) Error() string {
	return "a parameterized handler result may retain its local state capability"
}

func (StateResultEscapeError) Detail() string {
	return "This call returns a capture-capable value, so the compiler cannot prove that handler-local state stays inside its activation. Return immutable data instead."
}

// ResourceResultEscapeError is the call-site rule for a cleanup scope: the
// scope cannot prove that a capture-capable result does not retain the
// resource it just released. It applies only when the resource type could
// carry a capability at all, so scopes over scalars and `finally` — whose
// resource is Unit — leave their result unrestricted. In names the
// definition whose body holds the offending call.
type ResourceResultEscapeError struct{ In string }

func (ResourceResultEscapeError) Error() string {
	return "a cleanup scope result may retain the resource it released"
}

func (ResourceResultEscapeError) Detail() string {
	return "This scope returns a capture-capable value built from a capture-capable resource, so the compiler cannot prove the resource does not outlive its release. Return immutable data instead."
}

func (e ScopeEscapeError) Error() string {
	return fmt.Sprintf("RESOURCE ESCAPES: the result retains scoped capability %d after its lifetime ends", e.Scope)
}

func (e ScopeEscapeError) Detail() string {
	return fmt.Sprintf("The returned or externally retained value captures local capability %d, whose lifetime ends at this scope.", e.Scope)
}

func (a *captureAnalyzer) definition(d *Def) captureResult {
	oldCurrent := a.current
	a.current = d
	defer func() { a.current = oldCurrent }()
	env := map[string]types.CaptureSet{}
	for i, name := range d.Params {
		if name != "_" && i < len(d.ParamCaptures) {
			env[name] = types.VarCapture(d.ParamCaptures[i])
		}
	}
	evidence := map[int][]types.CaptureSet{}
	for _, ev := range d.EffectParams {
		evidence[ev.Unique] = append(evidence[ev.Unique], ev.Captures)
	}
	return a.expr(d.Body, env, evidence)
}

func cloneCaptureEnv(src map[string]types.CaptureSet) map[string]types.CaptureSet {
	dst := make(map[string]types.CaptureSet, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneCaptureEvidence(src map[int][]types.CaptureSet) map[int][]types.CaptureSet {
	dst := make(map[int][]types.CaptureSet, len(src))
	for k, v := range src {
		dst[k] = append([]types.CaptureSet(nil), v...)
	}
	return dst
}

func captureEvidence(evidence map[int][]types.CaptureSet, unique int) types.CaptureSet {
	stack := evidence[unique]
	if len(stack) == 0 {
		return types.CaptureSet{}
	}
	return stack[len(stack)-1]
}

func (a *captureAnalyzer) expr(e Expr, env map[string]types.CaptureSet, evidence map[int][]types.CaptureSet) captureResult {
	if e == nil {
		return captureResult{}
	}
	children := func(es ...Expr) captureResult {
		var r captureResult
		for _, e := range es {
			c := a.expr(e, env, evidence)
			r.value = types.UnionCaptures(r.value, c.value)
			r.uses = types.UnionCaptures(r.uses, c.uses)
		}
		return r
	}
	switch e := e.(type) {
	case *IntLit, *FloatLit, *StringLit, *CharLit, *UnitLit, *BoolLit, *TypeOf:
		return captureResult{}
	case *VarRef:
		var c types.CaptureSet
		if e.Local {
			c = env[e.Name]
		} else if d := a.defs[e.Name]; d != nil && !d.IsWorker() {
			c = d.ResultCaptures
		}
		return captureResult{value: c, uses: c}
	case *Neg:
		r := a.expr(e.Operand, env, evidence)
		r.value = types.CaptureSet{}
		return r
	case *NativeCall:
		r := children(e.Args...)
		if !a.canCarry(e.Ty, nil) {
			r.value = types.CaptureSet{}
		}
		return r
	case *Quote:
		r := children(e.Holes...)
		if !a.canCarry(e.Ty, nil) {
			r.value = types.CaptureSet{}
		}
		return r
	case *If:
		c := a.expr(e.Cond, env, evidence)
		x := a.expr(e.Then, env, evidence)
		y := a.expr(e.Else, env, evidence)
		return captureResult{value: types.UnionCaptures(x.value, y.value), uses: types.UnionCaptures(c.uses, x.uses, y.uses)}
	case *Seq:
		x := a.expr(e.First, env, evidence)
		y := a.expr(e.Then, env, evidence)
		return captureResult{value: y.value, uses: types.UnionCaptures(x.uses, y.uses)}
	case *Let:
		x := a.expr(e.Rhs, env, evidence)
		inner := cloneCaptureEnv(env)
		if e.Name != "_" {
			inner[e.Name] = x.value
		}
		y := a.expr(e.Body, inner, evidence)
		return captureResult{value: y.value, uses: types.UnionCaptures(x.uses, y.uses)}
	case *Lambda:
		inner := cloneCaptureEnv(env)
		if e.Param != "_" {
			inner[e.Param] = types.VarCapture(e.ParamCapture)
		}
		innerEvidence := cloneCaptureEvidence(evidence)
		bound := map[types.CaptureVar]bool{e.ParamCapture: true}
		for _, ev := range e.EffectParams {
			innerEvidence[ev.Unique] = append(innerEvidence[ev.Unique], ev.Captures)
			for _, v := range ev.Captures.Vars {
				bound[v] = true
			}
		}
		body := a.expr(e.Body, inner, innerEvidence)
		closure := body.uses.Without(nil, bound)
		return captureResult{value: closure, uses: closure}
	case *Perform:
		argResults := make([]captureResult, len(e.Args))
		var r captureResult
		for i, arg := range e.Args {
			argResults[i] = a.expr(arg, env, evidence)
			r.value = types.UnionCaptures(r.value, argResults[i].value)
			r.uses = types.UnionCaptures(r.uses, argResults[i].uses)
		}
		ev := e.Effect.Captures
		if ev.Empty() {
			ev = captureEvidence(evidence, e.Effect.Unique)
		}
		r.uses = types.UnionCaptures(r.uses, ev)
		if e.Op != nil && e.Op.BorrowsEvidence {
			r.value = types.UnionCaptures(r.value, ev)
		}
		retains := e.Op != nil && e.Op.RetainsArguments
		for _, scope := range ev.Scopes {
			retains = retains || a.scoped[scope]
		}
		if a.checking && retains {
			for _, arg := range argResults {
				for _, scope := range arg.value.Scopes {
					if a.scoped[scope] && !ev.HasScope(scope) {
						a.escapes[scope] = true
					}
				}
			}
		}
		if !a.canCarry(e.Ty, nil) {
			r.value = types.CaptureSet{}
		}
		return r
	case *ControlExit:
		r := children(e.Payload...)
		r.value = types.CaptureSet{}
		return r
	case *Suspend:
		// E7's compiler-only fixtures resume with scalar values. E8 attaches
		// ownership before capture-capable suspension results reach source.
		r := a.expr(e.Request, env, evidence)
		r.value = types.CaptureSet{}
		return r
	case *IteratorScope:
		producer := a.expr(e.Producer, env, evidence)
		consumer := a.expr(e.Consumer, env, evidence)
		value := types.UnionCaptures(producer.value, consumer.value)
		if !a.canCarry(e.Ty, nil) {
			value = types.CaptureSet{}
		}
		return captureResult{value: value, uses: types.UnionCaptures(producer.uses, consumer.uses)}
	case *IteratorForEach:
		action := a.expr(e.Action, env, evidence)
		cursor := a.expr(e.Cursor, env, evidence)
		return captureResult{uses: types.UnionCaptures(action.uses, cursor.uses)}
	case *ResumeTail:
		r := a.expr(e.Value, env, evidence)
		if e.NextState != nil {
			next := a.expr(e.NextState, env, evidence)
			r.uses = types.UnionCaptures(r.uses, next.uses)
		}
		if !a.canCarry(e.ClauseResult, nil) {
			r.value = types.CaptureSet{}
		}
		return r
	case *App:
		var parts []captureResult
		if e.CalleeKind == Value {
			parts = append(parts, a.expr(e.Callee, env, evidence))
		}
		for _, arg := range e.Args {
			parts = append(parts, a.expr(arg, env, evidence))
		}
		var uses, fallback types.CaptureSet
		for _, p := range parts {
			uses = types.UnionCaptures(uses, p.uses)
			fallback = types.UnionCaptures(fallback, p.value)
		}
		for _, ev := range e.EvidenceArgs {
			uses = types.UnionCaptures(uses, ev.Captures)
		}
		var value types.CaptureSet
		if e.CalleeKind == Worker {
			matched := false
			if ref, ok := e.Callee.(*VarRef); ok {
				if d := a.defs[ref.Name]; d != nil {
					if a.checking && trustedScopedRunner(d.Name) && a.canCarry(e.Ty, nil) && !trustedForwarder(a.current, d) {
						if resource, scope := a.scopeResourceType(d, e); scope {
							// A cleanup scope over a resource that cannot hold a
							// capability leaves its result unrestricted.
							if a.canCarry(resource, nil) && a.badResourceResult == "" {
								a.badResourceResult = a.currentName()
							}
						} else {
							if a.badStateResult == "" {
								a.badStateResult = a.currentName()
							}
						}
					}
					matched = true
					m := map[types.CaptureVar]types.CaptureSet{}
					for i, v := range d.ParamCaptures {
						if i < len(parts) {
							m[v] = parts[i].value
						}
					}
					for i, formal := range d.EffectParams {
						if i < len(e.EvidenceArgs) {
							for _, v := range formal.Captures.Vars {
								m[v] = e.EvidenceArgs[i].Captures
							}
						}
					}
					value = d.ResultCaptures.Substitute(m)
				}
			}
			if !matched {
				value = fallback
				for _, ev := range e.EvidenceArgs {
					value = types.UnionCaptures(value, ev.Captures)
				}
			}
		} else if e.CalleeKind == Ctor {
			value = fallback
		} else {
			value = fallback
			for _, ev := range e.EvidenceArgs {
				value = types.UnionCaptures(value, ev.Captures)
			}
		}
		if !a.canCarry(e.Ty, nil) {
			value = types.CaptureSet{}
		}
		return captureResult{value: value, uses: uses}
	case *Handle:
		var initial captureResult
		if e.State != nil {
			initial = a.expr(e.State.Initial, env, evidence)
		}
		innerEvidence := cloneCaptureEvidence(evidence)
		innerEvidence[e.Effect.Unique] = append(innerEvidence[e.Effect.Unique], e.Effect.Captures)
		body := a.expr(e.Body, env, innerEvidence)
		result := body
		result.uses = types.UnionCaptures(initial.uses, result.uses)
		if e.Return != nil {
			inner := cloneCaptureEnv(env)
			if e.State != nil {
				inner[e.State.Name] = initial.value
			}
			if e.Return.Param != "_" && e.Return.Param != "()" {
				inner[e.Return.Param] = body.value
			}
			ret := a.expr(e.Return.Body, inner, evidence)
			result.value = ret.value
			result.uses = types.UnionCaptures(body.uses, ret.uses)
		}
		// An operation payload arrives from the perform site, which this
		// analysis does not see, so each clause parameter binds an opaque
		// placeholder. The placeholders are allocated once per handler, not once
		// per fixed-point pass, and are discharged at the handler boundary along
		// with its scope: they belong to the handler, and letting one escape
		// into a caller's result summary would name a variable nothing binds —
		// and would never converge.
		binders := a.clauseBinders(e)
		for i, clause := range e.Clauses {
			inner := cloneCaptureEnv(env)
			if e.State != nil {
				inner[e.State.Name] = initial.value
			}
			for j, p := range clause.Params {
				if p != "_" && p != "()" {
					inner[p] = types.VarCapture(a.clauseVars[e][i][j])
				}
			}
			cl := a.expr(clause.Body, inner, evidence)
			result.uses = types.UnionCaptures(result.uses, cl.uses)
		}
		result.value = result.value.Without(nil, binders)
		result.uses = result.uses.Without(nil, binders)
		if e.Scoped && result.value.HasScope(e.Scope) {
			if e.State != nil && trustedScopedRunnerName(a.current) {
				result.value = result.value.Without(map[types.ScopeID]bool{e.Scope: true}, nil)
			} else if a.checking {
				a.escapes[e.Scope] = true
			}
		}
		result.uses = result.uses.Without(map[types.ScopeID]bool{e.Scope: true}, nil)
		return result
	case *Bracket:
		acquired := a.expr(e.Acquire, env, evidence)
		inner := cloneCaptureEnv(env)
		inner[e.Resource] = types.UnionCaptures(acquired.value, types.ScopeCapture(e.Scope))
		body := a.expr(e.Body, inner, evidence)
		release := a.expr(e.Release, inner, evidence)
		result := captureResult{value: body.value, uses: types.UnionCaptures(acquired.uses, body.uses, release.uses)}
		if result.value.HasScope(e.Scope) {
			if trustedScopedRunnerName(a.current) {
				// The intrinsic's own definition applies an abstract callback to
				// the resource, so the conservative indirect-call rule always
				// retains the scope here. Its callers are restricted instead.
				result.value = result.value.Without(map[types.ScopeID]bool{e.Scope: true}, nil)
			} else if a.checking {
				a.escapes[e.Scope] = true
			}
		}
		result.uses = result.uses.Without(map[types.ScopeID]bool{e.Scope: true}, nil)
		return result
	case *Case:
		scrut := a.expr(e.Scrut, env, evidence)
		inner := cloneCaptureEnv(env)
		inner[e.Bind] = scrut.value
		tree := a.tree(e.Tree, inner, evidence, scrut.value)
		return captureResult{value: tree.value, uses: types.UnionCaptures(scrut.uses, tree.uses)}
	default:
		return captureResult{}
	}
}

// scopeResourceType returns the instantiated resource type of a cleanup-scope
// call, and whether the callee is that intrinsic or a bundled resource runner
// at all. A resource runner's resource is fixed by name, so no instantiation
// is needed for it.
func (a *captureAnalyzer) scopeResourceType(d *Def, e *App) (types.Type, bool) {
	if resource, ok := types.ResourceRunner(d.Name); ok {
		if adt := a.adtsByName[resource]; adt != nil {
			return adt.Con, true
		}
		return nil, false
	}
	if d.Name != types.ScopeBracketName || len(d.Params) != 3 {
		return nil, false
	}
	args, _ := PeelFun(d.Type, 3)
	acquire, ok := args[0].(*types.TFun)
	if !ok {
		return nil, false
	}
	m := make(map[int]types.Type, len(d.TyParams))
	for i, tv := range d.TyParams {
		if i < len(e.TyArgs) {
			m[tv.ID] = e.TyArgs[i]
		}
	}
	return types.SubstRigid(acquire.Ret, m), true
}

func trustedScopedRunnerName(d *Def) bool {
	return d != nil && trustedScopedRunner(d.Name)
}

func trustedScopedRunner(name string) bool {
	switch name {
	case "State.run", "Writer.run", "Random.runSeeded", "Random.runSystem", types.ScopeBracketName:
		return true
	}
	_, resource := types.ResourceRunner(name)
	return resource
}

// trustedForwarder exempts a bundled runner's own call to the runner it
// wraps: Random.runSystem forwards to runSeeded, and a File resource runner
// forwards to the cleanup-scope intrinsic or to another File runner. The
// wrapper's polymorphic result is what its callers are checked against, so
// checking the forwarding call itself would reject the wrapper's definition.
func trustedForwarder(current, callee *Def) bool {
	if current == nil || callee == nil {
		return false
	}
	if current.Name == "Random.runSystem" && callee.Name == "Random.runSeeded" {
		return true
	}
	if _, resource := types.ResourceRunner(current.Name); resource {
		_, calleeResource := types.ResourceRunner(callee.Name)
		return callee.Name == types.ScopeBracketName || calleeResource
	}
	return false
}

func (a *captureAnalyzer) tree(t Tree, env map[string]types.CaptureSet, evidence map[int][]types.CaptureSet, scrut types.CaptureSet) captureResult {
	switch t := t.(type) {
	case *Unreachable:
		return captureResult{}
	case *Leaf:
		return a.expr(t.Body, env, evidence)
	case *Guard:
		c := a.expr(t.Cond, env, evidence)
		x := a.tree(t.Then, env, evidence, scrut)
		y := a.tree(t.Else, env, evidence, scrut)
		return captureResult{value: types.UnionCaptures(x.value, y.value), uses: types.UnionCaptures(c.uses, x.uses, y.uses)}
	case *SwitchCtor:
		var out captureResult
		for _, c := range t.Cases {
			inner := cloneCaptureEnv(env)
			for _, n := range c.Binds {
				if n != "" {
					inner[n] = scrut
				}
			}
			r := a.tree(c.Tree, inner, evidence, scrut)
			out.value = types.UnionCaptures(out.value, r.value)
			out.uses = types.UnionCaptures(out.uses, r.uses)
		}
		if t.Default != nil {
			r := a.tree(t.Default, env, evidence, scrut)
			out.value = types.UnionCaptures(out.value, r.value)
			out.uses = types.UnionCaptures(out.uses, r.uses)
		}
		return out
	case *SwitchLit:
		var out captureResult
		for _, c := range t.Cases {
			r := a.tree(c.Tree, env, evidence, scrut)
			out.value = types.UnionCaptures(out.value, r.value)
			out.uses = types.UnionCaptures(out.uses, r.uses)
		}
		if t.Default != nil {
			r := a.tree(t.Default, env, evidence, scrut)
			out.value = types.UnionCaptures(out.value, r.value)
			out.uses = types.UnionCaptures(out.uses, r.uses)
		}
		return out
	default:
		return captureResult{}
	}
}

func (a *captureAnalyzer) canCarry(t types.Type, seen map[int]bool) bool {
	switch t := t.(type) {
	case *types.TVar, *types.TFun:
		return true
	case *types.TCon:
		if a.b != nil {
			switch t.Unique {
			case a.b.Int.Unique, a.b.Float.Unique, a.b.String.Unique, a.b.Char.Unique, a.b.Bool.Unique, a.b.Unit.Unique:
				return false
			}
		}
		adt := a.adts[t.Unique]
		if adt == nil || types.ResourceType(adt.Con.Name) {
			// A compiler-known resource is a capability regardless of its
			// shape: the bundled handle is an Int behind a private
			// constructor, and that Int must not outlive its scope.
			return true
		}
		if seen == nil {
			seen = map[int]bool{}
		}
		if seen[t.Unique] {
			for _, arg := range t.Args {
				if a.canCarry(arg, seen) {
					return true
				}
			}
			return false
		}
		next := make(map[int]bool, len(seen)+1)
		for k, v := range seen {
			next[k] = v
		}
		next[t.Unique] = true
		for _, ctor := range adt.Ctors {
			for _, field := range adt.InstFields(ctor, t.Args) {
				if a.canCarry(field, next) {
					return true
				}
			}
		}
		return false
	default:
		return false
	}
}
