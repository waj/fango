package core

import (
	"encoding/json"
	"fmt"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
	"maps"
)

// WorkNeed is one latent registration constraint. Budget and Need still carry
// source row variables (including inference metavariables in collector mode).
// The selected owner is resolved by the same higher-order, recursive flow
// interpreter used for independent lifetime and exclusive-access validation.
type WorkNeed struct {
	Budget, Need types.Type
	// Immediate records row provenance before a caller supplies the facet's
	// owner. It is not a latent budget inclusion.
	Immediate bool
	Span      source.Span
	Owner, In string
}

// CollectWorkNeeds interprets source capture contracts without closing or
// checking their rows. Inference solves these inclusions before generalization;
// the ordinary Core flow pass reconstructs and checks them after elaboration.
// Unknown parameter owners remain latent in the callable's contract until a
// call substitutes an actual Work.run owner. Unknown rows are returned intact.
func CollectWorkNeeds(p *Prog, context []Def, b *types.Builtins) []WorkNeed {
	a := newCaptureAnalyzer(p, b)
	for i := range context {
		if a.defs[context[i].Name] == nil {
			a.defs[context[i].Name] = &context[i]
		}
	}
	var out []WorkNeed
	seen := map[string]bool{}
	for _, d := range p.Defs {
		f := &flowChecker{shape: a, defs: a.defs, objects: []*flowObject{nil}, objectIDs: map[string]int{}, owners: []*flowOwner{nil}, ownerIDs: map[string]int{}, contexts: map[string]*flowContext{}, errors: map[string]error{}, root: d.Name, active: map[int]int{}}
		f.collectWorkNeed = func(n WorkNeed) {
			data, _ := json.Marshal([]types.Type{n.Budget, n.Need})
			key := n.In + "/" + n.Owner + "/" + string(data)
			if !seen[key] {
				seen[key] = true
				out = append(out, n)
			}
		}
		args := make([]flowValue, len(d.Params))
		for i := range args {
			args[i].unknown = true
		}
		env := emptyFlowEnv()
		env.rows[0] = flowRow{unknown: true}
		for {
			f.generation++
			f.changed = false
			f.callDef(d.Name, args, nil, env, rootFlowSite, nil)
			if !f.changed {
				break
			}
		}
	}
	return out
}

// Source row substitutions are independent of runtime generic arguments: row
// parameters erase from the latter and IO has no evidence entry at all.
func matchSourceRows(pattern, actual types.Type, into map[int]types.Type) {
	if pattern == nil || actual == nil {
		return
	}
	switch p := pattern.(type) {
	case *types.TVar:
		if p.Rigid {
			if row, ok := actual.(types.Row); ok {
				actual, _ = workRow(row)
			}
			// The argument's nominal index identifies the stored computation.
			// Directional arrow adaptation may widen a later execution view;
			// it must not overwrite that index with the surrounding driver's row.
			if _, bound := into[p.ID]; !bound {
				into[p.ID] = actual
			}
		}
	case *types.TCon:
		if a, ok := actual.(*types.TCon); ok && a.Unique == p.Unique && len(a.Args) == len(p.Args) {
			for i, t := range p.Args {
				matchSourceRows(t, a.Args[i], into)
			}
		}
	case *types.TFun:
		if a, ok := actual.(*types.TFun); ok {
			// A stored value's nominal row index takes precedence over a
			// widened execution view. Match outward rows along the curried
			// spine before callback arguments: subtracting a callback's
			// explicit labels first would lose foreign effects in that row.
			matchSourceNominalArgs(p, a, into)
			matchSourceRows(p.Eff, a.Eff, into)
			matchSourceRows(p.Ret, a.Ret, into)
			matchSourceRows(p.Arg, a.Arg, into)
		}
	case types.Row:
		p, _ = workRow(p)
		a, ok := actual.(types.Row)
		if !ok {
			if p.Tail != nil && len(p.Labels) == 0 {
				matchSourceRows(p.Tail, actual, into)
			}
			return
		}
		a, _ = workRow(a)
		remaining := types.Row{Tail: a.Tail}
		for _, label := range a.Labels {
			found := false
			for _, want := range p.Labels {
				if label.Unique == want.Unique {
					found = true
					for i, t := range want.Args {
						if i < len(label.Args) {
							matchSourceRows(t, label.Args[i], into)
						}
					}
				}
			}
			if !found {
				remaining.Labels = append(remaining.Labels, label)
			}
		}
		if p.Tail != nil {
			matchSourceRows(p.Tail, remaining, into)
		}
	}
}

// Stored row indices also occur in driver callback parameters and later
// curried arguments. Find them before any widened execution view binds a row.
func matchSourceNominalArgs(pattern, actual *types.TFun, into map[int]types.Type) {
	if _, nominal := pattern.Arg.(*types.TCon); nominal {
		matchSourceRows(pattern.Arg, actual.Arg, into)
	} else if p, ok := pattern.Arg.(*types.TFun); ok {
		if a, ok := actual.Arg.(*types.TFun); ok {
			matchSourceNominalArgs(p, a, into)
		}
	}
	if p, ok := pattern.Ret.(*types.TFun); ok {
		if a, ok := actual.Ret.(*types.TFun); ok {
			matchSourceNominalArgs(p, a, into)
		}
	}
}

