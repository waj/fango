package elaborate

import (
	"maps"
	"slices"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/types"
)

// This sentinel exists only between elaboration and lexical row binding.
const pendingRow types.CaptureVar = -1

func explicitApplication(want, actual types.EffLabel) bool {
	if want.Unique != actual.Unique || len(want.Args) != len(actual.Args) {
		return false
	}
	for i := range want.Args {
		if !typeMayInstantiate(want.Args[i], actual.Args[i]) {
			return false
		}
	}
	return true
}

func typeMayInstantiate(pattern, actual types.Type) bool {
	if types.Equal(pattern, actual) {
		return true
	}
	switch pattern := pattern.(type) {
	case *types.TVar:
		return true
	case *types.TCon:
		other, ok := actual.(*types.TCon)
		if !ok || pattern.Unique != other.Unique || len(pattern.Args) != len(other.Args) {
			return false
		}
		for i := range pattern.Args {
			if !typeMayInstantiate(pattern.Args[i], other.Args[i]) {
				return false
			}
		}
		return true
	case *types.TFun:
		other, ok := actual.(*types.TFun)
		return ok && typeMayInstantiate(pattern.Arg, other.Arg) && typeMayInstantiate(pattern.Eff, other.Eff) && typeMayInstantiate(pattern.Ret, other.Ret)
	case types.Row:
		other, ok := actual.(types.Row)
		if !ok || len(pattern.Labels) > len(other.Labels) || pattern.Tail == nil && len(pattern.Labels) != len(other.Labels) {
			return false
		}
		used := make([]bool, len(other.Labels))
		for _, label := range pattern.Labels {
			found := false
			for i, candidate := range other.Labels {
				if used[i] || !explicitApplication(label, candidate) {
					continue
				}
				used[i], found = true, true
				break
			}
			if !found {
				return false
			}
		}
		return pattern.Tail != nil || other.Tail == nil
	}
	return false
}

func (el *elab) residualArgument(actual, explicit types.Row) *core.RowArgument {
	row := &core.RowArgument{}
	if actual.Tail != nil {
		row.From = pendingRow
	}
	for _, label := range types.SortedRow(actual).Labels {
		if label.Scoped {
			// The runner can interpret effects hidden behind its quantified
			// callback row. Forward that row even when the call site's only
			// visible label is an erased fresh permission.
			row.From = pendingRow
		}
		if !types.RuntimeEvidenceEffect(label) {
			continue
		}
		if slices.ContainsFunc(explicit.Labels, func(want types.EffLabel) bool { return explicitApplication(want, label) }) {
			continue
		}
		args := make([]types.Type, len(label.Args))
		for i, arg := range label.Args {
			args[i] = el.eraseRuntimeKinds(eraseRows(arg))
		}
		row.Effects = append(row.Effects, core.EffectInstance{Unique: label.Unique, Name: label.Name, Args: args, Captures: el.evidenceCaptures(label.Unique, label.Args), Control: el.evidenceControl(label.Unique, label.Args)})
	}
	return row
}

func (el *elab) valueAppWithRow(callee, arg core.Expr, raw types.Type) core.Expr {
	app := el.valueApp(callee, arg).(*core.App)
	app.SourceType = raw
	if app.Row != nil {
		app.Row = el.residualArgument(raw.(*types.TFun).Eff, callee.Type().(*types.TFun).Eff)
	}
	return app
}

// bindRows runs after ANF/specialization, before capture inference. Source call
// sites already retain their instantiated residual labels; this pass assigns
// lexical binders and fills only erased, abstract forwarding positions.
func bindRows(defs []core.Def, ck *infer.Checker) {
	for i := range defs {
		d := &defs[i]
		if core.ArrowOpenRow(d.Type, len(d.Params)) && d.RowParam == 0 {
			d.RowParam = ck.Sup.FreshCapture()
		}
		evidence := map[types.EffectKey]core.EffectInstance{}
		for _, ev := range append(append([]core.EffectInstance(nil), d.EffectParams...), d.RowEffects...) {
			evidence[ev.Key()] = ev
		}
		bindExpressionRows(d.Body, d.RowParam, evidence, ck)
	}
}

