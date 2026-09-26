package core

import (
	"fmt"

	"github.com/waj/fango/internal/types"
)

// InferCaptures computes symbolic result summaries and higher-order capture
// metadata for lexical evidence. Resources are checked at runtime. Lint independently
// reconstructs both forms after elaboration transforms.
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
	return nil
}

func verifyCaptures(p *Prog, b *types.Builtins) []error {
	return verifyCapturesIn(p, nil, b)
}

func verifyCapturesIn(p *Prog, context []Def, b *types.Builtins) []error {
	copyProg := *p
	copyProg.Defs = append([]Def(nil), p.Defs...)
	for i := range copyProg.Defs {
		copyProg.Defs[i].ResultCaptures = types.CaptureSet{}
	}
	a := newCaptureAnalyzer(&copyProg, b)
	for i := range context {
		if _, own := a.defs[context[i].Name]; !own {
			a.defs[context[i].Name] = &context[i]
		}
	}
	a.solve()
	var errs []error
	for i := range p.Defs {
		if !types.EqualCaptures(p.Defs[i].ResultCaptures, copyProg.Defs[i].ResultCaptures) {
			errs = append(errs, fmt.Errorf("def %s: result capture summary is stale", p.Defs[i].Name))
		}
	}

	return errs
}

type captureResult struct {
	value types.CaptureSet
	uses  types.CaptureSet
}

type captureAnalyzer struct {
	p          *Prog
	b          *types.Builtins
	defs       map[string]*Def
	adts       map[int]*types.ADTInfo
	nextVar    types.CaptureVar
	clauseVars map[*Handle][][]types.CaptureVar
}

func newCaptureAnalyzer(p *Prog, b *types.Builtins) *captureAnalyzer {
	a := &captureAnalyzer{p: p, b: b, defs: map[string]*Def{}, adts: map[int]*types.ADTInfo{}, clauseVars: map[*Handle][][]types.CaptureVar{}}
	for i := range p.Defs {
		d := &p.Defs[i]
		a.defs[d.Name] = d
		Inspect(d.Body, func(e Expr) {
			if lam, ok := e.(*Lambda); ok {
				if lam.RowParam > a.nextVar {
					a.nextVar = lam.RowParam
				}
			}
		})
		if d.RowParam > a.nextVar {
			a.nextVar = d.RowParam
		}
		for _, v := range d.ParamCaptures {
			if v > a.nextVar {
				a.nextVar = v
			}
		}
		for _, ev := range append(append([]EffectInstance(nil), d.EffectParams...), d.RowEffects...) {
			for _, v := range ev.Captures.Vars {
				if v > a.nextVar {
					a.nextVar = v
				}
			}
		}
	}
	for _, adt := range p.ADTs {
		a.adts[adt.Con.Unique] = adt
	}
	return a
}

func (a *captureAnalyzer) fresh() types.CaptureVar { a.nextVar++; return a.nextVar }

// clauseBinders allocates one placeholder per handler clause parameter, once
// per handler, and reports them as a set to discharge at the boundary.
func (a *captureAnalyzer) clauseBinders(e *Handle) map[types.CaptureVar]bool {
	vars, ok := a.clauseVars[e]
	if !ok {
		vars = make([][]types.CaptureVar, len(e.Clauses))
		for i, clause := range e.Clauses {
			count := len(clause.Params)
			if clause.SuppressedParam != "" {
				count++
			}
			vars[i] = make([]types.CaptureVar, count)
			for j := range vars[i] {
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
	for {
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

func (a *captureAnalyzer) definition(d *Def) captureResult {
	env := map[string]types.CaptureSet{}
	for i, name := range d.Params {
		if name != "_" && i < len(d.ParamCaptures) {
			env[name] = types.VarCapture(d.ParamCaptures[i])
		}
	}
	evidence := map[int][]types.CaptureSet{}
	for _, ev := range append(append([]EffectInstance(nil), d.EffectParams...), d.RowEffects...) {
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
	case *TaskSpawn:
		return children(e.Scope, e.Input)
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
		bound := map[types.CaptureVar]bool{e.ParamCapture: true, e.RowParam: true}
		for _, ev := range append(append([]EffectInstance(nil), e.EffectParams...), e.RowEffects...) {
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
		if !a.canCarry(e.Ty, nil) {
			r.value = types.CaptureSet{}
		}
		return r
	case *ControlExit:
		r := children(e.Payload...)
		r.value = types.CaptureSet{}
		return r

	case *FailureInspect:
		r := children(e.Args...)
		if !a.canCarry(e.Ty, nil) {
			r.value = types.CaptureSet{}
		}
		return r

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
		uses = RowCaptures(e.Row)
		fallback = uses
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
					matched = true
					m := map[types.CaptureVar]types.CaptureSet{}
					if d.RowParam != 0 {
						m[d.RowParam] = RowCaptures(e.Row)
					}
					for _, ev := range d.RowEffects {
						for _, v := range ev.Captures.Vars {
							m[v] = RowCaptures(e.Row)
						}
					}
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
			if clause.SuppressedParam != "" {
				inner[clause.SuppressedParam] = types.VarCapture(a.clauseVars[e][i][len(clause.Params)])
			}
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
		if e.Scoped {
			result.value = result.value.Without(map[types.ScopeID]bool{e.Scope: true}, nil)
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
		result.value = result.value.Without(map[types.ScopeID]bool{e.Scope: true}, nil)
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
		if t.Name == types.FailureTypeName {
			return true
		}
		if a.b != nil {
			switch t.Unique {
			case a.b.Int.Unique, a.b.Float.Unique, a.b.String.Unique, a.b.Char.Unique, a.b.Bool.Unique, a.b.Unit.Unique:
				return false
			}
		}
		adt := a.adts[t.Unique]
		if adt == nil || adt.Resource {
			// A marked resource is a capability regardless of its private
			// representation. Unknown types are conservatively capable too.
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