func workRow(t types.Type) (types.Row, bool) {
	switch t := t.(type) {
	case types.Row:
		for t.Tail != nil {
			tail, ok := t.Tail.(types.Row)
			if !ok {
				break
			}
			t.Labels = append(append([]types.EffLabel(nil), t.Labels...), tail.Labels...)
			t.Tail = tail.Tail
		}
		return t, true
	case *types.TVar:
		if t.Kind == types.RowVar {
			return types.Row{Tail: t}, true
		}
	}
	return types.Row{}, false
}

func workRowIncludes(budget, need types.Type) bool {
	b, bok := workRow(budget)
	n, nok := workRow(need)
	if !bok || !nok {
		return false
	}
	if n.Tail != nil && (b.Tail == nil || !types.Equal(b.Tail, n.Tail)) {
		return false
	}
	for _, label := range n.Labels {
		found := false
		for _, allowed := range b.Labels {
			if allowed.Unique == label.Unique && types.Equal(types.Row{Labels: []types.EffLabel{allowed}}, types.Row{Labels: []types.EffLabel{label}}) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (f *flowChecker) checkWorkBudget(facet flowValue, need types.Type) {
	for _, ref := range facet.refs {
		o := f.objects[ref]
		if o.kind != "work-owner" && o.kind != "coroutine-registry" {
			continue
		}
		if f.collectWorkNeed != nil {
			f.collectWorkNeed(WorkNeed{Budget: o.budget, Need: need, Span: f.location, Owner: o.allocation, In: f.root})
			continue
		}
		if !workRowIncludes(o.budget, need) {
			show := func(t types.Type) string {
				if t == nil {
					return "<missing>"
				}
				return types.Show(t)
			}
			err := fmt.Errorf("WORK EFFECT BUDGET: registered effects %s exceed destination Work.run owner's budget %s in %s", show(need), show(o.budget), f.root)
			f.errors[err.Error()] = err
		}
	}
}

func (f *flowChecker) workOwnerIDs(value flowValue) []int {
	var owners []int
	for _, ref := range value.refs {
		if f.objects[ref].kind == "work-owner" || f.objects[ref].kind == "coroutine-registry" {
			owners = append(owners, ref)
		}
	}
	return owners
}

func (f *flowChecker) checkWorkTransfer(cursor flowValue) {
	for _, ref := range cursor.refs {
		o := f.objects[ref]
		if o.kind != "coroutine" || len(o.fields) == 0 {
			continue
		}
		for _, owner := range append(f.captures(o.fields[0]), f.retainedExecutions(o.fields[0])...) {
			captured := f.owners[owner]
			if captured.code.Kind == "coroutine" || captured.code.Kind == "handle" && captured.code.Name != "" {
				err := fmt.Errorf("WORK CAPABILITY TRANSFER: packaged producer retains execution authority or mutable handler evidence from %s", captured.name)
				f.errors[err.Error()] = err
			}
		}
	}
}

// Lifetime and execution identity coincide for lexical cursors, but dynamic
// handles live as long as their registry. Work transfer must still inspect the
// captured execution identities, including handles nested inside closures/ADTs.
func (f *flowChecker) retainedExecutions(value flowValue) []int {
	seen := map[int]bool{}
	var owners []int
	var visit func(flowValue)
	visit = func(value flowValue) {
		for _, ref := range value.refs {
			if seen[ref] {
				continue
			}
			seen[ref] = true
			o := f.objects[ref]
			if o.kind == "coroutine" || o.kind == "pause" {
				owners = append(owners, o.owner)
			}
			for _, field := range o.fields {
				visit(field)
			}
			if o.kind == "lambda" {
				for name := range f.shape.free(o.code.Children[0], []string{o.code.Name}) {
					visit(o.env.values[name])
				}
			}
		}
	}
	visit(value)
	return owners
}

// Callback adaptation may widen an execution row with the surrounding driver's
// control. A closed producer contract identifies the actual stored residual;
// use it rather than charging that widened invocation view to the registry.
// Open or abstract producers retain the declared proof conservatively.
func (f *flowChecker) registeredBudget(producer flowValue, proof types.Type) types.Type {
	if producer.unknown || len(producer.refs) == 0 {
		return proof
	}
	var actual types.Row
	for _, ref := range producer.refs {
		o := f.objects[ref]
		var source types.Type
		if o.kind == "lambda" {
			source = types.SubstRigid(o.code.SourceType, o.env.types)
		}
		if o.kind == "global" {
			if d := f.defs[o.def]; d != nil {
				sub := maps.Clone(o.env.types)
				for i, tv := range d.TyParams {
					if i < len(o.code.TypeArgs) {
						sub[tv.ID] = types.SubstRigid(o.code.TypeArgs[i], o.env.types)
					}
				}
				source = types.SubstRigid(d.SourceType, sub)
			}
		}
		factory, ok := source.(*types.TFun)
		if !ok {
			return proof
		}
		body, ok := factory.Ret.(*types.TFun)
		if !ok {
			return proof
		}
		row, ok := workRow(body.Eff)
		if !ok || row.Tail != nil {
			return proof
		}
		for _, label := range row.Labels {
			if label.Name == types.CoroutineSuspensionName {
				continue
			}
			found := false
			for _, old := range actual.Labels {
				found = found || types.Equal(types.Row{Labels: []types.EffLabel{old}}, types.Row{Labels: []types.EffLabel{label}})
			}
			if !found {
				actual.Labels = append(actual.Labels, label)
			}
		}
	}
	if f.collectWorkNeed == nil && f.collectControlNeed == nil && !workRowIncludes(proof, actual) {
		err := fmt.Errorf("WORK EFFECT BUDGET: registration proof does not cover the actual producer residual in %s", f.root)
		f.errors[err.Error()] = err
	}
	return actual
}