func bindExpressionRows(expr core.Expr, current types.CaptureVar, evidence map[types.EffectKey]core.EffectInstance, ck *infer.Checker) {
	resolve := func(ev *core.EffectInstance) {
		if actual, found := evidence[ev.Key()]; found && ev.Captures.Empty() {
			ev.Captures, ev.Control = actual.Captures, actual.Control
		}
	}
	bind := func(row **core.RowArgument, needed bool) {
		if !needed {
			return
		}
		if *row == nil {
			*row = &core.RowArgument{From: current}
		}
		argument := *row
		if argument.From == pendingRow {
			argument.From = current
		}
		var kept []core.EffectInstance
		for _, ev := range argument.Effects {
			if _, found := evidence[ev.Key()]; !found && current != 0 {
				// An abstract callback receives this interpretation through its
				// invocation row rather than capturing it during construction.
				argument.From = current
				continue
			}
			resolve(&ev)
			kept = append(kept, ev)
		}
		argument.Effects = kept
	}
	core.InspectPruned(expr, func(e core.Expr) bool {
		switch e := e.(type) {
		case *core.Lambda:
			row := current
			if core.ArrowOpenRow(e.Ty, 1) {
				if e.RowParam == 0 {
					e.RowParam = ck.Sup.FreshCapture()
				}
				row = e.RowParam
			}
			inner := maps.Clone(evidence)
			for _, ev := range append(append([]core.EffectInstance(nil), e.EffectParams...), e.RowEffects...) {
				inner[ev.Key()] = ev
			}
			bindExpressionRows(e.Body, row, inner, ck)
			return false
		case *core.Handle:
			inner := maps.Clone(evidence)
			inner[e.Effect.Key()] = e.Effect
			bindExpressionRows(e.Body, current, inner, ck)
			for _, clause := range e.Clauses {
				bindExpressionRows(clause.Body, current, evidence, ck)
			}
			if e.Return != nil {
				bindExpressionRows(e.Return.Body, current, evidence, ck)
			}
			if e.State != nil {
				bindExpressionRows(e.State.Initial, current, evidence, ck)
			}
			return false
		case *core.App:
			arity := 1
			if e.CalleeKind == core.Worker {
				arity = len(e.Args)
			}
			bind(&e.Row, e.CalleeKind != core.Ctor && core.ArrowOpenRow(e.Callee.Type(), arity))
			for i := range e.EvidenceArgs {
				resolve(&e.EvidenceArgs[i])
			}

		case *core.Perform:
			resolve(&e.Effect)
		case *core.ControlExit:
			resolve(&e.Effect)
		}
		return true
	})
}

// callbackResidual avoids treating a surrounding row's upper bound as the
// requirements of a closed callback. A shared quantified tail is the union of
// the concrete callback rows supplying it, less each callback's explicit row.
// Open or structurally indirect sources retain ordinary abstract forwarding.
func (el *elab) callbackResidual(name string, arity int, args []ast.Expr, tyArgs []types.Type, fallback *core.RowArgument) *core.RowArgument {
	if fallback == nil || len(args) < arity {
		return fallback
	}
	scheme, ok := el.ck.Env.Lookup(name)
	if !ok {
		return fallback
	}
	origin := instantiateRuntimeParams(el.ck.Sub.Apply(scheme.Body), tyArgs)
	params, _ := core.PeelFun(origin, arity)
	final := arrowAt(origin, arity-1).(*types.TFun)
	tail, ok := final.Eff.Tail.(*types.TVar)
	if !ok {
		return fallback
	}
	found := false
	needed := types.Row{}
	containsTail := func(ty types.Type) bool {
		for _, v := range types.RigidVarsIn(ty) {
			if v.ID == tail.ID {
				return true
			}
		}
		return false
	}
	for i, param := range params {
		fn, ok := param.(*types.TFun)
		if !ok {
			if containsTail(param) {
				return fallback
			}
			continue
		}
		if containsTail(fn.Arg) || containsTail(fn.Ret) {
			return fallback
		}
		variable, ok := fn.Eff.Tail.(*types.TVar)
		if !ok || variable.ID != tail.ID {
			continue
		}
		actual, ok := el.callbackRequirements(args[i])
		if !ok || actual.Tail != nil {
			return fallback
		}
		found = true
		for _, label := range actual.Labels {
			if slices.ContainsFunc(fn.Eff.Labels, func(explicit types.EffLabel) bool {
				return explicitApplication(explicit, label)
			}) {
				continue
			}
			if !slices.ContainsFunc(needed.Labels, func(existing types.EffLabel) bool {
				return types.EffectLabelKey(existing) == types.EffectLabelKey(label)
			}) {
				needed.Labels = append(needed.Labels, label)
			}
		}
	}
	if !found {
		return fallback
	}
	return el.residualArgument(needed, final.Eff)
}

// Lambda annotations can be widened by contextual row inclusion. Retain the
// rows its body actually performs so unused ambient effects are not inherited.
func (el *elab) callbackRequirements(expr ast.Expr) (types.Row, bool) {
	if lambda, ok := expr.(*ast.Lambda); ok && len(lambda.Params) == 1 {
		if performed, found := el.ck.LambdaEffects[lambda]; found {
			row := types.Row{}
			for _, effect := range performed {
				actual, ok := el.apply(effect).(types.Row)
				if !ok {
					return types.Row{}, false
				}
				if actual.Tail != nil {
					return actual, true
				}
				for _, label := range actual.Labels {
					if !slices.ContainsFunc(row.Labels, func(existing types.EffLabel) bool {
						return types.EffectLabelKey(existing) == types.EffectLabelKey(label)
					}) {
						row.Labels = append(row.Labels, label)
					}
				}
			}
			return row, true
		}
	}
	fn, ok := el.apply(el.ck.ExprTypes[expr]).(*types.TFun)
	if !ok {
		return types.Row{}, false
	}
	return fn.Eff, true
}
