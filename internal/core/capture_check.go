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
	a := newCaptureAnalyzer(p, b)
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
	p        *Prog
	b        *types.Builtins
	defs     map[string]*Def
	adts     map[int]*types.ADTInfo
	nextVar  types.CaptureVar
	escapes  map[types.ScopeID]bool
	scoped   map[types.ScopeID]bool
	checking bool
}

func newCaptureAnalyzer(p *Prog, b *types.Builtins) *captureAnalyzer {
	a := &captureAnalyzer{p: p, b: b, defs: map[string]*Def{}, adts: map[int]*types.ADTInfo{}, escapes: map[types.ScopeID]bool{}, scoped: map[types.ScopeID]bool{}}
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
	}
	for i := range p.Defs {
		Rewrite(p.Defs[i].Body, func(t types.Type) types.Type { return t }, func(e Expr) Expr {
			if h, ok := e.(*Handle); ok && h.Scoped {
				a.scoped[h.Scope] = true
			}
			return e
		})
	}
	return a
}

func (a *captureAnalyzer) fresh() types.CaptureVar { a.nextVar++; return a.nextVar }

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
	return errs
}

type ScopeEscapeError struct{ Scope types.ScopeID }

func (e ScopeEscapeError) Error() string {
	return fmt.Sprintf("RESOURCE ESCAPES: the result retains scoped capability %d after its lifetime ends", e.Scope)
}

func (e ScopeEscapeError) Detail() string {
	return fmt.Sprintf("The returned or externally retained value captures local capability %d, whose lifetime ends at this scope.", e.Scope)
}

func (a *captureAnalyzer) definition(d *Def) captureResult {
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
		if a.checking && e.Op != nil && e.Op.RetainsArguments {
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
	case *ResumeTail:
		r := a.expr(e.Value, env, evidence)
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
		innerEvidence := cloneCaptureEvidence(evidence)
		innerEvidence[e.Effect.Unique] = append(innerEvidence[e.Effect.Unique], e.Effect.Captures)
		body := a.expr(e.Body, env, innerEvidence)
		result := body
		if e.Return != nil {
			inner := cloneCaptureEnv(env)
			if e.Return.Param != "_" && e.Return.Param != "()" {
				inner[e.Return.Param] = body.value
			}
			ret := a.expr(e.Return.Body, inner, evidence)
			result.value = ret.value
			result.uses = types.UnionCaptures(body.uses, ret.uses)
		}
		for _, clause := range e.Clauses {
			inner := cloneCaptureEnv(env)
			for _, p := range clause.Params {
				if p != "_" && p != "()" {
					inner[p] = types.VarCapture(a.fresh())
				}
			}
			cl := a.expr(clause.Body, inner, evidence)
			result.uses = types.UnionCaptures(result.uses, cl.uses)
		}
		if a.checking && e.Scoped && result.value.HasScope(e.Scope) {
			a.escapes[e.Scope] = true
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
		if adt == nil {
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
